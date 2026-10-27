package api

// Aeronautik (R845): Flugverkehr in Echtzeit aus Transpondertelegrammen.
//
// ── Woher die Daten kommen ──────────────────────────────────────────────────
// Ein RTL-SDR-Stick empfängt auf 1090 MHz, was Flugzeugtransponder senden
// (ADS-B/Mode S). readsb dekodiert und legt jede Sekunde aircraft.json ab
// (tools/setup-aeronautik.sh richtet das ein). Dieser Node liest die Datei.
//
// ── Woher und wohin ─────────────────────────────────────────────────────────
// Steht NICHT in den Telegrammen – nur Kennung, Rufzeichen, Position, Höhe,
// Geschwindigkeit, Kurs. Route und Flugzeugtyp schlagen wir bei adsbdb.com
// nach: höchstens eine Anfrage alle 1,5 s, Ergebnisse zwischengespeichert
// (Routen 12 h, Flugzeuge 7 Tage, "unbekannt" 3 h). Charter- und Privatflüge
// haben oft keinen Eintrag; die Seite zeigt das, statt zu raten.
//
// ── Nodes ohne Empfänger ────────────────────────────────────────────────────
// Ein Node mit Empfänger kündigt sich im Netz an (aero_empfaenger). Nodes ohne
// eigenen Stick holen das Bild des nächstgelegenen Empfängers per
// Direktverbindung – bereits mit Routen ergänzt.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const aeroProtocol = "/fundus/aeronautik/1.0.0"

func aeroQuelle() string {
	if v := strings.TrimSpace(os.Getenv("FUNDUS_ADSB_QUELLE")); v != "" {
		return v
	}
	return "/run/readsb/aircraft.json"
}

// ── Nachschlagen: Route und Flugzeug ─────────────────────────────────────────

type aeroFlughafen struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Stadt string `json:"stadt"`
	Land  string `json:"land"`
}

type aeroRoute struct {
	Von     *aeroFlughafen `json:"von,omitempty"`
	Nach    *aeroFlughafen `json:"nach,omitempty"`
	Airline string         `json:"airline,omitempty"`
}

type aeroFlugzeug struct {
	Typ         string `json:"typ,omitempty"`
	Hersteller  string `json:"hersteller,omitempty"`
	Kennzeichen string `json:"kennzeichen,omitempty"`
	Halter      string `json:"halter,omitempty"`
}

type aeroEintrag struct {
	route    *aeroRoute
	flugzeug *aeroFlugzeug
	bis      time.Time
}

var (
	aeroMu       sync.Mutex
	aeroRouten   = map[string]aeroEintrag{} // Rufzeichen → Route
	aeroGeraete  = map[string]aeroEintrag{} // Kennung (hex) → Flugzeug
	aeroWarte    = make(chan string, 400)   // "r:<rufzeichen>" oder "f:<hex>"
	aeroInArbeit = map[string]bool{}
	aeroSpuren   = map[string][][2]float64{} // hex → letzte Positionen
	aeroZuletzt  = map[string]time.Time{}
	aeroBild     []byte    // letztes fertiges Bild (JSON) – auch für andere Nodes
	aeroBildZeit time.Time
)

func aeroText(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}

func aeroFlughafenAus(m map[string]any) *aeroFlughafen {
	if m == nil {
		return nil
	}
	code := aeroText(m, "iata_code")
	if code == "" {
		code = aeroText(m, "icao_code")
	}
	return &aeroFlughafen{Code: code, Name: aeroText(m, "name"),
		Stadt: aeroText(m, "municipality"), Land: aeroText(m, "country_name")}
}

func (s *Server) aeroNachschlagen() {
	defer func() {
		if r := recover(); r != nil && s.log != nil {
			s.log.Error("Aeronautik: Nachschlagen abgebrochen", zap.Any("grund", r))
		}
	}()
	client := &http.Client{Timeout: 6 * time.Second}
	holen := func(url string) (map[string]any, int) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, 0
		}
		req.Header.Set("User-Agent", "Fundus-Aeronautik/1.0 (+https://fnd.resolve.bar)")
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0
		}
		defer resp.Body.Close()
		var d map[string]any
		_ = json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&d)
		return d, resp.StatusCode
	}
	for auftrag := range aeroWarte {
		art, schl := auftrag[:1], auftrag[2:]
		var eintrag aeroEintrag
		if art == "r" {
			d, code := holen("https://api.adsbdb.com/v0/callsign/" + schl)
			eintrag.bis = time.Now().Add(3 * time.Hour)
			if code == http.StatusOK {
				if resp, ok := d["response"].(map[string]any); ok {
					if fr, ok := resp["flightroute"].(map[string]any); ok {
						r := &aeroRoute{}
						r.Von = aeroFlughafenAus(mapAus(fr["origin"]))
						r.Nach = aeroFlughafenAus(mapAus(fr["destination"]))
						if al := mapAus(fr["airline"]); al != nil {
							r.Airline = aeroText(al, "name")
						}
						eintrag.route = r
						eintrag.bis = time.Now().Add(12 * time.Hour)
					}
				}
			}
			aeroMu.Lock()
			aeroRouten[schl] = eintrag
			delete(aeroInArbeit, auftrag)
			aeroMu.Unlock()
		} else {
			d, code := holen("https://api.adsbdb.com/v0/aircraft/" + schl)
			eintrag.bis = time.Now().Add(3 * time.Hour)
			if code == http.StatusOK {
				if resp, ok := d["response"].(map[string]any); ok {
					if ac, ok := resp["aircraft"].(map[string]any); ok {
						eintrag.flugzeug = &aeroFlugzeug{Typ: aeroText(ac, "type"), Hersteller: aeroText(ac, "manufacturer"),
							Kennzeichen: aeroText(ac, "registration"), Halter: aeroText(ac, "registered_owner")}
						eintrag.bis = time.Now().Add(7 * 24 * time.Hour)
					}
				}
			}
			aeroMu.Lock()
			aeroGeraete[schl] = eintrag
			delete(aeroInArbeit, auftrag)
			aeroMu.Unlock()
		}
		time.Sleep(1500 * time.Millisecond) // höflich bleiben: höchstens eine Anfrage alle 1,5 s
	}
}

func mapAus(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// aeroVormerken: nachschlagen lassen, falls nicht bekannt oder abgelaufen.
// Muss unter aeroMu aufgerufen werden.
func aeroVormerken(art, schl string, cache map[string]aeroEintrag) {
	if schl == "" {
		return
	}
	if e, ok := cache[schl]; ok && time.Now().Before(e.bis) {
		return
	}
	auftrag := art + ":" + schl
	if aeroInArbeit[auftrag] {
		return
	}
	select {
	case aeroWarte <- auftrag:
		aeroInArbeit[auftrag] = true
	default: // Warteschlange voll – beim nächsten Mal
	}
}

// ── Lesen und aufbereiten ────────────────────────────────────────────────────

func aeroRohLesen(quelle string) ([]byte, error) {
	if strings.HasPrefix(quelle, "http://") || strings.HasPrefix(quelle, "https://") {
		c := &http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get(quelle)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}
	return os.ReadFile(quelle)
}

func aeroZahl(m map[string]any, k string) (float64, bool) {
	f, ok := m[k].(float64)
	return f, ok
}

// aeroLokal: eigenes Bild aus readsb, ergänzt. Nil, wenn kein Empfänger da ist
// oder die Daten älter als 30 s sind.
func (s *Server) aeroLokal() []byte {
	aeroMu.Lock()
	if aeroBild != nil && time.Since(aeroBildZeit) < 900*time.Millisecond {
		b := aeroBild
		aeroMu.Unlock()
		return b
	}
	aeroMu.Unlock()

	roh, err := aeroRohLesen(aeroQuelle())
	if err != nil {
		return nil
	}
	var d struct {
		Now      float64          `json:"now"`
		Messages float64          `json:"messages"`
		Aircraft []map[string]any `json:"aircraft"`
	}
	if json.Unmarshal(roh, &d) != nil || d.Now == 0 {
		return nil
	}
	if time.Since(time.Unix(int64(d.Now), 0)) > 30*time.Second {
		return nil // Decoder steht – nichts Veraltetes zeigen
	}
	lat, lon := s.marktPosition() // Standort des Nodes (Einstellungen → Karte)
	jetzt := time.Now()

	aeroMu.Lock()
	defer aeroMu.Unlock()
	var flieger []gin.H
	for _, a := range d.Aircraft {
		hex := strings.ToLower(aeroText(a, "hex"))
		if hex == "" || strings.HasPrefix(hex, "~") {
			continue // ohne echte Kennung (TIS-B) überspringen
		}
		pLat, okLat := aeroZahl(a, "lat")
		pLon, okLon := aeroZahl(a, "lon")
		seenPos, _ := aeroZahl(a, "seen_pos")
		if !okLat || !okLon || seenPos > 60 {
			continue // ohne aktuelle Position nicht auf die Karte
		}
		flug := strings.ToUpper(aeroText(a, "flight"))
		e := gin.H{"hex": hex, "flug": flug, "lat": pLat, "lon": pLon, "alter_s": seenPos}
		if v, ok := a["alt_baro"].(string); ok && v == "ground" {
			e["am_boden"] = true
		} else if ft, ok := aeroZahl(a, "alt_baro"); ok {
			e["hoehe_m"] = math.Round(ft * 0.3048)
		}
		if kn, ok := aeroZahl(a, "gs"); ok {
			e["geschw_kmh"] = math.Round(kn * 1.852)
		}
		if tr, ok := aeroZahl(a, "track"); ok {
			e["kurs"] = tr
		}
		if vr, ok := aeroZahl(a, "baro_rate"); ok {
			e["steig_mmin"] = math.Round(vr * 0.3048)
		}
		if sq := aeroText(a, "squawk"); sq != "" {
			e["squawk"] = sq
			if sq == "7500" || sq == "7600" || sq == "7700" {
				e["notfall"] = true // Entführung, Funkausfall, Notfall
			}
		}
		if lat != 0 || lon != 0 {
			e["km"] = math.Round(haversineKm(lat, lon, pLat, pLon)*10) / 10
		}
		// Spur: neue Position anhängen, höchstens 120 Punkte.
		sp := aeroSpuren[hex]
		if n := len(sp); n == 0 || sp[n-1][0] != pLat || sp[n-1][1] != pLon {
			sp = append(sp, [2]float64{pLat, pLon})
			if len(sp) > 120 {
				sp = sp[len(sp)-120:]
			}
			aeroSpuren[hex] = sp
		}
		aeroZuletzt[hex] = jetzt
		e["spur"] = sp
		// Route und Flugzeug aus dem Zwischenspeicher, sonst vormerken.
		if flug != "" {
			if r, ok := aeroRouten[flug]; ok && r.route != nil {
				e["route"] = r.route
			}
			aeroVormerken("r", flug, aeroRouten)
		}
		if g, ok := aeroGeraete[hex]; ok && g.flugzeug != nil {
			e["flugzeug"] = g.flugzeug
		} else if reg := aeroText(a, "r"); reg != "" {
			e["flugzeug"] = &aeroFlugzeug{Kennzeichen: reg, Typ: aeroText(a, "t")}
		}
		aeroVormerken("f", hex, aeroGeraete)
		flieger = append(flieger, e)
	}
	// Spuren von Flugzeugen vergessen, die seit 5 min nicht mehr da sind.
	for hex, t := range aeroZuletzt {
		if jetzt.Sub(t) > 5*time.Minute {
			delete(aeroZuletzt, hex)
			delete(aeroSpuren, hex)
		}
	}
	sort.Slice(flieger, func(i, j int) bool {
		a, _ := flieger[i]["km"].(float64)
		b, _ := flieger[j]["km"].(float64)
		return a < b
	})
	if flieger == nil {
		flieger = []gin.H{}
	}
	bild, _ := json.Marshal(gin.H{"quelle": "lokal", "empfaenger": gin.H{"lat": lat, "lon": lon},
		"standort": gin.H{"lat": lat, "lon": lon}, // R849: eigene Position = Empfänger
		"node": aeroNodeName(),                     // R851: welcher Pi antwortet
		"zeit": int64(d.Now), "nachrichten": int64(d.Messages), "flugzeuge": flieger})
	aeroBild, aeroBildZeit = bild, jetzt
	return bild
}

// ── Ankündigen und teilen ────────────────────────────────────────────────────

func (s *Server) aeroAnkuendigen() {
	defer func() { _ = recover() }()
	time.Sleep(40 * time.Second)
	for {
		if s.aeroLokal() != nil && s.node != nil {
			lat, lon := s.marktPosition()
			rec := &storage.Record{ID: s.nodeID(), Type: storage.RecordAeroEmpfaenger,
				Data: map[string]any{"peer": s.nodeID(), "lat": lat, "lon": lon, "zeit": time.Now().Unix()}}
			rec.OwnerID, rec.CreatedAt = s.nodeID(), time.Now()
			if s.store.Put(rec) == nil {
				if data, err := marshalRecord(rec); err == nil {
					_ = s.node.Publish(context.Background(), p2p.TopicEnergy, data)
				}
			}
		}
		time.Sleep(10 * time.Minute)
	}
}

// aeroVomNachbarn: Bild des nächstgelegenen Empfängers holen (1 s zwischengespeichert).
var (
	aeroFremdMu   sync.Mutex
	aeroFremd     []byte
	aeroFremdZeit time.Time
)

func (s *Server) aeroVomNachbarn(ctx context.Context) []byte {
	aeroFremdMu.Lock()
	defer aeroFremdMu.Unlock()
	if aeroFremd != nil && time.Since(aeroFremdZeit) < time.Second {
		return aeroFremd
	}
	if s.node == nil {
		return nil
	}
	lat, lon := s.marktPosition()
	type kandidat struct {
		peer string
		km   float64
	}
	var ks []kandidat
	alle, _ := s.store.List(storage.RecordAeroEmpfaenger)
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		if z, _ := toFloatOK(r.Data["zeit"]); time.Since(time.Unix(int64(z), 0)) > 30*time.Minute {
			continue
		}
		peer, _ := r.Data["peer"].(string)
		if peer == "" || peer == s.nodeID() {
			continue
		}
		rl, _ := toFloatOK(r.Data["lat"])
		ro, _ := toFloatOK(r.Data["lon"])
		ks = append(ks, kandidat{peer, haversineKm(lat, lon, rl, ro)})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].km < ks[j].km })
	for _, k := range ks {
		c, abbr := context.WithTimeout(ctx, 2*time.Second)
		antw, err := s.node.SendAndReceive(c, k.peer, aeroProtocol, []byte("{}"))
		abbr()
		if err == nil && len(antw) > 0 {
			var bild map[string]any
			if json.Unmarshal(antw, &bild) == nil {
				bild["quelle"] = "peer"
				bild["peer_km"] = math.Round(k.km*10) / 10
				// R851: Standort und Name des EMPFÄNGERS nicht als die eigenen
				// ausgeben – sonst stünde das Häuschen am Ort des Empfängers.
				bild["empfaenger_node"] = bild["node"]
				bild["node"] = aeroNodeName()
				delete(bild, "standort")
				aeroVonHierAus(bild, lat, lon)
				aeroFremd, _ = json.Marshal(bild)
				aeroFremdZeit = time.Now()
				return aeroFremd
			}
		}
	}
	return nil
}

// GET /api/v1/aeronautik/live
func (s *Server) aeroLive(c *gin.Context) {
	if b := s.aeroLokal(); b != nil {
		c.Data(http.StatusOK, "application/json", b)
		return
	}
	if b := s.aeroVomNachbarn(c.Request.Context()); b != nil {
		c.Data(http.StatusOK, "application/json", b)
		return
	}
	c.JSON(http.StatusOK, gin.H{"quelle": "keine", "flugzeuge": []any{},
		"hinweis": fmt.Sprintf("Kein Empfänger: weder %s auf diesem Node noch ein anderer im Netz.", aeroQuelle())})
}

// GET /api/v1/aeronautik/einrichtung (R848): Fortschritt der Einrichtung, die
// setup-aeronautik.sh im Hintergrund ausführt. Leer, wenn nie eingerichtet.
func (s *Server) aeroEinrichtung(c *gin.Context) {
	roh, err := os.ReadFile("/run/fundus-aeronautik/status.json")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	var st map[string]any
	if json.Unmarshal(roh, &st) != nil {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	// Hängt sie? Beim Übersetzen frischt das Skript den Stand alle 5 s auf.
	fertig, _ := st["fertig"].(bool)
	if stand, ok := st["stand"].(float64); ok && !fertig && time.Since(time.Unix(int64(stand), 0)) > 10*time.Minute {
		st["haengt"] = true
	}
	c.JSON(http.StatusOK, st)
}

func (s *Server) registerAeronautik(r *gin.Engine) {
	r.GET("/api/v1/aeronautik/live", s.aeroLive)
	r.GET("/api/v1/aeronautik/einrichtung", s.aeroEinrichtung)
	if s.node != nil {
		s.node.RegisterProtocol(aeroProtocol, func(peerID string, data []byte) []byte {
			return s.aeroLokal()
		})
	}
	go s.aeroNachschlagen()
	go s.aeroAnkuendigen()
}

// aeroVonHierAus (R849): Entfernungen vom abfragenden Node aus, nicht vom
// Empfänger – sonst zeigte "Über dir" auf dem 39er, wie weit ein Flugzeug vom
// 71er entfernt ist. Danach neu nach Entfernung sortiert.
func aeroVonHierAus(bild map[string]any, lat, lon float64) {
	if lat == 0 && lon == 0 {
		return
	}
	bild["standort"] = gin.H{"lat": lat, "lon": lon}
	fl, ok := bild["flugzeuge"].([]any)
	if !ok {
		return
	}
	km := func(x any) float64 {
		if f, ok := x.(map[string]any); ok {
			if v, ok := f["km"].(float64); ok {
				return v
			}
		}
		return math.Inf(1)
	}
	for _, x := range fl {
		f, ok := x.(map[string]any)
		if !ok {
			continue
		}
		fLat, ok1 := f["lat"].(float64)
		fLon, ok2 := f["lon"].(float64)
		if ok1 && ok2 {
			f["km"] = math.Round(haversineKm(lat, lon, fLat, fLon)*10) / 10
		}
	}
	sort.SliceStable(fl, func(i, j int) bool { return km(fl[i]) < km(fl[j]) })
}

// aeroNodeName: Rechnername dieses Pi – die Seite nennt ihn, damit klar ist,
// welcher Pi antwortet (die Lastverteilung reicht die Schnittstelle weiter).
func aeroNodeName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "dieser Node"
}

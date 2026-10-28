package api

// Funkempfang (R859): Der RTL-SDR-Stick ist umschaltbar zwischen
//   adsb – Flugzeuge (readsb, aeronautik.go)
//   vdl2 – Funknachrichten Flugzeug ↔ Boden (dumpvdl2 → UDP 127.0.0.1:5555, JSON)
//   ais  – Schiffe (AIS-catcher → UDP 127.0.0.1:10110, NMEA)
//
// Umschalten: Der Node schreibt den Modus nach <DataDir>/funk-modus; der
// systemd-Pfadwächter fundus-funk.path (Root) wendet ihn an
// (tools/funk-modus.sh). Der Node braucht dafür keine sudo-Freigabe.
//
// Nichts wird protokolliert: Nachrichten und Schiffe liegen nur im
// Arbeitsspeicher (die letzten 300 Nachrichten, Schiffe 30 min).
// Nodes ohne Stick holen das Bild des nächsten Empfängers per Direktverbindung.

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

const funkProtocol = "/fundus/funk/1.0.0"

var funkModi = map[string]string{"adsb": "Flugzeuge", "vdl2": "Funknachrichten", "ais": "Schiffe"}

// ── Modus ────────────────────────────────────────────────────────────────────

func (s *Server) funkModusDatei() string { return filepath.Join(s.cfg.DataDir, "funk-modus") }

func funkHatStick() bool {
	for _, p := range []string{"/run/fundus-funk/status.json", "/usr/local/bin/dumpvdl2", "/usr/local/bin/AIS-catcher"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func (s *Server) funkStatusDaten() gin.H {
	modus := "adsb"
	if b, err := os.ReadFile(s.funkModusDatei()); err == nil {
		if m := strings.TrimSpace(string(b)); funkModi[m] != "" {
			modus = m
		}
	}
	h := gin.H{"gewaehlt": modus, "stick": funkHatStick(), "node": aeroNodeName()}
	if b, err := os.ReadFile("/run/fundus-funk/status.json"); err == nil {
		var st map[string]any
		if json.Unmarshal(b, &st) == nil {
			h["angewandt"] = st
		}
	}
	vorhanden := gin.H{}
	for m, p := range map[string]string{"vdl2": "/usr/local/bin/dumpvdl2", "ais": "/usr/local/bin/AIS-catcher"} {
		_, err := os.Stat(p)
		vorhanden[m] = err == nil
	}
	vorhanden["adsb"] = true
	h["vorhanden"] = vorhanden
	funkMu.Lock()
	h["nachrichten"], h["schiffe"] = len(funkNachrichten), len(funkSchiffe)
	funkMu.Unlock()
	return h
}

// GET /api/v1/funk/status
func (s *Server) funkStatus(c *gin.Context) { c.JSON(http.StatusOK, s.funkStatusDaten()) }

// POST /api/v1/admin/funk/modus {modus}
func (s *Server) funkModusSetzen(c *gin.Context) {
	var req struct{ Modus string }
	if c.ShouldBindJSON(&req) != nil || funkModi[req.Modus] == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Modus muss adsb, vdl2 oder ais sein."})
		return
	}
	if !funkHatStick() {
		c.JSON(http.StatusConflict, gin.H{"error": "Dieser Pi hat keinen Empfänger. Umschalten auf dem Pi mit dem Stick."})
		return
	}
	pfad := s.funkModusDatei()
	if err := os.WriteFile(pfad+".neu", []byte(req.Modus+"\n"), 0o664); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	if err := os.Rename(pfad+".neu", pfad); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "modus": req.Modus})
}

// ── Speicher (nur Arbeitsspeicher) ───────────────────────────────────────────

type funkSchiff struct {
	MMSI      string  `json:"mmsi"`
	Name      string  `json:"name,omitempty"`
	Ruf       string  `json:"rufzeichen,omitempty"`
	Typ       int     `json:"typ,omitempty"`
	Art       string  `json:"art,omitempty"`
	Ziel      string  `json:"ziel,omitempty"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Kurs      float64 `json:"kurs"` // -1 = unbekannt
	KmH       float64 `json:"kmh"`  // -1 = unbekannt
	Status    int     `json:"status"`
	hatPos    bool
	Zuletzt   int64   `json:"zuletzt"`
}

var (
	funkMu          sync.Mutex
	funkNachrichten []map[string]any // neueste zuerst
	funkSchiffe     = map[string]*funkSchiff{}
	funkLetztNachr  time.Time
	funkLetztSchiff time.Time
)

// ── VDL2 (dumpvdl2, JSON über UDP) ───────────────────────────────────────────

func funkVDL2(roh []byte) {
	var d map[string]any
	if json.Unmarshal(roh, &d) != nil {
		return
	}
	v := mapAus(d["vdl2"])
	if v == nil {
		return
	}
	avlc := mapAus(v["avlc"])
	n := map[string]any{"zeit": time.Now().Unix()}
	if f, ok := v["freq"].(float64); ok {
		n["mhz"] = math.Round(f/1e3) / 1e3
	}
	if avlc != nil {
		if src := mapAus(avlc["src"]); src != nil {
			n["von"], n["von_art"] = aeroText(src, "addr"), aeroText(src, "type")
		}
		if dst := mapAus(avlc["dst"]); dst != nil {
			n["an"], n["an_art"] = aeroText(dst, "addr"), aeroText(dst, "type")
		}
		if ac := mapAus(avlc["acars"]); ac != nil {
			n["flug"] = strings.TrimSpace(aeroText(ac, "flight"))
			n["kennzeichen"] = strings.TrimLeft(aeroText(ac, "reg"), ".")
			n["label"] = aeroText(ac, "label")
			n["text"] = aeroText(ac, "msg_text")
		} else {
			n["art"] = aeroText(avlc, "frame_type")
		}
	}
	// Leere Verwaltungsrahmen ohne ACARS-Inhalt nicht speichern – sie sind die
	// große Mehrheit und sagen einem Menschen nichts.
	if n["text"] == nil && n["flug"] == nil {
		return
	}
	funkMu.Lock()
	funkNachrichten = append([]map[string]any{n}, funkNachrichten...)
	if len(funkNachrichten) > 300 {
		funkNachrichten = funkNachrichten[:300]
	}
	funkLetztNachr = time.Now()
	funkMu.Unlock()
}

// ── AIS (AIS-catcher, NMEA über UDP) ─────────────────────────────────────────

type aisBits []byte

func aisEntpacken(nutz string, fuell int) aisBits {
	b := make(aisBits, 0, len(nutz)*6)
	for _, c := range nutz {
		v := int(c) - 48
		if v > 40 {
			v -= 8
		}
		if v < 0 || v > 63 {
			return nil
		}
		for i := 5; i >= 0; i-- {
			b = append(b, byte((v>>uint(i))&1))
		}
	}
	if fuell > 0 && fuell <= len(b) {
		b = b[:len(b)-fuell]
	}
	return b
}

func (b aisBits) u(start, n int) uint64 {
	if start+n > len(b) {
		return 0
	}
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<1 | uint64(b[start+i])
	}
	return v
}

func (b aisBits) i(start, n int) int64 {
	v := int64(b.u(start, n))
	if n > 0 && start+n <= len(b) && b[start] == 1 {
		v -= int64(1) << uint(n)
	}
	return v
}

func (b aisBits) t(start, n int) string {
	var sb strings.Builder
	for p := start; p+6 <= start+n && p+6 <= len(b); p += 6 {
		v := int(b.u(p, 6))
		if v < 32 {
			v += 64
		}
		sb.WriteByte(byte(v))
	}
	return strings.TrimSpace(strings.TrimRight(sb.String(), "@ "))
}

func aisArt(t int) string {
	switch {
	case t == 30:
		return "Fischerei"
	case t == 31 || t == 32 || t == 52:
		return "Schlepper"
	case t == 35:
		return "Militär"
	case t == 36:
		return "Segelboot"
	case t == 37:
		return "Sportboot"
	case t == 50:
		return "Lotse"
	case t == 51:
		return "Seenotrettung"
	case t == 55:
		return "Polizei"
	case t >= 60 && t <= 69:
		return "Fahrgastschiff"
	case t >= 70 && t <= 79:
		return "Frachtschiff"
	case t >= 80 && t <= 89:
		return "Tanker"
	case t >= 40 && t <= 49:
		return "Schnellboot"
	}
	return ""
}

var (
	aisTeileMu sync.Mutex
	aisTeile   = map[string][]string{} // Kanal+Folge → Teile
)

func funkNMEA(zeile string) {
	zeile = strings.TrimSpace(zeile)
	if !strings.HasPrefix(zeile, "!AIVDM") && !strings.HasPrefix(zeile, "!AIVDO") {
		return
	}
	if i := strings.Index(zeile, "*"); i > 0 {
		zeile = zeile[:i]
	}
	f := strings.Split(zeile, ",")
	if len(f) < 7 {
		return
	}
	anzahl, _ := strconv.Atoi(f[1])
	nr, _ := strconv.Atoi(f[2])
	fuell, _ := strconv.Atoi(f[6])
	nutz := f[5]
	if anzahl > 1 {
		schl := f[3] + "/" + f[4]
		aisTeileMu.Lock()
		if nr == 1 {
			aisTeile[schl] = nil
		}
		aisTeile[schl] = append(aisTeile[schl], nutz)
		teile := aisTeile[schl]
		if nr < anzahl || len(teile) != anzahl {
			aisTeileMu.Unlock()
			return
		}
		delete(aisTeile, schl)
		aisTeileMu.Unlock()
		nutz = strings.Join(teile, "")
	}
	b := aisEntpacken(nutz, fuell)
	if len(b) < 38 {
		return
	}
	typ := int(b.u(0, 6))
	mmsi := strconv.FormatUint(b.u(8, 30), 10)
	funkMu.Lock()
	defer funkMu.Unlock()
	sch := funkSchiffe[mmsi]
	if sch == nil {
		sch = &funkSchiff{MMSI: mmsi, Kurs: -1, KmH: -1}
	}
	pos := func(lonBit, latBit, sogBit, cogBit int) {
		lon := float64(b.i(lonBit, 28)) / 600000
		lat := float64(b.i(latBit, 27)) / 600000
		if math.Abs(lon) > 180 || math.Abs(lat) > 90 {
			return // 181/91 = keine Position
		}
		sch.Lat, sch.Lon, sch.hatPos = lat, lon, true
		if sog := b.u(sogBit, 10); sog < 1023 {
			sch.KmH = math.Round(float64(sog)/10*1.852*10) / 10
		}
		if cog := b.u(cogBit, 12); cog < 3600 {
			sch.Kurs = float64(cog) / 10
		}
	}
	switch typ {
	case 1, 2, 3:
		if len(b) < 137 {
			return
		}
		sch.Status = int(b.u(38, 4))
		pos(61, 89, 50, 116)
	case 18:
		if len(b) < 133 {
			return
		}
		pos(57, 85, 46, 112)
	case 19:
		if len(b) < 271 {
			return
		}
		pos(57, 85, 46, 112)
		sch.Name, sch.Typ = b.t(143, 120), int(b.u(263, 8))
	case 5:
		if len(b) < 240 { // bis einschließlich Schiffstyp; das Ziel liest t() so weit es reicht
			return
		}
		sch.Ruf, sch.Name, sch.Typ, sch.Ziel = b.t(70, 42), b.t(112, 120), int(b.u(232, 8)), b.t(302, 120)
	case 24:
		if len(b) < 160 {
			return
		}
		if b.u(38, 2) == 0 {
			sch.Name = b.t(40, 120)
		} else {
			sch.Typ, sch.Ruf = int(b.u(40, 8)), b.t(90, 42)
		}
	default:
		return
	}
	sch.Art = aisArt(sch.Typ)
	sch.Zuletzt = time.Now().Unix()
	funkSchiffe[mmsi] = sch
	funkLetztSchiff = time.Now()
}

// ── UDP-Empfang ──────────────────────────────────────────────────────────────

func (s *Server) funkLauschen(port int, verarbeiten func([]byte)) {
	defer func() { _ = recover() }()
	adr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	for {
		conn, err := net.ListenUDP("udp", adr)
		if err != nil {
			time.Sleep(time.Minute)
			continue
		}
		puffer := make([]byte, 65536)
		for {
			n, _, err := conn.ReadFromUDP(puffer)
			if err != nil {
				break
			}
			daten := append([]byte(nil), puffer[:n]...)
			func() { defer func() { _ = recover() }(); verarbeiten(daten) }()
		}
		conn.Close()
	}
}

// ── Ausgabe (lokal oder vom nächsten Empfänger) ──────────────────────────────

func (s *Server) funkLokal(art string) gin.H {
	lat, lon := s.marktPosition()
	funkMu.Lock()
	defer funkMu.Unlock()
	if art == "schiffe" {
		grenze := time.Now().Add(-30 * time.Minute).Unix()
		var liste []gin.H
		for k, sch := range funkSchiffe {
			if sch.Zuletzt < grenze {
				delete(funkSchiffe, k)
				continue
			}
			if !sch.hatPos {
				continue
			}
			e := gin.H{"mmsi": sch.MMSI, "name": sch.Name, "rufzeichen": sch.Ruf, "art": sch.Art, "typ": sch.Typ,
				"ziel": sch.Ziel, "lat": sch.Lat, "lon": sch.Lon, "kurs": sch.Kurs, "kmh": sch.KmH,
				"status": sch.Status, "alter_s": time.Now().Unix() - sch.Zuletzt}
			if lat != 0 || lon != 0 {
				e["km"] = math.Round(haversineKm(lat, lon, sch.Lat, sch.Lon)*10) / 10
			}
			liste = append(liste, e)
		}
		sort.Slice(liste, func(i, j int) bool {
			a, _ := liste[i]["km"].(float64)
			b, _ := liste[j]["km"].(float64)
			return a < b
		})
		if liste == nil {
			liste = []gin.H{}
		}
		return gin.H{"schiffe": liste, "zuletzt": funkLetztSchiff.Unix()}
	}
	kopie := make([]map[string]any, len(funkNachrichten))
	copy(kopie, funkNachrichten)
	return gin.H{"nachrichten": kopie, "zuletzt": funkLetztNachr.Unix()}
}

func (s *Server) funkAntwort(c *gin.Context, art string) {
	st := s.funkStatusDaten()
	if funkHatStick() {
		h := s.funkLokal(art)
		h["status"], h["quelle"] = st, "lokal"
		lat, lon := s.marktPosition()
		h["standort"] = gin.H{"lat": lat, "lon": lon}
		c.JSON(http.StatusOK, h)
		return
	}
	// Kein eigener Stick: nächsten Empfänger im Netz fragen.
	lat, lon := s.marktPosition()
	type kand struct {
		peer string
		km   float64
	}
	var ks []kand
	alle, _ := s.store.List(storage.RecordAeroEmpfaenger)
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		p, _ := r.Data["peer"].(string)
		if p == "" || p == s.nodeID() {
			continue
		}
		rl, _ := toFloatOK(r.Data["lat"])
		ro, _ := toFloatOK(r.Data["lon"])
		ks = append(ks, kand{p, haversineKm(lat, lon, rl, ro)})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].km < ks[j].km })
	anfrage, _ := json.Marshal(gin.H{"art": art})
	for _, k := range ks {
		if s.node == nil {
			break
		}
		ctx, abbr := context.WithTimeout(c.Request.Context(), 2*time.Second)
		antw, err := s.node.SendAndReceive(ctx, k.peer, funkProtocol, anfrage)
		abbr()
		var h map[string]any
		if err == nil && json.Unmarshal(antw, &h) == nil {
			h["quelle"], h["peer_km"] = "peer", math.Round(k.km*10)/10
			if lat != 0 || lon != 0 {
				h["standort"] = gin.H{"lat": lat, "lon": lon}
				if sl, ok := h["schiffe"].([]any); ok { // Entfernungen von hier aus
					for _, x := range sl {
						if m, ok := x.(map[string]any); ok {
							a, _ := m["lat"].(float64)
							o, _ := m["lon"].(float64)
							m["km"] = math.Round(haversineKm(lat, lon, a, o)*10) / 10
						}
					}
				}
			}
			c.JSON(http.StatusOK, h)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"quelle": "keine", "status": st, "schiffe": []any{}, "nachrichten": []any{}})
}

func (s *Server) registerFunk(r *gin.Engine) {
	r.GET("/api/v1/funk/status", s.funkStatus)
	r.GET("/api/v1/funk/schiffe", func(c *gin.Context) { s.funkAntwort(c, "schiffe") })
	r.GET("/api/v1/funk/nachrichten", func(c *gin.Context) { s.funkAntwort(c, "nachrichten") })
	if s.node != nil {
		s.node.RegisterProtocol(funkProtocol, func(peerID string, data []byte) []byte {
			var req struct{ Art string }
			_ = json.Unmarshal(data, &req)
			if req.Art != "schiffe" {
				req.Art = "nachrichten"
			}
			h := s.funkLokal(req.Art)
			h["status"] = s.funkStatusDaten()
			b, _ := json.Marshal(h)
			return b
		})
	}
	go s.funkLauschen(5555, funkVDL2)
	go s.funkLauschen(10110, func(b []byte) {
		for _, z := range strings.Split(string(b), "\n") {
			funkNMEA(z)
		}
	})
}

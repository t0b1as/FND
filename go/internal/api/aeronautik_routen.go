package api

// Aeronautik – Routen (R853): drei Anbieter, Pausen statt "unbekannt",
// Plausibilitätsprüfung gegen Position und Kurs.
//
// ── Warum ────────────────────────────────────────────────────────────────────
// R845 fragte nur adsbdb.com und merkte sich JEDE Antwort außer "OK" drei
// Stunden lang als "unbekannt". Drosselte der Dienst, galt jedes neue
// Rufzeichen als unbekannt – nach einer Weile wurden keine Routen mehr
// aufgelöst. Und Routendatenbanken liefern die PLANMÄSSIGE Strecke eines
// Rufzeichens; Fluggesellschaften nutzen Rufzeichen auch für andere Strecken.
//
// ── Wie ──────────────────────────────────────────────────────────────────────
// Anbieter der Reihe nach:
//   1. adsb.lol  – viele Rufzeichen in EINER Anfrage, mit Position
//   2. adsbdb.com
//   3. hexdb.io  – Route als Flughafencodes, Flughäfen einzeln nachgeschlagen
// Drosselung, Serverfehler oder keine Antwort: Der Anbieter pausiert (1, 2, 4
// … bis 30 min), die Rufzeichen bleiben offen. "Unbekannt" gilt erst, wenn
// ALLE Anbieter sicher nichts haben – und auch dann nur 3 h.
//
// Plausibilität: Für jede Etappe der Route Abstand des Flugzeugs zur
// Großkreisstrecke und Lage entlang der Strecke; weit vom Start und Ziel muss
// der Kurs grob zum Ziel zeigen. Die passende Etappe wird angezeigt; passt
// keine, ist die Route als zweifelhaft markiert statt einfach falsch.

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

var aeroClient = &http.Client{Timeout: 8 * time.Second}

// ── Anbieter mit Pausen ──────────────────────────────────────────────────────

type aeroAnbieter struct {
	name  string
	bis   time.Time     // pausiert bis
	pause time.Duration // nächste Pause
}

var (
	aeroAnbMu  sync.Mutex
	aeroAnbAll = map[string]*aeroAnbieter{
		"adsb.lol": {name: "adsb.lol"}, "adsbdb": {name: "adsbdb"}, "hexdb": {name: "hexdb"},
	}
)

func aeroAnbieterFrei(n string) bool {
	aeroAnbMu.Lock()
	defer aeroAnbMu.Unlock()
	return time.Now().After(aeroAnbAll[n].bis)
}

// aeroAnbieterFehler: gedrosselt oder gestört – pausieren, doppelt so lang wie zuletzt.
func aeroAnbieterFehler(n string) {
	aeroAnbMu.Lock()
	defer aeroAnbMu.Unlock()
	a := aeroAnbAll[n]
	if a.pause == 0 {
		a.pause = time.Minute
	} else if a.pause < 30*time.Minute {
		a.pause *= 2
	}
	a.bis = time.Now().Add(a.pause)
}

func aeroAnbieterOK(n string) {
	aeroAnbMu.Lock()
	defer aeroAnbMu.Unlock()
	aeroAnbAll[n].pause = 0
}

// aeroHolen: GET oder POST; Ergebnis, ob eine VERBINDLICHE Antwort kam (200
// oder 404) – alles andere zählt als Störung.
func aeroHolen(methode, url string, rumpf []byte) (map[string]any, []any, int, bool) {
	var leser io.Reader
	if rumpf != nil {
		leser = bytes.NewReader(rumpf)
	}
	req, err := http.NewRequest(methode, url, leser)
	if err != nil {
		return nil, nil, 0, false
	}
	req.Header.Set("User-Agent", "Fundus-Aeronautik/1.1 (+https://fnd.resolve.bar)")
	if rumpf != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := aeroClient.Do(req)
	if err != nil {
		return nil, nil, 0, false
	}
	defer resp.Body.Close()
	roh, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return nil, nil, resp.StatusCode, false
	}
	var m map[string]any
	var l []any
	if json.Unmarshal(roh, &m) != nil {
		_ = json.Unmarshal(roh, &l)
	}
	return m, l, resp.StatusCode, true
}

// ── Warteliste ───────────────────────────────────────────────────────────────

type aeroOffen struct {
	lat, lon float64
	verfehlt map[string]bool // Anbieter, die sicher nichts haben
}

var (
	aeroRtMu    sync.Mutex
	aeroRtOffen = map[string]*aeroOffen{}
)

// aeroRouteVormerken – aus aeroLokal (unter aeroMu). Nimmt nur auf, was nicht
// schon gültig im Zwischenspeicher steht.
func aeroRouteVormerken(cs string, lat, lon float64) {
	if e, ok := aeroRouten[cs]; ok && time.Now().Before(e.bis) {
		return
	}
	aeroRtMu.Lock()
	defer aeroRtMu.Unlock()
	if o := aeroRtOffen[cs]; o != nil {
		o.lat, o.lon = lat, lon
		return
	}
	if len(aeroRtOffen) < 500 {
		aeroRtOffen[cs] = &aeroOffen{lat: lat, lon: lon, verfehlt: map[string]bool{}}
	}
}

func aeroRouteFertig(cs string, r *aeroRoute) {
	aeroMu.Lock()
	if r != nil {
		aeroRouten[cs] = aeroEintrag{route: r, bis: time.Now().Add(12 * time.Hour)}
	} else {
		aeroRouten[cs] = aeroEintrag{bis: time.Now().Add(3 * time.Hour)} // alle Anbieter: nichts
	}
	aeroMu.Unlock()
	aeroRtMu.Lock()
	delete(aeroRtOffen, cs)
	aeroRtMu.Unlock()
}

func aeroRouteVerfehlt(cs, anbieter string) {
	aeroRtMu.Lock()
	o := aeroRtOffen[cs]
	alle := false
	if o != nil {
		o.verfehlt[anbieter] = true
		alle = o.verfehlt["adsb.lol"] && o.verfehlt["adsbdb"] && o.verfehlt["hexdb"]
	}
	aeroRtMu.Unlock()
	if alle {
		aeroRouteFertig(cs, nil)
	}
}

// ── Schleife ─────────────────────────────────────────────────────────────────

func (s *Server) aeroRoutenSchleife() {
	defer func() { _ = recover() }()
	for {
		time.Sleep(3 * time.Second)
		aeroRtMu.Lock()
		type auftrag struct {
			cs       string
			lat, lon float64
			verfehlt map[string]bool
		}
		var liste []auftrag
		for cs, o := range aeroRtOffen {
			v := map[string]bool{}
			for k, x := range o.verfehlt {
				v[k] = x
			}
			liste = append(liste, auftrag{cs, o.lat, o.lon, v})
		}
		aeroRtMu.Unlock()
		if len(liste) == 0 {
			continue
		}
		// 1. adsb.lol – bis zu 40 auf einmal
		if aeroAnbieterFrei("adsb.lol") {
			var stapel []map[string]any
			for _, a := range liste {
				if !a.verfehlt["adsb.lol"] && len(stapel) < 40 {
					stapel = append(stapel, map[string]any{"callsign": a.cs, "lat": a.lat, "lng": a.lon})
				}
			}
			if len(stapel) > 0 {
				s.aeroAdsbLol(stapel)
			}
		}
		// 2./3. Einzelanfragen für den Rest – je Runde höchstens drei, höflich verteilt
		einzeln := 0
		for _, a := range liste {
			if einzeln >= 3 {
				break
			}
			aeroRtMu.Lock()
			o := aeroRtOffen[a.cs]
			var v map[string]bool
			if o != nil {
				v = map[string]bool{"adsb.lol": o.verfehlt["adsb.lol"], "adsbdb": o.verfehlt["adsbdb"], "hexdb": o.verfehlt["hexdb"]}
			}
			aeroRtMu.Unlock()
			if o == nil || (!v["adsb.lol"] && aeroAnbieterFrei("adsb.lol")) {
				continue // erledigt oder adsb.lol kommt noch dran
			}
			switch {
			case !v["adsbdb"] && aeroAnbieterFrei("adsbdb"):
				s.aeroAdsbdb(a.cs)
				einzeln++
				time.Sleep(1500 * time.Millisecond)
			case !v["hexdb"] && aeroAnbieterFrei("hexdb"):
				s.aeroHexdb(a.cs)
				einzeln++
				time.Sleep(1500 * time.Millisecond)
			}
		}
	}
}

// ── Anbieter 1: adsb.lol (routeset) ──────────────────────────────────────────

func (s *Server) aeroAdsbLol(stapel []map[string]any) {
	rumpf, _ := json.Marshal(map[string]any{"planes": stapel})
	_, liste, _, ok := aeroHolen(http.MethodPost, "https://api.adsb.lol/api/0/routeset", rumpf)
	if !ok {
		aeroAnbieterFehler("adsb.lol")
		return
	}
	aeroAnbieterOK("adsb.lol")
	gefunden := map[string]bool{}
	for _, x := range liste {
		m := mapAus(x)
		if m == nil {
			continue
		}
		cs := strings.ToUpper(aeroText(m, "callsign"))
		hafen, _ := m["_airports"].([]any)
		if cs == "" || len(hafen) < 2 {
			continue
		}
		r := &aeroRoute{Quelle: "adsb.lol", Airline: aeroText(m, "airline_code")}
		for _, h := range hafen {
			hm := mapAus(h)
			if hm == nil {
				continue
			}
			f := &aeroFlughafen{Code: aeroText(hm, "iata"), Name: aeroText(hm, "name"),
				Stadt: aeroText(hm, "location"), Land: aeroText(hm, "countryiso2")}
			if f.Code == "" {
				f.Code = aeroText(hm, "icao")
			}
			f.Lat, _ = aeroZahl(hm, "lat")
			f.Lon, _ = aeroZahl(hm, "lon")
			r.Etappen = append(r.Etappen, f)
		}
		if len(r.Etappen) >= 2 {
			r.Von, r.Nach = r.Etappen[0], r.Etappen[len(r.Etappen)-1]
			gefunden[cs] = true
			aeroRouteFertig(cs, r)
		}
	}
	for _, p := range stapel {
		if cs, _ := p["callsign"].(string); !gefunden[cs] {
			aeroRouteVerfehlt(cs, "adsb.lol")
		}
	}
}

// ── Anbieter 2: adsbdb.com ───────────────────────────────────────────────────

func (s *Server) aeroAdsbdb(cs string) {
	d, _, code, ok := aeroHolen(http.MethodGet, "https://api.adsbdb.com/v0/callsign/"+cs, nil)
	if !ok {
		aeroAnbieterFehler("adsbdb")
		return
	}
	aeroAnbieterOK("adsbdb")
	if code == http.StatusOK {
		if fr := mapAus(mapAus(d["response"])["flightroute"]); fr != nil {
			von, nach := aeroFlughafenAus(mapAus(fr["origin"])), aeroFlughafenAus(mapAus(fr["destination"]))
			if von != nil && nach != nil {
				r := &aeroRoute{Von: von, Nach: nach, Quelle: "adsbdb", Etappen: []*aeroFlughafen{von, nach}}
				if al := mapAus(fr["airline"]); al != nil {
					r.Airline = aeroText(al, "name")
				}
				aeroRouteFertig(cs, r)
				return
			}
		}
	}
	aeroRouteVerfehlt(cs, "adsbdb")
}

// ── Anbieter 3: hexdb.io ─────────────────────────────────────────────────────

var (
	aeroHafenMu sync.Mutex
	aeroHafen   = map[string]*aeroFlughafen{} // ICAO → Flughafen (dauerhaft)
)

func aeroHexdbFlughafen(icao string) *aeroFlughafen {
	aeroHafenMu.Lock()
	if f, ok := aeroHafen[icao]; ok {
		aeroHafenMu.Unlock()
		return f
	}
	aeroHafenMu.Unlock()
	d, _, code, ok := aeroHolen(http.MethodGet, "https://hexdb.io/api/v1/airport/icao/"+icao, nil)
	f := &aeroFlughafen{Code: icao}
	if ok && code == http.StatusOK && d != nil {
		if iata := aeroText(d, "iata"); iata != "" {
			f.Code = iata
		}
		f.Name, f.Stadt, f.Land = aeroText(d, "airport"), aeroText(d, "region_name"), aeroText(d, "country_code")
		f.Lat, _ = aeroZahl(d, "latitude")
		f.Lon, _ = aeroZahl(d, "longitude")
	}
	if ok {
		aeroHafenMu.Lock()
		aeroHafen[icao] = f
		aeroHafenMu.Unlock()
	}
	return f
}

func (s *Server) aeroHexdb(cs string) {
	d, _, code, ok := aeroHolen(http.MethodGet, "https://hexdb.io/api/v1/route/icao/"+cs, nil)
	if !ok {
		aeroAnbieterFehler("hexdb")
		return
	}
	aeroAnbieterOK("hexdb")
	codes := strings.Split(aeroText(d, "route"), "-")
	if code != http.StatusOK || len(codes) < 2 {
		aeroRouteVerfehlt(cs, "hexdb")
		return
	}
	r := &aeroRoute{Quelle: "hexdb"}
	for _, c := range codes {
		if c = strings.TrimSpace(c); c != "" {
			r.Etappen = append(r.Etappen, aeroHexdbFlughafen(c))
		}
	}
	if len(r.Etappen) < 2 {
		aeroRouteVerfehlt(cs, "hexdb")
		return
	}
	r.Von, r.Nach = r.Etappen[0], r.Etappen[len(r.Etappen)-1]
	aeroRouteFertig(cs, r)
}

// ── Plausibilität ────────────────────────────────────────────────────────────

const erdR = 6371.0088

func aeroRad(g float64) float64 { return g * math.Pi / 180 }

// aeroPeilung: Anfangskurs von A nach B in Grad.
func aeroPeilung(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2, dl := aeroRad(lat1), aeroRad(lat2), aeroRad(lon2-lon1)
	y := math.Sin(dl) * math.Cos(p2)
	x := math.Cos(p1)*math.Sin(p2) - math.Sin(p1)*math.Cos(p2)*math.Cos(dl)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

func aeroWinkel(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// aeroEtappePasst: Abstand zur Großkreisstrecke A→B (km) und ob die Position
// zur Etappe passt. kurs < 0: unbekannt.
func aeroEtappePasst(a, b *aeroFlughafen, lat, lon, kurs float64) (float64, bool) {
	if a == nil || b == nil || (a.Lat == 0 && a.Lon == 0) || (b.Lat == 0 && b.Lon == 0) {
		return 0, false
	}
	dAB := haversineKm(a.Lat, a.Lon, b.Lat, b.Lon)
	dAP := haversineKm(a.Lat, a.Lon, lat, lon)
	dPB := haversineKm(lat, lon, b.Lat, b.Lon)
	t12, t13 := aeroRad(aeroPeilung(a.Lat, a.Lon, b.Lat, b.Lon)), aeroRad(aeroPeilung(a.Lat, a.Lon, lat, lon))
	quer := math.Abs(math.Asin(math.Sin(dAP/erdR)*math.Sin(t13-t12)) * erdR)
	laengs := math.Acos(math.Max(-1, math.Min(1, math.Cos(dAP/erdR)/math.Cos(quer/erdR)))) * erdR
	if math.Cos(t13-t12) < 0 {
		laengs = -laengs // hinter dem Start
	}
	// Flugwege weichen wegen der Luftraumstruktur oft deutlich vom Großkreis ab:
	// lieber großzügig, als richtige Routen als zweifelhaft zu markieren.
	grenze := math.Max(100, 0.20*dAB)
	ok := quer <= grenze && laengs >= -60 && laengs <= dAB+60
	// Weit weg von Start und Ziel muss der Kurs grob zum Ziel zeigen.
	if ok && kurs >= 0 && dAP > 40 && dPB > 40 {
		ok = aeroWinkel(kurs, aeroPeilung(lat, lon, b.Lat, b.Lon)) <= 100
	}
	return quer, ok
}

// aeroRoutePruefen: die zur Position passende Etappe; passt keine, die ganze
// Route mit zweifel=true. Ohne Koordinaten der Flughäfen: ungeprüft, ohne Zweifel.
func aeroRoutePruefen(r *aeroRoute, lat, lon float64, kursWert any) (*aeroRoute, bool) {
	kurs := -1.0
	if k, ok := kursWert.(float64); ok {
		kurs = k
	}
	if len(r.Etappen) < 2 {
		return r, false
	}
	koordinaten := true
	for _, f := range r.Etappen {
		if f == nil || (f.Lat == 0 && f.Lon == 0) {
			koordinaten = false
		}
	}
	if !koordinaten {
		return r, false
	}
	beste, besteQuer := -1, math.Inf(1)
	for i := 0; i+1 < len(r.Etappen); i++ {
		if q, ok := aeroEtappePasst(r.Etappen[i], r.Etappen[i+1], lat, lon, kurs); ok && q < besteQuer {
			beste, besteQuer = i, q
		}
	}
	if beste < 0 {
		return r, true
	}
	return &aeroRoute{Von: r.Etappen[beste], Nach: r.Etappen[beste+1], Airline: r.Airline, Quelle: r.Quelle}, false
}

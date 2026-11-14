package api

// Flugdaten aufzeichnen (R877, Echtzeit ab R878).
//
// Quelle: der SBS-Datenstrom von readsb (TCP 127.0.0.1:30003) – jede Meldung
// eines Flugzeugs kommt dort einzeln an und wird SOFORT mit dem Zeitpunkt
// ihres Eintreffens ausgewertet. Antwortet der Port nicht, liest die
// Aufzeichnung ersatzweise jede Sekunde das fertige Bild aus aeroLokal().
//
// Ablage:
//   <DataDir>/flugdaten/einstellungen.json
//   <DataDir>/flugdaten/tabellen/{tag-JJJJMMTT,woche-JJJJ-Www,monat-JJJJMM,jahr-JJJJ}.json[.gz]
//        Rekorde je Flugzeug, laufend gepflegt; das Quartett liest direkt daraus.
//        Abgeschlossene Zeiträume werden gepackt. Klein, bleiben dauerhaft.
//   <DataDir>/flugdaten/spuren/JJJJMMTT.jsonl.gz   (optional, für die Wiedergabe)
//        je Zeile [t (s, mit ms), hex, lat, lon, höhe_m (-1 Boden), km/h, kurs, m/min, km, rufzeichen]
//
// Plausibilitätsfilter: optional, alle Grenzen einstellbar.

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const flugZeitFormat = "20060102150405"

// flugWert: ein Rekord MIT Momentaufnahme (R881) – alle Werte vom selben
// Zeitpunkt: dem Stand des Flugzeugs im Augenblick des Rekords (je Feld der
// zuletzt empfangene Wert, im SBS-Strom meist < 1 s alt). Alte Tabellen ohne
// Momentaufnahme bleiben lesbar: dann sind nur V und T gesetzt.
type flugWert struct {
	V      float64  `json:"v"`
	T      string   `json:"t"` // JJJJMMTThhmmss, Ortszeit des Pi, Zeitpunkt des Eintreffens
	Hoehe  *float64 `json:"hoehe,omitempty"`  // m
	Boden  bool     `json:"boden,omitempty"`
	Geschw *float64 `json:"geschw,omitempty"` // km/h
	Steig  *float64 `json:"steig,omitempty"`  // m/s
	Km     *float64 `json:"km,omitempty"`
	Kurs   *float64 `json:"kurs,omitempty"` // Grad
	Lat    *float64 `json:"lat,omitempty"`
	Lon    *float64 `json:"lon,omitempty"`
	Squawk string   `json:"squawk,omitempty"`
	Flug   string   `json:"flug,omitempty"`
}

// flugOrt: der Knoten, der einen Datensatz aufgenommen hat, mit seinen Fundus-Koordinaten.
type flugOrt struct {
	Node string  `json:"node"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

type flugFoto struct {
	Datei  string `json:"datei"`
	Credit string `json:"credit"`
}

type flugSumme struct {
	Hex         string    `json:"hex"`
	Flug        []string  `json:"flug,omitempty"`
	Typ         string    `json:"typ,omitempty"`
	ICAO        string    `json:"icao,omitempty"`
	Hersteller  string    `json:"hersteller,omitempty"`
	Kennzeichen string    `json:"kennzeichen,omitempty"`
	Halter      string    `json:"halter,omitempty"`
	Route       string    `json:"route,omitempty"`
	Von         *aeroFlughafen `json:"von,omitempty"`  // R881: Ursprung
	Nach        *aeroFlughafen `json:"nach,omitempty"` // R881: Ziel
	Airline     string    `json:"airline,omitempty"`
	Land        string    `json:"land,omitempty"`      // R882: Land des Halters
	Betreiber   string    `json:"betreiber,omitempty"` // R882: ICAO-Betreibercode
	// Quellen je Angabe (R882): flugzeug (adsbdb/hexdb/readsb), route
	// (adsb.lol/adsbdb/hexdb), empfang (SBS-Datenstrom/aircraft.json · Pi).
	Quellen map[string]string `json:"quellen,omitempty"`
	Foto        *flugFoto `json:"foto,omitempty"` // R881: einmal gefunden, hier abgelegt
	KIFoto      *flugFoto `json:"ki_foto,omitempty"` // R885: KI-Bild getrennt vom Foto
	Aufnahme    *flugOrt  `json:"aufnahme,omitempty"` // R889: aufnehmender Knoten und seine Koordinaten
	Erste       string    `json:"erste"`
	Letzte      string    `json:"letzte"`
	Sicht       int       `json:"sicht"` // Zahl der Meldungen
	HoeheMax    *flugWert `json:"hoehe_max,omitempty"`
	HoeheMin    *flugWert `json:"hoehe_min,omitempty"`
	GeschwMax   *flugWert `json:"geschw_max,omitempty"`
	GeschwMin   *flugWert `json:"geschw_min,omitempty"`
	SteigMax    *flugWert `json:"steig_max,omitempty"` // m/s, positiv
	SinkMax     *flugWert `json:"sink_max,omitempty"`  // m/s, negativ
	KmMax       *flugWert `json:"km_max,omitempty"`
	KmMin       *flugWert `json:"km_min,omitempty"`
}

type flugFilter struct {
	An           bool    `json:"an"`
	MinMeldungen int     `json:"min_meldungen"` // erst ab so vielen Meldungen zählen
	MaxHoehe     float64 `json:"max_hoehe"`     // m
	MaxSprung    float64 `json:"max_sprung"`    // m Höhenänderung binnen 15 s
	MaxGeschw    float64 `json:"max_geschw"`    // km/h
	MaxSteig     float64 `json:"max_steig"`     // m/s (Betrag)
	MaxKm        float64 `json:"max_km"`
}

type flugEinstellungen struct {
	Aktiv        bool       `json:"aktiv"`         // Quartett-Tabellen pflegen
	Spuren       bool       `json:"spuren"`        // Spuren für die Wiedergabe
	SpurTakt     float64    `json:"spur_takt"`     // s; 0 = jede Meldung
	TageBehalten int        `json:"tage_behalten"` // Spuren
	TiefNurLuft  bool       `json:"tief_nur_luft"` // Tiefstwerte nur in der Luft
	Filter       flugFilter `json:"filter"`
}

func flugStandard() flugEinstellungen {
	// R879: Tabellen standardmäßig AN – sie sind klein, und ohne sie gibt es kein Quartett.
	return flugEinstellungen{Aktiv: true, Spuren: true, SpurTakt: 5, TageBehalten: 30, TiefNurLuft: true,
		Filter: flugFilter{An: true, MinMeldungen: 3, MaxHoehe: 15500, MaxSprung: 1500, MaxGeschw: 1400, MaxSteig: 50, MaxKm: 500}}
}

type flugZustand struct {
	flug, squawk               string
	hatGeschw, hatSteig, hatKurs bool
	hoehe, geschw, kurs, steig float64
	lat, lon, km               float64
	hatHoehe, hatPos, boden    bool
	meldungen                  int
	sprungHoehe                float64
	sprungZeit                 time.Time
	geschrieben, zuletzt       time.Time
	metaZeit                   time.Time
}

var (
	flugMu        sync.Mutex
	flugEinst     = flugStandard()
	flugEinstZeit time.Time
	flugTabellen  = map[string]map[string]*flugSumme{} // „tag-20261114“ → hex → Rekorde
	flugStand     = map[string]*flugZustand{}
	flugPuffer    []string
	flugSpurTag   string
	flugQuelle    = "keine"
	flugZaehler   int // Meldungen seit dem letzten Schreiben
	flugRate      float64
	flugOrtLat    float64
	flugOrtLon    float64
	flugNode      = aeroNodeName()
	reFlugTag     = regexp.MustCompile(`^[0-9]{8}$`)
	reFlugTabelle = regexp.MustCompile(`^(tag-[0-9]{8}|woche-[0-9]{4}-W[0-9]{2}|monat-[0-9]{6}|jahr-[0-9]{4})$`)
)

func (s *Server) flugOrdner(teil string) string { return filepath.Join(s.cfg.DataDir, "flugdaten", teil) }

func (s *Server) flugEinstellungenLesen() flugEinstellungen {
	e := flugStandard()
	if b, err := os.ReadFile(s.flugOrdner("einstellungen.json")); err == nil {
		_ = json.Unmarshal(b, &e)
	}
	if e.TageBehalten < 1 || e.TageBehalten > 3650 {
		e.TageBehalten = 30
	}
	if e.SpurTakt < 0 || e.SpurTakt > 60 {
		e.SpurTakt = 5
	}
	return e
}

// flugSchluessel: die vier Tabellen, in die ein Zeitpunkt fällt.
func flugSchluessel(t time.Time) [4]string {
	j, w := t.ISOWeek()
	return [4]string{"tag-" + t.Format("20060102"), fmt.Sprintf("woche-%04d-W%02d", j, w), "monat-" + t.Format("200601"), "jahr-" + t.Format("2006")}
}

func neuerWert(alt *flugWert, v float64, t string, groesser bool) *flugWert {
	if alt == nil || (groesser && v > alt.V) || (!groesser && v < alt.V) {
		return &flugWert{V: v, T: t}
	}
	return alt
}

// neuerMoment: wie neuerWert, aber mit Momentaufnahme – nur gebaut, wenn der
// Rekord tatsächlich besser ist.
func neuerMoment(alt *flugWert, v float64, groesser bool, moment func(v float64) *flugWert) *flugWert {
	if alt == nil || (groesser && v > alt.V) || (!groesser && v < alt.V) {
		return moment(v)
	}
	return alt
}

func zahlZeiger(v float64) *float64 { return &v }

// momentAus: Stand des Flugzeugs jetzt – alle Werte vom selben Augenblick.
func momentAus(z *flugZustand, ts string, ortBekannt bool) func(v float64) *flugWert {
	return func(v float64) *flugWert {
		m := &flugWert{V: v, T: ts, Boden: z.boden, Squawk: z.squawk, Flug: z.flug}
		if z.hatHoehe && !z.boden {
			m.Hoehe = zahlZeiger(z.hoehe)
		}
		if z.hatGeschw {
			m.Geschw = zahlZeiger(z.geschw)
		}
		if z.hatSteig {
			m.Steig = zahlZeiger(math.Round(z.steig*10) / 10)
		}
		if z.hatKurs {
			m.Kurs = zahlZeiger(math.Round(z.kurs))
		}
		if z.hatPos {
			m.Lat, m.Lon = zahlZeiger(math.Round(z.lat*1e5)/1e5), zahlZeiger(math.Round(z.lon*1e5)/1e5)
			if ortBekannt {
				m.Km = zahlZeiger(z.km)
			}
		}
		return m
	}
}

func enthaelt(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// flugEingang: EINE Meldung auswerten – aus dem SBS-Strom oder dem Ersatz.
// Felder, die die Meldung nicht enthält, sind nil. Aufruf unter flugMu.
func (s *Server) flugEingang(jetzt time.Time, hex, flug, squawk string, hoeheM *float64, boden *bool, geschwKmh, kurs, steigMs, lat, lon *float64) {
	e := flugEinst
	z := flugStand[hex]
	if z == nil {
		z = &flugZustand{}
		flugStand[hex] = z
	}
	z.meldungen++
	z.zuletzt = jetzt
	flugZaehler++
	if flug != "" {
		z.flug = flug
	}
	if squawk != "" {
		z.squawk = squawk
	}
	if boden != nil {
		z.boden = *boden
	}
	ts := jetzt.Format(flugZeitFormat)
	f := e.Filter
	zaehlt := !f.An || z.meldungen >= f.MinMeldungen
	inLuft := !z.boden

	// Höhe – Sprünge (Empfangsfehler) erkennen, wenn der Filter an ist.
	hoeheOK := false
	if hoeheM != nil && !z.boden {
		h := *hoeheM
		hoeheOK = !f.An || (h > 0 && h <= f.MaxHoehe)
		if hoeheOK && f.An && !z.sprungZeit.IsZero() && jetzt.Sub(z.sprungZeit) < 15*time.Second && math.Abs(h-z.sprungHoehe) > f.MaxSprung {
			hoeheOK = false
		}
		if hoeheOK {
			z.hoehe, z.hatHoehe, z.sprungHoehe, z.sprungZeit = h, true, h, jetzt
		}
	}
	geschwOK := geschwKmh != nil && (!f.An || (*geschwKmh >= 1 && *geschwKmh <= f.MaxGeschw))
	if geschwKmh != nil && (!f.An || geschwOK) {
		z.geschw, z.hatGeschw = *geschwKmh, true
	}
	if kurs != nil {
		z.kurs, z.hatKurs = *kurs, true
	}
	steigOK := steigMs != nil && !z.boden && (!f.An || math.Abs(*steigMs) <= f.MaxSteig)
	if steigMs != nil && (!f.An || steigOK || z.boden) {
		z.steig, z.hatSteig = *steigMs, true
	}
	posNeu, kmOK := false, false
	if lat != nil && lon != nil {
		z.lat, z.lon, z.hatPos, posNeu = *lat, *lon, true, true
		if flugOrtLat != 0 || flugOrtLon != 0 {
			z.km = math.Round(haversineKm(flugOrtLat, flugOrtLon, *lat, *lon)*100) / 100
			kmOK = !f.An || z.km <= f.MaxKm
		}
	}

	if e.Aktiv && zaehlt {
		moment := momentAus(z, ts, flugOrtLat != 0 || flugOrtLon != 0)
		for _, schl := range flugSchluessel(jetzt) {
			tab := flugTabellen[schl]
			if tab == nil {
				tab = s.flugTabelleLaden(schl)
				flugTabellen[schl] = tab
			}
			su := tab[hex]
			if su == nil {
				su = &flugSumme{Hex: hex, Erste: ts}
				tab[hex] = su
			}
			if su.Aufnahme == nil && (flugOrtLat != 0 || flugOrtLon != 0) {
				su.Aufnahme = &flugOrt{Node: flugNode, Lat: flugOrtLat, Lon: flugOrtLon}
			}
			su.Letzte, su.Sicht = ts, su.Sicht+1
			if z.flug != "" && len(su.Flug) < 3 && !enthaelt(su.Flug, z.flug) {
				su.Flug = append(su.Flug, z.flug)
			}
			if hoeheOK {
				su.HoeheMax = neuerMoment(su.HoeheMax, *hoeheM, true, moment)
				su.HoeheMin = neuerMoment(su.HoeheMin, *hoeheM, false, moment)
			}
			if geschwOK {
				su.GeschwMax = neuerMoment(su.GeschwMax, *geschwKmh, true, moment)
				if !e.TiefNurLuft || (inLuft && z.hatHoehe && z.hoehe > 0) {
					su.GeschwMin = neuerMoment(su.GeschwMin, *geschwKmh, false, moment)
				}
			}
			if steigOK {
				if v := math.Round(*steigMs*10) / 10; v > 0 {
					su.SteigMax = neuerMoment(su.SteigMax, v, true, moment)
				} else if v < 0 {
					su.SinkMax = neuerMoment(su.SinkMax, v, false, moment)
				}
			}
			if kmOK {
				su.KmMax = neuerMoment(su.KmMax, z.km, true, moment)
				if !e.TiefNurLuft || inLuft {
					su.KmMin = neuerMoment(su.KmMin, z.km, false, moment)
				}
			}
		}
	}
	// Spur für die Wiedergabe – bei jeder neuen Position oder im eingestellten Takt.
	if e.Spuren && posNeu && (e.SpurTakt == 0 || jetzt.Sub(z.geschrieben).Seconds() >= e.SpurTakt) {
		z.geschrieben = jetzt
		h := -1.0
		if !z.boden && z.hatHoehe {
			h = z.hoehe
		}
		tag := jetzt.Format("20060102")
		if flugSpurTag != "" && flugSpurTag != tag {
			s.flugSpurenSchreiben() // Tageswechsel: Rest in die alte Datei
		}
		flugSpurTag = tag
		zeile, _ := json.Marshal([]any{float64(jetzt.UnixMilli()) / 1000, hex, math.Round(z.lat*1e5) / 1e5, math.Round(z.lon*1e5) / 1e5,
			h, math.Round(z.geschw), math.Round(z.kurs), math.Round(z.steig * 60), z.km, z.flug})
		flugPuffer = append(flugPuffer, string(zeile))
	}
}

// ── Quelle 1: SBS-Datenstrom (Echtzeit) ──────────────────────────────────────

func sbsZahl(s string) *float64 {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// flugSBS: eine Zeile „MSG,typ,…“ – Felder nach dem BaseStation-Format:
// 4 hex, 10 Rufzeichen, 11 Höhe ft, 12 Geschw. kt, 13 Kurs, 14 lat, 15 lon,
// 16 Steigen ft/min, 21 am Boden (-1/0).
func (s *Server) flugSBS(jetzt time.Time, zeile string) {
	f := strings.Split(zeile, ",")
	if len(f) < 22 || f[0] != "MSG" {
		return
	}
	hex := strings.ToLower(strings.TrimSpace(f[4]))
	if hex == "" || strings.HasPrefix(hex, "~") {
		return
	}
	var hoehe, geschw, steig *float64
	if v := sbsZahl(f[11]); v != nil {
		m := math.Round(*v * 0.3048)
		hoehe = &m
	}
	if v := sbsZahl(f[12]); v != nil {
		k := math.Round(*v*1.852*10) / 10
		geschw = &k
	}
	if v := sbsZahl(f[16]); v != nil {
		ms := *v * 0.00508
		steig = &ms
	}
	var boden *bool
	switch strings.TrimSpace(f[21]) {
	case "-1", "1":
		b := true
		boden = &b
	case "0":
		b := false
		boden = &b
	}
	if f[1] == "2" { // Bodenposition
		b := true
		boden = &b
	}
	lat, lon := sbsZahl(f[14]), sbsZahl(f[15])
	if lat == nil || lon == nil {
		lat, lon = nil, nil
	}
	flugMu.Lock()
	s.flugEingang(jetzt, hex, strings.TrimSpace(f[10]), strings.TrimSpace(f[17]), hoehe, boden, geschw, sbsZahl(f[13]), steig, lat, lon)
	flugMu.Unlock()
}

func (s *Server) flugSBSLesen(adresse string) error {
	c, err := net.DialTimeout("tcp", adresse, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	flugMu.Lock()
	flugQuelle = "sbs"
	flugMu.Unlock()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 4096), 64*1024)
	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		if !sc.Scan() {
			return fmt.Errorf("Datenstrom beendet")
		}
		flugMu.Lock()
		an := flugEinst.Aktiv || flugEinst.Spuren
		flugMu.Unlock()
		if an {
			s.flugSBS(time.Now(), sc.Text())
		}
	}
}

// ── Quelle 2: Ersatz – das fertige Bild jede Sekunde ─────────────────────────

func (s *Server) flugErsatzTakt(jetzt time.Time) {
	bild := s.aeroLokal()
	if bild == nil {
		return
	}
	var d struct {
		Flugzeuge []map[string]any `json:"flugzeuge"`
	}
	if json.Unmarshal(bild, &d) != nil {
		return
	}
	zahl := func(m map[string]any, k string) *float64 {
		if v, ok := m[k].(float64); ok {
			return &v
		}
		return nil
	}
	flugMu.Lock()
	defer flugMu.Unlock()
	if flugQuelle == "sbs" {
		return
	}
	flugQuelle = "json"
	for _, f := range d.Flugzeuge {
		hex, _ := f["hex"].(string)
		if hex == "" {
			continue
		}
		fl, _ := f["flug"].(string)
		b, _ := f["am_boden"].(bool)
		var steig *float64
		if v := zahl(f, "steig_mmin"); v != nil {
			ms := *v / 60
			steig = &ms
		}
		sq, _ := f["squawk"].(string)
		s.flugEingang(jetzt, hex, fl, sq, zahl(f, "hoehe_m"), &b, zahl(f, "geschw_kmh"), zahl(f, "kurs"), steig, zahl(f, "lat"), zahl(f, "lon"))
	}
}

// ── Typ, Kennzeichen, Route nachtragen (alle 30 s je Flugzeug) ──────────────

func (s *Server) flugMetaNachtragen(jetzt time.Time) {
	type auftrag struct{ hex, flug string }
	var l []auftrag
	flugMu.Lock()
	for hex, z := range flugStand {
		if jetzt.Sub(z.metaZeit) >= 30*time.Second {
			z.metaZeit = jetzt
			l = append(l, auftrag{hex, z.flug})
		}
	}
	flugMu.Unlock()
	if len(l) == 0 {
		return
	}
	type meta struct {
		fz                     *aeroFlugzeug
		route, routeQuelle     string
		von, nach              *aeroFlughafen
		airline                string
	}
	m := map[string]meta{}
	aeroMu.Lock()
	for _, a := range l {
		var x meta
		if g, ok := aeroGeraete[a.hex]; ok && g.flugzeug != nil {
			x.fz = g.flugzeug
		} else {
			aeroVormerken("f", a.hex, aeroGeraete)
		}
		if r, ok := aeroRouten[a.flug]; ok && r.route != nil && r.route.Von != nil && r.route.Nach != nil {
			x.route = r.route.Von.Code + "–" + r.route.Nach.Code
			v, n := *r.route.Von, *r.route.Nach
			x.von, x.nach, x.airline, x.routeQuelle = &v, &n, r.route.Airline, r.route.Quelle
		}
		m[a.hex] = x
	}
	aeroMu.Unlock()
	empfang := map[string]string{"sbs": "SBS-Datenstrom", "json": "readsb aircraft.json"}
	flugMu.Lock()
	quelle := empfang[flugQuelle]
	for _, schl := range flugSchluessel(jetzt) {
		tab := flugTabellen[schl]
		for hex, x := range m {
			su := tab[hex]
			if su == nil {
				continue
			}
			if su.Quellen == nil {
				su.Quellen = map[string]string{}
			}
			if quelle != "" {
				su.Quellen["empfang"] = quelle + " · " + aeroNodeName()
			}
			if x.fz != nil {
				if x.fz.Land != "" {
					su.Land = x.fz.Land
				}
				if x.fz.Betreiber != "" {
					su.Betreiber = x.fz.Betreiber
				}
				if x.fz.Quelle != "" {
					su.Quellen["flugzeug"] = x.fz.Quelle
				}
			}
			if x.routeQuelle != "" && su.Quellen["route"] == "" {
				su.Quellen["route"] = x.routeQuelle
			}
			if x.fz != nil {
				for _, p := range [][2]*string{{&su.Typ, &x.fz.Typ}, {&su.ICAO, &x.fz.ICAO}, {&su.Hersteller, &x.fz.Hersteller},
					{&su.Kennzeichen, &x.fz.Kennzeichen}, {&su.Halter, &x.fz.Halter}} {
					if *p[1] != "" {
						*p[0] = *p[1]
					}
				}
			}
			// R883: auch Einträge, die schon eine Route als Codes haben, bekommen
			// die Flughäfen im Klartext – vorher nur, wenn noch gar keine Route da war.
			if x.von != nil && (su.Von == nil || su.Von.Name == "") {
				su.Route, su.Von, su.Nach = x.route, x.von, x.nach
			}
			if x.airline != "" && su.Airline == "" {
				su.Airline = x.airline
			}
		}
	}
	flugMu.Unlock()
}

// ── Schreiben, Laden, Aufräumen ──────────────────────────────────────────────

func jsonKompakt(pfad string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.WriteFile(pfad+".neu", b, 0o644); err != nil {
		return err
	}
	return os.Rename(pfad+".neu", pfad)
}

func (s *Server) flugSpurenSchreiben() {
	if len(flugPuffer) == 0 || flugSpurTag == "" {
		return
	}
	_ = os.MkdirAll(s.flugOrdner("spuren"), 0o755)
	if f, err := os.OpenFile(filepath.Join(s.flugOrdner("spuren"), flugSpurTag+".jsonl.gz"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		gz := gzip.NewWriter(f)
		_, _ = gz.Write([]byte(strings.Join(flugPuffer, "\n") + "\n"))
		_ = gz.Close()
		_ = f.Close()
	}
	flugPuffer = flugPuffer[:0]
}

// flugSchreiben: Spuren anhängen, Tabellen sichern; abgeschlossene Zeiträume
// packen und aus dem Speicher nehmen. Aufruf jede Minute (unter flugMu).
func (s *Server) flugSchreiben(jetzt time.Time) {
	s.flugSpurenSchreiben()
	_ = os.MkdirAll(s.flugOrdner("tabellen"), 0o755)
	aktuell := flugSchluessel(jetzt)
	for schl, tab := range flugTabellen {
		pfad := filepath.Join(s.flugOrdner("tabellen"), schl+".json")
		_ = jsonKompakt(pfad, tab)
		if !enthaelt(aktuell[:], schl) {
			if b, err := os.ReadFile(pfad); err == nil {
				if f, err := os.Create(pfad + ".gz.neu"); err == nil {
					gz := gzip.NewWriter(f)
					_, _ = gz.Write(b)
					_ = gz.Close()
					_ = f.Close()
					if os.Rename(pfad+".gz.neu", pfad+".gz") == nil {
						_ = os.Remove(pfad)
					}
				}
			}
			delete(flugTabellen, schl)
		}
	}
}

// flugTabelleLaden: laufend (.json) oder abgeschlossen (.json.gz).
func (s *Server) flugTabelleLaden(schl string) map[string]*flugSumme {
	m := map[string]*flugSumme{}
	pfad := filepath.Join(s.flugOrdner("tabellen"), schl+".json")
	if b, err := os.ReadFile(pfad); err == nil {
		_ = json.Unmarshal(b, &m)
		return m
	}
	if f, err := os.Open(pfad + ".gz"); err == nil {
		defer f.Close()
		if gz, err := gzip.NewReader(f); err == nil {
			_ = json.NewDecoder(gz).Decode(&m)
			_ = gz.Close()
		}
	}
	return m
}

// flugTabelle: Kopie einer Tabelle – laufend aus dem Speicher, sonst von der Karte.
func (s *Server) flugTabelle(schl string) map[string]*flugSumme {
	flugMu.Lock()
	if tab, ok := flugTabellen[schl]; ok {
		kopie := make(map[string]*flugSumme, len(tab))
		for k, v := range tab {
			x := *v
			x.Flug = append([]string(nil), v.Flug...)
			// Auch die Quellen kopieren: die Aufzeichnung schreibt weiter hinein,
			// während das Quartett die Kopie liest – geteilt hieße das Absturz.
			if v.Quellen != nil {
				x.Quellen = make(map[string]string, len(v.Quellen))
				for qk, qv := range v.Quellen {
					x.Quellen[qk] = qv
				}
			}
			kopie[k] = &x
		}
		flugMu.Unlock()
		return kopie
	}
	flugMu.Unlock()
	return s.flugTabelleLaden(schl)
}

func (s *Server) flugAufraeumen() {
	grenze := time.Now().AddDate(0, 0, -s.flugEinstellungenLesen().TageBehalten).Format("20060102")
	dateien, _ := filepath.Glob(filepath.Join(s.flugOrdner("spuren"), "*.jsonl.gz"))
	for _, d := range dateien {
		if tag := strings.TrimSuffix(filepath.Base(d), ".jsonl.gz"); reFlugTag.MatchString(tag) && tag < grenze {
			_ = os.Remove(d)
		}
	}
}

func (s *Server) flugStarten() {
	sicher := func(name string, f func()) {
		go func() {
			defer func() {
				if r := recover(); r != nil && s.log != nil {
					s.log.Error("Flugdaten: "+name+" abgebrochen", zap.Any("grund", r))
				}
			}()
			f()
		}()
	}
	// Verwaltung: Einstellungen, Standort, Ersatzquelle, Schreiben, Nachtragen, Aufräumen.
	sicher("Verwaltung", func() {
		takt := time.NewTicker(time.Second)
		defer takt.Stop()
		var schreiben, meta, aufraeumen time.Time
		for jetzt := range takt.C {
			flugMu.Lock()
			neuLesen := jetzt.Sub(flugEinstZeit) >= 5*time.Second
			flugMu.Unlock()
			if neuLesen {
				e := s.flugEinstellungenLesen()
				lat, lon := s.marktPosition()
				flugMu.Lock()
				flugEinst, flugEinstZeit, flugOrtLat, flugOrtLon, flugNode = e, jetzt, lat, lon, aeroNodeName()
				flugMu.Unlock()
			}
			flugMu.Lock()
			quelle, an := flugQuelle, flugEinst.Aktiv || flugEinst.Spuren
			flugMu.Unlock()
			if an && quelle != "sbs" {
				s.flugErsatzTakt(jetzt) // kein Datenstrom: jede Sekunde das Bild
			}
			if an && jetzt.Sub(meta) >= 10*time.Second {
				s.flugMetaNachtragen(jetzt)
				meta = jetzt
			}
			if jetzt.Sub(schreiben) >= time.Minute {
				flugMu.Lock()
				if !schreiben.IsZero() {
					flugRate = float64(flugZaehler) / jetzt.Sub(schreiben).Seconds()
				}
				flugZaehler = 0
				s.flugSchreiben(jetzt)
				for hex, z := range flugStand {
					if jetzt.Sub(z.zuletzt) > 10*time.Minute {
						delete(flugStand, hex)
					}
				}
				flugMu.Unlock()
				schreiben = jetzt
			}
			if jetzt.Sub(aufraeumen) >= 6*time.Hour {
				s.flugAufraeumen()
				aufraeumen = jetzt
			}
		}
	})
	// SBS-Datenstrom: verbinden, bei Abbruch nach 30 s erneut.
	sicher("SBS", func() {
		adresse := os.Getenv("FUNDUS_ADSB_SBS")
		if adresse == "" {
			adresse = "127.0.0.1:30003"
		}
		for {
			err := s.flugSBSLesen(adresse)
			flugMu.Lock()
			if flugQuelle == "sbs" {
				flugQuelle = "json"
				if s.log != nil {
					s.log.Info("Flugdaten: SBS-Datenstrom unterbrochen – Ersatz im Sekundentakt", zap.Error(err))
				}
			}
			flugMu.Unlock()
			time.Sleep(30 * time.Second)
		}
	})
}

// ── Schnittstellen ───────────────────────────────────────────────────────────

// GET /api/v1/aeronautik/aufzeichnung – Einstellungen, Zustand, vorhandene Daten
func (s *Server) flugUebersicht(c *gin.Context) {
	if !s.flugLokal() && s.flugWeiterleiten(c, flugFernAnfrage{Art: "uebersicht"}, 8*time.Second) {
		return // R879: vom Pi mit Empfänger
	}
	e := s.flugEinstellungenLesen()
	spuren, tabellen, belegt := []string{}, []string{}, int64(0)
	if l, _ := filepath.Glob(filepath.Join(s.flugOrdner("spuren"), "*.jsonl.gz")); l != nil {
		for _, d := range l {
			spuren = append(spuren, strings.TrimSuffix(filepath.Base(d), ".jsonl.gz"))
			if i, err := os.Stat(d); err == nil {
				belegt += i.Size()
			}
		}
	}
	if l, _ := filepath.Glob(filepath.Join(s.flugOrdner("tabellen"), "*.json*")); l != nil {
		for _, d := range l {
			t := strings.SplitN(filepath.Base(d), ".", 2)[0]
			if reFlugTabelle.MatchString(t) && !enthaelt(tabellen, t) {
				tabellen = append(tabellen, t)
			}
			if i, err := os.Stat(d); err == nil {
				belegt += i.Size()
			}
		}
	}
	sort.Strings(spuren)
	sort.Strings(tabellen)
	flugMu.Lock()
	quelle, rate, aktiv := flugQuelle, flugRate, len(flugStand)
	flugMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"einstellungen": e, "spuren": spuren, "tabellen": tabellen,
		"belegt_mb": math.Round(float64(belegt)/1e5) / 10, "stick": funkHatStick(),
		"quelle": quelle, "meldungen_s": math.Round(rate*10) / 10, "flugzeuge": aktiv,
		"nachtrag": flugNachtragZustand(), "ki_bilder": kiTypZustandLesen()})
}

// POST /api/v1/admin/aeronautik/aufzeichnung – flugEinstellungen
func (s *Server) flugEinstellen(c *gin.Context) {
	if !s.flugLokal() {
		c.JSON(http.StatusConflict, gin.H{"error": "Dieser Pi hat keinen Empfänger – die Aufzeichnung läuft auf dem Pi mit Stick. Dort einstellen (über dessen Adresse im Heimnetz)."})
		return
	}
	e := flugStandard()
	if c.ShouldBindJSON(&e) != nil || e.TageBehalten < 1 || e.TageBehalten > 3650 || e.SpurTakt < 0 || e.SpurTakt > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Werte (Aufbewahrung 1–3650 Tage, Takt 0–60 s)."})
		return
	}
	f := e.Filter
	if f.MinMeldungen < 1 || f.MinMeldungen > 100 || f.MaxHoehe <= 0 || f.MaxSprung <= 0 || f.MaxGeschw <= 0 || f.MaxSteig <= 0 || f.MaxKm <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Filtergrenzen müssen größer als 0 sein (Meldungen 1–100)."})
		return
	}
	_ = os.MkdirAll(s.flugOrdner(""), 0o755)
	if err := jsonAtomar(s.flugOrdner("einstellungen.json"), e, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	flugMu.Lock()
	flugEinstZeit = time.Time{} // sofort übernehmen
	flugMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /api/v1/aeronautik/aufzeichnung/spur?tag=JJJJMMTT&von=hhmm&bis=hhmm
func (s *Server) flugSpur(c *gin.Context) {
	if !isLANRequest(c) && !extRateAllow("flugspur:"+clientIP(c), 20) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Anfragen."})
		return
	}
	tag := c.Query("tag")
	if !s.flugLokal() && s.flugWeiterleiten(c, flugFernAnfrage{Art: "spur", Tag: tag, Von: c.Query("von"), Bis: c.Query("bis")}, 40*time.Second) {
		return // R879: vom Pi mit Empfänger
	}
	von, err1 := time.ParseInLocation("200601021504", tag+c.Query("von"), time.Local)
	bis, err2 := time.ParseInLocation("200601021504", tag+c.Query("bis"), time.Local)
	if !reFlugTag.MatchString(tag) || err1 != nil || err2 != nil || !bis.After(von) || bis.Sub(von) > 2*time.Hour {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tag=JJJJMMTT, von/bis=hhmm, höchstens 2 Stunden"})
		return
	}
	flugMu.Lock()
	if tag == flugSpurTag {
		s.flugSpurenSchreiben() // laufende Minute mitnehmen
	}
	flugMu.Unlock()
	f, err := os.Open(filepath.Join(s.flugOrdner("spuren"), tag+".jsonl.gz"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Für diesen Tag gibt es keine Spuren."})
		return
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Spurdatei beschädigt."})
		return
	}
	defer gz.Close()
	a, b := float64(von.Unix()), float64(bis.Unix())
	punkte := []json.RawMessage{}
	hexe := map[string]bool{}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		zeile := sc.Bytes()
		var kopf []json.RawMessage
		if json.Unmarshal(zeile, &kopf) != nil || len(kopf) < 2 {
			continue
		}
		var t float64
		var hex string
		if json.Unmarshal(kopf[0], &t) != nil || t < a || t > b || json.Unmarshal(kopf[1], &hex) != nil {
			continue
		}
		punkte = append(punkte, append(json.RawMessage{}, zeile...))
		hexe[hex] = true
		if len(punkte) > 600000 {
			break // Schutz: sehr volle Stunden bei „jede Meldung“
		}
	}
	tab := s.flugTabelle("tag-" + tag)
	angaben := gin.H{}
	for hex := range hexe {
		if su := tab[hex]; su != nil {
			angaben[hex] = gin.H{"typ": su.Typ, "icao": su.ICAO, "kennzeichen": su.Kennzeichen, "route": su.Route}
		}
	}
	lat, lon := s.marktPosition()
	c.JSON(http.StatusOK, gin.H{"punkte": punkte, "angaben": angaben, "standort": gin.H{"lat": lat, "lon": lon}, "node": aeroNodeName()})
}

func (s *Server) registerFlugdaten(r *gin.Engine) {
	r.GET("/api/v1/aeronautik/aufzeichnung", s.flugUebersicht)
	r.GET("/api/v1/aeronautik/aufzeichnung/spur", s.flugSpur)
	r.GET("/api/v1/aeronautik/quartett", s.flugQuartett)
	r.GET("/api/v1/aeronautik/quartett/typen", s.quartettTypen) // R887
	r.GET("/api/v1/aeronautik/typbild/:datei", s.typBildAusliefern)
	s.flugStarten()
	s.flugFernStarten() // R879: Anfragen von Pis ohne Empfänger beantworten
	go s.typBildHintergrund() // Fotos, die beim Quartett nicht rechtzeitig fertig wurden
	go s.kiTypBildArbeiter()  // R883: KI-Bilder für Typen ohne freies Foto
}

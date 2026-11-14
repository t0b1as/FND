package api

// Quartettwerte nachtragen (R880): Rekorde für einen gewählten Zeitraum
// (Tag, Woche, Monat, Jahr) aus schon Aufgezeichnetem in die Tabellen
// eintragen –
//   - aus den Spuren jedes Tages (neu berechnet mit den AKTUELLEN Filter-
//     einstellungen; so fein wie der Spurtakt der Aufzeichnung),
//   - aus den Tagesbilanzen von R877 (flugdaten/tage/), falls vorhanden.
// Jeder Tag fließt in alle vier Tabellen, zu denen er gehört. Bestehende
// Werte bleiben, wo sie besser sind – ein zweiter Lauf ändert nichts mehr.
// Läuft im Hintergrund; Fortschritt in der Übersicht.

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type flugNachtragStand struct {
	Laeuft  bool   `json:"laeuft"`
	Titel   string `json:"titel"`
	Tage    int    `json:"tage"`
	Fertig  int    `json:"fertig"`
	Punkte  int    `json:"punkte"`
	Bilanz  int    `json:"bilanzen"` // Tage aus R877-Tagesbilanzen
	Ohne    int    `json:"ohne"`     // Tage ohne Spuren und Bilanz
	Ende    int64  `json:"ende,omitempty"`
}

var (
	flugNachtragMu sync.Mutex
	flugNachtrag   flugNachtragStand
)

// flugVereinen: Rekorde q in ziel übernehmen, wo sie besser sind.
func flugVereinen(ziel, q *flugSumme) {
	besser := func(a, b *flugWert, groesser bool) *flugWert {
		switch {
		case a == nil:
			return b
		case b == nil:
			return a
		case groesser && b.V > a.V, !groesser && b.V < a.V:
			return b
		}
		return a
	}
	ziel.HoeheMax = besser(ziel.HoeheMax, q.HoeheMax, true)
	ziel.HoeheMin = besser(ziel.HoeheMin, q.HoeheMin, false)
	ziel.GeschwMax = besser(ziel.GeschwMax, q.GeschwMax, true)
	ziel.GeschwMin = besser(ziel.GeschwMin, q.GeschwMin, false)
	ziel.SteigMax = besser(ziel.SteigMax, q.SteigMax, true)
	ziel.SinkMax = besser(ziel.SinkMax, q.SinkMax, false)
	ziel.KmMax = besser(ziel.KmMax, q.KmMax, true)
	ziel.KmMin = besser(ziel.KmMin, q.KmMin, false)
	for _, fl := range q.Flug {
		if len(ziel.Flug) < 3 && !enthaelt(ziel.Flug, fl) {
			ziel.Flug = append(ziel.Flug, fl)
		}
	}
	for _, p := range [][2]*string{{&ziel.Typ, &q.Typ}, {&ziel.ICAO, &q.ICAO}, {&ziel.Hersteller, &q.Hersteller},
		{&ziel.Kennzeichen, &q.Kennzeichen}, {&ziel.Halter, &q.Halter}, {&ziel.Route, &q.Route}} {
		if *p[0] == "" {
			*p[0] = *p[1]
		}
	}
	if ziel.Von == nil && q.Von != nil {
		ziel.Von, ziel.Nach = q.Von, q.Nach
	}
	if ziel.Airline == "" {
		ziel.Airline = q.Airline
	}
	if ziel.Land == "" {
		ziel.Land = q.Land
	}
	if ziel.Betreiber == "" {
		ziel.Betreiber = q.Betreiber
	}
	for k, v := range q.Quellen {
		if ziel.Quellen == nil {
			ziel.Quellen = map[string]string{}
		}
		if ziel.Quellen[k] == "" {
			ziel.Quellen[k] = v
		}
	}
	if ziel.Foto == nil {
		ziel.Foto = q.Foto
	}
	if ziel.KIFoto == nil {
		ziel.KIFoto = q.KIFoto
	}
	if ziel.Erste == "" || (q.Erste != "" && q.Erste < ziel.Erste) {
		ziel.Erste = q.Erste
	}
	if q.Letzte > ziel.Letzte {
		ziel.Letzte = q.Letzte
	}
	if q.Sicht > ziel.Sicht { // nicht addieren – sonst zählt ein zweiter Lauf doppelt
		ziel.Sicht = q.Sicht
	}
}

// flugAusSpuren: Rekorde eines Tages aus seiner Spurdatei, mit den Filter-
// regeln der Echtzeit-Erfassung. Liefert auch die Zahl der Punkte.
func (s *Server) flugAusSpuren(tag string, e flugEinstellungen) (map[string]*flugSumme, int) {
	f, err := os.Open(filepath.Join(s.flugOrdner("spuren"), tag+".jsonl.gz"))
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, 0
	}
	defer gz.Close()
	type stand struct {
		meldungen   int
		sprungHoehe float64
		sprungZeit  float64
	}
	zustand := map[string]*stand{}
	ergebnis := map[string]*flugSumme{}
	fi := e.Filter
	punkte := 0
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var p []any
		if json.Unmarshal(sc.Bytes(), &p) != nil || len(p) < 10 {
			continue
		}
		t, _ := p[0].(float64)
		hex, _ := p[1].(string)
		hoehe, _ := p[4].(float64)
		geschw, _ := p[5].(float64)
		steigMin, _ := p[7].(float64)
		km, hatKm := p[8].(float64)
		flug, _ := p[9].(string)
		if hex == "" || t == 0 {
			continue
		}
		punkte++
		z := zustand[hex]
		if z == nil {
			z = &stand{}
			zustand[hex] = z
		}
		z.meldungen++
		if fi.An && z.meldungen < fi.MinMeldungen {
			continue
		}
		ts := time.Unix(int64(t), 0).Format(flugZeitFormat)
		su := ergebnis[hex]
		if su == nil {
			su = &flugSumme{Hex: hex, Erste: ts}
			ergebnis[hex] = su
		}
		su.Letzte, su.Sicht = ts, su.Sicht+1
		if flug != "" && len(su.Flug) < 3 && !enthaelt(su.Flug, flug) {
			su.Flug = append(su.Flug, flug)
		}
		boden := hoehe < 0
		lat, _ := p[2].(float64)
		lon, _ := p[3].(float64)
		kurs, _ := p[6].(float64)
		// Momentaufnahme: alle Werte dieses Spurpunkts stammen vom selben Augenblick.
		moment := func(v float64) *flugWert {
			m := &flugWert{V: v, T: ts, Boden: boden, Flug: flug, Kurs: zahlZeiger(kurs), Lat: zahlZeiger(lat), Lon: zahlZeiger(lon)}
			if !boden && hoehe > 0 {
				m.Hoehe = zahlZeiger(hoehe)
			}
			if geschw > 0 {
				m.Geschw = zahlZeiger(geschw)
			}
			m.Steig = zahlZeiger(math.Round(steigMin/60*10) / 10)
			if hatKm {
				m.Km = zahlZeiger(km)
			}
			return m
		}
		hoeheOK := !boden && (!fi.An || (hoehe > 0 && hoehe <= fi.MaxHoehe))
		if hoeheOK && fi.An && z.sprungZeit != 0 && t-z.sprungZeit < 15 && math.Abs(hoehe-z.sprungHoehe) > fi.MaxSprung {
			hoeheOK = false
		}
		if hoeheOK {
			z.sprungHoehe, z.sprungZeit = hoehe, t
			su.HoeheMax = neuerMoment(su.HoeheMax, hoehe, true, moment)
			su.HoeheMin = neuerMoment(su.HoeheMin, hoehe, false, moment)
		}
		if geschw > 0 && (!fi.An || (geschw >= 1 && geschw <= fi.MaxGeschw)) {
			su.GeschwMax = neuerMoment(su.GeschwMax, geschw, true, moment)
			if !e.TiefNurLuft || (!boden && hoehe > 0) {
				su.GeschwMin = neuerMoment(su.GeschwMin, geschw, false, moment)
			}
		}
		if ms := math.Round(steigMin/60*10) / 10; !boden && ms != 0 && (!fi.An || math.Abs(ms) <= fi.MaxSteig) {
			if ms > 0 {
				su.SteigMax = neuerMoment(su.SteigMax, ms, true, moment)
			} else {
				su.SinkMax = neuerMoment(su.SinkMax, ms, false, moment)
			}
		}
		if hatKm && km > 0 && (!fi.An || km <= fi.MaxKm) {
			su.KmMax = neuerMoment(su.KmMax, km, true, moment)
			if !e.TiefNurLuft || !boden {
				su.KmMin = neuerMoment(su.KmMin, km, false, moment)
			}
		}
	}
	return ergebnis, punkte
}

// flugTagEintragen: Rekorde eines Tages in alle vier Tabellen übernehmen.
func (s *Server) flugTagEintragen(tag time.Time, werte map[string]*flugSumme) {
	jetzt := flugSchluessel(time.Now())
	_ = os.MkdirAll(s.flugOrdner("tabellen"), 0o755)
	for _, schl := range flugSchluessel(tag) {
		flugMu.Lock()
		tab, imSpeicher := flugTabellen[schl]
		if !imSpeicher {
			tab = s.flugTabelleLaden(schl)
		}
		for hex, q := range werte {
			if ziel := tab[hex]; ziel != nil {
				flugVereinen(ziel, q)
			} else {
				x := *q
				x.Flug = append([]string(nil), q.Flug...)
				tab[hex] = &x
			}
		}
		if !imSpeicher {
			pfad := filepath.Join(s.flugOrdner("tabellen"), schl+".json")
			if enthaelt(jetzt[:], schl) {
				flugTabellen[schl] = tab // laufender Zeitraum: ab jetzt im Speicher weiterführen
				_ = jsonKompakt(pfad, tab)
			} else if b, err := json.Marshal(tab); err == nil {
				// abgeschlossener Zeitraum: gepackt ablegen
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
		}
		flugMu.Unlock()
	}
}

// flugZeitraum: erster und letzter Tag eines Zeitraums (höchstens bis heute).
func flugZeitraum(zeitraum string, tag time.Time) (time.Time, time.Time, string, bool) {
	tag = time.Date(tag.Year(), tag.Month(), tag.Day(), 0, 0, 0, 0, time.Local)
	var von, bis time.Time
	var titel string
	switch zeitraum {
	case "tag":
		von, bis, titel = tag, tag, tag.Format("02.01.2006")
	case "woche":
		wt := int(tag.Weekday()+6) % 7 // Montag = 0
		von = tag.AddDate(0, 0, -wt)
		bis = von.AddDate(0, 0, 6)
		j, w := tag.ISOWeek()
		titel = "KW " + itoa(w) + "/" + tag.Format("2006")
		_ = j
	case "monat":
		von = time.Date(tag.Year(), tag.Month(), 1, 0, 0, 0, 0, time.Local)
		bis = von.AddDate(0, 1, -1)
		titel = tag.Format("01/2006")
	case "jahr":
		von = time.Date(tag.Year(), 1, 1, 0, 0, 0, 0, time.Local)
		bis = time.Date(tag.Year(), 12, 31, 0, 0, 0, 0, time.Local)
		titel = tag.Format("2006")
	default:
		return von, bis, "", false
	}
	heute := time.Now()
	heute = time.Date(heute.Year(), heute.Month(), heute.Day(), 0, 0, 0, 0, time.Local)
	if bis.After(heute) {
		bis = heute
	}
	return von, bis, titel, !von.After(bis)
}

// POST /api/v1/admin/aeronautik/nachtragen {zeitraum, datum}
func (s *Server) flugNachtragenStarten(c *gin.Context) {
	if !s.flugLokal() {
		c.JSON(http.StatusConflict, gin.H{"error": "Dieser Pi hat keine Aufzeichnung – nachtragen auf dem Pi mit Stick."})
		return
	}
	var req struct{ Zeitraum, Datum string }
	_ = c.ShouldBindJSON(&req)
	tag, err := time.ParseInLocation("20060102", req.Datum, time.Local)
	if err != nil || !reFlugTag.MatchString(req.Datum) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "datum=JJJJMMTT"})
		return
	}
	von, bis, titel, ok := flugZeitraum(req.Zeitraum, tag)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Zeitraum liegt in der Zukunft oder ist ungültig."})
		return
	}
	flugNachtragMu.Lock()
	if flugNachtrag.Laeuft {
		flugNachtragMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "Es läuft bereits ein Nachtrag."})
		return
	}
	tage := int(bis.Sub(von).Hours()/24+0.5) + 1
	flugNachtrag = flugNachtragStand{Laeuft: true, Titel: titel, Tage: tage}
	flugNachtragMu.Unlock()
	e := s.flugEinstellungenLesen()
	go func() {
		defer func() {
			_ = recover()
			flugNachtragMu.Lock()
			flugNachtrag.Laeuft, flugNachtrag.Ende = false, time.Now().Unix()
			flugNachtragMu.Unlock()
		}()
		for d := von; !d.After(bis); d = d.AddDate(0, 0, 1) {
			tag := d.Format("20060102")
			werte, punkte := s.flugAusSpuren(tag, e)
			bilanz := false
			// Tagesbilanz aus R877, falls vorhanden.
			for _, pfad := range []string{filepath.Join(s.flugOrdner("tage"), tag+".json"), filepath.Join(s.flugOrdner("tage"), tag+".json.gz")} {
				alt := map[string]*flugSumme{}
				if b, err := os.ReadFile(pfad); err == nil && filepath.Ext(pfad) == ".json" {
					_ = json.Unmarshal(b, &alt)
				} else if f, err := os.Open(pfad); err == nil {
					if g, err := gzip.NewReader(f); err == nil {
						_ = json.NewDecoder(g).Decode(&alt)
						_ = g.Close()
					}
					_ = f.Close()
				}
				if len(alt) > 0 {
					bilanz = true
					if werte == nil {
						werte = map[string]*flugSumme{}
					}
					for hex, q := range alt {
						if ziel := werte[hex]; ziel != nil {
							flugVereinen(ziel, q)
						} else {
							werte[hex] = q
						}
					}
				}
			}
			if len(werte) > 0 {
				s.flugTagEintragen(d, werte)
			}
			flugNachtragMu.Lock()
			flugNachtrag.Fertig++
			flugNachtrag.Punkte += punkte
			if bilanz {
				flugNachtrag.Bilanz++
			}
			if len(werte) == 0 {
				flugNachtrag.Ohne++
			}
			flugNachtragMu.Unlock()
		}
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true, "tage": tage, "titel": titel})
}

func flugNachtragZustand() flugNachtragStand {
	flugNachtragMu.Lock()
	defer flugNachtragMu.Unlock()
	return flugNachtrag
}

package api

// Flugquartett (R877, Tabellen ab R878): 64 Karten aus den Rekorden eines
// Tages, einer Woche, eines Monats oder eines Jahres. Acht Kategorien – je Wert die Spitze und das Ende: Höhe, Geschwin-
// digkeit, Steigen/Sinken, Entfernung – mit je 8 Flugzeugen. Jedes Flugzeug
// höchstens einmal im Spiel; die Plätze werden REIHUM vergeben (jede Kategorie
// wählt abwechselnd ihren nächstbesten freien Kandidaten), damit nicht eine
// Kategorie alle guten Flugzeuge bekommt. Karte 1A … 16D.
//
// Die vier Spielwerte jeder Karte sind die Höchstwerte des Flugzeugs im
// Zeitraum (so sind alle Karten vergleichbar), Steigen/Sinken mit dem
// größeren Betrag; warum es im Spiel ist, steht als eigene Rekordzeile.
// Das PDF entsteht im Browser (static/quartett.js, jsPDF).

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type quartettKategorie struct {
	Schl, Titel, Einheit string
	Wert                 func(*flugSumme) *flugWert
	Groesser             bool
}

var quartettKategorien = []quartettKategorie{
	{"hoehe_max", "Höchste Höhe", "m", func(f *flugSumme) *flugWert { return f.HoeheMax }, true},
	{"hoehe_min", "Niedrigste Höhe", "m", func(f *flugSumme) *flugWert { return f.HoeheMin }, false},
	{"geschw_max", "Schnellstes", "km/h", func(f *flugSumme) *flugWert { return f.GeschwMax }, true},
	{"geschw_min", "Langsamstes", "km/h", func(f *flugSumme) *flugWert { return f.GeschwMin }, false},
	{"steig_max", "Stärkstes Steigen", "m/s", func(f *flugSumme) *flugWert { return f.SteigMax }, true},
	{"sink_max", "Stärkstes Sinken", "m/s", func(f *flugSumme) *flugWert { return f.SinkMax }, false},
	{"km_max", "Weiteste Entfernung", "km", func(f *flugSumme) *flugWert { return f.KmMax }, true},
	{"km_min", "Nächste Entfernung", "km", func(f *flugSumme) *flugWert { return f.KmMin }, false},
}





// GET /api/v1/aeronautik/quartett?zeitraum=tag|woche|monat|jahr&datum=JJJJMMTT[&fotos=1]
func (s *Server) flugQuartett(c *gin.Context) {
	if !isLANRequest(c) && !extRateAllow("quartett:"+clientIP(c), 6) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Anfragen."})
		return
	}
	zeitraum, datum := c.Query("zeitraum"), c.Query("datum")
	if !s.flugLokal() && s.flugWeiterleiten(c, flugFernAnfrage{Art: "quartett", Zeitraum: zeitraum, Datum: datum, Fotos: c.Query("fotos") == "1", KIBilder: c.Query("kibilder") == "1"}, 90*time.Second) {
		return // R879: vom Pi mit Empfänger
	}
	tag, err := time.ParseInLocation("20060102", datum, time.Local)
	if !reFlugTag.MatchString(datum) || err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "datum=JJJJMMTT"})
		return
	}
	schl := flugSchluessel(tag)
	monate := []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}
	var tabelle, titel, code string
	switch zeitraum {
	case "tag":
		tabelle, titel, code = schl[0], "Tag "+tag.Format("02.01.2006"), datum
	case "woche":
		j, w := tag.ISOWeek()
		tabelle, titel, code = schl[1], fmt.Sprintf("KW %d/%d", w, j), fmt.Sprintf("%04dW%02d", j, w)
	case "monat":
		tabelle, titel, code = schl[2], monate[tag.Month()-1]+" "+tag.Format("2006"), datum[:6]
	case "jahr":
		tabelle, titel, code = schl[3], "Jahr "+tag.Format("2006"), datum[:4]
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "zeitraum=tag, woche, monat oder jahr"})
		return
	}
	alle := s.flugTabelle(tabelle)
	if len(alle) == 0 {
		c.JSON(http.StatusOK, gin.H{"titel": titel, "code": code, "karten": []gin.H{}, "flugzeuge": 0,
			"hinweis": "Für diesen Zeitraum gibt es keine Quartettwerte – ist „Quartett-Tabellen pflegen“ eingeschaltet?"})
		return
	}
	// Kandidaten je Kategorie, beste zuerst.
	listen := make([][]*flugSumme, len(quartettKategorien))
	for k, kat := range quartettKategorien {
		for _, su := range alle {
			if kat.Wert(su) != nil {
				listen[k] = append(listen[k], su)
			}
		}
		kk := kat
		sort.Slice(listen[k], func(i, j int) bool {
			a, b := kk.Wert(listen[k][i]).V, kk.Wert(listen[k][j]).V
			if a == b {
				return listen[k][i].Hex < listen[k][j].Hex
			}
			if kk.Groesser {
				return a > b
			}
			return a < b
		})
	}
	// Reihum vergeben: jede Kategorie nimmt abwechselnd ihren nächstbesten freien Kandidaten.
	vergeben := map[string]bool{}
	gewaehlt := make([][]*flugSumme, len(quartettKategorien))
	pos := make([]int, len(quartettKategorien))
	for runde := 0; runde < 8; runde++ {
		for k := range quartettKategorien {
			for pos[k] < len(listen[k]) && vergeben[listen[k][pos[k]].Hex] {
				pos[k]++
			}
			if pos[k] < len(listen[k]) {
				su := listen[k][pos[k]]
				vergeben[su.Hex] = true
				gewaehlt[k] = append(gewaehlt[k], su)
			}
		}
	}
	// Typ und Kennzeichen nachschlagen, wo sie fehlen (höchstens 20 s warten).
	s.quartettNachschlagen(gewaehlt)
	// Fotos (Wikimedia Commons, frei lizenziert): zuerst aus dem Tabelleneintrag
	// – einmal gefunden, liegt es dort (R881). Sonst suchen, höchstens 35 s;
	// was dann fehlt, sucht der Node im Hintergrund weiter.
	mitFotos, mitKI := c.Query("fotos") == "1", c.Query("kibilder") == "1"
	aenderungen := map[string][]func(*flugSumme){} // je Flugzeug: was in die Tabelle soll
	merken := func(hex string, f func(*flugSumme)) { aenderungen[hex] = append(aenderungen[hex], f) }
	kiAusstehend := 0
	if mitFotos {
		frist := time.Now().Add(35 * time.Second)
		jeSuche := map[string]*typBild{}
		for _, kat := range gewaehlt {
			for _, su := range kat {
				if su.Foto != nil && su.Foto.Datei != "" {
					if _, err := os.Stat(filepath.Join(s.typBildOrdner(), su.Foto.Datei)); err == nil {
						continue // liegt schon in der Tabelle
					}
				}
				q := typBildSuche(su.Hersteller, su.Typ, su.ICAO)
				if q == "" {
					continue
				}
				b, ok := jeSuche[q]
				if !ok {
					b = s.typBildHolen(q, time.Now().Before(frist))
					jeSuche[q] = b
				}
				// R883: kein freies Foto (nachweislich) → KI-Bild, falls gewünscht.
				if b != nil && b.Datei == "" && mitKI {
					if ki := s.kiTypBildCache(q); ki != nil {
						b = ki
					} else {
						s.kiTypBildVormerken(kiTypAuftrag{Suche: q, Hersteller: su.Hersteller, Typ: su.Typ, ICAO: su.ICAO,
							Klasse: aeroKlasse(su.ICAO, su.Typ)})
						kiAusstehend++
					}
				}
				if b != nil && b.Datei != "" {
					foto := &flugFoto{Datei: b.Datei, Credit: b.Credit()}
					su.Foto = foto
					merken(su.Hex, func(z *flugSumme) {
						if z.Foto == nil || z.Foto.Datei == "" {
							z.Foto = foto
						}
					})
				}
			}
		}
	}
	// R883: Flughäfen im Klartext, wo nur Codes bekannt sind (höchstens 15 s).
	frist := time.Now().Add(15 * time.Second)
	for _, kat := range gewaehlt {
		for _, su := range kat {
			if (su.Von != nil && su.Von.Name != "") || su.Route == "" || time.Now().After(frist) {
				continue
			}
			codes := strings.Split(su.Route, "–")
			if len(codes) != 2 {
				continue
			}
			von, nach := aeroFlughafenNachCode(codes[0]), aeroFlughafenNachCode(codes[1])
			if von != nil && nach != nil && (von.Name != "" || nach.Name != "") {
				v, n := *von, *nach
				su.Von, su.Nach = &v, &n
				merken(su.Hex, func(z *flugSumme) {
					if z.Von == nil || z.Von.Name == "" {
						z.Von, z.Nach = &v, &n
					}
				})
			}
		}
	}
	if len(aenderungen) > 0 {
		s.flugEintraegeAendern(tabelle, aenderungen)
	}
	karten := []gin.H{}
	fehlend := 0
	for k, kat := range quartettKategorien {
		for r, su := range gewaehlt[k] {
			// Alle Werte der Karte stammen vom Augenblick des Rekords (R881).
			moment := kat.Wert(su)
			flug := moment.Flug
			if flug == "" && len(su.Flug) > 0 {
				flug = su.Flug[0]
			}
			karte := gin.H{
				"nr":        quartettNummer(k, r),
				"kategorie": kat.Schl, "kategorie_titel": kat.Titel, "einheit": kat.Einheit,
				"moment":    moment,
				"hex":       strings.ToUpper(su.Hex), "flug": flug, "typ": su.Typ, "icao": su.ICAO,
				"klasse":    aeroKlasse(su.ICAO, su.Typ), "hersteller": su.Hersteller,
				"kennzeichen": su.Kennzeichen, "halter": su.Halter, "airline": su.Airline,
				"route": su.Route, "von": su.Von, "nach": su.Nach,
				// R882: zu jedem Wert Minimum und Maximum mit Zeitstempel; bei
				// Steigen/Sinken die betragsmäßig größten in beide Richtungen.
				"werte": gin.H{
					"hoehe":  gin.H{"min": su.HoeheMin, "max": su.HoeheMax},
					"geschw": gin.H{"min": su.GeschwMin, "max": su.GeschwMax},
					"steig":  gin.H{"min": su.SinkMax, "max": su.SteigMax},
					"km":     gin.H{"min": su.KmMin, "max": su.KmMax},
				},
				"land": su.Land, "betreiber": su.Betreiber, "quellen": su.Quellen,
				"erste": su.Erste, "letzte": su.Letzte, "meldungen": su.Sicht,
			}
			if mitFotos {
				if su.Foto != nil && su.Foto.Datei != "" {
					karte["foto"] = gin.H{"url": "/api/v1/aeronautik/typbild/" + su.Foto.Datei, "credit": su.Foto.Credit}
				} else {
					fehlend++
				}
			}
			karten = append(karten, karte)
		}
	}
	c.JSON(http.StatusOK, gin.H{"titel": titel, "zeitraum": zeitraum, "code": code, "flugzeuge": len(alle),
		"karten": karten, "node": aeroNodeName(), "fotos_fehlend": fehlend, "ki_ausstehend": kiAusstehend})
}

// quartettNummer: Kategorie k (0–7), Rang r (0–7) → "1A" … "16D".
func quartettNummer(k, r int) string {
	return itoa(k*2+r/4+1) + string(rune('A'+r%4))
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func (s *Server) quartettNachschlagen(gewaehlt [][]*flugSumme) {
	fehlt := func() []*flugSumme {
		var l []*flugSumme
		aeroMu.Lock()
		defer aeroMu.Unlock()
		for _, kat := range gewaehlt {
			for _, su := range kat {
				if g, ok := aeroGeraete[su.Hex]; ok && g.flugzeug != nil {
					fz := g.flugzeug
					if su.Typ == "" {
						su.Typ = fz.Typ
					}
					if su.ICAO == "" {
						su.ICAO = fz.ICAO
					}
					if su.Kennzeichen == "" {
						su.Kennzeichen = fz.Kennzeichen
					}
					if su.Hersteller == "" {
						su.Hersteller = fz.Hersteller
					}
					if su.Halter == "" {
						su.Halter = fz.Halter
					}
				}
				if su.ICAO == "" && su.Typ == "" {
					l = append(l, su)
				}
			}
		}
		return l
	}
	offen := fehlt()
	if len(offen) == 0 {
		return
	}
	aeroMu.Lock()
	for _, su := range offen {
		aeroVormerken("f", su.Hex, aeroGeraete)
	}
	aeroMu.Unlock()
	frist := time.Now().Add(20 * time.Second)
	for len(offen) > 0 && time.Now().Before(frist) {
		time.Sleep(time.Second)
		offen = fehlt()
	}
}

// aeroKlasse: Bauart für die Silhouette auf der Karte – aus dem ICAO-
// Typcode, ersatzweise aus dem Typnamen.
func aeroKlasse(icao, typ string) string {
	c := strings.ToUpper(strings.TrimSpace(icao))
	t := strings.ToUpper(typ)
	hat := func(praefixe ...string) bool {
		for _, p := range praefixe {
			if strings.HasPrefix(c, p) {
				return true
			}
		}
		return false
	}
	switch {
	case c == "":
	case hat("A34", "A38", "B74", "A124", "IL96", "AN22", "AN12", "C5M", "E3TF", "E3CF", "K35R", "C135"):
		return "vierstrahler"
	case hat("A30", "A31", "A33", "A35", "B76", "B77", "B78", "MD11", "DC10", "A3ST", "BLCF", "KC2", "A400", "IL76", "C17"):
		if hat("A318", "A319", "A31N") {
			return "mittelstrecke"
		}
		if hat("A400") {
			return "turboprop"
		}
		return "grossraum"
	case hat("CRJ", "E135", "E145", "E35L", "F70", "F100", "B712", "MD8", "MD9", "DC9", "RJ", "B461", "B462", "B463"):
		return "heckjet"
	case hat("A2", "A19N", "A18N", "A32", "B73", "B37", "B38", "B39", "B3X", "B75", "BCS", "E17", "E19", "E29", "E75", "C919", "SU95", "A22"):
		return "mittelstrecke"
	case hat("AT4", "AT7", "DH8", "SF34", "B190", "D328", "JS41", "E120", "SW4", "L410", "DHC6", "DHC7", "C130", "AN26", "C295", "CN35", "P180", "BE20", "BE9", "C212"):
		return "turboprop"
	case hat("C25", "C5", "C6", "C7", "CL30", "CL35", "CL60", "GLF", "GL5T", "GL6T", "GL7T", "GLEX", "FA", "F2TH", "F900", "LJ", "E50P", "E55P", "E545", "E550", "PC24", "HDJT", "BE40", "H25", "PRM1", "SF50", "G150", "G280", "ASTR"):
		return "geschaeftsjet"
	case hat("EC", "H1", "H2", "AS3", "AS5", "AS6", "A109", "A119", "A139", "A169", "A189", "B06", "B407", "B412", "B429", "B505", "R22", "R44", "R66", "S76", "S92", "NH90", "EH10", "MI8", "MI17", "UH1", "H60", "H47", "CH47", "ALO3", "BK17", "EXPL", "TIGR", "LYNX"):
		return "hubschrauber"
	case hat("EUFI", "F16", "F15", "F18", "F35", "F22", "TOR", "RFAL", "MIR2", "GRIF", "A10", "T38", "HAWK", "AJET", "F5", "MG29", "SU2", "SU3", "M346", "L39", "PC21", "T6"):
		return "militaer"
	case hat("C1", "C2", "P28", "P32", "P46", "PA", "SR2", "DA4", "DA2", "DA5", "DR4", "BE3", "BE2", "M20", "TB", "EV97", "A210", "PIVI", "PC12", "C208", "TBM", "KODI", "G115", "D140", "RV", "AAT3", "ULAC", "GLID"):
		return "kleinflugzeug"
	}
	switch {
	case strings.Contains(t, "HELICOPTER") || strings.Contains(t, "HUBSCHRAUBER"):
		return "hubschrauber"
	case strings.Contains(t, "747") || strings.Contains(t, "A380") || strings.Contains(t, "A340"):
		return "vierstrahler"
	case strings.Contains(t, "777") || strings.Contains(t, "787") || strings.Contains(t, "A350") || strings.Contains(t, "A330") || strings.Contains(t, "767"):
		return "grossraum"
	case strings.Contains(t, "737") || strings.Contains(t, "A320") || strings.Contains(t, "A321") || strings.Contains(t, "A319"):
		return "mittelstrecke"
	case strings.Contains(t, "ATR") || strings.Contains(t, "DASH 8") || strings.Contains(t, "DHC-8"):
		return "turboprop"
	case strings.Contains(t, "CESSNA") || strings.Contains(t, "PIPER") || strings.Contains(t, "CIRRUS"):
		return "kleinflugzeug"
	}
	return "unbekannt"
}

// flugEintraegeAendern: Ergänzungen (Fotos, Flughäfen) in die Tabelle des
// Zeitraums schreiben – und in alle laufenden Tabellen, in denen das Flugzeug
// steht. So muss beim nächsten Mal nichts neu gesucht werden.
func (s *Server) flugEintraegeAendern(schl string, aenderungen map[string][]func(*flugSumme)) {
	flugMu.Lock()
	defer flugMu.Unlock()
	anwenden := func(tab map[string]*flugSumme) {
		for hex, fs := range aenderungen {
			if su := tab[hex]; su != nil {
				for _, f := range fs {
					f(su)
				}
			}
		}
	}
	for _, tab := range flugTabellen {
		anwenden(tab)
	}
	if _, imSpeicher := flugTabellen[schl]; imSpeicher {
		return // wird mit der nächsten Minute geschrieben
	}
	tab := s.flugTabelleLaden(schl)
	anwenden(tab)
	pfad := filepath.Join(s.flugOrdner("tabellen"), schl+".json")
	if _, err := os.Stat(pfad); err == nil {
		_ = jsonKompakt(pfad, tab)
		return
	}
	if b, err := json.Marshal(tab); err == nil {
		if f, err := os.Create(pfad + ".gz.neu"); err == nil {
			gz := gzip.NewWriter(f)
			_, _ = gz.Write(b)
			_ = gz.Close()
			_ = f.Close()
			_ = os.Rename(pfad+".gz.neu", pfad+".gz")
		}
	}
}

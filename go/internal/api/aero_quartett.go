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
	if !s.flugLokal() && s.flugWeiterleiten(c, flugFernAnfrage{Art: "quartett", Zeitraum: zeitraum, Datum: datum, Fotos: c.Query("fotos") == "1", KIBilder: c.Query("kibilder") == "1",
		Bilder: c.Query("bilder"), Bekannt: c.Query("bekannt") == "1"}, 90*time.Second) {
		return // R879: vom Pi mit Empfänger
	}
	tabelle, titel, code, ok := quartettZeitraum(zeitraum, datum)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "zeitraum=tag, woche, monat oder jahr; datum=JJJJMMTT"})
		return
	}
	alle := s.flugTabelle(tabelle)
	if len(alle) == 0 {
		c.JSON(http.StatusOK, gin.H{"titel": titel, "code": code, "karten": []gin.H{}, "flugzeuge": 0,
			"hinweis": "Für diesen Zeitraum gibt es keine Quartettwerte – ist „Quartett-Tabellen pflegen“ eingeschaltet?"})
		return
	}
	// R885: Typen erst aus dem Zwischenspeicher ergänzen – ein Typ, den der Node
	// längst kennt, soll nicht als „unbekannt“ herausfallen. Dann ggf. ausblenden.
	nurBekannte := c.Query("bekannt") == "1"
	typenErgaenzen(alle)
	ausgeblendet := 0
	if nurBekannte {
		for hex, su := range alle {
			if su.Typ == "" && su.ICAO == "" {
				delete(alle, hex)
				ausgeblendet++
			}
		}
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
	// Bilder (R885): fotos_ki = freie Fotos, sonst KI · fotos = nur freie Fotos ·
	// ki = alle von der KI · keine = Silhouetten. Foto und KI-Bild liegen
	// getrennt in der Tabelle; „alle von der KI“ überschreibt also kein Foto.
	modus := quartettBildModus(c)
	aenderungen := map[string][]func(*flugSumme){} // je Flugzeug: was in die Tabelle soll
	merken := func(hex string, f func(*flugSumme)) { aenderungen[hex] = append(aenderungen[hex], f) }
	kiAusstehend, kiOhneComfy := 0, false
	istKI := func(f *flugFoto) bool { return f != nil && strings.HasPrefix(f.Credit, "KI-erzeugt") }
	vorhanden := func(f *flugFoto) bool {
		if f == nil || f.Datei == "" {
			return false
		}
		_, err := os.Stat(filepath.Join(s.typBildOrdner(), f.Datei))
		return err == nil
	}
	bildFuer := map[string]*flugFoto{} // hex → Bild auf der Karte
	if modus != "keine" {
		frist := time.Now().Add(35 * time.Second)
		jeSuche := map[string]*typBild{}
		for _, kat := range gewaehlt {
			for _, su := range kat {
				// Altbestand (R883/R884): KI-Bild im Foto-Feld → ins KI-Feld.
				if istKI(su.Foto) {
					if su.KIFoto == nil {
						su.KIFoto = su.Foto
					}
					su.Foto = nil
					kf := su.KIFoto
					merken(su.Hex, func(z *flugSumme) {
						if istKI(z.Foto) {
							z.Foto = nil
						}
						if z.KIFoto == nil {
							z.KIFoto = kf
						}
					})
				}
				q := typBildSuche(su.Hersteller, su.Typ, su.ICAO)
				// Foto (außer bei „alle von der KI“)
				if modus != "ki" && !vorhanden(su.Foto) && q != "" {
					b, ok := jeSuche[q]
					if !ok {
						b = s.typBildHolen(q, time.Now().Before(frist))
						jeSuche[q] = b
					}
					if b != nil && b.Datei != "" && !b.KI {
						f := &flugFoto{Datei: b.Datei, Credit: b.Credit()}
						su.Foto = f
						merken(su.Hex, func(z *flugSumme) { z.Foto = f })
					}
				}
				if modus != "ki" && vorhanden(su.Foto) {
					bildFuer[su.Hex] = su.Foto
					continue
				}
				// KI-Bild (bei „Fotos, sonst KI“ und „alle von der KI“)
				if modus == "fotos" || q == "" {
					continue
				}
				if !vorhanden(su.KIFoto) {
					if ki := s.kiTypBildCache(q); ki != nil {
						f := &flugFoto{Datei: ki.Datei, Credit: ki.Credit()}
						su.KIFoto = f
						merken(su.Hex, func(z *flugSumme) { z.KIFoto = f })
					} else if s.kiBildLesen().aktiv() {
						s.kiTypBildVormerken(kiTypAuftrag{Suche: q, Hersteller: su.Hersteller, Typ: su.Typ, ICAO: su.ICAO,
							Klasse: aeroKlasse(su.ICAO, su.Typ)})
						kiAusstehend++
					} else {
						kiOhneComfy = true
					}
				}
				if vorhanden(su.KIFoto) {
					bildFuer[su.Hex] = su.KIFoto
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
			if modus != "keine" {
				if b := bildFuer[su.Hex]; b != nil {
					karte["foto"] = gin.H{"url": "/api/v1/aeronautik/typbild/" + b.Datei, "credit": b.Credit, "ki": istKI(b)}
				} else {
					fehlend++
				}
			}
			karten = append(karten, karte)
		}
	}
	c.JSON(http.StatusOK, gin.H{"titel": titel, "zeitraum": zeitraum, "code": code, "flugzeuge": len(alle),
		"karten": karten, "node": aeroNodeName(), "fotos_fehlend": fehlend, "ki_ausstehend": kiAusstehend, "ausgeblendet": ausgeblendet, "bilder": modus,
		"ki_hinweis": map[bool]string{true: "ComfyUI ist auf " + aeroNodeName() + " nicht eingerichtet (Einstellungen → KI → Bilderzeugung)."}[kiOhneComfy]})
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

// quartettBildModus: gewählte Bildquelle; ältere Aufrufe mit fotos=1/kibilder=1 gelten weiter.
func quartettBildModus(c *gin.Context) string {
	switch m := c.Query("bilder"); m {
	case "fotos_ki", "fotos", "ki", "keine":
		return m
	}
	if c.Query("fotos") == "1" {
		if c.Query("kibilder") == "1" {
			return "fotos_ki"
		}
		return "fotos"
	}
	return "keine"
}

// quartettZeitraum: Tabelle, Titel und Code eines Zeitraums – gemeinsam für
// Quartett und Typenliste.
func quartettZeitraum(zeitraum, datum string) (tabelle, titel, code string, ok bool) {
	tag, err := time.ParseInLocation("20060102", datum, time.Local)
	if !reFlugTag.MatchString(datum) || err != nil {
		return "", "", "", false
	}
	schl := flugSchluessel(tag)
	monate := []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}
	switch zeitraum {
	case "tag":
		return schl[0], "Tag " + tag.Format("02.01.2006"), datum, true
	case "woche":
		j, w := tag.ISOWeek()
		return schl[1], fmt.Sprintf("KW %d/%d", w, j), fmt.Sprintf("%04dW%02d", j, w), true
	case "monat":
		return schl[2], monate[tag.Month()-1] + " " + tag.Format("2006"), datum[:6], true
	case "jahr":
		return schl[3], "Jahr " + tag.Format("2006"), datum[:4], true
	}
	return "", "", "", false
}

// typenErgaenzen: Typen aus dem Zwischenspeicher nachtragen (vor Auswahl/Liste).
func typenErgaenzen(alle map[string]*flugSumme) {
	aeroMu.Lock()
	defer aeroMu.Unlock()
	for hex, su := range alle {
		if su.Typ == "" && su.ICAO == "" {
			if g, ok := aeroGeraete[hex]; ok && g.flugzeug != nil {
				su.Typ, su.ICAO = g.flugzeug.Typ, g.flugzeug.ICAO
				if su.Hersteller == "" {
					su.Hersteller = g.flugzeug.Hersteller
				}
				if su.Kennzeichen == "" {
					su.Kennzeichen = g.flugzeug.Kennzeichen
				}
			}
		}
	}
}

// GET /api/v1/aeronautik/quartett/typen?zeitraum&datum – Flugzeugtypen des
// Zeitraums mit Foto und KI-Bild, zum Durchsehen (R887). Je TYP, weil ein
// KI-Bild für alle Flugzeuge eines Typs gilt.
func (s *Server) quartettTypen(c *gin.Context) {
	zeitraum, datum := c.Query("zeitraum"), c.Query("datum")
	if !s.flugLokal() && s.flugWeiterleiten(c, flugFernAnfrage{Art: "typen", Zeitraum: zeitraum, Datum: datum}, 20*time.Second) {
		return
	}
	tabelle, titel, _, ok := quartettZeitraum(zeitraum, datum)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "zeitraum=tag, woche, monat oder jahr; datum=JJJJMMTT"})
		return
	}
	alle := s.flugTabelle(tabelle)
	typenErgaenzen(alle)
	type gruppe struct {
		suche, name, icao, klasse, hersteller, typ string
		anzahl                                     int
		foto                                       *flugFoto
	}
	jeTyp := map[string]*gruppe{}
	unbekannt := 0
	for _, su := range alle {
		q := typBildSuche(su.Hersteller, su.Typ, su.ICAO)
		if q == "" {
			unbekannt++
			continue
		}
		g := jeTyp[q]
		if g == nil {
			name := strings.TrimSpace(su.Hersteller + " " + su.Typ)
			if name == "" {
				name = su.ICAO
			}
			g = &gruppe{suche: q, name: name, icao: su.ICAO, klasse: aeroKlasse(su.ICAO, su.Typ), hersteller: su.Hersteller, typ: su.Typ}
			jeTyp[q] = g
		}
		g.anzahl++
		if g.foto == nil && su.Foto != nil && !strings.HasPrefix(su.Foto.Credit, "KI-erzeugt") {
			g.foto = su.Foto
		}
	}
	liste := make([]*gruppe, 0, len(jeTyp))
	for _, g := range jeTyp {
		liste = append(liste, g)
	}
	sort.Slice(liste, func(i, j int) bool {
		if liste[i].anzahl != liste[j].anzahl {
			return liste[i].anzahl > liste[j].anzahl
		}
		return liste[i].name < liste[j].name
	})
	offen := map[string]bool{}
	kiTypMu.Lock()
	for q := range kiTypInArbeit {
		offen[q] = true
	}
	kiTypMu.Unlock()
	typen := make([]gin.H, 0, len(liste))
	for _, g := range liste {
		e := gin.H{"suche": g.suche, "name": g.name, "icao": g.icao, "klasse": g.klasse, "anzahl": g.anzahl,
			"hersteller": g.hersteller, "typ": g.typ, "ki_offen": offen[g.suche]}
		foto := g.foto
		if foto == nil {
			if b := s.typBildCache(g.suche); b != nil && b.Datei != "" && !b.KI {
				foto = &flugFoto{Datei: b.Datei, Credit: b.Credit()}
			}
		}
		if foto != nil {
			e["foto"] = gin.H{"url": "/api/v1/aeronautik/typbild/" + foto.Datei, "credit": foto.Credit}
		}
		if ki := s.kiTypBildCache(g.suche); ki != nil {
			e["ki"] = gin.H{"url": "/api/v1/aeronautik/typbild/" + ki.Datei, "beschreibung": ki.Artikel, "ort": aeroNodeName()}
		}
		typen = append(typen, e)
	}
	c.JSON(http.StatusOK, gin.H{"titel": titel, "typen": typen, "unbekannt": unbekannt, "flugzeuge": len(alle), "node": aeroNodeName()})
}

// POST /api/v1/admin/aeronautik/kibild {suche, aktion: loeschen|neu, hersteller, typ, icao}
// Ein misslungenes KI-Bild löschen – und auf Wunsch neu malen lassen (R887).
func (s *Server) quartettKIBildAendern(c *gin.Context) {
	var req struct{ Suche, Aktion, Hersteller, Typ, ICAO string }
	if c.ShouldBindJSON(&req) != nil || req.Suche == "" || (req.Aktion != "loeschen" && req.Aktion != "neu") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	schl := s.kiTypSchluessel(req.Suche)
	datei := schl + ".png"
	geloescht := false
	for _, endung := range []string{".png", ".json"} {
		if os.Remove(filepath.Join(s.typBildOrdner(), schl+endung)) == nil {
			geloescht = true
		}
	}
	if !geloescht && req.Aktion == "loeschen" && !s.flugLokal() {
		c.JSON(http.StatusConflict, gin.H{"error": "Dieses KI-Bild liegt auf dem Empfänger – dort löschen (über dessen Adresse im Heimnetz)."})
		return
	}
	// Verweise in den laufenden Tabellen lösen; in abgeschlossenen Tabellen
	// zeigt der Verweis ins Leere und gilt damit als „kein Bild“.
	flugMu.Lock()
	for _, tab := range flugTabellen {
		for _, su := range tab {
			if su.KIFoto != nil && su.KIFoto.Datei == datei {
				su.KIFoto = nil
			}
			if su.Foto != nil && su.Foto.Datei == datei {
				su.Foto = nil
			}
		}
	}
	flugMu.Unlock()
	if req.Aktion == "neu" {
		if !s.kiBildLesen().aktiv() {
			c.JSON(http.StatusConflict, gin.H{"error": "ComfyUI ist auf " + aeroNodeName() + " nicht eingerichtet."})
			return
		}
		s.kiTypBildVormerken(kiTypAuftrag{Suche: req.Suche, Hersteller: req.Hersteller, Typ: req.Typ, ICAO: req.ICAO,
			Klasse: aeroKlasse(req.ICAO, req.Typ)})
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "geloescht": geloescht})
}

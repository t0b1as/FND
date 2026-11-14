package api

// Fotos der Flugzeugtypen für das Flugquartett (R878) – von Wikimedia Commons,
// NUR frei lizenziert (CC0, gemeinfrei, CC BY, CC BY-SA), mit Urheberangabe
// auf der Karte, wie es diese Lizenzen verlangen. Bewusst keine Bildersuche
// im allgemeinen Netz: Solche Bilder sind fast immer geschützt.
//
// Weg: Wikipedia-Suche nach dem Typ → Titelbild des besten Artikels (die
// Schnittstelle liefert dabei nur freie Bilder: pilicense=free) → Commons:
// Lizenz, Urheber, verkleinerte Fassung (800 px). Abgelegt unter
// <DataDir>/flugdaten/typbilder/<schlüssel>.{json,jpg,png}; ohne Treffer
// wird nach 14 Tagen erneut gesucht.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type typBild struct {
	Datei     string `json:"datei"` // leer = kein freies Foto gefunden
	Autor     string `json:"autor,omitempty"`
	Lizenz    string `json:"lizenz,omitempty"`
	LizenzURL string `json:"lizenz_url,omitempty"`
	Quelle    string `json:"quelle,omitempty"`
	Artikel   string `json:"artikel,omitempty"`
	Stand     int64  `json:"stand"`
	KI        bool   `json:"ki,omitempty"` // R883: von der KI erzeugt, kein Foto
	Manuell   bool   `json:"manuell,omitempty"` // R891: von Hand gewählt (oder gesperrt) – läuft nicht ab
}

// Credit: Urheberangabe für die Karte.
func (b *typBild) Credit() string {
	if b.KI {
		// Kennzeichnung vorn und kurz – auf der Karte darf „kein Foto“ nie wegfallen.
		modell := strings.TrimSuffix(b.Lizenz, filepath.Ext(b.Lizenz))
		if len(modell) > 18 {
			modell = modell[:18]
		}
		return "KI-erzeugt, kein Foto · " + modell + " · ComfyUI"
	}
	a := b.Autor
	if a == "" {
		a = "unbekannt"
	}
	return "Foto: " + kiKuerzen(a, 48) + " · " + b.Lizenz + " · Wikimedia Commons"
}

var (
	typBildMu       sync.Mutex
	typBildWarte    = make(chan string, 300)
	typBildInArbeit = map[string]bool{}
	reFreieLizenz   = regexp.MustCompile(`(?i)^(cc0( 1\.0)?|public domain|pd[- a-z0-9.]*|cc[ -]by(-sa)?([ -][0-9.]+)?( [a-z-]+)?)$`)
	reTypBildDatei  = regexp.MustCompile(`^[0-9a-f]{16}(-ki)?\.(jpg|png)$`)
	typBildClient   = &http.Client{Timeout: 20 * time.Second}
)

var typHersteller = []struct{ muster, name string }{
	{"AIRBUS HELI", "Airbus Helicopters"}, {"EUROCOPTER", "Eurocopter"}, {"AIRBUS", "Airbus"}, {"BOEING", "Boeing"},
	{"EMBRAER", "Embraer"}, {"BOMBARDIER", "Bombardier"}, {"CANADAIR", "Bombardier"}, {"CESSNA", "Cessna"}, {"TEXTRON", "Cessna"},
	{"PIPER", "Piper"}, {"BEECH", "Beechcraft"}, {"HAWKER", "Hawker"}, {"DASSAULT", "Dassault"}, {"GULFSTREAM", "Gulfstream"},
	{"PILATUS", "Pilatus"}, {"HAVILLAND", "De Havilland"}, {"AVIONS DE TRANSPORT", "ATR"}, {"ATR", "ATR"},
	{"ROBINSON", "Robinson"}, {"DIAMOND", "Diamond"}, {"CIRRUS", "Cirrus"}, {"AGUSTA", "AgustaWestland"}, {"LEONARDO", "Leonardo"},
	{"SAAB", "Saab"}, {"FOKKER", "Fokker"}, {"MCDONNELL", "McDonnell Douglas"}, {"LOCKHEED", "Lockheed"}, {"EUROFIGHTER", "Eurofighter"},
	{"BELL", "Bell"}, {"SIKORSKY", "Sikorsky"}, {"MOONEY", "Mooney"}, {"SOCATA", "Socata"}, {"DAHER", "Daher"}, {"HONDA", "Honda"},
	{"BRITISH AEROSPACE", "BAe"}, {"ANTONOV", "Antonov"}, {"SUKHOI", "Sukhoi"}, {"COMAC", "Comac"},
}

// typBildSuche: Suchbegriff aus Hersteller und Typ, etwa „Airbus A350“.
func typBildSuche(hersteller, typ, icao string) string {
	h := strings.ToUpper(hersteller + " " + typ)
	marke := ""
	for _, x := range typHersteller {
		if strings.Contains(h, x.muster) {
			marke = x.name
			break
		}
	}
	if f := strings.Fields(hersteller); marke == "" && len(f) > 0 {
		w := f[0]
		marke = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	modell := ""
	if f := strings.Fields(typ); len(f) > 0 {
		modell = f[0]
		rest := f[1:]
		if strings.EqualFold(modell, marke) && len(f) > 1 {
			modell, rest = f[1], f[2:]
		}
		// „CRJ 900“, „Dash 8“: reine Buchstaben + Zahl gehören zusammen.
		if len(rest) > 0 && strings.Trim(strings.ToUpper(modell), "ABCDEFGHIJKLMNOPQRSTUVWXYZ-") == "" &&
			rest[0] != "" && rest[0][0] >= '0' && rest[0][0] <= '9' {
			modell += " " + rest[0]
		}
	}
	if modell == "" {
		modell = icao
	}
	if modell == "" {
		return ""
	}
	return strings.TrimSpace(marke + " " + modell)
}

func (s *Server) typBildOrdner() string { return s.flugOrdner("typbilder") }

func typBildSchluessel(q string) string {
	h := sha1.Sum([]byte(strings.ToLower(q)))
	return hex.EncodeToString(h[:8])
}

func (s *Server) typBildCache(q string) *typBild {
	b, err := os.ReadFile(filepath.Join(s.typBildOrdner(), typBildSchluessel(q)+".json"))
	if err != nil {
		return nil
	}
	var t typBild
	if json.Unmarshal(b, &t) != nil {
		return nil
	}
	if t.Datei == "" && !t.Manuell && time.Since(time.Unix(t.Stand, 0)) > 14*24*time.Hour {
		return nil // damals nichts gefunden – erneut versuchen
	}
	return &t
}

// typBildHolen: aus dem Zwischenspeicher; sonst sofort suchen (jetzt=true)
// oder für den Hintergrund vormerken.
func (s *Server) typBildHolen(q string, jetzt bool) *typBild {
	if t := s.typBildCache(q); t != nil {
		return t
	}
	if !jetzt {
		s.typBildVormerken(q)
		return nil
	}
	t, err := s.typBildSuchen(q)
	if err != nil {
		s.typBildVormerken(q) // Netz gestört: später erneut
		return nil
	}
	return t
}

func (s *Server) typBildVormerken(q string) {
	typBildMu.Lock()
	defer typBildMu.Unlock()
	if typBildInArbeit[q] {
		return
	}
	select {
	case typBildWarte <- q:
		typBildInArbeit[q] = true
	default:
	}
}

func (s *Server) typBildHintergrund() {
	for q := range typBildWarte {
		if s.typBildCache(q) == nil {
			_, _ = s.typBildSuchen(q)
		}
		typBildMu.Lock()
		delete(typBildInArbeit, q)
		typBildMu.Unlock()
		time.Sleep(2 * time.Second)
	}
}

func typBildAbfrage(adresse string, ziel any) error {
	req, _ := http.NewRequest(http.MethodGet, adresse, nil)
	// Wikimedia verlangt eine aussagekräftige Kennung.
	req.Header.Set("User-Agent", "Fundus-Aeronautik/1.0 (https://github.com/t0b1as/FND; Flugquartett)")
	resp, err := typBildClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(ziel)
}

var reHTMLTags = regexp.MustCompile(`<[^>]*>`)

// typBildSuchen: Wikipedia → Commons → herunterladen und ablegen.
// Ein Ergebnis ohne Foto wird ebenfalls abgelegt (Datei leer).
func (s *Server) typBildSuchen(q string) (*typBild, error) {
	_ = os.MkdirAll(s.typBildOrdner(), 0o755)
	schl := typBildSchluessel(q)
	ergebnis := &typBild{Stand: time.Now().Unix()}
	ablegen := func() (*typBild, error) {
		_ = jsonAtomar(filepath.Join(s.typBildOrdner(), schl+".json"), ergebnis, 0o644)
		return ergebnis, nil
	}
	var such struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				Index     int    `json:"index"`
				PageImage string `json:"pageimage"`
			} `json:"pages"`
		} `json:"query"`
	}
	v := url.Values{"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "generator": {"search"},
		"gsrsearch": {q + " aircraft"}, "gsrnamespace": {"0"}, "gsrlimit": {"5"}, "prop": {"pageimages"},
		"piprop": {"name"}, "pilicense": {"free"}}
	if err := typBildAbfrage("https://en.wikipedia.org/w/api.php?"+v.Encode(), &such); err != nil {
		return nil, err
	}
	datei, artikel, beste := "", "", 1<<30
	for _, p := range such.Query.Pages {
		// R891: Der Artikel muss vom Typ handeln – sonst landete etwa ein
		// Flugzeugträger als „Foto“ auf der Karte.
		if p.PageImage != "" && p.Index < beste && typTitelPasst(q, p.Title) {
			datei, artikel, beste = p.PageImage, p.Title, p.Index
		}
	}
	if datei == "" {
		return ablegen()
	}
	time.Sleep(time.Second)
	var info struct {
		Query struct {
			Pages []struct {
				Missing   bool `json:"missing"`
				ImageInfo []struct {
					ThumbURL       string `json:"thumburl"`
					DescriptionURL string `json:"descriptionurl"`
					ExtMetadata    map[string]struct {
						Value any `json:"value"`
					} `json:"extmetadata"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	v = url.Values{"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "titles": {"File:" + datei},
		"prop": {"imageinfo"}, "iiprop": {"url|extmetadata"}, "iiurlwidth": {"800"}}
	if err := typBildAbfrage("https://commons.wikimedia.org/w/api.php?"+v.Encode(), &info); err != nil {
		return nil, err
	}
	if len(info.Query.Pages) == 0 || info.Query.Pages[0].Missing || len(info.Query.Pages[0].ImageInfo) == 0 {
		return ablegen() // nicht auf Commons – etwa ein unfreies Bild der Wikipedia
	}
	ii := info.Query.Pages[0].ImageInfo[0]
	meta := func(k string) string {
		if m, ok := ii.ExtMetadata[k]; ok {
			if t, ok := m.Value.(string); ok {
				return strings.TrimSpace(html.UnescapeString(reHTMLTags.ReplaceAllString(t, "")))
			}
		}
		return ""
	}
	lizenz := meta("LicenseShortName")
	if !reFreieLizenz.MatchString(lizenz) || ii.ThumbURL == "" {
		return ablegen()
	}
	time.Sleep(time.Second)
	req, _ := http.NewRequest(http.MethodGet, ii.ThumbURL, nil)
	req.Header.Set("User-Agent", "Fundus-Aeronautik/1.0 (https://github.com/t0b1as/FND; Flugquartett)")
	resp, err := typBildClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	endung := map[string]string{"image/jpeg": "jpg", "image/png": "png"}[strings.Split(resp.Header.Get("Content-Type"), ";")[0]]
	daten, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode != http.StatusOK || endung == "" {
		return ablegen()
	}
	if err := os.WriteFile(filepath.Join(s.typBildOrdner(), schl+"."+endung), daten, 0o644); err != nil {
		return nil, err
	}
	ergebnis.Datei = schl + "." + endung
	ergebnis.Autor = strings.Join(strings.Fields(meta("Artist")), " ")
	ergebnis.Lizenz, ergebnis.LizenzURL = lizenz, meta("LicenseUrl")
	ergebnis.Quelle, ergebnis.Artikel = ii.DescriptionURL, artikel
	return ablegen()
}

// GET /api/v1/aeronautik/typbild/:datei
func (s *Server) typBildAusliefern(c *gin.Context) {
	d := c.Param("datei")
	if !reTypBildDatei.MatchString(d) {
		c.Status(http.StatusNotFound)
		return
	}
	pfad := filepath.Join(s.typBildOrdner(), d)
	// Nur ein Pi OHNE eigene Aufzeichnung fragt weiter – sonst könnten sich zwei
	// Empfänger, denen dasselbe Foto fehlt, gegenseitig im Kreis fragen.
	if _, err := os.Stat(pfad); err != nil && !s.flugLokal() && s.flugWeiterleiten(c, flugFernAnfrage{Art: "typbild", Datei: d}, 20*time.Second) {
		return // R879: Foto liegt beim Pi mit Empfänger – geholt und hier abgelegt
	}
	c.Header("Cache-Control", "public, max-age=604800")
	c.File(pfad)
}

var (
	reModellZahl     = regexp.MustCompile(`[0-9]{2,4}`)
	reModellVorsilbe = regexp.MustCompile(`^[A-Z]{2,}`)
	reKeinFlieger = regexp.MustCompile(`(?i)\b(USS|HMS|HMCS|INS|FS|carrier|ship|class|squadron|wing|air base|airport|airline)\b`)
)

// typTitelPasst (R891): Handelt der Wikipedia-Artikel vom Typ? Der Titel muss
// den Modellnamen enthalten (oder dessen Zahl, etwa „18“ aus F/A-18E) und
// darf kein Schiff, Geschwader, Flughafen oder keine Airline sein.
func typTitelPasst(suche, titel string) bool {
	if reKeinFlieger.MatchString(titel) {
		return false
	}
	f := strings.Fields(suche)
	if len(f) < 2 {
		return true
	}
	modell := strings.ToUpper(f[len(f)-1])
	t := strings.ToUpper(titel)
	if strings.Contains(t, modell) {
		return true
	}
	if z := reModellZahl.FindString(modell); z != "" && strings.Contains(t, z) {
		return true
	}
	// Vorsilbe aus mindestens zwei Buchstaben (CRJ, ATR, EC) zählt ebenfalls
	vors := reModellVorsilbe.FindString(modell)
	return vors != "" && strings.Contains(t, vors)
}

// typBildInfos: Lizenz, Urheber und Vorschau zu Commons-Dateien (nur freie).
func typBildInfos(dateien []string, breite int) ([]map[string]any, error) {
	if len(dateien) == 0 {
		return nil, nil
	}
	titel := make([]string, 0, len(dateien))
	for _, d := range dateien {
		titel = append(titel, "File:"+strings.TrimPrefix(d, "File:"))
	}
	var info struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				Missing   bool   `json:"missing"`
				ImageInfo []struct {
					ThumbURL       string `json:"thumburl"`
					DescriptionURL string `json:"descriptionurl"`
					ExtMetadata    map[string]struct {
						Value any `json:"value"`
					} `json:"extmetadata"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	v := url.Values{"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "titles": {strings.Join(titel, "|")},
		"prop": {"imageinfo"}, "iiprop": {"url|extmetadata"}, "iiurlwidth": {strconv.Itoa(breite)}}
	if err := typBildAbfrage("https://commons.wikimedia.org/w/api.php?"+v.Encode(), &info); err != nil {
		return nil, err
	}
	var l []map[string]any
	for _, p := range info.Query.Pages {
		if p.Missing || len(p.ImageInfo) == 0 {
			continue
		}
		ii := p.ImageInfo[0]
		meta := func(k string) string {
			if m, ok := ii.ExtMetadata[k]; ok {
				if t, ok := m.Value.(string); ok {
					return strings.TrimSpace(html.UnescapeString(reHTMLTags.ReplaceAllString(t, "")))
				}
			}
			return ""
		}
		lizenz := meta("LicenseShortName")
		if !reFreieLizenz.MatchString(lizenz) || ii.ThumbURL == "" {
			continue
		}
		l = append(l, map[string]any{"datei": strings.TrimPrefix(p.Title, "File:"), "thumb": ii.ThumbURL, "lizenz": lizenz,
			"autor": strings.Join(strings.Fields(meta("Artist")), " "), "quelle": ii.DescriptionURL, "lizenz_url": meta("LicenseUrl")})
	}
	return l, nil
}

// GET /api/v1/aeronautik/typbild/kandidaten?suche=…[&text=…] – freie Bilder
// zur Auswahl (R891): Titelbilder passender Wikipedia-Artikel und Treffer der
// Commons-Dateisuche. Nur frei lizenziert.
func (s *Server) typBildKandidaten(c *gin.Context) {
	if !isLANRequest(c) && !extRateAllow("typkand:"+clientIP(c), 10) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Anfragen."})
		return
	}
	q := strings.TrimSpace(c.Query("suche"))
	text := strings.TrimSpace(c.Query("text"))
	if text == "" {
		text = q
	}
	if q == "" || len(text) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "suche fehlt"})
		return
	}
	dateien, gesehen := []string{}, map[string]bool{}
	dazu := func(d string) {
		d = strings.TrimPrefix(d, "File:")
		if d != "" && !gesehen[d] && len(dateien) < 24 {
			gesehen[d] = true
			dateien = append(dateien, d)
		}
	}
	var wiki struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				PageImage string `json:"pageimage"`
			} `json:"pages"`
		} `json:"query"`
	}
	v := url.Values{"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "generator": {"search"},
		"gsrsearch": {text + " aircraft"}, "gsrnamespace": {"0"}, "gsrlimit": {"8"}, "prop": {"pageimages"},
		"piprop": {"name"}, "pilicense": {"free"}}
	if typBildAbfrage("https://en.wikipedia.org/w/api.php?"+v.Encode(), &wiki) == nil {
		for _, p := range wiki.Query.Pages {
			if p.PageImage != "" && !reKeinFlieger.MatchString(p.Title) {
				dazu(p.PageImage)
			}
		}
	}
	var commons struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	v = url.Values{"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "list": {"search"},
		"srnamespace": {"6"}, "srsearch": {text + " filetype:bitmap"}, "srlimit": {"20"}}
	if typBildAbfrage("https://commons.wikimedia.org/w/api.php?"+v.Encode(), &commons) == nil {
		for _, t := range commons.Query.Search {
			dazu(t.Title)
		}
	}
	l, err := typBildInfos(dateien, 320)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Wikimedia nicht erreichbar: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"kandidaten": l, "suche": q, "text": text})
}

// POST /api/v1/admin/aeronautik/typbild/waehlen {suche, datei} oder {suche, keins: true}
// Foto für einen Typ von Hand festlegen oder den Typ sperren (R891).
func (s *Server) typBildWaehlen(c *gin.Context) {
	var req struct {
		Suche, Datei string
		Keins        bool
	}
	if c.ShouldBindJSON(&req) != nil || req.Suche == "" || (req.Datei == "" && !req.Keins) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	if !s.flugLokal() {
		c.JSON(http.StatusConflict, gin.H{"error": "Die Quartett-Tabellen liegen auf dem Empfänger – Foto dort wählen (über dessen Adresse im Heimnetz)."})
		return
	}
	_ = os.MkdirAll(s.typBildOrdner(), 0o755)
	schl := typBildSchluessel(req.Suche)
	alteDateien, _ := filepath.Glob(filepath.Join(s.typBildOrdner(), schl+".*"))
	neu := &typBild{Stand: time.Now().Unix(), Manuell: true}
	if !req.Keins {
		l, err := typBildInfos([]string{req.Datei}, 800)
		if err != nil || len(l) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Bild nicht gefunden oder nicht frei lizenziert."})
			return
		}
		k := l[0]
		req2, _ := http.NewRequest(http.MethodGet, k["thumb"].(string), nil)
		req2.Header.Set("User-Agent", "Fundus-Aeronautik/1.0 (https://github.com/t0b1as/FND; Flugquartett)")
		resp, err := typBildClient.Do(req2)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Download fehlgeschlagen: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		endung := map[string]string{"image/jpeg": "jpg", "image/png": "png"}[strings.Split(resp.Header.Get("Content-Type"), ";")[0]]
		daten, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
		if err != nil || resp.StatusCode != http.StatusOK || endung == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Bild konnte nicht geladen werden."})
			return
		}
		for _, d := range alteDateien {
			if !strings.HasSuffix(d, ".json") && !strings.Contains(d, "-ki.") {
				_ = os.Remove(d)
			}
		}
		if err := os.WriteFile(filepath.Join(s.typBildOrdner(), schl+"."+endung), daten, 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		neu.Datei, neu.Lizenz, neu.LizenzURL = schl+"."+endung, k["lizenz"].(string), k["lizenz_url"].(string)
		neu.Autor, neu.Quelle, neu.Artikel = k["autor"].(string), k["quelle"].(string), "von Hand gewählt"
	} else {
		for _, d := range alteDateien {
			if !strings.HasSuffix(d, ".json") && !strings.Contains(d, "-ki.") {
				_ = os.Remove(d)
			}
		}
	}
	if err := jsonAtomar(filepath.Join(s.typBildOrdner(), schl+".json"), neu, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Laufende Tabellen sofort angleichen; abgeschlossene richtet das Quartett
	// beim nächsten Erzeugen nach dem Zwischenspeicher aus.
	var foto *flugFoto
	if neu.Datei != "" {
		foto = &flugFoto{Datei: neu.Datei, Credit: neu.Credit()}
	}
	flugMu.Lock()
	for _, tab := range flugTabellen {
		for _, su := range tab {
			if typBildSuche(su.Hersteller, su.Typ, su.ICAO) == req.Suche {
				su.Foto = foto
			}
		}
	}
	flugMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true, "foto": foto})
}

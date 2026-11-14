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
	if t.Datei == "" && time.Since(time.Unix(t.Stand, 0)) > 14*24*time.Hour {
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
		"gsrsearch": {q + " aircraft"}, "gsrnamespace": {"0"}, "gsrlimit": {"3"}, "prop": {"pageimages"},
		"piprop": {"name"}, "pilicense": {"free"}}
	if err := typBildAbfrage("https://en.wikipedia.org/w/api.php?"+v.Encode(), &such); err != nil {
		return nil, err
	}
	datei, artikel, beste := "", "", 1<<30
	for _, p := range such.Query.Pages {
		if p.PageImage != "" && p.Index < beste {
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

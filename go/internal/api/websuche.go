package api

// Websuche (R895): eigene Metasuche im Node – siehe internal/websuche.
//
//   GET  /api/v1/websuche?q=…&sprache=de        Suche (Seite /suche, KI-Chat)
//   GET  /api/v1/admin/websuche                 Einstellungen, Platz, Laufwerke
//   POST /api/v1/admin/websuche                 Einstellungen speichern
//   GET  /api/v1/admin/websuche/knoten          Pis, die ihren Index anbieten
//
// Ablageort des eigenen Index (Crawler ab R896): lokal, auf einem Laufwerk
// unter /mnt oder /media, oder auf einem anderen Pi. Liegt er auf einem
// anderen Pi, fragt dieser Pi ihn über /fundus/suchindex/1.0.0 ab und
// schreibt selbst nichts. Ein Pi beantwortet solche Anfragen nur, wenn er
// seinen Index ausdrücklich freigibt.
//
// Einstellungen: <DataDir>/websuche.json.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/websuche"
	"github.com/gin-gonic/gin"
)

const suchindexProtocol = "/fundus/suchindex/1.0.0"

type websucheConfig struct {
	Aus       []string                      `json:"aus,omitempty"`     // abgeschaltete Dienste
	SearXNG   string                        `json:"searxng,omitempty"` // leer = die aus dem KI-Chat, falls gesetzt
	Sprache   string                        `json:"sprache,omitempty"`
	Index     websuche.Ort                  `json:"index"`
	Freigeben bool                          `json:"freigeben"`          // eigenen Index anderen Pis anbieten
	Crawler   websuche.CrawlerEinstellungen `json:"crawler"`            // R896
	Verbund   bool                          `json:"verbund"`            // R897: am Suchverbund teilnehmen
	Platz     websuche.Platzregel           `json:"platz_regel"`        // R898: Obergrenze, Rest auf dem Datenträger
	Umziehen  bool                          `json:"umziehen,omitempty"` // nur in der Anfrage: vorhandenen Index mitnehmen
}

var websucheDienste = []string{"duckduckgo", "mojeek", "wikipedia", "searxng", "index"}

func (k websucheConfig) an(dienst string) bool {
	for _, a := range k.Aus {
		if a == dienst {
			return false
		}
	}
	return true
}

func (s *Server) websucheDatei() string { return filepath.Join(s.cfg.DataDir, "websuche.json") }

func (s *Server) websucheLesen() websucheConfig {
	k := websucheConfig{Index: websuche.Ort{Modus: websuche.OrtLokal}}
	if b, err := os.ReadFile(s.websucheDatei()); err == nil {
		_ = json.Unmarshal(b, &k)
	}
	if k.Index.Modus == "" {
		k.Index.Modus = websuche.OrtLokal
	}
	if k.Sprache != "en" {
		k.Sprache = "de"
	}
	return k
}

// searxngAdresse: eigene Angabe, sonst die aus den KI-Chat-Einstellungen.
func (s *Server) websucheSearXNG(k websucheConfig) string {
	if k.SearXNG != "" {
		return k.SearXNG
	}
	return s.kiSucheLesen().SearXNG
}

// ---------------------------------------------------------------------------
// Ablage: einmal geöffnet, bei geändertem Ort neu.

var (
	wsAblageMu     sync.Mutex
	wsAblage       *websuche.Ablage
	wsAblageOrdner string
	wsAblageFehler string
)

func (s *Server) websucheAblage(k websucheConfig) (*websuche.Ablage, error) {
	ordner := k.Index.Ordner(s.cfg.DataDir)
	if ordner == "" {
		return nil, errors.New("Index liegt auf einem anderen Pi")
	}
	if u := wsUmzugStand(); u.Laeuft {
		return nil, errors.New("Index zieht gerade um")
	}
	wsAblageMu.Lock()
	defer wsAblageMu.Unlock()
	if wsAblage != nil && wsAblageOrdner == ordner {
		return wsAblage, nil
	}
	if wsAblage != nil { // anderer Ort eingestellt – alten Index schließen
		_ = wsAblage.Schliessen()
		wsAblage, wsAblageOrdner = nil, ""
	}
	if k.Index.Modus == websuche.OrtLaufwerk {
		// Nie auf die SD-Karte ausweichen, wenn die Platte fehlt.
		if pl := websuche.PlatzPruefen(k.Index, s.cfg.DataDir); pl.Fehler != "" {
			wsAblageFehler = pl.Fehler
			return nil, errors.New(pl.Fehler)
		}
	}
	a, err := websuche.Oeffnen(ordner, s.nodeID())
	if err != nil {
		wsAblageFehler = err.Error()
		return nil, err
	}
	wsAblage, wsAblageOrdner, wsAblageFehler = a, ordner, ""
	return a, nil
}

// websucheAblageSchliessen: Crawler anhalten, Index sichern und schließen.
func websucheAblageSchliessen() {
	wsCrawlerAnhalten()
	wsAblageMu.Lock()
	if wsAblage != nil {
		_ = wsAblage.Schliessen()
	}
	wsAblage, wsAblageOrdner, wsAblageFehler = nil, "", ""
	wsAblageMu.Unlock()
}

// ---------------------------------------------------------------------------
// Quellen und Suche

func (s *Server) websucheQuellen(k websucheConfig) []websuche.Quelle {
	var q []websuche.Quelle
	if k.an("duckduckgo") {
		q = append(q, websuche.DuckDuckGo{})
	}
	if k.an("mojeek") {
		q = append(q, websuche.Mojeek{})
	}
	if k.an("wikipedia") {
		q = append(q, websuche.Wikipedia{})
	}
	if adr := s.websucheSearXNG(k); adr != "" && k.an("searxng") {
		q = append(q, websuche.SearXNG{Basis: adr})
	}
	if k.an("index") {
		if k.Index.Modus == websuche.OrtKnoten {
			knoten := k.Index.Knoten
			q = append(q, websuche.Funktion{N: "index", G: 1.5, F: func(ctx context.Context, anfrage, sprache string) ([]websuche.Treffer, error) {
				return s.suchindexFern(ctx, knoten, anfrage, sprache)
			}})
		} else {
			q = append(q, websuche.Funktion{N: "index", G: 1.5, F: func(ctx context.Context, anfrage, sprache string) ([]websuche.Treffer, error) {
				return s.indexSuchen(ctx, k, anfrage)
			}})
		}
	}
	return q
}

// websucheSuchen: für die Seite und den KI-Chat.
func (s *Server) websucheSuchen(ctx context.Context, anfrage, sprache string) (*websuche.Ergebnis, error) {
	k := s.websucheLesen()
	if sprache != "de" && sprache != "en" {
		sprache = k.Sprache
	}
	e, err := websuche.Metasuche(ctx, anfrage, sprache, s.websucheQuellen(k), 9*time.Second)
	if err != nil {
		return nil, err
	}
	if len(e.Treffer) > 30 {
		e.Treffer = e.Treffer[:30]
	}
	return e, nil
}

// GET /api/v1/websuche?q=…
func (s *Server) websucheGet(c *gin.Context) {
	grenze := 15 // von außen: öffentlicher Node, sonst Suchmaschine für alle
	if isLANRequest(c) {
		grenze = 60
	}
	if !extRateAllow("websuche:"+c.ClientIP(), grenze) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Suchanfragen – bitte kurz warten."})
		return
	}
	e, err := s.websucheSuchen(c.Request.Context(), c.Query("q"), c.Query("sprache"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bitte einen Suchbegriff eingeben."})
		return
	}
	if len(e.Treffer) == 0 {
		e.Treffer = []websuche.Treffer{}
	}
	c.JSON(http.StatusOK, e)
}

// ---------------------------------------------------------------------------
// P2P: Index auf einem anderen Pi

type suchindexAnfrage struct {
	Art     string              `json:"art"` // status | suche | vormerken (R896) | kopie (R897)
	URL     string              `json:"url,omitempty"`
	Anfrage string              `json:"q,omitempty"`
	Sprache string              `json:"sprache,omitempty"`
	Weit    bool                `json:"weit,omitempty"`  // R897: im ganzen Verbund suchen (für Pis ohne eigenen Index)
	URLs    []string            `json:"urls,omitempty"`  // R897: Links aus dem Verbund
	Tiefe   int                 `json:"tiefe,omitempty"` // dazu: Linktiefe
	Doks    []websuche.Dokument `json:"doks,omitempty"`  // R897: Kopien für die zweite Ablage
	Weg     []string            `json:"weg,omitempty"`   // R897: entfernte Seiten
}

type suchindexAntwort struct {
	Status    int                `json:"status"`
	Fehler    string             `json:"fehler,omitempty"`
	Node      string             `json:"node"`
	Info      *websuche.Info     `json:"info,omitempty"`
	Treffer   []websuche.Treffer `json:"treffer,omitempty"`
	Freigeben bool               `json:"freigeben,omitempty"` // R897
	Verbund   bool               `json:"verbund,omitempty"`   // R897
	Voll      bool               `json:"voll,omitempty"`      // R898: nimmt nichts Neues an
}

func (s *Server) suchindexBearbeiten(peerID string, data []byte) []byte {
	antwort := func(a suchindexAntwort) []byte {
		a.Node = aeroNodeName()
		b, _ := json.Marshal(a)
		return b
	}
	if !extRateAllow("suchindex:"+peerID, 60) {
		return antwort(suchindexAntwort{Status: http.StatusTooManyRequests, Fehler: "zu viele Anfragen"})
	}
	var a suchindexAnfrage
	if json.Unmarshal(data, &a) != nil || (a.Art != "status" && a.Art != "suche" && a.Art != "vormerken" && a.Art != "kopie") ||
		len(a.Anfrage) > 600 || len(a.URLs) > 500 || len(a.Doks) > 200 || len(a.Weg) > 500 {
		return antwort(suchindexAntwort{Status: http.StatusBadRequest})
	}
	k := s.websucheLesen()
	if (!k.Freigeben && !k.Verbund) || k.Index.Modus == websuche.OrtKnoten {
		return antwort(suchindexAntwort{Status: http.StatusNotFound, Fehler: "dieser Pi bietet keinen Index an"})
	}
	ab, err := s.websucheAblage(k)
	if err != nil {
		return antwort(suchindexAntwort{Status: http.StatusServiceUnavailable, Fehler: err.Error()})
	}
	if a.Art == "status" {
		i := ab.Info()
		return antwort(suchindexAntwort{Status: http.StatusOK, Info: &i, Freigeben: k.Freigeben, Verbund: k.Verbund, Voll: s.websuchePlatz(k, ab).Voll})
	}
	if a.Art == "kopie" { // R897: nur unter Verbund-Mitgliedern
		if !k.Verbund || !s.verbundIstMitglied(peerID) {
			return antwort(suchindexAntwort{Status: http.StatusForbidden, Fehler: "nicht im Verbund"})
		}
		if s.websuchePlatz(k, ab).Voll && len(a.Doks) > 0 { // R898: Löschungen ja, Neues nein
			verbundKopieAnnehmen(ab, nil, a.Weg)
			return antwort(suchindexAntwort{Status: http.StatusInsufficientStorage, Fehler: "Speicher voll", Voll: true})
		}
		verbundKopieAnnehmen(ab, a.Doks, a.Weg)
		return antwort(suchindexAntwort{Status: http.StatusOK})
	}
	if a.Art == "vormerken" && len(a.URLs) > 0 {
		if !k.Verbund {
			return antwort(suchindexAntwort{Status: http.StatusForbidden, Fehler: "nicht im Verbund"})
		}
		for _, u := range a.URLs {
			wsCrawlerVormerkenTiefe(u, a.Tiefe)
		}
		return antwort(suchindexAntwort{Status: http.StatusOK})
	}
	if a.Art == "suche" && a.Weit {
		if !k.Freigeben {
			return antwort(suchindexAntwort{Status: http.StatusForbidden, Fehler: "dieser Pi bietet keinen Index an"})
		}
		ctx, abbr := context.WithTimeout(context.Background(), 7*time.Second)
		defer abbr()
		t, err := s.indexSuchen(ctx, k, a.Anfrage)
		if err != nil {
			return antwort(suchindexAntwort{Status: http.StatusInternalServerError, Fehler: err.Error()})
		}
		return antwort(suchindexAntwort{Status: http.StatusOK, Treffer: t})
	}
	if a.Art == "suche" && !k.Freigeben && !s.verbundIstMitglied(peerID) {
		return antwort(suchindexAntwort{Status: http.StatusForbidden, Fehler: "dieser Pi bietet keinen Index an"})
	}
	if a.Art == "vormerken" {
		if !wsCrawlerVormerken(a.URL) {
			return antwort(suchindexAntwort{Status: http.StatusConflict, Fehler: "Crawler aus oder Adresse schon vorgemerkt"})
		}
		return antwort(suchindexAntwort{Status: http.StatusOK})
	}
	ctx, abbr := context.WithTimeout(context.Background(), 6*time.Second)
	defer abbr()
	t, err := ab.Suchen(ctx, a.Anfrage, 20)
	if err != nil {
		return antwort(suchindexAntwort{Status: http.StatusInternalServerError, Fehler: err.Error()})
	}
	return antwort(suchindexAntwort{Status: http.StatusOK, Treffer: t})
}

func (s *Server) suchindexFrage(ctx context.Context, knoten string, a suchindexAnfrage) (*suchindexAntwort, error) {
	if s.node == nil {
		return nil, errors.New("kein P2P")
	}
	body, _ := json.Marshal(a)
	roh, err := s.node.SendAndReceive(ctx, knoten, suchindexProtocol, body)
	if err != nil {
		return nil, errors.New("Index-Pi nicht erreichbar")
	}
	var ant suchindexAntwort
	if json.Unmarshal(roh, &ant) != nil || ant.Status == 0 {
		return nil, errors.New("Index-Pi antwortet nicht verständlich (ältere Version?)")
	}
	if ant.Status != http.StatusOK {
		if ant.Fehler == "" {
			ant.Fehler = http.StatusText(ant.Status)
		}
		return &ant, errors.New(ant.Fehler)
	}
	return &ant, nil
}

func (s *Server) suchindexFern(ctx context.Context, knoten, anfrage, sprache string) ([]websuche.Treffer, error) {
	ant, err := s.suchindexFrage(ctx, knoten, suchindexAnfrage{Art: "suche", Anfrage: anfrage, Sprache: sprache, Weit: true})
	if err != nil {
		return nil, err
	}
	return ant.Treffer, nil
}

// ---------------------------------------------------------------------------
// Admin

// GET /api/v1/admin/websuche
func (s *Server) websucheEinstellungen(c *gin.Context) {
	k := s.websucheLesen()
	h := gin.H{
		"aus": k.Aus, "searxng": k.SearXNG, "searxng_ki": s.kiSucheLesen().SearXNG, "sprache": k.Sprache,
		"index": k.Index, "freigeben": k.Freigeben, "dienste": websucheDienste,
		"laufwerke":          websuche.Laufwerke(),
		"umzug":              wsUmzugStand(),
		"verbund":            k.Verbund,
		"verbund_mitglieder": verbundStandListe(),
		"platz_regel":        k.Platz,
		"index_vorhanden":    map[string]bool{},
	}
	for _, l := range websuche.Laufwerke() {
		h["index_vorhanden"].(map[string]bool)[l.Pfad] = websuche.IndexVorhanden(websuche.Ort{Modus: websuche.OrtLaufwerk, Pfad: l.Pfad}.Ordner(""))
	}
	if k.Index.Modus == websuche.OrtKnoten {
		ctx, abbr := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer abbr()
		if ant, err := s.suchindexFrage(ctx, k.Index.Knoten, suchindexAnfrage{Art: "status"}); err != nil {
			h["index_fehler"] = err.Error()
		} else {
			h["index_info"] = ant.Info
			h["index_node"] = ant.Node
		}
	} else {
		h["platz"] = websuche.PlatzPruefen(k.Index, s.cfg.DataDir)
		if a, err := s.websucheAblage(k); err != nil {
			h["index_fehler"] = err.Error()
		} else {
			h["index_info"] = a.Info()
			h["platz_stand"] = s.websuchePlatz(k, a) // R898
		}
	}
	c.JSON(http.StatusOK, h)
}

// POST /api/v1/admin/websuche
func (s *Server) websucheSpeichern(c *gin.Context) {
	var req websucheConfig
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	req.SearXNG = strings.TrimRight(strings.TrimSpace(req.SearXNG), "/")
	if req.SearXNG != "" && !reOllamaURL.MatchString(req.SearXNG) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SearXNG-Adresse wie http://192.168.1.20:8080"})
		return
	}
	var aus []string
	for _, a := range req.Aus {
		for _, d := range websucheDienste {
			if a == d {
				aus = append(aus, a)
			}
		}
	}
	req.Aus = aus
	if req.Index.Modus == "" {
		req.Index.Modus = websuche.OrtLokal
	}
	req.Index.Pfad = strings.TrimSpace(req.Index.Pfad)
	req.Index.Knoten = strings.TrimSpace(req.Index.Knoten)
	if err := req.Index.Pruefen(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	switch req.Index.Modus {
	case websuche.OrtLaufwerk:
		req.Index.Knoten = ""
		if pl := websuche.PlatzPruefen(req.Index, s.cfg.DataDir); pl.Fehler != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": pl.Fehler})
			return
		}
	case websuche.OrtKnoten:
		req.Index.Pfad = ""
		req.Freigeben = false // wer keinen Index hält, kann keinen anbieten
		req.Verbund = false
		if req.Index.Knoten == s.nodeID() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Das ist dieser Pi – dafür „lokal“ oder ein Laufwerk wählen."})
			return
		}
		ctx, abbr := context.WithTimeout(c.Request.Context(), 6*time.Second)
		_, err := s.suchindexFrage(ctx, req.Index.Knoten, suchindexAnfrage{Art: "status"})
		abbr()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Dieser Pi bietet keinen Index an: " + err.Error()})
			return
		}
	default:
		req.Index.Pfad, req.Index.Knoten = "", ""
	}
	if req.Sprache != "en" {
		req.Sprache = "de"
	}
	if req.Platz.GrenzeGB < 0 || req.Platz.GrenzeGB > 100000 || req.Platz.RestGB < 0 || req.Platz.RestGB > 100000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Speichergrenzen: 0 bis 100000 GB (0 = automatisch)"})
		return
	}
	alt := s.websucheLesen()
	req.Crawler = alt.Crawler // eigene Einstellung unter /websuche/crawler
	umziehen := req.Umziehen
	req.Umziehen = false
	if u := wsUmzugStand(); u.Laeuft {
		c.JSON(http.StatusConflict, gin.H{"error": "Ein Umzug des Index läuft noch."})
		return
	}
	altOrdner, neuOrdner := alt.Index.Ordner(s.cfg.DataDir), req.Index.Ordner(s.cfg.DataDir)
	if umziehen && altOrdner != "" && neuOrdner != "" && altOrdner != neuOrdner && websuche.IndexVorhanden(altOrdner) {
		// Umzug im Hintergrund; die neuen Einstellungen gelten, sobald er fertig ist.
		s.websucheUmziehen(altOrdner, neuOrdner, req)
		c.JSON(http.StatusOK, gin.H{"ok": true, "umzug": true})
		return
	}
	if err := jsonAtomar(s.websucheDatei(), req, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	if altOrdner != neuOrdner || alt.Verbund != req.Verbund {
		websucheAblageSchliessen()
		go func() {
			s.verbundAktualisieren() // R897: Mitglieder sofort, nicht erst in 3 Minuten
			s.websucheCrawlerStarten()
		}()
	}
	websuche.CacheLeeren()
	// Probesuche, damit ein Fehler hier auffällt und nicht erst beim Suchen.
	ctx, abbr := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer abbr()
	e, err := s.websucheSuchen(ctx, "Raspberry Pi", req.Sprache)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "pruefung": "✗ Probesuche: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "treffer": len(e.Treffer), "dienste": e.Dienste})
}

// GET /api/v1/admin/websuche/knoten – Pis, die ihren Index anbieten.
func (s *Server) websucheKnoten(c *gin.Context) {
	type eintrag struct {
		Peer string         `json:"peer"`
		Node string         `json:"node"`
		Info *websuche.Info `json:"info"`
	}
	liste := []eintrag{}
	if s.node != nil {
		ctx, abbr := context.WithTimeout(c.Request.Context(), 4*time.Second)
		defer abbr()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, p := range s.node.Peers() {
			wg.Add(1)
			go func(pid string) {
				defer wg.Done()
				ant, err := s.suchindexFrage(ctx, pid, suchindexAnfrage{Art: "status"})
				if err != nil || ant.Info == nil || !ant.Freigeben {
					return
				}
				mu.Lock()
				liste = append(liste, eintrag{pid, ant.Node, ant.Info})
				mu.Unlock()
			}(p.String())
		}
		wg.Wait()
	}
	c.JSON(http.StatusOK, gin.H{"knoten": liste})
}

func (s *Server) registerWebsuche() {
	s.router.GET("/api/v1/websuche", s.websucheGet)
	s.router.POST("/api/v1/websuche/vormerken", s.websucheVormerken) // R896: angeklickter Treffer
	if s.node != nil {
		s.node.RegisterProtocol(suchindexProtocol, s.suchindexBearbeiten)
	}
	go func() { // R896: Crawler nach dem Start, wenn eingeschaltet
		time.Sleep(90 * time.Second)
		s.verbundAktualisieren()
		s.websucheCrawlerStarten()
	}()
	go s.verbundSchleife() // R897
}

// websucheDiensteText: "duckduckgo: ok (8) · mojeek: nicht erreichbar"
func websucheDiensteText(d map[string]string) string {
	var teile []string
	for _, n := range websucheDienste {
		if v, ok := d[n]; ok {
			teile = append(teile, n+": "+v)
		}
	}
	return strings.Join(teile, " · ")
}

// websuchePlatz (R898): Größe des Index gegen Grenze und freien Platz.
func (s *Server) websuchePlatz(k websucheConfig, ab *websuche.Ablage) websuche.PlatzStand {
	if ab == nil {
		return websuche.PlatzStand{}
	}
	return k.Platz.Stand(ab.Ordner, ab.Index)
}

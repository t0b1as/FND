package api

// Crawler und Umzug des Suchindex (R896) – siehe internal/websuche.
//
//   GET  /api/v1/admin/websuche/crawler   Stand und Einstellungen
//   POST /api/v1/admin/websuche/crawler   Einstellungen; startet/hält an
//   POST /api/v1/websuche/vormerken       angeklickter Treffer → Warteschlange
//
// Der Crawler läuft auf dem Pi, der den Index hält. Liegt der Index auf
// einem anderen Pi, gehen vorgemerkte Adressen per P2P dorthin.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/websuche"
	"github.com/gin-gonic/gin"
)

var (
	wsCrawlerMu sync.Mutex
	wsCrawler   *websuche.Crawler

	wsUmzugMu sync.Mutex
	wsUmzug   websucheUmzug
)

type websucheUmzug struct {
	Laeuft  bool    `json:"laeuft"`
	Prozent float64 `json:"prozent"`
	Von     string  `json:"von,omitempty"`
	Nach    string  `json:"nach,omitempty"`
	Fehler  string  `json:"fehler,omitempty"`
	Fertig  int64   `json:"fertig,omitempty"` // Zeitpunkt des Abschlusses
}

func wsUmzugStand() websucheUmzug {
	wsUmzugMu.Lock()
	defer wsUmzugMu.Unlock()
	return wsUmzug
}

// websucheCrawlerStarten: läuft, wenn eingeschaltet und der Index hier liegt.
func (s *Server) websucheCrawlerStarten() {
	k := s.websucheLesen()
	if !k.Crawler.Aktiv || k.Index.Modus == websuche.OrtKnoten || wsUmzugStand().Laeuft {
		wsCrawlerAnhalten()
		return
	}
	ab, err := s.websucheAblage(k)
	if err != nil {
		return
	}
	wsCrawlerMu.Lock()
	defer wsCrawlerMu.Unlock()
	if wsCrawler != nil {
		return
	}
	wsCrawler = websuche.NeuerCrawler(ab.Index, ab.Ordner, k.Crawler)
	wsCrawler.Platz = func() string { // R898: anhalten, bevor der Speicher voll ist
		k := s.websucheLesen()
		if st := s.websuchePlatz(k, ab); st.Voll {
			return st.Grund
		}
		return ""
	}
	if k.Verbund { // R897: nur eigene Hosts lesen, Fremdes abgeben, Kopien verschicken
		wsCrawler.Zustaendig = s.verbundZustaendig
		wsCrawler.Abgeben = verbundAbgeben
		wsCrawler.Kopie = verbundKopie
		wsCrawler.Entfernt = verbundEntfernt
	}
	wsCrawler.Starten()
}

func wsCrawlerAnhalten() {
	wsCrawlerMu.Lock()
	c := wsCrawler
	wsCrawler = nil
	wsCrawlerMu.Unlock()
	if c != nil {
		c.Anhalten()
	}
}

func wsCrawlerVormerkenTiefe(u string, tiefe int) bool {
	wsCrawlerMu.Lock()
	c := wsCrawler
	wsCrawlerMu.Unlock()
	return c != nil && c.VormerkenTiefe(u, tiefe)
}

func wsCrawlerVormerken(u string) bool {
	wsCrawlerMu.Lock()
	c := wsCrawler
	wsCrawlerMu.Unlock()
	return c != nil && c.Vormerken(u)
}

// GET /api/v1/admin/websuche/crawler
func (s *Server) websucheCrawlerGet(c *gin.Context) {
	k := s.websucheLesen()
	h := gin.H{"einstellungen": k.Crawler.Vorgaben(), "aktiv": k.Crawler.Aktiv, "index_modus": k.Index.Modus}
	wsCrawlerMu.Lock()
	cr := wsCrawler
	wsCrawlerMu.Unlock()
	if cr != nil {
		h["stand"] = cr.Stand()
	}
	if k.Index.Modus == websuche.OrtKnoten {
		h["hinweis"] = "Der Index liegt auf einem anderen Pi – dort läuft auch der Crawler. Angeklickte Treffer werden ihm gemeldet."
	}
	c.JSON(http.StatusOK, h)
}

// POST /api/v1/admin/websuche/crawler
func (s *Server) websucheCrawlerSetzen(c *gin.Context) {
	var e websuche.CrawlerEinstellungen
	if c.ShouldBindJSON(&e) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	grenze := func(v, min, max int) int {
		if v < min {
			return min
		}
		if v > max {
			return max
		}
		return v
	}
	e.MBProStunde = grenze(e.MBProStunde, 10, 5000)
	e.SeitenJeHost = grenze(e.SeitenJeHost, 10, 100000)
	e.Tiefe = grenze(e.Tiefe, 1, 10)
	e.NeuNachTagen = grenze(e.NeuNachTagen, 1, 365)
	var start []string
	for _, roh := range e.Startseiten {
		roh = strings.TrimSpace(roh)
		if roh == "" {
			continue
		}
		if !strings.Contains(roh, "://") {
			roh = "https://" + roh
		}
		b, err := url.Parse(roh)
		if err != nil || websuche.LinkAufloesen(b, roh) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Keine gültige Webadresse: " + roh})
			return
		}
		start = append(start, websuche.LinkAufloesen(b, roh))
		if len(start) >= 200 {
			break
		}
	}
	e.Startseiten = start
	k := s.websucheLesen()
	if e.Aktiv && k.Index.Modus == websuche.OrtKnoten {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Der Index liegt auf einem anderen Pi – den Crawler dort einschalten."})
		return
	}
	k.Crawler = e
	if err := jsonAtomar(s.websucheDatei(), k, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	wsCrawlerAnhalten() // neue Einstellungen gelten ab dem Neustart des Crawlers
	if e.Aktiv {
		s.websucheCrawlerStarten()
		wsCrawlerMu.Lock()
		laeuft := wsCrawler != nil
		wsCrawlerMu.Unlock()
		if !laeuft {
			c.JSON(http.StatusOK, gin.H{"ok": true, "meldung": "Gespeichert, aber der Crawler startet nicht – Ablageort prüfen."})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "meldung": "Crawler läuft."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "meldung": "Crawler aus."})
}

// POST /api/v1/websuche/vormerken {url} – angeklickter Treffer.
func (s *Server) websucheVormerken(c *gin.Context) {
	if !extRateAllow("vormerken:"+c.ClientIP(), 30) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "zu viele"})
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.URL) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	k := s.websucheLesen()
	if !k.an("index") {
		c.JSON(http.StatusOK, gin.H{"ok": false})
		return
	}
	if k.Index.Modus == websuche.OrtKnoten {
		go func() {
			ctx, abbr := context.WithTimeout(context.Background(), 8*time.Second)
			defer abbr()
			_, _ = s.suchindexFrage(ctx, k.Index.Knoten, suchindexAnfrage{Art: "vormerken", URL: req.URL})
		}()
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if k.Verbund { // R897: an den zuständigen Pi
		if m := verbundZuteilung(); len(m) > 1 {
			if z := websuche.Zustaendige(hostVon(req.URL), m, 1); len(z) == 1 && z[0] != s.nodeID() {
				verbundAbgeben(req.URL, 0)
				c.JSON(http.StatusOK, gin.H{"ok": true})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": wsCrawlerVormerken(req.URL)})
}

// websucheUmziehen: Index im Hintergrund an den neuen Ort kopieren; erst
// danach gelten die neuen Einstellungen. Schlägt es fehl, bleibt alles beim Alten.
func (s *Server) websucheUmziehen(von, nach string, neu websucheConfig) {
	wsUmzugMu.Lock()
	wsUmzug = websucheUmzug{Laeuft: true, Von: von, Nach: nach}
	wsUmzugMu.Unlock()
	go func() {
		websucheAblageSchliessen()
		err := websuche.Umziehen(von, nach, func(k, g int64) {
			if g > 0 {
				wsUmzugMu.Lock()
				wsUmzug.Prozent = float64(k) * 100 / float64(g)
				wsUmzugMu.Unlock()
			}
		})
		if err == nil {
			neu.Crawler = s.websucheLesen().Crawler
			err = jsonAtomar(s.websucheDatei(), neu, 0o600)
		}
		wsUmzugMu.Lock()
		wsUmzug.Laeuft = false
		wsUmzug.Fertig = time.Now().Unix()
		if err != nil {
			wsUmzug.Fehler = err.Error()
		} else {
			wsUmzug.Prozent = 100
		}
		wsUmzugMu.Unlock()
		websuche.CacheLeeren()
		s.websucheCrawlerStarten()
	}()
}

package api

// Websuche für den KI-Chat (R871). Das Modell bekommt zwei Werkzeuge –
// „websuche“ und „seite_lesen“ – und entscheidet selbst, wann es sie braucht.
// Suchdienste:
//   - Ollama-Websuche (https://ollama.com/api/web_search, web_fetch): mit
//     kostenlosem Ollama-Konto, API-Schlüssel nötig. Anfragen gehen an Ollama.
//   - SearXNG, selbst betrieben (z.B. per Docker im Heimnetz): ohne Schlüssel,
//     privat. Seiten liest dann der Node selbst – über den abgesicherten Abruf
//     der Linkvorschau (nur öffentliche Adressen, Port 80/443), damit niemand
//     über den Chat Adressen im Heimnetz abfragen lassen kann. Die SearXNG-
//     Adresse selbst darf im Heimnetz liegen: Sie legt nur der Admin fest.
// Einstellungen: <DataDir>/ki-suche.json (0600) – der Schlüssel wird nie
// zurückgegeben.

import (
	"bytes"
	"context"
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
	"time"

	"github.com/gin-gonic/gin"
)

type kiSucheConfig struct {
	Anbieter  string `json:"anbieter"` // "" (aus) | "ollama" | "searxng"
	OllamaKey string `json:"ollama_key"`
	SearXNG   string `json:"searxng"`
}

type kiTreffer struct {
	Titel string `json:"titel"`
	URL   string `json:"url"`
	Text  string `json:"text"`
}

func (s *Server) kiSucheDatei() string { return filepath.Join(s.cfg.DataDir, "ki-suche.json") }

func (s *Server) kiSucheLesen() kiSucheConfig {
	var k kiSucheConfig
	if b, err := os.ReadFile(s.kiSucheDatei()); err == nil {
		_ = json.Unmarshal(b, &k)
	}
	return k
}

func (k kiSucheConfig) aktiv() bool {
	return (k.Anbieter == "ollama" && k.OllamaKey != "") || (k.Anbieter == "searxng" && k.SearXNG != "")
}

// GET /api/v1/admin/ki/suche
func (s *Server) kiSucheGet(c *gin.Context) {
	k := s.kiSucheLesen()
	c.JSON(http.StatusOK, gin.H{"anbieter": k.Anbieter, "searxng": k.SearXNG, "schluessel_gesetzt": k.OllamaKey != ""})
}

// POST /api/v1/admin/ki/suche {anbieter, searxng, ollama_key?}
func (s *Server) kiSucheSet(c *gin.Context) {
	var req kiSucheConfig
	if c.ShouldBindJSON(&req) != nil || (req.Anbieter != "" && req.Anbieter != "ollama" && req.Anbieter != "searxng") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	alt := s.kiSucheLesen()
	k := kiSucheConfig{Anbieter: req.Anbieter, OllamaKey: alt.OllamaKey, SearXNG: strings.TrimRight(strings.TrimSpace(req.SearXNG), "/")}
	if v := strings.TrimSpace(req.OllamaKey); v != "" {
		k.OllamaKey = v // leer = bisherigen behalten
	}
	if k.SearXNG != "" && !reOllamaURL.MatchString(k.SearXNG) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SearXNG-Adresse wie http://192.168.1.20:8080"})
		return
	}
	if err := jsonAtomar(s.kiSucheDatei(), k, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	// Gleich ausprobieren, damit ein Fehler hier auffällt und nicht im Chat.
	if !k.aktiv() {
		c.JSON(http.StatusOK, gin.H{"ok": true, "pruefung": "Websuche aus."})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	t, err := s.kiSuchen(ctx, k, "Wetter Frankfurt heute")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "pruefung": "✗ Probesuche fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "pruefung": fmt.Sprintf("✓ Probesuche: %d Treffer", len(t))})
}

var kiSucheClient = &http.Client{Timeout: 15 * time.Second}

func kiKuerzen(t string, n int) string {
	t = strings.TrimSpace(t)
	if r := []rune(t); len(r) > n {
		return string(r[:n]) + " …"
	}
	return t
}

// kiSuchen: bis zu 5 Treffer mit Titel, Adresse und kurzem Auszug.
func (s *Server) kiSuchen(ctx context.Context, k kiSucheConfig, anfrage string) ([]kiTreffer, error) {
	var treffer []kiTreffer
	switch k.Anbieter {
	case "ollama":
		b, _ := json.Marshal(map[string]any{"query": anfrage, "max_results": 5})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://ollama.com/api/web_search", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+k.OllamaKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := kiSucheClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Ollama-Websuche nicht erreichbar")
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("API-Schlüssel abgelehnt")
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("Kontingent der Ollama-Websuche erschöpft")
		}
		var d struct {
			Results []struct {
				Title, URL, Content string
			} `json:"results"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&d) != nil {
			return nil, fmt.Errorf("Ollama-Websuche antwortet mit %d", resp.StatusCode)
		}
		for _, r := range d.Results {
			treffer = append(treffer, kiTreffer{Titel: r.Title, URL: r.URL, Text: kiKuerzen(r.Content, 600)})
		}
	case "searxng":
		q := url.Values{"q": {anfrage}, "format": {"json"}, "language": {"de"}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, k.SearXNG+"/search?"+q.Encode(), nil)
		resp, err := kiSucheClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("SearXNG nicht erreichbar unter %s", k.SearXNG)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("SearXNG liefert kein JSON – in settings.yml unter search.formats „json“ ergänzen")
		}
		var d struct {
			Results []struct {
				Title, URL, Content string
			} `json:"results"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d) != nil {
			return nil, fmt.Errorf("SearXNG antwortet mit %d", resp.StatusCode)
		}
		for i, r := range d.Results {
			if i >= 5 {
				break
			}
			treffer = append(treffer, kiTreffer{Titel: r.Title, URL: r.URL, Text: kiKuerzen(r.Content, 600)})
		}
	default:
		return nil, fmt.Errorf("Websuche aus")
	}
	return treffer, nil
}

var (
	reHTMLBlock = regexp.MustCompile(`(?is)<(script|style|noscript|svg|nav|footer|header|form)\b.*?</(script|style|noscript|svg|nav|footer|header|form)>`)
	reHTMLTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reLeer      = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLeerZeile = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// kiSeiteLesen: Text einer Webseite, gekürzt. Über Ollama (web_fetch) oder –
// bei SearXNG – selbst, abgesichert gegen Adressen im Heimnetz.
func (s *Server) kiSeiteLesen(ctx context.Context, k kiSucheConfig, adresse string) (string, string, error) {
	if !feedLinkMuster.MatchString(adresse) {
		return "", "", fmt.Errorf("keine gültige Webadresse")
	}
	if k.Anbieter == "ollama" {
		b, _ := json.Marshal(map[string]any{"url": adresse})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://ollama.com/api/web_fetch", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+k.OllamaKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := kiSucheClient.Do(req)
		if err != nil {
			return "", "", fmt.Errorf("Ollama nicht erreichbar")
		}
		defer resp.Body.Close()
		var d struct {
			Title, Content string
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d) != nil {
			return "", "", fmt.Errorf("Seite nicht lesbar (%d)", resp.StatusCode)
		}
		return d.Title, kiKuerzen(d.Content, 6000), nil
	}
	daten, _, err := feedAbrufen(ctx, adresse, 3<<20, "text/html")
	if err != nil {
		return "", "", err
	}
	t := string(daten)
	titel := ""
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(t); m != nil {
		titel = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	t = reHTMLBlock.ReplaceAllString(t, " ")
	t = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr)\b[^>]*>`).ReplaceAllString(t, "\n")
	t = html.UnescapeString(reHTMLTag.ReplaceAllString(t, " "))
	t = reLeerZeile.ReplaceAllString(reLeer.ReplaceAllString(t, " "), "\n\n")
	return titel, kiKuerzen(t, 6000), nil
}

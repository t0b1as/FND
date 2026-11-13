package api

// KI-Einstellungen (R868): Verbindungstest zu Ollama – auch auf einem
// anderen Rechner im Heimnetz (z.B. Windows-PC mit Grafikkarte). Gespeichert
// werden die Werte über die Einstellungs-Datei (einstellungen_energie.go);
// sie wirken nach einem Neustart, weil der Analyzer beim Start angelegt wird.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// POST /api/v1/admin/llm/test {url, modell}
func (s *Server) llmTest(c *gin.Context) {
	var req struct {
		URL    string `json:"url"`
		Modell string `json:"modell"`
	}
	_ = c.ShouldBindJSON(&req)
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if url == "" {
		url = s.cfg.LLMURL
	}
	if !reOllamaURL.MatchString(url) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Adresse wie http://192.168.1.20:11434"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "fehler": "Ollama nicht erreichbar unter " + url +
			" – läuft es dort, hört es im Heimnetz (OLLAMA_HOST=0.0.0.0), und lässt die Firewall Port 11434 durch?"})
		return
	}
	defer resp.Body.Close()
	var d struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d) != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "fehler": "Unter dieser Adresse antwortet kein Ollama."})
		return
	}
	modelle := []gin.H{}
	gefunden := false
	gesucht := strings.TrimSpace(req.Modell)
	for _, m := range d.Models {
		modelle = append(modelle, gin.H{"name": m.Name, "gb": float64(m.Size) / 1e9})
		if gesucht != "" && (m.Name == gesucht || m.Name == gesucht+":latest") {
			gefunden = true
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "modelle": modelle, "modell_vorhanden": gefunden, "gesucht": gesucht})
}

// GET /api/v1/admin/llm – was gerade läuft. „erreichbar“ wird LIVE geprüft
// (R873), nicht aus dem Moment des Starts übernommen.
func (s *Server) llmZustand(c *gin.Context) {
	erreichbar, fehler := false, ""
	if s.analyzer != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		if err := s.analyzer.Ping(ctx); err != nil {
			fehler = err.Error()
		} else {
			erreichbar = true
		}
		cancel()
	}
	c.JSON(http.StatusOK, gin.H{"aktiv": s.analyzer != nil, "erreichbar": erreichbar, "fehler": fehler, "url": s.cfg.LLMURL, "modell": s.cfg.LLMModel,
		"textmodell": s.cfg.LLMTextModel, "fotos": s.cfg.LLMVision, "keep_alive": s.cfg.LLMKeepAlive,
		"eingeschaltet": s.cfg.LLMEnabled})
}

// NeustartGewuenscht: vom Neustart-Knopf gesetzt. main fährt dann wie bei
// "systemctl stop" sauber herunter und endet mit Exitcode 75 statt 0 –
// fundus-node.service hat Restart=on-failure und startet den Node nach 10 s
// neu. Bei Exitcode 0 bliebe er aus.
var NeustartGewuenscht atomic.Bool

// POST /api/v1/admin/neustart
func (s *Server) adminNeustart(c *gin.Context) {
	NeustartGewuenscht.Store(true)
	c.JSON(http.StatusOK, gin.H{"ok": true, "hinweis": "Der Node startet neu – in etwa 20 Sekunden wieder erreichbar."})
	go func() {
		time.Sleep(500 * time.Millisecond) // Antwort noch ausliefern
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}()
}

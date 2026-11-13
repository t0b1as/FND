package api

// KI-Chat (R870): Unterhaltung mit dem Modell aus Einstellungen → KI – auch
// auf einem Rechner mit Grafikkarte im Heimnetz. Die Antwort kommt Wort für
// Wort (Ollama streamt, der Node reicht jedes Stück sofort durch). Bricht der
// Browser ab, bricht auch Ollama ab. Der Verlauf liegt im Browser; der Node
// speichert keine Unterhaltungen.
//
// Zugang: im Heimnetz frei; von außen nur angemeldet und höchstens 12
// Nachrichten je Minute – sonst könnte jeder, der die Adresse kennt, die
// Grafikkarte nutzen.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fundus/node/internal/llm"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerKIChat() {
	s.router.GET("/api/v1/ki/status", s.kiStatus)
	s.router.POST("/api/v1/ki/chat", s.kiChat)
}

func (s *Server) kiModell(mitBild bool) string {
	if !mitBild && s.cfg.LLMTextModel != "" {
		return s.cfg.LLMTextModel
	}
	return s.cfg.LLMModel
}

// GET /api/v1/ki/status
func (s *Server) kiStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"aktiv": s.analyzer != nil, "modell": s.kiModell(false),
		"bildmodell": s.kiModell(true), "fotos": s.cfg.LLMVision})
}

// POST /api/v1/ki/chat {nachrichten: [{rolle, text, bild?}]}
func (s *Server) kiChat(c *gin.Context) {
	if !isLANRequest(c) {
		if sess := s.getSession(c); sess == nil || sess.identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Der KI-Chat ist von außen nur nach Anmeldung nutzbar."})
			return
		}
		if !extRateAllow("kichat:"+clientIP(c), 12) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Nachrichten – bitte kurz warten."})
			return
		}
	}
	if s.analyzer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Die KI ist aus oder Ollama war beim Start nicht erreichbar – Einstellungen → KI."})
		return
	}
	var req struct {
		Nachrichten []struct {
			Rolle string `json:"rolle"`
			Text  string `json:"text"`
			Bild  string `json:"bild"` // Base64-JPEG ohne data:-Präfix
		} `json:"nachrichten"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 24<<20)
	if c.ShouldBindJSON(&req) != nil || len(req.Nachrichten) == 0 || len(req.Nachrichten) > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	jetzt := time.Now().Format("Monday, 2. January 2006, 15:04")
	msgs := []map[string]any{{"role": "system", "content": "Du bist der KI-Assistent im Fundus-Netz, einem dezentralen " +
		"Marktplatz. Antworte in der Sprache der Frage, klar und eher knapp. Heute ist " + jetzt + "."}}
	gesamt, bilder := 0, 0
	for _, n := range req.Nachrichten {
		rolle := "user"
		if n.Rolle == "assistant" {
			rolle = "assistant"
		}
		if len(n.Text) > 16000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Eine Nachricht ist zu lang."})
			return
		}
		gesamt += len(n.Text)
		m := map[string]any{"role": rolle, "content": n.Text}
		if n.Bild != "" && rolle == "user" {
			if !s.cfg.LLMVision {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Bilder sind in Einstellungen → KI ausgeschaltet."})
				return
			}
			if bilder++; bilder > 4 || len(n.Bild) > 8<<20 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Höchstens 4 Bilder je Unterhaltung."})
				return
			}
			m["images"] = []string{n.Bild}
		}
		msgs = append(msgs, m)
	}
	if gesamt > 120000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Die Unterhaltung ist zu lang – bitte einen neuen Chat beginnen."})
		return
	}
	modell := s.kiModell(bilder > 0)
	anfrage := map[string]any{"model": modell, "messages": msgs, "stream": true,
		"keep_alive": s.cfg.LLMKeepAlive, "options": map[string]any{"temperature": 0.7, "num_ctx": 8192}}
	if llm.IstDenkmodell(modell) {
		anfrage["think"] = false
	}
	body, _ := json.Marshal(anfrage)
	// Der Kontext der Anfrage: bricht der Browser ab, bricht auch Ollama ab.
	ollamaReq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		strings.TrimRight(s.cfg.LLMURL, "/")+"/api/chat", bytes.NewReader(body))
	ollamaReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(ollamaReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Ollama nicht erreichbar unter " + s.cfg.LLMURL + "."})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Ollama: " + e.Error})
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no") // nginx: nicht puffern, sonst kommt alles erst am Ende
	c.Status(http.StatusOK)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var teil struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &teil) != nil {
			continue
		}
		if teil.Error != "" {
			fmt.Fprintf(c.Writer, "\n\n[Fehler: %s]", teil.Error)
			c.Writer.Flush()
			return
		}
		if teil.Message.Content != "" {
			_, _ = c.Writer.WriteString(teil.Message.Content)
			c.Writer.Flush()
		}
		if teil.Done {
			return
		}
	}
}

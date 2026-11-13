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
	"context"
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
	s.router.GET("/api/v1/ki/bild/:datei", s.kiBildAusliefern) // R873
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
		"bildmodell": s.kiModell(true), "fotos": s.cfg.LLMVision, "websuche": s.kiSucheLesen().aktiv(), "bilder": s.kiBildLesen().aktiv()})
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
	suche := s.kiSucheLesen()
	system := "Du bist der KI-Assistent im Fundus-Netz, einem dezentralen Marktplatz. Antworte in der Sprache der " +
		"Frage, klar und eher knapp. Heute ist " + jetzt + "."
	if suche.aktiv() {
		system += " Du kannst mit dem Werkzeug websuche im Internet suchen und mit seite_lesen eine gefundene Seite " +
			"lesen. Nutze die Suche für alles, was sich seit deinem Training geändert haben kann – Nachrichten, Preise, " +
			"Versionen, Termine, wer ein Amt innehat – und stütze dich dann auf die Ergebnisse statt auf dein Gedächtnis. " +
			"Liste keine Quellen auf, die Oberfläche zeigt sie an."
	}
	bild := s.kiBildLesen()
	if bild.aktiv() {
		system += " Mit dem Werkzeug bild_erzeugen kannst du ein Bild malen lassen – nur wenn der Nutzer ausdrücklich " +
			"ein Bild möchte. Schreib die Beschreibung auf Englisch und konkret: Motiv, Stil, Licht, Perspektive. Das " +
			"Bild sieht der Nutzer direkt; beschreib es danach nicht noch einmal ausführlich."
	}
	var werkzeuge []map[string]any
	if suche.aktiv() {
		werkzeuge = append(werkzeuge, kiWerkzeuge...)
	}
	if bild.aktiv() {
		werkzeuge = append(werkzeuge, kiBildWerkzeug)
	}
	msgs := []map[string]any{{"role": "system", "content": system}}
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
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no") // nginx: nicht puffern, sonst kommt alles erst am Ende
	gestartet := false
	start := func() {
		if !gestartet {
			c.Status(http.StatusOK)
			gestartet = true
		}
	}
	var quellen []kiTreffer
	runden, bilderGemalt := 1, 0
	if len(werkzeuge) > 0 {
		runden = 5 // bis zu 4 Werkzeug-Runden, dann die Antwort
	}
	for runde := 1; runde <= runden; runde++ {
		anfrage := map[string]any{"model": modell, "messages": msgs, "stream": true,
			"keep_alive": s.cfg.LLMKeepAlive, "options": map[string]any{"temperature": 0.7, "num_ctx": 8192}}
		if llm.IstDenkmodell(modell) {
			anfrage["think"] = false
		}
		if len(werkzeuge) > 0 && runde < runden {
			anfrage["tools"] = werkzeuge
		}
		inhalt, aufrufe, fehler, ok := s.kiRunde(c, anfrage, start)
		if !ok {
			return // Fehler schon gemeldet oder Browser weg
		}
		if fehler != "" {
			start()
			fmt.Fprintf(c.Writer, "\n\n[Fehler: %s]", fehler)
			c.Writer.Flush()
			return
		}
		if len(aufrufe) == 0 {
			break
		}
		// Werkzeuge ausführen (höchstens 3 je Runde) und weiterschreiben lassen.
		msgs = append(msgs, map[string]any{"role": "assistant", "content": inhalt, "tool_calls": aufrufe})
		for i, a := range aufrufe {
			if i >= 3 {
				break
			}
			name, args := kiAufruf(a)
			start()
			var ergebnis string
			frist := 20 * time.Second
			if name == "bild_erzeugen" {
				frist = 5 * time.Minute
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), frist)
			switch name {
			case "bild_erzeugen":
				beschr := kiKuerzen(fmt.Sprint(args["beschreibung"]), 900)
				if !bild.aktiv() || bilderGemalt >= 2 {
					ergebnis = "Bilderzeugung nicht verfügbar."
					break
				}
				bilderGemalt++
				kiSignal(c, gin.H{"malt": kiKuerzen(beschr, 160)})
				id, err := s.kiBildErzeugen(ctx, bild, beschr, fmt.Sprint(args["format"]), modell)
				if err != nil {
					ergebnis = "Bild konnte nicht erzeugt werden: " + err.Error()
					kiSignal(c, gin.H{"hinweis": ergebnis})
				} else {
					kiSignal(c, gin.H{"bild": gin.H{"url": "/api/v1/ki/bild/" + id + ".png", "text": beschr}})
					ergebnis = "Das Bild wurde erzeugt und dem Nutzer angezeigt."
				}
			case "websuche":
				q := kiKuerzen(fmt.Sprint(args["anfrage"]), 200)
				kiSignal(c, gin.H{"suche": q})
				t, err := s.kiSuchen(ctx, suche, q)
				if err != nil {
					ergebnis = "Suche fehlgeschlagen: " + err.Error()
					kiSignal(c, gin.H{"hinweis": ergebnis})
				} else {
					var sb strings.Builder
					for j, tr := range t {
						fmt.Fprintf(&sb, "[%d] %s\n%s\n%s\n\n", j+1, tr.Titel, tr.URL, tr.Text)
						quellen = kiQuelleMerken(quellen, tr)
					}
					if len(t) == 0 {
						sb.WriteString("Keine Treffer.")
					}
					ergebnis = sb.String()
				}
			case "seite_lesen":
				u := fmt.Sprint(args["url"])
				kiSignal(c, gin.H{"liest": kiKuerzen(u, 200)})
				titel, text, err := s.kiSeiteLesen(ctx, suche, u)
				if err != nil {
					ergebnis = "Seite nicht lesbar: " + err.Error()
				} else {
					ergebnis = titel + "\n\n" + text
					quellen = kiQuelleMerken(quellen, kiTreffer{Titel: titel, URL: u})
				}
			default:
				ergebnis = "Unbekanntes Werkzeug."
			}
			cancel()
			msgs = append(msgs, map[string]any{"role": "tool", "tool_name": name, "content": ergebnis})
		}
	}
	start()
	if len(quellen) > 0 {
		kiSignal(c, gin.H{"quellen": quellen})
	}
}

// Werkzeuge für das Modell – bewusst nur zwei, gut beschrieben: kleine
// Modelle wählen bei wenigen Werkzeugen deutlich zuverlässiger.
var kiWerkzeuge = []map[string]any{
	{"type": "function", "function": map[string]any{
		"name":        "websuche",
		"description": "Sucht im Internet nach aktuellen Informationen und liefert Titel, Adresse und Auszug der besten Treffer.",
		"parameters": map[string]any{"type": "object", "required": []string{"anfrage"},
			"properties": map[string]any{"anfrage": map[string]any{"type": "string", "description": "Suchbegriffe, kurz und gezielt"}}},
	}},
	{"type": "function", "function": map[string]any{
		"name":        "seite_lesen",
		"description": "Liest den Text einer Webseite, etwa eines Suchtreffers, wenn der Auszug nicht reicht.",
		"parameters": map[string]any{"type": "object", "required": []string{"url"},
			"properties": map[string]any{"url": map[string]any{"type": "string", "description": "vollständige Adresse mit https://"}}},
	}},
}

var kiBildWerkzeug = map[string]any{"type": "function", "function": map[string]any{
	"name":        "bild_erzeugen",
	"description": "Malt ein Bild nach einer Beschreibung und zeigt es dem Nutzer. Nur verwenden, wenn ausdrücklich ein Bild gewünscht ist.",
	"parameters": map[string]any{"type": "object", "required": []string{"beschreibung"},
		"properties": map[string]any{
			"beschreibung": map[string]any{"type": "string", "description": "englische Bildbeschreibung: Motiv, Stil, Licht, Perspektive"},
			"format":       map[string]any{"type": "string", "enum": []string{"quadrat", "hoch", "quer"}, "description": "Bildformat"},
		}},
}}

// kiSignal: Steuerbotschaft an die Seite, eingerahmt von ASCII 0x1E – kommt
// in normalem Text nicht vor.
func kiSignal(c *gin.Context, v any) {
	b, _ := json.Marshal(v)
	_, _ = c.Writer.WriteString("\x1e" + string(b) + "\x1e")
	c.Writer.Flush()
}

func kiQuelleMerken(l []kiTreffer, t kiTreffer) []kiTreffer {
	for _, q := range l {
		if q.URL == t.URL {
			return l
		}
	}
	if len(l) >= 8 {
		return l
	}
	return append(l, kiTreffer{Titel: kiKuerzen(t.Titel, 120), URL: t.URL})
}

func kiAufruf(a map[string]any) (string, map[string]any) {
	f, _ := a["function"].(map[string]any)
	name, _ := f["name"].(string)
	args, _ := f["arguments"].(map[string]any)
	if args == nil {
		// Manche Modelle liefern die Argumente als JSON-Text.
		if t, ok := f["arguments"].(string); ok {
			_ = json.Unmarshal([]byte(t), &args)
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	return name, args
}

// kiRunde: eine gestreamte Anfrage an Ollama. Text geht sofort an den Browser,
// Werkzeugaufrufe werden gesammelt. ok=false: Antwort schon geschrieben.
func (s *Server) kiRunde(c *gin.Context, anfrage map[string]any, start func()) (string, []map[string]any, string, bool) {
	body, _ := json.Marshal(anfrage)
	// Der Kontext der Anfrage: bricht der Browser ab, bricht auch Ollama ab.
	req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		strings.TrimRight(s.cfg.LLMURL, "/")+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		if c.Request.Context().Err() != nil {
			return "", nil, "", false
		}
		return "", nil, "Ollama nicht erreichbar unter " + s.cfg.LLMURL, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if strings.Contains(e.Error, "does not support tools") {
			return "", nil, "Dieses Modell kann keine Werkzeuge nutzen – für die Websuche ein anderes wählen (z.B. qwen3.5:9b).", true
		}
		return "", nil, "Ollama: " + e.Error, true
	}
	var inhalt strings.Builder
	var aufrufe []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var teil struct {
			Message struct {
				Content   string           `json:"content"`
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &teil) != nil {
			continue
		}
		if teil.Error != "" {
			return inhalt.String(), nil, teil.Error, true
		}
		if teil.Message.Content != "" {
			start()
			inhalt.WriteString(teil.Message.Content)
			_, _ = c.Writer.WriteString(teil.Message.Content)
			c.Writer.Flush()
		}
		aufrufe = append(aufrufe, teil.Message.ToolCalls...)
		if teil.Done {
			break
		}
	}
	return inhalt.String(), aufrufe, "", true
}

package api

// /bild-Befehl im KI-Chat (R942): malt immer, auch wenn das Sprachmodell
// Werkzeuge nicht zuverlässig aufruft. Das Sprachmodell formuliert nur die
// englische Bildbeschreibung (eine Anfrage ohne Werkzeuge). Lehnt es ab, geht
// im Heimnetz die eigene Beschreibung unverändert ans Bildmodell; von außen
// bleibt es bei der Ablehnung.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fundus/node/internal/llm"
	"github.com/gin-gonic/gin"
)

// kiBildBefehl: "/bild [hoch|quer|quadrat] <Beschreibung>".
func kiBildBefehl(text string) (wunsch, format string, ok bool) {
	t := strings.TrimSpace(text)
	if len(t) < 5 || !strings.EqualFold(t[:5], "/bild") {
		return "", "", false
	}
	rest := t[5:]
	if rest != "" && rest[0] != ' ' && rest[0] != ':' && rest[0] != '\n' {
		return "", "", false // z. B. "/bilder"
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	format = "quadrat"
	for _, f := range []string{"hoch", "quer", "quadrat"} {
		if l := strings.ToLower(rest); strings.HasPrefix(l, f+" ") || strings.HasPrefix(l, f+":") {
			format = f
			rest = strings.TrimSpace(strings.TrimPrefix(rest[len(f):], ":"))
			break
		}
	}
	return rest, format, true
}

var reKIAblehnung = regexp.MustCompile(`(?i)\b(i can(no|')t|i'?m (sorry|unable)|i am (sorry|unable)|unable to|as an ai|ich kann (das |dir )?(nicht|keine)|kann ich (dir )?nicht|tut mir leid|nicht (erstellen|erzeugen|helfen)|leider (nicht|kann))`)

func kiIstAblehnung(t string) bool { return reKIAblehnung.MatchString(t) }

const kiPromptSystem = "You write prompts for an image generator. Turn the user's request (any language) into ONE detailed " +
	"English prompt: subject, style, lighting, perspective, composition. For people, figures and statues state pose, " +
	"posture, anatomically correct proportions and material explicitly. Output only the prompt – no quotes, no " +
	"explanation, no preface."

// kiBildPrompt: Sprachmodell formuliert den Bild-Prompt (ohne Werkzeuge).
func (s *Server) kiBildPrompt(ctx context.Context, modell, wunsch string) string {
	anfrage := map[string]any{"model": modell, "stream": false, "keep_alive": s.cfg.LLMKeepAlive,
		"messages": []map[string]any{{"role": "system", "content": kiPromptSystem}, {"role": "user", "content": wunsch}},
		"options":  map[string]any{"temperature": 0.4, "num_ctx": 4096}}
	if llm.IstDenkmodell(modell) {
		anfrage["think"] = false
	}
	b, _ := json.Marshal(anfrage)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.LLMURL, "/")+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var a struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a) != nil {
		return ""
	}
	p := strings.TrimSpace(a.Message.Content)
	p = strings.Trim(p, "\"'`„“ ")
	return kiKuerzen(p, 900)
}

// kiBildDirekt beantwortet einen /bild-Befehl.
func (s *Server) kiBildDirekt(c *gin.Context, start func(), bild kiBildConfig, wunsch, format, modell string) {
	start()
	if !bild.aktiv() {
		_, _ = c.Writer.WriteString("Die Bilderzeugung ist nicht eingerichtet (Einstellungen → KI → Bilderzeugung).")
		return
	}
	if strings.TrimSpace(wunsch) == "" {
		_, _ = c.Writer.WriteString("Schreib hinter /bild, was gemalt werden soll – z. B. „/bild hoch Marmorstatue eines Athleten im Kontrapost“.")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()
	kiSignal(c, gin.H{"formuliert": true})
	prompt := s.kiBildPrompt(ctx, modell, wunsch)
	switch {
	case prompt == "":
		prompt = kiKuerzen(wunsch, 900)
	case kiIstAblehnung(prompt):
		if !isLANRequest(c) {
			_, _ = c.Writer.WriteString(prompt)
			return
		}
		kiSignal(c, gin.H{"hinweis": "Das Sprachmodell hat abgelehnt – deine Beschreibung geht unverändert ans Bildmodell."})
		prompt = kiKuerzen(wunsch, 900)
	}
	kiSignal(c, gin.H{"malt": kiKuerzen(prompt, 160)})
	id, err := s.kiBildErzeugen(ctx, bild, prompt, format, modell)
	if err != nil {
		_, _ = c.Writer.WriteString("Bild konnte nicht erzeugt werden: " + err.Error())
		return
	}
	kiSignal(c, gin.H{"bild": gin.H{"url": "/api/v1/ki/bild/" + id + ".png", "text": prompt}})
}

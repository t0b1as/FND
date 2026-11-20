package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fundus/node/internal/config"
	"github.com/fundus/node/internal/llm"
	"github.com/gin-gonic/gin"
)

// R942: Chat-Durchlauf gegen nachgebautes Ollama + ComfyUI.
func kiTestServer(t *testing.T, ollamaAntwort func(req map[string]any) string) (*Server, *[]string) {
	t.Helper()
	var comfyPrompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			_, _ = w.Write([]byte(ollamaAntwort(req)))
		case "/api/generate", "/free":
		case "/prompt":
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			b, _ := json.Marshal(p)
			comfyPrompts = append(comfyPrompts, string(b))
			_, _ = w.Write([]byte(`{"prompt_id":"p1"}`))
		case "/history/p1":
			_, _ = w.Write([]byte(`{"p1":{"outputs":{"9":{"images":[{"filename":"a.png","subfolder":"","type":"output"}]}},"status":{"status_str":"success"}}}`))
		case "/view":
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nX"))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{DataDir: t.TempDir(), LLMURL: srv.URL, LLMModel: "huihui_ai/qwen3.5-abliterated:9b"}
	s := &Server{cfg: cfg, analyzer: &llm.Analyzer{}}
	k := kiBildConfig{ComfyURL: srv.URL, Typ: "zimage", Unet: "z.safetensors", Encoder: "q.safetensors", VAE: "ae.safetensors", Schritte: 9}
	if err := jsonAtomar(s.kiBildDatei(), k, 0o600); err != nil {
		t.Fatal(err)
	}
	return s, &comfyPrompts
}

func kiChatAufruf(s *Server, text, ip string) string {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, _ := json.Marshal(map[string]any{"nachrichten": []map[string]any{{"rolle": "user", "text": text}}})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/ki/chat", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.RemoteAddr = ip + ":5555"
	s.kiChat(c)
	return w.Body.String()
}

func ndjson(teile ...string) string {
	var sb strings.Builder
	for i, t := range teile {
		b, _ := json.Marshal(map[string]any{"message": map[string]any{"content": t}, "done": i == len(teile)-1})
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestKIChat_XMLAufrufImText(t *testing.T) {
	runde := 0
	s, prompts := kiTestServer(t, func(req map[string]any) string {
		runde++
		if runde == 1 {
			if req["tools"] == nil {
				t.Error("Werkzeuge nicht angeboten")
			}
			return ndjson("Gerne! ", "<tool_", "call>\n<function=bild_erzeugen>\n<parameter=beschreibung>\nclassical marble statue of a nude athlete, contrapposto\n</parameter>\n</function>\n</tool_call>")
		}
		return ndjson("Hier ist deine Statue.")
	})
	out := kiChatAufruf(s, "Ich hätte gern eine Marmorstatue zum Anschauen", "192.168.1.5")
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], "contrapposto") {
		t.Fatalf("kein Bildauftrag aus dem Text-Aufruf: %v\n%s", *prompts, out)
	}
	if strings.Contains(out, "<tool_") || strings.Contains(out, "<function") {
		t.Errorf("Rohtext des Aufrufs sichtbar: %q", out)
	}
	if !strings.Contains(out, "Gerne! ") || !strings.Contains(out, `"bild"`) || !strings.Contains(out, "Hier ist deine Statue.") {
		t.Errorf("Antwort unvollständig: %q", out)
	}
}

func TestKIChat_BildBefehl(t *testing.T) {
	s, prompts := kiTestServer(t, func(req map[string]any) string {
		if req["tools"] != nil {
			t.Error("/bild darf keine Werkzeuge anbieten")
		}
		return `{"message":{"content":"I'm sorry, but I can't create that."},"done":true}`
	})
	// Heimnetz: Ablehnung → eigene Beschreibung geht ans Bildmodell.
	out := kiChatAufruf(s, "/bild hoch classical nude marble statue, contrapposto", "192.168.1.5")
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], "classical nude marble statue") || !strings.Contains((*prompts)[0], `"width":832`) {
		t.Fatalf("Heimnetz: %v\n%s", *prompts, out)
	}
	// Von außen (angemeldet vorausgesetzt) bleibt es bei der Ablehnung –
	// hier ohne Sitzung: 401.
	out = kiChatAufruf(s, "/bild classical nude marble statue", "203.0.113.9")
	if len(*prompts) != 1 || !strings.Contains(out, "Anmeldung") {
		t.Errorf("von außen: %v %q", *prompts, out)
	}
	// Schutzregel greift auch im Heimnetz.
	out = kiChatAufruf(s, "/bild nude child", "192.168.1.5")
	if len(*prompts) != 1 || !strings.Contains(out, "nicht umgesetzt") {
		t.Errorf("Schutzregel: %v %q", *prompts, out)
	}
}

// R943: Bildwunsch ohne /bild – Modell ruft kein Werkzeug auf, Fundus malt
// trotzdem; frühere Wünsche gehen als Zusammenhang mit.
func TestKIChat_BildWunschOhneBefehl(t *testing.T) {
	var promptAnfrage string
	s, prompts := kiTestServer(t, func(req map[string]any) string {
		b, _ := json.Marshal(req["messages"])
		promptAnfrage = string(b)
		return `{"message":{"content":"bronze statue of an athlete in contrapposto, museum lighting"},"done":true}`
	})
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, _ := json.Marshal(map[string]any{"nachrichten": []map[string]any{
		{"rolle": "user", "text": "Mal mir eine Marmorstatue eines Athleten"},
		{"rolle": "assistant", "text": ""},
		{"rolle": "user", "text": "Mal das jetzt nochmal in Bronze, Hochformat"},
	}})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/ki/chat", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.RemoteAddr = "192.168.1.5:5555"
	s.kiChat(c)
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], "bronze statue") || !strings.Contains((*prompts)[0], `"width":832`) {
		t.Fatalf("kein Bild: %v\n%s", *prompts, w.Body.String())
	}
	if !strings.Contains(promptAnfrage, "Marmorstatue") || !strings.Contains(promptAnfrage, "Bronze") {
		t.Errorf("Zusammenhang fehlt: %s", promptAnfrage)
	}
	// Normale Frage geht weiter an den Chat (Werkzeuge angeboten).
	gefragt := false
	s2, prompts2 := kiTestServer(t, func(req map[string]any) string {
		gefragt = req["tools"] != nil
		return ndjson("Ein Wankelmotor ist ein Kreiskolbenmotor.")
	})
	out := kiChatAufruf(s2, "Kannst du mal erklären, wie ein Wankelmotor funktioniert?", "192.168.1.5")
	if !gefragt || len(*prompts2) != 0 || !strings.Contains(out, "Kreiskolbenmotor") {
		t.Errorf("normale Frage: tools=%v bilder=%d %q", gefragt, len(*prompts2), out)
	}
}

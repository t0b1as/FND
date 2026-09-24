package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/llm"
)

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

// fakeOllama startet einen httptest-Server der eine feste Antwort zurückgibt.
func fakeOllama(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/generate":
			json.NewEncoder(w).Encode(map[string]any{
				"response": response,
				"done":     true,
			})
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
		case "/api/pull":
			json.NewEncoder(w).Encode(map[string]any{"status": "success"})
		default:
			http.NotFound(w, r)
		}
	}))
}

// validAnalysisJSON ist ein gültiges Analyse-Ergebnis als JSON-String.
const validAnalysisJSON = `{
  "condition":    "gut",
  "description":  "Bosch Akkuschrauber 18V, leicht gebraucht",
  "listing_text": "Gut erhaltener Bosch Akkuschrauber. Alle Funktionen intakt.",
  "price_min":    35.0,
  "price_max":    55.0,
  "category":     "Werkzeug",
  "keywords":     ["Akkuschrauber", "Bosch", "18V"]
}`

// =============================================================================
//  Ping
// =============================================================================

func TestAnalyzer_Ping_Success(t *testing.T) {
	srv := fakeOllama(t, "")
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := a.Ping(ctx); err != nil {
		t.Errorf("Ping should succeed against fake Ollama: %v", err)
	}
}

func TestAnalyzer_Ping_Unreachable(t *testing.T) {
	a := llm.New("http://127.0.0.1:19999", "model", "model", zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := a.Ping(ctx); err == nil {
		t.Error("Ping should fail for unreachable server")
	}
}

// =============================================================================
//  Analyze – Grundfälle
// =============================================================================

func TestAnalyzer_Analyze_ValidJSON(t *testing.T) {
	srv := fakeOllama(t, validAnalysisJSON)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())

	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{
		VoiceText: "Bosch Akkuschrauber, sehr gut erhalten",
		Language:  "de",
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if result.Condition != "gut" {
		t.Errorf("Condition = %q, want %q", result.Condition, "gut")
	}
	if result.Category != "Werkzeug" {
		t.Errorf("Category = %q, want %q", result.Category, "Werkzeug")
	}
	if result.PriceMin != 35.0 {
		t.Errorf("PriceMin = %.2f, want 35.0", result.PriceMin)
	}
	if result.PriceMax != 55.0 {
		t.Errorf("PriceMax = %.2f, want 55.0", result.PriceMax)
	}
	if len(result.Keywords) != 3 {
		t.Errorf("Keywords len = %d, want 3", len(result.Keywords))
	}
	if result.ListingText == "" {
		t.Error("ListingText must not be empty")
	}
}

func TestAnalyzer_Analyze_WithImages(t *testing.T) {
	srv := fakeOllama(t, validAnalysisJSON)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())

	// Minimales 1x1 JPEG (gültiger JPEG-Header)
	minimalJPEG := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00,
	}

	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{
		Images:    [][]byte{minimalJPEG},
		VoiceText: "Werkzeug gut erhalten",
		Language:  "de",
	})
	if err != nil {
		t.Fatalf("Analyze with image: %v", err)
	}
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestAnalyzer_Analyze_EmptyRequest_Errors(t *testing.T) {
	srv := fakeOllama(t, validAnalysisJSON)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())

	_, err := a.Analyze(context.Background(), llm.AnalyzeRequest{})
	if err == nil {
		t.Error("Empty request should return error")
	}
}

// =============================================================================
//  parseResponse – JSON-Extraktion aus LLM-Antworten
// =============================================================================

// parseResponse ist unexported, daher testen wir sie indirekt
// über Analyze mit manipulierten Antworten.

func TestAnalyzer_Analyze_JSONWithPreamble(t *testing.T) {
	// LLMs fügen manchmal Text vor dem JSON ein
	preamble := "Hier ist meine Analyse:\n\n" + validAnalysisJSON

	srv := fakeOllama(t, preamble)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err != nil {
		t.Fatalf("Should parse JSON even with preamble: %v", err)
	}
	if result.Condition != "gut" {
		t.Errorf("Condition = %q, want %q", result.Condition, "gut")
	}
}

func TestAnalyzer_Analyze_JSONWithMarkdown(t *testing.T) {
	wrapped := "```json\n" + validAnalysisJSON + "\n```"

	srv := fakeOllama(t, wrapped)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err != nil {
		t.Fatalf("Should parse JSON from markdown block: %v", err)
	}
	if result.PriceMin != 35.0 {
		t.Errorf("PriceMin = %.2f, want 35.0", result.PriceMin)
	}
}

func TestAnalyzer_Analyze_RetryOnInvalidJSON(t *testing.T) {
	// Erste Antwort: kein JSON. Zweite Antwort (Retry): gültiges JSON.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var resp string
		if callCount == 1 {
			resp = "Der Artikel sieht gut aus, Preis etwa 45 EUR."
		} else {
			resp = validAnalysisJSON
		}
		json.NewEncoder(w).Encode(map[string]any{"response": resp, "done": true})
	}))
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err != nil {
		t.Fatalf("Should succeed after retry: %v", err)
	}
	if callCount < 2 {
		t.Errorf("Expected at least 2 Ollama calls (initial + retry), got %d", callCount)
	}
	if result.Condition == "" {
		t.Error("Condition should be set after retry")
	}
}

func TestAnalyzer_Analyze_BothCallsFail_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": "Leider kann ich das nicht analysieren.",
			"done":     true,
		})
	}))
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	_, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err == nil {
		t.Error("Expected error when both calls return invalid JSON")
	}
}

func TestAnalyzer_Analyze_OllamaError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"error": "model not found",
		})
	}))
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	_, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err == nil {
		t.Error("Expected error for Ollama error response")
	}
}

// =============================================================================
//  Sanity-Checks auf AnalysisResult-Felder
// =============================================================================

func TestAnalyzer_Analyze_PriceMaxAtLeastPriceMin(t *testing.T) {
	// Modell gibt price_max < price_min zurück → muss korrigiert werden
	broken := `{
		"condition":"gut","description":"test","listing_text":"text",
		"price_min":100,"price_max":50,
		"category":"Sonstiges","keywords":[]
	}`

	srv := fakeOllama(t, broken)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.PriceMax < result.PriceMin {
		t.Errorf("PriceMax (%.2f) must be >= PriceMin (%.2f)", result.PriceMax, result.PriceMin)
	}
}

func TestAnalyzer_Analyze_EmptyCondition_GetsDefault(t *testing.T) {
	noCondition := `{
		"condition":"","description":"test","listing_text":"text",
		"price_min":10,"price_max":20,"category":"Sonstiges","keywords":["a"]
	}`

	srv := fakeOllama(t, noCondition)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Condition == "" {
		t.Error("Empty condition should get a default value")
	}
}

func TestAnalyzer_Analyze_NilKeywords_GetsEmptySlice(t *testing.T) {
	noKeywords := `{
		"condition":"gut","description":"test","listing_text":"text",
		"price_min":10,"price_max":20,"category":"Sonstiges"
	}`

	srv := fakeOllama(t, noKeywords)
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	result, err := a.Analyze(context.Background(), llm.AnalyzeRequest{VoiceText: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Keywords == nil {
		t.Error("Keywords must not be nil (should be empty slice)")
	}
}

// =============================================================================
//  Spracherkennung im Analyze-Aufruf
// =============================================================================

func TestAnalyzer_Analyze_LanguagePassedToPrompt(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			capturedBody = make([]byte, r.ContentLength)
			r.Body.Read(capturedBody)
		}
		json.NewEncoder(w).Encode(map[string]any{"response": validAnalysisJSON, "done": true})
	}))
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())
	_, _ = a.Analyze(context.Background(), llm.AnalyzeRequest{
		VoiceText: "great item",
		Language:  "en",
	})

	// Der Prompt sollte englische Schlüsselwörter enthalten
	if !strings.Contains(string(capturedBody), "expert") &&
		!strings.Contains(string(capturedBody), "marketplace") {
		t.Error("EN language should produce English prompt in Ollama request")
	}
}

// =============================================================================
//  Context-Cancellation
// =============================================================================

func TestAnalyzer_Analyze_ContextCancelled(t *testing.T) {
	// Server antwortet sehr langsam
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		json.NewEncoder(w).Encode(map[string]any{"response": validAnalysisJSON, "done": true})
	}))
	defer srv.Close()

	a := llm.New(srv.URL, "moondream2", "llama3.2:1b", zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := a.Analyze(ctx, llm.AnalyzeRequest{VoiceText: "test"})
	if err == nil {
		t.Error("Expected error for cancelled context")
	}
}

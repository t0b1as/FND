// Package llm kommuniziert mit einem lokal laufenden Ollama-Dienst.
// Bildmodell (Standard "moondream", ~1,8 GB) nur mit genug RAM (Pi 4/5 ab 4 GB).
// Reine Textanalysen nutzen standardmäßig DASSELBE Modell (nur eins im Speicher);
// ein eigenes Textmodell ist per FUNDUS_LLM_TEXT_MODEL möglich.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// AnalysisResult enthält das strukturierte Ergebnis einer LLM-Analyse.
type AnalysisResult struct {
	Condition   string   `json:"condition"`
	Description string   `json:"description"`
	ListingText string   `json:"listing_text"`
	PriceMin    float64  `json:"price_min"`
	PriceMax    float64  `json:"price_max"`
	Category    string   `json:"category"`
	Keywords    []string `json:"keywords"`

	// Aus Preisrecherche angereichert
	Brand         string        `json:"brand,omitempty"`
	Model         string        `json:"model,omitempty"`
	EAN           string        `json:"ean,omitempty"`
	PriceMarket   float64       `json:"price_market,omitempty"`  // Marktmittelwert
	PriceSources  []PriceSource `json:"price_sources,omitempty"` // Quellendetails
	PriceSearched bool          `json:"price_searched"`
}

// Analyzer führt LLM-Anfragen gegen Ollama aus.
type Analyzer struct {
	baseURL     string
	model       string
	textModel   string
	vision      bool   // Fotos an das Bildmodell schicken?
	keepAlive   string // Ollama keep_alive (Speicher nach Gebrauch freigeben)
	httpClient  *http.Client
	priceSearch *ProductSearcher
	log         *zap.Logger
}

// Configure: Fotoanalyse an/aus und wie lange Ollama das Modell hält.
func (a *Analyzer) Configure(vision bool, keepAlive string) {
	a.vision = vision
	if keepAlive != "" {
		a.keepAlive = keepAlive
	}
}

// New erstellt einen Analyzer.
func New(baseURL, model, textModel string, log *zap.Logger) *Analyzer {
	if baseURL == ""   { baseURL   = "http://127.0.0.1:11434" }
	if model == ""     { model     = "moondream" }
	if textModel == "" { textModel = model } // ein Modell für beides: spart Speicher

	return &Analyzer{
		baseURL:     baseURL,
		model:       model,
		textModel:   textModel,
		vision:      true,
		keepAlive:   "1m",
		log:         log,
		priceSearch: NewProductSearcher(log),
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// AnalyzeRequest enthält alles was der User eingegeben hat.
type AnalyzeRequest struct {
	Images      [][]byte // rohe Bild-Bytes (JPEG/PNG)
	VoiceText   string   // Sprach-Transkript vom Frontend
	Language    string   // "de" oder "en"
}

// Analyze analysiert Bilder + Sprachkommentar und gibt einen strukturierten
// Listing-Vorschlag zurück.
func (a *Analyzer) Analyze(ctx context.Context, req AnalyzeRequest) (*AnalysisResult, error) {
	if len(req.Images) == 0 && req.VoiceText == "" {
		return nil, fmt.Errorf("weder Bilder noch Sprachtext vorhanden")
	}

	lang := req.Language
	if lang == "" {
		lang = "de"
	}

	// Bilder als Base64 kodieren
	b64Images := make([]string, 0, len(req.Images))
	for _, img := range req.Images {
		b64Images = append(b64Images, base64.StdEncoding.EncodeToString(img))
	}

	// Prompt zusammenbauen (zentralisiert in prompt.go)
	prompt := ListingPrompt(req.VoiceText, len(b64Images), ParseLang(lang))

	a.log.Info("Starting LLM analysis",
		zap.Int("images", len(b64Images)),
		zap.Int("voiceTextLen", len(req.VoiceText)),
		zap.String("model", a.model),
	)

	// Ollama anfragen
	var rawResponse string
	var err error

	if len(b64Images) > 0 && a.vision {
		rawResponse, err = a.callOllama(ctx, a.model, prompt, b64Images)
		// Modell kann keine Bilder (reines Textmodell eingetragen) oder ist zu
		// groß → nur den Text auswerten, statt ganz zu scheitern.
		if err != nil && req.VoiceText != "" {
			a.log.Warn("Bildanalyse fehlgeschlagen – werte nur den Text aus", zap.String("model", a.model), zap.Error(err))
			rawResponse, err = a.callOllama(ctx, a.textModel, ListingPrompt(req.VoiceText, 0, ParseLang(lang)), nil)
		}
	} else {
		if req.VoiceText == "" {
			return nil, fmt.Errorf("Fotoanalyse ist auf diesem Node abgeschaltet (zu wenig RAM für ein Bildmodell) – bitte eine Beschreibung oder einen Sprachkommentar angeben")
		}
		// Kein Bild bzw. keine Fotoanalyse → reines Textmodell
		rawResponse, err = a.callOllama(ctx, a.textModel, ListingPrompt(req.VoiceText, 0, ParseLang(lang)), nil)
	}

	if err != nil {
		return nil, fmt.Errorf("ollama call failed: %w", err)
	}

	a.log.Debug("LLM raw response", zap.String("response", rawResponse[:min(len(rawResponse), 200)]))

	// JSON aus der Antwort extrahieren
	result, err := parseResponse(rawResponse)
	if err != nil {
		a.log.Warn("JSON parse failed, retrying with structured prompt", zap.Error(err))
		// Zweiter Versuch: expliziterer Prompt
		prompt2 := RetryPrompt(rawResponse, ParseLang(lang))
		rawResponse2, err2 := a.callOllama(ctx, a.textModel, prompt2, nil)
		if err2 != nil {
			return nil, fmt.Errorf("retry call failed: %w", err2)
		}
		result, err = parseResponse(rawResponse2)
		if err != nil {
			return nil, fmt.Errorf("could not parse LLM response: %w", err)
		}
	}

	a.log.Info("Analysis complete",
		zap.String("condition", result.Condition),
		zap.String("category", result.Category),
		zap.Float64("priceMin", result.PriceMin),
		zap.Float64("priceMax", result.PriceMax),
	)

	// Preisrecherche in externen Quellen – parallel zur Anzeige-Generierung
	// Suchbegriff: Hersteller + Modell (falls vom LLM erkannt) oder Keywords
	query := buildPriceQuery(result)
	if query != "" {
		priceCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		pr := a.priceSearch.Search(priceCtx, query, result.Category)
		if !pr.NotFound {
			result.PriceMarket  = pr.PriceMed
			result.PriceSources = pr.Sources
			// LLM-Preise mit Marktdaten verfeinern wenn LLM unsicher war
			if result.PriceMin <= 0 { result.PriceMin = pr.PriceMin * 0.75 }
			if result.PriceMax <= 0 { result.PriceMax = pr.PriceMax * 1.10 }
		}
		result.PriceSearched = true
	}

	return result, nil
}

// Ping prüft ob Ollama erreichbar ist und das Modell geladen werden kann.
func (a *Analyzer) Ping(ctx context.Context) error {
	url := a.baseURL + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ollama not reachable at %s: %w", a.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}
	return nil
}

// EnsureModel stellt sicher dass das Modell lokal verfügbar ist.
// Lädt es herunter falls nötig (dauert beim ersten Mal einige Minuten auf dem Pi).
func (a *Analyzer) EnsureModel(ctx context.Context) error {
	models := []string{a.textModel}
	if a.vision && a.model != a.textModel {
		models = append(models, a.model) // Bildmodell nur, wenn Fotoanalyse an ist und es ein anderes ist
	}
	for _, model := range models {
		a.log.Info("Ensuring model is available", zap.String("model", model))
		if err := a.pullModel(ctx, model); err != nil {
			a.log.Warn("Model pull failed", zap.String("model", model), zap.Error(err))
		}
	}
	return nil
}

// =============================================================================
//  Ollama API
// =============================================================================

type ollamaRequest struct {
	Model   string   `json:"model"`
	Prompt  string   `json:"prompt"`
	Images  []string `json:"images,omitempty"` // Base64-kodierte Bilder
	Stream  bool     `json:"stream"`
	KeepAlive string `json:"keep_alive,omitempty"` // Speicher nach Gebrauch freigeben (Pi 3: RAM knapp)
	Options struct {
		Temperature float64 `json:"temperature"`
		NumPredict  int     `json:"num_predict"`
	} `json:"options"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

type ollamaPullRequest struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream"`
}

func (a *Analyzer) callOllama(ctx context.Context, model, prompt string, images []string) (string, error) {
	body := ollamaRequest{
		Model:  model,
		Prompt: prompt,
		Images: images,
		Stream: false,
		KeepAlive: a.keepAlive,
	}
	body.Options.Temperature = 0.3  // niedrig = konsistentere Ausgaben
	body.Options.NumPredict  = 1024 // max Token

	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/api/generate", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(rawBody, &ollamaResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	if ollamaResp.Error != "" {
		return "", fmt.Errorf("ollama error: %s", ollamaResp.Error)
	}

	return ollamaResp.Response, nil
}

func (a *Analyzer) pullModel(ctx context.Context, model string) error {
	body := ollamaPullRequest{Name: model, Stream: false}
	data, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/api/pull", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Pull kann lange dauern – eigener Client ohne Timeout
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull returned status %d", resp.StatusCode)
	}
	return nil
}

// =============================================================================
//  Response-Parser
// =============================================================================

func parseResponse(raw string) (*AnalysisResult, error) {
	// JSON-Block aus der Antwort extrahieren (LLMs fügen manchmal Text davor ein)
	start := strings.Index(raw, "{")
	end   := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonStr := raw[start : end+1]

	var result AnalysisResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("JSON unmarshal failed: %w", err)
	}

	// Sanity checks
	if result.Condition == "" {
		result.Condition = "akzeptabel"
	}
	if result.PriceMax < result.PriceMin {
		result.PriceMax = result.PriceMin * 1.2
	}
	if len(result.Keywords) == 0 {
		result.Keywords = []string{}
	}

	return &result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildPriceQuery baut einen Suchbegriff für die Preisrecherche.
// Nutzt Brand+Model wenn vorhanden, sonst Keywords.
func buildPriceQuery(r *AnalysisResult) string {
	parts := make([]string, 0, 3)
	if r.Brand != "" { parts = append(parts, r.Brand) }
	if r.Model != "" { parts = append(parts, r.Model) }
	if len(parts) >= 2 {
		return strings.Join(parts, " ")
	}
	// Fallback: erste 3 Keywords
	if len(r.Keywords) > 0 {
		kw := r.Keywords
		if len(kw) > 3 { kw = kw[:3] }
		return strings.Join(kw, " ")
	}
	return ""
}

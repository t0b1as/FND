package llm_test

import (
	"strings"
	"testing"

	"github.com/fundus/node/internal/llm"
)

// =============================================================================
//  ParseLang
// =============================================================================

func TestParseLang(t *testing.T) {
	tests := []struct {
		input string
		want  llm.Lang
	}{
		// Englisch – alle Varianten
		{"en",    llm.LangEN},
		{"EN",    llm.LangEN},
		{"en-US", llm.LangEN},
		{"en-GB", llm.LangEN},
		{"  en  ", llm.LangEN},
		// Deutsch – alle unbekannten fallen auf DE zurück
		{"de",    llm.LangDE},
		{"DE",    llm.LangDE},
		{"de-AT", llm.LangDE},
		{"fr",    llm.LangDE}, // Unbekannt → Fallback DE
		{"",      llm.LangDE},
		{"zh",    llm.LangDE},
		{"ja",    llm.LangDE},
	}

	for _, tc := range tests {
		got := llm.ParseLang(tc.input)
		if got != tc.want {
			t.Errorf("ParseLang(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// =============================================================================
//  ListingPrompt
// =============================================================================

func TestListingPrompt_DE_ContainsExpectedFragments(t *testing.T) {
	prompt := llm.ListingPrompt("tolle Kamera, leichter Kratzer", 2, llm.LangDE)

	must := []string{
		"JSON",
		"condition",
		"listing_text",
		"price_min",
		"price_max",
		"keywords",
		"tolle Kamera",
		"neuwertig|gut|akzeptabel|defekt",
	}
	for _, s := range must {
		if !strings.Contains(prompt, s) {
			t.Errorf("DE prompt missing expected fragment %q", s)
		}
	}
}

func TestListingPrompt_EN_ContainsExpectedFragments(t *testing.T) {
	prompt := llm.ListingPrompt("great camera, minor scratch", 1, llm.LangEN)

	must := []string{
		"JSON",
		"condition",
		"listing_text",
		"price_min",
		"price_max",
		"keywords",
		"great camera",
		"new|good|acceptable|broken",
	}
	for _, s := range must {
		if !strings.Contains(prompt, s) {
			t.Errorf("EN prompt missing expected fragment %q", s)
		}
	}
}

// Ohne Bilder darf kein Bild-Hinweis im Prompt stehen
func TestListingPrompt_NoImages_NoImageReference(t *testing.T) {
	prompt := llm.ListingPrompt("kaputtes Radio", 0, llm.LangDE)
	if strings.Contains(prompt, "Bild") {
		t.Error("Prompt with 0 images should not reference 'Bild'")
	}
	if !strings.Contains(prompt, "kaputtes Radio") {
		t.Error("Voice text should appear in prompt")
	}
}

// Ohne Voice-Text kein Kommentar-Hinweis
func TestListingPrompt_NoVoiceText_NoCommentReference(t *testing.T) {
	prompt := llm.ListingPrompt("", 2, llm.LangDE)
	if strings.Contains(prompt, "Kommentar") {
		t.Error("Prompt with empty voice text should not reference 'Kommentar'")
	}
}

// Bilder UND Text – beides muss erscheinen
func TestListingPrompt_BothImageAndVoice(t *testing.T) {
	prompt := llm.ListingPrompt("leicht beschädigt", 3, llm.LangDE)
	if !strings.Contains(prompt, "3 Bild") {
		t.Error("Expected image count in prompt")
	}
	if !strings.Contains(prompt, "leicht beschädigt") {
		t.Error("Expected voice text in prompt")
	}
}

// Prompt darf nicht leer sein
func TestListingPrompt_NeverEmpty(t *testing.T) {
	for _, lang := range []llm.Lang{llm.LangDE, llm.LangEN} {
		for _, nImg := range []int{0, 1, 3} {
			for _, voice := range []string{"", "some text"} {
				p := llm.ListingPrompt(voice, nImg, lang)
				if len(p) < 50 {
					t.Errorf("Prompt too short (%d chars) for lang=%s img=%d voice=%q",
						len(p), lang, nImg, voice)
				}
			}
		}
	}
}

// DE und EN Prompts müssen verschieden sein
func TestListingPrompt_DEandEN_AreDifferent(t *testing.T) {
	de := llm.ListingPrompt("test", 1, llm.LangDE)
	en := llm.ListingPrompt("test", 1, llm.LangEN)
	if de == en {
		t.Error("DE and EN prompts should not be identical")
	}
}

// =============================================================================
//  RetryPrompt
// =============================================================================

func TestRetryPrompt_DE_ContainsInput(t *testing.T) {
	raw    := "Der Artikel sieht gut aus, Preis ca. 50 EUR"
	prompt := llm.RetryPrompt(raw, llm.LangDE)

	if !strings.Contains(prompt, raw) {
		t.Error("RetryPrompt DE should contain the raw response")
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("RetryPrompt DE should mention JSON")
	}
}

func TestRetryPrompt_EN_ContainsInput(t *testing.T) {
	raw    := "The item looks good, price around 50 EUR"
	prompt := llm.RetryPrompt(raw, llm.LangEN)

	if !strings.Contains(prompt, raw) {
		t.Error("RetryPrompt EN should contain the raw response")
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("RetryPrompt EN should mention JSON")
	}
}

func TestRetryPrompt_OutputFormatPresent(t *testing.T) {
	for _, lang := range []llm.Lang{llm.LangDE, llm.LangEN} {
		p := llm.RetryPrompt("anything", lang)
		requiredKeys := []string{"condition", "description", "listing_text", "price_min", "keywords"}
		for _, k := range requiredKeys {
			if !strings.Contains(p, k) {
				t.Errorf("RetryPrompt(%s) missing key %q in output format", lang, k)
			}
		}
	}
}

// =============================================================================
//  EnergyTokenSummaryPrompt
// =============================================================================

func TestEnergyTokenSummaryPrompt_ContainsMeterID(t *testing.T) {
	for _, lang := range []llm.Lang{llm.LangDE, llm.LangEN} {
		p := llm.EnergyTokenSummaryPrompt("DE001234", 5.678, lang)
		if !strings.Contains(p, "DE001234") {
			t.Errorf("EnergyTokenSummaryPrompt(%s) missing meter ID", lang)
		}
		if !strings.Contains(p, "5.6780") {
			t.Errorf("EnergyTokenSummaryPrompt(%s) missing kWh value", lang)
		}
	}
}

func TestEnergyTokenSummaryPrompt_NoJSON(t *testing.T) {
	// Summary-Prompt soll explizit kein JSON fordern
	p := llm.EnergyTokenSummaryPrompt("meter1", 1.0, llm.LangDE)
	if strings.Contains(p, `{"`) {
		t.Error("EnergyTokenSummaryPrompt should not contain JSON template")
	}
}

// =============================================================================
//  JobPrompt
// =============================================================================

func TestJobPrompt_DE_ContainsInputFields(t *testing.T) {
	input := llm.JobPromptInput{
		Type:        "offer",
		Title:       "Klempner gesucht",
		Description: "Rohre austauschen im Keller",
	}
	p := llm.JobPrompt(input, llm.LangDE)

	for _, want := range []string{"offer", "Klempner gesucht", "Rohre austauschen", "JSON"} {
		if !strings.Contains(p, want) {
			t.Errorf("JobPrompt DE missing %q", want)
		}
	}
}

func TestJobPrompt_EN_ContainsInputFields(t *testing.T) {
	input := llm.JobPromptInput{
		Type:        "request",
		Title:       "Plumber needed",
		Description: "Replace pipes in basement",
	}
	p := llm.JobPrompt(input, llm.LangEN)

	for _, want := range []string{"request", "Plumber needed", "Replace pipes", "JSON"} {
		if !strings.Contains(p, want) {
			t.Errorf("JobPrompt EN missing %q", want)
		}
	}
}

func TestJobPrompt_OutputFormat(t *testing.T) {
	input := llm.JobPromptInput{Type: "offer", Title: "Test", Description: "Desc"}
	for _, lang := range []llm.Lang{llm.LangDE, llm.LangEN} {
		p := llm.JobPrompt(input, lang)
		for _, k := range []string{"title", "listing_text", "keywords"} {
			if !strings.Contains(p, k) {
				t.Errorf("JobPrompt(%s) missing output key %q", lang, k)
			}
		}
	}
}

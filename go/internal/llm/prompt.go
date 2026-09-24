// Package llm – prompt.go
// Alle LLM-Prompts an einem Ort. Neue Sprache = neuer case-Zweig.
package llm

import (
	"fmt"
	"strings"
)

// Lang bezeichnet eine unterstützte Sprache.
type Lang string

const (
	LangDE Lang = "de"
	LangEN Lang = "en"
)

// ParseLang normalisiert einen Sprachstring auf einen bekannten Lang-Wert.
// Unbekannte Codes fallen auf LangDE zurück.
func ParseLang(s string) Lang {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "en", "en-us", "en-gb":
		return LangEN
	default:
		return LangDE
	}
}

// ============================================================================
//  Listing-Analyse
// ============================================================================

// ListingPrompt erstellt den Haupt-Analyse-Prompt für einen Artikel.
func ListingPrompt(voiceText string, numImages int, lang Lang) string {
	switch lang {
	case LangEN:
		return listingPromptEN(voiceText, numImages)
	default:
		return listingPromptDE(voiceText, numImages)
	}
}

func listingPromptDE(voiceText string, numImages int) string {
	var sb strings.Builder
	sb.WriteString("Du bist ein Experte für Gebrauchtwaren-Bewertungen auf einem dezentralen Marktplatz.\n")
	sb.WriteString("Analysiere ")
	if numImages > 0 {
		sb.WriteString(fmt.Sprintf("%d Bild(er) des Artikels", numImages))
	}
	if voiceText != "" {
		if numImages > 0 {
			sb.WriteString(" sowie den folgenden Verkäufer-Kommentar:\n")
		} else {
			sb.WriteString("den folgenden Verkäufer-Kommentar:\n")
		}
		sb.WriteString(fmt.Sprintf("%q\n", voiceText))
	}
	sb.WriteString(`
Antworte NUR mit einem gültigen JSON-Objekt, ohne Erklärungen davor oder danach:
{
  "condition":    "neuwertig|gut|akzeptabel|defekt",
  "description":  "Kurze sachliche Beschreibung (max. 2 Sätze)",
  "listing_text": "Vollständiger Angebotstext (3–5 Sätze, ansprechend formuliert)",
  "price_min":    <Zahl in EUR>,
  "price_max":    <Zahl in EUR>,
  "category":     "Elektronik|Möbel|Kleidung|Fahrzeug|Werkzeug|Haushalt|Sport|Sonstiges",
  "keywords":     ["Stichwort1", "Stichwort2", "Stichwort3"]
}`)
	return sb.String()
}

func listingPromptEN(voiceText string, numImages int) string {
	var sb strings.Builder
	sb.WriteString("You are an expert appraiser for a decentralised second-hand marketplace.\n")
	sb.WriteString("Analyse ")
	if numImages > 0 {
		sb.WriteString(fmt.Sprintf("%d image(s) of the item", numImages))
	}
	if voiceText != "" {
		if numImages > 0 {
			sb.WriteString(" along with this seller comment:\n")
		} else {
			sb.WriteString("the following seller comment:\n")
		}
		sb.WriteString(fmt.Sprintf("%q\n", voiceText))
	}
	sb.WriteString(`
Respond ONLY with a valid JSON object, no explanation before or after:
{
  "condition":    "new|good|acceptable|broken",
  "description":  "Short factual description (max 2 sentences)",
  "listing_text": "Complete marketplace listing text (3–5 sentences)",
  "price_min":    <number in EUR>,
  "price_max":    <number in EUR>,
  "category":     "Electronics|Furniture|Clothing|Vehicle|Tools|Household|Sports|Other",
  "keywords":     ["keyword1", "keyword2", "keyword3"]
}`)
	return sb.String()
}

// ============================================================================
//  Retry-Prompt (wenn erste Antwort kein valides JSON war)
// ============================================================================

func RetryPrompt(rawResponse string, lang Lang) string {
	switch lang {
	case LangEN:
		return fmt.Sprintf(
			"Convert the following assessment to valid JSON.\n"+
				"Respond ONLY with JSON, no markdown, no explanation:\n\n"+
				"Input: %s\n\n"+
				`Output format: {"condition":"","description":"","listing_text":"","price_min":0,"price_max":0,"category":"","keywords":[]}`,
			rawResponse)
	default:
		return fmt.Sprintf(
			"Wandle die folgende Bewertung in gültiges JSON um.\n"+
				"Antworte NUR mit JSON, kein Markdown, keine Erklärung:\n\n"+
				"Eingabe: %s\n\n"+
				`Ausgabe-Format: {"condition":"","description":"","listing_text":"","price_min":0,"price_max":0,"category":"","keywords":[]}`,
			rawResponse)
	}
}

// ============================================================================
//  Energie-Token-Zusammenfassung (optional, für Dashboard-Texte)
// ============================================================================

func EnergyTokenSummaryPrompt(meterID string, kWh float64, lang Lang) string {
	switch lang {
	case LangEN:
		return fmt.Sprintf(
			"Write one short sentence (max 15 words) describing an energy token: "+
				"meter %s, %.4f kWh produced. Plain text only, no JSON.", meterID, kWh)
	default:
		return fmt.Sprintf(
			"Schreibe einen kurzen Satz (max. 15 Wörter) der diesen Energie-Token beschreibt: "+
				"Zähler %s, %.4f kWh erzeugt. Nur Text, kein JSON.", meterID, kWh)
	}
}

// ============================================================================
//  Job-Inserat-Vorschlag
// ============================================================================

type JobPromptInput struct {
	Title       string
	Description string
	Type        string // "offer" | "request"
}

func JobPrompt(input JobPromptInput, lang Lang) string {
	switch lang {
	case LangEN:
		return fmt.Sprintf(
			"You are helping someone write a job listing for a decentralised marketplace.\n"+
				"Type: %s\nTitle: %s\nRaw description: %q\n\n"+
				"Respond ONLY with valid JSON:\n"+
				`{"title":"improved title","listing_text":"polished 3–5 sentence description","keywords":["k1","k2"]}`,
			input.Type, input.Title, input.Description)
	default:
		return fmt.Sprintf(
			"Du hilfst jemandem ein Job-Inserat für einen dezentralen Marktplatz zu schreiben.\n"+
				"Typ: %s\nTitel: %s\nRohbeschreibung: %q\n\n"+
				"Antworte NUR mit gültigem JSON:\n"+
				`{"title":"verbesserter Titel","listing_text":"polierter Angebotstext (3–5 Sätze)","keywords":["s1","s2"]}`,
			input.Type, input.Title, input.Description)
	}
}

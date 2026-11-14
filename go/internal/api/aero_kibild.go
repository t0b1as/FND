package api

// KI-Bilder für Flugzeugtypen ohne freies Foto (R883). Nur wenn Wikimedia
// Commons nachweislich nichts Freies hat. Das Sprachmodell (Ollama) be-
// schreibt die Bauart – Triebwerke, Flügel, Leitwerk –, ComfyUI malt danach.
// Gekennzeichnet als „KI-erzeugtes Bild – kein Foto“: Es zeigt nicht das
// echte Flugzeug und kann Details falsch haben.
//
// Ein Bild dauert mit Modellwechsel etwa 20 s – deshalb im Hintergrund, je
// Typ einmal; abgelegt unter flugdaten/typbilder/<schlüssel>-ki.{png,json}
// und beim nächsten Quartett im Tabelleneintrag.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/llm"
)

type kiTypAuftrag struct{ Suche, Hersteller, Typ, ICAO, Klasse string }

type kiTypStand struct {
	Offen  int    `json:"offen"`
	Fertig int    `json:"fertig"`
	Fehler string `json:"fehler,omitempty"`
}

var (
	kiTypMu      sync.Mutex
	kiTypWarte   = make(chan kiTypAuftrag, 200)
	kiTypInArbeit = map[string]bool{}
	kiTypZustand kiTypStand
	reKiPrompt   = regexp.MustCompile(`[^A-Za-z0-9 ,.;:()'/-]`)
)

func (s *Server) kiTypSchluessel(q string) string { return typBildSchluessel(q) + "-ki" }

// kiTypBildCache: schon erzeugt?
func (s *Server) kiTypBildCache(q string) *typBild {
	b, err := os.ReadFile(filepath.Join(s.typBildOrdner(), s.kiTypSchluessel(q)+".json"))
	if err != nil {
		return nil
	}
	var t typBild
	if json.Unmarshal(b, &t) != nil || t.Datei == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.typBildOrdner(), t.Datei)); err != nil {
		return nil
	}
	return &t
}

func (s *Server) kiTypBildVormerken(a kiTypAuftrag) {
	kiTypMu.Lock()
	defer kiTypMu.Unlock()
	if kiTypInArbeit[a.Suche] {
		return
	}
	select {
	case kiTypWarte <- a:
		kiTypInArbeit[a.Suche] = true
		kiTypZustand.Offen++
	default:
	}
}

func kiTypZustandLesen() kiTypStand {
	kiTypMu.Lock()
	defer kiTypMu.Unlock()
	return kiTypZustand
}

// kiTypBeschreibung: englische Bildbeschreibung – vom Sprachmodell, sonst Vorlage.
func (s *Server) kiTypBeschreibung(ctx context.Context, a kiTypAuftrag) string {
	name := strings.TrimSpace(a.Hersteller + " " + a.Typ)
	if name == "" {
		name = a.ICAO
	}
	vorlage := "realistic photograph of a " + name + " aircraft (" + KLASSE_EN[a.Klasse] + "), side view, in flight, " +
		"clear blue sky, plain white livery without logos or text, sharp detail, natural light"
	if s.analyzer == nil {
		return vorlage
	}
	modell := s.cfg.LLMTextModel
	if modell == "" {
		modell = s.cfg.LLMModel
	}
	anfrage := map[string]any{"model": modell, "stream": false, "keep_alive": s.cfg.LLMKeepAlive,
		"options": map[string]any{"temperature": 0.3, "num_predict": 160},
		"prompt": "Write ONE English Stable Diffusion prompt (max 60 words) for a realistic photograph of the aircraft type \"" + name +
			"\" (ICAO code " + a.ICAO + ") in flight, side view, against a clear sky. Describe its characteristic visual features " +
			"precisely: number and position of engines, wing position and shape, tail shape, fuselage length, landing gear. " +
			"Plain white livery without any logos or text. Reply with the prompt only, no quotes, no explanation."}
	if llm.IstDenkmodell(modell) {
		anfrage["think"] = false
	}
	b, _ := json.Marshal(anfrage)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.LLMURL, "/")+"/api/generate", bytes.NewReader(b))
	if err != nil {
		return vorlage
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return vorlage
	}
	defer resp.Body.Close()
	var d struct {
		Response string `json:"response"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&d) != nil {
		return vorlage
	}
	text := strings.TrimSpace(reKiPrompt.ReplaceAllString(d.Response, " "))
	if len(text) < 30 {
		return vorlage
	}
	if len(text) > 600 {
		text = text[:600]
	}
	// R884: Der Typname steht immer vorn – das Sprachmodell ergänzt nur Merkmale.
	return "realistic photograph of a " + name + " aircraft, " + text + ", plain white livery, no logos, no text"
}

// kiTypBildArbeiter: Aufträge nacheinander abarbeiten (die Grafikkarte kann
// ohnehin nur eins zur Zeit).
func (s *Server) kiTypBildArbeiter() {
	for a := range kiTypWarte {
		func() {
			defer func() {
				_ = recover()
				kiTypMu.Lock()
				delete(kiTypInArbeit, a.Suche)
				kiTypZustand.Offen--
				kiTypMu.Unlock()
			}()
			if s.kiTypBildCache(a.Suche) != nil {
				return
			}
			bild := s.kiBildLesen()
			if !bild.aktiv() {
				kiTypMu.Lock()
				kiTypZustand.Fehler = "ComfyUI ist nicht eingerichtet – Einstellungen → KI → Bilderzeugung"
				kiTypMu.Unlock()
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			beschreibung := s.kiTypBeschreibung(ctx, a)
			modell := s.cfg.LLMTextModel
			if modell == "" {
				modell = s.cfg.LLMModel
			}
			id, err := s.kiBildErzeugen(ctx, bild, beschreibung, "quer", modell)
			if err != nil {
				kiTypMu.Lock()
				kiTypZustand.Fehler = err.Error()
				kiTypMu.Unlock()
				return
			}
			daten, err := os.ReadFile(filepath.Join(s.kiBildOrdner(), id+".png"))
			if err != nil {
				return
			}
			_ = os.MkdirAll(s.typBildOrdner(), 0o755)
			schl := s.kiTypSchluessel(a.Suche)
			if os.WriteFile(filepath.Join(s.typBildOrdner(), schl+".png"), daten, 0o644) != nil {
				return
			}
			_ = jsonAtomar(filepath.Join(s.typBildOrdner(), schl+".json"), typBild{Datei: schl + ".png", KI: true,
				Autor: "KI", Lizenz: bild.Modell, Quelle: "ComfyUI", Artikel: beschreibung, Stand: time.Now().Unix()}, 0o644)
			kiTypMu.Lock()
			kiTypZustand.Fertig++
			kiTypZustand.Fehler = ""
			kiTypMu.Unlock()
		}()
	}
}

// Bauart auf Englisch – für die Vorlage, wenn das Sprachmodell fehlt.
var KLASSE_EN = map[string]string{
	"grossraum": "twin-engine wide-body airliner", "vierstrahler": "four-engine wide-body airliner",
	"mittelstrecke": "twin-engine narrow-body airliner", "heckjet": "regional jet with rear-mounted engines and T-tail",
	"turboprop": "twin turboprop high-wing airliner", "geschaeftsjet": "business jet with rear-mounted engines",
	"kleinflugzeug": "single-engine light aircraft", "hubschrauber": "helicopter", "militaer": "military fighter jet",
	"unbekannt": "aircraft",
}

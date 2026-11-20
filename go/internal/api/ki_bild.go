package api

// Bilderzeugung für den KI-Chat (R873) über ComfyUI auf einem Rechner mit
// Grafikkarte. Das Sprachmodell ruft das Werkzeug „bild_erzeugen“ mit einer
// englischen Bildbeschreibung auf; der Node schickt einen festen SDXL-
// Workflow an ComfyUI, wartet auf das Bild, legt es ab und zeigt es im Chat.
//
// Grafikspeicher: Ein Sprachmodell wie qwen3.5:9b und ein SDXL-Modell
// belegen je 6–7 GB – auf einer 8-GB-Karte nicht beides zugleich. Laufen
// Ollama und ComfyUI auf demselben Rechner (oder ist „teilen“ an), entlädt
// der Node vor dem Bild das Sprachmodell (Ollama keep_alive 0) und danach
// das Bildmodell (ComfyUI /free).
//
// Einstellungen: <DataDir>/ki-bild.json. Bilder: <DataDir>/ki-bilder/ (die
// letzten 50), ausgeliefert über /api/v1/ki/bild/<id>.png – Zugang wie der Chat.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type kiBildConfig struct {
	ComfyURL string `json:"comfy_url"`
	Modell   string `json:"modell"`
	Schritte int    `json:"schritte"`
	Teilen   bool   `json:"teilen"` // Grafikkarte mit Ollama teilen: Modelle wechselseitig entladen

	// R941: Bildmodell-Typ. "" / "sdxl": eine Checkpoint-Datei (Modell).
	// "zimage": Z-Image(-Turbo) aus drei Dateien – Diffusionsmodell
	// (models/diffusion_models, .safetensors oder .gguf), Text-Encoder Qwen3-4B
	// (models/text_encoders) und VAE ae.safetensors (models/vae).
	Typ     string `json:"typ"`
	Unet    string `json:"unet"`
	Encoder string `json:"encoder"`
	VAE     string `json:"vae"`
}

func (k kiBildConfig) zimage() bool { return k.Typ == "zimage" }

func (s *Server) kiBildDatei() string  { return filepath.Join(s.cfg.DataDir, "ki-bild.json") }
func (s *Server) kiBildOrdner() string { return filepath.Join(s.cfg.DataDir, "ki-bilder") }

func (s *Server) kiBildLesen() kiBildConfig {
	k := kiBildConfig{Schritte: 25, Teilen: true}
	if b, err := os.ReadFile(s.kiBildDatei()); err == nil {
		_ = json.Unmarshal(b, &k)
	}
	if k.Schritte < 4 || k.Schritte > 60 {
		k.Schritte = 25
	}
	return k
}

func (k kiBildConfig) aktiv() bool {
	if k.ComfyURL == "" {
		return false
	}
	if k.zimage() {
		return k.Unet != "" && k.Encoder != "" && k.VAE != ""
	}
	return k.Modell != ""
}

var kiComfyClient = &http.Client{Timeout: 30 * time.Second}

// comfyModelle: Checkpoint-Dateien, die ComfyUI kennt.
func comfyModelle(ctx context.Context, basis string) ([]string, error) {
	return comfyListe(ctx, basis, "CheckpointLoaderSimple", "ckpt_name")
}

// comfyListe: Auswahlliste eines Lade-Knotens (Dateien im passenden Ordner).
// Fehlt der Knoten (z. B. GGUF-Erweiterung nicht installiert) → leere Liste.
func comfyListe(ctx context.Context, basis, knoten, feldName string) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, basis+"/object_info/"+knoten, nil)
	resp, err := kiComfyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ComfyUI nicht erreichbar unter %s – läuft es, mit --listen 0.0.0.0, und lässt die Firewall den Port durch?", basis)
	}
	defer resp.Body.Close()
	var d map[string]struct {
		Input struct {
			Required map[string][]json.RawMessage `json:"required"`
		} `json:"input"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d) != nil {
		return nil, fmt.Errorf("unter dieser Adresse antwortet kein ComfyUI")
	}
	feld := d[knoten].Input.Required[feldName]
	if len(feld) == 0 {
		return []string{}, nil
	}
	// Ältere Fassungen: [["a.safetensors", …]]; neuere: ["COMBO", {"options": […]}].
	var liste []string
	if json.Unmarshal(feld[0], &liste) == nil {
		return liste, nil
	}
	if len(feld) > 1 {
		var o struct {
			Options []string `json:"options"`
		}
		if json.Unmarshal(feld[1], &o) == nil {
			return o.Options, nil
		}
	}
	return []string{}, nil
}

// GET /api/v1/admin/ki/bild
func (s *Server) kiBildGet(c *gin.Context) { c.JSON(http.StatusOK, s.kiBildLesen()) }

// POST /api/v1/admin/ki/bild {comfy_url, modell, schritte, teilen}
func (s *Server) kiBildSet(c *gin.Context) {
	var k kiBildConfig
	if c.ShouldBindJSON(&k) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	k.ComfyURL = strings.TrimRight(strings.TrimSpace(k.ComfyURL), "/")
	if k.ComfyURL != "" && !reOllamaURL.MatchString(k.ComfyURL) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Adresse wie http://192.168.1.20:8188"})
		return
	}
	if k.Typ != "zimage" {
		k.Typ = "sdxl"
	}
	if k.Schritte < 4 || k.Schritte > 60 {
		k.Schritte = 25
		if k.zimage() {
			k.Schritte = 9
		}
	}
	if err := jsonAtomar(s.kiBildDatei(), k, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// POST /api/v1/admin/ki/bild/test {comfy_url}
func (s *Server) kiBildTest(c *gin.Context) {
	var req struct {
		ComfyURL string `json:"comfy_url"`
	}
	_ = c.ShouldBindJSON(&req)
	basis := strings.TrimRight(strings.TrimSpace(req.ComfyURL), "/")
	if !reOllamaURL.MatchString(basis) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Adresse wie http://192.168.1.20:8188"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	m, err := comfyModelle(ctx, basis)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "fehler": err.Error()})
		return
	}
	// R941: Dateien für Z-Image (drei Ordner; .gguf nur mit ComfyUI-GGUF).
	unets, _ := comfyListe(ctx, basis, "UNETLoader", "unet_name")
	encoder, _ := comfyListe(ctx, basis, "CLIPLoader", "clip_name")
	vaes, _ := comfyListe(ctx, basis, "VAELoader", "vae_name")
	ggufUnets, _ := comfyListe(ctx, basis, "UnetLoaderGGUF", "unet_name")
	ggufEnc, _ := comfyListe(ctx, basis, "CLIPLoaderGGUF", "clip_name")
	unets = mitGGUF(unets, ggufUnets)
	encoder = mitGGUF(encoder, ggufEnc)
	vorschlag := gin.H{
		"unet":    erstesMit(unets, "z_image", "z-image", "zimage"),
		"encoder": erstesMit(encoder, "qwen_3_4b", "qwen3-4b", "qwen3_4b", "qwen-3-4b"),
		"vae":     vaeFuerZImage(vaes),
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "modelle": m, "unets": unets, "encoder": encoder, "vaes": vaes,
		"gguf": len(ggufUnets) > 0 || len(ggufEnc) > 0, "zimage_vorschlag": vorschlag})
}

// vaeFuerZImage: die Flux-/Z-Image-VAE heißt ae.safetensors (ggf. in einem
// Unterordner) – nicht mit sdxl_vae o. ä. verwechseln.
func vaeFuerZImage(liste []string) string {
	for _, f := range liste {
		l := strings.ToLower(strings.ReplaceAll(f, "\\", "/"))
		if i := strings.LastIndex(l, "/"); i >= 0 {
			l = l[i+1:]
		}
		if l == "ae.safetensors" || l == "ae.sft" || strings.Contains(l, "z_image") && strings.Contains(l, "vae") {
			return f
		}
	}
	return ""
}

func mitGGUF(a, b []string) []string {
	da := map[string]bool{}
	out := append([]string{}, a...)
	for _, x := range a {
		da[x] = true
	}
	for _, x := range b {
		if !da[x] {
			out = append(out, x)
		}
	}
	return out
}

// erstesMit: erste Datei, deren Name einen der Bausteine enthält. Bei
// Diffusionsmodellen gewinnt eine kleine Fassung (fp8/int8/gguf), weil die
// bf16-Datei (12 GB) eine 8-GB-Karte überfordert.
func erstesMit(liste []string, teile ...string) string {
	var treffer []string
	for _, f := range liste {
		l := strings.ToLower(f)
		for _, t := range teile {
			if strings.Contains(l, t) {
				treffer = append(treffer, f)
				break
			}
		}
	}
	for _, klein := range []string{"int8", "fp8", ".gguf"} {
		for _, f := range treffer {
			if strings.Contains(strings.ToLower(f), klein) {
				return f
			}
		}
	}
	if len(treffer) > 0 {
		return treffer[0]
	}
	return ""
}

var kiFormate = map[string][2]int{"quadrat": {1024, 1024}, "hoch": {832, 1216}, "quer": {1216, 832}}

func zufallsZahl() int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<48))
	return n.Int64()
}

// gleicherRechner: laufen Ollama und ComfyUI auf demselben Host?
func gleicherRechner(a, b string) bool {
	ua, e1 := url.Parse(a)
	ub, e2 := url.Parse(b)
	return e1 == nil && e2 == nil && ua.Hostname() != "" && strings.EqualFold(ua.Hostname(), ub.Hostname())
}

// kiBildErzeugen: Auftrag an ComfyUI, warten, Bild ablegen. Liefert die ID.
func (s *Server) kiBildErzeugen(ctx context.Context, k kiBildConfig, beschreibung, format string, sprachmodell string) (string, error) {
	// R942: gilt für jeden Weg (Werkzeug, Text-Aufruf, /bild) und jedes Modell.
	if kiBildGesperrt(beschreibung) {
		return "", fmt.Errorf("diese Bildbeschreibung wird nicht umgesetzt")
	}
	wh, ok := kiFormate[format]
	if !ok {
		wh = kiFormate["quadrat"]
	}
	teilen := k.Teilen || gleicherRechner(k.ComfyURL, s.cfg.LLMURL)
	if teilen && sprachmodell != "" {
		// Sprachmodell aus der Grafikkarte nehmen; Ollama lädt es bei der
		// nächsten Anfrage von selbst wieder.
		b, _ := json.Marshal(map[string]any{"model": sprachmodell, "keep_alive": 0})
		if r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.LLMURL, "/")+"/api/generate", bytes.NewReader(b)); err == nil {
			r.Header.Set("Content-Type", "application/json")
			if resp, err := kiComfyClient.Do(r); err == nil {
				resp.Body.Close()
			}
		}
	}
	workflow := comfyAblaufSDXL(k, beschreibung, wh)
	if k.zimage() {
		workflow = comfyAblaufZImage(k, beschreibung, wh)
	}
	b, _ := json.Marshal(map[string]any{"prompt": workflow, "client_id": "fundus"})
	return s.kiBildAusfuehren(ctx, k, b, teilen)
}

// comfyAblaufSDXL: bisheriger Ablauf (ein Checkpoint, CFG 6.5, Negativ-Prompt).
func comfyAblaufSDXL(k kiBildConfig, beschreibung string, wh [2]int) map[string]any {
	return map[string]any{
		"3": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": zufallsZahl(), "steps": k.Schritte, "cfg": 6.5,
			"sampler_name": "dpmpp_2m", "scheduler": "karras", "denoise": 1, "model": []any{"4", 0},
			"positive": []any{"6", 0}, "negative": []any{"7", 0}, "latent_image": []any{"5", 0}}},
		"4": map[string]any{"class_type": "CheckpointLoaderSimple", "inputs": map[string]any{"ckpt_name": k.Modell}},
		"5": map[string]any{"class_type": "EmptyLatentImage", "inputs": map[string]any{"width": wh[0], "height": wh[1], "batch_size": 1}},
		"6": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": beschreibung, "clip": []any{"4", 1}}},
		"7": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "blurry, low quality, deformed, watermark, text, signature, extra fingers", "clip": []any{"4", 1}}},
		"8": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"3", 0}, "vae": []any{"4", 2}}},
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"filename_prefix": "fundus", "images": []any{"8", 0}}},
	}
}

// comfyAblaufZImage (R941): Z-Image-Turbo nach der ComfyUI-Vorlage –
// getrennte Lader, ModelSamplingAuraFlow (shift 3), EmptySD3LatentImage,
// KSampler euler/simple, CFG 1 (Negativ-Prompt wirkungslos, bleibt leer).
// .gguf-Dateien laufen über die Knoten der Erweiterung ComfyUI-GGUF.
func comfyAblaufZImage(k kiBildConfig, beschreibung string, wh [2]int) map[string]any {
	unet := map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": k.Unet, "weight_dtype": "default"}}
	if strings.HasSuffix(strings.ToLower(k.Unet), ".gguf") {
		unet = map[string]any{"class_type": "UnetLoaderGGUF", "inputs": map[string]any{"unet_name": k.Unet}}
	}
	enc := map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{"clip_name": k.Encoder, "type": "lumina2", "device": "default"}}
	if strings.HasSuffix(strings.ToLower(k.Encoder), ".gguf") {
		enc = map[string]any{"class_type": "CLIPLoaderGGUF", "inputs": map[string]any{"clip_name": k.Encoder, "type": "lumina2"}}
	}
	return map[string]any{
		"10": unet,
		"11": enc,
		"12": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": k.VAE}},
		"13": map[string]any{"class_type": "ModelSamplingAuraFlow", "inputs": map[string]any{"model": []any{"10", 0}, "shift": 3}},
		"5":  map[string]any{"class_type": "EmptySD3LatentImage", "inputs": map[string]any{"width": wh[0], "height": wh[1], "batch_size": 1}},
		"6":  map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": beschreibung, "clip": []any{"11", 0}}},
		"7":  map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "", "clip": []any{"11", 0}}},
		"3": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": zufallsZahl(), "steps": k.Schritte, "cfg": 1,
			"sampler_name": "euler", "scheduler": "simple", "denoise": 1, "model": []any{"13", 0},
			"positive": []any{"6", 0}, "negative": []any{"7", 0}, "latent_image": []any{"5", 0}}},
		"8": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"3", 0}, "vae": []any{"12", 0}}},
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"filename_prefix": "fundus", "images": []any{"8", 0}}},
	}
}

// kiBildAusfuehren: Auftrag abschicken, warten, Bild holen und ablegen.
func (s *Server) kiBildAusfuehren(ctx context.Context, k kiBildConfig, b []byte, teilen bool) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, k.ComfyURL+"/prompt", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := kiComfyClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ComfyUI nicht erreichbar")
	}
	var auftrag struct {
		PromptID string         `json:"prompt_id"`
		Error    map[string]any `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&auftrag)
	resp.Body.Close()
	if auftrag.PromptID == "" {
		if k.zimage() {
			return "", fmt.Errorf("ComfyUI lehnt den Auftrag ab – liegen „%s“ (models/diffusion_models), „%s“ (models/text_encoders) und „%s“ (models/vae) vor? Für .gguf braucht ComfyUI die Erweiterung ComfyUI-GGUF; ältere ComfyUI-Fassungen kennen Z-Image noch nicht (aktualisieren)", k.Unet, k.Encoder, k.VAE)
		}
		return "", fmt.Errorf("ComfyUI lehnt den Auftrag ab – ist das Modell „%s“ in models/checkpoints vorhanden?", k.Modell)
	}
	// Warten, bis das Bild fertig ist (höchstens 4 Minuten). Bricht der
	// Browser ab, den Auftrag in ComfyUI abbrechen.
	var datei struct{ Filename, Subfolder, Type string }
	frist := time.Now().Add(4 * time.Minute)
	for datei.Filename == "" {
		select {
		case <-ctx.Done():
			if r, err := http.NewRequest(http.MethodPost, k.ComfyURL+"/interrupt", nil); err == nil {
				if resp, err := kiComfyClient.Do(r); err == nil {
					resp.Body.Close()
				}
			}
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
		if time.Now().After(frist) {
			return "", fmt.Errorf("ComfyUI braucht zu lange")
		}
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, k.ComfyURL+"/history/"+url.PathEscape(auftrag.PromptID), nil)
		resp, err := kiComfyClient.Do(r)
		if err != nil {
			continue
		}
		var h map[string]struct {
			Outputs map[string]struct {
				Images []struct{ Filename, Subfolder, Type string } `json:"images"`
			} `json:"outputs"`
			Status struct {
				StatusStr string `json:"status_str"`
			} `json:"status"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&h)
		resp.Body.Close()
		e, ok := h[auftrag.PromptID]
		if !ok {
			continue
		}
		if e.Status.StatusStr == "error" {
			return "", fmt.Errorf("ComfyUI meldet einen Fehler beim Erzeugen")
		}
		for _, o := range e.Outputs {
			if len(o.Images) > 0 {
				datei = o.Images[0]
			}
		}
	}
	q := url.Values{"filename": {datei.Filename}, "subfolder": {datei.Subfolder}, "type": {datei.Type}}
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, k.ComfyURL+"/view?"+q.Encode(), nil)
	resp, err = kiComfyClient.Do(r)
	if err != nil {
		return "", fmt.Errorf("Bild nicht abrufbar")
	}
	png, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || len(png) < 8 || string(png[1:4]) != "PNG" {
		return "", fmt.Errorf("Bild nicht abrufbar")
	}
	if teilen {
		// Bildmodell wieder freigeben, damit das Sprachmodell Platz hat.
		if r, err := http.NewRequest(http.MethodPost, k.ComfyURL+"/free", strings.NewReader(`{"unload_models":true,"free_memory":true}`)); err == nil {
			r.Header.Set("Content-Type", "application/json")
			if resp, err := kiComfyClient.Do(r); err == nil {
				resp.Body.Close()
			}
		}
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	name := hex.EncodeToString(id)
	_ = os.MkdirAll(s.kiBildOrdner(), 0o700)
	if err := os.WriteFile(filepath.Join(s.kiBildOrdner(), name+".png"), png, 0o600); err != nil {
		return "", err
	}
	// Nur die letzten 50 Bilder aufheben.
	if alle, err := filepath.Glob(filepath.Join(s.kiBildOrdner(), "*.png")); err == nil && len(alle) > 50 {
		sort.Slice(alle, func(i, j int) bool {
			a, _ := os.Stat(alle[i])
			b, _ := os.Stat(alle[j])
			return a != nil && b != nil && a.ModTime().Before(b.ModTime())
		})
		for _, alt := range alle[:len(alle)-50] {
			_ = os.Remove(alt)
		}
	}
	return name, nil
}

var reKiBild = regexp.MustCompile(`^[0-9a-f]{24}\.png$`)

// GET /api/v1/ki/bild/:datei – Zugang wie der Chat.
func (s *Server) kiBildAusliefern(c *gin.Context) {
	if !isLANRequest(c) {
		if sess := s.getSession(c); sess == nil || sess.identity == nil {
			c.Status(http.StatusUnauthorized)
			return
		}
	}
	d := c.Param("datei")
	if !reKiBild.MatchString(d) {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "private, max-age=86400")
	c.File(filepath.Join(s.kiBildOrdner(), d))
}

// kiBildGesperrt (R942): keine Nackt- oder sexuellen Darstellungen von
// Minderjährigen – unabhängig vom Sprachmodell, auch im Heimnetz.
var (
	reKIMinderjaehrig = regexp.MustCompile(`(?i)\b(child|children|kids?|underage|under-age|teens?|teenage|teenager|preteen|pre-teen|toddler|infant|bab(y|ies)|schoolgirl|schoolboy|loli|lolita|shota|young (girl|boy)|little (girl|boy)|minderjährig\w*|jugendlich\w*|schulmädchen|schuljunge|kleinkind\w*|säugling\w*|mädchen|(?:[1-9]|1[0-7])[ -]?(?:years?[ -]old|yo|jahre alt|jährig\w*))\b`)
	// Groß geschrieben = Hauptwort („Kind“, „Junge“) – nicht „kind of“, „junge Frau“.
	reKIMinderjaehrigNomen = regexp.MustCompile(`\b(Kind|Kinder|Kindes|Kindern|Junge|Jungen)\b`)
	reKINackt              = regexp.MustCompile(`(?i)\b(nude|nudity|naked|nsfw|topless|bottomless|undressed|unclothed|sexual|sexy|erotic|explicit|lingerie|genital\w*|breasts?|nackt\w*|erotisch\w*|sexuell\w*|unbekleidet\w*|oben ohne|intim\w*)\b`)
)

func kiBildGesperrt(beschreibung string) bool {
	return (reKIMinderjaehrig.MatchString(beschreibung) || reKIMinderjaehrigNomen.MatchString(beschreibung)) &&
		reKINackt.MatchString(beschreibung)
}

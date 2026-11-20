package api

import (
	"context"
	"encoding/json"
	"github.com/fundus/node/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R941: Dateilisten aus ComfyUI, Vorschlag für Z-Image und der Ablauf.
func TestKIBild_ZImage(t *testing.T) {
	listen := map[string]string{
		"UNETLoader":     `{"UNETLoader":{"input":{"required":{"unet_name":["COMBO",{"options":["z_image_turbo_bf16.safetensors","z_image_turbo_int8_convrot.safetensors","flux.safetensors"]}]}}}}`,
		"CLIPLoader":     `{"CLIPLoader":{"input":{"required":{"clip_name":[["clip_l.safetensors","qwen_3_4b_fp8_mixed.safetensors"]]}}}}`,
		"VAELoader":      `{"VAELoader":{"input":{"required":{"vae_name":["COMBO",{"options":["sdxl_vae.safetensors","ae.safetensors"]}]}}}}`,
		"UnetLoaderGGUF": `{}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := strings.TrimPrefix(r.URL.Path, "/object_info/")
		if b, ok := listen[k]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	u, _ := comfyListe(ctx, srv.URL, "UNETLoader", "unet_name")
	e, _ := comfyListe(ctx, srv.URL, "CLIPLoader", "clip_name")
	v, _ := comfyListe(ctx, srv.URL, "VAELoader", "vae_name")
	g, _ := comfyListe(ctx, srv.URL, "UnetLoaderGGUF", "unet_name")
	if len(u) != 3 || len(e) != 2 || len(v) != 2 || len(g) != 0 {
		t.Fatalf("Listen: %v %v %v %v", u, e, v, g)
	}
	if x := erstesMit(u, "z_image"); x != "z_image_turbo_int8_convrot.safetensors" {
		t.Errorf("Vorschlag Diffusionsmodell: %q (kleine Fassung erwartet)", x)
	}
	if x := erstesMit(e, "qwen_3_4b"); x != "qwen_3_4b_fp8_mixed.safetensors" {
		t.Errorf("Vorschlag Encoder: %q", x)
	}
	if x := vaeFuerZImage(v); x != "ae.safetensors" {
		t.Errorf("Vorschlag VAE: %q", x)
	}

	if x := vaeFuerZImage([]string{"sdxl_vae.safetensors"}); x != "" {
		t.Errorf("sdxl_vae darf nicht als Z-Image-VAE gelten: %q", x)
	}
	if x := vaeFuerZImage([]string{`flux\ae.safetensors`}); x == "" {
		t.Error("ae.safetensors im Unterordner nicht erkannt")
	}

	k := kiBildConfig{ComfyURL: "http://x:8188", Typ: "zimage", Unet: "z.safetensors", Encoder: "q.safetensors", VAE: "ae.safetensors", Schritte: 9}
	if !k.aktiv() {
		t.Fatal("Z-Image-Konfiguration muss aktiv sein")
	}
	k2 := k
	k2.VAE = ""
	if k2.aktiv() {
		t.Fatal("ohne VAE darf Z-Image nicht aktiv sein")
	}
	wf := comfyAblaufZImage(k, "marble statue", [2]int{1024, 1024})
	b, _ := json.Marshal(wf)
	for _, muss := range []string{`"UNETLoader"`, `"type":"lumina2"`, `"ModelSamplingAuraFlow"`, `"shift":3`,
		`"EmptySD3LatentImage"`, `"cfg":1`, `"sampler_name":"euler"`, `"scheduler":"simple"`, `"steps":9`, `"vae_name":"ae.safetensors"`} {
		if !strings.Contains(string(b), muss) {
			t.Errorf("Ablauf ohne %s", muss)
		}
	}
	// GGUF-Dateien → Knoten der Erweiterung
	k.Unet, k.Encoder = "z_image_turbo-Q5_K_M.gguf", "Qwen3-4B-Q8_0.gguf"
	b, _ = json.Marshal(comfyAblaufZImage(k, "x", [2]int{832, 1216}))
	if !strings.Contains(string(b), `"UnetLoaderGGUF"`) || !strings.Contains(string(b), `"CLIPLoaderGGUF"`) {
		t.Error("GGUF-Lader fehlen")
	}
	// Alter Ablauf unverändert
	sd := kiBildConfig{ComfyURL: "http://x", Modell: "sd_xl_base_1.0.safetensors", Schritte: 25}
	b, _ = json.Marshal(comfyAblaufSDXL(sd, "x", [2]int{1024, 1024}))
	if !sd.aktiv() || !strings.Contains(string(b), `"CheckpointLoaderSimple"`) || !strings.Contains(string(b), `"cfg":6.5`) {
		t.Error("SDXL-Ablauf verändert")
	}
}

// R941: kompletter Durchlauf gegen ein nachgebautes ComfyUI.
func TestKIBild_ZImageDurchlauf(t *testing.T) {
	var auftrag map[string]any
	ollamaEntladen, frei := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt":
			_ = json.NewDecoder(r.Body).Decode(&auftrag)
			_, _ = w.Write([]byte(`{"prompt_id":"p1"}`))
		case r.URL.Path == "/history/p1":
			_, _ = w.Write([]byte(`{"p1":{"outputs":{"9":{"images":[{"filename":"fundus_0001.png","subfolder":"","type":"output"}]}},"status":{"status_str":"success"}}}`))
		case r.URL.Path == "/view":
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nDATEN"))
		case r.URL.Path == "/free":
			frei = true
		case r.URL.Path == "/api/generate":
			ollamaEntladen = true
		}
	}))
	defer srv.Close()
	cfg := &config.Config{DataDir: t.TempDir(), LLMURL: srv.URL}
	s := &Server{cfg: cfg}
	k := kiBildConfig{ComfyURL: srv.URL, Typ: "zimage", Unet: "z_image_turbo_int8_convrot.safetensors",
		Encoder: "qwen_3_4b_fp8_mixed.safetensors", VAE: "ae.safetensors", Schritte: 9, Teilen: true}
	id, err := s.kiBildErzeugen(context.Background(), k, "classical marble statue, contrapposto", "hoch", "qwen3.5:9b")
	if err != nil || id == "" {
		t.Fatalf("Durchlauf: id=%q err=%v", id, err)
	}
	b, _ := json.Marshal(auftrag)
	for _, muss := range []string{"z_image_turbo_int8_convrot", "qwen_3_4b_fp8_mixed", "lumina2", "EmptySD3LatentImage", `"width":832`, "contrapposto"} {
		if !strings.Contains(string(b), muss) {
			t.Errorf("Auftrag ohne %s", muss)
		}
	}
	if strings.Contains(string(b), "CheckpointLoaderSimple") {
		t.Error("Z-Image-Auftrag enthält den SDXL-Lader")
	}
	if !ollamaEntladen || !frei {
		t.Errorf("Grafikkarte teilen: Ollama entladen=%v, ComfyUI frei=%v", ollamaEntladen, frei)
	}
}

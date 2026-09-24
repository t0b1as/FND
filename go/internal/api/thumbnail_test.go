package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestThumbDimensions(t *testing.T) {
	cases := []struct {
		w, h, edge     int
		wantW, wantH   int
	}{
		{4000, 3000, 256, 256, 192}, // Querformat
		{3000, 4000, 256, 192, 256}, // Hochformat
		{1000, 1000, 256, 256, 256}, // quadratisch
		{200, 100, 256, 200, 100},   // kleiner als edge → unverändert
		{100, 200, 256, 100, 200},   // klein hoch → unverändert
	}
	for _, c := range cases {
		gw, gh := thumbDimensions(c.w, c.h, c.edge)
		if gw != c.wantW || gh != c.wantH {
			t.Errorf("thumbDimensions(%d,%d,%d) = %d,%d, erwartet %d,%d",
				c.w, c.h, c.edge, gw, gh, c.wantW, c.wantH)
		}
	}
}

func TestThumbDimensionsLongEdge(t *testing.T) {
	// Lange Kante muss immer == edge sein, wenn das Bild größer ist.
	w, h := thumbDimensions(8000, 2000, 256)
	if w != 256 {
		t.Fatalf("lange Kante sollte 256 sein, ist %d", w)
	}
	if h < 1 {
		t.Fatal("kurze Kante darf nicht 0 werden")
	}
}

func TestMakeThumbnailFromPNG(t *testing.T) {
	// Synthetisches 512×512-PNG erzeugen.
	src := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			src.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png encode: %v", err)
	}

	thumb, err := makeThumbnail(buf.Bytes())
	if err != nil {
		t.Fatalf("makeThumbnail: %v", err)
	}
	if len(thumb) == 0 {
		t.Fatal("leeres Thumbnail")
	}
	// Ergebnis muss ein dekodierbares JPEG sein mit langer Kante 256.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("thumbnail nicht dekodierbar: %v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Fatalf("erwartet 256×256, bekam %d×%d", cfg.Width, cfg.Height)
	}
}

func TestMakeThumbnailRejectsGarbage(t *testing.T) {
	if _, err := makeThumbnail([]byte("kein bild")); err == nil {
		t.Fatal("Müll-Eingabe hätte fehlschlagen müssen")
	}
}

func TestBoxDownscaleAverages(t *testing.T) {
	// 2×2-Bild aus vier Farben → 1×1 muss deren Mittelwert sein.
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.SetRGBA(0, 0, color.RGBA{0, 0, 0, 255})
	src.SetRGBA(1, 0, color.RGBA{255, 0, 0, 255})
	src.SetRGBA(0, 1, color.RGBA{0, 255, 0, 255})
	src.SetRGBA(1, 1, color.RGBA{255, 255, 255, 255})
	dst := boxDownscale(src, 1, 1)
	r, g, b, _ := dst.At(0, 0).RGBA()
	// Mittel: R=(0+255+0+255)/4≈127, G=(0+0+255+255)/4≈127, B=(0+0+0+255)/4≈63
	r8, g8, b8 := r>>8, g>>8, b>>8
	if r8 < 120 || r8 > 135 || g8 < 120 || g8 > 135 || b8 < 55 || b8 > 70 {
		t.Fatalf("Mittelwert unerwartet: R=%d G=%d B=%d", r8, g8, b8)
	}
}

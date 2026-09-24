package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG-Decoder registrieren
	"os"
	"path/filepath"
	"sync"
)

// ─── Thumbnail-Erzeugung (serverseitig, gecacht, RAM-sicher) ─────────────────
//
// Ziel: Suchtrefferlisten sollen kleine Vorschaubilder zeigen, ohne große
// Base64-Blobs in die JSON-Antwort zu packen ODER das Vollbild zu laden. Der
// Client referenziert nur einen Hash; dieser Endpunkt liefert ein kleines,
// gecachtes JPEG (256 px lange Kante).
//
// RAM-Realität (1-GB-Pi): image.Decode dekomprimiert das GANZE Bild in einen
// Pixel-Buffer (24-MP-Foto ≈ 96 MB RGBA!). Daher drei Schutzmaßnahmen:
//   1. Cache  — jedes Thumbnail wird nur EINMAL erzeugt, dann von Disk geliefert.
//   2. Semaphor — höchstens N gleichzeitige Decodierungen (begrenzt RAM-Spitzen).
//   3. Pixel-Limit — Bilder über maxDecodePixels werden abgelehnt (Schutz vor
//      Decompression-Bombs und Speicher-Explosion).

const (
	thumbMaxEdge      = 256              // lange Kante des Thumbnails in px
	thumbJPEGQuality  = 80               // JPEG-Qualität des Thumbnails
	maxDecodePixels   = 40 * 1000 * 1000 // 40 MP Obergrenze fürs Decodieren
	maxOriginalBytes  = 64 * 1024 * 1024 // 64 MiB: größere Originale gar nicht erst puffern
	thumbConcurrency  = 2                // gleichzeitige Decodierungen (RAM-Schutz)
)

// thumbnailer erzeugt und cacht Thumbnails.
type thumbnailer struct {
	cacheDir string
	sem      chan struct{}
	mu       sync.Mutex // schützt gegen doppelte gleichzeitige Erzeugung desselben Hashes
	inflight map[string]*sync.Mutex
}

func newThumbnailer(dataDir string) *thumbnailer {
	dir := filepath.Join(dataDir, "thumb-cache")
	_ = os.MkdirAll(dir, 0o750)
	return &thumbnailer{
		cacheDir: dir,
		sem:      make(chan struct{}, thumbConcurrency),
		inflight: make(map[string]*sync.Mutex),
	}
}

func (t *thumbnailer) cachePath(hash string) string {
	// Hash ist 64 Hex-Zeichen → sicher als Dateiname.
	return filepath.Join(t.cacheDir, hash+".jpg")
}

// perHashLock gibt ein Mutex für genau diesen Hash zurück, sodass nicht zwei
// parallele Anfragen dasselbe Thumbnail gleichzeitig erzeugen.
func (t *thumbnailer) perHashLock(hash string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.inflight[hash]
	if !ok {
		m = &sync.Mutex{}
		t.inflight[hash] = m
	}
	return m
}

// get liefert das gecachte Thumbnail-JPEG für hash. Existiert es noch nicht,
// wird es aus den Originalbytes (über fetchOriginal) erzeugt und gecacht.
// fetchOriginal liefert die Originaldatei-Bytes (Aufrufer kapselt FileStore).
func (t *thumbnailer) get(ctx context.Context, hash string,
	fetchOriginal func(ctx context.Context) ([]byte, error)) ([]byte, error) {

	// 1. Cache-Treffer?
	if data, err := os.ReadFile(t.cachePath(hash)); err == nil && len(data) > 0 {
		return data, nil
	}

	// 2. Pro-Hash-Lock: verhindert doppelte gleichzeitige Erzeugung.
	hl := t.perHashLock(hash)
	hl.Lock()
	defer hl.Unlock()

	// Nach dem Lock erneut prüfen (anderer Goroutine kann fertig geworden sein).
	if data, err := os.ReadFile(t.cachePath(hash)); err == nil && len(data) > 0 {
		return data, nil
	}

	// 3. Original holen.
	orig, err := fetchOriginal(ctx)
	if err != nil {
		return nil, fmt.Errorf("original nicht abrufbar: %w", err)
	}
	if len(orig) > maxOriginalBytes {
		return nil, fmt.Errorf("original zu groß für Thumbnail (%d MiB > %d MiB)",
			len(orig)/(1024*1024), maxOriginalBytes/(1024*1024))
	}

	// 4. Semaphor: gleichzeitige Decodierungen begrenzen (RAM-Schutz).
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	thumb, err := makeThumbnail(orig)
	if err != nil {
		return nil, err
	}

	// 5. Cachen (Fehler hier sind nicht fatal — Thumbnail wird trotzdem geliefert).
	_ = os.WriteFile(t.cachePath(hash), thumb, 0o640)
	return thumb, nil
}

// makeThumbnail decodiert ein Bild, prüft die Pixelgrenze, skaliert auf
// thumbMaxEdge und encodet als JPEG. Reine Funktion (gut testbar mit der
// Pixel-/Format-Logik), abgesehen vom Decode der echten Bilddaten.
func makeThumbnail(orig []byte) ([]byte, error) {
	// Erst nur die Konfiguration lesen (billig, kein Voll-Decode) → Pixelgrenze
	// prüfen, BEVOR wir den großen Buffer allokieren.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(orig))
	if err != nil {
		return nil, fmt.Errorf("bild-konfig nicht lesbar: %w", err)
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return nil, fmt.Errorf("bild zu groß zum Verkleinern (%d MP > %d MP)",
			(cfg.Width*cfg.Height)/1_000_000, maxDecodePixels/1_000_000)
	}

	src, _, err := image.Decode(bytes.NewReader(orig))
	if err != nil {
		return nil, fmt.Errorf("bild nicht decodierbar: %w", err)
	}

	// Zielgröße proportional zur langen Kante berechnen.
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := thumbDimensions(w, h, thumbMaxEdge)

	// Box-Average-Downscaling, nur Standardbibliothek (kein externer Dependency).
	// Für Thumbnails völlig ausreichend und deutlich besser als Nearest-Neighbor.
	dst := boxDownscale(src, tw, th)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, fmt.Errorf("thumbnail encode: %w", err)
	}
	return out.Bytes(), nil
}

// boxDownscale verkleinert src auf (tw×th) per Box-Mittelung: jeder Zielpixel
// ist der Durchschnitt des zugehörigen Quellblocks. Reine Standardbibliothek.
// Speicher: nur das Zielbild zusätzlich (tw×th×4 ≈ 256 KiB bei 256²) — winzig.
func boxDownscale(src image.Image, tw, th int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	if tw <= 0 || th <= 0 || sw <= 0 || sh <= 0 {
		return dst
	}
	for ty := 0; ty < th; ty++ {
		// Quell-Zeilenbereich für diese Zielzeile.
		y0 := b.Min.Y + ty*sh/th
		y1 := b.Min.Y + (ty+1)*sh/th
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for tx := 0; tx < tw; tx++ {
			x0 := b.Min.X + tx*sw/tw
			x1 := b.Min.X + (tx+1)*sw/tw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var rs, gs, bs, as, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, bl, a := src.At(x, y).RGBA() // 16-bit pro Kanal
					rs += uint64(r)
					gs += uint64(g)
					bs += uint64(bl)
					as += uint64(a)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			dst.SetRGBA(tx, ty, color.RGBA{
				R: uint8((rs / n) >> 8),
				G: uint8((gs / n) >> 8),
				B: uint8((bs / n) >> 8),
				A: uint8((as / n) >> 8),
			})
		}
	}
	return dst
}

// thumbDimensions berechnet die Zielmaße, sodass die lange Kante maxEdge ist
// und das Seitenverhältnis erhalten bleibt. Kleinere Bilder werden NICHT
// hochskaliert (dann Originalmaße).
func thumbDimensions(w, h, maxEdge int) (int, int) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	if w <= maxEdge && h <= maxEdge {
		return w, h
	}
	if w >= h {
		return maxEdge, max1(h*maxEdge/w, 1)
	}
	return max1(w*maxEdge/h, 1), maxEdge
}

func max1(v, m int) int {
	if v < m {
		return m
	}
	return v
}

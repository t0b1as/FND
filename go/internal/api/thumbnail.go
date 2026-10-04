package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG-Decoder registrieren
	"os"
	"path/filepath"
	"strings"
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
	// Lange Kante des Thumbnails. 512 statt 256 seit R558: Im Bildraster des
	// Marktplatzes sind die Kacheln bis ~350 px breit, auf Displays mit
	// doppelter Pixeldichte entsprechend mehr – 256 px wirkten dort unscharf.
	thumbMaxEdge      = 512
	thumbJPEGQuality  = 90               // JPEG-Qualität (R559: 82 → 90, weniger Artefakte)
	// 24 MP (R594): Das dekodierte Bild belegt Breite×Höhe×4 Byte – bei 40 MP
	// allein 160 MB, bei zwei gleichzeitigen Bildern mehr als die
	// Speichergrenze des Dienstes. 24 MP deckt jede Handykamera ab.
	maxDecodePixels   = 24 * 1000 * 1000
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
	th := &thumbnailer{
		cacheDir: dir,
		sem:      make(chan struct{}, thumbConcurrency),
		inflight: make(map[string]*sync.Mutex),
	}
	go th.cleanOldThumbs() // Thumbnails früherer Kantenlängen aufräumen
	return th
}

func (t *thumbnailer) cachePath(hash string) string {
	// Hash ist 64 Hex-Zeichen → sicher als Dateiname. Die Kantenlänge steht mit
	// im Namen: Ändert sie sich (R558: 256 → 512), werden Thumbnails neu
	// erzeugt statt alte, unscharfe aus dem Cache zu liefern.
	return filepath.Join(t.cacheDir, fmt.Sprintf("%s-%d.jpg", hash, thumbMaxEdge))
}

// cleanOldThumbs entfernt Thumbnails früherer Kantenlängen (einmalig beim Start).
func (t *thumbnailer) cleanOldThumbs() {
	entries, err := os.ReadDir(t.cacheDir)
	if err != nil {
		return
	}
	suffix := fmt.Sprintf("-%d.jpg", thumbMaxEdge)
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, suffix) {
			continue
		}
		_ = os.Remove(filepath.Join(t.cacheDir, n))
	}
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

	// Lanczos-3-Downscaling (R559): deutlich schärfer und kantentreuer als die
	// frühere Box-Mittelung, nur Standardbibliothek.
	dst := lanczosDownscale(src, tw, th)

	var out bytes.Buffer
	// Als YCbCr 4:4:4 kodieren: Go halbiert bei RGBA die Farbauflösung (4:2:0),
	// was farbige Kanten ausfransen lässt. Unterstützt der Encoder 4:4:4 nicht,
	// ist das Ergebnis wie zuvor – schaden kann es nicht.
	if err := jpeg.Encode(&out, rgbaToYCbCr444(dst), &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, fmt.Errorf("thumbnail encode: %w", err)
	}
	return out.Bytes(), nil
}

// ── Lanczos-3-Skalierung (R559) ─────────────────────────────────────────────
// Separabel (erst waagerecht, dann senkrecht) mit vorab berechneten Gewichten.
// Der Filterradius wächst mit dem Verkleinerungsfaktor – das verhindert
// Treppenstufen an feinen Strukturen (Anti-Aliasing).

func lanczosKernel(x float64) float64 {
	const a = 3.0
	if x < 0 {
		x = -x
	}
	if x < 1e-8 {
		return 1
	}
	if x >= a {
		return 0
	}
	px := math.Pi * x
	return a * math.Sin(px) * math.Sin(px/a) / (px * px)
}

type tap struct {
	idx []int
	w   []float64
}

// buildTaps: für jedes Zielpixel die Quellpixel und ihre Gewichte.
func buildTaps(srcN, dstN int) []tap {
	const a = 3.0
	scale := float64(srcN) / float64(dstN)
	norm := math.Max(1, scale) // Filter beim Verkleinern aufweiten
	support := norm * a
	taps := make([]tap, dstN)
	for i := 0; i < dstN; i++ {
		center := (float64(i)+0.5)*scale - 0.5
		lo := int(math.Floor(center - support + 0.5))
		hi := int(math.Ceil(center + support - 0.5))
		n := hi - lo + 1
		if n < 1 {
			n = 1
			hi = lo
		}
		idx := make([]int, 0, n)
		w := make([]float64, 0, n)
		var sum float64
		for k := lo; k <= hi; k++ {
			weight := lanczosKernel((float64(k) - center) / norm)
			if weight == 0 {
				continue
			}
			c := k
			if c < 0 {
				c = 0
			} else if c >= srcN {
				c = srcN - 1
			}
			idx = append(idx, c)
			w = append(w, weight)
			sum += weight
		}
		if sum == 0 { // Notfall: nächster Nachbar
			c := int(center + 0.5)
			if c < 0 {
				c = 0
			} else if c >= srcN {
				c = srcN - 1
			}
			idx, w, sum = []int{c}, []float64{1}, 1
		}
		for k := range w {
			w[k] /= sum
		}
		taps[i] = tap{idx: idx, w: w}
	}
	return taps
}

// boxPreReduce verkleinert ganzzahlig um den Faktor k (Mittelwert je Block).
// Billig und speicherschonend – die Feinarbeit macht danach Lanczos.
func boxPreReduce(src image.Image, k int) *image.RGBA {
	b := src.Bounds()
	nw, nh := b.Dx()/k, b.Dy()/k
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	n := uint32(k * k)
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			var sr, sg, sb uint32
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					r, g, bl, _ := src.At(b.Min.X+x*k+dx, b.Min.Y+y*k+dy).RGBA()
					sr += r >> 8
					sg += g >> 8
					sb += bl >> 8
				}
			}
			out.SetRGBA(x, y, color.RGBA{R: uint8(sr / n), G: uint8(sg / n), B: uint8(sb / n), A: 255})
		}
	}
	return out
}

func lanczosDownscale(src image.Image, tw, th int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	if tw <= 0 || th <= 0 || sw <= 0 || sh <= 0 {
		return dst
	}
	// SPEICHER (R594): Vorher wurde das GANZE Quellbild als float64 gepuffert –
	// bei einem 12-Megapixel-Foto 288 MB. Zusammen mit einer zweiten Anfrage
	// sprengte das die Speichergrenze des Dienstes und der Node wurde beendet.
	// Jetzt erst ganzzahlig auf etwa das Doppelte der Zielgröße verkleinern
	// (billig), dann Lanczos darauf. Gleiche Bildqualität, ein Bruchteil des
	// Speichers; float32 statt float64 halbiert ihn zusätzlich.
	if k := min(sw/(2*tw), sh/(2*th)); k >= 2 {
		src = boxPreReduce(src, k)
		b = src.Bounds()
		sw, sh = b.Dx(), b.Dy()
	}
	buf := make([]float32, sw*sh*3)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			o := (y*sw + x) * 3
			buf[o] = float32(r >> 8)
			buf[o+1] = float32(g >> 8)
			buf[o+2] = float32(bl >> 8)
		}
	}
	// Waagerecht: sw → tw
	colTaps := buildTaps(sw, tw)
	tmp := make([]float32, tw*sh*3)
	for y := 0; y < sh; y++ {
		row := y * sw * 3
		out := y * tw * 3
		for i, t := range colTaps {
			var cr, cg, cb float32
			for k, si := range t.idx {
				o := row + si*3
				w := float32(t.w[k])
				cr += buf[o] * w
				cg += buf[o+1] * w
				cb += buf[o+2] * w
			}
			tmp[out+i*3] = cr
			tmp[out+i*3+1] = cg
			tmp[out+i*3+2] = cb
		}
	}
	// Senkrecht: sh → th
	rowTaps := buildTaps(sh, th)
	clamp := func(v float32) uint8 {
		if v <= 0 {
			return 0
		}
		if v >= 255 {
			return 255
		}
		return uint8(v + 0.5)
	}
	for j, t := range rowTaps {
		for x := 0; x < tw; x++ {
			var cr, cg, cb float32
			for k, sy := range t.idx {
				o := (sy*tw + x) * 3
				w := float32(t.w[k])
				cr += tmp[o] * w
				cg += tmp[o+1] * w
				cb += tmp[o+2] * w
			}
			dst.SetRGBA(x, j, color.RGBA{R: clamp(cr), G: clamp(cg), B: clamp(cb), A: 255})
		}
	}
	return dst
}

// rgbaToYCbCr444 wandelt ohne Farb-Unterabtastung um.
func rgbaToYCbCr444(src *image.RGBA) *image.YCbCr {
	b := src.Bounds()
	out := image.NewYCbCr(b, image.YCbCrSubsampleRatio444)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			i := src.PixOffset(x, y)
			yy, cb, cr := color.RGBToYCbCr(src.Pix[i], src.Pix[i+1], src.Pix[i+2])
			out.Y[out.YOffset(x, y)] = yy
			o := out.COffset(x, y)
			out.Cb[o] = cb
			out.Cr[o] = cr
		}
	}
	return out
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

package api

// Video-Wiedergabe für Formate, die Browser nicht direkt abspielen.
//
// Browser spielen MP4 und WebM. Viele Dateien (MKV, TS, MOV, FLV …) enthalten
// aber abspielbare Bilddaten (H.264/VP9/AV1) in einer anderen "Verpackung"
// und/oder eine Tonspur, die Browser nicht kennen (AC3, DTS, TrueHD).
//
//   GET /api/v1/files/probe/:hash       → Container, Codecs, Dauer (ffprobe)
//   GET /api/v1/files/stream/:hash?t=S  → ab Sekunde S als fragmentiertes MP4
//
// Umverpackt wird OHNE Neukodierung des Bildes (-c:v copy) – das schafft auch
// ein Pi 3. Nur der Ton wird bei Bedarf nach AAC (Stereo) gewandelt. ffmpeg
// liest die Quelle über die eigene Range-Schnittstelle, lädt also nur die
// benötigten Teile. Codecs, die nur durch Neukodierung abspielbar würden
// (MPEG-4 Part 2/XviD, WMV …), werden abgelehnt – dafür ist der Pi zu schwach.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Browser-taugliche Codecs (Bild direkt kopierbar / Ton ohne Wandlung).
var (
	browserVideo = map[string]bool{"h264": true, "vp8": true, "vp9": true, "av1": true, "hevc": true}
	browserAudio = map[string]bool{"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true}
	directVideo  = map[string]bool{"h264": true, "vp8": true, "vp9": true, "av1": true}
)

type mediaProbe struct {
	Available bool    `json:"available"`          // ffmpeg installiert
	Duration  float64 `json:"duration"`           // Sekunden
	Format    string  `json:"format"`             // Container laut ffprobe
	VCodec    string  `json:"vcodec"`
	ACodec    string  `json:"acodec"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Direct    bool    `json:"direct"`             // Browser kann die Datei direkt abspielen
	Remux     bool    `json:"remux"`              // per Umverpacken abspielbar
	Reason    string  `json:"reason,omitempty"`   // warum weder noch
}

var (
	probeCache   sync.Map                  // hash → *mediaProbe
	streamSlots  = make(chan struct{}, 2)  // höchstens 2 gleichzeitige Umverpackungen
)

func ffmpegAvailable() bool {
	_, e1 := exec.LookPath("ffmpeg")
	_, e2 := exec.LookPath("ffprobe")
	return e1 == nil && e2 == nil
}

func (s *Server) localDownloadURL(hash string) string {
	port := 3000
	if s.cfg != nil && s.cfg.Port > 0 {
		port = s.cfg.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d/api/v1/files/download/%s", port, hash)
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (s *Server) probeMedia(ctx context.Context, hash string) *mediaProbe {
	if v, ok := probeCache.Load(hash); ok {
		return v.(*mediaProbe)
	}
	p := &mediaProbe{Available: ffmpegAvailable()}
	if !p.Available {
		p.Reason = "ffmpeg ist auf diesem Node nicht installiert"
		return p
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ffprobe", "-v", "error",
		"-show_entries", "format=duration,format_name:stream=codec_type,codec_name,width,height",
		"-of", "json", s.localDownloadURL(hash)).Output()
	if err != nil {
		p.Reason = "Datei konnte nicht analysiert werden"
		return p
	}
	var r struct {
		Format struct {
			Duration string `json:"duration"`
			Name     string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			Type   string `json:"codec_type"`
			Codec  string `json:"codec_name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &r) != nil {
		p.Reason = "Analyse unlesbar"
		return p
	}
	p.Duration, _ = strconv.ParseFloat(r.Format.Duration, 64)
	p.Format = r.Format.Name
	for _, st := range r.Streams {
		switch {
		case st.Type == "video" && p.VCodec == "" && st.Codec != "mjpeg" && st.Codec != "png":
			p.VCodec, p.Width, p.Height = st.Codec, st.Width, st.Height
		case st.Type == "audio" && p.ACodec == "":
			p.ACodec = st.Codec
		}
	}
	audioOK := p.ACodec == "" || browserAudio[p.ACodec]
	// Direkt nur echte MP4-/WebM-Dateien. MKV (ffprobe meldet es als
	// "matroska,webm") spielt Chrome zwar über seinen WebM-Leser ab, der wertet
	// bei H.264 mit B-Frames / AAC-Ton die Zeitstempel aber ungenau aus →
	// Tonversatz. MKV daher immer sauber nach MP4 umverpacken.
	ext := ""
	if s.fileStore != nil {
		ext = strings.ToLower(filepath.Ext(s.fileStore.NameForHash(hash)))
	}
	nativeContainer := strings.Contains(p.Format, "mp4") ||
		(strings.Contains(p.Format, "webm") && ext == ".webm")
	p.Direct = nativeContainer && directVideo[p.VCodec] && audioOK
	p.Remux = browserVideo[p.VCodec]
	if !p.Direct && !p.Remux {
		if p.VCodec == "" {
			p.Remux = p.ACodec != "" // reine Tondatei: nach AAC wandeln
		} else {
			p.Reason = "Videocodec „" + p.VCodec + "“ spielt kein Browser ab; eine Neukodierung schafft der Pi nicht"
		}
	}
	probeCache.Store(hash, p)
	return p
}

// GET /api/v1/files/probe/:hash
func (s *Server) fileProbe(c *gin.Context) {
	s.applyPeerHint(c)
	hash := strings.ToLower(c.Param("hash"))
	if !validHash(hash) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Hash"})
		return
	}
	c.JSON(http.StatusOK, s.probeMedia(c.Request.Context(), hash))
}

// GET /api/v1/files/stream/:hash?t=SEKUNDEN
func (s *Server) fileStream(c *gin.Context) {
	s.applyPeerHint(c)
	hash := strings.ToLower(c.Param("hash"))
	if !validHash(hash) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Hash"})
		return
	}
	p := s.probeMedia(c.Request.Context(), hash)
	if !p.Available {
		c.JSON(http.StatusNotImplemented, gin.H{"error": p.Reason})
		return
	}
	if !p.Remux {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": p.Reason})
		return
	}
	select {
	case streamSlots <- struct{}{}:
		defer func() { <-streamSlots }()
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Es laufen bereits 2 Wiedergaben – bitte eine beenden"})
		return
	}

	t, _ := strconv.ParseFloat(c.Query("t"), 64)
	if t < 0 || (p.Duration > 0 && t >= p.Duration) {
		t = 0
	}
	// Ton-Versatz von Hand (ms, + = Ton später). Umsetzung: Tonspur als zweiter
	// Eingang mit -itsoffset (funktioniert in beide Richtungen).
	aMs, _ := strconv.Atoi(c.Query("a"))
	if aMs > 5000 {
		aMs = 5000
	} else if aMs < -5000 {
		aMs = -5000
	}
	src := s.localDownloadURL(hash)
	// Eingangsoptionen: Zeitstempel erzeugen, wo sie fehlen; beim Sprung Bild
	// UND Ton am selben Keyframe beginnen lassen (-noaccurate_seek). Sonst
	// startet das kopierte Bild am Keyframe VOR der Stelle, der gewandelte Ton
	// exakt an der Stelle – Versatz = Abstand zum Keyframe (oft Sekunden).
	input := func(extra ...string) []string {
		in := []string{"-fflags", "+genpts"}
		if t > 0 {
			in = append(in, "-ss", strconv.FormatFloat(t, 'f', 2, 64), "-noaccurate_seek")
		}
		in = append(in, extra...)
		return append(in, "-i", src)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, input()...)
	audioIn := "0"
	if aMs != 0 && p.ACodec != "" {
		args = append(args, input("-itsoffset", strconv.FormatFloat(float64(aMs)/1000, 'f', 3, 64))...)
		audioIn = "1"
	}
	args = append(args, "-map", "0:v:0?", "-map", audioIn+":a:0?", "-sn", "-dn")
	if p.VCodec != "" {
		args = append(args, "-c:v", "copy")
		if p.VCodec == "hevc" {
			args = append(args, "-tag:v", "hvc1") // für Safari/Edge mit HEVC-Unterstützung
		}
	}
	if p.ACodec != "" {
		if p.ACodec == "aac" {
			args = append(args, "-c:a", "copy")
		} else {
			// aresample=async: gleicht Lücken/Drift der Tonspur an den Zeitstempeln aus.
			args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "160k",
				"-af", "aresample=async=1:first_pts=0")
		}
	}
	// Negative Zeitstempel (z.B. nach Versatz) für ALLE Spuren gleich
	// verschieben – der relative Versatz bleibt erhalten.
	args = append(args, "-avoid_negative_ts", "make_zero", "-max_muxing_queue_size", "1024")
	args = append(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")

	// Endet, sobald der Browser die Verbindung schließt (Sprung, Schließen).
	cmd := exec.CommandContext(c.Request.Context(), "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ffmpeg: " + err.Error()})
		return
	}
	c.Header("Content-Type", "video/mp4")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Media-Offset", strconv.FormatFloat(t, 'f', 2, 64))
	c.Status(http.StatusOK)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				break
			}
			c.Writer.Flush()
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil && c.Request.Context().Err() == nil {
		s.log.Warn("Umverpacken fehlgeschlagen", zap.String("hash", hash[:16]),
			zap.String("ffmpeg", strings.TrimSpace(stderr.String())))
	}
}

// applyPeerHint übernimmt ?peer=<PeerID> (Besitzer eines Netzwerksuche-
// Treffers) als bevorzugte Quelle für Manifest und Chunks.
func (s *Server) applyPeerHint(c *gin.Context) {
	p := strings.TrimSpace(c.Query("peer"))
	if p == "" || p == "local" || s.fileStore == nil {
		return
	}
	if _, err := peer.Decode(p); err != nil {
		return
	}
	s.fileStore.AddPeerHint(p)
}

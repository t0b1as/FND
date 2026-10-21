package api

// Streamen aus einer Freigabe (R716).
//
// ── Was in R715 falsch war ─────────────────────────────────────────────────
//
// Ich hatte behauptet, Chromium spiele Matroska ab, wenn der Node es als
// video/webm ausweist. Das stimmt nicht. WebM ist zwar ein Matroska-Untersatz,
// aber Chromes Zerleger verlangt auch WebM-CODECS: VP8, VP9 oder AV1. Ein
// übliches .mkv enthält H.264 oder HEVC und wird deshalb abgelehnt, egal welcher
// Inhaltstyp daransteht. Die Datei direkt auszuliefern kann also nicht
// funktionieren.
//
// ── Was tatsächlich hilft ──────────────────────────────────────────────────
//
// Umpacken statt umwandeln: Das Bild wird unverändert durchgereicht (-c:v copy),
// nur der Ton bei Bedarf nach AAC gewandelt, und das Ganze als fragmentiertes
// MP4 ausgegeben. Das kostet kaum Rechenzeit – auch ein Pi 3 schafft es, weil
// das Bild gar nicht angefasst wird.
//
// Genau das gibt es für Chunk-Dateien längst (media_stream.go). Für Freigaben
// fehlte es. Hier derselbe Weg, nur mit dem Dateipfad als Quelle statt eines
// Inhalts-Hashes – das spart zusätzlich den Umweg über HTTP.
//
// ── Der Preis, den man kennen muss ─────────────────────────────────────────
//
// Umgepackt wird fortlaufend, deshalb gibt es KEINE Bereichsabfragen: Springen
// im Film funktioniert nur über den Startzeitpunkt (?t=Sekunde), wofür die
// Wiedergabe neu beginnt. Dateien, die der Browser von sich aus abspielt (MP4,
// WebM), werden deshalb weiterhin direkt ausgeliefert – dort ist Springen
// verlustfrei möglich. Umgepackt wird nur, was sonst gar nicht liefe.

import (
	"net/url"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// shareProbe: was in der Datei steckt.
type shareProbe struct {
	VCodec string
	ACodec string
}

// probeShareFile fragt ffprobe nach den Codecs der Datei.
func probeShareFile(ctx context.Context, pfad string) shareProbe {
	var p shareProbe
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ffprobe", "-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-of", "json", pfad).Output()
	if err != nil {
		return p
	}
	var r struct {
		Streams []struct {
			Type  string `json:"codec_type"`
			Codec string `json:"codec_name"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &r) != nil {
		return p
	}
	for _, st := range r.Streams {
		switch st.Type {
		case "video":
			if p.VCodec == "" {
				p.VCodec = st.Codec
			}
		case "audio":
			if p.ACodec == "" {
				p.ACodec = st.Codec
			}
		}
	}
	return p
}

// GET /api/v1/shares/:id/stream/*path?t=Sekunde
//
// Liefert die Datei als fragmentiertes MP4, Bild unverändert durchgereicht.
func (s *Server) shareStream(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	if !ffmpegAvailable() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Umpacken nicht möglich: ffmpeg ist auf diesem Node nicht installiert."})
		return
	}
	requester := s.shareRequester(c)
	pfad, err := mgr.ResolveForRead(c.Param("id"), requester, c.Param("path"))
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "verweigert") {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	// R734: Bei HEAD nur Auskunft geben, KEIN ffmpeg starten. Sonst erzeugt
	// jede Vorab-Abfrage eines Abspielers einen Umpack-Lauf, der sofort wieder
	// verworfen wird – auf einem Pi ist das spürbar.
	//
	// Ein umgepackter Strom entsteht fortlaufend, seine Länge steht also nicht
	// vorher fest. Deshalb hier bewusst KEIN Content-Length und KEIN
	// Accept-Ranges: Beides zu behaupten und dann nicht zu liefern wäre
	// schlimmer als die ehrliche Auskunft, dass nicht gespult werden kann.
	if c.Request.Method == http.MethodHead {
		c.Header("Content-Type", "video/mp4")
		c.Header("Accept-Ranges", "none")
		c.Status(http.StatusOK)
		return
	}

	t, _ := strconv.ParseFloat(c.Query("t"), 64)
	if t < 0 {
		t = 0
	}
	p := probeShareFile(c.Request.Context(), pfad)
	if p.VCodec == "" && p.ACodec == "" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error": "Keine abspielbare Spur gefunden."})
		return
	}
	// R719: Den Bildcodec MITTEILEN, bevor umgepackt wird.
	//
	// Umpacken wechselt nur das Behältnis – das Bild wird unverändert
	// durchgereicht. Das setzt voraus, dass der Browser diesen Codec selbst
	// dekodiert. Bei HEVC (H.265), wie in 4K-HDR-Dateien üblich, kann Chrome
	// das meist NICHT: Dann läuft nur der Ton, und das sieht aus wie ein
	// Fehler, obwohl alles wie vorgesehen arbeitet.
	//
	// Umwandeln wäre die Alternative, scheidet aber praktisch aus: 4K HEVC nach
	// H.264 zu wandeln schafft kein Raspberry Pi auch nur annähernd in
	// Echtzeit. Deshalb die ehrliche Auskunft statt eines stummen Bildes.
	c.Header("X-Video-Codec", p.VCodec)
	c.Header("X-Audio-Codec", p.ACodec)

	s.ffmpegStreamen(c, pfad, p, t)
}

// ffmpegStreamen packt eine Quelle (Dateipfad ODER Adresse) als fragmentiertes
// MP4 um und schickt das Ergebnis fortlaufend an den Browser (R718).
//
// Als gemeinsame Funktion, weil beide Wege – eigene und fremde Freigabe –
// genau dasselbe tun und sich nur in der Quelle unterscheiden. Zwei Kopien
// wären hier besonders gefährlich: Die ffmpeg-Parameter sind heikel (Versatz
// zwischen Bild und Ton), und eine Korrektur an nur einer Stelle fiele erst
// beim Ansehen auf.
func (s *Server) ffmpegStreamen(c *gin.Context, quelle string, p shareProbe, t float64) {
	// Eingangsoptionen: Zeitstempel erzeugen, wo sie fehlen, und beim Sprung
	// Bild UND Ton am selben Keyframe beginnen lassen. Ohne -noaccurate_seek
	// startet das kopierte Bild am Keyframe VOR der Stelle, der gewandelte Ton
	// exakt an der Stelle – der Versatz wäre der Abstand zum Keyframe.
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-fflags", "+genpts"}
	if t > 0 {
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 2, 64), "-noaccurate_seek")
	}
	args = append(args, "-i", quelle, "-map", "0:v:0?", "-map", "0:a:0?", "-sn", "-dn")
	if p.VCodec != "" {
		args = append(args, "-c:v", "copy")
		if p.VCodec == "hevc" {
			args = append(args, "-tag:v", "hvc1")
		}
	}
	if p.ACodec != "" {
		if p.ACodec == "aac" {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "160k",
				"-af", "aresample=async=1:first_pts=0")
		}
	}
	args = append(args, "-avoid_negative_ts", "make_zero", "-max_muxing_queue_size", "1024",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")

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
	if s.log != nil {
		s.log.Info("Freigabe wird umgepackt",
			zap.String("quelle", filepath.Base(quelle)),
			zap.String("video", p.VCodec), zap.String("ton", p.ACodec),
			zap.Float64("ab_sekunde", t))
	}

	c.Header("Content-Type", "video/mp4")
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				break // Browser hat abgebrochen (Sprung, Fenster zu)
			}
			c.Writer.Flush()
		}
		if rerr != nil {
			break
		}
	}
	_ = cmd.Wait()
	if stderr.Len() > 0 && s.log != nil {
		s.log.Debug("ffmpeg-Meldung", zap.String("text", stderr.String()))
	}
}

// port liefert den HTTP-Port dieses Node (R718). Gebraucht, damit ffmpeg den
// ortseigenen Fernstrom als Eingang benutzen kann.
func (s *Server) port() int {
	if s.cfg != nil && s.cfg.Port > 0 {
		return s.cfg.Port
	}
	return 3000
}

// urlQuery kodiert einen Pfad für die Verwendung als Abfragewert.
func urlQuery(v string) string { return url.QueryEscape(v) }

// GET /api/v1/shares/:id/probe/*path
//
// Was steckt in der Datei, und kann der Browser es abspielen? (R719)
// Die Oberfläche fragt das VOR dem Öffnen, damit sie bei HEVC gleich sagen
// kann, dass nur der Ton kommt – statt ein schwarzes Bild zu zeigen.
func (s *Server) shareProbeFile(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	if !ffmpegAvailable() {
		c.JSON(http.StatusOK, gin.H{"available": false,
			"reason": "ffmpeg ist auf diesem Node nicht installiert"})
		return
	}
	pfad, err := mgr.ResolveForRead(c.Param("id"), s.shareRequester(c), c.Param("path"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	p := probeShareFile(c.Request.Context(), pfad)
	c.JSON(http.StatusOK, gin.H{
		"available":     true,
		"video":         p.VCodec,
		"audio":         p.ACodec,
		"browser_video": browserKannVideo(p.VCodec),
	})
}

// browserKannVideo: Kann ein üblicher Browser diesen Bildcodec dekodieren,
// wenn er in MP4 verpackt ist?
//
// H.264 kann jeder. VP8/VP9/AV1 in WebM ebenfalls, in MP4 je nach Browser.
// HEVC hängt an Hardware und Plattform und ist deshalb als "nein" eingestuft –
// lieber eine Warnung zu viel als ein stummes schwarzes Bild.
func browserKannVideo(codec string) bool {
	switch strings.ToLower(codec) {
	case "h264", "avc1", "vp8", "vp9", "av1":
		return true
	}
	return false
}

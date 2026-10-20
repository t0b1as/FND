package api

// Fremde Freigaben strömen statt am Stück übertragen (R718).
//
// Bisher holte remoteShareDl die GANZE Datei in einem Zug über das P2P-Netz –
// mit 64 MB Obergrenze und stillschweigendem Abschneiden darüber. Bei fremden
// Freigaben war damit nicht nur Streamen unmöglich, sondern jede größere Datei
// überhaupt, und ein Download über 64 MB lieferte eine kaputte Datei ohne jede
// Warnung.
//
// Hier wird stattdessen abschnittsweise geholt: Größe erfragen, dann Blöcke von
// je 1 MiB, und was ankommt, geht sofort an den Browser weiter. Der Speicher­
// bedarf bleibt auf beiden Seiten bei wenigen Megabyte – entscheidend auf einem
// Pi mit 768 MB, wo der alte Weg beide Seiten gleichzeitig die ganze Datei
// halten ließ.
//
// Bereichsabfragen des Browsers werden durchgereicht. Damit springt ein Video
// und startet sofort, statt erst nach dem vollständigen Download.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/filestore"
)

// fernBlock: Größe eines über P2P geholten Abschnitts. 1 MiB ist ein
// Kompromiss: groß genug, dass der Netzaufwand je Block nicht ins Gewicht
// fällt, klein genug für einen Pi mit wenig Speicher.
const fernBlock int64 = 1 << 20

// fernFrage stellt eine Anfrage an den Besitzer-Node.
func (s *Server) fernFrage(ctx context.Context, peer string, req any) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cctx, abbr := context.WithTimeout(ctx, 30*time.Second)
	defer abbr()
	return s.node.SendAndReceive(cctx, peer, filestore.SharesProtocol, raw)
}

// fernGroesse erfragt die Dateigröße beim Besitzer.
func (s *Server) fernGroesse(ctx context.Context, peer, share, pfad string) (int64, error) {
	resp, err := s.fernFrage(ctx, peer, map[string]any{
		"type": "stat", "share": share, "path": pfad,
	})
	if err != nil {
		return 0, err
	}
	var r struct {
		Size  int64  `json:"size"`
		Error string `json:"error"`
	}
	if json.Unmarshal(resp, &r) != nil {
		return 0, fmt.Errorf("unverständliche Antwort")
	}
	if r.Error != "" {
		return 0, fmt.Errorf("%s", r.Error)
	}
	return r.Size, nil
}

// GET /api/v1/shares/remote/:peer/:share/stream?path=…
//
// Liefert eine Datei aus einer FREMDEN Freigabe, abschnittsweise geholt und
// mit Unterstützung für Bereichsabfragen.
func (s *Server) remoteShareStreamFile(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	peer := c.Param("peer")
	share := c.Param("share")
	pfad := c.Query("path")
	if pfad == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path fehlt"})
		return
	}

	gesamt, err := s.fernGroesse(c.Request.Context(), peer, share, pfad)
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "verweigert") {
			status = http.StatusForbidden
		} else if strings.Contains(err.Error(), "nicht gefunden") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if gesamt <= 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Datei ist leer oder nicht lesbar"})
		return
	}

	name := filepath.Base(pfad)
	von, bis := int64(0), gesamt-1
	teilweise := false

	// Bereichsabfrage des Browsers auswerten. Nur die einfache Form
	// "bytes=von-bis" – mehrteilige Bereiche fragt kein Abspieler an, und sie
	// zu unterstützen wäre viel Aufwand für einen Fall, der nie eintritt.
	if hdr := c.GetHeader("Range"); strings.HasPrefix(hdr, "bytes=") {
		teil := strings.TrimPrefix(hdr, "bytes=")
		if i := strings.IndexByte(teil, ','); i >= 0 {
			teil = teil[:i]
		}
		a, b, _ := strings.Cut(teil, "-")
		if a == "" && b != "" {
			// "-N": die letzten N Bytes
			if n, e := strconv.ParseInt(b, 10, 64); e == nil && n > 0 {
				if n > gesamt {
					n = gesamt
				}
				von = gesamt - n
				teilweise = true
			}
		} else {
			if n, e := strconv.ParseInt(a, 10, 64); e == nil && n >= 0 {
				von = n
				teilweise = true
			}
			if b != "" {
				if n, e := strconv.ParseInt(b, 10, 64); e == nil && n >= von {
					bis = n
				}
			}
		}
		if von >= gesamt {
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", gesamt))
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if bis >= gesamt {
			bis = gesamt - 1
		}
	}

	art := "inline"
	if c.Query("dl") != "" {
		art = "attachment"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, art, name))
	if ct := freigabeMime(name); ct != "" {
		c.Header("Content-Type", ct)
	}
	// Ohne diese Zusage versucht der Browser gar keine Bereichsabfrage und lädt
	// alles von vorn – Springen im Video wäre dann unmöglich.
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Length", strconv.FormatInt(bis-von+1, 10))
	if teilweise {
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", von, bis, gesamt))
		c.Status(http.StatusPartialContent)
	} else {
		c.Status(http.StatusOK)
	}

	// Abschnittsweise holen und sofort weiterschreiben.
	pos := von
	for pos <= bis {
		laenge := fernBlock
		if rest := bis - pos + 1; rest < laenge {
			laenge = rest
		}
		data, err := s.fernFrage(c.Request.Context(), peer, map[string]any{
			"type": "range", "share": share, "path": pfad,
			"off": pos, "len": laenge,
		})
		if err != nil {
			if s.log != nil {
				s.log.Warn("Fern-Abschnitt nicht erhalten", zap.Int64("ab", pos), zap.Error(err))
			}
			return // Kopfzeilen sind raus; der Strom endet hier
		}
		// Fehlerantworten kommen als JSON – rohe Daten beginnen praktisch nie
		// mit {"error". Der Vergleich ist deshalb ausreichend und billig.
		if len(data) > 8 && string(data[:8]) == `{"error"` {
			if s.log != nil {
				s.log.Warn("Fern-Abschnitt abgelehnt", zap.String("antwort", string(data[:120])))
			}
			return
		}
		if len(data) == 0 {
			return // Dateiende früher als erwartet
		}
		if _, werr := c.Writer.Write(data); werr != nil {
			return // Browser hat abgebrochen (Sprung, Fenster zu)
		}
		c.Writer.Flush()
		pos += int64(len(data))
	}
}

// GET /api/v1/shares/remote/:peer/:share/remux?path=…&t=Sekunde
//
// Umpacken einer FREMDEN Freigabe (R718).
//
// Das geht erst jetzt, und zwar wegen des strömenden Weges oben: ffmpeg
// bekommt als Eingang die ORTSEIGENE Adresse des Fernstroms. Dadurch holt es
// nur die Abschnitte, die es gerade braucht, statt dass die Datei vorher
// vollständig übertragen werden müsste. Genau so arbeitet das Umpacken der
// Chunk-Dateien schon lange (media_stream.go).
//
// Der Film wird dabei weiterhin NICHT umgewandelt – nur das Behältnis wechselt,
// und der Ton falls nötig. Die Last liegt beim fragenden Node; der Besitzer
// liefert bloß Bytes.
func (s *Server) remoteShareRemux(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	if !ffmpegAvailable() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Umpacken nicht möglich: ffmpeg ist auf diesem Node nicht installiert."})
		return
	}
	peer, share, pfad := c.Param("peer"), c.Param("share"), c.Query("path")
	if pfad == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path fehlt"})
		return
	}
	// Erreichbarkeit und Zugriff vorab klären – sonst scheitert erst ffmpeg,
	// und der Browser bekäme einen abgebrochenen Strom statt einer Auskunft.
	if _, err := s.fernGroesse(c.Request.Context(), peer, share, pfad); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	quelle := fmt.Sprintf("http://127.0.0.1:%d/api/v1/shares/remote/%s/%s/stream?path=%s",
		s.port(), peer, share, urlQuery(pfad))

	t, _ := strconv.ParseFloat(c.Query("t"), 64)
	if t < 0 {
		t = 0
	}
	p := probeShareFile(c.Request.Context(), quelle)
	if p.VCodec == "" && p.ACodec == "" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Keine abspielbare Spur gefunden."})
		return
	}
	if s.log != nil {
		s.log.Info("Fremde Freigabe wird umgepackt", zap.String("peer", peer),
			zap.String("video", p.VCodec), zap.String("ton", p.ACodec))
	}
	s.ffmpegStreamen(c, quelle, p, t)
}

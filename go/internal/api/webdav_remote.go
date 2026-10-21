package api

// WebDAV über P2P für FREMDE Freigaben (R726).
//
// In R725 hatte ich behauptet, das würde sich zu zäh anfühlen. Das war eine
// Vermutung, und Vermutungen haben mich in dieser Sitzung mehrfach in die Irre
// geführt. Also gebaut – samt Zeitmessung, damit sich die Frage beantworten
// lässt, statt sie zu beantworten.
//
// ── Wie es arbeitet ────────────────────────────────────────────────────────
//
// Der fragende Node spielt den WebDAV-Server für Windows und Office und
// übersetzt jeden Befehl in das P2P-Freigabeprotokoll:
//
//   PROPFIND → ls / stat        GET → range (abschnittsweise)
//   PUT      → putrange         DELETE → delete
//   MKCOL    → mkcol            MOVE   → move
//
// Alles läuft abschnittsweise, der Speicherbedarf bleibt also klein.
//
// ── Was dabei langsam sein wird ────────────────────────────────────────────
//
// Jeder Befehl ist eine eigene P2P-Anfrage mit Hin- und Rückweg. Beim Lesen
// fällt das kaum auf, weil 1 MiB je Anfrage übertragen wird. Beim Durchsehen
// eines Ordners mit vielen Dateien ebenfalls nicht, weil "ls" alles auf einmal
// liefert. Spürbar wird es, wenn Windows für jede Datei einzeln Eigenschaften
// abfragt, was der Explorer gern tut.
//
// Jede Anfrage wird deshalb mit ihrer Dauer protokolliert. Nach ein paar
// Minuten Benutzung steht im Protokoll, wo die Zeit hingeht.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// fernDavBlock: Abschnittsgröße beim Schreiben. Kleiner als beim Lesen, weil
// base64 ein Drittel aufschlägt und die Antwort in eine Nachricht passen muss.
const fernDavBlock = 512 << 10 // 512 KiB

// davFernAuth: dieselbe Kennung wie bei eigenen Freigaben, aber an Peer UND
// Freigabe gebunden.
func (s *Server) davFernAuth(c *gin.Context, peer, share string) bool {
	if s.sessionFID(c) != "" {
		return true
	}
	if _, pass, ok := c.Request.BasicAuth(); ok &&
		s.shareTokenGueltig(pass, "dav-remote:"+peer+":"+share, "") {
		return true
	}
	// R732: Digest, damit Windows über HTTP überhaupt Zugangsdaten herausgibt.
	if digestPruefen(c, s.fernDavToken(peer, share)) {
		return true
	}
	davAnmeldungVerlangen(c)
	return false
}

// fernDavToken stellt die Kennung aus (unbefristet, wie bei eigenen Freigaben).
func (s *Server) fernDavToken(peer, share string) string {
	return s.shareTokenBauen("dav-remote:"+peer+":"+share, "", davOhneFrist)
}

// fernOp führt eine Protokollanfrage aus und misst die Dauer.
func (s *Server) fernOp(ctx context.Context, peer string, req map[string]any) ([]byte, time.Duration, error) {
	start := time.Now()
	resp, err := s.fernFrage(ctx, peer, req)
	dauer := time.Since(start)
	if s.log != nil {
		art, _ := req["type"].(string)
		// R737: Debug. Je Befehl eine Zeile – beim Durchsehen eines Ordners
		// sind das dutzende. Die Dauer bleibt für die Fehlersuche wertvoll,
		// aber nicht im Normalbetrieb.
		s.log.Debug("WebDAV über P2P",
			zap.String("befehl", art),
			zap.Duration("dauer", dauer),
			zap.Int("bytes", len(resp)),
			zap.Bool("fehler", err != nil))
	}
	return resp, dauer, err
}

// davRemoteHandler bedient /davr/:peer/:share/*path (R741).
func (s *Server) davRemoteHandler(c *gin.Context) {
	if s.node == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	peer, share := c.Param("peer"), c.Param("share")
	if s.log != nil {
		_, _, hatAuth := c.Request.BasicAuth()
		s.log.Info("WebDAV-Anfrage (fern)", zap.String("methode", c.Request.Method),
			zap.String("pfad", c.Request.URL.Path),
			zap.Bool("mit_anmeldung", hatAuth),
			zap.String("von", c.ClientIP()))
	}
	if !s.davFernAuth(c, peer, share) {
		return
	}
	virt := strings.TrimPrefix(c.Param("path"), "/")
	basis := "/davr/" + peer + "/" + share + "/"
	ctx := c.Request.Context()

	switch c.Request.Method {

	case "OPTIONS":
		c.Header("DAV", "1,2")
		c.Header("MS-Author-Via", "DAV")
		c.Header("Allow", "OPTIONS, GET, HEAD, PROPFIND, PUT, DELETE, MKCOL, MOVE, LOCK, UNLOCK, PROPPATCH")
		c.Status(http.StatusOK)

	case "PROPFIND":
		tiefe := c.GetHeader("Depth")
		if tiefe == "" {
			tiefe = "1"
		}
		ms := davMultistatus{XMLNS: "DAV:"}

		// Erst als Verzeichnis versuchen. Klappt das nicht, ist es eine Datei –
		// dann die Größe erfragen. Zwei Anfragen im schlechtesten Fall, aber
		// das Protokoll kennt kein "was ist das hier".
		resp, _, err := s.fernOp(ctx, peer, map[string]any{
			"type": "ls", "share": share, "path": virt,
		})
		var lr struct {
			Entries []struct {
				Name    string    `json:"name"`
				IsDir   bool      `json:"is_dir"`
				Size    int64     `json:"size"`
				ModTime time.Time `json:"mod_time"`
			} `json:"entries"`
			Error string `json:"error"`
		}
		istOrdner := err == nil && json.Unmarshal(resp, &lr) == nil && lr.Error == ""

		if istOrdner {
			selbst := basis
			if virt != "" {
				selbst = basis + davPfad(virt) + "/"
			}
			name := share
			if virt != "" {
				name = path.Base(virt)
			}
			ms.Responses = append(ms.Responses, davEintrag(selbst, name, true, 0, time.Now()))
			if tiefe != "0" {
				vor := basis
				if virt != "" {
					vor = basis + davPfad(virt) + "/"
				}
				for _, e := range lr.Entries {
					// R749: Verzeichnisse MÜSSEN mit Schrägstrich enden.
					//
					// WebDAV unterscheidet Sammlungen von Dateien auch an der
					// Adresse. Ohne den Schrägstrich behandeln Klienten einen
					// Ordner als Datei oder verwerfen den Eintrag – bei davfs2
					// blieb der eingehängte Ordner dadurch leer, obwohl alle
					// 16 Einträge im XML standen.
					href := vor + davPfad(e.Name)
					if e.IsDir {
						href += "/"
					}
					ms.Responses = append(ms.Responses,
						davEintrag(href, e.Name, e.IsDir, e.Size, e.ModTime))
				}
			}
		} else {
			// R744: WARUM wurde das nicht als Verzeichnis erkannt?
			//
			// Genau hier entsteht der 404, den davfs2 beim Einhängen meldet –
			// und davfs2 verrät nur die nackte Zahl. Ohne diese Zeile lässt
			// sich nicht unterscheiden, ob die Gegenstelle nicht antwortet, den
			// Zugriff verweigert oder etwas anderes zurückgibt als erwartet.
			if s.log != nil {
				kurz := string(resp)
				if len(kurz) > 200 {
					kurz = kurz[:200]
				}
				s.log.Warn("WebDAV fern: kein Verzeichnis",
					zap.String("share", share), zap.String("pfad", virt),
					zap.Error(err),
					zap.String("fehler_der_gegenstelle", lr.Error),
					zap.String("antwort", kurz))
			}
			groesse, gerr := s.fernGroesse(ctx, peer, share, virt)
			if gerr != nil {
				if s.log != nil {
					s.log.Warn("WebDAV fern: auch keine Datei",
						zap.String("pfad", virt), zap.Error(gerr))
				}
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			ms.Responses = append(ms.Responses,
				davEintrag(basis+davPfad(virt), path.Base(virt), false, groesse, time.Now()))
		}

		// R746: Wie viele Einträge gehen tatsächlich raus? Der eingehängte
		// Ordner ist leer, das Einhängen klappt aber – die Frage ist also, ob
		// schon die Gegenstelle nichts liefert oder ob es beim Verpacken ins
		// XML verlorengeht.
		if s.log != nil {
			s.log.Info("WebDAV fern: PROPFIND beantwortet",
				zap.String("pfad", virt), zap.String("tiefe", tiefe),
				zap.Bool("als_ordner", istOrdner),
				zap.Int("eintraege_von_gegenstelle", len(lr.Entries)),
				zap.Int("antworten_im_xml", len(ms.Responses)))
		}
		raw, merr := xml.Marshal(&ms)
		if merr != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Header("Content-Type", `application/xml; charset="utf-8"`)
		c.Status(http.StatusMultiStatus)
		_, _ = c.Writer.Write([]byte(xml.Header))
		_, _ = c.Writer.Write(raw)

	case "GET", "HEAD":
		// Über den strömenden Weg aus R718 – der beherrscht Bereiche schon.
		c.Request.URL.RawQuery = "path=" + strings.ReplaceAll(virt, " ", "%20")
		s.remoteShareStreamFile(c)

	case "PUT":
		if virt == "" {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		defer c.Request.Body.Close()
		buf := make([]byte, fernDavBlock)
		var off int64
		gesamtStart := time.Now()
		for {
			n, rerr := io.ReadFull(c.Request.Body, buf)
			if n > 0 {
				resp, _, err := s.fernOp(ctx, peer, map[string]any{
					"type": "putrange", "share": share, "path": virt,
					"off": off, "data": base64.StdEncoding.EncodeToString(buf[:n]),
				})
				if err != nil || fernFehler(resp) {
					if s.log != nil {
						s.log.Warn("WebDAV-Schreiben über P2P fehlgeschlagen",
							zap.String("pfad", virt), zap.Int64("ab", off))
					}
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
				off += int64(n)
			}
			if rerr != nil {
				break // io.EOF oder ErrUnexpectedEOF am Dateiende
			}
		}
		if s.log != nil {
			s.log.Info("WebDAV über P2P: Datei gespeichert",
				zap.String("pfad", virt), zap.Int64("bytes", off),
				zap.Duration("gesamt", time.Since(gesamtStart)))
		}
		c.Status(http.StatusCreated)

	case "DELETE", "MKCOL":
		art := "delete"
		if c.Request.Method == "MKCOL" {
			art = "mkcol"
		}
		resp, _, err := s.fernOp(ctx, peer, map[string]any{
			"type": art, "share": share, "path": virt,
		})
		if err != nil || fernFehler(resp) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if art == "mkcol" {
			c.Status(http.StatusCreated)
		} else {
			c.Status(http.StatusNoContent)
		}

	case "MOVE":
		ziel := c.GetHeader("Destination")
		i := strings.Index(ziel, basis)
		if i < 0 {
			c.AbortWithStatus(http.StatusBadGateway)
			return
		}
		resp, _, err := s.fernOp(ctx, peer, map[string]any{
			"type": "move", "share": share, "path": virt,
			"dest": strings.TrimPrefix(ziel[i+len(basis):], "/"),
		})
		if err != nil || fernFehler(resp) {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Status(http.StatusCreated)

	case "LOCK":
		marke := "opaquelocktoken:" + generateID()
		c.Header("Lock-Token", "<"+marke+">")
		c.Header("Content-Type", `application/xml; charset="utf-8"`)
		c.Status(http.StatusOK)
		fmt.Fprintf(c.Writer, `%s<D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock>`+
			`<D:locktype><D:write/></D:locktype><D:lockscope><D:exclusive/></D:lockscope>`+
			`<D:depth>0</D:depth><D:timeout>Second-3600</D:timeout>`+
			`<D:locktoken><D:href>%s</D:href></D:locktoken>`+
			`</D:activelock></D:lockdiscovery></D:prop>`, xml.Header, marke)

	case "UNLOCK":
		c.Status(http.StatusNoContent)

	case "PROPPATCH":
		c.Header("Content-Type", `application/xml; charset="utf-8"`)
		c.Status(http.StatusMultiStatus)
		fmt.Fprintf(c.Writer, `%s<D:multistatus xmlns:D="DAV:"><D:response><D:href>%s</D:href>`+
			`<D:propstat><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`+
			`</D:response></D:multistatus>`, xml.Header, basis+davPfad(virt))

	default:
		c.AbortWithStatus(http.StatusMethodNotAllowed)
	}
}

// fernFehler: Trägt die Antwort eine Fehlermeldung?
func fernFehler(resp []byte) bool {
	return len(resp) > 8 && string(resp[:8]) == `{"error"`
}

// GET /api/v1/shares/remote/:peer/:share/davinfo – Zugang für fremde Freigaben.
func (s *Server) remoteDavInfo(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	peer, share := c.Param("peer"), c.Param("share")
	schema := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		schema = "https"
	}
	pfad := "/davr/" + peer + "/" + share + "/"
	c.JSON(http.StatusOK, gin.H{
		"url":         fmt.Sprintf("%s://%s%s", schema, c.Request.Host, pfad),
		"unc":         davUNCPfad(c.Request.Host, pfad, schema == "https"),
		"user":        "fundus",
		"password":    s.fernDavToken(peer, share),
		"unbefristet": true,
		"ueber_p2p":   true,
	})
}

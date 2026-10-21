package api

// WebDAV für beschreibbare Freigaben (R722).
//
// ── Wozu ───────────────────────────────────────────────────────────────────
//
// Word und Excel öffnen eine Datei seit R721 unmittelbar aus dem Netz, können
// sie aber nicht zurückspeichern: Dafür fragen sie vorher nach Eigenschaften
// (PROPFIND), setzen eine Sperre (LOCK) und schicken die Datei mit PUT zurück.
// Genau das ist WebDAV.
//
// Nebeneffekt, der fast wichtiger ist: Eine Freigabe lässt sich damit im
// Windows-Explorer als Netzlaufwerk einbinden und wie ein lokaler Ordner
// benutzen.
//
// ── Anmeldung ──────────────────────────────────────────────────────────────
//
// Weder Office noch der Explorer schicken unsere Sitzungskennung mit. Sie
// beherrschen aber HTTP-Basisanmeldung. Deshalb bekommt jede Freigabe eine
// eigene Zugangskennung, die der Nutzer sich in der Oberfläche abholt und in
// Windows einträgt. Sie gilt für GENAU diese Freigabe.
//
// Bewusst NICHT das Fundus-Passwort: Dessen Prüfung läuft über Argon2 und
// dauert ein bis zwei Sekunden. Der Explorer stellt für eine Ordneransicht
// dutzende Anfragen – das wäre unbenutzbar und würde den Node lahmlegen.
//
// ── Umfang ─────────────────────────────────────────────────────────────────
//
// Nur so viel, wie Office und der Explorer tatsächlich brauchen. Sperren werden
// vergeben und wieder freigegeben, aber nicht durchgesetzt: Bei einer
// Familienfreigabe ist das Risiko zweier gleichzeitiger Bearbeiter gering, und
// eine ernsthafte Sperrverwaltung wäre ein Vielfaches an Code. Office kommt
// damit zurecht; es will die Sperre bestätigt bekommen, mehr nicht.

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/filestore"
)

// R723: KEINE Frist mehr. Ein eingebundenes Netzlaufwerk soll nicht nach 30
// Tagen stillschweigend aufhören zu arbeiten – man sucht den Fehler dann
// überall, nur nicht bei einer abgelaufenen Kennung.
//
// Zurückziehen lässt sie sich trotzdem: Die Datei share-token.key im
// Datenverzeichnis löschen und den Node neu starten. Danach sind ALLE
// ausgestellten Kennungen und Abspiel-Verknüpfungen ungültig.
const davOhneFrist int64 = 0

// davToken: an die Freigabe gebunden, nicht an einen Pfad – der Explorer
// bewegt sich frei im Baum und könnte mit einer pfadgebundenen Kennung nichts
// anfangen.
func (s *Server) davToken(share string) string {
	return s.shareTokenBauen("dav:"+share, "", davOhneFrist)
}

func (s *Server) davTokenGueltig(share, tok string) bool {
	if tok == "" {
		return false
	}
	teil := strings.SplitN(tok, ".", 2)
	if len(teil) != 2 {
		return false
	}
	var ablauf int64
	if _, err := fmt.Sscanf(teil[0], "%d", &ablauf); err != nil {
		return false
	}
	// R723: Ablauf 0 heißt unbefristet. Jede andere Zahl wird wie bisher
	// geprüft, damit früher ausgestellte Kennungen weiter funktionieren.
	if ablauf != davOhneFrist && time.Now().Unix() > ablauf {
		return false
	}
	return s.shareTokenGueltig(tok, "dav:"+share, "")
}

// GET /api/v1/shares/:id/davinfo – Adresse und Zugangskennung zum Eintragen.
func (s *Server) shareDavInfo(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	share := c.Param("id")
	sh, ok := mgr.Get(share)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Freigabe nicht gefunden"})
		return
	}
	// Nur wer die Freigabe lesen darf, bekommt eine Kennung.
	if _, err := mgr.ResolveForRead(share, s.shareRequester(c), ""); err != nil {
		// Der Wurzelpfad ist kein Datei – Zugriff stattdessen über die Regel prüfen.
		if !sh.ReadAccess.Allows(s.shareRequester(c), mgr.GetContacts()) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugriff auf diese Freigabe"})
			return
		}
	}
	schema := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		schema = "https"
	}
	c.JSON(http.StatusOK, gin.H{
		"url":      fmt.Sprintf("%s://%s/dav/%s/", schema, c.Request.Host, share),
		"user":     "fundus",
		"password": s.davToken(share),
		"writable": sh.WriteAccess.Allows(s.shareRequester(c), mgr.GetContacts()),
		"unbefristet": true,
		"unc":         davUNC(c.Request.Host, share, schema == "https"),
	})
}

// davUNC baut den Netzwerkpfad, den Windows versteht (R723).
//
// Der Windows-Webdienst erwartet eine besondere Schreibweise: Bei HTTPS wird
// "@SSL" an den Rechnernamen gehängt, bei einem abweichenden Port dessen
// Nummer. "DavWWWRoot" ist ein von Windows erfundener Zwischenschritt, der im
// Pfad stehen MUSS, obwohl es ihn auf dem Server nicht gibt.
func davUNC(host, share string, https bool) string {
	// R727: Nur noch ein Aufruf. Beim Verallgemeinern in R726 blieb der alte
	// Rumpf stehen – seine Variablen benutzte danach niemand mehr, und Go
	// lehnt unbenutzte lokale Variablen ab.
	return davUNCPfad(host, "/dav/"+share+"/", https)
}

// davUNCPfad baut den Netzwerkpfad für einen BELIEBIGEN Pfad (R726).
//
// Verallgemeinert aus davUNC, weil es jetzt zwei Fälle gibt: eigene Freigaben
// unter /dav/<id>/ und fremde unter /dav/r/<peer>/<share>/. Eine zweite Kopie
// derselben Windows-Eigenheiten wäre genau die Doppelung, die mir hier schon
// mehrfach auseinandergelaufen ist.
func davUNCPfad(host, pfad string, https bool) string {
	rechner := host
	port := ""
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		rechner, port = host[:i], host[i+1:]
	}
	zusatz := ""
	if https {
		zusatz = "@SSL"
		if port != "" && port != "443" {
			zusatz = "@SSL@" + port
		}
	} else if port != "" && port != "80" {
		zusatz = "@" + port
	}
	// Rückstriche stehen im Rohtext WÖRTLICH: zwei am Anfang, danach je einer.
	rest := strings.ReplaceAll(strings.Trim(pfad, "/"), "/", `\`)
	return `\\` + rechner + zusatz + `\DavWWWRoot\` + rest
}

// GET /api/v1/shares/:id/davcmd – Verbindungsdatei zum Anklicken.
//
// Ein echter Link ginge nicht: Browser verweigern Netzwerkpfade und
// file://-Verweise aus einer Webseite heraus, aus gutem Grund. Eine kleine
// Befehlsdatei ist der nächstbeste Weg – ein Klick zum Laden, einer zum
// Ausführen, und das Laufwerk ist eingebunden.
//
// Sie enthält die Zugangskennung im Klartext. Das steht auch drin, damit
// niemand sie arglos weitergibt.
func (s *Server) shareDavCmd(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	share := c.Param("id")
	sh, ok := mgr.Get(share)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Freigabe nicht gefunden"})
		return
	}
	if !sh.ReadAccess.Allows(s.shareRequester(c), mgr.GetContacts()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugriff auf diese Freigabe"})
		return
	}
	https := c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	unc := davUNC(c.Request.Host, share, https)
	tok := s.davToken(share)

	// Zeilenenden als CRLF – mit reinen Zeilenumbrüchen führt die
	// Windows-Eingabeaufforderung die Datei nicht zuverlässig aus.
	zeilen := []string{
		"@echo off",
		"REM Fundus-Freigabe als Netzlaufwerk einbinden",
		"REM ACHTUNG: Diese Datei enthaelt das Kennwort im Klartext.",
		"REM Nach dem Einbinden kann sie geloescht werden.",
		"echo Binde " + sh.Name + " ein ...",
		"net use * \"" + unc + "\" /user:fundus " + tok + " /persistent:yes",
		"if errorlevel 1 (",
		"  echo.",
		"  echo Fehlgeschlagen. Die zwei haeufigsten Ursachen:",
		"  echo.",
		"  echo 1) Der Dienst WebClient laeuft nicht:",
		"  echo      net start WebClient",
		"  echo.",
		"  echo 2) Windows verweigert die Anmeldung ueber unverschluesseltes HTTP.",
		"  echo    Das ist eine Voreinstellung, kein Fehler. Abhilfe - entweder",
		"  echo    ueber HTTPS zugreifen, oder als Administrator einmalig:",
		"  echo      reg add HKLM\\SYSTEM\\CurrentControlSet\\Services\\WebClient\\Parameters /v BasicAuthLevel /t REG_DWORD /d 2 /f",
		"  echo      net stop WebClient ^& net start WebClient",
		"  pause",
		") else (",
		"  echo Fertig. Das Laufwerk erscheint im Explorer.",
		"  timeout /t 3 >nul",
		")",
	}
	inhalt := strings.Join(zeilen, "\r\n") + "\r\n"

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="fundus-%s.cmd"`, dateiName(sh.Name)))
	c.String(http.StatusOK, "%s", inhalt)
}

// dateiName macht aus einem Anzeigenamen etwas, das als Dateiname taugt.
func dateiName(n string) string {
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "freigabe"
	}
	return out
}

// davAuth prüft die Basisanmeldung. Gibt die Freigabe-ID zurück.
func (s *Server) davAuth(c *gin.Context, share string) bool {
	// Angemeldete Sitzung geht auch – praktisch beim Ausprobieren im Browser.
	if s.sessionFID(c) != "" {
		return true
	}
	erwartet := s.davToken(share)
	if _, pass, ok := c.Request.BasicAuth(); ok && s.davTokenGueltig(share, pass) {
		return true
	}
	// R732: Digest zusätzlich. Windows gibt über unverschlüsseltes HTTP KEINE
	// Basisanmeldung heraus – nachgewiesen im Protokoll: OPTIONS ohne
	// Anmeldung, 401, dann Stille. Mit Digest klappt es auch ohne HTTPS.
	if digestPruefen(c, erwartet) {
		return true
	}
	// Ohne gültige Anmeldung MUSS die Aufforderung kommen, sonst fragt weder
	// Office noch der Explorer nach Zugangsdaten – sie brechen stumm ab.
	davAnmeldungVerlangen(c)
	return false
}

// ── XML-Gerüst für PROPFIND ────────────────────────────────────────────────

type davProp struct {
	XMLName      xml.Name `xml:"D:prop"`
	ResourceType struct {
		Collection *struct{} `xml:"D:collection,omitempty"`
	} `xml:"D:resourcetype"`
	ContentLength int64  `xml:"D:getcontentlength,omitempty"`
	LastModified  string `xml:"D:getlastmodified,omitempty"`
	CreationDate  string `xml:"D:creationdate,omitempty"`
	DisplayName   string `xml:"D:displayname,omitempty"`
	ContentType   string `xml:"D:getcontenttype,omitempty"`
}

type davPropstat struct {
	XMLName xml.Name `xml:"D:propstat"`
	Prop    davProp  `xml:"D:prop"`
	Status  string   `xml:"D:status"`
}

type davResponse struct {
	XMLName  xml.Name    `xml:"D:response"`
	Href     string      `xml:"D:href"`
	Propstat davPropstat `xml:"D:propstat"`
}

type davMultistatus struct {
	XMLName   xml.Name      `xml:"D:multistatus"`
	XMLNS     string        `xml:"xmlns:D,attr"`
	Responses []davResponse `xml:"D:response"`
}

func davEintrag(href, name string, istOrdner bool, groesse int64, zeit time.Time) davResponse {
	var r davResponse
	r.Href = href
	r.Propstat.Status = "HTTP/1.1 200 OK"
	r.Propstat.Prop.DisplayName = name
	r.Propstat.Prop.LastModified = zeit.UTC().Format(http.TimeFormat)
	r.Propstat.Prop.CreationDate = zeit.UTC().Format(time.RFC3339)
	if istOrdner {
		r.Propstat.Prop.ResourceType.Collection = &struct{}{}
	} else {
		r.Propstat.Prop.ContentLength = groesse
		if ct := freigabeMime(name); ct != "" {
			r.Propstat.Prop.ContentType = ct
		}
	}
	return r
}

// davHandler bedient alle WebDAV-Befehle unter /dav/:id/*path.
func (s *Server) davHandler(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	share := c.Param("id")
	// R729: JEDE Anfrage protokollieren, noch vor der Anmeldung. Bei „ließ sich
	// nicht verbinden" ist die erste Frage, ob überhaupt etwas ankommt – in
	// R728 fehlte die Weiterleitung im Webserver, und genau das war unsichtbar.
	if s.log != nil {
		_, _, hatAuth := c.Request.BasicAuth()
		s.log.Info("WebDAV-Anfrage", zap.String("methode", c.Request.Method),
			zap.String("pfad", c.Request.URL.Path),
			zap.Bool("mit_anmeldung", hatAuth),
			zap.String("von", c.ClientIP()))
	}
	if !s.davAuth(c, share) {
		return
	}
	sh, ok := mgr.Get(share)
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	// Als Anfragender gilt hier der Besitzer: Die Zugangskennung wurde nur
	// jemandem ausgestellt, dessen Zugriff geprüft war (siehe shareDavInfo).
	const anfrager = "local"
	darfSchreiben := sh.WriteAccess.Allows(anfrager, mgr.GetContacts())

	virt := strings.TrimPrefix(c.Param("path"), "/")
	basis := "/dav/" + share + "/"

	switch c.Request.Method {

	case "OPTIONS":
		// DAV: 1,2 – die 2 steht für Sperren. Ohne sie verweigert Office das
		// Bearbeiten und öffnet nur lesend.
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

		if virt == "" {
			// Wurzel der Freigabe
			ms.Responses = append(ms.Responses, davEintrag(basis, sh.Name, true, 0, sh.UpdatedAt))
			if tiefe != "0" {
				eintraege, err := mgr.ListDir(share, anfrager, "")
				if err != nil {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
				for _, e := range eintraege {
					ms.Responses = append(ms.Responses,
						davEintrag(basis+davPfad(e.Name), e.Name, e.IsDir, e.Size, e.ModTime))
				}
			}
		} else if eintraege, err := mgr.ListDir(share, anfrager, virt); err == nil {
			// Unterordner
			ms.Responses = append(ms.Responses,
				davEintrag(basis+davPfad(virt)+"/", path.Base(virt), true, 0, time.Now()))
			if tiefe != "0" {
				for _, e := range eintraege {
					ms.Responses = append(ms.Responses,
						davEintrag(basis+davPfad(virt)+"/"+davPfad(e.Name), e.Name, e.IsDir, e.Size, e.ModTime))
				}
			}
		} else {
			// Datei
			fp, ferr := mgr.ResolveForRead(share, anfrager, virt)
			if ferr != nil {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			st, _ := os.Stat(fp)
			ms.Responses = append(ms.Responses,
				davEintrag(basis+davPfad(virt), path.Base(virt), false, st.Size(), st.ModTime()))
		}

		raw, err := xml.Marshal(&ms)
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Header("Content-Type", `application/xml; charset="utf-8"`)
		c.Status(http.StatusMultiStatus)
		_, _ = c.Writer.Write([]byte(xml.Header))
		_, _ = c.Writer.Write(raw)

	case "GET", "HEAD":
		fp, err := mgr.ResolveForRead(share, anfrager, virt)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if ct := freigabeMime(fp); ct != "" {
			c.Header("Content-Type", ct)
		}
		// ServeFile beherrscht Bereiche – wichtig, weil Office große Dateien
		// abschnittsweise liest.
		http.ServeFile(c.Writer, c.Request, fp)

	case "PUT":
		if !darfSchreiben {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if virt == "" {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		defer c.Request.Body.Close()
		if err := s.davSchreiben(mgr, share, anfrager, virt, c.Request.Body); err != nil {
			if s.log != nil {
				s.log.Warn("WebDAV-Schreiben fehlgeschlagen",
					zap.String("pfad", virt), zap.Error(err))
			}
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if s.log != nil {
			s.log.Info("WebDAV-Datei gespeichert", zap.String("share", share),
				zap.String("pfad", virt))
		}
		c.Status(http.StatusCreated)

	case "DELETE":
		if !darfSchreiben {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		fp, err := mgr.ResolveForWrite(share, anfrager, virt)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if err := os.Remove(fp); err != nil {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Status(http.StatusNoContent)

	case "MKCOL":
		if !darfSchreiben {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		fp, err := mgr.ResolveForWrite(share, anfrager, virt)
		if err != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		if err := os.Mkdir(fp, 0o775); err != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Status(http.StatusCreated)

	case "MOVE":
		if !darfSchreiben {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		ziel := c.GetHeader("Destination")
		i := strings.Index(ziel, basis)
		if i < 0 {
			c.AbortWithStatus(http.StatusBadGateway) // Ziel außerhalb der Freigabe
			return
		}
		zielVirt := strings.TrimPrefix(ziel[i+len(basis):], "/")
		von, err1 := mgr.ResolveForWrite(share, anfrager, virt)
		nach, err2 := mgr.ResolveForWrite(share, anfrager, zielVirt)
		if err1 != nil || err2 != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		if err := os.Rename(von, nach); err != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		c.Status(http.StatusCreated)

	case "LOCK":
		// Sperre bestätigen, ohne sie durchzusetzen – siehe Kopf der Datei.
		if !darfSchreiben {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
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
		// Office setzt gern Eigenschaften; wir speichern sie nicht, bestätigen
		// aber. Ein Fehler hier bricht das Speichern komplett ab.
		c.Header("Content-Type", `application/xml; charset="utf-8"`)
		c.Status(http.StatusMultiStatus)
		fmt.Fprintf(c.Writer, `%s<D:multistatus xmlns:D="DAV:"><D:response><D:href>%s</D:href>`+
			`<D:propstat><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`+
			`</D:response></D:multistatus>`, xml.Header, basis+davPfad(virt))

	default:
		c.AbortWithStatus(http.StatusMethodNotAllowed)
	}
}

// davSchreiben legt den Inhalt an der Zielstelle ab.
func (s *Server) davSchreiben(mgr *filestore.ShareManager, share, anfrager, virt string, r io.Reader) error {
	fp, err := mgr.ResolveForWrite(share, anfrager, virt)
	if err != nil {
		return err
	}
	// Erst daneben schreiben, dann umbenennen: Bricht Office mitten im
	// Speichern ab, bleibt sonst eine halbe Datei zurück – und die hätte das
	// Original ersetzt.
	tmp := fp + ".fnd-tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// davPfad kodiert jeden Bestandteil einzeln – Schrägstriche bleiben erhalten,
// weil sie die Pfadtrennung sind und nicht Teil eines Namens.
func davPfad(p string) string {
	teile := strings.Split(p, "/")
	for i, t := range teile {
		teile[i] = url.PathEscape(t)
	}
	return strings.Join(teile, "/")
}

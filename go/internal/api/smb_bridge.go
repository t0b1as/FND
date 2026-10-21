package api

// Brücke von einer FERNEN Freigabe zu einem Windows-Laufwerk (R735).
//
// ── Der Gedanke ────────────────────────────────────────────────────────────
//
// Windows und WebDAV vertragen sich schlecht: Über unverschlüsseltes HTTP gibt
// Windows keine Basisanmeldung heraus (im Protokoll nachgewiesen), der Umweg
// über Netzwerkpfade landet zuerst bei SMB, und der Webdienst muss laufen. SMB
// dagegen spricht Windows seit jeher reibungslos.
//
// Also andersherum: Der Pi im eigenen Netz bietet eine ganz normale
// Windows-Freigabe an. Deren Inhalt stammt aber von einem anderen Node –
// eingehängt über den eigenen WebDAV-Zugang des Pi mit davfs2. Windows merkt
// davon nichts.
//
//   Windows ──SMB──> Pi (Samba) ──davfs2/HTTP──> eigener Node ──P2P──> ferner Node
//
// Der Umweg über den eigenen HTTP-Zugang sieht gewunden aus, hat aber einen
// handfesten Vorteil: Er nutzt genau den Weg, der schon fertig und erprobt ist
// (R718 und R726), statt ein Dateisystem von Grund auf zu bauen.
//
// ── Wer macht was ──────────────────────────────────────────────────────────
//
// Einhängen und Samba einrichten verlangt root. Der Node läuft bewusst unter
// einem eigenen Benutzer, damit ein Fehler in ihm nicht das ganze System
// betrifft. Also übernimmt es der fundus-helper – derselbe privilegierte
// Dienst, der schon Laufwerke einhängt (R736).
//
// Der Node reicht nur Name, Adresse und Zugangskennung weiter. Einhängepunkt
// und Dateinamen bestimmt der Helfer selbst, und die Adresse muss auf
// 127.0.0.1 zeigen – sonst wäre er ein Werkzeug, um als root beliebige
// Verzeichnisse einzuhängen.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/helperproto"
)

// POST /api/v1/shares/remote/:peer/:share/smb    – einrichten
// DELETE /api/v1/shares/remote/:peer/:share/smb  – entfernen
// GET /api/v1/shares/smb                         – auflisten
func (s *Server) remoteSmbBridge(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	peer, share := c.Param("peer"), c.Param("share")

	var req struct {
		Name    string `json:"name"`
		SmbPass string `json:"smb_pass"`
	}
	_ = c.ShouldBindJSON(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = share
	}

	if c.Request.Method == http.MethodDelete {
		resp, err := helperproto.Do(helperproto.Request{
			Version: helperproto.ProtocolVersion,
			Action:  helperproto.ActionSmbBridgeRemove,
			Name:    name,
		})
		if err != nil || resp == nil || !resp.OK {
			c.JSON(http.StatusBadGateway, gin.H{"error": helperFehler(err, resp)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"removed": true})
		return
	}

	// Erreichbarkeit zuerst prüfen. Eine Brücke einzurichten, die dann ins
	// Leere führt, hinterlässt eine Einhängung, die jeden Zugriff hängen lässt.
	if _, err := s.fernFrage(c.Request.Context(), peer, map[string]any{
		"type": "ls", "share": share, "path": "",
	}); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "Ferne Freigabe nicht erreichbar: " + err.Error()})
		return
	}

	davURL := fmt.Sprintf("http://127.0.0.1:%d/dav/r/%s/%s/", s.port(), peer, share)
	resp, err := helperproto.Do(helperproto.Request{
		Version:  helperproto.ProtocolVersion,
		Action:   helperproto.ActionSmbBridgeAdd,
		Name:     name,
		DavURL:   davURL,
		DavToken: s.fernDavToken(peer, share),
		SmbPass:  req.SmbPass,
	})
	if err != nil || resp == nil || !resp.OK {
		c.JSON(http.StatusBadGateway, gin.H{"error": helperFehler(err, resp)})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"name":       dateiName(name),
		"mountpoint": resp.Mountpoint,
		"smb_user":   "fundussmb",
	})
}

// GET /api/v1/shares/smb – was ist eingerichtet?
func (s *Server) smbBridgeList(c *gin.Context) {
	resp, err := helperproto.Do(helperproto.Request{
		Version: helperproto.ProtocolVersion,
		Action:  helperproto.ActionSmbBridgeList,
	})
	if err != nil || resp == nil || !resp.OK {
		c.JSON(http.StatusOK, gin.H{"shares": []any{}, "hinweis": helperFehler(err, resp)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"shares": resp.Shares})
}

// helperFehler fasst Verbindungs- und Fachfehler zu einer Meldung zusammen.
//
// R738: Zeiger, denn helperproto.Do gibt *Response zurück. Die Nullprüfung ist
// dabei das Wesentliche: Bei einem Verbindungsfehler ist resp nil, und ein
// Zugriff auf resp.Error würde den Node abstürzen lassen – ausgerechnet dann,
// wenn der Helfer ohnehin schon nicht erreichbar ist.
func helperFehler(err error, resp *helperproto.Response) string {
	if err != nil {
		return "Helfer nicht erreichbar: " + err.Error()
	}
	if resp == nil {
		return "keine Antwort vom Helfer"
	}
	if resp.Error != "" {
		return resp.Error
	}
	return "unbekannter Fehler"
}

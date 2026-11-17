package api

// Besitzer pro Nutzer (R530): Angebote und Orders gehörten bisher dem NODE –
// auf einem Node mit mehreren Nutzern konnte jeder Angemeldete die Einträge
// anderer ändern, löschen oder stornieren. Jetzt wird beim Anlegen die
// Fundus-ID des angemeldeten Erstellers gespeichert (bei Angeboten zusätzlich
// seine Signatur über den Inhalts-Hash – prüfbar auch auf anderen Nodes und
// Grundlage für Bewertungen). Ändern/Löschen/Stornieren darf dann nur er.
//
// Einträge ohne Ersteller (Altbestand, oder ohne Anmeldung aus dem Heimnetz
// angelegt) gehören weiter dem Node: nur aus dem Heimnetz änderbar.

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Felder, die nur der Server setzt (nie aus einer Anfrage übernehmen).
var creatorFields = []string{"creator_fid", "creator_sig"}

func stripCreatorFields(m map[string]any) {
	for _, k := range creatorFields {
		delete(m, k)
	}
}

// sessionFID: Fundus-ID der angemeldeten Sitzung (klein), sonst "".
func (s *Server) sessionFID(c *gin.Context) string {
	if sess := s.getSession(c); sess != nil && sess.identity != nil {
		return strings.ToLower(sess.identity.FundusID)
	}
	return ""
}

// mayModify: darf der Aufrufer einen Eintrag mit diesem Ersteller ändern?
func (s *Server) mayModify(c *gin.Context, creator string) bool {
	// R927: Hotspot-Gäste (10.42.0.x) haben zwar eine private Adresse, sind aber
	// nicht der Betreiber – für sie gilt wie für alle anderen: nur der Ersteller.
	if s.isAdminRequest(c) || (isLANRequest(c) && !istHotspotGast(clientIP(c))) {
		return true // Betreiber: im Heimnetz (wie vor R530) oder mit Admin-Passwort
	}
	creator = strings.ToLower(strings.TrimSpace(creator))
	if creator == "" {
		return isLANRequest(c) // Node-eigener Eintrag: wie bisher nur Heimnetz
	}
	return s.sessionFID(c) == creator
}

func denyNotCreator(c *gin.Context, what string) {
	c.JSON(http.StatusForbidden, gin.H{"error": "Nur der Ersteller darf " + what + " (bitte mit dem Konto anmelden, mit dem es angelegt wurde)."})
}

// signCreator: Ersteller + Signatur über den Inhalts-Hash in die Daten schreiben.
func (s *Server) signCreator(c *gin.Context, data map[string]any, kind string) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	data["creator_fid"] = fid
	ch, _ := data["content_hash"].(string)
	if sig, err := sess.identity.Sign([]byte("fundus-" + kind + ":" + fid + ":" + ch)); err == nil {
		data["creator_sig"] = sig
	}
}

// istHotspotGast: Gerät im WLAN-Hotspot dieses Nodes (10.42.0.0/24, ohne den
// Pi selbst).
func istHotspotGast(ip string) bool {
	return strings.HasPrefix(ip, "10.42.0.") && ip != hotspotGW
}

// besucherIP (R927): Adresse des Browsers. Ruft die Lua-Oberfläche den Node
// beim Seitenaufbau direkt auf (127.0.0.1, ohne nginx), reicht sie die Adresse
// des Besuchers in X-Fundus-Besucher mit – geglaubt wird das nur von dort.
func besucherIP(c *gin.Context) string {
	if isLocalRequest(c) && c.GetHeader("X-Real-IP") == "" {
		if v := strings.TrimSpace(c.GetHeader("X-Fundus-Besucher")); v != "" {
			return v
		}
	}
	return clientIP(c)
}

// besucherIstBetreiber: Browser im Heimnetz (nicht im Hotspot) – dort darf der
// Betreiber auch fremde Angebote seines Nodes bearbeiten (wie mayModify).
func besucherIstBetreiber(c *gin.Context) bool {
	ip := net.ParseIP(besucherIP(c))
	if ip == nil {
		return false
	}
	return (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) && !istHotspotGast(ip.String())
}

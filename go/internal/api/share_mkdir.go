package api

// Ordner für eine Freigabe anlegen (R706).
//
// Bisher musste man den Ordner vorher über eine Konsole erzeugen. Für den
// häufigsten Fall – einen eigens angelegten Ablageordner freigeben – war das
// ein Bruch im Ablauf.
//
// ── Warum das eng geführt sein MUSS ────────────────────────────────────────
//
// Ein Endpunkt, der Verzeichnisse anlegt, ist ein Schreibzugriff auf das
// Dateisystem des Node. Ohne Grenzen ließe sich damit überall hineinschreiben,
// etwa nach /etc oder in das Datenverzeichnis des Node selbst. Deshalb:
//
//   * nur aus dem Heimnetz, nicht von außen
//   * nur unterhalb weniger erlaubter Wurzeln (Wechselmedien, Heimverzeichnis)
//   * das Datenverzeichnis des Node ist ausdrücklich ausgenommen
//   * der Name darf kein Trennzeichen enthalten – sonst wäre "../../etc" ein
//     gültiger "Name" und die Wurzelprüfung liefe ins Leere
//   * der übergeordnete Ordner muss bereits existieren; angelegt wird genau
//     EINE Ebene, keine ganzen Pfade

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// mkdirErlaubteWurzeln: unterhalb dieser Pfade darf angelegt werden.
var mkdirErlaubteWurzeln = []string{
	"/media", "/mnt", "/home", "/srv",
}

// POST /api/v1/shares/mkdir   { base, name }
func (s *Server) shareMkdir(c *gin.Context) {
	if !isLANRequest(c) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Ordner lassen sich nur aus dem Heimnetz anlegen."})
		return
	}
	var req struct {
		Base string `json:"base"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	base := strings.TrimSpace(req.Base)

	// Name: genau ein Bestandteil, keine Trennzeichen, keine Punkte-Tricks.
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Ungültiger Ordnername (keine Pfade, nur ein Name)."})
		return
	}
	if len(name) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ordnername zu lang."})
		return
	}
	if base == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Bitte zuerst über „Ordner öffnen“ auswählen, wo der neue Ordner liegen soll."})
		return
	}

	// Pfad bereinigen UND danach prüfen – nicht davor. Ein "/media/x/../../etc"
	// wird durch Clean zu "/etc", und genau das soll die Wurzelprüfung sehen.
	base = filepath.Clean(base)
	if !filepath.IsAbs(base) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pfad muss absolut sein."})
		return
	}
	if !mkdirWurzelErlaubt(base) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Dort darf nicht angelegt werden. Erlaubt sind Wechselmedien " +
				"und Heimverzeichnisse (/media, /mnt, /home, /srv)."})
		return
	}
	// Das Datenverzeichnis des Node bleibt tabu: Dort liegen Chain, Schlüssel
	// und Verlauf. Ein Ordner darin wäre bestenfalls nutzlos.
	if s.cfg != nil && s.cfg.DataDir != "" {
		dd := filepath.Clean(s.cfg.DataDir)
		if base == dd || strings.HasPrefix(base, dd+string(filepath.Separator)) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Im Datenverzeichnis des Node darf kein Ordner angelegt werden."})
			return
		}
	}
	// Übergeordneter Ordner muss existieren – angelegt wird genau eine Ebene.
	st, err := os.Stat(base)
	if err != nil || !st.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Der übergeordnete Ordner existiert nicht: " + base})
		return
	}

	ziel := filepath.Join(base, name)
	if _, err := os.Stat(ziel); err == nil {
		// Schon da ist kein Fehler – der Nutzer will ihn ja benutzen.
		c.JSON(http.StatusOK, gin.H{"ok": true, "path": ziel, "existierte": true})
		return
	}
	if err := os.Mkdir(ziel, 0o775); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Ordner konnte nicht angelegt werden: " + err.Error()})
		return
	}
	if s.log != nil {
		s.log.Info("Ordner für Freigabe angelegt", zap.String("pfad", ziel))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "path": ziel})
}

// mkdirWurzelErlaubt prüft, ob der Pfad unter einer erlaubten Wurzel liegt.
func mkdirWurzelErlaubt(p string) bool {
	for _, w := range mkdirErlaubteWurzeln {
		if p == w {
			return false // direkt in der Wurzel selbst nicht – dort gehört nichts hin
		}
		if strings.HasPrefix(p, w+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ── Wer fragt hier eigentlich? (R707) ──────────────────────────────────────
//
// Die Freigabeprüfung (AccessRule.Allows) vergleicht eine Node-Kennung. Diese
// Kennung kam bisher aus dem Kopfzeilenfeld X-Fundus-Peer-ID – einem Feld, das
// der Aufrufer selbst setzt und das NIRGENDS gesetzt oder geprüft wird.
//
// Damit war jede Beschränkung auf bestimmte Nodes oder auf Kontakte umgehbar:
// Es genügte, die erlaubte Kennung in die Anfrage zu schreiben. Nur "public"
// und "none" waren wirksam, weil sie gar nicht vergleichen.
//
// Der Besitzer schreibt ohnehin direkt auf seiner Platte; für ihn braucht es
// keine Kennung. Für fremde Nodes gilt bis auf Weiteres: Ohne nachgewiesene
// Kennung kein Schreibzugriff. Das ist die sichere Richtung – lieber eine
// Freigabe, die zu wenig erlaubt, als eine, die nur so aussieht.
//
// Sobald der P2P-Weg die geprüfte Gegenstelle durchreicht, kann hier deren
// Kennung eingesetzt werden; die Stelle ist bewusst EINE Funktion, damit das
// an einem Ort geschieht und nicht an fünf.
func (s *Server) shareRequester(c *gin.Context) string {
	// Ein mitgeschicktes Feld wird NICHT übernommen. Wenn eines da ist, ist das
	// einen Protokolleintrag wert: Entweder versucht jemand etwas, oder ein
	// eigener Dienst schickt es und wir müssen ihn umstellen.
	if h := c.GetHeader("X-Fundus-Peer-ID"); h != "" && s.log != nil {
		s.log.Warn("X-Fundus-Peer-ID wird ignoriert (nicht nachprüfbar)",
			zap.String("behauptet", h), zap.String("von", c.ClientIP()),
			zap.String("pfad", c.Request.URL.Path))
	}
	// R709: Eine ANGEMELDETE Anfrage genügt – ohne zusätzliche Heimnetz-Bedingung.
	//
	// In R707 stand hier "Heimnetz UND angemeldet". Das war falsch: Wer von
	// unterwegs über die öffentliche Adresse auf den EIGENEN Node geht, ist
	// nicht im Heimnetz und bekam deshalb eine Abweisung beim Öffnen eines
	// Verzeichnisses. Der Node gehört ihm trotzdem.
	//
	// Die Anmeldung ist hier das richtige Merkmal: Sie weist nach, dass es ein
	// Nutzer DIESES Node ist. Das spoofbare Kopfzeilenfeld bleibt weiterhin
	// außen vor – darum ging es in R707, und das bleibt so.
	if s.sessionFID(c) != "" {
		// Bewusst "local" und nicht die eigene Node-Kennung: Die vorhandenen
		// Lesewege prüfen auf genau diesen Wert, um lokale Pfade einzublenden.
		// Mit der Node-Kennung hätte der Besitzer seine eigene Freigabe wie ein
		// Fremder gesehen.
		return "local"
	}
	return "" // unbekannt: nur bei "public" erlaubt
}

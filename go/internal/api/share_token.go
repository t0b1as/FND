package api

// Abspiel-Verknüpfung für VLC und andere Abspieler (R720).
//
// ── Warum das nötig ist ────────────────────────────────────────────────────
//
// Im Browser scheitert 4K-HEVC daran, dass er den Codec nicht dekodieren kann
// (R719). Umwandeln auf dem Pi scheidet aus. Der naheliegende Ausweg: Die Daten
// kommen strömend über HTTP vom Node, dekodiert wird beim Nutzer – also in VLC.
// Der Node muss dafür gar nichts rechnen, er liefert nur Bytes.
//
// ── Warum es nicht einfach die normale Adresse sein kann ───────────────────
//
// Seit R659 verlangt jeder /api/-Pfad eine Anmeldung. VLC schickt keine
// Sitzungskennung mit; eine kopierte Adresse liefe also ins Leere.
//
// Deshalb eine befristete, signierte Adresse: Der angemeldete Nutzer lässt sich
// eine ausstellen, VLC ruft damit auf. Die Signatur bindet sie an GENAU diese
// Freigabe und diesen Pfad – sie taugt also nicht als Generalschlüssel.
//
// Gültigkeit: 12 Stunden. Lang genug für einen Film samt Pausen, kurz genug,
// dass eine versehentlich weitergegebene Adresse nicht dauerhaft offensteht.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const shareTokenTTL = 12 * time.Hour

var (
	shareSecretOnce sync.Once
	shareSecret     []byte
)

// shareTokenSecret: einmal erzeugt, dann auf der Platte. Nach einem Neustart
// bleiben ausgestellte Adressen damit gültig – sonst bräche ein laufender Film
// mitten im Abspielen ab.
func (s *Server) shareTokenSecret() []byte {
	shareSecretOnce.Do(func() {
		dir := "/opt/fundus/data"
		if s.cfg != nil && s.cfg.DataDir != "" {
			dir = s.cfg.DataDir
		}
		pfad := filepath.Join(dir, "share-token.key")
		if b, err := os.ReadFile(pfad); err == nil && len(b) >= 32 {
			shareSecret = b
			return
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			// Ohne Zufall lieber gar keine Verknüpfungen als vorhersehbare.
			shareSecret = nil
			return
		}
		_ = os.WriteFile(pfad, b, 0o600)
		shareSecret = b
	})
	return shareSecret
}

// shareTokenBauen erzeugt die Signatur für Freigabe + Pfad + Ablauf.
func (s *Server) shareTokenBauen(share, pfad string, ablauf int64) string {
	sec := s.shareTokenSecret()
	if sec == nil {
		return ""
	}
	m := hmac.New(sha256.New, sec)
	fmt.Fprintf(m, "%s\x00%s\x00%d", share, pfad, ablauf)
	sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
	return strconv.FormatInt(ablauf, 10) + "." + sig
}

// shareTokenGueltig prüft eine Adresse. Gibt true zurück, wenn die Signatur
// stimmt UND die Frist nicht abgelaufen ist.
func (s *Server) shareTokenGueltig(tok, share, pfad string) bool {
	if tok == "" {
		return false
	}
	teil := strings.SplitN(tok, ".", 2)
	if len(teil) != 2 {
		return false
	}
	ablauf, err := strconv.ParseInt(teil[0], 10, 64)
	if err != nil {
		return false
	}
	// R723: Ablauf 0 heißt unbefristet (Netzlaufwerk-Kennungen). Abspiel-
	// Verknüpfungen tragen weiterhin eine echte Frist und laufen ab.
	if ablauf != 0 && time.Now().Unix() > ablauf {
		return false
	}
	// Vergleich in gleichbleibender Zeit: Ein Vergleich, der bei der ersten
	// abweichenden Stelle abbricht, verrät über die Dauer, wie viel schon
	// stimmte – damit ließe sich eine Signatur Zeichen für Zeichen raten.
	erwartet := s.shareTokenBauen(share, pfad, ablauf)
	if erwartet == "" {
		return false
	}
	return hmac.Equal([]byte(tok), []byte(erwartet))
}

// GET /api/v1/shares/:id/playlist/*path
//
// Liefert eine M3U-Abspielliste mit einer befristeten Adresse. Der Browser
// speichert sie, das Betriebssystem öffnet sie mit dem eingestellten Abspieler
// – bei .m3u ist das meist VLC.
//
// Bewusst eine Abspielliste und nicht "vlc://": Dieses Schema ist auf den
// wenigsten Systemen überhaupt angemeldet, ein Klick darauf täte also nichts.
// Eine .m3u-Datei funktioniert überall dort, wo ein Abspieler installiert ist.
func (s *Server) sharePlaylist(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	share := c.Param("id")
	pfad := strings.TrimPrefix(c.Param("path"), "/")

	// Zugriff JETZT prüfen, mit der Sitzung des Nutzers. Die Verknüpfung wird
	// nur ausgestellt, wenn er die Datei ohnehin lesen darf.
	if _, err := mgr.ResolveForRead(share, s.shareRequester(c), pfad); err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "verweigert") {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	tok := s.shareTokenBauen(share, pfad, time.Now().Add(shareTokenTTL).Unix())
	if tok == "" {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Verknüpfung konnte nicht signiert werden"})
		return
	}

	// Absolute Adresse: VLC läuft nicht im Browser und kennt keine relativen
	// Pfade. Das Schema aus der Anfrage übernehmen, damit es hinter HTTPS auch
	// https bleibt – sonst verweigert mancher Abspieler den Mischbetrieb.
	schema := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		schema = "https"
	}
	adresse := fmt.Sprintf("%s://%s/api/v1/shares/%s/dl/%s?tok=%s",
		schema, c.Request.Host, url.PathEscape(share), pfadEscape(pfad), url.QueryEscape(tok))

	name := filepath.Base(pfad)

	// R721: Dieselbe Adresse dient zwei Zwecken – Abspieler und Office. Fragt
	// die Oberfläche mit ?url=1, bekommt sie nur die nackte Adresse als Text;
	// sie reicht sie dann an die Anwendung weiter. Eine Abspielliste wäre dort
	// sinnlos, und ein zweiter Endpunkt für dieselbe Signatur wäre Doppelung.
	if c.Query("url") != "" {
		c.String(http.StatusOK, "%s", adresse)
		return
	}

	// Erweiterte M3U mit Titel – VLC zeigt ihn dann statt der nackten Adresse.
	liste := "#EXTM3U\n#EXTINF:-1," + name + "\n" + adresse + "\n"

	c.Header("Content-Type", "audio/x-mpegurl")
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.m3u"`, strings.TrimSuffix(name, filepath.Ext(name))))
	c.String(http.StatusOK, liste)
}

// pfadEscape kodiert die Pfadbestandteile einzeln – ein PathEscape über den
// ganzen Pfad würde auch die Schrägstriche umschreiben und den Pfad zerstören.
func pfadEscape(p string) string {
	teile := strings.Split(p, "/")
	for i, t := range teile {
		teile[i] = url.PathEscape(t)
	}
	return strings.Join(teile, "/")
}

// shareTokenAusAnfrage prüft, ob die Anfrage eine gültige Verknüpfung trägt.
// Wird von der Anmeldesperre UND von den Auslieferungswegen benutzt.
func (s *Server) shareTokenAusAnfrage(c *gin.Context) bool {
	tok := c.Query("tok")
	if tok == "" {
		return false
	}
	// Freigabe und Pfad aus der Adresse holen – die Signatur ist an genau
	// dieses Paar gebunden.
	share := c.Param("id")
	pfad := strings.TrimPrefix(c.Param("path"), "/")
	if share == "" {
		return false
	}
	return s.shareTokenGueltig(tok, share, pfad)
}

package api

// Digest-Anmeldung für WebDAV (R732).
//
// ── Warum ──────────────────────────────────────────────────────────────────
//
// Nachgewiesen im Protokoll: Windows schickt OPTIONS ohne Anmeldung, bekommt
// 401 – und gibt auf, statt es mit Zugangsdaten zu wiederholen. Das ist die
// Voreinstellung von BasicAuthLevel: Basisanmeldung nur über HTTPS, weil das
// Kennwort dabei im Klartext ginge.
//
// Digest gibt Windows auch über unverschlüsseltes HTTP heraus, denn dabei
// wandert kein Kennwort über die Leitung, sondern nur eine Prüfsumme aus
// Kennwort, einer Einmalzahl des Servers und der Anfrage.
//
// Damit entfällt die Registrierungsänderung, zu der ich dich sonst hätte
// schicken müssen. Basis bleibt zusätzlich erlaubt – über HTTPS ist sie
// unbedenklich, und andere Programme als Windows beherrschen oft nur sie.
//
// ── Was Digest NICHT leistet ───────────────────────────────────────────────
//
// Die übertragenen Daten bleiben unverschlüsselt. Geschützt ist nur das
// Kennwort. Wer im Netz mitliest, sieht weiterhin die Dateiinhalte. Im eigenen
// Heimnetz ist das vertretbar; über das Internet gehört HTTPS davor, und das
// steht auch so in der Oberfläche.
//
// MD5 ist für Digest vorgeschrieben und gilt für sich genommen als schwach.
// Hier trägt es nichts Geheimes: Ein Angreifer, der die Prüfsumme bricht,
// bekäme die Zugangskennung einer einzelnen Freigabe – dieselbe, die ohnehin
// bei jeder Basisanmeldung über HTTP im Klartext ginge.

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const digestRealm = "Fundus-Freigabe"

// Ausgegebene Einmalzahlen mit Ausstellungszeit. Mehr braucht es nicht: Sie
// dienen dazu, dass eine abgehörte Prüfsumme nicht beliebig lange wiederholbar
// ist, nicht als Sitzungsverwaltung.
var (
	nonceMu  sync.Mutex
	nonces   = map[string]time.Time{}
	nonceTTL = 30 * time.Minute
)

func neueNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	n := hex.EncodeToString(b)
	nonceMu.Lock()
	// Alte beim Anlegen mit ausräumen, damit die Liste nicht wächst.
	if len(nonces) > 500 {
		for k, t := range nonces {
			if time.Since(t) > nonceTTL {
				delete(nonces, k)
			}
		}
	}
	nonces[n] = time.Now()
	nonceMu.Unlock()
	return n
}

func nonceBekannt(n string) bool {
	nonceMu.Lock()
	defer nonceMu.Unlock()
	t, da := nonces[n]
	if !da {
		return false
	}
	if time.Since(t) > nonceTTL {
		delete(nonces, n)
		return false
	}
	return true
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// digestFelder zerlegt die Authorization-Kopfzeile in ihre Bestandteile.
func digestFelder(hdr string) map[string]string {
	out := map[string]string{}
	rest := strings.TrimSpace(strings.TrimPrefix(hdr, "Digest"))
	// Durch Kommas trennen, aber nicht innerhalb von Anführungszeichen –
	// der uri-Wert enthält bei uns Pfade, die Kommas tragen können.
	var teile []string
	var akt strings.Builder
	inZitat := false
	for _, r := range rest {
		switch {
		case r == '"':
			inZitat = !inZitat
			akt.WriteRune(r)
		case r == ',' && !inZitat:
			teile = append(teile, akt.String())
			akt.Reset()
		default:
			akt.WriteRune(r)
		}
	}
	if akt.Len() > 0 {
		teile = append(teile, akt.String())
	}
	for _, t := range teile {
		k, v, ok := strings.Cut(strings.TrimSpace(t), "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

// digestPruefen prüft eine Digest-Anmeldung gegen das erwartete Kennwort.
func digestPruefen(c *gin.Context, erwartetesKennwort string) bool {
	hdr := c.GetHeader("Authorization")
	if !strings.HasPrefix(hdr, "Digest ") || erwartetesKennwort == "" {
		return false
	}
	f := digestFelder(hdr)
	if !nonceBekannt(f["nonce"]) {
		return false
	}
	// HA1 aus Benutzer, Bereich und Kennwort; HA2 aus Methode und Adresse.
	ha1 := md5hex(f["username"] + ":" + digestRealm + ":" + erwartetesKennwort)
	ha2 := md5hex(c.Request.Method + ":" + f["uri"])

	var erwartet string
	if f["qop"] == "auth" || f["qop"] == "auth-int" {
		erwartet = md5hex(ha1 + ":" + f["nonce"] + ":" + f["nc"] + ":" +
			f["cnonce"] + ":" + f["qop"] + ":" + ha2)
	} else {
		// Ältere Form ohne qop – manche Programme schicken sie noch.
		erwartet = md5hex(ha1 + ":" + f["nonce"] + ":" + ha2)
	}
	// Vergleich in gleichbleibender Zeit wäre hier übertrieben: Die Prüfsumme
	// ist öffentlich, geheim ist nur das Kennwort, das daraus nicht rückwärts
	// zu gewinnen ist. Trotzdem kostet es nichts.
	return len(erwartet) == len(f["response"]) &&
		subtleGleich(erwartet, f["response"])
}

func subtleGleich(a, b string) bool {
	var v byte
	for i := 0; i < len(a) && i < len(b); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0 && len(a) == len(b)
}

// davAnmeldungVerlangen fordert zur Anmeldung auf – Digest ZUERST.
//
// Die Reihenfolge entscheidet: Windows nimmt das erste Verfahren, das es
// beherrscht und für die Verbindung zulässt. Steht Basic vorn, wählt es Basic
// und bricht über HTTP ab – genau das Verhalten aus dem Protokoll.
func davAnmeldungVerlangen(c *gin.Context) {
	n := neueNonce()
	if n != "" {
		c.Writer.Header().Add("WWW-Authenticate", fmt.Sprintf(
			`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5`, digestRealm, n))
	}
	c.Writer.Header().Add("WWW-Authenticate",
		fmt.Sprintf(`Basic realm="%s"`, digestRealm))
	c.AbortWithStatus(http.StatusUnauthorized)
}

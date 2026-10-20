package api

// Anmeldepflicht (R659).
//
// Bisher ließ sich der Node auch ohne Anmeldung benutzen: Angebote ansehen,
// suchen, Fahrten sehen. Gewollt ist, dass ohne Konto gar nichts geht.
//
// Umgesetzt als eine Schicht VOR allen Endpunkten, nicht verstreut in den
// einzelnen Handlern – sonst wird irgendwann einer vergessen, und die Lücke
// fällt niemandem auf. Offen bleibt nur, was man zum Anmelden selbst braucht.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// gateOpenPrefixes: Pfade, die ohne Anmeldung erreichbar bleiben MÜSSEN.
// Alles, was nicht hier steht, verlangt eine Sitzung.
var gateOpenPrefixes = []string{
	// Anmelden und Konto anlegen
	"/api/v1/identity/derive",
	"/api/v1/identity/me",       // zum Prüfen, OB jemand angemeldet ist
	"/api/v1/identity/logout",
	"/api/v1/wallet/open",
	"/api/v1/wallet/derive",
	// Betriebszustand des Nodes – ohne das kann die Seite nichts anzeigen,
	// und es gibt nichts Persönliches preis.
	"/api/v1/health",
	"/api/v1/node/info",
	"/api/v1/update/info",
	"/api/v1/lang",
	"/api/v1/status",
	"/api/v1/nat",
	"/api/v1/donate/config",
	// Anmeldeseite der Erst-Einrichtung im Hotspot
	"/api/v1/wifi/",
	"/api/v1/setup/",
}

// gateOpenExact ergänzt einzelne Pfade.
var gateOpenExact = []string{
	"/api/v1/shop/price", // Referenzkurs: reine Information, kein Bezug zu Personen
}

func gateIstOffen(pfad string) bool {
	for _, p := range gateOpenExact {
		if pfad == p {
			return true
		}
	}
	for _, p := range gateOpenPrefixes {
		if strings.HasPrefix(pfad, p) {
			return true
		}
	}
	return false
}

// loginGate verlangt für alle übrigen Endpunkte eine angemeldete Sitzung.
func (s *Server) loginGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		pfad := c.Request.URL.Path
		if !strings.HasPrefix(pfad, "/api/") || gateIstOffen(pfad) {
			c.Next()
			return
		}
		// Der Betreiber im Heimnetz mit Admin-Passwort kommt durch: Er muss den
		// Node auch dann verwalten können, wenn gerade niemand angemeldet ist.
		if s.isAdminRequest(c) {
			c.Next()
			return
		}
		if sess := s.getSession(c); sess != nil && sess.identity != nil {
			c.Next()
			return
		}
		// R720: Befristete, signierte Abspiel-Verknüpfung. VLC und andere
		// Abspieler schicken keine Sitzungskennung mit – ohne diese Ausnahme
		// wäre eine solche Verknüpfung wertlos.
		//
		// Das weicht die Sperre nicht auf: Die Signatur ist an genau EINE
		// Freigabe und EINEN Pfad gebunden, läuft nach zwölf Stunden ab, und
		// ausgestellt wird sie nur jemandem, der die Datei ohnehin lesen darf.
		// Mehr als diese eine Datei gibt sie nicht preis.
		if strings.HasPrefix(pfad, "/api/v1/shares/") &&
			(s.shareTokenAusAnfrage(c) || s.remoteTokenGueltig(c)) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": "Bitte zuerst anmelden.",
			"hint": "Anmelden mit E-Mail und selbst gewähltem Passwort oder mit den " +
				"Schlüsselwörtern. Beides führt zum selben Konto.",
			"login_required": true,
		})
	}
}

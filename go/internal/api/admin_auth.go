package api

// Admin-Authentifizierung per Wallet-Login (Eigentumsmodell Stufe A).
//
// Schutzmodell:
//   - Lokale Anfragen (direkt an 127.0.0.1) gelten als vertrauenswürdig — der
//     Betreiber sitzt am Node. So sperrt man sich lokal NIE aus.
//   - Anfragen, die über den P2P-Tunnel kommen (Header X-Fundus-Tunnel gesetzt),
//     MÜSSEN eine gültige Admin-Session haben, deren Wallet-Adresse mit der
//     Node-Wallet (dem Eigentümer) übereinstimmt.
//   - Login: Wallet+Key → Identität ableiten → wenn Adresse == Node-Wallet,
//     wird ein Session-Cookie gesetzt.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/identity"
	"github.com/gin-gonic/gin"
)

var tunnelHeader = "X-Fundus-Tunnel" // nur vom P2P-Tunnel der Zielseite gesetzt

// ── EIN Admin-System ────────────────────────────────────────────────────────
//
// Früher gab es drei Wege zu Admin-Rechten: nginx-Basic-Auth, ein eigener
// Go-Admin-Login mit Session-Cookie (fundus_admin) und "angemeldete Wallet ==
// Admin-Wallet". Jetzt gilt nur noch:
//
//   1. Das Admin-Passwort (nginx Basic-Auth, beim Deploy mit -AdminPass
//      gesetzt). nginx prüft es und reicht den GEPRÜFTEN Benutzer als
//      X-Fundus-Owner weiter (siehe basicAuthOwner) – im Admin-Bereich immer,
//      bei Betreiber-Aktionen für schreibende Methoden.
//   2. Echte lokale Zugriffe direkt auf dem Pi (curl auf 127.0.0.1:3000), die
//      nicht über den Tunnel kommen – so sperrt man sich nie aus.
//
// Die Admin-Wallet bleibt für ihre fachlichen Aufgaben (z.B. Update-Signatur)
// erhalten, ist aber kein Login-Weg mehr.

// adminLoginGone: der frühere Go-Admin-Login ist abgeschafft.
func (s *Server) adminLoginGone(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{
		"error": "Der separate Admin-Login wurde abgeschafft. Admin-Bereich: mit dem Admin-Passwort (Browser-Abfrage) anmelden.",
	})
}

// adminAuthStatus meldet, ob die aktuelle Anfrage als Admin gilt.
func (s *Server) adminAuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"authenticated": s.isAdminRequest(c),
		"local":         isLocalRequest(c) && c.GetHeader(tunnelHeader) == "",
		"method":        "admin-password",
	})
}

// isAdminRequest: lokal am Pi ODER von nginx geprüftes Admin-Passwort.
func (s *Server) isAdminRequest(c *gin.Context) bool {
	if isLocalRequest(c) && c.GetHeader(tunnelHeader) == "" && c.GetHeader("X-Real-IP") == "" {
		return true // direkt auf 127.0.0.1:3000, nicht über nginx/Tunnel
	}
	return basicAuthOwner(c)
}

// tunnelGuardMiddleware ist die GLOBALE Schutzregel für Anfragen, die über den
// P2P-Tunnel kommen (Header X-Fundus-Tunnel). Sie gilt für ALLE Routen, nicht nur
// /admin — denn geldbewegende Endpunkte wie /wallet/transfer, /payout, /stake
// liegen NICHT unter /admin und müssen trotzdem geschützt sein.
//
// Regel: Über den Tunnel ohne gültige Admin-Session sind NUR lesende GET-Anfragen
// erlaubt. Alles Schreibende (POST/PUT/DELETE) und alle als sensibel markierten
// Pfade werden blockiert. Mit gültiger Admin-Session ist alles erlaubt.
// Lokale Anfragen (nicht über den Tunnel) sind davon völlig unberührt.
func (s *Server) tunnelGuardMiddleware() gin.HandlerFunc {
	// Pfade, die auch lesend (GET) NICHT über den Tunnel ohne Login erlaubt sind
	// (sie könnten sensible Daten preisgeben).
	sensitiveReadPrefixes := []string{
		"/api/v1/admin/",   // gesamter Admin-Bereich
		"/api/v1/wallet",   // Wallet-Details/Balance
	}
	// Endpunkte, die für den remote-Login nötig sind → immer erlaubt.
	loginExempt := map[string]bool{
		"/api/v1/admin/login":  true,
		"/api/v1/admin/logout": true,
		"/api/v1/admin/auth":   true,
		"/api/v1/admin/wallet": true, // nur GET (öffentliche Adresse); POST ist lokal-geschützt im Handler
	}
	return func(c *gin.Context) {
		// Keine Tunnel-Anfrage → unberührt durchlassen (lokaler Zugriff).
		if c.GetHeader(tunnelHeader) == "" {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		// Login-Endpunkte immer erlauben (sonst könnte man sich remote nie anmelden).
		if loginExempt[path] {
			c.Next()
			return
		}
		// Mit gültiger Admin-Session oder von nginx geprüftem Admin-Passwort ist
		// über den Tunnel alles erlaubt.
		if s.isAdminRequest(c) || basicAuthOwner(c) {
			c.Next()
			return
		}
		// Ab hier: Tunnel OHNE Admin-Session.
		// 1. Schreibende Methoden auf Betreiber-Pfade blockieren. Nutzer-Aktionen
		//    (Login, Messenger, Partner, Marktplatz) sind über die eigene Session
		//    bzw. Wallet-Signatur abgesichert und bleiben erlaubt.
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			if isOwnerPath(path) || strings.HasPrefix(path, "/api/v1/admin/") || strings.HasPrefix(path, "/api/v1/system/") {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "Schreibzugriff über den Tunnel erfordert Anmeldung als Node-Eigentümer",
				})
				return
			}
		}
		// 2. Sensible Lese-Pfade blockieren.
		for _, pre := range sensitiveReadPrefixes {
			if strings.HasPrefix(path, pre) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "Dieser Bereich erfordert Anmeldung als Node-Eigentümer",
				})
				return
			}
		}
		// Sonst: lesende Anfrage auf öffentlichen Bereich → erlaubt.
		c.Next()
	}
}

// adminAuthMiddleware schützt zusätzlich die Admin-Routen (Doppelschutz zur
// globalen tunnelGuardMiddleware, schadet nicht). Behalten für Klarheit.
func (s *Server) adminAuthMiddleware() gin.HandlerFunc {
	authExempt := map[string]bool{
		"/api/v1/admin/login":  true,
		"/api/v1/admin/logout": true,
		"/api/v1/admin/auth":   true,
		"/api/v1/admin/wallet": true,
	}
	return func(c *gin.Context) {
		if authExempt[c.FullPath()] {
			c.Next()
			return
		}
		if c.GetHeader(tunnelHeader) != "" && !s.isAdminRequest(c) && !basicAuthOwner(c) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Admin-Zugriff über den Tunnel erfordert Anmeldung (Wallet des Node-Eigentümers)",
			})
			return
		}
		c.Next()
	}
}

// isLocalRequest prüft, ob die Anfrage von localhost kommt.
func isLocalRequest(c *gin.Context) bool {
	ip := c.ClientIP()
	return ip == "127.0.0.1" || ip == "::1" || ip == "localhost"
}

// ─── Admin-Wallet persistent speichern (admin.json im DataDir) ───────────────

// adminWalletStore hält die zugelassene Admin-Wallet-Adresse persistent.
// Es wird NUR die abgeleitete öffentliche Adresse gespeichert — NIEMALS die
// Seed-Wörter oder ein Schlüssel.
type adminWalletStore struct {
	mu     sync.RWMutex
	path   string
	wallet string
}

func newAdminWalletStore(dataDir string) *adminWalletStore {
	as := &adminWalletStore{path: filepath.Join(dataDir, "admin.json")}
	as.load()
	return as
}

func (as *adminWalletStore) get() string {
	as.mu.RLock()
	defer as.mu.RUnlock()
	return as.wallet
}

func (as *adminWalletStore) set(wallet string) error {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.wallet = strings.ToLower(strings.TrimSpace(wallet))
	data, err := json.MarshalIndent(map[string]string{"admin_wallet": as.wallet}, "", "  ")
	if err != nil {
		return err
	}
	tmp := as.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, as.path)
}

func (as *adminWalletStore) load() {
	data, err := os.ReadFile(as.path)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(data, &m) == nil {
		as.wallet = m["admin_wallet"]
	}
}

// allowedAdminWallet ermittelt die zugelassene Admin-Wallet: persistierte
// (admin.json) hat Vorrang, dann Config (FUNDUS_ADMIN_WALLET), dann Node-Wallet.
func (s *Server) allowedAdminWallet() string {
	if s.adminWallet != nil {
		if w := s.adminWallet.get(); w != "" {
			return w
		}
	}
	if s.cfg != nil && s.cfg.AdminWallet != "" {
		return strings.ToLower(strings.TrimSpace(s.cfg.AdminWallet))
	}
	if s.fileStore != nil {
		return strings.ToLower(s.fileStore.NodeWalletAddress())
	}
	return ""
}

// adminSetWallet legt die Admin-Wallet aus 30 Seed-Wörtern fest. NUR LOKAL
// erlaubt (Henne-Ei: der Admin-Bereich wird durch die Admin-Wallet geschützt,
// also darf man sie nicht über den Tunnel setzen — sonst könnte sich jeder
// selbst zum Admin machen). Die Wörter werden NICHT gespeichert, nur die Adresse.
// Body: { "seed_words": "wort1 wort2 … wort30" }
func (s *Server) adminSetWallet(c *gin.Context) {
	if !isLocalRequest(c) || c.GetHeader(tunnelHeader) != "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Die Admin-Wallet kann aus Sicherheitsgründen nur lokal am Node gesetzt werden",
		})
		return
	}
	var req struct {
		SeedWords string `json:"seed_words" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	words := strings.Fields(req.SeedWords)
	addr, err := identity.DeriveAddressFromSeed(words)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Seed-Wörter ungültig: " + err.Error()})
		return
	}
	if s.adminWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Admin-Speicher nicht verfügbar"})
		return
	}
	if err := s.adminWallet.set(addr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	// Die abgeleitete Adresse zurückgeben (die Wörter NICHT).
	c.JSON(http.StatusOK, gin.H{"ok": true, "admin_wallet": strings.ToLower(addr)})
}

// adminGetWallet meldet die aktuell zugelassene Admin-Wallet (öffentliche Adresse).
func (s *Server) adminGetWallet(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"admin_wallet": s.allowedAdminWallet(),
		"configured":   s.allowedAdminWallet() != "",
	})
}

// randomToken erzeugt einen zufälligen Session-Token (32 Byte hex).
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback auf Zeit-basiert (sehr unwahrscheinlich, dass rand fehlschlägt).
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

// ownerPathPrefixes: Aktionen, die den Node als Ganzes betreffen (Node-Wallet,
// Node-Seed, Blockproduktion, Gebühren). Schreibend nur für den Betreiber.
var ownerPathPrefixes = []string{
	"/api/v1/wallet/payout",
	"/api/v1/wallet/stake",
	"/api/v1/wallet/unstake",
	"/api/v1/wallet/mint",
	"/api/v1/chain/produce",
	"/api/v1/grid/fee/",
	"/api/v1/files/node-seed",
	"/api/v1/files/info",    // DELETE = Datei löschen (GET bleibt frei)
	"/api/v1/files/share",   // öffentlich teilen
	"/api/v1/files/unshare", // Freigabe aufheben
}

// basicAuthOwner: nginx hat das Admin-Passwort (Basic-Auth) geprüft und den
// Benutzer in X-Fundus-Owner eingetragen. Verlässlich NUR, wo nginx die
// Anmeldung erzwingt: im Admin-Bereich immer, bei Betreiber-Pfaden nur für
// schreibende Methoden (limit_except GET). Sonst könnte ein Client den Wert
// über einen selbst gebauten Authorization-Header vorgeben.
func basicAuthOwner(c *gin.Context) bool {
	if c.GetHeader("X-Fundus-Owner") == "" {
		return false
	}
	p := c.Request.URL.Path
	if strings.HasPrefix(p, "/api/v1/admin/") {
		return true
	}
	m := c.Request.Method
	return m != http.MethodGet && m != http.MethodHead && isOwnerPath(p)
}

func isOwnerPath(p string) bool {
	for _, pre := range ownerPathPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// ownerGuardMiddleware schützt schreibende Betreiber-Aktionen. Erlaubt sind:
// echte lokale Anfragen / Admin-Session (isAdminRequest) oder Anfragen, die
// nginx per Basic-Auth (Admin-Passwort) geprüft hat (Header X-Fundus-Owner,
// den nginx nur in der geschützten Location setzt und sonst leert).
func (s *Server) ownerGuardMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		m := c.Request.Method
		if m == http.MethodGet || m == http.MethodHead || !isOwnerPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		if s.isAdminRequest(c) || basicAuthOwner(c) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "Nur für den Node-Betreiber – bitte mit dem Admin-Passwort anmelden",
		})
	}
}

package api

// Schutz für den Betrieb im öffentlichen Netz (Port 443).
//
//  1. Anmeldebremse: Die Anmeldung (Argon2id, 256 MiB, mehrere Sekunden, strikt
//     nacheinander) ist teuer. Pro Adresse sind 3 Versuche in 10 min frei, danach
//     120 s Sperre, jede weitere Sperre verdoppelt sich (max. 1 h); nach 30 min
//     Ruhe ist alles zurückgesetzt. Aus dem Heimnetz gilt keine Sperre.
//     (Bei E-Mail+Passwort gibt es keine "falschen" Versuche – jede Kombination
//     ergibt eine gültige, nur andere Identität –, daher zählt jeder Versuch.)
//  2. Uploads von außen nur mit Anmeldung (kein Größenlimit). Der Messenger
//     braucht dieselbe Schnittstelle für Anhänge; anonyme Uploads sind gesperrt.

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// clientIP: echte Adresse des Aufrufers (nginx setzt X-Real-IP), sonst die Verbindung.
func clientIP(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("X-Real-IP")); v != "" {
		return v
	}
	return c.ClientIP()
}

// isLANRequest: Aufrufer aus dem Heimnetz (private Bereiche) oder lokal.
func isLANRequest(c *gin.Context) bool {
	ip := net.ParseIP(clientIP(c))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

type loginThrottleState struct {
	attempts  []time.Time
	lockUntil time.Time
	locks     int
	lastSeen  time.Time
}

var (
	loginThrottleMu sync.Mutex
	loginThrottle   = map[string]*loginThrottleState{}
)

const (
	loginFree        = 3
	loginWindow      = 10 * time.Minute
	loginFirstLock   = 120 * time.Second
	loginMaxLock     = time.Hour
	loginQuietReset  = 30 * time.Minute
)

// loginThrottleMiddleware: Anmeldebremse pro Adresse (nicht im Heimnetz).
func (s *Server) loginThrottleMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || isLANRequest(c) {
			c.Next()
			return
		}
		ip := clientIP(c)
		now := time.Now()
		loginThrottleMu.Lock()
		st := loginThrottle[ip]
		if st == nil || now.Sub(st.lastSeen) > loginQuietReset {
			st = &loginThrottleState{}
			loginThrottle[ip] = st
		}
		st.lastSeen = now
		if now.Before(st.lockUntil) {
			wait := int(st.lockUntil.Sub(now).Seconds()) + 1
			loginThrottleMu.Unlock()
			c.Header("Retry-After", fmt.Sprint(wait))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":       fmt.Sprintf("Zu viele Anmeldeversuche – bitte in %d s erneut versuchen.", wait),
				"retry_after": wait,
			})
			return
		}
		// Versuche im Fenster zählen
		kept := st.attempts[:0]
		for _, t := range st.attempts {
			if now.Sub(t) < loginWindow {
				kept = append(kept, t)
			}
		}
		st.attempts = append(kept, now)
		if len(st.attempts) > loginFree {
			lock := loginFirstLock << uint(st.locks)
			if lock > loginMaxLock || lock <= 0 {
				lock = loginMaxLock
			}
			st.locks++
			st.lockUntil = now.Add(lock)
			st.attempts = nil
			wait := int(lock.Seconds())
			loginThrottleMu.Unlock()
			c.Header("Retry-After", fmt.Sprint(wait))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":       fmt.Sprintf("Zu viele Anmeldeversuche – bitte in %d s erneut versuchen.", wait),
				"retry_after": wait,
			})
			return
		}
		// Speicher begrenzen: alte Einträge gelegentlich entfernen
		if len(loginThrottle) > 5000 {
			for k, v := range loginThrottle {
				if now.Sub(v.lastSeen) > loginQuietReset {
					delete(loginThrottle, k)
				}
			}
		}
		loginThrottleMu.Unlock()
		c.Next()
	}
}

// uploadAuthMiddleware: Uploads von außen nur mit Anmeldung (Heimnetz frei).
func (s *Server) uploadAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead ||
			!strings.HasPrefix(c.Request.URL.Path, "/api/v1/files/upload") || isLANRequest(c) {
			c.Next()
			return
		}
		if sess := s.getSession(c); sess != nil && sess.identity != nil {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Hochladen nur für angemeldete Nutzer – bitte oben rechts anmelden."})
	}
}

// ── Teure bzw. heikle Endpunkte für Aufrufer von außen ──────────────────────
//  - /api/v1/connect: Node wählt beliebige Adressen an (Diagnose) → nur Heimnetz,
//    sonst ließe sich darüber ins Heimnetz hineinschauen.
//  - /api/v1/analyze: KI-Analyse (Ollama) → nur mit Anmeldung, max. 6 je 10 min.
//  - /api/v1/files/stream/: ffmpeg-Umverpackung → nur mit Anmeldung, max. 2
//    gleichzeitig von außen (sonst ist der Prozessor schnell ausgelastet).

var (
	analyzeMu    sync.Mutex
	analyzeHits  = map[string][]time.Time{}
	extStreamSem   = make(chan struct{}, 4) // von außen gesamt (Umverpacken kopiert nur, rechnet nicht um)
	extStreamMu    sync.Mutex
	extStreamPerIP = map[string]int{}
)

// extRate: einfache Begrenzung je Adresse und Zweck (Fenster 1 min).
var (
	extRateMu   sync.Mutex
	extRateHits = map[string][]time.Time{}
)

func extRateAllow(key string, limit int) bool {
	now := time.Now()
	extRateMu.Lock()
	defer extRateMu.Unlock()
	kept := extRateHits[key][:0]
	for _, t := range extRateHits[key] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		extRateHits[key] = kept
		return false
	}
	extRateHits[key] = append(kept, now)
	if len(extRateHits) > 5000 {
		for k, v := range extRateHits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Minute {
				delete(extRateHits, k)
			}
		}
	}
	return true
}

func (s *Server) publicHeavyGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		// Selbsterkennung (R566): Die erste Anfrage von AUSSEN über einen echten
		// Hostnamen verrät die öffentliche Adresse dieses Nodes – einmalig
		// merken, damit QR-Codes und Mail-Links von Anfang an stimmen. Eine feste
		// Vorgabe im Installer verbietet sich: Sie ließe fremde Nodes auf die
		// Adresse eines einzelnen Betreibers zeigen.
		s.learnPublicURL(c)
		// Von außen: Eine Anfrage, die eine SUCHE über alle Nodes auslöst
		// (Datei-, Partner-, Jobsuche, Marktplatz), darf das Netz nicht beliebig
		// belasten; chain/peers fragt alle Peers ab und ist Diagnose (Heimnetz).
		if !isLANRequest(c) {
			ip := clientIP(c)
			switch {
			case p == "/api/v1/chain/peers":
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "nur aus dem Heimnetz"})
				return
			case p == "/api/v1/files/search" || p == "/api/v1/partner/search" || p == "/api/v1/search" || p == "/api/v1/jobsearch":
				if !extRateAllow("search:"+ip, 20) {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Suchanfragen – bitte kurz warten."})
					return
				}
			case strings.HasPrefix(p, "/api/v1/files/probe/"):
				if !extRateAllow("probe:"+ip, 20) {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Anfragen – bitte kurz warten."})
					return
				}
			case strings.HasPrefix(p, "/api/v1/files/thumb/"):
				if !extRateAllow("thumb:"+ip, 150) {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Anfragen – bitte kurz warten."})
					return
				}
			}
		}
		// Komplette Dateiliste bzw. Ordner des Pi durchsuchen: Betreiber-Werkzeuge,
		// von außen ein Datenleck (private Dateien, Systempfade). Lesbar von außen
		// sind nur ausdrücklich freigegebene Dateien (/api/v1/files/shared).
		isConnect := p == "/api/v1/connect" || p == "/api/v1/files/list" || p == "/api/v1/files/browse"
		isAnalyze := p == "/api/v1/analyze"
		isStream := strings.HasPrefix(p, "/api/v1/files/stream/")
		if (!isConnect && !isAnalyze && !isStream) || isLANRequest(c) {
			c.Next()
			return
		}
		if isConnect {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "nur aus dem Heimnetz"})
			return
		}
		// Streams (MKV → MP4 per ffmpeg) auch OHNE Anmeldung: Freigaben sind
		// öffentlich gedacht (/shared). Schutz des Prozessors über die
		// Begrenzung unten (max. 4 gleichzeitig, 3 je Besucher).
		if !isStream {
			if sess := s.getSession(c); sess == nil || sess.identity == nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
				return
			}
		}
		if isAnalyze {
			ip, now := clientIP(c), time.Now()
			analyzeMu.Lock()
			kept := analyzeHits[ip][:0]
			for _, t := range analyzeHits[ip] {
				if now.Sub(t) < 10*time.Minute {
					kept = append(kept, t)
				}
			}
			if len(kept) >= 6 {
				analyzeHits[ip] = kept
				analyzeMu.Unlock()
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Analysen – bitte in einigen Minuten erneut versuchen."})
				return
			}
			analyzeHits[ip] = append(kept, now)
			if len(analyzeHits) > 5000 {
				for k, v := range analyzeHits {
					if len(v) == 0 || now.Sub(v[len(v)-1]) > 10*time.Minute {
						delete(analyzeHits, k)
					}
				}
			}
			analyzeMu.Unlock()
			c.Next()
			return
		}
		// Stream: höchstens 4 gleichzeitig von außen, 3 je Besucher-Adresse
		// (iOS/Safari stellt für ein Video mehrere kurze Anfragen parallel).
		ip := clientIP(c)
		extStreamMu.Lock()
		if extStreamPerIP[ip] >= 3 {
			extStreamMu.Unlock()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Videos gleichzeitig – bitte andere Videos zuerst schließen."})
			return
		}
		extStreamPerIP[ip]++
		extStreamMu.Unlock()
		defer func() {
			extStreamMu.Lock()
			if extStreamPerIP[ip]--; extStreamPerIP[ip] <= 0 {
				delete(extStreamPerIP, ip)
			}
			extStreamMu.Unlock()
		}()
		select {
		case extStreamSem <- struct{}{}:
			defer func() { <-extStreamSem }()
			c.Next()
		default:
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Gerade laufen zu viele Videos – bitte gleich erneut versuchen."})
		}
	}
}

// ── Schreibende Anfragen von außen: grundsätzlich verboten ──────────────────
// Ein Audit aller Endpunkte (R529) ergab, dass viele schreibende Funktionen
// keine eigene Prüfung haben – im Heimnetz unkritisch, von außen nicht (u.a.
// Ordner-Freigaben beliebiger Pfade anlegen, fremde Angebote/Orders löschen,
// Swap-Zustände ändern). Statt jede einzeln abzusichern, gilt für Aufrufer
// von AUSSEN (nicht Heimnetz, nicht P2P-Tunnel):
//   1. Betreiber-/Infrastruktur-Pfade: gesperrt (nur Heimnetz).
//   2. Durch Schlüssel/Signatur geschützte bzw. Anmelde-Pfade: offen.
//   3. Alles andere: nur mit Anmeldung.

var extWriteLANOnly = []string{
	"/api/v1/shares",        // Ordner-Freigaben (Pfade auf dem Pi!)
	"/api/v1/topology/",     // Energie-Netz: Verbindungen, Gebühren
	"/api/v1/grid/",         // Kapazität, Handel im Energie-Netz
	"/api/v1/meter/",        // Zählerstände
	"/api/v1/energy",        // Energie-Token anlegen
	"/api/v1/certificates",  // Zertifikate ausstellen
	"/api/v1/files/manifest",
	"/api/v1/swap/auto/",    // automatischer Handel des Nodes
}

var extWriteOpen = []string{
	"/api/v1/identity/derive",  // Anmeldung (mit Anmeldebremse)
	"/api/v1/wallet/open",      // Wallet öffnen (mit Anmeldebremse)
	"/api/v1/wallet/derive",
	"/api/v1/chain/tx",         // signierte Transaktion
	"/api/v1/chain/send",       // mit Seed-Wörtern
	"/api/v1/swap/keyaddr",     // Adresse aus mitgeschicktem Schlüssel
	"/api/v1/swap/derive-addrs",
	"/api/v1/swap/secret",
	"/api/v1/swap/fnd/",        // Sperren/Einlösen: Schlüssel im Anfragetext
	"/api/v1/swap/sol/",
}

// isSwapPhasePath: /api/v1/swap/<id>/phase – Zustand eines Swaps setzen.
func isSwapPhasePath(p string) bool {
	return strings.HasPrefix(p, "/api/v1/swap/") && strings.HasSuffix(p, "/phase")
}

func hasAnyPrefix(p string, list []string) bool {
	for _, pre := range list {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func (s *Server) externalWriteGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		m := c.Request.Method
		p := c.Request.URL.Path
		if isLANRequest(c) || !strings.HasPrefix(p, "/api/") {
			c.Next()
			return
		}
		// Lesend: Hinterlegungs-Übersicht und Adressbuch sind Betreiber-Sache.
		if m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions {
			// Adressbuch: je Nutzer (Besitzer-Prüfung im Handler, R553).
			if p == "/api/v1/swap/deposits" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "nur aus dem Heimnetz"})
				return
			}
			c.Next()
			return
		}
		if hasAnyPrefix(p, extWriteLANOnly) || isSwapPhasePath(p) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "nur aus dem Heimnetz"})
			return
		}
		if hasAnyPrefix(p, extWriteOpen) {
			c.Next()
			return
		}
		if sess := s.getSession(c); sess == nil || sess.identity == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
			return
		}
		c.Next()
	}
}

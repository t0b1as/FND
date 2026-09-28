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
	extStreamSem = make(chan struct{}, 2)
)

func (s *Server) publicHeavyGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		isConnect := p == "/api/v1/connect"
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
		if sess := s.getSession(c); sess == nil || sess.identity == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
			return
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
		// Stream: höchstens 2 gleichzeitig von außen
		select {
		case extStreamSem <- struct{}{}:
			defer func() { <-extStreamSem }()
			c.Next()
		default:
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Gerade laufen zu viele Videos – bitte gleich erneut versuchen."})
		}
	}
}

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

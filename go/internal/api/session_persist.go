package api

// Sitzungen über Neustarts und Updates des Nodes hinweg.
//
// Eine Sitzung enthält die abgeleitete Identität (private Schlüssel). Sie wird
// NICHT im Klartext abgelegt: data/sessions.json enthält pro Browser-Token nur
//   - den Hash des Tokens (Nachschlage-Schlüssel),
//   - die FundusID,
//   - das Sitzungsgeheimnis, verschlüsselt mit einem Schlüssel, der aus dem
//     Token selbst abgeleitet wird (XChaCha20-Poly1305).
// Das Token existiert nur als HttpOnly-Cookie im Browser. Wer nur die Platte
// des Nodes hat, kann die Sitzungen daher nicht entschlüsseln. Kommt der
// Browser nach einem Neustart mit seinem Cookie wieder, wird die Sitzung in
// Millisekunden wiederhergestellt – ohne den teuren Argon2-Durchlauf.
//
// Verfall: 30 Tage nach der letzten Nutzung; Abmelden löscht den Eintrag.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/identity"
)

type persistedSession struct {
	FID     string    `json:"fid"`
	Enc     string    `json:"enc"`
	Created time.Time `json:"created"`
	LastUse time.Time `json:"last_use"`
}

const sessionTTL = 30 * 24 * time.Hour

var (
	persistMu    sync.Mutex
	persistCache map[string]persistedSession // Token-Hash → Eintrag (lazy geladen)
	restoreMu    sync.Mutex                  // serialisiert Wiederherstellungen
)

func sessionTokenHash(token string) string {
	h := identity.Blake3Sum256([]byte("fundus-session-id-v1:" + token))
	return hex.EncodeToString(h[:])
}

func sessionTokenKey(token string) []byte {
	h := identity.Blake3Sum256([]byte("fundus-session-key-v1:" + token))
	return h[:]
}

func (s *Server) sessionsPath() string {
	if s.cfg == nil || s.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.DataDir, "sessions.json")
}

// loadPersistedLocked lädt die Ablage einmalig (persistMu muss gehalten sein)
// und verwirft abgelaufene Einträge.
func (s *Server) loadPersistedLocked() {
	if persistCache != nil {
		return
	}
	m := map[string]persistedSession{}
	if p := s.sessionsPath(); p != "" {
		_ = readJSONFile(p, &m)
	}
	for k, e := range m {
		if time.Since(e.LastUse) > sessionTTL {
			delete(m, k)
		}
	}
	persistCache = m
}

func (s *Server) savePersistedLocked() {
	if p := s.sessionsPath(); p != "" {
		if err := atomicWriteJSON(p, persistCache); err != nil && s.log != nil {
			s.log.Warn("Sitzungsablage nicht gespeichert", zap.Error(err))
		}
	}
}

// persistSession legt die Sitzung eines frisch angemeldeten Browsers ab.
func (s *Server) persistSession(token string, id *identity.Identity) {
	if s.sessionsPath() == "" || token == "" || id == nil {
		return
	}
	plain, err := json.Marshal(id.ExportSessionSecret())
	if err != nil {
		return
	}
	enc, err := identity.Encrypt(plain, sessionTokenKey(token))
	for i := range plain {
		plain[i] = 0
	}
	if err != nil {
		return
	}
	now := time.Now()
	persistMu.Lock()
	s.loadPersistedLocked()
	persistCache[sessionTokenHash(token)] = persistedSession{
		FID: strings.ToLower(id.FundusID), Enc: base64.StdEncoding.EncodeToString(enc),
		Created: now, LastUse: now,
	}
	s.savePersistedLocked()
	persistMu.Unlock()
}

// forgetSession löscht die Ablage eines Tokens (Abmelden, Kontowechsel).
func (s *Server) forgetSession(token string) {
	if token == "" || s.sessionsPath() == "" {
		return
	}
	persistMu.Lock()
	s.loadPersistedLocked()
	h := sessionTokenHash(token)
	if _, ok := persistCache[h]; ok {
		delete(persistCache, h)
		s.savePersistedLocked()
	}
	persistMu.Unlock()
}

// restoreSession stellt nach einem Neustart die Sitzung eines Browsers aus der
// verschlüsselten Ablage wieder her. nil, wenn es keine gültige Ablage gibt.
func (s *Server) restoreSession(c *gin.Context, token string) *Session {
	if token == "" || s.sessionsPath() == "" {
		return nil
	}
	restoreMu.Lock()
	defer restoreMu.Unlock()

	// Parallele Anfrage war schneller?
	sessionMu.RLock()
	if fid := sessionTokens[token]; fid != "" {
		if sess := sessionStore[fid]; sess != nil {
			sessionMu.RUnlock()
			return sess
		}
	}
	sessionMu.RUnlock()

	h := sessionTokenHash(token)
	persistMu.Lock()
	s.loadPersistedLocked()
	e, ok := persistCache[h]
	persistMu.Unlock()
	if !ok || time.Since(e.LastUse) > sessionTTL {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(e.Enc)
	if err != nil {
		return nil
	}
	plain, err := identity.Decrypt(raw, sessionTokenKey(token))
	if err != nil {
		return nil // falsches Token / manipuliert
	}
	var sec identity.SessionSecret
	uerr := json.Unmarshal(plain, &sec)
	for i := range plain {
		plain[i] = 0
	}
	if uerr != nil {
		return nil
	}
	id, err := identity.FromSessionSecret(sec)
	for i := range sec.EdSeed {
		sec.EdSeed[i] = 0
	}
	if err != nil || !strings.EqualFold(id.FundusID, e.FID) {
		return nil
	}

	// Läuft für diese Identität schon eine Session (anderer Browser)? Dann
	// nur das Token zuordnen – kein zweiter Messenger mit doppelten Handlern.
	sessionMu.RLock()
	sess := sessionStore[id.FundusID]
	sessionMu.RUnlock()
	if sess == nil {
		sess = s.buildSession(id)
	}
	sessionMu.Lock()
	if existing := sessionStore[id.FundusID]; existing != nil {
		sess = existing
	} else {
		sessionStore[id.FundusID] = sess
	}
	sessionTokens[token] = id.FundusID
	sessionMu.Unlock()

	persistMu.Lock()
	e.LastUse = time.Now()
	persistCache[h] = e
	s.savePersistedLocked()
	persistMu.Unlock()

	// Cookie-Laufzeit verlängern (gleitend 30 Tage).
	if c != nil {
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie("fundus_session", token, int(sessionTTL/time.Second), "/", "", false, true)
	}
	if s.log != nil {
		s.log.Info("Sitzung nach Neustart wiederhergestellt", zap.String("fundusID", id.FundusID[:10]+"…"))
	}
	return sess
}

package api

// Push-Benachrichtigungen (R536) – Web Push nach RFC 8030 (Zustellung),
// RFC 8291 (Verschlüsselung, aes128gcm) und RFC 8292 (VAPID), ausschließlich
// mit der Go-Standardbibliothek.
//
//   - VAPID: Der Node erzeugt einmalig ein P-256-Schlüsselpaar
//     (DataDir/push-vapid.pem) und signiert jede Zustellung (ES256-JWT).
//   - Jede Benachrichtigung ist für das Zielgerät verschlüsselt (ECDH mit dessen
//     Schlüssel, HKDF, AES-128-GCM); der Push-Dienst sieht den Inhalt nicht.
//   - Abos gehören zur Fundus-ID des angemeldeten Nutzers
//     (DataDir/push-subs.json). Abgelaufene (404/410) werden entfernt.
//
// Datensparsam: Die Texte nennen keine Nachrichteninhalte.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/messenger"
)

type pushSub struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"` // base64url, 65 Byte unkomprimiert
	Auth     string `json:"auth"`   // base64url, 16 Byte
	Created  int64  `json:"created"`
}

type pushService struct {
	mu        sync.Mutex
	subsPath  string
	subs      map[string][]pushSub // Fundus-ID (klein) → Abos (Geräte)
	vapid     *ecdsa.PrivateKey
	vapidPub  []byte // 65 Byte unkomprimiert
	client    *http.Client
	log       *zap.Logger
}

var (
	pushOnce sync.Once
	pushSvc  *pushService
)

var b64u = base64.RawURLEncoding

func decodeB64Any(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// push liefert den Dienst (lazy), nil wenn kein Datenverzeichnis.
func (s *Server) push() *pushService {
	pushOnce.Do(func() {
		if s.cfg == nil || s.cfg.DataDir == "" {
			return
		}
		ps := &pushService{
			subsPath: filepath.Join(s.cfg.DataDir, "push-subs.json"),
			subs:     map[string][]pushSub{},
			client:   &http.Client{Timeout: 15 * time.Second},
			log:      s.log,
		}
		keyPath := filepath.Join(s.cfg.DataDir, "push-vapid.pem")
		if raw, err := os.ReadFile(keyPath); err == nil {
			if blk, _ := pem.Decode(raw); blk != nil {
				if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
					ps.vapid = k
				}
			}
		}
		if ps.vapid == nil {
			k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return
			}
			der, err := x509.MarshalECPrivateKey(k)
			if err != nil {
				return
			}
			_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
			ps.vapid = k
		}
		pub, err := ps.vapid.PublicKey.ECDH()
		if err != nil {
			return
		}
		ps.vapidPub = pub.Bytes()
		if raw, err := os.ReadFile(ps.subsPath); err == nil {
			_ = json.Unmarshal(raw, &ps.subs)
		}
		pushSvc = ps
	})
	return pushSvc
}

func (ps *pushService) saveLocked() {
	raw, _ := json.MarshalIndent(ps.subs, "", "  ")
	tmp := ps.subsPath + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, ps.subsPath)
	}
}

func (ps *pushService) add(fid string, sub pushSub) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	fid = strings.ToLower(fid)
	// Dasselbe Gerät (Endpoint) nur einmal – auch nicht unter anderer ID.
	for f, list := range ps.subs {
		kept := list[:0]
		for _, x := range list {
			if x.Endpoint != sub.Endpoint {
				kept = append(kept, x)
			}
		}
		ps.subs[f] = kept
	}
	ps.subs[fid] = append(ps.subs[fid], sub)
	ps.saveLocked()
}

func (ps *pushService) remove(endpoint string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for f, list := range ps.subs {
		kept := list[:0]
		for _, x := range list {
			if x.Endpoint != endpoint {
				kept = append(kept, x)
			}
		}
		if len(kept) == 0 {
			delete(ps.subs, f)
		} else {
			ps.subs[f] = kept
		}
	}
	ps.saveLocked()
}

func (ps *pushService) has(fid, endpoint string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, x := range ps.subs[strings.ToLower(fid)] {
		if x.Endpoint == endpoint {
			return true
		}
	}
	return false
}

// ── Kryptografie ────────────────────────────────────────────────────────────

func hkdfExtract(salt, ikm []byte) []byte {
	m := hmac.New(sha256.New, salt)
	m.Write(ikm)
	return m.Sum(nil)
}

// hkdfExpand: HKDF-Expand für Längen ≤ 32 (ein HMAC-Block genügt).
func hkdfExpand(prk, info []byte, n int) []byte {
	m := hmac.New(sha256.New, prk)
	m.Write(info)
	m.Write([]byte{1})
	return m.Sum(nil)[:n]
}

// encryptPush verschlüsselt payload für ein Gerät (RFC 8291, aes128gcm).
func encryptPush(sub pushSub, payload []byte) ([]byte, error) {
	uaPubRaw, err := decodeB64Any(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := decodeB64Any(sub.Auth)
	if err != nil || len(authSecret) == 0 {
		return nil, errors.New("auth fehlt")
	}
	curve := ecdh.P256()
	uaPub, err := curve.NewPublicKey(uaPubRaw)
	if err != nil {
		return nil, fmt.Errorf("Geräteschlüssel: %w", err)
	}
	asPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	// IKM = HKDF(auth, ecdh, "WebPush: info" || 0 || ua_public || as_public, 32)
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPubRaw...), asPub...)
	ikm := hkdfExpand(hkdfExtract(authSecret, shared), keyInfo, 32)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	prk := hkdfExtract(salt, ikm)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain := append(append([]byte{}, payload...), 0x02) // Trenner: letzter Datensatz
	ct := gcm.Seal(nil, nonce, plain, nil)
	// Kopf: salt(16) || rs(4) || idlen(1) || keyid(as_public)
	var hdr bytes.Buffer
	hdr.Write(salt)
	rs := make([]byte, 4)
	binary.BigEndian.PutUint32(rs, 4096)
	hdr.Write(rs)
	hdr.WriteByte(byte(len(asPub)))
	hdr.Write(asPub)
	hdr.Write(ct)
	return hdr.Bytes(), nil
}

// vapidAuth: "vapid t=<JWT>, k=<öffentlicher Schlüssel>" für den Endpoint.
func (ps *pushService) vapidAuth(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	aud := u.Scheme + "://" + u.Host
	head := b64u.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{
		"aud": aud,
		"exp": time.Now().Add(12 * time.Hour).Unix(),
		"sub": "https://github.com/t0b1as/FND",
	})
	signing := head + "." + b64u.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, sv, err := ecdsa.Sign(rand.Reader, ps.vapid, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sv.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64u.EncodeToString(sig) + ", k=" + b64u.EncodeToString(ps.vapidPub), nil
}

// send stellt einer Abo-Adresse zu. gone=true → Abo ist ungültig (entfernen).
func (ps *pushService) send(sub pushSub, payload []byte, urgent bool) (gone bool, err error) {
	body, err := encryptPush(sub, payload)
	if err != nil {
		return true, err // unlesbare Schlüssel → Abo unbrauchbar
	}
	auth, err := ps.vapidAuth(sub.Endpoint)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Authorization", auth)
	if urgent {
		req.Header.Set("Urgency", "high")
	} else {
		req.Header.Set("Urgency", "normal")
	}
	resp, err := ps.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return true, fmt.Errorf("Abo abgelaufen (%d)", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		var b [300]byte
		n, _ := resp.Body.Read(b[:])
		return false, fmt.Errorf("Push-Dienst %d: %s", resp.StatusCode, strings.TrimSpace(string(b[:n])))
	}
	return false, nil
}

// pushNotify sendet an alle Geräte eines Nutzers (asynchron).
func (s *Server) pushNotify(fid, title, body, link, tag string, urgent bool) {
	ps := s.push()
	fid = strings.ToLower(strings.TrimSpace(fid))
	if ps == nil || fid == "" {
		return
	}
	ps.mu.Lock()
	subs := append([]pushSub(nil), ps.subs[fid]...)
	ps.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body, "url": link, "tag": tag})
	go func() {
		for _, sub := range subs {
			gone, err := ps.send(sub, payload, urgent)
			if gone {
				ps.remove(sub.Endpoint)
			}
			if err != nil && ps.log != nil {
				ps.log.Debug("Push nicht zugestellt", zap.Error(err))
			}
		}
	}()
}

// ── Swap ↔ Nutzer (für "Swap abgeschlossen") ────────────────────────────────

var (
	swapOwnersMu sync.Mutex
	swapOwners   = map[string]string{}
)

func setSwapOwner(swapID, fid string) {
	if swapID == "" || fid == "" {
		return
	}
	swapOwnersMu.Lock()
	swapOwners[swapID] = strings.ToLower(fid)
	swapOwnersMu.Unlock()
}

func swapOwner(swapID string) string {
	swapOwnersMu.Lock()
	defer swapOwnersMu.Unlock()
	return swapOwners[swapID]
}

// ── API ─────────────────────────────────────────────────────────────────────

func (s *Server) registerPushRoutes() {
	g := s.router.Group("/api/v1/push")
	g.GET("/key", s.pushKey)
	g.GET("/status", s.pushStatus)
	g.POST("/subscribe", s.pushSubscribe)
	g.POST("/unsubscribe", s.pushUnsubscribe)
	g.POST("/test", s.pushTest)
}

func (s *Server) pushKey(c *gin.Context) {
	ps := s.push()
	if ps == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Push nicht verfügbar"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"public_key": b64u.EncodeToString(ps.vapidPub)})
}

func (s *Server) pushStatus(c *gin.Context) {
	ps := s.push()
	fid := s.sessionFID(c)
	if ps == nil || fid == "" {
		c.JSON(http.StatusOK, gin.H{"subscribed": false, "logged_in": fid != ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscribed": ps.has(fid, c.Query("endpoint")), "logged_in": true})
}

func (s *Server) pushSubscribe(c *gin.Context) {
	ps := s.push()
	fid := s.sessionFID(c)
	if ps == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Push nicht verfügbar"})
		return
	}
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !strings.HasPrefix(req.Endpoint, "https://") || req.Keys.P256dh == "" || req.Keys.Auth == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiges Abo"})
		return
	}
	ps.add(fid, pushSub{Endpoint: req.Endpoint, P256dh: req.Keys.P256dh, Auth: req.Keys.Auth, Created: time.Now().Unix()})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) pushUnsubscribe(c *gin.Context) {
	ps := s.push()
	if ps == nil || s.sessionFID(c) == "" {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Endpoint != "" {
		ps.remove(req.Endpoint)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) pushTest(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	s.pushNotify(fid, "Fundus", "✓ Benachrichtigungen funktionieren.", "/", "test", false)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// notifyIncomingMsg: Nachricht/Anruf an einen Nutzer, dessen App gerade NICHT
// offen ist (kein WebSocket erreicht). Ohne Inhalt – nur wer schreibt/anruft.
func (s *Server) notifyIncomingMsg(sess *Session, payload *messenger.Payload) {
	if sess == nil || sess.identity == nil {
		return
	}
	name := "jemandem"
	if payload != nil && strings.TrimSpace(payload.SenderName) != "" {
		name = strings.TrimSpace(payload.SenderName)
	}
	if payload != nil && payload.Signal != nil {
		switch payload.Signal.Type {
		case messenger.SignalCallAudio, messenger.SignalCallVideo, messenger.SignalOffer:
			s.pushNotify(sess.identity.FundusID, "📞 Anruf", "Anruf von "+name, "/messenger", "call", true)
		}
		return
	}
	s.pushNotify(sess.identity.FundusID, "💬 Neue Nachricht", "Neue Nachricht von "+name, "/messenger", "msg", true)
}

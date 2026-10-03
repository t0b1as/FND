package api

// E-Mail-Benachrichtigungen (R557), parallel zu Push.
//
// Warum über ein bestehendes Postfach: Ein Pi am Heimanschluss kann E-Mails
// nicht selbst zustellen (Port 25 gesperrt, kein SPF/PTR → Spam). Der Node
// versendet daher über den SMTP-Zugang des Betreibers (wie ein Mailprogramm).
//
//   - SMTP-Zugang: nur Betreiber (Heimnetz + Admin), in DataDir/mail.json.
//     Das Passwort verlässt den Node nie und wird nie zurückgegeben.
//   - Adresse je Nutzer: in DataDir/mail-subs.json, an die Fundus-ID gebunden.
//     Jeder Nutzer pflegt nur seine eigene; bestätigt per Code an die Adresse.
//   - Versendet wird dasselbe wie per Push: Nachricht, Anruf, Kauf, Swap.

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type mailConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	From     string `json:"from"`
	TLSMode  string `json:"tls_mode"` // "starttls" (587) | "tls" (465)
	BaseURL  string `json:"base_url"` // öffentliche Adresse des Nodes: Links in Mails UND QR-Codes
}

type mailSub struct {
	Address   string `json:"address"`
	Confirmed bool   `json:"confirmed"`
	Code      string `json:"code,omitempty"`
	CodeAt    int64  `json:"code_at,omitempty"`
	Messages  bool   `json:"messages"`
	Trades    bool   `json:"trades"`
}

type mailService struct {
	mu       sync.Mutex
	cfgPath  string
	subsPath string
	cfg      mailConfig
	subs     map[string]mailSub // Fundus-ID (klein) → Abo
	log      *zap.Logger
}

var (
	mailOnce sync.Once
	mailSvc  *mailService
)

func (s *Server) mailer() *mailService {
	mailOnce.Do(func() {
		if s.cfg == nil || s.cfg.DataDir == "" {
			return
		}
		ms := &mailService{
			cfgPath:  filepath.Join(s.cfg.DataDir, "mail.json"),
			subsPath: filepath.Join(s.cfg.DataDir, "mail-subs.json"),
			subs:     map[string]mailSub{},
			log:      s.log,
		}
		if raw, err := os.ReadFile(ms.cfgPath); err == nil {
			_ = json.Unmarshal(raw, &ms.cfg)
		}
		if raw, err := os.ReadFile(ms.subsPath); err == nil {
			_ = json.Unmarshal(raw, &ms.subs)
		}
		mailSvc = ms
	})
	return mailSvc
}

func writeJSONFile(path string, v any) {
	raw, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (ms *mailService) saveCfgLocked()  { writeJSONFile(ms.cfgPath, ms.cfg) }
func (ms *mailService) saveSubsLocked() { writeJSONFile(ms.subsPath, ms.subs) }

func validEmail(a string) bool {
	a = strings.TrimSpace(a)
	if len(a) < 5 || len(a) > 254 || strings.ContainsAny(a, "\r\n") {
		return false
	}
	addr, err := mail.ParseAddress(a)
	return err == nil && addr.Address == a && strings.Count(a, "@") == 1
}

// send stellt eine Nachricht über den SMTP-Zugang des Betreibers zu.
func (ms *mailService) send(to, subject, body string) error {
	ms.mu.Lock()
	cfg := ms.cfg
	ms.mu.Unlock()
	if !cfg.Enabled || cfg.Host == "" || cfg.From == "" {
		return fmt.Errorf("E-Mail-Versand nicht eingerichtet")
	}
	if !validEmail(to) {
		return fmt.Errorf("ungültige Empfängeradresse")
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))
	// Kopfzeilen RFC-konform kodieren (Umlaute im Betreff).
	msg := "From: Fundus <" + cfg.From + ">\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + body

	var auth smtp.Auth
	if cfg.User != "" {
		auth = smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	}
	if cfg.TLSMode == "tls" { // 465: TLS von Anfang an
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		defer conn.Close()
		cl, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		defer cl.Quit()
		return smtpDeliver(cl, auth, cfg.From, to, msg)
	}
	cl, err := smtp.Dial(addr) // 587: STARTTLS
	if err != nil {
		return err
	}
	defer cl.Quit()
	if ok, _ := cl.Extension("STARTTLS"); ok {
		if err := cl.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if auth != nil {
		return fmt.Errorf("Server bietet kein STARTTLS – Passwort würde unverschlüsselt übertragen")
	}
	return smtpDeliver(cl, auth, cfg.From, to, msg)
}

func smtpDeliver(cl *smtp.Client, auth smtp.Auth, from, to, msg string) error {
	if auth != nil {
		if err := cl.Auth(auth); err != nil {
			return fmt.Errorf("Anmeldung am Mailserver: %w", err)
		}
	}
	if err := cl.Mail(from); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	return w.Close()
}

// mailNotify schickt einem Nutzer eine Benachrichtigung, wenn er eine bestätigte
// Adresse hinterlegt und die Art eingeschaltet hat. kind: "messages" | "trades".
func (s *Server) mailNotify(fid, kind, subject, body, link string) {
	ms := s.mailer()
	fid = strings.ToLower(strings.TrimSpace(fid))
	if ms == nil || fid == "" {
		return
	}
	ms.mu.Lock()
	sub, ok := ms.subs[fid]
	enabled := ms.cfg.Enabled
	ms.mu.Unlock()
	if !ok || !enabled || !sub.Confirmed {
		return
	}
	if (kind == "messages" && !sub.Messages) || (kind == "trades" && !sub.Trades) {
		return
	}
	full := body
	if link != "" {
		full += "\n\nÖffnen: " + link
	}
	full += "\n\n—\nDiese Nachricht kommt von deinem Fundus-Node.\nBenachrichtigungen ändern: Menü → E-Mail-Benachrichtigungen."
	go func() {
		if err := ms.send(sub.Address, subject, full); err != nil && ms.log != nil {
			ms.log.Debug("E-Mail nicht zugestellt", zap.Error(err))
		}
	}()
}

// notifyUser: ein Aufruf für beide Wege (Push sofort, E-Mail parallel).
func (s *Server) notifyUser(fid, kind, title, body, link, tag string, urgent bool) {
	s.pushNotify(fid, title, body, link, tag, urgent)
	s.mailNotify(fid, kind, title, body, s.publicLink(link))
}

// publicLink macht aus einem Pfad eine anklickbare Adresse (für E-Mails).
func (s *Server) publicLink(path string) string {
	if path == "" {
		return ""
	}
	ms := s.mailer()
	if ms == nil {
		return ""
	}
	ms.mu.Lock()
	host := strings.TrimSpace(ms.cfg.BaseURL)
	ms.mu.Unlock()
	if host == "" {
		return ""
	}
	return strings.TrimRight(host, "/") + path
}

// ── API ─────────────────────────────────────────────────────────────────────

func (s *Server) registerMailRoutes() {
	if s.cfg == nil || s.cfg.DataDir == "" {
		return
	}
	// Öffentliche Adresse des Nodes: wird für QR-Codes gebraucht, damit diese
	// nicht die lokale IP enthalten. Kein Geheimnis – die Adresse ist öffentlich.
	s.router.GET("/api/v1/public-url", func(c *gin.Context) {
		ms := s.mailer()
		url := ""
		if ms != nil {
			ms.mu.Lock()
			url = ms.cfg.BaseURL
			ms.mu.Unlock()
		}
		c.JSON(http.StatusOK, gin.H{"base_url": url})
	})
	g := s.router.Group("/api/v1/mail")
	g.GET("/status", s.mailStatus)       // eigener Stand (angemeldet)
	g.POST("/subscribe", s.mailSubscribe) // Adresse hinterlegen → Code
	g.POST("/confirm", s.mailConfirm)     // Code bestätigen
	g.POST("/prefs", s.mailPrefs)         // Arten ein/aus
	g.POST("/remove", s.mailRemove)       // Adresse löschen

	a := s.router.Group("/api/v1/admin/mail")
	a.Use(s.adminAuthMiddleware())
	a.GET("/config", s.mailConfigGet)   // SMTP-Zugang (ohne Passwort)
	a.POST("/config", s.mailConfigSet)  // SMTP-Zugang setzen
	a.POST("/test", s.mailConfigTest)   // Testmail an eine Adresse
}

func sixDigitCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "123456"
	}
	return fmt.Sprint(n.Int64() + 100000)
}

func (s *Server) mailStatus(c *gin.Context) {
	ms := s.mailer()
	fid := s.sessionFID(c)
	if ms == nil {
		c.JSON(http.StatusOK, gin.H{"available": false})
		return
	}
	ms.mu.Lock()
	enabled := ms.cfg.Enabled
	sub, ok := ms.subs[fid]
	ms.mu.Unlock()
	out := gin.H{"available": enabled, "logged_in": fid != ""}
	if ok {
		masked := sub.Address
		if i := strings.Index(masked, "@"); i > 1 {
			masked = masked[:1] + strings.Repeat("•", i-1) + masked[i:]
		}
		out["address"] = masked
		out["confirmed"] = sub.Confirmed
		out["messages"] = sub.Messages
		out["trades"] = sub.Trades
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) mailSubscribe(c *gin.Context) {
	ms := s.mailer()
	fid := s.sessionFID(c)
	if ms == nil || fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !validEmail(req.Address) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bitte eine gültige E-Mail-Adresse angeben."})
		return
	}
	ms.mu.Lock()
	if !ms.cfg.Enabled {
		ms.mu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Der Betreiber dieses Nodes hat den E-Mail-Versand nicht eingerichtet."})
		return
	}
	code := sixDigitCode()
	ms.subs[fid] = mailSub{Address: strings.TrimSpace(req.Address), Code: code, CodeAt: time.Now().Unix(), Messages: true, Trades: true}
	ms.saveSubsLocked()
	addr := strings.TrimSpace(req.Address)
	ms.mu.Unlock()
	if err := ms.send(addr, "Fundus: E-Mail bestätigen", "Dein Bestätigungscode lautet:\n\n    "+code+"\n\nGib ihn in Fundus ein, um Benachrichtigungen an diese Adresse zu erhalten.\nFalls du das nicht warst, kannst du diese Nachricht ignorieren."); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Code konnte nicht versendet werden: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "confirm_needed": true})
}

func (s *Server) mailConfirm(c *gin.Context) {
	ms := s.mailer()
	fid := s.sessionFID(c)
	if ms == nil || fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	_ = c.ShouldBindJSON(&req)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	sub, ok := ms.subs[fid]
	if !ok || sub.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Keine offene Bestätigung."})
		return
	}
	if time.Since(time.Unix(sub.CodeAt, 0)) > 30*time.Minute {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Code abgelaufen – bitte die Adresse erneut eintragen."})
		return
	}
	if strings.TrimSpace(req.Code) != sub.Code {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Code stimmt nicht."})
		return
	}
	sub.Confirmed, sub.Code = true, ""
	ms.subs[fid] = sub
	ms.saveSubsLocked()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) mailPrefs(c *gin.Context) {
	ms := s.mailer()
	fid := s.sessionFID(c)
	if ms == nil || fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		Messages *bool `json:"messages"`
		Trades   *bool `json:"trades"`
	}
	_ = c.ShouldBindJSON(&req)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	sub, ok := ms.subs[fid]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Keine Adresse hinterlegt."})
		return
	}
	if req.Messages != nil {
		sub.Messages = *req.Messages
	}
	if req.Trades != nil {
		sub.Trades = *req.Trades
	}
	ms.subs[fid] = sub
	ms.saveSubsLocked()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) mailRemove(c *gin.Context) {
	ms := s.mailer()
	fid := s.sessionFID(c)
	if ms == nil || fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	ms.mu.Lock()
	delete(ms.subs, fid)
	ms.saveSubsLocked()
	ms.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Betreiber: SMTP-Zugang ──────────────────────────────────────────────────

func (s *Server) mailConfigGet(c *gin.Context) {
	ms := s.mailer()
	if ms == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nicht verfügbar"})
		return
	}
	ms.mu.Lock()
	cfg := ms.cfg
	n := len(ms.subs)
	ms.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{ // Passwort wird NIE zurückgegeben
		"enabled": cfg.Enabled, "host": cfg.Host, "port": cfg.Port, "user": cfg.User,
		"from": cfg.From, "tls_mode": cfg.TLSMode, "base_url": cfg.BaseURL,
		"has_password": cfg.Password != "", "subscribers": n,
	})
}

func (s *Server) mailConfigSet(c *gin.Context) {
	ms := s.mailer()
	if ms == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nicht verfügbar"})
		return
	}
	var req struct {
		Enabled  bool   `json:"enabled"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"` // leer = unverändert lassen
		From     string `json:"from"`
		TLSMode  string `json:"tls_mode"`
		BaseURL  string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Enabled && (strings.TrimSpace(req.Host) == "" || !validEmail(req.From)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Server und Absenderadresse werden benötigt."})
		return
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.cfg.Enabled = req.Enabled
	ms.cfg.Host = strings.TrimSpace(req.Host)
	ms.cfg.Port = req.Port
	ms.cfg.User = strings.TrimSpace(req.User)
	ms.cfg.From = strings.TrimSpace(req.From)
	ms.cfg.TLSMode = req.TLSMode
	ms.cfg.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if req.Password != "" {
		ms.cfg.Password = req.Password
	}
	ms.saveCfgLocked()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) mailConfigTest(c *gin.Context) {
	ms := s.mailer()
	if ms == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nicht verfügbar"})
		return
	}
	var req struct {
		To string `json:"to"`
	}
	_ = c.ShouldBindJSON(&req)
	to := strings.TrimSpace(req.To)
	if !validEmail(to) {
		ms.mu.Lock()
		to = ms.cfg.From
		ms.mu.Unlock()
	}
	if err := ms.send(to, "Fundus: Testnachricht", "✓ Der E-Mail-Versand deines Fundus-Nodes funktioniert."); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "to": to})
}

// learnPublicURL merkt sich die öffentliche Adresse beim ersten Zugriff von außen.
func (s *Server) learnPublicURL(c *gin.Context) {
	ms := s.mailer()
	if ms == nil || isLANRequest(c) {
		return
	}
	host := c.Request.Host
	if fh := c.GetHeader("X-Forwarded-Host"); fh != "" {
		host = strings.TrimSpace(strings.Split(fh, ",")[0])
	}
	if i := strings.IndexByte(host, ','); i > 0 {
		host = strings.TrimSpace(host[:i])
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	// Nur echte Namen übernehmen – keine IPs, kein .local, kein localhost.
	if hostname == "" || hostname == "localhost" || strings.HasSuffix(hostname, ".local") ||
		net.ParseIP(hostname) != nil || !strings.Contains(hostname, ".") {
		return
	}
	scheme := "https"
	if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	} else if c.Request.TLS == nil {
		scheme = "http"
	}
	if scheme != "https" {
		return // nur gesicherte Adressen übernehmen
	}
	url := scheme + "://" + host
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.cfg.BaseURL != "" { // vom Betreiber gesetzt oder bereits gelernt
		return
	}
	ms.cfg.BaseURL = url
	ms.saveCfgLocked()
	if ms.log != nil {
		ms.log.Info("Öffentliche Adresse erkannt", zap.String("url", url))
	}
}

package api

// P2P-HTTP-Tunnel: erlaubt den Zugriff auf die Web-Oberfläche eines anderen
// Nodes über die bestehende libp2p-Verbindung — auch wenn dessen IP nicht direkt
// erreichbar ist (z.B. Node im isolierten Gastnetz).
//
// Ablauf:
//   Browser → /api/v1/proxy/<peer-id>/<pfad>  (auf dem LOKALEN Node)
//     → Anfrage serialisieren, per libp2p an den Ziel-Node senden
//       → Ziel-Node reicht sie an seinen lokalen API-Server weiter
//         → Antwort zurück denselben Weg
//
// SICHERHEIT: Die durchgereichte Anfrage trägt den Header X-Fundus-Tunnel. Die
// Admin-Middleware (admin_auth.go) blockiert damit Admin-Zugriffe ohne gültige
// Eigentümer-Session. Öffentliche Seiten (Marktplatz etc.) bleiben frei.

import (
	"bufio"
	"net"
	"net/http/httputil"
	"strconv"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// TunnelProtocol ist das libp2p-Protokoll für den HTTP-Tunnel.
const TunnelProtocol = "/fundus/http-tunnel/1.0.0"

// tunnelRequest ist eine serialisierte HTTP-Anfrage, die durch den Tunnel reist.
type tunnelRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`   // inkl. Query, z.B. "/marktplatz?q=x"
	Header map[string]string `json:"header"` // ausgewählte Header
	Body   []byte            `json:"body"`
}

// tunnelResponse ist die serialisierte HTTP-Antwort.
type tunnelResponse struct {
	Status int               `json:"status"`
	Header map[string]string `json:"header"`
	Body   []byte            `json:"body"`
	Error  string            `json:"error,omitempty"`
}

// registerTunnelHandler registriert den Ziel-seitigen Handler beim Node. Wird
// beim Start aufgerufen (wenn ein P2P-Node vorhanden ist).
func (s *Server) registerTunnelHandler() {
	if s.node == nil {
		return
	}
	// Streaming-Tunnel v2 (ohne Größengrenze, WebSocket-fähig).
	if sn, ok := s.node.(streamNode); ok {
		sn.HandleRawStream(TunnelStreamProtocol, s.handleTunnelStream)
	}
	s.node.RegisterProtocol(TunnelProtocol, func(peerID string, data []byte) []byte {
		return s.handleTunnelRequest(peerID, data)
	})
}

// handleTunnelRequest empfängt eine getunnelte HTTP-Anfrage, reicht sie an den
// eigenen lokalen API-/Webserver weiter und gibt die Antwort serialisiert zurück.
func (s *Server) handleTunnelRequest(peerID string, data []byte) []byte {
	var req tunnelRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return marshalTunnelErr("ungültige Tunnel-Anfrage")
	}

	// Ziel: der eigene lokale Webserver. Der Node bindet seine API auf
	// 127.0.0.1; die Web-UI läuft über OpenResty auf Port 80. Wir reichen an den
	// lokalen HTTP-Endpunkt weiter.
	targetURL := "http://127.0.0.1" + req.Path

	httpReq, err := http.NewRequest(req.Method, targetURL, bytes.NewReader(req.Body))
	if err != nil {
		return marshalTunnelErr("Anfrage bauen: " + err.Error())
	}
	for k, v := range req.Header {
		httpReq.Header.Set(k, v)
	}
	// KRITISCH: Diesen Header setzen, damit die Admin-Middleware Tunnel-Anfragen
	// erkennt und Admin-Zugriffe ohne Session blockiert.
	httpReq.Header.Set("X-Fundus-Tunnel", peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	httpReq = httpReq.WithContext(ctx)

	resp, err := tunnelHTTPClient.Do(httpReq)
	if err != nil {
		return marshalTunnelErr("lokaler Abruf fehlgeschlagen: " + err.Error())
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	tr := tunnelResponse{
		Status: resp.StatusCode,
		Header: map[string]string{},
		Body:   body,
	}
	// Nur relevante Header übertragen.
	for _, h := range []string{"Content-Type", "Set-Cookie", "Location", "Cache-Control", "WWW-Authenticate"} {
		if v := resp.Header.Get(h); v != "" {
			tr.Header[h] = v
		}
	}
	out, _ := json.Marshal(tr)
	return out
}

// tunnelHTTPClient ist der lokale Client für den Ziel-seitigen Weiterleitung.
var tunnelHTTPClient = &http.Client{Timeout: 20 * time.Second}

func marshalTunnelErr(msg string) []byte {
	out, _ := json.Marshal(tunnelResponse{Status: http.StatusBadGateway, Error: msg})
	return out
}

// proxyToPeer ist der anfragende-seitige Endpunkt. Er nimmt Browser-Anfragen
// unter /api/v1/proxy/<peer-id>/<pfad> entgegen und tunnelt sie zum Ziel-Node.
func (s *Server) proxyToPeerLegacy(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	peerID := c.Param("peerid")
	subPath := c.Param("path")
	if subPath == "" {
		subPath = "/"
	}
	if c.Request.URL.RawQuery != "" {
		subPath += "?" + c.Request.URL.RawQuery
	}

	body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 32*1024*1024))
	tReq := tunnelRequest{
		Method: c.Request.Method,
		Path:   subPath,
		Header: map[string]string{},
		Body:   body,
	}
	// Ausgewählte Header weitergeben.
	for _, h := range []string{"Content-Type", "Accept", "Accept-Language"} {
		if v := c.GetHeader(h); v != "" {
			tReq.Header[h] = v
		}
	}
	// Cookies getrennt halten: die LOKALE Session (fundus_session/-admin) geht
	// nicht an den fremden Node; dessen Session liegt im Browser als
	// r_fundus_session und wird hier unter dem Originalnamen weitergegeben.
	if ck := remoteCookieHeader(c.Request); ck != "" {
		tReq.Header["Cookie"] = ck
	}
	// Admin-Passwort (Basic-Auth) NUR weitergeben, wenn der Nutzer den
	// Admin-Zugang für genau diesen Peer freigegeben hat (eigener Node). Sonst
	// könnte ein fremder Node das lokale Admin-Passwort abgreifen, das der
	// Browser ggf. ungefragt mitsendet.
	adminOK := false
	if v, err := c.Cookie(remoteAuthCookie); err == nil && v == peerID {
		adminOK = true
		if a := c.GetHeader("Authorization"); a != "" {
			tReq.Header["Authorization"] = a
		}
	}

	reqData, _ := json.Marshal(tReq)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	// Verbindung während des Remote-Zugriffs aktiv halten (NAT-Zuordnung offen).
	if ka, ok := s.node.(interface{ KeepAlive(string) }); ok {
		ka.KeepAlive(peerID)
	}
	respData, err := s.node.SendAndReceive(ctx, peerID, TunnelProtocol, reqData)
	if err != nil && strings.Contains(err.Error(), "dial") {
		// Einmal nachfassen: kurz abgerissene Verbindung baut sich oft neu auf.
		time.Sleep(1500 * time.Millisecond)
		respData, err = s.node.SendAndReceive(ctx, peerID, TunnelProtocol, reqData)
	}
	if err != nil {
		if strings.Contains(c.GetHeader("Accept"), "text/html") {
			c.Data(http.StatusBadGateway, "text/html; charset=utf-8", tunnelDownPage(err.Error()))
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error":  "Entfernter Node gerade nicht erreichbar – Verbindung unterbrochen. Bitte kurz warten und erneut versuchen.",
			"detail": err.Error(),
		})
		return
	}

	var tResp tunnelResponse
	if err := json.Unmarshal(respData, &tResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ungültige Tunnel-Antwort"})
		return
	}
	if tResp.Error != "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": tResp.Error})
		return
	}

	// Header + Body an den Browser zurückgeben. Links im HTML müssen relativ
	// bleiben oder über denselben Proxy laufen — für einfache Seiten reicht das.
	// Admin-Bereich des entfernten Nodes ohne Freigabe → erklärende Seite statt
	// Passwort-Endlosschleife.
	if tResp.Status == http.StatusUnauthorized && !adminOK {
		c.Data(http.StatusUnauthorized, "text/html; charset=utf-8", remoteAdminHintPage(peerID))
		return
	}
	for k, v := range tResp.Header {
		if k == "WWW-Authenticate" && !adminOK {
			continue
		}
		switch k {
		case "Set-Cookie":
			v = renameRemoteSetCookie(v)
		case "Location":
			v = stripLocalHost(v)
		}
		c.Header(k, v)
	}
	ct := tResp.Header["Content-Type"]
	if ct == "" {
		ct = "application/octet-stream"
	}
	respBody := tResp.Body
	if strings.Contains(ct, "text/html") {
		respBody = injectRemoteBar(respBody, peerID, adminOK)
	}
	c.Data(tResp.Status, ct, respBody)
}

// =============================================================================
//  Remote-Modus: alle Links/Aufrufe über den Tunnel
// =============================================================================
//
// /api/v1/remote/enter/<peer> setzt das Cookie fundus_remote=<peer>. Solange es
// besteht, leiten nginx (API) und app.lua (Seiten) JEDE Anfrage über
// /api/v1/proxy/<peer>/… zum entfernten Node – damit funktionieren alle Links,
// Formulare und fetch()-Aufrufe der fremden Seiten automatisch.
// /api/v1/remote/exit beendet den Modus.

const remoteCookie = "fundus_remote"

// remoteAuthCookie: Admin-Zugang (Basic-Auth-Weitergabe) für genau einen Peer freigegeben.
const remoteAuthCookie = "fundus_remote_auth"

var peerIDRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{40,64}$`)

func (s *Server) remoteEnter(c *gin.Context) {
	pid := c.Param("peerid")
	if !peerIDRe.MatchString(pid) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Peer-ID"})
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: remoteCookie, Value: pid, Path: "/", MaxAge: 8 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	c.Redirect(http.StatusFound, "/")
}

func (s *Server) remoteExit(c *gin.Context) {
	for _, n := range []string{remoteCookie, remoteAuthCookie} {
		http.SetCookie(c.Writer, &http.Cookie{
			Name: n, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
	}
	c.Redirect(http.StatusFound, "/")
}

// remoteAdmin gibt für den aktuell besuchten Peer den Admin-Zugang frei.
func (s *Server) remoteAdmin(c *gin.Context) {
	pid, err := c.Cookie(remoteCookie)
	if err != nil || !peerIDRe.MatchString(pid) {
		c.Redirect(http.StatusFound, "/")
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: remoteAuthCookie, Value: pid, Path: "/", MaxAge: 8 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	next := c.Query("next")
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/files"
	}
	c.Redirect(http.StatusFound, next)
}

// remoteAdminHintPage erklärt, warum der Admin-Bereich eine Freigabe braucht.
func remoteAdminHintPage(peerID string) []byte {
	return []byte(`<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>Admin-Bereich des entfernten Nodes</title><link rel="stylesheet" href="/static/style.css"></head>` +
		`<body style="padding:24px;max-width:640px;margin:auto"><h2>&#128274; Admin-Bereich des entfernten Nodes</h2>` +
		`<p>Dieser Bereich ist auf dem entfernten Node mit dem Admin-Passwort geschützt. Damit du dich dort anmelden kannst, muss dein Browser das Passwort durch den Tunnel an diesen Node senden.</p>` +
		`<p><b>Nur bei eigenen Nodes freigeben.</b> Ein fremder Node könnte sonst dein Admin-Passwort mitlesen – der Browser sendet ein gespeichertes Admin-Passwort unter Umständen automatisch mit.</p>` +
		`<p><a class="btn" href="/api/v1/remote/admin?next=/files">Admin-Zugang für diesen Node freigeben</a> &nbsp; ` +
		`<a href="/api/v1/remote/exit">Zurück zum eigenen Node</a></p></body></html>`)
}

// remoteCookieHeader baut den Cookie-Header für den entfernten Node.
func remoteCookieHeader(r *http.Request) string {
	var parts []string
	for _, ck := range r.Cookies() {
		switch {
		case ck.Name == remoteCookie, ck.Name == remoteAuthCookie, ck.Name == "fundus_session", ck.Name == "fundus_admin":
			continue // lokal, nicht an Fremde
		case strings.HasPrefix(ck.Name, "r_"):
			parts = append(parts, strings.TrimPrefix(ck.Name, "r_")+"="+ck.Value)
		default:
			parts = append(parts, ck.Name+"="+ck.Value) // z.B. TOS, Sprache
		}
	}
	return strings.Join(parts, "; ")
}

// renameRemoteSetCookie legt Sessions des entfernten Nodes unter r_… ab,
// damit sie die lokale Session nicht überschreiben.
func renameRemoteSetCookie(v string) string {
	for _, n := range []string{"fundus_session=", "fundus_admin="} {
		if strings.HasPrefix(v, n) {
			return "r_" + v
		}
	}
	return v
}

// stripLocalHost macht absolute Weiterleitungen des Ziel-Nodes relativ.
func stripLocalHost(v string) string {
	for _, p := range []string{"http://127.0.0.1", "https://127.0.0.1", "http://localhost", "https://localhost"} {
		if strings.HasPrefix(v, p) {
			v = strings.TrimPrefix(v, p)
			if v == "" {
				v = "/"
			}
			return v
		}
	}
	return v
}

// injectRemoteBar blendet einen schwebenden Hinweis mit Rückweg ein.
func injectRemoteBar(body []byte, peerID string, adminOK bool) []byte {
	short := peerID
	if len(short) > 12 {
		short = short[:6] + "…" + short[len(short)-4:]
	}
	adminPart := ` &middot; <a href="/api/v1/remote/admin" style="color:#fff">Admin freigeben</a>`
	if adminOK {
		adminPart = ` &middot; &#128275; Admin`
	}
	bar := `<div id="fundus-remote-bar" style="position:fixed;bottom:14px;left:50%;transform:translateX(-50%);z-index:99999;background:#1f6feb;color:#fff;padding:8px 16px;border-radius:999px;font:14px system-ui,sans-serif;box-shadow:0 4px 18px rgba(0,0,0,.45);white-space:nowrap">` +
		`&#127760; Entfernter Node <b>` + short + `</b>` + adminPart + ` &middot; <a href="/api/v1/remote/exit" style="color:#fff;font-weight:700;text-decoration:underline">Zurück zum eigenen Node</a></div>`
	lower := bytes.ToLower(body)
	i := bytes.Index(lower, []byte("<body"))
	if i < 0 {
		return body
	}
	j := bytes.IndexByte(body[i:], '>')
	if j < 0 {
		return body
	}
	pos := i + j + 1
	out := make([]byte, 0, len(body)+len(bar))
	out = append(out, body[:pos]...)
	out = append(out, bar...)
	out = append(out, body[pos:]...)
	return out
}

// tunnelDownPage: verständliche Seite, wenn der entfernte Node nicht erreichbar ist.
func tunnelDownPage(detail string) []byte {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(detail)
	return []byte(`<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>Entfernter Node nicht erreichbar</title><link rel="stylesheet" href="/static/style.css"></head>` +
		`<body style="padding:24px;max-width:680px;margin:auto"><h2>&#128268; Entfernter Node nicht erreichbar</h2>` +
		`<p>Die Verbindung zum entfernten Node ist gerade unterbrochen, und ein neuer Verbindungsaufbau von hier aus ist gescheitert.</p>` +
		`<p>Meist liegt das daran, dass der Ziel-Node hinter einem Router ohne Portfreigabe sitzt. Dann kann nur <i>er</i> die Verbindung aufbauen – das passiert automatisch, wenn dieser Node bei ihm als Bootstrap-Peer eingetragen ist.</p>` +
		`<p><a class="btn" href="javascript:location.reload()">Erneut versuchen</a> &nbsp; <a href="/api/v1/remote/exit">Zurück zum eigenen Node</a></p>` +
		`<details style="margin-top:16px"><summary>Technische Details</summary><pre style="white-space:pre-wrap;font-size:12px">` + esc + `</pre></details></body></html>`)
}

// =============================================================================
//  Streaming-Tunnel v2
// =============================================================================
//
// v1 (TunnelProtocol) verpackte Anfrage und Antwort komplett als JSON mit
// base64-Body: max. 32 MB, alles im RAM, kein WebSocket. v2 ist eine echte
// Byte-Leitung über einen libp2p-Stream:
//
//   Browser → lokaler Node: httputil.ReverseProxy, dessen Transport statt TCP
//             einen libp2p-Stream zum Ziel-Node öffnet (eine Anfrage pro Stream).
//   Ziel-Node: liest die Anfrage, ersetzt sicherheitsrelevante Header (die
//             Tunnel-Kennung setzt AUSSCHLIESSLICH die Zielseite), schreibt sie an
//             den eigenen nginx (127.0.0.1:80) und reicht danach alle Bytes in
//             beide Richtungen durch – auch WebSocket-Frames nach dem Upgrade.

const TunnelStreamProtocol = "/fundus/http-tunnel/2.0.0"

// streamNode: die Rohdaten-Stream-Fähigkeit des p2p.Node (per Typprüfung, damit
// das P2PNode-Interface unverändert bleibt).
type streamNode interface {
	HandleRawStream(protocol string, h func(peerID string, c net.Conn))
	OpenRawStream(ctx context.Context, peerID, protocol string) (net.Conn, error)
}

// Header, die nie von der anfragenden Seite übernommen werden.
var tunnelStripHeaders = []string{
	"X-Fundus-Tunnel", "X-Fundus-Owner", "X-Real-IP",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded",
}

// handleTunnelStream: Zielseite.
func (s *Server) handleTunnelStream(peerID string, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	br := bufio.NewReaderSize(conn, 64<<10)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if !strings.HasPrefix(req.RequestURI, "/") { // nur Pfade, keine absoluten URLs
		return
	}
	for _, h := range tunnelStripHeaders {
		req.Header.Del(h)
	}
	req.Header.Set("X-Fundus-Tunnel", peerID)
	req.Host = "127.0.0.1"

	up, err := net.DialTimeout("tcp", "127.0.0.1:80", 5*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
		return
	}
	defer up.Close()

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, up) // Antwort (und bei WebSocket alle Frames) zurück
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		close(done)
	}()
	if err := req.Write(up); err != nil {
		return
	}
	_, _ = io.Copy(up, br) // weitere Bytes (WebSocket-Frames) zum Webserver
	if tc, ok := up.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	<-done
}

// proxyToPeer: anfragende Seite (/api/v1/proxy/<peer>/<pfad>).
func (s *Server) proxyToPeer(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	sn, ok := s.node.(streamNode)
	if !ok {
		s.proxyToPeerLegacy(c)
		return
	}
	peerID := c.Param("peerid")
	subPath := c.Param("path")
	if subPath == "" {
		subPath = "/"
	}
	if ka, ok := s.node.(interface{ KeepAlive(string) }); ok {
		ka.KeepAlive(peerID)
	}
	adminOK := false
	if v, err := c.Cookie(remoteAuthCookie); err == nil && v == peerID {
		adminOK = true
	}
	isPage := strings.Contains(c.GetHeader("Accept"), "text/html")
	legacyFallback := false

	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = "fundus-tunnel"
			r.URL.Path = subPath
			r.URL.RawPath = ""
			r.Host = "127.0.0.1"
			ck := remoteCookieHeader(c.Request)
			r.Header.Del("Cookie")
			if ck != "" {
				r.Header.Set("Cookie", ck)
			}
			// Admin-Passwort nur bei ausdrücklicher Freigabe für DIESEN Peer.
			if !adminOK {
				r.Header.Del("Authorization")
			}
			for _, h := range tunnelStripHeaders {
				r.Header.Del(h)
			}
			r.Header["X-Forwarded-For"] = nil // ReverseProxy soll keinen setzen
			// Seiten unkomprimiert holen, damit der Rückweg-Balken eingefügt
			// werden kann (Skripte/Styles/Dateien bleiben komprimierbar).
			if isPage {
				r.Header.Del("Accept-Encoding")
			}
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				conn, err := sn.OpenRawStream(ctx, peerID, TunnelStreamProtocol)
				if err != nil && strings.Contains(err.Error(), "dial") {
					time.Sleep(1500 * time.Millisecond) // kurz abgerissene Verbindung
					conn, err = sn.OpenRawStream(ctx, peerID, TunnelStreamProtocol)
				}
				return conn, err
			},
			DisableKeepAlives:     true, // eine Anfrage pro Stream (Tunnel-Kennung je Anfrage)
			ResponseHeaderTimeout: 90 * time.Second,
		},
		FlushInterval: -1, // sofort durchreichen (Streaming, Server-Sent Events)
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusSwitchingProtocols {
				return nil // WebSocket: nichts anfassen
			}
			if resp.StatusCode == http.StatusUnauthorized && !adminOK {
				body := remoteAdminHintPage(peerID)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(body))
				resp.ContentLength = int64(len(body))
				resp.Header = http.Header{}
				resp.Header.Set("Content-Type", "text/html; charset=utf-8")
				resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
				return nil
			}
			if !adminOK {
				resp.Header.Del("WWW-Authenticate")
			}
			if cks := resp.Header.Values("Set-Cookie"); len(cks) > 0 {
				resp.Header.Del("Set-Cookie")
				for _, v := range cks {
					resp.Header.Add("Set-Cookie", renameRemoteSetCookie(v))
				}
			}
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", stripLocalHost(loc))
			}
			ct := resp.Header.Get("Content-Type")
			if strings.Contains(ct, "text/html") && resp.Header.Get("Content-Encoding") == "" {
				data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
				resp.Body.Close()
				if err != nil {
					return err
				}
				data = injectRemoteBar(data, peerID, adminOK)
				resp.Body = io.NopCloser(bytes.NewReader(data))
				resp.ContentLength = int64(len(data))
				resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			msg := err.Error()
			// Gegenseite kennt v2 noch nicht (älterer Node) → alter Weg.
			if strings.Contains(msg, "protocol") && strings.Contains(msg, "not supported") {
				legacyFallback = true
				return
			}
			if isPage {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write(tunnelDownPage(msg))
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			out, _ := json.Marshal(gin.H{
				"error":  "Entfernter Node gerade nicht erreichbar – Verbindung unterbrochen. Bitte kurz warten und erneut versuchen.",
				"detail": msg,
			})
			_, _ = w.Write(out)
		},
	}
	rp.ServeHTTP(c.Writer, c.Request)
	if legacyFallback && !c.Writer.Written() {
		s.proxyToPeerLegacy(c)
	}
}

package api

// Linkvorschau für den Feed (R814).
//
// Beim ERSTELLEN eines Beitrags holt der Node einmal Titel, Beschreibung und
// Vorschaubild der verlinkten Seite. Das Bild legt er im eigenen Datei-
// Speicher ab, und alles reist im Beitrag mit. Leser berühren die fremde
// Seite dadurch gar nicht – sonst meldete sich jeder Browser bei jedem
// Feed-Aufruf dort an, wie bei eingebetteten YouTube-Playern.
//
// SICHERHEIT: Ein Node, der beliebige Adressen abruft, ließe sich missbrauchen,
// um ins Heimnetz zu schauen (http://10.10.11.25, http://localhost:3000 …).
// Deshalb prüft sicherDialer JEDE Verbindung vor dem Aufbau – auch nach einer
// Weiterleitung – und lehnt private, lokale und reservierte Adressen ab. Die
// Prüfung sitzt beim Verbindungsaufbau statt bei der Adresse, weil ein Name
// erst bei der Abfrage auf eine private Adresse zeigen könnte.

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type feedLink struct {
	URL          string `json:"url"`
	Titel        string `json:"title,omitempty"`
	Beschreibung string `json:"description,omitempty"`
	Seite        string `json:"site,omitempty"`
	Bild         string `json:"image,omitempty"` // Prüfsumme im Datei-Speicher
}

var feedLinkMuster = regexp.MustCompile(`^https?://[^\s"'<>]{3,500}$`)

func (l *feedLink) pruefen() string {
	if l == nil {
		return ""
	}
	l.URL = strings.TrimSpace(l.URL)
	if !feedLinkMuster.MatchString(l.URL) {
		return "Ungültiger Link."
	}
	kuerzen := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if r := []rune(s); len(r) > n {
			return string(r[:n]) + "…"
		}
		return s
	}
	l.Titel, l.Beschreibung, l.Seite = kuerzen(l.Titel, 200), kuerzen(l.Beschreibung, 400), kuerzen(l.Seite, 80)
	if l.Bild != "" && !feedHashMuster.MatchString(l.Bild) {
		return "Ungültiges Vorschaubild."
	}
	return ""
}

// ── Abgesicherter Abruf ──────────────────────────────────────────────────────

var errZielGesperrt = errors.New("Ziel nicht erlaubt (lokales oder privates Netz)")

func ipGesperrt(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		// 100.64.0.0/10 (Carrier-Grade-NAT) und 0.0.0.0/8 zählen nicht zu IsPrivate
		(ip.To4() != nil && (ip.To4()[0] == 0 || (ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64)))
}

func sicherDialer() func(ctx context.Context, netz, adresse string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return func(ctx context.Context, netz, adresse string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(adresse)
		if err != nil {
			return nil, err
		}
		if port != "80" && port != "443" {
			return nil, errZielGesperrt
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if ipGesperrt(ip) {
				continue
			}
			// Mit der GEPRÜFTEN Adresse verbinden, nicht erneut auflösen –
			// sonst könnte die zweite Auflösung anders ausfallen.
			if c, err := d.DialContext(ctx, netz, net.JoinHostPort(ip.String(), port)); err == nil {
				return c, nil
			}
		}
		return nil, errZielGesperrt
	}
}

var feedVorschauClient = &http.Client{
	Timeout: 8 * time.Second,
	Transport: &http.Transport{
		DialContext:           sicherDialer(),
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 6 * time.Second,
		MaxIdleConns:          4,
		Proxy:                 nil, // kein Umweg über einen Proxy, der die Prüfung aushebeln könnte
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("zu viele Weiterleitungen")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errZielGesperrt
		}
		return nil // die Zieladresse prüft der Dialer
	},
}

func feedAbrufen(ctx context.Context, adresse string, maxBytes int64, typPrefix string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adresse, nil)
	if err != nil {
		return nil, "", err
	}
	// Manche Seiten liefern ohne Browserkennung keine Vorschaudaten.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; FundusVorschau/1.0; +https://fnd.resolve.bar)")
	req.Header.Set("Accept-Language", "de,en;q=0.8")
	resp, err := feedVorschauClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", errors.New("Seite antwortet mit " + resp.Status)
	}
	typ := strings.ToLower(resp.Header.Get("Content-Type"))
	if typPrefix != "" && !strings.HasPrefix(typ, typPrefix) {
		return nil, typ, errors.New("unerwarteter Inhaltstyp: " + typ)
	}
	daten, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	return daten, typ, err
}

// ── Auswerten ────────────────────────────────────────────────────────────────

var (
	metaMuster  = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	attrMuster  = regexp.MustCompile(`(?is)([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	titelMuster = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

func feedMetaLesen(seite []byte) map[string]string {
	// Nur der Kopf zählt; danach abschneiden spart Arbeit und Fehlgriffe.
	if i := bytes.Index(bytes.ToLower(seite), []byte("</head>")); i > 0 {
		seite = seite[:i]
	}
	out := map[string]string{}
	for _, tag := range metaMuster.FindAll(seite, 200) {
		a := map[string]string{}
		for _, m := range attrMuster.FindAllSubmatch(tag, -1) {
			wert := string(m[2])
			if len(m[3]) > 0 {
				wert = string(m[3])
			}
			a[strings.ToLower(string(m[1]))] = html.UnescapeString(wert)
		}
		name := a["property"]
		if name == "" {
			name = a["name"]
		}
		name = strings.ToLower(name)
		if name != "" && a["content"] != "" {
			if _, schon := out[name]; !schon {
				out[name] = strings.TrimSpace(a["content"])
			}
		}
	}
	if m := titelMuster.FindSubmatch(seite); m != nil {
		out["<title>"] = strings.TrimSpace(html.UnescapeString(string(m[1])))
	}
	return out
}

func erster(werte ...string) string {
	for _, w := range werte {
		if strings.TrimSpace(w) != "" {
			return w
		}
	}
	return ""
}

var (
	feedVorschauMu     sync.Mutex
	feedVorschauCache  = map[string]feedLink{}
	feedVorschauZeiten = map[string]time.Time{}
)

// feedVorschau: POST /api/v1/feed/preview {"url": "..."}
func (s *Server) feedVorschau(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !feedLinkMuster.MatchString(strings.TrimSpace(req.URL)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Link."})
		return
	}
	adresse := strings.TrimSpace(req.URL)
	if !feedDrosseln("vorschau", strings.ToLower(sess.identity.FundusID), 120, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Vorschauen in kurzer Zeit."})
		return
	}
	feedVorschauMu.Lock()
	if l, ok := feedVorschauCache[adresse]; ok && time.Since(feedVorschauZeiten[adresse]) < time.Hour {
		feedVorschauMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"link": l})
		return
	}
	feedVorschauMu.Unlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	seite, _, err := feedAbrufen(ctx, adresse, 768*1024, "text/html")
	if err != nil {
		// Keine Vorschau ist kein Fehler: Der Link bleibt einfach ein Link.
		c.JSON(http.StatusOK, gin.H{"link": feedLink{URL: adresse}, "hinweis": err.Error()})
		return
	}
	m := feedMetaLesen(seite)
	basis, _ := url.Parse(adresse)
	l := feedLink{
		URL:          adresse,
		Titel:        erster(m["og:title"], m["twitter:title"], m["<title>"]),
		Beschreibung: erster(m["og:description"], m["twitter:description"], m["description"]),
		Seite:        erster(m["og:site_name"], basis.Hostname()),
	}
	if bildURL := erster(m["og:image:secure_url"], m["og:image"], m["twitter:image"], m["twitter:image:src"]); bildURL != "" && s.fileStore != nil {
		if b, err := basis.Parse(bildURL); err == nil && (b.Scheme == "https" || b.Scheme == "http") {
			if daten, typ, err := feedAbrufen(ctx, b.String(), 3*1024*1024, "image/"); err == nil && len(daten) > 0 {
				mime := strings.TrimSpace(strings.SplitN(typ, ";", 2)[0])
				if hash, err := s.fileStore.Upload(ctx, bytes.NewReader(daten), mime, "vorschau"); err == nil {
					l.Bild = hash
				}
			}
		}
	}
	_ = l.pruefen()
	feedVorschauMu.Lock()
	if len(feedVorschauCache) > 500 {
		feedVorschauCache, feedVorschauZeiten = map[string]feedLink{}, map[string]time.Time{}
	}
	feedVorschauCache[adresse], feedVorschauZeiten[adresse] = l, time.Now()
	feedVorschauMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"link": l})
}

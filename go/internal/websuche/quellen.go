package websuche

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Grundadressen – Tests leiten sie auf einen lokalen Server um.
var (
	DDGBasis    = "https://html.duckduckgo.com/html/"
	MojeekBasis = "https://www.mojeek.com/search"
	WikiBasis   = "https://%s.wikipedia.org" // %s = Sprache
)

const maxSeite = 3 << 20

var (
	reTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reLeer  = regexp.MustCompile(`\s+`)
	reHref  = regexp.MustCompile(`(?is)\bhref\s*=\s*"([^"]*)"`)
	reWikiS = regexp.MustCompile(`^[a-z]{2,3}$`)
)

// TextAus: Klartext aus einem HTML-Stück.
func TextAus(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reLeer.ReplaceAllString(s, " "))
}

func kuerzen(t string, n int) string {
	if r := []rune(t); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + " …"
	}
	return t
}

func holen(ctx context.Context, adresse string, kopf map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adresse, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", Agent)
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9,en;q=0.6")
	for k, v := range kopf {
		req.Header.Set(k, v)
	}
	resp, err := Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, errors.New("keine Antwort in der Frist")
		}
		return nil, 0, errors.New("nicht erreichbar")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSeite))
	return b, resp.StatusCode, err
}

// ---------------------------------------------------------------------------
// DuckDuckGo: die HTML-Fassung ohne JavaScript.

type DuckDuckGo struct{}

func (DuckDuckGo) Name() string { return "duckduckgo" }

func (DuckDuckGo) Suchen(ctx context.Context, anfrage, sprache string) ([]Treffer, error) {
	kl := "de-de"
	if sprache == "en" {
		kl = "us-en"
	}
	b, code, err := holen(ctx, DDGBasis+"?"+url.Values{"q": {anfrage}, "kl": {kl}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusForbidden || code == http.StatusTooManyRequests || code == 202 {
		return nil, Sperre{"begrenzt Anfragen (HTTP " + itoa(code) + ")"}
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("antwortet mit %d", code)
	}
	return DDGLesen(string(b))
}

var reDDGBlock = regexp.MustCompile(`(?s)<div[^>]+class="[^"]*\bresult\b[^"]*"`)

// DDGLesen: Treffer aus der HTML-Seite. Werbung (result--ad) entfällt.
func DDGLesen(s string) ([]Treffer, error) {
	idx := reDDGBlock.FindAllStringIndex(s, -1)
	var out []Treffer
	for i, p := range idx {
		ende := len(s)
		if i+1 < len(idx) {
			ende = idx[i+1][0]
		}
		block := s[p[0]:ende]
		kopf := s[p[0]:p[1]]
		if strings.Contains(kopf, "result--ad") {
			continue
		}
		a := stueck(block, `class="result__a"`, "</a>")
		if a == "" {
			continue
		}
		adr := ddgZiel(attrHref(a))
		if adr == "" {
			continue
		}
		titel := TextAus(nachTagEnde(a))
		text := TextAus(nachTagEnde(stueck(block, `class="result__snippet"`, "</a>")))
		out = append(out, Treffer{Titel: titel, URL: adr, Text: kuerzen(text, 400)})
	}
	if len(out) == 0 && (strings.Contains(s, "anomaly-modal") || strings.Contains(s, "challenge-form")) {
		return nil, Sperre{"verlangt eine Bestätigung (zu viele Anfragen)"}
	}
	return out, nil
}

// ddgZiel: DuckDuckGo leitet über //duckduckgo.com/l/?uddg=<Ziel>.
func ddgZiel(h string) string {
	h = html.UnescapeString(h)
	if strings.HasPrefix(h, "//") {
		h = "https:" + h
	}
	u, err := url.Parse(h)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		if z := u.Query().Get("uddg"); z != "" {
			h = z
		} else {
			return "" // eigene Seiten von DuckDuckGo (Werbung, Hilfe)
		}
	}
	if Normieren(h) == "" {
		return ""
	}
	return h
}

// ---------------------------------------------------------------------------
// Mojeek: eigener Index, keine Weitergabe von Anfragen.

type Mojeek struct{}

func (Mojeek) Name() string { return "mojeek" }

func (Mojeek) Suchen(ctx context.Context, anfrage, sprache string) ([]Treffer, error) {
	v := url.Values{"q": {anfrage}}
	if sprache == "de" {
		v.Set("lb", "de") // Sprachvorzug, kein Filter
	}
	b, code, err := holen(ctx, MojeekBasis+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusForbidden || code == http.StatusTooManyRequests {
		// Mojeek lässt automatisierte Abfragen nicht zu (R899) – nicht drängeln.
		return nil, Sperre{"lässt automatisierte Abfragen nicht zu (HTTP " + itoa(code) + ")"}
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("antwortet mit %d", code)
	}
	return MojeekLesen(string(b)), nil
}

var reMojeekLi = regexp.MustCompile(`(?s)<li\b[^>]*>`)

// MojeekLesen: <ul class="results-standard"><li>…<a class="title" href>…<p class="s">.
func MojeekLesen(s string) []Treffer {
	start := strings.Index(s, "results-standard")
	if start < 0 {
		return nil
	}
	s = s[start:]
	if e := strings.Index(s, "</ul>"); e > 0 {
		s = s[:e]
	}
	idx := reMojeekLi.FindAllStringIndex(s, -1)
	var out []Treffer
	for i, p := range idx {
		ende := len(s)
		if i+1 < len(idx) {
			ende = idx[i+1][0]
		}
		block := s[p[0]:ende]
		a := stueck(block, `class="title"`, "</a>")
		if a == "" {
			continue
		}
		adr := html.UnescapeString(attrHref(a))
		if Normieren(adr) == "" {
			continue
		}
		text := TextAus(nachTagEnde(stueck(block, `class="s"`, "</p>")))
		out = append(out, Treffer{Titel: TextAus(nachTagEnde(a)), URL: adr, Text: kuerzen(text, 400)})
	}
	return out
}

// ---------------------------------------------------------------------------
// Wikipedia: offizielle API.

type Wikipedia struct{}

func (Wikipedia) Name() string { return "wikipedia" }

func (Wikipedia) Suchen(ctx context.Context, anfrage, sprache string) ([]Treffer, error) {
	if !reWikiS.MatchString(sprache) {
		sprache = "de"
	}
	basis := fmt.Sprintf(WikiBasis, sprache)
	v := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {anfrage}, "srlimit": {"5"}, "format": {"json"}, "utf8": {"1"}}
	b, code, err := holen(ctx, basis+"/w/api.php?"+v.Encode(), map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("antwortet mit %d", code)
	}
	return WikiLesen(b, basis)
}

func WikiLesen(b []byte, basis string) ([]Treffer, error) {
	var d struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if json.Unmarshal(b, &d) != nil {
		return nil, errors.New("unlesbare Antwort")
	}
	var out []Treffer
	for _, r := range d.Query.Search {
		pfad := url.PathEscape(strings.ReplaceAll(r.Title, " ", "_"))
		out = append(out, Treffer{Titel: r.Title + " – Wikipedia", URL: basis + "/wiki/" + pfad, Text: kuerzen(TextAus(r.Snippet), 400)})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SearXNG im Netz (JSON-Schnittstelle). Zählt doppelt: es fasst selbst
// schon viele Dienste zusammen.

type SearXNG struct{ Basis string }

func (SearXNG) Name() string     { return "searxng" }
func (SearXNG) Gewicht() float64 { return 2 }

func (q SearXNG) Suchen(ctx context.Context, anfrage, sprache string) ([]Treffer, error) {
	v := url.Values{"q": {anfrage}, "format": {"json"}, "language": {sprache}}
	b, code, err := holen(ctx, strings.TrimRight(q.Basis, "/")+"/search?"+v.Encode(), map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	if code == http.StatusForbidden {
		return nil, errors.New("liefert kein JSON – in settings.yml unter search.formats „json“ ergänzen")
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("antwortet mit %d", code)
	}
	var d struct {
		Results []struct {
			Title, URL, Content string
		} `json:"results"`
	}
	if json.Unmarshal(b, &d) != nil {
		return nil, errors.New("unlesbare Antwort")
	}
	var out []Treffer
	for i, r := range d.Results {
		if i >= 20 {
			break
		}
		out = append(out, Treffer{Titel: r.Title, URL: r.URL, Text: kuerzen(TextAus(r.Content), 400)})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Hilfen zum Zerlegen

// stueck: ab dem Tag, das marke enthält, bis einschließlich ende.
func stueck(s, marke, ende string) string {
	i := strings.Index(s, marke)
	if i < 0 {
		return ""
	}
	anf := strings.LastIndex(s[:i], "<")
	if anf < 0 {
		return ""
	}
	j := strings.Index(s[i:], ende)
	if j < 0 {
		return s[anf:]
	}
	return s[anf : i+j]
}

// nachTagEnde: Inhalt nach dem öffnenden Tag.
func nachTagEnde(s string) string {
	if i := strings.Index(s, ">"); i >= 0 {
		return s[i+1:]
	}
	return ""
}

func attrHref(tag string) string {
	if e := strings.Index(tag, ">"); e > 0 {
		tag = tag[:e]
	}
	if m := reHref.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

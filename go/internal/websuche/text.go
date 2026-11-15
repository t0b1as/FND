package websuche

// Text zerlegen und Seiten auslesen (R896).

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var stoppwoerter = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`der die das den dem des ein eine einen einem einer eines und oder aber
		ist sind war waren wird werden wurde wurden hat haben hatte mit von zu zum zur im in an am auf aus bei
		fuer nach ueber unter vor nicht auch als wie so es sie er ich du wir ihr man sich noch nur schon
		dass da wenn dann hier dort mehr sehr kann koennen muss sein seine ihre
		the a an and or of to in on at for from by with as is are was were be been it this that these those
		not but if then than so no yes you we they he she i your our their its into about`) {
		stoppwoerter[w] = true
	}
}

var (
	umlaute        = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	umlauteZurueck = strings.NewReplacer("ae", "ä", "oe", "ö", "ue", "ü")
)

// Woerter: Suchwörter eines Textes – klein, Umlaute ausgeschrieben, ohne
// Stoppwörter, 2 bis 40 Zeichen. "Größe" und "Groesse" ergeben dasselbe Wort.
func Woerter(t string) []string {
	t = umlaute.Replace(strings.ToLower(t))
	var out []string
	var b strings.Builder
	fertig := func() {
		if b.Len() == 0 {
			return
		}
		w := b.String()
		b.Reset()
		if n := len([]rune(w)); n < 2 || n > 40 || stoppwoerter[w] {
			return
		}
		out = append(out, w)
	}
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			fertig()
		}
	}
	fertig()
	return out
}

// Seite: was aus einer HTML-Seite für den Index gebraucht wird.
type Seite struct {
	Titel        string
	Beschreibung string
	Text         string
	Sprache      string
	Links        []string
	NoIndex      bool // <meta name="robots" content="noindex">
	NoFollow     bool
	TDMVorbehalt bool // <meta name="tdm-reservation" content="1">
	Kanonisch    string
}

var (
	reTitel     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reMeta      = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	reAttr      = regexp.MustCompile(`(?is)([a-z\-:]+)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	reLinkTag   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	reA         = regexp.MustCompile(`(?is)<a\b[^>]*>`)
	reHTMLLang  = regexp.MustCompile(`(?is)<html\b[^>]*\blang\s*=\s*["']?([a-zA-Z]{2})`)
	reWeg       = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template|iframe|head)\b.*?</(script|style|noscript|svg|template|iframe|head)\s*>`)
	reKommentar = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBlockEnde = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/section|/article)\b[^>]*>`)
	reNavFuss   = regexp.MustCompile(`(?is)<(nav|footer)\b.*?</(nav|footer)\s*>`)
)

func attribute(tag string) map[string]string {
	m := map[string]string{}
	for _, a := range reAttr.FindAllStringSubmatch(tag, -1) {
		v := a[3]
		if v == "" {
			v = a[4]
		}
		if v == "" {
			v = a[5]
		}
		m[strings.ToLower(a[1])] = html.UnescapeString(v)
	}
	return m
}

// SeiteLesen: Titel, Beschreibung, Fließtext und Links einer Seite.
func SeiteLesen(roh string, basis *url.URL) Seite {
	var s Seite
	if m := reTitel.FindStringSubmatch(roh); m != nil {
		s.Titel = kuerzen(TextAus(m[1]), 200)
	}
	if m := reHTMLLang.FindStringSubmatch(roh); m != nil {
		s.Sprache = strings.ToLower(m[1])
	}
	for _, tag := range reMeta.FindAllString(roh, -1) {
		a := attribute(tag)
		name := strings.ToLower(a["name"] + a["property"])
		inhalt := strings.ToLower(a["content"])
		switch name {
		case "description", "og:description":
			if s.Beschreibung == "" {
				s.Beschreibung = kuerzen(strings.TrimSpace(a["content"]), 400)
			}
		case "robots", "fundusbot":
			s.NoIndex = s.NoIndex || strings.Contains(inhalt, "noindex") || strings.Contains(inhalt, "none")
			s.NoFollow = s.NoFollow || strings.Contains(inhalt, "nofollow") || strings.Contains(inhalt, "none")
		case "tdm-reservation":
			s.TDMVorbehalt = strings.TrimSpace(inhalt) == "1"
		}
	}
	for _, tag := range reLinkTag.FindAllString(roh, -1) {
		a := attribute(tag)
		if strings.EqualFold(a["rel"], "canonical") && basis != nil {
			if u, err := basis.Parse(a["href"]); err == nil {
				s.Kanonisch = u.String()
			}
		}
	}
	koerper := reKommentar.ReplaceAllString(roh, " ")
	koerper = reWeg.ReplaceAllString(koerper, " ")
	if !s.NoFollow && basis != nil {
		gesehen := map[string]bool{}
		for _, tag := range reA.FindAllString(koerper, -1) {
			a := attribute(tag)
			if strings.Contains(strings.ToLower(a["rel"]), "nofollow") {
				continue
			}
			if l := LinkAufloesen(basis, a["href"]); l != "" && !gesehen[l] {
				gesehen[l] = true
				s.Links = append(s.Links, l)
				if len(s.Links) >= 300 {
					break
				}
			}
		}
	}
	koerper = reNavFuss.ReplaceAllString(koerper, " ")
	koerper = reBlockEnde.ReplaceAllString(koerper, "\n")
	s.Text = kuerzen(TextAus(koerper), 20000)
	return s
}

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

// LinkAufloesen: absoluter http(s)-Link ohne Fragment, "" für alles andere.
func LinkAufloesen(basis *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := basis.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
	}
	if u.Path == "" {
		u.Path = "/"
	}
	// Dateien, die keine Seiten sind
	p := strings.ToLower(u.Path)
	for _, e := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".pdf", ".zip", ".gz", ".mp4", ".mp3", ".avi", ".mov", ".exe", ".iso", ".css", ".js", ".woff", ".woff2", ".ico", ".xml", ".json", ".dmg", ".apk", ".bin", ".7z", ".rar", ".tar"} {
		if strings.HasSuffix(p, e) {
			return ""
		}
	}
	return u.String()
}

// Auszug: Textstelle um die gesuchten Wörter (für die Trefferliste).
func Auszug(text string, suchwoerter []string, n int) string {
	if text == "" {
		return ""
	}
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	klein := []rune(strings.ToLower(text))
	if len(klein) != len(r) {
		return kuerzen(text, n)
	}
	beste := -1
	for _, w := range suchwoerter {
		for _, v := range []string{w, umlauteZurueck.Replace(w)} {
			if i := indexRunen(klein, []rune(v)); i >= 0 && (beste < 0 || i < beste) {
				beste = i
			}
		}
	}
	if beste < 0 {
		return kuerzen(text, n)
	}
	anf := beste - n/4
	if anf < 0 {
		anf = 0
	}
	ende := anf + n
	if ende > len(r) {
		ende = len(r)
	}
	out := strings.TrimSpace(string(r[anf:ende]))
	if anf > 0 {
		out = "… " + out
	}
	if ende < len(r) {
		out += " …"
	}
	return out
}

func indexRunen(heu, nadel []rune) int {
	if len(nadel) == 0 {
		return -1
	}
	for i := 0; i+len(nadel) <= len(heu); i++ {
		ok := true
		for j := range nadel {
			if heu[i+j] != nadel[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

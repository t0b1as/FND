package api

// Strenger HTML-Sanitizer für Rich-Text-Beschreibungen (Marktplatz, Partner).
// Erlaubt eine kleine Whitelist sicherer Formatierungs-Tags UND ein streng
// gefiltertes style-Attribut (nur bestimmte CSS-Eigenschaften mit geprüften
// Werten). Entfernt ALLES andere — <script>, Event-Handler, url(), expression(),
// javascript:. Läuft SERVER-seitig, damit ein manipuliertes Frontend keinen
// Schadcode einschleusen kann (der auf anderen Nodes ausgeführt würde).

import (
	"regexp"
	"strings"
)

// erlaubte Formatierungs-Tags. span/div/font erlauben Inline-Styles.
var allowedTags = map[string]bool{
	"b": true, "strong": true,
	"i": true, "em": true,
	"u": true,
	"p": true, "br": true,
	"ul": true, "ol": true, "li": true,
	"span": true, "div": true, "font": true,
}

// erlaubte CSS-Eigenschaften im style-Attribut. Nur harmlose Formatierung,
// KEINE URLs/Positionierung/Verhalten.
var allowedCSSProps = map[string]bool{
	"color":            true,
	"background-color": true,
	"font-size":        true,
	"font-family":      true,
	"font-weight":      true,
	"font-style":       true,
	"text-align":       true,
	"text-decoration":  true,
	"line-height":      true,
	"margin-top":       true,
	"margin-bottom":    true,
	"letter-spacing":   true,
}

var tagRe = regexp.MustCompile(`(?s)<\s*/?\s*([a-zA-Z0-9]+)([^>]*)>`)
var styleAttrRe = regexp.MustCompile(`(?i)style\s*=\s*("([^"]*)"|'([^']*)')`)

var cssDanger = []string{"url(", "expression", "javascript:", "@import", "/*", "*/", "\\", "&#"}

func cleanStyle(style string) string {
	var out []string
	for _, decl := range strings.Split(style, ";") {
		decl = strings.TrimSpace(decl)
		if decl == "" {
			continue
		}
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			continue
		}
		prop := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])
		if !allowedCSSProps[prop] {
			continue
		}
		low := strings.ToLower(val)
		bad := false
		for _, d := range cssDanger {
			if strings.Contains(low, d) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		if len(val) > 100 {
			continue
		}
		out = append(out, prop+":"+val)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, ";")
}

func sanitizeRichText(html string) string {
	if html == "" {
		return ""
	}
	if len(html) > 30000 {
		html = html[:30000]
	}

	for _, danger := range []string{"script", "style", "iframe", "object", "embed", "noscript", "svg"} {
		re := regexp.MustCompile(`(?is)<\s*` + danger + `.*?<\s*/\s*` + danger + `\s*>`)
		html = re.ReplaceAllString(html, "")
		reOpen := regexp.MustCompile(`(?is)<\s*/?\s*` + danger + `[^>]*>`)
		html = reOpen.ReplaceAllString(html, "")
	}

	html = tagRe.ReplaceAllStringFunc(html, func(tag string) string {
		m := tagRe.FindStringSubmatch(tag)
		if len(m) < 3 {
			return ""
		}
		name := strings.ToLower(m[1])
		attrs := m[2]
		if !allowedTags[name] {
			return ""
		}
		if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(tag, "<")), "/") {
			return "</" + name + ">"
		}
		if name == "br" {
			return "<br>"
		}
		style := ""
		if sm := styleAttrRe.FindStringSubmatch(attrs); sm != nil {
			raw := sm[2]
			if raw == "" {
				raw = sm[3]
			}
			style = cleanStyle(raw)
		}
		if style != "" {
			return "<" + name + ` style="` + style + `">`
		}
		return "<" + name + ">"
	})

	return strings.TrimSpace(html)
}

func hasRichMarkup(s string) bool {
	return strings.Contains(s, "<") && tagRe.MatchString(s)
}

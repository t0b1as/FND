package api

// Werkzeugaufrufe im Antworttext (R942).
//
// Ollama liefert Werkzeugaufrufe normalerweise in message.tool_calls. Passt
// die Vorlage eines Modells nicht (häufig bei umgebauten Fassungen wie
// „abliterated“), landet der Aufruf als Text in der Antwort – bei Qwen 3.5 im
// XML-Format <tool_call><function=…><parameter=…>…, bei anderen als JSON. Der
// Chat erkennt diese Formen, führt sie aus und zeigt den Rohtext nicht an.

import (
	"encoding/json"
	"regexp"
	"strings"
)

var kiWerkzeugNamen = map[string]bool{"bild_erzeugen": true, "websuche": true, "seite_lesen": true}

var (
	reToolCallBlock = regexp.MustCompile(`(?s)<tool_call>(.*?)(?:</tool_call>|$)`)
	reXMLFunktion   = regexp.MustCompile(`(?s)<function=([\w\-]+)>(.*?)(?:</function>|$)`)
	reXMLParameter  = regexp.MustCompile(`(?s)<parameter=([\w\-]+)>\s*(.*?)\s*</parameter>`)
	reCodeZaun      = regexp.MustCompile("(?s)^```[a-zA-Z]*\\s*(.*?)\\s*```$")
)

// kiTextAufrufVorsilben: Anfänge, bei denen die Ausgabe zurückgehalten wird,
// bis klar ist, ob ein Aufruf folgt.
var kiTextAufrufVorsilben = []string{"<tool_call>", "<function=", "{", "[{", "```json", "```tool"}

// kiVielleichtTextAufruf: könnte der (getrimmte) Antwortanfang ein Aufruf sein?
func kiVielleichtTextAufruf(t string) bool {
	if t == "" {
		return true
	}
	for _, p := range kiTextAufrufVorsilben {
		if strings.HasPrefix(t, p) || strings.HasPrefix(p, t) {
			return true
		}
	}
	return false
}

func kiAufrufBauen(name string, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	return map[string]any{"function": map[string]any{"name": name, "arguments": args}}
}

// kiJSONAufruf: {"name":…, "arguments"|"parameters":…} oder {"function":{…}}.
func kiJSONAufruf(roh string) []map[string]any {
	roh = strings.TrimSpace(roh)
	if m := reCodeZaun.FindStringSubmatch(roh); m != nil {
		roh = strings.TrimSpace(m[1])
	}
	var liste []map[string]any
	if strings.HasPrefix(roh, "[") {
		_ = json.Unmarshal([]byte(roh), &liste)
	} else {
		var einzeln map[string]any
		if json.Unmarshal([]byte(roh), &einzeln) == nil {
			liste = []map[string]any{einzeln}
		}
	}
	var out []map[string]any
	for _, e := range liste {
		if f, ok := e["function"].(map[string]any); ok {
			e = f
		}
		name, _ := e["name"].(string)
		if !kiWerkzeugNamen[name] {
			continue
		}
		var args map[string]any
		for _, k := range []string{"arguments", "parameters", "args"} {
			switch v := e[k].(type) {
			case map[string]any:
				args = v
			case string:
				_ = json.Unmarshal([]byte(v), &args)
			}
			if args != nil {
				break
			}
		}
		out = append(out, kiAufrufBauen(name, args))
	}
	return out
}

// kiTextAufrufe: alle im Text erkennbaren Werkzeugaufrufe.
func kiTextAufrufe(text string) []map[string]any {
	var out []map[string]any
	bloecke := reToolCallBlock.FindAllStringSubmatch(text, -1)
	if len(bloecke) == 0 && strings.Contains(text, "<function=") {
		bloecke = [][]string{{text, text}}
	}
	for _, b := range bloecke {
		inner := b[1]
		if fs := reXMLFunktion.FindAllStringSubmatch(inner, -1); len(fs) > 0 {
			for _, f := range fs {
				if !kiWerkzeugNamen[f[1]] {
					continue
				}
				args := map[string]any{}
				for _, p := range reXMLParameter.FindAllStringSubmatch(f[2], -1) {
					args[p[1]] = strings.TrimSpace(p[2])
				}
				out = append(out, kiAufrufBauen(f[1], args))
			}
			continue
		}
		out = append(out, kiJSONAufruf(inner)...)
	}
	if len(out) == 0 {
		out = kiJSONAufruf(text)
	}
	return out
}

var kiAufrufMarken = []string{"<tool_call>", "<function="}

// kiSichtbarBis: bis zu welcher Stelle der bisherige Antworttext gefahrlos
// angezeigt werden kann. Am Anfang hält jede mögliche Aufruf-Form zurück
// (auch JSON); mitten im Text nur die XML-Marken – samt einer angefangenen
// Marke am Ende ("<tool_ca").
func kiSichtbarBis(t string) int {
	if kiVielleichtTextAufruf(strings.TrimSpace(t)) {
		return 0
	}
	erste := -1
	for _, m := range kiAufrufMarken {
		if i := strings.Index(t, m); i >= 0 && (erste < 0 || i < erste) {
			erste = i
		}
	}
	if erste >= 0 {
		return erste
	}
	start := len(t) - 12
	if start < 0 {
		start = 0
	}
	for i := start; i < len(t); i++ {
		if t[i] != '<' {
			continue
		}
		for _, m := range kiAufrufMarken {
			if strings.HasPrefix(m, t[i:]) {
				return i
			}
		}
	}
	return len(t)
}

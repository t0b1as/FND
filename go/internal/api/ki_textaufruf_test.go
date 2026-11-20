package api

import (
	"strings"
	"testing"
)

func TestKITextAufrufe(t *testing.T) {
	faelle := []struct {
		name, text, werkzeug, arg, wert string
	}{
		{"Qwen-XML", "<tool_call>\n<function=bild_erzeugen>\n<parameter=beschreibung>\nclassical marble statue, contrapposto\n</parameter>\n<parameter=format>\nhoch\n</parameter>\n</function>\n</tool_call>",
			"bild_erzeugen", "beschreibung", "classical marble statue, contrapposto"},
		{"XML ohne tool_call", "<function=websuche><parameter=anfrage>wetter rodenbach</parameter></function>", "websuche", "anfrage", "wetter rodenbach"},
		{"JSON in tool_call", `<tool_call>{"name": "bild_erzeugen", "arguments": {"beschreibung": "a lighthouse", "format": "quer"}}</tool_call>`,
			"bild_erzeugen", "beschreibung", "a lighthouse"},
		{"blankes JSON", `{"name":"bild_erzeugen","parameters":{"beschreibung":"a cat"}}`, "bild_erzeugen", "beschreibung", "a cat"},
		{"JSON-Codeblock", "```json\n{\"name\":\"bild_erzeugen\",\"arguments\":\"{\\\"beschreibung\\\":\\\"a dog\\\"}\"}\n```", "bild_erzeugen", "beschreibung", "a dog"},
		{"function-Hülle", `[{"function":{"name":"seite_lesen","arguments":{"url":"https://x.de"}}}]`, "seite_lesen", "url", "https://x.de"},
	}
	for _, f := range faelle {
		a := kiTextAufrufe(f.text)
		if len(a) != 1 {
			t.Errorf("%s: %d Aufrufe", f.name, len(a))
			continue
		}
		name, args := kiAufruf(a[0])
		if name != f.werkzeug || args[f.arg] != f.wert {
			t.Errorf("%s: %s %v", f.name, name, args)
		}
	}
	// Kein Aufruf: normaler Text, unbekanntes Werkzeug
	for _, txt := range []string{"Hier ist ein Gedicht über das Meer.", `{"name":"rm_rf","arguments":{}}`, "Code: `x := map[string]int{}`"} {
		if a := kiTextAufrufe(txt); len(a) != 0 {
			t.Errorf("fälschlich erkannt: %q", txt)
		}
	}
}

func TestKISichtbarBis(t *testing.T) {
	for _, f := range []struct {
		text string
		bis  int
	}{
		{"Hallo Welt", 10},
		{"", 0},
		{"<tool", 0},
		{"<tool_call><function=x>", 0},
		{"{\"name\"", 0},
		{"Gerne, ich male das. <tool_call>…", len("Gerne, ich male das. ")},
		{"Gerne. <tool_ca", len("Gerne. ")},
		{"a < b und c > d", len("a < b und c > d")},
		{"```go\nfmt.Println()", len("```go\nfmt.Println()")},
	} {
		if g := kiSichtbarBis(f.text); g != f.bis {
			t.Errorf("%q: %d statt %d", f.text, g, f.bis)
		}
	}
	if !strings.HasPrefix("<function=", "<fun") {
		t.Fatal()
	}
}

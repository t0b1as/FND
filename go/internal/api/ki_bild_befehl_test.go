package api

import "testing"

func TestKIBildBefehl(t *testing.T) {
	for _, f := range []struct{ in, wunsch, format string; ok bool }{
		{"/bild Marmorstatue", "Marmorstatue", "quadrat", true},
		{"  /Bild hoch Statue im Kontrapost", "Statue im Kontrapost", "hoch", true},
		{"/bild: quer Leuchtturm", "Leuchtturm", "quer", true},
		{"/bild", "", "quadrat", true},
		{"/bilder zeigen", "", "", false},
		{"Mal mir ein Bild", "", "", false},
	} {
		w, fm, ok := kiBildBefehl(f.in)
		if ok != f.ok || (ok && (w != f.wunsch || fm != f.format)) {
			t.Errorf("%q → %q %q %v", f.in, w, fm, ok)
		}
	}
	for _, a := range []string{"I'm sorry, but I can't help with that.", "Ich kann keine Nacktbilder erstellen.", "Tut mir leid, das geht nicht."} {
		if !kiIstAblehnung(a) {
			t.Errorf("Ablehnung nicht erkannt: %q", a)
		}
	}
	if kiIstAblehnung("classical marble statue of a nude athlete, contrapposto, museum lighting") {
		t.Error("Prompt fälschlich als Ablehnung erkannt")
	}
}

func TestKIBildGesperrt(t *testing.T) {
	erlaubt := []string{
		"classical marble statue of a nude male athlete, contrapposto",
		"a kind of nude marble statue in the style of Canova",
		"nackte junge Frau als Marmorskulptur, klassisch",
		"nude statue of a 30 year old man, minor damage on the base",
		"children playing in a park, watercolor",
		"Kinder spielen im Garten",
	}
	gesperrt := []string{
		"nude child statue",
		"naked teenager",
		"nacktes Kind",
		"erotic photo of a 15 year old",
		"schoolgirl in lingerie",
		"Mädchen nackt",
	}
	for _, b := range erlaubt {
		if kiBildGesperrt(b) {
			t.Errorf("fälschlich gesperrt: %q", b)
		}
	}
	for _, b := range gesperrt {
		if !kiBildGesperrt(b) {
			t.Errorf("nicht gesperrt: %q", b)
		}
	}
}

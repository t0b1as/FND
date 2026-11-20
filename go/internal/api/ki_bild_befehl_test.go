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

func TestKIIstBildWunsch(t *testing.T) {
	ja := []string{
		"Mal mir eine Marmorstatue eines Athleten",
		"male mir bitte einen Leuchtturm bei Sturm",
		"Bitte zeichne einen Fuchs im Schnee",
		"Kannst du mir eine Statue im Kontrapost malen?",
		"Erstelle ein Bild von einem Segelflugzeug über dem Main",
		"Erzeuge ein Foto einer Bronzeskulptur",
		"draw me a cat in a spacesuit",
		"Please paint a sunset over the Alps",
		"Generate an image of a marble statue",
		"Zeichnest du mir einen Drachen?",
	}
	nein := []string{
		"Kannst du mal erklären, wie ein Wankelmotor funktioniert?",
		"Gib mal ein Beispiel für eine for-Schleife",
		"Wie erstelle ich ein Bild in GIMP?",
		"Was ist eine Statue im Kontrapost?",
		"Erstelle mir eine Einkaufsliste",
		"Wer hat die Mona Lisa gemalt?",
		"How do I create an image in Docker?",
		"Ich male gern in meiner Freizeit.",
	}
	for _, x := range ja {
		if !kiIstBildWunsch(x) {
			t.Errorf("nicht erkannt: %q", x)
		}
	}
	for _, x := range nein {
		if kiIstBildWunsch(x) {
			t.Errorf("fälschlich erkannt: %q", x)
		}
	}
	if kiBildFormatAus("Mal mir eine Statue im Hochformat") != "hoch" || kiBildFormatAus("Leuchtturm, Querformat") != "quer" || kiBildFormatAus("Katze") != "quadrat" {
		t.Error("Format falsch erkannt")
	}
}

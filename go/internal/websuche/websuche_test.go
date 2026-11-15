package websuche

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ddgSeite = `<html><body>
<div class="result results_links results_links_deep result--ad">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://duckduckgo.com/y.js?ad=1">Werbung</a></h2>
</div>
<div class="result results_links results_links_deep web-result ">
 <div class="links_main links_deep result__body">
  <h2 class="result__title">
   <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.raspberrypi.com%2Fdocumentation%2F&amp;rut=abc">Raspberry Pi <b>Dokumentation</b></a>
  </h2>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=x">Offizielle &amp; aktuelle <b>Doku</b>.</a>
 </div>
</div>
<div class="result results_links results_links_deep web-result ">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fde.wikipedia.org%2Fwiki%2FRaspberry_Pi&amp;rut=x">Raspberry Pi – Wikipedia</a></h2>
  <a class="result__snippet" href="#">Einplatinenrechner</a>
</div>
</body></html>`

const mojeekSeite = `<html><ul class="results-standard">
<li class=""><a class="ob" href="https://www.raspberrypi.com/documentation"><p class="i">raspberrypi.com</p></a>
<h2><a class="title" href="https://www.raspberrypi.com/documentation/?utm_source=x">Documentation – Raspberry Pi</a></h2>
<p class="s">The official <strong>documentation</strong>.</p></li>
<li class=""><h2><a class="title" href="https://example.org/pi">Pi &amp; mehr</a></h2><p class="s">Text</p></li>
</ul><ul class="andere"><li><a class="title" href="https://nicht.de">x</a></li></ul></html>`

func TestDDGLesen(t *testing.T) {
	r, err := DDGLesen(ddgSeite)
	if err != nil || len(r) != 2 {
		t.Fatalf("ddg: %v %+v", err, r)
	}
	if r[0].URL != "https://www.raspberrypi.com/documentation/" || r[0].Titel != "Raspberry Pi Dokumentation" || r[0].Text != "Offizielle & aktuelle Doku ." {
		t.Fatalf("ddg 0: %+v", r[0])
	}
	if _, err := DDGLesen(`<div class="anomaly-modal">`); err == nil {
		t.Fatal("Bestätigungsseite nicht erkannt")
	}
}

func TestMojeekLesen(t *testing.T) {
	r := MojeekLesen(mojeekSeite)
	if len(r) != 2 || r[1].Titel != "Pi & mehr" || r[0].Text != "The official documentation ." {
		t.Fatalf("mojeek: %+v", r)
	}
}

func TestWikiLesen(t *testing.T) {
	r, err := WikiLesen([]byte(`{"query":{"search":[{"title":"Raspberry Pi","snippet":"<span class=\"searchmatch\">Raspberry</span> Pi ist"}]}}`), "https://de.wikipedia.org")
	if err != nil || len(r) != 1 || r[0].URL != "https://de.wikipedia.org/wiki/Raspberry_Pi" || r[0].Text != "Raspberry Pi ist" {
		t.Fatalf("wiki: %v %+v", err, r)
	}
}

func TestNormieren(t *testing.T) {
	a := Normieren("https://www.raspberrypi.com/documentation/?utm_source=x")
	b := Normieren("http://raspberrypi.com/documentation")
	if a == "" || a != b {
		t.Fatalf("%q != %q", a, b)
	}
	if Normieren("javascript:alert(1)") != "" || Normieren("/relativ") != "" {
		t.Fatal("Unsinn akzeptiert")
	}
}

type fest struct {
	n string
	t []Treffer
	e error
	w time.Duration
}

func (f fest) Name() string { return f.n }
func (f fest) Suchen(ctx context.Context, q, s string) ([]Treffer, error) {
	select {
	case <-time.After(f.w):
	case <-ctx.Done():
		return nil, errors.New("keine Antwort in der Frist")
	}
	return f.t, f.e
}

func TestMetasucheZusammenfuehren(t *testing.T) {
	CacheLeeren()
	a := fest{n: "a", t: []Treffer{{URL: "https://x.de/1", Titel: "X1"}, {URL: "https://y.de/", Titel: "Y"}}}
	b := fest{n: "b", t: []Treffer{{URL: "https://www.y.de", Titel: "Y", Text: "länger"}, {URL: "https://z.de", Titel: "Z"}}}
	c := fest{n: "c", e: errors.New("kaputt")}
	d := fest{n: "d", w: time.Second}
	e, err := Metasuche(context.Background(), "  pi   test ", "de", []Quelle{a, b, c, d}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if e.Anfrage != "pi test" || len(e.Treffer) != 3 || e.Treffer[0].Titel != "Y" || len(e.Treffer[0].Quellen) != 2 || e.Treffer[0].Text != "länger" {
		t.Fatalf("%+v", e)
	}
	if e.Dienste["c"] != "kaputt" || !strings.Contains(e.Dienste["d"], "Frist") || e.Dienste["a"] != "ok (2)" {
		t.Fatalf("dienste %+v", e.Dienste)
	}
	e2, _ := Metasuche(context.Background(), "PI TEST", "de", []Quelle{a, b, c, d}, time.Second)
	if !e2.Cache {
		t.Fatal("Cache nicht genutzt")
	}
	if _, err := Metasuche(context.Background(), "  ", "de", nil, time.Second); err != ErrLeer {
		t.Fatal("leere Anfrage")
	}
}

func TestQuellenGegenServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/ddg"):
			if r.URL.Query().Get("q") != "pi" {
				w.WriteHeader(400)
			}
			w.Write([]byte(ddgSeite))
		case strings.HasPrefix(r.URL.Path, "/mojeek"):
			w.Write([]byte(mojeekSeite))
		case strings.HasSuffix(r.URL.Path, "/w/api.php"):
			w.Write([]byte(`{"query":{"search":[{"title":"Pi","snippet":"x"}]}}`))
		case r.URL.Path == "/search":
			w.Write([]byte(`{"results":[{"title":"S","url":"https://s.de","content":"c"}]}`))
		}
	}))
	defer srv.Close()
	DDGBasis, MojeekBasis, WikiBasis = srv.URL+"/ddg", srv.URL+"/mojeek", srv.URL+"/%s"
	CacheLeeren()
	e, err := Metasuche(context.Background(), "pi", "de", []Quelle{DuckDuckGo{}, Mojeek{}, Wikipedia{}, SearXNG{Basis: srv.URL}}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"duckduckgo", "mojeek", "wikipedia", "searxng"} {
		if !strings.HasPrefix(e.Dienste[n], "ok") {
			t.Fatalf("%s: %s", n, e.Dienste[n])
		}
	}
	// raspberrypi.com/documentation von DDG und Mojeek → ein Treffer, oben
	gefunden := false
	for _, x := range e.Treffer {
		if strings.Contains(x.URL, "raspberrypi.com/documentation") {
			gefunden = len(x.Quellen) == 2 && len(e.Treffer) > 0 && x.Punkte >= e.Treffer[0].Punkte-1e-12
		}
	}
	if !gefunden {
		t.Fatalf("zusammenführen: %+v", e.Treffer)
	}
}

func TestOrt(t *testing.T) {
	ok := []Ort{{Modus: "lokal"}, {Modus: "laufwerk", Pfad: "/mnt/usb1"}, {Modus: "laufwerk", Pfad: "/media/root/My Passport/"},
		{Modus: "knoten", Knoten: "12D3KooWFvYsuvWEpyAVZntVJ1wnVAhxQxYurNSesjpWVz4KbuWC"}}
	for _, o := range ok {
		if err := o.Pruefen(); err != nil {
			t.Fatalf("%+v: %v", o, err)
		}
	}
	schlecht := []Ort{{Modus: "laufwerk", Pfad: "/etc"}, {Modus: "laufwerk", Pfad: "/mnt"}, {Modus: "laufwerk", Pfad: "/mnt/../etc"},
		{Modus: "laufwerk", Pfad: "mnt/x"}, {Modus: "knoten", Knoten: "abc"}, {Modus: "irgendwo"}}
	for _, o := range schlecht {
		if o.Pruefen() == nil {
			t.Fatalf("angenommen: %+v", o)
		}
	}
	if (Ort{Modus: "laufwerk", Pfad: "/mnt/usb1/"}).Ordner("/d") != "/mnt/usb1/fundus-suchindex" {
		t.Fatal("Ordner")
	}
	if (Ort{}).Ordner("/d") != "/d/suchindex" || (Ort{Modus: "knoten"}).Ordner("/d") != "" {
		t.Fatal("Ordner lokal/knoten")
	}
}

func TestPlatzUndAblage(t *testing.T) {
	d := t.TempDir()
	pl := PlatzPruefen(Ort{Modus: "lokal"}, d)
	if !pl.Schreibbar || pl.FreiGB <= 0 {
		t.Fatalf("%+v", pl)
	}
	// Laufwerk, das nicht eingehängt ist → Fehler, nichts angelegt
	pl = PlatzPruefen(Ort{Modus: "laufwerk", Pfad: "/mnt/gibtsnicht-fundus"}, d)
	if pl.Fehler == "" {
		t.Fatal("fehlendes Laufwerk angenommen")
	}
	if _, err := os.Stat("/mnt/gibtsnicht-fundus"); err == nil {
		t.Fatal("Ordner angelegt")
	}
	a, err := Oeffnen(filepath.Join(d, "suchindex"), "knoten1")
	if err != nil {
		t.Fatal(err)
	}
	i := a.Info()
	if !i.Bereit || i.Node != "knoten1" {
		t.Fatalf("%+v", i)
	}
	a2, err := Oeffnen(filepath.Join(d, "suchindex"), "anderer")
	if err != nil || a2.Info().Node != "knoten1" {
		t.Fatal("Kennung nicht übernommen")
	}
}

func TestLaufwerke(t *testing.T) {
	d := t.TempDir()
	f := filepath.Join(d, "mounts")
	os.WriteFile(f, []byte("/dev/root / ext4 rw 0 0\n/dev/sda1 /mnt/usb1 ext4 rw 0 0\n/dev/sdb1 /media/root/My\\040Passport ntfs3 rw 0 0\nhttp://x /mnt/dav davfs rw 0 0\n"), 0o644)
	MountsDatei = f
	l := Laufwerke()
	if len(l) != 2 || l[1].Pfad != "/media/root/My Passport" {
		t.Fatalf("%+v", l)
	}
}

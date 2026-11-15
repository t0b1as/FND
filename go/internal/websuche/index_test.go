package websuche

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWoerter(t *testing.T) {
	w := Woerter("Die Größe der Grösse – GROESSE, Pi 5 und x")
	if strings.Join(w, " ") != "groesse groesse groesse pi" {
		t.Fatalf("%v", w)
	}
}

func TestSeiteLesen(t *testing.T) {
	b, _ := url.Parse("https://beispiel.de/a/b.html")
	s := SeiteLesen(`<html lang="de"><head><title>Titel &amp; mehr</title>
<meta name="description" content="Kurz">
<meta name="robots" content="index, nofollow">
<link rel="canonical" href="/a/b">
<script>var x="<a href='/geheim'>"</script></head><body><nav><a href="/nav">Nav</a></nav>
<p>Hallo <b>Welt</b></p><a href="../c.html#x">c</a><a href="mailto:x@y">m</a><a href="bild.jpg">b</a></body></html>`, b)
	if s.Titel != "Titel & mehr" || s.Beschreibung != "Kurz" || s.Sprache != "de" || !s.NoFollow || s.NoIndex || s.Kanonisch != "https://beispiel.de/a/b" {
		t.Fatalf("%+v", s)
	}
	if len(s.Links) != 0 { // nofollow
		t.Fatalf("links %v", s.Links)
	}
	if !strings.Contains(s.Text, "Hallo Welt") || strings.Contains(s.Text, "Nav") || strings.Contains(s.Text, "geheim") {
		t.Fatalf("text %q", s.Text)
	}
	s = SeiteLesen(`<a href="../c.html#x">c</a><a href="mailto:x@y">m</a><a href="bild.jpg">b</a><a rel="nofollow" href="/n">n</a><a href="http://Beispiel.de:80/d">d</a>`, b)
	if strings.Join(s.Links, " ") != "https://beispiel.de/c.html http://beispiel.de/d" {
		t.Fatalf("links %v", s.Links)
	}
}

func TestRobots(t *testing.T) {
	r := RobotsLesen(`
User-agent: *
Disallow: /privat/
Allow: /privat/offen
Disallow: /*.php$
Crawl-delay: 3

User-agent: GoogleBot
Disallow: /
`)
	for p, w := range map[string]bool{"/": true, "/privat/x": false, "/privat/offen/y": true, "/a.php": false, "/a.php?x=1": true} {
		if r.Erlaubt(p) != w {
			t.Fatalf("%s: %v", p, !w)
		}
	}
	if r.Verzoegerung != 3*time.Second {
		t.Fatal("delay")
	}
	r = RobotsLesen("User-agent: FundusBot\nDisallow: /\n\nUser-agent: *\nDisallow:\n")
	if r.Erlaubt("/x") {
		t.Fatal("FundusBot-Gruppe nicht beachtet")
	}
	if !RobotsLesen("User-agent: *\nDisallow:\n").Erlaubt("/x") {
		t.Fatal("leeres Disallow")
	}
	td := TDMRepLesen([]byte(`[{"location":"/frei/*","tdm-reservation":0},{"location":"/*","tdm-reservation":1}]`))
	if td.Vorbehalt("/frei/a") || !td.Vorbehalt("/sonst") {
		t.Fatal("tdmrep")
	}
}

func TestIndex(t *testing.T) {
	d := t.TempDir()
	ix, err := IndexOeffnen(d)
	if err != nil {
		t.Fatal(err)
	}
	ix.Hinzufuegen(Dokument{URL: "https://a.de/pi", Titel: "Raspberry Pi Kühlung", Text: "Ein Lüfter für den Raspberry Pi hält die Temperatur niedrig."})
	ix.Hinzufuegen(Dokument{URL: "https://b.de/kaffee", Titel: "Kaffee", Text: "Espresso und Kaffee. Kein Pi hier, nur ein Wort Raspberry am Rand."})
	for i := 0; i < 900; i++ { // zwei Abschnitte, Puffer, später Zusammenlegen
		ix.Hinzufuegen(Dokument{URL: fmt.Sprintf("https://c.de/%d", i), Titel: fmt.Sprintf("Seite %d", i), Text: "Füllwort Füllwort anderes Thema Nummer " + fmt.Sprint(i)})
	}
	tr, err := ix.Suchen(context.Background(), "raspberry pi lüfter", 5)
	if err != nil || len(tr) < 2 || tr[0].URL != "https://a.de/pi" {
		t.Fatalf("%v %+v", err, tr)
	}
	if !strings.Contains(tr[0].Text, "Lüfter") {
		t.Fatalf("Auszug %q", tr[0].Text)
	}
	// Ersetzen: neue Fassung, alte fällt weg
	ix.Hinzufuegen(Dokument{URL: "https://www.a.de/pi/", Titel: "Neu", Text: "Ganz anderes über Gärten"})
	if tr, _ := ix.Suchen(context.Background(), "lüfter", 5); len(tr) != 0 {
		t.Fatalf("alte Fassung gefunden: %+v", tr)
	}
	if tr, _ := ix.Suchen(context.Background(), "gaerten", 5); len(tr) != 1 {
		t.Fatalf("Umlaut-Suche: %+v", tr)
	}
	ix.Loeschen("https://b.de/kaffee")
	if n, _ := ix.Stand(); n != 901 {
		t.Fatalf("stand %d", n)
	}
	if err := ix.Schliessen(); err != nil {
		t.Fatal(err)
	}
	ix, err = IndexOeffnen(d)
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := ix.Suchen(context.Background(), "nummer 777", 3); len(tr) == 0 || tr[0].URL != "https://c.de/777" {
		t.Fatalf("nach Neustart: %+v", tr)
	}
	if tr, _ := ix.Suchen(context.Background(), "kaffee", 3); len(tr) != 0 {
		t.Fatal("gelöschte Seite gefunden")
	}
	// viele Abschnitte → zusammenlegen
	for i := 0; i < 9*puffDokumente; i++ {
		ix.Hinzufuegen(Dokument{URL: fmt.Sprintf("https://d.de/%d", i), Titel: "x", Text: "massenware"})
	}
	if _, ab := ix.Stand(); ab > maxAbschnitte {
		t.Fatalf("nicht zusammengelegt: %d", ab)
	}
	if tr, _ := ix.Suchen(context.Background(), "nummer 777", 3); len(tr) == 0 || tr[0].URL != "https://c.de/777" {
		t.Fatalf("nach Zusammenlegen: %+v", tr)
	}
	ix.Schliessen()
}

func TestIndexAbsturz(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	ix.Hinzufuegen(Dokument{URL: "https://a.de/1", Titel: "eins", Text: "gesichert"})
	ix.Schreiben()
	ix.Hinzufuegen(Dokument{URL: "https://a.de/2", Titel: "zwei", Text: "verloren"})
	// Absturz: Dateien nicht geschlossen, Puffer nicht geschrieben
	f, _ := os.OpenFile(filepath.Join(d, "url.idx"), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte{1, 2, 3}) // halber Eintrag
	f.Close()
	ix2, err := IndexOeffnen(d)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := ix2.Stand(); n != 1 {
		t.Fatalf("stand %d", n)
	}
	if hat, _ := ix2.Hat("https://a.de/2"); hat {
		t.Fatal("ungesicherte Seite gilt als vorhanden – würde nie neu gelesen")
	}
	if tr, _ := ix2.Suchen(context.Background(), "gesichert", 3); len(tr) != 1 {
		t.Fatal("gesicherte Seite fehlt")
	}
}

func TestUmziehen(t *testing.T) {
	d := t.TempDir()
	von, nach := filepath.Join(d, "a"), filepath.Join(d, "b", "fundus-suchindex")
	ab, err := Oeffnen(von, "n1")
	if err != nil {
		t.Fatal(err)
	}
	ab.Index.Hinzufuegen(Dokument{URL: "https://x.de", Titel: "umzug", Text: "kiste"})
	ab.Schliessen()
	os.MkdirAll(filepath.Dir(nach), 0o755)
	var zuletzt int64
	if err := Umziehen(von, nach, func(k, g int64) { zuletzt = k }); err != nil {
		t.Fatal(err)
	}
	if zuletzt == 0 {
		t.Fatal("kein Fortschritt")
	}
	if _, err := os.Stat(filepath.Join(von, "ablage.json")); err == nil {
		t.Fatal("alter Index noch da")
	}
	ab2, err := Oeffnen(nach, "n2")
	if err != nil || ab2.Info().Node != "n1" {
		t.Fatal("Kennung")
	}
	if tr, _ := ab2.Suchen(context.Background(), "kiste", 3); len(tr) != 1 {
		t.Fatal("Index nach Umzug leer")
	}
	ab2.Schliessen()
}

func TestCrawler(t *testing.T) {
	var srv *httptest.Server
	aufrufe := map[string]int{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aufrufe[r.URL.Path]++
		html := func(s string) { w.Header().Set("Content-Type", "text/html; charset=utf-8"); w.Write([]byte(s)) }
		switch r.URL.Path {
		case "/robots.txt":
			w.Write([]byte("User-agent: *\nDisallow: /verboten\nCrawl-delay: 0.01\n"))
		case "/.well-known/tdmrep.json":
			w.Write([]byte(`[{"location":"/tdm/*","tdm-reservation":1}]`))
		case "/":
			html(`<title>Start</title><p>Willkommen auf der Startseite mit genug Text für den Index hier.</p>
<a href="/a">a</a><a href="/verboten">v</a><a href="/tdm/x">t</a><a href="/noindex">n</a><a href="/bild.png">b</a><a href="https://anderswo.example/">f</a>`)
		case "/a":
			html(`<title>Seite A</title><p>Zahnrad Getriebe Übersetzung – ausführlicher Text über Getriebe.</p><a href="/b">b</a>`)
		case "/b":
			html(`<title>Seite B</title><p>Tiefe zwei, Text über Kugellager.</p><a href="/c">c</a>`)
		case "/c":
			html(`<title>Seite C</title><p>Tiefe drei – zu tief bei Tiefe 2.</p>`)
		case "/noindex":
			html(`<meta name="robots" content="noindex"><title>N</title><p>nicht aufnehmen bitte sehr gerne</p>`)
		case "/tdm/x", "/verboten":
			html(`<title>X</title><p>darf nie gelesen werden, steht unter Vorbehalt</p>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	d := t.TempDir()
	ab, _ := Oeffnen(d, "n")
	c := NeuerCrawler(ab.Index, d, CrawlerEinstellungen{Startseiten: []string{srv.URL + "/"}, Tiefe: 2, MBProStunde: 50})
	c.ErlaubeLokal = true
	c.Starten()
	frist := time.Now().Add(20 * time.Second)
	for time.Now().Before(frist) {
		st := c.Stand()
		if st.Warteschlange == 0 && st.Gelesen >= 4 && st.Pause != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.Anhalten()
	st := c.Stand()
	if aufrufe["/verboten"] > 0 || aufrufe["/tdm/x"] > 0 || aufrufe["/c"] > 0 {
		t.Fatalf("verbotene/zu tiefe Seiten gelesen: %v", aufrufe)
	}
	if st.Aufgenommen != 3 {
		t.Fatalf("aufgenommen %d (%+v) %v", st.Aufgenommen, st, aufrufe)
	}
	if tr, _ := ab.Suchen(context.Background(), "getriebe", 3); len(tr) != 1 || !strings.HasSuffix(tr[0].URL, "/a") {
		t.Fatalf("%+v", tr)
	}
	if tr, _ := ab.Suchen(context.Background(), "aufnehmen", 3); len(tr) != 0 {
		t.Fatal("noindex aufgenommen")
	}
	// Neustart: frische Seiten werden nicht erneut geholt
	vorher := aufrufe["/a"]
	c2 := NeuerCrawler(ab.Index, d, CrawlerEinstellungen{Startseiten: []string{srv.URL + "/a"}, Tiefe: 2})
	c2.ErlaubeLokal = true
	c2.Starten()
	time.Sleep(600 * time.Millisecond)
	c2.Anhalten()
	if aufrufe["/a"] != vorher {
		t.Fatal("frische Seite neu geholt")
	}
	ab.Schliessen()
}

func TestOeffentlich(t *testing.T) {
	for ip, w := range map[string]bool{"8.8.8.8": true, "10.10.11.25": false, "192.168.1.2": false, "127.0.0.1": false, "100.64.1.1": false, "169.254.1.1": false, "::1": false, "fdcf::1": false, "2a02:e4c0::1": true} {
		if oeffentlich(parseIP(ip)) != w {
			t.Fatalf("%s", ip)
		}
	}
}

func parseIP(s string) net.IP { return net.ParseIP(s) }

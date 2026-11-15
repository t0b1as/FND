package websuche

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestZustaendige(t *testing.T) {
	m := []string{"p1", "p2", "p3", "p4", "p5"}
	z := Zustaendige("www.Heise.de:443", m, 2)
	if len(z) != 2 || z[0] == z[1] {
		t.Fatal(z)
	}
	// gleiche Zuteilung, egal in welcher Reihenfolge die Liste kommt
	if z2 := Zustaendige("heise.de", []string{"p5", "p3", "p1", "p4", "p2", "p2"}, 2); z2[0] != z[0] || z2[1] != z[1] {
		t.Fatal("Reihenfolge ändert Zuteilung")
	}
	// Verteilung und Stabilität: ein Pi kommt hinzu → nur ~1/6 der Hosts wechselt
	zaehl := map[string]int{}
	gewechselt := 0
	for i := 0; i < 6000; i++ {
		h := fmt.Sprintf("host%d.de", i)
		a := Zustaendige(h, m, 1)[0]
		zaehl[a]++
		if Zustaendige(h, append(m, "p6"), 1)[0] != a {
			gewechselt++
		}
	}
	for p, n := range zaehl {
		if n < 1000 || n > 1400 {
			t.Fatalf("ungleich verteilt: %s %d", p, n)
		}
	}
	if gewechselt < 800 || gewechselt > 1200 {
		t.Fatalf("zu viele Wechsel: %d", gewechselt)
	}
	if len(Zustaendige("x", nil, 2)) != 0 || len(Zustaendige("x", []string{"a"}, 2)) != 1 {
		t.Fatal("Randfälle")
	}
}

func TestZusammenfuehren(t *testing.T) {
	a := []Treffer{{URL: "https://a.de/1", Punkte: 3}, {URL: "https://b.de", Punkte: 1}}
	b := []Treffer{{URL: "https://www.a.de/1/", Punkte: 5, Titel: "neu"}, {URL: "https://c.de", Punkte: 2}}
	z := Zusammenfuehren([][]Treffer{a, b}, 2)
	if len(z) != 2 || z[0].Titel != "neu" || z[1].URL != "https://c.de" {
		t.Fatalf("%+v", z)
	}
}

func TestCrawlerVerbund(t *testing.T) {
	d := t.TempDir()
	ab, _ := Oeffnen(d, "n")
	defer ab.Schliessen()
	c := NeuerCrawler(ab.Index, d, CrawlerEinstellungen{Tiefe: 3})
	var abgegeben []string
	c.Zustaendig = func(h string) bool { return VerbundHost(h) == "hier.de" }
	c.Abgeben = func(u string, tiefe int) { abgegeben = append(abgegeben, fmt.Sprintf("%s@%d", u, tiefe)) }
	c.Vormerken("https://fremd.de/x")
	c.Vormerken("https://www.hier.de/y")
	c.mu.Lock()
	for i := 0; i < 3; i++ {
		c.naechsterGesperrt()
	}
	n := len(c.schlange)
	c.mu.Unlock()
	if len(abgegeben) != 1 || abgegeben[0] != "https://fremd.de/x@0" || n != 0 {
		t.Fatalf("%v %d", abgegeben, n)
	}
	if c.VormerkenTiefe("https://hier.de/z", 9) {
		t.Fatal("zu tief angenommen")
	}
}

// Drei Pis, drei Websites: jede Website liest genau ein Pi, jede Seite liegt
// auf zwei Pis, die Suche über alle findet alles.
func TestVerbundSimuliert(t *testing.T) {
	type pi struct {
		id      string
		ab      *Ablage
		c       *Crawler
		gelesen map[string]int
	}
	var server []*httptest.Server
	zugriffe := map[string]map[string]int{} // Host → Pfad → Anzahl
	var zmu sync.Mutex
	for s := 0; s < 3; s++ {
		s := s
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			zmu.Lock()
			if zugriffe[r.Host] == nil {
				zugriffe[r.Host] = map[string]int{}
			}
			zugriffe[r.Host][r.URL.Path]++
			zmu.Unlock()
			if r.URL.Path == "/robots.txt" || r.URL.Path == "/.well-known/tdmrep.json" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			links := ""
			for o := range server {
				if o != s {
					links += `<a href="` + server[o].URL + `/seite1">x</a>`
				}
			}
			fmt.Fprintf(w, `<title>Website %d %s</title><p>Inhalt der Website nummer%d auf Seite %s mit genug Text.</p><a href="/seite1">1</a><a href="/seite2">2</a>%s`, s, r.URL.Path, s, r.URL.Path, links)
		}))
		server = append(server, srv)
	}
	defer func() {
		for _, s := range server {
			s.Close()
		}
	}()
	ids := []string{"pi-a", "pi-b", "pi-c"}
	pis := map[string]*pi{}
	var nmu sync.Mutex // „Netz“: Rückrufe laufen unter der Sperre des Senders
	type nachricht struct {
		an    string
		url   string
		tiefe int
		dok   *Dokument
	}
	postfach := make(chan nachricht, 10000)
	for _, id := range ids {
		d := t.TempDir()
		ab, _ := Oeffnen(d, id)
		p := &pi{id: id, ab: ab}
		p.c = NeuerCrawler(ab.Index, d, CrawlerEinstellungen{Tiefe: 3, FremdeHosts: true})
		p.c.ErlaubeLokal = true
		id := id
		p.c.Zustaendig = func(h string) bool { return Zustaendige(h, ids, 1)[0] == id }
		p.c.Abgeben = func(u string, tiefe int) {
			pu, _ := url.Parse(u)
			postfach <- nachricht{an: Zustaendige(pu.Host, ids, 1)[0], url: u, tiefe: tiefe}
		}
		p.c.Kopie = func(d Dokument) {
			pu, _ := url.Parse(d.URL)
			postfach <- nachricht{an: Zustaendige(pu.Host, ids, 2)[1], dok: &d}
		}
		pis[id] = p
	}
	stopp := make(chan bool)
	go func() {
		for {
			select {
			case <-stopp:
				return
			case n := <-postfach:
				nmu.Lock()
				if n.dok != nil {
					pis[n.an].ab.Index.Hinzufuegen(*n.dok)
				} else {
					pis[n.an].c.VormerkenTiefe(n.url, n.tiefe)
				}
				nmu.Unlock()
			}
		}
	}()
	// Alle Startseiten nur bei pi-a eintragen – Fremdes muss weitergereicht werden.
	for _, p := range pis {
		p.c.Starten()
	}
	for _, s := range server {
		pis["pi-a"].c.Vormerken(s.URL + "/")
	}
	frist := time.Now().Add(25 * time.Second)
	for time.Now().Before(frist) {
		fertig := true
		for _, p := range pis {
			if st := p.c.Stand(); st.Warteschlange > 0 || st.Aufgenommen == 0 {
				fertig = false
			}
		}
		if fertig && len(postfach) == 0 {
			time.Sleep(1500 * time.Millisecond)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, p := range pis {
		p.c.Anhalten()
	}
	close(stopp)
	// 1. Jede Seite höchstens einmal gelesen (je Website ein Pi)
	for h, pf := range zugriffe {
		for pfad, n := range pf {
			if n > 1 {
				t.Fatalf("%s%s %d-mal gelesen", h, pfad, n)
			}
		}
	}
	// 2. Jede Seite auf genau zwei Pis
	for _, s := range server {
		for _, pfad := range []string{"/", "/seite1", "/seite2"} {
			n := 0
			for _, p := range pis {
				if hat, _ := p.ab.Index.Hat(s.URL + pfad); hat {
					n++
				}
			}
			if n != 2 {
				t.Fatalf("%s%s liegt auf %d Pis", s.URL, pfad, n)
			}
		}
	}
	// 3. Suche über alle findet jede Website, ohne Doppel
	var listen [][]Treffer
	for _, p := range pis {
		tr, _ := p.ab.Suchen(context.Background(), "inhalt website", 20)
		listen = append(listen, tr)
	}
	if z := Zusammenfuehren(listen, 50); len(z) != 9 {
		t.Fatalf("%d Treffer statt 9", len(z))
	}
	// 4. Ausfall: ein Pi weg – über die Kopien bleibt alles auffindbar
	listen = nil
	for _, id := range ids[1:] {
		tr, _ := pis[id].ab.Suchen(context.Background(), "inhalt website", 20)
		listen = append(listen, tr)
	}
	if z := Zusammenfuehren(listen, 50); len(z) != 9 {
		t.Fatalf("nach Ausfall %d Treffer statt 9", len(z))
	}
	for _, p := range pis {
		p.ab.Schliessen()
	}
}

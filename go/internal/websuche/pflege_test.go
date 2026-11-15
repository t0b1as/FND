package websuche

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVerdichten(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	for i := 0; i < 300; i++ {
		ix.Hinzufuegen(Dokument{URL: fmt.Sprintf("https://a.de/%d", i), Titel: "t", Text: strings.Repeat(fmt.Sprintf("wort%d ", i), 200)})
	}
	for i := 0; i < 200; i++ {
		ix.Loeschen(fmt.Sprintf("https://a.de/%d", i))
	}
	ix.Schreiben()
	vorher := ix.datGr
	if ix.TotAnteil() < 0.6 {
		t.Fatalf("tot %f", ix.TotAnteil())
	}
	ok, err := ix.Verdichten(false)
	if err != nil || !ok || ix.datGr >= vorher/2 || ix.TotAnteil() != 0 {
		t.Fatalf("%v %v %d→%d", ok, err, vorher, ix.datGr)
	}
	if tr, _ := ix.Suchen(context.Background(), "wort250", 3); len(tr) != 1 || tr[0].URL != "https://a.de/250" {
		t.Fatalf("nach Verdichten: %+v", tr)
	}
	// weiter schreibbar, und nach Neustart alles da
	ix.Hinzufuegen(Dokument{URL: "https://b.de", Titel: "neu", Text: "nachher"})
	ix.Schliessen()
	ix, _ = IndexOeffnen(d)
	for _, q := range []string{"wort299", "nachher"} {
		if tr, _ := ix.Suchen(context.Background(), q, 3); len(tr) != 1 {
			t.Fatalf("%s nach Neustart: %+v", q, tr)
		}
	}
	if n, _ := ix.Stand(); n != 101 {
		t.Fatalf("stand %d", n)
	}
	ix.Schliessen()
}

func TestVerdichtenAbsturz(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	ix.Hinzufuegen(Dokument{URL: "https://a.de", Titel: "bleibt", Text: "da"})
	ix.Schliessen()
	// halbfertige Dateien ohne Marke → verwerfen
	os.WriteFile(filepath.Join(d, "dok.dat.neu"), []byte("kaputt"), 0o640)
	ix, _ = IndexOeffnen(d)
	if _, err := os.Stat(filepath.Join(d, "dok.dat.neu")); err == nil {
		t.Fatal("halbe Datei nicht verworfen")
	}
	if tr, _ := ix.Suchen(context.Background(), "bleibt", 3); len(tr) != 1 {
		t.Fatal("Index beschädigt")
	}
	ix.Schliessen()
}

func TestPlatzregel(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	defer ix.Schliessen()
	ix.Hinzufuegen(Dokument{URL: "https://a.de", Titel: "x"})
	st := Platzregel{}.Stand(d, ix)
	if !st.Auto || st.GrenzeGB <= 0 || st.Voll {
		t.Fatalf("%+v", st)
	}
	if st := (Platzregel{GrenzeGB: 0.000000001}).Stand(d, ix); !st.Voll || !strings.Contains(st.Grund, "Speichergrenze") {
		t.Fatalf("%+v", st)
	}
	if st := (Platzregel{RestGB: 1e9}).Stand(d, ix); !st.Voll || !strings.Contains(st.Grund, "fast voll") {
		t.Fatalf("%+v", st)
	}
}

func TestVeraltete(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	defer ix.Schliessen()
	alt := time.Now().Add(-40 * 24 * time.Hour).Unix()
	for i := 0; i < 5; i++ {
		ix.Hinzufuegen(Dokument{URL: fmt.Sprintf("https://alt.de/%d", i), Titel: "x", Zeit: alt})
	}
	ix.Hinzufuegen(Dokument{URL: "https://neu.de", Titel: "x"})
	v := ix.Veraltete(time.Now().Add(-30*24*time.Hour), 3)
	v2 := ix.Veraltete(time.Now().Add(-30*24*time.Hour), 3)
	if len(v) != 3 || len(v2) != 3 || v[0] == v2[0] {
		t.Fatalf("%v %v", v, v2)
	}
	for _, u := range append(v, v2...) {
		if strings.Contains(u, "neu.de") {
			t.Fatal("frische Seite als veraltet")
		}
	}
}

// Nicht erreichbare Seiten: erst nach 3 Fehlversuchen über mindestens 5 Tage heraus.
func TestUnerreichbar(t *testing.T) {
	var mu sync.Mutex
	kaputt := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		k := kaputt
		mu.Unlock()
		switch r.URL.Path {
		case "/robots.txt", "/.well-known/tdmrep.json":
			http.NotFound(w, r)
		case "/weg":
			if k {
				http.NotFound(w, r)
				return
			}
			fallthrough
		default:
			if k && r.URL.Path == "/kaputt" {
				w.WriteHeader(502)
				return
			}
			if r.URL.Path == "/alt" {
				http.Redirect(w, r, "/neu", http.StatusMovedPermanently)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<title>Seite %s</title><p>Inhalt ausreichend lang für den Index hier.</p>", r.URL.Path)
		}
	}))
	defer srv.Close()
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	defer ix.Schliessen()
	jetzt := time.Now()
	var jmu sync.Mutex
	c := NeuerCrawler(ix, d, CrawlerEinstellungen{Tiefe: 1})
	c.ErlaubeLokal = true
	c.Jetzt = func() time.Time { jmu.Lock(); defer jmu.Unlock(); return jetzt }
	lies := func(u string) {
		h := &hostZustand{}
		c.lesen(context.Background(), auftrag{url: u}, h)
	}
	for _, p := range []string{"/kaputt", "/weg", "/alt"} {
		lies(srv.URL + p)
	}
	if hat, _ := ix.Hat(srv.URL + "/kaputt"); !hat {
		t.Fatal("nicht aufgenommen")
	}
	if hat, _ := ix.Hat(srv.URL + "/neu"); !hat {
		t.Fatal("Weiterleitungsziel fehlt")
	}
	mu.Lock()
	kaputt = true
	mu.Unlock()
	// 404: sofort heraus
	jetzt = jetzt.Add(31 * 24 * time.Hour)
	lies(srv.URL + "/weg")
	if hat, _ := ix.Hat(srv.URL + "/weg"); hat {
		t.Fatal("404 nicht entfernt")
	}
	// 502: dreimal, Tag 0, 2, 4 → noch drin (unter 5 Tagen); Tag 7 → heraus
	for _, tag := range []int{0, 2, 4} {
		jmu.Lock()
		jetzt2 := jetzt.Add(time.Duration(tag) * 24 * time.Hour)
		jmu.Unlock()
		c.Jetzt = func() time.Time { return jetzt2 }
		lies(srv.URL + "/kaputt")
		if hat, _ := ix.Hat(srv.URL + "/kaputt"); !hat {
			t.Fatalf("zu früh entfernt (Tag %d)", tag)
		}
	}
	j7 := jetzt.Add(7 * 24 * time.Hour)
	c.Jetzt = func() time.Time { return j7 }
	lies(srv.URL + "/kaputt")
	if hat, _ := ix.Hat(srv.URL + "/kaputt"); hat {
		t.Fatal("nicht entfernt nach 4 Fehlversuchen über 7 Tage")
	}
	if c.Stand().Entfernt != 1 {
		t.Fatalf("stand %+v", c.Stand())
	}
	// Erreichbar nach einem Aussetzer: Zähler weg
	lies(srv.URL + "/neu")
	if len(c.fehl) != 0 {
		t.Fatalf("fehl %v", c.fehl)
	}
}

func TestPlatzPause(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	defer ix.Schliessen()
	c := NeuerCrawler(ix, d, CrawlerEinstellungen{})
	c.Platz = func() string { return "Speichergrenze erreicht" }
	c.Vormerken("https://a.de/")
	c.platzPruefen()
	c.mu.Lock()
	g := c.pauseGrundGesperrt()
	c.mu.Unlock()
	if g != "Speichergrenze erreicht" {
		t.Fatal(g)
	}
}

func TestCgroupGrenze(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "memory.high"), []byte("402653184\n"), 0o644)
	os.WriteFile(filepath.Join(d, "memory.max"), []byte("max\n"), 0o644)
	if g := grenzeAus(d); g != 402653184 {
		t.Fatal(g)
	}
	os.WriteFile(filepath.Join(d, "memory.max"), []byte("300000000\n"), 0o644)
	if g := grenzeAus(d); g != 300000000 {
		t.Fatal(g)
	}
	if g := grenzeAus(t.TempDir()); g != 0 {
		t.Fatal(g)
	}
}

func TestBandbreiteNeustart(t *testing.T) {
	d := t.TempDir()
	ix, _ := IndexOeffnen(d)
	defer ix.Schliessen()
	c := NeuerCrawler(ix, d, CrawlerEinstellungen{})
	c.zaehleBytes(5_000_000)
	c.bandbreiteSichern()
	c2 := NeuerCrawler(ix, d, CrawlerEinstellungen{})
	c2.bandbreiteLaden()
	if mb := c2.Stand().MBStunde; mb < 4.9 {
		t.Fatalf("Stundenkonto verloren: %f", mb)
	}
}

// Package websuche: Metasuche ohne Zusatzdienst (R895).
//
// Der Node fragt mehrere Suchdienste parallel ab und führt die Treffer
// zusammen – dieselbe Arbeit, die SearXNG macht, aber im Go-Prozess und mit
// wenigen MB statt 200–300 MB RAM. Damit läuft die Suche auch auf einer Pi 3.
//
// Quellen: DuckDuckGo (HTML-Seite), Mojeek (eigener Index, britisch),
// Wikipedia (API) und – wenn eingerichtet – ein SearXNG im Netz, das dann
// mit Vorrang zählt. Ab R896 kommt der eigene Index als weitere Quelle dazu.
//
// Zusammenführen: Reciprocal Rank Fusion (1/(60+Rang) je Quelle). Eine
// Adresse, die mehrere Dienste liefern, steigt nach oben. Gleiche Adressen
// werden über eine normierte Form erkannt (ohne Schema, www., Tracking).
//
// Nur Standardbibliothek – das Paket lässt sich ohne die Abhängigkeiten des
// Nodes übersetzen und testen.
package websuche

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Treffer: ein Ergebnis. Quellen nennt alle Dienste, die es lieferten.
type Treffer struct {
	Titel   string   `json:"titel"`
	URL     string   `json:"url"`
	Text    string   `json:"text"`
	Quellen []string `json:"quellen"`
	Punkte  float64  `json:"punkte"`
}

// Quelle: ein Suchdienst.
type Quelle interface {
	Name() string
	Suchen(ctx context.Context, anfrage, sprache string) ([]Treffer, error)
}

// Gewichtet: Quellen mit eigenem Gewicht (SearXNG und eigener Index zählen mehr).
type Gewichtet interface{ Gewicht() float64 }

// Ergebnis einer Metasuche.
type Ergebnis struct {
	Anfrage string            `json:"anfrage"`
	Treffer []Treffer         `json:"treffer"`
	Dienste map[string]string `json:"dienste"` // Name → "ok (n)" oder Fehlertext
	DauerMs int64             `json:"dauer_ms"`
	Cache   bool              `json:"cache,omitempty"`
}

// ErrLeer: nichts zu suchen.
var ErrLeer = errors.New("leere Anfrage")

// Agent: so meldet sich der Node bei den Diensten.
const Agent = "Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36 FundusNode"

// Client: gemeinsamer HTTP-Client; Tests ersetzen ihn.
var Client = &http.Client{Timeout: 9 * time.Second}

const rrfK = 60.0

// Metasuche: alle Quellen parallel, höchstens frist lang. Quellen, die
// nicht rechtzeitig antworten, fehlen im Ergebnis (mit Hinweis).
func Metasuche(ctx context.Context, anfrage, sprache string, quellen []Quelle, frist time.Duration) (*Ergebnis, error) {
	anfrage = strings.Join(strings.Fields(anfrage), " ")
	if anfrage == "" {
		return nil, ErrLeer
	}
	if r := []rune(anfrage); len(r) > 300 {
		anfrage = string(r[:300])
	}
	if sprache == "" {
		sprache = "de"
	}
	if e := cacheLesen(anfrage, sprache, quellen); e != nil {
		return e, nil
	}
	t0 := time.Now()
	ctx, abbr := context.WithTimeout(ctx, frist)
	defer abbr()
	type antwort struct {
		name    string
		gewicht float64
		t       []Treffer
		err     error
	}
	kanal := make(chan antwort, len(quellen))
	var wg sync.WaitGroup
	for _, q := range quellen {
		wg.Add(1)
		go func(q Quelle) {
			defer wg.Done()
			g := 1.0
			if gw, ok := q.(Gewichtet); ok {
				g = gw.Gewicht()
			}
			t, err := q.Suchen(ctx, anfrage, sprache)
			kanal <- antwort{q.Name(), g, t, err}
		}(q)
	}
	wg.Wait()
	close(kanal)

	e := &Ergebnis{Anfrage: anfrage, Dienste: map[string]string{}}
	nachURL := map[string]*Treffer{}
	var reihe []*Treffer
	for a := range kanal {
		if a.err != nil {
			e.Dienste[a.name] = a.err.Error()
			continue
		}
		e.Dienste[a.name] = "ok (" + itoa(len(a.t)) + ")"
		gesehen := map[string]bool{}
		rang := 0
		for _, t := range a.t {
			n := Normieren(t.URL)
			if n == "" || gesehen[n] {
				continue
			}
			gesehen[n] = true
			rang++
			p := a.gewicht / (rrfK + float64(rang))
			if v, ok := nachURL[n]; ok {
				v.Punkte += p
				v.Quellen = append(v.Quellen, a.name)
				if len(t.Text) > len(v.Text) {
					v.Text = t.Text
				}
				if v.Titel == "" {
					v.Titel = t.Titel
				}
				continue
			}
			neu := &Treffer{Titel: t.Titel, URL: t.URL, Text: t.Text, Quellen: []string{a.name}, Punkte: p}
			nachURL[n] = neu
			reihe = append(reihe, neu)
		}
	}
	sort.SliceStable(reihe, func(i, j int) bool { return reihe[i].Punkte > reihe[j].Punkte })
	for _, t := range reihe {
		sort.Strings(t.Quellen)
		if t.Titel == "" {
			t.Titel = t.URL
		}
		e.Treffer = append(e.Treffer, *t)
	}
	e.DauerMs = time.Since(t0).Milliseconds()
	if len(e.Treffer) > 0 {
		cacheSchreiben(anfrage, sprache, quellen, e)
	}
	return e, nil
}

// Normieren: Vergleichsform einer Adresse – ohne Schema, www., Fragment,
// Tracking-Parameter und abschließenden Schrägstrich. "" = keine Webadresse.
func Normieren(roh string) string {
	u, err := url.Parse(strings.TrimSpace(roh))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		host += ":" + p
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" || lk == "ref" || lk == "msclkid" {
			q.Del(k)
		}
	}
	pfad := strings.TrimRight(u.EscapedPath(), "/")
	s := host + pfad
	if enc := q.Encode(); enc != "" {
		s += "?" + enc
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------------------------------------------------------------------------
// Zwischenspeicher: gleiche Anfrage binnen 10 Minuten aus dem Speicher.

type cacheEintrag struct {
	e    *Ergebnis
	zeit time.Time
}

var (
	cacheMu   sync.Mutex
	cacheMap  = map[string]cacheEintrag{}
	CacheZeit = 10 * time.Minute
)

func cacheSchluessel(anfrage, sprache string, quellen []Quelle) string {
	n := make([]string, 0, len(quellen))
	for _, q := range quellen {
		n = append(n, q.Name())
	}
	sort.Strings(n)
	return strings.ToLower(anfrage) + "\x00" + sprache + "\x00" + strings.Join(n, ",")
}

func cacheLesen(anfrage, sprache string, quellen []Quelle) *Ergebnis {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	c, ok := cacheMap[cacheSchluessel(anfrage, sprache, quellen)]
	if !ok || time.Since(c.zeit) > CacheZeit {
		return nil
	}
	kopie := *c.e
	kopie.Cache = true
	return &kopie
}

func cacheSchreiben(anfrage, sprache string, quellen []Quelle, e *Ergebnis) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if len(cacheMap) >= 200 {
		for k, v := range cacheMap {
			if time.Since(v.zeit) > CacheZeit || len(cacheMap) >= 200 {
				delete(cacheMap, k)
			}
		}
	}
	cacheMap[cacheSchluessel(anfrage, sprache, quellen)] = cacheEintrag{e, time.Now()}
}

// CacheLeeren: nach geänderten Einstellungen.
func CacheLeeren() {
	cacheMu.Lock()
	cacheMap = map[string]cacheEintrag{}
	cacheMu.Unlock()
}

package api

// Suchverbund (R897) – siehe internal/websuche/verbund.go.
//
// Pis mit eigenem Index können einem Verbund beitreten (Einstellungen →
// Websuche). Dann gilt:
//   - Jede Website hat einen zuständigen Pi (Rendezvous-Hashing über die
//     Mitglieder). Nur er liest sie; Links auf fremde Websites gibt jeder Pi
//     an den Zuständigen weiter.
//   - Jede aufgenommene Seite geht als Kopie an den Zweitzuständigen. Fällt
//     ein Pi aus, findet die Suche seine Websites weiter über die Kopie.
//   - Eine Suche geht an alle Mitglieder; die Treffer werden zusammengeführt
//     (gleiche Adressen einmal).
//
// Mitglieder: alle 3 Minuten fragt jeder Pi die anderen nach ihrem Stand. Wer
// 15 Minuten lang nicht antwortet, fällt heraus – kurze Aussetzer verschieben
// so keine Zuständigkeiten.

import (
	"context"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/fundus/node/internal/websuche"
)

const verbundGnade = 15 * time.Minute

type verbundMitglied struct {
	Peer      string  `json:"peer"`
	Node      string  `json:"node"`
	Dokumente int     `json:"dokumente"`
	FreiGB    float64 `json:"frei_gb"`
	Selbst    bool    `json:"selbst,omitempty"`
	Voll      bool    `json:"voll,omitempty"` // R898: bekommt keine neuen Websites und Kopien
	Zeit      int64   `json:"zeit"`
}

type verbundLink struct {
	url   string
	tiefe int
}

var (
	vbMu    sync.RWMutex
	vbStand = map[string]verbundMitglied{}
	vbAn    bool

	vbAbgabe = make(chan verbundLink, 5000)
	vbKopie  = make(chan websuche.Dokument, 2000)
	vbWeg    = make(chan string, 2000)
)

// verbundAktualisieren: Mitglieder neu erfragen.
func (s *Server) verbundAktualisieren() {
	k := s.websucheLesen()
	if !k.Verbund || k.Index.Modus == websuche.OrtKnoten {
		vbMu.Lock()
		vbAn = false
		vbStand = map[string]verbundMitglied{}
		vbMu.Unlock()
		return
	}
	jetzt := time.Now()
	selbst := verbundMitglied{Peer: s.nodeID(), Node: aeroNodeName(), Selbst: true, Zeit: jetzt.Unix()}
	if ab, err := s.websucheAblage(k); err == nil {
		i := ab.Info()
		selbst.Dokumente, selbst.FreiGB = i.Dokumente, i.FreiGB
		selbst.Voll = s.websuchePlatz(k, ab).Voll
	}
	neu := []verbundMitglied{selbst}
	if s.node != nil {
		ctx, abbr := context.WithTimeout(context.Background(), 5*time.Second)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, p := range s.node.Peers() {
			wg.Add(1)
			go func(pid string) {
				defer wg.Done()
				ant, err := s.suchindexFrage(ctx, pid, suchindexAnfrage{Art: "status"})
				if err != nil || !ant.Verbund || ant.Info == nil {
					return
				}
				mu.Lock()
				neu = append(neu, verbundMitglied{Peer: pid, Node: ant.Node, Dokumente: ant.Info.Dokumente, FreiGB: ant.Info.FreiGB, Voll: ant.Voll, Zeit: jetzt.Unix()})
				mu.Unlock()
			}(p.String())
		}
		wg.Wait()
		abbr()
	}
	vbMu.Lock()
	defer vbMu.Unlock()
	vbAn = true
	for _, m := range neu {
		vbStand[m.Peer] = m
	}
	for p, m := range vbStand {
		if jetzt.Sub(time.Unix(m.Zeit, 0)) > verbundGnade {
			delete(vbStand, p)
		}
	}
}

func verbundMitglieder() []string {
	vbMu.RLock()
	defer vbMu.RUnlock()
	l := make([]string, 0, len(vbStand))
	for p := range vbStand {
		l = append(l, p)
	}
	sort.Strings(l)
	return l
}

// verbundZuteilung (R898): Mitglieder, die Neues annehmen – volle Pis
// bekommen keine Websites und keine Kopien mehr; durchsucht werden sie weiter.
func verbundZuteilung() []string {
	vbMu.RLock()
	defer vbMu.RUnlock()
	l := make([]string, 0, len(vbStand))
	for p, m := range vbStand {
		if !m.Voll {
			l = append(l, p)
		}
	}
	sort.Strings(l)
	return l
}

func verbundStandListe() []verbundMitglied {
	vbMu.RLock()
	defer vbMu.RUnlock()
	l := make([]verbundMitglied, 0, len(vbStand))
	for _, m := range vbStand {
		l = append(l, m)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Selbst || (!l[j].Selbst && l[i].Node < l[j].Node) })
	return l
}

func (s *Server) verbundIstMitglied(peer string) bool {
	vbMu.RLock()
	defer vbMu.RUnlock()
	_, ok := vbStand[peer]
	return ok && peer != s.nodeID()
}

// verbundZustaendig: liest dieser Pi den Host?
func (s *Server) verbundZustaendig(host string) bool {
	m := verbundZuteilung()
	if len(m) <= 1 {
		return true
	}
	return websuche.Zustaendige(host, m, 1)[0] == s.nodeID()
}

// Rückrufe des Crawlers – dürfen nicht blockieren (volle Puffer: verwerfen).
func verbundAbgeben(u string, tiefe int) {
	select {
	case vbAbgabe <- verbundLink{u, tiefe}:
	default:
	}
}

func verbundKopie(d websuche.Dokument) {
	select {
	case vbKopie <- d:
	default:
	}
}

func verbundEntfernt(u string) {
	select {
	case vbWeg <- u:
	default:
	}
}

// verbundKopieAnnehmen: Kopien eines anderen Pis aufnehmen (neuere Fassung gewinnt).
func verbundKopieAnnehmen(ab *websuche.Ablage, doks []websuche.Dokument, weg []string) {
	if ab == nil || ab.Index == nil {
		return
	}
	for _, d := range doks {
		if hat, wann := ab.Index.Hat(d.URL); hat && wann.Unix() >= d.Zeit {
			continue
		}
		_ = ab.Index.Hinzufuegen(d)
	}
	for _, u := range weg {
		ab.Index.Loeschen(u)
	}
}

func hostVon(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return ""
}

// verbundSchleife: Mitglieder pflegen, Gesammeltes alle 10 s verschicken.
func (s *Server) verbundSchleife() {
	mitglieder := time.NewTicker(3 * time.Minute)
	senden := time.NewTicker(10 * time.Second)
	defer mitglieder.Stop()
	defer senden.Stop()
	links := map[string]map[int][]string{} // Peer → Tiefe → Adressen
	kopien := map[string][]websuche.Dokument{}
	weg := map[string][]string{}
	n := 0
	ziel := func(u string, rang int) string {
		m := verbundZuteilung()
		z := websuche.Zustaendige(hostVon(u), m, rang+1)
		if len(z) <= rang || z[rang] == s.nodeID() {
			return ""
		}
		return z[rang]
	}
	abschicken := func() {
		if n == 0 {
			return
		}
		ctx, abbr := context.WithTimeout(context.Background(), 20*time.Second)
		defer abbr()
		for p, nachTiefe := range links {
			for t, l := range nachTiefe {
				for len(l) > 0 {
					teil := l
					if len(teil) > 500 {
						teil = l[:500]
					}
					l = l[len(teil):]
					_, _ = s.suchindexFrage(ctx, p, suchindexAnfrage{Art: "vormerken", URLs: teil, Tiefe: t})
				}
			}
		}
		for p, d := range kopien {
			for len(d) > 0 {
				teil := d
				if len(teil) > 100 {
					teil = d[:100]
				}
				d = d[len(teil):]
				_, _ = s.suchindexFrage(ctx, p, suchindexAnfrage{Art: "kopie", Doks: teil, Weg: weg[p]})
				delete(weg, p)
			}
		}
		for p, w := range weg {
			_, _ = s.suchindexFrage(ctx, p, suchindexAnfrage{Art: "kopie", Weg: w})
		}
		links, kopien, weg, n = map[string]map[int][]string{}, map[string][]websuche.Dokument{}, map[string][]string{}, 0
	}
	for {
		select {
		case <-mitglieder.C:
			s.verbundAktualisieren()
		case <-senden.C:
			abschicken()
		case l := <-vbAbgabe:
			if p := ziel(l.url, 0); p != "" {
				if links[p] == nil {
					links[p] = map[int][]string{}
				}
				links[p][l.tiefe] = append(links[p][l.tiefe], l.url)
				n++
			}
		case d := <-vbKopie:
			if p := ziel(d.URL, 1); p != "" {
				kopien[p] = append(kopien[p], d)
				n++
				if len(kopien[p]) >= 100 {
					abschicken()
				}
			}
		case u := <-vbWeg:
			if p := ziel(u, 1); p != "" {
				weg[p] = append(weg[p], u)
				n++
			}
		}
	}
}

// indexSuchen: eigener Index; im Verbund zusätzlich alle anderen Mitglieder.
func (s *Server) indexSuchen(ctx context.Context, k websucheConfig, anfrage string) ([]websuche.Treffer, error) {
	ab, err := s.websucheAblage(k)
	if !k.Verbund {
		if err != nil {
			return nil, err
		}
		return ab.Suchen(ctx, anfrage, 20)
	}
	var mu sync.Mutex
	var listen [][]websuche.Treffer
	var wg sync.WaitGroup
	if err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if t, err := ab.Suchen(ctx, anfrage, 20); err == nil {
				mu.Lock()
				listen = append(listen, t)
				mu.Unlock()
			}
		}()
	}
	fctx, abbr := context.WithTimeout(ctx, 4*time.Second)
	defer abbr()
	for _, p := range verbundMitglieder() {
		if p == s.nodeID() {
			continue
		}
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			ant, err := s.suchindexFrage(fctx, p, suchindexAnfrage{Art: "suche", Anfrage: anfrage})
			if err == nil {
				mu.Lock()
				listen = append(listen, ant.Treffer)
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	if len(listen) == 0 && err != nil {
		return nil, err
	}
	return websuche.Zusammenfuehren(listen, 20), nil
}

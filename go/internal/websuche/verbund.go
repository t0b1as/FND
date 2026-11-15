package websuche

// Suchverbund (R897): Websites werden über die teilnehmenden Pis verteilt.
//
// Zuteilung per Rendezvous-Hashing: Für jeden Host bekommt jeder Pi eine
// Prüfsumme aus Host und Peer-ID; die Pis mit den höchsten Werten sind
// zuständig – der erste liest die Website, der zweite hält eine Kopie.
// Alle Pis rechnen mit derselben Mitgliederliste dasselbe aus, ohne sich
// abzustimmen. Kommt ein Pi hinzu oder fällt einer weg, wechseln nur die
// Hosts, die ihn betreffen (etwa 1/n), nicht alle.

import (
	"hash/fnv"
	"sort"
	"strings"
)

// VerbundHost: Vergleichsform eines Hosts (klein, ohne www., ohne Port 80/443).
func VerbundHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		if p := h[i+1:]; p == "80" || p == "443" {
			h = h[:i]
		}
	}
	return strings.TrimPrefix(h, "www.")
}

func hrw(host, peer string) uint64 {
	f := fnv.New64a()
	f.Write([]byte(host))
	f.Write([]byte{0})
	f.Write([]byte(peer))
	x := f.Sum64()
	// Nachmischen (splitmix64) – FNV allein streut die letzten Bits schwach.
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// Zustaendige: die n zuständigen Pis für einen Host, wichtigster zuerst.
func Zustaendige(host string, mitglieder []string, n int) []string {
	host = VerbundHost(host)
	type w struct {
		peer string
		v    uint64
	}
	ws := make([]w, 0, len(mitglieder))
	gesehen := map[string]bool{}
	for _, m := range mitglieder {
		if m == "" || gesehen[m] {
			continue
		}
		gesehen[m] = true
		ws = append(ws, w{m, hrw(host, m)})
	}
	sort.Slice(ws, func(i, j int) bool {
		if ws[i].v != ws[j].v {
			return ws[i].v > ws[j].v
		}
		return ws[i].peer < ws[j].peer
	})
	if n > len(ws) {
		n = len(ws)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = ws[i].peer
	}
	return out
}

// Zusammenfuehren: Trefferlisten mehrerer Pis zu einer; gleiche Adressen
// einmal (die höhere Wertung zählt), höchstens n.
func Zusammenfuehren(listen [][]Treffer, n int) []Treffer {
	beste := map[string]Treffer{}
	var reihe []string
	for _, l := range listen {
		for _, t := range l {
			k := Normieren(t.URL)
			if k == "" {
				continue
			}
			if alt, ok := beste[k]; ok {
				if t.Punkte > alt.Punkte {
					beste[k] = t
				}
				continue
			}
			beste[k] = t
			reihe = append(reihe, k)
		}
	}
	out := make([]Treffer, 0, len(reihe))
	for _, k := range reihe {
		out = append(out, beste[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Punkte > out[j].Punkte })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

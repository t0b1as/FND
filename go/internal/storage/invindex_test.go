package storage

import (
	"sort"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func newInvTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func putListing(t *testing.T, s *Store, id, title, desc, cat string) {
	t.Helper()
	r := &Record{ID: id, Type: RecordListing, Data: map[string]any{
		"title": title, "description": desc, "category": cat,
	}}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
}

// linearMatch ist die Referenz: exakt die alte lineare Substring-Suche.
func linearMatch(recs []*Record, q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []string
	for _, r := range recs {
		hay := strings.ToLower(
			get(r, "title") + " " + get(r, "description") + " " +
				get(r, "listing_text") + " " + get(r, "keywords"))
		if q == "" || strings.Contains(hay, q) {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	return out
}

func get(r *Record, k string) string {
	if v, ok := r.Data[k].(string); ok {
		return v
	}
	return ""
}

// indexMatch nutzt den Index + Verifikation (wie der Handler).
func indexMatch(t *testing.T, s *Store, allRecs []*Record, q string) []string {
	qLower := strings.ToLower(strings.TrimSpace(q))
	var cand []*Record
	if len([]rune(qLower)) >= 3 {
		ids, ok := s.SearchCandidates(RecordListing, qLower)
		if !ok {
			cand = allRecs
		} else {
			for id := range ids {
				if rec, err := s.Get(RecordListing, id); err == nil && rec != nil {
					cand = append(cand, rec)
				}
			}
		}
	} else {
		cand = allRecs
	}
	// Verifikation (exakt wie der Handler).
	var out []string
	for _, r := range cand {
		hay := strings.ToLower(
			get(r, "title") + " " + get(r, "description") + " " +
				get(r, "listing_text") + " " + get(r, "keywords"))
		if qLower == "" || strings.Contains(hay, qLower) {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	return out
}

// TestInvIndexMatchesLinear: das Kernversprechen — der Index liefert für diverse
// Queries EXAKT dieselben Treffer wie der lineare Substring-Scan.
func TestInvIndexMatchesLinear(t *testing.T) {
	s := newInvTestStore(t)
	data := []struct{ id, title, desc, cat string }{
		{"L1", "Bürostuhl schwarz", "ergonomischer Drehstuhl", "moebel"},
		{"L2", "Esstisch aus Eiche", "massiver Holztisch", "moebel"},
		{"L3", "Stuhl Klassiker", "vier Beine Holz", "moebel"},
		{"L4", "Fahrrad 28 Zoll", "kaum gefahren", "sport"},
		{"L5", "Schreibtisch weiß", "höhenverstellbar mit Tisch-Platte", "moebel"},
		{"L6", "Türgriff Messing", "antik", "bau"},
	}
	for _, d := range data {
		putListing(t, s, d.id, d.title, d.desc, d.cat)
	}
	all, _ := s.List(RecordListing)

	// Diverse Queries: Infix ("tisch" in Esstisch/Schreibtisch), Wort, Umlaut, Miss.
	queries := []string{"tisch", "stuhl", "holz", "tür", "eiche", "fahrrad",
		"schwarz", "xyz", "höhenverstellbar", "dreh"}
	for _, q := range queries {
		want := linearMatch(all, q)
		got := indexMatch(t, s, all, q)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("Query %q: Index=%v ≠ Linear=%v", q, got, want)
		}
	}
}

// TestInvIndexInfixMatch: der wichtige Trigramm-Vorteil — "tisch" findet auch
// "Esstisch" und "Schreibtisch" (Infix), nicht nur Wortanfänge.
func TestInvIndexInfixMatch(t *testing.T) {
	s := newInvTestStore(t)
	putListing(t, s, "A", "Esstisch", "", "")
	putListing(t, s, "B", "Schreibtisch", "", "")
	putListing(t, s, "C", "Sofa", "", "")
	all, _ := s.List(RecordListing)
	got := indexMatch(t, s, all, "tisch")
	if strings.Join(got, ",") != "A,B" {
		t.Fatalf("Infix-Suche 'tisch' = %v, want [A B]", got)
	}
}

// TestInvIndexUpdateRemovesStale: nach einem Update findet die alte Beschreibung
// nicht mehr.
func TestInvIndexUpdateRemovesStale(t *testing.T) {
	s := newInvTestStore(t)
	putListing(t, s, "L1", "altertitel", "", "")
	putListing(t, s, "L1", "neuertitel", "", "") // selbe ID, neuer Text
	all, _ := s.List(RecordListing)
	if got := indexMatch(t, s, all, "altertitel"); len(got) != 0 {
		t.Fatalf("alter Text sollte nicht mehr matchen, got %v", got)
	}
	if got := indexMatch(t, s, all, "neuertitel"); strings.Join(got, ",") != "L1" {
		t.Fatalf("neuer Text sollte L1 finden, got %v", got)
	}
}

// TestInvIndexDeleteRemoves: gelöschte Records erscheinen nicht mehr im Index.
func TestInvIndexDeleteRemoves(t *testing.T) {
	s := newInvTestStore(t)
	putListing(t, s, "L1", "löschmich", "", "")
	if err := s.Delete(RecordListing, "L1"); err != nil {
		t.Fatal(err)
	}
	ids, ok := s.SearchCandidates(RecordListing, "löschmich")
	if ok && len(ids) > 0 {
		t.Fatalf("gelöschter Record sollte nicht im Index sein, got %v", ids)
	}
}

// TestCategoryCandidates: Browsing per Kategorie ohne Suchbegriff.
func TestCategoryCandidates(t *testing.T) {
	s := newInvTestStore(t)
	putListing(t, s, "L1", "A", "", "moebel")
	putListing(t, s, "L2", "B", "", "moebel")
	putListing(t, s, "L3", "C", "", "sport")
	ids, ok := s.CategoryCandidates(RecordListing, "moebel")
	if !ok || len(ids) != 2 {
		t.Fatalf("Kategorie moebel sollte 2 Records liefern, got %v", ids)
	}
	if _, has := ids["L3"]; has {
		t.Fatal("L3 (sport) sollte nicht in moebel sein")
	}
}

// TestTrigramsUnicode: Umlaute werden runenbasiert zerlegt (keine kaputten Bytes).
func TestTrigramsUnicode(t *testing.T) {
	tg := trigrams("tür")
	if len(tg) != 1 || tg[0] != "tür" {
		t.Fatalf("trigrams('tür') = %v, want [tür]", tg)
	}
	// "öäü" → ein Trigramm "öäü".
	if tg2 := trigrams("öäü"); len(tg2) != 1 || tg2[0] != "öäü" {
		t.Fatalf("trigrams('öäü') = %v", tg2)
	}
}

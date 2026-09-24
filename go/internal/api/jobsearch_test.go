package api

import (
	"fmt"
	"testing"
)

// Job-Sammler darf nicht über searchCollectMax wachsen.
func TestJobSearchCollectLimit(t *testing.T) {
	m := newJobSearchManager()
	id := "jobsearch-1"
	for i := 0; i < searchCollectMax+150; i++ {
		m.add(id, []JobSearchHit{{ID: fmt.Sprintf("job-%d", i)}})
	}
	got := m.get(id)
	if len(got) != searchCollectMax {
		t.Fatalf("erwartet %d, bekam %d", searchCollectMax, len(got))
	}
}

// Deduplizierung über die ID bleibt erhalten.
func TestJobSearchDedup(t *testing.T) {
	m := newJobSearchManager()
	id := "jobsearch-2"
	for i := 0; i < 8; i++ {
		m.add(id, []JobSearchHit{{ID: "same-job"}})
	}
	if got := m.get(id); len(got) != 1 {
		t.Fatalf("Dedup verletzt: %d", len(got))
	}
}

// Gebot/Gesuch-Werte bleiben im Hit erhalten.
func TestJobSearchPreservesType(t *testing.T) {
	m := newJobSearchManager()
	id := "jobsearch-3"
	m.add(id, []JobSearchHit{
		{ID: "a", JobType: "offer", Title: "Stellenangebot"},
		{ID: "b", JobType: "request", Title: "Stellengesuch"},
	})
	got := m.get(id)
	if len(got) != 2 {
		t.Fatalf("erwartet 2, bekam %d", len(got))
	}
	types := map[string]bool{}
	for _, h := range got {
		types[h.JobType] = true
	}
	if !types["offer"] || !types["request"] {
		t.Fatal("Gebot und Gesuch müssen beide erhalten bleiben")
	}
}

// Leere Suche → keine Treffer, kein Panic.
func TestJobSearchEmpty(t *testing.T) {
	m := newJobSearchManager()
	if got := m.get("nichts"); got != nil && len(got) != 0 {
		t.Fatalf("leere Suche sollte 0 liefern, bekam %d", len(got))
	}
}

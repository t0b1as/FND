package api

import (
	"fmt"
	"testing"
)

// Der searchManager-Sammler darf insgesamt nicht über searchCollectMax wachsen.
func TestSearchCollectLimit(t *testing.T) {
	m := newSearchManager()
	id := "search-1"
	// Mehr Treffer einspeisen als das Limit erlaubt (in mehreren Batches,
	// wie sie von verschiedenen Nodes eintrudeln).
	total := searchCollectMax + 200
	for i := 0; i < total; i++ {
		m.add(id, []SearchHit{{ID: fmt.Sprintf("hit-%d", i)}})
	}
	got := m.get(id)
	if len(got) > searchCollectMax {
		t.Fatalf("Sammlung überschritt Limit: %d > %d", len(got), searchCollectMax)
	}
	if len(got) != searchCollectMax {
		t.Fatalf("erwartet exakt %d, bekam %d", searchCollectMax, len(got))
	}
}

// Deduplizierung bleibt trotz Limit erhalten (gleiche ID nur einmal).
func TestSearchCollectDedup(t *testing.T) {
	m := newSearchManager()
	id := "search-2"
	for i := 0; i < 10; i++ {
		m.add(id, []SearchHit{{ID: "same"}}) // immer dieselbe ID
	}
	got := m.get(id)
	if len(got) != 1 {
		t.Fatalf("Dedup verletzt: erwartet 1, bekam %d", len(got))
	}
}

// Batch, der das Limit überschreitet, wird sauber abgeschnitten.
func TestSearchCollectPartialBatch(t *testing.T) {
	m := newSearchManager()
	id := "search-3"
	// Erst fast voll, dann ein großer Batch.
	for i := 0; i < searchCollectMax-5; i++ {
		m.add(id, []SearchHit{{ID: fmt.Sprintf("a-%d", i)}})
	}
	big := make([]SearchHit, 100)
	for i := range big {
		big[i] = SearchHit{ID: fmt.Sprintf("b-%d", i)}
	}
	m.add(id, big)
	got := m.get(id)
	if len(got) != searchCollectMax {
		t.Fatalf("erwartet %d nach Teilbatch, bekam %d", searchCollectMax, len(got))
	}
}

// Konstanten-Sanity: die Limits müssen sinnvoll gestaffelt sein.
func TestSearchLimitsSane(t *testing.T) {
	if searchRespondMax > searchLocalMax {
		t.Fatal("Antwort-Limit sollte nicht größer als lokales Limit sein")
	}
	if searchCollectMax < searchRespondMax {
		t.Fatal("Sammel-Limit sollte mind. so groß wie das Antwort-Limit sein")
	}
	if searchLocalMax <= 0 || searchRespondMax <= 0 || searchCollectMax <= 0 {
		t.Fatal("Limits müssen positiv sein")
	}
}

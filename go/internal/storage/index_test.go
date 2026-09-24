package storage

import (
	"testing"

	"go.uber.org/zap"
)

func newIndexTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func listingWithHash(id, hash string) *Record {
	return &Record{
		ID:   id,
		Type: RecordListing,
		Data: map[string]any{"content_hash": hash, "title": "T-" + id},
	}
}

// TestIndexFindByField: nach Put ist der Record per content_hash in O(1) findbar.
func TestIndexFindByField(t *testing.T) {
	s := newIndexTestStore(t)
	if err := s.Put(listingWithHash("L1", "abc123")); err != nil {
		t.Fatal(err)
	}
	id, ok := s.FindByField(RecordListing, "content_hash", "abc123")
	if !ok || id != "L1" {
		t.Fatalf("FindByField = %q,%v — want L1,true", id, ok)
	}
	// Normalisierung: mit 0x-Präfix + Großschreibung muss genauso treffen.
	id2, ok2 := s.FindByField(RecordListing, "content_hash", "0xABC123")
	if !ok2 || id2 != "L1" {
		t.Fatalf("normalisierter Lookup = %q,%v — want L1,true", id2, ok2)
	}
}

// TestIndexUpdateMovesEntry: ändert sich der Feldwert, zeigt der alte nicht mehr,
// der neue schon.
func TestIndexUpdateMovesEntry(t *testing.T) {
	s := newIndexTestStore(t)
	s.Put(listingWithHash("L1", "oldhash"))
	s.Put(listingWithHash("L1", "newhash")) // selber Record, neuer Hash

	if _, ok := s.FindByField(RecordListing, "content_hash", "oldhash"); ok {
		t.Fatal("alter Hash müsste aus dem Index entfernt sein")
	}
	if id, ok := s.FindByField(RecordListing, "content_hash", "newhash"); !ok || id != "L1" {
		t.Fatalf("neuer Hash sollte L1 finden, got %q,%v", id, ok)
	}
}

// TestIndexDeleteRemovesEntry: ein Tombstone entfernt den Index-Eintrag.
func TestIndexDeleteRemovesEntry(t *testing.T) {
	s := newIndexTestStore(t)
	s.Put(listingWithHash("L1", "abc123"))
	if err := s.Delete(RecordListing, "L1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FindByField(RecordListing, "content_hash", "abc123"); ok {
		t.Fatal("nach Delete dürfte der Index-Eintrag nicht mehr existieren")
	}
	if _, ok := s.FindRecordByField(RecordListing, "content_hash", "abc123"); ok {
		t.Fatal("FindRecordByField dürfte gelöschten Record nicht liefern")
	}
}

// TestFindRecordByField: liefert den vollen Record.
func TestFindRecordByField(t *testing.T) {
	s := newIndexTestStore(t)
	s.Put(listingWithHash("L7", "deadbeef"))
	rec, ok := s.FindRecordByField(RecordListing, "content_hash", "deadbeef")
	if !ok || rec == nil || rec.ID != "L7" {
		t.Fatalf("FindRecordByField sollte L7 liefern, got %v,%v", rec, ok)
	}
	if rec.Data["title"] != "T-L7" {
		t.Fatalf("Record-Daten unvollständig: %v", rec.Data)
	}
}

// TestRebuildIndex: baut den Index aus bestehenden Records neu auf.
func TestRebuildIndex(t *testing.T) {
	s := newIndexTestStore(t)
	s.Put(listingWithHash("L1", "h1"))
	s.Put(listingWithHash("L2", "h2"))
	if err := s.RebuildIndex(RecordListing); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, h string }{{"L1", "h1"}, {"L2", "h2"}} {
		if id, ok := s.FindByField(RecordListing, "content_hash", c.h); !ok || id != c.id {
			t.Fatalf("nach Rebuild: %s→%s fehlt (got %q,%v)", c.h, c.id, id, ok)
		}
	}
}

// TestIndexMissReturnsFalse: unbekannter Wert liefert false.
func TestIndexMissReturnsFalse(t *testing.T) {
	s := newIndexTestStore(t)
	if _, ok := s.FindByField(RecordListing, "content_hash", "nichtda"); ok {
		t.Fatal("unbekannter Wert sollte false liefern")
	}
}

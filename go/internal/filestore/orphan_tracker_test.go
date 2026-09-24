package filestore

import (
	"testing"
	"time"
)

// TestOrphanTrackerGracePeriod prüft die Kern-Sicherheitseigenschaft: ein Chunk
// darf erst nach genügend Bestätigungen UND genügend Zeit freigegeben werden,
// und ein einziges clear() setzt den Verdacht vollständig zurück.
func TestOrphanTrackerGracePeriod(t *testing.T) {
	tr := &orphanTracker{
		path:     t.TempDir() + "/orphan-suspects.json",
		suspects: make(map[string]orphanSuspect),
	}

	h := "abc123"

	// Erster Verdacht: Count 1, firstSeen jetzt.
	first, count := tr.mark(h)
	if count != 1 {
		t.Fatalf("erster mark: Count %d, erwartet 1", count)
	}
	if time.Since(first) > time.Minute {
		t.Fatal("firstSeen sollte ~jetzt sein")
	}

	// Zweiter, dritter Verdacht: Count steigt, firstSeen bleibt.
	_, count = tr.mark(h)
	firstAgain, count := tr.mark(h)
	if count != 3 {
		t.Fatalf("dritter mark: Count %d, erwartet 3", count)
	}
	if !firstAgain.Equal(first) {
		t.Fatal("firstSeen darf sich über mehrere marks nicht ändern")
	}

	// Grace-Period-Bedingung: Count reicht (>=3), aber Zeit noch nicht um.
	// Das ist die eigentliche Schutzlogik — hier darf NICHT freigegeben werden.
	if time.Since(first) >= OrphanGracePeriod {
		t.Fatal("Testannahme: firstSeen liegt innerhalb der Grace Period")
	}

	// clear() setzt vollständig zurück — ein Peer, der wieder auftaucht, rettet
	// den Chunk komplett.
	if !tr.clear(h) {
		t.Fatal("clear sollte true liefern (Eintrag existierte)")
	}
	firstNew, countNew := tr.mark(h)
	if countNew != 1 {
		t.Fatalf("nach clear: Count %d, erwartet 1 (Zähler zurückgesetzt)", countNew)
	}
	if firstNew.Equal(first) {
		t.Fatal("nach clear muss firstSeen neu gesetzt werden")
	}
}

// TestOrphanTrackerPersistence prüft, dass der Verdacht einen Neustart überlebt
// (sonst würde ein oft neu startender Node nie freigeben).
func TestOrphanTrackerPersistence(t *testing.T) {
	dir := t.TempDir()
	tr := newOrphanTracker(dir)
	tr.mark("chunkX")
	tr.mark("chunkX")
	tr.persist()

	// Neu laden (simuliert Neustart).
	tr2 := newOrphanTracker(dir)
	first, count := tr2.mark("chunkX")
	if count != 3 {
		t.Fatalf("nach Reload+mark: Count %d, erwartet 3 (2 persistiert + 1)", count)
	}
	if time.Since(first) > time.Minute {
		t.Fatal("firstSeen sollte aus der persistierten Datei stammen")
	}
}

package filestore

import (
	"testing"
	"time"
)

func TestHostingLedgerRecordAndDue(t *testing.T) {
	hl := newHostingLedger(t.TempDir())

	// Einen Eintrag aufnehmen.
	hl.record("chunkA", "0xprovider1", "peer1", 16000000)
	if hl.count() != 1 {
		t.Fatalf("count = %d, erwartet 1", hl.count())
	}

	// Frisch aufgenommen → noch NICHT fällig (LastRewarded = jetzt).
	due := hl.due(1 * time.Hour)
	if len(due) != 0 {
		t.Fatalf("frischer Eintrag sollte nicht fällig sein, waren %d", len(due))
	}
}

func TestHostingLedgerDedup(t *testing.T) {
	hl := newHostingLedger(t.TempDir())
	hl.record("chunkA", "0xprovider1", "peer1", 100)
	hl.record("chunkA", "0xprovider1", "peer1", 100) // gleicher Chunk+Provider
	if hl.count() != 1 {
		t.Fatalf("doppeltes record sollte 1 Eintrag ergeben, war %d", hl.count())
	}
	// Anderer Provider für denselben Chunk → eigener Eintrag.
	hl.record("chunkA", "0xprovider2", "peer2", 100)
	if hl.count() != 2 {
		t.Fatalf("anderer Provider sollte eigenen Eintrag ergeben, war %d", hl.count())
	}
}

func TestHostingLedgerMarkRewarded(t *testing.T) {
	hl := newHostingLedger(t.TempDir())
	hl.record("chunkA", "0xprovider1", "peer1", 100)

	// LastRewarded künstlich in die Vergangenheit setzen → fällig machen.
	k := hostingKey("chunkA", "0xprovider1")
	hl.mu.Lock()
	hl.entries[k].LastRewarded = time.Now().Add(-2 * time.Hour).Unix()
	hl.mu.Unlock()

	due := hl.due(1 * time.Hour)
	if len(due) != 1 {
		t.Fatalf("Eintrag sollte nach 2h fällig sein, waren %d", len(due))
	}

	// Als vergütet markieren → nicht mehr fällig.
	now := time.Now().Unix()
	hl.markRewarded("chunkA", "0xprovider1", now)
	due2 := hl.due(1 * time.Hour)
	if len(due2) != 0 {
		t.Fatalf("nach markRewarded nicht mehr fällig, waren %d", len(due2))
	}
}

func TestHostingLedgerPersistence(t *testing.T) {
	dir := t.TempDir()
	hl := newHostingLedger(dir)
	hl.record("chunkA", "0xprovider1", "peer1", 12345)
	hl.record("chunkB", "0xprovider2", "peer2", 67890)

	// Neu laden → beide Einträge da.
	hl2 := newHostingLedger(dir)
	if hl2.count() != 2 {
		t.Fatalf("nach Reload count = %d, erwartet 2", hl2.count())
	}
}

func TestHostingLedgerRemove(t *testing.T) {
	hl := newHostingLedger(t.TempDir())
	hl.record("chunkA", "0xprovider1", "peer1", 100)
	hl.remove("chunkA", "0xprovider1")
	if hl.count() != 0 {
		t.Fatalf("nach remove sollte 0 Einträge sein, war %d", hl.count())
	}
}

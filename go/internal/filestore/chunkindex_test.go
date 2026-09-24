package filestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChunkRefBasicCounting(t *testing.T) {
	dir := t.TempDir()
	ci := newChunkRefIndex(dir)

	// Erste Referenz → neu.
	if !ci.addRef("chunkA", "file:doc1") {
		t.Error("erste Referenz sollte isNew=true liefern")
	}
	// Zweite Referenz (anderer Owner) → nicht neu.
	if ci.addRef("chunkA", "file:doc2") {
		t.Error("zweite Referenz sollte isNew=false liefern")
	}
	if ci.refCount("chunkA") != 2 {
		t.Errorf("refCount = %d, erwartet 2", ci.refCount("chunkA"))
	}

	// Eine Referenz entfernen → noch nicht verwaist.
	if ci.removeRef("chunkA", "file:doc1") {
		t.Error("Chunk sollte noch referenziert sein (doc2)")
	}
	// Letzte Referenz entfernen → jetzt verwaist.
	if !ci.removeRef("chunkA", "file:doc2") {
		t.Error("Chunk sollte nach letzter Referenz verwaist sein")
	}
	if ci.refCount("chunkA") != 0 {
		t.Errorf("refCount nach Entfernen = %d, erwartet 0", ci.refCount("chunkA"))
	}
}

func TestChunkRefDedup(t *testing.T) {
	ci := newChunkRefIndex(t.TempDir())
	// Gleicher Owner zweimal → nur eine Referenz.
	ci.addRef("c", "file:x")
	ci.addRef("c", "file:x")
	if ci.refCount("c") != 1 {
		t.Errorf("doppelter gleicher Owner sollte 1 Referenz ergeben, war %d", ci.refCount("c"))
	}
}

func TestChunkRefHostedOnly(t *testing.T) {
	ci := newChunkRefIndex(t.TempDir())

	// Nur host-Referenz → isHostedOnly = true.
	ci.addRef("h", "host:remote")
	if !ci.isHostedOnly("h") {
		t.Error("Chunk mit nur host:-Owner sollte isHostedOnly=true sein")
	}
	// Zusätzlich eine eigene Datei → nicht mehr hosted-only.
	ci.addRef("h", "file:mydoc")
	if ci.isHostedOnly("h") {
		t.Error("Chunk mit file:-Owner darf nicht hosted-only sein")
	}
	// Chunk ohne Referenz → nicht hosted-only.
	if ci.isHostedOnly("unknown") {
		t.Error("unbekannter Chunk darf nicht hosted-only sein")
	}
}

func TestChunkRefPersistence(t *testing.T) {
	dir := t.TempDir()
	ci := newChunkRefIndex(dir)
	ci.addRef("p1", "file:a")
	ci.addRef("p1", "host:remote")
	ci.addRef("p2", "file:b")

	// Datei muss existieren.
	if _, err := os.Stat(filepath.Join(dir, "chunkrefs.json")); err != nil {
		t.Fatalf("chunkrefs.json fehlt: %v", err)
	}

	// Neu laden → gleiche Daten.
	ci2 := newChunkRefIndex(dir)
	if ci2.refCount("p1") != 2 {
		t.Errorf("nach Reload p1 refCount = %d, erwartet 2", ci2.refCount("p1"))
	}
	if ci2.refCount("p2") != 1 {
		t.Errorf("nach Reload p2 refCount = %d, erwartet 1", ci2.refCount("p2"))
	}
	if !ci2.hasAnyRef("p1") {
		t.Error("p1 sollte nach Reload referenziert sein")
	}
}

func TestChunkRefRemoveUnknown(t *testing.T) {
	ci := newChunkRefIndex(t.TempDir())
	// Unbekannten Chunk entfernen → als verwaist behandeln (true), kein Crash.
	if !ci.removeRef("ghost", "file:x") {
		t.Error("Entfernen eines unbekannten Chunks sollte nowOrphan=true liefern")
	}
}

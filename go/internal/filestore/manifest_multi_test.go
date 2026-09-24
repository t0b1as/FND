package filestore

import (
	"fmt"
	"testing"
)

func TestSubManifestSplitReassemble(t *testing.T) {
	// Liste aus N Pseudo-Hashes erzeugen.
	n := 1000
	hashes := make([]string, n)
	for i := 0; i < n; i++ {
		hashes[i] = fmt.Sprintf("%064x", i)
	}

	blocks, err := splitIntoSubManifests(hashes)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	// Bei 400/Block → 1000 ergibt 3 Blöcke (400+400+200).
	if len(blocks) != 3 {
		t.Fatalf("erwartet 3 Sub-Manifeste, bekam %d", len(blocks))
	}

	back, err := reassembleFromSubManifests(blocks)
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if len(back) != n {
		t.Fatalf("rekonstruiert %d != %d", len(back), n)
	}
	for i := range hashes {
		if back[i] != hashes[i] {
			t.Fatalf("Hash %d weicht ab: %s != %s", i, back[i], hashes[i])
		}
	}
}

func TestNeedsMultiLevel(t *testing.T) {
	// Kleine Liste → inline.
	small := make([]string, 10)
	for i := range small {
		small[i] = fmt.Sprintf("%064x", i)
	}
	if needsMultiLevel(small) {
		t.Fatal("kleine Liste sollte inline bleiben")
	}

	// Große Liste (128 GiB @ 16 MiB = 8192 Chunks) → mehrstufig.
	big := make([]string, 8192)
	for i := range big {
		big[i] = fmt.Sprintf("%064x", i)
	}
	if !needsMultiLevel(big) {
		t.Fatal("8192-Hash-Liste muss mehrstufig sein (DHT-Limit)")
	}
}

func TestSubManifestEmpty(t *testing.T) {
	blocks, err := splitIntoSubManifests(nil)
	if err != nil {
		t.Fatalf("split nil: %v", err)
	}
	if blocks != nil {
		t.Fatal("leere Liste → keine Blöcke")
	}
}

func Test128GiBManifestFitsDHT(t *testing.T) {
	// Realitätscheck: 128 GiB bei 16 MiB Chunks = 8192 Chunks. Nach Aufteilung
	// muss JEDES Sub-Manifest unter der Inline-Schwelle (32 KiB) liegen, und das
	// Root-Manifest (nur Sub-Hashes) ebenfalls.
	const chunks = (128 << 30) / (16 << 20) // 8192
	hashes := make([]string, chunks)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}
	blocks, err := splitIntoSubManifests(hashes)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	for i, b := range blocks {
		if len(b) > manifestInlineThreshold {
			t.Fatalf("Sub-Manifest %d zu groß: %d > %d", i, len(b), manifestInlineThreshold)
		}
	}
	// Root: Anzahl Sub-Manifeste muss klein bleiben (8192/400 = 21 → ~1.5 KiB).
	if len(blocks) > 100 {
		t.Fatalf("zu viele Sub-Manifeste fürs Root-Manifest: %d", len(blocks))
	}
	t.Logf("128 GiB: %d Chunks → %d Sub-Manifeste", chunks, len(blocks))
}

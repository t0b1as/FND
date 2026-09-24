package filestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteLoadForUpload(t *testing.T) {
	// 1 GB Datei, Redundanz 5 → 4 Remote-Repliken → 4 GB Fremd-Last.
	got := remoteLoadForUpload(1_000_000_000, 5)
	if got != 4_000_000_000 {
		t.Fatalf("erwartet 4 GB, bekam %d", got)
	}
	// Redundanz 1 → keine Remote-Last (nur lokale Kopie).
	if remoteLoadForUpload(1_000_000_000, 1) != 0 {
		t.Fatal("Redundanz 1 darf keine Remote-Last erzeugen")
	}
	// Redundanz 3 → 2 Repliken.
	if remoteLoadForUpload(500_000_000, 3) != 1_000_000_000 {
		t.Fatal("Redundanz 3 × 0.5 GB = 1 GB erwartet")
	}
}

func TestCheckFairnessAllows(t *testing.T) {
	// 100 GB frei, 1 GB Datei × 4 Remote = 4 GB → ok.
	if err := checkFairness(100*1024*1024*1024, 1_000_000_000, 5); err != nil {
		t.Fatalf("sollte erlaubt sein: %v", err)
	}
}

func TestCheckFairnessBlocks(t *testing.T) {
	// 3 GB frei, 1 GB Datei × 4 Remote = 4 GB > 3 GB → blockieren.
	if err := checkFairness(3*1024*1024*1024, 1_000_000_000, 5); err == nil {
		t.Fatal("sollte blockiert werden (4 GB > 3 GB frei)")
	}
	// 2 GB frei (10 angeboten − 8 belegt), +4 GB → blockieren.
	if err := checkFairness(2*1024*1024*1024, 1_000_000_000, 5); err == nil {
		t.Fatal("nur 2 GB frei, 4 GB nötig → blockieren")
	}
}

func TestCheckFairnessCumulative(t *testing.T) {
	// 5 GB frei (10 angeboten − 5 belegt), +4 GB = passt.
	if err := checkFairness(5*1024*1024*1024, 1_000_000_000, 5); err != nil {
		t.Fatalf("4 GB <= 5 GB frei sollte erlaubt sein: %v", err)
	}
}

func TestOfferFreeBytesViaConsumed(t *testing.T) {
	// Freie Menge = OfferGB − verbraucht, robust gegen OfferGB-Änderung.
	dir := t.TempDir()
	rc := newRemoteConsumed(dir)
	rc.add(4_000_000_000) // 4 GB belegt
	offerBytes := int64(10) * 1024 * 1024 * 1024
	free := offerBytes - rc.get()
	if free != offerBytes-4_000_000_000 {
		t.Fatalf("freie Menge falsch: %d", free)
	}
}

func TestRemoteConsumedPersistence(t *testing.T) {
	dir := t.TempDir()
	rc := newRemoteConsumed(dir)
	if rc.get() != 0 {
		t.Fatal("frischer Zähler muss 0 sein")
	}
	rc.add(4_000_000_000)
	rc.add(2_000_000_000)
	if rc.get() != 6_000_000_000 {
		t.Fatalf("erwartet 6 GB, bekam %d", rc.get())
	}
	// Neu laden → Persistenz.
	rc2 := newRemoteConsumed(dir)
	if rc2.get() != 6_000_000_000 {
		t.Fatalf("nach Reload erwartet 6 GB, bekam %d", rc2.get())
	}
	// Datei existiert?
	if _, err := os.Stat(filepath.Join(dir, fairnessFile)); err != nil {
		t.Fatalf("Fairness-Datei fehlt: %v", err)
	}
}

func TestRemoteConsumedNoNegative(t *testing.T) {
	dir := t.TempDir()
	rc := newRemoteConsumed(dir)
	rc.add(-5_000_000_000) // darf nicht negativ werden
	if rc.get() != 0 {
		t.Fatalf("Zähler darf nicht negativ sein, ist %d", rc.get())
	}
}

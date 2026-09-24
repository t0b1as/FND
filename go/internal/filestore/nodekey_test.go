package filestore

import (
	"os"
	"path/filepath"
	"testing"
)

// Neu erzeugte Node-Wallet ist seed-basiert, persistiert und beim zweiten Laden
// identisch (gleiche Adresse).
func TestLoadOrCreateNodeWalletPersists(t *testing.T) {
	dir := t.TempDir()

	info1, err := loadOrCreateNodeWallet(dir)
	if err != nil {
		t.Fatalf("erste Erzeugung: %v", err)
	}
	if !info1.Created {
		t.Fatal("erste Erzeugung sollte Created=true sein")
	}
	if !info1.FromSeed {
		t.Fatal("neue Wallet sollte seed-basiert sein")
	}
	if len(info1.SeedWords) != 30 {
		t.Fatalf("erwartet 30 Seed-Wörter, bekam %d", len(info1.SeedWords))
	}

	info2, err := loadOrCreateNodeWallet(dir)
	if err != nil {
		t.Fatalf("zweites Laden: %v", err)
	}
	if info2.Created {
		t.Fatal("zweites Laden sollte Created=false sein")
	}
	if info2.SeedWords != nil {
		t.Fatal("beim Laden dürfen keine Seed-Wörter zurückkommen")
	}
	if info1.Address != info2.Address {
		t.Fatalf("Adresse instabil: %s != %s", info1.Address, info2.Address)
	}
}

// Die node.seed-Datei muss streng 0600 sein (nur Besitzer).
func TestNodeSeedFilePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrCreateNodeWallet(dir); err != nil {
		t.Fatalf("Erzeugung: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, nodeSeedFile))
	if err != nil {
		t.Fatalf("node.seed nicht gefunden: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("node.seed Rechte = %o, erwartet 600", perm)
	}
}

// Leeres Verzeichnis → Fehler.
func TestNodeWalletEmptyDir(t *testing.T) {
	if _, err := loadOrCreateNodeWallet(""); err == nil {
		t.Fatal("leeres Verzeichnis sollte Fehler geben")
	}
}

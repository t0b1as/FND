package chain

// Migration alter JSON-Block-Dateien (blocks/*.json) in den binären
// Blockstore (bbolt, chain.db). Manuell ausgelöst via 'fundus-admin migrate-chain'.
//
// Sicherheit: Die Blöcke werden inhaltlich NICHT verändert — nur der Container
// (JSON-Datei → binärer DB-Eintrag). Nach der Migration wird der State per Replay
// aus der DB neu aufgebaut und der Head-Hash gegen meta.json geprüft. Stimmt er,
// ist bewiesen, dass die Migration verlustfrei war. Die alten JSON-Dateien werden
// NICHT gelöscht (bleiben als Backup, bis manuell entfernt).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MigrateJSONToStore liest alle JSON-Blöcke unter dir/blocks/ und schreibt sie
// binär in den Blockstore dir/chain.db. Gibt die Anzahl migrierter Blöcke zurück.
// progress (optional) wird pro Block mit der aktuellen Höhe aufgerufen.
func MigrateJSONToStore(dir string, progress func(height, total uint64)) (uint64, error) {
	// meta.json bestimmt die höchste Höhe und den erwarteten Head-Hash.
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return 0, fmt.Errorf("migrate: meta.json nicht lesbar (keine Chain vorhanden?): %w", err)
	}
	var m struct {
		Height   uint64 `json:"height"`
		HeadHash string `json:"head_hash"`
	}
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		return 0, fmt.Errorf("migrate: meta.json ungültig: %w", err)
	}

	store, err := OpenBlockStore(dir)
	if err != nil {
		return 0, err
	}
	defer store.Close()

	// Bereits migriert? Vergleich der tatsächlich vorhandenen Daten (JSON-Höhe
	// gegen DB-Höhe). meta.json wird für diese Entscheidung NICHT mehr
	// befragt: sie wird beim Start neu geschrieben und stand nach einem
	// Fehlstart auf 0 — daher die Meldung "enthält bereits Höhe 0".
	mig, err := InspectMigration(dir, store)
	if err != nil {
		return 0, err
	}
	if !NeedsMigration(mig) {
		return 0, fmt.Errorf("migrate: nichts zu tun (JSON bis Höhe %d, Blockstore bis %d)", mig.JSONHeight, mig.DBHeight)
	}

	// JSON-Blöcke einlesen (nur Datei-Zugriff, kein Store) und binär schreiben.
	var count uint64
	for h := uint64(0); h <= m.Height; h++ {
		blk, err := loadBlockJSONFile(dir, h)
		if err != nil {
			return count, fmt.Errorf("migrate: Block %d lesen: %w", h, err)
		}
		if err := store.Put(h, blk); err != nil {
			return count, fmt.Errorf("migrate: Block %d schreiben: %w", h, err)
		}
		count++
		if progress != nil {
			progress(h, m.Height)
		}
	}
	return count, nil
}

// loadBlockJSONFile liest einen einzelnen JSON-Block direkt von der Platte, ohne
// eine Blockchain-Instanz. Für die Migration.
func loadBlockJSONFile(dir string, height uint64) (*Block, error) {
	path := filepath.Join(dir, "blocks", fmt.Sprintf("%020d.json", height))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w wireBlock
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	return wireToBlock(&w)
}

// VerifyStoreAgainstMeta baut den State per Replay aus dem Blockstore auf und
// prüft, ob der resultierende Head-Hash zu meta.json passt. Beweist, dass die
// Migration verlustfrei war. Aufzurufen NACH MigrateJSONToStore. Der Fee-Collector
// wird aus dem Genesis-Header gelesen (einzige Quelle der Wahrheit).
func VerifyStoreAgainstMeta(dir string) error {
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return err
	}
	var m struct {
		Height   uint64 `json:"height"`
		HeadHash string `json:"head_hash"`
	}
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		return err
	}
	store, err := OpenBlockStore(dir)
	if err != nil {
		return err
	}
	defer store.Close()

	gen, err := store.Get(0)
	if err != nil {
		return fmt.Errorf("verify: Genesis lesen: %w", err)
	}
	feeCollector := gen.Header.FeeCollector
	st := NewState()
	for _, a := range gen.GenesisAllocs {
		st.Credit(a.Address, a.Balance)
	}
	head := &gen.Header
	for h := uint64(1); h <= m.Height; h++ {
		blk, err := store.Get(h)
		if err != nil {
			return fmt.Errorf("verify: Block %d lesen: %w", h, err)
		}
		if err := ApplyBlock(head, blk, st, feeCollector); err != nil {
			return fmt.Errorf("verify: Block %d anwenden: %w", h, err)
		}
		head = &blk.Header
	}
	if head.Hash() != hexTo32(m.HeadHash) {
		return fmt.Errorf("verify: Head-Hash nach Replay aus DB weicht von meta.json ab — Migration NICHT verlustfrei")
	}
	return nil
}

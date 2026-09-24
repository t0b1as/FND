package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// ─── Chain-Synchronisation (Spec §11, Multi-Node) ────────────────────────────
//
// Diese exportierten Methoden ermöglichen es einem Node, fehlende Blöcke von
// einem Peer zu beziehen und einzuspielen. Die Validierung läuft über dasselbe
// ApplyBlock wie bei eigener Produktion — ein importierter Block ist also genauso
// streng geprüft (Höhe, prev_hash, tx_root, state_root) wie ein selbst gebauter.

// GetBlock gibt den Block einer Höhe zurück (für Peer-Anfragen). Höhe 0 = Genesis
// wird NICHT über diesen Weg geliefert (Genesis ist deterministisch/lokal).
func (bc *Blockchain) GetBlock(height uint64) (*Block, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if height == 0 {
		return nil, errors.New("chain: Genesis wird nicht über Sync geliefert")
	}
	if height > bc.height {
		return nil, errors.New("chain: Höhe über Head")
	}
	return bc.loadBlock(height)
}

// ExportBlockJSON serialisiert den Block einer Höhe als JSON (Wire-Format) zur
// Übertragung an einen Peer.
func (bc *Blockchain) ExportBlockJSON(height uint64) ([]byte, error) {
	blk, err := bc.GetBlock(height)
	if err != nil {
		return nil, err
	}
	return json.Marshal(blockToWire(blk))
}

// ImportBlockJSON nimmt einen per JSON (Wire-Format) übertragenen Block entgegen,
// validiert ihn gegen den aktuellen Head und spielt ihn ein. Nur der direkte
// Nachfolger (head.Height+1) wird akzeptiert — Lücken muss der Aufrufer der
// Reihe nach schließen. Idempotent gegenüber bereits bekannten Höhen.
func (bc *Blockchain) ImportBlockJSON(data []byte) error {
	var w wireBlock
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	blk, err := wireToBlock(&w)
	if err != nil {
		return err
	}
	return bc.ImportBlock(blk)
}

// ImportBlock validiert + übernimmt einen Block als neuen Head. Akzeptiert nur
// den direkten Nachfolger; ältere/gleiche Höhen werden ignoriert (kein Fehler),
// Lücken (Höhe > head+1) werden abgelehnt.
func (bc *Blockchain) ImportBlock(blk *Block) error {
	if blk == nil {
		return errors.New("chain: leerer Block")
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if blk.Header.Height <= bc.height {
		// Konkurrierender Block auf einer bereits belegten Höhe? Wenn Hash und
		// Vorgänger identisch sind, ist es schlicht derselbe Block (idempotent,
		// harmlos). Weicht der Hash ab, liegt eine ECHTE Kollision vor — zwei
		// legitim signierte Blöcke für dieselbe Höhe (z.B. primärer Proposer +
		// Fallback bei Partition/Uhren-Drift). Wir können den bereits
		// angewendeten Block ohne Reorg nicht ersetzen, melden die Kollision aber
		// als erkennbaren, NICHT-fatalen Hinweis, damit der Sync-Layer sie loggen
		// kann. Fork-Choice-Regel "niedrigere Runde gewinnt" greift präventiv nur
		// VOR dem Anwenden (siehe Doku/Grenzen — echter Reorg ist Future Work).
		if blk.Header.Height == bc.height && bc.head != nil && blk.Header.Hash() != bc.head.Hash() {
			bc.forkCollisions++
			// Double-Sign-Prüfung: Stammen BEIDE Blöcke vom SELBEN Signierer?
			// Nur dann ist es beweisbares Double-Signing (Equivocation). Zwei
			// VERSCHIEDENE Proposer (primär + Fallback) für dieselbe Höhe sind ein
			// legitimer Fork, KEIN Fehlverhalten — den lösen wir nicht per Slash.
			bc.maybeReportDoubleSign(blk)
			return fmt.Errorf("chain: konkurrierender Block auf Höhe %d verworfen (eigener Round %d, fremder Round %d) — mögliche Fork",
				blk.Header.Height, bc.head.Round, blk.Header.Round)
		}
		return nil // schon bekannt — idempotent
	}
	if blk.Header.Height != bc.height+1 {
		return errors.New("chain: Lücke — Block ist nicht der direkte Nachfolger")
	}
	// PoA-Konsens: Nur Blöcke vom berechtigten, signierenden Proposer annehmen.
	// Schützt davor, dass ein nicht autorisierter Node der Kette Blöcke unterschiebt.
	// Zeitbewusst: Fallback-Runden (falls der primäre Proposer ausfiel) werden
	// gegen den Zeitstempel des aktuellen Head (= Vorgängerblock) plausibilisiert.
	if bc.valSet != nil {
		var prevTS uint64
		if bc.head != nil {
			prevTS = bc.head.Timestamp
		}
		if err := VerifyBlockConsensusAt(blk, bc.valSet, prevTS); err != nil {
			return fmt.Errorf("chain: Konsens-Prüfung fehlgeschlagen: %w", err)
		}
	}
	// Volle Validierung gegen den aktuellen Head (Höhe, prev_hash, tx_root,
	// state_root) auf dem echten State.
	if err := ApplyBlock(bc.head, blk, bc.state, bc.feeCollector); err != nil {
		return err
	}
	if err := bc.persistBlock(blk); err != nil {
		return err
	}
	bc.head = &blk.Header
	bc.height = blk.Header.Height
	// Validator-Set nach dem Block neu ableiten (Stake kann sich geändert haben).
	// IDENTISCHE Position wie in ProduceBlockRound — sonst forkt die Kette.
	bc.refreshValidatorSetLocked()
	return bc.saveMeta()
}

// GenesisHeaderHash gibt den Hash des Genesis-Headers zurück (Höhe 0). Peers
// vergleichen diesen, bevor sie synchronisieren — bei Abweichung gehören sie zu
// unterschiedlichen Chains und dürfen NICHT mischen.
func (bc *Blockchain) GenesisHeaderHash() [32]byte {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.genesisHash
}

// GetEscrow gibt eine Kopie eines Escrows aus dem aktuellen State zurück (für die
// API/Vertragsvorschau). false, wenn unbekannt oder bereits geschlossen+gepruned.
func (bc *Blockchain) GetEscrow(id [32]byte) (Escrow, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.state.GetEscrow(id)
}

// HasEscrowForContent prüft (thread-sicher), ob ein Listing mit diesem Content-
// Hash bereits einen Escrow hat — der aus der Chain abgeleitete Verkauft-Status.
func (bc *Blockchain) HasEscrowForContent(contentHash [32]byte) bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.state == nil {
		return false
	}
	return bc.state.HasEscrowForContent(contentHash)
}

// GetHTLC liefert einen HTLC per ID (Blockchain-Wrapper für die API).
func (bc *Blockchain) GetHTLC(id [32]byte) (HTLC, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.state == nil {
		return HTLC{}, false
	}
	return bc.state.GetHTLC(id)
}

// FindHTLCByHashlock sucht einen HTLC per Hashlock + Empfänger (für den Swap-
// Orchestrator).
func (bc *Blockchain) FindHTLCByHashlock(hashlock [32]byte, recipient Address) ([32]byte, HTLC, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.state == nil {
		return [32]byte{}, HTLC{}, false
	}
	return bc.state.FindHTLCByHashlock(hashlock, recipient)
}

// ListHTLCs gibt alle HTLCs zurück (Diagnose).
func (bc *Blockchain) ListHTLCs() []HTLC {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.state == nil {
		return nil
	}
	out := make([]HTLC, 0, len(bc.state.htlcs))
	for _, h := range bc.state.htlcs {
		cp := *h
		cp.Amount = new(big.Int).Set(h.Amount)
		out = append(out, cp)
	}
	return out
}

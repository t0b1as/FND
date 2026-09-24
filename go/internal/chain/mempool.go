package chain

import (
	"errors"
	"sync"
)

// Mempool sammelt validierte, noch nicht in einem Block enthaltene
// Transaktionen. Phase 2: einfache FIFO-Sammlung mit Dedup nach Tx-Hash.
// Phase 3 ergänzt Gebühren-Priorisierung bei der Block-Auswahl.
type Mempool struct {
	mu   sync.Mutex
	txs  []*Transaction
	seen map[[32]byte]bool
	max  int
}

// NewMempool erzeugt einen Mempool mit Kapazitätsgrenze.
func NewMempool(max int) *Mempool {
	if max <= 0 {
		max = 10000
	}
	return &Mempool{seen: make(map[[32]byte]bool), max: max}
}

// Add prüft die Signatur und legt die Tx ab (Dedup nach Hash). Die volle
// State-Validierung (Nonce, Guthaben) erfolgt erst bei der Block-Anwendung;
// hier nur die billige Signaturprüfung als Spam-/Müll-Filter.
func (m *Mempool) Add(tx *Transaction) error {
	// TxStorageReward (Mint) darf NIEMALS über den Mempool eingereicht werden.
	// Dieser Tx-Typ wird ausschließlich vom Block-Produzenten beim Blockbau aus
	// den lokal gesammelten Quittungen erzeugt. Würde der Mempool ihn annehmen,
	// könnte ein Angreifer einen selbstgebauten Reward-Tx einschleusen, der beim
	// nächsten Block zusätzlich angewendet wird. Die Quittungs-Verifikation in
	// applyStorageReward fängt gefälschte Quittungen zwar ab, aber wir lehnen den
	// Tx-Typ hier grundsätzlich ab (Defense in Depth).
	if tx.Type == TxStorageReward {
		return errors.New("mempool: Storage-Reward kann nicht eingereicht werden (nur Produzent)")
	}
	if err := tx.VerifySignature(); err != nil {
		return err
	}
	h := tx.Hash()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen[h] {
		return errors.New("mempool: Transaktion bereits bekannt")
	}
	if len(m.txs) >= m.max {
		return errors.New("mempool: voll")
	}
	m.seen[h] = true
	m.txs = append(m.txs, tx)
	return nil
}

// Take entnimmt bis zu n Transaktionen (für die Block-Produktion) und entfernt
// sie aus dem Pool.
func (m *Mempool) Take(n int) []*Transaction {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 || n > len(m.txs) {
		n = len(m.txs)
	}
	out := make([]*Transaction, n)
	copy(out, m.txs[:n])
	rest := m.txs[n:]
	m.txs = make([]*Transaction, len(rest))
	copy(m.txs, rest)
	for _, tx := range out {
		delete(m.seen, tx.Hash())
	}
	return out
}

// Len liefert die Anzahl wartender Transaktionen.
func (m *Mempool) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.txs)
}

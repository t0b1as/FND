package filestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

// =============================================================================
//  receiptStore — Sammlung verdienter Quittungen (Provider-Seite)
// =============================================================================
//
// Dieser Node erbringt Storage-/Transfer-Leistung für andere und sammelt dafür
// vom KONSUMENTEN signierte Quittungen. Sie sind der fälschungssichere Nachweis,
// den der Node später beim Minten vorlegt (Phase 3). Quittungen werden:
//   - dedupliziert per ReceiptID (gegen Doppeleinreichung),
//   - persistiert (übersteht Neustart),
//   - erst beim Minten entwertet (markiert/entfernt).

type receiptStore struct {
	mu       sync.Mutex
	log      *zap.Logger
	path     string                       // Datei für die Persistenz (JSON)
	pending  map[[32]byte]chain.Receipt   // ReceiptID → Quittung (noch nicht gemintet)
}

// newReceiptStore lädt vorhandene Quittungen aus dem DataDir oder legt einen
// leeren Speicher an. Fehler beim Laden werden geloggt, aber nicht fatal.
func newReceiptStore(dataDir string, log *zap.Logger) *receiptStore {
	rs := &receiptStore{
		log:     log,
		path:    filepath.Join(dataDir, "receipts.json"),
		pending: make(map[[32]byte]chain.Receipt),
	}
	rs.load()
	return rs
}

// Add prüft und speichert eine eingegangene Quittung. Gibt true zurück, wenn die
// Quittung neu und gültig war (also gezählt wurde), false bei Duplikat/Fehler.
// Der Aufrufer hat zuvor sicherzustellen, dass die Quittung diesen Node als
// Provider ausweist.
func (rs *receiptStore) Add(r chain.Receipt) bool {
	if err := r.VerifyReceipt(); err != nil {
		if rs.log != nil {
			rs.log.Warn("Ungültige Quittung verworfen", zap.Error(err))
		}
		return false
	}
	id := r.ReceiptID()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, exists := rs.pending[id]; exists {
		return false // Doppeleinreichung
	}
	rs.pending[id] = r
	rs.persistLocked()
	return true
}

// PendingBytes summiert die noch nicht geminteten Bytes, getrennt nach Art.
// Basis für die Verdienst-Berechnung beim Minten.
func (rs *receiptStore) PendingBytes() (fetchBytes, storeBytes uint64) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.pending {
		switch r.Kind {
		case chain.ReceiptFetch:
			fetchBytes += r.Bytes
		case chain.ReceiptStore:
			storeBytes += r.Bytes
		}
	}
	return
}

// Snapshot liefert eine Kopie aller offenen Quittungen (für das Minten).
func (rs *receiptStore) Snapshot() []chain.Receipt {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]chain.Receipt, 0, len(rs.pending))
	for _, r := range rs.pending {
		out = append(out, r)
	}
	return out
}

// Count gibt die Zahl offener Quittungen zurück.
func (rs *receiptStore) Count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.pending)
}

// Settle entfernt die genannten Quittungen (nach erfolgreichem Mint).
func (rs *receiptStore) Settle(ids [][32]byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, id := range ids {
		delete(rs.pending, id)
	}
	rs.persistLocked()
}

// --- Persistenz -------------------------------------------------------------

// receiptFileEntry ist die serialisierbare Form (Map-Keys [32]byte gehen nicht
// direkt nach JSON, daher Liste).
func (rs *receiptStore) persistLocked() {
	list := make([]chain.Receipt, 0, len(rs.pending))
	for _, r := range rs.pending {
		list = append(list, r)
	}
	data, err := json.Marshal(list)
	if err != nil {
		if rs.log != nil {
			rs.log.Warn("Quittungen serialisieren fehlgeschlagen", zap.Error(err))
		}
		return
	}
	tmp := rs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		if rs.log != nil {
			rs.log.Warn("Quittungen schreiben fehlgeschlagen", zap.Error(err))
		}
		return
	}
	_ = os.Rename(tmp, rs.path) // atomar
}

func (rs *receiptStore) load() {
	data, err := os.ReadFile(rs.path)
	if err != nil {
		return // existiert noch nicht — normal beim ersten Start
	}
	var list []chain.Receipt
	if err := json.Unmarshal(data, &list); err != nil {
		if rs.log != nil {
			rs.log.Warn("Quittungen laden fehlgeschlagen, starte leer", zap.Error(err))
		}
		return
	}
	for _, r := range list {
		// Beim Laden erneut verifizieren — schützt vor manipulierter Datei.
		if r.VerifyReceipt() == nil {
			rs.pending[r.ReceiptID()] = r
		}
	}
}

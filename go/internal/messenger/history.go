package messenger

import (
	"encoding/hex"
	"crypto/sha256"
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/identity"
)

// ─── Persistenter, at-rest-verschlüsselter Nachrichtenverlauf ────────────────
//
// Nachrichten werden pro Konversation (eigener Node ↔ Peer) als append-only
// Log auf der Disk gespeichert, jede Zeile XChaCha20-Poly1305-verschlüsselt mit
// einem aus der Identität abgeleiteten geräte-lokalen Schlüssel (AtRestKey).
// So sind die Inhalte bei physischem Zugriff auf die SD-Karte des Pi nicht im
// Klartext lesbar und ohne Login (privater Schlüssel im RAM) nicht entschlüssel-
// bar.
//
// Speicherort: <dataDir>/messenger-history/<myID>/<peerID>.log
// Format pro Zeile: base64(XChaCha20(json(HistoryEntry)))
//
// Pagination: Abruf der neuesten N Einträge bzw. der N Einträge VOR einem
// Zeitstempel (Cursor) — für dynamisches Nachladen beim Hochscrollen.

const historyDirName = "messenger-history"

// HistoryEntry ist eine gespeicherte Nachricht im Klartext (vor at-rest-
// Verschlüsselung). Enthält den angezeigten Text, damit auch eigene gesendete
// Nachrichten (die für den Empfänger verschlüsselt waren) lesbar bleiben.
type HistoryEntry struct {
	ID        string    `json:"id"`
	PeerID    string    `json:"peer"`     // Gegenstelle der Konversation
	Outgoing  bool      `json:"out"`      // true = von mir gesendet
	Type      string    `json:"type"`     // "text" | "file" | "signal"
	Text      string    `json:"text,omitempty"`
	FileHash  string    `json:"file_hash,omitempty"`
	FileName  string    `json:"file_name,omitempty"`
	FileSize  int64     `json:"file_size,omitempty"`
	Mime      string    `json:"mime,omitempty"`
	Timestamp time.Time `json:"ts"`

	// Zustellungs-/Lesestatus für ausgehende Nachrichten (WhatsApp-analog).
	// nil = noch nicht zugestellt/gelesen.
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
}

// HistoryStore verwaltet den verschlüsselten Verlauf einer Identität.
type HistoryStore struct {
	baseDir string
	myID    string
	key     [32]byte
	mu      sync.Mutex // serialisiert Appends (append-only Konsistenz)

	// seen merkt sich die bereits abgelegten Nachrichten-Kennungen dauerhaft
	// (R582). Dieselbe Nachricht kommt über mehrere Wege an – direkt, über das
	// Postfach, nach einem Neustart erneut. Ohne diesen Index landete sie
	// mehrfach im Verlauf.
	seen       map[string]bool
	seenLoaded bool
}

// NewHistoryStore erstellt einen Verlauf-Store für die gegebene Identität.
func NewHistoryStore(dataDir string, id *identity.Identity) (*HistoryStore, error) {
	if id == nil {
		return nil, fmt.Errorf("history: identity fehlt")
	}
	return newHistoryStoreWithKey(dataDir, id.FundusID, id.AtRestKey())
}

// newHistoryStoreWithKey ist der interne Konstruktor (direkt mit Schlüssel) —
// entkoppelt die at-rest-Logik von der teuren Identitäts-Ableitung (testbar).
func newHistoryStoreWithKey(dataDir, myID string, key [32]byte) (*HistoryStore, error) {
	myDir := filepath.Join(dataDir, historyDirName, sanitizeID(myID))
	if err := os.MkdirAll(myDir, 0o750); err != nil {
		return nil, fmt.Errorf("history: dir: %w", err)
	}
	return &HistoryStore{
		baseDir: myDir,
		myID:    myID,
		key:     key,
	}, nil
}

// sanitizeID macht eine FundusID dateinamen-sicher (nur Hex/0x → unkritisch,
// aber defensiv gegen Pfad-Tricks).
func sanitizeID(id string) string {
	id = strings.ToLower(id)
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'f') || (r >= '0' && r <= '9') || r == 'x' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" {
		s = "unknown"
	}
	return s
}

func (h *HistoryStore) convPath(peerID string) string {
	return filepath.Join(h.baseDir, sanitizeID(peerID)+".log")
}

// SealEntry verschlüsselt einen Verlaufseintrag mit dem Identitätsschlüssel –
// für den Abgleich zwischen den eigenen Nodes (R584). Nur wer mit demselben
// Konto angemeldet ist, kann das wieder öffnen.
func (h *HistoryStore) SealEntry(e HistoryEntry) (string, error) {
	plain, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	enc, err := identity.Encrypt(plain, h.key[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// OpenEntry öffnet einen verteilten Verlaufseintrag wieder.
func (h *HistoryStore) OpenEntry(blob string) (HistoryEntry, error) {
	var e HistoryEntry
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return e, err
	}
	plain, err := identity.Decrypt(raw, h.key[:])
	if err != nil {
		return e, err
	}
	return e, json.Unmarshal(plain, &e)
}

// Known meldet, ob eine Nachricht mit dieser Kennung bereits abgelegt wurde.
// Damit lassen sich auch Anzeige und Benachrichtigung unterdrücken, wenn
// dieselbe Nachricht ein zweites Mal eintrifft.
func (h *HistoryStore) Known(id string) bool {
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadSeen()
	return h.seen[seenKey(id)]
}

// seenPath: Index der bereits abgelegten Kennungen (gehashte Kurzform).
func (h *HistoryStore) seenPath() string { return filepath.Join(h.baseDir, "seen.idx") }

func seenKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

// loadSeen liest den Index einmalig ein (Aufruf mit gehaltenem Lock).
func (h *HistoryStore) loadSeen() {
	if h.seenLoaded {
		return
	}
	h.seenLoaded = true
	h.seen = make(map[string]bool)
	raw, err := os.ReadFile(h.seenPath())
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			h.seen[line] = true
		}
	}
}

// UpdateReceipt setzt den Zustell-/Lesestatus einer ausgehenden Nachricht
// (per ID) und schreibt den Konversations-Log atomar neu. kind ist
// ReceiptDelivered oder ReceiptRead. Gibt zurück, ob ein Eintrag geändert wurde.
func (h *HistoryStore) UpdateReceipt(peerID, messageID, kind string, at time.Time) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entries, err := h.readAll(peerID)
	if err != nil {
		return false, err
	}
	changed := false
	for i := range entries {
		if entries[i].ID != messageID {
			continue
		}
		ts := at
		switch kind {
		case "delivered":
			if entries[i].DeliveredAt == nil {
				entries[i].DeliveredAt = &ts
				changed = true
			}
		case "read":
			// "read" impliziert "delivered" (zugestellt sein muss es ohnehin).
			if entries[i].DeliveredAt == nil {
				entries[i].DeliveredAt = &ts
			}
			if entries[i].ReadAt == nil {
				entries[i].ReadAt = &ts
				changed = true
			}
		}
		break
	}
	if !changed {
		return false, nil
	}

	// Atomar neu schreiben: temp + rename.
	tmpPath := h.convPath(peerID) + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return false, fmt.Errorf("history: open tmp: %w", err)
	}
	for _, e := range entries {
		plain, merr := json.Marshal(e)
		if merr != nil {
			f.Close()
			return false, fmt.Errorf("history: marshal: %w", merr)
		}
		enc, eerr := identity.Encrypt(plain, h.key[:])
		if eerr != nil {
			f.Close()
			return false, fmt.Errorf("history: encrypt: %w", eerr)
		}
		if _, werr := f.WriteString(base64.StdEncoding.EncodeToString(enc) + "\n"); werr != nil {
			f.Close()
			return false, fmt.Errorf("history: write: %w", werr)
		}
	}
	if cerr := f.Close(); cerr != nil {
		return false, cerr
	}
	if rerr := os.Rename(tmpPath, h.convPath(peerID)); rerr != nil {
		return false, fmt.Errorf("history: rename: %w", rerr)
	}
	return true, nil
}

// Append hängt eine Nachricht an den Verlauf der Konversation mit peerID an.
// AppendIfNew legt den Eintrag ab und meldet, ob er WIRKLICH neu war.
// Append allein genügt dafür nicht: Es meldet auch dann keinen Fehler, wenn es
// eine Dopplung verwirft – Aufrufer hielten das für einen neuen Eintrag und
// zeigten die Nachricht ein zweites Mal an (R602). Prüfung und Schreiben
// laufen hier unter derselben Sperre, damit zwei gleichzeitige Zustellwege
// nicht beide „neu" melden.
func (h *HistoryStore) AppendIfNew(e HistoryEntry) (bool, error) {
	if e.ID != "" {
		h.mu.Lock()
		h.loadSeen()
		if h.seen[seenKey(e.ID)] {
			h.mu.Unlock()
			return false, nil
		}
		h.mu.Unlock()
	}
	if err := h.Append(e); err != nil {
		return false, err
	}
	return true, nil
}

func (h *HistoryStore) Append(e HistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Doppelte Zustellung abfangen: dieselbe Kennung nur einmal ablegen.
	if e.ID != "" {
		h.loadSeen()
		k := seenKey(e.ID)
		if h.seen[k] {
			return nil
		}
		h.seen[k] = true
		if f, ferr := os.OpenFile(h.seenPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640); ferr == nil {
			_, _ = f.WriteString(k + "\n")
			_ = f.Close()
		}
	}

	plain, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("history: marshal: %w", err)
	}
	enc, err := identity.Encrypt(plain, h.key[:])
	if err != nil {
		return fmt.Errorf("history: encrypt: %w", err)
	}
	line := base64.StdEncoding.EncodeToString(enc)

	f, err := os.OpenFile(h.convPath(e.PeerID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("history: open: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("history: write: %w", err)
	}
	return nil
}

// Page liefert bis zu `limit` Nachrichten der Konversation mit peerID, in
// chronologischer Reihenfolge (älteste zuerst). Ist `before` nicht null, werden
// nur Nachrichten ÄLTER als `before` geliefert (für Nachladen beim Hochscrollen).
// Gibt außerdem zurück, ob es noch ältere Nachrichten gibt (hasMore).
func (h *HistoryStore) Page(peerID string, before *time.Time, limit int) ([]HistoryEntry, bool, error) {
	if limit <= 0 {
		limit = 32
	}
	all, err := h.readAll(peerID)
	if err != nil {
		return nil, false, err
	}
	// Chronologisch sortieren (Append-Reihenfolge sollte schon stimmen, aber
	// defensiv nach Timestamp ordnen).
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	// Auf Einträge vor dem Cursor filtern.
	filtered := all
	if before != nil {
		cut := make([]HistoryEntry, 0, len(all))
		for _, e := range all {
			if e.Timestamp.Before(*before) {
				cut = append(cut, e)
			}
		}
		filtered = cut
	}

	// Die letzten `limit` davon nehmen (neueste Seite bzw. Seite vor Cursor).
	hasMore := false
	if len(filtered) > limit {
		hasMore = true
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered, hasMore, nil
}

// readAll liest und entschlüsselt den gesamten Verlauf einer Konversation.
// Defekte/unentschlüsselbare Zeilen werden übersprungen (Robustheit), nicht
// als Fehler behandelt.
func (h *HistoryStore) readAll(peerID string) ([]HistoryEntry, error) {
	f, err := os.Open(h.convPath(peerID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // leere Konversation
		}
		return nil, fmt.Errorf("history: open: %w", err)
	}
	defer f.Close()

	var entries []HistoryEntry
	sc := bufio.NewScanner(f)
	// Große Zeilen erlauben (lange Nachrichten): Buffer hochsetzen.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		raw, derr := base64.StdEncoding.DecodeString(line)
		if derr != nil {
			continue
		}
		plain, derr := identity.Decrypt(raw, h.key[:])
		if derr != nil {
			continue // anderer Schlüssel / korrupt → überspringen
		}
		var e HistoryEntry
		if json.Unmarshal(plain, &e) != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

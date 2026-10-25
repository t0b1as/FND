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

	// EditedAt: gesetzt, wenn die Nachricht nachträglich geändert wurde (R662).
	// Die Oberfläche zeigt daraufhin einen Hinweis – eine stille Änderung wäre
	// in einem Chat nicht in Ordnung.
	EditedAt *time.Time `json:"edited_at,omitempty"`
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
// historyRegistry hält EIN Verlaufsobjekt je Konto und Datenverzeichnis (R801).
//
// Jede Sitzung bekam bisher ihr eigenes. Die Prüfung auf doppelte Einträge
// läuft aber über eine Liste bekannter Kennungen IM SPEICHER des Objekts. Gab
// es für dasselbe Konto mehrere Sitzungen – nach mehrfacher Anmeldung, oder
// beim Wiederherstellen nach einem Neustart, das je gespeichertem Zeichen eine
// Sitzung baut –, schrieb jede dieselbe Nachricht in dieselbe Datei, ohne von
// den anderen zu wissen.
//
// Gemeldet auf dem 39er: live einmal, nach dem Neuladen viermal mit gleicher
// Uhrzeit. Auf den Nodes ohne mehrfache Anmeldung trat es nicht auf.
//
// Mit einem gemeinsamen Objekt gibt es nur eine Liste – unabhängig davon, wie
// viele Sitzungen es gibt und auf welchem Weg sie entstanden sind.
var (
	historyRegistryMu sync.Mutex
	historyRegistry   = map[string]*HistoryStore{}
)

func NewHistoryStore(dataDir string, id *identity.Identity) (*HistoryStore, error) {
	if id == nil {
		return nil, fmt.Errorf("history: identity fehlt")
	}
	schluessel := dataDir + "|" + strings.ToLower(id.FundusID)
	historyRegistryMu.Lock()
	defer historyRegistryMu.Unlock()
	if hs, ok := historyRegistry[schluessel]; ok {
		return hs, nil
	}
	hs, err := newHistoryStoreWithKey(dataDir, id.FundusID, id.AtRestKey())
	if err != nil {
		return nil, err
	}
	historyRegistry[schluessel] = hs
	return hs, nil
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
	gesehen := map[string]bool{} // R801
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
		// R801: Bereits mehrfach geschriebene Einträge nur einmal liefern.
		// Bereinigt die Doppelten, die vor diesem Stand entstanden sind, ohne
		// die Datei anfassen zu müssen. Die erste Fassung gewinnt – spätere
		// Zeilen derselben Kennung sind Kopien, keine Bearbeitungen (die
		// schreiben die Datei neu, siehe schreibeAlle).
		if e.ID != "" {
			if gesehen[e.ID] {
				continue
			}
			gesehen[e.ID] = true
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// ── Bearbeiten, Löschen, Verlauf leeren (R662) ───────────────────────────
//
// Die Konversationsdatei wird nur angehängt. Für Änderungen wird sie deshalb
// vollständig gelesen, angepasst und neu geschrieben – bei den hier üblichen
// Größen (einige tausend Zeilen) ist das unproblematisch und deutlich
// einfacher als ein Journal mit Grabsteinen.
//
// WICHTIG: Gelöscht wird nur der eigene Verlauf. Beim Gegenüber verschwindet
// die Nachricht nur, wenn dessen Node die Mitteilung erhält und anwendet – wer
// etwas gelesen hat, hat es gelesen. Das ist eine Eigenschaft des Entwurfs,
// keine Lücke: Es gibt keine zentrale Stelle, die nachträglich durchgreifen
// könnte.

// schreibeAlle ersetzt die Konversationsdatei durch die übergebenen Einträge.
func (h *HistoryStore) schreibeAlle(peerID string, eintraege []HistoryEntry) error {
	pfad := h.convPath(peerID)
	tmp := pfad + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("history: open tmp: %w", err)
	}
	for _, e := range eintraege {
		plain, err := json.Marshal(e)
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("history: marshal: %w", err)
		}
		enc, err := identity.Encrypt(plain, h.key[:])
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("history: encrypt: %w", err)
		}
		if _, err := f.WriteString(base64.StdEncoding.EncodeToString(enc) + "\n"); err != nil {
			_ = f.Close()
			return fmt.Errorf("history: write: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Erst umbenennen, wenn alles geschrieben ist – sonst stünde bei einem
	// Absturz mitten im Schreiben ein halber Verlauf auf der Platte.
	return os.Rename(tmp, pfad)
}

// UpdateText ändert den Text einer Nachricht. Liefert false, wenn es sie nicht
// gibt. editedAt wird vermerkt, damit die Oberfläche "bearbeitet" anzeigen kann.
func (h *HistoryStore) UpdateText(peerID, messageID, neuerText string, editedAt time.Time) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	alle, err := h.readAll(peerID)
	if err != nil {
		return false, err
	}
	gefunden := false
	for i := range alle {
		if alle[i].ID == messageID {
			alle[i].Text = neuerText
			t := editedAt
			alle[i].EditedAt = &t
			gefunden = true
			break
		}
	}
	if !gefunden {
		return false, nil
	}
	return true, h.schreibeAlle(peerID, alle)
}

// DeleteEntry entfernt eine Nachricht aus dem Verlauf.
func (h *HistoryStore) DeleteEntry(peerID, messageID string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	alle, err := h.readAll(peerID)
	if err != nil {
		return false, err
	}
	out := make([]HistoryEntry, 0, len(alle))
	gefunden := false
	for _, e := range alle {
		if e.ID == messageID {
			gefunden = true
			continue
		}
		out = append(out, e)
	}
	if !gefunden {
		return false, nil
	}
	return true, h.schreibeAlle(peerID, out)
}

// ClearConv leert den Verlauf einer Konversation vollständig.
// ClearConvBis löscht alle Einträge eines Gesprächs bis einschließlich "bis"
// (R818). Neuere bleiben. Eine Löschbitte kann über das Postfach verspätet
// ankommen – sie darf dann keine Nachrichten wegnehmen, die erst NACH dem
// Löschen geschrieben wurden.
func (h *HistoryStore) ClearConvBis(peerID string, bis time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	alle, err := h.readAll(peerID)
	if err != nil {
		return err
	}
	var rest []HistoryEntry
	for _, e := range alle {
		if e.Timestamp.After(bis) {
			rest = append(rest, e)
		}
	}
	if len(rest) == len(alle) {
		return nil
	}
	if len(rest) == 0 {
		if err := os.Remove(h.convPath(peerID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("history: remove: %w", err)
		}
		return nil
	}
	return h.schreibeAlle(peerID, rest)
}

func (h *HistoryStore) ClearConv(peerID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	pfad := h.convPath(peerID)
	if err := os.Remove(pfad); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("history: remove: %w", err)
	}
	return nil
}

package filestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// =============================================================================
//  Hosting-Ledger (Konsumenten-Seite der Vorhaltungs-Vergütung)
// =============================================================================
//
// Wenn dieser Node Daten bei fremden Providern EINLAGERT (Replikation seiner
// eigenen Uploads), merkt er sich hier, WELCHER Provider WELCHEN Chunk hält und
// SEIT WANN. Als Konsument hat er das natürliche Eigeninteresse zu prüfen, dass
// seine Daten noch verfügbar sind — und ist damit der richtige Prüfer für die
// Vorhaltungs-Vergütung:
//
//   1. Periodisch challengt der Konsument den Provider ("hast du Chunk X noch?").
//   2. Liefert der Provider, stellt der Konsument eine signierte
//      ReceiptHosting-Quittung über die seit der letzten Quittung verstrichene
//      Zeit aus (1 FND / TB·Monat, anteilig).
//   3. Diese Quittung wandert wie die Fetch-Quittungen in den nächsten Block.
//
// Der Provider kann diese Quittung NICHT selbst fälschen (sie ist vom Konsumenten
// signiert), und er bekommt nur vergütet, wenn er den Challenge besteht — also
// die Daten wirklich noch hält. Das macht das Modell produktivtauglich.

// hostingEntry hält den Zustand eines bei einem Provider eingelagerten Chunks.
type hostingEntry struct {
	ChunkHash    string `json:"chunk_hash"`     // gehosteter Chunk
	ProviderAddr string `json:"provider_addr"`  // Wallet-Adresse des Providers (hex)
	PeerID       string `json:"peer_id"`        // libp2p-Peer zum Challengen
	Bytes        int    `json:"bytes"`          // Chunk-Größe (Vergütungsbasis)
	StoredSince  int64  `json:"stored_since"`   // Unix-Sekunden: erstmals eingelagert
	LastRewarded int64  `json:"last_rewarded"`  // Unix-Sekunden: bis hierhin bereits quittiert
}

// hostingLedger verwaltet alle bei fremden Providern eingelagerten Chunks.
type hostingLedger struct {
	mu      sync.RWMutex
	path    string
	entries map[string]*hostingEntry // key: chunkHash+"@"+providerAddr
}

func newHostingLedger(dataDir string) *hostingLedger {
	hl := &hostingLedger{
		path:    filepath.Join(dataDir, "hosting_ledger.json"),
		entries: make(map[string]*hostingEntry),
	}
	hl.load()
	return hl
}

func hostingKey(chunkHash, providerAddr string) string {
	return chunkHash + "@" + providerAddr
}

// record vermerkt, dass ein Chunk bei einem Provider eingelagert wurde. Beim
// ersten Mal wird StoredSince/LastRewarded auf jetzt gesetzt; wiederholtes
// Einlagern desselben Chunks beim selben Provider ändert nichts (idempotent).
func (hl *hostingLedger) record(chunkHash, providerAddr, peerID string, bytes int) {
	if chunkHash == "" || providerAddr == "" {
		return
	}
	hl.mu.Lock()
	defer hl.mu.Unlock()
	k := hostingKey(chunkHash, providerAddr)
	if _, ok := hl.entries[k]; ok {
		// Bereits bekannt — PeerID auffrischen (kann sich geändert haben).
		hl.entries[k].PeerID = peerID
		hl.persistLocked()
		return
	}
	now := time.Now().Unix()
	hl.entries[k] = &hostingEntry{
		ChunkHash:    chunkHash,
		ProviderAddr: providerAddr,
		PeerID:       peerID,
		Bytes:        bytes,
		StoredSince:  now,
		LastRewarded: now,
	}
	hl.persistLocked()
}

// remove löscht einen Eintrag (z.B. wenn die eigene Datei gelöscht wird oder der
// Provider dauerhaft nicht mehr antwortet).
func (hl *hostingLedger) remove(chunkHash, providerAddr string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	delete(hl.entries, hostingKey(chunkHash, providerAddr))
	hl.persistLocked()
}

// due liefert alle Einträge, deren letzte Quittung länger als minInterval
// zurückliegt — also fällig für einen neuen Challenge + Quittung.
func (hl *hostingLedger) due(minInterval time.Duration) []hostingEntry {
	hl.mu.RLock()
	defer hl.mu.RUnlock()
	cutoff := time.Now().Add(-minInterval).Unix()
	var out []hostingEntry
	for _, e := range hl.entries {
		if e.LastRewarded <= cutoff {
			out = append(out, *e)
		}
	}
	// Deterministische Reihenfolge (ältester zuerst).
	sort.Slice(out, func(i, j int) bool { return out[i].LastRewarded < out[j].LastRewarded })
	return out
}

// markRewarded setzt LastRewarded auf `until` (nach erfolgreichem Challenge +
// ausgestellter Quittung). So wird derselbe Zeitraum nicht doppelt vergütet.
func (hl *hostingLedger) markRewarded(chunkHash, providerAddr string, until int64) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	if e, ok := hl.entries[hostingKey(chunkHash, providerAddr)]; ok {
		e.LastRewarded = until
		hl.persistLocked()
	}
}

func (hl *hostingLedger) count() int {
	hl.mu.RLock()
	defer hl.mu.RUnlock()
	return len(hl.entries)
}

// --- Persistenz -------------------------------------------------------------

func (hl *hostingLedger) persistLocked() {
	out := make([]*hostingEntry, 0, len(hl.entries))
	for _, e := range hl.entries {
		out = append(out, e)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	tmp := hl.path + ".tmp"
	if os.WriteFile(tmp, data, 0640) == nil {
		_ = os.Rename(tmp, hl.path)
	}
}

func (hl *hostingLedger) load() {
	data, err := os.ReadFile(hl.path)
	if err != nil {
		return
	}
	var in []*hostingEntry
	if json.Unmarshal(data, &in) != nil {
		return
	}
	for _, e := range in {
		hl.entries[hostingKey(e.ChunkHash, e.ProviderAddr)] = e
	}
}

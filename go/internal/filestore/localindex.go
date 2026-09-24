package filestore

// Lokaler Datei-Index: eine einfache, persistente Liste aller eigenen Uploads
// (Hash, Name, Größe, Mime), unabhängig von Wallet/Seed. Der verschlüsselte
// PersonalIndex (DHT) bleibt daneben bestehen für die Cross-Pi-Wiederherstellung
// wenn eine Seed vorhanden ist — der lokale Index sorgt aber dafür, dass die
// Dateiliste IMMER funktioniert, auch ohne eingerichtete Wallet.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// LocalIndexEntry ist ein Eintrag in der lokalen Dateiliste.
type LocalIndexEntry struct {
	Hash     string `json:"hash"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type,omitempty"`
	AddedAt  int64  `json:"added_at"`
	// IsDir markiert einen Ordner-Eintrag: Hash ist dann der Manifest-Hash, über
	// den der ganze Verzeichnisbaum abrufbar ist. Count/Size beziehen sich auf
	// den gesamten Ordnerinhalt.
	IsDir bool `json:"is_dir,omitempty"`
	Count int  `json:"count,omitempty"` // Anzahl Dateien (nur bei Ordnern)
}

// LocalIndex hält die lokale Dateiliste (persistent als JSON).
type LocalIndex struct {
	mu      sync.RWMutex
	path    string
	entries map[string]*LocalIndexEntry // hash → Eintrag
}

// NewLocalIndex lädt die lokale Dateiliste aus DataDir (oder beginnt leer).
func NewLocalIndex(dataDir string) *LocalIndex {
	li := &LocalIndex{
		path:    filepath.Join(dataDir, "fileindex.json"),
		entries: make(map[string]*LocalIndexEntry),
	}
	if raw, err := os.ReadFile(li.path); err == nil {
		var list []*LocalIndexEntry
		if json.Unmarshal(raw, &list) == nil {
			for _, e := range list {
				if e != nil && e.Hash != "" {
					li.entries[e.Hash] = e
				}
			}
		}
	}
	return li
}

func (li *LocalIndex) persist() {
	list := make([]*LocalIndexEntry, 0, len(li.entries))
	for _, e := range li.entries {
		list = append(list, e)
	}
	if data, err := json.Marshal(list); err == nil {
		_ = os.WriteFile(li.path, data, 0o640)
	}
}

// Add fügt einen Eintrag hinzu (idempotent per Hash).
func (li *LocalIndex) Add(hash, name string, size int64, mime string) {
	if hash == "" {
		return
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	if _, exists := li.entries[hash]; exists {
		return
	}
	li.entries[hash] = &LocalIndexEntry{
		Hash: hash, Name: name, Size: size, MimeType: mime,
		AddedAt: time.Now().Unix(),
	}
	li.persist()
}

// AddDir trägt einen Ordner-Eintrag ein (Hash ist der Manifest-Hash). count und
// size beziehen sich auf den gesamten Ordnerinhalt.
func (li *LocalIndex) AddDir(manifestHash, name string, totalSize int64, count int) {
	if manifestHash == "" {
		return
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	li.entries[manifestHash] = &LocalIndexEntry{
		Hash: manifestHash, Name: name, Size: totalSize,
		MimeType: "application/x-fundus-dir", AddedAt: time.Now().Unix(),
		IsDir: true, Count: count,
	}
	li.persist()
}

// Remove entfernt einen Eintrag.
func (li *LocalIndex) Remove(hash string) {
	li.mu.Lock()
	defer li.mu.Unlock()
	delete(li.entries, hash)
	li.persist()
}

// List liefert alle Einträge (neueste zuerst).
func (li *LocalIndex) List() []LocalIndexEntry {
	li.mu.RLock()
	defer li.mu.RUnlock()
	out := make([]LocalIndexEntry, 0, len(li.entries))
	for _, e := range li.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt > out[j].AddedAt })
	return out
}

// Count liefert die Anzahl der eigenen Dateien.
func (li *LocalIndex) Count() int {
	li.mu.RLock()
	defer li.mu.RUnlock()
	return len(li.entries)
}

// NameForHash liefert den gespeicherten Dateinamen (inkl. Endung) zu einem Hash,
// falls die Datei im lokalen Index steht. Leer, wenn unbekannt.
func (li *LocalIndex) NameForHash(hash string) string {
	li.mu.RLock()
	defer li.mu.RUnlock()
	if e, ok := li.entries[hash]; ok {
		return e.Name
	}
	return ""
}

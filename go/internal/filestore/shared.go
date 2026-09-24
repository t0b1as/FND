package filestore

// Öffentlich geteilte Dateien: ein Node kann eigene Dateien als "geteilt"
// markieren. Diese werden bei einem netzweiten Datei-Such-Ping lokal
// durchsucht und als Treffer zurückgegeben. Persistent als JSON unter DataDir.
//
// Bewusst getrennt vom (privaten, verschlüsselten) PersonalIndex: geteilte
// Dateien sind öffentlich auffindbar, der PersonalIndex bleibt privat.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SharedFile beschreibt eine öffentlich auffindbare Datei.
type SharedFile struct {
	Hash      string `json:"hash"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mime_type,omitempty"`
	Encrypted bool   `json:"encrypted"` // .fnde → braucht Passwort beim Empfänger
	SharedAt  int64  `json:"shared_at"` // Unix-Sekunden
}

// SharedStore hält die geteilten Dateien eines Nodes (persistent).
type SharedStore struct {
	mu    sync.RWMutex
	path  string
	files map[string]*SharedFile // hash → Eintrag
}

// NewSharedStore lädt den geteilten Bestand aus DataDir (oder beginnt leer).
func NewSharedStore(dataDir string) *SharedStore {
	s := &SharedStore{
		path:  filepath.Join(dataDir, "shared.json"),
		files: make(map[string]*SharedFile),
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		var list []*SharedFile
		if json.Unmarshal(raw, &list) == nil {
			for _, f := range list {
				if f != nil && f.Hash != "" {
					s.files[f.Hash] = f
				}
			}
		}
	}
	return s
}

func (s *SharedStore) persist() {
	list := make([]*SharedFile, 0, len(s.files))
	for _, f := range s.files {
		list = append(list, f)
	}
	if data, err := json.Marshal(list); err == nil {
		_ = os.WriteFile(s.path, data, 0o640)
	}
}

// Add markiert eine Datei als geteilt.
func (s *SharedStore) Add(f *SharedFile) {
	if f == nil || f.Hash == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.SharedAt == 0 {
		f.SharedAt = time.Now().Unix()
	}
	s.files[f.Hash] = f
	s.persist()
}

// Remove hebt die Freigabe auf.
func (s *SharedStore) Remove(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, hash)
	s.persist()
}

// IsShared prüft, ob ein Hash freigegeben ist.
func (s *SharedStore) IsShared(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.files[hash]
	return ok
}

// List liefert alle geteilten Dateien (neueste zuerst).
func (s *SharedStore) List() []SharedFile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SharedFile, 0, len(s.files))
	for _, f := range s.files {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SharedAt > out[j].SharedAt })
	return out
}

// Search durchsucht die geteilten Dateien per Teilstring im Namen
// (case-insensitive). Leere Query → alle. Begrenzt auf max Treffer.
func (s *SharedStore) Search(query string, max int) []SharedFile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]SharedFile, 0, 8)
	for _, f := range s.files {
		if q == "" || strings.Contains(strings.ToLower(f.Name), q) {
			out = append(out, *f)
			if max > 0 && len(out) >= max {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SharedAt > out[j].SharedAt })
	return out
}

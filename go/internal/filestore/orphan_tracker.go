package filestore

// orphanTracker merkt sich pro host:-Chunk den Verdacht, verwaist zu sein:
// seit wann (firstSeen) und wie oft in Folge (count) er als nicht-verankert
// erkannt wurde. Persistent, damit ein Neustart die Grace Period nicht
// zurücksetzt — ein Node, der oft neu startet, würde sonst nie freigeben.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type orphanSuspect struct {
	FirstSeen time.Time `json:"first_seen"`
	Count     int       `json:"count"`
}

type orphanTracker struct {
	mu       sync.Mutex
	path     string
	suspects map[string]orphanSuspect
	changed  bool
}

func newOrphanTracker(dataDir string) *orphanTracker {
	t := &orphanTracker{
		path:     filepath.Join(dataDir, "orphan-suspects.json"),
		suspects: make(map[string]orphanSuspect),
	}
	t.load()
	return t
}

// mark erhöht den Verdacht für einen Chunk und liefert firstSeen + neuen Count.
func (t *orphanTracker) mark(hash string) (time.Time, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.suspects[hash]
	if !ok {
		s = orphanSuspect{FirstSeen: time.Now().UTC(), Count: 0}
	}
	s.Count++
	t.suspects[hash] = s
	t.changed = true
	return s.FirstSeen, s.Count
}

// clear entfernt den Verdacht (Chunk ist wieder verankert oder freigegeben).
// Gibt true zurück, wenn tatsächlich ein Eintrag entfernt wurde.
func (t *orphanTracker) clear(hash string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.suspects[hash]; ok {
		delete(t.suspects, hash)
		t.changed = true
		return true
	}
	return false
}

func (t *orphanTracker) dirty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changed
}

func (t *orphanTracker) persist() {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := json.Marshal(t.suspects)
	if err != nil {
		return
	}
	tmp := t.path + ".tmp"
	if os.WriteFile(tmp, data, 0o640) == nil {
		_ = os.Rename(tmp, t.path)
		t.changed = false
	}
}

func (t *orphanTracker) load() {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	var m map[string]orphanSuspect
	if json.Unmarshal(data, &m) == nil && m != nil {
		t.suspects = m
	}
}

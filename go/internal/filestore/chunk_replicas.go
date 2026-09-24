package filestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// chunkReplicasIndex speichert die GEWÜNSCHTE Replikatzahl pro Chunk persistent.
// Damit respektiert der Replikations-Manager die beim Upload gewählte Redundanz,
// statt jeden Chunk stur auf TargetReplicas hochzuziehen. Fehlt ein Eintrag,
// gilt TargetReplicas als Standard (Rückwärtskompatibilität mit Altbestand).
type chunkReplicasIndex struct {
	mu   sync.RWMutex
	path string
	want map[string]int // chunkHash → gewünschte Replikatzahl
}

func newChunkReplicasIndex(dataDir string) *chunkReplicasIndex {
	ci := &chunkReplicasIndex{
		path: filepath.Join(dataDir, "chunk_replicas.json"),
		want: make(map[string]int),
	}
	ci.load()
	return ci
}

// set merkt die gewünschte Replikatzahl für einen Chunk (idempotent). Ein
// höherer Wert gewinnt: Teilt sich ein Chunk zwischen zwei Dateien mit
// unterschiedlicher Redundanz, gilt die höhere (Deduplizierung darf die
// Sicherheit der anspruchsvolleren Datei nicht senken).
func (ci *chunkReplicasIndex) set(chunkHash string, replicas int) {
	if replicas <= 0 {
		return
	}
	ci.mu.Lock()
	defer ci.mu.Unlock()
	if cur, ok := ci.want[chunkHash]; !ok || replicas > cur {
		ci.want[chunkHash] = replicas
		ci.persistLocked()
	}
}

// get liefert die gewünschte Replikatzahl oder fallback (meist TargetReplicas),
// wenn für den Chunk nichts hinterlegt ist.
func (ci *chunkReplicasIndex) get(chunkHash string, fallback int) int {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	if v, ok := ci.want[chunkHash]; ok && v > 0 {
		return v
	}
	return fallback
}

// remove löscht den Eintrag (z.B. wenn ein Chunk vollständig entfernt wird).
func (ci *chunkReplicasIndex) remove(chunkHash string) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	if _, ok := ci.want[chunkHash]; ok {
		delete(ci.want, chunkHash)
		ci.persistLocked()
	}
}

func (ci *chunkReplicasIndex) persistLocked() {
	data, err := json.Marshal(ci.want)
	if err != nil {
		return
	}
	tmp := ci.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, ci.path)
}

func (ci *chunkReplicasIndex) load() {
	data, err := os.ReadFile(ci.path)
	if err != nil {
		return
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err == nil && m != nil {
		ci.want = m
	}
}

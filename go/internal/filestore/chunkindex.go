package filestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// =============================================================================
//  Chunk-Index — Reference Counting + Ownership-Tabelle
// =============================================================================
//
// Jeder Chunk wird content-adressiert genau EINMAL gespeichert (Deduplizierung),
// kann aber von mehreren "Ownern" referenziert werden:
//
//   - file:<contentHash>   → Chunk gehört zu einer eigenen hochgeladenen Datei
//   - host:<uploaderAddr>  → Chunk wird fürs Netz gehostet (fremde Replikation)
//   - cache                → temporär vom Download gecacht (niedrigste Priorität)
//
// Ein Chunk wird physisch erst gelöscht, wenn die LETZTE Referenz entfernt wird.
// Das löst beide Probleme:
//   1. Löscht man eine eigene Datei, verschwinden ihre Chunks automatisch
//      (sofern kein anderer Owner sie noch braucht) → keine verwaisten Chunks.
//   2. Fremde gehostete Chunks (host:...) sind klar von eigenen unterscheidbar
//      und werden NICHT gelöscht, nur weil keine eigene Datei sie referenziert.
//
// Persistenz: <DataDir>/chunkrefs.json (atomar geschrieben). Wird beim Start
// geladen; fehlt sie, startet der Index leer und wird durch laufende Operationen
// wieder aufgebaut (bzw. einmalig aus den vorhandenen Manifesten migriert).

// chunkRefs hält pro Chunk-Hash die Menge der Owner-Strings.
type chunkRefIndex struct {
	mu   sync.RWMutex
	path string
	refs map[string]map[string]bool // chunkHash → set(owner)
}

func newChunkRefIndex(dataDir string) *chunkRefIndex {
	ci := &chunkRefIndex{
		path: filepath.Join(dataDir, "chunkrefs.json"),
		refs: make(map[string]map[string]bool),
	}
	ci.load()
	return ci
}

// addRef fügt eine Owner-Referenz zu einem Chunk hinzu. Gibt true zurück, wenn
// der Chunk vorher keine Referenz hatte (also neu ist).
func (ci *chunkRefIndex) addRef(hash, owner string) (isNew bool) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	set := ci.refs[hash]
	if set == nil {
		set = make(map[string]bool)
		ci.refs[hash] = set
		isNew = true
	}
	set[owner] = true
	ci.persistLocked()
	return isNew
}

// removeRef entfernt eine Owner-Referenz. Gibt true zurück, wenn danach KEINE
// Referenz mehr übrig ist (der Chunk also physisch gelöscht werden darf).
func (ci *chunkRefIndex) removeRef(hash, owner string) (nowOrphan bool) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	set := ci.refs[hash]
	if set == nil {
		return true // unbekannt → als verwaist behandeln
	}
	delete(set, owner)
	if len(set) == 0 {
		delete(ci.refs, hash)
		ci.persistLocked()
		return true
	}
	ci.persistLocked()
	return false
}

// refCount liefert die Zahl der Owner eines Chunks.
func (ci *chunkRefIndex) refCount(hash string) int {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return len(ci.refs[hash])
}

// isCacheOnly meldet, ob ein Chunk NUR als Download-Cache referenziert ist
// (keine eigene Datei, keine fremde Hosting-Pflicht). Solche Chunks kann man
// gefahrlos entfernen — sie werden bei Bedarf neu aus dem Netz geholt.
func (ci *chunkRefIndex) isCacheOnly(hash string) bool {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	set := ci.refs[hash]
	if len(set) == 0 {
		return false // gar keine Referenz → das ist ein Orphan, nicht Cache
	}
	for o := range set {
		// Sobald eine file:- oder host:-Referenz existiert, ist es kein reiner Cache.
		if strings.HasPrefix(o, "file:") || strings.HasPrefix(o, "host:") {
			return false
		}
	}
	return true
}

// owners liefert die Owner-Liste eines Chunks (für Diagnose).
func (ci *chunkRefIndex) owners(hash string) []string {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	set := ci.refs[hash]
	out := make([]string, 0, len(set))
	for o := range set {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// hasAnyRef sagt, ob ein Chunk überhaupt referenziert ist.
func (ci *chunkRefIndex) hasAnyRef(hash string) bool {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return len(ci.refs[hash]) > 0
}

// isHostedOnly sagt, ob ein Chunk AUSSCHLIESSLICH fürs Netz gehostet wird (nur
// host:-Owner, keine eigene Datei). Solche Chunks darf die GC nicht anrühren.
func (ci *chunkRefIndex) isHostedOnly(hash string) bool {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	set := ci.refs[hash]
	if len(set) == 0 {
		return false
	}
	for owner := range set {
		if len(owner) < 5 || owner[:5] != "host:" {
			return false // hat mindestens einen nicht-host-Owner
		}
	}
	return true
}

// hasFileRef sagt, ob ein Chunk zu mindestens einer EIGENEN hochgeladenen Datei
// gehört (file:-Owner). Für die Speicher-Aufschlüsselung (eigene Dateien vs.
// gehostete Fremd-Replikate vs. Cache).
func (ci *chunkRefIndex) hasFileRef(hash string) bool {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	for owner := range ci.refs[hash] {
		if strings.HasPrefix(owner, "file:") {
			return true
		}
	}
	return false
}

// knownChunks liefert alle im Index bekannten Chunk-Hashes.
func (ci *chunkRefIndex) knownChunks() map[string]bool {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	out := make(map[string]bool, len(ci.refs))
	for h := range ci.refs {
		out[h] = true
	}
	return out
}

// --- Persistenz -------------------------------------------------------------

// persistedRefs ist die serialisierbare Form (Sets als sortierte Slices).
type persistedRefs map[string][]string

func (ci *chunkRefIndex) persistLocked() {
	out := make(persistedRefs, len(ci.refs))
	for h, set := range ci.refs {
		owners := make([]string, 0, len(set))
		for o := range set {
			owners = append(owners, o)
		}
		sort.Strings(owners)
		out[h] = owners
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	tmp := ci.path + ".tmp"
	if os.WriteFile(tmp, data, 0640) == nil {
		_ = os.Rename(tmp, ci.path)
	}
}

func (ci *chunkRefIndex) load() {
	data, err := os.ReadFile(ci.path)
	if err != nil {
		return
	}
	var in persistedRefs
	if json.Unmarshal(data, &in) != nil {
		return
	}
	for h, owners := range in {
		set := make(map[string]bool, len(owners))
		for _, o := range owners {
			set[o] = true
		}
		ci.refs[h] = set
	}
}

// =============================================================================
//  Migration & GC-Integration (FileStore-Methoden)
// =============================================================================

// migrateChunkRefsIfNeeded baut den Referenz-Index einmalig aus den vorhandenen
// Manifesten auf, falls er leer ist (erster Start nach dem Update). Bestehende
// Chunks werden so ihren Dateien zugeordnet (file:-Owner), statt als verwaist zu
// gelten. Idempotent: bei bereits gefülltem Index passiert nichts.
func (fs *FileStore) migrateChunkRefsIfNeeded() {
	if fs.chunkRefs == nil || fs.localIdx == nil {
		return
	}
	// Index schon befüllt? Dann nichts tun.
	if len(fs.chunkRefs.knownChunks()) > 0 {
		return
	}
	entries := fs.localIdx.List()
	if len(entries) == 0 {
		return
	}
	migrated := 0
	for _, entry := range entries {
		owner := "file:" + entry.Hash
		manifestData, err := fs.loadChunkLocal("manifest_" + entry.Hash)
		if err != nil || len(manifestData) == 0 {
			continue
		}
		fs.chunkRefs.addRef("manifest_"+entry.Hash, owner)
		var m FileManifest
		if json.Unmarshal(manifestData, &m) != nil {
			continue
		}
		for _, h := range m.ChunkHashes {
			fs.chunkRefs.addRef(h, owner)
			migrated++
		}
		if len(m.ManifestChunks) > 0 {
			subDatas := make([][]byte, 0, len(m.ManifestChunks))
			for _, subHash := range m.ManifestChunks {
				fs.chunkRefs.addRef(subHash, owner)
				if subData, serr := fs.loadChunkLocal(subHash); serr == nil {
					subDatas = append(subDatas, subData)
				}
			}
			if full, rerr := reassembleFromSubManifests(subDatas); rerr == nil {
				for _, h := range full {
					fs.chunkRefs.addRef(h, owner)
					migrated++
				}
			}
		}
	}
	if migrated > 0 && fs.log != nil {
		fs.log.Info("Chunk-Referenzen migriert",
			zap.Int("dateien", len(entries)),
			zap.Int("chunk_referenzen", migrated))
	}
}

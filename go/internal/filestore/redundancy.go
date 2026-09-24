package filestore

// Redundanz einer Datei: wie oft ihre Chunks im Netz repliziert sind.
//
// Die Redundanz einer Datei ist die SCHWÄCHSTE Stelle — das Minimum der
// Replikatzahlen über alle ihre Chunks. Eine Datei mit 100 gut replizierten
// Chunks und einem einzigen Chunk auf nur einem Peer hat effektiv Redundanz 1:
// fällt dieser Peer aus, ist die Datei unvollständig. Deshalb zählt das Minimum,
// nicht der Durchschnitt.

import (
	"context"
	"encoding/json"
	"time"
)

// FileRedundancy liefert die aktuelle Netz-Redundanz einer Datei: das Minimum
// der Replikatzahl über alle Chunks (min), das gewünschte Ziel (target) und die
// Anzahl geprüfter Chunks. Bei Fehler wird err gesetzt.
func (fs *FileStore) FileRedundancy(ctx context.Context, contentHash string) (min, target, chunks int, err error) {
	m, err := fs.loadManifest(ctx, contentHash)
	if err != nil {
		return 0, 0, 0, err
	}
	hashes := m.ChunkHashes
	if len(m.ManifestChunks) > 0 {
		subDatas := make([][]byte, 0, len(m.ManifestChunks))
		for _, subHash := range m.ManifestChunks {
			d, ferr := fs.fetchChunk(ctx, subHash)
			if ferr != nil {
				return 0, 0, 0, ferr
			}
			subDatas = append(subDatas, d)
		}
		full, rerr := reassembleFromSubManifests(subDatas)
		if rerr != nil {
			return 0, 0, 0, rerr
		}
		hashes = full
	}
	if len(hashes) == 0 {
		return 0, 0, 0, nil
	}

	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	min = -1
	for _, h := range hashes {
		count := fs.chunkReplicaCount(lctx, h)
		if min == -1 || count < min {
			min = count
		}
		t := fs.targetReplicasFor(h)
		if t > target {
			target = t
		}
	}
	if min < 0 {
		min = 0
	}
	return min, target, len(hashes), nil
}

// chunkReplicaCount ermittelt, auf wie vielen Peers ein Chunk laut DHT-Location
// liegt. Wir selbst zählen mit, wenn wir ihn halten (wir sind ein gültiges
// Replikat). Fehlt die Location, 0.
func (fs *FileStore) chunkReplicaCount(ctx context.Context, hash string) int {
	data, err := fs.p2p.DHTget(ctx, DHTNamespaceChunk+hash)
	if err != nil || len(data) == 0 {
		// Kein DHT-Eintrag — aber vielleicht halten wir ihn lokal.
		fs.mu.RLock()
		_, have := fs.chunks[hash]
		fs.mu.RUnlock()
		if have {
			return 1
		}
		return 0
	}
	var loc ChunkLocation
	if json.Unmarshal(data, &loc) != nil {
		return 0
	}
	// Eindeutige Peer-IDs zählen.
	seen := make(map[string]bool, len(loc.PeerIDs))
	for _, pid := range loc.PeerIDs {
		if pid != "" {
			seen[pid] = true
		}
	}
	return len(seen)
}

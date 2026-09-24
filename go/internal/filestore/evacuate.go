package filestore

// Sichere Evakuierung gehosteter Chunks beim Verringern der Freigabe.
//
// PROBLEM: Verringert der Nutzer sein Speicher-Angebot unter den bereits durch
// host:-Replikate belegten Platz, müssen überzählige Replikate abgegeben werden.
// Sie einfach zu löschen würde dem Netz Redundanz nehmen — im schlimmsten Fall
// die letzte Kopie einer Datei.
//
// LÖSUNG (goldene Regel, wie bei der Orphan-GC): ein host:-Chunk wird lokal erst
// freigegeben, NACHDEM sichergestellt ist, dass er im Netz ausreichend repliziert
// ist. Reicht die Redundanz noch nicht, wird der Chunk zuerst zu weiteren Peers
// gepusht. Lässt sich die Redundanz nicht herstellen (keine Peers erreichbar),
// bleibt der Chunk liegen — kein Datenverlust, lieber das Angebot vorübergehend
// überschritten.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"
)

// evacuateToOffer gibt so lange gehostete Chunks ab, bis der belegte Platz das
// (verringerte) Angebot wieder einhält — aber nur solche, die im Netz sicher
// repliziert sind oder sicher repliziert werden konnten. Läuft asynchron nach
// einer Angebots-Verringerung.
func (fs *FileStore) evacuateToOffer(ctx context.Context) {
	if fs.chunkRefs == nil {
		return
	}
	// Zielgröße: das aktuelle Gesamt-Angebot in Bytes.
	offerBytes := int64(fs.volumes.totalOfferGB() * 1e9)

	fs.mu.RLock()
	used := fs.used
	fs.mu.RUnlock()
	if used <= offerBytes {
		return // Angebot wird bereits eingehalten
	}
	toFree := used - offerBytes

	// Kandidaten: ausschließlich gehostete Chunks (eigene Dateien nie evakuieren).
	var candidates []string
	for h := range fs.chunkRefs.knownChunks() {
		if strings.HasPrefix(h, "manifest_") {
			continue
		}
		if fs.chunkRefs.isHostedOnly(h) {
			candidates = append(candidates, h)
		}
	}

	freed := int64(0)
	evac, kept := 0, 0
	for _, hash := range candidates {
		if freed >= toFree {
			break
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		// SICHERHEIT: Nur abgeben, wenn der Chunk im Netz ausreichend repliziert
		// ist. Sonst zuerst dorthin pushen; klappt das nicht, behalten.
		if !fs.ensureReplicatedElsewhere(ctx, hash) {
			kept++
			continue
		}
		sz := fs.chunkSizeOnDisk(hash)
		for _, owner := range fs.chunkRefs.owners(hash) {
			if strings.HasPrefix(owner, "host:") {
				_ = fs.releaseChunk(hash, owner)
			}
		}
		freed += sz
		evac++
	}
	if evac > 0 || kept > 0 {
		fs.log.Info("Angebots-Verringerung: gehostete Chunks evakuiert",
			zap.Int("abgegeben", evac),
			zap.Int("behalten_mangels_redundanz", kept),
			zap.Int64("freigegeben_bytes", freed),
			zap.Int64("ziel_bytes", toFree))
	}
}

// ensureReplicatedElsewhere stellt sicher, dass ein Chunk auf genügend ANDEREN
// Peers liegt (nicht auf uns). Ist die Redundanz schon erfüllt, true. Sonst wird
// der Chunk zu weiteren Peers gepusht; gelingt das bis zum Ziel, true. Kann die
// Redundanz nicht hergestellt werden, false → Chunk NICHT abgeben.
func (fs *FileStore) ensureReplicatedElsewhere(ctx context.Context, hash string) bool {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	target := fs.targetReplicasFor(hash)
	self := fs.p2p.ID().String()

	// Aktuelle Location aus dem DHT lesen.
	var loc ChunkLocation
	if data, err := fs.p2p.DHTget(lctx, DHTNamespaceChunk+hash); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &loc)
	}
	// Erreichbare andere Peers, die den Chunk halten.
	var others []string
	for _, pid := range loc.PeerIDs {
		if pid != self {
			others = append(others, pid)
		}
	}
	aliveOthers := fs.pingPeers(lctx, others)
	if len(aliveOthers) >= target {
		return true // Redundanz ohne uns bereits erfüllt
	}

	// Zu wenig Redundanz → Chunk zu weiteren Peers pushen.
	data, err := fs.loadChunkLocal(hash)
	if err != nil {
		return false // wir haben ihn nicht mehr lokal → nichts zu pushen
	}
	need := target - len(aliveOthers)
	have := map[string]bool{self: true}
	for _, p := range aliveOthers {
		have[p] = true
	}
	pushed := 0
	for _, peer := range fs.selectPeers(need * 2) {
		if pushed >= need {
			break
		}
		if have[peer] {
			continue
		}
		if err := fs.replicateChunkToPeer(lctx, peer, hash, data); err == nil {
			aliveOthers = append(aliveOthers, peer)
			have[peer] = true
			pushed++
		}
	}
	// Location im DHT aktualisieren, damit die neue Verteilung sichtbar ist.
	if pushed > 0 {
		loc.PeerIDs = aliveOthers
		loc.UpdatedAt = time.Now().UTC()
		if data, e := json.Marshal(loc); e == nil {
			_ = fs.p2p.DHTput(lctx, DHTNamespaceChunk+hash, data)
		}
	}
	return len(aliveOthers) >= target
}

// chunkSizeOnDisk liefert die Größe eines Chunks auf der Platte (0 bei Fehler).
func (fs *FileStore) chunkSizeOnDisk(hash string) int64 {
	data, err := fs.loadChunkLocal(hash)
	if err != nil {
		return 0
	}
	return int64(len(data))
}

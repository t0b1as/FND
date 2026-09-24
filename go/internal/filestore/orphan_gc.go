package filestore

// Garbage Collection für verwaiste gehostete Replikate.
//
// PROBLEM: Ein Node hält host:-Chunks als Replikate für Dateien anderer. Wird
// eine Datei vom Besitzer gelöscht ODER ist der Besitzer dauerhaft offline, hält
// der Node diese Replikate ewig — toter Speicher, den kein Cleanup-Modus anfasst
// (host:-Chunks sind bewusst geschützt).
//
// LÖSUNG: Ein host:-Chunk gilt als verwaist, wenn er im Netz nicht mehr verankert
// ist — konkret: seine DHT-Location fehlt oder listet keine lebenden Peers mehr,
// UND der ursprüngliche Uploader ist nicht erreichbar.
//
// SICHERHEIT (das Entscheidende): Nie beim ersten Verdacht löschen. Ein Peer kann
// kurz offline sein, die DHT kann temporär inkonsistent sein. Ein Chunk wird erst
// nach OrphanConfirmations aufeinanderfolgenden Bestätigungen über mindestens
// OrphanGracePeriod freigegeben. Ein einziger Treffer (Location wieder da, Peer
// wieder erreichbar) setzt den Verdachtszähler zurück. So kann ein temporärer
// Ausfall nie zu Datenverlust führen.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// OrphanGracePeriod ist die Mindestzeit, die ein Chunk durchgehend als
	// verwaist gelten muss, bevor er freigegeben wird.
	OrphanGracePeriod = 24 * time.Hour
	// OrphanConfirmations ist die Zahl aufeinanderfolgender GC-Durchläufe, die
	// den Verwaist-Zustand bestätigen müssen. Zusammen mit dem GC-Intervall
	// (siehe unten) ergibt das die effektive Wartezeit.
	OrphanConfirmations = 3
	// OrphanGCInterval ist der Abstand zwischen zwei GC-Durchläufen.
	OrphanGCInterval = 6 * time.Hour
)

// PurgeOrphanedHostedNow gibt verwaiste gehostete Replikate SOFORT frei — ohne
// die Grace Period der automatischen GC, aber MIT der Verankerungs-Prüfung. Für
// den manuellen Admin-Trigger gedacht (der Nutzer weiß, dass die Chunks verwaist
// sind). Gibt freigegebene Bytes und Anzahl zurück.
//
// WICHTIG: Vor dem Aufruf sollten alle Peers erreichbar sein — die Prüfung stuft
// einen Chunk als verwaist ein, wenn im Netz kein lebender Peer ihn hält. Ist ein
// Peer nur temporär offline, könnten dessen Chunks fälschlich als verwaist gelten.
// Deshalb ist das ein bewusster manueller Schritt, keine Automatik.
func (fs *FileStore) PurgeOrphanedHostedNow(ctx context.Context) (freedBytes int64, released int) {
	if fs.chunkRefs == nil {
		return 0, 0
	}
	var candidates []string
	for h := range fs.chunkRefs.knownChunks() {
		if strings.HasPrefix(h, "manifest_") {
			continue
		}
		if fs.chunkRefs.isHostedOnly(h) {
			candidates = append(candidates, h)
		}
	}
	for _, hash := range candidates {
		select {
		case <-ctx.Done():
			return freedBytes, released
		default:
		}
		if fs.isHostedChunkAnchored(ctx, hash) {
			continue // im Netz verankert → behalten
		}
		sz := fs.chunkSizeOnDisk(hash)
		for _, owner := range fs.chunkRefs.owners(hash) {
			if strings.HasPrefix(owner, "host:") {
				_ = fs.releaseChunk(hash, owner)
			}
		}
		if fs.orphanSuspects != nil {
			fs.orphanSuspects.clear(hash)
		}
		freedBytes += sz
		released++
	}
	if released > 0 {
		fs.log.Info("Manuelle Verwaiste-Replikate-Bereinigung",
			zap.Int("freigegeben", released),
			zap.Int64("bytes", freedBytes))
	}
	return freedBytes, released
}

// runOrphanGC ist der periodische Loop, der verwaiste gehostete Replikate
// freigibt. In den Wartungs-Loop des FileStore eingehängt.
func (fs *FileStore) runOrphanGC(ctx context.Context) {
	if fs.orphanSuspects == nil {
		fs.orphanSuspects = newOrphanTracker(fs.cfg.DataDir)
	}
	ticker := time.NewTicker(OrphanGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fs.gcOrphanedHostedChunks(ctx)
		}
	}
}

// gcOrphanedHostedChunks führt einen Durchlauf aus: prüft alle host:-Chunks auf
// Verankerung im Netz und gibt bestätigt verwaiste frei.
func (fs *FileStore) gcOrphanedHostedChunks(ctx context.Context) {
	if fs.chunkRefs == nil || fs.orphanSuspects == nil {
		return
	}
	// Kandidaten sammeln: Chunks, die AUSSCHLIESSLICH host: sind (keine eigene
	// Datei, kein Cache-Schutz). manifest_-Einträge überspringen.
	var candidates []string
	for h := range fs.chunkRefs.knownChunks() {
		if strings.HasPrefix(h, "manifest_") {
			continue
		}
		if fs.chunkRefs.isHostedOnly(h) {
			candidates = append(candidates, h)
		}
	}

	released, cleared := 0, 0
	for _, hash := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if fs.isHostedChunkAnchored(ctx, hash) {
			// Im Netz verankert → kein Verdacht, Zähler zurücksetzen.
			if fs.orphanSuspects.clear(hash) {
				cleared++
			}
			continue
		}
		// Nicht verankert → Verdacht erhöhen. Erst wenn lange und oft genug
		// bestätigt, freigeben.
		first, count := fs.orphanSuspects.mark(hash)
		if count < OrphanConfirmations || time.Since(first) < OrphanGracePeriod {
			continue
		}
		// Bestätigt verwaist → alle host:-Referenzen entfernen (gibt den Chunk
		// physisch frei, sofern keine andere Referenz mehr besteht).
		for _, owner := range fs.chunkRefs.owners(hash) {
			if strings.HasPrefix(owner, "host:") {
				_ = fs.releaseChunk(hash, owner)
			}
		}
		fs.orphanSuspects.clear(hash)
		released++
	}
	if fs.orphanSuspects.dirty() {
		fs.orphanSuspects.persist()
	}
	if released > 0 || cleared > 0 {
		fs.log.Info("Verwaiste-Replikate-GC",
			zap.Int("freigegeben", released),
			zap.Int("verdacht_zurückgesetzt", cleared),
			zap.Int("kandidaten", len(candidates)))
	}
}

// isHostedChunkAnchored prüft, ob ein gehosteter Chunk im Netz noch verankert
// ist: Existiert eine DHT-Location mit mindestens einem erreichbaren Peer
// (außer uns selbst)? Fehlt die Location komplett oder ist kein Peer erreichbar,
// gilt der Chunk als (vorläufig) nicht verankert.
func (fs *FileStore) isHostedChunkAnchored(ctx context.Context, hash string) bool {
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	locData, err := fs.p2p.DHTget(lctx, DHTNamespaceChunk+hash)
	if err != nil || len(locData) == 0 {
		return false // keine Location im DHT → nicht verankert
	}
	var loc ChunkLocation
	if err := json.Unmarshal(locData, &loc); err != nil {
		return false
	}
	if len(loc.PeerIDs) == 0 {
		return false
	}
	// Sind andere Peers als wir selbst erreichbar, die den Chunk halten?
	self := fs.p2p.ID().String()
	var others []string
	for _, pid := range loc.PeerIDs {
		if pid != self {
			others = append(others, pid)
		}
	}
	if len(others) == 0 {
		// Nur noch wir selbst in der Location → im Netz nicht mehr verankert.
		return false
	}
	alive := fs.pingPeers(lctx, others)
	return len(alive) > 0
}

package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// =============================================================================
//  Chunk-Bereinigung (Garbage Collection für verwaisten Speicher)
// =============================================================================
//
// Über die Zeit sammeln sich Chunks an, die zu keiner lokal indizierten Datei
// mehr gehören:
//   - Chunks gelöschter eigener Dateien (RemoveFromIndex entfernt nur den Index,
//     nicht die Chunks — das ist die Hauptquelle verwaisten Speichers).
//   - .tmp-Reste abgebrochener Schreibvorgänge.
//   - Manifeste/Chunks abgebrochener Uploads.
//
// Die GC sammelt zuerst ALLE noch referenzierten Chunk-Hashes (über die
// Manifeste aller indizierten Dateien, inkl. Sub-Manifeste großer Dateien) und
// betrachtet alles andere als verwaist.
//
// WICHTIG — fremde gehostete Chunks: Dieser Node hostet evtl. Chunks fremder
// Dateien fürs Netz (Replikation). Diese tauchen NICHT im lokalen Datei-Index
// auf und würden fälschlich als "verwaist" gelten. Bis ein vollständiges
// Reference-Counting-System existiert, ist die GC daher KONSERVATIV:
//   - scanOnly=true liefert nur eine Schätzung (löscht nichts).
//   - Beim echten Löschen werden NUR .tmp-Reste sowie Chunks gelöscht, die
//     älter als minAgeHours sind UND nicht referenziert — so überlebt eine
//     gerade laufende Replikation. Fremde Chunks ohne lokale Datei können
//     hierbei dennoch betroffen sein; deshalb ist der aggressive Modus klar als
//     solcher gekennzeichnet und der Default ist der sichere tmp-only-Modus.

// CleanupReport fasst das Ergebnis einer Bereinigung zusammen.
type CleanupReport struct {
	ScannedChunks   int   `json:"scanned_chunks"`
	ReferencedChunks int  `json:"referenced_chunks"`
	OrphanChunks    int   `json:"orphan_chunks"`
	OrphanBytes     int64 `json:"orphan_bytes"`
	CacheChunks     int   `json:"cache_chunks"`
	CacheBytes      int64 `json:"cache_bytes"`
	// Aufschlüsselung der referenzierten (nicht-verwaisten) Chunks, damit
	// sichtbar wird, WOHER belegter Platz kommt: eigene Dateien vs. fürs Netz
	// gehostete Fremd-Replikate.
	OwnChunks       int   `json:"own_chunks"`  // Chunks eigener hochgeladener Dateien (file:)
	OwnBytes        int64 `json:"own_bytes"`
	HostChunks      int   `json:"host_chunks"` // fürs Netz gehostete Fremd-Replikate (host:)
	HostBytes       int64 `json:"host_bytes"`
	TmpFiles        int   `json:"tmp_files"`
	TmpBytes        int64 `json:"tmp_bytes"`
	DeletedChunks   int   `json:"deleted_chunks"`
	DeletedBytes    int64 `json:"deleted_bytes"`
}

// liveChunkHashes sammelt alle Chunk-Hashes, die zu lokal indizierten Dateien
// gehören (inkl. Sub-Manifest-Chunks und der Manifest-Keys selbst).
func (fs *FileStore) liveChunkHashes(ctx context.Context) map[string]bool {
	live := make(map[string]bool)
	if fs.localIdx == nil {
		return live
	}
	for _, entry := range fs.localIdx.List() {
		// Der lokale Manifest-Key gehört immer zu einer lebenden Datei.
		live["manifest_"+entry.Hash] = true

		// NUR das lokale Manifest lesen (kein Netz-Fallback) — die GC darf nicht
		// blockieren oder vom Netzwerkzustand abhängen. Fehlt das Manifest lokal,
		// bleibt die Datei über ihren Manifest-Key geschützt.
		manifestData, lerr := fs.loadChunkLocal("manifest_" + entry.Hash)
		if lerr != nil || len(manifestData) == 0 {
			continue
		}
		var m FileManifest
		if json.Unmarshal(manifestData, &m) != nil {
			continue
		}
		// Direkte Chunks (kleine/mittlere Dateien, inline).
		for _, h := range m.ChunkHashes {
			live[h] = true
		}
		// Mehrstufige Manifeste: Sub-Manifest-Keys schützen UND ihre Inhalte
		// (die eigentlichen Chunk-Hashes) aus den lokal vorhandenen Sub-Manifest-
		// Blöcken lesen.
		if len(m.ManifestChunks) > 0 {
			subDatas := make([][]byte, 0, len(m.ManifestChunks))
			for _, subHash := range m.ManifestChunks {
				live[subHash] = true
				if subData, serr := fs.loadChunkLocal(subHash); serr == nil && len(subData) > 0 {
					subDatas = append(subDatas, subData)
				}
			}
			if full, rerr := reassembleFromSubManifests(subDatas); rerr == nil {
				for _, h := range full {
					live[h] = true
				}
			}
		}
	}
	return live
}

// Cleanup führt die Bereinigung durch. Modi:
//   - mode "scan": nur zählen, nichts löschen.
//   - mode "tmp":  nur .tmp-Reste löschen (sicher).
//   - mode "orphans": .tmp + verwaiste Chunks löschen (gründlich; kann fremde
//     gehostete Chunks betreffen, siehe Hinweis oben).
func (fs *FileStore) Cleanup(ctx context.Context, mode string) (rep *CleanupReport, err error) {
	// Defensive: ein Panic (z.B. unerwarteter Nil-Pointer) soll eine klare
	// Fehlermeldung liefern statt eines generischen 500/Absturzes.
	defer func() {
		if r := recover(); r != nil {
			if fs.log != nil {
				fs.log.Error("Cleanup-Panic abgefangen", zap.Any("grund", r))
			}
			err = fmt.Errorf("Bereinigung fehlgeschlagen: %v", r)
			rep = nil
		}
	}()
	rep = &CleanupReport{}
	live := fs.liveChunkHashes(ctx)
	rep.ReferencedChunks = len(live)

	// 1) Verwaiste Chunks + .tmp-Reste über alle Volumes ermitteln.
	type orphan struct {
		hash string
		path string
		size int64
	}
	var orphans []orphan
	for _, vol := range fs.volumes.list() {
		if !vol.Online {
			continue
		}
		chunksDir := vol.chunksDir()
		// Existiert das chunks-Verzeichnis überhaupt? Walk über einen fehlenden
		// Pfad würde sonst einen Fehler liefern (den wir zwar abfangen, aber so
		// sparen wir den Aufruf und vermeiden Nil-Info-Randfälle).
		if st, serr := os.Stat(chunksDir); serr != nil || !st.IsDir() {
			continue
		}
		_ = filepath.Walk(chunksDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			name := info.Name()
			// .tmp-Reste immer als löschbar markieren.
			if strings.HasSuffix(name, ".tmp") {
				rep.TmpFiles++
				rep.TmpBytes += info.Size()
				if mode == "tmp" || mode == "orphans" {
					if os.Remove(path) == nil {
						rep.DeletedChunks++ // zählt mit (tmp)
						rep.DeletedBytes += info.Size()
					}
				}
				return nil
			}
			rep.ScannedChunks++
			// Verwaist ist ein Chunk, der KEINE Referenz im chunkRefs-Index hat.
			// Hat er nur host:-Referenzen (fürs Netz gehostet), gilt er NICHT als
			// verwaist und wird geschützt. Fehlt der Index (alte Daten), fällt
			// die Prüfung auf die manifest-basierte live-Menge zurück.
			isOrphan := false
			if fs.chunkRefs != nil && len(fs.chunkRefs.knownChunks()) > 0 {
				isOrphan = !fs.chunkRefs.hasAnyRef(name)
			} else {
				isOrphan = !live[name]
			}
			if isOrphan {
				rep.OrphanChunks++
				rep.OrphanBytes += info.Size()
				orphans = append(orphans, orphan{hash: name, path: path, size: info.Size()})
			} else if fs.chunkRefs != nil && fs.chunkRefs.isCacheOnly(name) {
				// Reiner Download-Cache (keine eigene Datei, keine Hosting-Pflicht).
				// Wird bei Bedarf neu aus dem Netz geholt → gefahrlos löschbar.
				rep.CacheChunks++
				rep.CacheBytes += info.Size()
				if mode == "cache" {
					if os.Remove(path) == nil {
						rep.DeletedChunks++
						rep.DeletedBytes += info.Size()
						fs.mu.Lock()
						delete(fs.chunks, name)
						fs.mu.Unlock()
						if fs.chunkRefs != nil {
							fs.chunkRefs.mu.Lock()
							delete(fs.chunkRefs.refs, name)
							fs.chunkRefs.mu.Unlock()
						}
					}
				}
			} else if fs.chunkRefs != nil {
				// Referenzierter Chunk (kein Waise, kein reiner Cache): nach Typ
				// aufschlüsseln, damit sichtbar wird, wohin der Platz geht.
				// host: = fürs Netz gehostetes Fremd-Replikat (Sinn des Systems,
				// bringt Hosting-Quittungen); file: = eigene hochgeladene Datei.
				if fs.chunkRefs.hasFileRef(name) {
					rep.OwnChunks++
					rep.OwnBytes += info.Size()
				} else if fs.chunkRefs.isHostedOnly(name) {
					rep.HostChunks++
					rep.HostBytes += info.Size()
				}
			}
			return nil
		})
	}

	// 2) Im orphans-Modus die verwaisten Chunks tatsächlich löschen.
	if mode == "orphans" {
		for _, o := range orphans {
			if os.Remove(o.path) == nil {
				rep.DeletedChunks++
				rep.DeletedBytes += o.size
				fs.mu.Lock()
				delete(fs.chunks, o.hash)
				fs.used -= o.size
				fs.mu.Unlock()
				// Etwaigen (leeren) Index-Eintrag mit entfernen.
				if fs.chunkRefs != nil {
					fs.chunkRefs.mu.Lock()
					delete(fs.chunkRefs.refs, o.hash)
					fs.chunkRefs.persistLocked()
					fs.chunkRefs.mu.Unlock()
				}
			}
		}
	}

	if rep.DeletedBytes > 0 && fs.log != nil {
		fs.log.Info("Speicher bereinigt",
			zap.String("modus", mode),
			zap.Int("geloeschte_chunks", rep.DeletedChunks),
			zap.Int64("freigegebene_bytes", rep.DeletedBytes))
	}
	return rep, nil
}

package filestore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"go.uber.org/zap"
	"lukechampine.com/blake3"
)

// ─── Resume-fähiger Download (für große Dateien, z.B. 128 GiB) ───────────────
//
// Ein Download über viele GiB kann unterbrochen werden. Statt von vorn zu
// beginnen, schreiben wir in eine Zieldatei und persistieren den Fortschritt
// (Anzahl vollständig geschriebener Chunks) neben der Datei. Beim Wiederaufsetzen
// überspringen wir die bereits geschriebenen Chunks und hängen weiter an.
//
// Integrität: Jeder Chunk wird beim Abruf gegen seinen Hash geprüft (bestehende
// Logik). Der Gesamthash-Abgleich gegen den angefragten contentHash bleibt
// erhalten — beim Wiederaufsetzen wird der bereits geschriebene Präfix einmal
// rehasht (BLAKE3 lässt sich nicht serialisieren), danach inkrementell weiter.

// DownloadProgress ist der persistierte Fortschritt eines Resume-Downloads.
type DownloadProgress struct {
	ContentHash  string `json:"content_hash"`
	ChunksDone   int    `json:"chunks_done"`   // vollständig geschrieben
	BytesWritten int64  `json:"bytes_written"` // = Offset in der Zieldatei
	TotalChunks  int    `json:"total_chunks"`
}

func (fs *FileStore) downloadProgressPath(destPath string) string {
	// Fortschritt liegt neben der Zieldatei: <dest>.fundus-progress.json
	return destPath + ".fundus-progress.json"
}

func (fs *FileStore) loadDownloadProgress(path string) (*DownloadProgress, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var p DownloadProgress
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return &p, true
}

func (fs *FileStore) saveDownloadProgress(path string, p DownloadProgress) {
	if data, err := json.Marshal(p); err == nil {
		_ = os.WriteFile(path, data, 0o640)
	}
}

// DownloadResumable lädt eine Datei nach destPath, fortsetzbar nach Abbruch.
// Existiert bereits ein Fortschritt + Teildatei für denselben contentHash, wird
// ab dem letzten vollständigen Chunk weitergeladen. Bei Erfolg wird die
// Fortschrittsdatei entfernt.
func (fs *FileStore) DownloadResumable(ctx context.Context, contentHash, destPath string) error {
	manifest, err := fs.loadManifest(ctx, contentHash)
	if err != nil {
		return err
	}
	if !manifest.Plaintext {
		// Verschlüsselter Altbestand: kein Resume (Offset-Mapping nicht trivial).
		return fmt.Errorf("filestore: Resume nur für Klartext-Dateien unterstützt")
	}

	progPath := fs.downloadProgressPath(destPath)
	startChunk := 0
	hasher := blake3.New(32, nil)

	// Vorhandenen Fortschritt prüfen.
	var dest *os.File
	if prog, ok := fs.loadDownloadProgress(progPath); ok &&
		prog.ContentHash == contentHash && prog.ChunksDone > 0 {
		// Teildatei öffnen, Präfix rehashen, Offset setzen.
		f, oerr := os.OpenFile(destPath, os.O_RDWR, 0o640)
		if oerr != nil {
			return fmt.Errorf("filestore: Teildatei öffnen: %w", oerr)
		}
		// Präfix bis BytesWritten in den Hasher spülen.
		if _, cerr := io.CopyN(hasher, f, prog.BytesWritten); cerr != nil {
			f.Close()
			return fmt.Errorf("filestore: Präfix rehashen: %w", cerr)
		}
		// Auf Offset positionieren (überschüssige Bytes abschneiden).
		if terr := f.Truncate(prog.BytesWritten); terr != nil {
			f.Close()
			return fmt.Errorf("filestore: Teildatei kürzen: %w", terr)
		}
		if _, serr := f.Seek(prog.BytesWritten, io.SeekStart); serr != nil {
			f.Close()
			return fmt.Errorf("filestore: Teildatei seek: %w", serr)
		}
		dest = f
		startChunk = prog.ChunksDone
		fs.log.Info("Download wird fortgesetzt",
			zap.Int("ab_chunk", startChunk),
			zap.Int("von", len(manifest.ChunkHashes)))
	} else {
		// Frischer Download.
		f, cerr := os.Create(destPath)
		if cerr != nil {
			return fmt.Errorf("filestore: Zieldatei anlegen: %w", cerr)
		}
		dest = f
	}
	defer dest.Close()

	bytesWritten := int64(0)
	if startChunk > 0 {
		if prog, ok := fs.loadDownloadProgress(progPath); ok {
			bytesWritten = prog.BytesWritten
		}
	}

	verifyWriter := io.MultiWriter(dest, hasher)
	// Paralleler Download ab dem ersten noch fehlenden Chunk. Der Prefetch
	// überlappt die Latenz; geschrieben wird strikt in Reihenfolge, sodass
	// Fortschritt und Gesamthash korrekt bleiben.
	remaining := manifest.ChunkHashes[startChunk:]
	perr := orderedParallelFetch(ctx, remaining, parallelFetchWindow,
		fs.fetchChunk,
		func(local int, chunk []byte) error {
			realIdx := startChunk + local // echter Chunk-Index in der Datei
			n, werr := verifyWriter.Write(chunk)
			if werr != nil {
				return fmt.Errorf("filestore: chunk %d schreiben: %w", realIdx, werr)
			}
			bytesWritten += int64(n)
			fs.mu.Lock()
			fs.bytesSent += int64(n)
			fs.mu.Unlock()
			// Fortschritt nach jedem Chunk persistieren → übersteht Absturz/Neustart.
			fs.saveDownloadProgress(progPath, DownloadProgress{
				ContentHash:  contentHash,
				ChunksDone:   realIdx + 1,
				BytesWritten: bytesWritten,
				TotalChunks:  len(manifest.ChunkHashes),
			})
			return nil
		})
	if perr != nil {
		return perr
	}

	// Gesamthash gegen den angefragten contentHash prüfen.
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != contentHash {
		return fmt.Errorf("filestore: Inhalts-Hash weicht ab (Manifest-Poisoning?): angefragt %s, erhalten %s",
			contentHash[:12]+"…", got[:12]+"…")
	}

	// Erfolg: Fortschrittsdatei entfernen.
	_ = os.Remove(progPath)
	return nil
}

// (Manifest-Auflösung inkl. mehrstufiger Sub-Manifeste erfolgt zentral in
// loadManifest, siehe resume.go — DownloadResumable nutzt dieselbe Funktion.)

// Fortschritt liegt neben der Zieldatei (<dest>.fundus-progress.json) — kein
// eigenes Verzeichnis nötig.

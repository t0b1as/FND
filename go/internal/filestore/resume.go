package filestore

// Resumefähige Uploads.
//
// Konzept: Der Client schneidet eine grosse Datei in Bloecke und laedt sie
// einzeln hoch. Jeder Block wird als Teil-Datei auf der SSD abgelegt
// (tmp/resume/<upload_id>/<index>.part). Bei einem Disconnect fragt der Client
// erneut welche Bloecke bereits da sind und sendet nur die fehlenden.
// Bei "finish" werden die Bloecke der Reihe nach zu einem Reader zusammen-
// gesetzt und durch die bestehende Upload()-Pipeline (Chunking, Verschluesselung,
// Verteilung) geschoben. So bleibt die RAM-Last gering (Streaming von der SSD).

import (
	"sync"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// ResumeSession beschreibt einen laufenden, unterbrechbaren Upload.
type ResumeSession struct {
	UploadID   string    `json:"upload_id"`
	FileName   string    `json:"file_name"`
	MimeType   string    `json:"mime_type"`
	TotalSize  int64     `json:"total_size"`
	BlockSize  int64     `json:"block_size"`
	TotalBlocks int      `json:"total_blocks"`
	Redundancy  int      `json:"redundancy,omitempty"` // gewählte Replikatzahl (>= MinReplicas)
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ResumeStatus ist der pollbare Zustand der serverseitigen Finalisierung.
// Wird als status.json in der Session abgelegt → übersteht Reload/Neustart.
type ResumeStatus struct {
	State       string `json:"state"`        // "uploading" | "finalizing" | "done" | "error"
	ContentHash string `json:"content_hash"` // gesetzt wenn done
	Error       string `json:"error"`        // gesetzt wenn error
	FileName    string `json:"file_name"`
	UpdatedAt   string `json:"updated_at"`
	// Fortschritt der Finalisierung (Chunking), damit die UI nicht stumm
	// "wird verarbeitet" zeigt. Phase: "lesen" | "chunking" | "manifest" | "index".
	Phase         string `json:"phase,omitempty"`
	ChunksDone    int    `json:"chunks_done,omitempty"`
	ChunksTotal   int    `json:"chunks_total,omitempty"`
}

func (fs *FileStore) writeStatus(dir string, st ResumeStatus) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if data, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "status.json"), data, 0o640)
	}
}

// GetResumeStatus liefert den aktuellen Finalisierungs-Status einer Session.
func (fs *FileStore) GetResumeStatus(uploadID string) (*ResumeStatus, bool) {
	dir := fs.sessionDir(sanitizeID(uploadID))
	raw, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		// Keine status.json: evtl. noch beim Block-Upload oder unbekannt
		if _, e := os.Stat(dir); e == nil {
			return &ResumeStatus{State: "uploading"}, true
		}
		return nil, false
	}
	var st ResumeStatus
	if json.Unmarshal(raw, &st) != nil {
		return nil, false
	}
	return &st, true
}

// resumeDir liefert das Verzeichnis für Upload-Sessions. Normalerweise
// <Speicher>/tmp/resume. Ist es nicht beschreibbar (z.B. von root angelegt,
// fremde Rechte auf einem Laufwerk), wird einmalig auf ein Verzeichnis unter
// dem System-Temp ausgewichen – sonst schlüge jeder Upload fehl
// ("session-dir: permission denied").
var (
	resumeDirOnce sync.Once
	resumeDirPath string
)

func (fs *FileStore) resumeDir() string {
	resumeDirOnce.Do(func() {
		primary := filepath.Join(fs.cfg.DataDir, "tmp", "resume")
		if dirWritable(primary) {
			resumeDirPath = primary
			return
		}
		fallback := filepath.Join(os.TempDir(), "fundus-resume")
		if dirWritable(fallback) {
			resumeDirPath = fallback
			if fs.log != nil {
				fs.log.Warn("Upload-Verzeichnis nicht beschreibbar – weiche aus (Rechte prüfen: sudo chown -R fundus:fundus <Speicher>/tmp)",
					zap.String("primaer", primary), zap.String("ersatz", fallback))
			}
			return
		}
		resumeDirPath = primary // beide gescheitert → Fehler zeigt den Originalpfad
	})
	return resumeDirPath
}

// dirWritable legt das Verzeichnis an und prüft per Testdatei, ob es beschreibbar ist.
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

func (fs *FileStore) sessionDir(uploadID string) string {
	return filepath.Join(fs.resumeDir(), sanitizeID(uploadID))
}

// sanitizeID erlaubt nur alphanumerische Zeichen (Schutz gegen Pfad-Traversal).
func sanitizeID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			out = append(out, c)
		}
	}
	return string(out)
}

// BeginResumeUpload legt eine neue Upload-Session an (oder gibt eine bestehende
// zurueck) und meldet welche Bloecke bereits vorhanden sind.
func (fs *FileStore) BeginResumeUpload(uploadID, fileName, mimeType string,
	totalSize, blockSize int64, redundancy int) (*ResumeSession, []int, error) {

	if blockSize <= 0 {
		blockSize = 4 * 1024 * 1024 // 4 MiB Default
	}
	if redundancy < MinReplicas {
		redundancy = MinReplicas
	}
	if redundancy > MaxReplicas {
		redundancy = MaxReplicas
	}
	id := sanitizeID(uploadID)
	if id == "" {
		return nil, nil, fmt.Errorf("ungueltige upload_id")
	}
	dir := fs.sessionDir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("session-dir: %w", err)
	}

	totalBlocks := 0
	if totalSize > 0 {
		totalBlocks = int((totalSize + blockSize - 1) / blockSize)
	}

	sess := &ResumeSession{
		UploadID:    id,
		FileName:    fileName,
		MimeType:    mimeType,
		TotalSize:   totalSize,
		BlockSize:   blockSize,
		TotalBlocks: totalBlocks,
		Redundancy:  redundancy,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	// Bestehende Meta laden (falls Reconnect) – sonst neu schreiben
	metaPath := filepath.Join(dir, "session.json")
	if raw, err := os.ReadFile(metaPath); err == nil {
		var existing ResumeSession
		if json.Unmarshal(raw, &existing) == nil && existing.TotalSize == totalSize {
			sess = &existing
			sess.UpdatedAt = time.Now().UTC()
		}
	}
	if data, err := json.Marshal(sess); err == nil {
		_ = os.WriteFile(metaPath, data, 0o640)
	}

	return sess, fs.haveBlocks(id), nil
}

// haveBlocks listet die Indizes bereits hochgeladener Bloecke.
func (fs *FileStore) haveBlocks(uploadID string) []int {
	dir := fs.sessionDir(uploadID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var have []int
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".part" {
			continue
		}
		idxStr := name[:len(name)-len(".part")]
		if idx, err := strconv.Atoi(idxStr); err == nil {
			have = append(have, idx)
		}
	}
	sort.Ints(have)
	return have
}

// StoreResumeBlock speichert einen einzelnen Block atomar.
func (fs *FileStore) StoreResumeBlock(uploadID string, index int, r io.Reader) error {
	id := sanitizeID(uploadID)
	dir := fs.sessionDir(id)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("unbekannte session")
	}
	if index < 0 {
		return fmt.Errorf("ungueltiger block-index")
	}
	final := filepath.Join(dir, strconv.Itoa(index)+".part")
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, final)
}

// StartFinishResumeUpload startet die Finalisierung im Hintergrund und kehrt
// SOFORT zurueck. Der Fortschritt wird ueber GetResumeStatus abgefragt. So
// gibt es keinen HTTP-Timeout mehr bei grossen Dateien (die Verschluesselung
// kann Minuten dauern). Gibt einen Fehler nur zurueck, wenn die Session
// unvollstaendig ist (sofort pruefbar).
func (fs *FileStore) StartFinishResumeUpload(uploadID string) error {
	id := sanitizeID(uploadID)
	dir := fs.sessionDir(id)

	metaPath := filepath.Join(dir, "session.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("session-meta nicht gefunden: %w", err)
	}
	var sess ResumeSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return fmt.Errorf("session-meta ungueltig: %w", err)
	}

	have := fs.haveBlocks(id)
	if sess.TotalBlocks > 0 && len(have) != sess.TotalBlocks {
		return fmt.Errorf("unvollstaendig: %d von %d Bloecken", len(have), sess.TotalBlocks)
	}

	// Doppelstart-Schutz: Wenn bereits eine Finalisierung läuft (.finalizing
	// existiert) oder abgeschlossen ist (status=done), nicht erneut starten.
	// Sonst laufen mehrere Goroutinen und das Frontend pollt evtl. eine
	// Session, die nie sauber abschließt.
	if _, err := os.Stat(filepath.Join(dir, ".finalizing")); err == nil {
		return nil // läuft schon — kein Fehler, Frontend pollt /status
	}
	if st, ok := fs.GetResumeStatus(id); ok && st.State == "done" {
		return nil // schon fertig
	}

	_ = os.WriteFile(filepath.Join(dir, ".finalizing"),
		[]byte(time.Now().UTC().Format(time.RFC3339)), 0o640)
	fs.writeStatus(dir, ResumeStatus{State: "finalizing", FileName: sess.FileName})

	go fs.runFinish(id, dir, sess, have)
	return nil
}

// runFinish setzt die Bloecke zusammen, schiebt sie durch die Upload-Pipeline
// und schreibt das Ergebnis in status.json.
func (fs *FileStore) runFinish(id, dir string, sess ResumeSession, have []int) {
	fs.log.Info("Finalisierung gestartet",
		zap.String("file", sess.FileName),
		zap.Int("bloecke", len(have)),
		zap.Int("redundanz", sess.Redundancy))

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for i := 0; i < len(have); i++ {
			part := filepath.Join(dir, strconv.Itoa(have[i])+".part")
			f, oerr := os.Open(part)
			if oerr != nil {
				pw.CloseWithError(oerr)
				return
			}
			if _, cerr := io.Copy(pw, f); cerr != nil {
				f.Close()
				pw.CloseWithError(cerr)
				return
			}
			f.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	// Fortschritts-Callback: schreibt den Chunking-Fortschritt in status.json,
	// damit das Frontend ihn pollen und anzeigen kann. Gedrosselt auf max. alle
	// 500ms, um die Disk nicht mit status-Writes zu überlasten.
	var lastWrite time.Time
	fs.setProgressCb(func(phase string, done, total int) {
		now := time.Now()
		if done < total && now.Sub(lastWrite) < 500*time.Millisecond {
			return
		}
		lastWrite = now
		fs.writeStatus(dir, ResumeStatus{
			State: "finalizing", FileName: sess.FileName,
			Phase: phase, ChunksDone: done, ChunksTotal: total,
		})
	})
	defer fs.setProgressCb(nil) // nach der Finalisierung Callback entfernen

	hash, uerr := fs.UploadWithRedundancy(ctx, pr, sess.MimeType, sess.FileName, sess.Redundancy)
	if uerr != nil {
		fs.log.Warn("Finalisierung fehlgeschlagen", zap.String("file", sess.FileName), zap.Error(uerr))
		fs.writeStatus(dir, ResumeStatus{State: "error", Error: uerr.Error(), FileName: sess.FileName})
		_ = os.Remove(filepath.Join(dir, ".finalizing"))
		return
	}

	for i := 0; i < len(have); i++ {
		_ = os.Remove(filepath.Join(dir, strconv.Itoa(have[i])+".part"))
	}
	_ = os.Remove(filepath.Join(dir, ".finalizing"))
	fs.writeStatus(dir, ResumeStatus{State: "done", ContentHash: hash, FileName: sess.FileName})
	fs.log.Info("Finalisierung abgeschlossen, Status=done",
		zap.String("file", sess.FileName),
		zap.String("hash", hash[:16]+"…"))
}

// AbortResumeUpload verwirft eine laufende Session.
func (fs *FileStore) AbortResumeUpload(uploadID string) {
	_ = os.RemoveAll(fs.sessionDir(sanitizeID(uploadID)))
}

// GCResumeSessions raeumt Sessions die seit maxAge KEINE Aktivitaet mehr
// hatten. Maßgeblich ist der juengste Zeitstempel aller Dateien in der Session
// (Block-Parts + session.json), nicht die Verzeichnis-mtime — sonst koennte
// ein langsamer, aber aktiver Upload faelschlich geloescht werden.
func (fs *FileStore) GCResumeSessions(maxAge time.Duration) {
	entries, err := os.ReadDir(fs.resumeDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessDir := filepath.Join(fs.resumeDir(), e.Name())
		// Wird gerade finalisiert? → niemals loeschen
		if _, err := os.Stat(filepath.Join(sessDir, ".finalizing")); err == nil {
			continue
		}
		if fs.sessionIdleSince(sessDir) > maxAge {
			_ = os.RemoveAll(sessDir)
		}
	}
}

// sessionIdleSince liefert wie lange in einer Session NICHTS mehr geschrieben
// wurde (basierend auf der juengsten Datei darin).
func (fs *FileStore) sessionIdleSince(sessDir string) time.Duration {
	files, err := os.ReadDir(sessDir)
	if err != nil {
		return 0 // unklar → nicht loeschen
	}
	newest := time.Time{}
	for _, f := range files {
		info, err := f.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		// Leere Session (kein Block, keine Meta) → Verzeichnis-Alter nutzen
		if di, err := os.Stat(sessDir); err == nil {
			return time.Since(di.ModTime())
		}
		return 0
	}
	return time.Since(newest)
}

// =============================================================================
//  Range-Download (für Video-Seeking und resumefähige Downloads)
// =============================================================================

// loadManifest holt das Manifest (lokal → direkte Peers → DHT) und parst es.
func (fs *FileStore) loadManifest(ctx context.Context, contentHash string) (*FileManifest, error) {
	manifestKey := "manifest_" + contentHash
	manifestData, _ := fs.loadChunkLocal(manifestKey)
	if len(manifestData) == 0 {
		directPeers := fs.selectPeers(8)
		if len(directPeers) > 0 {
			if d, e := fs.fetchChunkFromPeers(ctx, manifestKey, directPeers); e == nil && len(d) > 0 {
				manifestData = d
				_ = fs.storeChunkLocal(manifestKey, d)
			}
		}
	}
	if len(manifestData) == 0 {
		dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		d, err := fs.p2p.DHTget(dctx, DHTNamespaceFile+contentHash)
		if err != nil {
			return nil, fmt.Errorf("manifest nicht gefunden: %w", err)
		}
		manifestData = d
	}
	var m FileManifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return nil, fmt.Errorf("manifest unmarshal: %w", err)
	}
	// Mehrstufiges Manifest auflösen: Sub-Manifeste laden, vollständige
	// Chunk-Hash-Liste rekonstruieren. So funktionieren DownloadRange, FileSize
	// und alle Konsumenten transparent auch für große (128-GiB-)Dateien.
	if len(m.ManifestChunks) > 0 {
		subDatas := make([][]byte, 0, len(m.ManifestChunks))
		for i, subHash := range m.ManifestChunks {
			d, ferr := fs.fetchChunk(ctx, subHash)
			if ferr != nil {
				return nil, fmt.Errorf("sub-manifest %d/%d nicht abrufbar: %w",
					i+1, len(m.ManifestChunks), ferr)
			}
			subDatas = append(subDatas, d)
		}
		full, rerr := reassembleFromSubManifests(subDatas)
		if rerr != nil {
			return nil, fmt.Errorf("sub-manifest rekonstruieren: %w", rerr)
		}
		m.ChunkHashes = full
	}
	return &m, nil
}

// FileSize liefert die Klartext-Groesse einer Datei (fuer Content-Length/Range).
func (fs *FileStore) FileSize(ctx context.Context, contentHash string) (int64, string, error) {
	m, err := fs.loadManifest(ctx, contentHash)
	if err != nil {
		return 0, "", err
	}
	return m.Size, m.MimeType, nil
}

// DownloadAvailability prüft, wie viele Chunks der Datei lokal vorhanden sind.
// Gibt (vorhanden, gesamt, bereit) zurück. "bereit" ist true, wenn alle Chunks
// lokal liegen — dann kann der Download ohne Lücke streamen. Nutzt nur billige
// In-Memory-Checks (kein Disk-/Netz-Zugriff für die Chunk-Daten). Bei mehr-
// stufigen Manifesten werden auch die Sub-Manifeste mitgezählt.
func (fs *FileStore) DownloadAvailability(ctx context.Context, contentHash string) (have, total int, ready bool, err error) {
	m, err := fs.loadManifest(ctx, contentHash)
	if err != nil {
		return 0, 0, false, err
	}
	// Vollständige Chunk-Liste rekonstruieren (bei mehrstufigem Manifest erst
	// die Sub-Manifeste laden).
	hashes := m.ChunkHashes
	if len(m.ManifestChunks) > 0 {
		subDatas := make([][]byte, 0, len(m.ManifestChunks))
		for _, subHash := range m.ManifestChunks {
			d, ferr := fs.fetchChunk(ctx, subHash)
			if ferr != nil {
				// Sub-Manifest fehlt → noch nicht bereit.
				return 0, len(m.ManifestChunks), false, nil
			}
			subDatas = append(subDatas, d)
		}
		full, rerr := reassembleFromSubManifests(subDatas)
		if rerr != nil {
			return 0, 0, false, rerr
		}
		hashes = full
	}
	total = len(hashes)
	fs.mu.RLock()
	for _, h := range hashes {
		if _, ok := fs.chunks[h]; ok {
			have++
		}
	}
	fs.mu.RUnlock()
	return have, total, have == total, nil
}

// DownloadRange schreibt den Byte-Bereich [start, end] (inklusiv) in w.
// Nur die benoetigten Chunks werden geholt und entschluesselt — effizient
// fuer Video-Seeking, da nicht die ganze Datei verarbeitet werden muss.
func (fs *FileStore) DownloadRange(ctx context.Context, contentHash string,
	start, end int64, w io.Writer) error {

	m, err := fs.loadManifest(ctx, contentHash)
	if err != nil {
		return err
	}
	if start < 0 {
		start = 0
	}
	if end <= 0 || end >= m.Size {
		end = m.Size - 1
	}
	if start > end {
		return fmt.Errorf("ungueltiger range")
	}

	var encKey []byte
	if !m.Plaintext {
		encKey = fs.deriveEncKey(contentHash)
	}

	// Klartext-Chunks sind ChunkSize gross. Start-/End-Chunk berechnen.
	startChunk := int(start / ChunkSize)
	endChunk := int(end / ChunkSize)

	for i := startChunk; i <= endChunk && i < len(m.ChunkHashes); i++ {
		chunk, ferr := fs.fetchChunk(ctx, m.ChunkHashes[i])
		if ferr != nil {
			return fmt.Errorf("chunk %d: %w", i, ferr)
		}
		plain := chunk
		if !m.Plaintext {
			var derr error
			plain, derr = fs.decryptChunk(chunk, encKey, i)
			if derr != nil {
				return fmt.Errorf("chunk %d entschluesseln: %w", i, derr)
			}
		}
		// Innerhalb des ersten/letzten Chunks auf den Range zuschneiden
		chunkStart := int64(i) * ChunkSize
		lo := int64(0)
		hi := int64(len(plain))
		if start > chunkStart {
			lo = start - chunkStart
		}
		if end < chunkStart+int64(len(plain))-1 {
			hi = end - chunkStart + 1
		}
		if lo < 0 {
			lo = 0
		}
		if hi > int64(len(plain)) {
			hi = int64(len(plain))
		}
		if lo >= hi {
			continue
		}
		if _, err := w.Write(plain[lo:hi]); err != nil {
			return err
		}
	}
	return nil
}

// ListResumeSessions liefert eine Uebersicht aller laufenden Resume-Uploads
// (fuer Admin/Diagnose). Zeigt Fortschritt pro Session.
func (fs *FileStore) ListResumeSessions() []map[string]any {
	entries, err := os.ReadDir(fs.resumeDir())
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		var sess ResumeSession
		if raw, err := os.ReadFile(filepath.Join(fs.sessionDir(id), "session.json")); err == nil {
			_ = json.Unmarshal(raw, &sess)
		}
		have := len(fs.haveBlocks(id))
		info, _ := e.Info()
		var age string
		if info != nil {
			age = time.Since(info.ModTime()).Round(time.Minute).String()
		}
		out = append(out, map[string]any{
			"upload_id":    id,
			"file_name":    sess.FileName,
			"total_blocks": sess.TotalBlocks,
			"have_blocks":  have,
			"total_size":   sess.TotalSize,
			"age":          age,
		})
	}
	return out
}

// PurgeResumeSessions loescht ALLE laufenden Resume-Uploads (Admin-Aktion).
func (fs *FileStore) PurgeResumeSessions() int {
	entries, err := os.ReadDir(fs.resumeDir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			_ = os.RemoveAll(filepath.Join(fs.resumeDir(), e.Name()))
			n++
		}
	}
	return n
}

// FreeBytes liefert den noch verfuegbaren reservierten Speicher in Bytes.
func (fs *FileStore) FreeBytes() int64 {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	maxBytes := fs.cfg.AllocGB * 1024 * 1024 * 1024
	free := maxBytes - fs.used
	if free < 0 {
		free = 0
	}
	return free
}

// FileInfo ist ein Eintrag für die Dateiliste im Filemanager.
type FileInfo struct {
	Hash     string `json:"hash"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type,omitempty"`
	Shared   bool   `json:"shared"` // true = öffentlich im Netzwerk freigegeben
	IsDir    bool   `json:"is_dir,omitempty"` // Ordner (Hash = Manifest-Hash)
	Count    int    `json:"count,omitempty"`  // Dateianzahl im Ordner
}

// ListFiles liefert alle Dateien aus dem lokalen Index (neueste zuerst).
// Nutzt den lokalen Index (immer aktiv), nicht den optionalen DHT-Index.
func (fs *FileStore) ListFiles() []FileInfo {
	if fs.localIdx == nil {
		return nil
	}
	entries := fs.localIdx.List()
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, FileInfo{
			Hash:     e.Hash,
			Name:     e.Name,
			Size:     e.Size,
			MimeType: e.MimeType,
			Shared:   fs.IsShared(e.Hash),
			IsDir:    e.IsDir,
			Count:    e.Count,
		})
	}
	return out
}

// --- Geteilte Dateien (öffentlich auffindbar) ---

// ShareFile markiert eine eigene Datei als öffentlich geteilt.
func (fs *FileStore) ShareFile(hash, name string, size int64, mime string, encrypted bool) {
	if fs.shared == nil {
		return
	}
	fs.shared.Add(&SharedFile{
		Hash: hash, Name: name, Size: size, MimeType: mime, Encrypted: encrypted,
	})
}

// UnshareFile hebt die Freigabe auf.
func (fs *FileStore) UnshareFile(hash string) {
	if fs.shared != nil {
		fs.shared.Remove(hash)
	}
}

// IsShared prüft, ob ein Hash freigegeben ist.
func (fs *FileStore) IsShared(hash string) bool {
	return fs.shared != nil && fs.shared.IsShared(hash)
}

// ListShared liefert alle eigenen geteilten Dateien.
func (fs *FileStore) ListShared() []SharedFile {
	if fs.shared == nil {
		return nil
	}
	return fs.shared.List()
}

// SearchShared durchsucht die lokal geteilten Dateien (für Such-Pings).
func (fs *FileStore) SearchShared(query string, max int) []SharedFile {
	if fs.shared == nil {
		return nil
	}
	return fs.shared.Search(query, max)
}

// RemoveFromIndex entfernt eine Datei aus dem lokalen Index und der Freigabe
// und gibt ihre Chunks frei (Reference Counting): Chunks, die nur zu dieser
// Datei gehörten, werden physisch gelöscht; von anderen Dateien oder fürs Netz
// gehostete Chunks bleiben erhalten.
func (fs *FileStore) RemoveFromIndex(hash string) {
	// Sofort aus der eigenen Dateiliste und der Freigabe nehmen – damit
	// verschwindet die Datei für den Nutzer unmittelbar.
	if fs.localIdx != nil {
		fs.localIdx.Remove(hash)
	}
	if fs.shared != nil {
		fs.shared.Remove(hash)
	}
	// Chunks freigeben kann bei großen Dateien (Videos: hunderte Chunk-Dateien
	// auf der SD-Karte) lange dauern → im Hintergrund. Reihenfolge bleibt:
	// erst Chunks über das Manifest freigeben, dann den Chunk-Index-Eintrag.
	go func() {
		fs.releaseFileChunks(hash)
		if fs.index != nil {
			fs.index.Remove(hash)
		}
	}()
}

// releaseFileChunks gibt alle Chunks frei, die zu einer Datei gehören (Owner
// "file:<hash>"). Liest das lokale Manifest (inkl. Sub-Manifeste), um die volle
// Chunk-Liste zu bekommen.
func (fs *FileStore) releaseFileChunks(contentHash string) {
	if fs.chunkRefs == nil {
		return
	}
	owner := "file:" + contentHash
	// Lokales Manifest lesen (nur lokal, kein Netz).
	manifestData, err := fs.loadChunkLocal("manifest_" + contentHash)
	if err != nil || len(manifestData) == 0 {
		// Kein Manifest → wenigstens den Manifest-Key freigeben.
		_ = fs.releaseChunk("manifest_"+contentHash, owner)
		return
	}
	var m FileManifest
	if json.Unmarshal(manifestData, &m) == nil {
		// Direkte Chunks freigeben.
		for _, h := range m.ChunkHashes {
			_ = fs.releaseChunk(h, owner)
		}
		// Sub-Manifeste: deren Inhalte + die Sub-Manifest-Blöcke selbst.
		if len(m.ManifestChunks) > 0 {
			subDatas := make([][]byte, 0, len(m.ManifestChunks))
			for _, subHash := range m.ManifestChunks {
				if subData, serr := fs.loadChunkLocal(subHash); serr == nil {
					subDatas = append(subDatas, subData)
				}
			}
			if full, rerr := reassembleFromSubManifests(subDatas); rerr == nil {
				for _, h := range full {
					_ = fs.releaseChunk(h, owner)
				}
			}
			for _, subHash := range m.ManifestChunks {
				_ = fs.releaseChunk(subHash, owner)
			}
		}
	}
	// Manifest-Chunk selbst freigeben.
	_ = fs.releaseChunk("manifest_"+contentHash, owner)
}

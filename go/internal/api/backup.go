package api

// Automatische, verschlüsselte Sicherung (R537).
//
// Was: alles im Datenverzeichnis außer Neu-Ladbarem (Chain, Datei-Teile,
// Vorschaubilder, Temporäres) + /etc/fundus/fundus.env + Seed-Datei.
// Wie: tar.gz → XChaCha20-Poly1305 (Projektstandard wie Identität, Dateispeicher
// und Messenger) mit einem Schlüssel aus dem Sicherungspasswort
// (Argon2id). Der Node speichert nur den ABGELEITETEN Schlüssel (backup.json),
// damit die tägliche Sicherung ohne Passworteingabe läuft.
// Wohin: in den verteilten Dateispeicher (3 Kopien auf anderen Nodes).
// Wiederfinden: Aus dem Passwort entsteht eine zweite, unabhängige Kennung;
// unter ihr liegt ein kleiner Verweis auf die neueste Sicherung im Netz
// (Record "backup_ptr"). Auf einem neuen Pi genügt deshalb das Passwort.
// Einspielen: beim nächsten Start VOR dem Öffnen der Datenbank
// (ApplyStagedRestore in main.go).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
)

const (
	backupMagic     = "FNDBAK2\n" // 2 = XChaCha20-Poly1305
	backupMaxBytes  = 200 << 20 // 200 MB unverschlüsselt (Pi-Speicher!)
	backupReplicas  = 3
	backupInterval  = 24 * time.Hour
	backupEtcDir    = "/etc/fundus"
	backupMinPwLen  = 12
)

type backupState struct {
	Enabled   bool   `json:"enabled"`
	Key       string `json:"key"`       // base64, 32 Byte (aus Passwort + Salt)
	Salt      string `json:"salt"`      // base64, 16 Byte
	LookupID  string `json:"lookup_id"` // hex – Kennung des Verweises im Netz
	LastHash  string `json:"last_hash,omitempty"`
	LastTime  int64  `json:"last_time,omitempty"`
	LastSize  int64  `json:"last_size,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

var (
	backupMu      sync.Mutex // eine Sicherung/Wiederherstellung gleichzeitig
	backupLoopOne sync.Once
)

func (s *Server) backupStatePath() string { return filepath.Join(s.cfg.DataDir, "backup.json") }

func (s *Server) loadBackupState() backupState {
	var st backupState
	if s.cfg == nil || s.cfg.DataDir == "" {
		return st
	}
	if raw, err := os.ReadFile(s.backupStatePath()); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (s *Server) saveBackupState(st backupState) {
	raw, _ := json.MarshalIndent(st, "", "  ")
	tmp := s.backupStatePath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.backupStatePath())
	}
}

// Schlüssel (mit Salt) und Such-Kennung (fester Kontext) aus dem Passwort.
func backupKey(pw string, salt []byte) []byte {
	return argon2.IDKey([]byte(pw), salt, 3, 64*1024, 2, 32)
}
func backupLookupID(pw string) string {
	return hex.EncodeToString(argon2.IDKey([]byte(pw), []byte("fundus-backup-lookup-v1"), 3, 64*1024, 2, 16))
}

// Nicht sichern: neu ladbar/erzeugbar oder flüchtig.
func backupSkip(name string, isDir bool) bool {
	switch name {
	case "chain", "blocks", "chain.db", "chunks", "thumb-cache", "tmp", "storage.alloc",
		"status.json", "update-status.json", ".finalizing", ".fundus-wtest", "orphan-suspects.json",
		"chain-move.pending", "restore-staging", "restore.pending", "restored-etc":
		return true
	}
	if strings.HasPrefix(name, "chain.") || strings.HasSuffix(name, ".tmp") {
		return true
	}
	return false
}

// buildBackupArchive: tar.gz mit data/… und etc/… (in den Speicher).
func (s *Server) buildBackupArchive() ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var total int64
	add := func(src, name string, info os.FileInfo) error {
		total += info.Size()
		if total > backupMaxBytes {
			return fmt.Errorf("Datenmenge über %d MB – Sicherung abgebrochen", backupMaxBytes>>20)
		}
		f, err := os.Open(src)
		if err != nil {
			return nil // nicht lesbar (z.B. Rechte) → überspringen
		}
		defer f.Close()
		hdr := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	}
	root := s.cfg.DataDir
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if backupSkip(top, info.IsDir()) || (info.Mode()&os.ModeSymlink) != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		return add(p, "data/"+filepath.ToSlash(rel), info)
	})
	if err != nil {
		return nil, err
	}
	for _, e := range []struct{ src, name string }{
		{filepath.Join(backupEtcDir, "fundus.env"), "etc/fundus.env"},
		{s.cfg.SeedFile, "etc/seed"},
	} {
		if e.src == "" {
			continue
		}
		if info, err := os.Stat(e.src); err == nil && !info.IsDir() {
			if err := add(e.src, e.name, info); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Format: magic(8) || salt(16) || nonce(24) || XChaCha20-Poly1305(tar.gz)
func backupSeal(key, plain []byte, salt []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(backupMagic), salt...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plain, []byte(backupMagic)), nil
}

func backupOpen(pw string, blob []byte) ([]byte, error) {
	ns := chacha20poly1305.NonceSizeX
	if len(blob) < len(backupMagic)+16+ns+chacha20poly1305.Overhead || string(blob[:len(backupMagic)]) != backupMagic {
		return nil, errors.New("keine Fundus-Sicherung (oder älteres Format – bitte neu sichern)")
	}
	p := blob[len(backupMagic):]
	salt, nonce, ct := p[:16], p[16:16+ns], p[16+ns:]
	aead, err := chacha20poly1305.NewX(backupKey(pw, salt))
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ct, []byte(backupMagic))
	if err != nil {
		return nil, errors.New("falsches Passwort oder beschädigte Sicherung")
	}
	return plain, nil
}

// runBackup erstellt, verschlüsselt, verteilt und verweist.
func (s *Server) runBackup(ctx context.Context) error {
	backupMu.Lock()
	defer backupMu.Unlock()
	st := s.loadBackupState()
	if !st.Enabled {
		return errors.New("Sicherung nicht eingerichtet")
	}
	fail := func(err error) error {
		st.LastError = err.Error()
		s.saveBackupState(st)
		return err
	}
	if s.fileStore == nil {
		return fail(errors.New("Dateispeicher aus – Sicherung braucht ihn zum Verteilen"))
	}
	key, err1 := base64.StdEncoding.DecodeString(st.Key)
	salt, err2 := base64.StdEncoding.DecodeString(st.Salt)
	if err1 != nil || err2 != nil || len(key) != 32 || len(salt) != 16 {
		return fail(errors.New("Sicherungsschlüssel beschädigt – bitte neu einrichten"))
	}
	plain, err := s.buildBackupArchive()
	if err != nil {
		return fail(err)
	}
	blob, err := backupSeal(key, plain, salt)
	if err != nil {
		return fail(err)
	}
	host, _ := os.Hostname()
	name := fmt.Sprintf("fundus-sicherung-%s-%s.fndbak", host, time.Now().Format("20060102-1504"))
	hash, err := s.fileStore.UploadWithRedundancy(ctx, bytes.NewReader(blob), "application/octet-stream", name, backupReplicas)
	if err != nil {
		return fail(fmt.Errorf("Ablage im Dateispeicher: %w", err))
	}
	st.LastHash, st.LastTime, st.LastSize, st.LastError = hash, time.Now().Unix(), int64(len(blob)), ""
	s.saveBackupState(st)
	s.publishBackupPointer(ctx, st, name)
	if s.log != nil {
		s.log.Info("Sicherung erstellt", zap.String("hash", hash), zap.Int("bytes", len(blob)))
	}
	return nil
}

// publishBackupPointer: Verweis auf die neueste Sicherung ins Netz.
func (s *Server) publishBackupPointer(ctx context.Context, st backupState, name string) {
	ownerID := ""
	if s.node != nil {
		ownerID = s.node.ID().String()
	}
	rec := &storage.Record{
		ID:        "backup-" + st.LookupID,
		Type:      storage.RecordBackupPtr,
		OwnerID:   ownerID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data: map[string]any{
			"hash": st.LastHash, "salt": st.Salt, "time": st.LastTime, "size": st.LastSize, "name": name,
		},
	}
	if s.node != nil {
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	_ = s.store.Put(rec)
	if s.node != nil {
		if raw, err := marshalRecord(rec); err == nil {
			_ = s.node.Publish(ctx, p2p.TopicBackup, raw)
		}
	}
}

// backupLoop: täglich sichern (erster Versuch 10 min nach dem Start).
func (s *Server) backupLoop() {
	time.Sleep(10 * time.Minute)
	for {
		st := s.loadBackupState()
		if st.Enabled && time.Since(time.Unix(st.LastTime, 0)) >= backupInterval {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if err := s.runBackup(ctx); err != nil && s.log != nil {
				s.log.Warn("Sicherung fehlgeschlagen", zap.Error(err))
			}
			cancel()
		}
		time.Sleep(time.Hour)
	}
}

// ── API (nur Heimnetz + Admin) ──────────────────────────────────────────────

func (s *Server) registerBackupRoutes() {
	if s.cfg == nil || s.cfg.DataDir == "" {
		return
	}
	g := s.router.Group("/api/v1/admin/backup")
	g.Use(s.adminAuthMiddleware())
	g.GET("/status", s.backupStatus)
	g.POST("/setup", s.backupSetup)
	g.POST("/run", s.backupRunNow)
	g.POST("/restore", s.backupRestore)
	backupLoopOne.Do(func() { go s.backupLoop() })
}

func (s *Server) backupStatus(c *gin.Context) {
	st := s.loadBackupState()
	c.JSON(http.StatusOK, gin.H{
		"enabled": st.Enabled, "last_hash": st.LastHash, "last_time": st.LastTime,
		"last_size": st.LastSize, "last_error": st.LastError, "replicas": backupReplicas,
		"filestore": s.fileStore != nil,
	})
}

func (s *Server) backupSetup(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len([]rune(req.Password)) < backupMinPwLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Bitte ein Sicherungspasswort mit mindestens %d Zeichen wählen.", backupMinPwLen)})
		return
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		s.internalError(c, err)
		return
	}
	st := s.loadBackupState()
	st.Enabled = true
	st.Salt = base64.StdEncoding.EncodeToString(salt)
	st.Key = base64.StdEncoding.EncodeToString(backupKey(req.Password, salt))
	st.LookupID = backupLookupID(req.Password)
	st.LastTime = 0 // mit neuem Passwort sofort neu sichern
	s.saveBackupState(st)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_ = s.runBackup(ctx)
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) backupRunNow(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	if err := s.runBackup(ctx); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	st := s.loadBackupState()
	c.JSON(http.StatusOK, gin.H{"ok": true, "hash": st.LastHash, "size": st.LastSize})
}

// POST /restore {password, hash?} – Sicherung finden, laden, entschlüsseln,
// bereitlegen; eingespielt wird beim Neustart vor dem Öffnen der Datenbank.
func (s *Server) backupRestore(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
		Hash     string `json:"hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Sicherungspasswort fehlt"})
		return
	}
	if s.fileStore == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dateispeicher aus – zum Laden der Sicherung nötig (FUNDUS_STORAGE_OFFER_GB ≠ 0)."})
		return
	}
	if !backupMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "Gerade läuft eine Sicherung – bitte kurz warten."})
		return
	}
	defer backupMu.Unlock()
	hash := strings.TrimSpace(req.Hash)
	if hash == "" {
		rec, err := s.store.Get(storage.RecordBackupPtr, "backup-"+backupLookupID(req.Password))
		if err != nil || rec == nil || rec.Data == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Keine Sicherung zu diesem Passwort gefunden. Ist der Node schon einige Minuten mit dem Netz verbunden? Sonst später erneut versuchen oder den Hash der Sicherung angeben."})
			return
		}
		hash, _ = rec.Data["hash"].(string)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	if err := s.fileStore.Download(ctx, hash, &buf); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Sicherung nicht abrufbar: " + err.Error()})
		return
	}
	plain, err := backupOpen(req.Password, buf.Bytes())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	staging := filepath.Join(s.cfg.DataDir, "restore-staging")
	_ = os.RemoveAll(staging)
	n, err := extractBackup(plain, staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Entpacken: " + err.Error()})
		return
	}
	if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "restore.pending"), []byte(hash), 0o600); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "files": n, "restart": true})
	// Neustart, damit die Daten VOR dem Öffnen der Datenbank eingespielt werden.
	go func() {
		time.Sleep(2 * time.Second)
		if s.log != nil {
			s.log.Warn("Wiederherstellung bereitgelegt – Neustart zum Einspielen")
		}
		os.Exit(3)
	}()
}

func extractBackup(plain []byte, dir string) (int, error) {
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return n, fmt.Errorf("ungültiger Pfad %q", h.Name) // Schutz gegen Pfad-Ausbruch
		}
		dst := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return n, err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return n, err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return n, err
		}
		f.Close()
		n++
	}
	return n, nil
}

// ApplyStagedRestore spielt eine bereitgelegte Wiederherstellung ein. Aufruf in
// main.go VOR storage.New (die Datenbank darf noch nicht geöffnet sein).
func ApplyStagedRestore(dataDir, seedFile string, log *zap.Logger) {
	marker := filepath.Join(dataDir, "restore.pending")
	if _, err := os.Stat(marker); err != nil {
		return
	}
	staging := filepath.Join(dataDir, "restore-staging")
	src := filepath.Join(staging, "data")
	// Datenbank vollständig ersetzen (keine Mischung alter/neuer LevelDB-Dateien).
	if _, err := os.Stat(filepath.Join(src, "db")); err == nil {
		_ = os.RemoveAll(filepath.Join(dataDir, "db"))
	}
	moved := 0
	_ = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dataDir, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o750)
		if os.Rename(p, dst) == nil {
			moved++
		}
		return nil
	})
	// /etc/fundus: nur, wo der Node schreiben darf; sonst zum Übernehmen ablegen.
	kept := []string{}
	for _, e := range []struct{ name, dst string }{
		{"fundus.env", filepath.Join(backupEtcDir, "fundus.env")},
		{"seed", seedFile},
	} {
		p := filepath.Join(staging, "etc", e.name)
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if e.dst != "" && os.WriteFile(e.dst, raw, 0o600) == nil {
			continue
		}
		keep := filepath.Join(dataDir, "restored-etc", e.name)
		_ = os.MkdirAll(filepath.Dir(keep), 0o700)
		_ = os.WriteFile(keep, raw, 0o600)
		kept = append(kept, e.name)
	}
	_ = os.RemoveAll(staging)
	_ = os.Remove(marker)
	if log != nil {
		log.Warn("Wiederherstellung eingespielt", zap.Int("dateien", moved), zap.Strings("von_hand_uebernehmen", kept))
	}
}

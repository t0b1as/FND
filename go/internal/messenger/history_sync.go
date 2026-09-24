package messenger

// Verlauf: Abgleich mit dem Heim-Node (Hybrid-Datenzugriff) und Aufräumen (TTL).
//
// Die Verlaufsdateien sind pro Nutzer mit dessen at-rest-Schlüssel verschlüsselt
// (eine base64-Zeile pro Nachricht). Daraus folgt:
//   - Der Heim-Node kann Zeilen OHNE Schlüssel speichern und ausliefern
//     (RawLines/AppendRawLine) – er sieht nie Klartext.
//   - Der Node, an dem der Nutzer gerade angemeldet ist, hat den Schlüssel in der
//     Session und kann die Zeilen des Heim-Nodes entschlüsseln und einmischen
//     (Merge) bzw. eigene Einträge für den Heim-Node verschlüsseln (EncryptEntry).
//   - Einzelne Nachrichten altern nur bei angemeldetem Nutzer aus (Prune); ganze
//     Nutzerverzeichnisse lange inaktiver Nutzer entfernt PruneInactiveUsers.

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/identity"
)

// rawMu serialisiert schlüssellose Zugriffe (Heim-Node-Seite).
var rawMu sync.Mutex

const maxRawLine = 1 << 20 // 1 MiB pro Verlaufszeile

func userDir(dataDir, myID string) string {
	return filepath.Join(dataDir, historyDirName, sanitizeID(myID))
}

// EncryptEntry verschlüsselt einen Eintrag als Verlaufszeile (für den Heim-Node).
func (h *HistoryStore) EncryptEntry(e HistoryEntry) (string, error) {
	plain, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	enc, err := identity.Encrypt(plain, h.key[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// Merge übernimmt Zeilen des Heim-Nodes, die lokal noch fehlen (nach ID).
// Page sortiert nach Zeitstempel, daher genügt Anhängen.
func (h *HistoryStore) Merge(peerID string, lines []string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	local, err := h.readAll(peerID)
	if err != nil {
		return 0, err
	}
	have := make(map[string]bool, len(local))
	for _, e := range local {
		if e.ID != "" {
			have[e.ID] = true
		}
	}
	var add []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || len(line) > maxRawLine {
			continue
		}
		raw, derr := base64.StdEncoding.DecodeString(line)
		if derr != nil {
			continue
		}
		plain, derr := identity.Decrypt(raw, h.key[:])
		if derr != nil {
			continue // fremder Schlüssel / defekt
		}
		var e HistoryEntry
		if json.Unmarshal(plain, &e) != nil || e.ID == "" || have[e.ID] {
			continue
		}
		have[e.ID] = true
		add = append(add, line)
	}
	if len(add) == 0 {
		return 0, nil
	}
	f, err := os.OpenFile(h.convPath(peerID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, fmt.Errorf("history: open: %w", err)
	}
	defer f.Close()
	for _, l := range add {
		if _, err := f.WriteString(l + "\n"); err != nil {
			return 0, err
		}
	}
	return len(add), nil
}

// Prune entfernt Nachrichten älter als maxAge aus allen Konversationen des
// angemeldeten Nutzers (braucht den Schlüssel). Gibt die Anzahl entfernter
// Nachrichten zurück.
func (h *HistoryStore) Prune(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	files, _ := filepath.Glob(filepath.Join(h.baseDir, "*.log"))
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, fp := range files {
		peer := strings.TrimSuffix(filepath.Base(fp), ".log")
		h.mu.Lock()
		entries, err := h.readAll(peer)
		if err != nil {
			h.mu.Unlock()
			continue
		}
		keep := entries[:0]
		for _, e := range entries {
			if e.Timestamp.After(cutoff) {
				keep = append(keep, e)
			}
		}
		n := len(entries) - len(keep)
		if n > 0 {
			if len(keep) == 0 {
				_ = os.Remove(fp)
			} else if err := h.writeAll(peer, keep); err != nil {
				h.mu.Unlock()
				continue
			}
			removed += n
		}
		h.mu.Unlock()
	}
	return removed, nil
}

// writeAll schreibt eine Konversation atomar neu (Aufrufer hält h.mu).
func (h *HistoryStore) writeAll(peerID string, entries []HistoryEntry) error {
	tmp := h.convPath(peerID) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	for _, e := range entries {
		line, err := h.EncryptEntry(e)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.WriteString(line + "\n"); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, h.convPath(peerID))
}

// ── Schlüssellose Zugriffe (Heim-Node) ──────────────────────────────────────

// RawConversations listet die Konversationen eines Nutzers (Dateinamen-IDs).
func RawConversations(dataDir, myID string) []string {
	files, _ := filepath.Glob(filepath.Join(userDir(dataDir, myID), "*.log"))
	out := make([]string, 0, len(files))
	for _, fp := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(fp), ".log"))
	}
	return out
}

// RawLines liefert die letzten max verschlüsselten Zeilen einer Konversation.
func RawLines(dataDir, myID, peerID string, max int) ([]string, error) {
	rawMu.Lock()
	defer rawMu.Unlock()
	f, err := os.Open(filepath.Join(userDir(dataDir, myID), sanitizeID(peerID)+".log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if max > 0 && len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines, sc.Err()
}

// AppendRawLine hängt eine (vom Nutzer verschlüsselte) Zeile an – ohne sie
// entschlüsseln zu können. Doppelte Zeilen werden übersprungen.
func AppendRawLine(dataDir, myID, peerID, line string) error {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > maxRawLine {
		return fmt.Errorf("history: ungültige Zeile")
	}
	if _, err := base64.StdEncoding.DecodeString(line); err != nil {
		return fmt.Errorf("history: kein base64")
	}
	existing, _ := RawLines(dataDir, myID, peerID, 0)
	for _, l := range existing {
		if l == line {
			return nil
		}
	}
	rawMu.Lock()
	defer rawMu.Unlock()
	dir := userDir(dataDir, myID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, sanitizeID(peerID)+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// PruneInactiveUsers löscht Verlaufsverzeichnisse, deren jüngste Datei älter als
// maxAge ist (Nutzer lange nicht mehr aktiv). Gibt die Anzahl zurück.
func PruneInactiveUsers(dataDir string, maxAge time.Duration) int {
	if maxAge <= 0 {
		return 0
	}
	base := filepath.Join(dataDir, historyDirName)
	dirs, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	n := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p := filepath.Join(base, d.Name())
		newest := time.Time{}
		files, _ := os.ReadDir(p)
		for _, f := range files {
			if info, err := f.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if newest.Before(cutoff) {
			if os.RemoveAll(p) == nil {
				n++
			}
		}
	}
	return n
}

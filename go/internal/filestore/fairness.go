package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ─── Speicher-Fairness (Give-to-Get, OfferGB-basiert) ────────────────────────
//
// Regel: Wer das Netz für N Kopien einer Datei nutzt, belegt (N-1)×Dateigröße
// FREMDEN Speicher (die eigene Kopie zählt als 1, die übrigen N-1 liegen auf
// anderen Nodes). Fair ist das nur, wenn der Uploader selbst mindestens so viel
// anbietet, wie er fremd belegt.
//
// Invariante: OfferGB (dem Netz angebotener Speicher) >= kumulativ fremd
// belegter Speicher (Summe über alle eigenen Uploads von (N-1)×Größe).
//
// Diese Datei führt einen persistenten Zähler der bereits verursachten Remote-
// Last und stellt die Vorab-Prüfung bereit. OfferGB ist sofort prüfbar (Config),
// daher harte Schranke ohne Netz-Roundtrip für Prüfung 1.

const fairnessFile = "remote-consumed.json"

// remoteConsumed persistiert die kumulativ fremd belegten Bytes.
type remoteConsumed struct {
	mu    sync.Mutex
	path  string
	Bytes int64 `json:"remote_consumed_bytes"`
}

func newRemoteConsumed(dataDir string) *remoteConsumed {
	rc := &remoteConsumed{path: filepath.Join(dataDir, fairnessFile)}
	if raw, err := os.ReadFile(rc.path); err == nil {
		var loaded remoteConsumed
		if json.Unmarshal(raw, &loaded) == nil {
			rc.Bytes = loaded.Bytes
		}
	}
	return rc
}

func (rc *remoteConsumed) get() int64 {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.Bytes
}

// add bucht zusätzliche fremd belegte Bytes und persistiert.
func (rc *remoteConsumed) add(delta int64) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.Bytes += delta
	if rc.Bytes < 0 {
		rc.Bytes = 0
	}
	if data, err := json.Marshal(struct {
		Bytes int64 `json:"remote_consumed_bytes"`
	}{rc.Bytes}); err == nil {
		_ = os.WriteFile(rc.path, data, 0o640)
	}
}

// remoteLoadForUpload berechnet die fremd belegte Last eines Uploads:
// (redundancy-1) × Dateigröße. Die eigene lokale Kopie zählt nicht.
func remoteLoadForUpload(fileSize int64, redundancy int) int64 {
	if redundancy < 1 {
		redundancy = 1
	}
	return int64(redundancy-1) * fileSize
}

// OfferFreeBytes gibt die noch freie Angebots-Kapazität zurück: der dem Netz
// angebotene Speicher (OfferGB) abzüglich der bereits durch eigene Uploads
// fremd belegten Bytes. Wird aus OfferGB − verbraucht berechnet, bleibt also
// korrekt, auch wenn OfferGB nachträglich geändert wird.
func (fs *FileStore) OfferFreeBytes() int64 {
	offerBytes := fs.cfg.OfferGB * 1024 * 1024 * 1024
	free := offerBytes - fs.remoteConsumed.get()
	if free < 0 {
		free = 0
	}
	return free
}

// checkFairness prüft, ob die freie Angebots-Kapazität die Remote-Last dieses
// Uploads deckt. Gibt nil zurück, wenn genug frei ist, sonst einen sprechenden
// Fehler. offerFreeBytes = bereits berechnete freie Menge (OfferGB − belegt).
func checkFairness(offerFreeBytes, fileSize int64, redundancy int) error {
	thisLoad := remoteLoadForUpload(fileSize, redundancy)
	if thisLoad > offerFreeBytes {
		return fmt.Errorf(
			"Speicher-Fairness: dieser Upload belegt %.2f GB fremden Speicher (Redundanz %d), "+
				"aber dein Angebot hat nur noch %.2f GB frei. "+
				"Erhöhe OfferGB oder wähle eine geringere Redundanz",
			float64(thisLoad)/1e9, redundancy,
			float64(offerFreeBytes)/1e9)
	}
	return nil
}

// CheckUploadAllowed führt BEIDE Vorab-Prüfungen aus, bevor ein Upload startet:
//  1. Fairness (OfferGB deckt die kumulative Remote-Last) — sofort prüfbar.
//  2. Verfügbarkeit (genug erreichbare Peers mit echtem freiem Platz) — netznah.
// Gibt beim ersten Verstoß einen sprechenden Fehler zurück (harte Schranke).
func (fs *FileStore) CheckUploadAllowed(ctx context.Context, fileSize int64, redundancy int) error {
	if redundancy < MinReplicas {
		redundancy = MinReplicas
	}
	if redundancy > MaxReplicas {
		redundancy = MaxReplicas
	}
	// Prüfung 1: Fairness gegen die freie Angebots-Kapazität (kein Netz-Roundtrip).
	if err := checkFairness(fs.OfferFreeBytes(), fileSize, redundancy); err != nil {
		return err
	}
	// Prüfung 2: tatsächliche Verfügbarkeit im erreichbaren Netz.
	if err := fs.checkAvailability(ctx, fileSize, redundancy); err != nil {
		return err
	}
	return nil
}

// bookRemoteLoad verbucht die durch einen erfolgreichen Upload verursachte
// Remote-Last (für die Fairness-Invariante künftiger Uploads).
func (fs *FileStore) bookRemoteLoad(fileSize int64, redundancy int) {
	if fs.remoteConsumed == nil {
		return
	}
	fs.remoteConsumed.add(remoteLoadForUpload(fileSize, redundancy))
}

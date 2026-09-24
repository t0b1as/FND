package api

// Externe Laufwerke unter /mnt erkennen, damit der Storage dorthin verlegt
// werden kann (mehr Kapazität für Chunks). Das tatsächliche Verlegen geschieht
// über FUNDUS_DATA_DIR (Deploy/Neustart) — ein Live-Umzug laufender Chunks
// wäre riskant, daher zeigt das Admin-UI nur an und gibt eine Anleitung.

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fundus/node/internal/filestore"
	"github.com/fundus/node/internal/helperproto"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type mountInfo struct {
	Device     string  `json:"device"`
	MountPoint string  `json:"mount_point"`
	FsType     string  `json:"fs_type"`
	UUID       string  `json:"uuid"` // stabile Dateisystem-UUID (für Wiedererkennung)
	TotalGB    float64 `json:"total_gb"`
	FreeGB     float64 `json:"free_gb"`
	UsedGB     float64 `json:"used_gb"`
	Accessible bool    `json:"accessible"`  // false = Node kann nicht lesen (udisks2-Mount) → Einbinden anbieten
	ForFundus  bool    `json:"for_fundus"`  // true = bereits unter /mnt/fundus- (vom Helper eingebunden)
}

// listMountedDrives liest /proc/mounts und liefert Laufwerke unter /mnt.
// deviceUUIDs liest die Zuordnung Device→UUID aus /dev/disk/by-uuid/. Die dort
// liegenden Symlinks zeigen von der UUID auf das Device (z.B. ../../sda1). Wir
// kehren das um, damit wir zu einem gemounteten Device die stabile UUID finden.
// Braucht weder root noch externe Tools.
func deviceUUIDs() map[string]string {
	out := make(map[string]string)
	dir := "/dev/disk/by-uuid"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		uuid := e.Name()
		target, err := os.Readlink(filepath.Join(dir, uuid))
		if err != nil {
			continue
		}
		// target ist relativ (z.B. ../../sda1) → zu absolutem /dev/-Pfad auflösen.
		dev := filepath.Clean(filepath.Join(dir, target))
		out[dev] = uuid
	}
	return out
}

func listMountedDrives() []mountInfo {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()

	uuids := deviceUUIDs()
	var out []mountInfo
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		device, mountPoint, fsType := fields[0], fields[1], fields[2]
		// Oktal-Escapes in /proc/mounts dekodieren (z.B. "My\040Passport").
		device = filestore.UnescapeMount(device)
		mountPoint = filestore.UnescapeMount(mountPoint)
		// Externe Datenträger: /mnt, /media oder /run/media (Desktop-Automount).
		isExt := strings.HasPrefix(mountPoint, "/mnt") ||
			strings.HasPrefix(mountPoint, "/media/") ||
			strings.HasPrefix(mountPoint, "/run/media/")
		if !isExt {
			continue
		}
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true

		freeBytes, totalBytes := filestore.DiskStatfs(mountPoint)
		// Ein Laufwerk, das per udisks2 nur für den Desktop-User gemountet ist,
		// liefert 0 (Permission denied für den Node-Service). Es TROTZDEM anzeigen —
		// mit accessible=false —, damit die UI anbieten kann, es per fundus-helper
		// zugänglich einzubinden (Remount nach /mnt/fundus-<uuid>).
		accessible := totalBytes > 0
		forFundus := strings.HasPrefix(mountPoint, "/mnt/fundus-")
		total := float64(totalBytes) / 1e9
		free := float64(freeBytes) / 1e9
		out = append(out, mountInfo{
			Device:     device,
			MountPoint: mountPoint,
			FsType:     fsType,
			UUID:       uuids[filepath.Clean(device)],
			TotalGB:    total,
			FreeGB:     free,
			UsedGB:     total - free,
			Accessible: accessible,
			ForFundus:  forFundus,
		})
	}
	return out
}

// GET /api/v1/admin/storage/drives – gemountete Laufwerke unter /mnt anzeigen
func (s *Server) adminListDrives(c *gin.Context) {
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("adminListDrives-Panic", zap.Any("grund", r))
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Laufwerks-Scan fehlgeschlagen", "detail": "siehe Node-Log"})
		}
	}()
	drives := listMountedDrives()
	dataDir := ""
	if s.cfg != nil {
		dataDir = s.cfg.DataDir
	}
	c.JSON(http.StatusOK, gin.H{
		"drives":           drives,
		"current_data_dir": dataDir,
	})
}

// GET /api/v1/admin/storage/volumes – freigegebene Speicherorte anzeigen
func (s *Server) adminListVolumes(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	vols := s.fileStore.ListVolumes()
	// Effektive Gesamtsumme = Summe der angezeigten (ggf. verteilten) Angebote,
	// damit die Übersicht zu den Reglern passt (nicht nur die persistierten).
	var effTotal float64
	for _, v := range vols {
		effTotal += v.OfferGB
	}
	c.JSON(http.StatusOK, gin.H{
		"volumes":         vols,
		"fairness_min_gb": s.fileStore.FairnessMinGB(),
		"total_offer_gb":  effTotal,
	})
}

// adminSetVolumeOffer setzt die Angebotsmenge eines Laufwerks.
// Body: {"path": "...", "offer_gb": 123.4}
func (s *Server) adminSetVolumeOffer(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var body struct {
		Path    string  `json:"path"`
		OfferGB float64 `json:"offer_gb"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path fehlt"})
		return
	}
	set, err := s.fileStore.SetVolumeOffer(body.Path, body.OfferGB)
	if err != nil {
		// Fairness-Unterschreitung o.ä. → 409 (fachlicher Konflikt), klare Meldung.
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"offer_gb":       set,
		"total_offer_gb": s.fileStore.TotalOfferGB(),
	})
}

// POST /api/v1/admin/storage/chain-move – Chain auf ein anderes Laufwerk
// verschieben. Body: {target}. Der eigentliche Move (kopieren, validieren,
// umschalten, löschen) läuft beim nächsten Node-Start, damit die DB nicht in
// Benutzung ist; dieser Handler schreibt nur die Anweisung und stößt den
// Neustart über den Helper an.
func (s *Server) adminChainMove(c *gin.Context) {
	if s.fileStore == nil || s.cfg == nil || s.cfg.DataDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target (Ziel-Laufwerkspfad) fehlt"})
		return
	}
	target := filepath.Clean(body.Target)

	// Sicherheit: Ziel MUSS ein erkanntes Laufwerk sein (aus der Volume-Liste),
	// damit kein beliebiger Pfad geschrieben wird. Primary (System) ausschließen.
	var match *filestore.VolumeInfo
	for _, v := range s.fileStore.ListVolumes() {
		if filepath.Clean(v.Path) == target {
			vv := v
			match = &vv
			break
		}
	}
	if match == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ziel ist kein erkanntes Laufwerk"})
		return
	}
	if match.Primary {
		c.JSON(http.StatusConflict, gin.H{"error": "Chain liegt bereits auf dem primären Laufwerk"})
		return
	}
	if !match.Online {
		c.JSON(http.StatusConflict, gin.H{"error": "Ziel-Laufwerk ist offline"})
		return
	}

	// Platzprüfung: die Chain-DB muss auf das Ziel passen (mit Sicherheitspuffer).
	chainDir := filepath.Join(s.cfg.DataDir, "chain")
	var chainBytes int64
	if fi, err := os.Stat(filepath.Join(chainDir, "chain.db")); err == nil {
		chainBytes = fi.Size()
	}
	needGB := float64(chainBytes)/1e9 + 0.5 // +0.5 GB Puffer
	if match.FreeGB > 0 && match.FreeGB < needGB {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Zu wenig Platz auf dem Ziel: %.1f GB frei, ~%.1f GB nötig", match.FreeGB, needGB)})
		return
	}

	// Anweisung schreiben (wird beim nächsten Start ausgeführt).
	pendingMove := filepath.Join(s.cfg.DataDir, "chain-move.pending")
	if err := os.WriteFile(pendingMove, []byte(target), 0o640); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Anweisung konnte nicht gespeichert werden"})
		return
	}

	// Node über den Helper neu starten → Move läuft beim Start.
	_, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionRestartNode})
	if err != nil {
		// Datei bleibt liegen; der Move läuft beim nächsten manuellen Neustart.
		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"pending": true,
			"note":    "Verschiebung vorgemerkt, aber automatischer Neustart nicht möglich — Node bitte manuell neu starten (systemctl restart fundus-node).",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"restarting": true,
		"note":       "Node startet neu und verschiebt die Chain. Das kann je nach Chain-Größe einen Moment dauern.",
	})
}

// POST /api/v1/admin/storage/volumes – Speicherort freigeben {path, label}
func (s *Server) adminAddVolume(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var req struct {
		Path  string `json:"path"`
		Label string `json:"label"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pfad fehlt"})
		return
	}
	if err := s.fileStore.AddVolume(req.Path, req.Label); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "volumes": s.fileStore.ListVolumes()})
}

// DELETE /api/v1/admin/storage/volumes – Freigabe entfernen {path}
func (s *Server) adminRemoveVolume(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pfad fehlt"})
		return
	}
	if err := s.fileStore.RemoveVolume(req.Path); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "volumes": s.fileStore.ListVolumes()})
}

// POST /api/v1/admin/storage/cleanup?mode=scan|tmp|orphans
// Bereinigt verwaisten Chunk-Speicher. Default "scan" (zeigt nur an).
func (s *Server) adminCleanup(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	mode := c.Query("mode")
	if mode == "" {
		mode = "scan"
	}
	// orphan-hosted: verwaiste gehostete Replikate SOFORT freigeben (ohne die
	// sonst übliche Grace Period der automatischen GC). Die Sicherheitsprüfung
	// "ist der Chunk im Netz noch verankert?" bleibt — nur die Wartezeit entfällt,
	// weil der Nutzer es bewusst auslöst.
	if mode == "orphan-hosted" {
		freed, count := s.fileStore.PurgeOrphanedHostedNow(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{
			"mode":            "orphan-hosted",
			"released_chunks": count,
			"released_bytes":  freed,
		})
		return
	}
	if mode != "scan" && mode != "tmp" && mode != "orphans" && mode != "cache" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Modus"})
		return
	}
	rep, err := s.fileStore.Cleanup(c.Request.Context(), mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

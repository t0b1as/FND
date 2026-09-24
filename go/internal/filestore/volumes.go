package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"go.uber.org/zap"
)

// =============================================================================
//  Volume-Manager — mehrere Speicherorte parallel (Multi-Volume-Storage)
// =============================================================================
//
// Ein Volume ist ein Speicherort für Chunks: das Haupt-DataDir oder ein
// zusätzlich freigegebenes Laufwerk/Verzeichnis (z.B. eine USB-Platte unter
// /mnt/...). Ziele:
//   - Vorhandene Chunks auf JEDEM Volume dem Netz bereitstellen (Lesen sucht
//     über alle Volumes).
//   - Neue Chunks dürfen auf jedem Volume mit Platz landen (mehr Netz-Kapazität).
//   - Daten werden NICHT verschoben — ein Volume wird nur zur Liste hinzugefügt.
//
// Persistenz: Die Volume-Liste liegt in <DataDir>/volumes.json. Das Haupt-
// DataDir ist immer Volume 0 und kann nicht entfernt werden.

// Volume beschreibt einen Speicherort.
type Volume struct {
	Path  string `json:"path"`  // Wurzelverzeichnis (chunks/ liegt darunter)
	Label string `json:"label"` // Anzeigename (z.B. Mount-Point oder "Hauptspeicher")
	// Primary kennzeichnet das Haupt-DataDir (Volume 0). Nicht entfernbar.
	Primary bool `json:"primary"`
	// UUID: stabile Dateisystem-UUID des Laufwerks (falls ermittelbar). Erlaubt
	// die Wiedererkennung, wenn dieselbe Platte an einem anderen Mount-Point
	// auftaucht. Leer für Ordner-Freigaben ohne eigenes Laufwerk.
	UUID string `json:"uuid,omitempty"`
	// Online: ob der Pfad aktuell erreichbar ist (z.B. USB eingesteckt). Wird
	// dynamisch geprüft; nicht persistiert.
	Online bool `json:"online"`
	// OfferGB: wie viel Platz DIESES Laufwerk dem Netz zur Verfügung stellt (GB).
	// Die Summe über alle Volumes ergibt die gesamte Angebotsmenge. Muss über der
	// Fairness-Untergrenze liegen (Summe ≥ was die eigenen Dateien als Redundanz
	// verursachen). 0 = dieses Laufwerk trägt nichts bei (nur eigene Nutzung).
	OfferGB float64 `json:"offer_gb"`
}

// chunksDir liefert das chunks-Unterverzeichnis dieses Volumes.
func (v Volume) chunksDir() string {
	return filepath.Join(v.Path, "chunks")
}

// chunkPathFor baut den vollständigen Pfad eines Chunks auf diesem Volume.
func (v Volume) chunkPathFor(hash string) string {
	return filepath.Join(v.Path, "chunks", hash[:2], hash)
}

// freeBytes ermittelt den freien Speicher dieses Volumes (via statfs).
func (v Volume) freeBytes() int64 {
	free, _ := diskStatfs(v.Path)
	return free
}

// totalBytes ermittelt die Gesamtkapazität dieses Volumes (via statfs). Obergrenze
// für den Offer-Regler des Laufwerks.
func (v Volume) totalBytes() int64 {
	_, total := diskStatfs(v.Path)
	return total
}

// volumeManager verwaltet die Liste der Volumes threadsicher.
type volumeManager struct {
	mu      sync.RWMutex
	log     *zap.Logger
	path    string   // volumes.json
	volumes []Volume // Volume[0] ist immer das Primary-DataDir
}

// newVolumeManager initialisiert mit dem Haupt-DataDir als Primary-Volume und
// lädt zusätzlich gespeicherte Volumes aus volumes.json.
func newVolumeManager(dataDir string, log *zap.Logger) *volumeManager {
	vm := &volumeManager{
		log:  log,
		path: filepath.Join(dataDir, "volumes.json"),
		volumes: []Volume{
			{Path: dataDir, Label: "Hauptspeicher", Primary: true, Online: true},
		},
	}
	vm.load()
	return vm
}

// list liefert eine Kopie aller Volumes.
func (vm *volumeManager) list() []Volume {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	out := make([]Volume, len(vm.volumes))
	copy(out, vm.volumes)
	return out
}

// add fügt ein zusätzliches Volume hinzu (Daten werden NICHT verschoben). Legt
// das chunks-Verzeichnis an und prüft Schreibrechte. Dedupliziert per Pfad.
func (vm *volumeManager) add(path, label string) error {
	clean := filepath.Clean(path)
	vm.mu.Lock()
	defer vm.mu.Unlock()
	for _, v := range vm.volumes {
		if filepath.Clean(v.Path) == clean {
			return fmt.Errorf("Volume %q ist bereits freigegeben", clean)
		}
	}
	// Verzeichnis + chunks/ anlegen, Schreibrecht prüfen.
	cd := filepath.Join(clean, "chunks")
	if err := os.MkdirAll(cd, 0750); err != nil {
		if isReadOnlyErr(err) {
			return fmt.Errorf("Laufwerk %q ist schreibgeschützt. Häufig bei exFAT/NTFS-Platten von Windows: bitte einmal an Windows sauber auswerfen oder auf dem Node 'sudo umount <gerät> && sudo fsck.exfat -y <gerät>' ausführen, dann neu einstecken", filepath.Base(clean))
		}
		return fmt.Errorf("Volume %q nicht beschreibbar: %w", clean, err)
	}
	probe := filepath.Join(clean, ".fundus-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0640); err != nil {
		if isReadOnlyErr(err) {
			return fmt.Errorf("Laufwerk %q ist schreibgeschützt. Häufig bei exFAT/NTFS-Platten von Windows: bitte einmal an Windows sauber auswerfen oder auf dem Node 'sudo umount <gerät> && sudo fsck.exfat -y <gerät>' ausführen, dann neu einstecken", filepath.Base(clean))
		}
		return fmt.Errorf("Volume %q nicht beschreibbar: %w", clean, err)
	}
	_ = os.Remove(probe)

	if label == "" {
		label = clean
	}
	// UUID des Laufwerks merken (für Wiedererkennung bei wechselndem Mount-Point).
	uuid := uuidForPath(clean)
	vm.volumes = append(vm.volumes, Volume{Path: clean, Label: label, UUID: uuid, Online: true})
	vm.persistLocked()
	return nil
}

// remove entfernt ein zusätzliches Volume aus der Liste (Daten bleiben liegen,
// werden danach aber nicht mehr durchsucht). Das Primary-Volume ist geschützt.
func (vm *volumeManager) remove(path string) error {
	clean := filepath.Clean(path)
	vm.mu.Lock()
	defer vm.mu.Unlock()
	for i, v := range vm.volumes {
		if filepath.Clean(v.Path) == clean {
			if v.Primary {
				return fmt.Errorf("Hauptspeicher kann nicht entfernt werden")
			}
			vm.volumes = append(vm.volumes[:i], vm.volumes[i+1:]...)
			vm.persistLocked()
			return nil
		}
	}
	return fmt.Errorf("Volume %q nicht gefunden", clean)
}

// volumeForNewChunk wählt das Volume mit dem meisten freien Platz für einen
// neuen Chunk. So füllen sich Volumes gleichmäßig und der Schreibzugriff
// verteilt sich.
// primaryVolume liefert das Primary-Volume (internes DataDir, immer beschreibbar).
func (vm *volumeManager) primaryVolume() Volume {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.volumes[0]
}

func (vm *volumeManager) volumeForNewChunk() Volume {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	// Primary-Volume (DataDir) ist immer verfügbar und der sichere Default.
	best := vm.volumes[0]
	bestFree := best.freeBytes()
	for _, v := range vm.volumes[1:] {
		// Offline-Volumes (abgezogene Platte) überspringen — sonst landen
		// Chunks im Nirgendwo.
		if !vm.pathUsable(v.Path) {
			continue
		}
		// Read-only-Volumes überspringen (z.B. exFAT mit dirty-Flag, das zur
		// Laufzeit auf read-only umgeschaltet hat). Sonst scheitert das Schreiben.
		if !isWritableDir(filepath.Join(v.Path, "chunks")) {
			continue
		}
		if f := v.freeBytes(); f > bestFree {
			best, bestFree = v, f
		}
	}
	return best
}

// isWritableDir prüft, ob in ein Verzeichnis geschrieben werden kann (legt es
// an, falls nötig, und testet mit einer temporären Datei). Erkennt read-only-
// Mounts zuverlässig — auch solche, die erst zur Laufzeit read-only wurden.
func isWritableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".fundus-wtest")
	if err := os.WriteFile(probe, []byte("x"), 0640); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// totalFreeBytes summiert den freien Speicher über alle Volumes.
func (vm *volumeManager) totalFreeBytes() int64 {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	var total int64
	for _, v := range vm.volumes {
		total += v.freeBytes()
	}
	return total
}

// totalOfferGB summiert die Angebotsmengen aller Volumes. Das ist die gesamte
// Menge, die dieser Node dem Netz zur Verfügung stellt.
func (vm *volumeManager) totalOfferGB() float64 {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	var total float64
	for _, v := range vm.volumes {
		total += v.OfferGB
	}
	return total
}

// setOffer setzt die Angebotsmenge eines Volumes (per Pfad). Begrenzt auf
// [0, physische Kapazität]. Die Fairness-Untergrenze (Summe ≥ Eigenbedarf) wird
// vom FileStore geprüft, bevor diese Methode gerufen wird. Gibt die tatsächlich
// gesetzte Menge zurück (ggf. gedeckelt).
func (vm *volumeManager) setOffer(path string, gb float64) (float64, error) {
	clean := filepath.Clean(path)
	vm.mu.Lock()
	defer vm.mu.Unlock()
	for i := range vm.volumes {
		if filepath.Clean(vm.volumes[i].Path) != clean {
			continue
		}
		if gb < 0 {
			gb = 0
		}
		// Nach oben auf die physische Kapazität des Laufwerks deckeln.
		capGB := float64(vm.volumes[i].totalBytes()) / (1024 * 1024 * 1024)
		if capGB > 0 && gb > capGB {
			gb = capGB
		}
		vm.volumes[i].OfferGB = gb
		vm.persistLocked()
		return gb, nil
	}
	return 0, fmt.Errorf("filestore: Volume nicht gefunden: %s", path)
}

// --- Persistenz -------------------------------------------------------------

func (vm *volumeManager) persistLocked() {
	// Nicht-primäre Volumes komplett speichern. Vom Primary-Volume das Angebot
	// (OfferGB) mitspeichern — sonst geht das Hauptspeicher-Angebot bei jedem
	// Neustart verloren (Primary wird sonst frisch mit Angebot 0 erzeugt).
	extra := make([]Volume, 0)
	for _, v := range vm.volumes {
		if !v.Primary {
			extra = append(extra, v)
		} else if v.OfferGB > 0 {
			// Primary nur wegen des Angebots mitspeichern (als Marker mit Primary=true).
			extra = append(extra, Volume{Path: v.Path, Primary: true, OfferGB: v.OfferGB})
		}
	}
	data, err := json.Marshal(extra)
	if err != nil {
		return
	}
	tmp := vm.path + ".tmp"
	if os.WriteFile(tmp, data, 0640) == nil {
		_ = os.Rename(tmp, vm.path)
	}
}

func (vm *volumeManager) load() {
	data, err := os.ReadFile(vm.path)
	if err != nil {
		return
	}
	var extra []Volume
	if json.Unmarshal(data, &extra) != nil {
		return
	}
	for _, v := range extra {
		// Gespeichertes Primary-Angebot: auf das bereits existierende Primary-Volume
		// (Volume 0) anwenden, statt ein Duplikat anzulegen. So überlebt das
		// Hauptspeicher-Angebot einen Neustart.
		if v.Primary {
			for i := range vm.volumes {
				if vm.volumes[i].Primary {
					vm.volumes[i].OfferGB = v.OfferGB
					break
				}
			}
			continue
		}
		v.Primary = false
		// UUID-basierte Wiedererkennung: Wenn der gespeicherte Pfad nicht mehr
		// existiert, aber das Laufwerk mit derselben UUID an einem ANDEREN
		// Mount-Point hängt, den Pfad aktualisieren. So überlebt die Freigabe
		// einen wechselnden Mount-Point (z.B. /mnt/usb0 → /mnt/usb1).
		// UUID nachtragen, falls sie fehlt (alte Freigaben ohne UUID). Nötig,
		// damit die Platte bei wechselndem Mount-Point wiedergefunden wird.
		if v.UUID == "" && vm.pathUsable(v.Path) {
			if u := uuidForPath(v.Path); u != "" {
				v.UUID = u
			}
		}
		if !vm.pathUsable(v.Path) && v.UUID != "" {
			if mp := findMountByUUID(v.UUID); mp != "" {
				v.Path = mp
			}
		}
		// Letzter Ausweg: Pfad nicht nutzbar UND keine UUID → per Basename (Label)
		// unter /media/*/ suchen (fängt "My Passport" vs "My Passport1" ab).
		if !vm.pathUsable(v.Path) && v.UUID == "" {
			if mp := findMountByBasename(filepath.Base(v.Path)); mp != "" {
				v.Path = mp
				if u := uuidForPath(mp); u != "" {
					v.UUID = u // gleich UUID nachtragen für die Zukunft
				}
			}
		}
		v.Online = vm.pathUsable(v.Path)
		vm.volumes = append(vm.volumes, v)
		if !v.Online && vm.log != nil {
			vm.log.Warn("Freigegebenes Volume aktuell offline (Platte abgezogen?)",
				zap.String("path", v.Path), zap.String("uuid", v.UUID))
		}
	}
}

// pathUsable prüft, ob ein Volume-Pfad erreichbar und beschreibbar ist.
func (vm *volumeManager) pathUsable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return true
}

// uuidForPath ermittelt die Dateisystem-UUID des Laufwerks, auf dem ein Pfad
// liegt. Dazu wird der längste passende Mount-Point aus /proc/mounts gesucht und
// dessen Device über /dev/disk/by-uuid/ einer UUID zugeordnet. Leer, wenn nicht
// ermittelbar (z.B. Ordner auf dem Root-Dateisystem). Kein root/Tool nötig.
func uuidForPath(path string) string {
	clean := filepath.Clean(path)
	// Mount-Points aus /proc/mounts lesen.
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	type mp struct{ dev, point string }
	var mounts []mp
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		mounts = append(mounts, mp{dev: f[0], point: f[1]})
	}
	// Längsten passenden Mount-Point finden (genauester Treffer).
	best := ""
	bestDev := ""
	for _, m := range mounts {
		if (clean == m.point || strings.HasPrefix(clean, m.point+"/")) && len(m.point) > len(best) {
			best = m.point
			bestDev = m.dev
		}
	}
	if bestDev == "" {
		return ""
	}
	// Device → UUID über /dev/disk/by-uuid/.
	entries, err := os.ReadDir("/dev/disk/by-uuid")
	if err != nil {
		return ""
	}
	bestDev = filepath.Clean(bestDev)
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/dev/disk/by-uuid", e.Name()))
		if err != nil {
			continue
		}
		dev := filepath.Clean(filepath.Join("/dev/disk/by-uuid", target))
		if dev == bestDev {
			return e.Name()
		}
	}
	return ""
}

// findMountByUUID sucht den aktuellen Mount-Point eines Laufwerks anhand seiner
// UUID. Liefert "" wenn die Platte nicht (mehr) gemountet ist.
func findMountByUUID(uuid string) string {
	if uuid == "" {
		return ""
	}
	target, err := os.Readlink(filepath.Join("/dev/disk/by-uuid", uuid))
	if err != nil {
		return ""
	}
	wantDev := filepath.Clean(filepath.Join("/dev/disk/by-uuid", target))
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if filepath.Clean(f[0]) == wantDev {
			return f[1]
		}
	}
	return ""
}

// refreshOnline aktualisiert den Online-Status aller Volumes (z.B. nachdem eine
// USB-Platte wieder angesteckt wurde). Legt das chunks-Verzeichnis bei wieder
// erreichbaren Volumes neu an.
func (vm *volumeManager) refreshOnline() {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	for i := range vm.volumes {
		if vm.volumes[i].Primary {
			vm.volumes[i].Online = true
			continue
		}
		// Pfad nicht erreichbar, aber UUID an anderem Mount-Point? → Pfad neu
		// setzen (Platte wurde an anderer Stelle wieder eingehängt).
		if !vm.pathUsable(vm.volumes[i].Path) && vm.volumes[i].UUID != "" {
			if mp := findMountByUUID(vm.volumes[i].UUID); mp != "" {
				vm.volumes[i].Path = mp
			}
		}
		nowOnline := vm.pathUsable(vm.volumes[i].Path)
		vm.volumes[i].Online = nowOnline
		if nowOnline {
			_ = os.MkdirAll(filepath.Join(vm.volumes[i].Path, "chunks"), 0750)
		}
	}
}

// sortedByFreeDesc liefert Volumes nach freiem Platz absteigend (für Scans).
func (vm *volumeManager) sortedByFreeDesc() []Volume {
	vols := vm.list()
	sort.Slice(vols, func(i, j int) bool {
		return vols[i].freeBytes() > vols[j].freeBytes()
	})
	return vols
}

// =============================================================================
//  FileStore-Methoden für Volume-Verwaltung (öffentliche API)
// =============================================================================

// VolumeInfo beschreibt ein Volume für die API (inkl. Live-Speicherdaten).
type VolumeInfo struct {
	Path    string  `json:"path"`
	Label   string  `json:"label"`
	Primary bool    `json:"primary"`
	Online  bool    `json:"online"`
	UUID    string  `json:"uuid"`
	FreeGB  float64 `json:"free_gb"`
	Chunks  int     `json:"chunks"`
	OfferGB float64 `json:"offer_gb"`  // was dieses Laufwerk dem Netz anbietet
	MinGB   float64 `json:"min_gb"`    // Untergrenze des Reglers (Fairness-Anteil dieses Laufwerks)
	TotalGB float64 `json:"total_gb"`  // physische Kapazität (Regler-Obergrenze)
	UsedGB  float64 `json:"used_gb"`   // eigene Belegung auf diesem Laufwerk
}

// ListVolumes liefert alle Speicherorte mit freiem Platz und Chunk-Zahl. Das
// umfasst freigegebene Volumes UND vom Helper eingehängte, noch nicht
// freigegebene Laufwerke (/mnt/fundus-*) — Letztere erscheinen als verfügbare
// Regler-Zeilen. Wo noch kein manuelles Angebot gesetzt ist, wird das
// kaskadierend über die Laufwerke verteilte Fairness-Minimum als Startwert
// gezeigt (statt 0), damit der Node die Fairness-Untergrenze von sich aus erfüllt.
func (fs *FileStore) ListVolumes() []VolumeInfo {
	fs.volumes.refreshOnline()
	vols := fs.volumes.list()
	// Entdeckte, noch nicht freigegebene gemountete Laufwerke anhängen.
	discovered := fs.discoverMountedVolumes()
	allVols := append(append([]Volume{}, vols...), discovered...)

	// Deduplizieren nach bereinigtem Pfad: Ein Laufwerk kann SOWOHL freigegeben
	// (vols) ALS AUCH als Mount entdeckt (discovered) sein — dann erschiene es
	// doppelt, die Regler würden dem falschen Laufwerk zugeordnet und die Minimum-
	// Verteilung (Map nach Pfad) überschriebe sich selbst. Freigegebene Einträge
	// haben Vorrang (sie tragen das gesetzte Angebot).
	seen := make(map[string]bool, len(allVols))
	deduped := make([]Volume, 0, len(allVols))
	for _, v := range allVols {
		key := filepath.Clean(v.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, v)
	}
	allVols = deduped

	counts := make(map[string]int)
	fs.mu.RLock()
	for _, vp := range fs.chunks {
		counts[vp]++
	}
	fs.mu.RUnlock()

	// Kapazität pro Laufwerk (GB) für die Minimum-Verteilung.
	capGB := func(v Volume) float64 {
		tb := float64(v.totalBytes())
		return tb / 1e9
	}
	// Summe der bereits manuell gesetzten Angebote.
	var manualSum float64
	for _, v := range allVols {
		manualSum += v.OfferGB
	}
	// Fehlt zum Fairness-Minimum etwas, kaskadierend auf die Laufwerke verteilen,
	// die noch KEIN manuelles Angebot haben (OfferGB == 0).
	minGB := fs.FairnessMinGB()
	distributed := map[string]float64{}
	if manualSum < minGB {
		var noOffer []Volume
		for _, v := range allVols {
			if v.OfferGB == 0 {
				noOffer = append(noOffer, v)
			}
		}
		distributed = distributeMinimum(noOffer, minGB-manualSum, capGB)
	}
	// minShares: das VOLLE Fairness-Minimum kaskadierend über alle Laufwerke
	// verteilt — die Regler-Untergrenze pro Laufwerk (unabhängig von manuellen
	// Angeboten). Das erste Laufwerk trägt bis zu seiner Kapazität, der Rest
	// fließt auf die nächsten.
	minShares := distributeMinimum(allVols, minGB, capGB)

	out := make([]VolumeInfo, 0, len(allVols))
	for _, v := range allVols {
		free, total, used := 0.0, 0.0, 0.0
		if v.Online {
			fb, tb := float64(v.freeBytes()), float64(v.totalBytes())
			free, total = fb/1e9, tb/1e9
			if tb > fb {
				used = (tb - fb) / 1e9
			}
		}
		// Effektives Angebot: manuell gesetzt, sonst der verteilte Minimum-Anteil.
		offer := v.OfferGB
		if offer == 0 {
			offer = distributed[filepath.Clean(v.Path)]
		}
		// Untergrenze des Reglers: der Fairness-Anteil DIESES Laufwerks. Er ergibt
		// sich aus der kaskadierenden Verteilung des vollen Minimums über alle
		// Laufwerke (unabhängig von manuellen Angeboten). Darunter darf der Regler
		// nicht, sonst würde die Fairness-Untergrenze verletzt.
		minShare := minShares[filepath.Clean(v.Path)]
		out = append(out, VolumeInfo{
			Path:    v.Path,
			Label:   v.Label,
			Primary: v.Primary,
			Online:  v.Online,
			UUID:    v.UUID,
			FreeGB:  free,
			Chunks:  counts[v.Path],
			OfferGB: offer,
			MinGB:   minShare,
			TotalGB: total,
			UsedGB:  used,
		})
	}
	return out
}

// AddVolume gibt einen zusätzlichen Speicherort frei (Laufwerk/Ordner). Daten
// werden NICHT verschoben; vorhandene Chunks dort werden eingelesen und dem Netz
// verfügbar gemacht, neuer Platz steht für weitere Chunks bereit.
func (fs *FileStore) AddVolume(path, label string) error {
	if err := fs.volumes.add(path, label); err != nil {
		return err
	}
	added := 0
	var scan func(volPath, dir string)
	scan = func(volPath, dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				scan(volPath, filepath.Join(dir, name))
				continue
			}
			if strings.HasSuffix(name, ".tmp") {
				continue
			}
			fs.mu.Lock()
			if _, seen := fs.chunks[name]; !seen {
				fs.chunks[name] = volPath
				if fi, err := e.Info(); err == nil {
					fs.used += fi.Size()
				}
				added++
			}
			fs.mu.Unlock()
		}
	}
	clean := filepath.Clean(path)
	scan(clean, filepath.Join(clean, "chunks"))
	if fs.log != nil {
		fs.log.Info("Volume freigegeben",
			zap.String("path", clean),
			zap.Int("vorhandene_chunks", added))
	}
	return nil
}

// RemoveVolume entfernt einen zusätzlichen Speicherort aus der Freigabe. Die
// dort liegenden Chunks werden aus dem Index genommen (Daten bleiben auf Disk).
func (fs *FileStore) RemoveVolume(path string) error {
	if err := fs.volumes.remove(path); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	fs.mu.Lock()
	for h, vp := range fs.chunks {
		if vp == clean {
			delete(fs.chunks, h)
		}
	}
	fs.mu.Unlock()
	if fs.log != nil {
		fs.log.Info("Volume aus Freigabe entfernt", zap.String("path", clean))
	}
	return nil
}

// FairnessMinGB ist die Untergrenze der Gesamt-Angebotsmenge: So viel Speicher
// verursachen die eigenen hochgeladenen Dateien als Redundanz im Netz
// ((replicas-1)×Größe, kumulativ in remoteConsumed gebucht). Fair ist, dem Netz
// mindestens ebenso viel zurückzugeben. Die Summe der Volume-Angebote darf diesen
// Wert nicht unterschreiten.
func (fs *FileStore) FairnessMinGB() float64 {
	if fs.remoteConsumed == nil {
		return 0
	}
	return float64(fs.remoteConsumed.get()) / 1e9
}

// TotalOfferGB ist die Summe der Angebote über alle Volumes.
func (fs *FileStore) TotalOfferGB() float64 {
	return fs.volumes.totalOfferGB()
}

// SetVolumeOffer setzt die Angebotsmenge eines Laufwerks. Die neue GESAMTSUMME
// (über alle Volumes) muss die Fairness-Untergrenze einhalten — sonst würde der
// Node weniger anbieten, als seine eigenen Dateien das Netz kosten. Gibt die
// tatsächlich gesetzte Menge zurück (ggf. auf die Laufwerkskapazität gedeckelt).
func (fs *FileStore) SetVolumeOffer(path string, gb float64) (float64, error) {
	clean := filepath.Clean(path)
	// Ist der Pfad ein entdecktes, aber noch nicht freigegebenes Laufwerk? Dann
	// automatisch freigeben, sobald ihm ein Angebot > 0 zugewiesen wird — so
	// werden gemountete Laufwerke (/mnt/fundus-*) beim ersten Schieben des
	// Reglers nutzbar. Bei Angebot 0 nichts freigeben (reine Anzeige).
	known := false
	for _, v := range fs.volumes.list() {
		if filepath.Clean(v.Path) == clean {
			known = true
			break
		}
	}
	if !known {
		if gb <= 0 {
			return 0, nil // nichts freizugeben, Angebot bleibt 0
		}
		// Prüfen, ob der Pfad ein tatsächlich ENTDECKTES, brauchbares Laufwerk ist
		// (nicht nur /mnt/fundus-*, sondern jeder erkannte externe Mount unter
		// /mnt oder /media). Nur dann freigeben — sonst ist es ein Fantasiepfad.
		var discLabel string
		isDiscovered := false
		for _, dv := range fs.discoverMountedVolumes() {
			if filepath.Clean(dv.Path) == clean {
				isDiscovered = true
				discLabel = dv.Label
				break
			}
		}
		if !isDiscovered {
			return 0, fmt.Errorf("filestore: Volume nicht gefunden: %s", path)
		}
		if discLabel == "" {
			discLabel = filepath.Base(clean)
		}
		if err := fs.AddVolume(clean, discLabel); err != nil {
			return 0, fmt.Errorf("filestore: Laufwerk freigeben: %w", err)
		}
	}

	// Prospektive Gesamtsumme berechnen: aktuelle Summe minus altes Offer dieses
	// Volumes plus neues (gedeckeltes) Offer.
	var oldOffer float64
	for _, v := range fs.volumes.list() {
		if filepath.Clean(v.Path) == clean {
			oldOffer = v.OfferGB
			break
		}
	}
	newTotal := fs.volumes.totalOfferGB() - oldOffer + gb
	minGB := fs.FairnessMinGB()
	if newTotal < minGB {
		return 0, fmt.Errorf("filestore: Gesamt-Angebot %.1f GB unterschreitet die Fairness-Untergrenze %.1f GB (was deine eigenen Dateien im Netz verursachen)", newTotal, minGB)
	}
	set, err := fs.volumes.setOffer(path, gb)
	if err != nil {
		return 0, err
	}
	// Globale OfferGB in der Config-Sicht aktualisieren, damit die
	// Fairness-Prüfung bei Uploads (die gegen cfg.OfferGB prüft) mitzieht.
	fs.cfg.OfferGB = int64(fs.volumes.totalOfferGB())

	// Wurde das Angebot VERRINGERT und liegt jetzt unter dem belegten Platz,
	// überzählige gehostete Chunks sicher abgeben (erst im Netz replizieren,
	// dann lokal freigeben). Asynchron, damit das UI nicht wartet.
	if gb < oldOffer {
		go fs.evacuateToOffer(context.Background())
	}
	if fs.log != nil {
		fs.log.Info("Volume-Angebot gesetzt",
			zap.String("path", path), zap.Float64("offer_gb", set),
			zap.Float64("total_offer_gb", fs.volumes.totalOfferGB()))
	}
	return set, nil
}

// isReadOnlyErr erkennt, ob ein Fehler von einem schreibgeschützten Dateisystem
// stammt (EROFS). Bei exFAT/NTFS von Windows passiert das, wenn der Treiber wegen
// errors=remount-ro auf read-only umschaltet (dirty-Flag / Metadaten-Inkonsistenz).
func isReadOnlyErr(err error) bool {
	return errors.Is(err, syscall.EROFS)
}

// findMountByBasename sucht einen gemounteten Pfad, dessen Basename dem
// gesuchten entspricht oder mit einem Ziffern-Suffix beginnt (udisks2 hängt bei
// Namenskollision als "<Label>1", "<Label>2" ein). Fängt den Fall ab, dass eine
// Platte unter "My Passport1" statt "My Passport" gemountet ist.
// Die /proc/mounts-Zeilen sind space-escaped (\040 für Leerzeichen).
func findMountByBasename(want string) string {
	if want == "" {
		return ""
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// Mount-Point: \040 → Leerzeichen zurückübersetzen.
		point := strings.ReplaceAll(f[1], "\\040", " ")
		base := filepath.Base(point)
		// Exakter Treffer oder "<want>" gefolgt von Ziffern (Kollisions-Suffix).
		if base == want {
			return point
		}
		if strings.HasPrefix(base, want) {
			suffix := base[len(want):]
			allDigits := suffix != ""
			for _, r := range suffix {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return point
			}
		}
	}
	return ""
}

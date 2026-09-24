package filestore

// Entdeckung gemounteter Laufwerke + kaskadierende Verteilung des
// Fairness-Minimums über mehrere Laufwerke.
//
// Hintergrund: Ein "Volume" ist ein Speicherort, auf dem der Node Chunks ablegt.
// Der fundus-helper hängt externe Laufwerke automatisch unter /mnt/fundus-<uuid>
// ein. Diese sollen im Angebots-UI als eigene Regler erscheinen — auch bevor sie
// explizit freigegeben sind. discoverMountedVolumes findet sie; die Freigabe
// erfolgt automatisch, sobald ihnen ein Angebot > 0 zugewiesen wird (setOffer).
//
// Verteilung: Das Fairness-Minimum (was die eigenen Dateien als Redundanz
// verursachen) wird kaskadierend über die verfügbaren Laufwerke verteilt — erst
// das erste bis zu seiner Kapazität, der Rest auf das nächste, usw. So greift die
// Verteilung auch bei mehr als zwei Partitionen.

import (
	"os"
	"path/filepath"
	"strings"
)

// mountBasePrefix ist das Präfix, unter dem der Helper Laufwerke einhängt.
const mountBasePrefix = "/mnt/fundus-"

// discoverMountedVolumes findet vom Helper eingehängte Laufwerke (/mnt/fundus-*),
// die noch NICHT als Volume freigegeben sind. Sie erscheinen dadurch im UI als
// verfügbare Regler-Zeilen. Bereits freigegebene Pfade werden ausgelassen
// (Duplikate vermeiden).
func (fs *FileStore) discoverMountedVolumes() []Volume {
	shared := make(map[string]bool)
	for _, v := range fs.volumes.list() {
		shared[filepath.Clean(v.Path)] = true
	}

	var out []Volume
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return out
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		dev, mp, fstype := unescapeMount(f[0]), unescapeMount(f[1]), f[2]
		clean := filepath.Clean(mp)
		if shared[clean] || seen[clean] {
			continue
		}
		if !isUsableDataMount(dev, clean, fstype) {
			continue
		}
		seen[clean] = true
		// Label + UUID ableiten: bei /mnt/fundus-<uuid> aus dem Pfad, sonst der
		// Mountpoint-Basename.
		var uuid, label string
		if strings.HasPrefix(clean, mountBasePrefix) {
			uuid = strings.TrimPrefix(clean, mountBasePrefix)
			label = "Laufwerk " + shortUUID(uuid)
		} else {
			label = filepath.Base(clean)
		}
		out = append(out, Volume{
			Path:   clean,
			Label:  label,
			UUID:   uuid,
			Online: true,
		})
	}
	return out
}

// unescapeMount dekodiert die Oktal-Escapes, die /proc/mounts für Sonderzeichen
// in Geräte- und Mountpfaden verwendet: \040=Leerzeichen, \011=Tab, \012=Newline,
// \134=Backslash usw. Ohne diese Dekodierung erschiene z.B. "My Passport" als
// "My\040Passport" — sowohl im Label als auch (fatal) im Dateipfad.
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '7' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			v := (int(s[i+1]-'0') << 6) | (int(s[i+2]-'0') << 3) | int(s[i+3]-'0')
			b.WriteByte(byte(v))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isUsableDataMount entscheidet, ob ein Mount ein echtes externes Datenlaufwerk
// ist, das als Speicher-Volume angeboten werden darf. Schließt Systempartitionen
// und Pseudo-Dateisysteme aus.
func isUsableDataMount(dev, mountpoint, fstype string) bool {
	// Nur echte Block-Devices (keine tmpfs/overlay/proc/sysfs/cgroup/…).
	if !strings.HasPrefix(dev, "/dev/") {
		return false
	}
	// Nur bekannte, beschreibbare Datei­systeme.
	switch fstype {
	case "ext4", "ext3", "ext2", "vfat", "exfat", "ntfs", "ntfs3", "btrfs", "xfs", "f2fs":
		// ok
	default:
		return false
	}
	// Systempfade ausschließen (Wurzel, Boot, Firmware, RAM-Disks).
	systemPaths := []string{"/", "/boot", "/boot/firmware", "/boot/efi", "/var", "/home", "/usr"}
	for _, sp := range systemPaths {
		if mountpoint == sp {
			return false
		}
	}
	// Die SD-Karte des Pi (mmcblk) ist das System-Medium → nicht anbieten, außer
	// sie ist bewusst unter /mnt, /media oder /run/media eingehängt (externer Datenträger).
	if strings.HasPrefix(dev, "/dev/mmcblk") &&
		!isExternalMountPath(mountpoint) {
		return false
	}
	// Konservativ: nur Mounts unter /mnt, /media oder /run/media als Daten-Volumes
	// anbieten. /run/media/<user>/ ist der Standard-Automount-Ort moderner Desktop-
	// Systeme (Manjaro, Ubuntu-Desktop mit udisks2) — ohne den würden extern
	// eingesteckte Laufwerke gar nicht als nutzbar erkannt.
	if !isExternalMountPath(mountpoint) {
		return false
	}
	return true
}

// isExternalMountPath erkennt die konventionellen Orte für externe Datenträger.
func isExternalMountPath(mountpoint string) bool {
	return strings.HasPrefix(mountpoint, "/mnt/") ||
		strings.HasPrefix(mountpoint, "/media/") ||
		strings.HasPrefix(mountpoint, "/run/media/")
}

// shortUUID kürzt eine UUID für die Anzeige (erste 8 Zeichen).
func shortUUID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

// distributeMinimum verteilt eine Zielmenge (GB) kaskadierend über die
// gegebenen Laufwerke: jedes Laufwerk bekommt so viel, wie es (bis zu seiner
// Kapazität) tragen kann, der Rest fließt zum nächsten. Reicht die
// Gesamtkapazität nicht, wird so viel wie möglich verteilt. Gibt eine Map
// Pfad→GB zurück. capacityGB(v) liefert die nutzbare Kapazität eines Laufwerks.
func distributeMinimum(vols []Volume, targetGB float64, capacityGB func(Volume) float64) map[string]float64 {
	out := make(map[string]float64, len(vols))
	remaining := targetGB
	for _, v := range vols {
		if remaining <= 0 {
			out[filepath.Clean(v.Path)] = 0
			continue
		}
		capV := capacityGB(v)
		give := remaining
		if give > capV {
			give = capV
		}
		out[filepath.Clean(v.Path)] = give
		remaining -= give
	}
	return out
}

// UnescapeMount ist der exportierte Zugang zu unescapeMount (Oktal-Escapes aus
// /proc/mounts dekodieren), damit auch das api-Paket ihn nutzen kann.
func UnescapeMount(s string) string { return unescapeMount(s) }

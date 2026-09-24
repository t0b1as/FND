//go:build !windows

package filestore

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// diskStatfs liefert freien und gesamten Speicher eines Pfads (Bytes).
//
// Primär via statfs-Syscall (schnell, keine externen Prozesse). ABER: Bei
// FUSE-Mounts (exFAT/NTFS über udisks2 unter /media/), die von root ohne
// allow_other gemountet wurden, verweigert der Kernel dem Node-User den
// statfs-Zugriff → (0, 0). In diesem Fall weichen wir auf `df` aus, das die
// Größe über einen anderen Weg ermittelt und diese Einschränkung nicht hat.
// So funktioniert der Speicher-Regler auch für udisks2-gemountete Platten,
// ohne fstab-Eintrag oder allow_other.
func diskStatfs(path string) (freeBytes, totalBytes int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) == nil {
		free := int64(st.Bavail) * int64(st.Bsize)
		total := int64(st.Blocks) * int64(st.Bsize)
		if total > 0 {
			return free, total
		}
	}
	// statfs scheiterte oder lieferte 0 → df-Fallback.
	return diskStatfsViaDF(path)
}

// diskStatfsViaDF ermittelt frei/gesamt via `df -B1 <path>` (Bytes, POSIX-Format).
func diskStatfsViaDF(path string) (freeBytes, totalBytes int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// -B1: Blockgröße 1 Byte (exakte Byte-Werte). -P: POSIX-Format (stabile Spalten).
	out, err := exec.CommandContext(ctx, "df", "-B1", "-P", path).Output()
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	// Letzte Zeile ist die Datenzeile. POSIX-Format:
	//   Filesystem  1-blocks  Used  Available  Capacity  Mounted on
	// Der Mountpunkt-Name kann Leerzeichen enthalten ("My Passport"), aber die
	// Zahlen stehen an festen Positionen VON VORNE (Felder 1,2,3 nach Filesystem).
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, 0
	}
	total, e1 := strconv.ParseInt(f[1], 10, 64)
	avail, e2 := strconv.ParseInt(f[3], 10, 64)
	if e1 != nil || e2 != nil {
		return 0, 0
	}
	return avail, total
}

// DiskStatfs ist der exportierte Zugang zu diskStatfs für andere Pakete (z.B.
// api), damit die plattformspezifische Logik nur hier lebt.
func DiskStatfs(path string) (freeBytes, totalBytes int64) { return diskStatfs(path) }

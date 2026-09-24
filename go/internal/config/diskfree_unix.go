//go:build linux || darwin || freebsd || netbsd || openbsd

package config

import "syscall"

// freeDiskGB ermittelt den freien Speicher (in GB) für das angegebene
// Verzeichnis via syscall.Statfs. Nur auf Unix-Systemen verfügbar — auf dem
// Pi (Linux) ist das der reale Pfad. Gibt (freeGB, true) bei Erfolg zurück,
// sonst (0, false).
func freeDiskGB(dir string) (int64, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, false
	}
	freeGB := (int64(stat.Bavail) * int64(stat.Bsize)) / (1024 * 1024 * 1024)
	return freeGB, true
}

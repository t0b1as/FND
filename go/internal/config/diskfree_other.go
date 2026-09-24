//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package config

// freeDiskGB-Fallback für Plattformen ohne syscall.Statfs (z.B. Windows).
// Relevant nur, damit die Admin-Tools (fnd-wallet/fundus-admin) auf dem
// Windows-PC kompilieren — dort läuft kein Storage-Node, daher ist die
// automatische Speicher-Reservierung ohne Bedeutung. Gibt (0, false) zurück,
// wodurch Filesharing deaktiviert bleibt. Auf dem Pi (Linux) greift stattdessen
// diskfree_unix.go mit dem echten Statfs.
func freeDiskGB(dir string) (int64, bool) {
	return 0, false
}

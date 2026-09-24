//go:build windows

package filestore

// diskStatfs auf Windows: Stub. Die Zielplattform der Nodes ist Linux (ARM);
// diese Implementierung existiert nur, damit der Baum unter Windows kompiliert
// und getestet werden kann (lokale Entwicklung). Sie meldet großzügige Werte, um
// Kapazitäts-Checks in Tests nicht künstlich fehlschlagen zu lassen — echte
// Speicherwerte gelten nur auf der Linux-Zielplattform.
func diskStatfs(path string) (freeBytes, totalBytes int64) {
	const tb = int64(1) << 40 // 1 TiB
	return tb, tb
}

// DiskStatfs ist der exportierte Zugang zu diskStatfs für andere Pakete.
func DiskStatfs(path string) (freeBytes, totalBytes int64) { return diskStatfs(path) }

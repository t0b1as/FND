//go:build linux

package filestore

import (
	"os"
	"syscall"
	"unsafe"
)

// FALLOC_FL_KEEP_SIZE: Reserviert Disk-Blöcke ohne die Dateigröße zu ändern.
// Blöcke sind allokiert aber nicht initialisiert → kein I/O-Overhead.
const FALLOC_FL_KEEP_SIZE = 0x01

// fallocate ruft den Linux fallocate(2) Syscall auf.
// Reserviert physische Disk-Blöcke für die angegebene Größe.
// Schlägt fehl auf: FAT32, NFS ohne Server-Support, tmpfs.
func fallocate(f *os.File, size int64) error {
	// fallocate(fd, mode, offset, len)
	_, _, errno := syscall.Syscall6(
		syscall.SYS_FALLOCATE,
		f.Fd(),
		uintptr(FALLOC_FL_KEEP_SIZE),
		0,            // offset
		uintptr(size),
		0, 0,
	)
	if errno != 0 {
		return errno
	}
	// Größe explizit setzen (KEEP_SIZE ändert sie nicht)
	return syscall.Ftruncate(int(f.Fd()), size)
}

// syscall.Statfs_t ist auf Linux verfügbar – kein Import nötig.
var _ = unsafe.Sizeof(0) // Import-Verwendungsnachweis

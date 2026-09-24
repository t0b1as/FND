//go:build !linux

package filestore

import (
	"fmt"
	"os"
)

// fallocate – Fallback für nicht-Linux Systeme (macOS, Windows).
// Auf dem Pi (Linux/ARM) wird immer fallocate_linux.go verwendet.
func fallocate(f *os.File, size int64) error {
	return fmt.Errorf("fallocate nicht verfügbar auf diesem OS – nutze Sparse File Fallback")
}

package websuche

// Speichergrenze für die Go-Laufzeit (R900).
//
// systemd begrenzt den Node mit MemoryHigh/MemoryMax. Überschreitet er
// MemoryHigh, bremst der Kernel ihn hart aus – der Node wirkt dann
// aufgehängt. Die Go-Laufzeit weiß davon nichts und räumt erst spät auf.
// Mit einer weichen Grenze knapp darunter räumt sie rechtzeitig auf.
// Ein gesetztes GOMEMLIMIT hat Vorrang.

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// CgroupWurzel: Tests ersetzen sie.
var CgroupWurzel = "/sys/fs/cgroup"

// CgroupGrenze: kleinste von memory.high und memory.max der eigenen cgroup
// (cgroup v2), 0 = keine.
func CgroupGrenze() int64 {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0
	}
	pfad := ""
	for _, z := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(z, "0::") {
			pfad = strings.TrimPrefix(z, "0::")
		}
	}
	if pfad == "" {
		return 0
	}
	return grenzeAus(filepath.Join(CgroupWurzel, pfad))
}

func grenzeAus(ordner string) int64 {
	var min int64
	for _, n := range []string{"memory.high", "memory.max"} {
		b, err := os.ReadFile(filepath.Join(ordner, n))
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "max" {
			continue
		}
		if x, err := strconv.ParseInt(v, 10, 64); err == nil && x > 0 && (min == 0 || x < min) {
			min = x
		}
	}
	return min
}

// SpeicherGrenzeSetzen: 85 % der cgroup-Grenze als weiche Grenze der
// Go-Laufzeit. Gibt die gesetzte Grenze zurück (0 = nichts geändert).
func SpeicherGrenzeSetzen() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0
	}
	g := CgroupGrenze()
	if g <= 0 {
		return 0
	}
	ziel := g * 85 / 100
	debug.SetMemoryLimit(ziel)
	return ziel
}

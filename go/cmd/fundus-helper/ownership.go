package main

// Eigentümer der Fundus-Datenordner reparieren.
//
// Läuft der Node einmal versehentlich als root (z.B. "sudo fundus-node …" statt
// "sudo -u fundus …"), legt er Dateien als root an. Danach kann der Dienst
// (User=fundus) seine eigene Datenbank bzw. den Dateispeicher nicht mehr öffnen:
// entweder startet er gar nicht ("permission denied" auf db/LOCK) oder ohne
// Dateispeicher (Bilder laden nicht). Der Helper läuft als root und setzt beim
// Start und alle 10 min nur die Dateien zurück, die NICHT fundus gehören.

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

var fundusOwnedDirs = []string{"/opt/fundus/data", "/opt/fundus/chunks"}

func ownershipWatch(ctx context.Context) {
	fixOwnership()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fixOwnership()
		}
	}
}

func fixOwnership() {
	u, err := user.Lookup("fundus")
	if err != nil {
		return
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return
	}
	fixed := 0
	for _, root := range fundusOwnedDirs {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil // Symlinks nicht verfolgen/ändern
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok && (int(st.Uid) != uid || int(st.Gid) != gid) {
				if os.Lchown(p, uid, gid) == nil {
					fixed++
				}
			}
			return nil
		})
	}
	if fixed > 0 {
		logf("Eigentümer korrigiert: %d Datei(en) in %v gehören wieder fundus (Node lief vermutlich einmal als root)", fixed, fundusOwnedDirs)
	}
}

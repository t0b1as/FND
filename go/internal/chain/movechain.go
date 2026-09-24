package chain

// Sicheres Verschieben der Chain-Datenbank auf ein anderes Laufwerk.
//
// Ablauf (goldene Regel: das Alte NIE löschen, bevor das Neue validiert ist):
//   1. Ziel-Verzeichnis auf der Platte anlegen.
//   2. chain.db + meta.json + genesis.json dorthin KOPIEREN (Original bleibt).
//   3. Kopie VALIDIEREN: Store öffnen, State per Replay aufbauen, Head-Hash
//      gegen meta.json prüfen. Nur wenn identisch, gilt die Kopie als gültig.
//   4. UMSCHALTEN: das lokale chain/-Verzeichnis durch einen Symlink auf die
//      Platte ersetzen (das alte chain/ wird nach chain.moved-<ts> umbenannt).
//   5. Erst nach erfolgreichem Umschalten darf der Aufrufer das umbenannte alte
//      Verzeichnis löschen.
//
// Diese Funktion wird beim Node-START ausgeführt (bevor die Chain geöffnet wird),
// nicht im laufenden Betrieb — so ist die DB garantiert nicht in Benutzung.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// MoveChain kopiert das Chain-Verzeichnis von srcDir auf targetBase (ein
// gemountetes Laufwerk), validiert die Kopie per Head-Hash und ersetzt srcDir
// durch einen Symlink auf das Ziel. Gibt den Zielpfad zurück. Das alte,
// umbenannte Verzeichnis wird bei Erfolg gelöscht.
//
// srcDir:     bisheriges Chain-Verzeichnis (z.B. /opt/fundus/data/chain)
// targetBase: Wurzel des Ziel-Laufwerks (z.B. /mnt/fundus-<uuid>)
func MoveChain(srcDir, targetBase string, logf func(string, ...interface{})) (string, error) {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	// Wenn srcDir bereits ein Symlink ist, liegt die Chain schon ausgelagert.
	if fi, err := os.Lstat(srcDir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		cur, _ := os.Readlink(srcDir)
		if filepath.Dir(cur) == filepath.Clean(targetBase) || cur == filepath.Join(targetBase, "fundus-chain") {
			return cur, fmt.Errorf("chain: liegt bereits auf %s", targetBase)
		}
	}

	targetDir := filepath.Join(targetBase, "fundus-chain")

	// 1. Ziel-Verzeichnis (leer) anlegen.
	if _, err := os.Stat(targetDir); err == nil {
		return "", fmt.Errorf("chain: Zielverzeichnis existiert bereits: %s (vorher aufräumen)", targetDir)
	}
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return "", fmt.Errorf("chain: Zielverzeichnis anlegen: %w", err)
	}

	// 2. Dateien kopieren (Original bleibt unangetastet).
	for _, name := range []string{"chain.db", "meta.json", "genesis.json"} {
		src := filepath.Join(srcDir, name)
		if _, err := os.Stat(src); err != nil {
			if name == "chain.db" {
				os.RemoveAll(targetDir)
				return "", fmt.Errorf("chain: %s fehlt in %s — nichts zu verschieben", name, srcDir)
			}
			continue // meta/genesis optional
		}
		if err := copyFile(src, filepath.Join(targetDir, name)); err != nil {
			os.RemoveAll(targetDir)
			return "", fmt.Errorf("chain: %s kopieren: %w", name, err)
		}
		logf("Chain-Move: %s kopiert", name)
	}

	// 3. Kopie validieren (Head-Hash-Replay aus der Ziel-DB).
	logf("Chain-Move: validiere Kopie (Head-Hash) …")
	if err := VerifyStoreAgainstMeta(targetDir); err != nil {
		os.RemoveAll(targetDir)
		return "", fmt.Errorf("chain: Validierung der Kopie fehlgeschlagen (Original unangetastet): %w", err)
	}
	logf("Chain-Move: Kopie validiert ✓")

	// 4. Umschalten: altes chain/ umbenennen, Symlink setzen.
	movedOld := srcDir + fmt.Sprintf(".moved-%d", time.Now().Unix())
	if err := os.Rename(srcDir, movedOld); err != nil {
		os.RemoveAll(targetDir)
		return "", fmt.Errorf("chain: altes Verzeichnis umbenennen: %w", err)
	}
	if err := os.Symlink(targetDir, srcDir); err != nil {
		// Rollback: altes Verzeichnis zurück.
		os.Rename(movedOld, srcDir)
		os.RemoveAll(targetDir)
		return "", fmt.Errorf("chain: Symlink setzen (Rollback ausgeführt): %w", err)
	}
	logf("Chain-Move: %s → %s (Symlink gesetzt)", srcDir, targetDir)

	// 5. Altes Verzeichnis löschen (Kopie ist validiert und live).
	if err := os.RemoveAll(movedOld); err != nil {
		logf("Chain-Move: altes Verzeichnis %s konnte nicht gelöscht werden: %v (manuell entfernen)", movedOld, err)
	}
	return targetDir, nil
}

// copyFile kopiert eine Datei mit fsync (haltbar auf die Zielplatte geschrieben).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil { // fsync: Daten physisch auf die Platte
		out.Close()
		return err
	}
	return out.Close()
}

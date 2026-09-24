package main

// Automount: gemerkte Laufwerke automatisch einhängen — beim Booten und beim
// Einstecken. Die Liste der gemerkten Filesystem-UUIDs liegt persistent unter
// /var/lib/fundus-helper/automount.list (eine UUID pro Zeile).
//
// Ablauf:
//   - Wird eine Platte über die UI eingebunden (handleMount), landet ihre UUID
//     in dieser Liste.
//   - Wird sie explizit ausgehängt (handleUnmount), fliegt sie wieder raus.
//   - Beim Start des Daemons und periodisch (sowie per udev beim Einstecken)
//     werden alle gemerkten, vorhandenen und noch nicht gemounteten Laufwerke
//     eingehängt.
//
// So kommt eine einmal eingebundene Platte nach Reboot oder Wiederanstecken von
// selbst zurück — an denselben Mountpunkt /mnt/fundus-<uuid> (UUID = stabil).

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// automountMu schützt die Listendatei gegen gleichzeitige Zugriffe (Boot-Scan,
// periodischer Scan, udev-Trigger, mount/unmount können parallel auftreten).
var automountMu sync.Mutex

// mountMu serialisiert ALLE Mount-/Unmount-Operationen — egal ob sie vom
// periodischen Scan (direkt) oder über den Socket (handleConn) ausgelöst werden.
// Ohne diese Sperre könnten Scan und Socket-Trigger dieselbe Platte gleichzeitig
// zu mounten versuchen (Race auf mount/ /proc/mounts). Die Operationen sind
// selten und kurz, daher ist ein einfacher globaler Mutex angemessen.
var mountMu sync.Mutex

// stateDir liefert das persistente Zustandsverzeichnis. systemd setzt bei
// StateDirectory=fundus-helper die Variable STATE_DIRECTORY auf den absoluten
// Pfad; als Fallback nutzen wir den Standardpfad.
func stateDir() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		// Kann mehrere (durch ':' getrennte) Pfade enthalten — ersten nehmen.
		if i := strings.IndexByte(d, ':'); i >= 0 {
			d = d[:i]
		}
		return d
	}
	return "/var/lib/fundus-helper"
}

func automountListPath() string {
	return filepath.Join(stateDir(), "automount.list")
}

// readAutomountList liest die gemerkten UUIDs. Fehlt die Datei, ist die Liste leer.
func readAutomountList() []string {
	automountMu.Lock()
	defer automountMu.Unlock()
	return readAutomountListLocked()
}

func readAutomountListLocked() []string {
	data, err := os.ReadFile(automountListPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && uuidRe.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

func writeAutomountListLocked(uuids []string) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	content := strings.Join(uuids, "\n")
	if content != "" {
		content += "\n"
	}
	// 0600: nur root. Enthält nur UUIDs, aber kein Grund, es offen zu legen.
	return os.WriteFile(automountListPath(), []byte(content), 0o600)
}

// registerAutomount fügt eine UUID hinzu (idempotent).
func registerAutomount(uuid string) {
	if !uuidRe.MatchString(uuid) {
		return
	}
	automountMu.Lock()
	defer automountMu.Unlock()
	cur := readAutomountListLocked()
	for _, u := range cur {
		if u == uuid {
			return // schon drin
		}
	}
	cur = append(cur, uuid)
	if err := writeAutomountListLocked(cur); err != nil {
		logf("WARN: Automount-Liste schreiben: %v", err)
		return
	}
	logf("Automount gemerkt: %s", uuid)
}

// unregisterAutomount entfernt eine UUID.
func unregisterAutomount(uuid string) {
	automountMu.Lock()
	defer automountMu.Unlock()
	cur := readAutomountListLocked()
	var next []string
	found := false
	for _, u := range cur {
		if u == uuid {
			found = true
			continue
		}
		next = append(next, u)
	}
	if !found {
		return
	}
	if err := writeAutomountListLocked(next); err != nil {
		logf("WARN: Automount-Liste schreiben: %v", err)
		return
	}
	logf("Automount vergessen: %s", uuid)
}

// isAutomount meldet, ob eine UUID für Automount registriert ist.
func isAutomount(uuid string) bool {
	for _, u := range readAutomountList() {
		if u == uuid {
			return true
		}
	}
	return false
}


// scanAndMountNew erkennt ALLE einsteckbaren externen Datentraeger und bindet neue
// (noch nicht registrierte) automatisch ein — VOLLAUTOMATISCHER Modus.
//
// SICHERHEIT: Es werden AUSSCHLIESSLICH echte externe Wechseldatentraeger
// angefasst. NIEMALS die System-/Boot-Platte, die laufende Wurzel, Swap, die
// SD-Karte des Pi oder als Systempfad gemountete Geraete. Diese Ausschluesse sind
// nicht verhandelbar — ohne sie wuerde der erste Reboot das System zerstoeren.
func scanAndMountNew() int {
	out, err := runCmd(10*time.Second, "/bin/lsblk",
		"-P", "-b", "-o", "NAME,PATH,UUID,FSTYPE,SIZE,MOUNTPOINT,RM,TYPE")
	if err != nil {
		return 0
	}
	mounted := 0
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		kv := parseLsblkLine(l)
		if !safeToAutoMount(kv) {
			continue
		}
		uuid := kv["UUID"]
		if isAutomount(uuid) {
			continue // schon registriert → scanAndMountRegistered kuemmert sich
		}
		if mp, err := mountByUUID(uuid); err == nil && mp != "" {
			registerAutomount(uuid)
			logf("Auto-Mount: neues Laufwerk %s (%s) eingebunden unter %s", kv["NAME"], uuid, mp)
			mounted++
		} else if err != nil {
			logf("Auto-Mount %s fehlgeschlagen: %v", uuid, err)
		}
	}
	return mounted
}

// safeToAutoMount entscheidet, ob ein Laufwerk automatisch uebernommen werden darf.
// STRENG restriktiv — im Zweifel NEIN. Diese Regeln schuetzen das System.
func safeToAutoMount(kv map[string]string) bool {
	if kv["TYPE"] != "part" && kv["TYPE"] != "disk" {
		return false
	}
	if kv["FSTYPE"] == "" || kv["UUID"] == "" || kv["FSTYPE"] == "swap" {
		return false
	}
	name, path := kv["NAME"], kv["PATH"]
	// SD-Karte des Pi (mmcblk*) und NVMe (meist System) NIEMALS auto-uebernehmen.
	if strings.HasPrefix(name, "mmcblk") || strings.Contains(path, "mmcblk") ||
		strings.HasPrefix(name, "nvme") || strings.Contains(path, "nvme") {
		return false
	}
	mp := kv["MOUNTPOINT"]
	if mp != "" {
		if isSystemMountPath(mp) || strings.HasPrefix(mp, "/mnt/fundus-") {
			return false
		}
	}
	// Muss ein WECHSELdatentraeger sein (RM=1). Fest verbaute Platten nicht.
	if kv["RM"] != "1" {
		return false
	}
	return true
}

// isSystemMountPath erkennt kritische Systempfade, die niemals angefasst werden.
func isSystemMountPath(mp string) bool {
	if mp == "/" {
		return true
	}
	for _, p := range []string{"/boot", "/home", "/usr", "/var", "/etc", "/opt", "/root", "/srv"} {
		if mp == p || strings.HasPrefix(mp, p+"/") {
			return true
		}
	}
	return false
}

// scanAndMountRegistered hängt alle gemerkten, vorhandenen und noch nicht
// gemounteten Laufwerke ein. Fehlende (nicht eingesteckte) werden übersprungen.
// Rückgabe: Anzahl erfolgreich (neu oder bereits) gemounteter Laufwerke.
func scanAndMountRegistered() int {
	mounted := 0
	for _, uuid := range readAutomountList() {
		// Vorhanden? /dev/disk/by-uuid/<uuid> muss existieren.
		devLink := filepath.Join("/dev/disk/by-uuid", uuid)
		if _, err := os.Stat(devLink); err != nil {
			continue // nicht eingesteckt
		}
		// SICHERHEIT: Auch registrierte Laufwerke gegen die Schutzregel prüfen —
		// niemals die System-SD-Karte/NVMe mounten, selbst wenn sie fälschlich in
		// der automount.list steht (z.B. aus einem alten Test). Eine solche UUID
		// wird zusätzlich aus der Liste entfernt, damit der Spuk aufhört.
		if !safeUUIDToMount(uuid) {
			logf("Automount: UUID %s ist ein System-/Boot-Medium — wird NICHT gemountet und aus der Liste entfernt", uuid)
			unregisterAutomount(uuid)
			continue
		}
		mp, err := mountByUUID(uuid)
		if err != nil {
			logf("Automount %s fehlgeschlagen: %v", uuid, err)
			continue
		}
		if mp != "" {
			mounted++
		}
	}
	return mounted
}

// safeUUIDToMount prüft anhand des Geräts hinter einer UUID, ob es sicher
// mountbar ist (kein System-Medium). Löst die UUID zum Gerätepfad auf und
// wendet dieselbe Schutzlogik wie safeToAutoMount an.
func safeUUIDToMount(uuid string) bool {
	// UUID → Gerät auflösen.
	devLink := filepath.Join("/dev/disk/by-uuid", uuid)
	target, err := os.Readlink(devLink)
	if err != nil {
		return false
	}
	// target ist relativ, z.B. "../../mmcblk0p2" → Basename reicht für die Prüfung.
	dev := filepath.Base(target)
	// System-SD-Karte des Pi und NVMe niemals.
	if strings.HasPrefix(dev, "mmcblk") || strings.HasPrefix(dev, "nvme") {
		return false
	}
	return true
}

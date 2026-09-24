// Command fundus-helper ist ein kleiner, privilegierter Daemon (läuft als root),
// der genau die Systemoperationen ausführt, die der unprivilegierte fundus-node
// nicht selbst darf: externe Laufwerke mounten und das WLAN wechseln.
//
// Er ist bewusst minimal gehalten (kleine, auditierbare Angriffsfläche):
//   - lauscht NUR auf einem Unix-Domain-Socket (kein Netzwerk),
//   - Socket-Rechte 0660 root:fundus → nur der Node darf verbinden,
//   - akzeptiert NUR die festen Aktionen aus helperproto,
//   - validiert jeden Parameter streng (UUID-Regex, SSID-Länge) BEVOR ein
//     Systemaufruf erfolgt,
//   - führt externe Tools mit fixem Pfad und getrennten Argumenten aus (kein
//     Shell-Interpolation → keine Command-Injection).
//
// Siehe internal/helperproto für das Protokoll und das Sicherheitsmodell.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fundus/node/internal/helperproto"
)

// fundusGID wird beim Start aus dem fundus-User ermittelt, um den Socket der
// fundus-Gruppe zuzuordnen (nur der Node darf verbinden).
const fundusUserName = "fundus"

// Erlaubte Dateisystem-UUID-Formate:
//   - ext/xfs/btrfs: 8-4-4-4-12 Hex (klassische UUID mit Bindestrichen)
//   - vfat/FAT:      XXXX-XXXX (8 Hex mit einem Bindestrich)
//   - ntfs:          16 Hex ohne Bindestrich
// Streng, damit weder Shell-Metazeichen noch reine Bindestrich-Ketten oder
// Flag-artige Werte (z.B. "-o") durchkommen — Letztere könnten von mount als
// Option fehlinterpretiert werden. Jede Variante verlangt Hex-Ziffern.
var uuidRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$|^[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}$|^[0-9A-Fa-f]{16}$`)

// Mountpunkt-Basis: Der Helper mountet ausschließlich unter diesem Präfix, nie
// an einen vom Aufrufer frei gewählten Pfad.
const mountBase = "/mnt/fundus-"

func main() {
	// CLI-Modi für die udev-Trigger und manuelle Auslösung. Laufen als root
	// (udev), können also auf den Socket zugreifen. Schlägt es fehl (z.B. Daemon
	// noch nicht bereit), passiert nichts Schlimmes — der periodische Scan im
	// Daemon fängt registrierte Laufwerke später ab.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--automount-scan":
			// Alle gemerkten Laufwerke prüfen (Boot-Auffang, manuell).
			resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionAutomountScan})
			if err != nil {
				logf("Automount-Trigger: Daemon nicht erreichbar (%v)", err)
			} else if !resp.OK {
				logf("Automount-Trigger: %s", resp.Error)
			}
			os.Exit(0)
		case "--automount-add":
			// Konkrete, gerade eingesteckte Platte mounten UND merken. So mountet
			// auch eine fabrikneue Platte beim ersten Einstecken automatisch.
			if len(os.Args) < 3 {
				logf("Automount-Add: keine UUID angegeben")
				os.Exit(2)
			}
			uuid := os.Args[2]
			resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionMountDrive, UUID: uuid})
			if err != nil {
				logf("Automount-Add: Daemon nicht erreichbar (%v) — periodischer Scan übernimmt", err)
			} else if !resp.OK {
				logf("Automount-Add %s: %s", uuid, resp.Error)
			} else {
				logf("Automount-Add: %s eingehängt", uuid)
			}
			os.Exit(0)
		}
	}

	logf("fundus-helper startet (Protokoll v%d)", helperproto.ProtocolVersion)

	if os.Geteuid() != 0 {
		fatal("fundus-helper muss als root laufen")
	}

	gid, err := lookupFundusGID()
	if err != nil {
		// Nicht fatal: dann gehört der Socket root:root und wir lockern die
		// Rechte NICHT — der Node kann nicht verbinden, aber wir laufen nicht
		// mit einem unsicher offenen Socket weiter.
		logf("WARN: fundus-GID nicht gefunden (%v) — Socket bleibt root-only", err)
		gid = 0
	}

	// Alten Socket entfernen (z.B. nach unsauberem Stop).
	_ = os.Remove(helperproto.SocketPath)

	ln, err := listenUnix(helperproto.SocketPath, gid)
	if err != nil {
		fatal("Socket anlegen: %v", err)
	}
	defer ln.Close()
	logf("höre auf %s", helperproto.SocketPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		logf("Signal empfangen, fahre herunter")
		_ = ln.Close()
		_ = os.Remove(helperproto.SocketPath)
	}()

	// Automount beim Booten: einmal direkt alle gemerkten Laufwerke einhängen.
	// (Die OS-eigenen Partitionen sind zu diesem Zeitpunkt längst gemountet und
	// werden ohnehin nur angefasst, wenn sie in unserer Liste stehen.)
	if n := scanAndMountRegistered(); n > 0 {
		logf("Automount beim Start: %d Laufwerk(e) eingehängt", n)
	}

	// Periodischer Scan als robuster Auffang für das Einstecken (falls die
	// udev-Regel nicht greift) und für spät auftauchende Geräte nach dem Boot.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				scanAndMountRegistered()
				scanAndMountNew() // vollautomatisch: neue externe Datenträger übernehmen
			}
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return // sauberer Shutdown
			}
			logf("Accept-Fehler: %v", err)
			continue
		}
		// Verbindungen seriell abarbeiten: Die Operationen (mount/wifi) sind
		// selten und kurz; serielle Bearbeitung vermeidet Races auf mount/nmcli.
		handleConn(conn)
	}
}

// handleConn liest genau eine Request-Zeile, bearbeitet sie und antwortet.
func handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}

	var req helperproto.Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(conn, helperproto.Response{OK: false, Error: "ungültiges JSON"})
		return
	}
	if req.Version != helperproto.ProtocolVersion {
		writeResp(conn, helperproto.Response{OK: false,
			Error: fmt.Sprintf("Protokollversion %d != erwartet %d", req.Version, helperproto.ProtocolVersion)})
		return
	}

	resp := dispatch(req)
	writeResp(conn, resp)
}

// dispatch wählt die Aktion und ruft den passenden Handler. Jeder Handler
// validiert seine Eingaben selbst nochmals streng.
func dispatch(req helperproto.Request) helperproto.Response {
	switch req.Action {
	case helperproto.ActionPing:
		return helperproto.Response{OK: true}
	case helperproto.ActionListBlockDevices:
		return handleListBlockDevices()
	case helperproto.ActionMountDrive:
		return handleMount(req.UUID)
	case helperproto.ActionUnmountDrive:
		return handleUnmount(req.UUID)
	case helperproto.ActionListWifi:
		return handleListWifi()
	case helperproto.ActionConnectWifi:
		return handleConnectWifi(req.SSID, req.Password)
	case helperproto.ActionWifiStatus:
		return handleWifiStatus()
	case helperproto.ActionAutomountScan:
		n := scanAndMountRegistered()
		logf("Automount-Scan: %d Laufwerk(e) gemountet/aktiv", n)
		return helperproto.Response{OK: true}
	case helperproto.ActionApplyUpdate:
		return handleApplyUpdate(req.Manifest)
	case helperproto.ActionRestartNode:
		return handleRestartNode()
	default:
		return helperproto.Response{OK: false, Error: "unbekannte Aktion"}
	}
}

// ─── Mount ───────────────────────────────────────────────────────────────────

// handleMount mountet das Gerät mit der gegebenen UUID an /mnt/fundus-<uuid>.
// Der Mountpunkt wird vom Helper bestimmt (nie vom Aufrufer), das Gerät wird
// über /dev/disk/by-uuid/<uuid> aufgelöst — beides verhindert Pfad-Tricks.
// mountByUUID mountet das Gerät mit der gegebenen UUID an /mnt/fundus-<uuid>.
// Gemeinsame Logik für den UI-Mount (handleMount) und den Automount-Scan.
// Gibt den Mountpunkt zurück (auch wenn schon gemountet); leerer String + Fehler
// bei Problemen. Registriert NICHT selbst für Automount — das entscheidet der
// Aufrufer.
func mountByUUID(uuid string) (string, error) {
	if !uuidRe.MatchString(uuid) {
		return "", fmt.Errorf("ungültige UUID")
	}
	// Alle Mount-Operationen serialisieren (Scan vs. Socket-Trigger, s. mountMu).
	mountMu.Lock()
	defer mountMu.Unlock()

	devLink := filepath.Join("/dev/disk/by-uuid", uuid)
	dev, err := filepath.EvalSymlinks(devLink)
	if err != nil {
		return "", fmt.Errorf("Gerät zu UUID nicht gefunden")
	}
	// Sicherheitscheck: aufgelöstes Gerät muss unter /dev liegen.
	if !strings.HasPrefix(dev, "/dev/") {
		return "", fmt.Errorf("aufgelöstes Gerät ungültig")
	}

	mountPoint := mountBase + uuid
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return "", fmt.Errorf("Mountpunkt anlegen: %w", err)
	}

	// FS-Typ ermitteln (für die richtigen Mount-Optionen).
	fsType := blkidType(dev)

	// Schon gemountet? Wenn unter /mnt/fundus- → idempotent zurückgeben.
	// Wenn WOANDERS (z.B. udisks2 unter /media/<user>/, für den Node unlesbar),
	// erst aushängen, damit wir es zugänglich neu einhängen können.
	if mp, ok := currentMountPoint(dev); ok {
		if strings.HasPrefix(mp, mountBase) {
			return mp, nil // schon unter /mnt/fundus- eingebunden
		}
		// Die Platte ist von udisks2 unter /media/<user>/ oder /run/media/<user>/
		// gemountet. udisks2 ist so konfiguriert, dass es mit allow_other mountet
		// (siehe Deploy: /etc/udisks2/mount_options.conf), also kann der Node-User
		// die Platte dort direkt lesen. NICHT umhängen — das führte nur zum Kampf
		// mit udisks2, das sofort neu mountete. Diesen Mountpunkt beibehalten.
		if strings.HasPrefix(mp, "/media/") || strings.HasPrefix(mp, "/run/media/") {
			logf("Laufwerk unter %s (udisks2, allow_other) — wird dort belassen und genutzt", mp)
			return mp, nil
		}
		// Ein anderer Fremd-Mount (nicht udisks2, nicht /mnt/fundus-) → übernehmen.
		logf("Laufwerk unter %s (Fremd-Mount) → haenge aus und binde neu ein", mp)
		if _, err := runCmd(10*time.Second, "/bin/umount", dev); err != nil {
			_, _ = runCmd(10*time.Second, "/usr/bin/udisksctl", "unmount", "-b", dev)
		}
	}

	// Mount-Optionen je nach Dateisystem. exFAT/NTFS/vfat kennen keine Unix-Rechte,
	// daher uid/gid des fundus-Users setzen, sonst kann der Node nicht lesen.
	baseOpts := "nosuid,nodev,noexec"
	var mountArgs []string
	switch fsType {
	case "ntfs", "ntfs3":
		// NTFS: expliziter ntfs-3g-Treiber (beschreibbar, FUSE). allow_other, damit
		// der Node-User (fundus) auf den root-Mount zugreifen und statfs machen kann.
		uid, gid := os.Getuid(), os.Getgid()
		if fu, fg, ok := fundusUIDGID(); ok { uid, gid = fu, fg }
		opts := fmt.Sprintf("%s,uid=%d,gid=%d,umask=0022,allow_other", baseOpts, uid, gid)
		mountArgs = []string{"-t", "ntfs-3g", "-o", opts, dev, mountPoint}
	case "exfat", "vfat":
		// exFAT läuft je nach System über FUSE (exfat-fuse) oder Kernel-Treiber.
		// allow_other ist für den FUSE-Fall nötig, damit der Node-User (fundus)
		// zugreifen und die Größe (statfs) lesen kann — sonst bleibt sie 0.
		uid, gid := os.Getuid(), os.Getgid()
		if fu, fg, ok := fundusUIDGID(); ok { uid, gid = fu, fg }
		opts := fmt.Sprintf("%s,uid=%d,gid=%d,umask=0022,allow_other", baseOpts, uid, gid)
		mountArgs = []string{"-t", fsType, "-o", opts, dev, mountPoint}
	default:
		// ext4 etc.: Unix-Rechte gelten. Nach dem Mount ggf. chown (unten).
		if fsType != "" {
			mountArgs = []string{"-t", fsType, "-o", baseOpts, dev, mountPoint}
		} else {
			mountArgs = []string{"-o", baseOpts, dev, mountPoint}
		}
	}

	out, err := runCmd(15*time.Second, "/bin/mount", mountArgs...)
	if err != nil {
		// allow_other braucht 'user_allow_other' in /etc/fuse.conf. Fehlt das, oder
		// ist es der Kernel-Treiber (der allow_other nicht kennt), scheitert der
		// Mount. Dann ohne allow_other erneut versuchen (Kernel-Treiber-Fall).
		if strings.Contains(strings.ToLower(sanitize(out)), "allow_other") ||
			strings.Contains(strings.ToLower(sanitize(out)), "fuse") {
			retryArgs := make([]string, 0, len(mountArgs))
			for _, a := range mountArgs {
				retryArgs = append(retryArgs, strings.Replace(a, ",allow_other", "", 1))
			}
			logf("mount mit allow_other fehlgeschlagen, versuche ohne: %v", retryArgs)
			out2, err2 := runCmd(15*time.Second, "/bin/mount", retryArgs...)
			if err2 == nil {
				logf("gemountet (ohne allow_other): %s (%s) → %s", dev, fsType, mountPoint)
				out, err = out2, nil
			} else {
				logf("mount fehlgeschlagen (auch ohne allow_other): dev=%s fs=%s → %s", dev, fsType, sanitize(out2))
				return "", fmt.Errorf("mount (%s) fehlgeschlagen: %s", fsType, sanitize(out2))
			}
		} else {
			logf("mount fehlgeschlagen: dev=%s fs=%s args=%v → %s", dev, fsType, mountArgs, sanitize(out))
			return "", fmt.Errorf("mount (%s) fehlgeschlagen: %s", fsType, sanitize(out))
		}
	}
	// Bei nativen Unix-Dateisystemen den Eigentümer auf fundus setzen.
	switch fsType {
	case "exfat", "vfat", "ntfs", "ntfs3":
		// uid/gid schon per Mount-Option gesetzt.
	default:
		if fu, fg, ok := fundusUIDGID(); ok {
			_ = os.Chown(mountPoint, fu, fg)
		}
	}
	logf("gemountet: %s (%s) → %s", dev, fsType, mountPoint)
	return mountPoint, nil
}

// blkidType ermittelt den Dateisystemtyp eines Geräts.
func blkidType(dev string) string {
	out, err := runCmd(5*time.Second, "/sbin/blkid", "-s", "TYPE", "-o", "value", dev)
	if err != nil {
		// Fallback: /usr/sbin/blkid
		out, err = runCmd(5*time.Second, "/usr/sbin/blkid", "-s", "TYPE", "-o", "value", dev)
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(out)
}

// fundusUIDGID liefert uid/gid des fundus-Users (für Mount-Optionen fremder FS).
// Liest /etc/passwd DIREKT statt os/user.Lookup — letzteres kann bei Cross-
// Compilation ohne cgo Probleme machen; das direkte Parsen ist garantiert
// cgo-frei und robust.
func fundusUIDGID() (int, int, bool) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		// Format: name:x:uid:gid:gecos:home:shell
		f := strings.Split(line, ":")
		if len(f) < 4 || f[0] != "fundus" {
			continue
		}
		uid, e1 := strconv.Atoi(f[2])
		gid, e2 := strconv.Atoi(f[3])
		if e1 != nil || e2 != nil {
			return 0, 0, false
		}
		return uid, gid, true
	}
	return 0, 0, false
}

// handleMount ist der UI-Pfad: mountet ein Gerät und merkt es für Automount vor,
// damit es nach Reboot/Wiederanstecken von selbst zurückkommt.
func handleMount(uuid string) helperproto.Response {
	mp, err := mountByUUID(uuid)
	if err != nil {
		return helperproto.Response{OK: false, Error: err.Error()}
	}
	registerAutomount(uuid)
	return helperproto.Response{OK: true, MountPoint: mp}
}

// handleUnmount hängt ein zuvor gemountetes Gerät wieder aus.
func handleUnmount(uuid string) helperproto.Response {
	if !uuidRe.MatchString(uuid) {
		return helperproto.Response{OK: false, Error: "ungültige UUID"}
	}
	mountPoint := mountBase + uuid
	// Nur Mountpunkte unter unserer Basis akzeptieren.
	if !strings.HasPrefix(mountPoint, mountBase) {
		return helperproto.Response{OK: false, Error: "unerlaubter Mountpunkt"}
	}
	// Serialisieren gegen parallele Mounts (s. mountMu).
	mountMu.Lock()
	defer mountMu.Unlock()
	out, err := runCmd(10*time.Second, "/bin/umount", mountPoint)
	if err != nil {
		return helperproto.Response{OK: false, Error: "umount fehlgeschlagen: " + out}
	}
	_ = os.Remove(mountPoint)
	unregisterAutomount(uuid)
	logf("ausgehängt: %s", mountPoint)
	return helperproto.Response{OK: true}
}

// ─── WLAN ────────────────────────────────────────────────────────────────────

// SSID-Validierung: max. 32 Bytes (IEEE 802.11), keine Steuerzeichen. Das
// Passwort wird NICHT geloggt und nur an nmcli als getrenntes Argument gereicht.
func validSSID(ssid string) bool {
	if ssid == "" || len(ssid) > 32 {
		return false
	}
	for _, r := range ssid {
		if r < 0x20 { // Steuerzeichen
			return false
		}
	}
	return true
}

func handleConnectWifi(ssid, password string) helperproto.Response {
	if !validSSID(ssid) {
		return helperproto.Response{OK: false, Error: "ungültige SSID"}
	}
	if len(password) > 63 {
		return helperproto.Response{OK: false, Error: "Passwort zu lang"}
	}
	const nm = "/usr/bin/nmcli"
	// nmcli mit getrennten Argumenten (keine Shell → keine Injection).

	// Schon mit diesem Netz verbunden? Dann nichts anfassen – das Profil des
	// aktiven Netzes darf nie gelöscht werden (sonst wäre der Pi bei einem
	// Tippfehler im Passwort nicht mehr erreichbar).
	active := activeWifiSSID()
	if active == ssid {
		return helperproto.Response{OK: true}
	}

	// 1. Veraltetes Profil gleichen Namens entfernen. Hauptursache für
	//    "802-11-wireless-security.key-mgmt: property is missing": nmcli
	//    übernimmt sonst dessen unvollständige Sicherheitseinstellungen.
	_, _ = runCmd(10*time.Second, nm, "connection", "delete", "id", ssid)
	// 2. Neu scannen, damit NetworkManager die Verschlüsselungsart des Netzes kennt.
	_, _ = runCmd(15*time.Second, nm, "device", "wifi", "rescan")
	time.Sleep(2 * time.Second)

	// 3. Standardweg.
	args := []string{"device", "wifi", "connect", ssid}
	if password != "" {
		args = append(args, "password", password)
	}
	out, err := runCmd(45*time.Second, nm, args...)

	// 4. Rückfall: Profil ausdrücklich mit Schlüsselverwaltung anlegen
	//    (WPA2-PSK, danach WPA3-SAE).
	if err != nil && password != "" && strings.Contains(out, "key-mgmt") {
		ifname := wifiInterface()
		for _, km := range []string{"wpa-psk", "sae"} {
			_, _ = runCmd(10*time.Second, nm, "connection", "delete", "id", ssid)
			add := []string{"connection", "add", "type", "wifi", "con-name", ssid,
				"ifname", ifname, "ssid", ssid,
				"wifi-sec.key-mgmt", km, "wifi-sec.psk", password}
			if o2, e2 := runCmd(20*time.Second, nm, add...); e2 != nil {
				out, err = o2, e2
				continue
			}
			out, err = runCmd(45*time.Second, nm, "connection", "up", "id", ssid)
			if err == nil {
				break
			}
		}
		if err != nil {
			// Fehlgeschlagenes Profil nicht liegen lassen (sonst beim nächsten Versuch wieder Altlast).
			_, _ = runCmd(10*time.Second, nm, "connection", "delete", "id", ssid)
		}
	}
	if err != nil {
		// out kann das Passwort NICHT enthalten (nmcli echo't es nicht).
		msg := sanitize(out)
		if strings.Contains(out, "Secrets were required") || strings.Contains(strings.ToLower(out), "password") {
			msg = "Passwort falsch oder Netz lehnt die Anmeldung ab"
		}
		return helperproto.Response{OK: false, Error: "Verbindung fehlgeschlagen: " + msg}
	}
	logf("WLAN verbunden: SSID=%q", ssid)
	return helperproto.Response{OK: true}
}

// activeWifiSSID liefert die SSID der aktiven WLAN-Verbindung ("" = keine).
func activeWifiSSID() string {
	out, err := runCmd(10*time.Second, "/usr/bin/nmcli", "-t", "-f", "ACTIVE,SSID", "device", "wifi")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "yes:") {
			return strings.ReplaceAll(strings.TrimPrefix(line, "yes:"), "\\:", ":")
		}
	}
	return ""
}

// wifiInterface ermittelt das WLAN-Gerät (meist wlan0).
func wifiInterface() string {
	out, err := runCmd(10*time.Second, "/usr/bin/nmcli", "-t", "-f", "DEVICE,TYPE", "device")
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			if parts := strings.SplitN(strings.TrimSpace(line), ":", 2); len(parts) == 2 && parts[1] == "wifi" {
				return parts[0]
			}
		}
	}
	return "wlan0"
}

func handleWifiStatus() helperproto.Response {
	// Aktive Verbindung + IP über nmcli abfragen.
	out, err := runCmd(10*time.Second, "/usr/bin/nmcli", "-t", "-f", "ACTIVE,SSID", "device", "wifi")
	if err != nil {
		return helperproto.Response{OK: false, Error: sanitize(out)}
	}
	st := &helperproto.WifiState{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "yes:") {
			st.Connected = true
			st.SSID = strings.TrimPrefix(l, "yes:")
			break
		}
	}
	if st.Connected {
		if ip, e := runCmd(5*time.Second, "/usr/bin/nmcli", "-t", "-f", "IP4.ADDRESS", "device", "show"); e == nil {
			st.IP = firstIP(ip)
		}
	}
	return helperproto.Response{OK: true, WifiStatus: st}
}

func handleListWifi() helperproto.Response {
	out, err := runCmd(15*time.Second, "/usr/bin/nmcli", "-t", "-f", "ACTIVE,SSID,SIGNAL,SECURITY", "device", "wifi", "list")
	if err != nil {
		return helperproto.Response{OK: false, Error: sanitize(out)}
	}
	var nets []helperproto.WifiNetwork
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		// Format: ACTIVE:SSID:SIGNAL:SECURITY (nmcli maskiert ':' in Feldern als '\:')
		f := splitNmcli(l)
		if len(f) < 4 {
			continue
		}
		ssid := f[1]
		if ssid == "" || seen[ssid] {
			continue
		}
		seen[ssid] = true
		signal, _ := strconv.Atoi(f[2])
		nets = append(nets, helperproto.WifiNetwork{
			SSID:    ssid,
			Signal:  signal,
			Secured: f[3] != "" && f[3] != "--",
			Active:  f[0] == "yes",
		})
	}
	return helperproto.Response{OK: true, WifiNetworks: nets}
}

// ─── Block-Geräte auflisten ──────────────────────────────────────────────────

func handleListBlockDevices() helperproto.Response {
	// lsblk mit maschinenlesbarer Ausgabe: NAME,PATH,UUID,FSTYPE,SIZE,MOUNTPOINT,RM,TYPE
	out, err := runCmd(10*time.Second, "/bin/lsblk",
		"-P", "-b", "-o", "NAME,PATH,UUID,FSTYPE,SIZE,MOUNTPOINT,RM,TYPE")
	if err != nil {
		return helperproto.Response{OK: false, Error: sanitize(out)}
	}
	var devs []helperproto.BlockDevice
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		kv := parseLsblkLine(l)
		if kv["TYPE"] != "part" && kv["TYPE"] != "disk" {
			continue
		}
		if kv["FSTYPE"] == "" || kv["UUID"] == "" {
			continue // ohne Dateisystem/UUID nicht mountbar
		}
		sizeBytes, _ := strconv.ParseFloat(kv["SIZE"], 64)
		devs = append(devs, helperproto.BlockDevice{
			Name:       kv["NAME"],
			Path:       kv["PATH"],
			UUID:       kv["UUID"],
			FsType:     kv["FSTYPE"],
			SizeGB:     sizeBytes / (1024 * 1024 * 1024),
			Mounted:    kv["MOUNTPOINT"] != "",
			MountPoint: kv["MOUNTPOINT"],
			Removable:  kv["RM"] == "1",
			AutoMount:  isAutomount(kv["UUID"]),
		})
	}
	return helperproto.Response{OK: true, BlockDevices: devs}
}

// ─── Hilfen ──────────────────────────────────────────────────────────────────

// currentMountPoint prüft über /proc/mounts, ob ein Gerät bereits gemountet ist.
func currentMountPoint(dev string) (string, bool) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", false
	}
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == dev {
			return f[1], true
		}
	}
	return "", false
}

// sanitize kürzt und entschärft Tool-Ausgaben, bevor sie nach außen gehen
// (keine mehrzeiligen internen Details lecken).
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func firstIP(nmcliOut string) string {
	for _, l := range strings.Split(nmcliOut, "\n") {
		l = strings.TrimPrefix(l, "IP4.ADDRESS[1]:")
		l = strings.TrimSpace(l)
		if l != "" && strings.Contains(l, ".") {
			if i := strings.IndexByte(l, '/'); i >= 0 {
				return l[:i]
			}
			return l
		}
	}
	return ""
}

func writeResp(conn net.Conn, resp helperproto.Response) {
	b, _ := json.Marshal(resp)
	b = append(b, '\n')
	_, _ = conn.Write(b)
}

func logf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[fundus-helper] "+format+"\n", args...)
}

func fatal(format string, args ...interface{}) {
	logf("FATAL: "+format, args...)
	os.Exit(1)
}

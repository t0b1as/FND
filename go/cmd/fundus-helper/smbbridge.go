package main

// SMB-Brücke für ferne Freigaben (R736).
//
// Der Node kann das nicht selbst: Einhängen und Samba einrichten verlangt root,
// und der Node läuft bewusst unter einem eigenen Benutzer. Also macht es der
// Helfer – wie schon beim Einhängen von Laufwerken.
//
//   Windows ──SMB──> dieser Pi (Samba) ──davfs2──> eigener Node ──P2P──> fern
//
// ── Was der Aufrufer NICHT bestimmen darf ──────────────────────────────────
//
// Nur den Anzeigenamen, die WebDAV-Adresse und die Zugangskennung. Alles
// andere legt der Helfer fest:
//
//   * der Einhängepunkt ist immer /srv/fundus/<bereinigter Name>
//   * die Samba-Datei heißt immer /etc/samba/smb.conf.d/fundus-<Name>.conf
//   * die WebDAV-Adresse MUSS auf 127.0.0.1 zeigen
//
// Ohne diese Grenzen wäre der Helfer ein Werkzeug, um als root beliebige
// Verzeichnisse einzuhängen und freizugeben. Dieselbe Überlegung wie beim
// Laufwerk-Einhängen, wo der Mountpunkt auch nie vom Aufrufer kommt.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fundus/node/internal/helperproto"
)

const (
	smbBasis    = "/srv/fundus"
	smbConfDir  = "/etc/samba/smb.conf.d"
	smbConfMain = "/etc/samba/smb.conf"
	smbBenutzer = "fundussmb"
	davSecrets  = "/etc/davfs2/secrets"
)

var smbNameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)

// smbNameBereinigen macht aus einem Anzeigenamen einen Freigabenamen.
func smbNameBereinigen(n string) string {
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 38 {
		out = out[:38]
	}
	return out
}

// handleSmbBridgeAdd richtet Einhängung und Freigabe ein.
func handleSmbBridgeAdd(req helperproto.Request) helperproto.Response {
	name := smbNameBereinigen(req.Name)
	if !smbNameOK.MatchString(name) {
		return helperproto.Response{OK: false, Error: "ungültiger Freigabename"}
	}
	// Die Adresse MUSS auf diesen Rechner zeigen. Sonst ließe sich der Helfer
	// benutzen, um beliebige fremde WebDAV-Server als root einzuhängen.
	if !strings.HasPrefix(req.DavURL, "http://127.0.0.1:") {
		return helperproto.Response{OK: false,
			Error: "DAV-Adresse muss auf 127.0.0.1 zeigen"}
	}
	if strings.ContainsAny(req.DavURL, " \t\n\r\"'") || strings.Contains(req.DavURL, "..") {
		return helperproto.Response{OK: false, Error: "unzulässige Zeichen in der Adresse"}
	}
	if req.DavToken == "" || strings.ContainsAny(req.DavToken, " \t\n\r") {
		return helperproto.Response{OK: false, Error: "ungültige Zugangskennung"}
	}

	// R740: Fehlende Pakete selbst nachinstallieren.
	//
	// Das Deploy-Skript prüft sie, das Update über GitHub aber NICHT – es
	// verschiebt nur Dateien und startet neu. Auf einem so aktualisierten Node
	// wäre die Brücke sonst dauerhaft unbenutzbar, mit einer Fehlermeldung, die
	// zu einer Handarbeit auffordert.
	//
	// Installiert wird nur hier, also wenn der Betreiber die Funktion
	// ausdrücklich anfordert – nicht ungefragt im Hintergrund.
	if fehlend := fehlendePakete(); len(fehlend) > 0 {
		logf("SMB-Brücke: installiere fehlende Pakete: %s", strings.Join(fehlend, " "))
		args := append([]string{"install", "-y", "-qq"}, fehlend...)
		cmd := exec.Command("apt-get", args...)
		// Ohne diese Vorgabe bleibt davfs2 bei seiner Rückfrage stehen und der
		// Aufruf läuft in die Zeitüberschreitung.
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := cmd.CombinedOutput(); err != nil {
			return helperproto.Response{OK: false, Error: "Pakete fehlen (" +
				strings.Join(fehlend, ", ") + ") und ließen sich nicht installieren: " +
				strings.TrimSpace(letzteZeilen(string(out), 3))}
		}
		if fehlend2 := fehlendePakete(); len(fehlend2) > 0 {
			return helperproto.Response{OK: false,
				Error: "Pakete weiterhin nicht vorhanden: " + strings.Join(fehlend2, ", ")}
		}
	}

	mnt := filepath.Join(smbBasis, name)
	if err := os.MkdirAll(mnt, 0o775); err != nil {
		return helperproto.Response{OK: false, Error: "Einhängepunkt: " + err.Error()}
	}

	// Zugangsdaten für davfs2. Die Datei enthält Kennungen im Klartext und
	// bekommt deshalb 0600; die alte Zeile für dieselbe Adresse fliegt raus,
	// damit sich keine Altlasten ansammeln.
	if err := davSecretSetzen(req.DavURL, req.DavToken); err != nil {
		return helperproto.Response{OK: false, Error: "Zugangsdatei: " + err.Error()}
	}

	// Einhängen. use_locks=0, weil unser WebDAV Sperren zwar bestätigt, aber
	// nicht durchsetzt – davfs2 würde sonst darauf bauen.
	if !istEingehaengt(mnt) {
		cmd := exec.Command("mount", "-t", "davfs", "-o",
			"uid=root,gid=root,file_mode=0664,dir_mode=0775,use_locks=0",
			req.DavURL, mnt)
		if out, err := cmd.CombinedOutput(); err != nil {
			return helperproto.Response{OK: false,
				Error: "Einhängen fehlgeschlagen: " + strings.TrimSpace(string(out))}
		}
	}

	if err := sambaFreigabeSchreiben(name, mnt, req.Guest); err != nil {
		_ = exec.Command("umount", mnt).Run()
		return helperproto.Response{OK: false, Error: "Samba: " + err.Error()}
	}
	if !req.Guest && req.SmbPass != "" {
		if err := sambaBenutzerSetzen(req.SmbPass); err != nil {
			return helperproto.Response{OK: false, Error: "Samba-Benutzer: " + err.Error()}
		}
	}
	if out, err := exec.Command("systemctl", "reload-or-restart", "smbd").CombinedOutput(); err != nil {
		return helperproto.Response{OK: false,
			Error: "smbd neu laden: " + strings.TrimSpace(string(out))}
	}
	logf("SMB-Brücke eingerichtet: %s → %s", name, req.DavURL)
	return helperproto.Response{OK: true, Mountpoint: mnt}
}

// handleSmbBridgeRemove baut alles wieder ab.
func handleSmbBridgeRemove(req helperproto.Request) helperproto.Response {
	name := smbNameBereinigen(req.Name)
	if !smbNameOK.MatchString(name) {
		return helperproto.Response{OK: false, Error: "ungültiger Freigabename"}
	}
	mnt := filepath.Join(smbBasis, name)
	conf := filepath.Join(smbConfDir, "fundus-"+name+".conf")

	// Reihenfolge zählt: erst Samba die Freigabe nehmen, dann aushängen. Sonst
	// hält smbd den Einhängepunkt offen und umount scheitert.
	_ = os.Remove(conf)
	_ = includeZeileEntfernen(conf)
	_, _ = exec.Command("systemctl", "reload-or-restart", "smbd").CombinedOutput()

	if istEingehaengt(mnt) {
		if out, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
			// Zweiter Versuch: verzögert aushängen, falls noch jemand liest.
			if out2, err2 := exec.Command("umount", "-l", mnt).CombinedOutput(); err2 != nil {
				return helperproto.Response{OK: false, Error: "Aushängen: " +
					strings.TrimSpace(string(out)) + " / " + strings.TrimSpace(string(out2))}
			}
		}
	}
	_ = os.Remove(mnt) // nur wenn leer; schlägt sonst still fehl
	logf("SMB-Brücke entfernt: %s", name)
	return helperproto.Response{OK: true}
}

// handleSmbBridgeList zählt auf, was eingerichtet ist.
func handleSmbBridgeList() helperproto.Response {
	eintraege, err := os.ReadDir(smbConfDir)
	if err != nil {
		return helperproto.Response{OK: true, Shares: nil} // nichts eingerichtet
	}
	var out []helperproto.SmbShare
	for _, e := range eintraege {
		n := e.Name()
		if !strings.HasPrefix(n, "fundus-") || !strings.HasSuffix(n, ".conf") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(n, "fundus-"), ".conf")
		mnt := filepath.Join(smbBasis, name)
		out = append(out, helperproto.SmbShare{
			Name:       name,
			Mountpoint: mnt,
			Mounted:    istEingehaengt(mnt),
		})
	}
	return helperproto.Response{OK: true, Shares: out}
}

// ── Hilfen ────────────────────────────────────────────────────────────────

func istEingehaengt(pfad string) bool {
	return exec.Command("mountpoint", "-q", pfad).Run() == nil
}

func davSecretSetzen(url, token string) error {
	alt, _ := os.ReadFile(davSecrets)
	var neu []string
	for _, z := range strings.Split(string(alt), "\n") {
		if z == "" || strings.HasPrefix(z, url+" ") {
			continue
		}
		neu = append(neu, z)
	}
	neu = append(neu, url+" fundus "+token)
	// Erst daneben schreiben, dann umbenennen – ein Abbruch mittendrin würde
	// sonst alle Zugänge auf einmal unbrauchbar machen.
	tmp := davSecrets + ".neu"
	if err := os.WriteFile(tmp, []byte(strings.Join(neu, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, davSecrets)
}

func sambaFreigabeSchreiben(name, mnt string, gast bool) error {
	if err := os.MkdirAll(smbConfDir, 0o755); err != nil {
		return err
	}
	conf := filepath.Join(smbConfDir, "fundus-"+name+".conf")
	// R746: Gastzugang auf Wunsch – im Heimnetz oft das Angemessene.
	//
	// "map to guest = Bad User" gehört dazu: Ohne das weist Windows einen
	// unbekannten Benutzernamen ab, statt ihn als Gast zu behandeln. Windows
	// schickt nämlich immer einen Namen mit, auch wenn man keinen eingibt.
	//
	// Der Hinweis darf nicht fehlen: Damit kann JEDER im Netz lesen UND
	// schreiben, auch ein fremdes Gerät im WLAN.
	zugriff := "   guest ok = no\n   valid users = " + smbBenutzer
	if gast {
		zugriff = "   guest ok = yes\n   guest only = yes\n   force user = root"
	}
	inhalt := fmt.Sprintf(`[%s]
   path = %s
   browseable = yes
   read only = no
%s
   socket options = TCP_NODELAY
`, name, mnt, zugriff)
	if err := os.WriteFile(conf, []byte(inhalt), 0o644); err != nil {
		return err
	}
	// Samba kennt kein Verzeichnis-Include – jede Datei muss einzeln in der
	// Hauptdatei stehen.
	haupt, err := os.ReadFile(smbConfMain)
	if err != nil {
		return err
	}
	// R746: Gastzugang verlangt diese globale Einstellung – ohne sie weist
	// Samba einen unbekannten Benutzernamen ab, statt ihn als Gast zu nehmen.
	if gast && !strings.Contains(string(haupt), "map to guest") {
		if f, ferr := os.OpenFile(smbConfMain, os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			_, _ = f.WriteString("\n[global]\n   map to guest = Bad User\n")
			f.Close()
			haupt, _ = os.ReadFile(smbConfMain)
		}
	}
	zeile := "include = " + conf
	if strings.Contains(string(haupt), zeile) {
		return nil
	}
	f, err := os.OpenFile(smbConfMain, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + zeile + "\n")
	return err
}

func includeZeileEntfernen(conf string) error {
	haupt, err := os.ReadFile(smbConfMain)
	if err != nil {
		return err
	}
	zeile := "include = " + conf
	var neu []string
	for _, z := range strings.Split(string(haupt), "\n") {
		if strings.TrimSpace(z) == zeile {
			continue
		}
		neu = append(neu, z)
	}
	return os.WriteFile(smbConfMain, []byte(strings.Join(neu, "\n")), 0o644)
}

func sambaBenutzerSetzen(pass string) error {
	if exec.Command("id", "-u", smbBenutzer).Run() != nil {
		if out, err := exec.Command("useradd", "-M", "-s", "/usr/sbin/nologin",
			smbBenutzer).CombinedOutput(); err != nil {
			return fmt.Errorf("useradd: %s", strings.TrimSpace(string(out)))
		}
	}
	// Das Kennwort geht über die Standardeingabe, nicht als Befehlsargument:
	// Argumente stehen in der Prozessliste und wären für jeden sichtbar.
	cmd := exec.Command("smbpasswd", "-s", "-a", smbBenutzer)
	cmd.Stdin = strings.NewReader(pass + "\n" + pass + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("smbpasswd: %s", strings.TrimSpace(string(out)))
	}
	return exec.Command("smbpasswd", "-e", smbBenutzer).Run()
}

// fehlendePakete prüft, welche der benötigten Programme fehlen (R740).
func fehlendePakete() []string {
	var fehlend []string
	// Programm → Paketname. Beide liegen in /usr/sbin, das im PATH eines
	// Dienstes nicht immer steht – deshalb zusätzlich direkt nachsehen.
	for prog, paket := range map[string]string{"mount.davfs": "davfs2", "smbd": "samba"} {
		if _, err := exec.LookPath(prog); err == nil {
			continue
		}
		if _, err := os.Stat("/usr/sbin/" + prog); err == nil {
			continue
		}
		if _, err := os.Stat("/sbin/" + prog); err == nil {
			continue
		}
		fehlend = append(fehlend, paket)
	}
	return fehlend
}

// letzteZeilen kürzt eine Ausgabe auf die letzten n Zeilen.
func letzteZeilen(s string, n int) string {
	z := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(z) > n {
		z = z[len(z)-n:]
	}
	return strings.Join(z, " | ")
}

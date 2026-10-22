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
	"strconv"
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
	// R748: Zurückgebliebene Kennungsdatei entfernen.
	//
	// davfs2 legt unter /var/run/mount.davfs/<pfad>.pid eine Datei an und
	// verweigert das Einhängen, solange sie existiert – auch wenn der zugehörige
	// Vorgang längst beendet ist. Nach einem abgebrochenen Versuch bleibt sie
	// liegen, und von da an scheitert JEDES weitere Einhängen mit einer
	// Meldung, die nach einem belegten Verzeichnis aussieht.
	//
	// Entfernt wird sie nur, wenn wirklich kein Vorgang mehr läuft. Liefe noch
	// einer, würde das Löschen ein laufendes Einhängen zerstören.
	if err := davPidAufraeumen(mnt); err != nil {
		return helperproto.Response{OK: false, Error: err.Error()}
	}

	if !istEingehaengt(mnt) {
		cmd := exec.Command("mount", "-t", "davfs", "-o",
			"uid=root,gid=root,file_mode=0664,dir_mode=0775,use_locks=0",
			req.DavURL, mnt)
		if out, err := cmd.CombinedOutput(); err != nil {
			return helperproto.Response{OK: false,
				Error: "Einhängen fehlgeschlagen: " + strings.TrimSpace(string(out))}
		}
	}

	// R754: NACHSEHEN, ob die Einhängung trägt.
	//
	// Bisher galt ein fehlerfreies mount als Erfolg. Ein leerer Ordner sieht
	// aber genauso aus wie eine gescheiterte Einhängung, und beides führt zu
	// einer leeren Windows-Freigabe. Die Zahl im Protokoll unterscheidet sie.
	if eintraege, lerr := os.ReadDir(mnt); lerr != nil {
		logf("WARNUNG: Einhängepunkt %s nicht lesbar: %v", mnt, lerr)
	} else {
		logf("Einhängung %s: %d Einträge sichtbar, eingehängt=%v",
			mnt, len(eintraege), istEingehaengt(mnt))
		if len(eintraege) == 0 {
			logf("WARNUNG: Der eingehängte Ordner ist LEER. Entweder liefert die " +
				"ferne Freigabe nichts, oder davfs2 verwirft die Antwort " +
				"(Zwischenspeicher: /var/cache/davfs2).")
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
	// R754: ALLE Verweise auf diese Freigabe aus der Hauptdatei nehmen, nicht
	// nur den exakt passenden. Durch die falsche Einfügestelle aus R746 können
	// Zeilen an verschiedenen Orten stehen, auch in fremden Abschnitten – die
	// blieben sonst liegen und machten die Konfiguration unbrauchbar.
	_ = includeZeilenEntfernen("fundus-" + name + ".conf")
	// Neu starten, nicht nur neu laden: Nach einer vorher fehlerhaften
	// Konfiguration lädt smbd sie teils gar nicht erst neu ein.
	if out, err := exec.Command("systemctl", "restart", "smbd").CombinedOutput(); err != nil {
		logf("smbd-Neustart beim Entfernen: %s", strings.TrimSpace(string(out)))
	}

	if istEingehaengt(mnt) {
		if out, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
			// Zweiter Versuch: verzögert aushängen, falls noch jemand liest.
			if out2, err2 := exec.Command("umount", "-l", mnt).CombinedOutput(); err2 != nil {
				return helperproto.Response{OK: false, Error: "Aushängen: " +
					strings.TrimSpace(string(out)) + " / " + strings.TrimSpace(string(out2))}
			}
		}
	}
	// R748: Auch hier die Kennungsdatei wegräumen, sonst blockiert sie das
	// nächste Einrichten – genau das war der Fall, in dem „Entfernen" nichts
	// gebracht hat.
	_ = davPidAufraeumen(mnt)
	// R752: Und den Zwischenspeicher. davfs2 merkt sich Verzeichnislisten;
	// eine einmal leer gesehene Liste bleibt leer, auch nachdem der Server
	// längst richtig antwortet. Genau das hat uns nach der XML-Korrektur einen
	// weiteren Durchgang gekostet.
	_ = davCacheAufraeumen(name)
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
	// R753: "guest only" und "force user" widersprechen sich – das erste
	// verlangt das Gastkonto, das zweite root. Samba verträgt die Kombination
	// nicht, und Windows meldet dann "Falscher Parameter".
	//
	// Richtig ist: Gastzugang erlauben und über force user festlegen, mit
	// welchen Rechten gearbeitet wird. Die Einhängung gehört root, also root.
	zugriff := "   guest ok = no\n   valid users = " + smbBenutzer
	if gast {
		zugriff = "   guest ok = yes\n   force user = root"
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
		// R753: ebenfalls in den vorhandenen [global]-Abschnitt, statt einen
		// zweiten ans Ende zu hängen.
		_ = includeInGlobal("   map to guest = Bad User")
		haupt, _ = os.ReadFile(smbConfMain)
	}
	zeile := "include = " + conf
	if strings.Contains(string(haupt), zeile) {
		return nil
	}
	// R753: In den [global]-Abschnitt einfügen, NICHT ans Dateiende.
	//
	// Angehängt landet die Zeile innerhalb des letzten Abschnitts – bei einer
	// Standard-smb.conf ist das [print$], also die Druckertreiber. testparm
	// zeigte genau das: "include" mitten in [print$]. Dass die Freigabe
	// trotzdem auftauchte, machte es nur schwerer zu sehen.
	return includeInGlobal(zeile)
}

// includeZeilenEntfernen nimmt jede Zeile heraus, die auf diese Datei verweist
// (R754) – unabhängig von Einrückung und Abschnitt.
func includeZeilenEntfernen(dateiname string) error {
	roh, err := os.ReadFile(smbConfMain)
	if err != nil {
		return err
	}
	var neu []string
	entfernt := 0
	for _, z := range strings.Split(string(roh), "\n") {
		if strings.Contains(z, dateiname) && strings.Contains(z, "include") {
			entfernt++
			continue
		}
		neu = append(neu, z)
	}
	if entfernt == 0 {
		return nil
	}
	logf("%d Verweis(e) auf %s aus smb.conf entfernt", entfernt, dateiname)
	return os.WriteFile(smbConfMain, []byte(strings.Join(neu, "\n")), 0o644)
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

// davPidAufraeumen entfernt eine verwaiste davfs2-Kennungsdatei (R748).
//
// Der Dateiname entsteht aus dem Einhängepunkt, wobei Schrägstriche zu
// Bindestrichen werden: /srv/fundus/NZB → srv-fundus-NZB.pid
func davPidAufraeumen(mnt string) error {
	name := strings.TrimPrefix(mnt, "/")
	name = strings.ReplaceAll(name, "/", "-") + ".pid"
	pfad := filepath.Join("/var/run/mount.davfs", name)

	roh, err := os.ReadFile(pfad)
	if err != nil {
		return nil // keine Datei, nichts zu tun
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(roh)))
	if pid > 0 {
		// Läuft der Vorgang noch? /proc/<pid> ist die verlässliche Auskunft;
		// ein Signal 0 wäre auch möglich, sagt aber bei fremden Besitzern
		// weniger eindeutig.
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
			// Noch aktiv – aber ist es wirklich ein davfs2-Vorgang für DIESEN
			// Punkt? Wenn ja, nicht anfassen.
			if cmd, cerr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); cerr == nil &&
				strings.Contains(string(cmd), "davfs") {
				return fmt.Errorf("es läuft noch ein Einhängevorgang (PID %d) für %s – "+
					"bitte kurz warten oder 'sudo umount %s'", pid, mnt, mnt)
			}
		}
	}
	if err := os.Remove(pfad); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("verwaiste Kennungsdatei %s ließ sich nicht entfernen: %v", pfad, err)
	}
	logf("verwaiste davfs2-Kennungsdatei entfernt: %s", pfad)
	return nil
}

// davCacheAufraeumen entfernt den Zwischenspeicher einer Einhängung (R752).
//
// davfs2 legt unter /var/cache/davfs2/ je Einhängung ein Verzeichnis an, dessen
// Name den Pfad enthält. Es zu löschen erzwingt ein frisches Einlesen.
//
// Streng begrenzt: NUR unterhalb von /var/cache/davfs2, und nur Einträge, deren
// Name den Freigabenamen enthält. Ohne diese Schranken wäre das hier eine
// Funktion, die als root beliebige Verzeichnisse löscht.
func davCacheAufraeumen(name string) error {
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return fmt.Errorf("ungültiger Name")
	}
	const basis = "/var/cache/davfs2"
	eintraege, err := os.ReadDir(basis)
	if err != nil {
		return nil // kein Zwischenspeicher vorhanden
	}
	for _, e := range eintraege {
		if !e.IsDir() || !strings.Contains(e.Name(), name) {
			continue
		}
		pfad := filepath.Join(basis, e.Name())
		// Letzte Sicherung: Das Ergebnis MUSS unterhalb der Basis liegen.
		if !strings.HasPrefix(pfad, basis+string(filepath.Separator)) {
			continue
		}
		if err := os.RemoveAll(pfad); err == nil {
			logf("davfs2-Zwischenspeicher geleert: %s", pfad)
		}
	}
	return nil
}

// includeInGlobal fügt eine Zeile in den [global]-Abschnitt ein (R753).
//
// In einer smb.conf gilt eine Zeile immer für den Abschnitt, in dem sie steht.
// Ans Dateiende angehängt gehört sie zum LETZTEN Abschnitt – bei der
// Standarddatei sind das die Druckertreiber.
func includeInGlobal(zeile string) error {
	roh, err := os.ReadFile(smbConfMain)
	if err != nil {
		return err
	}
	zeilen := strings.Split(string(roh), "\n")
	for i, z := range zeilen {
		if strings.EqualFold(strings.TrimSpace(z), "[global]") {
			// Direkt hinter die Abschnittsüberschrift.
			neu := append([]string{}, zeilen[:i+1]...)
			neu = append(neu, zeile)
			neu = append(neu, zeilen[i+1:]...)
			return os.WriteFile(smbConfMain, []byte(strings.Join(neu, "\n")), 0o644)
		}
	}
	// Kein [global] vorhanden: einen anlegen, aber am ANFANG der Datei, damit
	// die Zeile nicht wieder in einem fremden Abschnitt landet.
	neu := append([]string{"[global]", zeile, ""}, zeilen...)
	return os.WriteFile(smbConfMain, []byte(strings.Join(neu, "\n")), 0o644)
}

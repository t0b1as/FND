// Package helperproto definiert das schmale, streng typisierte Protokoll zwischen
// dem unprivilegierten fundus-node und dem privilegierten fundus-helper (root).
//
// SICHERHEITSMODELL
// -----------------
// Der Helper läuft als root und ist damit ein hochsensibles Ziel. Deshalb:
//   - Kommunikation NUR über einen Unix-Domain-Socket (kein TCP, kein Netz).
//   - Der Socket ist mit 0660 root:fundus geschützt: nur der fundus-User (der
//     Node) darf verbinden.
//   - Es gibt KEIN generisches Kommando-Passthrough. Der Helper akzeptiert nur
//     die hier definierten Aktionen mit strikt validierten Feldern.
//   - Jeder Parameter wird im Helper gegen eine Whitelist / strenge Regex
//     geprüft, bevor irgendein Systemaufruf erfolgt.
//
// Das Ziel ist eine minimale, auditierbare Angriffsfläche: Selbst wenn der Node
// kompromittiert würde, kann er über den Helper nur genau diese eng definierten
// Aktionen auslösen — kein beliebiges Root-Kommando.
package helperproto

// SocketPath ist der feste Pfad des Helper-Sockets. Liegt unter /run (tmpfs),
// wird beim Boot neu erzeugt und ist nicht persistent.
const SocketPath = "/run/fundus-helper.sock"

// ProtocolVersion erlaubt es, Node und Helper bei Inkompatibilität sauber
// abzulehnen (statt undefiniertem Verhalten).
const ProtocolVersion = 1

// Action ist die Art der angeforderten privilegierten Operation.
type Action string

const (
	// ActionPing prüft nur, ob der Helper läuft und antwortet (Health-Check).
	ActionPing Action = "ping"

	// ActionListBlockDevices listet anschließbare Block-Geräte (lsblk-Äquivalent),
	// inkl. ob sie gemountet sind. Nur lesend, aber im Helper, weil manche Infos
	// Root-Rechte brauchen.
	ActionListBlockDevices Action = "list_block_devices"

	// ActionMountDrive mountet ein Gerät (per UUID identifiziert) an einen festen,
	// vom Helper kontrollierten Mountpunkt unter /mnt/fundus-<uuid>.
	ActionMountDrive Action = "mount_drive"

	// ActionUnmountDrive hängt ein zuvor vom Helper gemountetes Gerät wieder aus.
	ActionUnmountDrive Action = "unmount_drive"

	// ActionListWifi listet sichtbare WLAN-Netze (SSID, Signal, gesichert?).
	ActionListWifi Action = "list_wifi"

	// ActionConnectWifi verbindet mit einem WLAN (SSID + optional Passwort).
	ActionConnectWifi Action = "connect_wifi"

	// ActionWifiStatus liefert den aktuellen WLAN-Verbindungsstatus.
	ActionWifiStatus Action = "wifi_status"

	// ActionAutomountScan weist den Helper an, alle für Automount registrierten
	// Laufwerke zu prüfen und die vorhandenen, noch nicht gemounteten einzuhängen.
	// Wird intern beim Boot/periodisch und extern per udev (Einstecken) ausgelöst.
	ActionAutomountScan Action = "automount_scan"

	// ActionApplyUpdate weist den Helper an, ein signiertes Update anzuwenden:
	// Manifest-Signatur SELBST prüfen (Tor gegen kompromittierten Node), ZIP von
	// der Manifest-URL laden, Argon2id-Hash prüfen, nach /opt/fundus entpacken
	// (Backup + atomischer Swap) und den Node-Service neu starten. Der Helper
	// läuft als root und hat genau die Rechte, die der gehärtete Node nicht hat.
	ActionApplyUpdate Action = "apply_update"

	// ActionRestartNode weist den Helper an, den fundus-node-Dienst neu zu starten
	// (systemctl restart). Wird z.B. genutzt, um eine ausstehende Chain-
	// Verschiebung anzuwenden, die beim Start ausgeführt wird.
	ActionRestartNode Action = "restart_node"
)

// Request ist die vom Node gesendete Anfrage (JSON, eine Zeile).
type Request struct {
	Version int    `json:"version"`
	Action  Action `json:"action"`

	// Feld je nach Action. Nicht benötigte Felder bleiben leer und werden ignoriert.
	UUID     string `json:"uuid,omitempty"`     // mount/unmount: Filesystem-UUID
	SSID     string `json:"ssid,omitempty"`     // connect_wifi: Netzname
	Password string `json:"password,omitempty"` // connect_wifi: WLAN-Passwort (nie geloggt)
	Manifest string `json:"manifest,omitempty"` // apply_update: komplettes signiertes Manifest (JSON)
}

// Response ist die Antwort des Helpers (JSON, eine Zeile).
type Response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"` // ping: Revision des Helpers

	// Ergebnis-Nutzlast je nach Action.
	MountPoint   string        `json:"mount_point,omitempty"`   // mount_drive: wohin gemountet
	BlockDevices []BlockDevice `json:"block_devices,omitempty"` // list_block_devices
	WifiNetworks []WifiNetwork `json:"wifi_networks,omitempty"` // list_wifi
	WifiStatus   *WifiState    `json:"wifi_status,omitempty"`   // wifi_status
}

// BlockDevice beschreibt ein anschließbares Speichergerät.
type BlockDevice struct {
	Name       string `json:"name"`        // z.B. sda1
	Path       string `json:"path"`        // z.B. /dev/sda1
	UUID       string `json:"uuid"`        // Filesystem-UUID (stabil)
	FsType     string `json:"fs_type"`     // z.B. ext4, vfat
	SizeGB     float64 `json:"size_gb"`    // Kapazität
	Mounted    bool   `json:"mounted"`     // aktuell gemountet?
	MountPoint string `json:"mount_point"` // falls gemountet
	Removable  bool   `json:"removable"`   // Wechseldatenträger (USB)?
	AutoMount  bool   `json:"auto_mount"`  // für Automount registriert (kommt bei Boot/Einstecken wieder)?
}

// WifiNetwork ist ein sichtbares WLAN.
type WifiNetwork struct {
	SSID    string `json:"ssid"`
	Signal  int    `json:"signal"`  // 0..100
	Secured bool   `json:"secured"` // Passwort nötig?
	Active  bool   `json:"active"`  // aktuell verbunden?
}

// WifiState ist der aktuelle Verbindungsstatus.
type WifiState struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	IP        string `json:"ip,omitempty"`
	SetupAP   string `json:"setup_ap,omitempty"`      // aktiver Setup-Hotspot (SSID), sonst leer
	SetupAPParallel bool `json:"setup_ap_parallel,omitempty"` // parallel zur Verbindung (Testmodus)
}

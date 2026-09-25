package main

// Setup-Hotspot "FUNDUS Rnnn".
//
// Findet der Pi kein WLAN, öffnet er einen offenen Hotspot mit dem Namen
// "FUNDUS <Revision>". Wer sich verbindet, landet über die Anmeldeseiten-
// Erkennung (Android/iOS/Windows) in den Einstellungen und trägt dort die
// WLAN-Zugangsdaten ein – geschützt wie immer durch das Admin-Passwort.
//
// Betriebsarten (FUNDUS_SETUP_AP in /etc/fundus/fundus.env):
//   auto (Standard) – Hotspot, wenn >90 s kein WLAN verbunden ist. Das Funk-
//                     modul kann dann nicht gleichzeitig Client sein: alle
//                     5 min gibt der Hotspot (wenn niemand verbunden ist) kurz
//                     frei, damit sich der Pi mit einem bekannten Netz verbindet.
//   test            – Hotspot PARALLEL zur normalen Verbindung auf einer
//                     virtuellen Schnittstelle (uap0). Der Pi-Funkchip kann
//                     AP+Client nur auf DEMSELBEN Kanal wie das WLAN.
//   off             – kein Hotspot.
// FUNDUS_SETUP_AP_PASSWORD (≥ 8 Zeichen) macht den Hotspot WPA2-geschützt.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apConName     = "fundus-setup-ap"
	apVirtualIf   = "uap0"
	apAddress     = "10.42.0.1/24"
	captiveConf   = "/etc/NetworkManager/dnsmasq-shared.d/fundus-captive.conf"
	fundusEnvPath = "/etc/fundus/fundus.env"
)

var ap struct {
	sync.Mutex
	active    bool
	iface     string // wlan0 (exklusiv) oder uap0 (parallel)
	exclusive bool
	since     time.Time
	cache     []byte // letzte WLAN-Liste vor dem Start (exklusiv: Scan im AP-Modus kaum möglich)
}

func apSSID() string {
	if Version != "" && Version != "unbekannt" {
		return "FUNDUS " + Version
	}
	return "FUNDUS"
}

// readEnv liest einzelne Werte aus fundus.env (der Helper hat kein EnvironmentFile).
func readEnv(keys ...string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(fundusEnvPath)
	if err != nil {
		return out
	}
	defer f.Close()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if i := strings.IndexByte(l, '='); i > 0 {
			k := strings.TrimSpace(l[:i])
			if want[k] {
				out[k] = strings.Trim(strings.TrimSpace(l[i+1:]), `"'`)
			}
		}
	}
	return out
}

func setupAPMode() (mode, password string) {
	env := readEnv("FUNDUS_SETUP_AP", "FUNDUS_SETUP_AP_PASSWORD")
	mode = strings.ToLower(env["FUNDUS_SETUP_AP"])
	switch mode {
	case "off", "test":
	default:
		mode = "auto"
	}
	return mode, env["FUNDUS_SETUP_AP_PASSWORD"]
}

// clientWifiConnected: ist ein WLAN-Gerät (nicht der Hotspot) als Client verbunden?
func clientWifiConnected() bool {
	out, err := runCmd(10*time.Second, "/usr/bin/nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		f := splitNmcli(strings.TrimSpace(l))
		if len(f) < 4 || f[1] != "wifi" || f[0] == apVirtualIf {
			continue
		}
		if strings.HasPrefix(f[2], "connected") && f[3] != apConName {
			return true
		}
	}
	return false
}

// staChannel liefert Kanal und Band der bestehenden Client-Verbindung.
func staChannel(iface string) (channel int, band string) {
	out, err := runCmd(5*time.Second, "/usr/sbin/iw", "dev", iface, "link")
	if err != nil {
		return 0, ""
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "freq:") {
			fq, _ := strconv.Atoi(strings.Fields(strings.TrimPrefix(l, "freq:"))[0])
			switch {
			case fq >= 2412 && fq <= 2472:
				return (fq - 2407) / 5, "bg"
			case fq == 2484:
				return 14, "bg"
			case fq >= 5000:
				return (fq - 5000) / 5, "a"
			}
		}
	}
	return 0, ""
}

func startSetupAP(parallel bool, password string) {
	ap.Lock()
	defer ap.Unlock()
	if ap.active {
		return
	}
	base := wifiInterface()
	iface := base
	channel, band := 0, "bg"
	if parallel {
		// Virtuelle AP-Schnittstelle auf demselben Funkchip.
		_, _ = runCmd(5*time.Second, "/usr/sbin/iw", "dev", base, "interface", "add", apVirtualIf, "type", "__ap")
		_, _ = runCmd(5*time.Second, "/usr/bin/nmcli", "device", "set", apVirtualIf, "managed", "yes")
		time.Sleep(2 * time.Second)
		iface = apVirtualIf
		channel, band = staChannel(base)
		if band == "" {
			band = "bg"
		}
	} else {
		// Vorher scannen: im AP-Modus sieht der Chip kaum noch Netze.
		_, _ = runCmd(15*time.Second, "/usr/bin/nmcli", "device", "wifi", "rescan")
		time.Sleep(3 * time.Second)
		if resp := scanWifiList(); resp.OK { // ohne Cache-Prüfung (Sperre wird gehalten)
			ap.cache = nil
			if b, err := json.Marshal(resp.WifiNetworks); err == nil {
				ap.cache = b
			}
		}
	}

	// Anmeldeseiten-Erkennung: alle DNS-Anfragen der Hotspot-Clients auf den Pi.
	_ = os.MkdirAll("/etc/NetworkManager/dnsmasq-shared.d", 0o755)
	_ = os.WriteFile(captiveConf, []byte("# Fundus Setup-Hotspot: Captive-Portal\naddress=/#/10.42.0.1\n"), 0o644)

	_, _ = runCmd(10*time.Second, "/usr/bin/nmcli", "connection", "delete", "id", apConName)
	args := []string{"connection", "add", "type", "wifi", "ifname", iface, "con-name", apConName,
		"autoconnect", "no", "ssid", apSSID(),
		"802-11-wireless.mode", "ap", "802-11-wireless.band", band,
		"ipv4.method", "shared", "ipv4.addresses", apAddress, "ipv6.method", "disabled"}
	if channel > 0 {
		args = append(args, "802-11-wireless.channel", strconv.Itoa(channel))
	}
	if len(password) >= 8 && len(password) <= 63 {
		args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password)
	}
	if out, err := runCmd(20*time.Second, "/usr/bin/nmcli", args...); err != nil {
		logf("Setup-Hotspot: Anlegen fehlgeschlagen: %s", sanitize(out))
		_ = os.Remove(captiveConf)
		return
	}
	if out, err := runCmd(45*time.Second, "/usr/bin/nmcli", "connection", "up", "id", apConName); err != nil {
		logf("Setup-Hotspot: Start fehlgeschlagen: %s", sanitize(out))
		_, _ = runCmd(10*time.Second, "/usr/bin/nmcli", "connection", "delete", "id", apConName)
		_ = os.Remove(captiveConf)
		return
	}
	ap.active, ap.iface, ap.exclusive, ap.since = true, iface, !parallel, time.Now()
	mode := "exklusiv"
	if parallel {
		mode = "parallel (Test)"
	}
	logf("Setup-Hotspot aktiv: %q auf %s, %s, Adresse 10.42.0.1", apSSID(), iface, mode)
}

func stopSetupAP() {
	ap.Lock()
	defer ap.Unlock()
	if !ap.active {
		return
	}
	_, _ = runCmd(15*time.Second, "/usr/bin/nmcli", "connection", "down", "id", apConName)
	_, _ = runCmd(10*time.Second, "/usr/bin/nmcli", "connection", "delete", "id", apConName)
	if ap.iface == apVirtualIf {
		_, _ = runCmd(5*time.Second, "/usr/sbin/iw", "dev", apVirtualIf, "del")
	}
	_ = os.Remove(captiveConf)
	logf("Setup-Hotspot beendet")
	ap.active, ap.iface, ap.exclusive = false, "", false
}

// apHasClients: ist gerade jemand mit dem Hotspot verbunden?
func apHasClients() bool {
	ap.Lock()
	iface := ap.iface
	ap.Unlock()
	if iface == "" {
		return false
	}
	out, _ := runCmd(5*time.Second, "/usr/sbin/iw", "dev", iface, "station", "dump")
	return strings.Contains(out, "Station ")
}

func setupAPStatus() (active bool, ssid string, exclusive bool) {
	ap.Lock()
	defer ap.Unlock()
	return ap.active, apSSID(), ap.exclusive
}

// cachedWifiList: im exklusiven Hotspot-Betrieb die vorher gescannte Liste.
func cachedWifiList() ([]byte, bool) {
	ap.Lock()
	defer ap.Unlock()
	if ap.active && ap.exclusive && len(ap.cache) > 0 {
		return ap.cache, true
	}
	return nil, false
}

// setupAPWatch: Hintergrundschleife (Helper-Start).
func setupAPWatch(ctx context.Context) {
	select { // Bootphase: WLAN-Verbindung abwarten
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
	}
	var noWifiSince time.Time
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			stopSetupAP()
			return
		case <-t.C:
		}
		mode, pw := setupAPMode()
		active, _, exclusive := setupAPStatus()
		switch mode {
		case "off":
			if active {
				stopSetupAP()
			}
		case "test":
			if active && exclusive {
				stopSetupAP() // von exklusiv auf parallel wechseln
				active = false
			}
			if !active {
				startSetupAP(true, pw)
			}
		default: // auto
			if active && !exclusive {
				stopSetupAP() // Test-Hotspot aus, sobald wieder "auto"
				active = false
			}
			if clientWifiConnected() {
				noWifiSince = time.Time{}
				if active {
					stopSetupAP()
				}
				continue
			}
			if !active {
				if noWifiSince.IsZero() {
					noWifiSince = time.Now()
				}
				if time.Since(noWifiSince) > 90*time.Second {
					startSetupAP(false, pw)
				}
				continue
			}
			// Hotspot läuft exklusiv: alle 5 min kurz freigeben, damit sich der
			// Pi mit einem bekannten Netz verbinden kann – nur ohne Hotspot-Gäste.
			ap.Lock()
			since := ap.since
			ap.Unlock()
			if time.Since(since) > 5*time.Minute && !apHasClients() {
				logf("Setup-Hotspot: Pause – versuche bekannte WLANs")
				stopSetupAP()
				_, _ = runCmd(45*time.Second, "/usr/bin/nmcli", "device", "connect", wifiInterface())
				time.Sleep(20 * time.Second)
				if clientWifiConnected() {
					noWifiSince = time.Time{}
					logf("Setup-Hotspot: WLAN wieder verbunden")
					continue
				}
				startSetupAP(false, pw)
			}
		}
	}
}

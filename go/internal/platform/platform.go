// Package platform erkennt, auf was für einem Gerät der Node läuft, und
// leitet daraus die Grenzen ab (R670).
//
// Bisher standen die Grenzen fest im Code: Pixelgrenze für Vorschaubilder,
// gleichzeitige Schlüsselaustausche, gleichzeitige Vorschaubilder. Sie waren
// auf einen Pi 4 mit 4 GB gemünzt. Auf einem Pi 3 mit 768 MB reißt allein ein
// großes Bild den Dienst um, auf einem Pi 5 mit 8 GB bremsen sie grundlos.
//
// Gemessen wird NICHT die Modellbezeichnung allein, sondern vor allem der
// Arbeitsspeicher und die Kernzahl. Das Modell dient nur der Anzeige: Ein Pi 4
// mit 2 GB verhält sich wie ein Pi 3 mit 2 GB, und genau danach soll sich die
// Grenze richten, nicht nach dem Namen auf der Platine.
package platform

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Profil: die abgeleiteten Grenzen.
type Profil struct {
	Modell    string // z.B. "Raspberry Pi 5 Model B Rev 1.0"
	RAMMiB    int    // gesamter Arbeitsspeicher
	Kerne     int
	Klasse    string // "klein", "mittel", "groß"
	NodeMiB   int    // was dem Node nach MemoryHigh=45% zusteht

	DecodePixel  int // Pixelgrenze beim Entschlüsseln von Bildern
	ThumbGleich  int // gleichzeitige Vorschaubilder
	ECDHGleich   int // gleichzeitige Schlüsselaustausche (je ~64 MiB)
	LoginGleich  int // gleichzeitige Anmeldungen (je 128 MiB Argon2)
	BalanceAnteil int // Vorschlag für das Gewicht in der Lastverteilung
}

// modell liest die Gerätebezeichnung. /proc/device-tree/model ist bei den Pis
// der verlässlichste Weg; /proc/cpuinfo ist der Rückfall.
func modell() string {
	if b, err := os.ReadFile("/proc/device-tree/model"); err == nil {
		return strings.TrimRight(strings.TrimSpace(string(b)), "\x00")
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, z := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(z, "Model") {
				if _, nach, ok := strings.Cut(z, ":"); ok {
					return strings.TrimSpace(nach)
				}
			}
		}
	}
	return "unbekannt"
}

// ramMiB liest den gesamten Arbeitsspeicher.
func ramMiB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, z := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(z, "MemTotal:") {
			f := strings.Fields(z)
			if len(f) >= 2 {
				if kb, e := strconv.Atoi(f[1]); e == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// Erkenne ermittelt das Profil.
func Erkenne() Profil {
	p := Profil{Modell: modell(), RAMMiB: ramMiB(), Kerne: runtime.NumCPU()}
	if p.RAMMiB <= 0 {
		p.RAMMiB = 1024 // unbekannt: vorsichtig annehmen
	}
	// MemoryHigh=45% aus der Dienstdatei – damit rechnen wir hier ebenfalls,
	// sonst würden die Grenzen etwas erlauben, was der Dienst nicht darf.
	p.NodeMiB = p.RAMMiB * 45 / 100

	// Grundbedarf des Nodes im Leerlauf, großzügig angesetzt.
	const grund = 180
	frei := p.NodeMiB - grund
	if frei < 0 {
		frei = 0
	}

	switch {
	case p.RAMMiB < 1200: // Pi 3 / Zero 2 / Pi 4 mit 1 GB
		p.Klasse = "klein"
		// 12 MP deckt jedes Handyfoto ab und braucht beim Entschlüsseln
		// rund 48 MiB – auf 768 MB das Höchste, was vertretbar ist.
		p.DecodePixel, p.ThumbGleich, p.ECDHGleich = 12_000_000, 1, 2
	case p.RAMMiB < 3000: // 2 GB
		p.Klasse = "klein"
		p.DecodePixel, p.ThumbGleich, p.ECDHGleich = 24_000_000, 2, 2
	case p.RAMMiB < 6000: // 4 GB (bisheriger Stand)
		p.Klasse = "mittel"
		p.DecodePixel, p.ThumbGleich, p.ECDHGleich = 64_000_000, 3, 2
	default: // 8 GB und mehr
		p.Klasse = "groß"
		p.DecodePixel, p.ThumbGleich, p.ECDHGleich = 120_000_000, 6, 8
	}

	// Anmeldungen: je 128 MiB Argon2. Der Speicher setzt die Obergrenze, die
	// Kerne die sinnvolle – Argon2 nutzt vier Stränge, mehr gleichzeitige
	// Vorgänge als Kerne bringen nichts außer Wartezeit.
	p.LoginGleich = frei / 128
	if p.LoginGleich < 1 {
		p.LoginGleich = 1
	}
	if max := p.Kerne; p.LoginGleich > max {
		p.LoginGleich = max
	}

	// Gewicht für die Lastverteilung: Die Anmeldung ist der Engpass, also
	// richtet sich das Gewicht danach. Ein Pi 5 bekommt so etwa das Vierfache
	// eines Pi 3 – ohne das würde ein gleichmäßiger Verteiler die kleinen
	// Geräte überlasten, während das große sich langweilt.
	p.BalanceAnteil = p.LoginGleich
	if p.Klasse == "groß" {
		p.BalanceAnteil = p.LoginGleich * 2 // mehr Bandbreite, schnellere Kerne
	}
	if p.BalanceAnteil < 1 {
		p.BalanceAnteil = 1
	}
	return p
}

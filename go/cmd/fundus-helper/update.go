package main

// Update-Ausführung im privilegierten Helper. Der Node reicht ein signiertes
// Manifest über den Socket herein; der Helper prüft die Signatur SELBST (über
// update.ApplyManifest → Manifest.Verify) und führt die privilegierten Schritte
// mit Root-Rechten aus: ZIP nach /opt/fundus entpacken und den Node neu starten
// — beides kann der gehärtete Node (ProtectSystem=strict, NoNewPrivileges) nicht.
//
// Sicherheitsmodell: Die Signaturprüfung im Helper ist das eigentliche Tor. Ein
// kompromittierter Node kann kein unsigniertes Update erzwingen, weil der Helper
// nur ein gegen die kanonische Autorität gültig signiertes Manifest akzeptiert.

import (
	"encoding/json"
	"time"

	"github.com/fundus/node/internal/helperproto"
	"github.com/fundus/node/internal/update"
)

// installDir ist das feste Zielverzeichnis der Fundus-Installation.
const installDir = "/opt/fundus"

// handleApplyUpdate nimmt ein signiertes Manifest entgegen und wendet das Update
// als root an (Signaturprüfung inklusive).
func handleApplyUpdate(manifestJSON string) helperproto.Response {
	if manifestJSON == "" {
		return helperproto.Response{OK: false, Error: "kein Manifest übergeben"}
	}
	var m update.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &m); err != nil {
		return helperproto.Response{OK: false, Error: "Manifest-JSON ungültig: " + err.Error()}
	}

	logf("Update-Auftrag empfangen: Version %s", m.Version)

	// update.ApplyManifest prüft die Signatur SELBST (Tor), lädt das ZIP, prüft
	// den Argon2id-Hash, entpackt nach /opt/fundus und startet den Node neu.
	// Der Helper ist root → kein sudo nötig, Schreibrechte auf /opt/fundus da.
	err := update.ApplyManifest(&m, update.ApplyOptions{
		InstallDir: installDir,
		RestartCmd: "systemctl restart fundus-node",
		// CurrentVer leer: der Node hat den Neuer-Check schon gemacht, bevor er
		// den Auftrag schickt. Der Helper prüft primär die Signatur.
	}, logf)
	if err != nil {
		logf("Update fehlgeschlagen: %v", err)
		return helperproto.Response{OK: false, Error: err.Error()}
	}
	logf("Update erfolgreich angewendet: Version %s", m.Version)
	return helperproto.Response{OK: true}
}

// handleRestartNode startet den fundus-node-Dienst neu. Der Restart wird leicht
// verzögert in einer Goroutine ausgeführt, damit die OK-Antwort noch über den
// Socket zum Node gelangt, bevor dieser beendet wird.
func handleRestartNode() helperproto.Response {
	go func() {
		time.Sleep(500 * time.Millisecond)
		logf("Node-Neustart angefordert → systemctl restart fundus-node")
		if out, err := runCmd(30*time.Second, "/bin/systemctl", "restart", "fundus-node"); err != nil {
			logf("Node-Neustart fehlgeschlagen: %v (%s)", err, out)
		}
	}()
	return helperproto.Response{OK: true}
}

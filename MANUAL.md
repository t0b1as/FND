# FUNDUS – Schnellstart für neue Nutzer

Fundus ist ein dezentraler Marktplatz auf eigener Hardware: Jeder Raspberry Pi ist ein vollwertiger Node. Die Nodes finden sich selbst, tauschen Anzeigen, Nachrichten und Dateien direkt untereinander aus und führen gemeinsam die FND-Blockchain. Es gibt keinen zentralen Server.

## 1. Voraussetzungen

- Raspberry Pi 3/4/5 mit **Raspberry Pi OS 64-Bit** (Bullseye oder neuer), per LAN oder WLAN im Netz
- Windows-PC mit **PowerShell 7+**, **OpenSSH** (bei Windows 10/11 dabei) und **Go 1.24+** – der Pi ist zu schwach zum Kompilieren, gebaut wird auf dem PC
- Die Release-Dateien `FND.zip` und `FND admin.zip` in einem Ordner, darin `deploy-fundus.ps1`

## 2. Pi vorbereiten

Im **Raspberry Pi Imager** vor dem Schreiben der SD-Karte unter *Einstellungen*: Benutzername und Passwort festlegen, **SSH aktivieren**, WLAN eintragen. Dann booten und die IP-Adresse im Router nachsehen.

## 3. SSH-Schlüssel einrichten (einmalig, PowerShell)

```powershell
ssh-keygen -t ed25519                     # Enter übernimmt den Standardpfad
type $env:USERPROFILE\.ssh\id_ed25519.pub | ssh pi@192.168.1.50 "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
ssh pi@192.168.1.50                       # muss jetzt ohne Passwort gehen
```

`pi` und `192.168.1.50` durch deinen Benutzer und die IP des Pi ersetzen. Das Deploy-Skript findet den Schlüssel automatisch (mehrere Schlüssel → kurze Auswahl, keiner → Passwortabfrage, erzwingen mit `-PiKey`).

## 4. Installieren und aktualisieren

**Erstinstallation** – im Ordner mit den ZIPs:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy-fundus.ps1 -PiHost 192.168.1.50 -PiUser pi -AdminPass "LangesAdminPasswort"
```

**Update** auf eine neue Version (Systempakete überspringen, Binary neu bauen):

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy-fundus.ps1 -PiHost 192.168.1.50 -PiUser pi -Rebuild -SkipSetup
```

**Spätere Updates** gehen auch ohne PC: Der Node prüft GitHub selbst und bietet neue Versionen unter *Einstellungen → Software-Update* an – ein Klick installiert sie, Daten und Wallets bleiben erhalten.

Mehrere Pis auf einmal: `deploy-all-pis.ps1`. Für 32-Bit-Pi-OS `-Arch arm` anhängen (prüfen mit `uname -m`).

## 5. Erster Aufruf

1. Im Browser **`https://<IP-des-Pi>`** öffnen und das selbstsignierte Zertifikat einmal akzeptieren. Nur über **https** funktionieren Anrufe, Kamera und Mikrofon.
2. Oben rechts **Anmelden** mit E-Mail und Passwort. Daraus entsteht deine Wallet – kein Konto, kein Server. **Das Passwort ist der Schlüssel zu deinem Guthaben:** lang und einzigartig wählen, es gibt keinen Reset.
3. **Admin-Bereich** (Dateiablage, Einstellungen, Node-Wallet): Benutzer `admin` und das `-AdminPass` aus Schritt 4.
4. Unter *Einstellungen* den **Standort** des Nodes setzen – sonst zählen Entfernungen im Markt und bei Fundus Love nicht.

## 6. Die Hauptfunktionen

| Bereich | Was es kann |
|---|---|
| 🛒 **Marktplatz** | Anzeigen mit Bildern und Videos einstellen; Suche über alle Nodes mit Filter nach Text, Kategorie, Zustand, Preis und Umkreis. Kauf über **Escrow**: das Geld liegt sicher, bis die Ware bestätigt ist. |
| 📈 **Orderbuch & Swap** | FND gegen SOL handeln. Orders werden im Netz verteilt, der Tausch läuft **atomar** über beide Blockchains (HTLC) – manuell per Klick oder automatisch per Matching. Eigene Orders und laufende Swaps überstehen Neustarts. |
| 💬 **Messenger** | Ende-zu-Ende-verschlüsselte Nachrichten, Bilder und Dateien, Offline-Zustellung per Mailbox, **Audio- und Videoanrufe** (WebRTC). |
| 💗 **Fundus Love** | Partnerbörse mit Profil, Galerie, Hobbys und Werten, automatischem **Matching** und Suche nach Alter, Umkreis und Interessen. Kontakt direkt über den Messenger. |
| 🗄️ **Dateiablage** | Dateien verteilt und redundant auf mehreren Nodes speichern; wer Speicher bereitstellt, erhält **FND-Belohnungen**. |
| 👛 **Wallet & Chain** | FND senden und empfangen, Guthaben und Verlauf, **Staking** für Validatoren; der Node produziert gemeinsam mit den anderen die Blöcke. |
| 🌐 **Fernzugriff** | Auf der Peers-Seite einen anderen Node per 🌐 öffnen und ihn wie den eigenen bedienen – alles läuft verschlüsselt durch das P2P-Netz. |
| 🏠 **Heim-Node** | An einem fremden Node angemeldet? Partnerprofil und Nachrichtenverlauf kommen automatisch von deinem Heim-Node. |
| 🧪 **Beta** | Jobs, Energie-Token und Zertifikate. |

## 7. Außerhalb des Heimnetzes

Nodes im gleichen LAN finden sich automatisch. Für Verbindungen über das Internet am Router **Port 4001 TCP und UDP** auf einen Pi weiterleiten (oder UPnP erlauben) und bei entfernten Nodes in `/opt/fundus/fundus.env` eintragen:
`FUNDUS_BOOTSTRAP_PEERS=/ip4/<öffentliche-IP>/tcp/4001/p2p/<Peer-ID>`. Anrufe zwischen zwei Mobilfunknetzen brauchen zusätzlich einen TURN-Server (`FUNDUS_TURN_URLS`).

## 8. Wenn etwas hakt

```bash
sudo systemctl status fundus-node                 # läuft der Node?
sudo journalctl -u fundus-node -n 50              # letzte Log-Zeilen
curl -s http://127.0.0.1:3000/health              # Revision und Zustand
curl -s http://127.0.0.1:3000/api/v1/nat          # von außen erreichbar?
```

Die Revision steht auch oben neben dem Logo. Weitere Details: `README.md`, `WALLET-SETUP.md`, `fundus.env` (alle Einstellungen kommentiert).

# Gebühren-Wallet neu erstellen und einbauen

## Warum neu?

Durch das Argon2id-Upgrade (64 MiB → 256 MiB) und den Salt-Wechsel
(`"fundus-fnd-v1"` → `"fundus-fnd-v2"`) erzeugt `fnd-wallet` jetzt
**andere Adressen** als zuvor. Die alte Adresse
`0xe2E39471fac9f148fE4d5d863eDE4C9c1da2bB01` wurde mit v1-Parametern
erzeugt und ist **nicht mehr kompatibel**.

> **WICHTIG (Phase 2.4, BLAKE3-Angleichung):** Die Adress-Ableitung wurde von
> Keccak (Ethereum-Stil, letzte 20 Bytes) auf **BLAKE3** (erste 20 Bytes,
> identisch zur Chain) umgestellt. Dadurch erzeugt `fnd-wallet` aus derselben
> Seed erneut eine **andere Adresse**. Frühere Beispieladressen
> (`0x7c4B93…` aus der Keccak-Zeit, `0xd6dfC75A…` aus der 700-MiB-Zeit) sind
> **veraltet**. Die aktuell gültige, via `fnd-wallet` (BLAKE3, 256 MiB)
> abgeleitete Adresse ist `0xea5594a7cc26d2456e9a033481d01a5ba101c8f8`; beim
> Neuaufsetzen stets die tatsächlich von `fnd-wallet` ausgegebene Adresse
> verwenden und `KanonischeFeeCollector` in `config/validate.go` entsprechend
> setzen. Vorteil: Wallet- und Chain-Adresse sind ab jetzt identisch
> (Voraussetzung für die Wallet→Chain-Anbindung, Phase 2.5).

---

## Schritt 1 — fnd-wallet bauen & ausführen

**Auf Windows (dein PC):** Das Admin-ZIP enthält `build-admin-tools.ps1`, das
`fnd-wallet` und `fundus-admin` lokal baut (NICHT über deploy-fundus.ps1 — das
ist nur für den Node auf dem Pi):

```powershell
powershell -ExecutionPolicy Bypass -File .\build-admin-tools.ps1 -GoCmd go1.25.4
# Binaries landen in .\bin\
```

**Auf Linux/Pi:**
```bash
cd go
export PATH=$PATH:/usr/local/go/bin
go build -o fnd-wallet ./cmd/fnd-wallet
```

### Mehrere Wallets auf einmal (Batch)

Für z.B. 13 Freundes-Wallets — eine Zeile pro Wallet (`<adresse>\t<30 wörter>`):

```bash
./fnd-wallet --generate 13 --out freunde.txt    # Linux/Pi
.\bin\fnd-wallet.exe --generate 13 --out freunde.txt   # Windows
```

Die Datei wird mit `chmod 600` geschrieben. **Sie enthält die Seeds im Klartext
— sicher verwahren, nach dem Verteilen löschen, niemals ins Git.** Anschließend
die Adressen über die Wallet-Oberfläche vom Fee-Collector bebuchen.

### Einzelne Wallet / Adresse prüfen

```bash
# Interaktiv (erzeugt neue Wörter und zeigt die Adresse):
./fnd-wallet

# Aus vorhandenem Seed (nur Adresse, kein Private Key):
./fnd-wallet --seed-file seed.txt --verify
```

Ausgabe (Beispiel):
```
╔══════════════════════════════════════════════════════════════╗
║  Fundus FND Fee Collector Wallet                             ║
║  Gnosis Chain (Chain-ID: 100) · Argon2id v2 (256 MiB)       ║
╠══════════════════════════════════════════════════════════════╣
║  Adresse:    0xea5594a7cc26d2456e9a033481d01a5ba101c8f8                                ║
║  Kurzform:   0x<ABCD>…<EF12>                                 ║
╠══════════════════════════════════════════════════════════════╣
║  Priv Key:   0x<PRIVATE_KEY>                                 ║
╠══════════════════════════════════════════════════════════════╣
║  ⚠  PRIVATE KEY NUR EINMALIG ANZEIGEN – SOFORT SICHERN!     ║
╚══════════════════════════════════════════════════════════════╝

# In /etc/fundus/fundus.env eintragen:
FUNDUS_FND_FEE_COLLECTOR=0xea5594a7cc26d2456e9a033481d01a5ba101c8f8
FUNDUS_WALLET_PRIV_KEY=0x<PRIVATE_KEY>
```

**Jetzt sofort:**
1. 30 Seed-Wörter auf Papier schreiben (offline, zwei Kopien)
2. Private Key notieren (für Contract-Deployment benötigt)
3. Adresse notieren → `0xea5594a7cc26d2456e9a033481d01a5ba101c8f8` für alle nachfolgenden Schritte

---

## Schritt 2 — Adresse in die Dateien einbauen

Die neue Adresse muss an **4 Stellen** eingetragen werden:

### A) `fundus.env`

```bash
FUNDUS_FND_FEE_COLLECTOR=0xea5594a7cc26d2456e9a033481d01a5ba101c8f8
```

→ Datei: `/mnt/user-data/outputs/fundus.env` Zeile 126

---

### B) `contracts/.env` (für Hardhat-Deployment)

```bash
FEE_COLLECTOR=0xea5594a7cc26d2456e9a033481d01a5ba101c8f8
DEPLOYER_PRIVATE_KEY=0x<PRIVATE_KEY>
```

→ Datei neu anlegen: `/mnt/user-data/outputs/contracts/.env`
→ **Nie in Git committen** (steht im `.gitignore`)

---

### C) `contracts/scripts/deploy.js`

Die Adresse wird aus der `.env`-Datei gelesen — **keine Änderung nötig**,
solange `.env` korrekt befüllt ist.

Verifikation:
```bash
cd contracts
grep "FEE_COLLECTOR" scripts/deploy.js
# Sollte ausgeben: const feeCollector = process.env.FEE_COLLECTOR;
```

---

### D) Contract-Deployment auf Gnosis Chain (Chiado Testnet zuerst)

```bash
cd /mnt/user-data/outputs/contracts
npm install

# Testnet (Chiado) zuerst:
npx hardhat run scripts/deploy.js --network chiado

# Mainnet (Gnosis Chain) erst nach Testnet-Prüfung:
# npx hardhat run scripts/deploy.js --network gnosis
```

Das Deployment-Skript gibt die Contract-Adressen aus und schreibt sie in
`deployed-addresses.json`. Diese dann in die Go-Config übernehmen
(`FUNDUS_CONTRACT_FND`, `FUNDUS_CONTRACT_MARKET` etc.).

---

## Schritt 3 — Deploy-Skript ausführen

Nach Einbau der Adresse in `fundus.env`:

```powershell
# Windows PowerShell:
.\deploy-fundus.ps1 -ZipPath .\FND_R001.zip -AdminZip .\FND_admin_R001.zip

# → Pi 1 IP eingeben, dann Pi 2 IP
# → Pi 2 bekommt automatisch Pi 1 als Bootstrap
# → Service wird nach jeder Installation gestartet
```

---

## Checkliste

- [ ] 30 Seed-Wörter offline notiert (zwei Kopien, zwei Orte)
- [ ] Private Key sicher verwahrt (nicht in Cloud)
- [ ] `fundus.env`: `FUNDUS_FND_FEE_COLLECTOR=0xea5594a7cc26d2456e9a033481d01a5ba101c8f8`
- [ ] `contracts/.env`: `FEE_COLLECTOR` + `DEPLOYER_PRIVATE_KEY`
- [ ] Chiado Testnet deployment erfolgreich
- [ ] Contract-Adressen in Go-Config eingetragen
- [ ] Gnosis Mainnet deployment
- [ ] `deploy-fundus.ps1` für beide Pis ausgeführt

---

## Zwei Ableitungen, zwei Arten von Adressen (Stand R456)

**Seed-Wörter → Wallet-Adresse gibt es in zwei Varianten** (gleiches Salt, gleiche
Normalisierung, gleiche BLAKE3-Adresse – nur die Argon2-Kosten unterscheiden sich,
und damit die Adresse):

| Ableitung | Argon2id | Wofür |
|---|---|---|
| **Nutzer-Wallet** (Standard) | 256 MiB, t=4 | Wallet öffnen, Überweisung, Shop, Escrow, Admin-Wallet, Fee-Collector `0xea5594a7…` – identisch zu `fnd-wallet` |
| **Node-Wallet / alt** | 128 MiB, t=2 | `node.seed` (Speicher-Einnahmen) und Nutzer-Adressen vor R456 |

Der **Login** leitet die Wallet nicht mehr ab (wäre ~10 s auf dem Pi). Stattdessen:
Menü oben rechts → **„Wallet öffnen"** → **„Als Login-Wallet hinterlegen"**. Die
Wallet (eigene oder eine andere, z.B. die Fee-Collector-Wallet über ihre
Seed-Wörter) steht dann bei jedem Login sofort bereit – verschlüsselt mit einem
Schlüssel, den nur diese Login-Identität erzeugen kann, und nur auf diesem Node.
Liegt auf der alten Adresse (vor R456) noch Guthaben, bietet „Wallet öffnen" den
**Umzug** an. **Kein Chain-Reset nötig.**

**Fundus-ID ≠ Wallet-Adresse.** Die Fundus-ID (Messenger, Kontakte, Partnerbörse)
entsteht aus einem Ed25519-Schlüssel, die Wallet-Adresse (FND) aus einem
secp256k1-Schlüssel. Beide sehen aus wie `0x…` mit 40 Zeichen. FND an eine
Fundus-ID wären verloren – der Node übersetzt eine eingegebene Fundus-ID
automatisch in die Wallet-Adresse derselben Person oder lehnt ab.

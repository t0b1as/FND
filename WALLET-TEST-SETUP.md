# FND Wallet — Testaufbau (Hardhat lokal)

Diese Anleitung setzt eine **lokale Test-Chain** (Hardhat, Chain-ID 31337) auf,
deployt die Contracts, mintet Test-FND und verbindet den Node. Damit kannst du
Wallet-Ableitung, Überweisung und Mint testen — ohne echtes Geld oder Testnet.

## Der einfachste Weg, FND zu besorgen

Drei Wege, vom einfachsten zum flexibelsten:

**A) Beim Deploy alles in die eigene Wallet (am einfachsten):**
Setze `FND_FEE_COLLECTOR` = deine eigene Wallet-Adresse. `mintInitial` mintet
dann das gesamte Supply (1 Billion FND) direkt zu dir — kein weiterer Schritt,
du kannst sofort überweisen.

**B) Jederzeit nachminten mit dem Mint-Skript (der direkte Weg):**
```
cd contracts
FND_ADDRESS=0x<FND-Contract> TO=0x<Empfänger> AMOUNT=10000 \
  npx hardhat run scripts/mint.js --network localhost
```
Nutzt den Deployer (Owner), mintet beliebig FND an eine Adresse. Kein
Node-Key-Minter-Setup nötig.

**C) Über die Wallet-Seite (`/wallet/mint`):** nur wenn der Node-Key als Minter
gesetzt ist (`FND_NODE_MINTER`, siehe unten). Praktisch, wenn du im Browser
bleiben willst.

Für einen reinen Transfertest reicht **A**. Für wiederholtes Testen ist **B** am
bequemsten.

---

## Überblick

```
Browser (Wallet-Seite)
   │  Seed (30 Wörter | Email+PW)  → nur an EIGENEN Node
   ▼
Go-Node  /api/v1/wallet/{derive,balance,transfer,mint}
   │  leitet Key ab, signiert EINMALIG, verwirft Key
   ▼
Hardhat-Node (127.0.0.1:8545, Chain 31337)  ← FND-Contract
```

## 1. Hardhat-Node starten (Terminal A)

Auf dem Rechner, der die Contracts hostet (kann ein Pi oder dein PC sein —
Hauptsache der Node erreicht `http://<host>:8545`):

```
cd contracts
npm install            # einmalig
npx hardhat node       # startet lokale Chain auf :8545, läuft im Vordergrund
```

Das gibt 20 vorfinanzierte Test-Accounts mit je 10000 ETH aus. **Account #0**
ist der Deployer/Owner. Kopiere dir dessen Adresse und Private Key.

## 2. Adressen vorbereiten

Du brauchst:

- **FND_FEE_COLLECTOR** — bekommt das initiale Supply. Für den Test am
  einfachsten: die Adresse von Hardhat-Account #0 (Deployer). Dann kann der
  Deployer direkt Test-FND verteilen.
- **FND_NODE_MINTER** (optional) — die Wallet-Adresse deines Node-Keys
  (`FUNDUS_WALLET_PRIVKEY` in fundus.env). Wird als FND-Minter gesetzt, damit
  `/wallet/mint` funktioniert.
- **FND_TEST_WALLET** (optional) — eine Adresse, die sofort Test-FND erhält.
  Z.B. die Adresse, die du dir mit „Wallet anzeigen" aus deinen 30 Wörtern
  ableitest.

## 3. Contracts deployen + Test-FND verteilen (Terminal B)

```
cd contracts

# Pflicht: Fee-Collector. Für den Test = Deployer-Adresse (Account #0).
export FND_FEE_COLLECTOR=0x<Hardhat-Account-0-Adresse>

# Optional: Node-Key als Minter (für /wallet/mint)
export FND_NODE_MINTER=0x<Wallet-Adresse-des-Node-Keys>

# Optional: Test-Wallet bekommt sofort 10000 FND
export FND_TEST_WALLET=0x<deine-abgeleitete-Wallet-Adresse>
export FND_TEST_AMOUNT=10000

npx hardhat run scripts/deploy.js --network localhost
```

Am Ende gibt das Skript die Contract-Adressen aus. Wichtig ist
`FUNDUS_FND_ADDRESS`.

## 4. fundus.env anpassen (auf dem Node)

```
FUNDUS_CHAIN_ID=31337
FUNDUS_CHAIN_RPC_URL=http://127.0.0.1:8545
FUNDUS_FND_ADDRESS=0x<die-aus-Schritt-3-ausgegebene-FND-Adresse>
FUNDUS_FND_FEE_COLLECTOR=0x<dieselbe-wie-FND_FEE_COLLECTOR>

# Der Node-Key, mit dem der Node selbst Transaktionen signiert (z.B. Mint).
# Für den Test: Private Key von Hardhat-Account #0, ODER dein eigener Node-Key,
# den du als FND_NODE_MINTER gesetzt hast.
FUNDUS_WALLET_PRIVKEY=<private-key-ohne-0x>
```

Läuft der Hardhat-Node auf einem anderen Rechner als der Fundus-Node, ersetze
`127.0.0.1` durch dessen IP und starte `hardhat node` mit `--hostname 0.0.0.0`.

## 5. Node neu deployen/starten

```
powershell -File .\deploy-fundus.ps1 -PiHost <IP> -PiUser tobias -Rebuild -GoCmd go1.25.4 -SudoPass "..." -CertPass "..."
```

Im Log sollte erscheinen: `FND wallet geladen` und (wenn Chain erreichbar)
`fee-collector on-chain verifiziert`.

## 6. Testen (Wallet-Seite im Browser)

1. **Wallet anzeigen** — Tab „30 Wörter" (eigene eingeben) oder „E-Mail +
   Passwort". Bei Email+PW kannst du „Neue 30 Wörter generieren" anhaken, um die
   Wörter zum Sichern angezeigt zu bekommen. → zeigt deine Wallet-Adresse.
2. **Balance** — die FND-Balance der Adresse (sollte deine Test-FND zeigen, wenn
   du FND_TEST_WALLET gesetzt hast).
3. **Überweisen** — Empfänger-Adresse + Betrag. Der Seed (oben eingegeben) wird
   zum Signieren genutzt. → gibt einen TX-Hash zurück.
4. **Minten** (nur Test) — funktioniert, wenn der Node-Key als Minter gesetzt
   ist (FND_NODE_MINTER). Mintet FND an eine beliebige Adresse.

## Hinweise zur Sicherheit (Testaufbau)

- Der Seed wird über die (HTTPS-)Verbindung an den **eigenen** Node gesendet,
  dort einmalig zum Ableiten des Keys genutzt und der Key sofort genullt.
  Seed-tragende Requests sind kurzlebig; der Node speichert den Seed nicht.
- Das ist KEIN Zero-Knowledge — der Node sieht den Seed/Key kurz im Klartext
  (zum Signieren nötig). Das Modell ist „Key auf den eigenen, vertrauten Node
  laden". Für echtes clientseitiges Signing wäre eine Browser-Krypto-Lib
  (z.B. ethers.js) nötig.
- Auf Mainnet (Chain 100) NIEMALS einen wertvollen Seed über diesen Weg
  schicken, wenn der Node nicht vollständig unter eigener Kontrolle ist.

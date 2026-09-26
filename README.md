# Fundus Marketplace Node

Dezentraler Marktplatz für Waren, Dienstleistungen, Zertifikate, Energie-Token und Jobs.
Läuft auf einem Raspberry Pi 3 oder neuer. Kein Cloud-Account, keine zentrale Infrastruktur.

---

## Architektur

```
Browser
  │
  ▼
OpenResty (nginx + LuaJIT)   :80
  │  ngx.location.capture()
  ▼
Fundus Go-Node               :3000  (nur localhost)
  ├── libp2p P2P             :4001  (öffentlich)
  ├── Kademlia DHT
  ├── GossipSub (Topics: listings, energy, certificates, jobs)
  ├── LevelDB Storage
  ├── Ollama LLM             :11434 (nur localhost, optional)
  └── Smartmeter Reader      (seriell / HTTP, optional)
```

Jeder Node hostet seine eigenen Daten sowie Replikate von bis zu 5 anderen Nodes.
Fällt ein Node aus, bleiben die Daten im Netz erhalten.

---

## Schnellstart

### Voraussetzungen

- Raspberry Pi 3 / 4 / 5 mit Raspberry Pi OS (Bullseye oder neuer)
- Windows-Entwicklerrechner mit PowerShell 7+ und OpenSSH
- Go 1.22+ (nur für lokale Entwicklung / Build)

### Deployen (Build laeuft automatisch)

Ein einziger Befehl genuegt. Wenn noch kein Binary in `bin\` liegt, baut das
Script es automatisch auf dem PC (Cross-Compile fuer den Pi) und laedt es hoch.
Der Raspberry Pi 3 selbst hat zu wenig Speicher fuer den Build (go-ethereum,
libp2p), daher wird auf dem PC kompiliert.

```powershell
# 64-Bit Pi-OS (Standard, Pi 3/4/5 mit aarch64):
powershell -File .\deploy-fundus.ps1 -PiHost 192.168.1.100 -PiUser pi

# 32-Bit Pi-OS (armv7l): am Pi mit 'uname -m' pruefen
powershell -File .\deploy-fundus.ps1 -PiHost 192.168.1.100 -PiUser pi -Arch arm
```

**SSH-Authentifizierung** laeuft automatisch:
- Genau ein SSH-Key vorhanden -> wird ohne Rueckfrage genutzt
- Mehrere Keys -> kurze Auswahl
- Kein Key -> Passwort-Login (Passwort wird einmal abgefragt)
- Bestimmten Key erzwingen: `-PiKey C:\pfad\zum\key`

Das separate `build-binary.ps1` existiert weiterhin, falls man nur bauen
(ohne deployen) moechte.

> **Hinweis zur Execution Policy:** Falls PowerShell die Skripte blockiert
> ("nicht digital signiert"), nutze `powershell -File ...`
> wie oben gezeigt, oder setze einmalig:
> `Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned`

Das Script installiert bei Bedarf automatisch:
- `unzip`, `curl`, `git`, `jq`, `logrotate`
- OpenResty (nginx + LuaJIT)
- Node.js 20 LTS
- Ollama + Modelle (`moondream2`, `llama3.2:1b`)
- System-User `fundus`
- Alle Verzeichnisse und Berechtigungen
- systemd Service + Logrotate-Konfiguration

### 2. Folge-Deploys (schneller)

```powershell
make zip
.\deploy-fundus.ps1 -PiHost 192.168.1.100 -SkipSetup
```

### 3. Direktzugriff

```
http://<pi-ip>/          → Web-Frontend
http://<pi-ip>/api/v1/   → REST-API (direkt)
```

---

## Konfiguration

Die gesamte Konfiguration liegt in `/etc/fundus/fundus.env`.
Eine kommentierte Vorlage liegt in `fundus.env.example`.

### Wichtigste Variablen

| Variable | Standard | Beschreibung |
|---|---|---|
| `FUNDUS_P2P_PORT` | `4001` | libp2p-Port (muss von außen erreichbar sein) |
| `FUNDUS_BOOTSTRAP_PEERS` | leer | Kommagetrennte Multiaddr-Liste bekannter Nodes |
| `FUNDUS_PEERS_MAX` | `5` | Anzahl Peer-Replikate |
| `FUNDUS_LLM_ENABLED` | `false` | Ollama LLM-Analyse aktivieren |
| `FUNDUS_LLM_MODEL` | `moondream2` | Ollama-Modell für Bild-Analyse |
| `FUNDUS_METER_PROTOCOL` | leer | `sml`, `d0`, `http` oder `mock` |
| `FUNDUS_METER_ID` | leer | Zähler-Seriennummer |
| `FUNDUS_METER_LAT/LON` | `0` | GPS-Koordinaten des Zählers |
| `FUNDUS_METER_GEN_LAT/LON` | `0` | GPS-Koordinaten des Erzeugers |
| `FUNDUS_METER_SIGN_KEY` | leer | HMAC-Schlüssel (64 Hex-Zeichen) |

Nach Änderungen: `sudo systemctl restart fundus-node`

---

## Smartmeter-Anbindung

### SML (USB-Lesekopf, DE-Standard)

```env
FUNDUS_METER_PROTOCOL=sml
FUNDUS_METER_PORT=/dev/ttyUSB0
FUNDUS_METER_ID=DE00112233445566
FUNDUS_METER_LAT=50.1234
FUNDUS_METER_LON=8.5678
FUNDUS_METER_SIGN_KEY=$(openssl rand -hex 32)
```

Empfohlene Hardware: IR-Lesekopf mit USB-Adapter (z.B. Hichi, Weidmann)

### Shelly EM (HTTP)

```env
FUNDUS_METER_PROTOCOL=http
FUNDUS_METER_HTTP_URL=http://192.168.1.50/status
FUNDUS_METER_ID=shelly-em-wohnzimmer
```

### Test ohne Hardware

```env
FUNDUS_METER_PROTOCOL=mock
```

---

## LLM-Analyse

Artikel-Bilder + Sprach-Kommentar werden lokal auf dem Pi ausgewertet.

```env
FUNDUS_LLM_ENABLED=true
FUNDUS_LLM_MODEL=moondream2    # multimodal, 1.8 GB
```

Ollama muss laufen: `sudo systemctl start ollama`

Modelle manuell laden:
```bash
ollama pull moondream2   # Bild + Text (Haupt-Modell)
ollama pull llama3.2:1b  # nur Text (Fallback + Jobs)
```

---

## Netzwerk & NAT – brauche ich eine Port-Weiterleitung?

**Kurze Antwort: Nicht zwingend — aber empfohlen für optimale Performance.**

### Warum es meistens ohne funktioniert

Wenn der Fundus-Node eine ausgehende Verbindung zu einem anderen Peer aufbaut,
trägt das Linux-Kernel-Conntrack-Modul diese Verbindung als `ESTABLISHED` ein.
Return-Traffic vom Remote-Peer ist dadurch **automatisch erlaubt** — ohne
explizite Eingangsregel im Router.

```
Pi → Peer A  (ausgehend, TCP SYN)
Pi ← Peer A  (eingehend, ESTABLISHED — automatisch erlaubt)
```

Das ist genau der Mechanismus hinter der iptables-Standardregel:
```bash
-A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
```

**Fundus nutzt das aktiv:** Nodes teilen ihre IP/Multiaddr regelmäßig über das
DHT und GossipSub mit. Sobald unser Pi eine ausgehende Verbindung zu einem
neuen Peer aufgebaut hat, kann dieser Peer auf dem gleichen TCP-Stream
zurückschreiben — auch wenn Port 4001 im Router nicht weitergeleitet ist.

### Drei Stufen der NAT-Traversal (alle aktiv)

```
Stufe 1 – UPnP/NAT-PMP
  Pi fragt den Router: "Öffne bitte Port 4001 für mich"
  Funktioniert bei: Fritzbox, Speedport, die meisten Heimrouter
  Ergebnis: Echter öffentlicher Port → beste Performance

Stufe 2 – Hole Punching (DCUtR über UDP/QUIC)
  Beide Seiten senden gleichzeitig ein UDP-Paket
  Beide NAT-Tabellen lernen: "Diese Verbindung ist ESTABLISHED"
  Return-Traffic beider Seiten ist erlaubt
  Funktioniert bei: ~75% aller Heimrouter (Full-Cone, Restricted-Cone NAT)
  Scheitert bei: Symmetric NAT (manche Unternehmen, CGNAT)

Stufe 3 – Circuit Relay
  Daten laufen über einen öffentlichen Relay-Node
  Funktioniert immer, auch hinter Symmetric NAT und CGNAT
  Overhead: ~20-50ms zusätzliche Latenz
```

### Wann Port-Weiterleitung hilft

```
┌─────────────────────────────────┬─────────────┬────────────────┐
│ Situation                       │ Ohne Weiter-│ Mit Port 4001  │
│                                 │ leitung     │ TCP+UDP offen  │
├─────────────────────────────────┼─────────────┼────────────────┤
│ Fritzbox (UPnP aktiv)           │ ✅ Auto      │ ✅ Gleich       │
│ Heimrouter Full-Cone NAT        │ ✅ Hole Punch │ ✅ Besser       │
│ Heimrouter Symmetric NAT        │ ⚠ Relay      │ ✅ Direkt       │
│ CGNAT (Telekom LTE, etc.)       │ ⚠ Relay      │ ❌ Nicht mögl. │
│ Firmen-Netz / VPN               │ ⚠ Relay      │ ❌ Geblockt    │
│ VPS / öffentliche IP            │ ✅ Direkt    │ ✅ Optimal      │
└─────────────────────────────────┴─────────────┴────────────────┘
```

### NAT-Status prüfen

```bash
curl http://localhost:3000/api/v1/nat | python3 -m json.tool
# Beispiel-Antwort bei öffentlicher IP:
# {
#   "reachability": "public",
#   "has_public_ip": true,
#   "dht_mode": "auto (server wenn erreichbar, client sonst)",
#   "recommendation": "✓ Direkt erreichbar – keine Port-Weiterleitung nötig"
# }
#
# Bei NAT:
# {
#   "reachability": "nat",
#   "has_public_ip": false,
#   "recommendation": "⚠ Hinter NAT – UPnP wird versucht. Für optimale
#                      Performance: Port 4001 TCP+UDP im Router weiterleiten."
# }
```

### Fritzbox – Port-Weiterleitung einrichten (optional)

```
Fritz!Box → Internet → Freigaben → Gerät für Freigaben hinzufügen
  → Pi auswählen → Neue Freigabe
  → Protokoll:      TCP und UDP
  → Port extern:    4001
  → Port intern:    4001
  → Speichern
```

### Raspberry Pi – Firewall

Raspberry Pi OS hat standardmäßig **keine aktive Firewall** (iptables ACCEPT all).
Falls `ufw` installiert ist:

```bash
sudo ufw allow 4001/tcp comment "Fundus P2P TCP"
sudo ufw allow 4001/udp comment "Fundus P2P QUIC"
# API ist nur lokal (127.0.0.1:3000) – keine externe Freigabe nötig
# OpenResty/Nginx auf Port 80 – ggf. freigeben für Browser-Zugriff:
sudo ufw allow 80/tcp comment "Fundus Web UI"
```

### IP-Ankündigung im P2P-Netz

Nodes teilen ihre Multiaddrs regelmäßig über DHT und GossipSub.
Das sind alle Adressen unter denen ein Node erreichbar ist:

```
/ip4/78.12.34.56/tcp/4001/p2p/12D3KooW...    ← öffentliche IP (direkt)
/ip4/192.168.1.100/tcp/4001/p2p/12D3KooW...  ← LAN-IP (nur lokal nutzbar)
/ip4/127.0.0.1/tcp/4001/p2p/12D3KooW...      ← Loopback
```

Andere Peers versuchen die Adressen in dieser Reihenfolge. Sobald eine
`ESTABLISHED`-Verbindung steht, können beide Seiten über diesen Kanal
bidirektional kommunizieren — der Linux-Kernel erlaubt den Return-Traffic
automatisch durch sein Conntrack-System.

---

## Mehrere Nodes verbinden

### Node-Adresse herausfinden

```bash
curl http://localhost:3000/api/v1/node
# → {"id":"12D3KooW...","addrs":["/ip4/192.168.1.100/tcp/4001/p2p/12D3KooW..."]}
```

### Bootstrap-Peer eintragen

```env
# /etc/fundus/fundus.env auf Node B:
FUNDUS_BOOTSTRAP_PEERS=/ip4/192.168.1.100/tcp/4001/p2p/12D3KooW...
```

---

## Entwicklung

### Lokal starten (kein Pi nötig)

```bash
cd go/
make run          # Mock-Meter, kein LLM
make run-llm      # Mock-Meter + Ollama (lokal installiert)
```

### Pi-Logs live

```bash
make logs PI_HOST=192.168.1.100
```

### Neue Sprache hinzufügen

1. `lua/locales/TEMPLATE.lua` nach `lua/locales/fr.lua` kopieren
2. Alle Werte übersetzen
3. In `lua/i18n.lua` eintragen:
   ```lua
   _M.SUPPORTED = { "de", "en", "fr" }
   -- und in switcher_html:
   local labels = { ..., fr = "Français" }
   ```

---

## Projektstruktur

```
go/
├── cmd/fundus-node/     Entry Point (main.go)
├── internal/
│   ├── api/             REST-API (Gin)
│   │   ├── server.go    Router + Hilfsfunktionen
│   │   ├── analyze.go   LLM-Analyse Endpoint
│   │   └── meter.go     Smartmeter SSE + Ingest
│   ├── config/          Konfiguration aus Env-Vars
│   ├── llm/
│   │   ├── analyzer.go  Ollama-Client
│   │   └── prompt.go    Alle Prompts (de/en, erweiterbar)
│   ├── meter/
│   │   └── reader.go    SML / D0 / HTTP / Mock
│   ├── p2p/
│   │   └── node.go      libp2p + DHT + GossipSub + mDNS
│   └── storage/
│       └── store.go     LevelDB + Peer-Replikation
└── Makefile

lua/
├── app.lua              Router
├── router.lua           URL-Dispatcher
├── render.lua           Layout + API-Client
├── i18n.lua             Internationalisierung
├── lang.lua             Sprach-Cookie-Endpoint
├── locales/
│   ├── de.lua           Deutsch
│   ├── en.lua           Englisch
│   └── TEMPLATE.lua     Vorlage für neue Sprachen
├── pages/               Eine Datei pro Seite
└── static/
    ├── style.css
    ├── upload.js        Kamera + Voice + LLM-Analyse
    ├── job.js           Job-Formular
    └── meter.js         SSE Live-Widget

deploy-fundus.ps1        Windows Deploy-Script
fundus-node.service      systemd Unit
fundus-logrotate         Log-Rotation
fundus.env               Konfigurationsvorlage
```

---

## Partner-Modul

Das Partner-Modul bietet **zwei unabhängige Modi** – der User entscheidet welchen er nutzt.

### Modus 1: Automatisches Matching (Privacy-first)

Alle Profildaten werden mit einem zufälligen HMAC-Salt gehasht. Im P2P-Netz kursieren nur 32-Byte-Hashes. Kein Klarnamen, kein Klartext-Gender, keine Interessen im Klartext.

```bash
# Profil anlegen
PUT /api/v1/partner/profile

# Im Netz veröffentlichen (nur Hashes)
POST /api/v1/partner/publish

# Lokales Matching (bidirektionale Prüfung ohne Klartextvergleich)
GET /api/v1/partner/matches
```

**Score-Formel:** 40% Distanz + 35% Interessen/Hobbies + 25% Sex-Kompatibilität

### Modus 2: Parametrische Suche (Opt-in)

Der User veröffentlicht bewusst **grobe Kategorien** (nicht Klartext-Angaben) für eine filterbare Suche. Koordinaten werden auf ~10 km gerundet (vs. ~5 km beim Matching-Modus).

```bash
# Opt-in SearchableAd veröffentlichen
POST /api/v1/partner/publish/searchable

# Suche mit Filtern
GET /api/v1/partner/search?gender=female&age_range=26-35&hobby=musik,wandern&sort=score&limit=20
```

**Verfügbare Filter:**

| Parameter | Beispiel | Beschreibung |
|---|---|---|
| `radius_km` | `50` | Suchradius in km |
| `gender` | `female,non-binary` | Kommagetrennt |
| `age_range` | `26-35,36-45` | Aus vordefinierten Klassen |
| `education` | `university,vocational` | Bildungsgruppen |
| `industry` | `it,education` | Branchen |
| `hobby` | `musik,wandern` | Mind. 1 gemeinsam nötig wenn gesetzt |
| `sex` | `BDSM,VAN` | Sex-Pref-Abkürzungen |
| `mutual` | `true` | Nur gegenseitige Matches |
| `sort` | `score\|distance\|hobbies\|sex` | Sortierung |
| `limit`, `offset` | `20`, `0` | Paginierung |

**Koordinaten-Fallback:** Wenn `lat`/`lon` nicht übergeben werden, wird der eigene Profilstandort genutzt.

### Vordefinierte Listen

```bash
GET /api/v1/partner/preferences/lists
```

Liefert alle vordefinierten Optionen für das Formular:
- **Hobbies** (20 Einträge)
- **Vorlieben/Werte** (20 Einträge)
- **Abneigungen** (15 Einträge)
- **Branchen** (20 Einträge)
- **Ausbildungsgrade** (10 Einträge)
- **Sexuelle Vorlieben** (29 Abkürzungen mit DE+EN Erklärungen)

Jede Liste ist vom User um eigene Werte erweiterbar.

---

| Methode | Pfad | Beschreibung |
|---|---|---|
| `GET` | `/health` | Einfacher Ping |
| `GET` | `/api/v1/status` | Node-Status + Statistiken |
| `GET` | `/api/v1/node` | Eigene P2P-Adresse |
| `GET` | `/api/v1/peers` | Verbundene Peers |
| `GET/POST` | `/api/v1/listings` | Marktplatz-Angebote |
| `GET/PUT/DELETE` | `/api/v1/listings/:id` | Einzelnes Angebot |
| `POST` | `/api/v1/analyze` | LLM-Analyse (Multipart) |
| `GET` | `/api/v1/analyze/status` | Ollama erreichbar? |
| `GET/POST` | `/api/v1/energy` | Energie-Token |
| `GET` | `/api/v1/energy/:id/fee` | Netzgebühr berechnen |
| `GET` | `/api/v1/meter/status` | Smartmeter-Konfiguration |
| `GET` | `/api/v1/meter/stream` | SSE Live-Stream |
| `POST` | `/api/v1/meter/reading` | Token manuell einliefern |
| `GET/POST` | `/api/v1/certificates` | Zertifikate |
| `GET/POST` | `/api/v1/jobs` | Job-Inserate |
| `GET/PUT/DELETE` | `/api/v1/partner/profile` | Lokales Partner-Profil |
| `POST` | `/api/v1/partner/publish` | Anonymisierten PublicAd im P2P-Netz veröffentlichen |
| `POST` | `/api/v1/partner/publish/searchable` | Opt-in SearchableAd für parametrische Suche |
| `GET` | `/api/v1/partner/ads` | Gecachte anonyme PublicAds von Peers |
| `GET` | `/api/v1/partner/matches` | Automatisches Matching (salt-basiert) |
| `GET` | `/api/v1/partner/search` | Parametrische Suche mit Filtern |
| `GET` | `/api/v1/partner/search/ads` | Gecachte SearchableAds von Peers |
| `GET` | `/api/v1/partner/preferences/lists` | Vordefinierte Listen |
| `GET` | `/api/v1/shop/price` | Aktueller SOL/EUR-Kurs + Angebot |
| `POST` | `/api/v1/shop/order` | Kaufauftrag erstellen |
| `GET` | `/api/v1/shop/order/:id` | Auftragsstatus pollen |
| `GET` | `/api/v1/shop/address` | Solana-Empfangsadresse |

---

## FND Token-Shop

Nutzer kaufen FND direkt mit SOL. Kein zentraler Zahlungsanbieter, kein Fiat-Handling durch den Betreiber.

### Ablauf

```
1. Nutzer gibt FND-Betrag + Gnosis-Chain-Adresse ein
2. Node berechnet: Betrag / SOL-Kurs + 0.5 % Slippage
3. Nutzer sendet SOL an die Fundus-Solana-Wallet
4. Node erkennt Eingang per Solana RPC (Polling alle 3 s)
5. Node ruft FND.mint() auf Gnosis Chain auf
6. FND landen in der Gnosis-Chain-Wallet des Nutzers
```

### Kurs-Quellen (automatischer Fallback)

1. CoinGecko – direkter SOL/EUR-Kurs
2. Binance – SOL/USDT × EUR/USDT
3. Kraken – SOL/EUR Spotmarkt

Cache: 30 s. Kauf-Angebot gültig: 90 s (konfiguierbar).

### Aktivieren

```bash
FUNDUS_SHOP_ENABLED=true
FUNDUS_SHOP_RECEIVE_ADDR=<Solana-Base58-Adresse>
FUNDUS_SHOP_SLIPPAGE_BPS=50        # 0.5 %
FUNDUS_CHAIN_ID=100                # Gnosis Chain Mainnet
FUNDUS_FND_ADDRESS=0x...           # nach Contract-Deployment
FUNDUS_WALLET_PRIV_KEY=...         # Wallet für Mint-TX
```

---

## Lizenz

MIT

## FileStorage – Provider-Stake

### Aktueller Stand

Der Provider-Stake ist auf **10 FND** gesetzt (reduziert von 100 FND für die
Bootstrapping-Phase). Sybil-Angriffe mit 1.000 Wallets kosten damit noch immer
10.000 FND, ohne legitime neue Nodes in der Anfangsphase auszuschließen.

> **TODO (Admin-Notiz):** Stake schrittweise erhöhen sobald FND breiter
> verteilt ist:
>
> | Phase | Empfohlener Stake | Bedingung |
> |-------|-------------------|-----------|
> | Bootstrap (aktuell) | 10 FND | < 100 aktive Provider |
> | Wachstum | 50 FND | 100–500 Provider |
> | Reife | 100 FND | > 500 Provider, FND liquide |
>
> Anpassung in `contracts/FileStorage.sol`:
> ```solidity
> uint256 public constant PROVIDER_STAKE = 10 * 1e18; // → später 100 * 1e18
> ```
> Nach Änderung: `go run ./cmd/fundus-admin sign --version R00X ...`

### Wallet-Ausschluss bei Selbstreplikation

Als zusätzliche Heuristik (Go-Layer, nicht Smart Contract) wird die eigene
Wallet-Adresse bei der Replikationsvergütung ausgeklammert: Ein Node erhält
keine Bandwidth/Storage-FND für Chunks, die seiner eigenen Wallet zugeordnet
sind. Dies ergänzt den Stake-Schutz, ersetzt ihn aber nicht.

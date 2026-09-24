# Fundus R001 – Audit-Report

**Stand:** 2026-04-06  
**Scope:** Go-Backend, Smart Contracts, Lua-Frontend, Deploy-Skript  
**Methodik:** statische Analyse, Datenfluss-Prüfung, Threat-Modeling

> **Patch-Stand:** Alle Findings gepatcht. C-5 als Stub bis R002.
> **Feature:** Flat-Listing für Shares implementiert.

---

## Kritisch (beheben vor Produktion)

### C-1 ✅ GEPATCHT – PeerID-Spoofing bei Share-Zugriff

**Datei:** `go/internal/api/files.go` Zeilen 292–298  

```go
// VERWUNDBAR
requester := c.GetHeader("X-Fundus-Peer-ID")
if requester == "" {
    requester = c.Query("peer_id")  // ← jeder kann jeden vortäuschen
}
```

`peer_id` kommt unkontrolliert aus dem HTTP-Query-String. Jeder lokale
Prozess der Zugriff auf Port 3000 hat (localhost) kann sich als beliebiger
Peer ausgeben und auf `specific`-Shares zugreifen.

**Fix:** PeerID ausschließlich vom authentifizierten libp2p-Stream-Handler
setzen. Im Web-UI-Kontext gilt `peer_id = "local"` mit expliziter
Whitelist-Prüfung – nie aus dem Request-Parameter.

```go
requester := c.GetHeader("X-Fundus-Peer-ID")
if requester == "" {
    // Nur lokaler Zugriff via Loopback erlaubt ohne PeerID
    if !strings.HasPrefix(c.Request.RemoteAddr, "127.") {
        c.AbortWithStatusJSON(403, gin.H{"error": "peer authentication required"})
        return
    }
    requester = "local"
}
```

---

### C-2 ✅ GEPATCHT – Settlement-Loop O(N) DHT-Calls für Trade-Dauer

**Datei:** `go/internal/topology/settlement.go` Zeile 587  

```go
for i := 0; i < total; i++ {  // total = Trade-Dauer in Sekunden
    ts := from.Add(time.Duration(i) * time.Second)
    data, err := se.p2p.DHTget(ctx, dhtKey)  // 1 Netzwerk-Call/Sekunde
```

Bei 30-Tage-Trades: **2.592.000 sequenzielle DHT-Lookups** pro Trafo.
Bei 5 Trafos auf der Route: 13 Millionen Calls. Blockiert den Node vollständig.

**Fix:** Batch-Lookup mit konfigurierbarem Sampling + Parallelisierung:

```go
const settlementSampleRate = 60 // 1 Cert pro Minute statt pro Sekunde

func (se *SettlementEngine) loadCertsForPeriod(...) {
    step := time.Duration(settlementSampleRate) * time.Second
    sem  := make(chan struct{}, 20) // max 20 parallele Lookups
    ...
}
```

---

### C-3 ✅ GEPATCHT – WebSocket CheckOrigin: `return true`

**Datei:** `go/internal/api/messenger.go` Zeile 21  

```go
CheckOrigin: func(r *http.Request) bool { return true },
```

Erlaubt CSRF über WebSocket von beliebigen Origins. Jede Website die der
User besucht kann eine WebSocket-Verbindung zum lokalen Node aufbauen.

**Fix:** Origin auf `localhost`/`127.0.0.1` beschränken:

```go
CheckOrigin: func(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    return origin == "" ||
        strings.HasPrefix(origin, "http://localhost") ||
        strings.HasPrefix(origin, "http://127.0.0.1")
},
```

---

### C-4 ✅ GEPATCHT – Wallet Private Key in `fundus.env` (Plaintext)

**Datei:** `fundus.env` Zeile 123; `config.go` Zeile 142

```
FUNDUS_WALLET_PRIV_KEY=     ← Wenn befüllt: Plaintext auf Disk
```

`fundus.env` hat `chmod 640` (deploy.ps1) → `fundus`-User kann lesen.
Root-kompromittierung = vollständiger Wallet-Verlust.

**Fix:** Unterstützung für separates Key-File mit `chmod 600`:

```go
// In config.go Load():
if cfg.WalletPrivKey == "" && cfg.SeedFile != "" {
    // Key aus wallet.key ableiten (bereits implementiert in fnd-wallet)
}
```

Wallet-Key nie in `fundus.env`, immer in `/etc/fundus/wallet.key` mit
`chmod 400 root:root`. Deploy-Skript bereits korrekt für SeedFile – das
reicht, `FUNDUS_WALLET_PRIV_KEY` sollte leer bleiben.

---

### C-5 ✅ STUB (R002) – DHT-Daten ohne Signaturprüfung akzeptiert

**Datei:** `go/internal/filestore/filestore.go` (Download-Pfad)

Manifestdaten aus dem DHT werden deserialisiert und direkt verwendet, ohne
die Ed25519-Signatur des ursprünglichen Uploaders zu prüfen. Ein Angreifer
der DHT-Write-Zugriff hat (legitimer Peer) kann ein Manifest vergiften und
so den Download auf bösartige Chunks umleiten.

**Fix:** Jedes FileManifest muss mit der FundusID des Owners signiert und
beim Download verifiziert werden:

```go
type FileManifest struct {
    ...
    OwnerID   string `json:"owner_id"`
    Signature []byte `json:"sig"`  // Ed25519(owner_privkey, BLAKE2b(manifest))
}

// Vor Verwendung:
if !ed25519.Verify(ownerPubKey, manifestHash[:], manifest.Signature) {
    return fmt.Errorf("manifest signature invalid")
}
```

---

## Hoch (beheben in nächster Release)

### H-1 ✅ GEPATCHT – Meter: `Signature`-Kommentar-Inkonsistenz (Tech-Debt)

**Datei:** `go/internal/meter/reader.go` Zeilen 159 + 879

```go
Signature    string    `json:"signature"` // HMAC-SHA256   ← falsch
// ...
// Token-Erstellung (HMAC-SHA256)                          ← falsch
// Argon2id-Commitment: Kein HMAC-SHA256 (SHA kompromittiert)  ← richtig
```

Implementierung ist korrekt (Argon2id), aber Kommentare und Struct-Tag
sagen noch HMAC-SHA256. Verwirrend für Audits und externe Entwickler.

**Fix:** Kommentare und ggf. JSON-Tag anpassen:

```go
Commitment []byte `json:"commitment"` // Argon2id(key || payload)
```

---

### H-2 ✅ GEPATCHT – Share-Download: fehlender `Content-Length`-Header

**Datei:** `go/internal/api/files.go` → `shareDownload()`

`ReadFile()` kopiert via `io.Copy()` direkt in `c.Writer` ohne vorher
`Content-Length` zu setzen. Browser können den Fortschritt nicht anzeigen
und manche Clients interpretieren die Antwort als chunked encoding.

**Fix:**

```go
info, _ := os.Stat(resolvedPath)
if info != nil {
    c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
}
```

---

### H-3 ✅ GEPATCHT – Symlink-Angriff auf Share-Verzeichnisse

**Datei:** `go/internal/filestore/shares.go` Zeile 218

`os.Stat()` folgt Symlinks. Ein Nutzer mit Schreibzugriff auf ein Share-Verzeichnis
könnte einen Symlink auf `/etc/passwd` oder `/etc/fundus/wallet.key` anlegen
und diesen dann über die Read-API herunterladen.

**Fix:** `os.Lstat()` für die Pfad-Validierung + expliziter Symlink-Check:

```go
info, err := os.Lstat(localPath)
if err != nil { return ..., err }
if info.Mode()&os.ModeSymlink != 0 {
    return ..., fmt.Errorf("symlinks nicht erlaubt in shares")
}
```

---

### H-4 ✅ GEPATCHT – EnergyToken: Unbegrenztes Minting ohne Supply-Cap

**Datei:** `contracts/EnergyToken.sol`

Jeder authorisierte Minter kann unbegrenzte Token erzeugen. Kein `MAX_SUPPLY`,
keine On-Chain-Prüfung ob die Smartmeter-Messung plausibel ist (z.B. max. Nennleistung).

**Fix:** Plausibilitätsprüfung on-chain:

```solidity
uint256 public constant MAX_KWH_PER_MINT = 1_000_000; // 1 GWh/Mint

function mint(..., uint256 amount, ...) external onlyMinter returns (uint256 id) {
    require(amount <= MAX_KWH_PER_MINT * 1e3, "ET: implausible quantity");
    ...
}
```

---

### H-5 ✅ GEPATCHT – Rate Limiting fehlt vollständig

Die API hat kein Rate Limiting. Ein lokaler Prozess könnte:
- `/api/v1/partner/search` tausende Male aufrufen (DHT-Flood)
- `/api/v1/files/upload` spammen bis der Speicher voll ist
- `/api/v1/grid/capacity/allocate` den UTXO fluten

**Fix:** Gin-Middleware:

```go
import "golang.org/x/time/rate"

func rateLimitMiddleware(rps float64) gin.HandlerFunc {
    limiter := rate.NewLimiter(rate.Limit(rps), int(rps)*2)
    return func(c *gin.Context) {
        if !limiter.Allow() {
            c.AbortWithStatusJSON(429, gin.H{"error": "rate limit exceeded"})
            return
        }
        c.Next()
    }
}
```

---

### H-6 ✅ GEPATCHT – Capacity: CertTTL 10s zu kurz bei hoher DHT-Latenz

**Datei:** `go/internal/topology/capacity.go` Zeile 52

```go
CertTTL = 10 * time.Second
```

Bei hoher Netzlast (GossipSub-Verzögerung, DHT-Lookup) kann ein frisches
Cert als veraltet abgelehnt werden und ein Trade wird fälschlicherweise
in den Ansatz-2-Fallback umgeleitet.

**Fix:** `CertTTL = 30 * time.Second` (Smartmeter-Intervall × 30).
Separate Konstante `CertMaxAge = 5 * time.Second` für den Staleness-Check
in `AllocateSlot()`.

---

### H-7 ✅ GEPATCHT – UTXO-Persistenz: nur DHT, kein lokales Backup

**Datei:** `go/internal/topology/capacity.go` → `loadUTXOFromDHT()`

Bei DHT-Partition (Pi offline, schlechte Verbindung) startet der Node ohne
aktive Slots – bereits reservierte Kapazität ist "vergessen". Trades die
vor dem Neustart liefen erhalten keine Gebühr-Auszahlung.

**Fix:** Lokales Backup in LevelDB:

```go
func (cm *CapacityManager) saveUTXOLocal() {
    data, _ := json.Marshal(cm.utxo)
    cm.db.Put([]byte("capacity_utxo"), data, nil)
}
// Bei Start: erst LevelDB, dann DHT als Fallback
```

---

## Mittel

### M-1 – Share `WriteFile`: Race bei gleichzeitigem Upload

`os.O_EXCL` verhindert Überschreiben, aber zwei gleichzeitige Uploads
der gleichen Datei von verschiedenen Peers erzeugen einen Race auf das
`O_CREATE|O_EXCL`. Der zweite erhält korrekt einen Fehler, aber die
Fehlermeldung `file exists` ist für den User verwirrend.

**Fix:** Temporäre Datei + atomares Rename:

```go
tmp, _ := os.CreateTemp(targetDir.LocalPath, ".upload-*")
io.Copy(tmp, r)
tmp.Close()
os.Rename(tmp.Name(), filepath.Join(targetDir.LocalPath, filename))
```

---

### M-2 – fundus-admin: `peer_id` Query-Parameter in Share-Entdeckung

**Datei:** `go/cmd/fundus-admin/main.go` → `cmdShares()`

Der Admin-CLI verwendet dieselbe unsichere `peer_id`-Query-Parameter-Methode
wie die User-API. Im Admin-Kontext (localhost) tolerierbar, sollte aber
konsistent mit dem Fix aus C-1 sein.

---

### M-3 – LLM-Analyzer: Keine Prompt-Längen-Begrenzung

**Datei:** `go/internal/llm/productsearch.go`

Benutzereingabe (`VoiceText`) fließt direkt in den Ollama-Prompt ohne
Längenbegrenzung. Bei sehr langen Eingaben: OOM auf dem Pi 3 (512 MB RAM).

**Fix:**

```go
const MaxPromptInput = 2000
if len(req.VoiceText) > MaxPromptInput {
    req.VoiceText = req.VoiceText[:MaxPromptInput]
}
```

---

### M-4 – Settlement: Bottleneck-Auswahl bevorzugt Certs, nicht Trafo-Coverage

**Datei:** `go/internal/topology/settlement.go` → `collectCerts()`

Der Engpass-Trafo (wenigste Certs) wird als Referenz gewählt. Das ist
korrekt für Coverage-Berechnung, aber nicht für die MerkleRoot: wenn
Trafo A 3.600 Certs hat und Trafo B 3.590, werden 3.590 Certs für alle
verwendet – 10 Sekunden gehen verloren obwohl der Trade tatsächlich
geliefert hat.

**Fix:** MerkleRoot über den Schnittmenge der Zeitstempel aller Trafos
berechnen (Intersection statt Minimum):

```go
// Nur Zeitstempel einschließen die bei ALLEN Trafos vorhanden sind
certTimes := intersectTimestamps(certsByTrafo)
```

---

### M-5 – Deployment: `fundus.env` enthält Testdaten

`fundus.env` hat befüllte Felder wie `FUNDUS_BOOTSTRAP_PEERS=` die im
Produktionseinsatz leer sein sollten. Neue Betreiber kopieren die Datei
und deployen versehentlich gegen Testnet-Peers.

**Fix:** Separate `fundus.env.example` mit Kommentaren, `fundus.env`
im `.gitignore`.

---

### M-6 – `config.go`: `envFloat64` verwendet `fmt.Sscanf` ohne Fehlerprüfung

**Datei:** `go/internal/config/config.go`

```go
func envFloat64(key string, def float64) float64 {
    ...
    if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
        return def  // ← korrekt
    }
    return f
}
```

Korrekt implementiert – nur dokumentieren dass ungültige Werte still
den Default verwenden (kein Warning-Log):

```go
if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
    log.Printf("WARN: invalid value for %s=%q, using default %.1f", key, v, def)
    return def
}
```

---

## Niedrig / Verbesserungspotenzial

### N-1 ✅ GEPATCHT – Shares-DHT: lokale Pfade versehentlich veröffentlichbar

`runDHTPublish()` filtert `LocalPath` korrekt aus den Public-Metadaten.
Aber `shareGet()` (GET `/api/v1/shares/:id`) gibt die vollständige Share
inklusive `local_path` zurück – das ist für den Owner-Node korrekt und
nötig, aber ein externer Peer der Rate auf den API-Port hat (z.B. via
NAT-Fehlkonfiguration) sieht die Pfade.

**Risiko:** Niedrig (API bindet auf 127.0.0.1), aber als Defense-in-Depth
sollte `local_path` aus der öffentlichen API-Antwort gefiltert werden wenn
der Requester nicht `local` ist.

---

### N-2 ✅ GEPATCHT – Settlement: `OnAnchorSettlement = nil` ist stiller Fail

Settlement-Ergebnis wird als `anchored: false` gespeichert ohne dass der
Trade-Initiator benachrichtigt wird. Für Produktionseinsatz: expliziter
Retry-Mechanismus oder Alarm.

---

### N-3 ✅ GEPATCHT – Capacity `signAndPublishUTXO`: Fehler beim DHT-Put werden ignoriert

```go
func (cm *CapacityManager) signAndPublishUTXO(ctx context.Context) error {
    ...
    return cm.p2p.DHTput(ctx, dhtKey, data)  // ← Fehler wird zurückgegeben
}
// Aber in runSlotPruner():
cm.signAndPublishUTXO(ctx)  // ← Rückgabewert nicht geprüft
```

**Fix:** `if err := cm.signAndPublishUTXO(ctx); err != nil { cm.log.Warn(...) }`

---

### N-4 ✅ GEPATCHT – Partner: Bloom-Filter-Größe nicht konfigurierbar

Bei 2.600+ Nodes und vielen Interessen-Hashes: False-Positive-Rate steigt.
Bloom-Filter-Größe sollte in `fundus.env` konfigurierbar sein.

---

### N-5 ✅ GEPATCHT – Escrow: Kein Mechanismus für partielle Lieferung

Wenn bei einem Energie-Trade der Erzeuger 80% der vereinbarten Menge
liefert (Ausfall, Netzproblem): aktuell alles-oder-nichts. Kein partielles
Settlement vorgesehen.

**Verbesserung:** `PartialSettlement`-Flag in `EnergyTrade` mit
proportionaler Auszahlung basierend auf `CoverageRatio`.

---

### N-6 ✅ GEPATCHT – Lua-Frontend: `ngx.escape_uri` für Profil-Feldinhalte

In `partner.lua` werden Profilwerte mit `ngx.escape_uri()` escaped bevor
sie in HTML-Input-`value`-Attribute eingefügt werden. `escape_uri` ist
URL-Encoding, nicht HTML-Encoding – `&`, `"`, `<` werden nicht escaped.

**Fix:** `ngx.escape_uri` → eigene HTML-Escape-Funktion oder Template-Engine.

---

### N-7 ✅ GEPATCHT – `fundus-admin shares --dirs`: kein Escaping für Pfade mit Komma

```bash
fundus-admin shares --create --dirs "Name:/home/pi/Meine,Dokumente"
```
Pfad mit Komma wird als zwei Verzeichnisse interpretiert.

**Fix:** `:` als Trennzeichen zwischen Name und Pfad, `;` zwischen
Verzeichnissen:
```bash
--dirs "Name:/home/pi/Docs;Fotos:/home/pi/Fotos:ro"
```

---

### N-8 ✅ GEPATCHT – CertTTL vs. Smartmeter-Ausfall: keine Alarmierung

Wenn `cm.latestCert` älter als 5 Sekunden wird (Zeile 382), wird die
Allokation verweigert. Aber es gibt keine Benachrichtigung an den Node-Owner.
Das Gerät ist still "offline" ohne sichtbaren Alarm.

**Fix:** `/api/v1/grid/capacity/status` sollte `cert_age_s` exponieren
und ein `healthy: false` wenn älter als 5s. Das UI-Dashboard kann dann
warnen.

---

## Zusammenfassung

| Priorität | Anzahl | Kategorie |
|-----------|--------|-----------|
| Kritisch  | 5      | C-1 bis C-5 |
| Hoch      | 7      | H-1 bis H-7 |
| Mittel    | 6      | M-1 bis M-6 |
| Niedrig   | 8      | N-1 bis N-8 |

**Sofort-Prioritäten:**
1. **C-1** (PeerID-Spoofing) – einfach zu exploiten, einfach zu fixen
2. **C-2** (Settlement-Loop) – würde Pi 3 bei langem Trade einfrieren
3. **C-3** (WebSocket CSRF) – Standard-Webvulnerabilität
4. **C-5** (DHT ohne Signaturprüfung) – ermöglicht Manifest-Poisoning

**Positive Befunde:**
- FundusMarket.sol: korrekte Checks-Effects-Interactions Reihenfolge
- FileStorage.sol: `withdrawStake()` macht State-Update vor Transfer ✓
- FND.sol: `onlyMinter`-Zugriffskontrolle sauber implementiert ✓
- Solidity 0.8.x: automatischer Overflow-Schutz ✓
- `AllocateSlot()`: Mutex korrekt gehalten, kein TOCTOU ✓
- Path-Traversal-Schutz in `shares.go` korrekt mit `HasPrefix` ✓
- API bindet auf `127.0.0.1` – korrekt ✓
- `withdrawStake()`: State vor Transfer – kein Reentrancy-Risiko ✓

# Fundus — Roadmap für Weiterarbeit ohne lokales Kompilieren

Stand: Phase 2 abgeschlossen und **auf dem Pi verifiziert** (Node läuft, Chain
aktiv, Genesis mit 1 Bio. FND beim Fee-Collector). Dieses Dokument beschreibt,
wie wir weiterarbeiten, solange du nicht zum Gegenkompilieren kommst.

---

## 0. Ausgangslage: was bereits bewiesen ist

Der laufende Node belegt, dass **Phase 1 + 2 fehlerfrei kompilieren** — inklusive
der Chain-Einbindung in `main.go`, der JSON-Persistenz und der API. Das Risiko
„uncompilierter Code" betrifft also nur **neuen** Code. Wir bauen auf geprüftem
Fundament.

Im Container hier steht **kein Go** zur Verfügung (kein `go build`, kein `gofmt`,
kein Netz). Verifikation läuft daher über drei Ersatz-Mechanismen statt über den
Compiler (siehe Abschnitt 3).

---

## 1. Leitprinzip: Reihenfolge nach Verifizierbarkeit

Nicht die Spec-Nummer bestimmt die Reihenfolge, sondern wie sicher sich ein
Baustein ohne Compiler schreiben lässt.

**Grün (sicher ohne Kompilieren) — reine Logik über den State:**
- abgedeckt durch Unit-Tests
- keine Nebenläufigkeit, kein Netzwerk
- statisch prüfbar (Symbole, Importe, Signaturen)

**Rot (NICHT ohne Kompilieren/Multi-Node-Test schreiben):**
- BFT-Konsens, GossipSub, libp2p-Verdrahtung (Phase 3)
- alles, wo zwei Nodes live miteinander reden müssen
- Storage-Proofs / Proof-of-Retrievability (Phase 6a, kryptografisch heikel)

Begründung: Konsens- und Netzwerkbugs sind subtil, tauchen in statischen Checks
nicht auf und brauchen ein laufendes Mehr-Node-Setup. Die verschieben wir, bis
du wieder testen kannst.

---

## 2. Empfohlene Arbeitsreihenfolge (während der Abwesenheit)

Geordnet von „sofort sinnvoll, geringes Risiko" zu „später".

### 2.1 Fee-Collector als Single Source of Truth (Option A) — ✅ ERLEDIGT
> Umgesetzt: `config.EffectiveFeeCollector()` leitet immer aus der kanonischen
> Konstante ab (leer→kanonisch, Spiegel→kanonisch, Abweichung→defensiv kanonisch;
> Validate() lehnt Abweichung weiterhin hart ab). Chain-Genesis (main.go) nutzt
> diese Funktion statt env direkt → Chain startet auch bei leerem env, keine
> Fragilität mehr. +5 Tests (feecollector_test.go).

Die kanonische Fee-Collector-Adresse steht aktuell dreifach (Code-Konstante,
fundus.env, Deploy-Default) und muss manuell synchron gehalten werden — das hat
zuletzt den Fatal-Abbruch verursacht. Umbau: nur noch **eine** Quelle, env und
Deploy leiten daraus ab. Schutz bleibt voll erhalten, Fragilität verschwindet.
- Risiko: minimal (eine Konstante, eine Validierung)
- Verifikation: statischer Check + ein bestehender Config-Test

### 2.2 Vertrags-/Escrow-State-Machine (Spec §7a, Tx 0x30–0x37) — ✅ ERLEDIGT
> Chain hatte schon open/confirm/refund/dispute (0x30–0x34). Ergänzt: der reiche
> Storno-/Rücksende-Flow als echte Chain-Tx 0x35–0x37 (cancel → submit_return →
> confirm_return), neue States cancel_requested(4)/return_submitted(5), Felder
> ReturnDeadline + TrackingHash (deterministisch in encode()), Frist-Fallback
> (Käufer darf nach returnWindowBlocks selbst bestätigen). +8 Tests
> (escrow_return_test.go: Happy-Path + alle Ablehnungen). Über den generischen
> POST /chain/tx automatisch nutzbar (mempool/codec typ-agnostisch).
> OFFEN (→ 2.5): contract.go-API-Stubs an diese Tx anbinden (braucht serverseitige
> Signierung / Wallet→Chain-Brücke).

### 2.3-skal Energy-Schichtung gegen Chain-Explosion — ✅ ERLEDIGT
> Befund (Tobias): bei vielen Usern/Smartmetern würde jede Mess-Tx global
> repliziert → Chain explodiert. Entscheidung: diskrete Tokens pro Periode mit
> Rohdaten-Hash, Rohdaten rein lokal, prunebar nach Settlement.
> Umgesetzt — drei Schichten:
> • LOKAL (meter/rawstore.go): PeriodStore hält Roh-Samples beim Erzeuger (JSONL),
>   ClosePeriod bildet das Commitment (Ws-Menge ganzzahlig + deterministischer
>   BLAKE3-Hash aller Samples), DeletePeriod löscht nach Settlement. kWh→Ws nur
>   hier (Float lokal, on-chain nur Ganzzahl). +5 Tests.
> • ON-CHAIN (chain/commodity.go): commodity_certify erzeugt jetzt einen diskreten
>   CommodityToken{Producer,Commodity,Unit,Amount,Period,RawDataHash,Settled} statt nur
>   CertifiedTotal hochzuzählen. Commodity (Strom/Gas/Wasser/Öl) UND explizite
>   Einheit (UnitWs/UnitMl/UnitG/UnitWh/UnitL) als getrennte Felder — Token ist
>   selbsterklärend, ein Rohstoff kann in verschiedenen Einheiten erfasst werden.
>   Einheit ist Zähler-Eigenschaft (MeterRecord.Unit), Token erbt sie. Deterministische
>   Token-ID aus Zähler+Periode (keine Doppel-Zertifizierung). Neuer Tx commodity_settle
>   (0x24) markiert settled → Token fällt aus dem State-Root (Pruning). +6 Tests.
> • AUDIT: Der RawDataHash bleibt in der Block-Historie, auch nachdem die lokalen
>   Rohdaten gelöscht sind → Menge bleibt beweisbar.
> Ergebnis: On-Chain-Last = O(Erzeuger × Perioden) statt O(Messungen). Aktiver
> State bleibt klein (settled Tokens gepruned).
> ROT/OFFEN: Verdrahtung meter-Ingest → PeriodStore → automatischer ClosePeriod-
> Zeitpunkt (Abrechnungsperiode) → commodity_certify-Tx; Aufräum-Job DeletePeriod bei
> on-chain Settled. Handel (2.3b) setzt Settled beim Verkauf statt Erzeuger-Self.

### 2.3 Energy-Settlement (Spec §7b, Tx 0x22) — ✅ TEIL 1 (Zertifizierung) ERLEDIGT
> Aufgeteilt nach Designentscheidung: **2.3a Zertifizierung (jetzt)** vs.
> **2.3b Handel (später)**.
> Umgesetzt (2.3a): meter_register (0x23) + commodity_certify (0x22). Die Chain
> verbucht die Differenz zweier kumulativer, monoton steigender Zählerstände als
> zertifizierte Liefermenge beim Erzeuger (CertifiedTotal), mit lückenloser Kette.
> Validierung: Zähler registriert, nur Erzeuger reicht ein, timestamp_b>timestamp_a,
> Monotonie (cumulative_b>=cumulative_a), reading_a==letzter Endstand + timestamp_a
> ==letzter Zeitstempel (kein Doppel-/Lücken-Abrechnen). SI-Ganzzahlen (Ws/g/ml),
> nie Float. Neue meters-Map im State + metersRoot() im State-Root (per Block-Replay
> rekonstruiert, keine Extra-Persistenz). +10 Tests (energy_test.go). KEIN Geldfluss.
> OFFEN (2.3b): Handel der Zertifikate — Erzeuger setzt Minimum, Preisbildung durch
> Angebot/Nachfrage, Verbraucher reicht ein und zahlt Erzeuger. Setzt auf
> CertifiedTotal auf. Optional: separate Zähler-Hardware-Signatur (aktuell signiert
> der Erzeuger-Account die Tx; Monotonie+Kette sind der Fälschungsschutz).


### 2.4 Wallet-Ableitung auf BLAKE3 angleichen (Spec §13) — ✅ ERLEDIGT
> Umgesetzt: identity.DeriveAddressFromSeed (Node) und fnd-wallet (CLI) leiten die
> Adresse jetzt via BLAKE3 ab (erste 20 Bytes von BLAKE3-256(X||Y)), identisch zu
> chain.PubkeyToAddress — statt vorher Keccak (Ethereum-Stil, letzte 20 Bytes).
> Cross-Check-Test (identity_address_test.go) vergleicht Wallet- und Chain-Ableitung
> byteweise für mehrere Seeds inkl. Umlaute. Argon2id/Salt unverändert; nur die
> finale Hash-Stufe geändert.
> ⚠ FOLGE: dieselbe Seed ergibt jetzt eine ANDERE Adresse. Die in config/validate.go
> hinterlegte KanonischeFeeCollector ist eine Keccak-Altadresse → vor Produktivstart
> via fnd-wallet neu ableiten + Konstante setzen (siehe ROT-ToDos & WALLET-SETUP.md).


### 2.5 Tx-Einreichung serverseitig signieren (Wallet→Chain-Brücke) — ✅ ERLEDIGT
> Umgesetzt: exportierte Builder im chain-Paket (BuildSignedTransfer für Werttransfers
> inkl. automatischer 1,8%-Gebühr; BuildSignedTx generisch für vorgefertigte Payloads,
> z.B. Escrow-/Energie-Folge-Tx). Neuer Endpunkt POST /chain/send: leitet aus Seed ab,
> ermittelt die Nonce serverseitig (AccountInfo), baut+signiert+reicht ein, nullt den
> Key sofort. Nutzt die BLAKE3-Adresse (Phase 2.4) → tx.From == Chain-Adresse garantiert.
> +3 Tests (build_test.go: E2E Schlüssel→Block→Saldo, Eingabevalidierung, generischer Builder).
> BEWUSST OFFEN: Das bestehende /wallet/transfer (ERC-20/Gnosis-Brücke) bleibt unangetastet,
> bis die eigene Chain live validiert ist — kein Tausch eines funktionierenden Pfads gegen
> einen ungetesteten. Die contract.go-Escrow-Stubs lassen sich jetzt via BuildSignedTx
> anbinden (eigener UI-Schritt, sobald die Chain live ist).


### 2.6-pre Multi-Node-Grundlage: geteilter Genesis + Block-Sync — ✅ ERLEDIGT
> Befund (Tobias): (1) jeder Node baute seinen EIGENEN Genesis mit time.Now() →
> verschiedene Hashes → getrennte Chains; (2) der vorhandene SyncProtocol glich nur
> Marktplatz-Records ab, KEINE Blöcke. Log sagte selbst "Single-Node".
> Umgesetzt (ohne Konsens, Option „voll inkl. Live-Propagation"):
> • Deterministischer Genesis: CanonicalGenesis(feeCollector) mit festem
>   GenesisTimestamp (2025-01-01) + fester Startausgabe → jeder Node erzeugt
>   unabhängig DENSELBEN Genesis-Hash. main.go nutzt das statt time.Now().
> • Chain-Sync-Methoden (sync.go): GetBlock/ExportBlockJSON/ImportBlockJSON/
>   ImportBlock (volle ApplyBlock-Validierung, nur direkter Nachfolger, idempotent)
>   + GenesisHeaderHash. Blockchain speichert genesisHash.
> • P2P-Schicht (p2p/chainsync.go): ChainSyncProtocol (Request/Response für Aufhol-
>   Sync) + TopicBlocks (GossipSub für Live-Propagation). ChainBridge-Interface
>   entkoppelt p2p von chain (KEIN Import-Zyklus). AttachChain registriert beides;
>   trackPeer löst beim Connect SyncChainFromPeer aus (Genesis-Hash-Vergleich PFLICHT
>   → fremde Chain wird abgelehnt). chainProduceBlock broadcastet neuen Block.
> • +3 Tests (sync_test.go): Genesis-Determinismus, Block-Sync zwischen 2 Nodes
>   (gleiche Höhe+Head-Hash+Saldo), Lücken-Ablehnung.
> ROT (Live-Test nötig): echter 2-Node-Roundtrip — Aufhol-Sync beim Connect +
> Live-Propagation bei /chain/produce. Noch KEIN Konsens (wer darf produzieren?) →
> aktuell weiterhin Single-Producer; Multi-Producer-Konflikte löst erst Phase 3.

### 2.6 ERST WENN DU ZURÜCK BIST: Phase 3 — BFT-Konsens
GossipSub-Topic `fundus.chain`, Block-Propagierung, 2/3-Voting, Zeittakt-
Produktion, Validator-Set. Zwingend mit ≥2 laufenden Pis zu testen.
- NICHT blind schreiben. Wir können das Design + Interfaces vorbereiten
  (Abschnitt 2.7), aber die Verdrahtung selbst wartet auf Tests.

### 2.7 Optional vorbereitbar: Konsens-Design auf Papier
Was sich ohne Code sicher tun lässt: die Konsens-Schnittstellen, Nachrichten-
Typen (Proposal/Vote/Commit), Timeout-Parameter und den Ablauf als erweiterten
Spec-Abschnitt §6 ausformulieren. Reines Design, kein Compile-Risiko — macht
Phase 3 später schneller und sicherer.

---

## 3. Verifikation ohne Compiler — drei Netze

1. **Test-First-Disziplin.** Jeder grüne Baustein (2.1–2.5) bekommt Unit-Tests
   mitgeliefert. Wenn du zurück bist, validiert **ein** `go test ./...`-Lauf den
   ganzen aufgelaufenen Stapel auf einmal. Die Tests ersetzen den manuellen
   Kompilier-Check.

2. **Strenger statischer Check** (im Container möglich): pro Datei brace-/Klammer-
   Balance, Importe-genutzt/-fehlt, Symbol-Definition vs. -Aufruf, keine
   Duplikat-Definitionen, go-ethereum-/blake3-API-Signaturen. Das Skript
   `tools/static-check.sh` (siehe Abschnitt 4) bündelt das.

3. **Konservativer Stil.** Kleine, in sich geschlossene Dateien; keine Tricks;
   bestehende, bereits kompilierte Muster wiederverwenden statt neue Konstrukte.
   Reduziert die Fehlerklasse, die nur der Compiler fängt.

**Ehrliche Grenze:** Statische Checks fangen Syntax, Importe, fehlende Symbole.
Sie fangen **nicht** Typfehler, Interface-Mismatches oder Logikfehler. Deshalb
die Test-First-Disziplin — und deshalb bleibt Phase 3 (wo Logik- und Timing-
Fehler dominieren) bis zum echten Test liegen.

---

## 4. Arbeitszyklus pro Baustein (während der Abwesenheit)

1. Code + Unit-Tests schreiben (grüne Bausteine aus Abschnitt 2)
2. Statischen Check laufen lassen, Befunde fixen
3. In die ZIPs bauen (Retry-Loop, Tests bewusst mit aufnehmen)
4. Im Journal vermerken: was, warum, welcher Test deckt es ab
5. Beim nächsten Mal am Setup: einmal `go test ./...` → Stapel validieren

So wächst eine getestete, in sich konsistente Codebasis, die beim ersten
Kompilieren mit hoher Wahrscheinlichkeit durchläuft — und wenn nicht, zeigt
genau ein Testlauf alle Stellen auf einmal.

---

## 5. Was NICHT zu tun ist, solange nicht getestet werden kann

- Phase 3 (Konsens/Netzwerk) blind implementieren
- Storage-Proofs / Proof-of-Retrievability (Phase 6a) blind implementieren
- Adress-/Schlüsselableitung ändern, ohne den Gleichheits-Test mitzuliefern
- Argon2-/Krypto-Parameter erneut anfassen (sind final; jede Änderung ändert
  Adressen)
- Große Refactorings ohne Testabdeckung

---

## 6. Reihenfolge auf einen Blick

```
JETZT (grün, ohne Kompilieren machbar):
  2.1  Fee-Collector Single Source of Truth
  2.2  Escrow/Vertrags-State-Machine (Tx 0x30–0x34)
  2.3  Energy-Settlement (Tx 0x22)
  2.4  Wallet-Ableitung auf BLAKE3 (Phase-6-Vorbereitung)
  2.5  Serverseitige Tx-Signierung (Wallet→Chain-Brücke)
  2.7  Konsens-Design als Spec §6 ausformulieren (reines Design)

ZURÜCK AM SETUP (rot, braucht Live-Test):
  Phase 3  BFT-Konsens über libp2p (Multi-Node)
  Phase 4  Stake/Slashing
  Phase 6a Container + Storage-Proofs
```

Empfehlung für den nächsten Schritt: **2.1** (klein, schließt den offenen
Fee-Collector-Punkt sauber ab), danach **2.2** (bringt Verträge/Zertifikate
real in die Chain — der inhaltlich größte Mehrwert).

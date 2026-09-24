# Fundus Chain — Spezifikation (Entwurf v0.1)

Eine eigene Proof-of-Stake-BFT-Blockchain für das Fundus-Ökosystem. Ersetzt die
Abhängigkeit von Gnosis Chain. FND wird die native Währung *und* das Gas. Die
Validatoren sind die gestakten Operator-Nodes; Konsens per 2/3-Mehrheit.

Dieses Dokument legt das Fundament fest, BEVOR Code geschrieben wird. Eine Chain
ist nur so sicher wie ihr schwächstes Detail — Blockformat, Konsens-Ablauf und
Zustandsübergänge müssen vorab durchdacht und stabil sein, weil spätere
Änderungen harte Brüche (Hard Forks) bedeuten.

---

## 1. Ziele und Nicht-Ziele

**Ziele**
- FND als native Währung und Gas — kein xDAI, keine externe Chain.
- Double-Spends werden *verhindert* (echter BFT-Konsens), nicht nur erkannt.
- Wiederverwendung der vorhandenen Infrastruktur: libp2p/GossipSub
  (Propagierung), redundante Speicherung (Block-/State-Persistenz),
  secp256k1/ed25519 (Signaturen).
- Sybil-Resistenz über Stake: Validatoren hinterlegen FND, Betrug kostet den
  Stake (Slashing).
- Die bestehende „Smart-Contract"-Logik (FND-Transfer, Escrow, FileStorage-
  Billing, EnergyToken/Grid) wird als deterministische State-Machine abgebildet.
- SOL-Einzahlung bleibt als On-Ramp (SOL → FND-Gutschrift).
- **Container**: Dateien, deren Fortbestand der Uploader per Tagesmiete bezahlt
  und für die er einen Download-Preis festlegen kann (§7c) — verbindet die Chain
  mit dem bestehenden Filesharing.

**Nicht-Ziele (Phase 1)**
- Keine vollständige Trustlessness à la Bitcoin von Tag eins — die Chain ist so
  dezentral wie ihre Validator-Menge (Bootstrap: wenige Operatoren).
- Keine Turing-vollständige VM (keine beliebigen Nutzer-Contracts mit eigenem
  Code) — stattdessen ein **System parametrisierbarer Vertrags-Templates**
  (§7a): ein fester, im Protokoll definierter Satz von Vertragsarten (Escrow,
  Kaufvertrag, Miete, Abo, Teilzahlung, Arbeitsvertrag), die der Nutzer mit
  eigenen Parametern befüllt. Das deckt reale Marktplatz- und Jobs-Anwendungen
  ab, bleibt aber deterministisch, klein und auditierbar.
- Kein Komitee-Sampling/Sharding in Phase 1 (erst nötig bei hunderten
  Validatoren).

---

## 2. Was wiederverwendet wird vs. was neu ist

| Schicht | Status | Quelle |
|---|---|---|
| P2P-Transport, Peer-Discovery | vorhanden | libp2p, mDNS, DHT |
| Nachrichten-Propagierung (Tx/Blöcke) | vorhanden, neues Topic | GossipSub |
| Signaturen, Adressen | vorhanden | secp256k1 (FND), ed25519 (Identität) |
| Persistenz, Redundanz | vorhanden | FileStore (5×-Redundanz) |
| Stake/Operator-Konzept | teilweise | fundus-admin `stake`, OperatorWallet |
| **Konsens (BFT)** | **NEU** | dieses Spec |
| **Blockformat, Mempool, State-Machine** | **NEU** | dieses Spec |
| **Fork-Choice / Finalität** | **NEU** | dieses Spec |

Der harte, neue Kern ist der **Konsens** + die **deterministische
State-Machine**. Alles andere ist Anbindung an Vorhandenes.

---

## 3. Account- und Zustandsmodell

**Account-basiert** (wie Ethereum), nicht UTXO — passt besser zu Salden,
Escrow und Billing.

```
Account {
  address   : 20 Bytes   // erste 20 Bytes von BLAKE3-256(secp256k1-PubKey), siehe §3a
  balance   : uint128     // FND in der kleinsten Einheit ("uFND", 1 FND = 1e9 uFND)
  nonce     : uint64      // fortlaufende Transaktionsnummer (Replay-Schutz)
}
```

> **Saldo-Dimensionierung (uint128, nicht uint64):** Salden summieren sich übers
> ganze Netz. Bei 1 FND = 1e9 uFND und einem Supply von z.B. 1 Bio. FND wären
> das 1e21 uFND — das sprengt uint64 (max ~1,8e19), passt aber bequem in uint128
> (~3,4e38). Beträge in Transaktionen ebenfalls uint128.

> **Nonce-Überlauf (uint64):** Die Nonce zählt PRO Account. uint64 reicht bis
> ~1,8e19 — selbst bei 1 Mio. Tx/Sekunde aus einem Account ~584.000 Jahre.
> Praktisch irrelevant (Ethereum nutzt dasselbe Limit ohne Probleme). Als
> sauberer Riegel statt stillem Überlauf: bei `nonce == 2^64-1` werden keine
> weiteren Txs des Accounts mehr akzeptiert (Ein-Zeilen-Regel in der
> Validierung).

> **Wichtig — Determinismus:** Salden und Gebühren sind ausschließlich
> **Ganzzahlen** (uint). NIEMALS Fließkomma im konsens-kritischen Pfad
> (Float-Rundung weicht zwischen Maschinen ab → unterschiedlicher State →
> Chain-Split). Die Wahl 1 FND = 1e9 uFND (statt 1e18 wie ERC-20) hält Beträge
> in uint64/uint128 handlich.

**Globaler State** = Abbildung `address → Account` plus Sonder-State (siehe §7:
Escrows, Validator-Set, Stakes). Der State wird über einen **Merkle-Baum**
zusammengefasst; die Wurzel (`state_root`) steht im Blockheader. So kann jeder
Node den State-Übergang verifizieren und ein neuer Node den State per Proof
nachladen.

---

## 3a. Kryptografische Primitive und Hashkette

Alle hier festgelegten Algorithmen sind **konsens-kritisch** — jede Abweichung
zwischen zwei Nodes führt zu unterschiedlichen Hashes/Adressen und damit zum
Chain-Split. Sie sind ab Genesis fix; Änderungen wären ein Hard Fork.

**Hash-Funktion: BLAKE3-256** (256-Bit-Ausgabe) — **einheitlich im gesamten
System**: Chain (Block-/Tx-/Merkle-/State-Hashes) UND FileStore
(Content-Adressen) UND Identitäts-/Grid-/Meter-/Topology-Ableitungen.
- Begründung: moderner, schneller Hash; intern selbst ein Merkle-Baum; auf
  großen Eingaben (Datei-Inhalte, Container) deutlich schneller dank
  Parallelismus.
- Go-Abhängigkeit: `lukechampine.com/blake3` (pure-Go, portabel auf aarch64,
  kein CGo/Assembly nötig). Ein zentraler Wrapper pro Subsystem.
- Hinweis: Die früher genutzten BLAKE2b-Hashes wurden vollständig auf BLAKE3
  umgestellt (Bestand waren nur Testdaten). Es gibt keine zwei Hash-Welten mehr.

**Signaturen:**
- **Transaktionen: secp256k1 (ECDSA)**, 65-Byte-Signatur (r,s,v mit Recovery-ID
  → Public Key aus Signatur ableitbar). Konsistent mit der FND-Wallet. Der zu
  signierende Digest ist `BLAKE3-256(canonical_tx_ohne_signature)`.
- **Konsens-Voten (Prevote/Precommit/Block-Commit): ed25519.** Schnelle
  Batch-Verifikation — bei vielen Validator-Signaturen pro Block ein realer
  Vorteil. Validatoren haben dafür einen ed25519-Konsens-Schlüssel (aus dem
  Seed ableitbar, getrennt vom secp256k1-Wallet-Schlüssel).
- Rationale für den Split: Tx brauchen Recoverability + Wallet-Kompatibilität
  (secp256k1); Konsens braucht schnelle Massen-Verifikation (ed25519).

**Adressen:** `address = erste 20 Bytes von BLAKE3-256(secp256k1_pubkey)`.
- 20 Bytes wie gängige Tooling-Konvention (kompakt). Bei Bedarf an höherer
  Kollisionsresistenz wären volle 32 Bytes möglich — 20 ist der bewusste
  Kompromiss.
- **Wichtig:** Das weicht von der bisherigen FND-Wallet-Adresse ab, die über
  `crypto.PubkeyToAddress` (Keccak-256) erzeugt wird. Da Frischstart (§9), ist
  das unkritisch — aber `DeriveAddressFromSeed` muss auf BLAKE3 umgestellt
  werden (siehe §13), damit Wallet und Chain dieselbe Adresse ergeben.

**Block-Hashkette (das „Chain" in Blockchain):** Jeder Block verweist über
`prev_hash = chainHash(canonical_prev_header)` auf seinen Vorgänger. Eine
Änderung an irgendeinem alten Block ändert dessen Hash und bricht alle
nachfolgenden `prev_hash` → Manipulation an der Historie ist sofort erkennbar.
Der `header_hash`, über den die Validatoren signieren, ist
`chainHash(canonical_header)`.

**Merkle-State-Baum:** Binärer Merkle-Baum über die nach `address` sortierten
Accounts (+ Contract-/Validator-State). Mit **Domain-Separation** gegen
Second-Preimage-Angriffe:
```
leaf_hash     = chainHash(0x00 || key || value)
internal_hash = chainHash(0x01 || left_hash || right_hash)
state_root    = Wurzel-Hash  (steht im Blockheader)
```
Das Prefix-Byte (0x00 Blatt / 0x01 Knoten) verhindert, dass ein Blatt als
innerer Knoten missdeutet werden kann. Der `state_root` erlaubt jedem Node, den
Zustandsübergang zu verifizieren, und einem neuen Node, den State per
Merkle-Proof selektiv nachzuladen.

**Kanonische Serialisierung:** Hashing und Signieren erfordern eine
**deterministische** Byte-Darstellung — sonst bauen zwei Nodes verschiedene
Hashes aus demselben logischen Objekt. Regeln: feste Feldreihenfolge,
längen-präfixierte variable Felder, Integer big-endian fester Breite, **keine**
Maps/ungeordneten Strukturen im serialisierten Bild. Eine zentrale
Encode/Decode-Funktion pro Typ.

**KDF (Seed → Schlüssel): Argon2id**, wie im bestehenden System
(`identity.go`), mit den dort verankerten Parametern. Aus dem Seed werden
deterministisch beide Schlüssel abgeleitet: secp256k1 (Wallet/Tx) und ed25519
(Konsens, nur für Validatoren relevant).

## 4. Transaktionsformat

```
Transaction {
  type      : uint8       // siehe Tabelle unten
  from      : 20 Bytes
  nonce     : uint64      // muss == Account.nonce sein
  fee       : uint128     // in uFND = 1,8 % des bewegten Werts (berechnet, §8); uint128, da 1,8 % eines uint128-Betrags uint64 sprengen kann
  payload   : bytes       // typ-abhängig (z.B. {to, amount} bei Transfer)
  signature : 65 Bytes    // secp256k1 (ECDSA, recoverable) über BLAKE3-256(Felder außer signature), siehe §3a
}
```

**Transaktionstypen (Phase 1):**

| type | Name | payload | Wirkung |
|---|---|---|---|
| 0x01 | `transfer` | to, amount | from→to, Saldo verschieben |
| 0x02 | `stake` | amount | FND als Validator-Stake sperren |
| 0x03 | `unstake` | amount | Stake entsperren (mit Verzögerung) |
| 0x10 | `escrow_open` | seller, arbiter, amount, deal_id | Escrow-Konto anlegen, Betrag sperren |
| 0x11 | `escrow_release` | deal_id | Freigabe an Verkäufer (braucht 2-von-3 Sigs) |
| 0x12 | `escrow_refund` | deal_id | Rückzahlung an Käufer (braucht 2-von-3 Sigs) |
| 0x20 | `storage_proof` | provider, container_id, nonce, proof | Storage-Proof eines Hosters (→ Anteil am Miet-Pool §7c) |
| 0x21 | `sol_credit` | to, amount, sol_txsig | FND-Gutschrift nach SOL-Einzahlung |
| 0x22 | `commodity_settle` | meter, commodity, reading_a, reading_b | Rohstoff-Verrechnung aus 2 signierten Zählerständen (§7b) |
| 0x30+ | Vertrags-Templates | siehe §7a | Kaufvertrag, Miete, Abo, Teilzahlung, Arbeitsvertrag |
| 0x40 | `container_create` | content_hash, size, price_per_dl, encrypted, prepaid | Bezahlte persistente Datei anlegen, expiry setzen (§7c) |
| 0x41 | `container_renew` | container_id, prepaid | Miete nachzahlen → expiry verlängern |
| 0x43 | `container_download` | container_id | Download bezahlen (price_per_dl → owner), löst Key-Freigabe aus |
| 0x44 | `container_expire` | container_id | Abgelaufenen Container (now > expiry) als EXPIRED markieren (→ GC) |

Typ 0x21 (`sol_credit`) ist die einzige **autorisierte Mint-Operation** — sie
bringt FND aus einer SOL-Einzahlung in Umlauf und darf nur unter den Bedingungen
in §7.4 ausgeführt werden. Typ 0x20 (`storage_proof`) erschafft KEIN FND, sondern
weist Hosting nach und steuert die Verteilung des nutzerfinanzierten Miet-Pools
(§7.3, §7c).

**Gültigkeitsregeln (jede Tx, von jedem Validator geprüft):**
1. Signatur gültig, `from` = Adresse des Signierschlüssels.
2. `nonce` == aktueller `Account.nonce` (kein Replay, keine Lücke).
3. `balance >= fee + (ausgehender Betrag)`.
4. Typ-spezifische Regeln erfüllt (siehe §7).

---

## 5. Blockformat

```
BlockHeader {
  height       : uint64
  prev_hash    : 32 Bytes   // Hash des Vorgängerblocks
  timestamp    : uint64      // Unix-Sekunden (Median der Validator-Zeiten)
  proposer     : 20 Bytes    // Validator, der den Block vorschlug
  tx_root      : 32 Bytes    // Merkle-Wurzel der Transaktionen
  state_root   : 32 Bytes    // Merkle-Wurzel des States NACH Anwendung der Txs
  val_set_hash : 32 Bytes    // Hash der aktiven Validator-Menge
}

Block {
  header        : BlockHeader
  transactions  : Transaction[]   // deterministisch geordnet (siehe unten)
  commit        : Signature[]      // >= 2/3 Validator-Signaturen (ed25519, §3a) über header_hash
}
```

**Transaktions-Ordnung im Block:** deterministisch nach (from-Adresse
aufsteigend, dann nonce aufsteigend). Das garantiert die Nonce-Reihenfolge je
Konto (nonce 5 vor 6) UND ist über alle Validatoren identisch → reproduzierbarer
Block-Hash und state_root. Eine Gebühren-Ordnung wäre hier falsch (sie könnte
Nonces eines Kontos vertauschen); Gebühren-Priorisierung gehört in die
Mempool-Auswahl, BEVOR der Block-Inhalt feststeht.

---

## 6. Konsens (BFT, Tendermint-artig)

Runden-basiert. Pro Höhe `H` gibt es einen rotierenden **Proposer** (aus der
Validator-Menge, gewichtet nach Stake). Eine Runde besteht aus drei Phasen:

```
1. PROPOSE   Proposer baut Block, signiert, broadcastet ihn.
2. PREVOTE   Jeder Validator prüft den Block (alle Txs, state_root) und
             broadcastet PREVOTE(block_hash) — oder PREVOTE(nil) bei Fehler.
3. PRECOMMIT Sieht ein Validator >= 2/3 PREVOTEs für denselben Hash,
             broadcastet er PRECOMMIT(block_hash).
4. COMMIT    Sieht ein Validator >= 2/3 PRECOMMITs, ist der Block FINAL.
             Die gesammelten Precommits bilden das `commit`-Feld.
```

**Finalität:** Anders als bei Nakamoto/PoW (probabilistisch) ist ein Block hier
**sofort final**, sobald er committed ist — keine Reorgs, keine
Bestätigungs-Wartezeit. Das vereinfacht die State-Machine enorm.

**Proposer-Ausfall / Timeouts:** Antwortet der Proposer nicht rechtzeitig
(Timeout pro Phase), erhöhen die Validatoren die Rundennummer und der nächste
Validator wird Proposer. So bleibt die Chain bei Ausfällen lebendig, solange
> 2/3 der Validatoren ehrlich und erreichbar sind.

**Sicherheitsgrenze:** Solange < 1/3 des Stakes bösartig ist, ist kein
Double-Spend und kein widersprüchlicher Block möglich. Das ist die harte
BFT-Garantie — und der Grund, warum die Schwelle ein **Anteil** (2/3) ist und
keine feste Zahl. Bei wenigen Validatoren prüft schlicht jeder jede Tx (billig).

---

## 7. State-Machine — die „Smart Contracts"

Jeder Transaktionstyp ist ein deterministischer Zustandsübergang. Alle
Validatoren wenden sie identisch an; der resultierende `state_root` muss
übereinstimmen, sonst wird der Block abgelehnt.

### 7.1 Transfer (0x01)
`balance[from] -= amount + fee; balance[to] += amount; nonce[from]++`.
Gebühr siehe §8.

### 7.2 Escrow (0x10/0x11/0x12) — 2-von-3 ohne externe Logik
- `escrow_open`: Käufer sperrt `amount`. Ein Escrow-Eintrag entsteht:
  `{deal_id, buyer, seller, arbiter, amount, state=OPEN}`. Der Betrag verlässt
  das Käufer-Konto und liegt im Escrow-State (nicht bei einer Partei).
- `escrow_release` / `escrow_refund`: Die Transaktion trägt **zwei** Signaturen
  aus der Menge {buyer, seller, arbiter}. Normalfall: buyer+seller geben frei.
  Streitfall: arbiter+eine Partei. Bei `release` → Betrag an seller, bei
  `refund` → an buyer. State → CLOSED.

Das ist ein 2-von-3-Multisig, das *durch den Konsens erzwungen* wird — kein
Vertrauen in eine einzelne Partei nötig, kein externer Vertrag.

### 7.3 Storage/Bandbreiten-Vergütung — nutzerfinanziert, KEIN Inflations-Mint
Hoster werden fürs Bereitstellen von Speicher/Bandbreite vergütet, aber das Geld
kommt aus **Nutzer-Zahlungen** (Container-Miete §7c, Download-Preise), nicht aus
protokoll-emittiertem Neugeld. Das vermeidet unkontrollierte Inflation.

Das bestehende `FileStorage.sol`-Konzept (Protokoll-Mining via `mintStorage`/
`mintBandwidth`) liefert die **technische Grundlage** — vor allem die
**Storage-Proofs** (Challenge-Response: Provider muss `Hash(nonce + chunkHash)`
fristgerecht liefern, Fake-Storage-Schutz). Diese Proof-Logik wandert in die
State-Machine und entscheidet, **welche** Hoster aus dem Miet-Reward-Pool (§7c)
bezahlt werden. Das *Minten als Belohnung* entfällt; die Proofs bleiben als
Nachweis-Mechanik erhalten.

> Designentscheidung: Ein einziges Geld-Modell fürs Filesharing — der Uploader
> bezahlt die Hoster (Miete), Downloader bezahlen den Uploader (Download-Preis).
> Kein paralleler Inflations-Mint, damit Hoster nicht doppelt vergütet werden.

### 7.4 SOL-Credit (0x21) — die On-Ramp-Brücke (Vertrauenspunkt!)
SOL-Einzahlung gutschreiben heißt, ein *externes* Ereignis (Solana-Zahlung) auf
unsere Chain zu bringen. Das ist ein **Bridge-/Oracle-Problem** und der
ehrlichste Schwachpunkt:
- Die Validatoren beobachten gemeinsam die SOL-Empfangsadresse. Eine
  `sol_credit`-Tx ist nur gültig, wenn **>= 2/3 der Validatoren** dieselbe
  Solana-Transaktion (`sol_txsig`) bestätigt haben und sie noch nicht
  gutgeschrieben wurde (Doppel-Credit-Schutz via verbrauchter `sol_txsig`-Liste
  im State).
- Vertrauensannahme: Die Validator-Mehrheit beobachtet Solana ehrlich. Das ist
  schwächer als der Rest der Chain — Bridges sind notorisch das Angriffsziel.
  Alternative später: ein Solana-Light-Client zur Verifikation (komplex).

### 7.5 Stake/Unstake (0x02/0x03)
- `stake`: sperrt FND, fügt `from` der Validator-Menge hinzu (ab Mindest-Stake).
- `unstake`: entsperrt nach einer **Unbonding-Periode** (z.B. ~2 Wochen
  Blockzeit) — verhindert, dass ein Validator kurz vor einem Angriff aussteigt
  und dem Slashing entgeht.

---

## 7a. Vertrags-Templates (Kaufverträge, Miete, Abo, Arbeitsverträge)

Statt einer Turing-vollständigen VM gibt es einen **festen Satz von
Vertrags-Templates**. Jedes Template ist eine im Protokoll definierte
State-Machine mit (a) festen Parametern, die der Nutzer befüllt, und (b) klar
definierten erlaubten Zustandsübergängen. So sind beliebige reale Verträge
abbildbar, ohne die Risiken eigener Vertragssprache (Gas-Metering, Sandboxing,
Exploits).

**Gemeinsame Struktur jedes Vertrags (im State):**
```
Contract {
  id          : 32 Bytes      // eindeutig (hash aus opener + nonce + typ)
  template    : uint8         // welcher Vertragstyp
  parties     : address[]     // Beteiligte (z.B. Käufer, Verkäufer, Arbiter)
  params      : bytes         // template-spezifische Parameter (s.u.)
  locked      : uint128       // gesperrte FND (Escrow/Kaution/Gehaltspuffer)
  state       : uint8         // OPEN / ACTIVE / FULFILLED / DISPUTED / CLOSED
  created_at  : uint64        // Blockhöhe
  next_action : uint64        // Blockhöhe der nächsten fälligen Aktion (z.B. Rate)
}
```

Vertrags-Aktionen sind eigene Transaktionstypen (0x30+), die — wo nötig — die
erforderlichen Signaturen tragen (analog zum 2-von-3-Escrow).

**Template 0x30 — `escrow` / Kaufvertrag (Lieferung/Frist/Schiedsspruch)**
- params: `amount, deadline_block, return_window, delivery_required(bool)`.
- Ablauf: Käufer `open` (sperrt amount) → Verkäufer liefert → Käufer
  `confirm_delivery` (Freigabe an Verkäufer) ODER bei Streit `dispute` →
  Arbiter entscheidet (`release`/`refund`, 2-von-3). Läuft `deadline_block`
  ohne Lieferung ab → automatischer Refund-Anspruch des Käufers.
- Deckt deinen „Kaufvertrag" voll ab: Lieferpflicht, Frist, Rückgabefenster,
  Schiedsspruch.
- **Anbindung an die bestehende Marktplatz-Vorschau:** Der vorhandene
  Kaufvertrag (`contract.go`, `escrowCreate`/`contractHTML`) hat bereits genau
  die passenden Felder. Abbildung auf das Template:
  - `BuyerWallet`/`SellerWallet` (+ Arbiter) → `parties`
  - `PriceFND`/`FeeFND`/`NetFND` → `locked` (gesperrter Betrag) + Gebühren
  - `FreezeDeadline` (14 T) / `ReturnDeadline` (28 T) → `deadline_block` /
    `return_window` (in Blockhöhen umgerechnet)
  - `Status`/`CancelReason` → die `state`-Maschine
  - `Title`/`Description`/`Condition`/`Category` → **bleiben off-chain**
    (Metadaten), nur per `ContentHash` im Vertrag verankert.
  > **Wichtiger Designpunkt:** Nur die Geld-/Zustandslogik (Beträge, Fristen,
  > Parteien, Freigabe-Bedingungen) kommt in den konsens-kritischen State. Lange
  > Texte/Warenbeschreibungen bleiben off-chain und werden per Hash referenziert
  > — hält den State klein und deterministisch. Die UI-Vorschau zeigt weiterhin
  > die volle Darstellung, indem sie On-Chain-Vertrag + Off-Chain-Metadaten
  > zusammenführt.

**Template 0x31 — `rental` (Miete mit Kaution)**
- params: `deposit, rent_per_period, period_blocks, start_block, end_block`.
- Ablauf: Mieter sperrt Kaution + erste Rate. Pro Periode wird automatisch die
  Rate an den Vermieter fällig (eine `rental_charge`-Tx, die jeder Validator
  gegen `next_action` prüfen kann). Am Ende: Kaution zurück (oder bei Schaden
  per 2-von-3 einbehalten).

**Template 0x32 — `subscription` / Abo (wiederkehrend, kündbar)**
- params: `amount_per_period, period_blocks, cancel_notice_blocks`.
- Ablauf: pro Periode wird `amount_per_period` vom Abonnenten an den Anbieter
  fällig. Kündbar mit Frist (`cancel`); ohne Deckung → Abo pausiert/endet.
  Keine feste Endhöhe (läuft bis Kündigung).

**Template 0x33 — `installment` / Teilzahlung & gestaffelte Freigabe (Milestones)**
- params: `total, milestones[]{amount, condition_ref}`.
- Ablauf: Gesamtbetrag gesperrt; pro Meilenstein wird bei Bestätigung
  (`milestone_confirm`, je nach Vertrag durch Käufer oder 2-von-3) die jeweilige
  Tranche freigegeben. Für Projektarbeit/Etappenzahlungen.

**Template 0x34 — `employment` / Arbeitsvertrag (an Jobs-Modul gekoppelt)**
- params: `job_ref(optional, Hash der Job-Ausschreibung), salary_per_period,
  period_blocks, start_block, end_block(0 = unbefristet), notice_blocks,
  prefund_periods`.
- Ablauf: Arbeitgeber legt den Vertrag an und sperrt im Voraus `prefund_periods`
  × `salary_per_period` (Gehaltspuffer — garantiert dem Arbeitnehmer, dass
  mindestens N Perioden gedeckt sind). Pro Periode wird das Gehalt automatisch
  fällig (`salary_pay`-Tx, Validatoren prüfen gegen `next_action`). Beide Seiten
  können mit Frist (`notice_blocks`) kündigen (`terminate`). `job_ref`
  verknüpft den Vertrag optional mit der bestehenden Job-Ausschreibung
  (Listing-Hash) — kein Umbau des Jobs-Moduls nötig, nur eine Referenz.
- Hinweis: Wiederkehrende Zahlungen (Miete/Abo/Gehalt) sind technisch dasselbe
  Muster — eine pro-Perioden-Fälligkeit gegen einen Vorab-gesperrten Puffer.
  Wir implementieren das Muster einmal und parametrisieren es je Template.

**Wiederkehrende Fälligkeiten — wie ohne „Cron" auf einer Chain?**
Eine Chain hat keinen Timer. Lösung: Jeder Vertrag trägt `next_action`
(Blockhöhe). Wird diese Höhe erreicht, darf **jeder** (typischerweise der
Begünstigte oder ein Validator) die fällige Aktion als Tx einreichen; die
State-Machine erlaubt sie nur, wenn `current_height >= next_action` und der
Puffer gedeckt ist, und setzt `next_action += period_blocks`. Validatoren
verifizieren das deterministisch. Keine Hintergrund-Magie, kein Vertrauen nötig.

> **Grenzen ehrlich:** Diese Templates decken die gängigen Verträge eines
> Marktplatzes + Jobs-Moduls ab. Was sie NICHT können: völlig freie, vom Nutzer
> programmierte Logik (z.B. ein exotischer Derivate-Vertrag). Sollte das je nötig
> werden, wäre das der Zeitpunkt, über eine eingeschränkte, deterministische VM
> nachzudenken — ein großes eigenes Projekt mit eigener Sicherheitsanalyse, klar
> nach Phase 1 verortet.

## 7a-bis. Streitschlichtung & Jury (Dispute Resolution)

Ein Escrow (0x30) kann optional gegen Streit **abgesichert** werden. Ohne
Absicherung gilt nur das Frist-/Bestätigungs-Modell (Käufer bestätigt → Freigabe;
Frist abgelaufen → Refund) — kein Mensch muss je urteilen. Mit Absicherung greift
im Streitfall eine Jury.

**Grundprinzip (bewusst gewählt):**
- **Absicherung ist optional** und wird beim `open` gesetzt (`insured=true`).
  Käufer ODER Verkäufer kann sie verlangen; bezahlt wird sie von dem, der den
  Escrow eröffnet (Käufer), als Teil des gesperrten Betrags.
- **Prämie = 1,8 % des Betrags, zusätzlich** zur normalen Tx-Gebühr. Sie fließt
  in einen **Jury-Pool** des Escrows.
- **Die Jury wird aus der Prämie bezahlt — auch wenn KEIN Streit stattfindet.**
  Die Prämie ist eine Versicherungsprämie: weg ist weg, ob eingelöst oder nicht.
  Bei reibungslosem Abschluss (confirm/Frist) wird der Pool an die vorgesehenen
  Juroren ausgeschüttet (Bereitschaftsentgelt).
- **Verursacher trägt die Kosten — wirtschaftlich, nicht als Zusatzstrafe.** Es
  gibt KEINE separate Strafzahlung obendrauf (on-chain lässt sich nur das
  Gesperrte umverteilen, nicht mehr). Stattdessen: Der Verlierer des Disputes
  trägt die Prämie wirtschaftlich — der Gewinner wird so gestellt, als hätte er
  sie nie gezahlt (seine Seite wird um die Prämie entlastet, die Prämie bleibt
  beim Jury-Pool).

**Ablauf mit Absicherung:**
1. `open` mit `insured=true` → gesperrt wird `amount + premium(1,8 %)`; die Prämie
   geht in den Jury-Pool des Escrows.
2. Normalfall (kein Streit): `confirm` → `amount` an Verkäufer, Pool an Juroren.
   Oder Frist → `refund`: `amount` an Käufer, Pool an Juroren.
3. Streitfall: eine Partei reicht `dispute` (0x33) ein. Die Jury stimmt ab
   (`juror_vote`, 0x34): Mehrheit für `release` (an Verkäufer) oder `refund` (an
   Käufer). Bei Mehrheit: `amount` an die Gewinnerseite, Pool an die Juroren, die
   mit der Mehrheit stimmten.

**Juror-Auswahl (Abhängigkeit ehrlich benannt):** Die faire Variante zieht
Juroren **zufällig und stake-gewichtet aus der Validator-/Staker-Menge**
(Kleros-Prinzip). Das setzt **Staking (Phase 4)** voraus — vorher gibt es keine
Menge, aus der gelost werden kann. Bis dahin wird die Juroren-Menge beim `open`
**explizit benannt** (z.B. ein fester Schlichter-Pool); die stake-gewichtete
Zufallsauswahl ersetzt das später, ohne die Tx-Formate zu ändern.

**Bestechungsschutz (späteres Härtungsthema):** Offene Stimmen sind bestechbar.
Die gehärtete Variante nutzt **Commit-Reveal** (Juror committet `hash(vote||salt)`,
reveal erst nach Ablauf der Commit-Phase) plus **Stake-Slashing** für Juroren,
die von der Mehrheit abweichen. Phase-1-Stub: offene Stimmen, Mehrheit zählt;
Commit-Reveal + Slashing kommen mit Phase 4.

**Messenger-Benachrichtigung:** Wird ein Escrow mit Absicherung eröffnet oder ein
`dispute` eingereicht, sollen die betroffenen Juroren über den Messenger
benachrichtigt werden (Hook: Event `escrow.dispute_opened` → Empfänger =
Juroren-Adressen → Messenger-Nachricht mit Escrow-ID + ContentHash-Anker). Die
On-Chain-Mechanik triggert das Event; die Zustellung ist Off-Chain-Integration
(eigene Test-fähige Runde, nicht konsens-kritisch).

**Tx-Typen Dispute/Jury:**
- `0x33 dispute` — Partei eröffnet Streit (nur Käufer/Verkäufer eines `insured`
  Escrows, nur solange OPEN).
- `0x34 juror_vote` — benannter Juror stimmt für `release` oder `refund`.



Das bestehende Echtzeit-Verrechnungssystem (elektrische Leistung; perspektivisch
Gas, Wasser, Öl) erzeugt Messdaten in **Sekundenauflösung** (`meter/reader.go`).
Diese gehören NICHT als Einzel-Transaktionen auf die Chain — eine Chain ist ein
Konsens-Medium, kein Zeitreihen-Logger.

**Die Mengenrechnung (warum):** Eine signierte Tx ist ~150 Bytes. Eine Messung
pro Sekunde pro Zähler = 86.400 Tx/Tag ≈ 13 MB/Tag/Zähler ≈ 4,7 GB/Jahr/Zähler.
Bei 1.000 Zählern × 5-fach-Redundanz ≈ 23 TB/Jahr und ~1.000 Tx/s allein für
Messdaten. Nicht tragbar.

**Lösung — Messung von Verrechnung trennen (wie ein echter Zähler):**

1. **Messung bleibt off-chain, sekundengenau.** Volle Auflösung beim Node /
   im FileStore (redundant). Jederzeit beweisbar.
2. **Zähler signiert KUMULATIVE Stände**, keine Deltas:
   ```
   MeterReading {
     meter_id   : bytes11      // Zähler-Seriennummer
     commodity  : uint8        // 0=Strom 1=Gas 2=Wasser 3=Öl … (wie EnergyToken.sol)
     timestamp  : uint64        // Unix-Sekunden
     cumulative : uint128       // monoton steigender Zählerstand in SI-Basiseinheit
     signature  : 65 Bytes      // Zähler-/Node-Schlüssel
   }
   ```
   Monoton steigend = fälschungssicher (kein Zurückdrehen). SI-Basiseinheiten als
   **Ganzzahl**: Strom **Ws** (Wattsekunden), Gas/Öl **g**, Wasser **ml** — frei
   erweiterbar für künftige Rohstoffe. Niemals Float (Konsens-Determinismus!).
3. **On-Chain nur die Verrechnung pro Abrechnungsintervall** (Minute/Stunde/
   Liefervorgang): die Tx `commodity_settle` (0x22) trägt zwei signierte Stände
   `reading_a` (Anfang) und `reading_b` (Ende). Die Chain verbucht die
   **Differenz** `cumulative_b − cumulative_a` als gelieferte Menge, rechnet sie
   per hinterlegtem Tarif in FND um und führt den Zahlungsfluss aus
   (Verbraucher → Erzeuger). So werden aus 86.400 Tx/Tag z.B. 24 (stündlich)
   oder 1.440 (minütlich) — Faktor 60–3.600 weniger.

**Validierung von `commodity_settle`:** Beide Stände signiert & vom selben
`meter_id`; `timestamp_b > timestamp_a`; `cumulative_b >= cumulative_a` (Monotonie);
`reading_a` == der zuletzt verbuchte Endstand dieses Zählers (lückenlose Kette,
kein doppeltes Abrechnen). Optional: der `meter_id` ist im State einem
Erzeuger-Account zugeordnet (Registrierung), damit nicht jeder beliebige Stände
einreicht.

> **Granularität ist frei wählbar pro Anwendungsfall:** Sekundengenaue
> Nachweise existieren off-chain weiter; die Chain bestimmt nur, wie oft
> abgerechnet wird (Tarif-/Vertragssache). Disput? Der sekundengenaue
> Off-Chain-Log (per Hash verankerbar) belegt jedes Intervall.

Das passt zum bestehenden `EnergyToken.sol`-Denkmodell (kumulative `quantity` in
Basiseinheiten, `mintBatch` für Zeitreihen, commodity-Typen 0–3 + Edelmetalle).
Die Token-Logik wandert von Solidity in diese State-Machine; die
Sekundenmessung im Go-`meter`-Modul bleibt unverändert.

## 7c. Container — bezahlte persistente Dateien auf der Chain

Ein **Container** ist eine Datei, deren Fortbestand im Netz der Uploader durch
eine **Tagesmiete** bezahlt. Er kann optional einen **Preis pro Download**
festlegen. Reicht das Miet-Guthaben nicht mehr, läuft der Container ab und wird
von den Nodes gelöscht. Die Datei-Bytes liegen im bestehenden FileStore
(5-fach-Redundanz); die Chain ist die Quelle der Wahrheit über Existenz,
Bezahlung und Lösch-Status.

> **BLAKE3 zahlt sich hier aus:** Die `content_hash` ist BLAKE3-256 des
> *gesamten* Datei-Inhalts — eine große Eingabe, bei der BLAKE3s Parallelismus
> echten Geschwindigkeitsvorteil bringt (anders als bei den kleinen
> Konsens-Objekten). Integration: Container-Dateien werden per BLAKE3 adressiert;
> der Container-State mappt die BLAKE3-Datei-ID auf das FileStore-Manifest
> (dessen Chunks jetzt ebenfalls BLAKE3 nutzen — einheitlich im ganzen System).

**Container-State (on-chain):**
```
Container {
  id           : 32 Bytes    // = BLAKE3-256 des Datei-Inhalts (Content-Adresse)
  owner        : 20 Bytes    // Uploader-Wallet
  size         : uint64       // Bytes (Basis der Mietberechnung)
  expiry       : uint64        // Unix-Sekunden: Ablaufzeitpunkt (danach Löschung)
  price_per_dl : uint128       // uFND pro Download (0 = gratis), fließt an owner
  encrypted    : bool          // Chiffretext gespeichert (nötig für bezahlte Downloads)
  state        : uint8         // ACTIVE / EXPIRED
  created_at   : uint64
}
```

**Miet-Tarif (fest):** **1 FND pro Jahr und GB**, im **Voraus** zu entrichten.
Die bezahlte Summe bestimmt, wie weit der Ablaufstempel in die Zukunft gesetzt
wird:
```
gezahlte_dauer (Sekunden) = prepaid_FND / (size_in_GB × 1 FND) × 31_536_000
```
Beispiel: 2 GB, 3 FND vorausbezahlt → 3 / 2 = 1,5 Jahre Laufzeit.

**Lebenszyklus (Ablaufstempel statt täglicher Abbuchung — einfacher):**
1. `container_create` (0x40): owner zahlt `prepaid` (≥ Mindestlaufzeit), setzt
   `price_per_dl`/`encrypted`. `expiry = now + gezahlte_dauer`. Die Bytes gehen
   in den FileStore (redundant). Die Miete fließt in den **Hosting-Reward-Pool**
   dieses Containers (s.u.).
2. `container_renew` (0x41): weitere Miete entrichten → `expiry += zusätzliche_
   dauer`. So lange erneuert wird, lebt der Container weiter.
3. `container_expire` (0x44): sobald `now > expiry`, markiert eine Transaktion
   (von jedem einreichbar, deterministisch gegen `expiry` geprüft) den Container
   als `EXPIRED`.
4. **Löschung (off-chain):** Nodes beobachten den Container-State. Bei `EXPIRED`
   (oder sobald sie selbst `now > expiry` sehen) wirft die FileStore-GC die
   lokalen Kopien weg. Die Chain stellt den Status fest; die Nodes setzen ihn
   lokal durch — kein zentraler Löschbefehl.

**Node-Entlohnung fürs Hosting (Storage-Belohnung):** Die Miete ist die
Vergütung der Nodes, die die Datei vorhalten. Sie fließt nicht an Treasury/Burn,
sondern an die **Hosting-Provider**.
- Modell: Die vorausbezahlte Miete liegt in einem Reward-Pool und wird über die
  Laufzeit an die hostenden Nodes ausgeschüttet, anteilig nach nachgewiesenem
  Hosting.
- **Ehrliche Kernschwierigkeit:** Faire Ausschüttung erfordert **Storage-Proofs**
  (Proof-of-Retrievability — ein Node beweist periodisch, dass er die Datei
  wirklich speichert, z.B. per Challenge: „liefere Chunk an Offset X mit
  Merkle-Proof"). Ohne Proof könnte ein Node Belohnung kassieren, ohne zu
  speichern. Das ist ein eigenes Subsystem.
  - *v1:* Provider registrieren sich pro Container; periodische
    Challenge-Response-Proofs gegen die im FileStore vorhandenen (BLAKE3-)Chunks;
    bestandene Proofs → anteilige Auszahlung aus dem Pool.
  - Die 5-fach-Redundanz definiert die Soll-Provider-Zahl; mehr als 5 ehrliche
    Hoster teilen sich den Pool, das diszipliniert die Replikation ökonomisch.

Damit ist das Filesharing-Geldmodell vollständig nutzerfinanziert (Uploader →
Hoster, Downloader → Uploader); ein protokoll-emittierter Inflations-Mint
entfällt (siehe §7.3).

**Bezahlter Download (`container_download`, 0x43) — ehrlich über die Grenzen:**
- Problem: In einem P2P-Netz kann man kryptografisch NICHT verhindern, dass ein
  Node, der Klartext-Bytes hält, sie ohne Bezahlung weitergibt.
- Lösung: Bei `price_per_dl > 0` MUSS `encrypted = true` sein. Die Nodes halten
  nur **Chiffretext** (gleiche Client-Verschlüsselung wie `.fnde`/libsodium). Die
  Download-Zahlung kauft den **Schlüssel**, nicht die Bytes.
- Ablauf: `container_download` überweist `price_per_dl` an `owner` (anteilig ggf.
  an Provider, s.u.). Die Zahlung ist on-chain sichtbar → löst die Key-Freigabe
  aus.
- **Key-Freigabe (der harte Teil):**
  - *v1 (pragmatisch):* Der Uploader-Node gibt den Schlüssel an den Zahler frei,
    sobald er die Zahlung on-chain sieht. Setzt voraus, dass der Uploader-Node
    online ist — vertretbar, da er ohnehin als mietzahlender Node läuft.
  - *später (trust-minimiert):* Schlüssel per Threshold-Secret-Sharing (Shamir
    t-von-n) auf die Provider-Nodes verteilt; bei beobachteter Zahlung senden t
    Provider ihre Shares an den Zahler, der den Schlüssel rekonstruiert. Eigenes
    Subsystem, klar nach v1.
- Gratis-Container (`price_per_dl = 0`) brauchen nichts davon — Klartext, freier
  Download.

**Wer bekommt die Miete? (offener ökonomischer Parameter)**
- Ökonomisch korrekt: die Storage-Provider, die die Redundanz-Kopien vorhalten.
  Faire Verteilung erfordert aber **Storage-Proofs** (Proof-of-Retrievability —
  Beweis, dass ein Node die Datei wirklich speichert). Das ist ein eigenes
  hartes Subsystem (knüpft an `FileStorage.sol mintStorage` an).
- *v1:* Miete → Treasury/Burn ODER gleichmäßig an registrierte Provider ohne
  vollen Proof. Echte Storage-Proofs als Folgeschritt.

**Filemanager-Integration:** Im Filemanager wird „als Container veröffentlichen"
eine Option neben dem bestehenden Upload/„🔒 verschlüsseln"/„🔗 teilen": Felder
für Tagesmiete, Download-Preis und Vorauszahlung; bei Download-Preis > 0 wird die
Verschlüsselung erzwungen. Die Anzeige zeigt Rest-Guthaben und voraussichtliche
Lebensdauer (`rent_balance / rent_per_day`).

## 8. Gebühren („Gas" in FND) — feste 1,8 %

- **Gebühr = feste 1,8 % des bewegten Werts** einer werttragenden Transaktion
  (Transfer, Kauf/Escrow-Freigabe, Container-Download, Miet-Zahlung). Beispiel:
  Transfer von 100 FND → 1,8 FND Gebühr. Keine variable Gas-Markt-Logik.
- **Aufteilung der eingenommenen Gebühren:** **50 % an die signierenden
  Validatoren** (anteilig nach Stake), **25 % verbrannt** (deflationär),
  **25 % in die Treasury-Wallet**. (Parameter, §11.)
- **Nicht-werttragende Transaktionen** (Konsens-Voten, `container_charge`/
  `_expire`, `commodity_settle`, Stake-Updates) bewegen keinen FND-Betrag → 1,8 %
  von 0 = 0. Damit das kein Spam-Tor ist: solche Txs werden entweder nur von
  Validatoren/Providern erzeugt (kein offener Einreichungspfad) ODER tragen eine
  minimale Flat-Mindestgebühr (kleiner Spam-Schutz-Parameter, §11). Ehrliche
  Anmerkung: die 1,8 % allein schützen werthaltige Txs vor Spam, nicht die
  wertlosen — daher der Mindestgebühr-Riegel.
- Die Gebühr ist getrennt von der **Container-Miete** (§7c) und vom
  **Download-Preis**: Gebühr → Validatoren/Burn/Treasury (Konsens-Belohnung);
  Miete → Hosting-Nodes (Storage-Belohnung); Download-Preis → Uploader.

> **Zur Sicherheit (frühere „2 Validatoren"-Idee):** Validatoren werden aus
> Gebühren belohnt — aber die *Bestätigung* hängt an der 2/3-Schwelle ALLER
> aktiven Validatoren, nicht an einer festen Zahl. Sonst genügte es, 2
> Validatoren zu korrumpieren, um einen Double-Spend durchzuwinken.

---

## 9. Genesis und Migration

**Genesis-Block (Höhe 0):** definiert Chain-ID, initiale Validator-Menge (deren
Adressen + Anfangs-Stake), initiale FND-Verteilung (z.B. das bisherige Supply an
den Fee-Collector), und alle Parameter aus §11. Fest verdrahtet/signiert.

**Migration von Gnosis: Frischstart.** Neue Chain, FND wird neu verteilt bzw.
über die SOL-On-Ramp erworben und unter Hostern via Miete umverteilt. Kein
Gnosis-Snapshot, keine Salden-Migration — das hält den Genesis einfach und
sauber. Der Genesis-State enthält die initiale Validator-Menge und eine
definierte Anfangs-Verteilung (z.B. ein Start-Supply an den/die Gründer-Wallet(s)
und/oder eine Treasury).

---

## 10. Sicherheitsmodell und bekannte Angriffe (ehrlich)

- **< 1/3 bösartiger Stake:** sicher (BFT-Garantie). **>= 1/3:** Liveness-Stopp
  möglich; **>= 2/3:** Angreifer kontrolliert die Chain. Daher ist die Streuung
  des Stakes über unabhängige Operatoren essenziell.
- **Nothing-at-Stake / Long-Range:** In BFT mit sofortiger Finalität weitgehend
  entschärft, ABER neue/lange offline gewesene Nodes brauchen **Weak
  Subjectivity** — einen vertrauten jüngeren Block-Hash (Checkpoint) als
  Startpunkt, sonst könnten sie auf eine gefälschte alte Historie gelockt werden.
  → Checkpoints periodisch veröffentlichen.
- **Zensur:** Eine Validator-Mehrheit kann Txs ausschließen. Gegenmittel:
  Validator-Vielfalt + Beobachtbarkeit (ausgelassene Txs sind sichtbar).
- **Bridge (SOL-Credit):** schwächster Punkt, siehe §7.4. Bewusst als
  Vertrauensannahme markiert.
- **Slashing-Bedingungen:** (a) Doppel-Signatur (zwei Blöcke gleicher Höhe) →
  schwerer Stake-Verlust; (b) anhaltende Nichterreichbarkeit → milder Abzug.

---

## 11. Offene Parameter (vor Genesis festzulegen)

| Parameter | Vorschlag | Anmerkung |
|---|---|---|
| Blockzeit | 2–5 s | Kompromiss Latenz/Overhead |
| kleinste Einheit | 1 FND = 1e9 uFND | uint-handlich |
| Mindest-Stake Validator | 10 FND (Bootstrap) → 100 FND | aus fundus-admin übernommen |
| BFT-Schwelle | 2/3 | fix (Sicherheit) |
| Hash (alles) | BLAKE3-256 | einheitlich: Chain + FileStore + Ableitungen |
| Tx-Signatur | secp256k1 (recoverable) | wie Wallet |
| Konsens-Signatur | ed25519 | schnelle Batch-Verifikation |
| Adresse | BLAKE3-256(pubkey)[:20] | 20 Bytes |
| Gas-Gebühr | feste 1,8 % des bewegten Werts | Split 50/25/25 |
| Gebühren-Split | 50 % Validatoren / 25 % Burn / 25 % Treasury | anpassbar |
| Flat-Mindestgebühr | klein (Spam-Schutz wertloser Txs) | offen |
| Unbonding-Periode | ~2 Wochen | gegen Exit-vor-Angriff |
| Slashing Doppel-Sig | z.B. 100 % des Stakes | hart |
| Container-Miete | 1 FND / Jahr / GB, im Voraus | fest; setzt expiry-Stempel |
| Container-Ablauf | bei now > expiry → Löschung | Verlängerung per container_renew |
| Miet-Empfänger | Hosting-Nodes (Storage-Belohnung) | Verteilung per Storage-Proof |
| Checkpoint-Intervall | z.B. alle 10 000 Blöcke | Weak Subjectivity |

---

## 12. Implementierungs-Phasen

1. **Block & State-Machine (ohne Netz):** Datenstrukturen, Tx-Validierung,
   Transfer-Logik, Merkle-State, deterministische Block-Anwendung. Lokal
   testbar mit Unit-Tests — kein Konsens nötig.
2. **Single-Node-Chain:** Ein Validator produziert Blöcke (Proposer = immer er).
   Beweist State-Machine + Persistenz End-to-End.
3. **BFT-Konsens (Mehr-Validatoren):** Propose/Prevote/Precommit über GossipSub,
   2/3-Sammlung, Timeouts/Rundenwechsel. Der eigentlich harte Teil.
4. **Stake/Validator-Set-Updates + Slashing.**
5. **Escrow, Billing, SOL-Credit** als Tx-Typen. Dann die **Vertrags-Templates**
   (§7a): zuerst das gemeinsame Vertrags-Gerüst + Escrow/Kaufvertrag (0x30),
   dann das wiederkehrende-Zahlung-Muster (Miete/Abo/Gehalt 0x31/0x32/0x34) und
   Teilzahlung (0x33). Arbeitsvertrag mit optionaler `job_ref` ans Jobs-Modul.
   Sowie **Energy/Commodity-Settlement** (§7b, Tx 0x22): kumulative Zählerstände
   + aggregierte Verrechnung.
6a. **Container** (§7c, Tx 0x40–0x44): zuerst Miete + Existenz + GC-Ablauf
   (nutzt das wiederkehrende-Zahlung-Muster + FileStore-Anbindung); bezahlter
   Download (Verschlüsselung + Key-Freigabe v1) danach; Storage-Proofs und
   Threshold-Key-Sharing als spätere Ausbaustufe.
6. **Genesis-Tooling, Checkpoints, Wallet-Anbindung** (die bestehende
   Wallet-Seite spricht dann diese Chain statt Gnosis).

Jede Phase ist für sich testbar. Phase 1–2 bringen schnell ein lauffähiges,
verifizierbares Fundament; Phase 3 ist der Schwerpunkt.

---

## 13. Auswirkungen auf bestehenden Code

- `go/internal/fnd/client.go` (Gnosis-RPC) → ersetzt durch einen internen
  Chain-Client, der Txs baut/signiert und an den lokalen Node-Mempool gibt.
- `contracts/*.sol` (Solidity) → entfallen; ihre Logik wandert in die
  State-Machine (§7).
- `go/internal/api/contract.go` (Kaufvertrag/Escrow-Vorschau) → bleibt als
  UI-/Metadaten-Schicht, ruft aber statt der HTTP-Escrow-Logik die
  Chain-Vertrags-Txs auf (0x30). Die Vorschau führt On-Chain-Vertrag +
  Off-Chain-Metadaten (per ContentHash) zusammen.
- `shop/sol_watcher.go` → bleibt, speist aber `sol_credit`-Txs statt Gnosis-Mint.
- `meter/reader.go` (Sekundenmessung) → bleibt unverändert; erzeugt zusätzlich
  signierte kumulative Zählerstände, die periodisch per `commodity_settle` (0x22)
  verrechnet werden (§7b). Hochfrequenz-Messdaten bleiben off-chain.
- FileStore + `files.lua` (Filemanager) → neue Option „als Container
  veröffentlichen" (§7c): Tagesmiete, Download-Preis, Vorauszahlung;
  Container-Bytes per BLAKE3 adressiert; bei `EXPIRED` greift die FileStore-GC.
  Bei Download-Preis > 0 wird die Client-Verschlüsselung erzwungen.
- Wallet-Seite/-API → unverändert im UX, zeigt aber Salden/Txs der eigenen Chain.
- `go/internal/identity/identity.go`: `DeriveAddressFromSeed`/
  `DerivePrivateKeyFromSeed` bleiben (secp256k1 aus Seed), aber die
  **Adress-Bildung** wechselt von `crypto.PubkeyToAddress` (Keccak) auf
  `BLAKE3-256(pubkey)[:20]` (§3a), damit Wallet und Chain dieselbe Adresse
  ergeben. Zusätzlich: ed25519-Konsens-Schlüssel aus dem Seed für Validatoren.
- libp2p/GossipSub → neues Topic `fundus.chain` für Tx/Block/Vote-Propagierung.

---

*Status: Entwurf zur Abstimmung. Nach Freigabe → Phase 1 (Block & State-Machine).*

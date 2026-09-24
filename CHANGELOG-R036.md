# FUNDUS R036 — Änderungen gegenüber R035

Zwei Blocker behoben, Stake von Grund auf implementiert. Alles mit Tests, deren
Namen den TODO-IDs entsprechen.

**Achtung, vor dem Deploy lesen:** `StateSchemaVersion` steigt von `0x07` auf
`0x08`. Das ist ein konsens-relevanter Bruch — siehe Abschnitt „Chain-Reset"
ganz unten.

---

## FND-001 · Tick-Timing-Bug behoben

`cmd/fundus-node/main.go`, Produktions-Loop. Die Bedingung war invertiert:

```go
tickInterval := time.Second
if chain.BlockTime == 0 {
    tickInterval = time.Duration(chain.BlockTime) * time.Second  // = 0
}
```

Genau im Fall `BlockTime == 0` wurde das Intervall auf 0 gesetzt, und
`time.NewTicker(0)` panict. Bei `BlockTime = 5` ist der Zweig tot — bis jemand
den Wert konfigurierbar macht. Dann fällt der Node beim Start um, nicht im
Betrieb, was die Ursache gut versteckt.

**Neu:** `cmd/fundus-node/tick.go` mit `tickIntervalFor(blockTimeSeconds uint64)`.
Zusage: Rückgabe immer > 0, über den gesamten uint64-Bereich.

**Tests:** `tick_test.go` — `TestFND_001_TickIntervalNeverZero` prüft den
Wertebereich inklusive `uint64`-Maximum, `TestFND_001_TickerAcceptsResult`
provoziert den Panic tatsächlich, statt über die Zahl zu argumentieren.

---

## FND-002 · Migrations-Wächter — zwei Fehler, einer davon still

Der Onepager nennt den lauten Fall. Beim Lesen kam ein zweiter dazu, der
schwerer wiegt.

### Der laute Fall (`migrate.go`)

Die Entscheidung hing an `meta.json`. Diese Datei wird beim Start neu
geschrieben; steht dort nach einem Fehlstart 0, lautet der Vergleich `0 >= 0`
und die Migration wird als „bereits erledigt" abgelehnt. Das ist die Meldung
*„Blockstore enthält bereits Höhe 0"*.

### Der stille Fall (`blockchain.go`, ~Zeile 81)

Der Wächter schlug nur an, wenn die DB **vollständig** leer war (`!hasAny`).
Sobald der Genesis einmal in `chain.db` lag, war `hasAny` true, der Wächter
schwieg — und der Node startete auf Höhe 0, während die echte Historie
unberührt als JSON in `blocks/` lag. Keine Fehlermeldung, keine Warnung.

Wird danach produziert, entsteht auf dem Genesis eine zweite Kette neben der
ersten. Das ist der Pfad, auf dem Salden verschwinden.

### Was geändert wurde

Beide Wächter fragen jetzt dieselbe reine Funktion. Entscheidungsgrundlage ist
ausschließlich der Vergleich vorhandener Daten: höchste JSON-Blockdatei gegen
höchste DB-Höhe. `meta.json` wird dafür nicht mehr befragt — es bleibt Quelle
für den Head-Hash-Abgleich in `VerifyStoreAgainstMeta`, aber nicht mehr für die
Ja/Nein-Frage.

**Neu:** `internal/chain/migrate_guard.go` — `NeedsMigration`,
`ScanJSONBlockHeight`, `InspectMigration`, Typ `MigrationState`.

**Geändert:** `blockchain.go` (Wächter ersetzt), `migrate.go` (Wächter ersetzt).

**Tests:** `migrate_guard_test.go` — acht Tabellenzeilen, Zeile 4
(*„Genesis in DB, Historie in JSON"*) ist der stille Bug. Dazu das Einlesen
von `blocks/` mit Lücken, Fremddateien und Genesis-allein.

---

## FND-020 bis FND-022 · Stake implementiert

Befund bestätigt: `TxStake` (0x02) und `TxUnstake` (0x03) existierten nur als
Konstanten in `types.go`. Kein Zweig im `applyTx`-Switch, keine Payload, keine
Ableitung des Validator-Sets. War nie funktionsfähig, nicht zerschossen.

### Modell: drei Zustände, nicht zwei

```
Guthaben ──TxStake──▶ gestakt ──TxUnstake──▶ freiwerdend ──Reifung──▶ Guthaben
```

Der Zwischenzustand ist Voraussetzung für Slashing: ohne Sperrfrist entzieht
sich ein Validator jeder Strafe, indem er direkt nach dem Fehlverhalten
unstaked. Gestraft werden kann nur, was noch gebunden ist. Freiwerdender Stake
zählt nicht mehr fürs Validator-Set, bleibt aber bis zur Reifung angreifbar.

`UnbondingPeriod = 720` Blöcke (~1 h bei BlockTime 5 s). Startwert, kein Dogma —
muss länger sein als das spätere Beweisfenster für Slashing.

### Die Reifung hängt an der Höhe

`MatureUnbonding(height)` läuft zu Beginn jedes Blocks, nicht als Transaktion.
Sonst könnte jemand seinen Stake beliebig lange in der Schwebe halten und die
Set-Größe verschleiern. Deterministisch trotz Map: Adressen werden sortiert
abgearbeitet.

**Wichtig:** Der Aufruf steht an identischer Position in `BuildBlock` **und**
`ApplyBlock`. Weicht die Position ab, weicht der State-Root des Produzenten von
dem der Prüfer ab und jeder Block wird abgelehnt.

### Validator-Set aus dem Chain-Zustand

`ValidatorSetFromState(s)` liefert alle Adressen mit aktivem Stake über
`MinValidatorStake` (10 FND, entsprechend der Bootstrap-Phase aus der Roadmap),
kanonisch sortiert. Leeres Set ist ein **Fehler**, kein stiller Leerlauf.

**Noch nicht verdrahtet:** `FUNDUS_VALIDATORS` bleibt vorerst der Weg.
`ValidatorSetFromState` existiert, wird aber von niemandem aufgerufen. Die
Umstellung ist konsens-relevant und gehört in einen eigenen Schritt.

### Dateien

**Neu:** `internal/chain/stake.go` — `StakeRecord`, `StakePayload` mit Codec,
`applyStake`, `applyUnstake`, `MatureUnbonding`, `StakedValidators`,
`ValidatorSetFromState`, `stakesRoot`.

**Geändert:** `state.go` (Feld `stakes`, Init, zwei Switch-Zweige,
`StateSchemaVersion` 0x08, `stakesRoot` in `Root()`), `block.go`
(`MatureUnbonding` in beiden Pfaden).

**Tests:** `stake_test.go`
- Payload-Roundtrip bis uint128-Maximum, Ablehnung falscher Längen
- `TestFND_020_ApplyNeverPanics` — Müll-Payloads gegen beide Tx-Typen. Ein
  Panic beim Anwenden eines fremden Blocks wäre ein Remote-DoS.
- Ablehnungen: Betrag 0, über dem Guthaben, Guthaben deckt Betrag aber nicht
  die Gebühr, Gebühr zu niedrig, falsche Nonce, Unstake über dem Gestakten
- Sperrfrist: ein Block vor Ablauf nichts, bei Ablauf alles
- Nachkündigen stellt die Uhr für den gesamten Topf neu (sonst umgehbar)
- `TestFND_021_Conservation` — Guthaben + Stake + freiwerdend + Collector
  bleibt über jede Folge konstant. Fängt mehr als die Einzelfälle zusammen.
- `TestFND_022_SetIsDeterministic` — Set 50-mal gebaut und verglichen. Go
  iteriert Maps absichtlich zufällig; weicht die Sortierung zwischen Nodes ab,
  weicht die Proposer-Zuordnung ab und die Kette forkt.

---

## Beide Pakete sind betroffen

`go/internal/chain` liegt in beiden ZIPs **byte-identisch**. Ich habe alle
Änderungen in beide Bäume eingespielt und die Gleichheit danach geprüft.

Das ist strukturell eine Falle: jede künftige Chain-Änderung muss doppelt
eingespielt werden, sonst driften Node und Admin auseinander — mit
Konsens-Wirkung, weil beide denselben State-Root berechnen müssen. Ein
gemeinsames Modul oder ein Sync-Schritt im Build wäre die Abhilfe. Nicht in
dieser Revision angefasst, aber notiert.

---

## Chain-Reset erforderlich

`StateSchemaVersion` 0x07 → 0x08, weil der Stake als committeter Merkle-Teilbaum
dazukommt. Jeder State-Root ändert sich damit, auch bei völlig
stake-freien Konten. Die bestehende Kette auf den beiden Pis wird als
inkompatibel erkannt — genau dafür ist das Versionsfeld da, aber es heißt:

1. Node auf beiden Pis stoppen
2. `chain.db`, `meta.json`, `blocks/` sichern (nicht löschen)
3. Frisch starten, Genesis wird neu geschrieben
4. Salden gehen dabei verloren — falls auf der Testkette etwas steht, das
   erhalten bleiben soll, vorher abstimmen

Falls ein Reset nicht in Frage kommt: Stake lässt sich zurückstellen, indem die
zwei Switch-Zweige und `stkRoot` in `Root()` auskommentiert werden. Dann bleibt
`StateSchemaVersion` bei 0x07, und FND-001/FND-002 sind trotzdem drin — beide
ändern den State-Root nicht.

---

## Nicht kompiliert

In der Arbeitsumgebung gab es keine Go-Toolchain und kein Netz. Der Code ist
gegen die echten Signaturen aus R035 geschrieben, aber der erste Lauf bei dir
ist der erste echte Compiler-Durchgang.

```
cd go
go build ./...
go test ./internal/chain/ -run 'FND_0' -v
go test ./cmd/fundus-node/ -run 'FND_0' -v
```

Sinnvolle Gegenprobe für FND-001 und FND-002: die Tests einmal gegen den
R035-Stand laufen lassen. Werden sie nicht rot, prüfen sie nicht das, was sie
prüfen sollen.

---

## Offen, in Reihenfolge

1. **Slashing.** Ohne das ist Stake eine Beitrittshürde, keine Sicherheit. Die
   Sperrfrist ist bereits da, damit Slashing etwas zu greifen hat.
2. **`ValidatorSetFromState` verdrahten** und `FUNDUS_VALIDATORS` ablösen.
3. **Wallet-Entsperrung** (der erste JETZT-Punkt aus R035) — noch offen, weil
   er eine Naht braucht: der Node darf nicht ohne Signierschlüssel hochkommen.
4. **Stake-Gebühr entscheiden.** Aktuell 1,8 % wie beim Transfer. Beim Stake
   wechselt aber kein Wert den Besitzer — das Geld bleibt beim Absender, es
   wird nur gesperrt. Als `StakeFee()` gekapselt: Umstellung ist eine Zeile,
   die Tests bleiben grün.
5. **Stake-Quelle entscheiden.** Guthaben oder nachgewiesener Speicherbeitrag.
   `StakedValidators` liest nur den Betrag; woher er kommt, entscheidet
   `applyStake`. Ein Wechsel tauscht diese eine Funktion, nicht das Modell.

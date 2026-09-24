# Fundus – Release-Checkliste (operativ)

Stand: aktueller Entwicklungsstand nach PoA-Konsens, Root-Helper-Daemon
(Mount/WLAN/Automount) und NAT-Traversal-Werkzeugen.

Diese Liste ist die **konkrete Abnahme-Reihenfolge** vor einem ersten
öffentlichen (oder halb-öffentlichen) Release. Sie ergänzt die strategische
`RELEASE-STRATEGIE.md` (was gehört ins Release) und `TODO.md` (Detailpunkte) um
das **Wie und in welcher Reihenfolge abhaken**.

Jeder Punkt ist markiert:
- **[CODE]** – ohne Live-System prüfbar (Logik, Kompilieren, Unit-Tests).
- **[PI]** – braucht mindestens einen Raspberry Pi (Deploy, Browser, Storage).
- **[2×PI]** – braucht beide Pis gleichzeitig (Konsens, Replikation, Quittungen).
- **[NETZ]** – braucht echtes Internet / Fremdnetz (NAT, Relay, Fernzugriff).

Grundregel bleibt: Ein Feature geht nur **ohne** Baustellen-Band live, wenn es
end-to-end getestet ist. Alles andere bekommt das Band oder wird ausgeblendet.

---

## Phase 0 – Bevor irgendetwas deployt wird

Diese Punkte zuerst, weil sie sonst spätere Tests wertlos machen (ungetesteter
Code auf der Chain = Risiko für echte Salden).

- [ ] **[CODE] `go test ./...` grün.** Der gesamte Go-Code muss lokal einmal
      durch den echten Compiler und die Testsuite. Besonders kritisch: der neue
      **PoA-Konsens** (`internal/chain/consensus.go`, `consensus_test.go`) und
      der **Helper-Daemon** (`cmd/fundus-helper/…`) wurden nur statisch geprüft
      (Klammer-Balance, Imports), **nie kompiliert**. Erwartete Stolpersteine:
      fehlende/überflüssige Imports, Signatur-Mismatches bei geänderten
      Funktionen. → Läuft auf PC oder Pi, nicht im Entwicklungscontainer.
- [ ] **[CODE] `go vet ./...` ohne Befund** (fängt viele Fehler vor dem Deploy).
- [ ] **[CODE] `golangci-lint run`** (falls verfügbar) – Stilfehler und tote
      Variablen.
- [ ] **[CODE] Debug-Logs entfernt/entschärft.** Prüfen, ob in
      `receiptflow.go`, `filestore.go` o.ä. noch laute Debug-Ausgaben stehen,
      die im Produktivbetrieb stören oder Interna leaken.
- [ ] **[CODE] Alle DEBUG-/TEST-Schalter aus.** Keine hartcodierten Test-Seeds,
      keine offenen Debug-Endpunkte, keine „magischen" Gutschriften.

---

## Phase 1 – Basis-Deploy auf einem Pi

Ziel: Ein einzelner Node läuft sauber, die Weboberfläche ist erreichbar, nichts
Sicherheitskritisches ist offen.

- [ ] **[PI] Deploy läuft durch** (`deploy-fundus.ps1 … -Rebuild`). Kein
      Einfrieren, kein Abbruch. Der Rebuild ist Pflicht bei neuem Go-Code.
- [ ] **[PI] Binary-Verifikation.** Nach dem Deploy stichprobenartig prüfen, ob
      die neue Binary wirklich übertragen wurde:
      `sudo strings /opt/fundus/bin/fundus-node | grep -c "PoA-Konsens"` (muss
      > 0 sein). Dasselbe für ein Helper-Merkmal in
      `/opt/fundus/bin/fundus-helper`.
- [ ] **[PI] `fundus-node` ist `active`** (`systemctl status fundus-node`), kein
      Crash-Loop.
- [ ] **[PI] Weboberfläche lädt** über HTTP und HTTPS. Kein 403 auf
      `/static/…` (CSS/JS/Icons) – das war ein bekannter Rechte-Fallstrick.
- [ ] **[PI] Kein Rechte-Rest.** `/opt/fundus/lua` und `/opt/fundus/static`
      sind für `nginx` lesbar; `data/` und `chunks/` gehören `fundus:fundus`.
- [ ] **[PI] Wallet-Grundfunktion.** Wallet öffnen, Saldo anzeigen, eine kleine
      Transfer-Transaktion senden – kommt an, Fee wird abgezogen.
- [ ] **[PI] Datei-Grundfunktion.** Upload → Download derselben Datei →
      Prüfsumme identisch. Auch über HTTPS (Streaming bestätigen).

---

## Phase 2 – Helper-Daemon (Mount / WLAN / Automount)

Der Helper ist optional – ohne ihn läuft der Node normal, nur Mount/WLAN fehlen.
Diese Phase prüft die neue privilegierte Komponente.

- [ ] **[PI] `fundus-helper` ist `active`** und **vor** dem Node gestartet
      (`systemctl status fundus-helper`). Socket vorhanden:
      `ls -l /run/fundus-helper.sock` → Rechte `srw-rw----`, Besitzer
      `root:fundus`.
- [ ] **[PI] Node erkennt den Helper.** Im Admin-Panel erscheinen die Karten
      „WLAN-Verbindung" und „Externe Laufwerke einbinden" (nur sichtbar, wenn
      der Helper läuft).
- [ ] **[PI] Block-Geräte-Scan.** „Geräte suchen" listet angeschlossene
      Platten/USB-Sticks mit UUID, Dateisystem, Größe.
- [ ] **[PI] Mount funktioniert.** Eine externe Platte einbinden → erscheint
      unter `/mnt/fundus-<uuid>`, „Auto"-Badge erscheint. Mit `nosuid,nodev,noexec`
      gemountet (`mount | grep fundus`).
- [ ] **[PI] Automount beim Booten.** Pi neu starten → die eingebundene Platte
      ist nach dem Boot ohne Zutun wieder gemountet. Log:
      „Automount beim Start: N Laufwerk(e)".
- [ ] **[PI] Automount beim Einstecken.** Platte abziehen und wieder
      einstecken → innerhalb weniger Sekunden automatisch wieder gemountet
      (udev-Trigger; spätestens nach ~15 s durch den periodischen Scan).
- [ ] **[PI] Aushängen vergisst.** Explizit „Aushängen" → Platte weg, „Auto"-
      Badge weg. Nach erneutem Einstecken bleibt sie ungemountet (bewusst
      entfernt).
- [ ] **[PI] WLAN-Scan** listet sichtbare Netze mit Signalstärke und
      Sicherheits-Status.
- [ ] **[NETZ] WLAN-Wechsel** (nur wenn gefahrlos möglich – Achtung: die
      IP-Adresse der Weboberfläche ändert sich dabei meist!). Verbindung mit
      Passwort testen, danach Status „Verbunden mit …".
- [ ] **[PI] Sicherheits-Sichtprüfung.** Der Helper akzeptiert nur die festen
      Aktionen; keine offene Kommando-Schnittstelle. Socket nicht per TCP
      erreichbar (`ss -ltn | grep -q fundus-helper` liefert nichts).

---

## Phase 3 – PoA-Konsens auf zwei Pis (der riskanteste Teil)

Erst wenn Phase 0 grün ist (Tests!). Hier entscheidet sich, ob die Chain
dezentral konsistent bleibt.

- [ ] **[2×PI] Validator-Set identisch.** `FUNDUS_VALIDATORS` auf **beiden** Pis
      exakt gleich gesetzt, mit **beiden** Node-Adressen (kommagetrennt). Beide
      Nodes kennen ihre eigene Wallet-Identität.
- [ ] **[2×PI] Beide Nodes finden sich** (peer-count ≥ 1, DHT-Discovery).
- [ ] **[2×PI] Rundenweise Produktion.** Log zeigt „PoA-Konsens aktiv" auf
      beiden; „PoA: Block produziert" erscheint **abwechselnd** – nie beide für
      dieselbe Höhe.
- [ ] **[2×PI] Kette konvergiert.** Beide Nodes zeigen dieselbe Blockhöhe und
      denselben State-Root für gleiche Höhen. Keine Fork.
- [ ] **[2×PI] Unberechtigte Blöcke werden abgelehnt.** Ein Block vom falschen
      Proposer für eine Höhe wird per `VerifyBlockConsensus` verworfen (im Log
      sichtbar).
- [ ] **[2×PI] Salden-Konsistenz.** Nach einigen Transaktionen zeigen beide
      Nodes für alle Adressen denselben Saldo.
- [ ] **[2×PI] Verdienst-Kette (Reward).** Datei Pi-A → Pi-B repliziert →
      Store-Quittung; Datei von Pi-B abrufen → Fetch-Quittung; nach
      Block-Produktion enthält der Block `TxStorageReward` → Provider-Saldo
      steigt. Selbst-Quittung (eigener Chunk) wird **nicht** gezählt.
- [ ] **[2×PI] Fallback-Rotation testen.** Den primären Proposer einer Höhe
      absichtlich stoppen → nach `BlockTime + FallbackTimeout` (Standard: 3×
      BlockTime = 15 s) muss der nächste Validator einspringen und produzieren
      (Log: „PoA: Block produziert" mit `round` > 0). Gestoppten Node wieder
      starten → er synchronisiert auf dieselbe Kette.
- [ ] **[2×PI] Fork-Zähler beobachten.** Nach Netz-Tests `ForkCollisions` prüfen
      (Indikator für konkurrierende Blöcke gleicher Höhe). Bleibt er 0, ist die
      Kette konsistent. Steigt er, Uhren-Sync (NTP) und Konnektivität der
      Validatoren prüfen.
- [ ] **[2×PI] Bekannte Grenzen bewusst.** Der PoA-Konsens ist ein **föderiertes
      Beta**:
      - **Kein Slashing** — ein bösartiger Validator wird nicht bestraft, nur
        überstimmt.
      - **Fallback-Rotation schließt die Liveness-Lücke**, kann aber unter echter
        Netz-Partition oder starkem Uhren-Drift **konkurrierende Blöcke (Fork)**
        erzeugen. Der große Fallback-Puffer (`FallbackTimeout` = 2×BlockTime)
        macht das unwahrscheinlich; erkannte Kollisionen werden gezählt und
        verworfen. **Ein echter Reorg (bereits übernommenen Block ersetzen) fehlt
        noch** — er bräuchte State-Snapshots/Rollback (Future Work). Bis dahin:
        Validatoren an stabilen Anschlüssen mit NTP-Zeitsync betreiben.
      - Validator-Set kommt aus der Config, nicht on-chain.
      → Start mit wenigen vertrauenswürdigen eigenen Nodes, ehrlich als
      „föderiertes PoA-Beta" benennen.

---

## Phase 4 – NAT / Fernerreichbarkeit

Nur nötig, wenn Nodes über verschiedene Standorte/Anschlüsse laufen sollen.

- [ ] **[NETZ] Anschlusstyp klären.** Hat der Heimanschluss eine echte
      öffentliche IPv4 oder CGNAT? (Bei CGNAT ist Hole-Punching oft nicht
      möglich, Relay-Fallback wird kritisch.)
- [ ] **[NETZ] NAT-Status im Node** (`/api/v1/…` NAT-Status): AutoNAT-Ergebnis,
      öffentliche vs. Relay-Adressen, ob ein Circuit-Relay greift.
- [ ] **[NETZ] Discovery über Fremdnetz.** Pi am Hotspot (CGNAT) – findet er den
      Heim-Node über die DHT? (Erwartung: Discovery klappt.)
- [ ] **[NETZ] Verbindung über NAT.** Hole-Punching (DCUtR) versuchen; bei
      symmetrischem NAT scheitert es wahrscheinlich → prüfen, ob der
      Circuit-Relay-Fallback trägt.
- [ ] **[NETZ] Relay-Zuverlässigkeit.** Öffentliche libp2p-Bootstrap-Relays sind
      unzuverlässig. Falls Fernbetrieb wichtig ist: **eigenen Relay-Node**
      aufsetzen (auf einem Server mit öffentlicher IP).
- [ ] **[NETZ] Manuelles Verbinden als Diagnose.** `/api/v1/connect` mit einer
      bekannten Multiaddr, um DHT-Discovery zu umgehen und den reinen
      Transport-Pfad zu testen.

---

## Phase 5 – Ehrlichkeits-Kennzeichnung (Baustellen-Bänder)

Bevor Nutzer dran sind: alles, was **nicht** end-to-end getestet ist, sichtbar
markieren.

- [ ] **[CODE/PI] Baustellen-Band** an Features der Kategorie B/C
      (siehe `RELEASE-STRATEGIE.md`): Solana-On-Ramp, Escrow-Split, alles
      Multi-Node-Abhängige, das nicht in Phase 3 grün wurde.
- [ ] **[PI] Solana-Entscheidung.** On-Ramp als Kern (dann stabil + getestet)
      oder Baustelle (Band)? Zentraler Watcher = Single Point of Trust – bewusst
      benennen. `TxSolCredit` (0x21) ist definiert, aber ungenutzt: entfernen
      oder sauber on-chain führen.
- [ ] **[PI] Wallet-Sicherheitshinweise sichtbar.** Seed-Verlust = Verdienst
      unwiderruflich weg. Bei verschlüsseltem Node-Seed: Passwort-Verlust
      bedeutet dasselbe. Diese Warnung muss der Nutzer sehen, bevor er sich
      darauf verlässt.

---

## Phase 6 – Betrieb & Wiederanlauf

- [ ] **[PI] Reboot-Test.** Pi neu starten → alle Dienste kommen hoch
      (`fundus-helper` vor `fundus-node`), Weboberfläche erreichbar, Automount
      greift, Node produziert/synchronisiert wieder.
- [ ] **[PI] Absturz-Erholung.** `fundus-node` killen → systemd startet neu
      (`Restart=on-failure`), Chain-State bleibt konsistent, keine doppelten
      Blöcke.
- [ ] **[PI] Log-Rotation** aktiv (`fundus-logrotate`), Platte läuft nicht voll.
- [ ] **[PI] Speicher-Bereinigung.** Cache-Cleanup
      (`/admin/storage/cleanup?mode=cache`) tastet eigene Dateien/Replikate
      nicht an – nur reinen Download-Cache.
- [ ] **[PI] Backup-Strategie für den Node-Seed.** Der Seed (`node.seed` oder
      `node.seed.enc`) ist die Identität + der Verdienst des Nodes. Sicher
      gesichert? Bei Verschlüsselung: Passwort getrennt und sicher hinterlegt?

---

## Phase 7 – Dezentrale Updates (Git + P2P)

Der Update-Mechanismus verteilt neue Releases über ein **signiertes Manifest**.
Node und Helper prüfen die Signatur gegen die kanonische Fee-Collector-Adresse;
das ZIP wird per Argon2id-Hash verifiziert. Ausgeführt wird als root vom
`fundus-helper` (der gehärtete Node darf selbst nicht nach `/opt/fundus`
schreiben oder neu starten).

### Betreiber-Workflow: ein Release signieren und veröffentlichen

Ablauf, um allen Nodes ein Update auszurollen:

1. **ZIP bauen** wie gewohnt (`deploy-fundus.ps1 -Rebuild` erzeugt die
   `FND R001.zip`, oder das Build-Skript). Das ist die Datei, die verteilt wird.
2. **ZIP in GitHub Releases hochladen.** Die stabile Download-URL des
   Release-Assets ist die spätere `--url` (die `NodeURL` im Manifest).
3. **Manifest signieren** mit dem Admin-Tool (aus der Admin-ZIP, gebaut per
   `build-admin-tools.ps1`). Der Signier-Key ist der **Fee-Collector-Private-Key**
   in einer Datei mit der Zeile `FUNDUS_WALLET_PRIV_KEY=0x…`:

   ```
   fundus-admin sign \
     --version R002 \
     --desc "Kurzbeschreibung der Änderungen" \
     --zip "./FND R002.zip" \
     --url "https://github.com/<user>/<repo>/releases/download/R002/FND-R002.zip" \
     --key-file ./feecollector.key  >  manifest.json
   ```

   Das Tool berechnet den Argon2id-Hash des ZIPs, signiert Version+Hash und
   prüft die Signatur sofort selbst. Ausgabe ist die fertige `manifest.json`.
4. **`manifest.json` ins Git-Repo pushen** (an die URL, die die Nodes in
   `FUNDUS_UPDATE_MANIFEST_URL` pollen — z.B. `manifest.json` im Repo-Root, roh
   über `raw.githubusercontent.com`).
5. **Fertig.** Nodes mit gesetzter `FUNDUS_UPDATE_MANIFEST_URL` finden das neue
   Manifest beim nächsten Poll (Standard: alle 15 Min), verbreiten es per P2P
   weiter und wenden es über den Helper an. Alternativ sofort anstoßen per
   `curl -X POST http://localhost:3000/api/v1/admin/update -d @manifest.json`
   (Admin-Auth) auf einem Node — von dort verbreitet es sich per P2P.

> **Sicherheit:** Der Fee-Collector-Key kann auf ALLEN Nodes Code als root
> installieren. Er ist so kritisch wie die Node-Seeds — offline und getrennt
> aufbewahren, niemals auf einen Node legen. Wer diesen Key hat, kontrolliert
> das gesamte Netz.

### Test-Checkliste Update-Mechanismus

- [ ] **[PI] `FUNDUS_UPDATE_MANIFEST_URL` gesetzt** (auf die Roh-URL der
      `manifest.json`). Leer = nur P2P-Empfang, kein Git-Polling.
- [ ] **[PI] Helper muss laufen.** Ohne `fundus-helper` kann kein Update
      angewendet werden (Node loggt „fundus-helper nicht verfügbar"). Prüfen:
      `systemctl status fundus-helper`.
- [ ] **[PI] `unzip` vorhanden** (Basis-Paket, vom Deploy installiert — der
      Helper entpackt damit).
- [ ] **[PI] Erst-Test auf EINEM Pi.** Ein Test-Release signieren, Manifest
      bereitstellen, prüfen: Node loggt „neues gültiges Manifest", übergibt an
      Helper, Helper installiert + startet neu, neue Version läuft. **Nicht
      gleich für alle Nodes ausrollen.**
- [ ] **[PI] Signatur-Ablehnung testen.** Ein Manifest mit falscher/fehlender
      Signatur bereitstellen → muss ignoriert werden (Node UND Helper lehnen ab).
- [ ] **[PI] Hash-Ablehnung testen.** Manifest korrekt signiert, aber ZIP unter
      der URL manipuliert → Argon2id-Mismatch → Update wird abgebrochen, kein
      Swap.
- [ ] **[PI] Rollback prüfen.** Nach fehlgeschlagenem Swap liegt die alte
      Installation als `/opt/fundus.backup` vor. Bei kaputtem Update: Dienst
      stoppen, `/opt/fundus.backup` zurückschieben, Dienst starten.
- [ ] **[2×PI] P2P-Weiterverbreitung.** Manifest auf EINEM Node einspielen
      (curl) → der zweite Node bekommt es per P2P, ohne selbst zu pollen.
- [ ] **[PI] Bekannte Grenze.** Ein Update, das *durchläuft* aber eine defekte
      Binary hinterlässt, scheitert erst beim Neustart — dann ist SSH +
      `.backup` nötig. Darum immer erst auf einem Pi testen. Der Argon2id-Hash
      schützt vor Übertragungsfehlern/Manipulation, nicht vor einem fehlerhaften
      Release selbst.

---

## Reihenfolge in einem Satz

**Erst Tests (Phase 0), dann ein Pi sauber (Phase 1–2), dann Konsens auf zwei
Pis (Phase 3), dann – falls nötig – Fernbetrieb (Phase 4), dann ehrlich
kennzeichnen (Phase 5) und Wiederanlauf absichern (Phase 6).**

Der mit Abstand kritischste Sprung ist **Phase 0 → Phase 3**: Der PoA-Konsens
ist neu, nie kompiliert getestet, und entscheidet über echte Salden. Nicht
überspringen, nicht abkürzen.

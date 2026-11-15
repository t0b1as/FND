# Fundus — Offene ToDos (operativ / Ausrollung)

Diese Liste sammelt konkrete, abarbeitbare Punkte außerhalb der strategischen
Chain-Roadmap (siehe FUNDUS-ROADMAP.md). Eingeteilt nach Verifizierbarkeit:
**GRÜN** = ohne Live-System machbar, **ROT** = braucht Pi / Browser / Netz.

---

## ROT — braucht Pi / Browser / Netz (Live-Test)

### Websuche und eigener Suchindex (R895 ff.)
- [x] **R895 — Metasuche im Node** (`internal/websuche`, GRÜN getestet): DuckDuckGo,
      Mojeek, Wikipedia, SearXNG (falls vorhanden, doppelt gewichtet), Zusammenführen
      per Reciprocal Rank Fusion, 10-min-Zwischenspeicher. Seite `/suche`, KI-Chat
      Suchdienst „Fundus“. Ablageort des Index: lokal, Laufwerk unter /mnt|/media
      (nie Ausweichen auf die SD-Karte), oder anderer Pi über `/fundus/suchindex/1.0.0`.
- [ ] **ROT:** Parser gegen die echten Seiten von DuckDuckGo und Mojeek prüfen
      (nur gegen nachgebaute HTML getestet); Index-Abfrage zwischen zwei Pis.
- [x] **R896 — Crawler + Index** (GRÜN getestet, 17 Tests): eigener BM25-Index
      (Abschnitte, gestaffeltes Zusammenlegen, absturzfest), robots.txt + TDMRep,
      1 Anfrage/s je Host, nur öffentliche Adressen, MB/h-Grenze, „nur nachts“,
      Warteschlange übersteht Neustarts, angeklickte Treffer werden vorgemerkt,
      Umzug des Index beim Wechsel des Ablageorts (Kopie, dann Wechsel).
- [ ] **ROT:** Crawler auf der Pi 3 laufen lassen (RAM, Geschwindigkeit der
      USB-Platte), Umzug SD → USB-Platte mit echtem Index.
- [x] **R897 — Suchverbund** (GRÜN, Simulation mit 3 Pis/3 Websites): Rendezvous-
      Hashing je Host (1/n Wechsel bei neuem Pi), Zweitzuständiger hält Kopie,
      Links und Klicks gehen an den Zuständigen, Suche an alle Mitglieder,
      Mitglieder fallen erst nach 15 min Funkstille heraus.
- [ ] **ROT:** Verbund mit echten Pis (fundus, fnd, fundus71/72/73) prüfen.
- [x] **R899:** Mojeek nur auf Wunsch (weist automatisierte Abfragen ab); sperrende
      Dienste 30 min Pause; Crawler: Absturzschutz je Seite, Platzprüfung außerhalb
      der Sperre, 1 Arbeiter unter 1,5 GB RAM, HTML nur bis 768 KB auswerten,
      neue Arbeit weckt ruhende Arbeiter; Stand-Anzeige gibt nicht mehr auf.
- [ ] **ROT:** Ursache „Stand nicht ladbar“ nach ~160 Seiten im Journal bestätigen.
- [x] **R900:** Index/Warteschlange alle 2 min sichern, beim Beenden des Nodes
      sauber schließen; Bandbreitenkonto übersteht Neustarts; Go-Speichergrenze
      85 % von MemoryHigh/Max (gegen Ausbremsen durch systemd); Anzeige „im Index“.
- [x] **R898 — Speicher und tote Seiten** (GRÜN, 27 Tests): Höchstgröße und
      Mindestrest (automatisch: SD höchstens 2 GB, 5 GB frei; Laufwerk bis 1 GB),
      Crawler pausiert, voller Pi nimmt im Verbund nichts Neues an; Zusammenlegen
      nur mit Platz; Verdichten ab 1/3 Gelöschtem (absturzfest); Neulesen
      veralteter Seiten; 404/410/401/403 sofort raus, Netzfehler/5xx nach 3
      Versuchen über mind. 5 Tage; Weiterleitung ersetzt die alte Adresse.
- [ ] Später: Aufräumen von Seiten, für die ein Pi nach Mitgliederwechsel nicht
      mehr zuständig ist (heute bleiben sie liegen; die Suche entfernt Doppel).

### Verdienst-System (Storage/Transfer → FND, kombiniertes Modell)
Modell (festgelegt): **Storage-Nachweis (Quittungen) + Block-Produzent verteilt**.
Rate: **1 FND pro TB und Monat** (Speicher) bzw. pro TB (Transfer). Verdienst wird
NEU ERZEUGT (Inflation akzeptiert), NICHT vom Fee-Collector gebucht.
- [x] **Phase 2a — Quittungs-Primitive** (`chain/receipt.go`, GRÜN): signierte
      Receipts (Fetch/Store), Konsument unterschreibt, Selbst-Quittung abgelehnt,
      Manipulation bricht Signatur. 7 Angriffs-Tests.
- [x] **Betreiber-Wallet konfigurierbar** (`FUNDUS_STORAGE_REWARD_ADDR`): wohin der
      Verdienst gebucht wird; Validierung + Fallback auf Node-Adresse.
- [x] **Phase 2b — Quittungen im Transfer-Pfad** (Code fertig, Live-Test ROT):
      store/fetch geben ProviderAddr zurück; Konsument signiert nach Empfang eine
      Receipt und sendet sie per Op=="receipt"; Provider nimmt nur Quittungen an,
      die ihn als Provider ausweisen. filestore.Config bekommt WalletPrivKey +
      RewardAddr. Telemetrie-Zählen bleibt, echter Nachweis = Quittungen.
- [x] **Phase 2c — Quittungs-Speicher** (`receiptstore.go`, GRÜN): persistenter
      receiptStore mit ReceiptID-Dedup, PendingBytes, Settle, atomarer Persistenz.
      5 Tests (Add/Dedup, PendingBytes, Settle, Persistenz, kaputte Datei).
- [ ] **Phase 2b LIVE-TEST (2 Pis):** Datei auf Pi-A hochladen (wird zu Pi-B
      repliziert) → Pi-B sammelt Store-Quittung; Datei auf Pi-B abrufen → Pi-A
      sammelt Fetch-Quittung. `receipts.json` auf beiden prüfen. Selbst-Quittung
      (eigener Chunk) darf NICHT gezählt werden.
- [x] **Phase 3 — Mint-Tx + Block-Reward** (Code fertig, Live-Test ROT):
      TxStorageReward (0x25) mintet FND gegen verifizierte Quittungen (1 FND/TB),
      Reward geht an Receipt.Provider. Anti-Replay über State.redeemedReceipts
      (im State-Root committet, Version 0x05). Produzent baut den Reward-Tx aus
      receiptStore, fügt ihn beim /produce voran; nach Block werden Quittungen
      gesettlet. Inflation gewollt, kein festes Limit (rein proportional).
      7 Tests (Mint, Replay, Selbst-Quittung, Manipulation, Multi-Provider, Fee).
- [ ] **Phase 3 LIVE-TEST:** Nach Datei-Transfer zwischen 2 Pis `/produce` auf dem
      Provider-Pi aufrufen → Block enthält TxStorageReward → Provider-Saldo steigt
      (1 FND/TB). Zweiter `/produce` darf dieselben Quittungen NICHT erneut minten.
      Anderer Pi muss den Block akzeptieren (State-Root inkl. redeemedReceipts).

### Ausrollung & Chain-Setup
- [ ] `go test ./...` auf dem Pi — validiert den ganzen aufgelaufenen Stapel
      (Job-Suche, Fairness, Thumbnails, Messenger-Verlauf, i18n-Backend).
- [ ] Fee-Collector-Wallet auf 256-MiB-Node NEU erstellen + Doppel-curl-Konsistenztest.
      ⚠ JETZT MIT BLAKE3 (Phase 2.4): Die alte KanonischeFeeCollector in
      config/validate.go ist eine Keccak-Altadresse. fnd-wallet gibt jetzt eine
      BLAKE3-Adresse aus (= Chain-Adresse). Diese als neue Konstante eintragen,
      sonst läuft der Node mit einer per-Seed nicht reproduzierbaren Adresse.
- [ ] Finalen Genesis mit neuer Fee-Collector-Adresse + Hash extern verankern.
- [ ] **Multi-Node-Sync live testen (2 Nodes):** beide müssen denselben Genesis-Hash
      loggen; Node 2 holt beim Connect die Blöcke von Node 1 auf (Aufhol-Sync); ein
      /chain/produce auf Node 1 propagiert live zu Node 2 (TopicBlocks). Prüfen:
      gleiche Höhe + Head-Hash + Saldo. Abweichender Genesis muss Sync ablehnen.
- [ ] **Messenger↔Marktplatz live:** Inserat mit aktiver Messenger-Identität erstellen
      → contact_pub_key landet im Record; auf der Detailseite "Verkäufer anschreiben"
      öffnet den Messenger mit vorausgewähltem Kontakt; verschlüsselte Nachricht kommt
      beim Verkäufer an. (Partner: pubKey-Lookup über P2P noch offen.)
- [ ] `-AdminPass` beim Deploy setzen.
- [ ] Messenger-Hook für Juror-Benachrichtigung (escrow.dispute_opened).

### i18n / Frontend Live-Verifikation
- [ ] Jede umgebaute Lua-Seite im Browser rendern (besonders render.lua, settings,
      wallet, partner, admin — dort viele `]] .. t() .. [[`-Konkatenationen; ein
      Fehler in render.lua betrifft JEDE Seite).
- [ ] Sprachumschaltung testen: Burger-Menü → Sprach-Untermenü → `de`/`en` wechseln,
      Cookie `fundus_lang` greift, alle Keys lösen auf (kein roher Key sichtbar).
- [ ] Emoji-Panel live: `fetch('/static/emoji-data.json')` lädt, 2415 Emojis
      rendern/filtern flüssig. (Rendering ist auf 200 Buttons gedeckelt mit
      "+N · Suche eingrenzen"-Hinweis → Performance abgesichert; nur noch
      visueller Check nötig.)

---

## GRÜN — ohne Live-System machbar

### Daten-Uploads (User liefert Datei, dann baue ich)
- [ ] **CLDR-Emoji-Annotationen hochladen** (de.xml + en.xml, ggf. fr/es/it aus
      `cldr-common/common/annotations/`). Dann generiere ich `emoji-data.json` neu
      mit vollständigen, mehrsprachigen Keywords für ALLE ~2400 Emojis — aktuell
      haben nur ~535 gängige auch deutsche Stichwörter, der Rest nur EN-Unicode-Namen.
      → macht die Emoji-Suche durchgängig mehrsprachig + beliebig erweiterbar.
- [ ] **BIP39 english.txt hochladen** (vollständige 2048-Wörter-Liste; aktuell nur
      510 Wörter im Node) — nötig für korrekte Wallet-Seed-Phrasen.

### i18n-Pflege
- [ ] Optional: kurierte deutsche Keywords schrittweise auf mehr Emojis ausweiten
      (Alternative zu CLDR, falls keine Datei geliefert wird — mehr Handarbeit).
- [ ] Neue Sprache hinzufügen ist vorbereitet: `locales/<code>.lua` aus TEMPLATE.lua
      kopieren, `i18n.SUPPORTED` erweitern, Label+Flagge in `lang_menu_html`.
      (640 Keys pro Sprache sind die vollständige Vorlage.)

### Chain-Weiterarbeit (Details in FUNDUS-ROADMAP.md §2)
- [x] 2.1 Fee-Collector als Single Source of Truth — erledigt (+5 Tests).
- [x] 2.2 Escrow-State-Machine: Storno/Rücksende als Chain-Tx 0x35–0x37 — erledigt (+8 Tests).
- [x] 2.3a Energie-Zertifizierung (Tx 0x22/0x23): Mengenverbuchung aus signierten
      Zählerständen, lückenlose Kette, +10 Tests — erledigt.
- [x] 2.4 Wallet-Ableitung auf BLAKE3 angeglichen (identity + fnd-wallet), Cross-Check-
      Test gegen chain.PubkeyToAddress — erledigt. ⚠ Fee-Collector-Neuableitung nötig (ROT).
- [ ] 2.3b Energie-Handel: Zertifikate handeln (Erzeuger-Minimum, Angebot/Nachfrage,
      Verbraucher zahlt Erzeuger). Setzt auf CertifiedTotal auf.
- [x] 2.5 Wallet→Chain-Brücke: BuildSignedTransfer/BuildSignedTx + POST /chain/send
      (serverseitig aus Seed signieren), +3 Tests — erledigt.
- [x] Multi-Node-Grundlage: deterministischer Genesis (CanonicalGenesis) + Block-Sync
      (P2P chainsync: Aufhol-Sync + Live-Propagation), +3 Tests — erledigt (GRÜN).
- [x] Energy-Schichtung: diskrete Tokens pro Periode (commodity_certify→unsettled,
      commodity_settle 0x24→settled+gepruned) + Rohdaten-Hash-Anker; lokaler PeriodStore
      (meter/rawstore.go: ClosePeriod-Commitment + DeletePeriod nach Settlement).
      +9 Tests — erledigt (GRÜN).
- [ ] Energy-Verdrahtung (ROT/Integration): meter-Ingest → PeriodStore.Append →
      periodischer ClosePeriod → commodity_certify-Tx (via BuildSignedTx); Aufräum-Job
      ruft DeletePeriod, sobald der Token on-chain Settled ist.
- [x] 2.5b contract.go-Escrow-Aktionen (open/confirm/cancel/submit-return/confirm-return)
      via submitEscrowTx + BuildSignedTx an die eigene Chain gebunden (Tx 0x30/0x31/
      0x35/0x36/0x37) — war bereits umgesetzt, in diesem Durchgang verifiziert.
- [x] Vertragsvorschau echt: buildContractData lädt jetzt den echten Escrow (GetEscrow)
      + verknüpftes Listing (content_hash-Lookup) statt Platzhalter; escrowGet liefert
      echte Escrow-Daten; Vorschau + HTML-Template auf eigene Chain (1,8 %, Fundus-Chain,
      Chain-ID dynamisch) statt Gnosis. +2 Tests (escrowStateString, uToFNDFloat).

---

## ERLEDIGT (diese Sitzungen)
- [x] Netzweite Job-Suche (Gebote/Gesuche) + File-Suche-Limit.
- [x] Vollständige i18n aller 23 Seiten (DE+EN, 640 Keys synchron).
- [x] Sprach-Untermenü im Burger-Menü.
- [x] Emoji-Picker auf vollen Unicode-Satz (2415) mit mehrsprachiger Suche + Lazy-Load.
- [x] Chain 2.1: Fee-Collector Single Source of Truth (+5 Tests).
- [x] Chain 2.2: Escrow Storno/Rücksende-Flow als Chain-Tx 0x35–0x37 (+8 Tests).
- [x] Chain 2.3a: Energie-Zertifizierung (meter_register 0x23 + commodity_certify 0x22, +10 Tests).
- [x] Messenger-Lesebestätigungen (WhatsApp-Häkchen): P2P-Quittungen (auto-delivered +
      read beim Chat-Öffnen), Status persistiert (UpdateReceipt), Häkchen + Zeitstempel-
      Tooltip in den Bubbles. Live-Test des P2P-Roundtrips steht aus (ROT).
- [x] Chain 2.4: Wallet-Adressableitung (identity + fnd-wallet) auf BLAKE3 angeglichen,
      identisch zur Chain. Cross-Check-Test (+3 Tests). ⚠ Fee-Collector neu ableiten (ROT).
- [x] Chain 2.5: serverseitiges Signieren — BuildSignedTransfer/BuildSignedTx + /chain/send
      (aus Seed ableiten→Nonce→signieren→Mempool), +3 E2E-Tests.
- [x] Multi-Node: deterministischer Genesis (jeder Node gleicher Hash) + P2P-Block-Sync
      (Aufhol-Sync beim Connect + Live-Propagation) + Genesis-Hash-Schutz, +3 Tests.
- [x] Energy-Schichtung: diskrete Energie-Tokens pro Periode mit Rohdaten-Hash, settle→
      prune (commodity_settle 0x24); lokaler Rohdaten-Store (meter/rawstore.go), +9 Tests.
- [x] Energy verallgemeinert: explizites Einheiten-Feld (Unit: Ws/ml/g/Wh/L) getrennt
      vom Commodity, in MeterRecord+CommodityToken+Register-Payload, Token erbt vom Zähler,
      +2 Tests. (Jetzt eingezogen, solange Chain frisch — später wäre es ein Hard Fork.)
- [x] EnergyToken→CommodityToken umbenannt (Typ/ID/Payloads/Tx-Konstanten/Verben),
      da der Token jetzt alle Rohstoffe abbildet. Tx-Werte 0x22/0x23/0x24 unverändert.
- [x] Messenger↔Marktplatz: Verkäufer/Inserent direkt anschreiben. whoami-Endpoint
      (+1 Test), pubKey-Einbettung beim Inserieren (Listings+Jobs), Kontakt-Buttons
      (listing_show+job_show), openChatFromURL im Messenger (?to=&key=). Shop=
      automatischer FND-Wechseldienst (kein Kontakt nötig). Partner=Anonymitäts-
      design, pubKey nicht eingebettet (eigener P2P-Lookup nötig, ROT).
- [x] Doku-Konsistenz: alle falschen Krypto-Kommentare gefixt (blake2b/SHA-256→BLAKE3,
      blake2bSum256/blake2b256→blake3Sum256), Single-Node→Single-Producer präzisiert.
- [x] Vertragsvorschau + escrowGet an die eigene Chain gebunden (echte Escrow+Listing-
      Daten statt Stub, Fundus-Chain statt Gnosis), +2 Tests.
- [x] Storage-Sekundärindex (storage/index.go): generischer Feld-Index `wert→id` für
      exakte Lookups in O(1) statt linearem Scan. Erster Nutzer: content_hash→Listing
      (Vertragsvorschau). Bei Put/Delete gepflegt, beim Start (re)gebaut, +6 Tests.
      Erweiterbar via indexedFields-Map.
- [x] Invertierter Trigramm-Index (storage/invindex.go) für skalierbare Volltextsuche:
      Text→Trigramme (runensicher, Umlaute), Query→Trigramm-Schnittmenge=Kandidaten,
      dann exakter strings.Contains-Verify → bit-identisch zur linearen Suche, aber nur
      Kandidaten geladen statt aller Records. Kategorie-Term für Browsing ohne Suchbegriff.
      search+jobsearch umgestellt; Limit von destruktivem In-Loop-Abbruch auf
      Sortieren-dann-Deckeln geändert (relevanteste statt willkürliche Treffer). Bei
      Put/Delete gepflegt, beim Start gebaut. +6 Tests (inkl. Index==Linear-Property).

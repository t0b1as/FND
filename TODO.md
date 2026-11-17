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
- [x] **R929:** Link-Vorschau: Abrufer von Facebook, WhatsApp, Telegram, X,
      LinkedIn, Discord u. a. bekommen die Seite direkt statt /tos;
      Open-Graph-Angaben (Titel, Text, Preis, Bild 512 px) für Angebote,
      Marktplatz, Aeronautik, Feed; fbclid/utm_* aus og:url entfernt.
- [ ] **ROT:** Mit dem Facebook Sharing Debugger (developers.facebook.com/tools/debug)
      prüfen und „Erneut scrapen“, damit die alte TOS-Vorschau aus dem Cache fliegt.
- [x] **R928:** Navigation (Startseite/Menü/Zugriffsleiste) je Konto statt je
      Browser: selbst-verschlüsselt beim Node (nav_prefs, fundus.contacts,
      Peer-Sync), ohne Anmeldung Standard. Aeronautik und Drive standardmäßig
      in der Zugriffsleiste. Alte Geräte-Einstellung (fundus_nav_vis) verworfen.
- [x] **R927:** Besitz eines Angebots = angemeldetes Konto ist Ersteller (API
      „mine“), nicht mehr „liegt auf diesem Node“ – Gäste sahen fremde
      Angebote als eigene (Bearbeiten/Löschen statt Kaufen). Betreiber im
      Heimnetz bearbeitet fremde Angebote seines Nodes weiter, sieht aber
      Kaufen. Hotspot-Geräte (10.42.0.x) gelten nicht mehr als Heimnetz
      (Go isLANRequest, nginx $fundus_lan).
- [x] **R926:** Marktplatz, Aeronautik und Feed ohne Anmeldung lesbar (nur
      GET; Feed zeigt Gästen nur öffentliche Beiträge). PayPal-Kauf und
      Warenkorb als Gast; Quartett-Druckdatei offen. Anlegen, Bearbeiten,
      Liken, Kommentieren, Kauf mit FND verlangen weiter ein Konto – erst dann
      kommt das Anmeldefenster, mit „Weiter ohne Anmeldung“.
- [x] **R925:** PayPal-Empfänger je Konto: nicht erreichte Nodes (Neustart,
      offline, ältere Version ohne /fundus/listing-edit/1.0.0) werden alle 3 min
      bis zu 12 h erneut versucht (⏳ → „✓ nachgeholt“); Node ohne Protokoll wird
      nach dem ersten Fehlschlag nicht für jedes Angebot erneut abgewartet;
      verständliche Fehlertexte.
- [ ] **ROT:** Node …4KbuWC auf R925 aktualisieren, dann Nachholen prüfen.
- [x] **R924:** PayPal-Empfänger je Konto: Übernehmen läuft im Hintergrund mit
      Ergebnis je Angebot (liegt auf, ✓/Fehler) und Protokollzeile; bis die
      Änderung ein Angebot erreicht, gilt der Konto-Empfänger (Anzeige und
      Zahlung auf diesem Node).
- [x] **R923:** PayPal-Empfänger je Konto (Einstellungen → PayPal): wird in
      alle Angebote des Kontos eingetragen (eigene direkt, auf anderen Nodes per
      signierter Weiterleitung) und in neue Angebote mit leerem Feld.
- [x] **R922:** PayPal in eigener Lasche (Zugang für Hotspot und Marktplatz,
      Liste der PayPal-Verkäufe eigener Angebote mit Lieferadresse). Marktplatz:
      Angebote mit PayPal-Empfänger (E-Mail/Händler-ID) lassen sich per PayPal
      bezahlen, 1 FND = 1 €, Geld direkt an den Verkäufer (payee). Betrag rechnet
      der Node; Bestand vor der Abbuchung reserviert; Kaufvermerk wie beim
      Escrow → Verkäufer-Node zieht Bestand ab, Push mit Lieferadresse.
      Warenkorb (/warenkorb, im Browser), bezahlt je Verkäufer.
- [ ] Marktplatz-PayPal im Sandbox-Modus mit zwei Konten durchspielen (payee ≠ App-Inhaber).
- [x] **R921:** Kartenrücken: „fnd.resolve.bar“ unter FUNDUS, gleich breit
      (Zeichenabstand angepasst).
- [x] **R920:** SR-71 auf dem Kartenrücken nach dem NASA-Foto (Umriss per
      GrabCut, tiefe Schatten als zweiter Ton), Spielname und „Best of“
      2,4 mm höher für mehr Platz.
- [x] **R919:** SR-71 auf dem Kartenrücken schräg von vorn oben statt
      Draufsicht – Umriss nach dem Foto des Nutzers nachgezeichnet, Gondeln mit
      feinen Linien in Hintergrundfarbe abgesetzt.
- [x] **R918:** Kartenrücken: Silhouette einer SR-71 Blackbird (Draufsicht,
      Nase rechts, eigene Umrisszeichnung) zwischen „Best of …“ und „TKM x HELIX“.
- [x] **R917:** Kartenrücken: heller Schein um das Logo (zur Mitte bis 62 %
      aufgehellt, ohne Transparenz – PDF/X-1a), Netzlinien folgen dem Schein.
- [x] **R916:** Kartenrücken: deutlicherer Verlauf (oben 90,230,130 →
      unten 12,130,72), schwarze Schrift weiter lesbar.
- [x] **R915:** Kartenrücken: Verlauf unten hell (20,150,85 statt fast
      schwarz) – schwarze Schrift bis unten lesbar.
- [x] **R914:** Kartenrücken: Logo und Schrift schwarz, Schrift Inter
      (eingebettet; FUNDUS in Black, wie jetzt auch in der Kopfzeile der
      Seite – inter-black.woff2 liegt im Paket), unter „TKM x HELIX“ der
      Erstellungszeitpunkt JJJJMMTThhmmss.
- [x] **R913:** Quartett: Entfernung mit drei Nachkommastellen (Meter); die
      Aufzeichnung speichert sie jetzt auf den Meter genau (vorher 10 m).
- [x] **R912:** Quartett für alle: auch die Druckdatei (PDF/X-1a) ohne
      Eigentümer-Anmeldung (öffentliche Endpunkte, Auftragsnummer zufällig).
      Besucher: höchstens 3 pro Stunde, nach 15 min Schluss; eine Umwandlung
      zur Zeit wie bisher; Eigentümer unbegrenzt.
- [x] **R911:** Hotspot-Diagnose in den Einstellungen: letzte Aufrufe der
      Anmeldeseite (Gerät, Adresse, Dauer, auch https-Versuche), Messung der
      Namensauflösung (jede Minute), Protokoll des Wächters, Starts seit
      Dienstbeginn. PayPal-Skript erst nach dem Laden der Seite.
- [ ] ROT: Anmeldeseite > 30 s und ERR_INTERNET_DISCONNECTED – Ursache mit der Diagnose klären.
- [x] **R910:** https vor der Freischaltung: Port 443 (außer PayPal) geht an
      den Pi (3443, eigenes Zertifikat) → Chrome/Firefox erkennen das
      Anmeldenetz und bieten „Im WLAN anmelden“ statt eines Netzfehlers;
      UDP (QUIC) als „Port geschlossen“ abgelehnt statt „verboten“
      (ERR_INTERNET_DISCONNECTED). Anmeldeseite immer unter 10.42.0.1:3088
      (Links über umgeleitete Namen machte Chrome zu https). Produkt oben,
      WLAN-Stufen darunter.
- [ ] Mit Android-Chrome prüfen: Suche aus der Adressleiste → Hinweis „Im WLAN anmelden“?
- [x] **R909:** ERR_INTERNET_DISCONNECTED: R908 beantwortete jeden Namen mit
      10.42.0.1 – Android stufte das WLAN als „ohne Internet“ ein. Jetzt echte
      Antworten, aber jede DNS-Anfrage nicht freigeschalteter Geräte geht an den
      Pi (auch bei eigenem DNS-Server); Prüfadressen von iOS/Android/Windows
      werden vorgehalten (min-cache-ttl 10 min, alle 15 min aufgefrischt).
      Kaufknopf wie die WLAN-Knöpfe (waagerecht, ohne Lastschrift). Eigene
      Produktseite /produkt auf der Anmeldeseite mit Text und bis zu 6 Bildern.
- [x] **R908:** Nicht freigeschaltete Geräte bekommen einen eigenen
      Namensdienst (Port 5300): jeder Name → 10.42.0.1 (TTL 0), nur PayPal echt –
      jede http-Adresse führt sofort zur Anmeldeseite, ohne fremden DNS.
      Verkauf ist ohne ausdrückliches Abschalten sichtbar (R904 war bis zum
      Einschalten unsichtbar), auch bei nicht erkanntem Gerät; ohne PayPal
      ersatzweise der Link; Einstellungen sagen, warum er fehlt.
- [x] **R907:** Anmeldeseite schneller: DHCP-Option 114 entfernt (verlangt
      HTTPS, iOS/Android warteten darauf), Anfragen an den Pi und nach
      draußen werden abgelehnt statt verworfen (kein Warten auf Zeitüberschreitung,
      z. B. privates DNS), DHCP authoritative + Rapid Commit, keine
      IPv6-Antworten, vor der Freischaltung 5/20 statt 2 Mbit/s, Angebot direkt
      in der Seite. Wächter startet nach einem Update mit neuer Fassung neu.
- [ ] Anmeldeseite mit iPhone und Android messen (Zeit bis zum Fenster).
- [x] **R906:** Node-Übersicht zeigt je Node die Adresse im Heimnetz und den
      freien Platz des Datenträgers (% und GB, unter 10 % rot).
- [x] **R905:** Index auf anderem Pi: Status klappte, jede Suche wurde
      abgewiesen, wenn der Index-Pi nur im Verbund war (nicht „anbieten“).
      Jetzt dürfen eigene Pis desselben Kontos immer suchen; Speichern prüft
      das gleich mit; die Auswahlliste fragt auch eigene Pis und
      Verbund-Mitglieder, die gerade nicht direkt verbunden sind.
- [x] **R904:** Verkauf auf der Hotspot-Anmeldeseite: FLIGHT QUAD 32 (9,90 €,
      einstellbar) als PayPal-Sofortkauf – Lieferadresse aus dem PayPal-Konto
      (GET_FROM_FILE), Zahlung von PayPal bestätigt, Beleg mit Adresse in
      hotspot-verkaeufe.jsonl, Push an den Eigentümer, Verkaufsliste mit
      „versendet“ in den Einstellungen – oder Link auf eine Produktseite.
- [ ] Hotspot-Verkauf im Sandbox-Modus mit echtem Telefon durchspielen.
- [x] **R903:** Karten (Leaflet) liefen beim Scrollen über die Titelzeile –
      eigener Stapelkontext. Quartett-Fotos ganz aufs Feld skaliert statt
      beschnitten, in voller Auflösung eingebettet (JPEG 96/95); Commons-Fotos
      bis 3840 px (Original, wenn kleiner), alte 800-px-Fotos werden im
      Hintergrund nachgeladen; Typenliste lädt 512-px-Vorschau (?klein=1).
- [x] **R902:** 59 × 91: Tiefschwarz – dunkle Flächen (K ≥ 90 %) mit mindestens
      40 % Cyan, reines Schwarz als C40 K100; Schrift und Linien bleiben reines K.
      FLIGHT QUAD 64 bei 59 × 91 wieder wählbar. Fotos mit Beschnitt nur bis zum
      Sicherheitsabstand (3 mm innerhalb der Schnittkante), Feldgrund läuft weiter.
- [x] **R901:** Quartett 59 × 91 mm genau nach Vorlage WIRmachenDRUCK
      („32 Karten + 1 Deckblatt“): nur FLIGHT QUAD 32 (66 Seiten à 65 × 97 mm),
      QR-Feld unten rechts und Druckerei-Vermerk am rechten Rand der Titelkarte
      frei, unterste Zeilen mit 3 mm Abstand auch an den 5-mm-Ecken, Dateiname
      „Spieletitel_Komponente_Sprache.pdf“.
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

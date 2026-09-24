# Fundus – Release-Strategie & Feature-Priorisierung

Stand: Arbeitsdokument zur Vorbereitung eines ersten nutzbaren Release.

Ziel: Ehrlich trennen, was für ein **funktionierendes, ehrliches Release**
essenziell ist, was als **Baustelle** markiert wird (sichtbar, aber nicht
blockierend), und was **vorerst deaktiviert** gehört.

---

## Leitprinzip

Ein Feature darf nur dann ohne Baustellen-Band live gehen, wenn es
**end-to-end funktioniert und getestet ist**. Alles andere bekommt entweder das
gelb-schwarze Band (funktioniert teilweise / in Arbeit) oder wird ausgeblendet
(gar nicht nutzbar / irreführend).

Grund: Ein sichtbar als „in Arbeit" markierter Button schafft Vertrauen. Ein
Button, der scheinbar funktioniert und dann fehlschlägt, zerstört es.

---

## Kategorie A – Essenziell (muss vor Release stabil sein)

Diese bilden den Kern und müssen ohne Band funktionieren:

1. **Wallet** – Erstellen/Öffnen, Balance, Transfer.
   Status: funktioniert. Fee-Split + Einnahmequellen-Erklärung neu ergänzt.
   Offen: Live-Verifikation des Fee-Splits auf zwei Nodes.

2. **Dateien (Upload/Download/Hosting)** – der eigentliche USP.
   Status: funktioniert nach den jüngsten Fixes (WriteTimeout, Streaming).
   Offen: Download-Performance (Startverzögerung, intermittierend), Streaming
   über HTTPS bestätigen.

3. **Chain-Grundfunktion** – Blöcke, Salden, State-Konsistenz, Selbstheilung.
   Status: funktioniert (Single-Producer). Selbstheilung bei Formatwechsel drin.
   Offen: siehe Kategorie C (BFT-Konsens) – für ein 1–2-Node-Testrelease
   akzeptabel, für echtes Multi-Node nicht.

4. **Storage-Vergütung** – Transfer + Vorhaltung + Fee-Split.
   Status: Code + Unit-Tests komplett. Offen: Mint-Live-Test auf zwei Pis.

---

## Kategorie B – Baustelle (gelb-schwarzes Band, bleibt sichtbar)

Funktionieren teilweise oder sind noch nicht end-to-end verifiziert. Nutzbar,
aber ehrlich als „in Arbeit" markiert:

- **Marktplatz (listings)** – Grundgerüst da, aber Kauf-/Verkaufsfluss nicht
  end-to-end getestet.
- **Jobs / Verträge** – Signatur-Fluss vorhanden, Live-Abwicklung offen.
- **Zertifikate (certificates)** – Commodity-Certify existiert in der Chain;
  UI-Fluss unklar.
- **Energie / Strom-Token** – komplexes Feature (Smart-Meter EDL21/SML). Chain-
  Logik da, aber Hardware-Anbindung/Settlement nicht live getestet.
- **Messenger** – bekannter offener Punkt (deriveIDFromPubKey/04-Prefix).
- **Partner** – Fluss unklar.
- **Topologie / Peers** – eher Diagnose-Ansicht, kann sichtbar bleiben, aber als
  „technisch/experimentell" markiert.

---

## Kategorie C – Größere offene Baustellen (strukturell)

- **BFT-Konsens** – aktuell Single-Producer. Bei gleichzeitiger Produktion auf
  mehreren Nodes divergieren die Chains. Für ein 1-Node- oder kontrolliertes
  2-Node-Release akzeptabel (nur ein Produzent aktiv), für offenes Multi-Node
  ein Muss. Größter Einzelaufwand.

- **Escrow-Gebühren-Split** – Transfer splittet 0,9/0,9; Escrow-Gebühren gehen
  noch komplett an den Collector. Konsistenz-Nachzug, kleiner Aufwand.

- **Block-Explorer / Chain-Status-UI** – Mint-Vorgänge und Salden sichtbar
  machen. Nützlich fürs Vertrauen, mittlerer Aufwand.

---

## Solana-Interface – Klärung

**Was existiert (und funktioniert):**
Eine SOL→FND On-Ramp. Der `SolWatcher` (go/internal/shop/sol_watcher.go) pollt
echt die Solana-Chain (getSignatures/getTransaction), matcht eingehende SOL-
Zahlungen gegen offene Bestellungen und löst dann `Minter.MintFND()` aus. Der
Shop (go/internal/api/shop.go) stellt Bestellung + Empfangsadresse + QR bereit.
Konfiguration über FUNDUS_SHOP_SOLANA_RPC und ShopReceiveAddr.

**Was zu klären ist:**

1. **Ungenutzter Tx-Typ `TxSolCredit` (0x21):** Ist als Typ definiert, wird aber
   in ApplyTransaction NICHT behandelt. Der Mint läuft über die Minter-
   Schnittstelle, nicht über diesen Tx-Typ. → Entweder entfernen (Altlast) oder
   den Mint sauber über diesen on-chain-Typ führen (nachvollziehbar auf der
   Chain, statt „magischer" Gutschrift).

2. **Vertrauensmodell:** Der Watcher ist eine zentrale Komponente – wer ihn
   betreibt, kontrolliert das Minting. Für ein dezentrales Netz ist das ein
   Single Point of Trust. Fürs Testrelease ok, aber bewusst zu benennen.

3. **Zweck & Umfang fürs Release:** Ist die SOL-On-Ramp ein Kern-Feature (dann
   Kategorie A, muss stabil + getestet sein) oder ein Zusatz (dann Baustelle
   mit Band)? Das ist die eigentliche offene Entscheidung.

**Empfehlung:** Fürs erste Release als **Baustelle** markieren. Die On-Ramp
funktioniert, aber das Vertrauensmodell (zentraler Watcher) und die
TxSolCredit-Inkonsistenz sollten vor einer „ohne Band"-Freigabe geklärt sein.

---

## Empfohlene Reihenfolge

1. **Baustellen-Band bauen** (wip-badge, wiederverwendbar) + auf Kategorie-B/C-
   Features setzen. → Schafft sofort einen ehrlichen, release-fähigen Zustand.
2. **Mint-Live-Test** auf zwei Pis. → Verifiziert die ganze Reward-Kette
   (Transfer → Quittungen → Vorhaltung → Mint → Fee-Split), die essenziell ist.
3. **Solana-Entscheidung** treffen (Kern vs. Baustelle) und TxSolCredit
   aufräumen.
4. Danach: Escrow-Split, Block-Explorer, und – als großes Thema – BFT-Konsens.

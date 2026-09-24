# Mint-Test: Storage-Reward auf der nativen Fundus-Chain

Ziel: Die ganze Reward-Kette einmal live durchspielen —
Datei-Transfer → Fetch-Quittungen → Block-Produktion → FND-Gutschrift beim
Provider → Fee-Split. Alles auf der **nativen** Chain, ohne Solana.

---

## Vorab: Der Node erzeugt seinen Wallet-Schlüssel automatisch

**Du musst nichts konfigurieren.** Jeder Node erzeugt beim ersten Start
automatisch seinen eigenen Wallet-Schlüssel und speichert ihn persistent in
`<DataDir>/node.key` (z.B. `/opt/fundus/data/node.key`, chmod 0600, nur für den
Besitzer lesbar). Bei jedem weiteren Start wird derselbe Schlüssel geladen.

Das ist das dezentrale Modell: Jeder User betreibt seinen eigenen Node und
bekommt automatisch seine eigene, persönliche Wallet — wie eine Bitcoin-Wallet.
Der Schlüssel wird NIEMALS mitdeployed und muss NICHT manuell gesetzt werden.

Der Schlüssel erfüllt drei Rollen:
- signiert die Storage-Quittungen (als Konsument),
- seine abgeleitete Adresse ist die **Node-Adresse** (selfAddr),
- sie ist zugleich die **Reward-Adresse** (wohin verdientes FND fließt),
  sofern FUNDUS_STORAGE_REWARD_ADDR leer bleibt.

**Schlüssel-Priorität im Node:**
1. `FUNDUS_WALLET_PRIV_KEY` gesetzt → dieser wird genutzt (nur nötig, wenn ein
   User eine bestehende Seed importieren will; optional, direkt auf seinem Pi).
2. Sonst → automatisch erzeugter persistenter `node.key` (Standardfall).

### Für den Test: nichts zu tun

Beide Pis haben nach dem Deploy + Neustart automatisch je einen eigenen
Schlüssel. Im Node-Log erscheint beim ersten Start:

```
Node-Wallet: neuer Schlüssel automatisch erzeugt und gespeichert
Quittungs-Identität aktiv   node_addr=0x…  reward_addr=0x…
```

Bei Folgestarts:

```
Node-Wallet: persistenter Schlüssel geladen
Quittungs-Identität aktiv   node_addr=0x…  reward_addr=0x…
```

Die `node_addr` aus dem Log ist die Adresse, deren Saldo im Test steigt. Notiere
sie dir von **beiden** Pis (sie sind unterschiedlich — jeder Node hat seine
eigene). Alternativ zeigt die Earnings-Karte in der Wallet die Node-Adresse an.

> Hinweis: Willst du bewusst denselben Schlüssel auf beiden Pis (damit ein Node
> für beide verdient), kannst du die `node.key` von einem Pi auf den anderen
> kopieren. Für einen realistischen Test ist es aber besser, jeden Node seinen
> eigenen behalten zu lassen — so sieht man, wie FND vom Provider zum Konsument
> fließt.


---

## Der Test-Ablauf

Annahme: Pi-A = 10.10.11.25, Pi-B = 10.10.11.39. „Provider" ist der Node, der
die Chunks hält und ausliefert; „Konsument" lädt herunter und quittiert.

### 1. Vorher: Salden notieren

Auf beiden Pis die aktuelle Node-Adresse + Saldo festhalten:

```
curl -s http://127.0.0.1:3000/api/v1/chain/status
```

Und den Saldo der jeweiligen Node-Adresse (Adresse aus dem Log „node_addr"):

```
curl -s "http://127.0.0.1:3000/api/v1/wallet/balance?address=<NODE_ADRESSE>"
```

### 2. Kleine Testdatei auf Pi-A hochladen

Über die Weboberfläche (https://10.10.11.25/files) eine **kleine** Datei
hochladen (wenige MB reichen). Den content-Hash notieren (steht in der Liste).

### 3. Datei auf Pi-B herunterladen

Über https://10.10.11.39/files die Datei per Hash herunterladen. Damit holt
Pi-B die Chunks von Pi-A → dabei entstehen **Fetch-Quittungen** (Pi-B quittiert
Pi-A als Provider).

Prüfen, ob Quittungen entstanden sind (auf Pi-B, dem Konsumenten):

```
sudo journalctl -u fundus-node -n 50 | grep -iE "Quittung|receipt|Provider"
```

### 4. Block produzieren (der eigentliche Mint)

Auf dem **Provider-Pi** (Pi-A, der die Quittungen gesammelt hat) einen Block
bauen. Das ist ein POST — mit curl also `-X POST`:

```
curl -X POST http://127.0.0.1:3000/api/v1/chain/produce
```

Antwort: `{"height":N,"hash":"..."}`. Der Block enthält jetzt die
Storage-Reward-Tx, die die gesammelten Quittungen zu FND mintet.

### 5. Ergebnis prüfen

Saldo der Provider-Node-Adresse erneut abfragen — er sollte um den geminteten
Betrag gestiegen sein (1 FND pro übertragenem TB; bei einer kleinen Datei
entsprechend ein winziger uFND-Betrag):

```
curl -s "http://127.0.0.1:3000/api/v1/wallet/balance?address=<PROVIDER_NODE_ADRESSE>"
```

Und die Chain-Höhe ist um 1 gestiegen:

```
curl -s http://127.0.0.1:3000/api/v1/chain/status
```

### 6. Anti-Replay verifizieren

Nochmal `produce` aufrufen:

```
curl -X POST http://127.0.0.1:3000/api/v1/chain/produce
```

Die bereits eingelösten Quittungen dürfen NICHT erneut minten (redeemedReceipts-
Schutz). Der Provider-Saldo bleibt gleich (evtl. entsteht ein leerer Block).

---

## Hinweise

- **Kleine Datei genügt.** Es geht um die Mechanik, nicht um große Beträge.
  1 FND/TB heißt: eine 10-MB-Datei erzeugt ~0,00001 FND = 10 000 uFND. Sichtbar,
  aber klein.
- **Vorhaltungs-Vergütung** (1 FND/TB·Monat) läuft separat über die periodischen
  Challenges (HostingRewardInterval, im Test 1 h). Für einen schnellen Sicht-
  Test könnte man das Intervall temporär kürzer stellen.
- **Fee-Split** wird bei diesem Reward-Mint NICHT ausgelöst (Reward-Tx ist
  gebührenfrei). Den Split sieht man bei einem normalen Transfer zwischen zwei
  Wallets.
- Läuft die Chain nicht (`chain/status` = 503), erst die Chain-Themen klären
  (Selbstheilung sollte sie aber automatisch hochbringen).

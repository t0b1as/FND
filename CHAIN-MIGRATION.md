# Chain-Migration: JSON-Dateien → bbolt (binär)

Die Chain-Persistenz wechselt von einer JSON-Datei pro Block auf einen binären
Blockstore (bbolt, `chain.db`). Das ist ~3× kompakter und beseitigt das
"Millionen Dateien in einem Verzeichnis"-Problem.

## Einmalig: Dependency holen (auf dem Build-PC)

bbolt ist eine neue Abhängigkeit. Vor dem ersten Build:

```
cd go
go get go.etcd.io/bbolt@v1.3.10
go mod tidy
```

Das füllt `go.sum`. Ohne diesen Schritt schlägt der Build fehl (fehlende
Abhängigkeit). Danach wie gewohnt bauen/deployen.

## Wallets

**Der Wechsel berührt keine Wallets.** Wallets sind Schlüsselpaare (aus dem Seed
abgeleitet), der Kontostand wird beim Start durch Replay der Blöcke rekonstruiert.
Das Speicherformat der Blöcke ändert nur den Container, nicht den Inhalt — Replay
ergibt exakt denselben State, dieselben Salden, denselben Head-Hash. Nichts an
`node.seed`/`node.key` ändert sich.

## Migration ausführen (pro Pi)

Der Node startet NICHT mit alten JSON-Blöcken und leerer DB — er weist auf die
Migration hin. Ablauf pro Pi:

1. **Node stoppen:** `sudo systemctl stop fundus-node`
2. **Migrieren** (liest blocks/*.json, schreibt chain.db, verifiziert Head-Hash).
   Am einfachsten über das Node-Binary selbst (kein Extra-Tool nötig):
   ```
   sudo -u fundus /opt/fundus/bin/fundus-node --migrate-chain
   ```
   Das nutzt automatisch das konfigurierte DataDir. Alternativ mit dem
   Admin-Tool (falls installiert) und explizitem Pfad:
   ```
   sudo /opt/fundus/bin/fundus-admin migrate-chain --dir /opt/fundus/data/chain
   ```
   Beide zeigen am Ende:
   `✓ Verifikation OK — Head-Hash stimmt. Migration verlustfrei.`
3. **Node starten:** `sudo systemctl start fundus-node`
4. **Läuft er sauber?** Logs prüfen. Erst dann:
5. **Alte JSON-Dateien löschen** (optional, sie sind das Backup):
   ```
   sudo rm -rf /opt/fundus/data/chain/blocks
   ```

Schlägt die Verifikation fehl, bricht die Migration ab und die alten JSON-Dateien
bleiben unangetastet — der Node läuft mit ihnen weiter, bis das Problem geklärt ist.

## Einen Block inspizieren (ersetzt "cat block.json")

```
sudo /opt/fundus/bin/fundus-admin dump-block --dir /opt/fundus/data/chain --height 42
```

Gibt den Block als lesbares JSON aus.

## Sicherheitsnetz

- Die Migration verändert Blöcke NICHT inhaltlich (nur JSON→binär).
- Nach der Migration wird der State per Replay aus der DB neu aufgebaut und der
  Head-Hash gegen `meta.json` geprüft. Stimmt er, ist die Migration bewiesen
  verlustfrei.
- Die alten JSON-Dateien werden nie automatisch gelöscht.
- `--dir` muss auf das Chain-Verzeichnis zeigen (Standard `/opt/fundus/data/chain`;
  ggf. anpassen, falls euer DataDir abweicht).

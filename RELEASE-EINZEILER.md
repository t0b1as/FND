> **Empfohlen: `push-release.ps1`** erledigt alles in einem Schritt – Binaries bauen,
> Update-Paket schnüren, Quellcode + Tag pushen, GitHub-Release mit Revisionsnamen
> anlegen, Manifest signieren und pushen:
>
> ```powershell
> pwsh -File .\push-release.ps1 -Notes "Kurzbeschreibung des Meilensteins"
> pwsh -File .\push-release.ps1 -DryRun     # nur bauen + signieren, nichts hochladen
> ```
>
> Wichtig: Das Update-Paket enthält fertige Binaries (der Pi kann nicht kompilieren).
> Das Quellcode-ZIP allein eignet sich NICHT als Update-Asset. Der manuelle Weg
> unten bleibt als Referenz.

# Release-Einzeiler — neue Master-Version signieren & veröffentlichen

Repo: `https://github.com/t0b1as/FND`

Voraussetzungen (einmalig):
- `fundus-admin` gebaut (`build-admin-tools.ps1`) und im PATH oder mit `./` davor.
- GitHub CLI `gh` installiert und eingeloggt (`gh auth login`).
- Fee-Collector-Key in einer Datei mit der Zeile `FUNDUS_WALLET_PRIV_KEY=0x…`.
  **AUSSERHALB des Repos halten, niemals committen.** Empfohlen: ein Pfad im
  Home-Verzeichnis, z.B. `~/.fundus/feecollector.key` — dann kann Git ihn
  strukturell gar nicht erfassen. Die Beispiele unten nutzen diesen Pfad.
- Das fertige ZIP liegt bereit (z.B. `FND R002.zip`).

Sicherheitsnetz: Selbst wenn eine Key-Datei doch mal im Repo-Ordner landet, hält
diese `.gitignore`-Zeile sie draußen (gegen ein versehentliches `git add -A`):

```
# .gitignore im Repo-Root
*.key
feecollector*
```

Der `git add manifest.json` im Einzeiler fügt ohnehin NUR die manifest.json hinzu
(kein `git add .`), der Key wird also nicht mitgepusht — die `.gitignore` und der
Pfad außerhalb des Repos sind die zusätzliche Absicherung.

Die Reihenfolge ist wichtig: erst das ZIP ins Release hochladen (damit die
Download-URL existiert), dann signieren (die URL wandert ins Manifest), dann das
Manifest pushen.

---

## Einzeiler (bash / macOS / Linux / WSL / Git-Bash)

Setze `VER` und den ZIP-Pfad, der Rest läuft durch:

```bash
VER=R002; ZIP="FND ${VER}.zip"; ASSET="FND-${VER}.zip"; \
gh release create "$VER" "$ZIP#$ASSET" -R t0b1as/FND -t "$VER" -n "Fundus $VER" && \
fundus-admin sign --version "$VER" --desc "Fundus $VER" --zip "$ZIP" \
  --url "https://github.com/t0b1as/FND/releases/download/$VER/$ASSET" \
  --key-file ~/.fundus/feecollector.key > manifest.json && \
git -C /pfad/zum/FND add manifest.json && \
git -C /pfad/zum/FND commit -m "Update-Manifest $VER" && \
git -C /pfad/zum/FND push origin main
```

Was jeder Teil tut:
1. `gh release create "$VER" "$ZIP#$ASSET"` — legt das Release `R002` an und lädt
   das ZIP als Asset unter dem sauberen Namen `FND-R002.zip` hoch (das `#` setzt
   den Asset-Namen, Leerzeichen im Dateinamen bleiben so draußen).
2. `fundus-admin sign … > manifest.json` — berechnet den Argon2id-Hash, signiert
   Version+Hash mit dem Fee-Collector-Key und schreibt das fertige Manifest nach
   `manifest.json` (nur das JSON landet in der Datei; Statusmeldungen gehen nach
   stderr auf den Bildschirm).
3. `git add/commit/push` — schiebt die `manifest.json` ins Repo. Nodes mit
   gesetzter `FUNDUS_UPDATE_MANIFEST_URL` finden sie beim nächsten Poll.

Ersetze `/pfad/zum/FND` durch dein lokales Repo-Verzeichnis. Läuft der Einzeiler
aus dem Repo-Ordner selbst, kannst du die drei `git -C /pfad/zum/FND` durch
schlichtes `git` ersetzen.

---

## Manifest-URL für die Nodes

Damit die Nodes die `manifest.json` im Repo-Root pollen, in `fundus.env`:

```
FUNDUS_UPDATE_MANIFEST_URL=https://raw.githubusercontent.com/t0b1as/FND/main/manifest.json
```

---

## PowerShell-Variante (Windows)

```powershell
$VER="R002"; $ZIP="FND $VER.zip"; $ASSET="FND-$VER.zip"
gh release create $VER "$ZIP#$ASSET" -R t0b1as/FND -t $VER -n "Fundus $VER"
fundus-admin sign --version $VER --desc "Fundus $VER" --zip $ZIP `
  --url "https://github.com/t0b1as/FND/releases/download/$VER/$ASSET" `
  --key-file $HOME\.fundus\feecollector.key | Out-File -Encoding ascii manifest.json
git add manifest.json; git commit -m "Update-Manifest $VER"; git push origin main
```

Hinweis PowerShell: `| Out-File -Encoding ascii` statt `>` — die Standard-
Umleitung von PowerShell schreibt UTF-16, das der JSON-Parser nicht mag.

---

## Sicherheit (kurz)

- Der Fee-Collector-Key kann auf ALLEN Nodes Code als root installieren. Er ist
  so kritisch wie die Node-Seeds — offline, getrennt, nie auf einem Node.
- Immer erst auf EINEM Pi testen, bevor das Manifest fürs ganze Netz live geht.
- Node UND Helper prüfen die Signatur; ohne gültige Signatur passiert nichts.

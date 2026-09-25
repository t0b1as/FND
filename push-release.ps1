<#
=============================================================================
  push-release.ps1 - Neue FUNDUS-Revision auf GitHub veroeffentlichen
=============================================================================

  Aufruf (im Ordner mit FND.zip):

      pwsh -File .\push-release.ps1                      # veroeffentlichen
      pwsh -File .\push-release.ps1 -DryRun              # nur bauen + signieren, nichts hochladen
      pwsh -File .\push-release.ps1 -Notes "Meilenstein: Hybrid-Datenzugriff"

  Ablauf:
    1. Prueft Werkzeuge (git, gh, go, fundus-admin), gh-Login und Signaturschluessel.
    2. Liest die Revision aus FND.zip (revision.txt) und prueft sie gegen
       NodeRevision im Go-Code. Die GitHub-Version heisst exakt so (z.B. R427).
    3. Baut fundus-node und fundus-helper fuer linux/arm64 und linux/arm (32 Bit).
    4. Schnuert das Update-Paket FND-<Rev>-update.zip: Binaries + Oberflaeche
       (lua/) + revision.txt + Doku. Genau dieses Paket installieren die Nodes.
    5. Pusht den Quellcode in den Repo-Klon (Commit + Tag <Rev>).
    6. Legt das GitHub-Release <Rev> an (Update-Paket + Quellcode-ZIP).
    7. Signiert das Manifest mit dem Fee-Collector-Schluessel und pusht
       manifest.json. Die Nodes finden die neue Version beim naechsten Abgleich
       (spaetestens nach 15 Minuten) und bieten sie unter
       Einstellungen -> Software-Update zur Installation an.

  Einmalige Voraussetzungen:
    - Git, GitHub CLI (gh auth login), Go 1.24+
    - fundus-admin im PATH (build-admin-tools.ps1 aus "FND admin.zip")
    - Signaturschluessel AUSSERHALB des Repos, Standard: ~\.fundus\feecollector.key
      (Inhalt: FUNDUS_WALLET_PRIV_KEY=0x...). Niemals committen.
=============================================================================
#>
param(
    [string]$ZipPath = ".\FND.zip",
    [string]$Repo    = "t0b1as/FND",
    [string]$RepoDir = "",                                   # lokaler Klon; leer = ..\FND-repo
    [string]$KeyFile = (Join-Path $HOME ".fundus\feecollector.key"),
    [string]$Notes   = "",
    [string]$GoCmd   = "go",
    [switch]$DryRun
)

# "Continue": Windows PowerShell 5.1 wertet umgeleitete stderr-Ausgaben externer
# Programme (git, gh) sonst als Abbruch. Exit-Codes werden ueberall geprueft,
# wichtige Cmdlets brechen per -ErrorAction Stop selbst ab.
$ErrorActionPreference = "Continue"
function Step([string]$t) { Write-Host "`n==> $t" -ForegroundColor Cyan }
function Ok([string]$t)   { Write-Host "    OK   $t" -ForegroundColor Green }
function Info([string]$t) { Write-Host "         $t" -ForegroundColor Gray }
function Fail([string]$t) { Write-Host "    FEHLER  $t" -ForegroundColor Red; exit 1 }
function Run([string]$what, [scriptblock]$cmd) {
    & $cmd
    if ($LASTEXITCODE -ne 0) { Fail "$what (Exit $LASTEXITCODE)" }
}

Add-Type -AssemblyName System.IO.Compression.FileSystem

$ScriptStand = "R461"   # Stand dieses Skripts (bei jedem Release mitgezogen)
Write-Host "push-release.ps1 - Stand $ScriptStand" -ForegroundColor Cyan

# -- 1. Voraussetzungen -------------------------------------------------------
Step "Voraussetzungen pruefen"
# Werkzeuge suchen: erst PATH, dann uebliche Installationsorte. Frisch
# installierte Programme fehlen sonst oft im PATH der offenen PowerShell.
function Find-Tool([string]$name, [string[]]$candidates, [string]$hint, [switch]$Optional) {
    $c = Get-Command $name -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
    foreach ($p in $candidates) {
        if ($p -and (Test-Path $p)) { return (Resolve-Path $p).Path }
    }
    if ($Optional) { return $null }
    Fail "$name nicht gefunden. $hint"
}
$pf   = $env:ProgramFiles
$pf86 = ${env:ProgramFiles(x86)}
$lad  = $env:LOCALAPPDATA
$here = $PSScriptRoot
$GIT = Find-Tool "git" @("$pf\Git\cmd\git.exe", "$pf86\Git\cmd\git.exe", "$lad\Programs\Git\cmd\git.exe") `
    "Installieren: winget install --id Git.Git"
$GH  = Find-Tool "gh" @("$pf\GitHub CLI\gh.exe", "$pf86\GitHub CLI\gh.exe", "$lad\Programs\GitHub CLI\gh.exe") `
    "Das ist die GitHub CLI (nicht GitHub Desktop). Installieren: winget install --id GitHub.cli - danach neues PowerShell-Fenster oeffnen und einmal 'gh auth login'."
$GO  = Find-Tool $GoCmd @("$pf\Go\bin\go.exe", "$env:USERPROFILE\go\bin\go.exe", "$env:USERPROFILE\sdk\go\bin\go.exe") `
    "Installieren: winget install --id GoLang.Go"
# fundus-admin IMMER frisch aus "FND admin.zip" bauen, wenn das ZIP daneben
# liegt: so passt das Signierwerkzeug garantiert zum Code dieses Releases
# (ein altes fundus-admin.exe kann z.B. eine veraltete Signaturpruefung haben).
$adminRev = ""
$adminZip = Join-Path $here "FND admin.zip"
if (Test-Path $adminZip) {
    Info "Baue fundus-admin aus 'FND admin.zip' (ca. 1 Min.)..."
    $adm = Join-Path ([IO.Path]::GetTempPath()) ("fnd-admin-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
    Expand-Archive -Path $adminZip -DestinationPath $adm -Force -ErrorAction Stop
    # Revision des Admin-Pakets merken: muss zu FND.zip passen (Pruefung weiter unten).
    $adminRev = ""
    $admSrv = Join-Path $adm "go\internal\api\server.go"
    if (Test-Path $admSrv) {
        $mm = Select-String -Path $admSrv -Pattern 'NodeRevision = "(R\d+)"'
        if ($mm) { $adminRev = $mm.Matches[0].Groups[1].Value }
    }
    New-Item -ItemType Directory -Force -Path (Join-Path $here "bin") -ErrorAction Stop | Out-Null
    $FA = Join-Path $here "bin\fundus-admin.exe"
    Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM -ErrorAction SilentlyContinue   # fuer Windows bauen
    Push-Location (Join-Path $adm "go")
    try {
        Run "go mod tidy (admin)" { & $GO mod tidy }
        Run "Build fundus-admin" { & $GO build -ldflags "-s -w" -o $FA ./cmd/fundus-admin }
    } finally {
        Pop-Location
        Remove-Item -Recurse -Force $adm -ErrorAction SilentlyContinue
    }
    Ok "fundus-admin gebaut: $FA"
} else {
    $FA = Find-Tool "fundus-admin" @("$here\bin\fundus-admin.exe", "$here\..\FND admin\bin\fundus-admin.exe") `
        "'FND admin.zip' neben das Skript legen - dann wird fundus-admin automatisch gebaut."
    Info "HINWEIS: vorhandenes fundus-admin wird verwendet - es muss zum Release passen."
}
Info "git: $GIT"
Info "gh:  $GH"
Info "go:  $GO"
Info "fundus-admin: $FA"
& $GH auth status *> $null
if ($LASTEXITCODE -ne 0) { Fail "GitHub CLI nicht angemeldet - einmal 'gh auth login' ausfuehren" }
if (-not (Test-Path $ZipPath)) { Fail "ZIP nicht gefunden: $ZipPath" }
if (-not (Test-Path $KeyFile)) { Fail "Signaturschluessel fehlt: $KeyFile" }
$keyFull = (Resolve-Path $KeyFile -ErrorAction Stop).Path
Ok "git, gh, go, fundus-admin, Schluessel vorhanden"

# -- 2. Revision lesen --------------------------------------------------------
Step "Revision ermitteln"
$work = Join-Path ([IO.Path]::GetTempPath()) ("fnd-release-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
$src  = Join-Path $work "src"
$bund = Join-Path $work "bundle"
New-Item -ItemType Directory -Force -Path $src, (Join-Path $bund "bin") -ErrorAction Stop | Out-Null
Expand-Archive -Path $ZipPath -DestinationPath $src -Force -ErrorAction Stop

$revNum = ((Get-Content (Join-Path $src "revision.txt") -TotalCount 1) -replace '\D', '')
if (-not $revNum) { Fail "revision.txt im ZIP fehlt oder ist leer" }
$VER = "R$revNum"
$goRev = (Select-String -Path (Join-Path $src "go\internal\api\server.go") -Pattern 'NodeRevision = "(R\d+)"').Matches[0].Groups[1].Value
if ($goRev -ne $VER) { Fail "revision.txt ($VER) passt nicht zu NodeRevision im Code ($goRev)" }
Ok "Version $VER"
# Beide Pakete muessen zur selben Revision gehoeren - sonst signiert ein altes
# fundus-admin (z.B. mit veralteter Signaturpruefung).
if ($adminRev -and $adminRev -ne $VER) {
    $others = (Get-ChildItem -Path $here -Filter "FND admin*.zip" -ErrorAction SilentlyContinue | ForEach-Object { $_.Name }) -join ", "
    Fail ("'FND admin.zip' ist $adminRev, 'FND.zip' ist $VER. Bitte die passende 'FND admin.zip' neben das Skript legen. " +
          "Hinweis: Der Browser speichert neue Downloads oft als 'FND admin (1).zip'. Gefunden: $others")
}
if ($adminRev) { Ok "FND admin.zip passt ($adminRev)" }
if ($VER -ne $ScriptStand) {
    Write-Host "    HINWEIS  Skript-Stand $ScriptStand, ZIP-Stand $VER - ggf. push-release.ps1 aus dem neuen ZIP verwenden." -ForegroundColor Yellow
}

if (-not $DryRun) {
    & $GH release view $VER -R $Repo *> $null
    if ($LASTEXITCODE -eq 0) { Fail "Release $VER existiert auf GitHub bereits - Revision erhoehen" }
}

# -- 3. Binaries bauen --------------------------------------------------------
Step "Binaries bauen (linux/arm64 + linux/arm)"
Push-Location (Join-Path $src "go")
try {
    # Wie deploy-fundus.ps1: go.mod/go.sum mit dem Code abgleichen. Der
    # abgeglichene Stand wird mit dem Quellcode ins Repo uebernommen.
    Run "go mod tidy" { & $GO mod tidy }
    Run "go mod download" { & $GO mod download }
    foreach ($arch in @("arm64", "arm")) {
        $env:GOOS = "linux"; $env:GOARCH = $arch; $env:CGO_ENABLED = "0"
        if ($arch -eq "arm") { $env:GOARM = "7" } else { Remove-Item Env:GOARM -ErrorAction SilentlyContinue }
        Run "Build fundus-node ($arch)" {
            & $GO build -trimpath -ldflags "-s -w -X main.Version=$VER" -o (Join-Path $bund "bin\fundus-node-linux-$arch") ./cmd/fundus-node
        }
        Run "Build fundus-helper ($arch)" {
            & $GO build -trimpath -ldflags "-s -w -X main.Version=$VER" -o (Join-Path $bund "bin\fundus-helper-linux-$arch") ./cmd/fundus-helper
        }
        Ok "linux/$arch"
    }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}

# -- 4. Update-Paket ----------------------------------------------------------
Step "Update-Paket schnueren"
Copy-Item -Recurse -Force (Join-Path $src "lua") (Join-Path $bund "lua") -ErrorAction Stop
foreach ($f in @("revision.txt", "MANUAL.md", "README.md")) {
    $p = Join-Path $src $f
    if (Test-Path $p) { Copy-Item -Force $p $bund }
}
$bundleZip = Join-Path $work "FND-$VER-update.zip"
# ZIP selbst schreiben: Windows PowerShell 5.1 (CreateFromDirectory) legt die
# Pfade mit Backslashes ab ("bin\fundus-node"). unzip auf dem Pi meldet das als
# Fehler (Exit 1) - Nodes bis R446 brechen die Installation dann ab.
Add-Type -AssemblyName System.IO.Compression
$baseLen = ((Resolve-Path $bund).Path.TrimEnd('\')).Length + 1
$fs = [IO.File]::Open($bundleZip, [IO.FileMode]::Create)
try {
    $za = New-Object IO.Compression.ZipArchive($fs, [IO.Compression.ZipArchiveMode]::Create)
    try {
        Get-ChildItem -Path $bund -Recurse -File | ForEach-Object {
            $rel = $_.FullName.Substring($baseLen).Replace('\', '/')
            [void][IO.Compression.ZipFileExtensions]::CreateEntryFromFile($za, $_.FullName, $rel, [IO.Compression.CompressionLevel]::Optimal)
        }
    } finally { $za.Dispose() }
} finally { $fs.Dispose() }
# Gegenpruefung: kein Eintrag mit Backslash, Binaries fuer beide Architekturen vorhanden.
$zr = [IO.Compression.ZipFile]::OpenRead($bundleZip)
try {
    $names = @($zr.Entries | ForEach-Object { $_.FullName })
} finally { $zr.Dispose() }
if ($names | Where-Object { $_ -like '*\*' }) { Fail "Update-Paket enthaelt Backslash-Pfade" }
foreach ($need in @("bin/fundus-node-linux-arm64", "bin/fundus-helper-linux-arm64", "bin/fundus-node-linux-arm", "lua/app.lua", "revision.txt")) {
    if ($names -notcontains $need) { Fail "Update-Paket unvollstaendig: $need fehlt" }
}
Ok ("FND-$VER-update.zip ({0:N1} MB)" -f ((Get-Item $bundleZip).Length / 1MB))

$assetName = "FND-$VER-update.zip"
$assetUrl  = "https://github.com/$Repo/releases/download/$VER/$assetName"

# -- Manifest signieren (auch im DryRun, zur Kontrolle) -----------------------
Step "Manifest signieren"
$desc = if ($Notes) { "FUNDUS $VER - $Notes" } else { "FUNDUS $VER" }
$manifestJson = (& $FA sign --version $VER --desc $desc --zip $bundleZip --url $assetUrl --key-file $keyFull) -join "`n"
if ($LASTEXITCODE -ne 0) { Fail "Signieren fehlgeschlagen" }
try { $mf = $manifestJson | ConvertFrom-Json } catch { Fail "fundus-admin lieferte kein gueltiges JSON" }
if ($mf.version -ne $VER -or -not $mf.signature) { Fail "Manifest unvollstaendig (Version/Signatur)" }
Ok "signiert, Hash $($mf.node_argon2.Substring(0,16))..."

if ($DryRun) {
    Step "DryRun - nichts hochgeladen"
    Info "Update-Paket: $bundleZip"
    Info "Manifest:"
    Write-Host $manifestJson
    exit 0
}

# -- 5. Quellcode pushen ------------------------------------------------------
Step "Quellcode in den Repo-Klon uebernehmen"
if (-not $RepoDir) { $RepoDir = Join-Path (Split-Path $PSScriptRoot -Parent) "FND-repo" }
if (-not (Test-Path (Join-Path $RepoDir ".git"))) {
    Info "Klone $Repo nach $RepoDir"
    Run "git clone" { & $GIT clone "https://github.com/$Repo.git" $RepoDir }
}
Run "git pull" { & $GIT -C $RepoDir pull --ff-only }

# Spiegeln: .git, manifest.json, .gitignore und LICENSE bleiben; Binaries und
# Schluessel kommen nie ins Repo.
robocopy $src $RepoDir /MIR /NFL /NDL /NJH /NJS /NP `
    /XD .git bin node_modules artifacts cache target `
    /XF manifest.json .gitignore LICENSE *.key .env | Out-Null
if ($LASTEXITCODE -ge 8) { Fail "robocopy (Exit $LASTEXITCODE)" }

$gi = Join-Path $RepoDir ".gitignore"
$giText = if (Test-Path $gi) { Get-Content $gi -Raw } else { "" }
foreach ($line in @("*.key", "feecollector*", "bin/", ".env")) {
    if ($giText -notmatch [regex]::Escape($line)) { Add-Content -Path $gi -Value $line }
}

Run "git add" { & $GIT -C $RepoDir add -A }
& $GIT -C $RepoDir diff --cached --quiet
if ($LASTEXITCODE -ne 0) {
    $commitMsg = "FUNDUS $VER"
    if ($Notes) { $commitMsg = "FUNDUS $VER - $Notes" }
    Run "git commit" { & $GIT -C $RepoDir commit -q -m $commitMsg }
}
Run "git tag"  { & $GIT -C $RepoDir tag -a $VER -m "FUNDUS $VER" }
Run "git push" { & $GIT -C $RepoDir push -q origin HEAD }
Run "git push tag" { & $GIT -C $RepoDir push -q origin $VER }
Ok "Quellcode und Tag $VER gepusht"

# -- 6. GitHub-Release --------------------------------------------------------
Step "GitHub-Release $VER anlegen"
$notesText = if ($Notes) { $Notes } else { "FUNDUS $VER" }
$notesText += "`n`nInstallation auf laufenden Nodes: Einstellungen -> Software-Update -> Jetzt installieren.`nNeuinstallation: siehe MANUAL.md."
Run "gh release create" {
    & $GH release create $VER "$bundleZip#$assetName" "$((Resolve-Path $ZipPath).Path)#FND-$VER-source.zip" `
        -R $Repo -t "FUNDUS $VER" -n $notesText --verify-tag
}
Ok "https://github.com/$Repo/releases/tag/$VER"

# -- 7. Manifest veroeffentlichen ----------------------------------------------
Step "Manifest veroeffentlichen"
[IO.File]::WriteAllText((Join-Path $RepoDir "manifest.json"), $manifestJson + "`n", (New-Object Text.UTF8Encoding($false)))
Run "git add manifest" { & $GIT -C $RepoDir add manifest.json }
Run "git commit manifest" { & $GIT -C $RepoDir commit -q -m "Update-Manifest $VER" }
Run "git push manifest" { & $GIT -C $RepoDir push -q origin HEAD }
Ok "manifest.json gepusht"

Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
Write-Host "`nFertig: FUNDUS $VER ist veroeffentlicht." -ForegroundColor Green
Write-Host "Die Nodes bieten das Update spaetestens in 15 Minuten unter Einstellungen -> Software-Update an." -ForegroundColor Green

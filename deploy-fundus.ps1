#!/usr/bin/env pwsh
# =============================================================================
#  Fundus Marketplace – Deploy Script (Single Pi)
#
#  Aufruf:
#    .\deploy-fundus.ps1
#    .\deploy-fundus.ps1 -PiHost 192.168.1.10 -ZipPath ".\FND.zip"
#
#  Parameter:
#    -ZipPath      Pfad zur ZIP-Datei         (Standard: .\FND.zip)
#    -AdminZip     Admin-ZIP optional          (Standard: .\FND admin.zip)
#    -PiHost       IP des Pi                  (Standard: interaktiv abfragen)
#    -PiUser       SSH-User                   (Standard: interaktiv abfragen)
#    -PiPort       SSH-Port                   (Standard: 22)
#    -RemoteDir    Zielverzeichnis             (Standard: /opt/fundus)
#    -ServiceName  systemd Service-Name       (Standard: fundus-node)
#    -FundusUser   Betriebssystem-User        (Standard: fundus)
#    -RpcUrl       Gnosis-RPC (nur optionaler ERC-20-Pfad; NICHT für die eigene Chain nötig)
#    -ChainId      Gnosis Chain-ID (optionaler ERC-20-Pfad; Standard 100)
#    -SkipSetup    Setup-Prüfung überspringen (Standard: false)
# =============================================================================

param(
    [string]$ZipPath     = ".\FND.zip",
    [string]$AdminZip    = ".\FND admin.zip",
    [string]$PiHost      = "",
    [string]$PiUser      = "",      # Leer = interaktiv abfragen
    [int]   $PiPort      = 22,
    [string]$RemoteDir   = "/opt/fundus",
    [string]$ServiceName = "fundus-node",
    [string]$FundusUser  = "fundus",
    [string]$RpcUrl      = "https://rpc.gnosischain.com",  # optional, nur ERC-20-Pfad (eigene Chain braucht das nicht)
    [string]$ChainId     = "100",   # optional ERC-20-Pfad: 100=Gnosis, 10200=Chiado, 31337=Hardhat
    [string]$FndAddress  = "",      # FND-Contract-Adresse auf dieser Chain (leer = Wallet zeigt "kein Contract")
    [string]$FeeCollector = "0xea5594a7cc26d2456e9a033481d01a5ba101c8f8",  # Gebühren-Adresse (BLAKE3/256-MiB-Ableitung via fnd-wallet)
    [switch]$SkipSetup   = $false,
    [switch]$WriteEnv    = $false,  # fundus.env (über)schreiben; sonst nur bei Erstinstallation
    [switch]$Rebuild     = $false,  # Binary neu kompilieren erzwingen
    [string]$GoCmd       = "",      # Go-Toolchain (z.B. "go1.25.4"), leer = auto
    [string]$BootstrapPeer = "",    # Multiaddr eines bekannten Peers (z.B. /ip4/10.10.11.39/tcp/4001/p2p/12D3...)
    [string]$SwapHtlcProgram = "",  # Solana-HTLC-Programm-ID für Atomic Swaps (leer = Swap-Ausführung inaktiv)
    [string]$SolanaRpc     = "",    # Solana-RPC-URL (z.B. http://10.10.11.85:8899 lokal, oder Devnet/Mainnet)
    [string]$SudoPass    = "",      # sudo-Passwort (spart interaktive Abfrage)
    [string]$CertPass    = "",      # Cert-/Wallet-Passphrase (spart interaktive Abfrage)
    [string]$AdminPass   = "",      # Admin-Web-Passwort (Basic-Auth, getrennt vom Linux-Account); leer = htpasswd unverändert lassen
    [string]$AdminUser   = "admin", # Benutzername für die Admin-Basic-Auth
    [string]$PiKey       = "",      # Expliziter SSH-Key-Pfad (optional)
    [string]$Arch        = "arm64"  # Ziel-Architektur fuer Build (arm64|arm)
)

# =============================================================================
#  Hilfsfunktionen
# =============================================================================
function Write-Step([string]$msg)  { Write-Host "`n  --> $msg" -ForegroundColor Cyan }
function Write-Ok([string]$msg)    { Write-Host "      [OK]      $msg" -ForegroundColor Green }
function Write-Fixed([string]$msg) { Write-Host "      [BEHOBEN] $msg" -ForegroundColor Yellow }
function Write-Info([string]$msg)  { Write-Host "      [INFO]    $msg" -ForegroundColor DarkGray }
function Write-Warn([string]$msg)  { Write-Host "      [WARN]    $msg" -ForegroundColor Yellow }
function Write-Fail([string]$msg)  {
    Write-Host "`n  [FEHLER] $msg" -ForegroundColor Red
    exit 1
}

# -----------------------------------------------------------------------------
#  Invoke-LocalBuild – kompiliert das Binary auf dem PC (Cross-Compile)
#  Wird automatisch aufgerufen wenn kein bin\fundus-node existiert.
# -----------------------------------------------------------------------------
function Invoke-LocalBuild([string]$targetArch) {
    Write-Step "Binary cross-kompilieren (linux/$targetArch)..."

    # Go-Toolchain waehlen. Go 1.26 hat einen quic-go-Inkompatibilitaetsbug
    # ("where's my session ticket?" Panic). Daher bevorzugt go1.25.x nutzen,
    # falls installiert. Mit -GoCmd kann explizit gewaehlt werden.
    $script:GoBin = $GoCmd
    if (-not $script:GoBin) {
        # Auto: go1.25.x bevorzugen wenn vorhanden
        $g125 = Get-Command "go1.25.4" -ErrorAction SilentlyContinue
        if (-not $g125) {
            # irgendeine go1.25.x im go/bin suchen
            $dl = Get-ChildItem "$env:USERPROFILE\go\bin\go1.25*" -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($dl) { $g125 = $dl }
        }
        if ($g125) {
            $script:GoBin = $g125.Name -replace '\.exe$',''
            Write-Info "Go 1.25 gefunden - nutze $($script:GoBin) (vermeidet Go-1.26 quic-go-Bug)"
        } else {
            $script:GoBin = "go"
        }
    }
    if (-not (Get-Command $script:GoBin -ErrorAction SilentlyContinue)) {
        Write-Fail "Go-Toolchain '$($script:GoBin)' nicht gefunden (https://go.dev/dl/)."
    }
    Write-Info "Go: $(& $script:GoBin version)"
    if ((& $script:GoBin version) -match "go1\.26") {
        Write-Warn "Go 1.26 erkannt - quic-go kann 'session ticket' Panic werfen."
        Write-Warn "Empfehlung: go install golang.org/dl/go1.25.4@latest; go1.25.4 download"
        Write-Warn "Dann erneut mit -GoCmd go1.25.4 deployen."
    }

    # go-Verzeichnis finden (neben dem Skript oder Skript liegt drin)
    $goDir = Join-Path $PSScriptRoot "go"
    if (-not (Test-Path (Join-Path $goDir "go.mod"))) {
        if (Test-Path (Join-Path $PSScriptRoot "go.mod")) { $goDir = $PSScriptRoot }
        else { Write-Fail "go.mod nicht gefunden - 'go'-Ordner fehlt neben dem Skript." }
    }

    Push-Location $goDir
    try {
        Write-Info "Dependencies laden (go mod tidy)..."
        $env:GOFLAGS = "-mod=mod"
        # go schreibt Fortschritt ("downloading ...") auf stderr; PowerShell würde
        # das sonst als rote Error-Records anzeigen, obwohl alles ok ist. Daher
        # stderr→stdout mergen und als reinen Text ausgeben.
        (& $script:GoBin mod tidy 2>&1 | ForEach-Object { "$_" }) | Out-Host
        if ($LASTEXITCODE -ne 0) { Write-Fail "go mod tidy fehlgeschlagen" }

        $env:GOOS = "linux"; $env:GOARCH = $targetArch; $env:CGO_ENABLED = "0"
        if ($targetArch -eq "arm") { $env:GOARM = "7" }

        $outDir = Join-Path $PSScriptRoot "bin"
        New-Item -ItemType Directory -Path $outDir -Force | Out-Null
        $outFile = Join-Path $outDir "fundus-node"

        Write-Info "Kompiliere (dauert 1-3 Min)..."
        (& $script:GoBin build -ldflags="-s -w -X main.Version=R$(((Get-Content (Join-Path $PSScriptRoot 'revision.txt') -TotalCount 1 -ErrorAction SilentlyContinue) -replace '\D',''))" -o $outFile ./cmd/fundus-node 2>&1 | ForEach-Object { "$_" }) | Out-Host
        if ($LASTEXITCODE -ne 0) { Write-Fail "Build fehlgeschlagen" }

        $sz = [math]::Round((Get-Item $outFile).Length / 1MB, 1)
        Write-Ok "Binary gebaut: bin\fundus-node ($sz MB, linux/$targetArch)"

        # Privilegierter Helfer-Daemon (Mount/WLAN). Separate kleine Binary, die
        # als root läuft. Gleiche Cross-Compile-Umgebung (GOOS/GOARCH schon gesetzt).
        $helperOut = Join-Path $outDir "fundus-helper"
        # WICHTIG: alte Helper-Binary VORHER löschen, damit bei einem Build-Fehler
        # NICHT versehentlich eine veraltete Binary hochgeladen wird (genau dieser
        # Bug ließ Mount-Fixes nie ankommen).
        if (Test-Path $helperOut) { Remove-Item $helperOut -Force }
        Write-Info "Kompiliere fundus-helper..."
        # Build OHNE Pipe aufrufen, sonst enthält $LASTEXITCODE den Exit-Code von
        # Out-Host (immer 0) statt den von 'go build' → Fehler würden verschluckt.
        & $script:GoBin build -ldflags="-s -w" -o $helperOut ./cmd/fundus-helper 2>&1 | Out-Host
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path $helperOut)) {
            Write-Fail "fundus-helper Build fehlgeschlagen — Deploy abgebrochen (Mount/WLAN wären sonst veraltet)."
        }
        $hsz = [math]::Round((Get-Item $helperOut).Length / 1MB, 1)
        Write-Ok "Binary gebaut: bin\fundus-helper ($hsz MB, linux/$targetArch)"
    } finally {
        $env:GOOS=""; $env:GOARCH=""; $env:GOARM=""; $env:CGO_ENABLED=""; $env:GOFLAGS=""
        Pop-Location
    }
}

# SSH-Befehle werden base64-kodiert übergeben damit PowerShell 5
# keine Probleme mit &&, ||, |, $VAR etc. hat.
# Schreibt SSH-Config damit "ssh tobias@ip" ohne -i funktioniert
function Add-SSHConfig {
    if (-not $script:SelectedKeyPath) { return }
    $configPath = "$env:USERPROFILE\.ssh\config"
    $hostEntry  = "Host $PiHost"
    $existing   = ""
    if (Test-Path $configPath) {
        $existing = Get-Content $configPath -Raw -ErrorAction SilentlyContinue
    }
    if ($existing -notmatch [regex]::Escape("Host $PiHost")) {
        $entry = @"

Host $PiHost
    User $PiUser
    IdentityFile $script:SelectedKeyPath
    StrictHostKeyChecking accept-new
"@
        Add-Content -Path $configPath -Value $entry -Encoding UTF8
        Write-Ok "SSH-Config aktualisiert: ssh ${PiUser}@${PiHost} funktioniert jetzt ohne -i"
    }
}

# SSH-Architektur: Key-Auth fuer alle Befehle (BatchMode + Output-Capture).
# Connect-SSH stellt sicher dass der Key auf dem Pi ist - einmalig per Passwort.
# Danach: alle Invoke-SSH / Invoke-SSH-Safe per Key, kein Passwort mehr noetig.

function Invoke-SSHRaw([string]$cmd) {
    # Wenn sudo-Passwort gesetzt: "echo PW | sudo -S" voranstellen
    if ($script:SudoPass -and $cmd -match "sudo") {
        $cmd = "echo '$script:SudoPass' | sudo -S -p '' sh -c " + "'" + ($cmd -replace "sudo\s+", "") + "'"
    }
    $keyOpt  = if ($script:SelectedKeyPath) { @("-i", $script:SelectedKeyPath) } else { @() }
    # Auth-Optionen: bei Passwort-Login ControlMaster nutzen (Passwort nur 1x),
    # bei Key-Auth Passwort komplett deaktivieren (kein Haengen am Prompt).
    if ($script:UsePassword) {
        $authOpt = @(
            "-o", "PasswordAuthentication=yes",
            "-o", "ControlMaster=auto",
            "-o", "ControlPath=$script:CtlPath",
            "-o", "ControlPersist=300")
    } else {
        $authOpt = @("-o", "PasswordAuthentication=no")
    }
    $sshArgs = $keyOpt + @(
        "-p", $PiPort,
        "-o", "ConnectTimeout=15",
        "-o", "ServerAliveInterval=10",
        "-o", "ServerAliveCountMax=12",
        "-o", "StrictHostKeyChecking=accept-new") + $authOpt + @(
        "${PiUser}@${PiHost}",
        $cmd)
    $errFile = [IO.Path]::GetTempFileName()
    $result  = & ssh @sshArgs 2>$errFile
    $rc      = $LASTEXITCODE
    Remove-Item $errFile -Force -ErrorAction SilentlyContinue
    $out = ($result | Where-Object { $_ } | ForEach-Object { $_.Trim() }) -join "`n"
    return [PSCustomObject]@{ Output = $out; ExitCode = $rc }
}

function Invoke-SSH([string]$cmd) {
    $r = Invoke-SSHRaw $cmd
    if ($r.ExitCode -ne 0) {
        Write-Fail "SSH fehlgeschlagen (Exit $($r.ExitCode)):`n  Ausgabe: $($r.Output)"
    }
    return $r.Output
}

function Invoke-SSH-Safe([string]$cmd) {
    return Invoke-SSHRaw $cmd
}

function Connect-SSH {
    $keyArg = if ($script:SelectedKeyPath) { @("-i", $script:SelectedKeyPath) } else { @() }
    if ($script:UsePassword) {
        # Passwort-Login: erste Verbindung oeffnet ControlMaster, ssh fragt Passwort
        # interaktiv ab (genau einmal, danach laeuft alles ueber den Socket).
        Write-Host "  Passwort fuer ${PiUser}@${PiHost} eingeben:" -ForegroundColor Cyan
        $authArgs = @(
            "-o", "PasswordAuthentication=yes",
            "-o", "ControlMaster=auto",
            "-o", "ControlPath=$script:CtlPath",
            "-o", "ControlPersist=300")
        & ssh @keyArg -p $PiPort -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new @authArgs "${PiUser}@${PiHost}" "echo connected" | Out-Host
        # Verbindung pruefen ueber den Master-Socket
        $test = Invoke-SSH-Safe "echo connected"
        if ($test.Output -match "connected") { Write-Ok "SSH-Verbindung hergestellt (Passwort)"; Add-SSHConfig; return }
    } else {
        $test = & ssh @keyArg -p $PiPort `
            -o ConnectTimeout=10 `
            -o StrictHostKeyChecking=accept-new `
            -o PasswordAuthentication=no `
            "${PiUser}@${PiHost}" "echo connected"
        if ($test -match "connected") { Write-Ok "SSH-Verbindung hergestellt"; Add-SSHConfig; return }
    }
    Write-Host "  Manuell testen: ssh ${PiUser}@${PiHost}" -ForegroundColor Gray
    Write-Fail "SSH-Verbindung fehlgeschlagen."
}

# =============================================================================
#  Banner
# =============================================================================
Write-Host ""
Write-Host "  ╔══════════════════════════════════════════╗" -ForegroundColor DarkCyan
Write-Host "  ║   Fundus Marketplace – Deploy Script     ║" -ForegroundColor DarkCyan
Write-Host "  ║   R001  •  Fundus-Chain                  ║" -ForegroundColor DarkCyan
Write-Host "  ╚══════════════════════════════════════════╝" -ForegroundColor DarkCyan
Write-Host ""

# =============================================================================
#  BLOCK A – Eingaben & Vorprüfungen
# =============================================================================
Write-Step "Eingaben prüfen..."

# ZIP-Datei
if (-not (Test-Path $ZipPath)) {
    # Automatisch nach dem Node-ZIP suchen: bevorzugt FND.zip, sonst ein
    # revisionsbehafteter Name (FND_R036.zip, "FND R001.zip" o.ä.) — das
    # Admin-ZIP (FND admin*) wird dabei ausgeschlossen.
    $found = Get-ChildItem -Filter "FND*.zip" -ErrorAction SilentlyContinue |
             Where-Object { $_.Name -notmatch "admin" } |
             Sort-Object { $_.Name -ne "FND.zip" } |
             Select-Object -First 1
    if ($found) {
        $ZipPath = $found.FullName
        Write-Info "ZIP gefunden: $ZipPath"
    } else {
        Write-Fail "ZIP nicht gefunden: $ZipPath`n  Bitte -ZipPath angeben oder 'FND.zip' ins gleiche Verzeichnis legen."
    }
}
$zipAbsolute = (Resolve-Path $ZipPath).Path
$zipSize     = [math]::Round((Get-Item $zipAbsolute).Length / 1MB, 2)
Write-Ok "ZIP: $(Split-Path $zipAbsolute -Leaf) ($zipSize MB)"

# Admin-ZIP prüfen (optional)
$deployAdmin = $false
if (Test-Path $AdminZip) {
    $adminAbs   = (Resolve-Path $AdminZip).Path
    $adminSize  = [math]::Round((Get-Item $adminAbs).Length / 1MB, 2)
    Write-Ok "Admin-ZIP: $(Split-Path $adminAbs -Leaf) ($adminSize MB)"
    $ans = Read-Host "  Admin-Paket auch installieren? [j/N]"
    $deployAdmin = ($ans -eq "j" -or $ans -eq "J")
}

# Pi-Username abfragen wenn nicht als Parameter angegeben
if ([string]::IsNullOrWhiteSpace($PiUser)) {
    $PiUser = Read-Host "  Pi SSH-Benutzername"
    if ([string]::IsNullOrWhiteSpace($PiUser)) { Write-Fail "Kein Benutzername angegeben." }
}

# Pi-IP abfragen wenn nicht als Parameter angegeben
if ([string]::IsNullOrWhiteSpace($PiHost)) {
    $PiHost = Read-Host "  Pi IP-Adresse"
    if ([string]::IsNullOrWhiteSpace($PiHost)) { Write-Fail "Keine IP angegeben." }
}
Write-Ok "Ziel: ${PiUser}@${PiHost}:${PiPort}"

# SSH-Tools prüfen
foreach ($tool in @("ssh","scp")) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        Write-Fail "'$tool' nicht gefunden. OpenSSH installieren: winget install Microsoft.OpenSSH.Beta"
    }
}
Write-Ok "SSH + SCP verfügbar"

# Bestätigung
Write-Host ""
Write-Host "  Plan: $zipAbsolute → ${PiUser}@${PiHost}:${RemoteDir}" -ForegroundColor Gray
if ($deployAdmin) { Write-Host "        + Admin-Paket" -ForegroundColor Gray }

# =============================================================================
#  BLOCK B – SSH-Verbindungstest
# =============================================================================

# =============================================================================
#  SSH-Authentifizierung (automatisch)
#  - Genau ein Key vorhanden  -> ohne Rueckfrage nutzen
#  - Mehrere Keys vorhanden    -> Auswahl
#  - Kein Key vorhanden        -> Passwort-Login (interaktiv durch ssh)
#  -PiKey <Pfad> erzwingt einen bestimmten Key.
# =============================================================================
$sshDir = "$env:USERPROFILE\.ssh"

$existingKeys = @()
if (Test-Path $sshDir) {
    $existingKeys = @(Get-ChildItem $sshDir -File |
        Where-Object {
            $_.Name -notin @("known_hosts","known_hosts.old","config","authorized_keys") -and
            $_.Extension -ne ".pub" -and
            (Test-Path "$($_.FullName).pub")
        } |
        Select-Object -ExpandProperty FullName)
}

$keyPath = $null

if ($PiKey) {
    # Explizit angegebener Key hat Vorrang
    if (-not (Test-Path $PiKey)) { Write-Fail "Angegebener Key nicht gefunden: $PiKey" }
    $keyPath = $PiKey
    Write-Ok "Verwende angegebenen Key: $(Split-Path $keyPath -Leaf)"
} elseif ($existingKeys.Count -eq 1) {
    # Genau ein Key -> ohne Rueckfrage nutzen
    $keyPath = $existingKeys[0]
    Write-Ok "SSH-Key gefunden: $(Split-Path $keyPath -Leaf) (wird automatisch verwendet)"
} elseif ($existingKeys.Count -gt 1) {
    # Mehrere Keys -> Auswahl
    Write-Host ""
    Write-Host "  Mehrere SSH-Keys gefunden:" -ForegroundColor Cyan
    for ($i = 0; $i -lt $existingKeys.Count; $i++) {
        $fp = (ssh-keygen -lf "$($existingKeys[$i]).pub" 2>$null)
        Write-Host "    [$($i+1)] $(Split-Path $existingKeys[$i] -Leaf)" -ForegroundColor White
        if ($fp) { Write-Host "         $fp" -ForegroundColor DarkGray }
    }
    Write-Host "    [P] Stattdessen Passwort-Login" -ForegroundColor Gray
    Write-Host ""
    $sel = Read-Host "  Key auswaehlen [1-$($existingKeys.Count) oder P]"
    if ($sel -match "^[Pp]$") {
        $keyPath = $null
        Write-Info "Passwort-Login gewaehlt"
    } elseif ($sel -match "^\d+$" -and [int]$sel -ge 1 -and [int]$sel -le $existingKeys.Count) {
        $keyPath = $existingKeys[[int]$sel - 1]
        Write-Ok "Verwende Key: $(Split-Path $keyPath -Leaf)"
    } else {
        Write-Fail "Ungueltige Auswahl: $sel"
    }
} else {
    # Kein Key -> Passwort-Login. ssh fragt das Passwort selbst interaktiv ab.
    Write-Info "Kein SSH-Key vorhanden - Passwort-Login wird verwendet"
    Write-Host "  (ssh fragt das Passwort fuer ${PiUser}@${PiHost} beim Verbinden ab)" -ForegroundColor DarkGray
    $keyPath = $null
}

# Falls ein Key gewaehlt wurde: pruefen ob er passwortgeschuetzt ist und
# dann einmalig in den ssh-agent laden (sonst fragt ssh bei jedem Aufruf).
if ($keyPath) {
    $ppTest = & ssh-keygen -y -f $keyPath -P "" 2>&1
    $hasPP = ($LASTEXITCODE -ne 0)
    if ($hasPP) {
        Write-Host ""
        Write-Host "  Key ist passwortgeschuetzt." -ForegroundColor Cyan
        Write-Host "  SSH-Agent wird gestartet (einmalige Admin-Abfrage moeglich)." -ForegroundColor DarkGray
        $svc = Get-Service ssh-agent -ErrorAction SilentlyContinue
        $agentOk = $false
        if ($svc -and $svc.Status -eq "Running") {
            $agentOk = $true
        } elseif ($svc) {
            try {
                Set-Service ssh-agent -StartupType Manual -ErrorAction Stop
                Start-Service ssh-agent -ErrorAction Stop
                $agentOk = $true
            } catch {
                # Keine Admin-Rechte: Agent-Aktivierung still ueberspringen.
                # Das Deploy nutzt ohnehin "-i keyPath" pro SSH-Aufruf; ssh
                # fragt die Passphrase dann selbst ab (oder nimmt -CertPass).
                Write-Info "SSH-Agent nicht verfuegbar (keine Admin-Rechte) - nutze Key direkt mit -i"
                $agentOk = $false
            }
        }
        # Key in den Agent laden – nur wenn der Agent verfuegbar ist.
        if ($agentOk) {
            # Schon im Agent geladen?
            $already = & ssh-add -l 2>$null | Select-String ([regex]::Escape((Split-Path $keyPath -Leaf)))
            if (-not $already) {
                if ($CertPass -ne "") {
                    # Passphrase nicht-interaktiv via SSH_ASKPASS übergeben
                    $askScript = Join-Path $env:TEMP "fnd_askpass.cmd"
                    Set-Content -Path $askScript -Value "@echo $CertPass" -Encoding Ascii
                    $env:SSH_ASKPASS = $askScript
                    $env:SSH_ASKPASS_REQUIRE = "force"
                    $env:DISPLAY = "localhost:0"   # erzwingt ASKPASS-Nutzung
                    echo "" | & ssh-add $keyPath 2>$null
                    Remove-Item $askScript -ErrorAction SilentlyContinue
                    Remove-Item Env:\SSH_ASKPASS -ErrorAction SilentlyContinue
                    Remove-Item Env:\SSH_ASKPASS_REQUIRE -ErrorAction SilentlyContinue
                    Write-Info "Key-Passphrase aus Parameter übernommen"
                } else {
                    Write-Host "  Passphrase fuer $(Split-Path $keyPath -Leaf):" -ForegroundColor Cyan
                    & ssh-add $keyPath
                }
            }
            Write-Ok "Key im Agent geladen - kein weiterer Prompt"
            $keyPath = $null   # Agent uebernimmt, kein -i noetig
        } else {
            # Kein Agent: Key bleibt als -i in jedem SSH-Aufruf. Bei -CertPass
            # via ASKPASS, sonst fragt ssh die Passphrase interaktiv pro Aufruf.
            if ($CertPass -ne "") {
                $askScript = Join-Path $env:TEMP "fnd_askpass.cmd"
                Set-Content -Path $askScript -Value "@echo $CertPass" -Encoding Ascii
                $env:SSH_ASKPASS = $askScript
                $env:SSH_ASKPASS_REQUIRE = "force"
                $env:DISPLAY = "localhost:0"
                Write-Info "Key-Passphrase via -CertPass (ohne Agent)"
            } else {
                Write-Info "Ohne Agent: ssh fragt die Key-Passphrase ggf. mehrfach ab"
            }
        }
    } else {
        Write-Ok "Key ohne Passphrase - direkt nutzbar"
    }
}

$script:SelectedKeyPath = $keyPath

# Auth-Modus festlegen: Key (inkl. Agent) ODER Passwort-Login
# UsePassword = true nur wenn KEIN Key gewaehlt UND kein Agent-Key aktiv ist.
$agentHasKey = $false
try { $agentHasKey = [bool](& ssh-add -l 2>$null | Where-Object { $_ -match "." }) } catch {}
if (-not $keyPath -and -not $agentHasKey) {
    $script:UsePassword = $true
    $script:CtlPath = "$env:TEMP\fundus-ssh-%h-%p-%r"
    Write-Info "Auth-Modus: Passwort-Login"
} else {
    $script:UsePassword = $false
    Write-Info "Auth-Modus: SSH-Key"
}

Write-Step "SSH-Verbindung herstellen..."
Connect-SSH
$ping = Invoke-SSH-Safe "echo pong"
if ($ping.ExitCode -ne 0 -or $ping.Output.Trim() -ne "pong") {
    Write-Fail "SSH-Verbindung fehlgeschlagen."
}
Write-Ok "SSH-Verbindung OK"

# sudo-Berechtigung prüfen
Write-Step "sudo-Berechtigung prüfen..."
$sudoCheck = Invoke-SSH-Safe "sudo -n true 2>&1 && echo sudo-ok || echo sudo-needs-pw"
if ($sudoCheck.Output -match "sudo-ok") {
    Write-Ok "Passwortfreies sudo verfügbar"
    $script:SudoPass = $null
} else {
    # Sudo-Passwort: aus Parameter übernehmen oder interaktiv abfragen
    if ($SudoPass -ne "") {
        $script:SudoPass = $SudoPass
        Write-Info "sudo-Passwort aus Parameter übernommen"
    } else {
        Write-Host ""
        Write-Host "  sudo-Passwort fuer ${PiUser}:" -ForegroundColor Cyan
        $secPw = Read-Host -AsSecureString
        $script:SudoPass = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
                               [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secPw))
    }
    # Testen
    $test = Invoke-SSH-Safe "echo '$script:SudoPass' | sudo -S -p '' true 2>&1 && echo sudo-ok || echo sudo-fail"
    if ($test.Output -match "sudo-ok") {
        Write-Ok "sudo-Passwort akzeptiert"
    } else {
        Write-Fail "sudo-Passwort falsch oder sudo nicht verfügbar"
    }
}

# =============================================================================
#  BLOCK C – System-Pakete installieren
# =============================================================================
if (-not $SkipSetup) {
    Write-Step "System aktualisieren..."
    Invoke-SSH "sudo apt-get update 2>&1 | tail -3"
    Write-Ok "apt update"

    # ── Basis-Pakete (immer aus Debian-Repo verfuegbar) ──────────────────────
    Write-Step "Basis-Pakete pruefen..."
    $basePkgs = @("unzip","curl","logrotate","ca-certificates","gnupg","exfatprogs","ntfs-3g")
    $toInstall = @()
    foreach ($pkg in $basePkgs) {
        $chk = Invoke-SSH-Safe "dpkg -l $pkg 2>/dev/null | grep -q '^ii' && echo ok || echo missing"
        if ($chk.Output.Trim() -ne "ok") { $toInstall += $pkg }
    }
    if ($toInstall.Count -gt 0) {
        $pkgList = $toInstall -join " "
        Write-Info "Installiere: $pkgList"
        Invoke-SSH "sudo apt-get install -y $pkgList 2>&1 | tail -5"
        $failed = @()
        foreach ($pkg in $toInstall) {
            $v = Invoke-SSH-Safe "dpkg -l $pkg 2>/dev/null | grep -q '^ii' && echo ok || echo missing"
            if ($v.Output.Trim() -ne "ok") { $failed += $pkg }
        }
        if ($failed.Count -gt 0) {
            Write-Fail "Basis-Pakete fehlgeschlagen: $($failed -join ', '). Speicherplatz pruefen (df -h)."
        }
        Write-Ok "Basis-Pakete installiert"
    } else {
        Write-Ok "Basis-Pakete vorhanden"
    }

    # FUSE erlauben, dass root-Mounts (exFAT/NTFS via FUSE) auch fuer andere User
    # (den Node-User) zugaenglich sind. Ohne 'user_allow_other' lehnt FUSE die
    # allow_other-Option ab, und der Node kann die Groesse externer Laufwerke nicht
    # lesen (Regler bleibt bei 0).
    Invoke-SSH "grep -q '^user_allow_other' /etc/fuse.conf 2>/dev/null || echo 'user_allow_other' | sudo tee -a /etc/fuse.conf >/dev/null"
    Write-Ok "FUSE user_allow_other gesetzt"

    # udisks2-Automount so konfigurieren, dass externe Platten mit GRUPPEN-
    # Schreibrecht gemountet werden (dmask/fmask=0002). Dann kann der Node-User
    # (in der passenden Gruppe) auf udisks2-gemountete Platten schreiben, OHNE
    # dass ein Skript nötig ist — echter Automount mit Schreibrecht.
    # dmask/fmask sind (anders als allow_other) erlaubte udisks2-Optionen. Wichtig:
    # in den _defaults setzen (werden dann automatisch angewandt) UND in _allow.
    $udisksConf = @'
[defaults]
exfat_defaults=uid=$UID,gid=$GID,iocharset=utf8,errors=remount-ro,dmask=0002,fmask=0002
exfat_allow=uid=$UID,gid=$GID,umask,dmask,fmask,iocharset,namecase,errors
ntfs_defaults=uid=$UID,gid=$GID,windows_names,dmask=0002,fmask=0002
ntfs_allow=uid=$UID,gid=$GID,umask,dmask,fmask,locale,norecover,ignore_case,windows_names,big_writes
vfat_defaults=uid=$UID,gid=$GID,shortname=mixed,utf8=1,showexec,flush,dmask=0002,fmask=0002
vfat_allow=uid=$UID,gid=$GID,flush,utf8,shortname,umask,dmask,fmask,codepage,iocharset,usefree,showexec
'@
    $udisksB64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($udisksConf))
    Invoke-SSH "sudo mkdir -p /etc/udisks2 && echo '$udisksB64' | base64 -d | sudo tee /etc/udisks2/mount_options.conf >/dev/null && sudo systemctl restart udisks2 2>/dev/null || true"
    Write-Ok "udisks2-Automount mit Gruppen-Schreibrecht konfiguriert (dmask=0002)"

    # ── Webserver mit Lua-Support (OpenResty ODER nginx+lua-Modul) ───────────
    Write-Step "Webserver (Lua-faehig) einrichten..."
    # Ist schon ein Lua-faehiger Webserver da?
    $orInstalled    = (Invoke-SSH-Safe "dpkg -l openresty 2>/dev/null | grep -q '^ii' && echo yes || echo no").Output.Trim()
    $nginxLuaModule = (Invoke-SSH-Safe "dpkg -l libnginx-mod-http-lua 2>/dev/null | grep -q '^ii' && echo yes || echo no").Output.Trim()

    if ($orInstalled -eq "yes") {
        Write-Ok "OpenResty bereits installiert"
        $script:WebServer = "openresty"
    } elseif ($nginxLuaModule -eq "yes") {
        Write-Ok "nginx + Lua-Modul bereits installiert"
        $script:WebServer = "nginx"
    } else {
        # Versuch 1: OpenResty-Repo
        Write-Info "Versuche OpenResty zu installieren..."
        $codename = (Invoke-SSH-Safe "lsb_release -cs 2>/dev/null").Output.Trim()
        if ([string]::IsNullOrWhiteSpace($codename)) { $codename = "bookworm" }
        $repoCode = (Invoke-SSH-Safe "curl -sf -o /dev/null -w '%{http_code}' http://openresty.org/package/raspberrypi/dists/$codename/Release 2>/dev/null || echo 000").Output.Trim()
        if ($repoCode -ne "200") {
            Write-Info "Kein OpenResty-Repo fuer '$codename' - teste 'bookworm'..."
            $bw = (Invoke-SSH-Safe "curl -sf -o /dev/null -w '%{http_code}' http://openresty.org/package/raspberrypi/dists/bookworm/Release 2>/dev/null || echo 000").Output.Trim()
            if ($bw -eq "200") { $codename = "bookworm" } else { $codename = "" }
        }

        $orOk = $false
        if ($codename -ne "") {
            Invoke-SSH "wget -qO - https://openresty.org/package/pubkey.gpg | sudo gpg --dearmor -o /usr/share/keyrings/openresty.gpg 2>/dev/null; echo done"
            Invoke-SSH "echo 'deb [signed-by=/usr/share/keyrings/openresty.gpg] http://openresty.org/package/raspberrypi $codename main' | sudo tee /etc/apt/sources.list.d/openresty.list"
            Invoke-SSH "sudo apt-get update 2>&1 | tail -3"
            Invoke-SSH "sudo apt-get install -y openresty 2>&1 | tail -5"
            $orOk = ((Invoke-SSH-Safe "dpkg -l openresty 2>/dev/null | grep -q '^ii' && echo yes || echo no").Output.Trim() -eq "yes")
        }

        if ($orOk) {
            Write-Ok "OpenResty installiert"
            $script:WebServer = "openresty"
        } else {
            # Versuch 2: Debian nginx + Lua-Modul (immer verfuegbar)
            Write-Info "OpenResty nicht verfuegbar - nutze nginx + libnginx-mod-http-lua..."
            Invoke-SSH "sudo rm -f /etc/apt/sources.list.d/openresty.list; sudo apt-get update 2>&1 | tail -2"
            Invoke-SSH "sudo apt-get install -y nginx libnginx-mod-http-lua lua-cjson 2>&1 | tail -5"
            $ngOk = ((Invoke-SSH-Safe "dpkg -l nginx 2>/dev/null | grep -q '^ii' && echo yes || echo no").Output.Trim() -eq "yes")
            $luaOk = ((Invoke-SSH-Safe "dpkg -l libnginx-mod-http-lua 2>/dev/null | grep -q '^ii' && echo yes || echo no").Output.Trim() -eq "yes")
            if ($ngOk -and $luaOk) {
                Write-Ok "nginx + Lua-Modul installiert"
                $script:WebServer = "nginx"
            } else {
                Write-Fail "Kein Lua-faehiger Webserver installierbar (weder OpenResty noch nginx+lua-modul). apt-Logs pruefen."
            }
        }
    }
}

# =============================================================================
#  BLOCK D – Go installieren (falls nicht vorhanden)
# =============================================================================
Write-Step "Go-Installation prüfen..."
$goCheck = Invoke-SSH-Safe "export PATH=`$PATH:/usr/local/go/bin && go version 2>/dev/null || echo missing"
if ($goCheck.Output.Trim() -eq "missing" -or $goCheck.Output -notmatch "go version") {
    Write-Info "Go nicht gefunden – installiere go1.22..."
    # Go-Download passend zur Ziel-Architektur (arm64 für Pis, amd64 für x86-Nodes).
    $goArchDl = if ($Arch -eq "amd64") { "amd64" } elseif ($Arch -eq "arm") { "armv6l" } else { "arm64" }
    $_c2 = @"
wget -q https://go.dev/dl/go1.22.4.linux-$goArchDl.tar.gz -O /tmp/go.tar.gz && sudo tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz
"@
    Invoke-SSH $_c2
    Invoke-SSH "echo 'export PATH=`$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh"
    $goVer = (Invoke-SSH-Safe "export PATH=`$PATH:/usr/local/go/bin && go version").Output.Trim()
    Write-Ok "Go installiert: $goVer"
} else {
    Write-Ok "Go: $($goCheck.Output.Trim())"
}

# =============================================================================
#  BLOCK E – Fundus-User anlegen
# =============================================================================
Write-Step "System-User '$FundusUser' prüfen..."
$userCheck = Invoke-SSH-Safe "id $FundusUser 2>/dev/null && echo ok || echo missing"
if ($userCheck.Output.Trim() -ne "ok") {
    $_c3 = @"
sudo useradd --system --create-home --home-dir /home/$FundusUser --shell /usr/sbin/nologin --comment Fundus $FundusUser || true
"@
    Invoke-SSH $_c3
    $_c4 = @"
sudo passwd -l $FundusUser 2>/dev/null || true
"@
    Invoke-SSH $_c4
    Write-Ok "User '$FundusUser' angelegt"
} else {
    Write-Ok "User '$FundusUser' vorhanden"
}

# Der Node-User braucht Zugriff auf udisks2-gemountete externe Platten. udisks2
# mountet unter /media/<desktop-user>/ mit einer ACL, die nur dem Desktop-User UND
# der root-GRUPPE Durchgang erlaubt (other = kein Zugriff). Damit der Node die
# Groesse externer Laufwerke lesen kann (df/statfs), muss fundus in der root-Gruppe
# sein. Das gibt LESE-Zugriff auf root-Gruppen-Dateien — auf einem dedizierten
# Fundus-Node akzeptabel; ohne diesen Zugriff bleibt der Speicher-Regler bei 0.
Invoke-SSH "sudo usermod -aG root $FundusUser"
Write-Ok "'$FundusUser' zur root-Gruppe hinzugefuegt (Zugriff auf externe Laufwerke)"

# =============================================================================
#  BLOCK F – Verzeichnisse anlegen
# =============================================================================
Write-Step "Verzeichnisstruktur..."
foreach ($dir in @($RemoteDir, "$RemoteDir/bin", "$RemoteDir/data", "/etc/fundus")) {
    $chk = Invoke-SSH-Safe "sudo test -d $dir && echo ok || echo missing"
    if ($chk.Output.Trim() -ne "ok") {
        Invoke-SSH "sudo mkdir -p $dir"
        Write-Fixed "Angelegt: $dir"
    }
}
$_c5 = @"
sudo chmod 755 $RemoteDir && sudo chmod 750 $RemoteDir/bin && sudo chmod 750 $RemoteDir/data
"@
Invoke-SSH $_c5
Invoke-SSH "sudo chown -R root:$FundusUser $RemoteDir"
$_c6 = @"
sudo chown root:$FundusUser /etc/fundus && sudo chmod 750 /etc/fundus
"@
Invoke-SSH $_c6
Write-Ok "Verzeichnisse OK"

# =============================================================================
#  BLOCK G – fundus.env schreiben (wenn nicht vorhanden)
# =============================================================================
Write-Step "Konfiguration schreiben..."
# env IMMER neu schreiben (alte Versionen koennten \r oder veraltete Werte haben).
# Bestehende wird vorher gesichert.
$envExists = Invoke-SSH-Safe "sudo test -f /etc/fundus/fundus.env && echo ok || echo missing"
$envPresent = ($envExists.Output.Trim() -eq "ok")
if ($envPresent) {
    Invoke-SSH "sudo cp /etc/fundus/fundus.env /etc/fundus/fundus.env.bak"
    Write-Info "Bestehende fundus.env gesichert (.bak)"
}
if ((-not $envPresent) -or $WriteEnv) {
    if ($envPresent) { Write-Info "fundus.env wird auf Wunsch (-WriteEnv) neu geschrieben..." }
    else { Write-Info "fundus.env (Erstinstallation) schreiben..." }
    # Config-Zeilen einzeln zusammenbauen, dann per printf schreiben.
    # Wichtig: KEIN Here-Doc (CRLF von PowerShell wuerde \r in die Datei schreiben).
    $envLines = @(
        "FUNDUS_NODE_TYPE=consumer",
        "FUNDUS_DATA_DIR=$RemoteDir/data",
        "FUNDUS_P2P_PORT=4001",
        "FUNDUS_API_PORT=3000",
        "FUNDUS_API_BIND=127.0.0.1",
        "FUNDUS_BOOTSTRAP_PEERS=$BootstrapPeer",
        "FUNDUS_TRAFO_RATED_KW=400",
        "FUNDUS_SETTLEMENT_METHOD=ansatz3",
        "FUNDUS_FND_FEE_COLLECTOR=$FeeCollector",
        "FUNDUS_CHAIN_ID=$ChainId",
        "FUNDUS_CHAIN_RPC_URL=$RpcUrl",
        "FUNDUS_FND_ADDRESS=$FndAddress",
        "FUNDUS_SEED_FILE=/etc/fundus/wallet.key",
        "FUNDUS_LOG_LEVEL=info",
        "FUNDUS_BLOOM_FILTER_SIZE=4096",
        "FUNDUS_STORAGE_DIR=$RemoteDir/chunks",
        "# Filesharing: -1=auto 50% des freien Speichers, 0=aus, >0=feste GB",
        "FUNDUS_STORAGE_OFFER_GB=-1",
        "FUNDUS_SWAP_HTLC_PROGRAM=$SwapHtlcProgram",
        "FUNDUS_SHOP_SOLANA_RPC=$SolanaRpc"
    )
    # Mit \n verbinden (Unix-Zeilenenden), via base64 sicher uebertragen
    $envText  = ($envLines -join "`n") + "`n"
    $envBytes = [System.Text.Encoding]::UTF8.GetBytes($envText)
    $envB64   = [Convert]::ToBase64String($envBytes)
    $remoteEnvTmp = "/tmp/fundus_env_deploy"
    Invoke-SSH "echo $envB64 | base64 -d > $remoteEnvTmp && sed -i 's/\r$//' $remoteEnvTmp"
    $_c7 = @"
sudo mv $remoteEnvTmp /etc/fundus/fundus.env && sudo chown $FundusUser`:$FundusUser /etc/fundus/fundus.env && sudo chmod 600 /etc/fundus/fundus.env
"@
    Invoke-SSH $_c7
    Write-Ok "fundus.env angelegt"
} else {
    Write-Ok "fundus.env vorhanden"
}

# Gezielte, idempotente Aktualisierung der Swap-Variablen — OHNE die ganze env
# zu überschreiben. Setzt jede Variable (ersetzt vorhandene Zeile bzw. hängt an),
# nur wenn ein Wert übergeben wurde. So bleiben alle anderen env-Werte erhalten.
function Set-EnvVar([string]$key, [string]$val) {
    if ([string]::IsNullOrEmpty($val)) { return }
    # Zeile ersetzen falls vorhanden, sonst anhängen (sed + Fallback).
    $cmd = @"
if sudo grep -q '^$key=' /etc/fundus/fundus.env; then
  sudo sed -i 's|^$key=.*|$key=$val|' /etc/fundus/fundus.env
else
  echo '$key=$val' | sudo tee -a /etc/fundus/fundus.env >/dev/null
fi
"@
    Invoke-SSH $cmd
    Write-Ok "env gesetzt: $key"
}
Set-EnvVar "FUNDUS_SWAP_HTLC_PROGRAM" $SwapHtlcProgram
Set-EnvVar "FUNDUS_SHOP_SOLANA_RPC" $SolanaRpc

# =============================================================================
#  BLOCK H – ZIP hochladen & entpacken
# =============================================================================
Write-Step "ZIP hochladen..."
$remoteZip = "/tmp/fundus_deploy.zip"
# SCP: Key zuerst, dann Passwort-Fallback
$scpDone = $false
if ($script:SelectedKeyPath -and (Test-Path $script:SelectedKeyPath)) {
    scp -i $script:SelectedKeyPath -P $PiPort -q -o StrictHostKeyChecking=accept-new -o BatchMode=yes "$zipAbsolute" "${PiUser}@${PiHost}:$remoteZip" 2>&1 | Out-Null
    $scpDone = ($LASTEXITCODE -eq 0)
    if (-not $scpDone) { Write-Info "Key-Login fehlgeschlagen - versuche Passwort..." }
}
if (-not $scpDone) {
    scp -P $PiPort -q -o StrictHostKeyChecking=accept-new "$zipAbsolute" "${PiUser}@${PiHost}:$remoteZip"
    if ($LASTEXITCODE -ne 0) { Write-Fail "SCP fehlgeschlagen" }
}
Write-Ok "Upload OK"

Write-Step "Entpacken..."
Invoke-SSH "sudo unzip -o $remoteZip -d $RemoteDir 2>&1 | tail -3"

# libsodium.js (sumo, browsers-sumo) fuer die clientseitige Verschluesselung.
# Der PC laedt die Datei und schiebt sie per scp auf den Pi. Das umgeht die
# sudo-Umschreibung + Quoting-Probleme von Invoke-SSHRaw bei komplexen
# Remote-Shell-Befehlen (frueherer Bug: Befehl brach an inneren Quotes).
# browsers-sumo liegt NICHT im npm-Tarball, nur im Git-Repo -> GitHub-raw
# primaer, jsDelivr GitHub-Mirror (/gh/) als Fallback.
$sodiumPath  = "$RemoteDir/lua/static/sodium.js"
$sodiumLocal = Join-Path (Split-Path $MyInvocation.MyCommand.Path -Parent) "sodium.js"
$sodiumUrl   = "https://raw.githubusercontent.com/jedisct1/libsodium.js/master/dist/browsers-sumo/sodium.js"
$sodiumUrl2  = "https://cdn.jsdelivr.net/gh/jedisct1/libsodium.js@master/dist/browsers-sumo/sodium.js"

# Status robust pruefen: gibt IMMER "yes" oder "no" zurueck (nie leer).
# Frueherer Bug: "test -s X && wc -c" gab bei fehlender Datei GAR NICHTS aus,
# .Output war leer statt "no".
$sizeChk = "if [ -s '$sodiumPath' ] && [ `$(wc -c < '$sodiumPath') -gt 100000 ]; then echo yes; else echo no; fi"
$sodiumOnPi = (Invoke-SSH-Safe $sizeChk).Output.Trim()

if ($sodiumOnPi -ne "yes") {
    # (1) Lokal neben dem Skript schon vorhanden? Sonst auf dem PC laden.
    $haveLocal = (Test-Path $sodiumLocal) -and ((Get-Item $sodiumLocal).Length -gt 100000)
    if (-not $haveLocal) {
        Write-Info "Lade libsodium.js (browsers-sumo) auf dem PC herunter..."
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        foreach ($u in @($sodiumUrl, $sodiumUrl2)) {
            try {
                Remove-Item $sodiumLocal -Force -ErrorAction SilentlyContinue
                Invoke-WebRequest -Uri $u -OutFile $sodiumLocal -UseBasicParsing -TimeoutSec 60
                if ((Test-Path $sodiumLocal) -and ((Get-Item $sodiumLocal).Length -gt 100000)) {
                    Write-Info "  geladen von: $u"
                    break
                }
            } catch { Remove-Item $sodiumLocal -Force -ErrorAction SilentlyContinue }
        }
        $haveLocal = (Test-Path $sodiumLocal) -and ((Get-Item $sodiumLocal).Length -gt 100000)
    }
    if ($haveLocal) {
        # (2) Verzeichnis anlegen, per scp nach /tmp, dann an den Zielort.
        Invoke-SSH "sudo mkdir -p $RemoteDir/lua/static"
        $scpKey = if ($script:SelectedKeyPath -and (Test-Path $script:SelectedKeyPath)) { @("-i", $script:SelectedKeyPath) } else { @() }
        & scp @scpKey -P $PiPort -q -o StrictHostKeyChecking=accept-new "$sodiumLocal" "${PiUser}@${PiHost}:/tmp/sodium.js" 2>&1 | Out-Null
        Invoke-SSH "sudo mv /tmp/sodium.js $sodiumPath"
    } else {
        # (3) PC-Download ging nicht -> Pi laedt selbst. EINFACHE Einzeiler je URL
        #     (kein verschachteltes Quoting, das Invoke-SSHRaw zerlegen koennte).
        Write-Info "PC-Download nicht moeglich - versuche direkt auf dem Pi..."
        Invoke-SSH "sudo mkdir -p $RemoteDir/lua/static"
        Invoke-SSH "rm -f /tmp/sodium.js; curl -fsSL --retry 2 -o /tmp/sodium.js $sodiumUrl"
        $g1 = (Invoke-SSH-Safe "if [ -s /tmp/sodium.js ] && [ `$(wc -c < /tmp/sodium.js) -gt 100000 ]; then echo yes; else echo no; fi").Output.Trim()
        if ($g1 -ne "yes") {
            Invoke-SSH "rm -f /tmp/sodium.js; curl -fsSL --retry 2 -o /tmp/sodium.js $sodiumUrl2"
        }
        Invoke-SSH "sudo mv /tmp/sodium.js $sodiumPath"
    }
    # Erfolgskontrolle (wieder robust, nie leer)
    $sodiumOk2 = (Invoke-SSH-Safe $sizeChk).Output.Trim()
    if ($sodiumOk2 -eq "yes") {
        Write-Ok "libsodium.js bereit"
    } else {
        Write-Warn "libsodium.js konnte nicht bereitgestellt werden - Browser-Verschluesselung deaktiviert"
    }
} else {
    Write-Info "libsodium.js bereits vorhanden"
}

$_c8 = @"
sudo chown -R root:$FundusUser $RemoteDir && sudo chmod -R u=rwX,g=rX,o= $RemoteDir
"@
Invoke-SSH $_c8
# SOFORT nach dem globalen chmod o= die Web-Leserechte fuer lua/static wieder-
# herstellen. Der Webserver-User (www-data/nobody) braucht o+rX, sonst 403 auf
# CSS/JS. Frueher lief das erst ~100 Zeilen spaeter (Block J0) - fror der Deploy
# vorher ein, blieb die Seite ungestylt. Daher hier direkt und robust.
Invoke-SSH "sudo chmod o+rX $RemoteDir && sudo chmod -R o+rX $RemoteDir/lua 2>/dev/null || true"

# Selbst-gehostete Fonts einmalig herunterladen (kein externer Request zur
# Laufzeit → DSGVO-konform). Nur laden, wenn noch nicht vorhanden (idempotent).
# Selbst-gehostete Fonts einmalig herunterladen (kein externer Request zur
# Laufzeit → DSGVO-konform). Jeder Download ist ein EINFACHER Einzeiler ohne
# Bash-Funktion/verschachtelte Quotes — sonst kollidiert er mit dem sh -c-
# Wrapper von Invoke-SSH (führte zu Exit 2). curl -f + || true = idempotent
# und bricht den Deploy nie ab.
$fontDir = "$RemoteDir/lua/static/fonts"
Invoke-SSH "mkdir -p $fontDir"
$fonts = @{
    "inter-regular.woff2"       = "https://fonts.gstatic.com/s/inter/v13/UcCO3FwrK3iLTeHuS_fvQtMwCp50KnMa1ZL7.woff2"
    "inter-bold.woff2"          = "https://fonts.gstatic.com/s/inter/v13/UcCO3FwrK3iLTeHuS_fvQtMwCp50KnMa25L7.woff2"
    "roboto-regular.woff2"      = "https://fonts.gstatic.com/s/roboto/v30/KFOmCnqEu92Fr1Mu4mxKKTU1Kg.woff2"
    "lora-regular.woff2"        = "https://fonts.gstatic.com/s/lora/v32/0QI6MX1D_JOuGQbT0gvTJPa787weuxJBkqt_.woff2"
    "merriweather-regular.woff2"= "https://fonts.gstatic.com/s/merriweather/v30/u-440qyriQwlOrhSvowK_l5-fCZM.woff2"
    "roboto-mono-regular.woff2" = "https://fonts.gstatic.com/s/robotomono/v23/L0xuDF4xlVMF-BfR8bXMIhJHg45mwgGEFl0_3vq_ROW4.woff2"
}
foreach ($f in $fonts.Keys) {
    # Nur laden, wenn noch nicht da (idempotent). Ein einzelner, einfacher Befehl.
    Invoke-SSH "test -f $fontDir/$f || curl -fsSL '$($fonts[$f])' -o $fontDir/$f || true"
}
Invoke-SSH "chmod -R o+rX $fontDir || true"
Write-Ok "Selbst-gehostete Fonts geprueft/geladen"

# data-Verzeichnis MUSS fundus gehoeren (LevelDB schreibt dort) - nach dem
# globalen chown nochmal explizit setzen. chunks/ ebenso (Filesharing-Daten).
Invoke-SSH "sudo mkdir -p $RemoteDir/data $RemoteDir/chunks"
Invoke-SSH "sudo chown -R ${FundusUser}:${FundusUser} $RemoteDir/data $RemoteDir/chunks && sudo chmod 750 $RemoteDir/data $RemoteDir/chunks"
$_c9 = @"
sudo chmod +x $RemoteDir/bin/fundus-node 2>/dev/null || true
"@
Invoke-SSH $_c9
Invoke-SSH "rm -f $remoteZip"
Write-Ok "Entpackt"

# Admin-ZIP
if ($deployAdmin -and (Test-Path $AdminZip)) {
    Write-Step "Admin-ZIP hochladen..."
    $remoteAdminZip = "/tmp/fundus_admin_deploy.zip"
    $scpDone2 = $false
    if ($script:SelectedKeyPath -and (Test-Path $script:SelectedKeyPath)) {
        scp -i $script:SelectedKeyPath -P $PiPort -q -o StrictHostKeyChecking=accept-new -o BatchMode=yes "$adminAbs" "${PiUser}@${PiHost}:$remoteAdminZip" 2>&1 | Out-Null
        $scpDone2 = ($LASTEXITCODE -eq 0)
        if (-not $scpDone2) { Write-Info "Key-Login fehlgeschlagen - versuche Passwort..." }
    }
    if (-not $scpDone2) {
        scp -P $PiPort -q -o StrictHostKeyChecking=accept-new "$adminAbs" "${PiUser}@${PiHost}:$remoteAdminZip"
    }
    Invoke-SSH "sudo unzip -o $remoteAdminZip -d $RemoteDir 2>&1 | tail -3"
    $_c10 = @"
sudo chown -R root:$FundusUser $RemoteDir && sudo chmod +x $RemoteDir/bin/fnd-wallet $RemoteDir/bin/fundus-admin 2>/dev/null || true
"@
    Invoke-SSH $_c10
    Invoke-SSH "rm -f $remoteAdminZip"
    Write-Ok "Admin-Paket entpackt"
}

# =============================================================================
#  BLOCK I – Binary bereitstellen (lokal vorkompiliert ODER auf Pi bauen)
# =============================================================================
$localBinary = ".\bin\fundus-node"

# -Rebuild erzwingt Neukompilierung (noetig nach Go-Quellcode-Aenderungen,
# da sonst nur der Binary-Hash verglichen wird, nicht der Quellstand).
if ($Rebuild -and (Test-Path $localBinary)) {
    Remove-Item $localBinary -Force
    Write-Info "-Rebuild: altes Binary geloescht, wird neu kompiliert"
}

# Hilfsfunktion: Binary hochladen + installieren
function Send-Binary([string]$localPath) {
    $binSize = [math]::Round((Get-Item $localPath).Length / 1MB, 1)
    Write-Info "bin\fundus-node ($binSize MB)"
    $remoteBin = "/tmp/fundus-node-upload"
    if ($script:SelectedKeyPath -and (Test-Path $script:SelectedKeyPath)) {
        scp -i $script:SelectedKeyPath -P $PiPort -q -o StrictHostKeyChecking=accept-new "$localPath" "${PiUser}@${PiHost}:$remoteBin"
    } else {
        scp -P $PiPort -q -o StrictHostKeyChecking=accept-new "$localPath" "${PiUser}@${PiHost}:$remoteBin"
    }
    if ($LASTEXITCODE -ne 0) { Write-Fail "Binary-Upload fehlgeschlagen" }
    Invoke-SSH "sudo mv $remoteBin $RemoteDir/bin/fundus-node && sudo chmod +x $RemoteDir/bin/fundus-node && sudo chown root:$FundusUser $RemoteDir/bin/fundus-node"

    # Helper mit hochladen (eigene Funktion, damit sie auch unabhängig vom Node
    # aufgerufen werden kann, wenn sich NUR der Helper geändert hat).
    Send-HelperBinary
}

# Send-HelperBinary lädt die fundus-helper-Binary hoch, installiert sie und
# VERIFIZIERT, dass der aktuelle Code wirklich am Zielort ankam.
function Send-HelperBinary() {
    $localHelper = Join-Path $PSScriptRoot "bin\fundus-helper"
    if (-not (Test-Path $localHelper)) { return }
    $remoteHelper = "/tmp/fundus-helper-upload"
    if ($script:SelectedKeyPath -and (Test-Path $script:SelectedKeyPath)) {
        scp -i $script:SelectedKeyPath -P $PiPort -q -o StrictHostKeyChecking=accept-new "$localHelper" "${PiUser}@${PiHost}:$remoteHelper"
    } else {
        scp -P $PiPort -q -o StrictHostKeyChecking=accept-new "$localHelper" "${PiUser}@${PiHost}:$remoteHelper"
    }
    if ($LASTEXITCODE -ne 0) {
        Write-Fail "fundus-helper Upload fehlgeschlagen — Deploy abgebrochen (sonst laeuft eine veraltete Binary)."
    }
    # Einzeln statt verkettet (&&), weil die sudo-Umformung verkettete Befehle
    # mit Quotes zerbrechen kann.
    Invoke-SSH "sudo mv $remoteHelper $RemoteDir/bin/fundus-helper"
    Invoke-SSH "sudo chmod 750 $RemoteDir/bin/fundus-helper"
    Invoke-SSH "sudo chown root:root $RemoteDir/bin/fundus-helper"
    Invoke-SSH "sudo systemctl restart fundus-helper 2>/dev/null || true"
    # Verifizieren, dass die Binary WIRKLICH am Ziel liegt und aktuell ist.
    $instCheck = Invoke-SSH-Safe "sudo strings $RemoteDir/bin/fundus-helper 2>/dev/null | grep -c 'System-/Boot-Medium' || echo 0"
    $instOk = "$($instCheck.Output)".Trim()
    if ($instOk -match "1") {
        Write-Ok "fundus-helper Binary installiert und verifiziert (aktueller Code)"
    } else {
        Write-Fail "fundus-helper: Binary kam NICHT korrekt am Ziel an (strings-count=$instOk). Deploy abgebrochen."
    }
}

# Priorisierung (lokales bin\ ist die Quelle der Wahrheit):
#  1. Lokales bin\fundus-node fehlt -> immer neu kompilieren.
#  2. Danach Upload nur wenn sich der SHA-256 vom Pi-Binary unterscheidet.
$piHasBinary = ((Invoke-SSH-Safe "sudo test -f $RemoteDir/bin/fundus-node && echo ok || echo no").Output.Trim() -eq "ok")

# Lokales Binary ist die Quelle der Wahrheit. Fehlt es, wird IMMER gebaut
# (auch wenn auf dem Pi schon eins liegt) - so deployt man nie versehentlich
# einen veralteten Stand.
if (-not (Test-Path $localBinary)) {
    Write-Info "Kein lokales Binary in bin\ - kompiliere..."
    Invoke-LocalBuild $Arch
    if (-not (Test-Path $localBinary)) { Write-Fail "Build lieferte kein Binary in bin\" }
}

# Ab hier existiert bin\fundus-node garantiert. Upload nur bei Hash-Unterschied.
$localHash = (Get-FileHash $localBinary -Algorithm SHA256).Hash.ToLower()
$piHash = ""
if ($piHasBinary) {
    $piHash = (Invoke-SSH-Safe "sudo sha256sum $RemoteDir/bin/fundus-node 2>/dev/null | cut -d' ' -f1").Output.Trim().ToLower()
}
if ($localHash -eq $piHash) {
    Write-Ok "Binary auf Pi ist aktuell (identisch mit bin\fundus-node)"
    # WICHTIG: Der Helper hat einen EIGENEN Hash-Vergleich. Sonst würde er nie
    # hochgeladen, wenn sich NUR der Helper-Code ändert (Node-Hash bleibt gleich)
    # — genau der Bug, der alle Mount-Fixes nie ankommen ließ.
    $localHelper = Join-Path $PSScriptRoot "bin\fundus-helper"
    if (Test-Path $localHelper) {
        $localHelperHash = (Get-FileHash $localHelper -Algorithm SHA256).Hash.ToLower()
        $piHelperHash = (Invoke-SSH-Safe "sudo sha256sum $RemoteDir/bin/fundus-helper 2>/dev/null | cut -d' ' -f1").Output.Trim().ToLower()
        if ($localHelperHash -eq $piHelperHash) {
            Write-Ok "fundus-helper auf Pi ist aktuell"
        } else {
            Write-Step "fundus-helper geaendert - lade hoch..."
            Send-HelperBinary
        }
    }
} else {
    Write-Step "Binary neu/geaendert - lade hoch..."
    Send-Binary $localBinary
    Write-Ok "Binary aktualisiert"
}

# =============================================================================
#  BLOCK J0 – Webserver (Web-UI auf Port 80) konfigurieren
# =============================================================================
# Webserver-User (www-data bei nginx, nobody bei OpenResty) braucht Lesezugriff
# auf /opt/fundus/lua. Der globale chmod o= nach dem Entpacken sperrt sonst aus -> 403.
Invoke-SSH "sudo chmod o+rX /opt/fundus && sudo chmod -R o+rX /opt/fundus/lua"

# Falls Setup uebersprungen wurde: Webserver-Typ erkennen
if (-not $script:WebServer) {
    $or = (Invoke-SSH-Safe "dpkg -l openresty 2>/dev/null | grep -q '^ii' && echo yes || echo no").Output.Trim()
    if ($or -eq "yes") { $script:WebServer = "openresty" } else { $script:WebServer = "nginx" }
}
Write-Step "Web-UI ($($script:WebServer)) konfigurieren..."

# Log-Verzeichnis fuer nginx-Logs
Invoke-SSH "sudo mkdir -p /var/log/fundus && sudo chown ${FundusUser}:${FundusUser} /var/log/fundus"

# Self-signed TLS-Zertifikat erzeugen (einmalig). Loest das Problem dass mobile
# Browser (HTTPS-First) automatisch auf Port 443 springen. Mit Cert antwortet
# nginx dort sauber; der Browser zeigt EINMAL eine Warnung, danach gemerkt.
$certDir = "/etc/fundus/tls"
$certExists = (Invoke-SSH-Safe "sudo test -f $certDir/fundus.crt && echo yes || echo no").Output.Trim()
if ($certExists -ne "yes") {
    Write-Info "Erzeuge self-signed TLS-Zertifikat..."
    # SAN: wenn PiHost eine IP ist → IP-SAN, sonst DNS-SAN
    if ($PiHost -match '^\d{1,3}(\.\d{1,3}){3}$') {
        $san = "IP:$PiHost,DNS:fundus.local"
    } else {
        $san = "DNS:$PiHost,DNS:fundus.local"
    }
    Invoke-SSH-Safe "sudo mkdir -p $certDir"
    # Versuch mit SAN (-addext, ab openssl 1.1.1). timeout 60s verhindert ein
    # ewiges Hängen bei wenig Entropie auf dem Pi; -rand /dev/urandom als
    # nicht-blockierende Entropiequelle.
    $genWithSan = (Invoke-SSH-Safe "sudo timeout 60 openssl req -x509 -nodes -days 3650 -newkey rsa:2048 -rand /dev/urandom -keyout $certDir/fundus.key -out $certDir/fundus.crt -subj '/CN=fundus.local' -addext 'subjectAltName=$san' >/dev/null 2>&1 && echo ok || echo fail").Output.Trim()
    if ($genWithSan -ne "ok") {
        # Fallback ohne SAN (aeltere openssl)
        Invoke-SSH-Safe "sudo timeout 60 openssl req -x509 -nodes -days 3650 -newkey rsa:2048 -rand /dev/urandom -keyout $certDir/fundus.key -out $certDir/fundus.crt -subj '/CN=fundus.local' >/dev/null 2>&1"
    }
    Invoke-SSH-Safe "sudo chmod 600 $certDir/fundus.key 2>/dev/null"
    # Verifizieren dass das Cert wirklich da ist (sonst crasht nginx 443-Block)
    $certOk = (Invoke-SSH-Safe "sudo test -f $certDir/fundus.crt -a -f $certDir/fundus.key && echo yes || echo no").Output.Trim()
    if ($certOk -eq "yes") {
        Write-Ok "TLS-Zertifikat erzeugt ($certDir)"
    } else {
        Write-Warn "TLS-Zertifikat konnte nicht erzeugt werden - HTTPS (443) deaktiviert"
    }
} else {
    Write-Info "TLS-Zertifikat bereits vorhanden"
}
# certOk immer setzen (auch wenn Cert schon existierte) für die SSL-Config-Logik
$certOk = (Invoke-SSH-Safe "sudo test -f $certDir/fundus.crt -a -f $certDir/fundus.key && echo yes || echo no").Output.Trim()

if ($script:WebServer -eq "openresty") {
    # ── OpenResty ────────────────────────────────────────────────────────────
    $orBin = "/usr/local/openresty/bin/openresty"
    $confDir = "/usr/local/openresty/nginx/conf/conf.d"
    Invoke-SSH "sudo mkdir -p $confDir"
    # Sicherstellen dass nginx.conf das conf.d einbindet
    $mainConf = "/usr/local/openresty/nginx/conf/nginx.conf"
    $hasInc = (Invoke-SSH-Safe "sudo grep -q 'conf.d/\*.conf' $mainConf && echo yes || echo no").Output.Trim()
    if ($hasInc -eq "no") {
        Invoke-SSH "sudo sed -i '/http {/a\    include conf.d/*.conf;' $mainConf"
    }
    # http-Kontext-Direktiven UND server-Block (beide nach conf.d, werden im http{} included)
    Invoke-SSH "sudo cp $RemoteDir/lua/fundus-http.conf $confDir/00-fundus-http.conf && sudo sed -i 's/\r\$//' $confDir/00-fundus-http.conf"
    Invoke-SSH "sudo cp $RemoteDir/lua/fundus-nginx.conf $confDir/fundus.conf && sudo sed -i 's/\r\$//' $confDir/fundus.conf"
    # HTTPS-Config nur wenn Cert existiert (sonst crasht der 443-ssl-Block)
    if ($certOk -eq "yes") {
        Invoke-SSH "sudo cp $RemoteDir/lua/fundus-nginx-ssl.conf $confDir/fundus-ssl.conf && sudo sed -i 's/\r\$//' $confDir/fundus-ssl.conf"
        Write-Info "HTTPS (443) aktiviert"
    } else {
        Invoke-SSH-Safe "sudo rm -f $confDir/fundus-ssl.conf"
    }
    $test = Invoke-SSH-Safe "sudo $orBin -t 2>&1"
    if ($test.Output -notmatch "successful") { Write-Warn "Config-Test: $($test.Output)" }
    Invoke-SSH "sudo systemctl enable openresty 2>/dev/null || true"
    Invoke-SSH-Safe "sudo timeout 60 systemctl restart openresty 2>&1"
    $svc = "openresty"
} else {
    # ── Debian nginx + Lua-Modul ──────────────────────────────────────────────
    # Debian aktiviert das Lua-Modul automatisch via /etc/nginx/modules-enabled/.
    # Nur falls NICHT vorhanden, manuell in nginx.conf laden (sonst Doppel-Lade-Fehler).
    $modAuto = (Invoke-SSH-Safe "ls /etc/nginx/modules-enabled/ 2>/dev/null | grep -q lua && echo yes || echo no").Output.Trim()
    $modInConf = (Invoke-SSH-Safe "sudo grep -q 'ngx_http_lua_module' /etc/nginx/nginx.conf 2>/dev/null && echo yes || echo no").Output.Trim()
    if ($modAuto -eq "no" -and $modInConf -eq "no") {
        Invoke-SSH "sudo sed -i '1i load_module modules/ngx_http_lua_module.so;' /etc/nginx/nginx.conf"
        Write-Info "Lua-Modul manuell in nginx.conf geladen"
    } else {
        Write-Info "Lua-Modul bereits aktiv (modules-enabled)"
    }
    # http-Kontext-Direktiven nach conf.d (wird im http{} included)
    Invoke-SSH "sudo cp $RemoteDir/lua/fundus-http.conf /etc/nginx/conf.d/fundus-http.conf && sudo sed -i 's/\r\$//' /etc/nginx/conf.d/fundus-http.conf"
    # server-Block nach sites-available + aktivieren
    Invoke-SSH "sudo cp $RemoteDir/lua/fundus-nginx.conf /etc/nginx/sites-available/fundus.conf && sudo sed -i 's/\r\$//' /etc/nginx/sites-available/fundus.conf"
    # HTTPS-Config nur wenn Cert existiert
    if ($certOk -eq "yes") {
        Invoke-SSH "sudo cp $RemoteDir/lua/fundus-nginx-ssl.conf /etc/nginx/sites-available/fundus-ssl.conf && sudo sed -i 's/\r\$//' /etc/nginx/sites-available/fundus-ssl.conf"
        Invoke-SSH "sudo ln -sf /etc/nginx/sites-available/fundus-ssl.conf /etc/nginx/sites-enabled/fundus-ssl.conf"
        Write-Info "HTTPS (443) aktiviert"
    } else {
        Invoke-SSH-Safe "sudo rm -f /etc/nginx/sites-enabled/fundus-ssl.conf"
    }
    Invoke-SSH "sudo ln -sf /etc/nginx/sites-available/fundus.conf /etc/nginx/sites-enabled/fundus.conf"
    Invoke-SSH "sudo rm -f /etc/nginx/sites-enabled/default"
    # Sicherstellen dass nginx.conf sites-enabled einbindet
    $hasSites = (Invoke-SSH-Safe "sudo grep -q 'sites-enabled' /etc/nginx/nginx.conf && echo yes || echo no").Output.Trim()
    if ($hasSites -eq "no") {
        Invoke-SSH "sudo sed -i '/http {/a\    include /etc/nginx/sites-enabled/*;' /etc/nginx/nginx.conf"
    }
    $hasConfd = (Invoke-SSH-Safe "sudo grep -q 'conf.d/\*.conf' /etc/nginx/nginx.conf && echo yes || echo no").Output.Trim()
    if ($hasConfd -eq "no") {
        Invoke-SSH "sudo sed -i '/http {/a\    include /etc/nginx/conf.d/*.conf;' /etc/nginx/nginx.conf"
    }
    # ── Admin-Basic-Auth: htpasswd erzeugen (getrennt vom Linux-Account) ──────
    # Nur wenn -AdminPass übergeben wurde; sonst bestehende Datei unangetastet.
    # Format: openssl passwd -apr1 (von nginx auth_basic verstanden, kein
    # apache2-utils nötig). Die Datei darf nur für root/nginx lesbar sein.
    $htFile = "/etc/nginx/fundus-admin.htpasswd"
    if ($AdminPass -ne "") {
        Write-Step "Admin-Basic-Auth einrichten (Benutzer: $AdminUser)..."
        # WICHTIG: Den apr1-Hash KOMPLETT auf dem Pi erzeugen UND schreiben, in
        # einem einzigen Remote-Befehl. Sonst enthaelt der Hash '$'-Zeichen
        # ($apr1$...), die beim Zurueckreichen durch PowerShell/SSH als Variablen
        # interpretiert und zerstoert werden → kaputtes htpasswd → 500 beim Login.
        # Das Passwort wird per Umgebungsvariable uebergeben (nicht in die
        # Kommandozeile interpoliert), damit Sonderzeichen sicher sind.
        $escPass = $AdminPass -replace "'", "'\''"
        $remoteCmd = "FPW='$escPass'; H=`$(openssl passwd -apr1 `"`$FPW`"); " +
                     "if echo `"`$H`" | grep -q '^\`$apr1\`$'; then " +
                     "echo `"$AdminUser`:`$H`" | sudo tee $htFile >/dev/null && " +
                     "sudo chown root:root $htFile && sudo chmod 644 $htFile && echo OK; " +
                     "else echo FAIL; fi"
        $res = (Invoke-SSH-Safe $remoteCmd).Output.Trim()
        if ($res -match "OK") {
            Write-Ok "Admin-Passwort gesetzt ($htFile)"
        } else {
            Write-Warn "Admin-Passwort konnte nicht gesetzt werden (openssl-Hash fehlgeschlagen)"
        }
    } else {
        # Existiert noch keine Datei? Dann einen Dummy mit GUELTIGEM Hash-Format
        # anlegen (Hash eines zufaelligen Passworts), damit auth_basic sauber mit
        # 401 (falsches Passwort) statt mit 500 (ungueltiges htpasswd-Format)
        # antwortet. Ein literaler Marker wie ':!locked' ist KEIN gueltiger
        # apr1-Hash und laesst nginx/OpenResty beim Login-Versuch 500 werfen.
        $exists = (Invoke-SSH-Safe "test -f $htFile && echo yes || echo no").Output.Trim()
        if ($exists -eq "no") {
            # Zufaelliges, niemandem bekanntes Passwort hashen → Login praktisch
            # unmoeglich, aber Format gueltig.
            $lockHash = (Invoke-SSH-Safe "openssl passwd -apr1 `"`$(openssl rand -base64 24)`"").Output.Trim()
            if ($lockHash -match '^\$apr1\$') {
                Invoke-SSH "echo '$AdminUser`:$lockHash' | sudo tee $htFile >/dev/null && sudo chown root:root $htFile && sudo chmod 644 $htFile"
            } else {
                # Fallback: leere Datei (auth_basic schlaegt dann mit 403 fehl, nicht 500)
                Invoke-SSH "sudo touch $htFile && sudo chown root:root $htFile && sudo chmod 644 $htFile"
            }
            Write-Warn "Kein -AdminPass uebergeben: Admin-Login ist gesperrt (sauberes 401). Deploy mit -AdminPass '...' zum Setzen."
        }
    }

    $test = Invoke-SSH-Safe "sudo nginx -t 2>&1"
    if ($test.Output -notmatch "successful") { Write-Warn "Config-Test: $($test.Output)" }
    Invoke-SSH "sudo systemctl enable nginx 2>/dev/null || true"
    Invoke-SSH-Safe "sudo timeout 60 systemctl restart nginx 2>&1"
    $svc = "nginx"
}

Start-Sleep -Seconds 2
$webStatus = (Invoke-SSH-Safe "systemctl is-active $svc 2>/dev/null || echo inactive").Output.Trim()
if ($webStatus -eq "active") {
    Write-Ok "Web-UI laeuft ($svc auf Port 80)"
} else {
    Write-Warn "$svc Status: $webStatus - Logs: journalctl -u $svc"
}

# =============================================================================
#  BLOCK J – systemd Service einrichten
# =============================================================================
Write-Step "systemd Service einrichten..."
$serviceFile = "/etc/systemd/system/${ServiceName}.service"
$svcCheck    = Invoke-SSH-Safe "sudo test -f $serviceFile && echo ok || echo missing"

# Mitgelieferte Service-Datei aus dem ZIP verwenden (immer ueberschreiben,
# damit Updates greifen). \r entfernen falls vorhanden (CRLF-Schutz).
# test mit sudo (Datei gehoert root:fundus, fuer 'other' nicht lesbar)
$zipSvc = Invoke-SSH-Safe "sudo test -f $RemoteDir/fundus-node.service && echo ok || echo missing"
if ($zipSvc.Output.Trim() -eq "ok") {
    Invoke-SSH "sudo cp $RemoteDir/fundus-node.service $serviceFile && sudo sed -i 's/\r$//' $serviceFile"
    Write-Fixed "Service-Datei installiert (aus Paket)"
} else {
    Write-Fail "fundus-node.service nicht im Paket gefunden (gesucht: $RemoteDir/fundus-node.service)"
}

# fundus-helper.service installieren (falls im Paket + Binary vorhanden). Der
# Helper ist optional: ohne ihn laeuft der Node normal, nur Mount/WLAN fehlen.
$zipHelperSvc = Invoke-SSH-Safe "sudo test -f $RemoteDir/fundus-helper.service && sudo test -f $RemoteDir/bin/fundus-helper && echo ok || echo missing"
if ($zipHelperSvc.Output.Trim() -eq "ok") {
    $helperServiceFile = "/etc/systemd/system/fundus-helper.service"
    Invoke-SSH "sudo cp $RemoteDir/fundus-helper.service $helperServiceFile && sudo sed -i 's/\r$//' $helperServiceFile"
    Invoke-SSH "sudo systemctl enable fundus-helper 2>/dev/null || true"

    # Automount: templated Trigger-Service (pro UUID) + udev-Regel (Einstecken).
    # Best-effort; ohne udev greift der periodische Scan im Helper trotzdem.
    $zipAutomount = Invoke-SSH-Safe "sudo test -f $RemoteDir/fundus-automount@.service && sudo test -f $RemoteDir/99-fundus-automount.rules && echo ok || echo missing"
    if ($zipAutomount.Output.Trim() -eq "ok") {
        Invoke-SSH "sudo cp '$RemoteDir/fundus-automount@.service' '/etc/systemd/system/fundus-automount@.service' && sudo sed -i 's/\r$//' '/etc/systemd/system/fundus-automount@.service'"
        Invoke-SSH "sudo cp $RemoteDir/99-fundus-automount.rules /etc/udev/rules.d/99-fundus-automount.rules && sudo sed -i 's/\r$//' /etc/udev/rules.d/99-fundus-automount.rules"
        Invoke-SSH-Safe "sudo udevadm control --reload-rules 2>&1 || true"
        Write-Fixed "Automount (Einstecken + Boot) eingerichtet"
    }

    Write-Fixed "fundus-helper.service installiert (Mount/WLAN aktiv)"
} else {
    Write-Info "fundus-helper nicht im Paket - Mount/WLAN-Funktionen werden uebersprungen"
}

Invoke-SSH "sudo systemctl daemon-reload"

# =============================================================================
#  BLOCK K – Service starten
# =============================================================================
# FINALER Rechte-Fix: data/ und chunks/ MUESSEN fundus:fundus gehoeren, damit
# LevelDB + Filestore schreiben koennen. Dies LETZTE chown, nach allen anderen
# (Admin-Unzip, Binary, Lua-chmod) die sonst data/ wieder auf root:fundus setzen.
# Service erst stoppen, damit kein Prozess das LevelDB-Lock haelt
Invoke-SSH "sudo systemctl stop $ServiceName 2>/dev/null || true"
# Alte Speicher-Reservierung aufraeumen: eine fehlerhafte fruehere Berechnung
# (OfferGB=50% → AllocGB=250%) konnte die Platte mit storage.alloc fuellen.
# Diese Reservierungsdatei gezielt entfernen - der Filestore legt sie beim
# Start mit korrekter Groesse neu an. Echte Chunk-Daten bleiben erhalten.
# So startet der Node auch wenn die Disk vorher randvoll war (kein Nachbasteln).
Invoke-SSH "sudo rm -f $RemoteDir/chunks/storage.alloc $RemoteDir/data/storage.alloc 2>/dev/null || true"
Invoke-SSH "sudo mkdir -p $RemoteDir/data $RemoteDir/chunks"
# Rechte-Fix OHNE teures rekursives Durchlaufen aller Chunks (das fror auf dem
# Pi mit hunderten Chunks + langsamer SD-Karte ein). Statt -R über alles:
#  - chown nur auf Dateien, die NICHT bereits dem Node-User gehören (find -exec),
#    im Normalfall sind das nur die Top-Verzeichnisse + wenige neue Dateien.
#  - Verzeichnis-Bits separat setzen (schnell), Datei-Bits nur wo nötig.
# Timeout schützt zusätzlich: Falls doch etwas hängt, bricht der Schritt nach
# 120s ab statt den ganzen Deploy einzufrieren.
# Arbeitsverzeichnisse (Upload-Sessions etc.) ZUERST und vollständig – sie sind
# klein. Die folgende find-Runde über alle Chunks kann bei großen Speichern in
# den Timeout laufen, bevor sie tmp/ erreicht ("session-dir: permission denied").
Invoke-SSH "sudo mkdir -p $RemoteDir/chunks/tmp/resume $RemoteDir/data/tmp && sudo chown -R ${FundusUser}:${FundusUser} $RemoteDir/chunks/tmp $RemoteDir/data/tmp 2>/dev/null || true"
Invoke-SSH "sudo timeout 120 find $RemoteDir/data $RemoteDir/chunks ! -user ${FundusUser} -exec chown ${FundusUser}:${FundusUser} {} + 2>/dev/null || true"
Invoke-SSH "sudo chown ${FundusUser}:${FundusUser} $RemoteDir/data $RemoteDir/chunks 2>/dev/null || true"
Invoke-SSH "sudo chmod u=rwX,go= $RemoteDir/data $RemoteDir/chunks 2>/dev/null || true"
# Verwaiste LevelDB-Locks aus abgestuerzten Vorlaeufern entfernen (Service ist gestoppt)
Invoke-SSH "sudo rm -f $RemoteDir/data/LOCK $RemoteDir/data/*/LOCK 2>/dev/null || true"
# Restart-Counter zuruecksetzen falls vorher im Crash-Loop blockiert
Invoke-SSH "sudo systemctl reset-failed $ServiceName 2>/dev/null || true"

Write-Step "Service starten..."
$_c13 = @"
sudo systemctl enable $ServiceName 2>/dev/null || true
"@
Invoke-SSH $_c13
# Helper zuerst (neu)starten, damit der Socket bereitsteht, bevor der Node kommt.
# Nur wenn die Service-Datei installiert wurde; Fehler nicht fatal.
Invoke-SSH-Safe "sudo test -f /etc/systemd/system/fundus-helper.service && sudo systemctl restart fundus-helper 2>&1 || true"

# Verifikation: läuft nach dem Restart wirklich die FRISCH gebaute Helper-Binary?
# Invoke-SSH-Safe liefert ein Objekt {Output, ExitCode} — wir brauchen .Output.
$helperCheckRes = Invoke-SSH-Safe "sudo strings /opt/fundus/bin/fundus-helper 2>/dev/null | grep -c 'System-/Boot-Medium' || echo 0"
$helperCheck = "$($helperCheckRes.Output)".Trim()
if ($helperCheck -eq "1") {
    Write-Ok "fundus-helper: aktuelle Binary laeuft (Mount-Schutzregeln aktiv)"
} else {
    Write-Warn "fundus-helper: laufende Binary enthaelt den aktuellen Code NICHT (strings-count=$helperCheck) - bitte melden!"
}

Invoke-SSH "sudo systemctl restart $ServiceName"

# Geduldig auf 'active' warten - der Node braucht auf dem Pi 10-30s
# (libp2p-Aufbau, Gnosis-RPC-Verbindung). 'activating' ist KEIN Fehler.
$status = ""
for ($i = 1; $i -le 20; $i++) {
    Start-Sleep -Seconds 2
    $status = (Invoke-SSH-Safe "systemctl is-active $ServiceName 2>/dev/null || echo inactive").Output.Trim()
    if ($status -eq "active") { break }
    if ($status -eq "failed") { break }
    # 'activating' -> weiter warten
}
if ($status -eq "active") {
    Write-Ok "Service laeuft (active)"
} elseif ($status -eq "failed") {
    Write-Warn "Service fehlgeschlagen – Logs: journalctl -u $ServiceName -n 30"
} else {
    Write-Warn "Service-Status: $status (startet evtl. noch) – Logs: journalctl -u $ServiceName"
}

# =============================================================================
#  BLOCK L – Health-Check
# =============================================================================
Write-Step "Health-Check (API auf :3000)..."

# Bis zu ~40s auf die API warten (Node-Start dauert auf dem Pi)
$api = "fail"
for ($i = 1; $i -le 20; $i++) {
    $api = (Invoke-SSH-Safe "curl -sf http://localhost:3000/api/v1/status 2>/dev/null || echo fail").Output.Trim()
    if ($api -ne "fail" -and $api -ne "") { break }
    Start-Sleep -Seconds 2
}
if ($api -ne "fail" -and $api -ne "") {
    Write-Ok "API antwortet"
    if ($api -match '"peer_id"\s*:\s*"([^"]+)"') {
        Write-Ok "Peer-ID: $($Matches[1])"
        Write-Host "    Bootstrap: /ip4/${PiHost}/tcp/4001/p2p/$($Matches[1])" -ForegroundColor DarkYellow
    }
} else {
    Write-Warn "API nach 40s nicht erreichbar – Logs: journalctl -u $ServiceName -n 30"
}

# =============================================================================
#  BLOCK M – Fee-Collector Verifikation
# =============================================================================
Write-Step "Fee-Collector prüfen..."
$cfgFee = (Invoke-SSH-Safe "sudo grep FUNDUS_FND_FEE_COLLECTOR /etc/fundus/fundus.env 2>/dev/null | cut -d= -f2").Output.Trim()
# Einzige Quelle in diesem Skript ist der Parameter $FeeCollector (oben).
$expectedFee = $FeeCollector
if ($cfgFee -and $cfgFee.ToLower() -eq $expectedFee.ToLower()) {
    Write-Ok "Fee-Collector korrekt: $cfgFee"
} elseif ($cfgFee) {
    Write-Warn "Fee-Collector in fundus.env: $cfgFee"
    Write-Warn "Erwartet:                   $expectedFee"
    Write-Warn "Bitte /etc/fundus/fundus.env auf dem Pi prüfen!"
} else {
    Write-Warn "Fee-Collector nicht lesbar"
}

# =============================================================================
#  Zusammenfassung
# =============================================================================
Write-Host ""
Write-Host "  ╔══════════════════════════════════════════╗" -ForegroundColor Green
Write-Host "  ║   Deployment abgeschlossen               ║" -ForegroundColor Green
Write-Host "  ╚══════════════════════════════════════════╝" -ForegroundColor Green
Write-Host ""
Write-Host "  Pi:      ${PiUser}@${PiHost}" -ForegroundColor Gray
Write-Host "  API:     http://${PiHost}:3000" -ForegroundColor Gray
Write-Host "  Web-UI:  http://${PiHost}:80"  -ForegroundColor Gray
Write-Host "  Logs:    ssh ${PiUser}@${PiHost} journalctl -fu $ServiceName" -ForegroundColor Gray
Write-Host ""
Write-Host "  Wallet anlegen: Web-UI → Wallet → Neu generieren" -ForegroundColor Cyan
Write-Host ""

# --- Externe Laufwerke pruefen: Hinweis, falls welche noch nicht fuer Fundus
# --- zugaenglich sind (udisks2-Mount unter /media, fuer den Node nicht lesbar).
# Invoke-SSH-Safe kann ein Array (mehrere Zeilen) liefern → immer zu String machen.
$driveRaw = Invoke-SSH-Safe "lsblk -o MOUNTPOINT -n -p 2>/dev/null | grep -E '^/media/|^/run/media/' | head -3"
$driveCheck = ($driveRaw | Out-String).Trim()
if ($driveCheck -ne "") {
    Write-Host "  .. Externe Laufwerke erkannt:" -ForegroundColor Cyan
    Write-Host "     $driveCheck" -ForegroundColor Gray
    Write-Host "     Diese werden vom fundus-helper automatisch fuer Fundus eingebunden" -ForegroundColor Gray
    Write-Host "     (innerhalb ~15s nach dem Einstecken, ueberlebt Neustarts)." -ForegroundColor Gray
    Write-Host ""
}

# Temporaeren Key loeschen
if ($script:CleanupTmpKey) {
    Remove-Item "$env:TEMP\deploy_$PID" -Force -ErrorAction SilentlyContinue
    Remove-Item "$env:TEMP\deploy_$PID.pub" -Force -ErrorAction SilentlyContinue
    Write-Info "Temporaerer Key geloescht"
}

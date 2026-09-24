<#
  deploy-fundus-arch.ps1  -  SSH-Deploy fuer einen Fundus-Node auf Arch/Manjaro.

  Fuer PowerShell 5.1 geschrieben: alle Shell-Kommandos mit Bash-Operatoren stehen in
  EINFACHEN Anfuehrungszeichen, damit PowerShell sie nicht selbst zu parsen
  versucht. Sie werden per String-Verkettung zusammengesetzt, wo Variablen noetig.

  Beispiel:
    .\deploy-fundus-arch.ps1 -PiHost 10.10.11.50 -PiUser tobias -ZipPath ".\FND.zip" -SudoPass "..." -Arch amd64
#>

param(
    [Parameter(Mandatory=$true)][string]$PiHost,
    [string]$PiUser       = "tobias",
    [string]$ZipPath      = ".\FND.zip",
    [string]$SudoPass     = "",
    [string]$Arch         = "amd64",
    [string]$GoCmd        = "",
    [switch]$SkipBuild    = $false,
    [switch]$SkipFrontend = $false
)

$ErrorActionPreference = "Stop"
function Write-Step($m){ Write-Host "`n=== $m ===" -ForegroundColor Cyan }
function Write-Ok($m){   Write-Host "  OK  $m" -ForegroundColor Green }
function Write-Info($m){ Write-Host "  ..  $m" -ForegroundColor Gray }
function Write-Warn($m){ Write-Host "  !!  $m" -ForegroundColor Yellow }
function Write-Fail($m){ Write-Host "  XX  $m" -ForegroundColor Red; exit 1 }

$target = "$PiUser@$PiHost"

# --- SSH-Verbindung vorab pruefen -------------------------------------------
# BatchMode=yes erzwingt: KEINE interaktive Passwortabfrage. Klappt der Login
# ohne Passwort (SSH-Key), geht es weiter. Sonst brechen wir mit klarem Hinweis
# ab, statt spaeter bei jedem scp/ssh stumm zu haengen.
Write-Host "`n=== SSH-Verbindung pruefen ===" -ForegroundColor Cyan
$sshTest = ssh -o BatchMode=yes -o ConnectTimeout=6 $target "echo ok" 2>&1
if ($sshTest -notmatch "ok") {
    Write-Host "  XX  SSH-Login zu $target verlangt ein Passwort (kein SSH-Key aktiv)." -ForegroundColor Red
    Write-Host ""
    Write-Host "  Ein Deploy-Skript kann die SSH-Passwortabfrage nicht bedienen -" -ForegroundColor Yellow
    Write-Host "  es wuerde bei jedem der vielen Uebertragungsschritte haengen." -ForegroundColor Yellow
    Write-Host "  Richte einmal einen SSH-Key ein, danach laeuft der Deploy glatt:" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "    ssh-keygen -t ed25519            # einmalig, falls noch kein Key" -ForegroundColor Gray
    Write-Host "  Dann den PUBLIC key (Datei id_ed25519.pub aus deinem .ssh-Ordner)" -ForegroundColor Gray
    Write-Host "  in ~/.ssh/authorized_keys auf dem Laptop eintragen. Am einfachsten" -ForegroundColor Gray
    Write-Host "  vom Laptop aus (dort direkt im Terminal):" -ForegroundColor Gray
    Write-Host "    mkdir -p ~/.ssh; nano ~/.ssh/authorized_keys   # Public-Key hineinkopieren" -ForegroundColor Gray
    Write-Host "  Danach dieses Skript erneut starten." -ForegroundColor Yellow
    exit 1
}
Write-Host "  OK  SSH-Login ohne Passwort moeglich (Key aktiv)" -ForegroundColor Green

# --- sudo-Passwort besorgen (Parameter ODER sichere Abfrage) ----------------
# Wird fuer alle 'sudo'-Schritte auf dem Node gebraucht. Nicht im Klartext als
# Parameter noetig - wenn -SudoPass leer ist, hier einmal sicher abfragen.
if (-not $SudoPass) {
    $secure = Read-Host -AsSecureString "sudo-Passwort fuer $PiUser auf $PiHost"
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    $SudoPass = [Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
}

# Remote-Kommando mit sudo (Passwort per stdin). $cmd ist ein reiner Bash-String
# in EINFACHEN Quotes vom Aufrufer - PowerShell interpretiert nichts darin.
function SSH-Sudo([string]$cmd){
    $payload = "echo '$SudoPass' | sudo -S -p '' bash -c " + [char]34 + $cmd + [char]34 + " 2>&1"
    ssh $target $payload
}
# Remote-Kommando ohne sudo.
function SSH-Plain([string]$cmd){
    ssh $target $cmd 2>&1
}

# --- 1. Binary bauen ---------------------------------------------------------
if (-not $SkipBuild) {
    Write-Step "Binary bauen (linux/$Arch)"
    $go = if ($GoCmd) { $GoCmd } else { "go" }
    Push-Location go
    try {
        $env:GOOS = "linux"; $env:GOARCH = $Arch; $env:CGO_ENABLED = "0"
        & $go mod tidy 2>&1 | Out-Null
        & $go build -o ..\bin\fundus-node .\cmd\fundus-node
        if ($LASTEXITCODE -ne 0) { Write-Fail "go build fehlgeschlagen" }
        & $go build -o ..\bin\fundus-helper .\cmd\fundus-helper 2>&1 | Out-Null
    } finally {
        $env:GOOS=""; $env:GOARCH=""; $env:CGO_ENABLED=""
        Pop-Location
    }
    Write-Ok "fundus-node gebaut (linux/$Arch)"
}

# --- 2. Dateien uebertragen --------------------------------------------------
Write-Step "Dateien uebertragen"
SSH-Sudo 'mkdir -p /opt/fundus/bin /opt/fundus/data /opt/fundus/chunks /etc/fundus'
scp $ZipPath ($target + ":/tmp/fnd.zip")
scp .\bin\fundus-node ($target + ":/tmp/fundus-node")
if (Test-Path .\bin\fundus-helper) { scp .\bin\fundus-helper ($target + ":/tmp/fundus-helper") }
SSH-Sudo 'unzip -o /tmp/fnd.zip -d /opt/fundus >/dev/null; mv /tmp/fundus-node /opt/fundus/bin/; chmod +x /opt/fundus/bin/fundus-node'
SSH-Sudo 'if [ -f /tmp/fundus-helper ]; then mv /tmp/fundus-helper /opt/fundus/bin/; chmod +x /opt/fundus/bin/fundus-helper; fi'
Write-Ok "Dateien in /opt/fundus"

# --- 3. Node-User + Basispakete ---------------------------------------------
Write-Step "Systemvorbereitung (pacman)"
SSH-Sudo 'if ! id fundus >/dev/null 2>&1; then useradd --system --create-home --home-dir /home/fundus --shell /usr/bin/nologin --comment Fundus fundus; fi'
SSH-Sudo 'pacman -Sy --needed --noconfirm unzip curl 2>&1 | tail -3'
Write-Ok "Basispakete + fundus-User"

# --- 4. Web-Frontend: OpenResty (AUR) ---------------------------------------
if (-not $SkipFrontend) {
    Write-Step "Web-Frontend (OpenResty via AUR)"
    $orHave = (SSH-Plain 'if command -v openresty >/dev/null 2>&1; then echo yes; else echo no; fi').Trim()
    if ($orHave -ne "yes") {
        $helper = (SSH-Plain 'if command -v yay >/dev/null 2>&1; then echo yay; elif command -v paru >/dev/null 2>&1; then echo paru; else echo none; fi').Trim()
        if ($helper -ne "none") {
            Write-Info "AUR-Helper: $helper"
            SSH-Plain ($helper + ' -S --needed --noconfirm openresty 2>&1 | tail -8')
        } else {
            Write-Warn "Kein AUR-Helper (yay/paru) - baue OpenResty manuell via makepkg."
            SSH-Sudo 'pacman -S --needed --noconfirm base-devel git 2>&1 | tail -3'
            SSH-Plain 'cd /tmp; rm -rf openresty-aur; git clone https://aur.archlinux.org/openresty.git openresty-aur 2>&1 | tail -2'
            SSH-Plain 'cd /tmp/openresty-aur; makepkg -si --noconfirm 2>&1 | tail -12'
        }
        $orHave = (SSH-Plain 'if command -v openresty >/dev/null 2>&1; then echo yes; else echo no; fi').Trim()
        if ($orHave -ne "yes") {
            Write-Warn "OpenResty konnte nicht automatisch installiert werden."
            Write-Warn "Auf dem Laptop: 'yay -S openresty', dann Skript mit -SkipFrontend erneut."
        } else {
            Write-Ok "OpenResty installiert"
        }
    } else {
        Write-Ok "OpenResty bereits vorhanden"
    }

    if ($orHave -eq "yes") {
        SSH-Sudo 'mkdir -p /etc/openresty/conf.d'
        SSH-Sudo 'if ! grep -q "conf.d/\*.conf" /etc/openresty/nginx.conf; then sed -i "/http {/a\    include conf.d/*.conf;" /etc/openresty/nginx.conf; fi'
        SSH-Sudo 'cp /opt/fundus/lua/fundus-http.conf /etc/openresty/conf.d/00-fundus-http.conf'
        SSH-Sudo 'cp /opt/fundus/lua/fundus-nginx.conf /etc/openresty/conf.d/fundus.conf'
        SSH-Sudo 'sed -i "s/\r$//" /etc/openresty/conf.d/00-fundus-http.conf /etc/openresty/conf.d/fundus.conf'
        SSH-Sudo 'chmod o+rX /opt/fundus; chmod -R o+rX /opt/fundus/lua'
        SSH-Sudo 'systemctl enable openresty 2>/dev/null; systemctl restart openresty 2>&1 | tail -3'
        $webUp = (SSH-Plain 'systemctl is-active openresty 2>/dev/null || echo inactive').Trim()
        if ($webUp -eq "active") {
            Write-Ok "OpenResty laeuft"
        } else {
            Write-Warn "OpenResty nicht aktiv - 'journalctl -u openresty' pruefen"
        }
    }
}

# --- 5. Node-Service ---------------------------------------------------------
Write-Step "Node-Service einrichten"
SSH-Sudo 'cp /opt/fundus/fundus-node.service /etc/systemd/system/fundus-node.service'
SSH-Sudo 'chown -R fundus:fundus /opt/fundus/data /opt/fundus/chunks'
SSH-Sudo 'if [ ! -f /etc/fundus/fundus.env ]; then cp /opt/fundus/fundus.env /etc/fundus/fundus.env; fi'
SSH-Sudo 'systemctl daemon-reload'
SSH-Sudo 'systemctl enable fundus-node 2>/dev/null; systemctl restart fundus-node 2>&1 | tail -3'
Start-Sleep -Seconds 3
$nodeUp = (SSH-Plain 'systemctl is-active fundus-node 2>/dev/null || echo inactive').Trim()
if ($nodeUp -eq "active") {
    Write-Ok "fundus-node laeuft"
} else {
    Write-Warn "fundus-node nicht aktiv - 'journalctl -u fundus-node' pruefen"
}

# --- 6. Naechste Schritte ----------------------------------------------------
Write-Step "Fertig"
Write-Host ""
Write-Host "  Node deployed auf $target." -ForegroundColor Cyan
Write-Host ""
Write-Host "  BEITRITTS-TEST:" -ForegroundColor Cyan
Write-Host "  1) Fee-Collector ist fest im Code (0xea5594...) - nichts setzen. Der Laptop"
Write-Host "     erzeugt automatisch denselben Genesis wie die Pis. Kein Kopieren noetig."
Write-Host "  2) In /etc/fundus/fundus.env: FUNDUS_VALIDATORS LEER lassen (NICHT die"
Write-Host "     Laptop-Adresse eintragen) - der Beitritt soll per Stake passieren."
Write-Host "  3) FUNDUS_BOOTSTRAP_PEERS auf eine Multiaddr eines Pi setzen, z.B.:"
Write-Host "       FUNDUS_BOOTSTRAP_PEERS=/ip4/10.10.11.39/tcp/4001/p2p/<PeerID-39er>"
Write-Host "  4) sudo systemctl restart fundus-node, dann Log pruefen:"
Write-Host "       journalctl -u fundus-node -b | grep -iE 'genesis|PoA-Konsens|angebunden'"
Write-Host "     genesis-Hash MUSS mit den Pis uebereinstimmen, keine 'abweichender'-Warnung."
Write-Host ""
Write-Host "  Dann: Laptop-Wallet-Adresse holen, von einem Pi FND (>=10) hinueberweisen," -ForegroundColor Cyan
Write-Host "  vom Laptop staken, und zusehen wie 'validators' auf beiden Pis 2->3 steigt."

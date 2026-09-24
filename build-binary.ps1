#!/usr/bin/env pwsh
# =============================================================================
#  Fundus - Binary Cross-Compile (Windows -> Raspberry Pi)
#
#  Baut das fundus-node Binary fuer den Pi auf deinem Windows-PC.
#  Anschliessend deploy-fundus.ps1 ausfuehren - es laedt das Binary hoch.
#
#  Voraussetzung: Go installiert (https://go.dev/dl/)
#  Aufruf:        .\build-binary.ps1
# =============================================================================

param(
    [string]$Arch = "arm64"   # arm64 = Pi 3/4/5 (64-Bit OS), arm = 32-Bit OS
)

function Write-Step($m) { Write-Host "`n  --> $m" -ForegroundColor Cyan }
function Write-Ok($m)   { Write-Host "      [OK]   $m" -ForegroundColor Green }
function Write-Fail($m) { Write-Host "`n  [FEHLER] $m" -ForegroundColor Red; exit 1 }

Write-Host ""
Write-Host "  ============================================" -ForegroundColor DarkCyan
Write-Host "   Fundus Binary Cross-Compile (-> $Arch)" -ForegroundColor DarkCyan
Write-Host "  ============================================" -ForegroundColor DarkCyan

# Go vorhanden?
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Fail "Go nicht gefunden. Installieren: https://go.dev/dl/"
}
Write-Ok "Go: $(go version)"

# Ins go-Verzeichnis wechseln
$goDir = Join-Path $PSScriptRoot "go"
if (-not (Test-Path (Join-Path $goDir "go.mod"))) {
    # Vielleicht liegt das Skript schon im go-Verzeichnis
    if (Test-Path (Join-Path $PSScriptRoot "go.mod")) {
        $goDir = $PSScriptRoot
    } else {
        Write-Fail "go.mod nicht gefunden. Skript muss neben dem 'go'-Ordner liegen."
    }
}
Set-Location $goDir
Write-Ok "Verzeichnis: $goDir"

# Dependencies + go.sum erzeugen (auf dem PC mit genug Speicher)
Write-Step "Dependencies laden (go mod tidy)..."
$env:GOFLAGS = "-mod=mod"
go mod tidy 2>&1 | Out-Host
if ($LASTEXITCODE -ne 0) { Write-Fail "go mod tidy fehlgeschlagen" }
Write-Ok "go.sum erzeugt"

# Cross-Compile fuer Linux/ARM
Write-Step "Cross-Compile fuer linux/$Arch..."
$env:GOOS   = "linux"
$env:GOARCH = $Arch
if ($Arch -eq "arm") { $env:GOARM = "7" }   # Pi 2/3 32-Bit = ARMv7
$env:CGO_ENABLED = "0"                        # statisches Binary, keine C-Abhaengigkeiten

$outDir = Join-Path $goDir "..\bin"
New-Item -ItemType Directory -Path $outDir -Force | Out-Null
$outFile = Join-Path $outDir "fundus-node"

go build -ldflags="-s -w -X main.Version=R$(((Get-Content (Join-Path $PSScriptRoot 'revision.txt') -TotalCount 1 -ErrorAction SilentlyContinue) -replace '\D',''))" -o $outFile ./cmd/fundus-node 2>&1 | Out-Host
if ($LASTEXITCODE -ne 0) { Write-Fail "Build fehlgeschlagen" }

# Aufraeumen der Umgebungsvariablen
$env:GOOS = ""; $env:GOARCH = ""; $env:GOARM = ""; $env:CGO_ENABLED = ""; $env:GOFLAGS = ""

$size = [math]::Round((Get-Item $outFile).Length / 1MB, 1)
Write-Ok "Binary erstellt: $outFile ($size MB)"
Write-Host ""
Write-Host "  Naechster Schritt:" -ForegroundColor Cyan
Write-Host "    .\deploy-fundus.ps1 -PiHost <IP>" -ForegroundColor White
Write-Host "  Das Deploy-Skript laedt dieses Binary automatisch hoch." -ForegroundColor Gray
Write-Host ""

# =============================================================================
# deploy-all-pis.ps1 - aktualisiert mehrere Pis PARALLEL.
#
# Baut Node + Helper EINMAL vorab und oeffnet dann pro Pi ein eigenes
# PowerShell-Fenster, in dem deploy-fundus.ps1 dieses Binary verteilt. Du siehst
# pro Pi eine eigene Konsole mit dem Fortschritt.
#
#   .\deploy-all-pis.ps1
#
# Die Zugangsdaten werden EINMAL abgefragt (nicht im Skript gespeichert) und an
# alle Fenster weitergereicht.
# =============================================================================

# --- Pi-Liste (letzte Oktett-Nummern im 10.10.11.x-Netz) ---
$PiIPs   = @("10.10.11.25", "10.10.11.39", "10.10.11.71", "10.10.11.72", "10.10.11.73")
$PiUser  = "tobias"

# Solana-Swap-Konfiguration (wird gezielt in jede fundus.env gesetzt, ohne die
# uebrige env zu ueberschreiben). Fuer den Produktivbetrieb spaeter leeren oder den
# lokalen Laptop-RPC gegen Devnet/Mainnet tauschen.
$SwapHtlcProgram = "DZ96w28m8tZPM3vjJHFrLXJnonWbqtTAvgZ46c2SdXtb"
$SolanaRpc       = "http://10.10.11.85:8899"

# --- Zugangsdaten einmal abfragen (sicher, nicht im Skript hinterlegt) ---
Write-Host "Zugangsdaten fuer das Deploy auf alle Pis (werden nur an die Fenster weitergegeben):" -ForegroundColor Cyan
# Ein Passwort fuer sudo, Cert UND Admin - unsichtbare Eingabe (-AsSecureString).
Write-Host "Passwort fuer das Deploy auf alle Pis (wird fuer sudo, Cert und Admin verwendet):" -ForegroundColor Cyan
$securePass = Read-Host "  Passwort" -AsSecureString
# SecureString in Klartext umwandeln, um es an die Deploy-Skripte weiterzugeben.
$bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($securePass)
$PlainPass = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
$SudoPass  = $PlainPass
$CertPass  = $PlainPass
$AdminPass = $PlainPass

# --- Pfad zum eigentlichen Deploy-Skript (liegt neben diesem Skript) ---
$deployScript = Join-Path $PSScriptRoot "deploy-fundus.ps1"
if (-not (Test-Path $deployScript)) {
    Write-Host "FEHLER: deploy-fundus.ps1 nicht gefunden neben diesem Skript ($PSScriptRoot)." -ForegroundColor Red
    exit 1
}

# --- EINMAL vorab bauen (Node + Helper). Frueher baute jedes Fenster mit
# -Rebuild gleichzeitig in dasselbe bin\ - die Builds behinderten sich, ein Pi
# bekam dann neue Oberflaeche mit altem Programm. ---
Write-Host ""
Write-Host "Baue Node + Helper einmal vorab..." -ForegroundColor Cyan
& powershell.exe -ExecutionPolicy Bypass -File $deployScript -BuildOnly
if ($LASTEXITCODE -ne 0) {
    Write-Host "FEHLER: Build fehlgeschlagen - kein Deploy gestartet." -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "Starte Deploy auf $($PiIPs.Count) Pis parallel..." -ForegroundColor Green

foreach ($ip in $PiIPs) {
    # Argumente fuer das Deploy-Skript zusammenbauen.
    $argList = @(
        "-NoExit",                              # Fenster bleibt offen, damit du das Ergebnis siehst
        "-ExecutionPolicy", "Bypass",
        "-File", "`"$deployScript`"",
        "-PiHost", $ip,
        "-PiUser", $PiUser,
        # kein -Rebuild: das vorab gebaute Binary (bin\fundus-node.rev) wird genutzt
        "-SudoPass", "`"$SudoPass`"",
        "-CertPass", "`"$CertPass`"",
        "-AdminPass", "`"$AdminPass`"",
        "-SwapHtlcProgram", "`"$SwapHtlcProgram`"",
        "-SolanaRpc", "`"$SolanaRpc`""
    )
    # Ein eigenes PowerShell-Fenster pro Pi oeffnen (parallel).
    Start-Process -FilePath "powershell.exe" -ArgumentList $argList
    Write-Host "  -> Fenster fuer $ip gestartet" -ForegroundColor Gray
    Start-Sleep -Milliseconds 400   # kleiner Versatz, damit die Fenster sich nicht ins Gehege kommen
}

Write-Host ""
Write-Host "Alle $($PiIPs.Count) Deploy-Fenster gestartet. Jedes Fenster zeigt seinen eigenen Fortschritt." -ForegroundColor Green
Write-Host "Dieses Fenster kann geschlossen werden." -ForegroundColor Gray

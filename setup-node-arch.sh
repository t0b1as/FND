#!/usr/bin/env bash
#
# setup-node-arch.sh - Fundus-Node auf Arch/Manjaro einrichten ODER aktualisieren.
#
# Direkt auf dem Laptop ausfuehren. Entpackt die ZIP, baut die Binary NEU und
# startet den Service - jedes Mal, auch beim Update. Der Zielpfad ist FEST
# (/opt/fundus), unabhaengig davon ob mit oder ohne sudo aufgerufen - so koennen
# Build-Ziel und Service-Pfad nie auseinanderlaufen.
#
#   bash setup-node-arch.sh [/pfad/zu/FND.zip]
#
# Ohne Pfad sucht das Skript die FND.zip auf USB-Sticks und im aktuellen Ordner.

set -e

c_step() { echo -e "\n\033[36m=== $* ===\033[0m"; }
c_ok()   { echo -e "\033[32m  OK  $*\033[0m"; }
c_info() { echo -e "\033[90m  ..  $*\033[0m"; }
c_warn() { echo -e "\033[33m  !!  $*\033[0m"; }
c_fail() { echo -e "\033[31m  XX  $*\033[0m"; exit 1; }

# FESTER Zielpfad - NICHT $HOME (das haengt von sudo ab und verursacht genau die
# Pfad-Verwirrung, bei der man an einer Stelle baut und der Service eine andere
# Binary startet). /opt/fundus ist der kanonische Ort, wie bei den Pis.
FUNDUS_HOME="/opt/fundus"
# Falls schon ein Service existiert, dessen Pfad UEBERNEHMEN, damit nicht zwei
# parallele Installationen entstehen (z.B. altes /root/fundus aus fruehem Setup).
EXISTING_EXEC=$(systemctl show fundus-node -p ExecStart 2>/dev/null | grep -oP 'path=\K[^ ;]+' | head -1)
if [ -n "$EXISTING_EXEC" ] && [ -d "$(dirname "$EXISTING_EXEC")/go" ]; then
    FUNDUS_HOME="$(dirname "$EXISTING_EXEC")"
    echo "  ..  bestehende Installation erkannt: $FUNDUS_HOME (wird aktualisiert)"
fi
ENVFILE="/etc/fundus/node.env"
# Auch den EnvironmentFile-Pfad des bestehenden Service uebernehmen (sonst liest
# der Service eine andere node.env als das Skript schreibt - genau der Bug von
# vorhin, wo FUNDUS_STORAGE_OFFER_GB nie ankam).
EXISTING_ENV=$(systemctl show fundus-node -p EnvironmentFiles 2>/dev/null | grep -oP '/[^ ]+node\.env' | head -1)
if [ -n "$EXISTING_ENV" ]; then
    ENVFILE="$EXISTING_ENV"
fi
SERVICE="/etc/systemd/system/fundus-node.service"
RUN_USER="${SUDO_USER:-$USER}"   # der eigentliche Nutzer, auch wenn per sudo aufgerufen
ZIP="$1"

# --- 1. ZIP finden -----------------------------------------------------------
c_step "FND.zip finden"
if [ -z "$ZIP" ]; then
    ZIP=$(find /run/media/"$RUN_USER" /media/"$RUN_USER" /mnt ./ ~ -maxdepth 3 -iname "FND.zip" 2>/dev/null | head -1)
fi
if [ -z "$ZIP" ] || [ ! -f "$ZIP" ]; then
    c_fail "FND.zip nicht gefunden. Pfad angeben: bash setup-node-arch.sh /pfad/zu/FND.zip"
fi
c_ok "gefunden: $ZIP"

# --- 2. Pakete ---------------------------------------------------------------
c_step "Pakete (Go, unzip)"
sudo pacman -Sy --needed --noconfirm go unzip 2>&1 | tail -2
c_ok "Go + unzip bereit"

# --- 3. Entpacken (immer frisch, alte Quellen ueberschreiben) ----------------
c_step "Entpacken nach $FUNDUS_HOME"
sudo mkdir -p "$FUNDUS_HOME" /etc/fundus
sudo unzip -o "$ZIP" -d "$FUNDUS_HOME" >/dev/null
# Revision aus der ZIP anzeigen, damit man SIEHT welche Version man deployt.
REV=$(sudo cat "$FUNDUS_HOME/revision.txt" 2>/dev/null || echo "?")
c_ok "entpackt (Revision R$REV)"

# --- 4. Binary NEU bauen (immer) ---------------------------------------------
# Der Build laeuft ins FESTE Ziel $FUNDUS_HOME/fundus-node - genau das, was der
# Service startet. Kein Auseinanderlaufen von Build-Ort und Service-Pfad moeglich.
c_step "Binary bauen (nativ)"
GO_BIN="$(command -v go || echo /usr/bin/go)"
# go build als root, damit es ins root-eigene /opt/fundus schreiben darf. GOCACHE
# auf ein beschreibbares Verzeichnis setzen (root hat ggf. keinen HOME-Cache).
sudo env "PATH=$PATH" GOCACHE=/tmp/gocache GOPATH=/tmp/gopath \
    bash -c "cd '$FUNDUS_HOME/go' && '$GO_BIN' get go.etcd.io/bbolt@v1.3.10 >/dev/null 2>&1; '$GO_BIN' mod tidy >/dev/null 2>&1; '$GO_BIN' build -o '$FUNDUS_HOME/fundus-node' ./cmd/fundus-node"
sudo test -x "$FUNDUS_HOME/fundus-node" || c_fail "Build fehlgeschlagen - Ausgabe oben pruefen"
# Verifikation: enthaelt die frische Binary den erwarteten neuen Code?
if sudo strings "$FUNDUS_HOME/fundus-node" | grep -q "LearnValidators"; then
    c_ok "fundus-node gebaut (R$REV, dezentrale Validator-Verbreitung aktiv)"
else
    c_warn "fundus-node gebaut, aber 'LearnValidators' fehlt - alte Quellen? ZIP pruefen."
fi

# --- 5. Verzeichnisse + Konfiguration ----------------------------------------
c_step "Verzeichnisse + Konfiguration"
sudo mkdir -p "$FUNDUS_HOME/data" "$FUNDUS_HOME/chunks"
# node.env NUR anlegen, wenn sie noch nicht existiert - vorhandene Einstellungen
# (z.B. FUNDUS_STORAGE_OFFER_GB) NICHT ueberschreiben.
if [ ! -f "$ENVFILE" ]; then
    sudo tee "$ENVFILE" >/dev/null <<ENVEOF
FUNDUS_DATA_DIR=$FUNDUS_HOME/data
FUNDUS_STORAGE_DIR=$FUNDUS_HOME/chunks
FUNDUS_STORAGE_OFFER_GB=1
FUNDUS_PORT=3000
FUNDUS_P2P_PORT=4001
FUNDUS_BOOTSTRAP_PEERS=
FUNDUS_VALIDATORS=
ENVEOF
    c_ok "node.env angelegt (Filesharing an, Validatoren per Netz-Discovery)"
else
    # Sicherstellen, dass Filesharing an ist (sonst keine Wallet) - idempotent.
    if ! sudo grep -q "FUNDUS_STORAGE_OFFER_GB=" "$ENVFILE"; then
        echo 'FUNDUS_STORAGE_OFFER_GB=1' | sudo tee -a "$ENVFILE" >/dev/null
    fi
    c_ok "node.env vorhanden (beibehalten)"
fi

# --- 6. systemd-Service (immer aktualisieren, Pfade fest) --------------------
c_step "Service einrichten"
sudo tee "$SERVICE" >/dev/null <<SVCEOF
[Unit]
Description=Fundus Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$FUNDUS_HOME
EnvironmentFile=$ENVFILE
ExecStart=$FUNDUS_HOME/fundus-node
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
SVCEOF
sudo systemctl daemon-reload
sudo systemctl enable fundus-node 2>/dev/null || true
sudo systemctl restart fundus-node
sleep 4
if [ "$(systemctl is-active fundus-node 2>/dev/null)" = "active" ]; then
    c_ok "fundus-node laeuft (R$REV)"
else
    c_warn "Node nicht aktiv - 'journalctl -u fundus-node -b | tail -30' pruefen"
fi

# --- 7. Status ---------------------------------------------------------------
c_step "Status"
sleep 2
sudo journalctl -u fundus-node -b --no-pager | grep -iE 'genesis_hash|Node-Wallet|PoA-Konsens|Filesharing' | tail -6
echo ""
c_info "Chain-Status live:   curl -s http://localhost:3000/api/v1/chain/status"
c_info "Der Laptop lernt die Validatoren beim Sync automatisch vom Netz (bis ~60s)."
c_info "Danach staken: Wallet-Seite -> 'Validator werden', oder per curl:"
c_info "  curl -s -X POST http://localhost:3000/api/v1/wallet/stake -H 'Content-Type: application/json' -d '{\"amount_fnd\":10}'"

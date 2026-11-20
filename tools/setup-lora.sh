#!/usr/bin/env bash
# Funkdienst einrichten (R945): Reticulum/LXMF für ein RNode-Funkmodul
# (Heltec WiFi LoRa 32 V3 o. ä. mit RNode-Firmware).
#
# Von Hand:            sudo bash setup-lora.sh
# Beim Deploy/Update:  sudo bash setup-lora.sh --auto
#   --auto: Ohne USB-Funkmodul und ohne frühere Einrichtung still beenden.
#
# Richtet ein: Python-Umgebung /opt/fundus/rns-venv (rns, lxmf, pyserial),
# Datenordner /opt/fundus/data/rns, Benutzer fundus in Gruppe dialout,
# Dienst fundus-rns.service. Bricht Deploy/Update nie ab.

PREFIX="${FUNDUS_PREFIX:-/opt/fundus}"
DATA="${FUNDUS_DATA_DIR:-$PREFIX/data}"
VENV="$PREFIX/rns-venv"
UNIT_QUELLE="$PREFIX/tools/fundus-rns.service"
AUTO=0; [ "${1:-}" = "--auto" ] && AUTO=1

ende() { echo "   $*"; exit 0; }
[ "$(id -u)" -eq 0 ] || { echo "Bitte mit sudo ausführen."; exit 1; }

geraet=$(ls /dev/serial/by-id/ 2>/dev/null | grep -E 'CP210|Silicon_Labs|CH340|CH9102|Espressif|JTAG|RNode' | head -1)
if [ -z "$geraet" ] && [ ! -x "$VENV/bin/python" ] && [ "$AUTO" -eq 1 ]; then
  ende "Funk (LoRa): kein USB-Funkmodul erkannt – übersprungen"
fi
echo "   Funk (LoRa): ${geraet:-kein Modul eingesteckt, Einrichtung bleibt erhalten}"

if ! python3 -c 'import venv, ensurepip' >/dev/null 2>&1; then
  echo "   python3-venv installieren …"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -q python3-venv >/dev/null 2>&1 || ende "python3-venv nicht installierbar – von Hand: sudo apt install python3-venv"
fi
if [ ! -x "$VENV/bin/python" ]; then
  python3 -m venv "$VENV" || ende "Python-Umgebung nicht anlegbar"
fi
# Feste Hauptversionen: Ein Sprung auf rns 2.x käme nur mit einem Fundus-Update.
if ! "$VENV/bin/pip" install -q --disable-pip-version-check "rns>=1.6.1,<2" "lxmf>=1.3.1,<2" "pyserial>=3.5" >/tmp/fundus-rns-pip.log 2>&1; then
  ende "Reticulum nicht installierbar (Internet?) – Protokoll: /tmp/fundus-rns-pip.log"
fi
echo "   Reticulum $("$VENV/bin/python" -c 'import importlib.metadata as m; print(m.version("rns"), "· LXMF", m.version("lxmf"))' 2>/dev/null)"

id fundus >/dev/null 2>&1 && usermod -aG dialout fundus
mkdir -p "$DATA/rns"
chown fundus:fundus "$DATA/rns"; chmod 700 "$DATA/rns"
# Der Dienst läuft als fundus: Umgebung wie der Rest von /opt/fundus
# (root:fundus, Gruppe darf lesen – so setzt es auch der Deploy).
chown -R root:fundus "$VENV" 2>/dev/null; chmod -R g+rX "$VENV" 2>/dev/null

if [ -f "$UNIT_QUELLE" ]; then
  sed 's/\r$//' "$UNIT_QUELLE" > /etc/systemd/system/fundus-rns.service
else
  ende "tools/fundus-rns.service fehlt im Paket"
fi
systemctl daemon-reload
systemctl enable fundus-rns >/dev/null 2>&1
systemctl restart fundus-rns
sleep 4
if systemctl is-active --quiet fundus-rns; then
  echo "   fundus-rns läuft (Status: Admin → Funk → LoRa)"
else
  echo "   ✗ fundus-rns startet nicht – journalctl -u fundus-rns"
fi
exit 0

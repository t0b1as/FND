#!/usr/bin/env bash
# Funkmodus des RTL-SDR-Sticks umschalten (R859).
#
# Ein Stick hört immer nur einen Frequenzbereich. Der Fundus-Node schreibt den
# gewünschten Modus nach $MODUS_DATEI; der systemd-Pfadwächter
# fundus-funk.path bemerkt die Änderung und startet fundus-funk.service, der
# dieses Skript mit Root-Rechten ausführt. So braucht der Node keine
# sudo-Freigabe, um Dienste zu steuern. Beim Systemstart läuft es ebenfalls.
#
#   adsb – Flugzeuge, 1090 MHz      (readsb)
#   vdl2 – Funknachrichten, 136 MHz (dumpvdl2 → UDP 5555)
#   ais  – Schiffe, 162 MHz         (AIS-catcher → UDP 10110)
#
# Status für die Seite: /run/fundus-funk/status.json

MODUS_DATEI="${FUNDUS_DATA_DIR:-/opt/fundus/data}/funk-modus"
STATUS_DIR=/run/fundus-funk
mkdir -p "$STATUS_DIR"; chmod 755 "$STATUS_DIR"

modus=$(tr -dc 'a-z0-9' < "$MODUS_DATEI" 2>/dev/null)
case "$modus" in
  vdl2) dienst=fundus-vdl2 ;;
  ais)  dienst=fundus-ais ;;
  *)    modus=adsb; dienst=readsb ;;
esac

# Alle anderen anhalten – der Stick kann nur einem Programm gehören.
for d in readsb fundus-vdl2 fundus-ais; do
  [ "$d" = "$dienst" ] || systemctl stop "$d" 2>/dev/null
done
sleep 1

fehler=""
if systemctl cat "$dienst" >/dev/null 2>&1; then
  systemctl restart "$dienst" 2>/dev/null || fehler="$dienst startet nicht"
else
  fehler="$dienst ist nicht installiert"
fi
sleep 3
aktiv=false
systemctl is-active --quiet "$dienst" && aktiv=true
[ "$aktiv" = false ] && [ -z "$fehler" ] && fehler="$dienst läuft nicht (journalctl -u $dienst)"

printf '{"modus":"%s","dienst":"%s","aktiv":%s,"seit":%s,"fehler":"%s"}\n' \
  "$modus" "$dienst" "$aktiv" "$(date +%s)" "$fehler" > "$STATUS_DIR/status.json.neu"
chmod 644 "$STATUS_DIR/status.json.neu"
mv "$STATUS_DIR/status.json.neu" "$STATUS_DIR/status.json"
echo "Funkmodus: $modus ($dienst, aktiv: $aktiv)${fehler:+ – $fehler}"
exit 0

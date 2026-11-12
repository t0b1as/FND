#!/usr/bin/env bash
# WLAN-Hotspot (R865): Einrichtung – Dienste immer, Pakete erst beim Einschalten.
# Aufruf: aus setup-aeronautik.sh (Deploy, Update, install.sh) und über den
# Pfadwächter fundus-hotspot-konfig.path, sobald die Einstellungen sich ändern.
# Nichts hier fasst die Netzwerkverbindung des Pi an – das tut nur der Wächter,
# und der nur auf einem FREIEN WLAN-Adapter.
DATA_DIR="${FUNDUS_DATA_DIR:-/opt/fundus/data}"
WERKZEUGE="$(dirname "$(readlink -f "$0")")"
[ "$(id -u)" -eq 0 ] || { echo "   Hotspot-Einrichtung braucht Root – übersprungen."; exit 0; }

AKTIV=$(python3 -c "import json,sys; print('an' if json.load(open(sys.argv[1])).get('aktiv') else 'aus')" "$DATA_DIR/hotspot.json" 2>/dev/null || echo aus)

# Pakete nur, wenn eingeschaltet. dnsmasq-base = nur das Programm, ohne
# systemweiten DNS-Dienst, der mit dem Netz des Pi kollidieren könnte.
if [ "$AKTIV" = an ]; then
  FEHLT=""
  for b in hostapd dnsmasq nft iw tc; do command -v "$b" >/dev/null 2>&1 || FEHLT="$FEHLT $b"; done
  if [ -n "$FEHLT" ]; then
    echo "   Hotspot: installiere Pakete ($FEHLT) …"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -q hostapd dnsmasq-base nftables iw iproute2 >/var/log/fundus-hotspot-setup.log 2>&1 \
      || echo "   ⚠ Hotspot-Pakete nicht installierbar – Protokoll: /var/log/fundus-hotspot-setup.log"
    # hostapd nicht als eigenen Dienst laufen lassen – das macht der Wächter.
    systemctl disable --now hostapd >/dev/null 2>&1 || true
  fi
fi

einheit() { local z=/etc/systemd/system/$1; [ "$(cat "$z" 2>/dev/null)" = "$2" ] && return 1; printf '%s\n' "$2" > "$z"; return 0; }
NEU=0
einheit fundus-hotspot.service "[Unit]
Description=Fundus: WLAN-Hotspot (Wächter)
After=network-online.target
[Service]
Environment=FUNDUS_DATA_DIR=$DATA_DIR
ExecStart=/bin/bash $WERKZEUGE/fundus-hotspot.sh
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target" && NEU=1
einheit fundus-hotspot-einrichtung.service "[Unit]
Description=Fundus: WLAN-Hotspot einrichten
[Service]
Type=oneshot
Environment=FUNDUS_DATA_DIR=$DATA_DIR
ExecStart=/bin/bash $WERKZEUGE/setup-hotspot.sh --auto" && NEU=1
einheit fundus-hotspot-konfig.path "[Unit]
Description=Fundus: WLAN-Hotspot-Einstellungen überwachen
[Path]
PathModified=$DATA_DIR/hotspot.json
Unit=fundus-hotspot-einrichtung.service
[Install]
WantedBy=multi-user.target" && NEU=1
if [ "$NEU" -eq 1 ]; then
  systemctl daemon-reload
  systemctl enable fundus-hotspot.service fundus-hotspot-konfig.path >/dev/null 2>&1
  systemctl restart fundus-hotspot.service 2>/dev/null
  systemctl start fundus-hotspot-konfig.path 2>/dev/null
  echo "   Hotspot-Dienste eingerichtet (Hotspot: $AKTIV)."
fi
systemctl is-active --quiet fundus-hotspot.service || systemctl start fundus-hotspot.service 2>/dev/null
exit 0

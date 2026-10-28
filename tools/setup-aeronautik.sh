#!/usr/bin/env bash
# Aeronautik (R845/R846): ADS-B-Empfang mit RTL-SDR-Stick (z. B. Terratec T Stick)
#
# Richtet readsb ein – den Decoder, der die Transpondertelegramme auf 1090 MHz
# empfängt und jede Sekunde /run/readsb/aircraft.json schreibt. Der Fundus-Node
# liest diese Datei (Seite "Aeronautik").
#
# Von Hand:            sudo bash setup-aeronautik.sh [breite länge]
# Beim Deploy (R846):  sudo bash setup-aeronautik.sh --auto
#
# Es wird der TATSÄCHLICHE Zustand geprüft, keine Merker-Datei: Treibersperre,
# readsb installiert und aktiv, Standort eingetragen und aktuell, DVB-Treiber
# nicht geladen. Ausgeführt wird nur, was fehlt. Ein Merker könnte lügen –
# etwa wenn readsb entfernt oder der Stick an einen anderen Pi gesteckt wurde.
#
# --auto: ohne Stick still beenden; Fehler nur melden, nie abbrechen – ein
# Problem mit dem Stick soll keinen Fundus-Deploy scheitern lassen.

# R848: --hintergrund – die eigentliche Einrichtung als eigener Dienst
# (systemd-run), damit der Deploy nicht minutenlang wartet. Der Fortschritt
# steht in $STATUS (für die Seite Aeronautik) und im Protokoll $LOG.
AUTO=0; HINTERGRUND=0
case "${1:-}" in
  --auto) AUTO=1; shift ;;
  --hintergrund) HINTERGRUND=1; AUTO=1; shift ;;
esac
LAT="${1:-}"; LON="${2:-}"
DATA_DIR="${FUNDUS_DATA_DIR:-/opt/fundus/data}"
SPERRE=/etc/modprobe.d/blacklist-rtl-sdr.conf
READSB_CFG=/etc/default/readsb
STATUS_DIR=/run/fundus-aeronautik
STATUS=$STATUS_DIR/status.json
LOG=/var/log/fundus-aeronautik-setup.log
DIENST=fundus-aeronautik-setup
START=$(date +%s)

# status <phase> <text> [fertig] [fehler] – als JSON für den Node (lesbar für alle)
status() {
  [ "$HINTERGRUND" -eq 1 ] || return 0
  mkdir -p "$STATUS_DIR"; chmod 755 "$STATUS_DIR"
  local letzte=""
  [ -f "$LOG" ] && letzte=$(grep -v '^[[:space:]]*$' "$LOG" | tail -1 | cut -c1-160)
  esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'; }
  printf '{"phase":"%s","text":"%s","seit":%s,"stand":%s,"fertig":%s,"fehler":%s,"letzte":"%s","protokoll":"%s"}\n' \
    "$(esc "$1")" "$(esc "$2")" "$START" "$(date +%s)" "${3:-false}" "${4:-false}" "$(esc "$letzte")" "$LOG" > "$STATUS.neu"
  chmod 644 "$STATUS.neu"; mv "$STATUS.neu" "$STATUS"
}

fertig()  { echo "   $*"; }
fehler()  {
  echo "   ✗ $*"
  status "fehler" "$*" true true
  if [ "$AUTO" -eq 1 ]; then echo "   (Deploy läuft weiter – Aeronautik bei Bedarf von Hand: sudo bash $0)"; exit 0; fi
  exit 1
}

[ "$(id -u)" -eq 0 ] || fehler "Bitte mit sudo ausführen."

# ── Stick vorhanden? ────────────────────────────────────────────────────────
if ! lsusb 2>/dev/null | grep -qiE "0bda:28(32|38)|RTL28[0-9]{2}|Terratec"; then
  if [ "$AUTO" -eq 1 ]; then echo "   Kein RTL-SDR-Stick – Aeronautik übersprungen."; exit 0; fi
  echo "Kein RTL-SDR-Stick gefunden. Steckt er? Alle USB-Geräte:"; lsusb; exit 1
fi
echo "   Stick: $(lsusb | grep -iE '0bda:28|RTL28|Terratec' | head -1 | cut -d' ' -f6-)"

# ── Standort ermitteln: Aufruf, sonst Node, sonst dessen Datei ──────────────
if [ -z "$LAT" ] || [ -z "$LON" ]; then
  ORT=$(curl -s --max-time 4 http://127.0.0.1:3000/api/v1/admin/location 2>/dev/null || true)
  [ -n "$ORT" ] || ORT=$(cat "$DATA_DIR/location.json" 2>/dev/null || true)
  LAT=$(echo "$ORT" | sed -n 's/.*"lat":[[:space:]]*\([-0-9.]*\).*/\1/p')
  LON=$(echo "$ORT" | sed -n 's/.*"lon":[[:space:]]*\([-0-9.]*\).*/\1/p')
fi
STANDORT_OK=0
if [ -n "$LAT" ] && [ -n "$LON" ] && [ "$LAT" != "0" ] && [ "$LON" != "0" ]; then STANDORT_OK=1; fi

# ── Zustand prüfen ──────────────────────────────────────────────────────────
BRAUCHT_SPERRE=0; BRAUCHT_ENTLADEN=0; BRAUCHT_READSB=0; BRAUCHT_DIENST=0; BRAUCHT_ORT=0
# R859: weitere Dekoder und die Umschaltung zwischen ihnen
BRAUCHT_VDL2=0; BRAUCHT_AIS=0; BRAUCHT_EINHEITEN=0
VDL2_BIN=${VDL2_BIN:-/usr/local/bin/dumpvdl2}; AIS_BIN=${AIS_BIN:-/usr/local/bin/AIS-catcher}
WERKZEUGE="$(dirname "$(readlink -f "$0")")"
MODUS=$(tr -dc 'a-z0-9' < "$DATA_DIR/funk-modus" 2>/dev/null); [ -n "$MODUS" ] || MODUS=adsb
[ -x "$VDL2_BIN" ] || BRAUCHT_VDL2=1
[ -x "$AIS_BIN" ] || BRAUCHT_AIS=1
[ -f "$SPERRE" ] && grep -q dvb_usb_rtl28xxu "$SPERRE" || BRAUCHT_SPERRE=1
lsmod 2>/dev/null | grep -q '^dvb_usb_rtl28xxu' && BRAUCHT_ENTLADEN=1
command -v readsb >/dev/null 2>&1 || BRAUCHT_READSB=1
if [ "$BRAUCHT_READSB" -eq 0 ]; then
  # Nur im Flugzeug-Modus muss readsb laufen – hört der Stick gerade auf
  # Schiffe oder Funknachrichten, ist ein gestoppter readsb richtig (R859).
  if [ "$MODUS" = adsb ]; then
    systemctl is-active --quiet readsb 2>/dev/null || BRAUCHT_DIENST=1
  fi
fi
if [ "$STANDORT_OK" -eq 1 ]; then
  IST_LAT=$(sed -n 's/.*--lat[ =]\([-0-9.]*\).*/\1/p' "$READSB_CFG" 2>/dev/null | head -1)
  IST_LON=$(sed -n 's/.*--lon[ =]\([-0-9.]*\).*/\1/p' "$READSB_CFG" 2>/dev/null | head -1)
  # Neu eintragen, wenn keiner da ist oder der Node inzwischen woanders steht
  # (Abweichung über ~100 m).
  if [ -z "$IST_LAT" ] || [ -z "$IST_LON" ] || \
     awk -v a="$IST_LAT" -v b="$LAT" -v c="$IST_LON" -v d="$LON" 'BEGIN{ x=a-b; y=c-d; if (x<0) x=-x; if (y<0) y=-y; exit !(x>0.001 || y>0.001) }'; then
    BRAUCHT_ORT=1
  fi
fi

# Units der Umschaltung: vorhanden und aktuell?
einheit() { # name inhalt – schreibt nur bei Abweichung; Rückgabe 0 = geändert
  local ziel=/etc/systemd/system/$1
  if [ "$(cat "$ziel" 2>/dev/null)" != "$2" ]; then printf '%s\n' "$2" > "$ziel"; return 0; fi
  return 1
}
E_VDL2="[Unit]
Description=Fundus Funk: VDL2-Funknachrichten (dumpvdl2)
After=network.target
[Service]
ExecStart=$VDL2_BIN --rtlsdr 0 --gain 40 --output decoded:json:udp:address=127.0.0.1,port=5555 136725000 136775000 136875000 136975000
Restart=on-failure
RestartSec=10"
E_AIS="[Unit]
Description=Fundus Funk: Schiffe (AIS-catcher)
After=network.target
[Service]
ExecStart=$AIS_BIN -d:0 -u 127.0.0.1 10110 -q
Restart=on-failure
RestartSec=10"
E_FUNK="[Unit]
Description=Fundus Funk: Modus des RTL-SDR-Sticks anwenden
After=network.target readsb.service
[Service]
Type=oneshot
Environment=FUNDUS_DATA_DIR=$DATA_DIR
ExecStart=/bin/bash $WERKZEUGE/funk-modus.sh
[Install]
WantedBy=multi-user.target"
E_PFAD="[Unit]
Description=Fundus Funk: Moduswechsel überwachen
[Path]
PathModified=$DATA_DIR/funk-modus
Unit=fundus-funk.service
[Install]
WantedBy=multi-user.target"
for n in fundus-vdl2.service fundus-ais.service fundus-funk.service fundus-funk.path; do
  case $n in fundus-vdl2.service) i="$E_VDL2";; fundus-ais.service) i="$E_AIS";; fundus-funk.service) i="$E_FUNK";; *) i="$E_PFAD";; esac
  [ "$(cat /etc/systemd/system/$n 2>/dev/null)" = "$i" ] || BRAUCHT_EINHEITEN=1
done
systemctl is-enabled --quiet fundus-funk.path 2>/dev/null || BRAUCHT_EINHEITEN=1

if [ $((BRAUCHT_SPERRE + BRAUCHT_ENTLADEN + BRAUCHT_READSB + BRAUCHT_DIENST + BRAUCHT_ORT + BRAUCHT_VDL2 + BRAUCHT_AIS + BRAUCHT_EINHEITEN)) -eq 0 ]; then
  N=$(grep -o '"hex"' /run/readsb/aircraft.json 2>/dev/null | wc -l)
  if [ "$MODUS" = adsb ]; then fertig "Aeronautik ist eingerichtet – nichts zu tun ($N Flugzeug(e) im Empfang)."
  else fertig "Empfänger eingerichtet – nichts zu tun (Modus: $MODUS)."; fi
  [ "$STANDORT_OK" -eq 1 ] || fertig "Hinweis: Standort fehlt – in Fundus unter Einstellungen auf der Karte setzen."
  exit 0
fi

# ── Lange Einrichtung nicht im Deploy abwarten (R848) ───────────────────────
if [ "$AUTO" -eq 1 ] && [ "$HINTERGRUND" -eq 0 ] && [ $((BRAUCHT_READSB + BRAUCHT_VDL2 + BRAUCHT_AIS)) -gt 0 ]; then
  if systemctl is-active --quiet "$DIENST" 2>/dev/null; then
    fertig "Einrichtung läuft bereits im Hintergrund – Fortschritt auf der Seite Aeronautik."
    exit 0
  fi
  : > "$LOG"; chmod 644 "$LOG"
  HINTERGRUND=1; status "start" "Einrichtung gestartet"; HINTERGRUND=0
  if command -v systemd-run >/dev/null 2>&1; then
    systemctl reset-failed "$DIENST" 2>/dev/null || true
    systemd-run --unit="$DIENST" --description="Fundus Aeronautik: readsb einrichten" --collect \
      --setenv=FUNDUS_DATA_DIR="$DATA_DIR" /bin/bash "$(readlink -f "$0")" --hintergrund $LAT $LON >/dev/null 2>&1 \
      || fehler "Hintergrund-Einrichtung konnte nicht gestartet werden."
  else
    FUNDUS_DATA_DIR="$DATA_DIR" setsid nohup /bin/bash "$(readlink -f "$0")" --hintergrund $LAT $LON >/dev/null 2>&1 &
  fi
  fertig "readsb wird im Hintergrund installiert und übersetzt (Pi 4: ~5 min, Pi 3: bis ~20 min)."
  fertig "Der Deploy läuft weiter. Fortschritt: Seite Aeronautik oder  tail -f $LOG"
  exit 0
fi
[ "$HINTERGRUND" -eq 1 ] && exec >> "$LOG" 2>&1   # im Hintergrund alles ins Protokoll

# ── Nur ausführen, was fehlt ────────────────────────────────────────────────
status "treiber" "DVB-T-Treiber sperren"
if [ "$BRAUCHT_SPERRE" -eq 1 ]; then
  mkdir -p "$(dirname "$SPERRE")"
  if printf '%s\n' "# Aeronautik: Stick für SDR-Software statt DVB-T" \
       "blacklist dvb_usb_rtl28xxu" "blacklist rtl2832" "blacklist rtl2830" "blacklist rtl2832_sdr" > "$SPERRE" \
     && grep -q dvb_usb_rtl28xxu "$SPERRE"; then
    fertig "DVB-T-Treiber gesperrt."
  else
    fehler "Treibersperre konnte nicht geschrieben werden ($SPERRE)."
  fi
fi
if [ "$BRAUCHT_ENTLADEN" -eq 1 ] || [ "$BRAUCHT_SPERRE" -eq 1 ]; then
  for m in rtl2832_sdr dvb_usb_rtl28xxu rtl2832 rtl2830; do modprobe -r "$m" 2>/dev/null || true; done
  if lsmod | grep -q '^dvb_usb_rtl28xxu'; then
    fertig "DVB-T-Treiber noch geladen – nach einem Neustart des Pi frei."
  else
    fertig "DVB-T-Treiber entladen."
  fi
fi
if [ "$BRAUCHT_READSB" -eq 1 ]; then
  fertig "readsb wird installiert (wiedehopf/adsb-scripts, dauert einige Minuten) …"
  status "installieren" "readsb wird installiert und auf dem Pi übersetzt"
  # Während des langen Übersetzens alle 5 s den Status auffrischen – die
  # Seite zeigt die jeweils letzte Protokollzeile, man sieht: es arbeitet.
  ( while sleep 5; do status "installieren" "readsb wird installiert und auf dem Pi übersetzt"; done ) &
  WAECHTER=$!
  INSTALLER=$(wget -qO - https://github.com/wiedehopf/adsb-scripts/raw/master/readsb-install.sh) \
    || { kill $WAECHTER 2>/dev/null; fehler "Installationsskript nicht ladbar (GitHub erreichbar?)"; }
  if [ "$HINTERGRUND" -eq 1 ]; then bash -c "$INSTALLER"; else bash -c "$INSTALLER" >> "$LOG" 2>&1; fi
  ERGEBNIS=$?
  kill $WAECHTER 2>/dev/null; wait $WAECHTER 2>/dev/null
  [ "$ERGEBNIS" -eq 0 ] || fehler "Installation von readsb fehlgeschlagen – Protokoll: $LOG"
  command -v readsb >/dev/null 2>&1 || fehler "readsb nach der Installation nicht gefunden – Protokoll: $LOG"
  fertig "readsb installiert."
  BRAUCHT_DIENST=1
  [ "$STANDORT_OK" -eq 1 ] && BRAUCHT_ORT=1
fi
# ── R859: Dekoder für Funknachrichten und Schiffe ──────────────────────────
bauen() { # titel phase befehle…  (mit Lebenszeichen wie bei readsb)
  local titel="$1" phase="$2"; shift 2
  fertig "$titel wird übersetzt (dauert einige Minuten) …"
  status "$phase" "$titel wird auf dem Pi übersetzt"
  ( while sleep 5; do status "$phase" "$titel wird auf dem Pi übersetzt"; done ) &
  local w=$!
  if [ "$HINTERGRUND" -eq 1 ]; then bash -c "$*"; else bash -c "$*" >> "$LOG" 2>&1; fi
  local e=$?
  kill $w 2>/dev/null; wait $w 2>/dev/null
  return $e
}
BAU=/tmp/fundus-funk-bau
if [ "$BRAUCHT_VDL2" -eq 1 ] || [ "$BRAUCHT_AIS" -eq 1 ]; then
  status "pakete" "Bauwerkzeuge und Bibliotheken installieren"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -q build-essential cmake git pkg-config librtlsdr-dev \
    libglib2.0-dev libjansson-dev libxml2-dev zlib1g-dev libsqlite3-dev libzmq3-dev >> "$LOG" 2>&1 \
    || fehler "Pakete für die Dekoder konnten nicht installiert werden – Protokoll: $LOG"
fi
if [ "$BRAUCHT_VDL2" -eq 1 ]; then
  bauen "dumpvdl2 (Funknachrichten)" "vdl2" "set -e; rm -rf $BAU; mkdir -p $BAU; cd $BAU
    git clone --depth 1 https://github.com/szpajder/libacars.git
    cmake -S libacars -B libacars/build -DCMAKE_BUILD_TYPE=Release && cmake --build libacars/build -j2 && cmake --install libacars/build && ldconfig
    git clone --depth 1 https://github.com/szpajder/dumpvdl2.git
    cmake -S dumpvdl2 -B dumpvdl2/build -DCMAKE_BUILD_TYPE=Release -DRTLSDR=TRUE && cmake --build dumpvdl2/build -j2
    install -m 755 dumpvdl2/build/src/dumpvdl2 $VDL2_BIN" \
    || fehler "dumpvdl2 ließ sich nicht übersetzen – Protokoll: $LOG"
  fertig "dumpvdl2 installiert."
fi
if [ "$BRAUCHT_AIS" -eq 1 ]; then
  bauen "AIS-catcher (Schiffe)" "ais" "set -e; mkdir -p $BAU; cd $BAU; rm -rf AIS-catcher
    git clone --depth 1 https://github.com/jvde-github/AIS-catcher.git
    cmake -S AIS-catcher -B AIS-catcher/build -DCMAKE_BUILD_TYPE=Release && cmake --build AIS-catcher/build -j2
    install -m 755 AIS-catcher/build/AIS-catcher $AIS_BIN" \
    || fehler "AIS-catcher ließ sich nicht übersetzen – Protokoll: $LOG"
  fertig "AIS-catcher installiert."
fi
rm -rf "$BAU"

status "standort" "Standort eintragen"
if [ "$BRAUCHT_ORT" -eq 1 ]; then
  if readsb-set-location "$LAT" "$LON" >/dev/null 2>&1; then
    fertig "Standort eingetragen: $LAT, $LON"
    BRAUCHT_DIENST=1
  else
    fertig "Standort konnte nicht eingetragen werden (readsb-set-location)."
  fi
elif [ "$STANDORT_OK" -eq 0 ]; then
  fertig "Kein Standort bekannt – in Fundus unter Einstellungen auf der Karte setzen;"
  fertig "beim nächsten Deploy wird er dann automatisch eingetragen."
fi
status "dienst" "Empfänger starten"
GEAENDERT=0
einheit fundus-vdl2.service "$E_VDL2" && GEAENDERT=1
einheit fundus-ais.service "$E_AIS" && GEAENDERT=1
einheit fundus-funk.service "$E_FUNK" && GEAENDERT=1
einheit fundus-funk.path "$E_PFAD" && GEAENDERT=1
if [ "$GEAENDERT" -eq 1 ] || [ "$BRAUCHT_EINHEITEN" -eq 1 ]; then
  systemctl daemon-reload
  systemctl enable fundus-funk.service fundus-funk.path >/dev/null 2>&1
  systemctl start fundus-funk.path 2>/dev/null
  fertig "Umschaltung eingerichtet (Flugzeuge / Funknachrichten / Schiffe)."
fi
[ -f "$DATA_DIR/funk-modus" ] || { echo adsb > "$DATA_DIR/funk-modus"; chown --reference="$DATA_DIR" "$DATA_DIR/funk-modus" 2>/dev/null; chmod 664 "$DATA_DIR/funk-modus"; }
# readsb bleibt beim Systemstart aktiv; fundus-funk.service hält ihn an, wenn
# ein anderer Modus gewählt ist. Angewandt wird der gewählte Modus jetzt.
systemctl enable readsb >/dev/null 2>&1 || true
if [ "$BRAUCHT_DIENST" -eq 1 ] || [ "$GEAENDERT" -eq 1 ] || [ "$BRAUCHT_VDL2" -eq 1 ] || [ "$BRAUCHT_AIS" -eq 1 ]; then
  FUNDUS_DATA_DIR="$DATA_DIR" bash "$WERKZEUGE/funk-modus.sh" | sed 's/^/   /'
fi

if [ "$MODUS" != adsb ]; then
  fertig "Empfänger bereit – Modus: $MODUS."
  status "fertig" "Empfänger bereit – Modus: $MODUS" true false
elif [ -s /run/readsb/aircraft.json ]; then
  N=$(grep -o '"hex"' /run/readsb/aircraft.json | wc -l)
  fertig "Aeronautik bereit: readsb läuft, gerade $N Flugzeug(e) im Empfang."
  status "fertig" "Aeronautik bereit – $N Flugzeug(e) im Empfang" true false
else
  status "fertig" "readsb läuft, hat aber noch nichts empfangen" true false
  fertig "readsb läuft, hat aber noch nichts geschrieben – systemctl status readsb"
fi
if [ "$AUTO" -eq 0 ]; then
  echo
  echo "Tipp: Die DVB-T-Antenne reicht oft nur 30–50 km. Ein Viertelwellenstrahler"
  echo "mit 6,9 cm über einer kleinen Massefläche, hoch und frei montiert, bringt"
  echo "typisch 150–250 km."
fi
exit 0

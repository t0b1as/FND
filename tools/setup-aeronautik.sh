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

AUTO=0
if [ "${1:-}" = "--auto" ]; then AUTO=1; shift; fi
LAT="${1:-}"; LON="${2:-}"
DATA_DIR="${FUNDUS_DATA_DIR:-/opt/fundus/data}"
SPERRE=/etc/modprobe.d/blacklist-rtl-sdr.conf
READSB_CFG=/etc/default/readsb

fertig()  { echo "   $*"; }
fehler()  {
  echo "   ✗ $*"
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
[ -f "$SPERRE" ] && grep -q dvb_usb_rtl28xxu "$SPERRE" || BRAUCHT_SPERRE=1
lsmod 2>/dev/null | grep -q '^dvb_usb_rtl28xxu' && BRAUCHT_ENTLADEN=1
command -v readsb >/dev/null 2>&1 || BRAUCHT_READSB=1
if [ "$BRAUCHT_READSB" -eq 0 ]; then
  systemctl is-enabled --quiet readsb 2>/dev/null && systemctl is-active --quiet readsb 2>/dev/null || BRAUCHT_DIENST=1
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

if [ $((BRAUCHT_SPERRE + BRAUCHT_ENTLADEN + BRAUCHT_READSB + BRAUCHT_DIENST + BRAUCHT_ORT)) -eq 0 ]; then
  N=$(grep -o '"hex"' /run/readsb/aircraft.json 2>/dev/null | wc -l)
  fertig "Aeronautik ist eingerichtet – nichts zu tun ($N Flugzeug(e) im Empfang)."
  [ "$STANDORT_OK" -eq 1 ] || fertig "Hinweis: Standort fehlt – in Fundus unter Einstellungen auf der Karte setzen."
  exit 0
fi

# ── Nur ausführen, was fehlt ────────────────────────────────────────────────
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
  if ! bash -c "$(wget -qO - https://github.com/wiedehopf/adsb-scripts/raw/master/readsb-install.sh)" > /tmp/readsb-install.log 2>&1; then
    fehler "Installation von readsb fehlgeschlagen – Protokoll: /tmp/readsb-install.log"
  fi
  command -v readsb >/dev/null 2>&1 || fehler "readsb nach der Installation nicht gefunden – Protokoll: /tmp/readsb-install.log"
  fertig "readsb installiert."
  BRAUCHT_DIENST=1
  [ "$STANDORT_OK" -eq 1 ] && BRAUCHT_ORT=1
fi
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
if [ "$BRAUCHT_DIENST" -eq 1 ]; then
  systemctl enable readsb >/dev/null 2>&1 || true
  systemctl restart readsb || fehler "readsb startet nicht – systemctl status readsb"
  sleep 5
fi

if [ -s /run/readsb/aircraft.json ]; then
  N=$(grep -o '"hex"' /run/readsb/aircraft.json | wc -l)
  fertig "Aeronautik bereit: readsb läuft, gerade $N Flugzeug(e) im Empfang."
else
  fertig "readsb läuft, hat aber noch nichts geschrieben – systemctl status readsb"
fi
if [ "$AUTO" -eq 0 ]; then
  echo
  echo "Tipp: Die DVB-T-Antenne reicht oft nur 30–50 km. Ein Viertelwellenstrahler"
  echo "mit 6,9 cm über einer kleinen Massefläche, hoch und frei montiert, bringt"
  echo "typisch 150–250 km."
fi
exit 0

#!/usr/bin/env bash
# WLAN-Hotspot (R865) – Wächter mit Root-Rechten (fundus-hotspot.service).
#
# Der Fundus-Node RECHNET (Stufen, Zahlungen, wer wie lange freigeschaltet
# ist) und schreibt zwei Dateien; dieser Wächter SETZT nur UM:
#   <DataDir>/hotspot.json            Einstellungen (aktiv, SSID, Kanal)
#   <DataDir>/hotspot-zugaenge.json   freigeschaltete Geräte (MAC, Stufe, Mbit/s, bis)
# Status für die Seite: /run/fundus-hotspot/status.json
#
# Der Hotspot läuft NUR, wenn der Pi über eine ANDERE Schnittstelle online ist
# (eth0 oder ein zweiter WLAN-Adapter). Die Schnittstelle der eigenen
# Verbindung wird NIE angefasst – nur die Weiterleitung dorthin (NAT).
#
# Ablauf für ein Gerät im Hotspot:
#   nicht freigeschaltet → nur DNS, DHCP und die Anmeldeseite (Port 3088) und
#   PayPal; jede http-Anfrage landet auf der Anmeldeseite (so erkennen
#   Telefone den Hotspot wie im Hotel)
#   freigeschaltet → ins Netz, gedrosselt auf die Mbit/s seiner Stufe

DATA="${FUNDUS_DATA_DIR:-/opt/fundus/data}"
RUN=/run/fundus-hotspot
NETZ=10.42.0
GW=$NETZ.1
PORTAL=3088
TABELLE=fundus_hotspot
IFB=ifb-fhs
SYSNET="${FUNDUS_SYSNET:-/sys/class/net}"   # vorgebbar für Tests
mkdir -p "$RUN"; chmod 755 "$RUN"

log() { echo "$(date +%H:%M:%S) $*"; }

status() { # laeuft grund [ap] [uplink] [geraete]
  printf '{"laeuft":%s,"grund":"%s","ap":"%s","uplink":"%s","geraete":%s,"stand":%s}\n' \
    "$1" "$2" "${3:-}" "${4:-}" "${5:-0}" "$(date +%s)" > "$RUN/status.json.neu"
  chmod 644 "$RUN/status.json.neu"; mv "$RUN/status.json.neu" "$RUN/status.json"
}

# Einstellungen lesen (python3 ist auf Raspberry Pi OS immer da)
einstellungen() {
  python3 - "$DATA/hotspot.json" << 'PY' 2>/dev/null
import json, re, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    d = {}
ssid = re.sub(r'[^\w .-]', '', str(d.get("ssid") or "Fundus WLAN"))[:32] or "Fundus WLAN"
kanal = int(d.get("kanal") or 6)
if kanal not in (1, 6, 11): kanal = 6
print(("an" if d.get("aktiv") else "aus"), kanal, ("an" if d.get("mitbenutzen") else "aus"), ssid)
PY
}

# Freigeschaltete Geräte: "mac down up" je Zeile, nur gültige und unabgelaufene
zugaenge() {
  python3 - "$DATA/hotspot-zugaenge.json" << 'PY' 2>/dev/null
import json, re, sys, time
try:
    liste = json.load(open(sys.argv[1]))
except Exception:
    liste = []
jetzt = time.time()
for z in liste:
    mac = str(z.get("mac", "")).lower()
    if not re.fullmatch(r'([0-9a-f]{2}:){5}[0-9a-f]{2}', mac) or float(z.get("bis", 0)) <= jetzt:
        continue
    d = max(1, min(1000, int(float(z.get("down", 1)))))
    u = max(1, min(1000, int(float(z.get("up", 1)))))
    print(mac, d, u)
PY
}

uplink() { ip -4 route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}'; }

# Ein WLAN-Adapter, der NICHT die eigene Verbindung ist und Zugangspunkt kann
ap_schnittstelle() {
  local up="$1" i phy
  for i in $(ls "$SYSNET" 2>/dev/null | grep '^wlan' | sort); do
    [ "$i" = "$up" ] && continue
    phy=$(cat "$SYSNET/$i/phy80211/name" 2>/dev/null) || continue
    iw phy "$phy" info 2>/dev/null | grep -q '^\s*\* AP$' && { echo "$i"; return; }
  done
}

# ── R866: eingebautes WLAN mitbenutzen (Verbindung UND Hotspot) ─────────────
# Der Chip kann gleichzeitig verbunden sein und ein Netz aufspannen – aber nur
# auf EINEM Kanal. Der Hotspot läuft deshalb auf einer zweiten, virtuellen
# Schnittstelle (uap0) auf dem Kanal des Routers. wlan0 selbst wird dabei nur
# ausgelesen, nie umkonfiguriert.
VIRT=uap0
mitbenutzbar() { # Schnittstelle der Verbindung → Erfolg, wenn AP + Client gleichzeitig gehen
  local phy; phy=$(cat "$SYSNET/$1/phy80211/name" 2>/dev/null) || return 1
  iw phy "$phy" info 2>/dev/null | awk '/valid interface combinations/{f=1;next} f && /^[[:space:]]*\*/ {print} f && !/^[[:space:]]/ {f=0}' \
    | grep -E 'managed' | grep -qE '(^|[^A-Za-z])AP([^A-Za-z]|$)'
}
kanal_von() { iw dev "$1" info 2>/dev/null | awk '$1=="channel" {print $2; exit}'; }
kanal_tauglich() { # 2,4 GHz 1–13 und 5 GHz 36–48; Kanäle mit Radarerkennung kann der Pi als Hotspot nicht
  local k="$1"; [ -n "$k" ] || return 1
  { [ "$k" -ge 1 ] && [ "$k" -le 13 ]; } || { [ "$k" -ge 36 ] && [ "$k" -le 48 ]; }
}
GETEILT=0; FEHL=0; SPERRE_BIS=0; GESPERRT_FUER=""; PRUEF_BIS=0; PING_FEHL=0; GESTARTET_KANAL=""

LAEUFT=0; AP=""; UP=""; ZUSTAND=""

starten() {
  AP="$1"; UP="$2"; local kanal="$3" ssid="$4"
  log "starte Hotspot: $AP (Verbindung über $UP), Kanal $kanal, „$ssid“"
  command -v nmcli >/dev/null && nmcli device set "$AP" managed no 2>/dev/null
  ip addr flush dev "$AP"; ip addr add "$GW/24" dev "$AP"; ip link set "$AP" up
  sysctl -qw "net.ipv6.conf.$AP.disable_ipv6=1" 2>/dev/null
  cat > "$RUN/hostapd.conf" << EOF
interface=$AP
driver=nl80211
ssid=$ssid
utf8_ssid=1
hw_mode=$( [ "$kanal" -gt 14 ] && echo a || echo g )
channel=$kanal
country_code=DE
ieee80211d=1
ieee80211n=1
wmm_enabled=1
auth_algs=1
ignore_broadcast_ssid=0
EOF
  # Anmeldeseite und PayPal sind auch vor der Freischaltung erreichbar.
  cat > "$RUN/dnsmasq.conf" << EOF
interface=$AP
bind-interfaces
except-interface=lo
dhcp-range=$NETZ.10,$NETZ.250,255.255.255.0,12h
dhcp-option=3,$GW
dhcp-option=6,$GW
dhcp-option=114,http://$GW:$PORTAL/
dhcp-leasefile=$RUN/leases
nftset=/paypal.com/paypalobjects.com/braintreegateway.com/4#inet#$TABELLE#offen
EOF
  # nftset ist nicht in jedem dnsmasq einkompiliert. Fehlt es, die PayPal-
  # Adressen stattdessen selbst regelmäßig auflösen und freigeben.
  PAYPAL_SELBST=0
  if ! dnsmasq --test --conf-file="$RUN/dnsmasq.conf" >/dev/null 2>&1; then
    sed -i '/^nftset=/d' "$RUN/dnsmasq.conf"; PAYPAL_SELBST=1
    log "dnsmasq ohne nftset – PayPal-Adressen werden selbst aufgelöst"
  fi
  hostapd -B -P "$RUN/hostapd.pid" "$RUN/hostapd.conf" > "$RUN/hostapd.log" 2>&1 \
    || { log "hostapd startet nicht: $(tail -2 "$RUN/hostapd.log")"; stoppen; status false "WLAN-Zugangspunkt startet nicht" "$AP" "$UP"; return 1; }
  # R867: ERST die Sperrregeln, DANN die Weiterleitung – und nur, wenn die
  # Regeln wirklich geladen sind. Vorher wurde die Weiterleitung zuerst
  # eingeschaltet und das Laden nicht geprüft: Scheiterten die Regeln, kamen
  # Gäste ohne Anmeldung ins Netz (fail-open).
  nft delete table inet "$TABELLE" 2>/dev/null
  if ! nft -f - > "$RUN/nft.log" 2>&1 << EOF
table inet $TABELLE {
  set frei { type ipv4_addr; }
  set offen { type ipv4_addr; flags timeout; timeout 1h; }
  chain vor { type nat hook prerouting priority dstnat; policy accept;
    iifname "$AP" ip saddr != @frei tcp dport 80 dnat ip to $GW:$PORTAL
  }
  chain nach { type nat hook postrouting priority srcnat; policy accept;
    oifname "$UP" ip saddr $NETZ.0/24 masquerade
  }
  chain rein { type filter hook input priority filter; policy accept;
    iifname "$AP" ct state established,related accept
    iifname "$AP" udp dport { 53, 67 } accept
    iifname "$AP" tcp dport { 53, $PORTAL } accept
    iifname "$AP" drop
  }
  chain weiter { type filter hook forward priority filter; policy accept;
    oifname "$AP" ct state established,related accept
    iifname "$AP" meta nfproto ipv6 drop
    iifname "$AP" ip saddr @frei oifname "$UP" accept
    iifname "$AP" ip daddr @offen tcp dport 443 oifname "$UP" accept
    iifname "$AP" reject with icmpx type admin-prohibited
    oifname "$AP" drop
  }
}
EOF
  then
    log "Sperrregeln nicht ladbar: $(tr '\n' ' ' < "$RUN/nft.log" | cut -c1-300)"
    FW_FEHLER="$(tr '\n' ' ' < "$RUN/nft.log" | tr -d '"\\' | cut -c1-200)"
    stoppen; status false "Sperrregeln nicht ladbar – Hotspot bleibt aus: $FW_FEHLER" "$1" "$2"; return 1
  fi
  if ! nft list chain inet "$TABELLE" weiter >/dev/null 2>&1; then
    stoppen; status false "Sperrregeln nach dem Laden nicht vorhanden – Hotspot bleibt aus" "$1" "$2"; return 1
  fi
  sysctl -qw net.ipv4.ip_forward=1
  dnsmasq --conf-file="$RUN/dnsmasq.conf" --pid-file="$RUN/dnsmasq.pid" \
    || { log "dnsmasq startet nicht"; stoppen; status false "Adressvergabe (dnsmasq) startet nicht" "$AP" "$UP"; return 1; }
  LAEUFT=1; ZUSTAND=""
  return 0
}

stoppen() {
  [ -f "$RUN/hostapd.pid" ] && kill "$(cat "$RUN/hostapd.pid")" 2>/dev/null
  [ -f "$RUN/dnsmasq.pid" ] && kill "$(cat "$RUN/dnsmasq.pid")" 2>/dev/null
  rm -f "$RUN/hostapd.pid" "$RUN/dnsmasq.pid"
  nft delete table inet "$TABELLE" 2>/dev/null
  if [ -n "$AP" ]; then
    tc qdisc del dev "$AP" root 2>/dev/null; tc qdisc del dev "$AP" ingress 2>/dev/null
    ip addr flush dev "$AP" 2>/dev/null
    command -v nmcli >/dev/null && nmcli device set "$AP" managed yes 2>/dev/null
  fi
  ip link del "$IFB" 2>/dev/null
  # Nur die selbst angelegte virtuelle Schnittstelle entfernen – nie wlan0.
  [ "$AP" = "$VIRT" ] && iw dev "$VIRT" del 2>/dev/null
  [ "$LAEUFT" -eq 1 ] && log "Hotspot gestoppt"
  LAEUFT=0; AP=""; UP=""; ZUSTAND=""; GETEILT=0
}

# Freischaltungen und Drosselung abgleichen – nur bei Änderung neu aufbauen.
abgleichen() {
  local liste neu ip mac d u n=10 geraete=0
  liste=$(zugaenge)
  # MAC → aktuelle Adresse aus den Leases dieser Adressvergabe
  neu=""
  while read -r mac d u; do
    [ -z "$mac" ] && continue
    ip=$(awk -v m="$mac" 'tolower($2)==m {print $3; exit}' "$RUN/leases" 2>/dev/null)
    [ -n "$ip" ] && neu="$neu$ip $d $u
"
  done <<< "$liste"
  if [ "${PAYPAL_SELBST:-0}" -eq 1 ]; then
    for h in www.paypal.com www.paypalobjects.com c.paypal.com t.paypal.com api-m.paypal.com \
             www.sandbox.paypal.com api-m.sandbox.paypal.com b.stats.paypal.com; do
      for ip in $(getent ahostsv4 "$h" 2>/dev/null | awk '{print $1}' | sort -u); do
        nft add element inet "$TABELLE" offen "{ $ip }" 2>/dev/null
      done
    done
  fi
  [ "$neu" = "$ZUSTAND" ] && return
  ZUSTAND="$neu"
  nft flush set inet "$TABELLE" frei 2>/dev/null
  # Drosselung neu: hinunter auf dem Hotspot, hinauf über ein ifb-Gerät
  tc qdisc del dev "$AP" root 2>/dev/null; tc qdisc del dev "$AP" ingress 2>/dev/null
  ip link del "$IFB" 2>/dev/null
  tc qdisc add dev "$AP" root handle 1: htb default 9
  tc class add dev "$AP" parent 1: classid 1:9 htb rate 2mbit ceil 2mbit   # Anmeldeseite
  ip link add "$IFB" type ifb 2>/dev/null; ip link set "$IFB" up
  tc qdisc add dev "$AP" handle ffff: ingress
  tc filter add dev "$AP" parent ffff: protocol ip matchall action mirred egress redirect dev "$IFB"
  tc qdisc add dev "$IFB" root handle 1: htb default 9
  tc class add dev "$IFB" parent 1: classid 1:9 htb rate 2mbit ceil 2mbit
  while read -r ip d u; do
    [ -z "$ip" ] && continue
    nft add element inet "$TABELLE" frei "{ $ip }"
    tc class add dev "$AP" parent 1: classid "1:$n" htb rate "${d}mbit" ceil "${d}mbit"
    tc filter add dev "$AP" parent 1: protocol ip prio 1 u32 match ip dst "$ip/32" flowid "1:$n"
    tc class add dev "$IFB" parent 1: classid "1:$n" htb rate "${u}mbit" ceil "${u}mbit"
    tc filter add dev "$IFB" parent 1: protocol ip prio 1 u32 match ip src "$ip/32" flowid "1:$n"
    n=$((n + 1)); geraete=$((geraete + 1))
  done <<< "$ZUSTAND"
  log "Freischaltungen: $geraete Gerät(e)"
}

trap 'stoppen; status false "Dienst beendet"; exit 0' TERM INT
status false "startet"
while true; do
  read -r AKTIV KANAL MITBENUTZEN SSID <<< "$(einstellungen)"
  KONF_HASH=$(md5sum "$DATA/hotspot.json" 2>/dev/null | cut -c1-12)
  # Geänderte Einstellungen heben eine Sperre nach Fehlschlägen auf.
  [ -n "$GESPERRT_FUER" ] && [ "$GESPERRT_FUER" != "$KONF_HASH" ] && { GESPERRT_FUER=""; FEHL=0; SPERRE_BIS=0; }
  WILL_KANAL="$KANAL"; TEILEN=0
  [ -n "$AKTIV" ] || AKTIV=aus
  U=$(uplink)
  A=""; [ -n "$U" ] && A=$(ap_schnittstelle "$U")
  GRUND=""
  FEHLT=""; for b in hostapd dnsmasq nft iw tc; do command -v "$b" >/dev/null 2>&1 || FEHLT="$FEHLT $b"; done
  if [ "$AKTIV" != an ]; then GRUND="ausgeschaltet"
  elif [ -n "$FEHLT" ]; then GRUND="Pakete werden installiert ($FEHLT) – einen Moment"
  elif [ -z "$U" ]; then GRUND="keine Internetverbindung"
  elif ! { [ "${U#eth}" != "$U" ] || [ "${U#wlan}" != "$U" ] || [ "${U#end}" != "$U" ] || [ "${U#enx}" != "$U" ]; }; then GRUND="Verbindung über $U wird nicht unterstützt"
  elif [ -z "$A" ]; then
    # Kein freier Adapter: eingebautes WLAN mitbenutzen, falls erlaubt und möglich.
    UK=$(kanal_von "$U")
    if [ "$MITBENUTZEN" != an ] || [ "${U#wlan}" = "$U" ]; then
      GRUND="kein freier WLAN-Adapter – die Verbindung läuft über $U; für den Hotspot braucht es Kabel, einen zweiten WLAN-Adapter oder die Option „eingebautes WLAN mitbenutzen“"
    elif [ -n "$GESPERRT_FUER" ]; then
      GRUND="Mitbenutzen nach 3 Fehlschlägen abgeschaltet – die Verbindung des Pi riss jedes Mal ab. Zum erneuten Versuch die Option aus- und wieder einschalten."
    elif [ "$(date +%s)" -lt "$SPERRE_BIS" ]; then
      GRUND="Mitbenutzen pausiert nach einem Fehlschlag – neuer Versuch um $(date -d "@$SPERRE_BIS" +%H:%M)"
    elif ! mitbenutzbar "$U"; then
      GRUND="der WLAN-Chip kann nicht gleichzeitig verbunden sein und einen Hotspot anbieten"
    elif ! kanal_tauglich "$UK"; then
      GRUND="dein Router funkt auf Kanal ${UK:-?} – dort kann der Pi keinen Hotspot betreiben (Radarerkennung). Router auf 2,4 GHz oder Kanal 36–48 stellen."
    else
      TEILEN=1; WILL_KANAL="$UK"; A="$VIRT"
    fi
  fi
  if [ -n "$GRUND" ]; then
    [ "$LAEUFT" -eq 1 ] && stoppen
    status false "$GRUND" "" "$U"
  else
    # Läuft er schon, aber Adapter, Verbindung oder Einstellungen haben sich geändert? Neu starten.
    if [ "$LAEUFT" -eq 1 ] && { [ "$A" != "$AP" ] || [ "$U" != "$UP" ] || [ "$KANAL $MITBENUTZEN $SSID" != "$(cat "$RUN/gestartet" 2>/dev/null)" ]; }; then
      stoppen
    fi
    if [ "$LAEUFT" -eq 0 ]; then
      if [ "$TEILEN" -eq 1 ]; then
        iw dev "$VIRT" del 2>/dev/null
        if ! iw dev "$U" interface add "$VIRT" type __ap 2>/dev/null; then
          log "virtuelle Schnittstelle nicht anlegbar"; status false "der WLAN-Chip lehnt die zweite Schnittstelle ab" "" "$U"; sleep 5; continue
        fi
      fi
      if starten "$A" "$U" "$WILL_KANAL" "$SSID"; then
        echo "$KANAL $MITBENUTZEN $SSID" > "$RUN/gestartet"
        GETEILT=$TEILEN; GESTARTET_KANAL="$WILL_KANAL"
        [ "$TEILEN" -eq 1 ] && { PRUEF_BIS=$(( $(date +%s) + 30 )); PING_FEHL=0; log "geteilt mit $U auf Kanal $WILL_KANAL – prüfe, ob die Verbindung hält"; }
      fi
    fi
    # Rückfallsicherung beim Mitbenutzen: erreicht der Pi seinen Router nicht
    # mehr, sofort abbauen – sonst wäre er ohne Kabel nicht mehr erreichbar.
    if [ "$LAEUFT" -eq 1 ] && [ "$GETEILT" -eq 1 ]; then
      ROUTER=$(ip -4 route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="via"){print $(i+1); exit}}')
      if [ -n "$ROUTER" ] && ping -c1 -W2 -I "$UP" "$ROUTER" >/dev/null 2>&1; then PING_FEHL=0
      else PING_FEHL=$((PING_FEHL + 1)); fi
      if [ "$PING_FEHL" -ge 2 ]; then
        stoppen; FEHL=$((FEHL + 1)); SPERRE_BIS=$(( $(date +%s) + 600 ))
        log "Verbindung des Pi gestört – Hotspot abgebaut (Fehlschlag $FEHL von 3)"
        [ "$FEHL" -ge 3 ] && GESPERRT_FUER="$KONF_HASH"
        status false "Verbindung des Pi gestört – Hotspot abgebaut, Fehlschlag $FEHL von 3" "" "$U"
        sleep 5; continue
      fi
      # Eine Zeit lang stabil? Zähler zurücksetzen.
      [ "$(date +%s)" -ge "$PRUEF_BIS" ] && [ "$PING_FEHL" -eq 0 ] && FEHL=0
      # Router hat den Kanal gewechselt? Hotspot zieht nach.
      NK=$(kanal_von "$UP")
      if [ -n "$NK" ] && [ "$NK" != "$GESTARTET_KANAL" ]; then log "Router wechselt auf Kanal $NK – Hotspot zieht nach"; stoppen; fi
    fi
    if [ "$LAEUFT" -eq 1 ]; then
      if ! nft list chain inet "$TABELLE" weiter >/dev/null 2>&1; then
        # Jemand hat die Firewall neu geladen und unsere Tabelle gelöscht:
        # sofort abbauen statt Gäste ungeschützt weiterzuleiten.
        log "Sperrregeln verschwunden – Hotspot abgebaut"; stoppen
        status false "Sperrregeln wurden von außen gelöscht (Firewall neu geladen?) – Hotspot neu gestartet" "" "$U"
      elif [ -f "$RUN/hostapd.pid" ] && kill -0 "$(cat "$RUN/hostapd.pid")" 2>/dev/null; then
        abgleichen
        status true "$( [ "$GETEILT" -eq 1 ] && echo "läuft – teilt das eingebaute WLAN mit der Verbindung, Kanal $GESTARTET_KANAL" || echo "läuft")" "$AP" "$UP" "$(printf '%s' "$ZUSTAND" | grep -c .)"
      else
        log "hostapd beendet – neu starten"; stoppen
      fi
    fi
  fi
  RUNDE=$((${RUNDE:-0} + 1))
  [ -n "$FUNDUS_HOTSPOT_RUNDEN" ] && [ "$RUNDE" -ge "$FUNDUS_HOTSPOT_RUNDEN" ] && break   # nur für Tests
  sleep 5
done

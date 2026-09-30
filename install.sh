#!/bin/bash
# =============================================================================
#  Fundus – Installation auf einem Raspberry Pi (ohne PowerShell)
#
#  Raspberry Pi OS (Lite reicht) mit dem Raspberry Pi Imager flashen, dort
#  Benutzer, WLAN und SSH eintragen, Pi starten, dann auf dem Pi:
#
#    curl -fsSL https://raw.githubusercontent.com/t0b1as/FND/main/install.sh | sudo bash
#
#  Optionen (als Umgebungsvariablen vor "bash"):
#    FUNDUS_ADMIN_PASS=…   Admin-Passwort für Einstellungen/Dateiverwaltung
#                          (sonst Abfrage am Terminal bzw. Zufallspasswort)
#    FUNDUS_VERSION=R531   bestimmte Version statt der neuesten
#    FUNDUS_BOOTSTRAP=…    Einstiegspunkt(e) ins Netz (Multiaddr, Komma-getrennt)
#
#  Erneut ausführen = Neuinstallation der Programme; Daten, Konfiguration,
#  Zertifikat und Admin-Passwort bleiben erhalten. Spätere Updates kommen
#  automatisch über die Update-Funktion des Nodes.
# =============================================================================
set -euo pipefail

REPO="${FUNDUS_REPO:-t0b1as/FND}"
FND_VERSION="${FUNDUS_VERSION:-latest}"
PREFIX=/opt/fundus
FUSER=fundus
ADMIN_USER=admin
HTPASSWD=/etc/nginx/fundus-admin.htpasswd
TLS_DIR=/etc/fundus/tls

step() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok()   { printf '    \033[32m[OK]\033[0m %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '    \033[33m[WARN]\033[0m %s\n' "$*"; }
die()  { printf '\n\033[1;31m[FEHLER]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "Bitte mit sudo ausführen:  curl -fsSL … | sudo bash"
command -v apt-get >/dev/null || die "Nur für Raspberry Pi OS / Debian (apt-get fehlt)."

case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv8l) ARCH=arm ;;
  *) die "Nicht unterstützte Architektur $(uname -m) (Pi 3/4/5 nötig; Pi Zero/1 werden nicht unterstützt)." ;;
esac
# /etc/os-release in einer Unter-Shell lesen: Die Datei setzt u.a. VERSION
# ("13 (trixie)") und hätte sonst unsere Variablen überschrieben.
CODENAME=$(. /etc/os-release && echo "${VERSION_CODENAME:-bookworm}")
OS_NAME=$(. /etc/os-release && echo "${PRETTY_NAME:-Linux}")

printf '\n\033[1;32m  FUNDUS – Installation\033[0m  (%s, %s, %s)\n' "$ARCH" "$OS_NAME" "$REPO"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

# ── 1. Pakete ────────────────────────────────────────────────────────────────
step "Systempakete"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq unzip curl logrotate ca-certificates gnupg openssl exfatprogs ntfs-3g ffmpeg iw >/dev/null
ok "Basis-Pakete"

# ── 2. Webserver mit Lua (OpenResty bevorzugt, sonst nginx + Lua-Modul) ──────
step "Webserver"
if dpkg -l openresty 2>/dev/null | grep -q '^ii'; then
  WEB=openresty
elif dpkg -l libnginx-mod-http-lua 2>/dev/null | grep -q '^ii'; then
  WEB=nginx
else
  WEB=""
  for cn in "$CODENAME" bookworm; do
    if curl -sf -o /dev/null "http://openresty.org/package/raspberrypi/dists/$cn/Release"; then
      curl -fsSL https://openresty.org/package/pubkey.gpg | gpg --dearmor --yes -o /usr/share/keyrings/openresty.gpg
      echo "deb [signed-by=/usr/share/keyrings/openresty.gpg] http://openresty.org/package/raspberrypi $cn main" > /etc/apt/sources.list.d/openresty.list
      apt-get update -qq && apt-get install -y -qq openresty >/dev/null && WEB=openresty
      break
    fi
  done
  if [ -z "$WEB" ]; then
    rm -f /etc/apt/sources.list.d/openresty.list; apt-get update -qq
    apt-get install -y -qq nginx libnginx-mod-http-lua lua-cjson >/dev/null && WEB=nginx
  fi
fi
[ -n "$WEB" ] || die "Kein Lua-fähiger Webserver installierbar (weder OpenResty noch nginx + Lua-Modul)."
ok "$WEB"

# ── 3. System-Benutzer ───────────────────────────────────────────────────────
step "Benutzer '$FUSER'"
id "$FUSER" >/dev/null 2>&1 || useradd --system --create-home --home-dir /home/$FUSER --shell /usr/sbin/nologin --comment Fundus "$FUSER"
usermod -aG root "$FUSER"   # wie deploy-fundus.ps1
ok "vorhanden"

# ── 4. Release herunterladen ─────────────────────────────────────────────────
step "Release von GitHub"
if [ "$FND_VERSION" = latest ]; then
  FND_VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name" *: *"([^"]+)".*/\1/' || true)
  [ -n "$FND_VERSION" ] || die "Neueste Version nicht ermittelbar (GitHub erreichbar?)."
fi
BASE="https://github.com/$REPO/releases/download/$FND_VERSION"
curl -fsSL -o "$TMP/update.zip" "$BASE/FND-$FND_VERSION-update.zip" || die "Update-Paket $FND_VERSION nicht gefunden."
curl -fsSL -o "$TMP/source.zip" "$BASE/FND-$FND_VERSION-source.zip" || die "Quellpaket $FND_VERSION nicht gefunden."
mkdir -p "$TMP/u" "$TMP/s"
unzip -q "$TMP/update.zip" -d "$TMP/u"
unzip -q "$TMP/source.zip" -d "$TMP/s"
[ -f "$TMP/u/bin/fundus-node-linux-$ARCH" ] || die "Programm für $ARCH fehlt im Paket."
ok "$FND_VERSION"

# ── 5. Dateien einspielen ────────────────────────────────────────────────────
step "Installieren nach $PREFIX"
systemctl stop fundus-node 2>/dev/null || true
mkdir -p "$PREFIX/bin" "$PREFIX/data" "$PREFIX/chunks" /etc/fundus /var/log/fundus
install -m 0755 "$TMP/u/bin/fundus-node-linux-$ARCH"   "$PREFIX/bin/fundus-node"
install -m 0755 "$TMP/u/bin/fundus-helper-linux-$ARCH" "$PREFIX/bin/fundus-helper"
rm -rf "$PREFIX/lua.new"; cp -a "$TMP/u/lua" "$PREFIX/lua.new"
rm -rf "$PREFIX/lua.old"; [ -d "$PREFIX/lua" ] && mv "$PREFIX/lua" "$PREFIX/lua.old"
mv "$PREFIX/lua.new" "$PREFIX/lua"; rm -rf "$PREFIX/lua.old"
for f in fundus-node.service fundus-helper.service fundus-automount@.service 99-fundus-automount.rules fundus-logrotate README.md MANUAL.md; do
  [ -f "$TMP/s/$f" ] && cp "$TMP/s/$f" "$PREFIX/" && sed -i 's/\r$//' "$PREFIX/$f"
done
[ -f "$TMP/u/revision.txt" ] && cp "$TMP/u/revision.txt" "$PREFIX/"
chmod 755 "$PREFIX"; chmod 750 "$PREFIX/bin"
chown -R root:$FUSER "$PREFIX"
chown -R $FUSER:$FUSER "$PREFIX/data" "$PREFIX/chunks" /var/log/fundus
chmod 750 "$PREFIX/data"
chown root:$FUSER /etc/fundus; chmod 750 /etc/fundus
ok "Programme, Oberfläche, Dienste"

# ── 6. Konfiguration (nur beim ersten Mal) ───────────────────────────────────
step "Konfiguration"
BOOT="${FUNDUS_BOOTSTRAP:-}"
if [ -z "$BOOT" ]; then
  BOOT=$(curl -fsSL "https://raw.githubusercontent.com/$REPO/main/bootstrap-peers.txt" 2>/dev/null | grep -v '^\s*#' | grep -v '^\s*$' | paste -sd, - || true)
fi
if [ ! -f /etc/fundus/fundus.env ]; then
  cat > /etc/fundus/fundus.env <<EOF
FUNDUS_NODE_TYPE=consumer
FUNDUS_DATA_DIR=$PREFIX/data
FUNDUS_P2P_PORT=4001
FUNDUS_API_PORT=3000
FUNDUS_API_BIND=127.0.0.1
FUNDUS_BOOTSTRAP_PEERS=$BOOT
FUNDUS_TRAFO_RATED_KW=400
FUNDUS_SETTLEMENT_METHOD=ansatz3
FUNDUS_FND_FEE_COLLECTOR=0xea5594a7cc26d2456e9a033481d01a5ba101c8f8
FUNDUS_CHAIN_ID=100
FUNDUS_CHAIN_RPC_URL=https://rpc.gnosischain.com
FUNDUS_FND_ADDRESS=
FUNDUS_SEED_FILE=/etc/fundus/wallet.key
FUNDUS_LOG_LEVEL=info
FUNDUS_BLOOM_FILTER_SIZE=4096
FUNDUS_STORAGE_DIR=$PREFIX/chunks
# Filesharing: -1=auto 50% des freien Speichers, 0=aus, >0=feste GB
FUNDUS_STORAGE_OFFER_GB=-1
FUNDUS_SWAP_HTLC_PROGRAM=B1ysbjJT1f7dWvVwMp55oo1KYu4GwnwKeCF12DhL7K2N
FUNDUS_SHOP_SOLANA_RPC=https://api.mainnet-beta.solana.com
EOF
  chown $FUSER:$FUSER /etc/fundus/fundus.env; chmod 600 /etc/fundus/fundus.env
  ok "fundus.env angelegt${BOOT:+ (Einstieg ins Netz: $BOOT)}"
else
  ok "fundus.env vorhanden – unverändert"
fi

# ── 7. TLS-Zertifikat (selbstsigniert, falls noch keins) ─────────────────────
step "Zertifikat"
IP=$(hostname -I 2>/dev/null | awk '{print $1}')
if [ ! -f "$TLS_DIR/fundus.crt" ] || [ ! -f "$TLS_DIR/fundus.key" ]; then
  mkdir -p "$TLS_DIR"
  SAN="DNS:$(hostname).local,DNS:fundus.local${IP:+,IP:$IP}"
  openssl req -x509 -nodes -days 3650 -newkey rsa:2048 -keyout "$TLS_DIR/fundus.key" -out "$TLS_DIR/fundus.crt" \
    -subj '/CN=fundus.local' -addext "subjectAltName=$SAN" >/dev/null 2>&1 \
  || openssl req -x509 -nodes -days 3650 -newkey rsa:2048 -keyout "$TLS_DIR/fundus.key" -out "$TLS_DIR/fundus.crt" -subj '/CN=fundus.local' >/dev/null 2>&1
  chmod 600 "$TLS_DIR/fundus.key"
  ok "selbstsigniert erzeugt (gültiges Zertifikat später: tools/setup-letsencrypt.sh)"
else
  ok "vorhanden – unverändert"
fi

# ── 8. Admin-Passwort ────────────────────────────────────────────────────────
step "Admin-Passwort"
mkdir -p /etc/nginx
GENPASS=""
if [ -n "${FUNDUS_ADMIN_PASS:-}" ]; then
  PASS="$FUNDUS_ADMIN_PASS"
elif [ -s "$HTPASSWD" ]; then
  PASS=""
elif [ -r /dev/tty ]; then
  while :; do
    read -rsp "    Neues Admin-Passwort (Einstellungen, Dateiverwaltung): " P1 </dev/tty; echo
    read -rsp "    Wiederholen: " P2 </dev/tty; echo
    [ -n "$P1" ] && [ "$P1" = "$P2" ] && break
    warn "leer oder verschieden – bitte erneut"
  done
  PASS="$P1"
else
  PASS=$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-16); GENPASS="$PASS"
fi
if [ -n "$PASS" ]; then
  echo "$ADMIN_USER:$(openssl passwd -apr1 "$PASS")" > "$HTPASSWD"
  chown root:root "$HTPASSWD"; chmod 644 "$HTPASSWD"
  ok "gesetzt (Benutzer: $ADMIN_USER)"
else
  ok "vorhanden – unverändert"
fi

# ── 9. Webserver einrichten ──────────────────────────────────────────────────
step "Webserver-Konfiguration ($WEB)"
USE_SSL=0; [ -f "$TLS_DIR/fundus.crt" ] && [ -f "$TLS_DIR/fundus.key" ] && USE_SSL=1
if [ "$WEB" = openresty ]; then
  CONF=/usr/local/openresty/nginx/conf/conf.d; MAIN=/usr/local/openresty/nginx/conf/nginx.conf
  mkdir -p "$CONF"
  grep -q 'conf.d/\*.conf' "$MAIN" || sed -i '/http {/a\    include conf.d/*.conf;' "$MAIN"
  cp "$PREFIX/lua/fundus-http.conf"  "$CONF/00-fundus-http.conf"
  cp "$PREFIX/lua/fundus-nginx.conf" "$CONF/fundus.conf"
  if [ $USE_SSL = 1 ]; then cp "$PREFIX/lua/fundus-nginx-ssl.conf" "$CONF/fundus-ssl.conf"; else rm -f "$CONF/fundus-ssl.conf"; fi
  sed -i 's/\r$//' "$CONF"/00-fundus-http.conf "$CONF"/fundus*.conf
  TEST="/usr/local/openresty/bin/openresty -t"
else
  if ! ls /etc/nginx/modules-enabled/ 2>/dev/null | grep -q lua && ! grep -q ngx_http_lua_module /etc/nginx/nginx.conf; then
    sed -i '1i load_module modules/ngx_http_lua_module.so;' /etc/nginx/nginx.conf
  fi
  cp "$PREFIX/lua/fundus-http.conf"  /etc/nginx/conf.d/fundus-http.conf
  cp "$PREFIX/lua/fundus-nginx.conf" /etc/nginx/sites-available/fundus.conf
  ln -sf /etc/nginx/sites-available/fundus.conf /etc/nginx/sites-enabled/fundus.conf
  if [ $USE_SSL = 1 ]; then
    cp "$PREFIX/lua/fundus-nginx-ssl.conf" /etc/nginx/sites-available/fundus-ssl.conf
    ln -sf /etc/nginx/sites-available/fundus-ssl.conf /etc/nginx/sites-enabled/fundus-ssl.conf
  else
    rm -f /etc/nginx/sites-enabled/fundus-ssl.conf
  fi
  rm -f /etc/nginx/sites-enabled/default
  sed -i 's/\r$//' /etc/nginx/conf.d/fundus-http.conf /etc/nginx/sites-available/fundus*.conf
  grep -q 'sites-enabled' /etc/nginx/nginx.conf || sed -i '/http {/a\    include /etc/nginx/sites-enabled/*;' /etc/nginx/nginx.conf
  grep -q 'conf.d/\*.conf' /etc/nginx/nginx.conf || sed -i '/http {/a\    include /etc/nginx/conf.d/*.conf;' /etc/nginx/nginx.conf
  TEST="nginx -t"
fi
$TEST >/dev/null 2>&1 || { $TEST; die "Webserver-Konfiguration fehlerhaft (siehe oben)."; }
systemctl enable "$WEB" >/dev/null 2>&1 || true
systemctl restart "$WEB"
ok "aktiv"

# ── 10. Dienste ──────────────────────────────────────────────────────────────
step "Dienste"
cp "$PREFIX/fundus-node.service"   /etc/systemd/system/fundus-node.service
cp "$PREFIX/fundus-helper.service" /etc/systemd/system/fundus-helper.service
if [ -f "$PREFIX/fundus-automount@.service" ] && [ -f "$PREFIX/99-fundus-automount.rules" ]; then
  cp "$PREFIX/fundus-automount@.service" /etc/systemd/system/
  cp "$PREFIX/99-fundus-automount.rules" /etc/udev/rules.d/
  udevadm control --reload-rules 2>/dev/null || true
fi
[ -f "$PREFIX/fundus-logrotate" ] && cp "$PREFIX/fundus-logrotate" /etc/logrotate.d/fundus
systemctl daemon-reload
systemctl enable fundus-helper fundus-node >/dev/null 2>&1
systemctl restart fundus-helper
systemctl restart fundus-node
ok "fundus-node, fundus-helper"

# ── 11. Prüfen ───────────────────────────────────────────────────────────────
step "Start prüfen"
for i in $(seq 1 45); do
  H=$(curl -s --max-time 2 http://127.0.0.1:3000/health || true)
  echo "$H" | grep -q '"status":"ok"' && break
  sleep 2
done
if echo "${H:-}" | grep -q '"status":"ok"'; then
  ok "läuft: $H"
else
  warn "Node antwortet noch nicht. Log: sudo journalctl -u fundus-node -n 50 --no-pager"
fi

printf '\n\033[1;32m  Fertig.\033[0m\n'
printf '  Oberfläche:   https://%s   (selbstsigniert: Warnung einmal bestätigen)\n' "${IP:-<IP-des-Pi>}"
printf '  Admin:        Benutzer %s\n' "$ADMIN_USER"
[ -n "$GENPASS" ] && printf '  \033[1;33mAdmin-Passwort (bitte notieren): %s\033[0m\n' "$GENPASS"
printf '  Nächster Schritt: Einstellungen → Node-Wallet einrichten.\n'
printf '  Updates kommen automatisch (Einstellungen → Software-Update).\n\n'

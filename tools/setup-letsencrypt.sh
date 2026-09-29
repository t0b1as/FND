#!/bin/bash
# Fundus: gültiges TLS-Zertifikat von Let's Encrypt einrichten.
#
# Prüfung per TLS-ALPN-01 über PORT 443 – Port 80 muss NICHT freigegeben sein.
# Dafür wird der Webserver (openresty oder nginx) für wenige Sekunden angehalten,
# bei der Ausstellung und bei jeder Verlängerung (nur wenn < 30 Tage Rest).
#
# Aufruf auf dem öffentlich erreichbaren Pi:
#   sudo bash setup-letsencrypt.sh fnd.resolve.bar deine@mail.de
#
# Voraussetzungen: Die Domain zeigt auf deine öffentliche IP, und der Router
# leitet Port 443 an diesen Pi weiter.
set -euo pipefail

DOMAIN="${1:-}"
EMAIL="${2:-}"
if [ -z "$DOMAIN" ] || [ -z "$EMAIL" ]; then
  echo "Aufruf: sudo bash $0 <domain> <e-mail>"; exit 1
fi
[ "$(id -u)" -eq 0 ] || { echo "Bitte mit sudo ausführen."; exit 1; }

LEGO_DIR=/etc/fundus/letsencrypt
TLS_DIR=/etc/fundus/tls

# ── Webserver erkennen ────────────────────────────────────────────────────────
if systemctl is-active --quiet openresty; then WEB=openresty
elif systemctl is-active --quiet nginx; then WEB=nginx
else echo "Kein laufender Webserver (openresty/nginx) gefunden."; exit 1; fi
echo "Webserver: $WEB"

# ── lego installieren ────────────────────────────────────────────────────────
if ! command -v lego >/dev/null 2>&1; then
  echo "Installiere lego …"
  apt-get update -qq && apt-get install -y -qq lego
fi
command -v lego >/dev/null 2>&1 || { echo "lego konnte nicht installiert werden."; exit 1; }

# ── Konfiguration für die Verlängerung ablegen ───────────────────────────────
mkdir -p "$LEGO_DIR"
cat > "$LEGO_DIR/fundus-cert.conf" <<EOF
DOMAIN=$DOMAIN
EMAIL=$EMAIL
EOF

# ── Verlängerungs-/Installationsskript ───────────────────────────────────────
cat > /usr/local/sbin/fundus-cert-renew <<'EOF'
#!/bin/bash
# Holt/verlängert das Let's-Encrypt-Zertifikat (TLS-ALPN-01 über Port 443) und
# spielt es für Fundus ein. Ohne "--force" nur, wenn es in < 30 Tagen abläuft.
set -uo pipefail
. /etc/fundus/letsencrypt/fundus-cert.conf
LEGO_DIR=/etc/fundus/letsencrypt
TLS_DIR=/etc/fundus/tls
CRT="$LEGO_DIR/certificates/$DOMAIN.crt"
KEY="$LEGO_DIR/certificates/$DOMAIN.key"

if [ "${1:-}" != "--force" ] && [ -f "$CRT" ] && openssl x509 -checkend 2592000 -noout -in "$CRT" >/dev/null 2>&1; then
  exit 0   # noch mehr als 30 Tage gültig
fi

if systemctl is-active --quiet openresty; then WEB=openresty; else WEB=nginx; fi
MODE=run; [ -f "$CRT" ] && MODE=renew
EXTRA=""; [ "$MODE" = renew ] && EXTRA="--days 30"

systemctl stop "$WEB"
lego --accept-tos --email "$EMAIL" --domains "$DOMAIN" --tls --path "$LEGO_DIR" $MODE $EXTRA
RC=$?
systemctl start "$WEB"
[ $RC -eq 0 ] || { echo "lego fehlgeschlagen ($RC) – altes Zertifikat bleibt aktiv."; exit $RC; }

# Einspielen (vorheriges einmalig sichern)
mkdir -p "$TLS_DIR"
[ -f "$TLS_DIR/fundus.crt" ] && [ ! -f "$TLS_DIR/fundus.crt.selfsigned" ] && cp -a "$TLS_DIR/fundus.crt" "$TLS_DIR/fundus.crt.selfsigned"
[ -f "$TLS_DIR/fundus.key" ] && [ ! -f "$TLS_DIR/fundus.key.selfsigned" ] && cp -a "$TLS_DIR/fundus.key" "$TLS_DIR/fundus.key.selfsigned"
install -m 0644 "$CRT" "$TLS_DIR/fundus.crt"
install -m 0600 "$KEY" "$TLS_DIR/fundus.key"
systemctl reload "$WEB" || systemctl restart "$WEB"
echo "Zertifikat für $DOMAIN eingespielt: $(openssl x509 -enddate -noout -in "$TLS_DIR/fundus.crt")"
EOF
chmod 0750 /usr/local/sbin/fundus-cert-renew

# ── Täglicher Timer ──────────────────────────────────────────────────────────
cat > /etc/systemd/system/fundus-cert-renew.service <<'EOF'
[Unit]
Description=Fundus: Let's-Encrypt-Zertifikat verlängern (falls fällig)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/fundus-cert-renew
EOF
cat > /etc/systemd/system/fundus-cert-renew.timer <<'EOF'
[Unit]
Description=Fundus: täglich prüfen, ob das Zertifikat verlängert werden muss

[Timer]
OnCalendar=*-*-* 04:17:00
RandomizedDelaySec=45min
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now fundus-cert-renew.timer >/dev/null

# ── Erstausstellung ──────────────────────────────────────────────────────────
echo "Hole Zertifikat für $DOMAIN (Webserver pausiert kurz) …"
/usr/local/sbin/fundus-cert-renew --force
echo
echo "Fertig. Aufruf: https://$DOMAIN"
echo "Verlängerung: automatisch (Timer fundus-cert-renew.timer, täglich geprüft)."

#!/bin/bash
# webserver-update.sh (R933) – nginx/OpenResty auf Sicherheitsstand bringen.
# Aufruf: sudo bash webserver-update.sh [alles]
#   alles = zusätzlich alle Systempakete aktualisieren (apt-get upgrade)
# Ausgabe: Zeilen "OK …", "INFO …", "WARN …" für deploy-fundus.ps1.
# Bisher lief beim Deploy nur "apt-get update"; installiert wurde nur, was
# fehlte – Sicherheitsupdates des Webservers kamen nie an (NGINX2Root,
# CVE-2026-123007 ff., Nov. 2026). Neu gestartet wird der Webserver danach vom
# Deploy (restart, nicht reload – erst dann läuft das neue Programm).
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a   # needrestart fragt sonst nach, welche Dienste neu starten
# DPkg::Lock::Timeout: läuft gerade unattended-upgrades, warten statt abbrechen.
APTOPT=(-y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=300)

apt-get update -qq >/dev/null 2>&1 || echo "WARN apt-get update fehlgeschlagen"

# Nur INSTALLIERTE Pakete nennen – ein unbekannter Name ließe apt abbrechen.
pk=$(dpkg-query -W -f='${binary:Package} ${Status}\n' \
  openresty openresty-openssl3 openresty-openssl111 openresty-pcre2 openresty-pcre openresty-zlib \
  nginx nginx-common nginx-core nginx-light nginx-full libnginx-mod-http-lua libnginx-mod-http-ndk \
  2>/dev/null | awk '/ installed$/{print $1}' | sed 's/:.*//' | tr '\n' ' ')
if [ -n "$pk" ]; then
  r=$(apt-get install --only-upgrade "${APTOPT[@]}" $pk 2>&1 | grep -E 'upgraded,|^E:' | tail -2 | tr '\n' ' ')
  n=$(echo "$r" | grep -oE '^[0-9]+ upgraded' | grep -oE '^[0-9]+')
  if [ "${n:-0}" -gt 0 ]; then echo "OK Webserver-Pakete aktualisiert ($n): ${pk% }"; else echo "INFO Webserver-Pakete (${pk% }): aktuell, kein neueres Paket im Repository"; fi
fi

# Wie viele Systempakete warten? (nur Hinweis; aktualisiert wird mit „alles“)
offen=$(apt-get -s upgrade 2>/dev/null | grep -cE '^Inst ')
if [ "$1" != "alles" ] && [ "${offen:-0}" -gt 0 ]; then
  echo "INFO $offen weitere Systempakete haben Updates - Deploy mit -SystemUpgrade spielt sie ein"
fi

if [ "$1" = "alles" ]; then
  # --with-new-pkgs: auch Updates, die ein neues Abhängigkeitspaket brauchen
  # (sonst bleiben sie dauerhaft „zurückgehalten“). Entfernt nichts.
  dpkg --configure -a >/dev/null 2>&1
  r=$(apt-get upgrade --with-new-pkgs "${APTOPT[@]}" 2>&1 | grep -E 'upgraded,|^E:' | tail -2 | tr '\n' ' ')
  echo "INFO Systempakete: $r"
  [ -f /var/run/reboot-required ] && echo "WARN Neustart des Pi noetig (Kernel/Bibliotheken) - bei Gelegenheit: sudo reboot"
fi

# R936: SCTP-Kernelmodul sperren. Ungepatchte Lücke im SCTP-Sendepfad
# (Kernel ab 4.15, Nov. 2026) – Fundus braucht kein Kernel-SCTP (libp2p nutzt
# TCP/QUIC; WebRTC-SCTP liefe ohnehin im Programm, nicht im Kernel).
if [ ! -f /etc/modprobe.d/fundus-no-sctp.conf ]; then
  printf '# Fundus (R936): SCTP wird nicht gebraucht – ungepatchte Kernel-Lücke (Nov. 2026)\ninstall sctp /bin/false\n' > /etc/modprobe.d/fundus-no-sctp.conf
  echo "OK SCTP-Kernelmodul gesperrt"
fi
if lsmod 2>/dev/null | grep -q '^sctp '; then
  if modprobe -r sctp 2>/dev/null; then echo "OK SCTP-Modul entladen"; else echo "WARN SCTP-Modul ist geladen und in Benutzung - nach Neustart des Pi gesperrt"; fi
fi

# Stand prüfen: NGINX2Root ist ab nginx 1.30.7 (stable) bzw. 1.31.9 behoben.
# Erst das Programm finden, dann fragen – sonst landet „No such file“ als
# vermeintliche Version in der Ausgabe (R934).
wbin=""
for b in /usr/local/openresty/bin/openresty /usr/local/openresty/nginx/sbin/nginx /usr/sbin/nginx $(command -v nginx 2>/dev/null); do
  [ -x "$b" ] && { wbin=$b; break; }
done
if [ -z "$wbin" ]; then
  echo "WARN Kein nginx/OpenResty gefunden"; exit 0
fi
wv=$("$wbin" -v 2>&1 | grep -m1 -i 'version')
v=$(echo "$wv" | grep -oE '(openresty|nginx)/[0-9]+\.[0-9]+\.[0-9]+' | head -1 | cut -d/ -f2)
if [ -z "$v" ]; then
  echo "WARN Webserver-Version nicht ermittelbar: $wv"; exit 0
fi
IFS=. read -r ma mi pa <<<"$v"
gepatcht=0
if [ "$ma" -gt 1 ] || { [ "$ma" -eq 1 ] && [ "$mi" -ge 32 ]; } || { [ "$ma" -eq 1 ] && [ "$mi" -eq 31 ] && [ "$pa" -ge 9 ]; } || { [ "$ma" -eq 1 ] && [ "$mi" -eq 30 ] && [ "$pa" -ge 7 ]; }; then
  gepatcht=1
fi
hinweis="Fundus nutzt weder HTTP/2 noch HTTP/3, TLS ist auf 1.2 begrenzt."
if [ $gepatcht = 1 ]; then
  echo "OK Webserver ${wv#nginx version: } - NGINX2Root behoben"
elif echo "$wv" | grep -q openresty; then
  echo "WARN Webserver ${wv#nginx version: } - noch ohne NGINX2Root-Fix (ab 1.30.7/1.31.9). $hinweis Naechstes Deploy aktualisiert erneut."
else
  deb=$(dpkg-query -W -f='${Version}' nginx 2>/dev/null)
  echo "INFO Webserver ${wv#nginx version: }, Debian-Paket $deb - Debian patcht ohne Versionssprung; Stand: security-tracker.debian.org/tracker/CVE-2026-123007. $hinweis"
fi
exit 0

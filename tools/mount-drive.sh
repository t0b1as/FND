#!/usr/bin/env bash
#
# mount-drive.sh — externes Laufwerk dauerhaft für den Fundus-Node zugänglich machen.
#
# Problem: Desktop-Systeme (udisks2) mounten USB-Laufwerke unter /media/<user>/ oder
# /run/media/<user>/ — nur für den Desktop-User lesbar. Der fundus-node-Dienst läuft
# als eigener User und bekommt "Permission denied". Dieses Skript trägt das Laufwerk
# mit den richtigen Rechten dauerhaft in die /etc/fstab ein (Mount nach /mnt/fundus-<label>),
# sodass der Node es lesen/schreiben kann — auch nach einem Neustart.
#
#   sudo bash mount-drive.sh              # interaktiv: zeigt Laufwerke, fragt welches
#   sudo bash mount-drive.sh /dev/sda1    # direkt ein Gerät angeben
#
# Idempotent: ein bereits eingetragenes Laufwerk wird NICHT doppelt hinzugefügt.

set -e
[ "$(id -u)" -eq 0 ] || { echo "Bitte mit sudo ausführen."; exit 1; }

FUNDUS_USER="${FUNDUS_USER:-fundus}"
id "$FUNDUS_USER" >/dev/null 2>&1 || { echo "User '$FUNDUS_USER' existiert nicht. FUNDUS_USER=... setzen."; exit 1; }
UID_F=$(id -u "$FUNDUS_USER"); GID_F=$(id -g "$FUNDUS_USER")

DEV="$1"
if [ -z "$DEV" ]; then
    echo "Externe Laufwerke:"
    lsblk -o NAME,SIZE,FSTYPE,LABEL,MOUNTPOINT -p -n | grep -E "/dev/sd|/dev/nvme" | grep -vE "part /$|swap" || true
    echo ""
    read -rp "Gerät eingeben (z.B. /dev/sda1): " DEV
fi
[ -b "$DEV" ] || { echo "Kein Blockgerät: $DEV"; exit 1; }

UUID=$(blkid -s UUID -o value "$DEV")
FSTYPE=$(blkid -s TYPE -o value "$DEV")
LABEL=$(blkid -s LABEL -o value "$DEV" | tr ' ' '_')
[ -n "$UUID" ] || { echo "Keine UUID für $DEV gefunden."; exit 1; }
[ -n "$LABEL" ] || LABEL="drive-${UUID:0:8}"

MOUNT="/mnt/fundus-${LABEL}"

# Schon in der fstab? Dann nichts tun (idempotent).
if grep -q "UUID=$UUID" /etc/fstab; then
    echo "UUID=$UUID steht bereits in /etc/fstab — nichts zu tun."
    echo "Mountpunkt: $(grep "UUID=$UUID" /etc/fstab | awk '{print $2}')"
    exit 0
fi

# Mount-Optionen je nach Dateisystem: exfat/vfat/ntfs brauchen uid/gid, ext4 nicht.
case "$FSTYPE" in
    exfat|vfat|ntfs|ntfs3)
        OPTS="defaults,uid=$UID_F,gid=$GID_F,umask=0022,nofail" ;;
    *)
        OPTS="defaults,nofail" ;;  # ext4 etc.: Rechte über chown unten
esac

# Falls das Laufwerk gerade von udisks2 woanders gemountet ist, erst aushängen.
CUR=$(findmnt -n -o TARGET --source "$DEV" 2>/dev/null || true)
if [ -n "$CUR" ] && [ "$CUR" != "$MOUNT" ]; then
    echo "Hänge $DEV von $CUR aus (war Desktop-Mount)..."
    umount "$DEV" 2>/dev/null || udisksctl unmount -b "$DEV" 2>/dev/null || true
fi

# Dateisystem-Check VOR dem Mount: behebt das "dirty"-Flag, mit dem Windows
# exFAT/NTFS-Platten oft hinterlässt (führt sonst zu read-only). Nur wenn das
# Laufwerk nicht mehr gemountet ist.
if ! findmnt -n --source "$DEV" >/dev/null 2>&1; then
    case "$FSTYPE" in
        exfat)
            if command -v fsck.exfat >/dev/null 2>&1; then
                echo "Prüfe exFAT-Dateisystem (behebt dirty-Flag)..."
                fsck.exfat -y "$DEV" || echo "  fsck meldete Probleme (fortfahren)."
            fi ;;
        ntfs|ntfs3)
            if command -v ntfsfix >/dev/null 2>&1; then
                echo "Prüfe NTFS-Dateisystem (ntfsfix)..."
                ntfsfix "$DEV" || echo "  ntfsfix meldete Probleme (fortfahren)."
            fi ;;
    esac
fi

mkdir -p "$MOUNT"
FSTAB_LINE="UUID=$UUID $MOUNT $FSTYPE $OPTS 0 0"
echo "$FSTAB_LINE" >> /etc/fstab
echo "In /etc/fstab eingetragen:"
echo "  $FSTAB_LINE"

systemctl daemon-reload
mount "$MOUNT" || { echo "Mount fehlgeschlagen — fstab-Zeile prüfen."; exit 1; }

# Bei ext4 o.ä. (kein uid-Mount) den Eigentümer setzen, damit der Node schreiben darf.
case "$FSTYPE" in
    exfat|vfat|ntfs|ntfs3) : ;;
    *) chown "$FUNDUS_USER:$FUNDUS_USER" "$MOUNT" ;;
esac

echo ""
echo "✓ $DEV ist jetzt unter $MOUNT für den Fundus-Node zugänglich (auch nach Neustart)."
echo "  Im Speicher-Bereich der Web-UI erscheint es nun mit dem Regler."

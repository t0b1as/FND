#!/usr/bin/env bash
# Statischer Konsistenz-Check für das Fundus-Go-Projekt OHNE Compiler.
# Fängt: Klammer-Balance, ungenutzte/fehlende Importe (heuristisch),
# Symbol-Definition vs. -Aufruf, Duplikat-Definitionen.
# FÄNGT NICHT: Typfehler, Interface-Mismatches, Logikfehler — dafür: go test.
#
# Nutzung:  bash tools/static-check.sh [go-verzeichnis]   (default: ./go)

set -u
ROOT="${1:-./go}"
fail=0

echo "== 1. Klammer-/Brace-Balance pro Datei =="
# 1a. Verlässlicher Tiefen-Tracker (ignoriert Strings/Kommentare): findet eine
#     fehlende/zu viele '}' eindeutig (Endtiefe != 0) und meldet die Funktion,
#     vor der die Tiefe nicht 0 war. Robuster als reine Zeichenzählung.
while IFS= read -r f; do
  python3 - "$f" 2>/dev/null << 'PYDEPTH'
import re, sys
f = sys.argv[1]; depth = 0; warn = 0; firstbad = 0
for ln, line in enumerate(open(f, encoding="utf-8", errors="replace"), 1):
    if line.lstrip().startswith("//"):
        continue
    code = re.sub(r'"(\\.|[^"\\])*"', '', line)
    code = re.sub(r'`[^`]*`', '', code)
    code = re.sub(r"'(\\.|[^'\\])'", '', code)
    if code.startswith("func ") and depth != 0 and firstbad == 0:
        firstbad = ln
    depth += code.count("{") - code.count("}")
if depth != 0:
    print(f"  \u26a0 {f}: Klammer-Tiefe Endstand {depth} (fehlende/zu viele '}}'); erste auffaellige Funktion ~Z.{firstbad}")
PYDEPTH
done < <(find "$ROOT" -name '*.go')
echo "   (Tiefen-Check: keine ⚠ = strukturell balanciert)"

echo "== 1b. Grobe Zeichenzählung (Zusatz, kann bei Kommentar-Klammern Fehlalarme geben) =="
while IFS= read -r f; do
  for pair in '{ }' '( )'; do
    o="${pair% *}"; c="${pair#* }"
    no=$(grep -o "$o" "$f" | wc -l); nc=$(grep -o "$c" "$f" | wc -l)
    if [ "$no" != "$nc" ]; then
      echo "  ⚠ $f: '$o$c' unbalanciert ($no/$nc)"; fail=1
    fi
  done
done < <(find "$ROOT" -name '*.go')
echo "   ok"

echo "== 2. Heuristik: importierte Pakete auch genutzt? =="
while IFS= read -r f; do
  # Importzeilen im import(...)-Block extrahieren
  awk '/^import \(/{p=1;next} /^\)/{p=0} p' "$f" | grep '"' | while read -r line; do
    path=$(echo "$line" | grep -oP '"[^"]+"' | tr -d '"')
    [ -z "$path" ] && continue
    # Alias?
    alias=$(echo "$line" | awk '{if ($1 ~ /^[a-z]/ && $1 !~ /"/) print $1}')
    pkg="${alias:-$(basename "$path")}"
    # blank/underscore-Import ignorieren
    echo "$line" | grep -qP '^\s*_\s' && continue
    grep -q "\b$pkg\." "$f" || echo "  ⚠ $f: import '$path' (als $pkg) evtl. ungenutzt"
  done
done < <(find "$ROOT" -name '*.go' ! -name '*_test.go')
echo "   (Warnungen oben prüfen; manche Pakete werden nur als Typ genutzt)"

echo "== 3. Duplikat-Definitionen (gleiche Funktion ohne Receiver / gleicher Typname) =="
find "$ROOT" -name '*.go' ! -name '*_test.go' -exec grep -hoP '^func \K\w+|^type \K\w+' {} \; \
  | sort | uniq -d | while read -r sym; do
    [ -n "$sym" ] && echo "  ⚠ Symbol mehrfach top-level definiert: $sym"
  done
echo "   (Methoden auf verschiedenen Typen sind KEIN Duplikat — manuell prüfen)"

echo "== 4. go-ethereum / blake3 API-Signaturen (häufige Fehlerquellen) =="
grep -rn "crypto\.Sign(\|crypto\.SigToPub(\|crypto\.FromECDSAPub(\|blake3\.Sum256(\|blake3\.New(" "$ROOT" \
  --include='*.go' | sed 's/^/  /'
echo "   (Sign(hash,priv) / SigToPub(hash,sig) / FromECDSAPub(pub) / Sum256(b) prüfen)"

echo ""
if [ "$fail" = "0" ]; then
  echo "✓ Statische Basis-Checks ohne harte Fehler. ERSETZT NICHT 'go test ./...'."
else
  echo "✗ Harte Befunde oben — bitte fixen."
fi
exit $fail

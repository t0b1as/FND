#!/usr/bin/env bash
# Lua-Syntax-Vorabcheck für das Fundus-Frontend.
# Fängt Syntaxfehler (verschachtelte unescapte Quotes, unbalancierte Klammern,
# unterminierte Strings), die sonst erst zur Laufzeit auf dem Pi auffallen würden
# (z.B. via require-Fehler → i18n liefert rohe Keys, oder 500 auf jeder Seite).
# Ergänzt static-check.sh (das nur Go prüft).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LUA_FILES=$(find "$ROOT/lua" -name "*.lua")
if python3 "$ROOT/tools/lua-syntax-check.py" $LUA_FILES; then
  echo "✓ Lua-Syntax: alle Dateien sauber."
  exit 0
else
  echo "✗ Lua-Syntaxfehler gefunden — bitte vor dem Deploy beheben."
  exit 1
fi

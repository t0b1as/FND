import sys

LUA_RESERVED = {"and","break","do","else","elseif","end","false","for","function",
                "goto","if","in","local","nil","not","or","repeat","return","then",
                "true","until","while"}

def check_format_specifiers(path):
    # Prüft das gefährlichste Muster: literales % (z.B. CSS "width:100%;") in einem
    # string.format(...)-Aufruf. Solche %-Zeichen müssen %% sein, sonst Laufzeit-
    # fehler (→ leere Seite). Der Format-String in wallet.lua ist oft fragmentiert
    # (([[ ... ]] .. t() .. [[ ... ]])), daher wird der GANZE string.format(...)-
    # Aufruf bis zur balancierten schließenden ) erfasst und JEDES [[...]]- bzw.
    # [==[...]==]-Fragment darin geprüft. Lua-Patterns (gsub/match) stehen außerhalb
    # von string.format und werden nicht gemeldet.
    import re
    src = open(path, encoding="utf-8", errors="replace").read()
    errors = []
    valid_spec = re.compile(r"[-+ #0]*\d*(?:\.\d+)?[sdifgGxXoceqEuc]")

    def scan_fragment(frag, base_line):
        i = 0
        while i < len(frag):
            if frag[i] != "%":
                i += 1; continue
            if i + 1 < len(frag) and frag[i+1] == "%":
                i += 2; continue
            m = valid_spec.match(frag, i+1)
            if m:
                i = m.end(); continue
            ln = base_line + frag[:i].count("\n")
            nxt = frag[i+1] if i+1 < len(frag) else ""
            errors.append((ln, f"ungültiges '%{nxt}' in string.format — literales % als %% escapen"))
            i += 1

    for fm in re.finditer(r"string\.format\s*\(", src):
        # Erfasse den Aufruf bis zur balancierten schließenden ).
        depth = 0
        j = fm.end() - 1  # auf '(' positioniert
        n = len(src)
        call_start = j
        while j < n:
            if src[j] == "(":
                depth += 1
            elif src[j] == ")":
                depth -= 1
                if depth == 0:
                    break
            j += 1
        call = src[call_start:j]
        call_base = src[:call_start].count("\n") + 1
        # Alle Langstring-Fragmente [[...]] und [=*[...]=*] im Aufruf prüfen.
        for sm in re.finditer(r"\[(=*)\[(.*?)\]\1\]", call, re.DOTALL):
            frag = sm.group(2)
            frag_base = call_base + call[:sm.start()].count("\n")
            scan_fragment(frag, frag_base)
    return errors


def check_reserved_keys(path):
    """Findet reservierte Lua-Schlüsselwörter, die in Kurzform als Tabellen-Key
    benutzt werden (z.B. `local = "x"`). Das ist ein Syntaxfehler; korrekt wäre
    `["local"] = "x"`. Solche Fehler verschieben die Klammerbilanz nicht und
    werden vom String-/Klammer-Scanner nicht erfasst."""
    import re
    errors = []
    in_long = 0  # grobe Unterdrückung innerhalb von [[ ]] (kein Key-Kontext)
    for i, line in enumerate(open(path, encoding="utf-8"), 1):
        stripped = line.lstrip()
        if stripped.startswith("--"):
            continue
        m = re.match(r'\s*([A-Za-z_][A-Za-z0-9_]*)\s*=[^=]', line)
        if m and m.group(1) in LUA_RESERVED:
            errors.append((i, f"reserviertes Wort '{m.group(1)}' als Tabellen-Key "
                              f"(nutze [\"{m.group(1)}\"] = ...)"))
    return errors

def check_long_string_concat(path):
    """Findet das Muster ]] .. t(...) .. [[ INNERHALB eines [==[...]==]-Blocks
    (oder umgekehrt). Solche Konkatenationen erscheinen dann als ROHTEXT statt
    aufgelöst zu werden, weil ]] / [[ den umschließenden [==[ ]==]-Block nicht
    schließen. Syntaktisch gültig, semantisch falsch — der String-/Klammer-Scanner
    fängt es nicht."""
    import re
    src = open(path, encoding="utf-8").read()
    errors = []
    for m in re.finditer(r'\[==\[(.*?)\]==\]', src, re.DOTALL):
        block = m.group(1)
        if re.search(r'\]\]\s*\.\.\s*[a-zA-Z_]', block):
            ln = src[:m.start()].count("\n") + 1
            inner_ln = ln + block[:re.search(r'\]\]\s*\.\.', block).start()].count("\n")
            errors.append((inner_ln, "']] .. ' in [==[...]==]-Block → erscheint als "
                                     "Rohtext (nutze %s-Platzhalter statt Konkatenation)"))
    # Gegenrichtung: ]==] .. in einem [[...]]-Block (seltener)
    return errors

def check_lua(path):
    """String-aware Lua-Syntaxprüfung: parst Strings (", ', [[]], [=[]=]) mit
    Escapes korrekt und prüft Klammer-Balance auf dem Code AUSSERHALB von
    Strings/Kommentaren. Fängt verschachtelte unescapte Quotes (verschieben die
    Klammerbilanz) und unbalancierte (){}/[]."""
    src = open(path, encoding="utf-8").read()
    i, n = 0, len(src)
    line = 1
    stack = []  # (char, line)
    pairs = {")": "(", "]": "[", "}": "{"}
    errors = []
    while i < n:
        c = src[i]
        if c == "\n":
            line += 1; i += 1; continue
        # Kommentare
        if c == "-" and i+1 < n and src[i+1] == "-":
            # Langkommentar --[[ ]]
            if i+3 < n and src[i+2] == "[" and src[i+3] in "[=":
                # finde Level
                j = i+3; eq = 0
                while j < n and src[j] == "=": eq += 1; j += 1
                if j < n and src[j] == "[":
                    close = "]" + "="*eq + "]"
                    end = src.find(close, j+1)
                    if end < 0: errors.append((line, "unterminierter Langkommentar")); break
                    line += src[i:end].count("\n"); i = end + len(close); continue
            # Zeilenkommentar
            nl = src.find("\n", i)
            if nl < 0: break
            i = nl; continue
        # Langstring [[ ]] / [=[ ]=]
        if c == "[" and i+1 < n and src[i+1] in "[=":
            j = i+1; eq = 0
            while j < n and src[j] == "=": eq += 1; j += 1
            if j < n and src[j] == "[":
                close = "]" + "="*eq + "]"
                end = src.find(close, j+1)
                if end < 0: errors.append((line, "unterminierter Langstring")); break
                line += src[i:end].count("\n"); i = end + len(close); continue
        # Quote-Strings
        if c in "\"'":
            q = c; j = i+1
            while j < n:
                if src[j] == "\\": j += 2; continue
                if src[j] == "\n":
                    errors.append((line, f"unterminierter String (Zeilenumbruch in {q}...{q})")); break
                if src[j] == q: break
                j += 1
            else:
                errors.append((line, "unterminierter String am Dateiende")); break
            if j < n and src[j] == "\n":
                # String lief in die nächste Zeile → wahrscheinl. innerer Quote
                i = j; continue
            i = j+1; continue
        # Klammern
        if c in "([{":
            stack.append((c, line))
        elif c in ")]}":
            if not stack or stack[-1][0] != pairs[c]:
                errors.append((line, f"unerwartetes '{c}'"))
            else:
                stack.pop()
        i += 1
    for ch, ln in stack:
        errors.append((ln, f"nicht geschlossenes '{ch}'"))
    return errors

ok = True
for path in sys.argv[1:]:
    errs = check_lua(path) + check_reserved_keys(path) + check_long_string_concat(path) + check_format_specifiers(path)
    errs.sort()
    if errs:
        ok = False
        for ln, msg in errs[:5]:
            print(f"  ⚠ {path}:{ln}: {msg}")
    else:
        print(f"  ✓ {path}")
sys.exit(0 if ok else 1)

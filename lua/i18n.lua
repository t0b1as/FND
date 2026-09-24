-- =============================================================================
--  i18n.lua – Internationalisierung für Fundus Marketplace
--
--  Verwendung in Seiten:
--    local i18n = require "i18n"
--    local t    = i18n.init()   -- erkennt Sprache aus Header / Cookie
--    ngx.print(t("nav.market"))
--
--  Neue Sprache hinzufügen:
--    1. locales/<code>.lua anlegen (de.lua als Vorlage)
--    2. SUPPORTED_LANGS unten erweitern
--    3. Fertig – Fallback auf "de" ist automatisch aktiv
-- =============================================================================

local cjson = require "cjson.safe"
local dotted_get  -- forward declaration (lokal, vermeidet globale Variable)
local _M    = {}

-- ---------------------------------------------------------------------------
--  Konfiguration
-- ---------------------------------------------------------------------------

-- Alle unterstützten Sprachcodes (ISO 639-1).
-- Reihenfolge bestimmt die Anzeigereihenfolge im Sprachmenü.
_M.SUPPORTED = { "de", "en" }

-- Standardsprache wenn keine passende erkannt wird.
_M.DEFAULT = "de"

-- Cookie-Name für manuell gewählte Sprache.
local LANG_COOKIE = "fundus_lang"

-- ---------------------------------------------------------------------------
--  Sprache erkennen
-- ---------------------------------------------------------------------------

-- Gibt den aktiven Sprachcode zurück (z.B. "de" oder "en").
-- Priorität: Cookie > Accept-Language-Header > Default
function _M.detect()
    -- 1) Cookie
    local cookie_str = ngx.var.http_cookie or ""
    for pair in cookie_str:gmatch("[^;]+") do
        local k, v = pair:match("^%s*([^=]+)=(.+)$")
        if k and k:gsub("%s","") == LANG_COOKIE then
            local lang = v:gsub("%s",""):lower():sub(1, 5)
            if _M.is_supported(lang) then
                return lang
            end
        end
    end

    -- 2) Accept-Language Header
    -- Format: "de-DE,de;q=0.9,en-US;q=0.8,en;q=0.7"
    local accept = ngx.var.http_accept_language or ""
    for tag in accept:gmatch("[a-zA-Z%-]+") do
        local code = tag:lower():sub(1,2)
        if _M.is_supported(code) then
            return code
        end
    end

    -- 3) Fallback
    return _M.DEFAULT
end

-- Prüft ob ein Sprachcode unterstützt wird.
function _M.is_supported(code)
    for _, lang in ipairs(_M.SUPPORTED) do
        if lang == code then return true end
    end
    return false
end

-- ---------------------------------------------------------------------------
--  Locale laden
-- ---------------------------------------------------------------------------

-- Cache damit jede Locale-Datei nur einmal geladen wird.
local _cache = {}

-- Lädt die Locale-Tabelle für einen Sprachcode.
-- Fällt auf DEFAULT zurück wenn die Datei fehlt.
local function load_locale(lang)
    if _cache[lang] then return _cache[lang] end

    local ok, tbl = pcall(require, "locales." .. lang)
    if not ok or type(tbl) ~= "table" then
        ngx.log(ngx.WARN, "i18n: locale '", lang, "' nicht ladbar: ", tostring(tbl))
        if lang ~= _M.DEFAULT then
            return load_locale(_M.DEFAULT)
        end
        return {}
    end

    _cache[lang] = tbl
    return tbl
end

-- ---------------------------------------------------------------------------
--  Übersetzungsfunktion
-- ---------------------------------------------------------------------------

-- init() erkennt die Sprache und gibt eine t()-Funktion zurück.
-- Optionaler Parameter: lang (überschreibt automatische Erkennung).
--
-- t("key")              → einfacher String
-- t("key", {name="X"}) → String mit Platzhaltern: "Hallo %{name}"
-- t("key", 3)           → Pluralform: gibt key_plural wenn n != 1
function _M.init(lang_override)
    local lang    = lang_override or _M.detect()
    local strings = load_locale(lang)
    local default = (lang ~= _M.DEFAULT) and load_locale(_M.DEFAULT) or strings

    -- Aktuellen Sprachcode im ngx-Context speichern (für render.lua nutzbar)
    ngx.ctx.lang = lang

    -- t() – Übersetzungsfunktion
    local function t(key, params)
        -- Dotted keys: "nav.market" → strings.nav.market
        local val = dotted_get(strings, key)
                 or dotted_get(default, key)
                 or key  -- Fallback: Key selbst anzeigen

        -- Pluralform: t("items", 3) wählt "items_plural" wenn n != 1
        if type(params) == "number" then
            if params ~= 1 then
                local plural = dotted_get(strings, key .. "_plural")
                            or dotted_get(default, key .. "_plural")
                if plural then val = plural end
            end
            params = { n = params }
        end

        -- Platzhalter ersetzen: %{name} → params.name
        if type(params) == "table" then
            val = val:gsub("%%{(%w+)}", function(k)
                return tostring(params[k] or "")
            end)
        end

        return val
    end

    return t, lang
end

-- ---------------------------------------------------------------------------
--  Sprachmenü-HTML
-- ---------------------------------------------------------------------------

-- Gibt ein Drawer-Untermenü (Links) für die Sprachauswahl zurück.
-- Jede Sprache ist ein Eintrag; die aktive ist markiert. Beim Klick wird das
-- Cookie gesetzt und die Seite neu geladen (switchLang, siehe unten).
function _M.lang_menu_html(current_lang)
    local labels = { de = "Deutsch", en = "English", fr = "Français",
                     es = "Español", it = "Italiano", pl = "Polski" }
    local flags  = { de = "🇩🇪", en = "🇬🇧", fr = "🇫🇷",
                     es = "🇪🇸", it = "🇮🇹", pl = "🇵🇱" }
    local parts = {}
    for _, code in ipairs(_M.SUPPORTED) do
        local active = (code == current_lang) and " active" or ""
        local label  = labels[code] or code:upper()
        local flag   = flags[code] or "🌐"
        table.insert(parts, string.format(
            '<a href="#" class="lang-item%s" onclick="switchLang(\'%s\');return false;">'
            .. '<span class="di">%s</span>%s</a>',
            active, code, flag, label
        ))
    end
    table.insert(parts, [[
<script>
function switchLang(code) {
    document.cookie = "fundus_lang=" + code + "; path=/; max-age=31536000; SameSite=Lax";
    location.reload();
}
</script>]])
    return table.concat(parts)
end

-- Gibt ein <select>-Element für die Sprachauswahl zurück.
-- Beim Wechsel wird /lang?set=<code>&back=<uri> aufgerufen.
function _M.switcher_html(current_lang)
    local parts = { '<select class="lang-switcher" onchange="switchLang(this.value)">' }
    local labels = { de = "Deutsch", en = "English", fr = "Français",
                     es = "Español", it = "Italiano", pl = "Polski" }
    for _, code in ipairs(_M.SUPPORTED) do
        local selected = (code == current_lang) and ' selected' or ''
        local label    = labels[code] or code:upper()
        table.insert(parts, string.format(
            '<option value="%s"%s>%s</option>', code, selected, label
        ))
    end
    table.insert(parts, '</select>')
    table.insert(parts, [[
<script>
function switchLang(code) {
    document.cookie = "fundus_lang=" + code + "; path=/; max-age=31536000; SameSite=Lax";
    location.reload();
}
</script>]])
    return table.concat(parts)
end

-- ---------------------------------------------------------------------------
--  Hilfsfunktion: Punkt-Notation für verschachtelte Tabellen
-- ---------------------------------------------------------------------------

dotted_get = function(tbl, key)
    if type(tbl) ~= "table" then return nil end
    local val = tbl
    for part in key:gmatch("[^%.]+") do
        if type(val) ~= "table" then return nil end
        val = val[part]
    end
    if type(val) == "string" then return val end
    return nil
end

return _M

-- =============================================================================
--  lang.lua – Sprachauswahl-Endpoint
--  GET /lang?set=en&back=/listings
--  Setzt Cookie und leitet weiter.
-- =============================================================================

local i18n = require "i18n"

local lang = ngx.var.arg_set or ""
-- R819: entschlüsseln – ngx.var.arg_* liefert den Wert roh. Bei einfachen
-- Pfaden (/listings) fiel das nie auf; ein Ziel mit eigenem Parameter
-- (/tos?back=/feed) kam doppelt kodiert an.
local back = ngx.unescape_uri(ngx.var.arg_back or "/")
-- Nur Ziele auf DIESER Seite. Vorher leitete /lang auf jedes Ziel weiter,
-- auch auf fremde Seiten ("//beispiel.org", "https://…") – ein offener
-- Weiterleiter, mit dem sich Links auf Fundus tarnen ließen.
if back:sub(1, 1) ~= "/" or back:sub(1, 2) == "//" or back:find("[\r\n\\]") then
    back = "/"
end

-- Nur gültige Codes akzeptieren
if not i18n.is_supported(lang) then
    lang = i18n.DEFAULT
end

-- Cookie setzen (1 Jahr)
ngx.header["Set-Cookie"] = "fundus_lang=" .. lang ..
    "; Path=/; Max-Age=31536000; SameSite=Lax"

ngx.redirect(back, 302)

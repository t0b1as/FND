-- =============================================================================
--  lang.lua – Sprachauswahl-Endpoint
--  GET /lang?set=en&back=/listings
--  Setzt Cookie und leitet weiter.
-- =============================================================================

local i18n = require "i18n"

local lang = ngx.var.arg_set or ""
local back = ngx.var.arg_back or "/"

-- Nur gültige Codes akzeptieren
if not i18n.is_supported(lang) then
    lang = i18n.DEFAULT
end

-- Cookie setzen (1 Jahr)
ngx.header["Set-Cookie"] = "fundus_lang=" .. lang ..
    "; Path=/; Max-Age=31536000; SameSite=Lax"

ngx.redirect(back, 302)

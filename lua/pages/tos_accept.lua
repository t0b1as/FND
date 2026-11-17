-- =============================================================================
--  tos_accept.lua – Nutzungsbedingungen akzeptieren (POST-Handler)
--  Wird von nginx via "location = /tos/accept { content_by_lua_file ... }"
--  DIREKT ausgefuehrt (nicht ueber den app.lua-Router). Daher laeuft die
--  Logik hier im main chunk - das ist in diesem Kontext korrekt und sicher,
--  weil content_by_lua_file einen eigenen Request-Kontext hat (kein require).
-- =============================================================================

local tos = require "pages.tos"

-- POST-Body lesen
ngx.req.read_body()
local args = ngx.req.get_post_args()
local back = args["back"] or "/"
-- Das Formular trägt das Ziel URL-kodiert ("%2Flistings") – zurückwandeln,
-- sonst landete man nach dem Akzeptieren immer auf der Startseite.
if type(back) == "string" and back:sub(1, 1) == "%" then back = ngx.unescape_uri(back) end
if type(back) ~= "string" then back = "/" end

-- Nur sichere relative Redirect-Ziele erlauben
if not back:match("^/[^/]") and back ~= "/" then
    back = "/"
end

-- Cookie setzen (1 Jahr). KEIN HttpOnly noetig - wird serverseitig gelesen,
-- aber HttpOnly schadet nicht. SameSite=Lax erlaubt den Redirect-Flow.
ngx.header["Set-Cookie"] = tos.COOKIE .. "=" .. tos.VERSION ..
    "; Path=/; Max-Age=31536000; SameSite=Lax"

-- R930: Angemeldet → die Zustimmung beim KONTO vermerken. Hier – nicht im
-- app.lua-Router – kommt das Formular an (nginx: location = /tos/accept).
-- Fehlte das, blieb die Zustimmung ungespeichert und der Guard schickte
-- nach dem Akzeptieren wieder auf /tos.
local render = require "render"
if render.sitzung() then
    local _, err, st = render.api_post("/v1/konto/tos", { version = tos.VERSION })
    if st == 200 then
        render.tos_gemerkt(tos.VERSION)
    else
        ngx.log(ngx.ERR, "TOS-Zustimmung nicht gespeichert: ", tostring(st), " ", tostring(err))
    end
end

return ngx.redirect(back, 303)

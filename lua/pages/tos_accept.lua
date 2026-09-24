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

-- Nur sichere relative Redirect-Ziele erlauben
if not back:match("^/[^/]") and back ~= "/" then
    back = "/"
end

-- Cookie setzen (1 Jahr). KEIN HttpOnly noetig - wird serverseitig gelesen,
-- aber HttpOnly schadet nicht. SameSite=Lax erlaubt den Redirect-Flow.
ngx.header["Set-Cookie"] = tos.COOKIE .. "=" .. tos.VERSION ..
    "; Path=/; Max-Age=31536000; SameSite=Lax"

return ngx.redirect(back, 303)

-- =============================================================================
--  Fundus Marketplace – Lua Frontend Router
--  /opt/fundus/lua/app.lua
-- =============================================================================

-- package.path absichern, damit require auch Unterverzeichnisse (locales/, pages/)
-- findet - unabhaengig von der nginx lua_package_path-Konfiguration.
local FUNDUS_LUA = "/opt/fundus/lua"
if not package.path:find(FUNDUS_LUA, 1, true) then
    package.path = FUNDUS_LUA .. "/?.lua;" .. FUNDUS_LUA .. "/?/init.lua;" .. package.path
end

local router = require "router"

-- Remote-Modus: Ist das Cookie fundus_remote=<PeerID> gesetzt, werden alle
-- Seiten vom entfernten Node über den P2P-Tunnel geholt. So funktionieren alle
-- Links der fremden Seiten. Beenden über /api/v1/remote/exit.
do
    local rp = ngx.var.cookie_fundus_remote
    if rp and #rp >= 40 and #rp <= 64 and rp:match("^[1-9A-HJ-NP-Za-km-z]+$") then
        return ngx.exec("/api/v1/proxy/" .. rp .. ngx.var.uri, ngx.var.args)
    end
end

local routes = {
    -- Nutzungsbedingungen
    { method = "GET",  pattern = "^/tos$",
      handler = function()
          local tos = require "pages.tos"
          tos.render()
      end },

    -- TOS akzeptieren: setzt den Cookie und leitet zurück. OHNE diese Route
    -- läuft der POST ins Leere, der Cookie wird nie gesetzt, und der TOS-Guard
    -- im Header leitet jede Seite endlos auf /tos zurück.
    { method = "POST", pattern = "^/tos/accept$",
      handler = function()
          local tos = require "pages.tos"
          ngx.header["Set-Cookie"] = tos.COOKIE .. "=" .. tos.VERSION ..
              "; Path=/; Max-Age=31536000; SameSite=Lax"
          -- back-Ziel absichern: nur interne Pfade (beginnt mit einem einzelnen
          -- "/", kein "//" — sonst Open-Redirect auf fremde Hosts).
          ngx.req.read_body()
          local args = ngx.req.get_post_args() or {}
          local back = args.back or "/"
          if type(back) ~= "string" or back:sub(1,1) ~= "/" or back:sub(1,2) == "//" then
              back = "/"
          end
          return ngx.redirect(back, 302)
      end },

    -- Startseite
    { method = "GET",  pattern = "^/$",
      handler = require "pages.home" },

    -- Markt
    { method = "GET",  pattern = "^/listings$",
      handler = require "pages.listings" },
    { method = "GET",  pattern = "^/listings/new$",
      handler = require "pages.listing_new" },
    { method = "GET",  pattern = "^/listings/([^/]+)/edit$",
      handler = require "pages.listing_edit" },
    { method = "GET",  pattern = "^/listings/([^/]+)$",
      handler = require "pages.listing_show" },

    -- Energie-Token
    { method = "GET",  pattern = "^/energy$",
      handler = require "pages.energy" },

    -- Zertifikate
    { method = "GET",  pattern = "^/certificates$",
      handler = require "pages.certificates" },
    { method = "GET",  pattern = "^/certificates/new$",
      handler = require "pages.certificate_new" },
    { method = "GET",  pattern = "^/certificates/([^/]+)$",
      handler = require "pages.certificate_show" },

    -- Jobs
    { method = "GET",  pattern = "^/jobs$",
      handler = require "pages.jobs" },
    { method = "GET",  pattern = "^/jobs/new$",
      handler = require "pages.job_new" },
    { method = "GET",  pattern = "^/jobs/([^/]+)$",
      handler = require "pages.job_show" },

    -- FND Shop
    { method = "GET", pattern = "^/shop$",
      handler = require "pages.shop" },
    { method = "GET", pattern = "^/swaptest$",
      handler = require "pages.swaptest" },

    -- Messenger
    { method = "GET", pattern = "^/messenger$",
      handler = require "pages.messenger" },

    -- Partner (privacy-first)
    { method = "GET",  pattern = "^/partner$",
      handler = require "pages.partner" },

    -- Netzwerk
    { method = "GET",  pattern = "^/peers$",
      handler = require "pages.peers" },

    -- Netz-Topologie: Node-Profil, Verbindungen, Routing
    { method = "GET",  pattern = "^/topology$",
      handler = require "pages.topology" },

    -- Dezentraler Filemanager
    { method = "GET",  pattern = "^/files$",
      handler = require "pages.files" },

    -- Wallet & Identität
    { method = "GET",  pattern = "^/wallet$",
      handler = require "pages.wallet" },

    -- Node-Einstellungen (Meter, NodeType, GPS, Storage)
    { method = "GET",  pattern = "^/settings$",
      handler = require "pages.settings" },

    -- Admin-Panel
    { method = "GET",  pattern = "^/admin$",
      handler = require "pages.admin" },

    -- Interner Health-Check (kein i18n nötig)
    { method = "GET",  pattern = "^/health$",
      handler = function() ngx.header["Content-Type"] = "application/json"; ngx.say('{"status":"ok"}') end },
}

router.dispatch(routes)

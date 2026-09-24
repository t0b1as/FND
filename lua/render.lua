-- =============================================================================
--  render.lua – HTML-Layout-Helfer, API-Client, i18n-Integration
-- =============================================================================

local cjson = require "cjson.safe"

-- cjson decodiert JSON null als cjson.null (ein userdata-Sentinel, NICHT nil).
-- Das bricht spaeter #tbl und ipairs(). Dieser Scrubber ersetzt alle null-Werte
-- rekursiv durch nil, sodass "x or {}" und #x wieder zuverlaessig funktionieren.
local cjson_null = cjson.null
local function scrub_null(v)
    if v == cjson_null then return nil end
    if type(v) == "table" then
        for k, val in pairs(v) do
            v[k] = scrub_null(val)
        end
    end
    return v
end
local i18n  = require "i18n"
local _M    = {}

-- ---------------------------------------------------------------------------
--  API-Hilfsfunktionen
-- ---------------------------------------------------------------------------

-- Interner HTTP-Client via ngx.socket.tcp (funktioniert in OpenResty UND
-- Debian libnginx-mod-http-lua; ngx.location.capture gibt es nur in OpenResty).
local API_HOST = "127.0.0.1"
local API_PORT = 3000

local function http_request(method, path, body)
    local sock = ngx.socket.tcp()
    sock:settimeout(3000) -- 3s (localhost-Backend; früher 30s → Seiten hingen bei Backend-Last)
    local ok, err = sock:connect(API_HOST, API_PORT)
    if not ok then
        return nil, "connect failed: " .. (err or "?"), 0
    end

    local headers = {
        method .. " /api" .. path .. " HTTP/1.1",
        "Host: " .. API_HOST,
        "Connection: close",
    }
    -- Session-Cookie des Browsers weiterreichen, damit interne API-Anfragen
    -- (z.B. Partnerprofil laden beim Seitenaufbau) als eingeloggt gelten.
    -- Kam die Seite über den P2P-Tunnel (nur von 127.0.0.1 glaubwürdig),
    -- die Kennung weiterreichen: sonst gälten interne Abrufe als Betreiber.
    local tun = ngx.var.http_x_fundus_tunnel
    if tun and tun ~= "" and ngx.var.remote_addr == "127.0.0.1" then
        table.insert(headers, "X-Fundus-Tunnel: " .. tun)
    end
    local cookie = ngx.var.http_cookie
    if cookie and cookie ~= "" then
        table.insert(headers, "Cookie: " .. cookie)
    end
    if body then
        table.insert(headers, "Content-Type: application/json")
        table.insert(headers, "Content-Length: " .. #body)
    end
    local req = table.concat(headers, "\r\n") .. "\r\n\r\n" .. (body or "")

    local _, serr = sock:send(req)
    if serr then
        sock:close()
        return nil, "send failed: " .. serr, 0
    end

    -- Statuszeile lesen
    local status_line, rerr = sock:receive("*l")
    if not status_line then
        sock:close()
        return nil, "receive failed: " .. (rerr or "?"), 0
    end
    local status = tonumber(status_line:match("HTTP/%d%.%d (%d+)")) or 0

    -- Header lesen bis Leerzeile, Content-Length + chunked erkennen
    local content_length, chunked = nil, false
    while true do
        local line = sock:receive("*l")
        if not line or line == "" then break end
        local cl = line:match("[Cc]ontent%-[Ll]ength:%s*(%d+)")
        if cl then content_length = tonumber(cl) end
        if line:match("[Tt]ransfer%-[Ee]ncoding:%s*chunked") then chunked = true end
    end

    -- Body lesen
    local resp_body = ""
    if chunked then
        while true do
            local size_line = sock:receive("*l")
            if not size_line then break end
            local size = tonumber(size_line, 16)
            if not size or size == 0 then break end
            local chunk = sock:receive(size)
            resp_body = resp_body .. (chunk or "")
            sock:receive("*l") -- trailing CRLF
        end
    elseif content_length and content_length > 0 then
        resp_body = sock:receive(content_length) or ""
    end

    sock:close()
    return resp_body, nil, status
end

function _M.api_get(path)
    local body, err, status = http_request("GET", path, nil)
    if err then return nil, err end
    if status ~= 200 then return nil, "API error " .. status end
    local data, derr = cjson.decode(body)
    if data == nil then
        return nil, "JSON decode error: " .. (derr or "unknown")
    end
    return scrub_null(data), nil
end

function _M.api_post(path, payload)
    local body = cjson.encode(payload)
    local resp, err, status = http_request("POST", path, body)
    if err then return nil, err, status end
    local data, derr = cjson.decode(resp)
    return scrub_null(data), derr, status
end

-- ---------------------------------------------------------------------------
--  HTML-Layout
-- ---------------------------------------------------------------------------

-- header(title_key, active) → gibt t()-Funktion zurück
-- title_key kann ein i18n-Key sein ("home.title") oder ein Literal-String.

-- read_revision() liest den Build-Revisionsstand aus /opt/fundus/revision.txt
-- (bei jeder ZIP-Generierung erhöht) und gibt ihn als "Rnnn" zurück. Fehlt die
-- Datei, wird nichts angezeigt.
local function read_revision()
    -- Höchste Revision aus beiden Dateien: lua/revision.txt kommt bei jedem
    -- Frontend-Deploy mit, die Datei im Wurzelverzeichnis nur beim vollen
    -- Deploy. So zeigt die Kopfzeile nie einen veralteten Stand an.
    local best = 0
    for _, p in ipairs({ "/opt/fundus/revision.txt", "/opt/fundus/lua/revision.txt" }) do
        local f = io.open(p, "r")
        if f then
            local v = tonumber(((f:read("*l") or ""):gsub("%s+", "")))
            f:close()
            if v and v > best then best = v end
        end
    end
    return best > 0 and ("R" .. best) or ""
end

-- rev() liefert die Build-Revision (für Cache-Busting von /static-Dateien).
function _M.rev()
    return read_revision()
end

function _M.header(title_key, active)
    active = active or ""
    local t, lang = i18n.init()
    local rev = read_revision()

    -- TOS-Guard: beim ersten Aufruf auf /tos weiterleiten
    -- Ausnahme: /tos selbst und /static/ werden nicht geprüft
    local uri = ngx.var.uri or ""
    if uri ~= "/tos" and not uri:match("^/tos/") and not uri:match("^/static/") then
        local tos = require "pages.tos"
        if not tos.accepted() then
            -- Aktuelle URL als Back-Parameter mitgeben
            local back = uri
            if ngx.var.args and ngx.var.args ~= "" then
                back = back .. "?" .. ngx.var.args
            end
            return ngx.redirect("/tos?back=" .. ngx.escape_uri(back), 302)
        end
    end

    ngx.print(string.format([[
<!DOCTYPE html>
<html lang="%s">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <link rel="stylesheet" href="/static/style.css?v=]] .. rev .. [[">
  <link rel="icon" type="image/svg+xml" href="/static/favicon.svg?v=]] .. rev .. [[">
  <link rel="manifest" href="/static/manifest.json">
  <meta name="theme-color" content="#00e676">
  <meta name="mobile-web-app-capable" content="yes">
  <meta name="apple-mobile-web-app-capable" content="yes">
  <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">
  <meta name="apple-mobile-web-app-title" content="FUNDUS">
  <link rel="apple-touch-icon" href="/static/icon.svg?v=]] .. rev .. [[">
  <script>
  // fundusMe(): EINE gemeinsame /identity/me-Abfrage pro Seite (vorher 3-4x).
  // Liefert die Session-Daten oder null. force=true nach Login/Logout.
  window.fundusMe = function(force){
    if (force || !window.__meP) {
      window.__meP = fetch('/api/v1/identity/me', {credentials:'same-origin'})
        .then(function(r){ return r.ok ? r.json() : null; })
        .catch(function(){ return null; });
    }
    return window.__meP;
  };
  // Hinweis bei http: Browser sperren dort Kamera/Mikrofon (Anrufe) und weitere
  // Funktionen. Ausgenommen: Zugriff direkt auf dem Node (localhost).
  document.addEventListener('DOMContentLoaded', function(){
    var h = location.hostname;
    if (window.isSecureContext || h === 'localhost' || h === '127.0.0.1') return;
    try { if (sessionStorage.getItem('fnd_http_hint') === '0') return; } catch(e){}
    var b = document.createElement('div');
    b.className = 'http-hint';
    b.innerHTML = '&#9888;&#65039; Unverschl&uuml;sselte Verbindung: Anrufe und Kamera funktionieren nur &uuml;ber HTTPS. ' +
      '<a href="https://' + location.host + location.pathname + location.search + '">Zu HTTPS wechseln</a>' +
      ' <button type="button" aria-label="Schlie&szlig;en">&times;</button>';
    b.querySelector('button').onclick = function(){ b.remove(); try { sessionStorage.setItem('fnd_http_hint','0'); } catch(e){} };
    document.body.insertBefore(b, document.body.firstChild);
  });
  </script>
  <script src="/static/navvis.js?v=]] .. rev .. [["></script>
  <script src="/static/walletauth.js?v=]] .. rev .. [[" defer></script>
  <script src="/static/msgnotify.js?v=]] .. rev .. [[" defer></script>
</head>
<body class="%s">
<nav>
  <a href="/" class="brand">
    <svg class="brand-logo" viewBox="0 0 200 200" aria-hidden="true">
      <path fill="currentColor" fill-rule="evenodd" d="M74.66 9.68A59.331 59.331 0 0 0 40.72 65.78A59.331 59.331 0 0 0 9.11 123.23A93.806 93.806 0 0 1 74.66 9.68ZM80.05 8.35A93.806 93.806 0 0 1 119.94 8.34A48.000 48.000 0 0 1 147.14 61.04A59.331 59.331 0 0 0 100.00 68.22A59.331 59.331 0 0 0 52.86 61.04A48.000 48.000 0 0 1 80.05 8.35ZM15.42 145.42A48.000 48.000 0 0 0 16.99 148.24A59.331 59.331 0 0 0 18.66 151.00A96.000 96.000 0 0 0 181.05 151.46A59.331 59.331 0 0 0 182.73 148.70A48.000 48.000 0 0 0 184.32 145.90A96.000 96.000 0 0 0 105.10 4.14A59.331 59.331 0 0 0 101.88 4.02A48.000 48.000 0 0 0 98.65 4.02A59.331 59.331 0 0 0 95.43 4.12A96.000 96.000 0 0 0 15.42 145.42ZM58.43 76.01A48.000 48.000 0 0 1 100.00 100.01A48.000 48.000 0 0 1 141.56 76.01A48.000 48.000 0 0 1 100.00 100.01A48.000 48.000 0 0 1 99.99 148.01A48.000 48.000 0 0 1 100.00 100.01A48.000 48.000 0 0 1 58.43 76.01ZM42.68 78.66A59.331 59.331 0 0 0 72.47 115.90A59.331 59.331 0 0 0 89.82 160.31A48.000 48.000 0 0 1 30.59 163.11A93.806 93.806 0 0 1 10.64 128.57A48.000 48.000 0 0 1 42.68 78.66ZM34.45 167.10A59.331 59.331 0 0 0 100.00 168.46A59.331 59.331 0 0 0 165.55 167.11A93.806 93.806 0 0 1 34.45 167.10ZM169.40 163.11A48.000 48.000 0 0 1 110.17 160.32A59.331 59.331 0 0 0 127.52 115.90A59.331 59.331 0 0 0 157.32 78.66A48.000 48.000 0 0 1 189.35 128.56A93.806 93.806 0 0 1 169.40 163.11ZM190.88 123.23A59.331 59.331 0 0 0 159.28 65.79A59.331 59.331 0 0 0 125.33 9.69A93.806 93.806 0 0 1 190.88 123.23Z"/>
      <circle cx="100" cy="100" r="92.50" fill="none" stroke="currentColor" stroke-width="7"/>
    </svg>
    <span>FUNDUS</span>]] .. (rev ~= "" and ('<span class="brand-rev">' .. rev .. '</span>') or "") .. [[
  </a>
  <div class="nav-spacer"></div>
  <div class="nav-quick" id="nav-quick"></div>
  <div class="nav-spacer"></div>
  <div class="wallet-badge" id="wallet-badge"></div>
  <button class="burger" id="burger" aria-label="Menu" onclick="toggleDrawer()">
    <span></span><span></span><span></span>
  </button>
</nav>
<script>try{window.navApply&&window.navApply();}catch(e){}</script>

<div class="drawer-overlay" id="overlay" onclick="toggleDrawer()"></div>
<div class="drawer" id="drawer">
  <div class="drawer-section">]] .. t("nav.sec_marketplace") .. [[</div>
  <a href="/listings"     class="%s" data-nav-key="listings" data-nav-zone="burger"><span class="di">🛒</span>]] .. t("nav.market") .. [[</a>
  <a href="/energy"       class="%s" data-nav-key="energy" data-nav-zone="burger"><span class="di">⚡</span>]] .. t("nav.energy") .. [[<span class="wip-pill">BETA</span></a>
  <a href="/certificates" class="%s" data-nav-key="certificates" data-nav-zone="burger"><span class="di">📜</span>]] .. t("nav.certificates") .. [[<span class="wip-pill">BETA</span></a>
  <a href="/shop"         class="%s" data-nav-key="shop" data-nav-zone="burger"><span class="di">💰</span>]] .. t("nav.shop") .. [[</a>
  <div class="drawer-sep"></div>
  <div class="drawer-section">]] .. t("nav.sec_work") .. [[</div>
  <a href="/jobs"     class="%s" data-nav-key="jobs" data-nav-zone="burger"><span class="di">💼</span>]] .. t("nav.jobs") .. [[<span class="wip-pill">BETA</span></a>
  <a href="/files"    class="%s" data-nav-key="files" data-nav-zone="burger"><span class="di">📁</span>]] .. t("nav.files_label") .. [[</a>
  <div class="drawer-sep"></div>
  <div class="drawer-section">]] .. t("nav.sec_communication") .. [[</div>
  <a href="/messenger" class="%s" data-nav-key="messenger" data-nav-zone="burger"><span class="di">💬</span>]] .. t("nav.messenger") .. [[<span class="wip-pill">BETA</span></a>
  <a href="/partner"   class="%s" data-nav-key="partner" data-nav-zone="burger" style="color:var(--rose,#ff6b9d)"><span class="di">💗</span>]] .. t("nav.partner_label") .. [[</a>
  <div class="drawer-sep"></div>
  <div class="drawer-section">]] .. t("nav.sec_infrastructure") .. [[</div>
  <a href="/topology" class="%s"><span class="di">🗺</span>]] .. t("nav.topology_label") .. [[</a>
  <a href="/peers"    class="%s"><span class="di">🌐</span>]] .. t("nav.peers") .. [[</a>
  <div class="drawer-sep"></div>
  <div class="drawer-section">]] .. t("nav.sec_account") .. [[</div>
  <a href="/wallet"   class="%s" data-nav-key="wallet" data-nav-zone="burger"><span class="di">👛</span>]] .. t("nav.wallet") .. [[</a>
  <a href="/settings" class="%s"><span class="di">⚙</span>]] .. t("nav.settings") .. [[</a>
  <a href="/admin"    class="%s"><span class="di">🔧</span>]] .. t("nav.admin") .. [[</a>
  <div class="drawer-sep"></div>
  <div class="drawer-section">]] .. t("nav.sec_language") .. [[</div>
  ]] .. i18n.lang_menu_html(lang) .. [[
</div>

<main>
]],
        lang,
        t("nav.brand"),
        (active == "" and "home" or ""),
        -- Drawer: active classes for each link
        active == "listings"     and "active" or "",
        active == "energy"       and "active" or "",
        active == "certificates" and "active" or "",
        active == "shop"         and "active" or "",
        active == "jobs"         and "active" or "",
        active == "files"        and "active" or "",
        active == "messenger"    and "active" or "",
        active == "partner"      and "active" or "",
        active == "topology"     and "active" or "",
        active == "peers"        and "active" or "",
        active == "wallet"       and "active" or "",
        active == "settings"     and "active" or "",
        active == "admin"        and "active" or ""
    ))

    return t
end

function _M.footer()
    local t = i18n.init()
    ngx.print(string.format([[
</main>
<footer><small><a href="/tos" class="footer-link">%s</a> &middot; <a href="/shop#spenden" class="footer-link">💚 Fundus unterstützen</a></small></footer>
<script>
function toggleDrawer(){
  const open=document.getElementById("drawer").classList.toggle("open");
  document.getElementById("burger").classList.toggle("open",open);
  document.getElementById("overlay").classList.toggle("open",open);
}
window.addEventListener("resize",function(){
  if(window.innerWidth>900){
    ["drawer","overlay"].forEach(function(id){document.getElementById(id).classList.remove("open");});
    document.getElementById("burger").classList.remove("open");
  }
});
// PWA: Service Worker registrieren (macht die Seite installierbar). Der SW liegt
// unter /sw.js im Root-Scope, damit er die ganze App kontrolliert. Er cacht nur
// statische Assets — Live-Daten gehen immer ans Netz.
if ("serviceWorker" in navigator) {
  window.addEventListener("load", function(){
    navigator.serviceWorker.register("/sw.js").catch(function(){});
  });
}
</script>
<script src="/static/sodium.js"></script>
<script src="/static/crypto.js?v=%s"></script>
<script src="/static/progress.js?v=%s"></script>
</body></html>
]], t("footer.tagline"), os.time(), os.time()))
end

function _M.error_page(title, detail)
    local t = _M.header(title)
    ngx.print("<p class='error'>" .. ngx.escape_uri(detail or "") .. "</p>")
    _M.footer()
    return t
end

-- record_table(records, columns) → HTML-String
function _M.record_table(records, columns)
    -- records kann nil, cjson.null (userdata) oder eine leere/volle Tabelle sein.
    -- Nur echte Tabellen mit Eintraegen rendern.
    if type(records) ~= "table" or #records == 0 then
        local t = i18n.init()
        return "<p class='empty'>" .. t("general.no_entries") .. "</p>"
    end
    local rows, body_rows = {}, {}
    local headers = {}
    for _, col in ipairs(columns) do
        table.insert(headers, "<th>" .. col.label .. "</th>")
    end
    table.insert(rows, "<thead><tr>" .. table.concat(headers) .. "</tr></thead>")
    for _, record in ipairs(records) do
        local cells = {}
        for _, col in ipairs(columns) do
            local val = ""
            if col.key == "id" then
                val = string.format('<a href="%s/%s">%s…</a>',
                    ngx.var.uri, record.id, tostring(record.id):sub(1,8))
            elseif col.key:find("^data%.") then
                val = tostring((record.data or {})[col.key:sub(6)] or "")
            else
                val = tostring(record[col.key] or "")
            end
            table.insert(cells, "<td data-label='" .. col.label .. "'>" .. val .. "</td>")
        end
        table.insert(body_rows, "<tr>" .. table.concat(cells) .. "</tr>")
    end
    return "<table><tbody>" .. table.concat(rows) .. table.concat(body_rows) .. "</tbody></table>"
end

-- html_escape: schützt vor XSS in HTML-Attributen und Textknoten.
-- ngx.escape_uri ist URL-Encoding, nicht HTML-Encoding!
local function html_escape(s)
    if not s then return "" end
    s = tostring(s)
    s = s:gsub("&",  "&amp;")
    s = s:gsub("<",  "&lt;")
    s = s:gsub(">",  "&gt;")
    s = s:gsub('"',  "&quot;")
    s = s:gsub("'",  "&#39;")
    return s
end
_M.html_escape = html_escape

return _M

-- Lastverteilung auf mehrere Pis (R669)
--
-- Der öffentlich erreichbare Node (derzeit der 71er) liefert die Oberfläche
-- selbst aus und reicht die SCHNITTSTELLE an einen der Pis weiter. Er selbst
-- ist dabei einer der Empfänger, trägt also seinen Teil der Last mit.
--
-- Warum die Zuordnung kleben MUSS:
--   Eine Sitzung lebt im Arbeitsspeicher genau des Nodes, der die Anmeldung
--   gerechnet hat, und der Verlauf liegt verschlüsselt auf dessen Platte.
--   Würde jede Anfrage reihum verteilt, wäre man bei jedem zweiten Aufruf
--   abgemeldet. Deshalb bekommt jeder Besucher einen Node zugewiesen und
--   bleibt dort – über ein Jahr hinweg, nicht nur für einen Besuch.
--
-- Was NICHT verteilt wird:
--   * /api/v1/admin/  – verwaltet diesen Pi, nicht irgendeinen
--   * /api/v1/proxy/ und /api/v1/remote/ – die Fernsteuerung selbst
--   * alles, solange die Verteilung nicht eingeschaltet ist

local M = {}

local PEERS_DATEI  = "/opt/fundus/data/balance-peers.txt"
local AUTO_DATEI   = "/opt/fundus/data/balance-peers.auto.txt"  -- Selbsterkennung (R671)
local AN_DATEI     = "/opt/fundus/data/balance.enabled"
-- R672: Voreinstellung ist der verschlüsselte P2P-Tunnel. Nur wenn diese Datei
-- vorhanden ist, wird direkt per HTTP weitergereicht – im eigenen Netz eine
-- vertretbare, aber bewusste Entscheidung. Im Tunnel prüft libp2p die
-- Gegenstelle kryptografisch und verschlüsselt mit Noise; direkt wandern
-- E-Mail und Passwort bei jeder Anmeldung im Klartext über das Netz.
local DIREKT_DATEI = "/opt/fundus/data/balance-direct.enabled"
local COOKIE       = "fnd_node"
local COOKIE_TAGE  = 365
local PRUEF_TAKT   = 30        -- Sekunden zwischen den Erreichbarkeitsprüfungen
local AUSZEIT      = 2         -- Sekunden für die Prüfung selbst

local cache = ngx.shared.fundus_cache

-- liesDatei: kleine Hilfe, gibt nil zurück wenn es sie nicht gibt.
local function liesDatei(pfad)
    local f = io.open(pfad, "r")
    if not f then return nil end
    local inhalt = f:read("*a")
    f:close()
    return inhalt
end

-- aktiv: Ist die Verteilung eingeschaltet? Über eine Datei, damit sie sich im
-- Betrieb umlegen lässt, ohne den Webserver neu zu laden.
function M.aktiv()
    local zwischen = cache:get("bal:aktiv")
    if zwischen ~= nil then return zwischen == 1 end
    local an = liesDatei(AN_DATEI) ~= nil
    cache:set("bal:aktiv", an and 1 or 0, 5)
    return an
end

-- peers: Liste der Empfänger, je Zeile "host:port". Zeilen mit # sind Notizen.
-- peers: Empfänger aus der Selbsterkennung (R671) und, falls vorhanden, aus
-- der von Hand gepflegten Liste. Von Hand eingetragene haben VORRANG: Wer
-- etwas bewusst gesetzt hat, soll nicht von der Erkennung überstimmt werden.
function M.peers()
    local roh = liesDatei(PEERS_DATEI)
    local auto = liesDatei(AUTO_DATEI)
    if not roh and not auto then return {} end
    roh = (roh or "") .. "\n" .. (auto or "")
    local out, kennt = {}, {}
    for zeile in roh:gmatch("[^\r\n]+") do
        local z = zeile:match("^%s*(.-)%s*$")
        if z ~= "" and z:sub(1, 1) ~= "#" then
            -- Nur einfache Host-Port-Angaben zulassen. Sonst stünde hier ein
            -- Einfallstor: Wer die Datei schreiben kann, bestimmt sonst, wohin
            -- die Anfragen samt Sitzungsdaten gehen.
            -- Format: "host:port" oder "host:port gewicht=8" (R670).
            -- Ohne Gewicht gilt 1. Das Gewicht richtet sich nach der Zahl
            -- gleichzeitiger Anmeldungen, die ein Gerät verkraftet – bei
            -- ungleichen Geräten ist eine gleichmäßige Verteilung falsch:
            -- Die kleinen wären am Limit, während das große sich langweilt.
            local adr, rest = z:match("^([%w%.%-]+:%d+)%s*(.*)$")
            if adr and not kennt[adr] then
                kennt[adr] = true          -- doppelte Einträge nur einmal
                local g = tonumber((rest or ""):match("gewicht=(%d+)") or "1") or 1
                if g < 1 then g = 1 end
                if g > 64 then g = 64 end
                -- peer=<Kennung>: nötig für den Tunnel. Ohne sie bleibt nur
                -- der direkte Weg, dann wird dieser Empfänger im Tunnelbetrieb
                -- übersprungen statt unverschlüsselt bedient.
                local peer = (rest or ""):match("peer=([1-9A-HJ-NP-Za-km-z]+)")
                out[#out + 1] = { adresse = adr, gewicht = g, peer = peer }
            end
        end
    end
    return out
end

-- gesund: Erreichbarkeitsstand eines Empfängers aus dem Zwischenspeicher.
local function gesund(peer)
    local v = cache:get("bal:up:" .. peer)
    -- Unbekannt gilt als gesund: Beim ersten Aufruf ist noch nichts geprüft,
    -- und alle als tot zu behandeln wäre der schlechtere Fehlerfall.
    return v == nil or v == 1
end

-- waehle: Empfänger für diesen Besucher bestimmen.
-- Reihum unter den erreichbaren, damit sich die Anmeldungen verteilen.
local function waehle(peers)
    -- Gewichtet reihum: Jeder Empfänger steht so oft im Topf, wie sein Gewicht
    -- angibt. Ein Pi 5 mit Gewicht 8 bekommt damit achtmal so viele neue
    -- Besucher wie ein Pi 3 mit Gewicht 1.
    local topf = {}
    for _, p in ipairs(peers) do
        if gesund(p.adresse) then
            for _ = 1, p.gewicht do topf[#topf + 1] = p.adresse end
        end
    end
    if #topf == 0 then return nil end
    local n = cache:incr("bal:rr", 1, 0) or 1
    return topf[(n % #topf) + 1]
end

-- direkt: Wird unverschlüsselt weitergereicht?
local function direkt()
    local z = cache:get("bal:direkt")
    if z ~= nil then return z == 1 end
    local d = liesDatei(DIREKT_DATEI) ~= nil
    cache:set("bal:direkt", d and 1 or 0, 5)
    return d
end

-- eigen: Zeigt die Adresse auf diesen Node selbst?
local function eigen(adr)
    return adr:find("^127%.0%.0%.1:") ~= nil or adr:find("^localhost:") ~= nil
end

-- zustellen: Legt fest, WIE die Anfrage zum Empfänger kommt.
-- Rückgabe: true, wenn zugestellt werden kann.
local function zustellen(eintrag, uri)
    -- Eigener Node: nichts umschreiben, bleibt lokal.
    if eigen(eintrag.adresse) then
        ngx.var.api_base = "http://" .. eintrag.adresse
        return true
    end
    if direkt() then
        ngx.var.api_base = "http://" .. eintrag.adresse
        return true
    end
    -- Tunnel: Der eigene Node nimmt die Anfrage an und reicht sie über das
    -- verschlüsselte P2P-Netz weiter. Dafür braucht es die Peer-Kennung.
    if not eintrag.peer then return false end
    ngx.req.set_uri("/api/v1/proxy/" .. eintrag.peer .. uri, false)
    return true
end

-- route: Im rewrite_by_lua der /api/-Stelle aufrufen – ZULETZT, nach der
-- Heimnetz-Sperre. Gibt den Empfänger zurück.
function M.route(uri)
    if not M.aktiv() then return nil end
    -- Verwaltung und Fernsteuerung bleiben auf diesem Pi.
    if uri:find("/api/v1/admin/", 1, true)
        or uri:find("/api/v1/proxy/", 1, true)
        or uri:find("/api/v1/remote/", 1, true) then
        return nil
    end

    local peers = M.peers()
    if #peers == 0 then return nil end

    -- Schon zugewiesen? Dann unbedingt dort bleiben.
    local gesetzt = ngx.var["cookie_" .. COOKIE]
    if gesetzt and gesetzt:match("^[%w%.%-]+:%d+$") then
        for _, p in ipairs(peers) do
            if p.adresse == gesetzt then
                if gesund(p.adresse) and zustellen(p, uri) then
                    return p.adresse
                end
                -- Zugewiesener Node ist nicht erreichbar: neu zuweisen. Die
                -- Sitzung ist dann ohnehin weg, der Nutzer muss sich neu
                -- anmelden – besser als eine tote Seite.
                break
            end
        end
    end

    -- Im Tunnelbetrieb nur Empfänger mit bekannter Peer-Kennung: lieber beim
    -- eigenen Node bleiben als unverschlüsselt ausliefern.
    local tauglich = {}
    for _, p in ipairs(peers) do
        if direkt() or eigen(p.adresse) or p.peer then tauglich[#tauglich + 1] = p end
    end
    local ziel = waehle(tauglich)
    if not ziel then return nil end
    for _, p in ipairs(tauglich) do
        if p.adresse == ziel then
            if not zustellen(p, uri) then return nil end
            break
        end
    end
    ngx.header["Set-Cookie"] = COOKIE .. "=" .. ziel ..
        "; Path=/; Max-Age=" .. (COOKIE_TAGE * 86400) .. "; SameSite=Lax"
    return ziel
end

-- pruefen: Erreichbarkeit aller Empfänger feststellen. Läuft im Hintergrund.
function M.pruefen()
    local http_ok = pcall(require, "resty.http")
    for _, pe in ipairs(M.peers()) do
        local p = pe.adresse
        local host, port = p:match("^([^:]+):(%d+)$")
        local sock = ngx.socket.tcp()
        sock:settimeout(AUSZEIT * 1000)
        local ok = sock:connect(host, tonumber(port))
        if ok then
            -- Nicht nur die Verbindung, sondern eine echte Antwort prüfen:
            -- Ein Node, dessen Port offen ist, der aber nicht antwortet, wäre
            -- sonst "gesund" und würde Anfragen verschlucken.
            sock:send("GET /health HTTP/1.0\r\nHost: " .. host .. "\r\n\r\n")
            local zeile = sock:receive("*l")
            ok = zeile ~= nil and zeile:find("200") ~= nil
            sock:close()
        end
        cache:set("bal:up:" .. p, ok and 1 or 0, PRUEF_TAKT * 3)
    end
    local _ = http_ok
end

-- starte: wiederkehrende Prüfung einrichten (einmal je Arbeitsprozess).
function M.starte()
    if not cache then return end
    local function takt(vorzeitig)
        if vorzeitig then return end
        pcall(M.pruefen)
        ngx.timer.at(PRUEF_TAKT, takt)
    end
    ngx.timer.at(1, takt)
end

-- stand: Für die Anzeige in den Einstellungen.
function M.stand()
    local out = { aktiv = M.aktiv(), transport = direkt() and "direkt" or "p2p", peers = {} }
    for _, p in ipairs(M.peers()) do
        out.peers[#out.peers + 1] = { adresse = p.adresse, gewicht = p.gewicht,
                                      peer = p.peer, erreichbar = gesund(p.adresse) }
    end
    return out
end

return M

-- pages/certificates.lua
--
-- R827: Zertifikate aus Zählertelegrammen.
--
-- Oben die zertifizierten Perioden: Belege aus dem Netz, gegen die Chain
-- geprüft (Anker, Gerätesignaturen, Menge, Zählerkennung). Darunter die
-- aktuellen Ablesungen dieses Nodes mit Prüfung der Gerätesignatur.
local render = require "render"

local function esc(s)
    return (tostring(s == nil and "" or s):gsub("[&<>\"]", { ["&"]="&amp;", ["<"]="&lt;", [">"]="&gt;", ['"']="&quot;" }))
end
local function zeit(t)
    t = tonumber(t) or 0
    if t <= 0 then return "–" end
    return os.date("!%d.%m.%y %H:%M", t) .. " UTC"
end
local function kurz(a)
    a = tostring(a or "")
    if #a < 14 then return esc(a) end
    return '<code title="' .. esc(a) .. '">' .. esc(a:sub(1, 6)) .. "…" .. esc(a:sub(-4)) .. "</code>"
end
local function haken(ok, titel)
    if ok then
        return '<span title="' .. esc(titel) .. '" style="color:var(--green)">✓</span>'
    end
    return '<span title="' .. esc(titel) .. ' – nicht erfüllt" style="color:#ff8a80">✗</span>'
end
local function kwh(v)
    return string.format("%.3f", tonumber(v) or 0):gsub("%.", ",")
end

return function()
    local data, err = render.api_get("/v1/zertifikate")

    ngx.header["Content-Type"] = "text/html"
    local t = render.header("certificate.title", "certificates")

    ngx.print([[
<div class="set-card" style="margin-bottom:14px"><div class="set-body">
  <p style="margin:0 0 6px"><b>Zertifikate aus Zählertelegrammen.</b> Der Zählerleser unterschreibt jede
  Ablesung mit seinem Geräteschlüssel. Der Node zertifiziert die eingespeiste Menge je Periode auf der
  Chain und legt die beiden unterschriebenen Ablesungen als Beleg ins Netz – jeder kann sie nachprüfen.</p>
  <p class="meta" style="margin:0">Bewiesen ist, dass <i>dieses Lesegerät</i> die Stände gemessen hat. Das
  Telegramm der optischen Schnittstelle trägt selbst keine Unterschrift des Zählers. Den Zählerleser
  richtest du unter <a href="/energy">Energie</a> ein.</p>
</div></div>
]])

    if err then
        ngx.print("<p class='error'>" .. esc(err) .. "</p>")
        render.footer()
        return
    end
    data = data or {}

    -- Zertifizierte Perioden
    ngx.print('<h3 style="margin:18px 0 8px">Zertifizierte Perioden</h3>')
    local perioden = data.perioden or {}
    if #perioden == 0 then
        ngx.print('<p class="meta">Noch keine. Die erste Periode wird zertifiziert, sobald ein Zähler Einspeisung meldet ' ..
            'und die Periode (' .. esc(data.periode_min or 60) .. ' min) abgelaufen ist.</p>')
    else
        ngx.print('<div style="overflow-x:auto"><table class="record-table"><thead><tr>' ..
            '<th>Zähler</th><th>Zeitraum</th><th>kWh</th><th title="Zertifikat auf der Chain, Anker passt zum Beleg">Chain</th>' ..
            '<th title="Anfang und Ende vom Gerät unterschrieben">Signaturen</th><th title="Menge = Differenz der Zählwerke">Menge</th>' ..
            '<th title="Zählerkennung passt zur Zählernummer">Kennung</th><th>Erzeuger</th></tr></thead><tbody>')
        for _, p in ipairs(perioden) do
            local sig = haken(p.signaturen, "Gerätesignaturen gültig")
            if p.anfang_ohne then
                sig = sig .. ' <span class="meta" title="Der Anfangsstand lag nicht mehr signiert vor; geprüft ist nur das Ende.">(nur Ende)</span>'
            end
            ngx.print("<tr><td>" .. esc(p.zaehler) .. "</td><td>" .. zeit(p.von) .. " – " .. zeit(p.bis) ..
                "</td><td style='text-align:right'>" .. kwh(p.kwh) .. "</td><td>" .. haken(p.auf_chain, "auf der Chain") ..
                "</td><td>" .. sig .. "</td><td>" .. haken(p.menge_stimmt, "Menge stimmt") ..
                "</td><td>" .. haken(p.kennung, "Kennung passt") .. "</td><td>" .. kurz(p.erzeuger) .. "</td></tr>")
        end
        ngx.print("</tbody></table></div>")
    end

    -- Viertelstunden (R829): Bezug und Einspeisung je 15 min, belegt durch
    -- zwei gerätesignierte Ablesungen.
    local vq = render.api_get("/v1/viertelstunden?anzahl=12")
    local viertel = (vq and vq.viertelstunden) or {}
    if #viertel > 0 then
        ngx.print('<h3 style="margin:22px 0 8px">Viertelstunden</h3>')
        ngx.print('<p class="meta" style="margin:0 0 8px">Bezug und Einspeisung je 15 Minuten, belegt durch zwei ' ..
            'vom Gerät unterschriebene Ablesungen. Lücken und unvollständige Viertelstunden sind nicht handelbar.</p>')
        ngx.print('<div style="overflow-x:auto"><table class="record-table"><thead><tr><th>Zähler</th><th>Viertelstunde</th>' ..
            '<th>Bezug kWh</th><th>Einspeisung kWh</th><th title="Beide Ablesungen vom Gerät unterschrieben">Signaturen</th>' ..
            '<th title="Mengen = Differenz der Zählwerke">Menge</th><th>Zustand</th></tr></thead><tbody>')
        for _, v in ipairs(viertel) do
            local zustand = '<span style="color:var(--green)">vollständig</span>'
            if v.luecke then
                zustand = '<span class="meta" title="Ablesungen fehlten – spannt über mehrere Viertelstunden">Lücke</span>'
            elseif v.unvollstaendig then
                zustand = '<span class="meta" title="Eine Ablesung lag mehr als 60 s nach der Grenze">unvollständig</span>'
            end
            ngx.print("<tr><td>" .. esc(v.zaehler) .. "</td><td>" .. zeit(v.von) .. "</td><td style='text-align:right'>" ..
                kwh(v.bezug_kwh) .. "</td><td style='text-align:right'>" .. kwh(v.einspeisung_kwh) .. "</td><td>" ..
                haken(v.signaturen, "Gerätesignaturen gültig") .. "</td><td>" .. haken(v.menge_stimmt and v.grenzen, "Menge und Grenzen stimmen") ..
                "</td><td>" .. zustand .. "</td></tr>")
        end
        ngx.print("</tbody></table></div>")
    end

    -- Aktuelle Ablesungen dieses Nodes
    local ablesungen = data.ablesungen or {}
    if #ablesungen > 0 then
        ngx.print('<h3 style="margin:22px 0 8px">Ablesungen dieses Nodes</h3>')
        ngx.print('<div style="overflow-x:auto"><table class="record-table"><thead><tr><th>Zähler</th><th>Zeit</th>' ..
            '<th>Bezug kWh</th><th>Einspeisung kWh</th><th>Gerätesignatur</th><th>Gerät</th></tr></thead><tbody>')
        for _, a in ipairs(ablesungen) do
            ngx.print("<tr><td>" .. esc(a.zaehler) .. "</td><td>" .. zeit(a.zeit) .. "</td><td style='text-align:right'>" ..
                kwh(a.kwh_bezug) .. "</td><td style='text-align:right'>" .. kwh(a.kwh_einspeisung) .. "</td><td>" ..
                haken(a.signatur_gueltig, "Gerätesignatur gültig") .. "</td><td>" .. kurz(a.geraet) .. "</td></tr>")
        end
        ngx.print("</tbody></table></div>")
        if data.zaehler_wallet and data.zaehler_wallet ~= "" then
            ngx.print('<p class="meta" style="margin-top:8px">Erzeuger auf der Chain (Zähler-Wallet dieses Nodes): ' ..
                kurz(data.zaehler_wallet) .. '</p>')
        end
    end

    render.footer()
end

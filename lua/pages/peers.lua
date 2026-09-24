-- pages/peers.lua
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("peers.title", "peers")

    local node,  nerr = render.api_get("/v1/node")
    local peers, perr = render.api_get("/v1/peers")
    local chain, cerr = render.api_get("/v1/chain/status")

    if not nerr and node then
        ngx.print("<section><h2>" .. t("peers.own_node") .. "</h2>")
        ngx.print("<p class='mono'>" .. (node.id or "–") .. "</p><ul>")
        for _, addr in ipairs(node.addrs or {}) do
            ngx.print("<li><code>" .. addr .. "</code></li>")
        end
        ngx.print("</ul></section>")
    end

    -- Chain-Status: Höhe + aktuelles Validator-Set. Zeigt live, wenn ein neuer
    -- Node per Stake dem Validator-Set beitritt (Anzahl steigt, Adresse taucht auf).
    if not cerr and chain then
        ngx.print("<section><h2>" .. t("peers.chain") .. "</h2>")
        ngx.print("<p class='meta'>" .. t("peers.height") .. ": <strong>" .. tostring(chain.height or 0) .. "</strong>")
        ngx.print(" &middot; " .. t("peers.validators") .. ": <strong>" .. tostring(chain.validators or 0) .. "</strong></p>")
        if chain.validator_addrs and #chain.validator_addrs > 0 then
            ngx.print("<ul>")
            for _, addr in ipairs(chain.validator_addrs) do
                ngx.print("<li><code>" .. addr .. "</code></li>")
            end
            ngx.print("</ul>")
        end
        ngx.print("</section>")
    end

    ngx.print("<section><h2>" .. t("peers.connected") .. "</h2>")
    if perr then
        ngx.print("<p class='error'>" .. perr .. "</p>")
    elseif not peers or peers.count == 0 then
        ngx.print("<p class='empty'>" .. t("peers.none") .. "</p>")
    else
        ngx.print("<p class='meta'>" .. t("peers.count", peers.count) .. "</p><ul>")
        local detailed = peers.peers_detailed or {}
        if #detailed > 0 then
            for _, pd in ipairs(detailed) do
                local shortId = pd.id
                if #shortId > 20 then
                    shortId = string.sub(shortId, 1, 12) .. "…" .. string.sub(shortId, -6)
                end
                if pd.ipv4 and pd.ipv4 ~= "" then
                    -- Erreichbar (selbes Netz): direkter Link + Tunnel-Link als Alternative.
                    ngx.print("<li><a href='http://" .. pd.ipv4 .. "/' target='_blank' rel='noopener' " ..
                        "style='text-decoration:none'>🔗 <code>" .. pd.ipv4 .. "</code></a> " ..
                        "<a href='/api/v1/remote/enter/" .. pd.id .. "' " ..
                        "style='text-decoration:none;margin-left:8px' title='" .. t("peers.via_tunnel") .. "'>🌐</a> " ..
                        "<span class='meta' style='font-size:11px'><code>" .. shortId .. "</code></span></li>")
                else
                    -- Keine direkte IP (z.B. Gastnetz): nur über den P2P-Tunnel erreichbar.
                    ngx.print("<li><a href='/api/v1/remote/enter/" .. pd.id .. "' " ..
                        "style='text-decoration:none'>🌐 " .. t("peers.via_tunnel") .. "</a> " ..
                        "<span class='meta' style='font-size:11px'><code>" .. shortId .. "</code></span></li>")
                end
            end
        else
            for _, pid in ipairs(peers.peers or {}) do
                ngx.print("<li><code>" .. pid .. "</code></li>")
            end
        end
        ngx.print("</ul>")
    end
    ngx.print("</section>")

    render.footer()
end

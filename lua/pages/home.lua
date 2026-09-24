-- pages/home.lua
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    -- "__none__" → header rendert keinen <h1> (eigener Hero unten)
    local t = render.header("__none__", "")

    local status, err = render.api_get("/v1/status")

    if err then
        ngx.print("<p class='error'>" .. t("general.node_offline", {reason=err}) .. "</p>")
        render.footer()
        return
    end

    local nodeId   = (status.node_id or "–")
    local peers    = status.peers or 0
    local rec      = (status.storage and status.storage.records) or {}
    local listings = rec.listing or 0
    local energy   = rec.energy or 0
    local certs    = rec.certificate or 0
    local jobs     = rec.job or 0

    ngx.print(string.format([[
<div class="home-hero home-hero-slim">
  <p class="home-sub">%s</p>
</div>

<div class="tile-grid">
  <a href="/listings" class="tile tile-green" data-nav-key="listings" data-nav-zone="landing">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#128722;</div>
    <div class="tile-value">%d</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/energy" class="tile tile-gold wip" data-wip="BETA" data-nav-key="energy" data-nav-zone="landing">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#9889;</div>
    <div class="tile-value">%d</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/certificates" class="tile tile-blue wip" data-wip="BETA" data-nav-key="certificates" data-nav-zone="landing">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#127894;</div>
    <div class="tile-value">%d</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/jobs" class="tile tile-purple wip" data-wip="BETA" data-nav-key="jobs" data-nav-zone="landing">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#128188;</div>
    <div class="tile-value">%d</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/peers" class="tile tile-cyan">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#127760;</div>
    <div class="tile-value">%d</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/files" class="tile tile-pink" data-nav-key="files" data-nav-zone="landing">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#128190;</div>
    <div class="tile-value">&#10516;</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/partner" class="tile tile-rose" data-nav-key="partner" data-nav-zone="landing">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#129309;</div>
    <div class="tile-value">&#10084;</div>
    <div class="tile-label">%s</div>
  </a>
  <a href="/wallet" class="tile tile-amber">
    <div class="tile-glow"></div>
    <div class="tile-icon">&#128091;</div>
    <div class="tile-value">FND</div>
    <div class="tile-label">%s</div>
  </a>
</div>

<section class="home-node">
  <div class="home-node-head">
    <span class="home-node-dot"></span>
    <span class="home-node-id mono">%s&#8230;</span>
    <span class="home-node-peers">&#8226; %d Peers</span>
  </div>
  <details class="home-addrs">
    <summary>%s</summary>
    <ul class="addr-list">
]],
        t("footer.tagline"),
        listings, t("home.listings_count"),
        energy,   t("home.energy_count"),
        certs,    (t("nav.certificates") or "Zertifikate"),
        jobs,     (t("nav.jobs") or "Jobs"),
        peers,    t("home.peers"),
        (t("nav.files") or "Speicher"),
        (t("nav.partner") or "Partner"),
        (t("nav.wallet") or "Wallet"),
        nodeId:sub(1, 24),
        peers,
        t("home.own_addresses")
    ))

    for _, addr in ipairs(status.addrs or {}) do
        ngx.print("<li><code>" .. render.html_escape(addr) .. "</code></li>")
    end
    ngx.print("</ul></details></section>")

    render.footer()
end

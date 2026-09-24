-- pages/energy.lua
local render = require "render"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t, lang = render.header("energy.title", "energy"), ngx.ctx.lang or "de"
    lang = ngx.ctx.lang or lang

    -- Meter-Status prüfen
    local meterStatus, _ = render.api_get("/v1/meter/status")
    local meterEnabled   = meterStatus and meterStatus.enabled

    -- Live-Widget nur wenn Meter aktiv
    if meterEnabled then
        local jsStr = cjson.encode({
            kwh_label  = t("energy.col_kwh"),
            watt_label = "W",
            meter_id   = meterStatus.meter_id or "",
            lang       = lang,
        })
        ngx.print(string.format([[
<section class="meter-live">
  <h2>%s <span class="live-badge">LIVE</span></h2>
  <div class="meter-grid">
    <div class="stat-card">
      <span class="label">%s</span>
      <span class="value" id="live-kwh">–</span>
    </div>
    <div class="stat-card">
      <span class="label">%s</span>
      <span class="value" id="live-watt">–</span>
    </div>
    <div class="stat-card">
      <span class="label">%s</span>
      <span class="value mono small" id="live-ts">–</span>
    </div>
  </div>
</section>
<script>const METER_STRINGS = %s;</script>
<script src="/static/meter.js?v=]] .. require("render").rev() .. [["></script>
]], t("energy.title"), t("energy.col_kwh"), "Watt aktuell",
    t("energy.col_timestamp"), jsStr))
    end

    -- Token-Tabelle
    local data, err = render.api_get("/v1/energy")
    if err then
        ngx.print("<p class='error'>" .. err .. "</p>")
    else
        local tokens = data.tokens or {}
        ngx.print("<p class='meta'>" .. t("energy.count", #tokens) .. "</p>")

        local columns = {
            { key="id",             label=t("energy.col_id")        },
            { key="data.meter_id",  label=t("energy.col_meter")     },
            { key="data.kwh",       label=t("energy.col_kwh")       },
            { key="data.lat",       label=t("energy.col_lat")       },
            { key="data.lon",       label=t("energy.col_lon")       },
            { key="data.timestamp", label=t("energy.col_timestamp") },
        }
        ngx.print(render.record_table(tokens, columns))
    end

    ngx.print(string.format([[
<section class="info-box">
  <h2>%s</h2><p>%s</p>
</section>
]], t("energy.fee_title"), t("energy.fee_desc")))

    render.footer()
end

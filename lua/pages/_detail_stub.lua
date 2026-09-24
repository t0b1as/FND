-- pages/_detail_stub.lua
-- Hilfsmodul: erzeugt eine einfache Detail-Seite für einen beliebigen Record-Typ.
-- Wird von certificate_show, job_show usw. genutzt.

local render = require "render"
local _M = {}

function _M.show(apiPath, titleKey, active, captures)
    local id = captures and captures[1] or ""
    ngx.header["Content-Type"] = "text/html"
    local t = render.header(titleKey, active)

    if id == "" then
        ngx.print("<p class='error'>" .. t("general.not_found") .. "</p>")
        render.footer(); return
    end

    local record, err = render.api_get(apiPath .. "/" .. id)
    if err or not record then
        ngx.print("<p class='error'>" .. t("general.not_found") .. ": " .. (err or id) .. "</p>")
        render.footer(); return
    end

    -- Rohe JSON-Darstellung der Daten
    local cjson = require "cjson.safe"
    local pretty = cjson.encode(record.data or {})

    ngx.print(string.format([[
<div class="detail-card">
  <p class="meta">ID: <code>%s</code></p>
  <pre class="json-view">%s</pre>
  <a href="/%s" class="btn-secondary">&larr; %s</a>
</div>
]], record.id, ngx.escape_uri(pretty), active, t("general.back")))

    render.footer()
end

return _M

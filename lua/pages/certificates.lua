-- pages/certificates.lua
local render = require "render"

return function()
    local data, err = render.api_get("/v1/certificates")

    ngx.header["Content-Type"] = "text/html"
    local t = render.header("certificate.title", "certificates")

    ngx.print('<a href="/certificates/new" class="btn">' .. t("listing.new"):gsub("Angebot","Zertifikat") .. '</a>')
    if err then
        ngx.print("<p class='error'>" .. err .. "</p>")
    else
        local columns = {
            { key="id",           label=t("certificate.col_id")     },
            { key="data.type",    label=t("certificate.col_type")   },
            { key="created_at",   label=t("certificate.col_issued") },
        }
        ngx.print(render.record_table(data.records or {}, columns))
    end

    render.footer()
end

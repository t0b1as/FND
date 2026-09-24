-- =============================================================================
--  router.lua – einfacher URL-Router für OpenResty/nginx
--  Handler printen ihren Output selbst (ngx.print) und geben einen
--  beliebigen Wert zurück (oft die t()-Funktion). Der Router darf diesen
--  Rueckgabewert NICHT an ngx.say geben.
-- =============================================================================

local _M = {}

function _M.dispatch(routes)
    local method = ngx.req.get_method()
    local uri    = ngx.var.uri

    -- favicon & Co. ohne Route kurz abhandeln (kein HTML noetig)
    if uri == "/favicon.ico" then
        ngx.status = 204  -- No Content
        return ngx.exit(204)
    end

    for _, route in ipairs(routes) do
        if route.method == method or route.method == "*" then
            local captures = { uri:match(route.pattern) }
            local matched  = uri:match(route.pattern) ~= nil
            if matched then
                -- Handler printet selbst; Rueckgabewert wird ignoriert.
                local ok, err = pcall(route.handler, captures)
                if not ok then
                    ngx.log(ngx.ERR, "Handler error: ", tostring(err))
                    if not ngx.headers_sent then
                        ngx.status = 500
                        ngx.header["Content-Type"] = "text/html"
                        ngx.print("<h1>Interner Fehler</h1><pre>" ..
                                  tostring(err):gsub("[<>]", "") .. "</pre>")
                    end
                end
                return
            end
        end
    end

    -- Keine Route gefunden
    ngx.status = 404
    ngx.header["Content-Type"] = "text/html"
    -- error_page printet selbst, daher KEIN ngx.say drumherum
    require("render").error_page("Seite nicht gefunden", uri)
end

return _M

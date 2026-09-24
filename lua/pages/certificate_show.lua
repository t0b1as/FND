-- pages/certificate_show.lua
local stub = require "pages._detail_stub"
return function(c) stub.show("/v1/certificates", "certificate.title", "certificates", c) end

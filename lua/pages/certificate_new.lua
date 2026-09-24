-- pages/certificate_new.lua
local render = require "render"
local cjson  = require "cjson.safe"
local i18n   = require "i18n"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t, lang = i18n.init()
    render.header("certificate.title", "certificates")
    lang = ngx.ctx.lang or lang

    local certTypes = {
        { "Energieausweis",      t("certificate.ct_energy")  },
        { "CO2-Zertifikat",      t("certificate.ct_co2")     },
        { "Herkunftsnachweis",   t("certificate.ct_origin")  },
        { "Qualitätszertifikat", t("certificate.ct_quality") },
        { "Smartmeter-Lesung",   t("certificate.ct_meter")   },
        { "Sonstiges",           t("certificate.ct_other")   },
    }
    local typeOpts = {}
    for _, ct in ipairs(certTypes) do
        table.insert(typeOpts, string.format('<option value="%s">%s</option>', ct[1], ct[2]))
    end

    local jsStr = cjson.encode({
        publishing = t("general.publishing"),
        published  = t("general.published"),
        lang       = lang,
    })

    ngx.print(string.format([[
<div class="upload-page">
<section class="step">
  <div class="step-header"><span class="step-num">1</span><h2>%s</h2></div>
  <form id="cert-form" onsubmit="submitCert(event)">
    <div class="field-row">
      <div class="field">
        <label>%s</label>
        <select id="c-type">%s</select>
      </div>
      <div class="field">
        <label>]] .. t("certificate.issuer") .. [[</label>
        <input type="text" id="c-issuer" placeholder="z.B. Bundesnetzagentur">
      </div>
    </div>
    <div class="field">
      <label>]] .. t("certificate.description") .. [[</label>
      <textarea id="c-desc" rows="4" placeholder="]] .. t("certificate.desc_placeholder") .. [["></textarea>
    </div>
    <div class="field">
      <label>]] .. t("certificate.valid_until") .. [[</label>
      <input type="date" id="c-expires">
    </div>

    <!-- Energie-Token-Felder (nur bei Smartmeter-Lesung) -->
    <div id="energy-fields" style="display:none">
      <hr style="margin:1rem 0;border-color:var(--border)">
      <p class="meta">]] .. t("certificate.meter_data_note") .. [[</p>
      <div class="field-row">
        <div class="field">
          <label>]] .. t("certificate.meter_id") .. [[</label>
          <input type="text" id="c-meter-id" placeholder="DE001234…">
        </div>
        <div class="field">
          <label>kWh</label>
          <input type="number" id="c-kwh" step="0.0001" placeholder="0.0000">
        </div>
      </div>
      <div class="field-row">
        <div class="field">
          <label>]] .. t("certificate.latitude") .. [[</label>
          <input type="number" id="c-lat" step="0.000001">
        </div>
        <div class="field">
          <label>]] .. t("certificate.longitude") .. [[</label>
          <input type="number" id="c-lon" step="0.000001">
        </div>
      </div>
      <button type="button" class="btn" style="margin-bottom:1rem"
              onclick="loadFromMeter()">
        Vom lokalen Smartmeter laden
      </button>
    </div>

    <div class="form-actions">
      <button type="submit" class="btn" id="submit-btn">%s</button>
      <span id="submit-status"></span>
    </div>
  </form>
</section>
</div>

<script>const FUNDUS_STRINGS = %s;</script>
<script>
document.getElementById("c-type").addEventListener("change", function() {
    const energyFields = document.getElementById("energy-fields");
    energyFields.style.display = this.value === "Smartmeter-Lesung" ? "block" : "none";
});

async function loadFromMeter() {
    try {
        const resp = await fetch("/api/v1/meter/status");
        const status = await resp.json();
        if (!status.enabled) {
            alert("Kein Smartmeter konfiguriert.");
            return;
        }
        // Letzten Token aus der Energie-Tabelle holen
        const tokResp = await fetch("/api/v1/energy");
        const tokData = await tokResp.json();
        const tokens = tokData.tokens || [];
        if (tokens.length === 0) { alert("Noch keine Messwerte vorhanden."); return; }
        const latest = tokens[tokens.length - 1];
        const d = latest.data || {};
        if (d.meter_id) document.getElementById("c-meter-id").value = d.meter_id;
        if (d.kwh)      document.getElementById("c-kwh").value = d.kwh;
        if (d.lat)      document.getElementById("c-lat").value = d.lat;
        if (d.lon)      document.getElementById("c-lon").value = d.lon;
    } catch(e) {
        alert("Fehler beim Laden: " + e.message);
    }
}

async function submitCert(e) {
    e.preventDefault();
    const btn = document.getElementById("submit-btn");
    const st  = document.getElementById("submit-status");
    btn.disabled = true;
    st.textContent = FUNDUS_STRINGS.publishing || "…";

    const type = document.getElementById("c-type").value;
    const payload = {
        type:        type,
        issuer:      document.getElementById("c-issuer").value,
        description: document.getElementById("c-desc").value,
        expires:     document.getElementById("c-expires").value || null,
    };

    if (type === "Smartmeter-Lesung") {
        payload.meter_id = document.getElementById("c-meter-id").value;
        payload.kwh      = parseFloat(document.getElementById("c-kwh").value) || 0;
        payload.lat      = parseFloat(document.getElementById("c-lat").value) || 0;
        payload.lon      = parseFloat(document.getElementById("c-lon").value) || 0;
    }

    try {
        const resp = await fetch("/api/v1/certificates", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify(payload),
        });
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        const cert = await resp.json();
        st.innerHTML = `<span class="success">${FUNDUS_STRINGS.published || "OK"}</span>`;
        setTimeout(() => { location.href = "/certificates/" + cert.id; }, 1200);
    } catch(err) {
        st.innerHTML = `<span class="error">${err.message}</span>`;
        btn.disabled = false;
    }
}
</script>
]],
        t("certificate.title"),
        t("certificate.col_type"), table.concat(typeOpts),
        t("general.publish"),
        jsStr
    ))

    render.footer()
end

-- pages/listing_edit.lua
-- Lädt das bestehende Listing und rendert das neue-Angebot-Formular vorausgefüllt.
local render = require "render"
local cjson  = require "cjson.safe"
local i18n   = require "i18n"

return function(captures)
    local id = captures and captures[1] or ""
    ngx.header["Content-Type"] = "text/html"
    local t, lang = i18n.init()
    render.header("listing.detail_title", "listings")
    lang = ngx.ctx.lang or lang

    local record, err = render.api_get("/v1/listings/" .. id)
    if err or not record then
        ngx.print("<p class='error'>" .. t("general.not_found") .. "</p>")
        render.footer(); return
    end

    local d = record.data or {}
    local jsStr = cjson.encode({
        publishing = t("general.publishing"),
        published  = t("general.published"),
        lang       = lang,
        prefill = {
            title       = d.title or "",
            description = d.description or d.listing_text or "",
            price_min   = d.price_min,
            price_max   = d.price_max,
            condition   = d.condition or "",
            category    = d.category  or "",
            video_hash  = d.video_hash or "",
            video_hashes = type(d.video_hashes) == "table" and d.video_hashes or {},
            delivery    = d.delivery or "pickup",
            keywords    = type(d.keywords) == "table" and table.concat(d.keywords,", ") or (d.keywords or ""),
            images       = type(d.images) == "table" and d.images or {},
            image_hashes = type(d.image_hashes) == "table" and d.image_hashes or {},
        },
    })

    -- Kategorie/Zustand-Optionen mit Vorauswahl des aktuellen Werts (damit beim
    -- Bearbeiten der gespeicherte Wert markiert ist). Dieselben Listen wie im
    -- Erstellungsformular.
    local conditions = {
        {"neuwertig", t("listing.condition.new")},
        {"gut",       t("listing.condition.good")},
        {"akzeptabel",t("listing.condition.acceptable")},
        {"defekt",    t("listing.condition.broken")},
    }
    local categories = {
        {"Elektronik", t("listing.category.electronics")},
        {"Möbel",      t("listing.category.furniture")},
        {"Kleidung",   t("listing.category.clothing")},
        {"Fahrzeug",   t("listing.category.vehicle")},
        {"Werkzeug",   t("listing.category.tools")},
        {"Haushalt",   t("listing.category.household")},
        {"Sport",      t("listing.category.sports")},
        {"Sonstiges",  t("listing.category.other")},
    }
    local curCond = d.condition or ""
    local curCat  = d.category  or ""
    local cond_opts, cat_opts = {}, {}
    for _, c in ipairs(conditions) do
        local sel = (c[1] == curCond) and " selected" or ""
        table.insert(cond_opts, string.format('<option value="%s"%s>%s</option>', c[1], sel, c[2]))
    end
    for _, c in ipairs(categories) do
        local sel = (c[1] == curCat) and " selected" or ""
        table.insert(cat_opts, string.format('<option value="%s"%s>%s</option>', c[1], sel, c[2]))
    end
    local cond_html = table.concat(cond_opts)
    local cat_html  = table.concat(cat_opts)

    ngx.print(string.format([[
<form id="edit-form" onsubmit="submitEdit(event)">
  <div class="field"><label>%s</label>
    <input type="text" id="f-title" value="%s" required></div>
  <div class="field"><label>%s</label>
    <textarea id="f-description" rows="5">%s</textarea></div>
  <div class="field"><label>%s</label>
    <input type="number" id="f-price" value="%.2f" min="0" step="0.01"></div>
  <div class="field">
    <label>]] .. t("listing.seller_wallet") .. [[</label>
    <div style="display:flex;gap:6px;flex-wrap:wrap">
      <select id="f-seller-wallet" style="flex:1;min-width:180px">
        <option value="">]] .. t("listing.seller_wallet_none") .. [[</option>
      </select>
      <button type="button" class="btn-sm" onclick="openAddrBook()">📖 ]] .. t("listing.manage_addrbook") .. [[</button>
    </div>
    <small class="meta" data-current-wallet="%s">]] .. t("listing.seller_wallet_hint") .. [[</small></div>
  <div class="field">
    <label>%s</label>
    <div class="fee-mode-row" style="margin:6px 0;font-size:13px">
      <label style="margin-right:16px;cursor:pointer">
        <input type="radio" name="f-delivery" value="pickup" onchange="deliveryModeChanged()"> %s
      </label>
      <label style="cursor:pointer">
        <input type="radio" name="f-delivery" value="shipping" onchange="deliveryModeChanged()"> %s
      </label>
    </div>
    <div id="f-shipping-cost-row" style="display:none;margin-top:6px">
      <input type="number" id="f-shipping-cost" value="%.2f" min="0" step="0.01" placeholder="%s">
    </div>
  </div>
  <div class="field"><label>%s</label>
    <input type="text" id="f-keywords" value="%s"></div>
  <div class="field-row">
    <div class="field"><label>%s</label>
      <select id="f-category">%s</select></div>
    <div class="field"><label>%s</label>
      <select id="f-condition">%s</select></div>
  </div>
  <div class="field"><label>%s</label>
    <input type="text" id="f-plz" inputmode="numeric" maxlength="5" value="%s"></div>
  <div class="field">
    <label>Bilder &amp; Video</label>
    <div class="image-previews" id="image-previews"></div>
    <div class="image-previews" id="video-previews" style="margin-top:8px"></div>
    <input type="file" id="image-input" accept="image/*,video/*" multiple style="display:none">
    <button type="button" class="btn-secondary" onclick="document.getElementById('image-input').click()">+ Medien hinzufügen</button>
    <div id="video-name" class="video-name"></div>
  </div>
  <div class="form-actions">
    <button type="submit" class="btn" id="submit-btn">%s</button>
    <span id="submit-status"></span>
    <a href="/listings/%s" class="btn-secondary">%s</a>
  </div>
</form>
<script>const FUNDUS_STRINGS = %s;</script>
<script src="/static/upload.js?v=]] .. require("render").rev() .. [["></script>
<script src="/static/richtext.js?v=]] .. require("render").rev() .. [["></script>
<script>
document.addEventListener("DOMContentLoaded", function(){
  if (typeof initRichEditor === "function") initRichEditor("f-description");
});
</script>
<script>
// Empfangsadress-Dropdown befüllen und den gespeicherten Wert vorselektieren.
document.addEventListener("DOMContentLoaded", async function(){
  if (typeof loadAddrBookDropdown === "function") await loadAddrBookDropdown();
  const note = document.querySelector("[data-current-wallet]");
  const cur = note ? note.getAttribute("data-current-wallet") : "";
  const sel = document.getElementById("f-seller-wallet");
  if (sel && cur) {
    // Falls die gespeicherte Adresse nicht im Adressbuch ist, als Option ergänzen.
    let found = false;
    for (const o of sel.options) { if (o.value === cur) { found = true; break; } }
    if (!found) { const o=document.createElement("option"); o.value=cur; o.textContent=cur.slice(0,10)+"…"+cur.slice(-6)+" (aktuell)"; sel.appendChild(o); }
    sel.value = cur;
  }
});
</script>
<script>
const LT = ]] .. (require("cjson.safe").encode({ upload_failed = t("listing.upload_failed"), add_image_failed = t("listing.add_image_failed") }) or "{}") .. [[
// Vorhandene Medien in die globalen Arrays der neuen Upload-Mechanik (upload.js)
// vorladen — dann werden sie als Kacheln mit Thumbnails + Lösch-Buttons
// dargestellt, genau wie im Anlege-Formular.
window._mktImageHashes = ((FUNDUS_STRINGS.prefill && FUNDUS_STRINGS.prefill.image_hashes) || []).slice();
window._mktImageThumbs = ((FUNDUS_STRINGS.prefill && FUNDUS_STRINGS.prefill.images) || []).slice();
window._mktVideoHashes = ((FUNDUS_STRINGS.prefill && FUNDUS_STRINGS.prefill.video_hashes) || []).slice();
// Einzelnes altes Video (Rückwärtskompat) in das Array übernehmen.
(function(){
    const singleVid = (FUNDUS_STRINGS.prefill && FUNDUS_STRINGS.prefill.video_hash) || "";
    if (singleVid && window._mktVideoHashes.indexOf(singleVid) < 0) {
        window._mktVideoHashes.unshift(singleVid);
    }
})();

// Die vorhandenen Medien beim Laden rendern — über dieselben Render-Funktionen
// wie das Anlege-Formular (konsistentes Modell, keine Race-Conditions).
document.addEventListener("DOMContentLoaded", function(){
    if (typeof renderMktImages === "function") renderMktImages();
    if (typeof renderMktVideos === "function") renderMktVideos();
});
// Versandart: bei "Versand" das Kostenfeld einblenden, bei Selbstabholung aus.
function deliveryModeChanged() {
    const mode = (document.querySelector('input[name="f-delivery"]:checked')||{}).value || "pickup";
    const row = document.getElementById("f-shipping-cost-row");
    if (row) row.style.display = (mode === "shipping") ? "block" : "none";
}
// Gespeicherte Versandart vorauswählen und Kostenfeld ggf. einblenden.
(function initDelivery(){
    const mode = (FUNDUS_STRINGS.prefill && FUNDUS_STRINGS.prefill.delivery) || "pickup";
    const radio = document.querySelector('input[name="f-delivery"][value="' + mode + '"]')
        || document.querySelector('input[name="f-delivery"][value="pickup"]');
    if (radio) radio.checked = true;
    deliveryModeChanged();
})();

// Vorhandenes Video anzeigen (damit klar ist, dass eines existiert und beim
// Speichern erhalten bleibt, solange kein neues hochgeladen wird).




function hashString(s){let h=0;for(let i=0;i<s.length;i++){h=(h<<5)-h+s.charCodeAt(i);h|=0;}return h;}

async function resumableUpload(file, onProgress, redundancy) {
    const BLOCK = 4 * 1024 * 1024;
    const idSeed = file.name + "|" + file.size + "|" + (file.lastModified || 0);
    const uploadId = "u" + Math.abs(hashString(idSeed)).toString(36) + (file.size %% 100000).toString(36);
    if (window.FundusProgress) FundusProgress.set(uploadId, { name: file.name, state: "uploading", progress: 0 });
    const report = (p) => { if (onProgress) onProgress(p); if (window.FundusProgress) FundusProgress.progress(uploadId, p); };
    const beginRes = await fetch("/api/v1/files/upload/begin", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ upload_id: uploadId, file_name: file.name,
            mime_type: file.type || "application/octet-stream", total_size: file.size, block_size: BLOCK,
            redundancy: redundancy || 0 }),
    });
    if (!beginRes.ok) throw new Error("begin");
    const begin = await beginRes.json();
    const have = new Set(begin.have_blocks || []);
    const totalBlocks = begin.total_blocks;
    for (let i = 0; i < totalBlocks; i++) {
        if (have.has(i)) { report((i+1)/totalBlocks); continue; }
        const slice = file.slice(i*BLOCK, Math.min((i+1)*BLOCK, file.size));
        let ok = false;
        for (let a = 0; a < 3 && !ok; a++) {
            try { const r = await fetch(`/api/v1/files/upload/block?id=${uploadId}&index=${i}`, { method:"POST", body: slice }); ok = r.ok; }
            catch(e){ ok = false; }
            if (!ok) await new Promise(res => setTimeout(res, 800));
        }
        if (!ok) throw new Error("block "+i);
        report((i+1)/totalBlocks);
    }
    const finRes = await fetch(`/api/v1/files/upload/finish?id=${uploadId}`, { method:"POST" });
    if (!finRes.ok) throw new Error("finish");
    if (window.FundusProgress) FundusProgress.finalizing(uploadId);
    for (;;) {
        await new Promise(res => setTimeout(res, 1500));
        let d;
        try { const sr = await fetch(`/api/v1/files/upload/status?id=${uploadId}`); if (!sr.ok) continue; d = await sr.json(); }
        catch(e){ continue; }
        if (d.state === "done") { if (window.FundusProgress) FundusProgress.done(uploadId); return d.content_hash; }
        if (d.state === "error") { if (window.FundusProgress) FundusProgress.set(uploadId, { state:"error", error:d.error }); throw new Error("Verarbeitung: " + (d.error||"")); }
    }
}



// Vorschau-Frame aus dem gewählten Video ziehen (clientseitig, ohne Upload).


async function submitEdit(e) {
    e.preventDefault();
    const btn = document.getElementById("submit-btn");
    const st  = document.getElementById("submit-status");
    btn.disabled = true;
    st.textContent = FUNDUS_STRINGS.publishing || "…";
    const payload = {
        title:        document.getElementById("f-title").value,
        description:  document.getElementById("f-description").value,
        price:        parseFloat(document.getElementById("f-price").value) || 0,
        seller_wallet: (document.getElementById("f-seller-wallet")||{}).value || "",
        delivery:     (document.querySelector('input[name="f-delivery"]:checked')||{}).value || "pickup",
        shipping_cost: parseFloat((document.getElementById("f-shipping-cost")||{}).value) || 0,
        keywords:     document.getElementById("f-keywords").value.split(",").map(s=>s.trim()).filter(Boolean),
        category:     (document.getElementById("f-category")||{}).value || "",
        condition:    (document.getElementById("f-condition")||{}).value || "",
        plz:          (document.getElementById("f-plz")||{}).value.trim() || "",
        images:       mktFinalMedia().images,
        image_hashes: mktFinalMedia().image_hashes,
        video_hashes: mktFinalMedia().video_hashes,
    };
    try {
        const resp = await fetch("/api/v1/listings/%s", {
            method: "PUT", headers: {"Content-Type":"application/json"}, body: JSON.stringify(payload)
        });
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        st.innerHTML = '<span class="success">' + (FUNDUS_STRINGS.published||"OK") + '</span>';
        setTimeout(() => { location.href = "/listings/%s"; }, 1200);
    } catch(err) {
        st.innerHTML = '<span class="error">' + err.message + '</span>';
        btn.disabled = false;
    }
}
</script>
]],
        t("upload.field_title"),       render.html_escape(d.title or ""),
        t("upload.field_description"), render.html_escape(d.description or d.listing_text or ""),
        t("upload.field_price"),    tonumber(d.price_min) or tonumber(d.price) or 0,
        tostring(d.seller_wallet or ""),
        t("upload.field_delivery"),
        t("upload.delivery_pickup"),
        t("upload.delivery_shipping"),
        tonumber(d.shipping_cost) or 0,
        t("upload.shipping_cost_ph"),
        t("upload.field_keywords"),    render.html_escape(type(d.keywords)=="table" and table.concat(d.keywords,", ") or (d.keywords or "")),
        t("upload.field_category"),    cat_html,
        t("upload.field_condition"),   cond_html,
        t("upload.field_plz"),         render.html_escape(tostring(d.plz or "")),
        t("general.save"), id, t("general.back"),
        jsStr, id, id
    ))

    render.footer()
end

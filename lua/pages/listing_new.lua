-- pages/listing_new.lua
local render = require "render"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t, lang = render.header("upload.title", "listings"), ngx.ctx.lang or "de"

    -- Sprache neu lesen falls render.header sie gesetzt hat
    lang = ngx.ctx.lang or lang

    local llmStatus, _ = render.api_get("/v1/analyze/status")
    local llmEnabled   = llmStatus and llmStatus.enabled and llmStatus.reachable

    -- JS-Strings als JSON ins HTML einbetten damit upload.js sie nutzen kann
    local jsStrings = cjson.encode({
        mic_start         = t("upload.mic_start"),
        mic_stop          = t("upload.mic_stop"),
        mic_done          = t("upload.mic_done"),
        mic_no_permission = t("upload.mic_no_permission"),
        mic_error         = t("upload.mic_error"),
        mic_unsupported   = t("upload.mic_unsupported"),
        analyze_error     = t("upload.analyze_error"),
        analyzing         = t("upload.analyzing"),
        needs_content     = t("upload.needs_content"),
        publishing        = t("general.publishing"),
        published         = t("general.published"),
        lang              = lang,
    })

    ngx.print([[<div class="upload-page">]])

    -- Schritt 1: Bilder (im Filemanager-Stil: Drop-Zone mit Icon/Titel)
    ngx.print(string.format([[
<section class="step" id="step-images">
  <div class="step-header"><span class="step-num">1</span><h2>%s</h2></div>
  <div class="drop-zone" id="image-drop"
       onclick="document.getElementById('image-input').click()">
    <div class="drop-icon">📷</div>
    <div class="drop-title">%s</div>
    <div class="drop-sub">%s</div>
  </div>
  <input type="file" id="image-input" accept="image/*,video/*" multiple capture="environment" style="display:none">
  <div class="image-previews" id="image-previews"></div>
  <div class="image-previews" id="video-previews" style="margin-top:8px"></div>
  <div id="video-name" class="video-name"></div>
</section>
]], t("upload.step_images"), t("upload.image_btn"), t("upload.media_hint")))

    -- Schritt 2: Spracheingabe
    ngx.print(string.format([[
<section class="step" id="step-voice">
  <div class="step-header"><span class="step-num">2</span><h2>%s</h2></div>
  <div class="voice-area">
]], t("upload.step_voice")))

    if llmEnabled then
        ngx.print(string.format([[
    <button class="btn-mic" id="mic-btn" onclick="toggleRecording()">
      <span class="mic-icon" id="mic-icon">&#9679;</span>
      <span id="mic-label">%s</span>
    </button>
    <p class="hint" id="mic-hint">%s</p>
]], t("upload.mic_start"), t("upload.mic_hint")))
    else
        ngx.print(string.format([[
    <p class="info-box">%s<br><small><code>%s</code></small></p>
]], t("upload.llm_disabled"), t("upload.llm_disabled_hint")))
    end

    ngx.print(string.format([[
    <textarea id="voice-text" placeholder="%s" rows="4" oninput="onVoiceTextChange()"></textarea>
  </div>
</section>
]], t("upload.voice_placeholder")))

    -- Schritt 3: Analyse
    ngx.print(string.format([[
<section class="step" id="step-analyze">
  <div class="step-header"><span class="step-num">3</span><h2>%s</h2></div>
]], t("upload.step_analyze")))
    if llmEnabled then
        ngx.print(string.format([[
  <button class="btn" id="analyze-btn" onclick="runAnalysis()" disabled>%s</button>
  <div class="analysis-status" id="analysis-status" style="display:none">
    <span class="spinner"></span> <span id="analysis-status-text">%s</span>
  </div>
]], t("upload.analyze_btn"), t("upload.analyzing")))
    else
        ngx.print("<p class='hint'>" .. t("upload.analyze_skipped") .. "</p>")
    end
    ngx.print("</section>")

    -- Schritt 4: Formular
    -- Kategorien + Zustände als lokalisierte Optionen
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

    local cond_opts, cat_opts = {}, {}
    for _, c in ipairs(conditions) do
        table.insert(cond_opts, string.format('<option value="%s">%s</option>', c[1], c[2]))
    end
    for _, c in ipairs(categories) do
        table.insert(cat_opts, string.format('<option value="%s">%s</option>', c[1], c[2]))
    end

    ngx.print(string.format([[
<section class="step" id="step-form">
  <div class="step-header"><span class="step-num">4</span><h2>%s</h2></div>
  <form id="listing-form" onsubmit="submitListing(event)">
    <div class="field">
      <label for="f-title">%s</label>
      <input type="text" id="f-title" name="title" required placeholder="%s">
    </div>
    <div class="field-row">
      <div class="field">
        <label for="f-category">%s</label>
        <select id="f-category" name="category">%s</select>
      </div>
      <div class="field">
        <label for="f-condition">%s</label>
        <select id="f-condition" name="condition">%s</select>
      </div>
    </div>
    <div class="field">
      <label for="f-price">%s</label>
      <input type="number" id="f-price" name="price" min="0" step="0.01" placeholder="0.00">
    </div>
    <div class="field">
      <label for="f-seller-wallet">]] .. t("listing.seller_wallet") .. [[</label>
      <div style="display:flex;gap:6px;flex-wrap:wrap">
        <select id="f-seller-wallet" name="seller_wallet" style="flex:1;min-width:180px">
          <option value="">]] .. t("listing.seller_wallet_none") .. [[</option>
        </select>
        <button type="button" class="btn-sm" onclick="openAddrBook()">📖 ]] .. t("listing.manage_addrbook") .. [[</button>
      </div>
      <small class="meta">]] .. t("listing.seller_wallet_hint") .. [[</small>
    </div>
    <div class="field">
      <label>%s</label>
      <div class="fee-mode-row" style="margin:6px 0;font-size:13px">
        <label style="margin-right:16px;cursor:pointer">
          <input type="radio" name="f-delivery" value="pickup" checked onchange="deliveryModeChanged()"> %s
        </label>
        <label style="cursor:pointer">
          <input type="radio" name="f-delivery" value="shipping" onchange="deliveryModeChanged()"> %s
        </label>
      </div>
      <div id="f-shipping-cost-row" style="display:none;margin-top:6px">
        <input type="number" id="f-shipping-cost" min="0" step="0.01" placeholder="%s">
      </div>
    </div>
    <div class="field">
      <label for="f-description">%s</label>
      <textarea id="f-description" name="description" rows="5" placeholder="%s"></textarea>
    </div>
    <div class="field">
      <label for="f-keywords">%s</label>
      <input type="text" id="f-keywords" name="keywords" placeholder="%s">
    </div>
    <div class="field">
      <label for="f-plz">%s</label>
      <input type="text" id="f-plz" name="plz" inputmode="numeric" maxlength="5" placeholder="%s">
    </div>
    <div class="form-actions">
      <button type="submit" class="btn" id="submit-btn">%s</button>
      <span id="submit-status"></span>
    </div>
  </form>
</section>
</div>

<script>const FUNDUS_STRINGS = %s;</script>
<script src="/static/upload.js?v=]] .. require("render").rev() .. [["></script>
<script src="/static/richtext.js?v=]] .. require("render").rev() .. [["></script>
<script>
document.addEventListener("DOMContentLoaded", function(){
  if (typeof initRichEditor === "function") initRichEditor("f-description");
});
</script>
]],
        t("upload.step_form"),
        t("upload.field_title"),     t("upload.title_placeholder"),
        t("upload.field_category"),  table.concat(cat_opts),
        t("upload.field_condition"), table.concat(cond_opts),
        t("upload.field_price"),
        t("upload.field_delivery"),
        t("upload.delivery_pickup"),
        t("upload.delivery_shipping"),
        t("upload.shipping_cost_ph"),
        t("upload.field_description"), t("upload.desc_placeholder"),
        t("upload.field_keywords"),    t("upload.kw_placeholder"),
        t("upload.field_plz"),         t("upload.plz_placeholder"),
        t("general.publish"),
        jsStrings
    ))

    render.footer()
end

-- pages/job_new.lua
local render = require "render"
local i18n   = require "i18n"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t, lang = i18n.init()
    -- render.header setzt ngx.ctx.lang
    render.header("job.title", "jobs")
    lang = ngx.ctx.lang or lang

    local llmStatus, _ = render.api_get("/v1/analyze/status")
    local llmEnabled   = llmStatus and llmStatus.enabled and llmStatus.reachable

    local jobTypes = {
        { "offer",   t("job.type_offer")   },
        { "request", t("job.type_request") },
    }
    local typeOpts = {}
    for _, jt in ipairs(jobTypes) do
        table.insert(typeOpts, string.format('<option value="%s">%s</option>', jt[1], jt[2]))
    end

    local jsStr = cjson.encode({
        analyzing  = t("upload.analyzing"),
        analyze_error = t("upload.analyze_error"),
        publishing = t("general.publishing"),
        published  = t("general.published"),
        lang       = lang,
    })

    ngx.print(string.format([[
<div class="upload-page">
<section class="step">
  <div class="step-header"><span class="step-num">1</span><h2>%s</h2></div>
  <form id="job-form" onsubmit="submitJob(event)">
    <div class="field-row">
      <div class="field">
        <label>%s</label>
        <select id="j-type" name="type">%s</select>
      </div>
    </div>
    <div class="field">
      <label>%s</label>
      <input type="text" id="j-title" name="title" required placeholder="%s">
    </div>
    <div class="field">
      <label>%s</label>
      <textarea id="j-desc" name="description" rows="4" placeholder="%s"
                oninput="onJobDescChange()"></textarea>
    </div>
]], t("job.title"),
    t("job.col_type"), table.concat(typeOpts),
    t("job.col_title"), t("upload.title_placeholder"),
    t("upload.field_description"), t("upload.desc_placeholder")))

    if llmEnabled then
        ngx.print(string.format([[
    <button type="button" class="btn" id="job-analyze-btn"
            onclick="polishJobText()" disabled>%s</button>
    <div class="analysis-status" id="analysis-status" style="display:none">
      <span class="spinner"></span> <span id="analysis-status-text">%s</span>
    </div>
]], t("upload.analyze_btn"), t("upload.analyzing")))
    end

    ngx.print(string.format([[
    <div class="field">
      <label>%s</label>
      <input type="text" id="j-keywords" name="keywords" placeholder="%s">
    </div>
    <div class="form-actions">
      <button type="submit" class="btn" id="submit-btn">%s</button>
      <span id="submit-status"></span>
    </div>
  </form>
</section>
</div>
<script>const FUNDUS_STRINGS = %s;</script>
<script src="/static/job.js?v=]] .. require("render").rev() .. [["></script>
]], t("upload.field_keywords"), t("upload.kw_placeholder"),
    t("general.publish"), jsStr))

    render.footer()
end

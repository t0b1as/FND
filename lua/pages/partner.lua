-- pages/partner.lua
-- Partner-Profil mit vollständigen Präferenz-Listen.
-- Alle Daten bleiben lokal – nur Hashes werden publiziert.
local render = require "render"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("partner.title", "partner")

    -- Daten laden
    local profile, _   = render.api_get("/v1/partner/profile")
    local lists, lerr  = render.api_get("/v1/partner/preferences/lists")
    -- Matches werden clientseitig per fetch geladen (loadMatches im JS-Teil),
    -- damit das Session-Cookie zuverlässig mitgeht (wie beim Profil).

    -- Hilfsfunktion: prüft ob ein Wert in einer Liste ist
    local function selected(list, val)
        if not list then return false end
        for _, v in ipairs(list) do
            if v == val then return true end
        end
        return false
    end

    -- Hilfsfunktion: rendert eine Checkbox-Gruppe
    local function checkbox_group(name, items, selected_fn, label_key)
        local parts = {"<div class='pref-grid'>"}
        for _, item in ipairs(items) do
            local val    = item.Value
            local label  = (ngx.ctx.lang == "en" and item.LabelEN) or item.LabelDE
            local chk    = selected_fn(val) and " checked" or ""
            table.insert(parts, string.format(
                '<label class="pref-check"><input type="checkbox" name="%s" value="%s"%s> %s</label>',
                name, val, chk, label
            ))
        end
        -- "Eigener Wert" Eingabefeld
        table.insert(parts, string.format(
            '<div class="pref-custom"><input type="text" class="pref-custom-input" placeholder="+ %s …" data-group="%s"></div>',
            t("partner.add_custom"), name
        ))
        table.insert(parts, "</div>")
        return table.concat(parts)
    end

    -- Profil-Werte extrahieren
    local function pget(key)
        return (profile and profile[key]) or ""
    end
    local function plist(key)
        if profile and type(profile[key]) == "table" then return profile[key] end
        return {}
    end
    local function seeking(key)
        if profile and profile.seeking and type(profile.seeking[key]) == "table" then
            return profile.seeking[key]
        end
        return {}
    end

    local hobbies     = plist("hobbies")
    local prefs       = plist("preferences")
    local dislikes    = plist("dislikes")
    local sexPrefs    = plist("sexual_prefs")
    local selSexAbbrs = {}
    for _, sp in ipairs(sexPrefs) do
        selSexAbbrs[sp.abbr] = sp.role or "switch"
    end

    -- Tabs-Navigation
    ngx.print([[
<div class="tab-nav" id="partner-tabs">
  <button class="tab-btn active" onclick="showTab('basics')">]] .. t("partner.tab_profile") .. [[</button>
  <button class="tab-btn"        onclick="showTab('hobbies')">]] .. t("partner.tab_hobbies") .. [[</button>
  <button class="tab-btn"        onclick="showTab('job')">]] .. t("partner.tab_job") .. [[</button>
  <button class="tab-btn"        onclick="showTab('sex')">]] .. t("partner.tab_sex") .. [[</button>
  <button class="tab-btn"        onclick="showTab('seeking')">]] .. t("partner.tab_seeking") .. [[</button>
  <button class="tab-btn"        onclick="showTab('search')">🔍 Suche</button>
  <button class="tab-btn"        onclick="showTab('matches')">]] .. t("partner.tab_matches") .. [[</button>
</div>

<form id="profile-form" novalidate onsubmit="saveProfile(event); return false;">
]])

    -- =========================================================================
    --  TAB 1: Basics
    -- =========================================================================
    local ageRanges = {{"18-25","18–25"},{"26-35","26–35"},{"36-45","36–45"},{"46-55","46–55"},{"56+","56+"}}
    local ageOpts = {}
    for _, o in ipairs(ageRanges) do
        table.insert(ageOpts, string.format('<option value="%s"%s>%s</option>',
            o[1], o[1] == pget("age_range") and " selected" or "", o[2]))
    end

    ngx.print(string.format([[
<div class="tab-panel" id="tab-basics">
  <section class="step">
    <div class="step-header"><span class="step-num">1</span><h2>%s</h2></div>
    <div class="field-row">
      <div class="field">
        <label>%s</label>
        <input type="text" id="p-nick" name="nickname" value="%s" placeholder="z.B. Wanderfreund42">
      </div>
      <div class="field">
        <label>%s</label>
        <select id="p-gender" name="gender">
          <option value="male"%s>]] .. t("partner.gender_male") .. [[</option>
          <option value="female"%s>]] .. t("partner.gender_female") .. [[</option>
        </select>
      </div>
    </div>
    <div class="field-row">
      <div class="field">
        <label>Geburtsdatum</label>
        <input type="date" id="p-birthdate" name="birthdate" max="2007-12-31" value="%s">
        <small class="meta">Dein Alter wird automatisch berechnet und aktualisiert sich.</small>
      </div>
      <div class="field">
        <label>Berechnetes Alter</label>
        <input type="text" id="p-age-display" readonly placeholder="—" style="background:var(--bg)">
      </div>
      <div class="field">
        <label>%s</label>
        <input type="number" id="p-radius" name="radius_km" value="%s" min="1" max="500" step="1">
      </div>
    </div>
    <div class="field">
      <label>%s <small class="meta" style="color:var(--amber)">(wird im Netzwerk öffentlich geteilt)</small></label>
      <textarea id="p-bio" name="bio" rows="4" placeholder="Erzähl etwas über dich…">%s</textarea>
    </div>
    <div class="field">
      <label>%s</label>
      <input type="file" id="p-image-input" accept="image/*,video/*" multiple style="display:none" onchange="handlePartnerMedia(this.files)">
      <button type="button" class="btn" onclick="document.getElementById('p-image-input').click()"
              style="height:40px;font-size:14px">%s</button>
      <div id="p-image-previews" style="display:grid;grid-template-columns:repeat(auto-fill,110px);grid-auto-rows:110px;gap:8px;margin-top:10px;justify-content:start"></div>
      <div id="p-video-preview" style="display:flex;gap:8px;flex-wrap:wrap;margin-top:10px"></div>
    </div>
  </section>
</div>
]],
        t("partner.step_profile"),
        t("partner.label_nickname"), ngx.escape_uri(pget("nickname")),
        t("partner.label_gender"),   (pget("gender") == "male" and " selected" or ""), (pget("gender") == "female" and " selected" or ""),
        ngx.escape_uri(pget("birthdate") or ""),
        t("partner.label_radius"),   tostring(
            (profile and profile.seeking and profile.seeking.radius_km) or 50),
        t("partner.label_bio"),      ngx.escape_uri(pget("bio")),
        t("partner.label_images"),   t("partner.btn_add_images")
    ))

    -- =========================================================================
    --  TAB 2: Hobbies & Vorlieben / Abneigungen
    -- =========================================================================
    ngx.print('<div class="tab-panel hidden" id="tab-hobbies">')

    if lerr or not lists then
        ngx.print("<p class='error'>" .. t("partner.lists_load_error") .. "</p>")
    else
        -- Hobbies
        ngx.print(string.format([[
<section class="step">
  <div class="step-header"><span class="step-num">2</span><h2>]] .. t("partner.h_hobbies") .. [[</h2></div>
  <p class="meta">]] .. t("partner.hobbies_hint") .. [[</p>
  %s
</section>
]], checkbox_group("hobbies", lists.hobbies or {}, function(v) return selected(hobbies, v) end, "hobbies")))

        -- Vorlieben
        ngx.print(string.format([[
<section class="step" style="margin-top:1rem">
  <div class="step-header"><span class="step-num">3</span><h2>]] .. t("partner.h_preferences") .. [[</h2></div>
  <p class="meta">]] .. t("partner.prefs_hint") .. [[</p>
  %s
</section>
]], checkbox_group("preferences", lists.preferences or {}, function(v) return selected(prefs, v) end, "prefs")))

        -- Abneigungen
        ngx.print(string.format([[
<section class="step" style="margin-top:1rem">
  <div class="step-header"><span class="step-num">4</span><h2>]] .. t("partner.h_dislikes") .. [[</h2></div>
  <p class="meta">]] .. t("partner.dislikes_hint") .. [[</p>
  %s
</section>
]], checkbox_group("dislikes", lists.dislikes or {}, function(v) return selected(dislikes, v) end, "dislikes")))
    end

    ngx.print("</div>") -- tab-hobbies

    -- =========================================================================
    --  TAB 3: Beruf & Bildung
    -- =========================================================================
    ngx.print('<div class="tab-panel hidden" id="tab-job"><section class="step">')
    ngx.print('<div class="step-header"><span class="step-num">5</span><h2>' .. t("partner.h_job_edu") .. '</h2></div>')

    -- Ausbildungsgrad
    ngx.print("<div class='field'><label>" .. t("partner.label_education") .. "</label>")
    ngx.print("<select id='p-edu' name='education'>")
    ngx.print("<option value=''>– bitte wählen –</option>")
    if lists and lists.education_levels then
        for _, el in ipairs(lists.education_levels) do
            local sel = el.Value == pget("education") and " selected" or ""
            local lbl = (ngx.ctx.lang == "en" and el.LabelEN) or el.LabelDE
            ngx.print(string.format('<option value="%s"%s>%s</option>', el.Value, sel, lbl))
        end
    end
    ngx.print("</select></div>")

    -- Berufsbezeichnung (Freitext)
    ngx.print(string.format([[
<div class="field">
  <label>%s</label>
  <input type="text" id="p-profession" name="profession" value="%s" placeholder="z.B. Softwareentwickler, Lehrerin, Koch">
</div>
]], t("partner.label_profession"), ngx.escape_uri(pget("profession"))))

    -- Branche
    if lists and lists.industries then
        ngx.print("<div class='field'><label>" .. t("partner.label_industry") .. "</label>")
        ngx.print("<select id='p-industry' name='industry'>")
        ngx.print("<option value=''>– bitte wählen –</option>")
        for _, ind in ipairs(lists.industries) do
            local sel = ind.Value == pget("industry") and " selected" or ""
            local lbl = (ngx.ctx.lang == "en" and ind.LabelEN) or ind.LabelDE
            ngx.print(string.format('<option value="%s"%s>%s</option>', ind.Value, sel, lbl))
        end
        ngx.print("</select></div>")
    end

    ngx.print("</section></div>") -- tab-job

    -- =========================================================================
    --  TAB 4: Sexuelle Vorlieben
    -- =========================================================================
    ngx.print('<div class="tab-panel hidden" id="tab-sex"><section class="step">')
    ngx.print(string.format(
        '<div class="step-header"><span class="step-num">6</span><h2>%s</h2></div>',
        t("partner.label_sex_prefs")))
    ngx.print('<p class="meta">' .. t("partner.sex_prefs_hint") .. '</p>')

    if lists and lists.sexual_preferences then
        -- Kompaktes Layout wie "Ich suche": Checkboxen einfach hintereinander im
        -- pref-grid. Erklärung als Tooltip (title), Rollen-Buttons erscheinen nur
        -- bei Auswahl darunter.
        ngx.print('<div class="pref-grid">')
        for _, sp in ipairs(lists.sexual_preferences) do
            local abbr    = sp.Abbr
            local labelDE = sp.LabelDE
            local labelEN = sp.LabelEN
            local exDE    = sp.ExplainDE
            local exEN    = sp.ExplainEN
            local hasRoles = sp.HasRoles
            local label   = (ngx.ctx.lang == "en" and labelEN) or labelDE
            local explain = (ngx.ctx.lang == "en" and exEN)    or exDE
            local curRole = selSexAbbrs[abbr] or ""
            local isChk   = curRole ~= "" and ' checked' or ''
            ngx.print(string.format(
                '<label class="pref-check" title="%s"><input type="checkbox" class="sex-abbr-cb" data-abbr="%s" data-has-roles="%s"%s> %s %s</label>',
                explain:gsub('"','&quot;'), abbr, hasRoles and "true" or "false", isChk, abbr, label))
        end
        ngx.print('</div>')

        -- Rollen-Auswahl (Aktiv/Passiv/Switch) für die Vorlieben mit Rollen,
        -- gesammelt unter dem Grid. Erscheint nur, wenn die zugehörige Vorliebe
        -- angehakt ist (per JS ein-/ausgeblendet).
        for _, sp in ipairs(lists.sexual_preferences) do
            if sp.HasRoles then
                local abbr = sp.Abbr
                local curRole = selSexAbbrs[abbr] or ""
                local shown = curRole ~= "" and "" or " hidden"
                local roles = {{"aktiv","Aktiv (Top/Geber)"},{"passiv","Passiv (Bottom/Empfänger)"},{"switch","Switch (beides)"}}
                if ngx.ctx.lang == "en" then
                    roles = {{"aktiv","Active (Top/Giver)"},{"passiv","Passive (Bottom/Receiver)"},{"switch","Switch (both)"}}
                end
                ngx.print(string.format('<div class="role-btns%s" id="roles-%s" style="margin-top:6px"><span class="abbr-badge">%s</span> ', shown, abbr, abbr))
                for _, role in ipairs(roles) do
                    local active = curRole == role[1] and " role-active" or ""
                    ngx.print(string.format(
                        '<button type="button" class="role-btn%s" data-abbr="%s" data-role="%s" onclick="setRole(this)">%s</button>',
                        active, abbr, role[1], role[2]))
                end
                ngx.print("</div>")
            end
        end

        -- Custom-Feld
        ngx.print(string.format([[
<div class="pref-custom" style="margin-top:1rem">
  <input type="text" class="pref-custom-input" placeholder="+ Eigene Abkürzung / Vorliebe …" data-group="sex_custom">
</div>
]]))
    end

    ngx.print("</section></div>") -- tab-sex

    -- =========================================================================
    --  TAB 5: Ich suche
    -- =========================================================================
    local seekGenders = seeking("genders")
    ngx.print('<div class="tab-panel hidden" id="tab-seeking"><section class="step">')
    ngx.print('<div class="step-header"><span class="step-num">7</span><h2>' .. t("partner.h_seeking") .. '</h2></div>')

    ngx.print(string.format([[
<div class="field">
  <label>%s</label>
  <select id="p-seek-gender" name="seek_gender">
    <option value="male"%s>]] .. t("partner.gender_male") .. [[</option>
    <option value="female"%s>]] .. t("partner.gender_female") .. [[</option>
  </select>
</div>
]], t("partner.label_seek_gender"),
    (seekGenders[1] == "male" and " selected" or ""),
    (seekGenders[1] == "female" and " selected" or "")))

    -- Altersklassen suchen
    ngx.print("<div class='field'><label>" .. t("partner.label_seek_age") .. "</label><div class='pref-grid'>")
    local seekAges = seeking("age_ranges")
    for _, o in ipairs(ageRanges) do
        local chk = selected(seekAges, o[1]) and " checked" or ""
        ngx.print(string.format(
            '<label class="pref-check"><input type="checkbox" name="seek_age" value="%s"%s> %s</label>',
            o[1], chk, o[2]))
    end
    ngx.print("</div></div>")

    -- Gesuchte Ausbildungsgrade
    ngx.print("<div class='field'><label>" .. t("partner.label_seek_edu") .. "</label><div class='pref-grid'>")
    local seekEdus = seeking("educations")
    if lists and lists.education_levels then
        for _, el in ipairs(lists.education_levels) do
            local chk = selected(seekEdus, el.Value) and " checked" or ""
            local lbl = (ngx.ctx.lang == "en" and el.LabelEN) or el.LabelDE
            ngx.print(string.format(
                '<label class="pref-check"><input type="checkbox" name="seek_edu" value="%s"%s> %s</label>',
                el.Value, chk, lbl))
        end
    end
    ngx.print("</div></div>")

    -- Gesuchte sexuelle Vorlieben
    local seekSexAbbrs = seeking("sex_pref_abbrs")
    ngx.print("<div class='field'><label>" .. t("partner.label_seek_sex") .. "</label><div class='pref-grid'>")
    if lists and lists.sexual_preferences then
        for _, sp in ipairs(lists.sexual_preferences) do
            local chk  = selected(seekSexAbbrs, sp.Abbr) and " checked" or ""
            local label = (ngx.ctx.lang == "en" and sp.LabelEN) or sp.LabelDE
            ngx.print(string.format(
                '<label class="pref-check"><input type="checkbox" name="seek_sex" value="%s"%s> <span class="abbr-badge small">%s</span> %s</label>',
                sp.Abbr, chk, sp.Abbr, label))
        end
    end
    ngx.print("</div></div>")

    ngx.print("</section></div>") -- tab-seeking

    -- =========================================================================
    --  TAB 6: Matches
    -- =========================================================================
    ngx.print(string.format([[
<div class="tab-panel hidden" id="tab-matches">
  <section class="step">
    <div class="step-header"><span class="step-num">✓</span><h2>%s (<span id="match-count">…</span>)</h2></div>
    <div id="matches-container"><p class="empty">%s</p></div>
  </section>
</div>
]], t("partner.step_matches"), t("general.loading")))

    -- =========================================================================
    --  TAB 7: Parametrische Suche
    -- =========================================================================
    if lists and lists.sexual_preferences then
        local sexOptsSearch = {}
        for _, sp in ipairs(lists.sexual_preferences) do
            local label = (ngx.ctx.lang == "en" and sp.LabelEN) or sp.LabelDE
            table.insert(sexOptsSearch, string.format(
                '<label class="pref-check"><input type="checkbox" class="srch-sex" value="%s"> <span class="abbr-badge small">%s</span> %s</label>',
                sp.Abbr, sp.Abbr, label))
        end

        ngx.print([[
<div class="tab-panel hidden" id="tab-search">
<section class="step">
  <div class="step-header"><span class="step-num">🔍</span><h2>]] .. t("partner.h_param_search") .. [[</h2></div>
  <p class="meta">
    Sucht in öffentlichen Suchprofilen die andere Nutzer freiwillig veröffentlicht haben.
    Nur Kategorien – keine Klartextnamen oder genauen Koordinaten.
  </p>

  <div class="field-row">
    <div class="field">
      <label>]] .. t("partner.label_radius") .. [[</label>
      <input type="number" id="srch-radius" value="50" min="1" max="500" step="5">
    </div>
    <div class="field">
      <label>]] .. t("partner.label_sort") .. [[</label>
      <select id="srch-sort">
        <option value="score">]] .. t("partner.sort_score") .. [[</option>
        <option value="distance">]] .. t("partner.sort_distance") .. [[</option>
        <option value="hobbies">]] .. t("partner.sort_hobbies") .. [[</option>
        <option value="sex">]] .. t("partner.sort_sex") .. [[</option>
      </select>
    </div>
  </div>

  <div class="field-row">
    <div class="field">
      <label>]] .. t("partner.label_search_gender") .. [[</label>
      <input type="text" id="srch-gender" placeholder="female, male, non-binary …" style="display:none">
      <select id="srch-gender-sel">
        <option value="">]] .. t("partner.gender_any") .. [[</option>
        <option value="male">]] .. t("partner.gender_male") .. [[</option>
        <option value="female">]] .. t("partner.gender_female") .. [[</option>
      </select>
    </div>
    <div class="field">
      <label>]] .. t("partner.label_age_class") .. [[</label>
      <div style="display:flex;gap:8px;align-items:center">
        <input type="number" id="srch-age-min" min="18" max="120" placeholder="von" style="width:80px">
        <span>–</span>
        <input type="number" id="srch-age-max" min="18" max="120" placeholder="bis" style="width:80px">
      </div>
    </div></div>
]])

        -- Bildung & Branche
        ngx.print([[
  <div class="field-row">
    <div class="field">
      <label>]] .. t("partner.label_edu_level") .. [[</label>
      <div class="pref-grid" style="padding:6px">
        <label class="pref-check"><input type="checkbox" class="srch-edu" value="school"> Schule</label>
        <label class="pref-check"><input type="checkbox" class="srch-edu" value="vocational"> Ausbildung</label>
        <label class="pref-check"><input type="checkbox" class="srch-edu" value="university"> Studium</label>
        <label class="pref-check"><input type="checkbox" class="srch-edu" value="self-taught"> Autodidakt</label>
      </div>
    </div>
    <div class="field">
      <label>]] .. t("partner.label_reciprocity") .. [[</label>
      <label class="pref-check" style="border:none;padding:0">
        <input type="checkbox" id="srch-mutual"> Nur gegenseitige Matches
      </label>
    </div>
  </div>

  <div class="field">
    <label>]] .. t("partner.label_hobbies_match") .. [[</label>
]])
        if lists.hobbies then
            ngx.print("<div class='pref-grid'>")
            for _, h in ipairs(lists.hobbies) do
                local lbl = (ngx.ctx.lang == "en" and h.LabelEN) or h.LabelDE
                ngx.print(string.format(
                    '<label class="pref-check"><input type="checkbox" class="srch-hobby" value="%s"> %s</label>',
                    h.Value, lbl))
            end
            ngx.print("</div>")
        end

        ngx.print("</div>") -- field hobbies

        -- Sex-Prefs Suche
        ngx.print([[
  <div class="field">
    <label>]] .. t("partner.label_sex_match") .. [[</label>
    <div class="pref-grid">
]])
        ngx.print(table.concat(sexOptsSearch))
        ngx.print([[
    </div>
    <div class="pref-custom" style="margin-top:8px">
      <input type="text" class="pref-custom-input" placeholder="+ ]] .. t("partner.custom_own") .. [[ …" data-group="srch_sex_custom">
    </div>
  </div>

  <div class="form-actions">
    <button type="button" class="btn" onclick="runSearch()">]] .. t("partner.btn_search") .. [[</button>
    <button type="button" class="btn" style="background:none;border-color:var(--border);color:var(--muted)"
            onclick="clearSearch()">]] .. t("partner.btn_reset") .. [[</button>
    <span id="search-status"></span>
  </div>
</section>

<section class="step" id="search-results-section" style="margin-top:1rem;display:none">
  <h2>]] .. t("partner.h_results") .. [[ <span id="search-count" class="meta"></span></h2>
  <div id="search-results" class="status-grid"></div>
  <div id="search-pagination" style="margin-top:1rem;display:flex;gap:8px;align-items:center"></div>
</section>
</div>
]]) -- tab-search
    end

    -- =========================================================================
    --  Aktionsleiste
    -- =========================================================================
    ngx.print(string.format([[
<div class="form-actions" style="margin-top:1.5rem;position:sticky;bottom:0;background:var(--surface);padding:1rem 0;border-top:1px solid var(--border)">
  <button type="submit" class="btn">%s</button>
  <span id="partner-status"></span>
</div>
</form>
]], t("partner.btn_save")))

    -- =========================================================================
    --  JavaScript
    -- =========================================================================
    local jsStr = cjson.encode({
        saving     = t("partner.saving"),
        saved      = t("partner.saved"),
        publishing = t("partner.publishing"),
        published  = t("partner.published"),
        error      = t("partner.error"),
        no_results = t("partner.no_results"),
        col_peer   = t("partner.col_peer"),
        col_score  = t("partner.col_score"),
        col_distance = t("partner.col_distance"),
        col_age_class = t("partner.col_age_class"),
        col_hobbies = t("partner.col_hobbies"),
        col_prefs  = t("partner.col_prefs"),
        btn_next   = t("partner.btn_next"),
        btn_prev   = t("partner.btn_prev"),
        page_label = t("partner.page_label"),
        searching  = t("partner.searching"),
    })

    ngx.print(string.format([[
<script src="/static/richtext.js?v=]] .. require("render").rev() .. [["></script>
<script>
const PS = %s;
window.PARTNER_PROFILE = { image_hashes: %s, video_hashes: %s };

function escapeHtml(s){ return String(s==null?'':s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
// decodeMaybe dekodiert alte URL-encodierte Bios zurück zu Klartext.
function decodeMaybe(s){ if(!s) return ''; try { return /%%[0-9A-Fa-f]{2}/.test(s) ? decodeURIComponent(s) : s; } catch(e){ return s; } }
</script>
]], jsStr,
    require("cjson.safe").encode((profile and profile.image_hashes) or {}) or "[]",
    require("cjson.safe").encode((profile and profile.video_hashes) or {}) or "[]"))
    ngx.print('<script src="/static/partner.js?v=' .. render.rev() .. '"></script>\n')

    render.footer()
end

-- pages/partner_show.lua – Matching-Ergebnis-Anzeige
-- Wird nach der Präferenzsuche aufgerufen: GET /partner/matches
local render = require "render"
local cjson  = require "cjson.safe"

return function()
  ngx.header["Content-Type"] = "text/html"
  local t = render.header("partner.matches_title", "partner")

  local matches, err = render.api_get("/v1/partner/matches")

  if err then
    ngx.print("<p class='error'>" .. err .. "</p>")
    render.footer(); return
  end

  local list = matches and matches.matches or {}
  ngx.print(string.format([[
<p class="page-meta">%d %s · <a href="/partner">← %s</a></p>
<div class="match-list">
]],
    #list,
    t("partner.matches_found"),
    t("general.back")
  ))

  if #list == 0 then
    ngx.print(string.format([[
<div class="match-empty">
  <div class="match-empty-icon">💔</div>
  <div>%s</div>
  <a href="/partner" class="btn-sm" style="margin-top:12px;display:inline-block">%s</a>
</div>
]], t("partner.no_matches"), t("partner.adjust_prefs")))
  else
    for _, m in ipairs(list) do
      local score = math.floor((m.score or 0) * 100)
      local scoreColor = score >= 80 and "var(--grn)" or score >= 50 and "var(--amber)" or "var(--muted)"
      -- Bilder-Strip aus image_hashes (öffentliches Profil).
      local imgHtml = ""
      if m.image_hashes and #m.image_hashes > 0 then
        imgHtml = "<div class='match-images'>"
        for _, h in ipairs(m.image_hashes) do
          imgHtml = imgHtml .. "<img src='/api/v1/files/download/" .. h .. "' loading='lazy' " ..
            "onclick=\"window.open(this.src)\" style='cursor:pointer'>"
        end
        imgHtml = imgHtml .. "</div>"
      end
      local nick = m.nickname and m.nickname ~= "" and m.nickname or ((m.fundus_id or "–"):sub(1,12) .. "…")
      local bio = m.bio and m.bio ~= "" and ("<div class='match-bio'>" .. bio_escape(m.bio) .. "</div>") or ""
      ngx.print(string.format([[
<div class="match-card">
  <div class="match-score-bar" style="--score-color:%s">
    <div class="match-score-fill" style="width:%d%%"></div>
  </div>
  %s
  <div class="match-body">
    <div class="match-header">
      <span class="match-id">%s</span>
      <span class="match-score" style="color:%s">%d%% %s</span>
    </div>
    %s
    <div class="match-tags">%s</div>
    <div class="match-actions">
      <button class="btn-contact" onclick="contactMatch('%s')">💬 %s</button>
    </div>
  </div>
</div>
]],
        scoreColor, score,
        imgHtml,
        bio_escape(nick),
        scoreColor, score, t("partner.match_pct"),
        bio,
        buildTags(m.common_interests or {}),
        m.fundus_id or "",
        t("partner.contact")
      ))
    end
  end

  ngx.print([[</div>

<style>
.page-meta{font-size:12.5px;color:var(--muted);margin-bottom:1.25rem}
.match-list{display:flex;flex-direction:column;gap:10px}
.match-card{background:var(--sur);border:1px solid var(--brd);border-radius:var(--r);overflow:hidden}
.match-score-bar{height:3px;background:var(--brd2)}
.match-score-fill{height:100%;background:var(--score-color,var(--grn));transition:width .5s}
.match-body{padding:.9rem 1.1rem}
.match-header{display:flex;align-items:center;justify-content:space-between;margin-bottom:8px}
.match-id{font-size:10.5px;color:var(--muted)}
.match-score{font-size:13px;font-weight:700}
.match-tags{display:flex;gap:5px;flex-wrap:wrap;margin-bottom:10px}
.match-images{display:flex;gap:6px;overflow-x:auto;padding:8px 1.1rem 0}
.match-images img{width:110px;height:110px;object-fit:cover;border-radius:10px;flex-shrink:0;border:1px solid var(--brd)}
.match-bio{font-size:13px;color:var(--muted);margin-bottom:10px;line-height:1.4}
.match-tag{font-size:10px;font-weight:600;padding:2px 7px;border-radius:9999px;
  background:var(--grn-bg);border:1px solid var(--grn-brd);color:var(--grn)}
.match-actions{display:flex;gap:8px}
.btn-contact{padding:6px 14px;background:var(--acc);color:#fff;border:none;
  border-radius:var(--rs);cursor:pointer;font-size:13px;font-weight:500}
.btn-contact:hover{background:#3b7cf5}
.btn-sm{padding:4px 12px;font-size:12px;background:var(--sur2);
  border:1px solid var(--brd2);border-radius:var(--rs);color:var(--dim);
  cursor:pointer;text-decoration:none}
.match-empty{text-align:center;padding:3rem 1rem;color:var(--muted)}
.match-empty-icon{font-size:48px;margin-bottom:12px}
</style>

<script>
async function contactMatch(fundusId) {
  // Leitet zum Messenger mit vorausgefüllter Empfänger-ID
  window.location.href = '/messenger?to=' + encodeURIComponent(fundusId);
}
</script>
]])

  render.footer()
end

function buildTags(interests)
  if not interests or #interests == 0 then return "" end
  local out = {}
  for _, v in ipairs(interests) do
    table.insert(out, '<span class="match-tag">' .. tostring(v) .. '</span>')
  end
  return table.concat(out)
end

-- bio_escape: einfaches HTML-Escaping für offene Profiltexte (Nickname, Bio).
function bio_escape(s)
  if not s then return "" end
  s = tostring(s)
  s = s:gsub("&", "&amp;"):gsub("<", "&lt;"):gsub(">", "&gt;"):gsub('"', "&quot;")
  return s
end

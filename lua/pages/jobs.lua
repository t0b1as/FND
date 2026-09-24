-- pages/jobs.lua – Job-Börse mit netzweiter Suche (Gebote & Gesuche)
local render = require "render"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("job.title", "jobs")

    -- Übersetzte Strings für den JS-Teil (renderJobResults / Status).
    local js = cjson.encode({
        searching     = t("job.searching"),
        no_results    = t("job.no_results"),
        wait_collect  = t("general.wait_collecting"),
        badge_offer   = t("job.badge_offer"),
        badge_request = t("job.badge_request"),
        salary_from   = t("job.salary_from"),
        no_title      = t("job.no_title"),
    })

    ngx.print(string.format([[
<div class="job-page">
  <div class="job-head">
    <h1>%s</h1>
    <a href="/jobs/new" class="btn btn-green">%s</a>
  </div>

  <div class="search-bar">
    <input type="text" id="j-q" placeholder="%s" class="search-input"
           onkeydown="if(event.key==='Enter'){event.preventDefault();runJobSearch();}">
    <select id="j-type" class="search-select">
      <option value="">%s</option>
      <option value="offer">%s</option>
      <option value="request">%s</option>
    </select>
    <input type="text" id="j-plz" inputmode="numeric" maxlength="5" placeholder="%s"
           class="search-plz"
           onkeydown="if(event.key==='Enter'){event.preventDefault();runJobSearch();}">
    <select id="j-radius" class="search-select">
      <option value="">%s</option>
      <option value="10">10 km</option>
      <option value="25">25 km</option>
      <option value="50">50 km</option>
      <option value="100">100 km</option>
    </select>
    <label class="wait-complete" title="%s">
      <input type="checkbox" id="j-wait-complete">
      <span>%s</span>
    </label>
    <button class="btn" onclick="runJobSearch()">%s</button>
  </div>
  <div id="job-results"></div>
</div>

<script>
const JOBT = %s;
let jobSearchId = null;
let jobPollTimer = null;

async function runJobSearch() {
    const res = document.getElementById('job-results');
    res.innerHTML = '<p class="muted">' + JOBT.searching + '</p>';
    if (jobPollTimer) { clearInterval(jobPollTimer); jobPollTimer = null; }
    const waitComplete = document.getElementById('j-wait-complete').checked;

    const p = new URLSearchParams();
    const q = document.getElementById('j-q').value.trim();
    const type = document.getElementById('j-type').value;
    const plz = document.getElementById('j-plz').value.trim();
    const radius = document.getElementById('j-radius').value;
    if (q) p.set('q', q);
    if (type) p.set('job_type', type);
    if (plz) p.set('plz', plz);
    if (radius) p.set('radius_km', radius);

    try {
        const r = await fetch('/api/v1/jobsearch?' + p.toString());
        const d = await r.json();
        jobSearchId = d.search_id;
        if (waitComplete) {
            res.innerHTML = '<p class="muted">' + JOBT.wait_collect + '</p>';
            let polls = 0, latest = d.hits || [];
            jobPollTimer = setInterval(async () => {
                polls++;
                try {
                    const rr = await fetch('/api/v1/jobsearch/results?id=' + jobSearchId);
                    const dd = await rr.json();
                    latest = dd.hits || latest;
                } catch(e) {}
                if (polls >= 8) {
                    clearInterval(jobPollTimer); jobPollTimer = null;
                    renderJobResults(latest);
                }
            }, 1000);
        } else {
            renderJobResults(d.hits || []);
            let polls = 0;
            jobPollTimer = setInterval(async () => {
                polls++;
                try {
                    const rr = await fetch('/api/v1/jobsearch/results?id=' + jobSearchId);
                    const dd = await rr.json();
                    renderJobResults(dd.hits || []);
                } catch(e) {}
                if (polls >= 6) { clearInterval(jobPollTimer); jobPollTimer = null; }
            }, 1000);
        }
    } catch(e) {
        res.innerHTML = '<p class="error">' + e.message + '</p>';
    }
}

function renderJobResults(items) {
    const res = document.getElementById('job-results');
    if (!items.length) { res.innerHTML = '<p class="muted">' + JOBT.no_results + '</p>'; return; }
    let html = '<div class="job-list">';
    for (const it of items) {
        const badge = it.job_type === 'request'
            ? '<span class="job-badge req">' + JOBT.badge_request + '</span>'
            : '<span class="job-badge off">' + JOBT.badge_offer + '</span>';
        const loc = it.location ? (' · ' + escapeHtmlJob(it.location)) : '';
        const dist = (it.distance_km != null && it.distance_km > 0)
            ? (' · ' + it.distance_km + ' km') : '';
        const sal = (it.salary_min && it.salary_min > 0)
            ? ('<span class="job-sal">' + JOBT.salary_from + ' ' + it.salary_min + ' €</span>') : '';
        html += '<a class="job-item card" href="/jobs/' + encodeURIComponent(it.id) + '">'
              + badge
              + '<div class="job-item-main"><div class="job-item-title">'
              + escapeHtmlJob(it.title || JOBT.no_title) + '</div>'
              + '<div class="job-item-sub muted">' + escapeHtmlJob(it.category || '')
              + loc + dist + '</div></div>' + sal + '</a>';
    }
    html += '</div>';
    res.innerHTML = html;
}

function escapeHtmlJob(s) {
    return (s || '').replace(/[&<>"']/g, c =>
        ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

runJobSearch();
</script>
]],
    t("job.board_title"), t("job.new_listing"),
    t("job.search_placeholder"),
    t("job.filter_all"), t("job.filter_offers"), t("job.filter_requests"),
    t("general.plz_short"),
    t("general.radius_any"),
    t("general.wait_complete_tip"), t("general.wait_complete"),
    t("general.search"),
    js))

    render.footer()
end

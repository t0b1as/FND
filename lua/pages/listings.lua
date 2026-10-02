-- pages/listings.lua
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("listing.title_page", "listings")

    ngx.print('<div class="top-bar"><a href="/listings/new" class="btn">' .. t("listing.new") .. '</a><div id="filter-mount"></div></div>')

    -- Suchsteuerung ist in die Filterleiste (bei den Ergebnissen) gewandert.
    -- Versteckte Elemente behalten, damit der bestehende Such-JS-Code sie lesen kann.
    ngx.print(string.format([[
<div style="display:none">
  <input type="text" id="s-q">
  <input type="text" id="s-plz">
  <select id="s-radius"><option value="">%s</option><option value="10">10 km</option><option value="25">25 km</option><option value="50">50 km</option><option value="100">100 km</option></select>
  <select id="s-category"><option value="">%s</option><option value="möbel">%s</option><option value="elektronik">%s</option><option value="kleidung">%s</option><option value="fahrzeug">%s</option><option value="werkzeug">%s</option><option value="sonstiges">%s</option></select>
  <select id="s-sort"><option value="newest">%s</option><option value="distance">%s</option><option value="price">%s</option></select>
  <input type="checkbox" id="s-wait-complete">
</div>
<div id="results"></div>
<script>
const SRCHT = { wait_collecting: "%s", col_title: "%s", col_price: "%s", col_condition: "%s", col_category: "%s", col_distance: "%s" };

let currentSearchId = null;
let pollTimer = null;
let currentSort = 'newest';

async function runSearch() {
    const p = new URLSearchParams();
    const q = document.getElementById('s-q').value.trim();
    const cat = document.getElementById('s-category').value;
    const plz = document.getElementById('s-plz').value.trim();
    const radius = document.getElementById('s-radius').value;
    currentSort = document.getElementById('s-sort').value || 'newest';
    if (q) p.set('q', q);
    if (cat) p.set('category', cat);
    if (plz) p.set('plz', plz);
    if (radius) p.set('radius_km', radius);
    const res = document.getElementById('results');
    res.innerHTML = '<p class="muted">%s</p>';
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
    const waitComplete = document.getElementById('s-wait-complete').checked;
    try {
        // Netzwerkweite Suche: lokale Treffer sofort, dann Netz-Treffer pollen
        const r = await fetch('/api/v1/search?' + p.toString());
        const d = await r.json();
        currentSearchId = d.search_id;

        if (waitComplete) {
            // Modus „komplette Netzantwort": NICHT nach jedem Poll rendern,
            // sondern Antworten sammeln und erst am Ende EINMAL korrekt sortiert
            // anzeigen. Reihenfolge/Filter sind dann vollständig statt vorläufig.
            res.innerHTML = '<p class="muted">' + SRCHT.wait_collecting + '</p>';
            let polls = 0;
            let latest = d.hits || [];
            pollTimer = setInterval(async () => {
                polls++;
                try {
                    const rr = await fetch('/api/v1/search/results?id=' + currentSearchId);
                    const dd = await rr.json();
                    latest = dd.hits || latest;
                } catch(e) {}
                if (polls >= 8) { // etwas länger warten für Vollständigkeit
                    clearInterval(pollTimer); pollTimer = null;
                    renderResults(sortHits(latest)); // einmal, vollständig sortiert
                }
            }, 1000);
        } else {
            // Progressiver Modus: Treffer erscheinen sofort und wachsen live.
            renderResults(sortHits(d.hits || []));
            let polls = 0;
            pollTimer = setInterval(async () => {
                polls++;
                try {
                    const rr = await fetch('/api/v1/search/results?id=' + currentSearchId);
                    const dd = await rr.json();
                    updateResults(sortHits(dd.hits || [])); // nur bei Änderung neu zeichnen
                } catch(e) {}
                if (polls >= 6) { clearInterval(pollTimer); pollTimer = null; }
            }, 1000);
        }
    } catch(e) {
        res.innerHTML = '<p class="error">' + e.message + '</p>';
    }
}

function sortHits(hits) {
    const h = hits.slice();
    if (currentSort === 'distance') {
        h.sort((a,b) => (a.distance_km==null?1e9:a.distance_km) - (b.distance_km==null?1e9:b.distance_km));
    } else if (currentSort === 'price') {
        h.sort((a,b) => (a.price_min||0) - (b.price_min||0));
    }
    return h;
}
let _allResults = [];   // gefilterte, sortierte Ergebnisse (angezeigt)
let _unfiltered = [];   // alle Ergebnisse (vor Filtern)
let _catFilter  = '';   // aktiver Kategoriefilter
let _condFilter = '';   // aktiver Zustandsfilter
let _priceFilter = '';  // max. Preis (leer = alle)
let _textFilter = '';   // aktiver Volltextfilter (lokal)

// Filterleiste oben neben "Neues Angebot" rendern.
function renderFilterBar() {
    const mount = document.getElementById('filter-mount');
    if (!mount) return;
    const radiusVal = document.getElementById('s-radius') ? document.getElementById('s-radius').value : '';
    mount.innerHTML =
        '<input type="text" class="filter-text" placeholder="Titel/Beschreibung filtern…" value="'+esc(_textFilter)+'" oninput="filterByText(this.value)">' +
        '<select class="filter-cat" onchange="filterByCategory(this.value)">'+categoryOptions()+'</select>' +
        '<select class="filter-cond" onchange="filterByCondition(this.value)">'+conditionOptions()+'</select>' +
        '<input type="number" class="filter-price" min="0" placeholder="max €" value="'+esc(_priceFilter)+'" oninput="filterByPrice(this.value)">' +
        '<input type="number" class="filter-radius" min="0" placeholder="km" value="'+esc(radiusVal)+'" onchange="setRadius(this.value)" title="Umkreis in km">' +
        '<select class="filter-sort" onchange="setSortFromSelect(this.value)">' +
            '<option value="newest"'+(_sortCol==='newest'?' selected':'')+'>Neueste</option>' +
            '<option value="distance"'+(_sortCol==='distance'?' selected':'')+'>Entfernung</option>' +
            '<option value="price"'+(_sortCol==='price'?' selected':'')+'>Preis</option>' +
            '<option value="title"'+(_sortCol==='title'?' selected':'')+'>Titel</option>' +
        '</select>';
}
function filterByPrice(v) { _priceFilter = v || ''; applyFilters(); }
function setRadius(km) {
    const r = document.getElementById('s-radius');
    if (r) {
        // Passenden Options-Wert setzen oder eine neue Option hinzufügen.
        let found = false;
        for (const o of r.options) { if (o.value === String(km)) { found = true; break; } }
        if (!found && km) { const o = document.createElement('option'); o.value = String(km); o.text = km+' km'; r.appendChild(o); }
        r.value = km ? String(km) : '';
    }
    runSearch(); // neue Netzwerksuche mit geändertem Radius
}
let _currentPage = 1;   // aktuelle Seite
const PER_PAGE = 10;    // Anzeigen pro Seite

// Kategorie-Optionen aus den vorhandenen Ergebnissen (nur was wirklich da ist).
function categoryOptions() {
    const cats = new Set();
    for (const it of _unfiltered) { if (it.category) cats.add(it.category); }
    let opts = '<option value="">Alle Kategorien</option>';
    for (const c of Array.from(cats).sort()) {
        const sel = (c === _catFilter) ? ' selected' : '';
        opts += '<option value="'+esc(c)+'"'+sel+'>'+esc(c)+'</option>';
    }
    return opts;
}
function conditionOptions() {
    const conds = new Set();
    for (const it of _unfiltered) { if (it.condition) conds.add(it.condition); }
    let opts = '<option value="">Alle Zustände</option>';
    for (const c of Array.from(conds).sort()) {
        const sel = (c === _condFilter) ? ' selected' : '';
        opts += '<option value="'+esc(c)+'"'+sel+'>'+esc(c)+'</option>';
    }
    return opts;
}
// Kombinierte lokale Filterung (Text + Kategorie + Zustand).
function applyFilters() {
    const tf = _textFilter.toLowerCase().trim();
    const maxP = parseFloat(_priceFilter);
    const favs = _favOnly ? favSet() : null;
    _allResults = _unfiltered.filter(it => {
        if (favs && !favs.has(it.id)) return false;
        if (_catFilter && it.category !== _catFilter) return false;
        if (_condFilter && it.condition !== _condFilter) return false;
        if (!isNaN(maxP) && (parseFloat(it.price_min)||0) > maxP) return false;
        if (tf) {
            const hay = ((it.title||'') + ' ' + (it.description||'') + ' ' + (it.category||'')).toLowerCase();
            if (!hay.includes(tf)) return false;
        }
        return true;
    });
    applySortAndRender();
}
function filterByCategory(cat) { _catFilter = cat || ''; applyFilters(); }
function filterByCondition(cond) { _condFilter = cond || ''; applyFilters(); }
function filterByText(txt) {
    _textFilter = txt || '';
    applyFilters();
    const inp = document.querySelector('.filter-text');
    if (inp) { inp.focus(); inp.setSelectionRange(inp.value.length, inp.value.length); }
}

// Signatur der Trefferliste: nur neu zeichnen, wenn sich wirklich etwas
// geändert hat (sonst flackerte die Seite mobil im Sekundentakt beim Polling).
let _lastSig = '';
function hitsSig(items) {
    return (items || []).map(function(h){ return (h.id || h.listing_id || h.title || '') + '@' + (h.price_min || ''); }).join('|');
}
// updateResults: neue Netz-Treffer übernehmen, Filter/Seite dabei behalten.
function updateResults(items) {
    const sig = hitsSig(items);
    if (sig === _lastSig) return;
    if (!_unfiltered.length) { renderResults(items); return; }
    _lastSig = sig;
    _unfiltered = items || [];
    const page = _currentPage;
    applyFilters();
    if (page > 1 && page !== _currentPage) { _currentPage = page; renderResultsPage(); }
}
function renderResults(items) {
    // items sind bereits sortiert; global halten fürs Paging + Filter.
    _lastSig = hitsSig(items);
    _unfiltered = items || [];
    _catFilter = '';
    _condFilter = '';
    _textFilter = '';
    _allResults = _unfiltered.slice();
    _currentPage = 1;
    renderResultsPage();
}

function renderResultsPage() {
    const res = document.getElementById('results');
    const items = _allResults;
    if (!items.length) { res.innerHTML = '<p class="muted">%s</p>'; return; }

    const totalPages = Math.max(1, Math.ceil(items.length / PER_PAGE));
    if (_currentPage > totalPages) _currentPage = totalPages;
    const start = (_currentPage - 1) * PER_PAGE;
    const pageItems = items.slice(start, start + PER_PAGE);

    // Filterleiste oben (neben Neues Angebot) rendern.
    renderFilterBar();
    let html = renderChips() + renderViewToggle();
    if (_view === 'grid') {
        html += '<div class="hit-grid">';
        for (const it of pageItems) html += renderHitCard(it);
        html += '</div>';
        if (totalPages > 1) {
            html += '<div class="pager">';
            html += '<button class="pager-btn" ' + (_currentPage <= 1 ? 'disabled' : '') + ' onclick="gotoPage(' + (_currentPage-1) + ')">‹ Zurück</button>';
            html += '<span class="pager-info">Seite ' + _currentPage + ' / ' + totalPages + ' · ' + items.length + ' Anzeigen</span>';
            html += '<button class="pager-btn" ' + (_currentPage >= totalPages ? 'disabled' : '') + ' onclick="gotoPage(' + (_currentPage+1) + ')">Weiter ›</button>';
            html += '</div>';
        }
        res.innerHTML = html;
        return;
    }
    html += '<table class="record-table"><thead><tr>' +
        '<th></th>' +
        '<th class="sortable" onclick="sortByColumn(\'title\')">'+SRCHT.col_title+sortArrow('title')+'</th>' +
        '<th class="sortable" onclick="sortByColumn(\'price\')">'+SRCHT.col_price+sortArrow('price')+'</th>' +
        '<th>'+SRCHT.col_condition+'</th>' +
        '<th class="sortable" onclick="sortByColumn(\'category\')">'+SRCHT.col_category+sortArrow('category')+'</th>' +
        '<th class="sortable" onclick="sortByColumn(\'distance\')">'+SRCHT.col_distance+sortArrow('distance')+'</th></tr></thead><tbody>';
    for (const it of pageItems) {
        const dist = (it.distance_km) ? (it.distance_km + ' km') : '–';
        const price = it.price_min ? (parseFloat(it.price_min).toFixed(2) + ' FND') : '–';
        let thumb = '';
        if (it.thumbnail) {
            thumb = '<img src="' + it.thumbnail + '" class="hit-thumb" loading="lazy" alt="">';
        } else if (it.image_hash) {
            thumb = '<img src="/api/v1/files/thumb/' + encodeURIComponent(it.image_hash) +
                    '" class="hit-thumb" loading="lazy" alt="">';
        } else {
            thumb = '<div class="hit-thumb hit-thumb-ph">🎬</div>';
        }
        const src = (it.from_peer && it.from_peer !== 'local') ? ' <span class="hit-remote" title="aus dem Netzwerk">·</span>' : '';
        const isSold = !!it.sold;
        const soldBadge = isSold ? ' <span class="sold-badge">VERKAUFT</span>' : '';
        const rowStyle = isSold ? 'cursor:pointer;opacity:0.5' : 'cursor:pointer';
        html += '<tr onclick="location.href=\'/listings/' + it.id + '\'" style="' + rowStyle + '">' +
            '<td class="cell-thumb">' + thumb + '</td>' +
            '<td data-label="' + SRCHT.col_title + '">' + esc(it.title || '–') + src + soldBadge + '</td>' +
            '<td data-label="' + SRCHT.col_price + '">' + price + '</td>' +
            '<td data-label="' + SRCHT.col_condition + '">' + esc(it.condition || '–') + '</td>' +
            '<td data-label="' + SRCHT.col_category + '">' + esc(it.category || '–') + '</td>' +
            '<td data-label="' + SRCHT.col_distance + '">' + dist + '</td></tr>';
    }
    html += '</tbody></table>';

    // Seitennavigation nur, wenn mehr als eine Seite existiert.
    if (totalPages > 1) {
        html += '<div class="pager">';
        html += '<button class="pager-btn" ' + (_currentPage <= 1 ? 'disabled' : '') + ' onclick="gotoPage(' + (_currentPage-1) + ')">‹ Zurück</button>';
        html += '<span class="pager-info">Seite ' + _currentPage + ' / ' + totalPages + ' · ' + items.length + ' Anzeigen</span>';
        html += '<button class="pager-btn" ' + (_currentPage >= totalPages ? 'disabled' : '') + ' onclick="gotoPage(' + (_currentPage+1) + ')">Weiter ›</button>';
        html += '</div>';
    }
    res.innerHTML = html;
}

// ── Bildraster, Chips, Favoriten (R552) ─────────────────────────────────────
let _view = localStorage.getItem('fundus.listings.view') || 'grid';
function setView(v){ _view = v; localStorage.setItem('fundus.listings.view', v); renderResultsPage(); }
function favSet(){ try { return new Set(JSON.parse(localStorage.getItem('fundus.favs') || '[]')); } catch(e){ return new Set(); } }
function toggleFav(ev, id){
    ev.preventDefault(); ev.stopPropagation();
    const f = favSet(); if (f.has(id)) f.delete(id); else f.add(id);
    localStorage.setItem('fundus.favs', JSON.stringify(Array.from(f)));
    if (_favOnly) applyFilters(); else renderResultsPage();
}
let _favOnly = false;
function setFavOnly(on){ _favOnly = !!on; applyFilters(); }
function renderViewToggle(){
    return '<div class="view-toggle">' +
        '<button class="vt' + (_view === 'grid' ? ' on' : '') + '" onclick="setView(\'grid\')" title="Raster">▦</button>' +
        '<button class="vt' + (_view === 'list' ? ' on' : '') + '" onclick="setView(\'list\')" title="Liste">☰</button></div>';
}
function renderChips(){
    const cats = {};
    for (const it of _unfiltered) { const c = (it.category || '').trim(); if (c) cats[c] = (cats[c] || 0) + 1; }
    const names = Object.keys(cats).sort(function(a, b){ return cats[b] - cats[a]; }).slice(0, 12);
    let h = '<div class="chips">';
    h += '<button class="chip' + (!_catFilter && !_favOnly ? ' on' : '') + '" onclick="setFavOnly(false);filterByCategory(\'\')">Alle</button>';
    h += '<button class="chip chip-fav' + (_favOnly ? ' on' : '') + '" onclick="setFavOnly(!_favOnly)">♥ Favoriten</button>';
    for (const c of names) {
        h += '<button class="chip' + (_catFilter === c ? ' on' : '') + '" onclick="setFavOnly(false);filterByCategory(\'' + esc(c).replace(/'/g, '') + '\')">' + esc(c) + ' <span class="chip-n">' + cats[c] + '</span></button>';
    }
    return h + '</div>';
}
function renderHitCard(it){
    const favs = favSet();
    const price = it.price_min ? (parseFloat(it.price_min).toFixed(2).replace('.', ',') + ' FND') : '–';
    let img = '';
    if (it.thumbnail) img = '<img src="' + it.thumbnail + '" loading="lazy" alt="">';
    else if (it.image_hash) img = '<img src="/api/v1/files/thumb/' + encodeURIComponent(it.image_hash) + '" loading="lazy" alt="">';
    else img = '<div class="hc-ph">' + ((window.FUNDUS_ICONS || {}).listings || '') + '</div>';
    const meta = [it.category ? esc(it.category) : '', it.distance_km ? it.distance_km + ' km' : '', it.condition ? esc(it.condition) : ''].filter(Boolean).join(' · ');
    return '<a class="hit-card' + (it.sold ? ' sold' : '') + '" href="/listings/' + it.id + '">' +
        '<div class="hc-img">' + img +
        '<button class="hc-fav' + (favs.has(it.id) ? ' on' : '') + '" onclick="toggleFav(event,\'' + it.id + '\')" title="Merken">♥</button>' +
        (it.sold ? '<span class="sold-badge hc-sold">VERKAUFT</span>' : '') +
        '<span class="hc-price">' + price + '</span></div>' +
        '<div class="hc-title">' + esc(it.title || '–') + '</div>' +
        '<div class="hc-meta">' + meta + '</div></a>';
}

function gotoPage(p) {
    const totalPages = Math.max(1, Math.ceil(_allResults.length / PER_PAGE));
    _currentPage = Math.min(Math.max(1, p), totalPages);
    renderResultsPage();
    document.getElementById('results').scrollIntoView({ behavior: 'smooth', block: 'start' });
}

// Sortierung per Spaltentitel-Klick. Erneuter Klick kehrt die Richtung um.
let _sortCol = 'distance';
let _sortDir = 1; // 1 = aufsteigend, -1 = absteigend
function sortArrow(col) {
    if (_sortCol !== col) return '';
    return _sortDir > 0 ? ' ▲' : ' ▼';
}
function sortByColumn(col) {
    if (_sortCol === col) { _sortDir = -_sortDir; } else { _sortCol = col; _sortDir = 1; }
    applySortAndRender();
}
function setSortFromSelect(col) {
    if (_sortCol === col) { _sortDir = -_sortDir; } else { _sortCol = col; _sortDir = (col === 'newest') ? -1 : 1; }
    applySortAndRender();
}
function applySortAndRender() {
    const d = _sortDir;
    _allResults.sort((a, b) => {
        let av, bv;
        switch (_sortCol) {
            case 'price':    av = parseFloat(a.price_min)||0; bv = parseFloat(b.price_min)||0; break;
            case 'distance': {
                const ad = parseFloat(a.distance_km), bd = parseFloat(b.distance_km);
                av = isNaN(ad) ? 1e9 : ad;
                bv = isNaN(bd) ? 1e9 : bd;
                break;
            }
            case 'title':    return d * (a.title||'').localeCompare(b.title||'');
            case 'category': return d * (a.category||'').localeCompare(b.category||'');
            case 'newest': {
                av = a.created_at ? new Date(a.created_at).getTime() : 0;
                bv = b.created_at ? new Date(b.created_at).getTime() : 0;
                break;
            }
            default:         av = 0; bv = 0;
        }
        return d * (av - bv);
    });
    _currentPage = 1;
    renderResultsPage();
}
function esc(s) {
    return String(s).replace(/[<>&"']/g, c => ({'<':'&lt;','>':'&gt;','&':'&amp;','"':'&quot;',"'":'&#39;'}[c]));
}
runSearch(); // initial alle laden
</script>
]],
        t("listing.any_distance"),
        t("listing.all_categories"),
        t("listing.category.furniture"), t("listing.category.electronics"),
        t("listing.category.clothing"), t("listing.category.vehicle"),
        t("listing.category.tools"), t("listing.category.other"),
        t("listing.sort_newest"), t("listing.sort_distance"), t("listing.sort_price"),
        t("general.wait_collecting"),
        t("listing.col_title"), t("listing.col_price"),
        t("listing.col_condition"), t("listing.col_category"),
        t("listing.col_distance"),
        t("general.loading"),
        t("general.no_results")
    ))

    render.footer()
end

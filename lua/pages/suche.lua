-- Websuche (R895): eigene Metasuche des Nodes. Fragt DuckDuckGo, Mojeek,
-- Wikipedia, ein SearXNG im Netz (falls eingerichtet) und den eigenen Index
-- gleichzeitig und führt die Treffer zusammen (/api/v1/websuche).
local render = require "render"

return function()
    render.header("Websuche", "suche")
    ngx.print([==[
<style>
.ws { max-width:820px; margin:0 auto; padding:4px 0 30px; }
.ws h1 { font-size:20px; margin:0 0 12px; }
.ws-form { display:flex; gap:8px; }
.ws-form input { flex:1; min-width:0; font-size:16px; padding:11px 13px; border-radius:12px; }
.ws-form button { width:auto; flex:0 0 auto; }
.ws-stand { font-size:12.5px; color:var(--text-dim,#9aa0b4); margin:10px 2px 4px; min-height:1.2em; }
.ws-dienste { display:flex; flex-wrap:wrap; gap:6px; margin:4px 0 12px; }
.ws-dienst { font-size:11.5px; padding:2px 8px; border-radius:999px; border:1px solid var(--border,#2a2f3a); color:var(--text-dim,#9aa0b4); }
.ws-dienst.ok { border-color:rgba(61,220,132,.35); color:var(--text,#e6e8ee); }
.ws-dienst.fehler { border-color:rgba(229,72,77,.45); }
.ws-liste { display:flex; flex-direction:column; gap:16px; }
.ws-t a.ws-titel { font-size:16.5px; font-weight:600; color:var(--accent,#7cb7ff); text-decoration:none; overflow-wrap:anywhere; }
.ws-t a.ws-titel:hover { text-decoration:underline; }
.ws-url { font-size:12.5px; color:var(--green,#3ddc84); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.ws-text { font-size:14px; line-height:1.5; margin-top:3px; overflow-wrap:anywhere; }
.ws-quellen { font-size:11.5px; color:var(--text-dim,#9aa0b4); margin-top:3px; }
.ws-leer { text-align:center; color:var(--text-dim,#9aa0b4); margin-top:40px; font-size:14px; }
</style>
<div class="ws">
  <h1>Websuche</h1>
  <form class="ws-form" id="ws-form" role="search">
    <input type="search" id="ws-q" placeholder="Suchbegriff …" aria-label="Suchbegriff" autocomplete="off" enterkeyhint="search">
    <button type="submit" class="btn btn-primary">Suchen</button>
  </form>
  <div class="ws-stand" id="ws-stand"></div>
  <div class="ws-dienste" id="ws-dienste"></div>
  <div class="ws-liste" id="ws-liste"><div class="ws-leer">Durchsucht mehrere Dienste gleichzeitig – ohne Konto, ohne Werbung. Einstellungen unter <a href="/settings#websuche-card">Einstellungen → Websuche</a>.</div></div>
</div>
<script>
(function(){
  var $ = function(id){ return document.getElementById(id); };
  function esc(t){ return String(t == null ? '' : t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  var NAMEN = { duckduckgo:'DuckDuckGo', mojeek:'Mojeek', wikipedia:'Wikipedia', searxng:'SearXNG', index:'Eigener Index' };
  var laufend = null;
  function anzeigeURL(u){ try { var x = new URL(u); return x.hostname.replace(/^www\./,'') + decodeURIComponent(x.pathname).replace(/\/$/,''); } catch(e){ return u; } }
  function suchen(q, merken){
    q = (q || '').trim();
    if (!q) return;
    if (laufend) laufend.abort();
    laufend = new AbortController();
    if (merken) history.pushState({ q:q }, '', '/suche?q=' + encodeURIComponent(q));
    document.title = q + ' – Websuche';
    $('ws-stand').textContent = 'Suche …';
    $('ws-dienste').innerHTML = '';
    fetch('/api/v1/websuche?q=' + encodeURIComponent(q), { credentials:'same-origin', signal: laufend.signal })
      .then(function(r){ return r.json(); })
      .then(function(d){
        if (d.error) { $('ws-stand').textContent = '✗ ' + d.error; $('ws-liste').innerHTML = ''; return; }
        var t = d.treffer || [];
        $('ws-stand').textContent = t.length + ' Treffer · ' + (d.dauer_ms / 1000).toFixed(1).replace('.', ',') + ' s' + (d.cache ? ' · aus dem Zwischenspeicher' : '');
        var dn = d.dienste || {};
        $('ws-dienste').innerHTML = Object.keys(dn).sort().map(function(k){
          var ok = dn[k].indexOf('ok') === 0;
          return '<span class="ws-dienst ' + (ok ? 'ok' : 'fehler') + '" title="' + esc(dn[k]) + '">' + (ok ? '✓ ' : '✗ ') + esc(NAMEN[k] || k) + (ok ? ' ' + esc(dn[k].replace(/^ok /, '')) : '') + '</span>';
        }).join('');
        if (!t.length) { $('ws-liste').innerHTML = '<div class="ws-leer">Keine Treffer.</div>'; return; }
        $('ws-liste').innerHTML = t.map(function(x){
          return '<div class="ws-t"><div class="ws-url">' + esc(anzeigeURL(x.url)) + '</div>' +
            '<a class="ws-titel" href="' + esc(x.url) + '" rel="noopener noreferrer" target="_blank">' + esc(x.titel) + '</a>' +
            (x.text ? '<div class="ws-text">' + esc(x.text) + '</div>' : '') +
            '<div class="ws-quellen">' + (x.quellen || []).map(function(k){ return esc(NAMEN[k] || k); }).join(' · ') + '</div></div>';
        }).join('');
      })
      .catch(function(e){ if (e && e.name === 'AbortError') return; $('ws-stand').textContent = '✗ Suche fehlgeschlagen.'; });
  }
  // R896: angeklickte Treffer liest der Crawler bald selbst (nur die Adresse wird gemeldet).
  $('ws-liste').addEventListener('click', function(ev){
    var a = ev.target.closest && ev.target.closest('a.ws-titel');
    if (!a) return;
    try {
      var daten = new Blob([JSON.stringify({ url: a.href })], { type: 'application/json' });
      if (!(navigator.sendBeacon && navigator.sendBeacon('/api/v1/websuche/vormerken', daten)))
        fetch('/api/v1/websuche/vormerken', { method:'POST', credentials:'same-origin', headers:{ 'Content-Type':'application/json' }, body: JSON.stringify({ url: a.href }), keepalive: true });
    } catch(e) {}
  });
  $('ws-form').onsubmit = function(ev){ ev.preventDefault(); suchen($('ws-q').value, true); };
  window.addEventListener('popstate', function(ev){ var q = ev.state && ev.state.q || ''; $('ws-q').value = q; if (q) suchen(q, false); });
  var start = new URLSearchParams(location.search).get('q');
  if (start) { $('ws-q').value = start; suchen(start, false); } else { $('ws-q').focus(); }
})();
</script>
]==])
    render.footer()
end

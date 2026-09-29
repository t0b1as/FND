-- pages/shared.lua – Freigaben: NUR LESEND, auch von außen erreichbar.
-- Zeigt ausschließlich Dateien, die der Betreiber ausdrücklich freigegeben hat,
-- plus eine Suche über die Freigaben im Fundus-Netz. Hochladen, Löschen und
-- Freigeben bleiben der Betreiber-Verwaltung (/files, nur Heimnetz) vorbehalten.
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    render.header("nav.shared_label", "shared")
    ngx.print([==[
<section>
  <h2>🔗 Freigaben dieses Nodes</h2>
  <p class="meta">Vom Betreiber freigegebene Dateien – zum Ansehen und Herunterladen.</p>
  <div id="sh-own" class="sh-list">Lade …</div>
</section>
<section style="margin-top:1.5rem">
  <h2>🔎 Im Fundus-Netz suchen</h2>
  <p class="meta">Durchsucht die Freigaben aller erreichbaren Nodes.</p>
  <div style="display:flex;gap:8px;flex-wrap:wrap">
    <input type="search" id="sh-q" placeholder="Dateiname oder Stichwort" style="flex:1;min-width:180px"
           onkeydown="if(event.key==='Enter')shSearch()">
    <button class="btn" onclick="shSearch()">Suchen</button>
  </div>
  <div id="sh-status" class="meta" style="margin-top:6px"></div>
  <div id="sh-hits" class="sh-list" style="margin-top:8px"></div>
</section>
<style>
.sh-list  { display:flex; flex-direction:column; gap:6px; }
.sh-row   { display:flex; align-items:center; gap:12px; padding:8px 10px; border:1px solid var(--border);
            border-radius:10px; background:var(--surface); }
.sh-ico   { width:44px; height:44px; flex:0 0 44px; border-radius:8px; display:flex; align-items:center;
            justify-content:center; font-size:22px; background:rgba(255,255,255,.04); overflow:hidden; }
.sh-ico img { width:100%; height:100%; object-fit:cover; }
.sh-main  { flex:1; min-width:0; }
.sh-name  { font-weight:600; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.sh-sub   { font-size:12px; color:var(--muted); }
.sh-row .btn { width:auto; padding:6px 12px; }
</style>
<script>
(function(){
  function esc(t){ return String(t==null?'':t).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function size(n){ n = Number(n)||0; if (n < 1024) return n + ' B'; if (n < 1048576) return (n/1024).toFixed(1) + ' KB';
    if (n < 1073741824) return (n/1048576).toFixed(1) + ' MB'; return (n/1073741824).toFixed(2) + ' GB'; }
  function icon(m){ m = String(m||''); if (m.indexOf('image/')===0) return '🖼️'; if (m.indexOf('video/')===0) return '🎬';
    if (m.indexOf('audio/')===0) return '🎵'; if (m.indexOf('pdf')>=0) return '📄'; if (m.indexOf('zip')>=0||m.indexOf('compressed')>=0) return '🗜️'; return '📦'; }
  function row(f, extra){
    var h = String(f.hash||''); if (!/^[0-9a-f]{64}$/.test(h)) return '';
    var isImg = String(f.mime_type||'').indexOf('image/')===0 && !f.encrypted;
    var ico = isImg ? '<img loading="lazy" src="/api/v1/files/thumb/' + h + '" alt="" onerror="this.parentNode.textContent=\'🖼️\'">' : icon(f.mime_type);
    var url = '/api/v1/files/download/' + h;
    return '<div class="sh-row"><div class="sh-ico">' + ico + '</div>' +
      '<div class="sh-main"><div class="sh-name" title="' + esc(f.name) + '">' + esc(f.name || h.slice(0,12)+'…') + '</div>' +
      '<div class="sh-sub">' + size(f.size) + (f.encrypted ? ' · 🔒 verschlüsselt (Passwort nötig)' : '') + (extra ? ' · ' + extra : '') + '</div></div>' +
      '<a class="btn btn-outline" href="' + url + '" target="_blank" rel="noopener">Öffnen</a>' +
      '<a class="btn" href="' + url + '" download="' + esc(f.name || h) + '">⬇</a></div>';
  }
  fetch('/api/v1/files/shared').then(function(r){ return r.json(); }).then(function(d){
    var fs = (d && d.files) || [];
    fs.sort(function(a,b){ return (b.shared_at||0) - (a.shared_at||0); });
    document.getElementById('sh-own').innerHTML = fs.length ? fs.map(function(f){ return row(f); }).join('') :
      '<p class="meta">Dieser Node hat derzeit keine Dateien freigegeben.</p>';
  }).catch(function(){ document.getElementById('sh-own').innerHTML = '<p class="meta">Freigaben nicht abrufbar.</p>'; });

  var poll = null;
  window.shSearch = async function(){
    var q = document.getElementById('sh-q').value.trim();
    var st = document.getElementById('sh-status'), box = document.getElementById('sh-hits');
    if (!q) { st.textContent = 'Bitte einen Suchbegriff eingeben.'; return; }
    if (poll) clearInterval(poll);
    st.textContent = '⏳ Suche im Netz …'; box.innerHTML = '';
    try {
      var r = await fetch('/api/v1/files/search?q=' + encodeURIComponent(q)); var d = await r.json();
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      var id = d.search_id, n = 0;
      function show(hits){
        var seen = {}; hits = (hits||[]).filter(function(h){ if (seen[h.hash]) return false; seen[h.hash] = 1; return true; });
        box.innerHTML = hits.map(function(h){ return row(h, h.from_peer === 'local' ? 'dieser Node' : 'Node ' + esc(String(h.from_peer||'').slice(-6))); }).join('');
        return hits.length;
      }
      var cnt = show(d.hits);
      poll = setInterval(async function(){
        n++;
        try { var r2 = await fetch('/api/v1/files/search/results?id=' + encodeURIComponent(id)); var d2 = await r2.json(); cnt = show(d2.hits); } catch(e){}
        if (n >= 6) { clearInterval(poll); poll = null; st.textContent = cnt ? cnt + ' Treffer.' : 'Keine Treffer.'; }
        else st.textContent = '⏳ Suche im Netz … ' + cnt + ' Treffer bisher';
      }, 1500);
    } catch(e){ st.textContent = '✗ ' + e.message; }
  };
})();
</script>
]==])
    render.footer()
end

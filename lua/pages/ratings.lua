-- pages/ratings.lua – Bewertungen von Handelspartnern.
--   /ratings?escrow=<id>  Handel: Parteien, Bewertungen, Formular (für Käufer/Verkäufer)
--   /ratings?addr=<0x…>   alle Bewertungen einer Adresse mit Durchschnitt
-- Alle Bewertungen werden vom Node gegen die Chain geprüft (Signatur, Escrow, Rollen).
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    render.header("nav.ratings_label", "ratings")
    ngx.print([==[
<section class="rt-wrap">
  <h2>⭐ Bewertungen</h2>
  <div id="rt-main" class="meta">Lade …</div>
</section>
<style>
.rt-wrap { max-width:760px; margin:0 auto; }
.rt-card { border:1px solid var(--border); border-radius:12px; background:var(--surface); padding:12px 14px; margin:10px 0; }
.rt-stars { color:#00e676; letter-spacing:2px; font-size:18px; }
.rt-pick button { background:none; border:0; font-size:30px; color:#666; cursor:pointer; padding:0 2px; line-height:1; }
.rt-pick button.on { color:#00e676; }
.rt-sum { font-size:28px; font-weight:700; }
.rt-row { display:flex; justify-content:space-between; gap:8px; flex-wrap:wrap; }
.rt-comment { margin-top:6px; white-space:pre-wrap; word-break:break-word; }
.rt-form textarea { width:100%; min-height:90px; margin-top:8px; border-radius:12px; border:1px solid var(--border);
  background:var(--bg,#0e0e1a); color:var(--text); padding:10px 12px; font:inherit; }
</style>
<script>
(function(){
  var main = document.getElementById('rt-main');
  var q = new URLSearchParams(location.search);
  function esc(t){ return String(t==null?'':t).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function short(a){ a = String(a||''); return a.length > 14 ? a.slice(0,8) + '…' + a.slice(-4) : a; }
  function stars(n){ var s = ''; for (var i = 1; i <= 5; i++) s += i <= n ? '★' : '☆'; return '<span class="rt-stars">' + s + '</span>'; }
  function day(t){ try { return new Date(t*1000).toLocaleDateString('de-DE'); } catch(e){ return ''; } }
  function card(r, showDeal){
    var who = r.role === 'buyer' ? 'Käufer' : 'Verkäufer';
    return '<div class="rt-card"><div class="rt-row"><div>' + stars(r.stars) + '</div><div class="meta">' + day(r.time) + '</div></div>' +
      '<div class="meta" style="font-size:12px">von ' + who + ' <a href="/ratings?addr=' + encodeURIComponent(r.rater) + '"><code>' + esc(short(r.rater)) + '</code></a>' +
      (showDeal ? ' · <a href="/ratings?escrow=' + encodeURIComponent(r.escrow_id) + '">Handel</a>' : '') + '</div>' +
      (r.comment ? '<div class="rt-comment">' + esc(r.comment) + '</div>' : '') + '</div>';
  }

  async function showAddr(addr){
    var d = await (await fetch('/api/v1/ratings?addr=' + encodeURIComponent(addr))).json();
    var head = '<div class="rt-card"><div class="meta">Adresse</div><code style="word-break:break-all">' + esc(addr) + '</code>' +
      (d.count ? '<div style="margin-top:8px"><span class="rt-sum">' + d.avg.toFixed(1).replace('.', ',') + '</span> ' + stars(Math.round(d.avg)) +
        ' <span class="meta">aus ' + d.count + ' Bewertung' + (d.count === 1 ? '' : 'en') + '</span></div>' : '<div class="meta" style="margin-top:8px">Noch keine Bewertungen.</div>') + '</div>';
    main.innerHTML = head + (d.ratings || []).map(function(r){ return card(r, true); }).join('') +
      '<p class="meta" style="margin-top:12px">Bewertungen sind an abgeschlossene Käufe auf der Fundus-Chain gebunden und vom Bewertenden signiert – jeder Node prüft sie selbst.</p>';
  }

  async function showEscrow(id){
    var r = await fetch('/api/v1/ratings/escrow/' + encodeURIComponent(id)); var d = await r.json();
    if (!r.ok) { main.textContent = '✗ ' + (d.error || ('HTTP ' + r.status)); return; }
    var mine = (d.ratings || []).filter(function(x){ return d.my_role && x.role === d.my_role; })[0];
    var html = '<div class="rt-card"><div class="meta">Handel (Escrow)</div><code style="word-break:break-all">' + esc(d.escrow_id) + '</code>' +
      '<div style="margin-top:8px">Käufer: <a href="/ratings?addr=' + encodeURIComponent(d.buyer) + '"><code>' + esc(short(d.buyer)) + '</code></a> · ' +
      'Verkäufer: <a href="/ratings?addr=' + encodeURIComponent(d.seller) + '"><code>' + esc(short(d.seller)) + '</code></a></div>' +
      '<div class="meta" style="margin-top:4px">' + (d.closed ? '✓ abgeschlossen' : '⏳ noch nicht abgeschlossen – Bewerten ist erst danach möglich') + '</div></div>';
    if (d.can_rate) {
      html += '<div class="rt-card rt-form"><b>' + (mine ? 'Deine Bewertung ändern' : 'Deinen Handelspartner bewerten') + '</b>' +
        '<div class="meta" style="font-size:12px">Du bewertest den ' + (d.my_role === 'buyer' ? 'Verkäufer' : 'Käufer') + '.</div>' +
        '<div class="rt-pick" id="rt-pick" style="margin-top:6px"></div>' +
        '<textarea id="rt-comment" maxlength="500" placeholder="Wie lief der Handel? (optional, max. 500 Zeichen)">' + esc(mine ? mine.comment : '') + '</textarea>' +
        '<div style="margin-top:8px"><button class="btn" id="rt-send">Bewertung speichern</button> <span id="rt-out" class="meta"></span></div></div>';
    } else if (d.closed && !d.my_role) {
      html += '<p class="meta">Bewerten können nur Käufer und Verkäufer dieses Handels – mit dem Konto angemeldet, dessen Wallet am Handel beteiligt war.</p>';
    }
    html += (d.ratings || []).map(function(x){ return card(x, false); }).join('');
    main.innerHTML = html;
    if (d.can_rate) {
      var val = mine ? mine.stars : 0, pick = document.getElementById('rt-pick');
      function paint(){ pick.innerHTML = [1,2,3,4,5].map(function(i){ return '<button type="button" data-v="' + i + '" class="' + (i <= val ? 'on' : '') + '">★</button>'; }).join(''); }
      paint();
      pick.onclick = function(e){ var v = e.target.getAttribute('data-v'); if (v) { val = +v; paint(); } };
      document.getElementById('rt-send').onclick = async function(){
        var out = document.getElementById('rt-out');
        if (!val) { out.textContent = 'Bitte 1 bis 5 Sterne wählen.'; return; }
        this.disabled = true; out.textContent = '⏳ Wird signiert und verteilt …';
        try {
          var rr = await fetch('/api/v1/ratings', { method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({ escrow_id: d.escrow_id, stars: val, comment: document.getElementById('rt-comment').value }) });
          var dd = await rr.json();
          if (!rr.ok) throw new Error(dd.error || ('HTTP ' + rr.status));
          out.textContent = '✓ Gespeichert.'; setTimeout(function(){ showEscrow(id); }, 600);
        } catch(e){ out.textContent = '✗ ' + e.message; this.disabled = false; }
      };
    }
  }

  var e = q.get('escrow'), a = q.get('addr');
  (e ? showEscrow(e) : a ? showAddr(a) : Promise.resolve(main.textContent = 'Keine Adresse oder kein Handel angegeben.'))
    .catch(function(err){ main.textContent = '✗ ' + err.message; });
})();
</script>
]==])
    render.footer()
end

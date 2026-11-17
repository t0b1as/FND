-- Warenkorb (R922): Angebote sammeln und später per PayPal bezahlen.
-- Der Inhalt liegt nur in diesem Browser (localStorage „fnd.warenkorb“).
-- Bezahlt wird je Verkäufer, weil eine PayPal-Zahlung genau einen Empfänger
-- hat. Preise und Bestand werden beim Öffnen frisch vom Node geholt; den
-- Betrag rechnet der Node beim Bezahlen ohnehin selbst aus.
local render = require "render"

return function()
    render.header("Warenkorb", "warenkorb")
    ngx.print([==[
<style>
.wk { max-width:860px; margin:0 auto; padding:4px 0 30px; }
.wk h1 { font-size:20px; margin:0 0 6px; }
.wk-gruppe { border:1px solid var(--border,#2a2f3a); border-radius:14px; padding:14px; margin:14px 0; background:var(--sur,transparent); }
.wk-gruppe h2 { font-size:15px; margin:0 0 10px; }
.wk-pos { display:grid; grid-template-columns:56px 1fr auto; gap:10px; align-items:center; padding:8px 0; border-top:1px solid var(--border,#2a2f3a); }
.wk-pos:first-of-type { border-top:0; }
.wk-pos img { width:56px; height:56px; object-fit:cover; border-radius:8px; background:rgba(127,127,127,.15); }
.wk-pos .t a { font-weight:600; color:inherit; text-decoration:none; }
.wk-pos .m { font-size:12.5px; color:var(--text-dim,#9aa0b4); margin-top:2px; }
.wk-pos .r { text-align:right; white-space:nowrap; }
.wk-pos input[type=number] { width:62px; }
.wk-pos .weg { background:none; border:0; color:var(--text-dim,#9aa0b4); cursor:pointer; font-size:12px; padding:2px 0; }
.wk-pos.fehlt { opacity:.55; }
.wk-summe { display:flex; justify-content:space-between; font-weight:700; font-size:16px; margin:10px 0; }
.wk-status { font-size:13px; margin-top:8px; min-height:1em; }
@media (max-width:560px) { .wk-pos { grid-template-columns:44px 1fr; } .wk-pos img { width:44px; height:44px; } .wk-pos .r { grid-column:2; text-align:left; } }
</style>
<div class="wk">
  <h1>Warenkorb</h1>
  <p class="meta">Bezahlt wird per PayPal, je Verkäufer getrennt – das Geld geht direkt an ihn. 1 FND = 1 €. Der Warenkorb liegt nur in diesem Browser.</p>
  <div id="wk-inhalt"><p class="meta">Lade …</p></div>
</div>
<script src="/static/marktplatz-paypal.js?v=]==] .. render.rev() .. [==["></script>
<script>
(function(){
  var M = window.FundusMarktPayPal, W = M.warenkorb, esc = M.esc, euro = M.euro;
  var frisch = {}; // id → aktuelle Angebotsdaten (oder null = nicht mehr da)
  var ppBereit = false;

  function holen(id){
    return fetch('/api/v1/listings/' + encodeURIComponent(id)).then(function(r){ return r.ok ? r.json() : null; }).catch(function(){ return null; });
  }
  function frei(d){ var q = +(d.quantity || 0), s = +(d.sold_count || 0); return d.sold === true ? 0 : Math.max(0, q - s); }

  function zeichnen(){
    var l = W.lesen(), ziel = document.getElementById('wk-inhalt');
    if (!l.length) { ziel.innerHTML = '<p>Dein Warenkorb ist leer. <a href="/listings">Zum Marktplatz →</a></p>'; return; }
    var gruppen = {};
    l.forEach(function(p){ (gruppen[p.empf || '?'] = gruppen[p.empf || '?'] || []).push(p); });
    var h = '';
    Object.keys(gruppen).forEach(function(empf, gi){
      var pos = gruppen[empf], summe = 0, ok = true;
      h += '<div class="wk-gruppe"><h2>Verkäufer ' + (gi + 1) + ' <span class="meta" style="font-weight:400">· PayPal: ' + esc(empf.indexOf('@') > 0 ? empf.replace(/^(.).*(@.*)$/, '$1…$2') : empf) + '</span></h2>';
      pos.forEach(function(p){
        var f = frisch[p.id], d = f && f.data, fehlt = '', max = p.max || 99;
        if (f === null || (d && !d.paypal)) { fehlt = 'nicht mehr per PayPal erhältlich'; ok = false; }
        else if (d) {
          max = frei(d);
          if (max < 1) { fehlt = 'ausverkauft'; ok = false; }
          else if (p.anzahl > max) { p.anzahl = max; W.schreiben(l); }
          p.preis = +d.price_min || p.preis; p.titel = d.title || p.titel;
          p.versandkosten = +d.shipping_cost || 0; p.versand_moeglich = d.delivery === 'shipping';
          if (String(d.paypal).toLowerCase() !== String(p.empf).toLowerCase()) { p.empf = d.paypal; W.schreiben(l); }
        }
        var vers = p.versand && p.versand_moeglich;
        var betrag = p.preis * p.anzahl + (vers ? p.versandkosten : 0);
        if (!fehlt) summe += betrag;
        h += '<div class="wk-pos' + (fehlt ? ' fehlt' : '') + '"><img src="/api/v1/listings/' + encodeURIComponent(p.id) + '/thumb" alt="" loading="lazy" onerror="this.style.visibility=\'hidden\'">' +
          '<div><div class="t"><a href="/listings/' + encodeURIComponent(p.id) + '">' + esc(p.titel) + '</a></div>' +
          '<div class="m">' + euro(p.preis) + ' je Stück' + (fehlt ? ' · <b>' + fehlt + '</b>' : '') + '</div>' +
          (p.versand_moeglich ? '<label class="m"><input type="checkbox" data-versand="' + esc(p.id) + '"' + (vers ? ' checked' : '') + '> Versand' + (p.versandkosten ? ' (+' + euro(p.versandkosten) + ')' : '') + ' – sonst Abholung</label>' : '<div class="m">Abholung</div>') +
          '</div><div class="r"><input type="number" min="1" max="' + max + '" value="' + p.anzahl + '" data-anzahl="' + esc(p.id) + '"' + (fehlt ? ' disabled' : '') + '>' +
          '<div style="margin-top:4px"><b>' + euro(betrag) + '</b></div><button class="weg" data-weg="' + esc(p.id) + '">entfernen</button></div></div>';
      });
      h += '<div class="wk-summe"><span>Summe</span><span>' + euro(summe) + '</span></div>';
      if (!ppBereit) h += '<p class="meta">PayPal ist auf diesem Node nicht eingerichtet.</p>';
      else if (!ok) h += '<p class="meta">Bitte zuerst entfernen, was nicht mehr erhältlich ist.</p>';
      else h += '<div id="wk-pp-' + gi + '" data-empf="' + esc(empf) + '" style="max-width:420px"></div>';
      h += '<div class="wk-status" id="wk-st-' + gi + '"></div></div>';
    });
    ziel.innerHTML = h;
    // Knöpfe je Verkäufer
    Object.keys(gruppen).forEach(function(empf, gi){
      var el = document.getElementById('wk-pp-' + gi); if (!el) return;
      var melde = function(t){ document.getElementById('wk-st-' + gi).textContent = t; };
      M.knopf('#wk-pp-' + gi, function(){
        return W.lesen().filter(function(p){ return p.empf === empf; })
          .map(function(p){ return { listing_id: p.id, anzahl: p.anzahl, versand: !!(p.versand && p.versand_moeglich) }; });
      }, { meldung: melde, fertig: function(x){
        W.schreiben(W.lesen().filter(function(p){ return p.empf !== empf; }));
        zeichnen();
        var box = document.createElement('div'); box.className = 'wk-gruppe';
        box.innerHTML = '<b>✓ Bezahlt: ' + euro(+x.betrag) + (x.sandbox ? ' (Sandbox)' : '') + '</b><div class="meta">' +
          (x.ort ? 'Versand an ' + esc(x.name) + ', ' + esc(x.ort) + '. ' : '') + 'Beleg ' + esc(x.beleg) + '. Der Verkäufer wurde benachrichtigt; die Bestätigung kommt auch per E-Mail von PayPal.</div>';
        document.getElementById('wk-inhalt').prepend(box);
      } }).catch(function(e){ melde('✗ ' + e.message); });
    });
  }

  document.addEventListener('change', function(e){
    var t = e.target, l = W.lesen();
    if (t.dataset.anzahl) { l.forEach(function(p){ if (p.id === t.dataset.anzahl) p.anzahl = Math.max(1, Math.min(+t.value || 1, +t.max || 99)); }); W.schreiben(l); zeichnen(); }
    if (t.dataset.versand) { l.forEach(function(p){ if (p.id === t.dataset.versand) p.versand = t.checked; }); W.schreiben(l); zeichnen(); }
  });
  document.addEventListener('click', function(e){
    var id = e.target.dataset && e.target.dataset.weg; if (!id) return;
    W.schreiben(W.lesen().filter(function(p){ return p.id !== id; })); zeichnen();
  });

  Promise.all([M.info()].concat(W.lesen().map(function(p){ return holen(p.id).then(function(r){ frisch[p.id] = r; }); })))
    .then(function(r){ ppBereit = !!(r[0] && r[0].bereit); zeichnen(); });
})();
</script>
]==])
    render.footer()
end

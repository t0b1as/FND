package api

// Anmeldeseite des WLAN-Hotspots (R865) – wird vom Node direkt ausgeliefert
// (hotspot.go, Port 3088). Eigenständig: keine Fundus-Oberfläche, keine
// Anmeldung; nur das PayPal-Skript wird nachgeladen, wenn PayPal eingerichtet ist.
const hotspotSeite = `<!DOCTYPE html>
<html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>WLAN</title>
<style>
:root { color-scheme: light dark; --bg:#f6f7f9; --karte:#fff; --text:#15171c; --leise:#5c6270; --rand:#dfe2e8; --akzent:#6d3fd8; }
@media (prefers-color-scheme: dark) { :root { --bg:#0f1116; --karte:#1a1d25; --text:#eceef3; --leise:#9aa0b4; --rand:#2c303c; --akzent:#a763f7; } }
* { box-sizing:border-box; }
body { margin:0; font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif; background:var(--bg); color:var(--text); }
main { max-width:520px; margin:0 auto; padding:24px 16px 40px; }
h1 { font-size:24px; margin:0 0 4px; }
.leise { color:var(--leise); font-size:14px; }
.status { margin:16px 0; padding:12px 14px; border-radius:12px; background:var(--karte); border:1px solid var(--rand); }
.status.an { border-color:#2fbf71; }
.stufe { margin:12px 0; padding:16px; border-radius:14px; background:var(--karte); border:1px solid var(--rand); }
.kopf { display:flex; justify-content:space-between; align-items:baseline; gap:10px; }
.kopf b { font-size:18px; }
.preis { font-size:20px; font-weight:700; color:var(--akzent); white-space:nowrap; }
.werte { color:var(--leise); font-size:14px; margin:4px 0 12px; }
button.gratis { width:100%; padding:12px; border:0; border-radius:10px; background:var(--akzent); color:#fff; font-size:16px; font-weight:600; cursor:pointer; }
.fehler { color:#e5484d; font-size:14px; margin-top:8px; }
footer { margin-top:24px; font-size:12.5px; color:var(--leise); }
</style></head><body><main>
<h1>WLAN</h1>
<div class="leise">Wähle eine Geschwindigkeit. Alle Preise in Euro inklusive Umsatzsteuer.</div>
<div id="status" class="status" style="display:none"></div>
<div id="stufen"><div class="leise" style="margin-top:16px">Lade Angebot …</div></div>
<footer>Betrieben mit Fundus. Die Zahlung läuft über PayPal; dieser Hotspot sieht keine Zahlungsdaten.</footer>
</main>
<script>
(function(){
  var $ = function(id){ return document.getElementById(id); };
  function esc(t){ return String(t == null ? '' : t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  function euro(x){ return Number(x).toLocaleString('de-DE', { style: 'currency', currency: 'EUR' }); }
  function dauer(h){ return h >= 24 && h % 24 === 0 ? (h / 24) + (h === 24 ? ' Tag' : ' Tage') : h + (h === 1 ? ' Stunde' : ' Stunden'); }
  function mbit(x){ return x >= 1000 ? '1 Gbit/s' : x + ' Mbit/s'; }
  function post(url, daten){
    return fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(daten) })
      .then(function(r){ return r.json().then(function(d){ if (!r.ok) throw new Error(d.error || 'Fehler'); return d; }); });
  }
  function statusZeigen(z){
    var el = $('status');
    if (!z) { el.style.display = 'none'; return; }
    el.className = 'status an';
    el.innerHTML = '<b>Verbunden</b> – ' + esc(z.name) + ', ' + mbit(z.down) + ', bis ' +
      new Date(z.bis * 1000).toLocaleString('de-DE', { weekday: 'short', hour: '2-digit', minute: '2-digit' }) + ' Uhr.' +
      '<div class="leise">Du kannst diese Seite schließen.</div>';
    el.style.display = '';
  }
  function fehler(id, text){ var e = $('f-' + id); if (e) e.textContent = text; }
  function laden(){
    fetch('/api/angebot').then(function(r){ return r.json(); }).then(function(d){
      statusZeigen(d.zugang);
      if (d.hinweis) { $('stufen').innerHTML = '<div class="status">' + esc(d.hinweis) + '</div>'; return; }
      var pp = d.paypal || {};
      $('stufen').innerHTML = (d.stufen || []).map(function(s){
        var knopf = s.preis > 0
          ? (pp.bereit ? '<div id="pp-' + esc(s.id) + '"></div>' : '<div class="leise">Bezahlung derzeit nicht verfügbar.</div>')
          : '<button class="gratis" data-stufe="' + esc(s.id) + '">Kostenlos verbinden</button>';
        return '<div class="stufe"><div class="kopf"><b>' + esc(s.name) + '</b><span class="preis">' + (s.preis > 0 ? euro(s.preis) : 'gratis') + '</span></div>' +
          '<div class="werte">' + mbit(s.down) + ' ↓ · ' + mbit(s.up) + ' ↑ · ' + dauer(s.stunden) + '</div>' + knopf +
          '<div class="fehler" id="f-' + esc(s.id) + '"></div></div>';
      }).join('');
      document.querySelectorAll('button.gratis').forEach(function(b){
        b.onclick = function(){
          b.disabled = true; b.textContent = 'Verbinde …';
          post('/api/gratis', { stufe: b.dataset.stufe }).then(function(x){ statusZeigen(x.zugang); b.textContent = 'Verbunden'; })
            .catch(function(e){ b.disabled = false; b.textContent = 'Kostenlos verbinden'; fehler(b.dataset.stufe, e.message); });
        };
      });
      if (pp.bereit && pp.client_id) paypalLaden(pp.client_id, d.stufen || []);
    }).catch(function(){ $('stufen').innerHTML = '<div class="status">Angebot nicht ladbar – bitte Seite neu laden.</div>'; });
  }
  function paypalLaden(clientId, stufen){
    var sk = document.createElement('script');
    sk.src = 'https://www.paypal.com/sdk/js?client-id=' + encodeURIComponent(clientId) + '&currency=EUR&intent=capture&components=buttons&locale=de_DE';
    sk.onload = function(){
      stufen.filter(function(s){ return s.preis > 0; }).forEach(function(s){
        window.paypal.Buttons({
          style: { layout: 'horizontal', tagline: false, height: 44 },
          createOrder: function(){ return post('/api/bestellung', { stufe: s.id }).then(function(d){ return d.id; }); },
          onApprove: function(data){
            fehler(s.id, 'Zahlung wird bestätigt …');
            return post('/api/abschluss', { id: data.orderID }).then(function(x){
              fehler(s.id, ''); statusZeigen(x.zugang);
            }).catch(function(e){ fehler(s.id, e.message); });
          },
          onError: function(){ fehler(s.id, 'Die Zahlung hat nicht geklappt. Es wurde nichts abgebucht.'); }
        }).render('#pp-' + s.id);
      });
    };
    var weg = function(){ stufen.forEach(function(s){ if (s.preis > 0) fehler(s.id, 'PayPal nicht erreichbar – bitte später erneut versuchen.'); }); };
    sk.onerror = weg;
    // Antwortet PayPal gar nicht (statt abzulehnen), käme nie ein Fehler – deshalb eine Frist.
    setTimeout(function(){ if (!window.paypal) weg(); }, 15000);
    document.head.appendChild(sk);
  }
  laden();
})();
</script></body></html>`

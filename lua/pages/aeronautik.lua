-- Aeronautik (R845): Flugverkehr in Echtzeit aus Transpondertelegrammen.
-- Daten: /api/v1/aeronautik/live (internal/api/aeronautik.go) – vom eigenen
-- Empfänger (readsb) oder vom nächstgelegenen im Netz.
local render = require "render"
local icons = require "icons"

return function()
    ngx.header["Content-Type"] = "text/html"
    render.header("Aeronautik", "aeronautik")

    ngx.print([==[
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/leaflet/1.9.4/leaflet.min.css">
<script src="https://cdnjs.cloudflare.com/ajax/libs/leaflet/1.9.4/leaflet.min.js" defer></script>
<style>
.ae-titel { margin:0; font-size:20px; font-weight:800; display:flex; align-items:center; gap:8px; color:#5ec8ff;
  text-shadow:0 0 10px rgba(94,200,255,.6); }
.ae-titel .ico { width:24px; height:24px; filter:drop-shadow(0 0 5px rgba(94,200,255,.8)); }
.ae-stand { margin-left:auto; font-size:12.5px; color:var(--muted); }
.ae-wrap { display:grid; grid-template-columns: 1fr 340px; gap:14px; }
@media (max-width: 900px) { .ae-wrap { grid-template-columns: 1fr; } }
#ae-karte { height:min(72vh, 680px); border-radius:12px; border:1px solid var(--border); }
/* R850: Telefon – Karte in der oberen Hälfte, darunter Details und Liste.
   svh = kleine Bildschirmhöhe: bleibt gleich, wenn die Adressleiste beim
   Scrollen ein- und ausfährt (vh springt dabei). */
@media (max-width: 900px) {
  #ae-karte { height:50vh; height:50svh; }
  .ae-liste { max-height:none; }
  .ae-legende { order:3; }   /* Flugdaten zuerst, Legende ans Ende */
}
/* Kein Zoom per Doppeltipp: ein gezoomtes Fenster löst feste Leisten (untere
   Navigation) vom Bildschirmrand. Kartenzoom per Zwei-Finger bleibt. */
.ae-wrap, .ae-seite, #ae-einrichtung { touch-action: manipulation; }
.ae-seite { display:flex; flex-direction:column; gap:14px; min-width:0; }
.ae-karte { border:1px solid var(--border); border-radius:12px; padding:12px 14px; background:rgba(26,29,39,.2); }
.ae-karte h3 { margin:0 0 8px; font-size:14px; }
.ae-detail .flug { font-size:20px; font-weight:800; letter-spacing:.02em; }
.ae-route { display:flex; align-items:center; gap:8px; margin:8px 0; font-size:15px; }
.ae-route .code { font-weight:800; font-size:18px; }
.ae-route .ort { font-size:12px; color:var(--muted); }
.ae-werte { display:grid; grid-template-columns: auto 1fr; gap:3px 12px; font-size:13px; }
.ae-werte span:nth-child(odd) { color:var(--muted); }
.ae-liste { max-height:46vh; overflow-y:auto; }
.ae-zeile { display:flex; align-items:center; gap:8px; padding:6px 4px; border-bottom:1px solid var(--border); cursor:pointer; font-size:13px; }
.ae-zeile:hover, .ae-zeile.aktiv { background:rgba(94,200,255,.08); }
.ae-zeile .fl { font-weight:700; min-width:74px; }
.ae-zeile .rt { flex:1; min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; color:var(--muted); }
.ae-zeile .km { font-variant-numeric:tabular-nums; white-space:nowrap; }
.ae-flieger { background:none; border:none; }
.ae-flieger svg { width:26px; height:26px; filter:drop-shadow(0 0 3px rgba(0,0,0,.9)); transition:transform .8s linear; }
.ae-hinweis { font-size:13px; color:var(--muted); line-height:1.5; }
.ae-notfall { color:#ff5a5a; font-weight:800; }
.ae-legende div { font-size:12.5px; line-height:1.6; color:var(--muted); }
.ae-legende span { display:inline-block; width:18px; font-size:15px; }
.ae-ein-kopf { display:flex; align-items:center; gap:10px; font-weight:700; }
.ae-ein-kopf .dreh { width:16px; height:16px; border:2px solid rgba(94,200,255,.3); border-top-color:#5ec8ff; border-radius:50%; animation:ae-dreh 1s linear infinite; }
@keyframes ae-dreh { to { transform:rotate(360deg); } }
.ae-ein-schritte { display:flex; gap:6px; margin:10px 0 6px; flex-wrap:wrap; font-size:12px; }
.ae-ein-schritte span { padding:2px 8px; border-radius:9px; border:1px solid var(--border); color:var(--muted); }
.ae-ein-schritte span.ok { border-color:rgba(61,220,132,.5); color:#3ddc84; }
.ae-ein-schritte span.jetzt { border-color:#5ec8ff; color:#5ec8ff; }
.ae-ein-zeile { font-family:monospace; font-size:12px; color:var(--muted); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
#ae-karte .leaflet-tile { filter:brightness(.62) contrast(1.1) saturate(.6); }
</style>
<div class="top-bar" style="margin-bottom:14px"><h2 class="ae-titel"><span class="ico">]==] .. icons.svg("aeronautik") .. [==[</span>Aeronautik</h2>
  <span class="ae-stand" id="ae-stand">verbinde …</span></div>
<div id="ae-einrichtung" class="ae-karte" style="display:none;margin-bottom:14px"></div>
<div id="ae-ort" class="ae-karte ae-hinweis" style="display:none;margin-bottom:14px"></div>
<div class="ae-wrap">
  <div id="ae-karte"></div>
  <div class="ae-seite">
    <div class="ae-karte ae-detail" id="ae-detail"><div class="ae-hinweis">Auf ein Flugzeug tippen – in der Karte oder in der Liste.</div></div>
    <div class="ae-karte ae-legende"><h3>Farben</h3>
      <div><span style="color:#3ddc84">✈</span> unter 3.000 m – Start, Landung, Kleinflugzeuge, Hubschrauber</div>
      <div><span style="color:#ffc94d">✈</span> 3.000–8.000 m – Steig- und Sinkflug, kurze Strecken</div>
      <div><span style="color:#5ec8ff">✈</span> über 8.000 m – Reiseflug</div>
      <div><span style="color:#9aa0b4">✈</span> am Boden</div>
      <div><span style="color:#ff3b3b">✈</span> Notfall-Code: 7500 Entführung, 7600 Funkausfall, 7700 Notfall</div>
      <div style="margin-top:4px">⌂ Standort dieses Pi · Ringe 25, 50, 100, 200 km</div>
    </div>
    <div class="ae-karte"><h3>Über dir</h3><div class="ae-liste" id="ae-liste"><div class="ae-hinweis">Warte auf Daten …</div></div></div>
  </div>
</div>
<script>
(function(){
  var $ = function(id){ return document.getElementById(id); };
  var karte = null, marker = {}, spuren = {}, gewaehlt = null, letzte = null, listeAlt = '', detailAlt = '';
  var haus = null, empfMarke = null, ringe = [], ringMitte = '', zentriert = false;
  function ringeUm(m){
    var schl = m.lat.toFixed(4) + ',' + m.lon.toFixed(4);
    if (schl === ringMitte) return;
    ringMitte = schl;
    ringe.forEach(function(r){ karte.removeLayer(r); });
    ringe = [25, 50, 100, 200].map(function(k){
      return L.circle([m.lat, m.lon], { radius: k * 1000, color: '#5ec8ff', weight: 1, opacity: .25, fill: false, interactive: false }).addTo(karte);
    });
    if (!zentriert) { zentriert = true; karte.setView([m.lat, m.lon], 9); }
  }
  var FLUGZEUG = '<svg viewBox="0 0 24 24"><path fill="FARBE" stroke="#0b0d12" stroke-width="0.8" d="M12 2.5c.8 0 1.3.9 1.3 2v5.2l7.2 4.3v2l-7.2-2.2v4.4l2.2 1.6v1.7L12 20.7l-3.5.8v-1.7l2.2-1.6v-4.4l-7.2 2.2v-2l7.2-4.3V4.5c0-1.1.5-2 1.3-2z"/></svg>';
  function farbe(f){
    if (f.notfall) return '#ff3b3b';
    if (f.am_boden) return '#9aa0b4';
    var h = f.hoehe_m || 0;
    return h < 3000 ? '#3ddc84' : h < 8000 ? '#ffc94d' : '#5ec8ff';   // tief, mittel, Reiseflug
  }
  function esc(t){ return String(t == null ? '' : t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  function zahl(n, d){ return n == null ? '–' : Number(n).toLocaleString('de-DE', {maximumFractionDigits: d || 0}); }
  function ort(a){ return a ? '<span class="code">' + esc(a.code || '?') + '</span><span class="ort">' + esc([a.stadt, a.land].filter(Boolean).join(', ')) + '</span>' : '<span class="code">?</span>'; }
  function routeText(f){
    if (!f.route || !f.route.von) return f.flug ? 'Route unbekannt' : 'ohne Rufzeichen';
    // R853: passt die Route nicht zu Position und Kurs, mit Fragezeichen
    return (f.route.von.code || '?') + ' → ' + (f.route.nach && f.route.nach.code || '?') + (f.route_zweifel ? ' ?' : '');
  }

  function starten(){
    if (typeof L === 'undefined') { setTimeout(starten, 150); return; }
    karte = L.map('ae-karte', { zoomControl: true }).setView([50.3, 8.95], 9);
    L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 18, attribution: '&copy; OpenStreetMap' }).addTo(karte);
    [100, 500, 1500].forEach(function(ms){ setTimeout(function(){ karte.invalidateSize(); }, ms); });
    if (window.ResizeObserver) new ResizeObserver(function(){ karte.invalidateSize(); }).observe($('ae-karte'));
    laden();
  }

  function detailSetzen(h){ if (h !== detailAlt) { detailAlt = h; $('ae-detail').innerHTML = h; } }
  function detail(f){
    if (!f) { detailSetzen('<div class="ae-hinweis">Auf ein Flugzeug tippen – in der Karte oder in der Liste.</div>'); return; }
    var r = f.route || {}, z = f.flugzeug || {};
    var h = '<div class="flug">' + esc(f.flug || f.hex.toUpperCase()) + (f.notfall ? ' <span class="ae-notfall">NOTFALL ' + esc(f.squawk) + '</span>' : '') + '</div>';
    if (r.airline) h += '<div class="ae-hinweis">' + esc(r.airline) + '</div>';
    h += r.von ? '<div class="ae-route"' + (f.route_zweifel ? ' style="opacity:.55"' : '') + '><div>' + ort(r.von) + '</div><div>→</div><div>' + ort(r.nach) + '</div></div>' +
                 (f.route_zweifel ? '<div class="ae-hinweis" style="margin:-2px 0 8px">Laut Datenbank – passt aber nicht zu Position und Kurs. Vermutlich wird das Rufzeichen gerade für eine andere Strecke verwendet.</div>' : '')
               : '<div class="ae-hinweis" style="margin:8px 0">' + (f.flug ? 'Für dieses Rufzeichen ist keine Route bekannt – oft bei Charter- und Privatflügen.' : 'Sendet kein Rufzeichen.') + '</div>';
    h += '<div class="ae-werte">' +
      '<span>Höhe</span><span>' + (f.am_boden ? 'am Boden' : zahl(f.hoehe_m) + ' m') + '</span>' +
      '<span>Geschwindigkeit</span><span>' + zahl(f.geschw_kmh) + ' km/h</span>' +
      '<span>Kurs</span><span>' + zahl(f.kurs) + '°</span>' +
      '<span>Steigen/Sinken</span><span>' + (f.steig_mmin == null ? '–' : (f.steig_mmin > 0 ? '+' : '') + zahl(f.steig_mmin) + ' m/min') + '</span>' +
      '<span>Entfernung</span><span>' + (f.km == null ? '–' : zahl(f.km, 1) + ' km') + '</span>' +
      (z.typ ? '<span>Typ</span><span>' + esc([z.hersteller, z.typ].filter(Boolean).join(' ')) + '</span>' : '') +
      (z.kennzeichen ? '<span>Kennzeichen</span><span>' + esc(z.kennzeichen) + '</span>' : '') +
      (z.halter ? '<span>Halter</span><span>' + esc(z.halter) + '</span>' : '') +
      '<span>Transponder</span><span>' + esc(f.hex.toUpperCase()) + (f.squawk ? ' · Squawk ' + esc(f.squawk) : '') + '</span>' +
      (r.quelle ? '<span>Route laut</span><span>' + esc(r.quelle) + '</span>' : '') +
      '</div>';
    detailSetzen(h);
  }

  function waehlen(hex){
    gewaehlt = hex;
    Object.keys(spuren).forEach(function(k){ spuren[k].setStyle({ opacity: k === hex ? 0.95 : 0.25, weight: k === hex ? 3 : 1.5 }); });
    var f = (letzte && letzte.flugzeuge || []).find(function(x){ return x.hex === hex; });
    detail(f);
    document.querySelectorAll('.ae-zeile').forEach(function(z){ z.classList.toggle('aktiv', z.dataset.hex === hex); });
  }

  function zeichnen(d){
    letzte = d;
    var flieger = d.flugzeuge || [], da = {};
    // R851: Häuschen = Standort des Pi, der diese Seite bedient. Gesetzt oder
    // verschoben, sobald eine Antwort ihn enthält – nicht nur bei der ersten.
    var st = d.standort && (d.standort.lat || d.standort.lon) ? d.standort : null;
    var e = d.empfaenger && (d.empfaenger.lat || d.empfaenger.lon) ? d.empfaenger : null;
    if (st) {
      if (!haus) haus = L.marker([st.lat, st.lon], { icon: L.divIcon({ className: 'ae-flieger', iconSize: [24, 24], iconAnchor: [12, 12],
          html: '<svg viewBox="0 0 24 24" style="width:24px;height:24px"><path fill="#ffffff" stroke="#0b0d12" stroke-width="1" d="M3 11.5 12 4l9 7.5V20a1 1 0 0 1-1 1h-5v-6h-6v6H4a1 1 0 0 1-1-1z"/></svg>' }),
          zIndexOffset: 1000 }).addTo(karte);
      else haus.setLatLng([st.lat, st.lon]);
      haus.bindTooltip('Standort von ' + esc(d.node || 'diesem Node'));
    }
    if (d.quelle === 'peer' && e) {
      if (!empfMarke) empfMarke = L.circleMarker([e.lat, e.lon], { radius: 5, color: '#5ec8ff', fillOpacity: 1 }).addTo(karte);
      else empfMarke.setLatLng([e.lat, e.lon]);
      empfMarke.bindTooltip('Empfänger ' + esc(d.empfaenger_node || '') + ' (' + zahl(d.peer_km, 1) + ' km entfernt)');
    }
    var mitte = st || e;
    if (mitte) ringeUm(mitte);
    // Fehlt der Standort, sagen WELCHEM Pi – die Lastverteilung reicht die
    // Seite an einen festen Pi weiter, nicht unbedingt an den, den man aufruft.
    var ort = $('ae-ort');
    if (st) ort.style.display = 'none';
    else {
      ort.innerHTML = 'Dem Pi <b>' + esc(d.node || '?') + '</b>, der diese Seite bedient, fehlt der Standort – deshalb kein Häuschen und keine Entfernungen' +
        (e ? '; die Ringe liegen ersatzweise um den Empfänger' : '') + '. Setzen auf diesem Pi unter Einstellungen → <a href="/settings#tab=standort">Standort</a>' +
        ' (über seine Adresse im Heimnetz, falls du ihn von außen aufrufst).';
      ort.style.display = '';
    }
    flieger.forEach(function(f){
      da[f.hex] = true;
      var icon = L.divIcon({ className: 'ae-flieger', iconSize: [26, 26], iconAnchor: [13, 13],
        html: '<div style="transform:rotate(' + (f.kurs || 0) + 'deg)">' + FLUGZEUG.replace('FARBE', farbe(f)) + '</div>' });
      if (!marker[f.hex]) {
        marker[f.hex] = L.marker([f.lat, f.lon], { icon: icon }).addTo(karte)
          .on('click', (function(h){ return function(){ waehlen(h); }; })(f.hex));
      } else {
        marker[f.hex].setLatLng([f.lat, f.lon]).setIcon(icon);
      }
      marker[f.hex].bindTooltip(esc(f.flug || f.hex.toUpperCase()) + ' · ' + esc(routeText(f)) + ' · ' + (f.am_boden ? 'Boden' : zahl(f.hoehe_m) + ' m'), { direction: 'top', offset: [0, -12] });
      var punkte = (f.spur || []).map(function(p){ return [p[0], p[1]]; });
      if (!spuren[f.hex]) spuren[f.hex] = L.polyline(punkte, { color: farbe(f), weight: 1.5, opacity: .25, interactive: false }).addTo(karte);
      else spuren[f.hex].setLatLngs(punkte).setStyle({ color: farbe(f) });
    });
    Object.keys(marker).forEach(function(hex){
      if (!da[hex]) { karte.removeLayer(marker[hex]); karte.removeLayer(spuren[hex]); delete marker[hex]; delete spuren[hex]; }
    });
    // Liste: die nächsten 30, nach Entfernung
    var nah = flieger.slice(0, 30);
    // R850: Nur neu aufbauen, wenn sich die Liste geändert hat. Jede Sekunde
    // komplett neu zu bauen verschob auf Telefonen den Inhalt mitten im
    // Scrollen – die Seite sprang, die untere Leiste schien mitzuwandern.
    var listeNeu = nah.length ? nah.map(function(f){
      return '<div class="ae-zeile" data-hex="' + esc(f.hex) + '"><span style="color:' + farbe(f) + '">●</span>' +
        '<span class="fl">' + esc(f.flug || f.hex.toUpperCase()) + '</span><span class="rt"' + (f.route_zweifel ? ' style="opacity:.55"' : '') + '>' + esc(routeText(f)) + '</span>' +
        '<span class="km">' + (f.km == null ? '' : zahl(f.km, 1) + ' km') + '</span></div>';
    }).join('') : '<div class="ae-hinweis">Gerade kein Flugzeug mit Position in Reichweite.</div>';
    if (listeNeu !== listeAlt) {
      listeAlt = listeNeu;
      $('ae-liste').innerHTML = listeNeu;
      document.querySelectorAll('.ae-zeile').forEach(function(z){ z.onclick = function(){ waehlen(z.dataset.hex); var m = marker[z.dataset.hex]; if (m) karte.panTo(m.getLatLng()); }; });
    }
    if (gewaehlt) waehlen(gewaehlt);
    $('ae-stand').textContent = flieger.length + ' Flugzeuge · ' + (d.quelle === 'peer' ? 'Empfänger im Netz (' + zahl(d.peer_km, 1) + ' km entfernt)' : 'eigener Empfänger') +
      ' · ' + new Date((d.zeit || Date.now() / 1000) * 1000).toLocaleTimeString('de-DE');
  }

  // R848: Fortschritt der Einrichtung im Hintergrund (setup-aeronautik.sh).
  var SCHRITTE = [['treiber','Treiber'],['installieren','readsb übersetzen'],['standort','Standort'],['dienst','Start'],['fertig','bereit']];
  function einrichtung(){
    fetch('/api/v1/aeronautik/einrichtung', { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(st){
      var el = $('ae-einrichtung');
      if (!st || !st.phase) { el.style.display = 'none'; return; }
      var alter = Date.now() / 1000 - (st.stand || 0);
      // Erfolgreich abgeschlossen und schon eine Weile her: kein Hinweis mehr.
      if (st.fertig && !st.fehler && alter > 120) { el.style.display = 'none'; return; }
      var min = Math.max(0, Math.round(((st.stand || 0) - (st.seit || 0)) / 60));
      var idx = SCHRITTE.findIndex(function(x){ return x[0] === st.phase; });
      var kopf = st.fehler ? '<span class="ae-notfall">✗ Einrichtung fehlgeschlagen</span>'
        : st.haengt ? '<span class="ae-notfall">Einrichtung meldet sich seit über 10 Minuten nicht</span>'
        : st.fertig ? '✓ Aeronautik eingerichtet'
        : '<span class="dreh"></span>Aeronautik wird eingerichtet · ' + min + ' min';
      el.innerHTML = '<div class="ae-ein-kopf">' + kopf + '</div>' +
        '<div class="ae-ein-schritte">' + SCHRITTE.map(function(x, i){
          var k = (st.fertig && !st.fehler) || i < idx ? 'ok' : i === idx && !st.fehler ? 'jetzt' : '';
          return '<span class="' + k + '">' + x[1] + '</span>'; }).join('') + '</div>' +
        '<div class="ae-hinweis">' + esc(st.text || '') +
          (st.phase === 'installieren' && !st.fehler ? ' – auf einem Pi 4 etwa 5 Minuten, auf einem Pi 3 bis zu 20.' : '') + '</div>' +
        (st.letzte ? '<div class="ae-ein-zeile" title="' + esc(st.letzte) + '">' + esc(st.letzte) + '</div>' : '') +
        (st.fehler || st.haengt ? '<div class="ae-hinweis" style="margin-top:6px">Protokoll auf dem Pi: <code>' + esc(st.protokoll || '') + '</code></div>' : '');
      el.style.display = '';
    }).catch(function(){}).then(function(){ setTimeout(einrichtung, 5000); });
  }
  einrichtung();

  function laden(){
    fetch('/api/v1/aeronautik/live', { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(d){
      if (d.quelle === 'keine') {
        $('ae-stand').textContent = 'kein Empfänger';
        $('ae-liste').innerHTML = '<div class="ae-hinweis">Kein Empfänger erreichbar. Mit einem RTL-SDR-Stick (z. B. Terratec T Stick) auf einem Pi das Skript <code>tools/setup-aeronautik.sh</code> aus dem Fundus-Paket mit <code>sudo bash</code> ausführen.</div>';
      } else zeichnen(d);
    }).catch(function(){ $('ae-stand').textContent = 'Verbindung unterbrochen …'; })
      .then(function(){ setTimeout(laden, 1000); });
  }
  starten();
})();
</script>
]==])
    render.footer()
end

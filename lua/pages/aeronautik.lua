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
/* R859: Reiter und Empfänger-Modus */
.ae-reiter { display:flex; gap:6px; margin:0 0 10px; flex-wrap:wrap; }
.ae-reiter button { padding:7px 14px; border-radius:18px; border:1px solid var(--border); background:rgba(26,29,39,.4); color:var(--muted); cursor:pointer; font-size:13.5px; }
.ae-reiter button.aktiv { border-color:#5ec8ff; color:#5ec8ff; background:rgba(94,200,255,.1); }
/* R877: Aufzeichnung, Wiedergabe, Quartett */
.ae-werkzeuge { display:flex; gap:6px; flex-wrap:wrap; align-items:center; margin:0 0 10px; }
.ae-werkzeuge button { padding:6px 12px; border-radius:16px; border:1px solid var(--border); background:rgba(26,29,39,.4); color:var(--text); cursor:pointer; font-size:13px; }
.ae-werkzeuge button.aktiv { border-color:#5ec8ff; color:#5ec8ff; }
.ae-werkzeuge .rec { color:#ff5a5a; }
.ae-panel { margin:0 0 12px; }
.ae-panel .zeile { display:flex; gap:10px; align-items:center; flex-wrap:wrap; margin:6px 0; font-size:13.5px; }
.ae-panel select, .ae-panel input[type=date], .ae-panel input[type=number] { padding:6px 8px; border-radius:8px; font-size:14px; }
.ae-wg-regler { flex:1; min-width:180px; }
.ae-wg-zeit { font-variant-numeric:tabular-nums; font-weight:600; min-width:70px; }
/* R887: Typenliste mit Foto und KI-Bild */
.ae-typen { max-height:440px; overflow-y:auto; border:1px solid var(--border); border-radius:10px; margin-top:8px; }
.ae-typ { display:grid; grid-template-columns:96px 96px 1fr auto; gap:10px; align-items:center; padding:8px 10px; border-bottom:1px solid var(--border); font-size:13px; }
.ae-typ:last-child { border-bottom:0; }
.ae-typ .bild { width:96px; height:54px; border-radius:6px; background:rgba(127,127,127,.12); display:flex; align-items:center; justify-content:center; overflow:hidden; font-size:11px; color:var(--muted); text-align:center; }
.ae-typ .bild img { width:100%; height:100%; object-fit:cover; cursor:zoom-in; }
.ae-typ .tasten { display:flex; flex-direction:column; gap:4px; }
.ae-typ .klein { font-size:11.5px; color:var(--muted); }
.ae-kand { grid-column:1 / -1; border-top:1px dashed var(--border); padding-top:8px; }
.ae-kand .raster { display:grid; grid-template-columns:repeat(auto-fill, minmax(120px, 1fr)); gap:8px; margin-top:8px; }
.ae-kand figure { margin:0; cursor:pointer; border:2px solid transparent; border-radius:8px; overflow:hidden; background:rgba(127,127,127,.1); }
.ae-kand figure:hover { border-color:var(--green); }
.ae-kand img { width:100%; height:80px; object-fit:cover; display:block; }
.ae-kand figcaption { font-size:10.5px; padding:3px 5px; color:var(--muted); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
@media (max-width:640px) { .ae-typ { grid-template-columns:72px 72px 1fr; } .ae-typ .bild { width:72px; height:42px; } .ae-typ .tasten { grid-column:1 / -1; flex-direction:row; } }
.ae-modus { display:flex; align-items:center; gap:10px; flex-wrap:wrap; padding:9px 12px; margin:0 0 12px; border-radius:10px;
  border:1px solid var(--border); font-size:13px; color:var(--muted); background:rgba(26,29,39,.25); }
.ae-modus b { color:var(--text); }
.ae-modus button { padding:5px 12px; border-radius:14px; border:1px solid #5ec8ff; background:none; color:#5ec8ff; cursor:pointer; font-size:12.5px; }
.ae-modus.anders { border-color:rgba(255,201,77,.5); }
.ae-nachr { border-bottom:1px solid var(--border); padding:9px 2px; font-size:13px; }
/* R863: Wettersatelliten */
.ae-wetter-kopf { display:flex; gap:12px; align-items:center; flex-wrap:wrap; margin:0 0 12px; font-size:13.5px; }
.ae-wetter-tab { width:100%; border-collapse:collapse; font-size:13px; }
.ae-wetter-tab td, .ae-wetter-tab th { padding:6px 8px; border-bottom:1px solid var(--border); text-align:left; }
.ae-wetter-tab th { color:var(--muted); font-weight:500; font-size:12px; }
.ae-galerie { display:grid; grid-template-columns:repeat(auto-fill, minmax(260px, 1fr)); gap:12px; }
.ae-ueberflug .bilder { display:flex; gap:6px; margin-top:8px; overflow-x:auto; }
.ae-ueberflug .bilder img { height:150px; border-radius:6px; background:#000; display:block; }

.ae-nachr .kopf { display:flex; gap:10px; flex-wrap:wrap; align-items:baseline; }
.ae-nachr .kopf b { font-size:14px; }
.ae-nachr .kopf span { color:var(--muted); font-size:12px; }
.ae-nachr pre { margin:5px 0 0; white-space:pre-wrap; word-break:break-word; font-size:12px; color:var(--text); font-family:ui-monospace,monospace; }
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
<div class="ae-reiter" id="ae-reiter">
  <button type="button" data-a="flug" class="aktiv">✈ Flugzeuge</button>
  <button type="button" data-a="nachrichten">💬 Funknachrichten</button>
  <button type="button" data-a="schiffe">🚢 Schiffe</button>
  <button type="button" data-a="wetter">🛰 Wetter</button>
  <button type="button" id="ae-ausst-knopf" style="margin-left:auto;display:none">⚙ Ausstattung</button>
</div>
<div id="ae-ausstattung" class="ae-karte" style="display:none;margin-bottom:12px"></div>
<div id="ae-aus" class="ae-karte" style="display:none"></div>
<div id="ae-modus" class="ae-modus" style="display:none"></div>
<div id="ae-einrichtung" class="ae-karte" style="display:none;margin-bottom:14px"></div>
<div id="ae-ort" class="ae-karte ae-hinweis" style="display:none;margin-bottom:14px"></div>
<div id="ae-nachrichten" class="ae-karte" style="display:none"><div class="ae-hinweis">Warte auf Funknachrichten …</div></div>
<div id="ae-wetter" style="display:none"><div class="ae-hinweis">Lade Wettersatelliten …</div></div>
<div id="ae-werkzeuge-wrap">
<div class="ae-werkzeuge">
  <button type="button" id="ae-w-auf">⏺ Aufzeichnung</button>
  <button type="button" id="ae-w-wieder">▶ Wiedergabe</button>
  <button type="button" id="ae-w-quartett">🃏 Flugquartett</button>
  <span class="ae-hinweis" id="ae-w-status"></span>
</div>
<div id="ae-p-auf" class="ae-karte ae-panel" style="display:none">
  <h3>Flugdaten aufzeichnen</h3>
  <div class="ae-hinweis" id="ae-auf-quelle" style="margin-bottom:6px"></div>
  <div class="zeile"><label><input type="checkbox" id="ae-auf-an"> Quartett-Tabellen pflegen (Tag, Woche, Monat, Jahr)</label></div>
  <div class="zeile"><label><input type="checkbox" id="ae-auf-spuren"> Spuren für die Wiedergabe aufzeichnen</label>
    <select id="ae-auf-takt"><option value="0">jede Meldung</option><option value="1">jede Sekunde</option><option value="2">alle 2 s</option><option value="5">alle 5 s</option><option value="10">alle 10 s</option></select>
    aufbewahren <input type="number" id="ae-auf-tage" min="1" max="3650" style="width:70px"> Tage</div>
  <div class="zeile"><label><input type="checkbox" id="ae-auf-luft"> Tiefstwerte (niedrigste Höhe, langsamstes, nächstes) nur in der Luft</label></div>
  <div class="zeile"><label><input type="checkbox" id="ae-f-an"> Plausibilitätsfilter</label></div>
  <div class="zeile" id="ae-f-werte">
    ab <input type="number" id="ae-f-min" min="1" max="100" style="width:56px"> Meldungen ·
    Höhe ≤ <input type="number" id="ae-f-hoehe" style="width:76px"> m ·
    Sprung ≤ <input type="number" id="ae-f-sprung" style="width:66px"> m/15 s ·
    ≤ <input type="number" id="ae-f-geschw" style="width:66px"> km/h ·
    |Steigen| ≤ <input type="number" id="ae-f-steig" style="width:56px"> m/s ·
    ≤ <input type="number" id="ae-f-km" style="width:62px"> km</div>
  <div class="ae-hinweis" id="ae-auf-info"></div>
  <div class="zeile"><button type="button" class="btn btn-primary" id="ae-auf-speichern" style="width:auto">Speichern</button><span class="ae-hinweis" id="ae-auf-meldung"></span></div>
</div>
<div id="ae-p-wieder" class="ae-karte ae-panel" style="display:none">
  <h3>Wiedergabe</h3>
  <div class="zeile"><select id="ae-wg-tag"></select>
    <button type="button" class="btn-sm" id="ae-wg-spiel">▶ Abspielen</button>
    <select id="ae-wg-tempo"><option value="1">1×</option><option value="10" selected>10×</option><option value="60">60×</option><option value="300">300×</option></select>
    <button type="button" class="btn-sm" id="ae-wg-live">Zurück zu Live</button></div>
  <div class="zeile"><span class="ae-wg-zeit" id="ae-wg-zeit">--:--</span><input type="range" class="ae-wg-regler" id="ae-wg-regler" min="0" max="1439" step="1" value="0"></div>
  <div class="ae-hinweis" id="ae-wg-hinweis"></div>
</div>
<div id="ae-p-quartett" class="ae-karte ae-panel" style="display:none">
  <h3>Flugquartett</h3>
  <div class="ae-hinweis" style="margin-bottom:6px">32 oder 64 Karten aus den aufgezeichneten Rekorden, dazu eine Titelkarte mit den Regeln: je vier oder acht Flugzeuge mit der größten und kleinsten Höhe, Geschwindigkeit, Steig- bzw. Sinkrate und Entfernung. Jede Karte zeigt zu allen vier Werten Minimum und Maximum, jeweils mit Zeitstempel. Nach jeder Vorderseite folgt ihre Rückseite.</div>
  <div class="zeile"><select id="ae-q-zeitraum"><option value="tag">Tag</option><option value="woche">Woche</option><option value="monat">Monat</option><option value="jahr">Jahr</option></select>
    <input type="date" id="ae-q-datum">
    <select id="ae-q-anzahl"><option value="64">FLIGHT QUAD 64 (64 Karten)</option><option value="32">FLIGHT QUAD 32 (32 Karten)</option></select>
    <select id="ae-q-groesse"><option value="63x88">63 × 88 mm</option><option value="59x91">59 × 91 mm</option></select>
    <select id="ae-q-format"><option value="karten">Einzelkarten mit 3 mm Beschnitt (RGB)</option><option value="druckerei">Druckerei: wie Einzelkarten, PDF/X-1a (CMYK)</option><option value="a4">DIN A4 zum Ausschneiden und Falten (RGB)</option><option value="a4_druck">DIN A4 zum Ausschneiden und Falten, PDF/X-1a (CMYK)</option></select>
    <button type="button" class="btn btn-primary" id="ae-q-los" style="width:auto">PDF erzeugen</button>
    <button type="button" class="btn-sm" id="ae-q-abbrechen" style="display:none">Umwandlung abbrechen</button>
    <button type="button" class="btn-sm" id="ae-q-nachtrag" title="Rekorde für den gewählten Zeitraum aus den aufgezeichneten Spuren (und alten Tagesbilanzen) in die Tabellen eintragen">Aus Aufzeichnung nachtragen</button></div>
  <div class="zeile">Bilder: <select id="ae-q-bilder">
      <option value="fotos_ki">Freie Fotos, sonst KI-Bild</option>
      <option value="fotos">Nur freie Fotos (Wikimedia Commons)</option>
      <option value="ki">Alle Bilder von der KI</option>
      <option value="keine">Keine Bilder (Silhouetten)</option></select>
    <label><input type="checkbox" id="ae-q-bekannt"> Unbekannte Flugzeugtypen ausblenden</label></div>
  <div class="ae-hinweis" style="margin:-2px 0 4px">Freie Fotos: nur frei lizenziert, Urheber steht auf der Karte. KI-Bilder: vom Sprachmodell beschrieben und von ComfyUI gemalt, je Typ einmal im Hintergrund, auf der Karte als „KI-erzeugt, kein Foto“ gekennzeichnet. Fehlt ein Bild, bleibt die Silhouette.</div>
  <div class="ae-hinweis" id="ae-q-meldung"></div>
  <div class="zeile" style="margin-top:10px"><button type="button" class="btn-sm" id="ae-q-typen-auf">Typen &amp; Bilder durchsehen</button>
    <span id="ae-q-typen-leiste" style="display:none;gap:8px;align-items:center;flex-wrap:wrap">
      <input type="search" id="ae-q-typen-suche" placeholder="Typ suchen …" style="padding:6px 8px;border-radius:8px;font-size:14px;width:150px">
      <select id="ae-q-typen-filter"><option value="alle">alle Typen</option><option value="ki">mit KI-Bild</option><option value="ohnefoto">ohne Foto</option><option value="ohne">ohne jedes Bild</option></select>
      <span class="ae-hinweis" id="ae-q-typen-info"></span></span></div>
  <div class="ae-typen" id="ae-q-typen" style="display:none"></div>
</div>
</div>
<div class="ae-wrap" id="ae-wrap">
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
  var SCHRITTE = [['treiber','Treiber'],['installieren','readsb'],['pakete','Pakete'],['vdl2','dumpvdl2'],['ais','AIS-catcher'],
    ['satdump','SatDump'],['standort','Standort'],['dienst','Start'],['fertig','bereit']];
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
          (/^(installieren|vdl2|ais)$/.test(st.phase) && !st.fehler ? ' – auf einem Pi 4 etwa 5 Minuten, auf einem Pi 3 bis zu 20.' : st.phase === 'satdump' && !st.fehler ? ' – das größte Programm: auf einem Pi 4 rund eine Stunde, auf einem Pi 3 mehrere Stunden. Der Pi bleibt dabei bedienbar.' : '') + '</div>' +
        (st.letzte ? '<div class="ae-ein-zeile" title="' + esc(st.letzte) + '">' + esc(st.letzte) + '</div>' : '') +
        (st.fehler || st.haengt ? '<div class="ae-hinweis" style="margin-top:6px">Protokoll auf dem Pi: <code>' + esc(st.protokoll || '') + '</code></div>' : '');
      el.style.display = '';
    }).catch(function(){}).then(function(){ setTimeout(einrichtung, 5000); });
  }
  einrichtung();

  // ── R859: Ansichten, Empfänger-Modus, Schiffe, Funknachrichten ──────────
  var ansicht = /^#(nachrichten|schiffe|wetter)$/.test(location.hash) ? location.hash.slice(1) : 'flug';
  var MODUS_FUER = { flug: 'adsb', nachrichten: 'vdl2', schiffe: 'ais' };
  var MODUS_NAME = { adsb: 'Flugzeuge', vdl2: 'Funknachrichten', ais: 'Schiffe' };
  var funkStatus = null, umschaltenSeit = 0, schiffMarker = {}, schiffGewaehlt = null, letzteSchiffe = [], nachrAlt = '';
  function ansichtSetzen(a){
    ansicht = a;
    history.replaceState(null, '', a === 'flug' ? location.pathname : '#' + a);
    document.querySelectorAll('#ae-reiter button').forEach(function(b){ b.classList.toggle('aktiv', b.dataset.a === a); });
    bereicheZeigen();
    // Ebenen der anderen Ansicht von der Karte nehmen
    if (karte) {
      if (a !== 'flug') { Object.keys(marker).forEach(function(h){ karte.removeLayer(marker[h]); karte.removeLayer(spuren[h]); delete marker[h]; delete spuren[h]; }); }
      if (a !== 'schiffe') { Object.keys(schiffMarker).forEach(function(m){ karte.removeLayer(schiffMarker[m]); delete schiffMarker[m]; }); }
      setTimeout(function(){ karte.invalidateSize(); }, 50);
    }
    listeAlt = ''; detailAlt = ''; nachrAlt = ''; gewaehlt = null; schiffGewaehlt = null;
    detail(null);
    modusZeigen();
  }
  document.querySelectorAll('#ae-reiter button').forEach(function(b){ b.onclick = function(){ ansichtSetzen(b.dataset.a); }; });

  // ── R864: Ausstattung ────────────────────────────────────────────────────
  var KOMP_FUER = { nachrichten: 'vdl2', schiffe: 'ais', wetter: 'meteor' };
  var KOMP_INFO = {
    vdl2: ['💬 Funknachrichten (VDL2)', 'Antenne: Stab etwa 55 cm, senkrecht.'],
    ais: ['🚢 Schiffe (AIS)', 'Antenne: Stab etwa 46 cm, senkrecht, freie Sicht Richtung Fluss.'],
    meteor: ['🛰 Wettersatelliten (Meteor-M)', 'Antenne: waagerechter Dipol 2 × 53 cm, Nord-Süd, freier Himmel.']
  };
  function abgeschaltet(){
    var k = KOMP_FUER[ansicht], st = funkStatus;
    return !!(k && st && st.stick && st.komponenten && !st.komponenten[k]);
  }
  function bereicheZeigen(){
    var aus = abgeschaltet();
    $('ae-aus').style.display = aus ? '' : 'none';
    $('ae-nachrichten').style.display = !aus && ansicht === 'nachrichten' ? '' : 'none';
    $('ae-wetter').style.display = !aus && ansicht === 'wetter' ? '' : 'none';
    $('ae-wrap').style.display = !aus && (ansicht === 'flug' || ansicht === 'schiffe') ? '' : 'none';
    $('ae-werkzeuge-wrap').style.display = !aus && ansicht === 'flug' ? '' : 'none';
    if (aus) {
      var k = KOMP_FUER[ansicht], i = KOMP_INFO[k];
      $('ae-aus').innerHTML = '<h3>' + i[0] + ' ist nicht zugeschaltet</h3><div class="ae-hinweis">' + i[1] +
        ' Erst nach dem Zuschalten wird der Dekoder auf dem Pi installiert.' + speicherHinweis(k) + '</div>' +
        '<button type="button" class="btn btn-primary" style="width:auto;margin-top:10px" id="ae-aus-auf">Ausstattung öffnen</button>';
      $('ae-aus-auf').onclick = function(){ ausstattungZeigen(true); };
    }
  }
  function speicherHinweis(k){
    var st = funkStatus || {};
    if (k !== 'meteor' || (st.vorhanden && st.vorhanden.meteor)) return '';
    return st.ram_mb && st.ram_mb < 2000 ? ' <b>Auf diesem Pi (' + zahl(st.ram_mb) + ' MB Arbeitsspeicher) wird SatDump nur installiert, wenn es ein fertiges Paket gibt – Übersetzen braucht mindestens 2 GB.</b>' : ' SatDump ist groß: Übersetzen dauert auf einem Pi 4 rund eine Stunde.';
  }
  function ausstattungZeigen(auf){
    var el = $('ae-ausstattung'), st = funkStatus || {}, k = st.komponenten || {};
    if (!auf) { el.style.display = 'none'; return; }
    el.innerHTML = '<h3>Empfänger-Ausstattung</h3><div class="ae-hinweis" style="margin-bottom:8px">✈ Flugzeuge sind immer dabei. Alles andere braucht eine eigene Antenne und wird erst nach dem Zuschalten installiert.</div>' +
      ['vdl2', 'ais', 'meteor'].map(function(n){
        return '<label style="display:block;margin:8px 0"><input type="checkbox" data-k="' + n + '"' + (k[n] ? ' checked' : '') + '> <b>' + KOMP_INFO[n][0] + '</b>' +
          '<div class="ae-hinweis" style="margin-left:24px">' + KOMP_INFO[n][1] + speicherHinweis(n) + '</div></label>';
      }).join('') +
      '<div style="display:flex;gap:10px;align-items:center;margin-top:10px"><button type="button" class="btn btn-primary" style="width:auto" id="ae-ausst-speichern">Übernehmen</button>' +
      '<button type="button" class="btn-sm" id="ae-ausst-zu">Schließen</button><span class="ae-hinweis" id="ae-ausst-status"></span></div>';
    el.style.display = '';
    $('ae-ausst-zu').onclick = function(){ ausstattungZeigen(false); };
    $('ae-ausst-speichern').onclick = function(){
      var w = {}; el.querySelectorAll('input[data-k]').forEach(function(c){ w[c.dataset.k] = c.checked; });
      $('ae-ausst-status').textContent = 'Speichere …';
      fetch('/api/v1/admin/funk/komponenten', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(w) }).then(function(r){ return r.json(); }).then(function(d){
          if (d.error) { $('ae-ausst-status').textContent = '✗ ' + d.error; return; }
          if (funkStatus) funkStatus.komponenten = { vdl2: !!w.vdl2, ais: !!w.ais, meteor: !!w.meteor };
          $('ae-ausst-status').textContent = '✓ Übernommen – die Einrichtung startet jetzt im Hintergrund. Fortschritt im Reiter Flugzeuge.';
          bereicheZeigen();
        }).catch(function(){ $('ae-ausst-status').textContent = '✗ Speichern fehlgeschlagen.'; });
    };
  }
  $('ae-ausst-knopf').onclick = function(){ ausstattungZeigen($('ae-ausstattung').style.display === 'none'); };

  function modusZeigen(){
    var el = $('ae-modus'), st = funkStatus;
    if (!st || (!st.stick && !st.empfaenger_stick)) { el.style.display = 'none'; return; }
    if (ansicht === 'wetter' || abgeschaltet()) { el.style.display = 'none'; return; } // Planer schaltet selbst / nicht zugeschaltet
    var an = st.angewandt || {}, aktuell = an.modus || st.gewaehlt, gewollt = MODUS_FUER[ansicht];
    var vorh = st.vorhanden || {};
    var h = 'Empfänger ' + (st.node ? '<b>' + esc(st.node) + '</b> ' : '') + 'hört gerade auf <b>' + esc(MODUS_NAME[aktuell] || aktuell) + '</b>';
    if (an.fehler) h += ' – <span class="ae-notfall">' + esc(an.fehler) + '</span>';
    if (an.wartet) h += ' · <b>Wettersatellit</b> belegt den Stick – danach ' + esc(MODUS_NAME[an.modus] || an.modus);
    var umschaltend = Date.now() - umschaltenSeit < 15000 && st.gewaehlt !== aktuell;
    if (umschaltend) h += ' · schaltet um auf ' + esc(MODUS_NAME[st.gewaehlt]) + ' …';
    else if (aktuell !== gewollt) {
      if (!st.stick) h += ' · Umschalten auf dem Pi mit dem Stick';
      else if (vorh[gewollt] === false) h += ' · Der Dekoder für ' + esc(MODUS_NAME[gewollt]) + ' wird beim nächsten Deploy installiert.';
      else h += '<button type="button" id="ae-umschalten">Auf ' + esc(MODUS_NAME[gewollt]) + ' umschalten</button>';
    }
    el.innerHTML = h;
    el.classList.toggle('anders', aktuell !== gewollt);
    el.style.display = '';
    var k = $('ae-umschalten');
    if (k) k.onclick = function(){
      k.disabled = true; k.textContent = 'schalte um …';
      fetch('/api/v1/admin/funk/modus', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ modus: gewollt }) }).then(function(r){ return r.json(); }).then(function(d){
          if (d.error) { k.textContent = d.error; return; }
          umschaltenSeit = Date.now(); if (funkStatus) funkStatus.gewaehlt = gewollt; modusZeigen(); setTimeout(funkStatusLaden, 4000);
        }).catch(function(){ k.textContent = 'Umschalten fehlgeschlagen'; });
    };
  }
  function funkStatusLaden(){
    fetch('/api/v1/funk/status', { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(st){
      funkStatus = st; modusZeigen();
      $('ae-ausst-knopf').style.display = st.stick ? '' : 'none';
      bereicheZeigen();
    }).catch(function(){});
  }
  setInterval(funkStatusLaden, 5000);

  // Schiffe
  var SCHIFF = '<svg viewBox="0 0 24 24"><path fill="FARBE" stroke="#0b0d12" stroke-width="0.9" d="M12 2.5 15.5 8v12.5a1 1 0 0 1-1 1h-5a1 1 0 0 1-1-1V8z"/></svg>';
  var NAVSTATUS = { 0:'unterwegs mit Maschine', 1:'vor Anker', 2:'manövrierunfähig', 3:'eingeschränkt manövrierfähig', 5:'festgemacht', 7:'beim Fischen', 8:'unter Segeln' };
  function schiffFarbe(s){
    return { 'Frachtschiff':'#3ddc84', 'Tanker':'#ff6b6b', 'Fahrgastschiff':'#5ec8ff', 'Sportboot':'#b48cff', 'Segelboot':'#b48cff',
      'Schlepper':'#ffb84d', 'Lotse':'#ffb84d', 'Polizei':'#ffffff', 'Seenotrettung':'#ff3b3b' }[s.art] || '#9aa0b4';
  }
  function schiffDetail(s){
    if (!s) { detailSetzen('<div class="ae-hinweis">Auf ein Schiff tippen – in der Karte oder in der Liste.</div>'); return; }
    detailSetzen('<div class="flug">' + esc(s.name || 'MMSI ' + s.mmsi) + '</div>' +
      (s.art ? '<div class="ae-hinweis">' + esc(s.art) + '</div>' : '') +
      (s.ziel ? '<div class="ae-route"><div>→</div><div><span class="code">' + esc(s.ziel) + '</span></div></div>' : '') +
      '<div class="ae-werte">' +
      '<span>Geschwindigkeit</span><span>' + (s.kmh < 0 ? '–' : zahl(s.kmh, 1) + ' km/h') + '</span>' +
      '<span>Kurs</span><span>' + (s.kurs < 0 ? '–' : zahl(s.kurs) + '°') + '</span>' +
      (NAVSTATUS[s.status] ? '<span>Status</span><span>' + NAVSTATUS[s.status] + '</span>' : '') +
      '<span>Entfernung</span><span>' + (s.km == null ? '–' : zahl(s.km, 1) + ' km') + '</span>' +
      (s.rufzeichen ? '<span>Rufzeichen</span><span>' + esc(s.rufzeichen) + '</span>' : '') +
      '<span>MMSI</span><span>' + esc(s.mmsi) + '</span>' +
      '<span>Zuletzt</span><span>vor ' + zahl(s.alter_s) + ' s</span></div>');
  }
  function schiffeZeichnen(d){
    var liste = d.schiffe || [], da = {};
    letzteSchiffe = liste;
    var st = d.standort && (d.standort.lat || d.standort.lon) ? d.standort : null;
    if (st) ringeUm(st);
    liste.forEach(function(s){
      da[s.mmsi] = true;
      var icon = L.divIcon({ className: 'ae-flieger', iconSize: [22, 22], iconAnchor: [11, 11],
        html: '<div style="transform:rotate(' + (s.kurs < 0 ? 0 : s.kurs) + 'deg)">' + SCHIFF.replace('FARBE', schiffFarbe(s)) + '</div>' });
      if (!schiffMarker[s.mmsi]) schiffMarker[s.mmsi] = L.marker([s.lat, s.lon], { icon: icon }).addTo(karte)
        .on('click', (function(m){ return function(){ schiffGewaehlt = m; schiffDetail(letzteSchiffe.find(function(x){ return x.mmsi === m; })); }; })(s.mmsi));
      else schiffMarker[s.mmsi].setLatLng([s.lat, s.lon]).setIcon(icon);
      schiffMarker[s.mmsi].bindTooltip(esc(s.name || s.mmsi) + (s.ziel ? ' → ' + esc(s.ziel) : ''), { direction: 'top', offset: [0, -10] });
    });
    Object.keys(schiffMarker).forEach(function(m){ if (!da[m]) { karte.removeLayer(schiffMarker[m]); delete schiffMarker[m]; } });
    var neu = liste.length ? liste.slice(0, 40).map(function(s){
      return '<div class="ae-zeile" data-mmsi="' + esc(s.mmsi) + '"><span style="color:' + schiffFarbe(s) + '">●</span>' +
        '<span class="fl">' + esc(s.name || s.mmsi) + '</span><span class="rt">' + esc([s.art, s.ziel ? '→ ' + s.ziel : ''].filter(Boolean).join(' ')) + '</span>' +
        '<span class="km">' + (s.km == null ? '' : zahl(s.km, 1) + ' km') + '</span></div>';
    }).join('') : '<div class="ae-hinweis">Gerade kein Schiff mit Position in Reichweite.' +
      (d.zuletzt > 0 ? '' : ' Auf dem Main senden Binnenschiffe AIS – ob sie hier ankommen, hängt von Lage und Antenne ab.') + '</div>';
    if (neu !== listeAlt) {
      listeAlt = neu; $('ae-liste').innerHTML = neu;
      document.querySelectorAll('.ae-zeile[data-mmsi]').forEach(function(z){ z.onclick = function(){
        schiffGewaehlt = z.dataset.mmsi; schiffDetail(letzteSchiffe.find(function(x){ return x.mmsi === schiffGewaehlt; }));
        var m = schiffMarker[schiffGewaehlt]; if (m) karte.panTo(m.getLatLng()); }; });
    }
    if (schiffGewaehlt) schiffDetail(liste.find(function(x){ return x.mmsi === schiffGewaehlt; }));
    $('ae-stand').textContent = liste.length + ' Schiffe · ' + (d.quelle === 'peer' ? 'Empfänger im Netz' : 'eigener Empfänger');
  }
  function nachrichtenZeichnen(d){
    var liste = d.nachrichten || [];
    var h = liste.length ? liste.slice(0, 150).map(function(n){
      return '<div class="ae-nachr"><div class="kopf"><b>' + esc(n.flug || n.von || '?') + '</b>' +
        (n.kennzeichen ? '<span>' + esc(n.kennzeichen) + '</span>' : '') +
        (n.label ? '<span>Label ' + esc(n.label) + '</span>' : '') +
        '<span>' + new Date(n.zeit * 1000).toLocaleTimeString('de-DE') + (n.mhz ? ' · ' + zahl(n.mhz, 3) + ' MHz' : '') + '</span></div>' +
        (n.text ? '<pre>' + esc(n.text) + '</pre>' : '') + '</div>';
    }).join('') : '<div class="ae-hinweis">Noch keine Funknachrichten. Flugzeuge funken nicht ununterbrochen – in der Nähe eines Flughafens kommen meist mehrere pro Minute.</div>';
    if (h !== nachrAlt) { nachrAlt = h; $('ae-nachrichten').innerHTML = h; }
    $('ae-stand').textContent = liste.length + ' Nachrichten · ' + (d.quelle === 'peer' ? 'Empfänger im Netz' : 'eigener Empfänger') + ' · nur im Arbeitsspeicher';
  }
  // ── R863: Wettersatelliten ──────────────────────────────────────────────
  var wetterAlt = '';
  function uhr(t){ var d = new Date(t * 1000); return d.toLocaleDateString('de-DE', { weekday: 'short', day: '2-digit', month: '2-digit' }) + ' ' + d.toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' }); }
  function bildRang(n){ n = n.toLowerCase(); return /natural|221|rgb/.test(n) ? 0 : /123|125|ir/.test(n) ? 1 : 2; }
  function wetterZeichnen(d){
    var h = '';
    if (!d.stick) h += '<div class="ae-karte ae-hinweis">Wetterbilder entstehen auf dem Pi mit dem Stick' + (d.node ? '' : '') + ' – diese Seite über dessen Adresse öffnen.</div>';
    else if (!d.satdump) h += '<div class="ae-karte ae-hinweis">SatDump fehlt noch – es wird beim nächsten Deploy oder Update installiert. Den Fortschritt zeigt der Reiter Flugzeuge.</div>';
    else h += '<div class="ae-wetter-kopf"><label><input type="checkbox" id="ae-wauto"' + (d.automatik ? ' checked' : '') + '> Bei Überflügen automatisch aufnehmen</label>' +
      '<span class="ae-hinweis">Der Stick wechselt dafür für einen Überflug auf 137,9 MHz und danach zurück. Antenne: waagerechter Dipol, 2 × 53 cm, Nord-Süd.</span></div>';
    if (d.laeuft) h += '<div class="ae-modus anders">🛰 Aufnahme läuft: <b>' + esc(d.laeuft.sat || '') + '</b> bis ' + esc(new Date((d.laeuft.los || 0) * 1000).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })) + '</div>';
    var plan = d.plan || {}, kommend = plan.ueberfluege || [];
    h += '<div class="ae-karte" style="margin-bottom:12px"><h3>Nächste Überflüge' + (plan.min_el ? ' <span class="ae-hinweis">(ab ' + zahl(plan.min_el) + '° Höhe)</span>' : '') + '</h3>';
    if (plan.fehler) h += '<div class="ae-hinweis ae-notfall">' + esc(plan.fehler) + '</div>';
    h += kommend.length ? '<table class="ae-wetter-tab"><tr><th>Satellit</th><th>Zeit</th><th>Höhe</th><th>Richtung</th></tr>' + kommend.map(function(p){
        return '<tr><td>' + esc(p.sat) + '</td><td>' + uhr(p.aos) + '–' + new Date(p.los * 1000).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' }) + '</td><td>' + zahl(p.max_el) + '°</td><td>' + esc(p.von) + ' → ' + esc(p.nach) + '</td></tr>';
      }).join('') + '</table>' : '<div class="ae-hinweis">' + (d.satdump ? 'Noch kein Plan – der Planer rechnet nach dem Start und dann alle 30 Minuten.' : 'Der Plan entsteht, sobald SatDump installiert ist.') + '</div>';
    h += '</div>';
    var fl = d.ueberfluege || [];
    h += '<div class="ae-karte"><h3>Letzte Überflüge</h3>' + (fl.length ? '<div class="ae-galerie">' + fl.map(function(u){
        var bilder = (u.bilder || []).slice().sort(function(a, b){ return bildRang(a) - bildRang(b); }).slice(0, 4);
        var zustand = u.zustand === 'aufnahme' ? 'Aufnahme läuft …' : u.zustand === 'dekodiert' ? 'wird dekodiert …' :
          bilder.length ? bilder.length + ' Bild(er)' : 'keine Bilder – Signal zu schwach? Antenne und Ausrichtung prüfen';
        return '<div class="ae-ueberflug"><b>' + esc(u.sat || '') + '</b> <span class="ae-hinweis">' + (u.aos ? uhr(u.aos) : esc(u.ordner)) +
          (u.max_el ? ' · ' + zahl(u.max_el) + '°' : '') + (u.von ? ' · ' + esc(u.von) + ' → ' + esc(u.nach) : '') + '</span>' +
          '<div class="ae-hinweis">' + zustand + '</div>' +
          (bilder.length ? '<div class="bilder">' + bilder.map(function(b){
            var url = '/api/v1/funk/wetter/bild/' + encodeURIComponent(u.ordner) + '/' + b.split('/').map(encodeURIComponent).join('/');
            return '<a href="' + url + '" target="_blank" rel="noopener"><img loading="lazy" src="' + url + '" alt="' + esc(b) + '"></a>';
          }).join('') + '</div>' : '') + '</div>';
      }).join('') + '</div>' : '<div class="ae-hinweis">Noch keine Überflüge aufgenommen.</div>') + '</div>';
    if (h !== wetterAlt) {
      wetterAlt = h; $('ae-wetter').innerHTML = h;
      var k = $('ae-wauto');
      if (k) k.onchange = function(){
        fetch('/api/v1/admin/funk/wetter', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ automatik: k.checked }) }).then(function(r){ return r.json(); })
          .then(function(x){ if (x.error) { alert(x.error); k.checked = !k.checked; } })
          .catch(function(){ k.checked = !k.checked; });
      };
    }
    $('ae-stand').textContent = kommend.length ? 'nächster Überflug ' + uhr(kommend[0].aos) : 'Wettersatelliten';
  }

  function funkLaden(){
    if (ansicht === 'flug') { setTimeout(funkLaden, 1000); return; }
    if (ansicht === 'wetter') {
      fetch('/api/v1/funk/wetter', { credentials: 'same-origin' }).then(function(r){ return r.json(); })
        .then(function(d){ if (ansicht === 'wetter') wetterZeichnen(d); })
        .catch(function(){}).then(function(){ setTimeout(funkLaden, 10000); });
      return;
    }
    var art = ansicht;
    fetch('/api/v1/funk/' + art, { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(d){
      if (ansicht !== art) return;
      if (d.status) { funkStatus = Object.assign(funkStatus || {}, d.status); if (d.quelle === 'peer') funkStatus.empfaenger_stick = true; modusZeigen(); }
      if (art === 'schiffe') { if (karte) schiffeZeichnen(d); } else nachrichtenZeichnen(d);
    }).catch(function(){ $('ae-stand').textContent = 'Verbindung unterbrochen …'; })
      .then(function(){ setTimeout(funkLaden, 3000); });
  }

  function laden(){
    if (ansicht !== 'flug' || wg) { setTimeout(laden, 1000); return; } // R859: andere Ansicht · R877: Wiedergabe läuft
    fetch('/api/v1/aeronautik/live', { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(d){
      if (ansicht !== 'flug') return; // inzwischen andere Ansicht
      if (d.quelle === 'keine') {
        $('ae-stand').textContent = 'kein Empfänger';
        $('ae-liste').innerHTML = '<div class="ae-hinweis">Kein Empfänger erreichbar. Mit einem RTL-SDR-Stick (z. B. Terratec T Stick) auf einem Pi das Skript <code>tools/setup-aeronautik.sh</code> aus dem Fundus-Paket mit <code>sudo bash</code> ausführen.</div>';
      } else zeichnen(d);
    }).catch(function(){ $('ae-stand').textContent = 'Verbindung unterbrochen …'; })
      .then(function(){ setTimeout(laden, 1000); });
  }
  // ── R877: Aufzeichnung, Wiedergabe, Flugquartett ─────────────────────────
  var wg = null;  // laufende Wiedergabe
  function zwei(n){ return (n < 10 ? '0' : '') + n; }
  function tagText(t){ return t.slice(6, 8) + '.' + t.slice(4, 6) + '.' + t.slice(0, 4); }
  function tagStart(t){ return new Date(+t.slice(0, 4), +t.slice(4, 6) - 1, +t.slice(6, 8)).getTime() / 1000; }
  function panel(name){
    ['auf', 'wieder', 'quartett'].forEach(function(n){
      var offen = n === name && $('ae-p-' + n).style.display === 'none';
      $('ae-p-' + n).style.display = offen ? '' : 'none';
      $('ae-w-' + n).classList.toggle('aktiv', offen);
    });
  }
  var uebersicht = null;
  var QUELLE = { sbs: 'Echtzeit: SBS-Datenstrom von readsb', json: 'Ersatz: Bild im Sekundentakt (SBS-Port 30003 antwortet nicht)', keine: 'kein Empfänger' };
  function uebersichtLaden(){
    return fetch('/api/v1/aeronautik/aufzeichnung', { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(d){
      uebersicht = d;
      var e = d.einstellungen || {}, f = e.filter || {}, an = e.aktiv || e.spuren;
      $('ae-w-auf').innerHTML = an ? '<span class="rec">●</span> Aufzeichnung läuft' : '⏺ Aufzeichnung';
      if (!$('ae-p-auf').dataset.bearbeitet) {
        $('ae-auf-an').checked = !!e.aktiv; $('ae-auf-spuren').checked = !!e.spuren;
        $('ae-auf-takt').value = String(e.spur_takt); $('ae-auf-tage').value = e.tage_behalten;
        $('ae-auf-luft').checked = !!e.tief_nur_luft; $('ae-f-an').checked = !!f.an;
        $('ae-f-min').value = f.min_meldungen; $('ae-f-hoehe').value = f.max_hoehe; $('ae-f-sprung').value = f.max_sprung;
        $('ae-f-geschw').value = f.max_geschw; $('ae-f-steig').value = f.max_steig; $('ae-f-km').value = f.max_km;
        $('ae-f-werte').style.opacity = f.an ? '' : '.45';
      }
      // R879: Pi ohne Empfänger – Daten kommen vom Pi mit Stick; einstellen nur dort.
      var fern = d.fern ? 'Daten vom Empfänger <b>' + esc(d.empfaenger_node || '?') + '</b> · ' : '';
      $('ae-auf-quelle').innerHTML = fern + (an ? '<b>' + esc(QUELLE[d.quelle] || d.quelle) + '</b> · ' + zahl(d.meldungen_s, 1) + ' Meldungen/s · ' + d.flugzeuge + ' Flugzeuge'
        : (d.stick || d.fern ? 'Aufzeichnung aus.' : 'Aufzeichnen kann nur ein Pi mit eigenem Empfänger – im Netz ist gerade keiner erreichbar.'));
      [].forEach.call($('ae-p-auf').querySelectorAll('input,select,button'), function(el){ el.disabled = !!d.fern; });
      $('ae-q-nachtrag').disabled = !!d.fern || !!(d.nachtrag && d.nachtrag.laeuft);
      var kb = d.ki_bilder || {};
      $('ae-q-bilder').title = kb.offen ? kb.offen + ' KI-Bild(er) in Arbeit' : '';
      if (kb.offen && !$('ae-q-los').disabled) $('ae-q-meldung').textContent = 'KI erzeugt Bilder im Hintergrund: noch ' + kb.offen + ' offen, ' + kb.fertig + ' fertig.' + (kb.fehler ? ' Letzter Fehler: ' + kb.fehler : '');
      else if (kb.fehler && !kb.offen && $('ae-q-meldung').textContent.indexOf('KI erzeugt') === 0) $('ae-q-meldung').textContent = '✗ KI-Bilder: ' + kb.fehler;
      $('ae-q-nachtrag').title = d.fern ? 'Nachtragen nur auf dem Empfänger ' + (d.empfaenger_node || '') : $('ae-q-nachtrag').title;
      $('ae-auf-meldung').textContent = d.fern ? 'Einstellen nur auf dem Empfänger ' + (d.empfaenger_node || '') + ' – über dessen Adresse im Heimnetz.' : $('ae-auf-meldung').textContent;
      var tab = d.tabellen || [];
      $('ae-auf-info').textContent = 'Belegt: ' + zahl(d.belegt_mb, 1) + ' MB · ' + d.spuren.length + ' Tag(e) mit Spuren · Tabellen: ' +
        ['tag', 'woche', 'monat', 'jahr'].map(function(k){ return tab.filter(function(t){ return t.indexOf(k + '-') === 0; }).length + ' ' + { tag: 'Tage', woche: 'Wochen', monat: 'Monate', jahr: 'Jahre' }[k]; }).join(', ') +
        '. Tabellen bleiben dauerhaft, Spuren nur so lange wie eingestellt. „Jede Meldung“ braucht ein Vielfaches an Speicher.';
      var sel = $('ae-wg-tag'), alt = sel.value;
      sel.innerHTML = d.spuren.length ? d.spuren.slice().reverse().map(function(t){ return '<option value="' + t + '">' + tagText(t) + '</option>'; }).join('') : '<option value="">keine Aufzeichnung</option>';
      if (alt && d.spuren.indexOf(alt) >= 0) sel.value = alt;
      return d;
    }).catch(function(){});
  }
  // Während man tippt, nicht vom regelmäßigen Auffrischen überschreiben lassen.
  ['ae-auf-an','ae-auf-spuren','ae-auf-takt','ae-auf-tage','ae-auf-luft','ae-f-an','ae-f-min','ae-f-hoehe','ae-f-sprung','ae-f-geschw','ae-f-steig','ae-f-km'].forEach(function(id){
    $(id).addEventListener('input', function(){ $('ae-p-auf').dataset.bearbeitet = '1'; if (id === 'ae-f-an') $('ae-f-werte').style.opacity = this.checked ? '' : '.45'; });
  });
  $('ae-w-auf').onclick = function(){ panel('auf'); };
  $('ae-w-wieder').onclick = function(){ panel('wieder'); };
  $('ae-w-quartett').onclick = function(){ panel('quartett'); };
  $('ae-auf-speichern').onclick = function(){
    var n = function(id){ return +$(id).value; };
    var e = { aktiv: $('ae-auf-an').checked, spuren: $('ae-auf-spuren').checked, spur_takt: n('ae-auf-takt'), tage_behalten: n('ae-auf-tage') || 30,
      tief_nur_luft: $('ae-auf-luft').checked,
      filter: { an: $('ae-f-an').checked, min_meldungen: n('ae-f-min'), max_hoehe: n('ae-f-hoehe'), max_sprung: n('ae-f-sprung'),
        max_geschw: n('ae-f-geschw'), max_steig: n('ae-f-steig'), max_km: n('ae-f-km') } };
    fetch('/api/v1/admin/aeronautik/aufzeichnung', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(e) })
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(d){ return [r, d]; }); }).then(function(x){
        var r = x[0], d = x[1];
        $('ae-auf-meldung').textContent = r.status === 401 || r.status === 403 ? 'Nur der Eigentümer dieses Nodes kann das ändern.' : d.error ? '✗ ' + d.error : '✓ Gespeichert – gilt sofort';
        if (r.ok) delete $('ae-p-auf').dataset.bearbeitet;
        uebersichtLaden();
      }).catch(function(){ $('ae-auf-meldung').textContent = '✗ Speichern fehlgeschlagen.'; });
  };

  // Wiedergabe: eine Stunde laden, die nächste rechtzeitig nachladen, zwischen
  // den Punkten gleiten, über zeichnen() darstellen wie im Live-Betrieb.
  function wgFenster(tag, abSek){
    var start = Math.max(0, Math.min(1439, Math.floor(abSek / 60)));
    var ende = Math.min(1439, start + 60);
    return { tag: tag, von: zwei(Math.floor(start / 60)) + zwei(start % 60), bis: zwei(Math.floor(ende / 60)) + zwei(ende % 60), ende: ende };
  }
  function wgNachladen(abSek, neu){
    if (!wg || wg.lade) return;
    var f = wgFenster(wg.tag, abSek);
    if (f.von === f.bis) return;
    wg.lade = true;
    fetch('/api/v1/aeronautik/aufzeichnung/spur?tag=' + f.tag + '&von=' + f.von + '&bis=' + f.bis, { credentials: 'same-origin' })
      .then(function(r){ return r.json(); }).then(function(d){
        if (!wg) return;
        if (d.error) { $('ae-wg-hinweis').textContent = d.error; return; }
        if (neu) wg.spuren = {};
        (d.punkte || []).forEach(function(p){
          var a = wg.spuren[p[1]] || (wg.spuren[p[1]] = []);
          if (!a.length || p[0] > a[a.length - 1][0]) a.push(p);
        });
        Object.keys(d.angaben || {}).forEach(function(h){ wg.angaben[h] = d.angaben[h]; });
        wg.standort = d.standort; wg.node = d.node;
        wg.geladenBis = tagStart(wg.tag) + f.ende * 60;
        $('ae-wg-hinweis').textContent = '';
      }).catch(function(){ $('ae-wg-hinweis').textContent = 'Spur nicht ladbar.'; })
      .then(function(){ if (wg) wg.lade = false; });
  }
  function wgBild(){
    if (!wg) return;
    var t = wg.zeit, flieger = [];
    Object.keys(wg.spuren).forEach(function(hex){
      var a = wg.spuren[hex], lo = 0, hi = a.length - 1, i = -1;
      while (lo <= hi) { var m = (lo + hi) >> 1; if (a[m][0] <= t) { i = m; lo = m + 1; } else hi = m - 1; }
      if (i < 0 || t - a[i][0] > 60) return;   // noch nicht da oder schon weg
      var p = a[i], q = a[i + 1], k = 0;
      if (q && q[0] - p[0] <= 60) k = (t - p[0]) / (q[0] - p[0]);
      var w = function(j){ return q && k ? p[j] + (q[j] - p[j]) * k : p[j]; };
      var spur = [];
      for (var j = Math.max(0, i - 40); j <= i; j++) if (t - a[j][0] < 600) spur.push([a[j][2], a[j][3]]);
      var ang = wg.angaben[hex] || {};
      var f = { hex: hex, flug: p[9] || '', lat: w(2), lon: w(3), geschw_kmh: p[5], kurs: p[6], steig_mmin: p[7], km: p[8], spur: spur,
        flugzeug: ang.typ || ang.kennzeichen ? { typ: ang.typ, icao: ang.icao, kennzeichen: ang.kennzeichen } : undefined };
      if (p[4] < 0) f.am_boden = true; else f.hoehe_m = Math.round(w(4));
      flieger.push(f);
    });
    flieger.sort(function(a, b){ return (a.km == null ? 1e9 : a.km) - (b.km == null ? 1e9 : b.km); });
    zeichnen({ quelle: 'lokal', standort: wg.standort, node: wg.node, zeit: t, flugzeuge: flieger });
    var d = new Date(t * 1000), uhr = zwei(d.getHours()) + ':' + zwei(d.getMinutes()) + ':' + zwei(d.getSeconds());
    $('ae-wg-zeit').textContent = uhr;
    $('ae-stand').textContent = 'Wiedergabe ' + tagText(wg.tag) + ' ' + uhr + (wg.laeuft ? ' · ' + wg.tempo + '×' : ' · angehalten') + ' · ' + flieger.length + ' Flugzeuge';
    $('ae-wg-regler').value = Math.floor((t - tagStart(wg.tag)) / 60);
  }
  function wgStarten(tag, minute){
    if (!tag) return;
    wg = { tag: tag, zeit: tagStart(tag) + minute * 60, spuren: {}, angaben: {}, laeuft: false, tempo: +$('ae-wg-tempo').value, geladenBis: 0 };
    wgNachladen(minute * 60 - 120, true);
  }
  $('ae-wg-tag').onchange = function(){ wgStarten(this.value, +$('ae-wg-regler').value); };
  $('ae-wg-regler').oninput = function(){
    var tag = $('ae-wg-tag').value;
    if (!tag) return;
    if (!wg || wg.tag !== tag) { wgStarten(tag, +this.value); return; }
    wg.zeit = tagStart(tag) + this.value * 60;
    var sek = wg.zeit - tagStart(tag);
    if (wg.zeit > wg.geladenBis || !Object.keys(wg.spuren).length) wgNachladen(sek - 120, true);
    wgBild();
  };
  $('ae-wg-tempo').onchange = function(){ if (wg) wg.tempo = +this.value; };
  $('ae-wg-spiel').onclick = function(){
    if (!wg) wgStarten($('ae-wg-tag').value, +$('ae-wg-regler').value);
    if (!wg) return;
    wg.laeuft = !wg.laeuft;
    this.textContent = wg.laeuft ? '⏸ Anhalten' : '▶ Abspielen';
  };
  $('ae-wg-live').onclick = function(){
    wg = null; $('ae-wg-spiel').textContent = '▶ Abspielen';
    $('ae-stand').textContent = 'zurück zu Live …';
  };
  setInterval(function(){
    if (!wg) return;
    if (wg.laeuft) {
      wg.zeit += 0.25 * wg.tempo;
      var tagEnde = tagStart(wg.tag) + 86400;
      if (wg.zeit >= tagEnde) { wg.zeit = tagEnde - 1; wg.laeuft = false; $('ae-wg-spiel').textContent = '▶ Abspielen'; }
      if (wg.geladenBis && wg.zeit > wg.geladenBis - 300) wgNachladen(wg.geladenBis - tagStart(wg.tag), false);
    }
    wgBild();
  }, 250);

  // Flugquartett: Daten vom Node, PDF im Browser (jsPDF erst bei Bedarf laden).
  (function(){ var d = new Date(); $('ae-q-datum').value = d.getFullYear() + '-' + zwei(d.getMonth() + 1) + '-' + zwei(d.getDate()); })();
  function skript(src){
    return new Promise(function(ok, fehler){
      if (document.querySelector('script[data-src="' + src + '"]')) return ok();
      var s = document.createElement('script'); s.src = src + '?v=]==] .. render.rev() .. [==['; s.dataset.src = src;
      s.onload = ok; s.onerror = function(){ fehler(new Error('Skript nicht ladbar: ' + src)); };
      document.head.appendChild(s);
    });
  }
  // R880: Quartettwerte für den gewählten Zeitraum aus Aufgezeichnetem nachtragen.
  function nachtragZeigen(n){
    if (!n) return;
    if (n.laeuft) $('ae-q-meldung').textContent = 'Trage nach (' + n.titel + '): ' + n.fertig + ' von ' + n.tage + ' Tagen · ' + zahl(n.punkte) + ' Spurpunkte …';
    else if (n.ende && $('ae-q-meldung').dataset.nachtrag) {
      delete $('ae-q-meldung').dataset.nachtrag;
      $('ae-q-meldung').textContent = '✓ Nachgetragen (' + n.titel + '): ' + (n.tage - n.ohne) + ' von ' + n.tage + ' Tagen mit Daten, ' + zahl(n.punkte) + ' Spurpunkte' +
        (n.bilanzen ? ', ' + n.bilanzen + ' Tag(e) aus älteren Tagesbilanzen' : '') +
        (n.ohne ? '. ' + n.ohne + ' Tag(e) ohne Aufzeichnung – Spuren werden nur so lange aufbewahrt wie eingestellt.' : '.');
      $('ae-q-nachtrag').disabled = false;
    }
  }
  $('ae-q-nachtrag').onclick = function(){
    var datum = $('ae-q-datum').value.replace(/-/g, '');
    if (!/^\d{8}$/.test(datum)) { $('ae-q-meldung').textContent = 'Bitte ein Datum wählen.'; return; }
    var knopf = this; knopf.disabled = true;
    fetch('/api/v1/admin/aeronautik/nachtragen', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ zeitraum: $('ae-q-zeitraum').value, datum: datum }) })
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(d){ return [r, d]; }); }).then(function(x){
        var r = x[0], d = x[1];
        if (r.status === 401 || r.status === 403) throw new Error('Nur der Eigentümer dieses Nodes kann nachtragen.');
        if (d.error) throw new Error(d.error);
        $('ae-q-meldung').dataset.nachtrag = '1';
        $('ae-q-meldung').textContent = 'Nachtrag gestartet: ' + d.tage + ' Tag(e) (' + d.titel + ') …';
        var warten = setInterval(function(){
          uebersichtLaden().then(function(u){ if (u) nachtragZeigen(u.nachtrag); if (!u || !u.nachtrag || !u.nachtrag.laeuft) clearInterval(warten); });
        }, 2000);
      }).catch(function(e){ $('ae-q-meldung').textContent = '✗ ' + e.message; knopf.disabled = false; });
  };

  $('ae-q-los').onclick = function(){
    var knopf = this, datum = $('ae-q-datum').value.replace(/-/g, ''), zeitraum = $('ae-q-zeitraum').value, format = $('ae-q-format').value;
    var anzahl = $('ae-q-anzahl').value, groesse = $('ae-q-groesse').value;
    if (!/^\d{8}$/.test(datum)) { $('ae-q-meldung').textContent = 'Bitte ein Datum wählen.'; return; }
    knopf.disabled = true; $('ae-q-meldung').textContent = 'Werte Rekorde aus und schlage Flugzeugtypen nach …';
    var bilder = $('ae-q-bilder').value, bekannt = $('ae-q-bekannt').checked;
    if (bilder !== 'keine') $('ae-q-meldung').textContent = 'Werte Rekorde aus, schlage Typen nach und suche Bilder – beim ersten Mal bis zu einer Minute …';
    fetch('/api/v1/aeronautik/quartett?zeitraum=' + zeitraum + '&datum=' + datum + '&bilder=' + bilder + (bekannt ? '&bekannt=1' : '') + '&anzahl=' + anzahl, { credentials: 'same-origin' })
      .then(function(r){ return r.json(); }).then(function(d){
        if (d.error) throw new Error(d.error);
        if (!d.karten || !d.karten.length) throw new Error(d.hinweis || 'Keine Daten für diesen Zeitraum.');
        $('ae-q-meldung').textContent = 'Erzeuge PDF …';
        return skript('/static/jspdf.umd.min.js').then(function(){ return skript('/static/quartett.js'); }).then(function(){
          return FundusQuartett.erzeugen(d, format, function(n, gesamt){ $('ae-q-meldung').textContent = 'Erzeuge Karte ' + n + ' von ' + gesamt + ' …'; }, { groesse: groesse });
        }).then(function(doc){
          var name = 'FlightQuad' + (d.anzahl || anzahl) + '-' + d.code + '-' + groesse;
          if (format === 'druckerei' || format === 'a4_druck') return druckdatei(doc, d, name + (format === 'a4_druck' ? '-A4' : ''), groesse, format);
          doc.save(name + (format === 'a4' ? '-A4' : '') + '.pdf');
          return '';
        }).then(function(zusatz){
          var soll = +(d.anzahl || anzahl);
          $('ae-q-meldung').textContent = (zusatz || '') + '✓ Titelkarte und ' + d.karten.length + ' Spielkarten aus ' + d.flugzeuge + ' Flugzeugen (' + d.titel + ')' +
            (d.karten.length < soll ? ' – für ' + soll + ' Karten waren nicht genug Flugzeuge mit vollständigen Werten aufgezeichnet.' : '.') +
            (d.fern ? ' Daten vom Empfänger ' + (d.empfaenger_node || '') + '.' : '') +
            (d.fotos_fehlend ? ' ' + d.fotos_fehlend + ' Karte(n) noch ohne Foto – der Node sucht im Hintergrund weiter; später erneut erzeugen.' : '') +
            (d.ki_ausstehend ? ' ' + d.ki_ausstehend + ' KI-Bild(er) werden im Hintergrund erzeugt' + (d.ki_hier ? ' auf ' + d.ki_hier : '') + ' (je etwa 20 s) – danach erneut erzeugen.' : '') +
            (d.ki_hinweis ? ' KI-Bilder nicht möglich: ' + d.ki_hinweis : '') +
            (d.ausgeblendet ? ' ' + d.ausgeblendet + ' Flugzeug(e) unbekannten Typs ausgeblendet.' : '');
        });
      }).catch(function(e){ $('ae-q-meldung').textContent = '✗ ' + e.message; })
      .then(function(){ knopf.disabled = false; });
  };
  // ── R889: Druckdatei – die Seite liefert Einzelseiten mit Beschnitt, der Node
  // wandelt mit Ghostscript in PDF/X-1a (CMYK, Farbdeckung ≤ 200 %, Schriften
  // als Pfade). Asynchron: hochladen, Stand abfragen, fertige Datei holen.
  function druckdatei(doc, d, name, groesse, format){
    var seiten = doc.getNumberOfPages();
    $('ae-q-meldung').textContent = 'Lade die Seiten zur Umwandlung hoch …';
    var basis = '/api/v1/admin/aeronautik/druckdatei';
    return fetch(basis + '?titel=' + encodeURIComponent('FLIGHT QUAD ' + (d.anzahl || 64) + ' ' + d.code), { method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/pdf' }, body: doc.output('blob') })
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(x){ return [r, x]; }); })
      .then(function(x){
        var r = x[0], a = x[1];
        if (r.status === 401 || r.status === 403) throw new Error('Die Druckdatei kann nur der Eigentümer dieses Nodes erzeugen.');
        if (!a.id) throw new Error(a.error || 'Umwandlung nicht gestartet (' + r.status + ').');
        var start = Date.now();
        return new Promise(function(fertig, fehler){
          (function fragen(){
            fetch(basis + '/' + a.id, { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(st){
              if (st.status === 'fertig') { $('ae-q-abbrechen').style.display = 'none'; return fertig(a.id); }
              if (st.status === 'fehler' || st.status === 'abgebrochen' || st.error) { $('ae-q-abbrechen').style.display = 'none'; return fehler(new Error(st.fehler || st.error)); }
              $('ae-q-meldung').textContent = 'Wandle für die Druckerei um (CMYK, PDF/X-1a): ' +
                (st.seiten ? 'Seite ' + st.seite + ' von ' + st.seiten : 'beginne') + ' · ' + (st.sekunden || 0) + ' s';
              $('ae-q-abbrechen').style.display = ''; $('ae-q-abbrechen').dataset.id = a.id;
              setTimeout(fragen, 2000);
            }).catch(function(){ setTimeout(fragen, 4000); });
          })();
        });
      })
      .then(function(id){ return fetch(basis + '/' + id + '?datei=1', { credentials: 'same-origin' }); })
      .then(function(r){ if (!r.ok) throw new Error('Druckdatei konnte nicht geladen werden (' + r.status + ').'); return r.blob(); })
      .then(function(blob){
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob); a.download = name + '-Druckerei-PDFX1a.pdf';
        document.body.appendChild(a); a.click(); a.remove();
        setTimeout(function(){ URL.revokeObjectURL(a.href); }, 60000);
        var m = groesse.split('x').map(Number);
        if (format === 'a4_druck') return 'Druckdatei: ' + seiten + ' Seiten DIN A4 (Karten ' + m[0] + ' × ' + m[1] + ' mm), PDF/X-1a, CMYK. ';
        return 'Druckdatei: ' + seiten + ' Seiten ' + (m[0] + 6) + ' × ' + (m[1] + 6) + ' mm (' + m[0] + ' × ' + m[1] + ' + 3 mm Beschnitt), PDF/X-1a, CMYK. ';
      });
  }

  // ── R887: Typen des Zeitraums mit Foto und KI-Bild; misslungene KI-Bilder löschen ──
  var typen = [];
  function typenLaden(){
    var datum = $('ae-q-datum').value.replace(/-/g, '');
    $('ae-q-typen-info').textContent = 'lade …';
    fetch('/api/v1/aeronautik/quartett/typen?zeitraum=' + $('ae-q-zeitraum').value + '&datum=' + datum, { credentials: 'same-origin' })
      .then(function(r){ return r.json(); }).then(function(d){
        if (d.error) throw new Error(d.error);
        typen = d.typen || [];
        $('ae-q-typen-info').textContent = d.titel + ': ' + typen.length + ' Typen, ' + d.flugzeuge + ' Flugzeuge' + (d.unbekannt ? ', ' + d.unbekannt + ' ohne Typ' : '') + (d.fern ? ' · vom Empfänger ' + (d.empfaenger_node || '') : '');
        typenZeichnen();
      }).catch(function(e){ $('ae-q-typen-info').textContent = '✗ ' + e.message; });
  }
  function bildZelle(b, art){
    if (!b) return '<div class="bild">' + (art === 'ki' ? 'kein KI-Bild' : 'kein Foto') + '</div>';
    return '<div class="bild"><img loading="lazy" src="' + esc(b.url) + '" alt="" title="' + esc(art === 'ki' ? 'KI-Bild' + (b.ort ? ' (auf ' + b.ort + ')' : '') + (b.beschreibung ? ': ' + b.beschreibung : '') : (b.credit || 'Foto')) + '" data-gross="' + esc(b.url) + '"></div>';
  }
  function typenZeichnen(){
    var such = $('ae-q-typen-suche').value.trim().toLowerCase(), filter = $('ae-q-typen-filter').value;
    var liste = typen.filter(function(t){
      if (such && (t.name + ' ' + t.icao).toLowerCase().indexOf(such) < 0) return false;
      if (filter === 'ki') return !!t.ki;
      if (filter === 'ohnefoto') return !t.foto;
      if (filter === 'ohne') return !t.foto && !t.ki;
      return true;
    });
    $('ae-q-typen').innerHTML = liste.length ? liste.map(function(t, i){
      return '<div class="ae-typ" data-i="' + typen.indexOf(t) + '">' + bildZelle(t.foto, 'foto') + bildZelle(t.ki, 'ki') +
        '<div><b>' + esc(t.name) + '</b> <span class="klein">' + esc(t.icao || '') + '</span><div class="klein">' + t.anzahl + ' Flugzeug(e)' +
        (t.ki_offen ? ' · KI malt gerade …' : '') + '</div></div>' +
        '<div class="tasten"><button type="button" class="btn-sm" data-aktion="foto">Anderes Foto</button>' + (t.ki ? '<button type="button" class="btn-sm" data-aktion="loeschen">KI-Bild löschen</button>' : '') +
        '<button type="button" class="btn-sm" data-aktion="neu"' + (t.ki_offen ? ' disabled' : '') + '>' + (t.ki ? 'Neu malen' : 'KI-Bild malen') + '</button></div></div>';
    }).join('') : '<div class="ae-hinweis" style="padding:12px">Keine passenden Typen.</div>';
  }
  $('ae-q-typen-auf').onclick = function(){
    var auf = $('ae-q-typen').style.display === 'none';
    $('ae-q-typen').style.display = auf ? '' : 'none';
    $('ae-q-typen-leiste').style.display = auf ? 'inline-flex' : 'none';
    this.textContent = auf ? 'Typenliste schließen' : 'Typen & Bilder durchsehen';
    if (auf) typenLaden();
  };
  $('ae-q-abbrechen').onclick = function(){
    var id = this.dataset.id; if (!id) return;
    fetch('/api/v1/admin/aeronautik/druckdatei/' + id + '/abbrechen', { method: 'POST', credentials: 'same-origin' });
  };
  // R891: anderes Foto aus Wikipedia/Wikimedia Commons wählen – nur frei lizenziert.
  function kandidatenOeffnen(zeile, t, text){
    var alt = zeile.querySelector('.ae-kand'); if (alt) alt.remove();
    var feld = document.createElement('div'); feld.className = 'ae-kand';
    feld.innerHTML = '<div style="display:flex;gap:6px;flex-wrap:wrap;align-items:center"><input type="search" value="' + esc(text) + '" style="padding:6px 8px;border-radius:8px;font-size:14px;flex:1;min-width:160px">' +
      '<button type="button" class="btn-sm" data-aktion="kfsuchen">Suchen</button><button type="button" class="btn-sm" data-aktion="keins">Kein Foto</button></div>' +
      '<div class="ae-hinweis" style="margin-top:6px">Suche bei Wikipedia und Wikimedia Commons …</div><div class="raster"></div>';
    zeile.appendChild(feld);
    fetch('/api/v1/aeronautik/typbild/kandidaten?suche=' + encodeURIComponent(t.suche) + '&text=' + encodeURIComponent(text), { credentials: 'same-origin' })
      .then(function(r){ return r.json(); }).then(function(d){
        if (d.error) throw new Error(d.error);
        var l = d.kandidaten || [];
        feld.querySelector('.ae-hinweis').textContent = l.length ? l.length + ' frei lizenzierte Bilder – zum Übernehmen anklicken. Urheber und Lizenz kommen auf die Karte.' : 'Nichts Freies gefunden – anderen Suchbegriff probieren, etwa mit Baureihe oder Airline.';
        feld.querySelector('.raster').innerHTML = l.map(function(k){
          return '<figure data-datei="' + esc(k.datei) + '" title="' + esc(k.datei + ' · ' + (k.autor || '') + ' · ' + k.lizenz) + '"><img loading="lazy" src="' + esc(k.thumb) + '" alt=""><figcaption>' + esc(k.lizenz + (k.autor ? ' · ' + k.autor : '')) + '</figcaption></figure>';
        }).join('');
      }).catch(function(e){ feld.querySelector('.ae-hinweis').textContent = '✗ ' + e.message; });
  }
  function fotoWaehlen(zeile, wahl){
    var t = typen[+zeile.dataset.i];
    var hinweis = zeile.querySelector('.ae-kand .ae-hinweis');
    if (hinweis) hinweis.textContent = wahl.keins ? 'Sperre …' : 'Übernehme das Bild …';
    fetch('/api/v1/admin/aeronautik/typbild/waehlen', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ suche: t.suche, datei: wahl.datei || '', keins: !!wahl.keins }) })
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(d){ return [r, d]; }); }).then(function(x){
        var r = x[0], d = x[1];
        if (r.status === 401 || r.status === 403) throw new Error('Nur der Eigentümer dieses Nodes kann das.');
        if (d.error) throw new Error(d.error);
        t.foto = d.foto && d.foto.datei ? { url: '/api/v1/aeronautik/typbild/' + d.foto.datei + '?v=' + Date.now(), credit: d.foto.credit } : null;
        typenZeichnen();
        $('ae-q-typen-info').textContent = wahl.keins ? '✓ „' + t.name + '“: kein Foto mehr.' : '✓ Neues Foto für „' + t.name + '“ übernommen – gilt ab dem nächsten Quartett.';
      }).catch(function(e){ if (hinweis) hinweis.textContent = '✗ ' + e.message; else $('ae-q-typen-info').textContent = '✗ ' + e.message; });
  }
  $('ae-q-typen-suche').oninput = typenZeichnen;
  $('ae-q-typen-filter').onchange = typenZeichnen;
  $('ae-q-typen').onclick = function(e){
    var img = e.target.closest('img[data-gross]');
    if (img) { window.open(img.dataset.gross, '_blank', 'noopener'); return; }
    var bild = e.target.closest('figure[data-datei]');
    if (bild) { fotoWaehlen(bild.closest('.ae-typ'), { datei: bild.dataset.datei }); return; }
    var knopf = e.target.closest('button[data-aktion]');
    if (!knopf) return;
    var t = typen[+knopf.closest('.ae-typ').dataset.i];
    if (knopf.dataset.aktion === 'foto') { kandidatenOeffnen(knopf.closest('.ae-typ'), t, t.suche); return; }
    if (knopf.dataset.aktion === 'kfsuchen') { kandidatenOeffnen(knopf.closest('.ae-typ'), t, knopf.closest('.ae-kand').querySelector('input').value); return; }
    if (knopf.dataset.aktion === 'keins') { if (confirm('Für „' + t.name + '“ kein Foto verwenden? Dann gilt KI-Bild oder Silhouette.')) fotoWaehlen(knopf.closest('.ae-typ'), { keins: true }); return; }
    if (knopf.dataset.aktion === 'loeschen' && !confirm('KI-Bild für „' + t.name + '“ löschen?')) return;
    knopf.disabled = true;
    fetch('/api/v1/admin/aeronautik/kibild', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ suche: t.suche, aktion: knopf.dataset.aktion, hersteller: t.hersteller, typ: t.typ, icao: t.icao }) })
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(d){ return [r, d]; }); }).then(function(x){
        var r = x[0], d = x[1];
        if (r.status === 401 || r.status === 403) throw new Error('Nur der Eigentümer dieses Nodes kann das.');
        if (d.error) throw new Error(d.error);
        if (knopf.dataset.aktion === 'loeschen') t.ki = null; else { t.ki = null; t.ki_offen = true; }
        typenZeichnen();
        $('ae-q-typen-info').textContent = knopf.dataset.aktion === 'loeschen' ? '✓ KI-Bild für „' + t.name + '“ gelöscht.' : '✓ „' + t.name + '“ wird neu gemalt (etwa 20 s) – Liste später neu öffnen.';
      }).catch(function(e){ knopf.disabled = false; $('ae-q-typen-info').textContent = '✗ ' + e.message; });
  };

  uebersichtLaden();
  setInterval(function(){ if (ansicht === 'flug') uebersichtLaden(); }, 60000);

  starten();
  funkStatusLaden();
  funkLaden();
  if (ansicht !== 'flug') setTimeout(function(){ ansichtSetzen(ansicht); }, 0);
})();
</script>
]==])
    render.footer()
end

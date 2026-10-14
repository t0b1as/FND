-- pages/drive.lua
-- Drive: Transportdienst im Fundus-Netz (R635).
-- Jeder kann Kunde UND Fahrer sein. Kunden stellen Fahrtgesuche ein (öffentlich
-- oder gezielt an einen Fahrer von der Karte), Fahrer nehmen sie an.
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("Drive", "drive")

    -- Karte: Leaflet von einem CDN. Lädt es nicht (kein Internet), bleibt die
    -- Fahrerliste – die trägt die Funktion, die Karte ist Zugabe.
    ngx.print([==[
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/leaflet/1.9.4/leaflet.min.css">
<script src="https://cdnjs.cloudflare.com/ajax/libs/leaflet/1.9.4/leaflet.min.js" defer></script>
<div class="tab-nav" id="drive-tabs">
  <button class="tab-btn active" onclick="driveTab('neu')">Fahrt anfragen</button>
  <button class="tab-btn"        onclick="driveTab('gesuche')">Fahrtgesuche</button>
  <button class="tab-btn"        onclick="driveTab('fahrer')">Fahrer finden</button>
  <button class="tab-btn"        onclick="driveTab('profil')">Mein Fahrerprofil</button>
</div>

<!-- ── Fahrt anfragen ─────────────────────────────────────────────────── -->
<section id="tab-neu" class="drive-tab">
  <div class="set-card">
    <div class="set-head"><h3>Strecke</h3></div>
    <div class="set-body">
      <p class="meta">Start, beliebig viele Zwischenhalte und Ziel. Adresse oder
        Postleitzahl eingeben – die Strecke wird daraus berechnet.</p>
      <div id="stops"></div>
      <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:8px">
        <button class="btn btn-outline" style="width:auto" onclick="addStop()">+ Zwischenhalt</button>
        <button class="btn" style="width:auto" onclick="berechneRoute()">Strecke berechnen</button>
      </div>
      <div id="route-info" class="route-info"></div>
    </div>
  </div>

  <div class="set-card">
    <div class="set-head"><h3>Was soll transportiert werden?</h3></div>
    <div class="set-body">
      <div class="deliv-pick">
        <label class="deliv-opt"><input type="radio" name="dkind" value="person" checked onchange="kindChanged()"> Personen</label>
        <label class="deliv-opt"><input type="radio" name="dkind" value="goods" onchange="kindChanged()"> Ware</label>
      </div>
      <div id="person-felder">
        <div class="set-grid">
          <label>Personen <input type="number" id="d-seats" min="1" max="8" value="1"></label>
        </div>
      </div>
      <div id="goods-felder" style="display:none">
        <div class="set-grid">
          <label>Länge (cm) <input type="number" id="g-len" min="0" step="1"></label>
          <label>Breite (cm) <input type="number" id="g-wid" min="0" step="1"></label>
          <label>Höhe (cm) <input type="number" id="g-hei" min="0" step="1"></label>
          <label>Gewicht (kg) <input type="number" id="g-kg" min="0" step="0.1"></label>
        </div>
        <label class="meta" style="display:block;margin-top:8px">Was genau?
          <input type="text" id="g-desc" placeholder="z.B. Waschmaschine, auf Palette" maxlength="120"></label>
      </div>
      <div class="set-grid" style="margin-top:10px">
        <label>Wunschzeit <input type="text" id="d-when" placeholder="z.B. Samstag vormittags" maxlength="80"></label>
      </div>
      <label class="meta" style="display:block;margin-top:8px">Hinweis für den Fahrer
        <input type="text" id="d-note" placeholder="optional" maxlength="200"></label>

      <div class="deliv-pick" style="margin-top:12px">
        <label class="deliv-opt"><input type="radio" name="dtarget" value="" checked> Öffentlich ausschreiben</label>
        <label class="deliv-opt"><input type="radio" name="dtarget" value="direct"> Gezielt an einen Fahrer</label>
      </div>
      <div id="target-wahl" style="display:none;margin-top:6px">
        <select id="d-driver" class="set-search" style="width:100%"></select>
      </div>

      <div style="display:flex;gap:8px;margin-top:14px">
        <button class="btn" style="width:auto" onclick="gesuchSenden()">Fahrtgesuch einstellen</button>
        <span id="neu-status" class="meta"></span>
      </div>
    </div>
  </div>
</section>

<!-- ── Fahrtgesuche ───────────────────────────────────────────────────── -->
<section id="tab-gesuche" class="drive-tab" style="display:none">
  <div class="set-card">
    <div class="set-head"><h3>Offene Fahrtgesuche</h3>
      <button class="btn-sm" onclick="ladeGesuche()">↻</button></div>
    <div class="set-body"><div id="gesuch-liste"><div class="empty-hint" data-empty="listings">Lade …</div></div></div>
  </div>
</section>

<!-- ── Fahrer finden ──────────────────────────────────────────────────── -->
<section id="tab-fahrer" class="drive-tab" style="display:none">
  <div class="set-card">
    <div class="set-head"><h3>Fahrer in deiner Nähe</h3>
      <button class="btn-sm" onclick="ladeFahrer()">↻</button></div>
    <div class="set-body">
      <div id="karte" class="drive-map"></div>
      <div id="fahrer-liste"></div>
    </div>
  </div>
</section>

<!-- ── Fahrerprofil ───────────────────────────────────────────────────── -->
<section id="tab-profil" class="drive-tab" style="display:none">
  <div class="set-card">
    <div class="set-head"><h3>Mein Fahrerprofil</h3></div>
    <div class="set-body">
      <p class="meta">Du legst deine Preise selbst fest. Die Anfahrt kannst du als
        Pauschale, pro Kilometer oder beides berechnen – was du nicht willst, bleibt auf 0.</p>
      <label class="set-check"><input type="checkbox" id="p-active"> Ich nehme derzeit Aufträge an</label>
      <div class="set-grid">
        <label>Fahrtkosten je km (FND) <input type="number" id="p-perkm" min="0" step="0.01"></label>
        <label>An-/Abfahrtspauschale (FND) <input type="number" id="p-flat" min="0" step="0.01"></label>
        <label>An-/Abfahrt je km (FND) <input type="number" id="p-appkm" min="0" step="0.01"></label>
        <label>Standort (PLZ) <input type="text" id="p-plz" maxlength="5" placeholder="63674"></label>
        <label>oder genau orten
          <button class="btn btn-outline" style="width:auto" onclick="profilStandort()">◎ Meinen Standort</button></label>
        <label>Einsatzradius (km) <input type="number" id="p-radius" min="0" step="1" value="50"></label>
        <label>Fahrzeug <input type="text" id="p-vehicle" maxlength="60" placeholder="z.B. Kombi, Anhänger 750 kg"></label>
        <label>Sitzplätze <input type="number" id="p-seats" min="0" max="8" value="4"></label>
      </div>
      <label class="set-check" style="margin-top:8px"><input type="checkbox" id="p-goods" onchange="cargoToggle()"> Ich transportiere auch Ware</label>
      <div id="cargo-felder" style="display:none">
        <div class="set-grid">
          <label>max. Länge (cm) <input type="number" id="c-len" min="0"></label>
          <label>max. Breite (cm) <input type="number" id="c-wid" min="0"></label>
          <label>max. Höhe (cm) <input type="number" id="c-hei" min="0"></label>
          <label>max. Gewicht (kg) <input type="number" id="c-kg" min="0"></label>
        </div>
      </div>
      <label class="meta" style="display:block;margin-top:8px">Hinweis für Kunden
        <input type="text" id="p-note" maxlength="200" placeholder="optional"></label>
      <div style="display:flex;gap:8px;margin-top:14px">
        <button class="btn" style="width:auto" onclick="profilSpeichern()">Speichern</button>
        <span id="profil-status" class="meta"></span>
      </div>
    </div>
  </div>
</section>
]==])

    ngx.print([==[
<script>
// ── Zustand ───────────────────────────────────────────────────────────────
let _stops = [{label:"",lat:0,lon:0},{label:"",lat:0,lon:0}];
let _route = null;
let _karte = null, _marker = [];
let _letzteGesuche = [];

function driveTab(name){
  document.querySelectorAll('.drive-tab').forEach(function(s){ s.style.display='none'; });
  document.querySelectorAll('#drive-tabs .tab-btn').forEach(function(b){ b.classList.remove('active'); });
  const el = document.getElementById('tab-'+name); if (el) el.style.display='';
  const idx = {neu:0, gesuche:1, fahrer:2, profil:3}[name] || 0;
  document.querySelectorAll('#drive-tabs .tab-btn')[idx].classList.add('active');
  if (name === 'gesuche') ladeGesuche();
  if (name === 'fahrer')  ladeFahrer();
  if (name === 'profil')  ladeProfil();
}

// ── Halte ─────────────────────────────────────────────────────────────────
function zeichneStops(){
  const box = document.getElementById('stops');
  box.innerHTML = _stops.map(function(s, i){
    const titel = i === 0 ? 'Start' : (i === _stops.length-1 ? 'Ziel' : 'Halt ' + i);
    const weg = (_stops.length > 2 && i > 0 && i < _stops.length-1)
      ? '<button class="btn-sm" onclick="delStop('+i+')" title="Halt entfernen">✕</button>' : '';
    // Ortung (R636): Die wenigsten kennen die Postleitzahl ihres aktuellen
    // Standorts – und das Handy weiß sie auf ein paar Meter genau.
    const ort = '<button class="btn-sm" onclick="standortFuer('+i+')" title="Meinen Standort verwenden">◎</button>';
    return '<div class="stop-row"><span class="stop-label">'+titel+'</span>' +
           '<input type="text" class="stop-input" value="'+escAttr(s.label)+'" ' +
           'placeholder="Adresse oder PLZ" oninput="setStop('+i+',this.value)">' + ort + weg + '</div>';
  }).join('');
}
function escAttr(v){ return String(v||'').replace(/"/g,'&quot;').replace(/</g,'&lt;'); }
function setStop(i,v){ _stops[i].label = v; _stops[i].lat = 0; _stops[i].lon = 0; _route = null; }
function addStop(){ _stops.splice(_stops.length-1, 0, {label:"",lat:0,lon:0}); zeichneStops(); }
function delStop(i){ _stops.splice(i,1); zeichneStops(); }

// Adresse → Koordinaten. Erst die PLZ-Tabelle des Nodes (offline, schnell),
// sonst der Suchdienst von OpenStreetMap.
async function geocode(text){
  const s = String(text||'').trim();
  if (!s) return null;
  const plz = s.match(/\b(\d{5})\b/);
  if (plz) {
    try {
      const r = await fetch('/api/v1/grid/plz/' + plz[1]);
      if (r.ok) { const d = await r.json(); if (d.lat && d.lon) return {lat:d.lat, lon:d.lon}; }
    } catch(e) {}
  }
  try {
    const u = 'https://nominatim.openstreetmap.org/search?format=json&limit=1&countrycodes=de&q=' +
              encodeURIComponent(s);
    const r = await fetch(u, {headers:{'Accept':'application/json'}});
    if (r.ok) { const a = await r.json(); if (a[0]) return {lat:parseFloat(a[0].lat), lon:parseFloat(a[0].lon)}; }
  } catch(e) {}
  return null;
}

// ── Ortung (R636) ─────────────────────────────────────────────────────────
// Fragt den Browser nach dem Standort. Das Gerät fragt den Nutzer um Erlaubnis;
// ohne sie passiert nichts. Die Koordinaten bleiben auf dem Gerät, bis ein
// Gesuch abgeschickt wird.
function holeStandort(){
  return new Promise(function(erfuellt, abgelehnt){
    if (!navigator.geolocation) { abgelehnt(new Error('Dieses Gerät kennt keine Ortung.')); return; }
    // R637: Browser geben den Standort NUR in einem sicheren Kontext frei –
    // also über https oder localhost. Über http://10.10.11.x wird die Anfrage
    // stillschweigend verweigert: kein Popup, keine Fehlermeldung, nichts
    // passiert. Genau das war mobil zu sehen. Deshalb vorher prüfen und es
    // deutlich sagen, statt den Nutzer rätseln zu lassen.
    if (window.isSecureContext === false) {
      abgelehnt(new Error('Ortung braucht eine gesicherte Verbindung. Diese Seite läuft über ' +
        location.protocol + '//' + location.host + '. Bitte über https öffnen, ' +
        'dann fragt das Gerät nach der Erlaubnis.'));
      return;
    }
    navigator.geolocation.getCurrentPosition(
      function(p){ erfuellt({lat: p.coords.latitude, lon: p.coords.longitude, genauigkeit: p.coords.accuracy}); },
      function(e){
        const texte = {1:'Ortung wurde abgelehnt. Bitte im Browser erlauben.',
                       2:'Standort nicht ermittelbar.', 3:'Ortung hat zu lange gedauert.'};
        abgelehnt(new Error(texte[e.code] || e.message));
      },
      {enableHighAccuracy: true, timeout: 15000, maximumAge: 60000});
  });
}

// Koordinaten → lesbare Adresse. Klappt das nicht, bleiben die Koordinaten –
// für die Routenberechnung genügen sie ohnehin.
async function adresseZu(lat, lon){
  try {
    const u = 'https://nominatim.openstreetmap.org/reverse?format=json&zoom=18&lat=' + lat + '&lon=' + lon;
    const r = await fetch(u, {headers:{'Accept':'application/json'}});
    if (r.ok) {
      const d = await r.json();
      const a = d.address || {};
      const teile = [a.road, a.house_number, a.postcode, a.city || a.town || a.village].filter(Boolean);
      if (teile.length) return teile.join(' ');
      if (d.display_name) return d.display_name.split(',').slice(0,3).join(',').trim();
    }
  } catch(e) {}
  return lat.toFixed(5) + ', ' + lon.toFixed(5);
}

async function standortFuer(i){
  const info = document.getElementById('route-info');
  info.textContent = 'Ermittle deinen Standort …';
  try {
    const p = await holeStandort();
    _stops[i].lat = p.lat; _stops[i].lon = p.lon;
    _stops[i].label = await adresseZu(p.lat, p.lon);
    _route = null;
    zeichneStops();
    info.textContent = '✓ Standort übernommen (auf etwa ' + Math.round(p.genauigkeit) + ' m genau).';
  } catch(e) { info.textContent = '✗ ' + e.message; }
}

async function berechneRoute(){
  const info = document.getElementById('route-info');
  info.textContent = 'Ermittle Strecke …';
  for (let i = 0; i < _stops.length; i++) {
    if (!_stops[i].label.trim()) { info.textContent = '✗ Bitte alle Halte ausfüllen.'; return; }
    if (!_stops[i].lat) {
      const g = await geocode(_stops[i].label);
      if (!g) { info.textContent = '✗ Adresse nicht gefunden: ' + _stops[i].label; return; }
      _stops[i].lat = g.lat; _stops[i].lon = g.lon;
    }
  }
  try {
    const r = await fetch('/api/v1/drive/route', {method:'POST', credentials:'same-origin',
      headers:{'Content-Type':'application/json'}, body: JSON.stringify({stops:_stops})});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    _route = d;
    const quelle = d.source === 'osrm' ? 'über den Routendienst' : 'geschätzt (Luftlinie × 1,25)';
    info.innerHTML = '<b>' + d.distance_km.toFixed(1) + ' km</b> · etwa ' + d.duration_min + ' Minuten ' +
                     '<span class="meta">(' + quelle + ')</span>';
    zeigeKostenSchaetzung(d.distance_km);
  } catch(e) { info.textContent = '✗ ' + e.message; }
}

// Kostenschätzung (R637): nach dem NÄCHSTGELEGENEN Fahrer, denn der fährt am
// ehesten – und seine Anfahrt bestimmt einen guten Teil des Preises. Die
// Spanne der übrigen Fahrer steht als Einordnung daneben.
async function zeigeKostenSchaetzung(km){
  const info = document.getElementById('route-info');
  try {
    // Standort des ersten Halts als Bezugspunkt für die Anfahrt.
    const start = _stops[0] || {};
    let url = '/api/v1/drive/drivers';
    if (start.lat || start.lon) url += '?lat=' + start.lat + '&lon=' + start.lon;
    const r = await fetch(url);
    const d = await r.json();
    const fahrer = (d.drivers||[]).filter(function(x){ return x.per_km_fnd > 0; });
    if (!fahrer.length) {
      info.innerHTML += '<div class="meta" style="margin-top:4px">Noch kein Fahrer im Netz, ' +
        'der Preise hinterlegt hat.</div>';
      return;
    }
    function preis(f){
      const anfahrt = (f.distance_km != null) ? f.distance_km : 0;
      return f.approach_flat_fnd + anfahrt * f.approach_per_km + km * f.per_km_fnd;
    }
    // Nächstgelegener zuerst – die Liste kommt bereits nach Entfernung sortiert.
    const naechster = fahrer[0];
    const p = preis(naechster);
    const bew = (naechster.rating && naechster.rating.count)
      ? ' · ★ ' + naechster.rating.avg : '';
    let txt = '<div class="kosten-schaetzung">Geschätzt bei <b>' +
      escHtml(naechster.name || naechster.fid.slice(0,10)) + '</b>' +
      (naechster.distance_km != null ? ' (' + naechster.distance_km.toFixed(1) + ' km entfernt' + bew + ')' : bew) +
      ': <b>' + p.toFixed(2) + ' FND</b>';
    if (fahrer.length > 1) {
      const alle = fahrer.map(preis).sort(function(a,b){ return a-b; });
      txt += '<span class="meta"> · ' + fahrer.length + ' Fahrer im Netz, ' +
             alle[0].toFixed(2) + ' bis ' + alle[alle.length-1].toFixed(2) + ' FND</span>';
    }
    info.innerHTML += txt + '</div>';
  } catch(e) {}
}

function kindChanged(){
  const ware = document.querySelector('input[name="dkind"]:checked').value === 'goods';
  document.getElementById('goods-felder').style.display  = ware ? '' : 'none';
  document.getElementById('person-felder').style.display = ware ? 'none' : '';
}

document.addEventListener('change', function(e){
  if (e.target && e.target.name === 'dtarget') {
    const direkt = e.target.value === 'direct';
    document.getElementById('target-wahl').style.display = direkt ? '' : 'none';
    if (direkt) fuelleFahrerAuswahl();
  }
});

async function fuelleFahrerAuswahl(){
  const sel = document.getElementById('d-driver');
  try {
    const r = await fetch('/api/v1/drive/drivers');
    const d = await r.json();
    sel.innerHTML = (d.drivers||[]).map(function(f){
      const bew = (f.rating && f.rating.count) ? (' · ★ ' + f.rating.avg) : '';
      return '<option value="' + f.fid + '">' + (f.name || f.fid.slice(0,10)) +
             ' · ' + (f.per_km_fnd||0).toFixed(2) + ' FND/km' + bew + '</option>';
    }).join('') || '<option value="">Noch keine Fahrer im Netz</option>';
  } catch(e) { sel.innerHTML = '<option value="">Fahrerliste nicht abrufbar</option>'; }
}

async function gesuchSenden(){
  const st = document.getElementById('neu-status');
  if (!_route) { st.textContent = '✗ Bitte zuerst die Strecke berechnen.'; return; }
  const ware = document.querySelector('input[name="dkind"]:checked').value === 'goods';
  const ziel = document.querySelector('input[name="dtarget"]:checked').value === 'direct'
             ? (document.getElementById('d-driver').value || '') : '';
  const body = {
    kind: ware ? 'goods' : 'person',
    stops: _stops,
    when_text: document.getElementById('d-when').value.trim(),
    note: document.getElementById('d-note').value.trim(),
    target_driver: ziel,
  };
  if (ware) {
    body.goods = {
      length_cm: +document.getElementById('g-len').value || 0,
      width_cm:  +document.getElementById('g-wid').value || 0,
      height_cm: +document.getElementById('g-hei').value || 0,
      weight_kg: +document.getElementById('g-kg').value  || 0,
      desc:      document.getElementById('g-desc').value.trim(),
    };
  } else {
    body.seats = +document.getElementById('d-seats').value || 1;
  }
  st.textContent = 'Sende …';
  try {
    const r = await fetch('/api/v1/drive/rides', {method:'POST', credentials:'same-origin',
      headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    st.textContent = '✓ Gesuch eingestellt.';
    driveTab('gesuche');
  } catch(e) { st.textContent = '✗ ' + e.message; }
}

// ── Gesuche ───────────────────────────────────────────────────────────────
async function ladeGesuche(){
  const box = document.getElementById('gesuch-liste');
  try {
    const r = await fetch('/api/v1/drive/rides', {credentials:'same-origin'});
    const d = await r.json();
    const liste = d.rides || [];
    if (!liste.length) { box.innerHTML = '<div class="empty-hint" data-empty="listings">Keine Fahrtgesuche</div>'; return; }
    _letzteGesuche = liste;
    box.innerHTML = liste.map(gesuchKarte).join('');
  } catch(e) { box.innerHTML = '<div class="empty-hint">Nicht abrufbar: ' + e.message + '</div>'; }
}

function gesuchKarte(g){
  const weg = g.stops.map(function(s){ return s.label || '?'; }).join(' → ');
  const stat = {open:'offen', locked:'vergeben', done:'abgeschlossen', cancelled:'zurückgezogen'}[g.status] || g.status;
  const farbe = {open:'var(--green)', locked:'#ffc107', done:'var(--muted)', cancelled:'#ff8a80'}[g.status];
  let ware = '';
  if (g.kind === 'goods' && g.goods) {
    const m = [g.goods.length_cm, g.goods.width_cm, g.goods.height_cm].filter(Boolean).join(' × ');
    ware = '<div class="meta">Ware: ' + (g.goods.desc || '') +
           (m ? ' · ' + m + ' cm' : '') + (g.goods.weight_kg ? ' · ' + g.goods.weight_kg + ' kg' : '') + '</div>';
  }
  let aktionen = '';
  if (g.status === 'open' && !g.mine) {
    aktionen = '<button class="btn-sm" onclick="annehmen(\'' + g.id + '\')">Annehmen</button>';
  } else if (g.status === 'locked' && g.driving) {
    // Fahrer: abschließen, zurückgeben – und Barzahlung quittieren (R636).
    aktionen = '<button class="btn-sm" onclick="navFuer(\'' + g.id + '\')">🧭 Navigation</button>' +
               '<button class="btn-sm" onclick="rideAktion(\'' + g.id + '\',\'done\')">Fahrt abgeschlossen</button>' +
               '<button class="btn-sm" onclick="barQuittieren(\'' + g.id + '\',' + (g.price_fnd||0) + ')">💶 Bar erhalten</button>' +
               '<button class="btn-sm" onclick="rideAktion(\'' + g.id + '\',\'release\')">Zurückgeben</button>';
  } else if (g.status === 'locked' && g.mine) {
    // Kunde: hinterlegen, freigeben, erledigt.
    aktionen = (g.escrow_id
        ? '<button class="btn-sm" onclick="freigeben(\'' + g.escrow_id + '\')">✓ Fahrt bestätigen und auszahlen</button>'
        : '<button class="btn-sm" onclick="bezahlen(\'' + g.id + '\')">💳 Betrag hinterlegen</button>') +
      '<button class="btn-sm" onclick="rideAktion(\'' + g.id + '\',\'done\')">Erledigt</button>';
  } else if (g.status === 'open' && g.mine) {
    aktionen = '<button class="btn-sm btn-danger" onclick="rideAktion(\'' + g.id + '\',\'cancel\')">Zurückziehen</button>';
  }
  if (g.status === 'done') {
    // Wer bewertet wen? (R637) Der Kunde bewertet den Fahrer und umgekehrt.
    const wen = g.mine ? 'Fahrer' : 'Kunde';
    aktionen = '<button class="btn-sm" onclick="bewerten(\'' + g.id + '\')">⭐ ' + wen + ' bewerten</button>';
  }
  // Wer ist beteiligt? (R637) Kunde und Fahrer mit Namen zeigen.
  // Fehlende Felder dürfen die Liste nicht zerreißen: ältere Gesuche kennen
  // den Namen noch nicht (R637).
  let leute = '<span class="meta">Kunde: ' + escHtml(wer(g.customer_name, g.customer_fid)) + '</span>';
  if (g.driver_name || g.driver_fid) {
    leute += '<span class="meta"> · Fahrer: ' + escHtml(wer(g.driver_name, g.driver_fid)) + '</span>';
  }
  return '<div class="ride-card">' +
    '<div class="ride-top"><b>' + (g.kind === 'goods' ? '📦 Ware' : '🧍 Personen') + '</b>' +
    '<span class="ride-status" style="color:' + farbe + '">' + stat + '</span></div>' +
    '<div class="ride-route">' + escHtml(weg) + '</div>' +
    '<div>' + leute + '</div>' +
    '<div class="meta">' + (g.distance_km||0).toFixed(1) + ' km · etwa ' + (g.duration_min||0) + ' min' +
      (g.route_source === 'luftlinie' ? ' (geschätzt)' : '') +
      (g.when_text ? ' · ' + escHtml(g.when_text) : '') + '</div>' +
    ware +
    (g.note ? '<div class="meta">„' + escHtml(g.note) + '“</div>' : '') +
    preisZeile(g) +
    zahlungsZeile(g) +
    '<div class="ride-actions">' + aktionen + '</div></div>';
}
// Preis am Gesuch (R639): Steht ein Fahrer fest, gilt der vereinbarte Betrag.
// Vorher die Schätzung nach dem nächstgelegenen Fahrer – vorher stand an
// offenen Gesuchen gar kein Preis.
function preisZeile(g){
  if (g.price_fnd) {
    return '<div class="meta">Vereinbart: <b>' + g.price_fnd.toFixed(2) + ' FND</b></div>';
  }
  if (g.estimate_fnd) {
    const wer = g.estimate_name ? (' bei ' + escHtml(g.estimate_name)) : '';
    const rest = (g.estimate_count > 1) ? (' · ' + g.estimate_count + ' Fahrer in Reichweite') : '';
    return '<div class="meta">Geschätzt' + wer + ': <b>' + g.estimate_fnd.toFixed(2) +
           ' FND</b><span class="meta">' + rest + '</span></div>';
  }
  return '<div class="meta">Noch kein Fahrer mit Preisen in Reichweite.</div>';
}

// Zahlungsstand, nach Abschluss mit dem tatsächlich erhaltenen Betrag (R637).
function zahlungsZeile(g){
  const betrag = g.paid_fnd || g.price_fnd || 0;
  if (g.payment_mode === 'cash') {
    return '<div class="bezahlt">💶 Bar erhalten: <b>' + betrag.toFixed(2) +
           ' FND</b> <span class="meta">– vom Fahrer quittiert</span></div>';
  }
  if (g.escrow_id) {
    return g.status === 'done'
      ? '<div class="bezahlt">💳 Ausgezahlt: <b>' + betrag.toFixed(2) + ' FND</b></div>'
      : '<div class="bezahlt">💳 <b>' + betrag.toFixed(2) +
        ' FND</b> hinterlegt <span class="meta">– Freigabe nach der Fahrt</span></div>';
  }
  if (g.status === 'done') {
    return '<div class="meta">Keine Zahlung über Fundus vermerkt.</div>';
  }
  return '';
}

// Name, sonst gekürzte Kennung, sonst Platzhalter.
function wer(name, fid){
  if (name && String(name).trim()) return String(name);
  if (fid && String(fid).length > 10) return String(fid).slice(0,10) + '…';
  return fid || 'unbekannt';
}

function escHtml(s){ return String(s||'').replace(/[<>&]/g, function(c){ return {'<':'&lt;','>':'&gt;','&':'&amp;'}[c]; }); }

async function annehmen(id){
  await rideAktion(id, 'accept');
  // R637: Direkt die Navigation öffnen – vom aktuellen Standort über alle
  // Halte des Kunden bis zum Ziel. Der Fahrer soll nach dem Annehmen nicht
  // erst Adressen abtippen müssen.
  const g = (_letzteGesuche || []).find(function(x){ return x.id === id; });
  if (g) navigationOeffnen(g);
}

// Baut eine Route für die Karten-App des Geräts: Start ist der aktuelle
// Standort (falls verfügbar), dann alle Halte des Kunden der Reihe nach.
// Entfernung zweier Punkte in Metern (Haversine).
function metersBetween(lat1, lon1, lat2, lon2){
  const R = 6371000, rad = Math.PI/180;
  const dLat = (lat2-lat1)*rad, dLon = (lon2-lon1)*rad;
  const a = Math.sin(dLat/2)**2 + Math.cos(lat1*rad)*Math.cos(lat2*rad)*Math.sin(dLon/2)**2;
  return 2*R*Math.asin(Math.sqrt(a));
}

// Navigation erneut öffnen (Knopf in der Fahrtkarte).
function navFuer(id){
  const g = (_letzteGesuche || []).find(function(x){ return x.id === id; });
  if (g) navigationOeffnen(g);
}

async function navigationOeffnen(g){
  if (!g || !g.stops || !g.stops.length) return;
  const punkte = g.stops.filter(function(s){ return s.lat || s.lon; });
  if (!punkte.length) { alert('Für diese Fahrt sind keine Koordinaten hinterlegt.'); return; }

  let eigen = null;
  try { eigen = await holeStandort(); } catch(e) {}

  const ziel = punkte[punkte.length-1];
  let halte = punkte.slice(0, punkte.length-1);

  // R638: Steht der Fahrer bereits am Abholort, stand dieselbe Adresse als
  // Start UND als erstes Zwischenziel in der Route. Liegt der eigene Standort
  // dicht genug am ersten Halt, fällt dieser Halt weg.
  let start = null;
  if (eigen) {
    start = eigen.lat + ',' + eigen.lon;
    if (halte.length && metersBetween(eigen.lat, eigen.lon, halte[0].lat, halte[0].lon) < 250) {
      halte = halte.slice(1);
    }
  } else if (halte.length) {
    // Ohne eigenen Standort ist der erste Halt der Start.
    start = halte[0].lat + ',' + halte[0].lon;
    halte = halte.slice(1);
  }
  // Auch doppelte Halte hintereinander herausnehmen.
  const zwischen = [];
  halte.forEach(function(h){
    const letzter = zwischen.length ? zwischen[zwischen.length-1] : null;
    const txt = h.lat + ',' + h.lon;
    if (txt !== letzter) zwischen.push(txt);
  });

  let url = 'https://www.google.com/maps/dir/?api=1' +
            '&destination=' + encodeURIComponent(ziel.lat + ',' + ziel.lon) +
            '&travelmode=driving';
  if (start)           url += '&origin=' + encodeURIComponent(start);
  if (zwischen.length) url += '&waypoints=' + encodeURIComponent(zwischen.join('|'));
  window.open(url, '_blank', 'noopener');
}

async function rideAktion(id, was){
  try {
    const r = await fetch('/api/v1/drive/rides/' + encodeURIComponent(id) + '/' + was,
      {method:'POST', credentials:'same-origin'});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    ladeGesuche();
  } catch(e) { alert('✗ ' + e.message); }
}

async function bewerten(id){
  const s = prompt('Wie viele Sterne? (1 bis 5)');
  if (s === null) return;
  const n = parseInt(s, 10);
  if (!(n >= 1 && n <= 5)) { alert('Bitte 1 bis 5 angeben.'); return; }
  const k = prompt('Kommentar (optional)') || '';
  try {
    const r = await fetch('/api/v1/drive/rides/' + encodeURIComponent(id) + '/rate',
      {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
       body: JSON.stringify({stars:n, comment:k})});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    alert('✓ Danke für die Bewertung.');
  } catch(e) { alert('✗ ' + e.message); }
}

// ── Fahrer und Karte ──────────────────────────────────────────────────────
async function ladeFahrer(){
  const liste = document.getElementById('fahrer-liste');
  try {
    const r = await fetch('/api/v1/drive/drivers');
    const d = await r.json();
    const f = d.drivers || [];
    if (!f.length) { liste.innerHTML = '<div class="empty-hint" data-empty="listings">Noch keine Fahrer im Netz</div>'; return; }
    liste.innerHTML = f.map(function(x){
      const bew = (x.rating && x.rating.count) ? ('★ ' + x.rating.avg + ' (' + x.rating.count + ')') : 'noch nicht bewertet';
      return '<div class="ride-card"><div class="ride-top"><b>' + escHtml(x.name || x.fid.slice(0,10)) + '</b>' +
        '<span class="meta">' + bew + '</span></div>' +
        '<div class="meta">' + (x.per_km_fnd||0).toFixed(2) + ' FND/km' +
        (x.approach_flat_fnd ? ' · Anfahrt ' + x.approach_flat_fnd.toFixed(2) + ' FND' : '') +
        (x.approach_per_km ? ' · Anfahrt ' + x.approach_per_km.toFixed(2) + ' FND/km' : '') + '</div>' +
        '<div class="meta">' + escHtml(x.vehicle || '') +
        (x.goods ? ' · transportiert Ware' : '') +
        (x.distance_km != null ? ' · ' + x.distance_km.toFixed(1) + ' km entfernt' : '') + '</div>' +
        (x.note ? '<div class="meta">„' + escHtml(x.note) + '“</div>' : '') + '</div>';
    }).join('');
    zeichneKarte(f);
  } catch(e) { liste.innerHTML = '<div class="empty-hint">Nicht abrufbar: ' + e.message + '</div>'; }
}

// Karte nur, wenn die Bibliothek geladen werden konnte. Ohne Internet bleibt
// die Liste – die ist das Wesentliche.
function zeichneKarte(fahrer){
  const box = document.getElementById('karte');
  if (typeof L === 'undefined') { box.style.display = 'none'; return; }
  box.style.display = '';
  if (!_karte) {
    _karte = L.map('karte').setView([50.2, 9.0], 8);
    L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png',
      {maxZoom: 18, attribution: '© OpenStreetMap'}).addTo(_karte);
  }
  _marker.forEach(function(m){ _karte.removeLayer(m); });
  _marker = [];
  const punkte = [];
  fahrer.forEach(function(f){
    if (!f.lat && !f.lon) return;
    const m = L.marker([f.lat, f.lon]).addTo(_karte);
    m.bindPopup('<b>' + escHtml(f.name || '') + '</b><br>' + (f.per_km_fnd||0).toFixed(2) + ' FND/km' +
      '<br><a href="#" onclick="waehleFahrer(\'' + f.fid + '\');return false;">Gezielt anfragen</a>');
    _marker.push(m); punkte.push([f.lat, f.lon]);
  });
  if (punkte.length) _karte.fitBounds(punkte, {padding:[30,30], maxZoom: 11});
}

function waehleFahrer(fid){
  driveTab('neu');
  document.querySelector('input[name="dtarget"][value="direct"]').checked = true;
  document.getElementById('target-wahl').style.display = '';
  fuelleFahrerAuswahl().then(function(){
    const sel = document.getElementById('d-driver');
    if (sel) sel.value = fid;
  });
}

// ── Fahrerprofil ──────────────────────────────────────────────────────────
function cargoToggle(){
  document.getElementById('cargo-felder').style.display =
    document.getElementById('p-goods').checked ? '' : 'none';
}

async function ladeProfil(){
  try {
    const r = await fetch('/api/v1/drive/driver/me', {credentials:'same-origin'});
    const d = await r.json();
    const p = d.driver; if (!p) return;
    document.getElementById('p-active').checked = !!p.active;
    document.getElementById('p-perkm').value   = p.per_km_fnd || '';
    document.getElementById('p-flat').value    = p.approach_flat_fnd || '';
    document.getElementById('p-appkm').value   = p.approach_per_km || '';
    document.getElementById('p-plz').value     = p.plz || '';
    document.getElementById('p-radius').value  = p.radius_km || 50;
    document.getElementById('p-vehicle').value = p.vehicle || '';
    document.getElementById('p-seats').value   = p.seats || 4;
    document.getElementById('p-note').value    = p.note || '';
    document.getElementById('p-goods').checked = !!p.goods;
    if (p.cargo) {
      document.getElementById('c-len').value = p.cargo.length_cm || '';
      document.getElementById('c-wid').value = p.cargo.width_cm || '';
      document.getElementById('c-hei').value = p.cargo.height_cm || '';
      document.getElementById('c-kg').value  = p.cargo.weight_kg || '';
    }
    cargoToggle();
  } catch(e) {}
}

let _profilLat = 0, _profilLon = 0;

async function profilStandort(){
  const st = document.getElementById('profil-status');
  st.textContent = 'Ermittle deinen Standort …';
  try {
    const p = await holeStandort();
    _profilLat = p.lat; _profilLon = p.lon;
    st.textContent = '✓ Standort übernommen (±' + Math.round(p.genauigkeit) + ' m). Jetzt speichern.';
  } catch(e) { st.textContent = '✗ ' + e.message; }
}

// ── Bezahlen (R636) ───────────────────────────────────────────────────────
// Über die Chain: Betrag hinterlegen, Freigabe erst nach bestätigter Fahrt.
async function bezahlen(id){
  try {
    const r = await fetch('/api/v1/drive/rides/' + encodeURIComponent(id) + '/payee',
      {credentials:'same-origin'});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    const betrag = prompt('Betrag in FND, der hinterlegt werden soll:', (d.price_fnd || 0).toFixed(2));
    if (betrag === null) return;
    const woerter = prompt('Deine 12 Seed-Wörter (zum Signieren der Hinterlegung):');
    if (!woerter) return;
    const e = await fetch('/api/v1/escrow', {method:'POST', credentials:'same-origin',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({seller: d.wallet, amount_fnd: String(betrag),
                            words: woerter.trim().split(/\s+/)})});
    const ed = await e.json();
    if (!e.ok) throw new Error(ed.error || ('HTTP ' + e.status));
    await fetch('/api/v1/drive/rides/' + encodeURIComponent(id) + '/escrow',
      {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
       body: JSON.stringify({escrow_id: ed.escrow_id})});
    alert('✓ Betrag hinterlegt. Er wird freigegeben, sobald du die Fahrt bestätigst.');
    ladeGesuche();
  } catch(err) { alert('✗ ' + err.message); }
}

// Bar: Der Fahrer quittiert den Erhalt.
async function barQuittieren(id, vorschlag){
  const b = prompt('Erhaltener Betrag in FND:', (vorschlag || 0).toFixed(2));
  if (b === null) return;
  try {
    const r = await fetch('/api/v1/drive/rides/' + encodeURIComponent(id) + '/cash',
      {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
       body: JSON.stringify({amount_fnd: parseFloat(b) || 0})});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    ladeGesuche();
  } catch(e) { alert('✗ ' + e.message); }
}

// Freigabe der Hinterlegung nach der Fahrt (Kunde).
async function freigeben(escrowId){
  if (!confirm('Fahrt bestätigen? Danach wird der Betrag an den Fahrer ausgezahlt.')) return;
  try {
    const r = await fetch('/api/v1/escrow/' + encodeURIComponent(escrowId) + '/confirm',
      {method:'POST', credentials:'same-origin'});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    ladeGesuche();
  } catch(e) { alert('✗ ' + e.message); }
}

async function profilSpeichern(){
  const st = document.getElementById('profil-status');
  const body = {
    name: '',
    active: document.getElementById('p-active').checked,
    per_km_fnd: +document.getElementById('p-perkm').value || 0,
    approach_flat_fnd: +document.getElementById('p-flat').value || 0,
    approach_per_km: +document.getElementById('p-appkm').value || 0,
    plz: document.getElementById('p-plz').value.trim(),
    lat: _profilLat, lon: _profilLon,
    radius_km: +document.getElementById('p-radius').value || 0,
    vehicle: document.getElementById('p-vehicle').value.trim(),
    seats: +document.getElementById('p-seats').value || 0,
    note: document.getElementById('p-note').value.trim(),
    goods: document.getElementById('p-goods').checked,
    cargo: {
      length_cm: +document.getElementById('c-len').value || 0,
      width_cm:  +document.getElementById('c-wid').value || 0,
      height_cm: +document.getElementById('c-hei').value || 0,
      weight_kg: +document.getElementById('c-kg').value  || 0,
    },
  };
  st.textContent = 'Speichere …';
  try {
    const r = await fetch('/api/v1/drive/driver/me', {method:'PUT', credentials:'same-origin',
      headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    st.textContent = '✓ Gespeichert';
  } catch(e) { st.textContent = '✗ ' + e.message; }
}

zeichneStops();
</script>
]==])

    render.footer()
end

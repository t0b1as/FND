-- pages/settings.lua – Node-Einstellungen
local render = require "render"
local cjson  = require "cjson.safe"

return function()
  ngx.header["Content-Type"] = "text/html"
  local t = render.header("settings.title", "settings")

  local status, _ = render.api_get("/v1/status")
  local meterSt,_ = render.api_get("/v1/meter/status")
  local nat, _    = render.api_get("/v1/nat")

ngx.print([[
<div class="set-layout">

<!-- ── Software-Update ───────────────────────────────────────── -->
<div class="set-card">
  <div class="set-head"><h3>Software-Update</h3></div>
  <div class="set-body">
    <div id="upd-status" class="status-line">Prüfe…</div>
    <div id="upd-avail" style="display:none;margin-top:10px;padding:10px 12px;border:1px solid var(--green,#00e676);border-radius:10px">
      <div><b>Neue Version verfügbar: <span id="upd-ver"></span></b></div>
      <div id="upd-desc" class="meta" style="margin:4px 0 8px"></div>
      <button class="btn-prim" id="upd-apply" onclick="updApply()">Jetzt installieren</button>
      <span class="meta" style="margin-left:8px">Signatur und Prüfsumme werden vor der Installation geprüft. Daten, Wallets und Einstellungen bleiben erhalten.</span>
    </div>
    <div style="margin-top:10px">
      <button class="btn" onclick="updCheck(this)">Jetzt prüfen</button>
      <span id="upd-src" class="meta" style="margin-left:8px"></span>
    </div>
    <div id="upd-msg" class="status-line" style="margin-top:8px"></div>
  </div>
</div>
<script>
function updRender(d) {
  var st = document.getElementById('upd-status');
  var av = document.getElementById('upd-avail');
  if (!d || d.error) { st.textContent = (d && d.error) || 'Status nicht abrufbar.'; return; }
  st.innerHTML = 'Installiert: <b>' + (d.current || '?') + '</b>' +
    (d.enabled ? (d.auto ? ' · automatische Installation an' : '') : ' · Update-Prüfung ausgeschaltet');
  document.getElementById('upd-src').textContent = d.source ? ('Quelle: ' + d.source.replace('https://raw.githubusercontent.com/', 'github.com/').replace('/main/manifest.json', '')) : '';
  if (d.available && d.available.version) {
    av.style.display = 'block';
    document.getElementById('upd-ver').textContent = d.available.version;
    var when = d.available.published_at ? new Date(d.available.published_at).toLocaleString('de-DE') : '';
    document.getElementById('upd-desc').textContent = (d.available.description || '') + (when ? ' · veröffentlicht ' + when : '');
  } else {
    av.style.display = 'none';
  }
}
async function updLoad() {
  try {
    var r = await fetch('/api/v1/admin/update/status', {credentials:'same-origin'});
    updRender(await r.json());
  } catch (e) { updRender({error: 'Status nicht abrufbar.'}); }
}
async function updCheck(btn) {
  var msg = document.getElementById('upd-msg');
  btn.disabled = true; msg.textContent = 'Frage GitHub ab…';
  try {
    var r = await fetch('/api/v1/admin/update/check', {method:'POST', credentials:'same-origin'});
    var d = await r.json();
    updRender(d);
    msg.textContent = (d.available && d.available.version) ? '' : 'Du bist auf dem neuesten Stand.';
  } catch (e) { msg.textContent = 'Prüfung fehlgeschlagen: ' + e.message; }
  btn.disabled = false;
}
async function updApply() {
  var ver = document.getElementById('upd-ver').textContent;
  if (!confirm('Version ' + ver + ' jetzt installieren? Der Node startet danach neu (2–5 Minuten).')) return;
  var btn = document.getElementById('upd-apply'), msg = document.getElementById('upd-msg');
  btn.disabled = true; msg.textContent = 'Starte Installation…';
  try {
    var r = await fetch('/api/v1/admin/update/apply', {method:'POST', credentials:'same-origin'});
    var d = await r.json();
    if (!r.ok) { msg.textContent = 'Fehler: ' + (d.error || r.status); btn.disabled = false; return; }
    msg.textContent = d.message || 'Update läuft…';
    // Nach dem Neustart automatisch neu laden, sobald der Node wieder antwortet.
    var tries = 0;
    var t = setInterval(async function(){
      tries++;
      try {
        var h = await fetch('/api/v1/admin/update/status', {cache:'no-store', credentials:'same-origin'});
        if (h.ok) { var j = await h.json(); if (j.current === ver) { clearInterval(t); location.reload(); } }
      } catch (e) {}
      if (tries > 60) { clearInterval(t); msg.textContent = 'Der Node antwortet noch nicht mit ' + ver + ' – bitte später neu laden und das Log prüfen.'; }
    }, 10000);
  } catch (e) { msg.textContent = 'Fehler: ' + e.message; btn.disabled = false; }
}
document.addEventListener('DOMContentLoaded', updLoad);
</script>

<!-- ── Navigation anpassen ──────────────────────────────────── -->
<div class="set-card">
  <div class="set-head"><h3>Navigation anpassen</h3></div>
  <div class="set-body">
    <p class="set-hint" style="margin-bottom:10px">Lege fest, wo jeder Bereich erscheint: als Kachel auf der Startseite, im Menü (Burger), und/oder als Schnellzugriff-Button oben in der Titelleiste (Ein-Klick-Navigation). Gilt nur für dieses Gerät.</p>
    <table class="nav-vis-table" id="nav-vis-table">
      <thead>
        <tr><th>Bereich</th><th>Startseite</th><th>Menü</th><th>Schnellzugriff</th></tr>
      </thead>
      <tbody id="nav-vis-body"></tbody>
    </table>
  </div>
</div>

<!-- ── Node-Typ & Netz ──────────────────────────────────────── -->
<div class="set-card">
  <div class="set-head"><h3>]] .. t("settings.node_profile") .. [[</h3></div>
  <div class="set-body">
    <div class="set-row">
      <div class="set-label">]] .. t("settings.node_type") .. [[<div class="set-hint">]] .. t("settings.node_type_hint") .. [[</div></div>
      <select id="s-nodetype" class="set-input">
        <option value="consumer">]] .. t("settings.type_consumer") .. [[</option>
        <option value="prosumer">]] .. t("settings.type_prosumer") .. [[</option>
        <option value="substation">]] .. t("settings.type_substation") .. [[</option>
        <option value="power_plant">]] .. t("settings.type_power_plant") .. [[</option>
      </select>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.voltage_level") .. [[</div>
      <select id="s-voltage" class="set-input">
        <option value="lv">]] .. t("settings.voltage_lv") .. [[</option>
        <option value="mv">]] .. t("settings.voltage_mv") .. [[</option>
        <option value="hv">]] .. t("settings.voltage_hv") .. [[</option>
        <option value="ehv">]] .. t("settings.voltage_ehv") .. [[</option>
      </select>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.gps_position") .. [[<div class="set-hint">]] .. t("settings.gps_hint") .. [[</div></div>
      <div class="set-input-row">
        <input type="number" id="s-lat" placeholder="]] .. t("settings.placeholder_lat") .. [[" step="0.000001" class="set-input">
        <input type="number" id="s-lon" placeholder="]] .. t("settings.placeholder_lon") .. [[" step="0.000001" class="set-input">
        <button class="btn-sm" onclick="getGPS()">📍 Standort</button>
      </div>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.parent_transformer") .. [[<div class="set-hint">]] .. t("settings.parent_hint") .. [[</div></div>
      <input type="text" id="s-parent" placeholder="]] .. t("settings.placeholder_parent") .. [[" class="set-input">
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.wheeling_fee") .. [[<div class="set-hint">]] .. t("settings.wheeling_hint") .. [[</div></div>
      <div class="set-input-row">
        <input type="number" id="s-fee" step="0.01" min="0" max="10" placeholder="0.30" class="set-input" style="width:100px">
        <span class="set-unit">% pro Transaktion</span>
      </div>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.gmaps_key") .. [[<div class="set-hint">]] .. t("settings.gmaps_hint") .. [[</div></div>
      <input type="text" id="s-gmaps" placeholder="AIzaSy…" class="set-input">
    </div>
    <div class="set-actions">
      <button class="btn-prim" onclick="saveProfile()">]] .. t("settings.save_profile") .. [[</button>
      <div id="profile-msg" class="status-line"></div>
    </div>
  </div>
</div>

<!-- ── Smartmeter ──────────────────────────────────────────── -->
<div class="set-card">
  <div class="set-head">
    <h3>]] .. t("settings.smartmeter") .. [[</h3>
    <button class="btn-sm" onclick="detectMeter()">🔍 Auto-Scan</button>
  </div>
  <div class="set-body">
    <div id="detect-status" class="detect-bar" style="display:none"></div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.protocol") .. [[</div>
      <select id="s-proto" class="set-input" onchange="onProtoChange()">
        <option value="">]] .. t("settings.proto_disabled") .. [[</option>
        <option value="sml">]] .. t("settings.proto_sml") .. [[</option>
        <option value="d0">]] .. t("settings.proto_d0") .. [[</option>
        <option value="http">HTTP – Shelly EM / Tasmota / Tibber Pulse</option>
        <option value="mock">]] .. t("settings.proto_mock") .. [[</option>
      </select>
    </div>
    <div id="serial-fields">
      <div class="set-row">
        <div class="set-label">]] .. t("settings.serial_port") .. [[<div class="set-hint">]] .. t("settings.serial_hint") .. [[</div></div>
        <div class="set-input-row">
          <select id="s-port" class="set-input">
            <option value="/dev/ttyUSB0">/dev/ttyUSB0 (USB-Lesekopf)</option>
            <option value="/dev/ttyUSB1">/dev/ttyUSB1</option>
            <option value="/dev/ttyAMA0">/dev/ttyAMA0 (Pi GPIO UART)</option>
            <option value="/dev/ttyS0">/dev/ttyS0</option>
          </select>
          <button class="btn-sm" onclick="loadPorts()">↻ Ports</button>
        </div>
      </div>
      <div class="set-row">
        <div class="set-label">]] .. t("settings.baudrate") .. [[</div>
        <select id="s-baud" class="set-input">
          <option value="115200">115200 (SML Standard, Hichi-Lesekopf)</option>
          <option value="9600">9600 (ältere SML-Zähler)</option>
          <option value="300">300 (D0-Handshake)</option>
        </select>
      </div>
      <div class="set-row">
        <div class="set-label">]] .. t("settings.parity") .. [[</div>
        <select id="s-parity" class="set-input">
          <option value="N">]] .. t("settings.parity_none") .. [[</option>
          <option value="E">]] .. t("settings.parity_even") .. [[</option>
          <option value="O">]] .. t("settings.parity_odd") .. [[</option>
        </select>
      </div>
    </div>
    <div id="http-fields" style="display:none">
      <div class="set-row">
        <div class="set-label">HTTP-URL<div class="set-hint">z.B. http://192.168.1.50/status</div></div>
        <input type="text" id="s-httpurl" placeholder="http://…" class="set-input">
      </div>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.meter_id") .. [[<div class="set-hint">]] .. t("settings.meter_id_hint") .. [[</div></div>
      <input type="text" id="s-meterid" placeholder="]] .. t("settings.placeholder_auto") .. [[" class="set-input">
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.meter_coords") .. [[</div>
      <div class="set-input-row">
        <input type="number" id="s-mlat" placeholder="]] .. t("settings.placeholder_lat_short") .. [[" step="0.000001" class="set-input">
        <input type="number" id="s-mlon" placeholder="]] .. t("settings.placeholder_lon_short") .. [["  step="0.000001" class="set-input">
      </div>
    </div>
    <div class="set-actions">
      <button class="btn-prim" onclick="saveMeter()">]] .. t("settings.save_meter") .. [[</button>
      <div id="meter-msg" class="status-line"></div>
    </div>
  </div>
</div>

<!-- ── Filesharing / Storage ────────────────────────────────── -->
<div class="set-card">
  <div class="set-head"><h3>]] .. t("settings.storage_head") .. [[</h3></div>
  <div class="set-body">
    <div class="set-row">
      <div class="set-label">]] .. t("settings.offer_volume") .. [[<div class="set-hint">]] .. t("settings.offer_hint") .. [[</div></div>
      <div class="set-input-row">
        <input type="number" id="s-offer" min="0" step="1" placeholder="]] .. t("settings.placeholder_offer") .. [[" class="set-input" style="width:120px">
        <span class="set-unit">GB</span>
      </div>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.alloc_volume") .. [[<div class="set-hint">]] .. t("settings.alloc_hint") .. [[</div></div>
      <div class="set-input-row">
        <input type="number" id="s-alloc" min="0" step="1" placeholder="0" class="set-input" style="width:120px">
        <span class="set-unit">GB</span>
      </div>
    </div>
    <div class="set-row">
      <div class="set-label">]] .. t("settings.storage_dir") .. [[</div>
      <input type="text" id="s-storagedir" placeholder="/opt/fundus/chunks" class="set-input">
    </div>
    <div class="storage-bar-wrap">
      <div class="storage-bar-label">
        <span>]] .. t("settings.usage") .. [[</span>
        <span id="storage-pct">–</span>
      </div>
      <div class="storage-bar">
        <div class="storage-fill" id="storage-fill" style="width:0%"></div>
      </div>
    </div>
    <div class="set-actions">
      <button class="btn-prim" onclick="saveStorage()">]] .. t("settings.save_storage") .. [[</button>
      <button class="btn-sm"   onclick="expandStorage()" style="margin-left:6px">]] .. t("settings.expand_storage") .. [[</button>
      <div id="storage-msg" class="status-line"></div>
    </div>
  </div>
</div>

<!-- ── NAT-Status ──────────────────────────────────────────── -->
<div class="set-card">
  <div class="set-head"><h3>]] .. t("settings.nat_head") .. [[</h3></div>
  <div class="set-body">
    <div id="nat-info"><div class="empty-hint">]] .. t("settings.loading") .. [[</div></div>
  </div>
</div>

<div class="set-card">
  <div class="set-head"><h3>]] .. t("settings.wifi_head") .. [[</h3>
    <button class="btn-sm" onclick="scanWifi()">]] .. t("settings.wifi_scan") .. [[</button>
  </div>
  <div class="set-body">
    <div id="wifi-current" class="status-line" style="margin-bottom:8px"></div>
    <div id="wifi-list"><div class="empty-hint">]] .. t("settings.loading") .. [[</div></div>
    <div id="wifi-msg" class="status-line" style="margin-top:8px"></div>
  </div>
</div>

<div class="set-card">
  <div class="set-head"><h3>]] .. t("settings.admin_head") .. [[</h3></div>
  <div class="set-body">
    <p class="meta" style="margin:0 0 8px 0">]] .. t("settings.admin_hint") .. [[</p>
    <div id="admin-current" class="status-line" style="margin-bottom:8px"></div>
    <textarea id="admin-seed" rows="3" placeholder="]] .. t("settings.admin_seed_ph") .. [["
              style="width:100%;font-family:monospace;font-size:12px"></textarea>
    <button class="btn-prim" style="margin-top:8px" onclick="saveAdminWallet()">]] .. t("settings.admin_save") .. [[</button>
    <div id="admin-msg" class="status-line" style="margin-top:8px"></div>
  </div>
</div>

</div><!-- set-layout -->

<style>
.set-layout { display:flex; flex-direction:column; gap:12px; }
.set-card { background:var(--sur); border:1px solid var(--brd); border-radius:var(--r); overflow:hidden; }
.set-head { background:var(--sur2); border-bottom:1px solid var(--brd);
            padding:.65rem 1.1rem; display:flex; align-items:center; justify-content:space-between; }
.set-head h3 { margin:0; font-size:13.5px; font-weight:600; }
.set-body { padding:1rem 1.1rem; display:flex; flex-direction:column; gap:0; }

.set-row { display:grid; grid-template-columns:200px 1fr; gap:12px; align-items:start;
           padding:10px 0; border-bottom:1px solid var(--brd); }
.set-row:last-of-type { border-bottom:none; }
.set-label { font-size:12.5px; font-weight:600; color:var(--txt); padding-top:3px; }
.set-hint  { font-size:10.5px; color:var(--muted); font-weight:400; margin-top:1px; }
.set-input { width:100%; padding:7px 10px; background:var(--sur2); border:1px solid var(--brd2);
             border-radius:var(--rs); color:var(--txt); font-size:13px; outline:none; }
.set-input:focus { border-color:var(--acc); }
.set-input-row { display:flex; gap:6px; align-items:center; flex-wrap:wrap; }
.set-input-row .set-input { flex:1; min-width:80px; }
.set-unit  { font-size:12px; color:var(--muted); white-space:nowrap; }
.set-actions { padding-top:12px; display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.btn-prim { padding:7px 14px; background:var(--acc); color:#fff; border:none;
            border-radius:var(--rs); cursor:pointer; font-size:13px; font-weight:500; }
.btn-prim:hover { background:#3b7cf5; }
.btn-sm   { padding:4px 12px; font-size:12px; background:var(--sur2);
            border:1px solid var(--brd2); border-radius:var(--rs); color:var(--dim); cursor:pointer; }
.btn-sm:hover { border-color:var(--acc); color:var(--acc); }
.status-line  { font-size:12.5px; color:var(--muted); }
.empty-hint   { font-size:13px; color:var(--muted); font-style:italic; }

.detect-bar { background:var(--grn-bg); border:1px solid var(--grn-brd);
              border-radius:var(--rs); padding:8px 12px; font-size:12.5px;
              color:var(--grn); margin-bottom:10px; }

.storage-bar-wrap { margin:12px 0 4px; }
.storage-bar-label { display:flex; justify-content:space-between; font-size:11px;
                     color:var(--muted); margin-bottom:4px; }
.storage-bar  { height:6px; background:var(--brd2); border-radius:3px; overflow:hidden; }
.storage-fill { height:100%; background:var(--grn); border-radius:3px; transition:width .5s; }

.nat-row { display:flex; gap:10px; align-items:center; padding:6px 0;
           border-bottom:1px solid var(--brd); font-size:13px; }
.nat-row:last-child { border-bottom:none; }
.nat-key { width:160px; flex-shrink:0; font-size:11px; font-weight:700;
           text-transform:uppercase; letter-spacing:.06em; color:var(--muted); }

@media(max-width:600px){ .set-row { grid-template-columns:1fr; gap:4px; }
  .set-label { padding-top:0; } }
</style>

<script>
const ST = ]] .. (require("cjson.safe").encode({
  scanning_ports = t("settings.scanning_ports"), no_connection = t("settings.no_connection"),
  no_meter = t("settings.no_meter"), writing_env = t("settings.writing_env"),
  meter_saved = t("settings.meter_saved"), nat_unavailable = t("settings.nat_unavailable"), saved_word = t("settings.saved_word"), saving_word = t("settings.saving_word"), fee_skipped = t("settings.fee_skipped"),
  error_word = t("settings.error_word"), reachability = t("settings.reachability"),
  public_ip = t("settings.public_ip"), behind_nat = t("settings.behind_nat"), recommendation = t("settings.recommendation"),
  wifi_connected = t("settings.wifi_connected"), wifi_not_connected = t("settings.wifi_not_connected"),
  wifi_scanning = t("settings.wifi_scanning"), wifi_none = t("settings.wifi_none"), wifi_password = t("settings.wifi_password"),
  wifi_connecting = t("settings.wifi_connecting"), wifi_helper_missing = t("settings.wifi_helper_missing"),
  admin_current = t("settings.admin_current"), admin_none = t("settings.admin_none"),
  admin_need_seed = t("settings.admin_need_seed"), admin_too_few = t("settings.admin_too_few"),
  admin_saving = t("settings.admin_saving"), admin_saved = t("settings.admin_saved"),
}) or "{}") .. [[;
function onProtoChange() {
  const proto = document.getElementById('s-proto').value;
  document.getElementById('serial-fields').style.display = (proto==='http'||proto==='') ? 'none' : '';
  document.getElementById('http-fields').style.display   = proto==='http' ? '' : 'none';
}

function getGPS() {
  navigator.geolocation.getCurrentPosition(pos => {
    document.getElementById('s-lat').value = pos.coords.latitude.toFixed(6);
    document.getElementById('s-lon').value = pos.coords.longitude.toFixed(6);
  }, () => alert('Standort nicht verfügbar'));
}

async function detectMeter() {
  const bar = document.getElementById('detect-status');
  bar.style.display = '';
  bar.textContent = ST.scanning_ports;
  const r = await fetch('/api/v1/meter/detect?probe=1').catch(()=>({ok:false}));
  if (!r.ok) { bar.textContent = ST.no_connection; bar.style.color='var(--red)'; return; }
  const d = await r.json();
  if (d.detected) {
    bar.textContent = '✓ ' + d.detected.port + ' · ' + d.detected.baud + ' Baud · ' + d.detected.protocol.toUpperCase();
    document.getElementById('s-port').value  = d.detected.port;
    document.getElementById('s-baud').value  = d.detected.baud.toString();
    document.getElementById('s-proto').value = d.detected.protocol;
    onProtoChange();
  } else {
    bar.textContent = ST.no_meter + (d.ports||[]).join(', ');
    bar.style.color = 'var(--amber)';
  }
}

async function loadPorts() {
  const r = await fetch('/api/v1/meter/detect').catch(()=>({ok:false}));
  if (!r.ok) return;
  const d = await r.json();
  const sel = document.getElementById('s-port');
  sel.innerHTML = (d.ports||[]).map(p => `<option value="${p}">${p}</option>`).join('');
}

async function saveProfile() {
  const lat = parseFloat(document.getElementById('s-lat').value)||0;
  const lon = parseFloat(document.getElementById('s-lon').value)||0;
  const feeEl = document.getElementById('s-fee');
  const msg = document.getElementById('profile-msg');
  msg.textContent = ST.saving_word || 'Speichere …'; msg.style.color='var(--muted)';
  try {
    // Standort speichern — das ist der Kern und muss für jeden Node funktionieren.
    const rLoc = await fetch('/api/v1/admin/location', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body:JSON.stringify({lat: lat, lon: lon})
    });
    if (!rLoc.ok) {
      const e = await rLoc.json().catch(()=>({}));
      msg.textContent = '✗ ' + (e.error || (ST.error_word||'Fehler')); msg.style.color='var(--red)';
      return;
    }
    // Transit-Gebühr NUR versuchen, wenn ein Wert gesetzt ist. Sie gilt nur für
    // Trafostationen; bei anderen Node-Typen lehnt der Endpunkt ab — das darf die
    // Standort-Speicherung NICHT als Fehler erscheinen lassen.
    const feeVal = feeEl ? parseFloat(feeEl.value) : NaN;
    if (!isNaN(feeVal) && feeVal > 0) {
      const rFee = await fetch('/api/v1/topology/fee', {
        method:'PUT', headers:{'Content-Type':'application/json'},
        body:JSON.stringify({transit_fee_percent: feeVal})
      });
      if (!rFee.ok) {
        // Standort ist gespeichert; Gebühr nicht anwendbar → freundlicher Hinweis.
        msg.textContent = '✓ ' + (ST.saved_word || 'Gespeichert') + ' (' + (ST.fee_skipped || 'Gebühr nur für Trafostationen') + ')';
        msg.style.color='var(--amber)';
        return;
      }
    }
    msg.textContent = '✓ ' + (ST.saved_word || 'Gespeichert'); msg.style.color='var(--grn)';
  } catch(e) {
    msg.textContent = '✗ ' + e.message; msg.style.color='var(--red)';
  }
}

// Beim Öffnen der Einstellungen die gespeicherte Position ins Formular laden.
async function loadOwnLocation() {
  try {
    const r = await fetch('/api/v1/admin/location');
    if (!r.ok) return;
    const d = await r.json();
    const la = document.getElementById('s-lat'), lo = document.getElementById('s-lon');
    if (la && d.lat) la.value = d.lat;
    if (lo && d.lon) lo.value = d.lon;
  } catch(e){}
}
loadOwnLocation();

async function saveMeter() {
  const msg = document.getElementById('meter-msg');
  msg.textContent = ST.writing_env;
  // In Produktion: POST /api/v1/admin/config mit Meter-Parametern
  msg.textContent = ST.meter_saved;
  msg.style.color = 'var(--grn)';
}

async function saveStorage() {
  const offer = parseInt(document.getElementById('s-offer').value)||0;
  const r = await fetch('/api/v1/admin/storage/expand', {
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({offer_gb: offer})
  });
  const msg = document.getElementById('storage-msg');
  const d = await r.json().catch(()=>({}));
  msg.textContent = r.ok ? '✓ ' + (d.message||ST.saved_word) : '✗ ' + (d.error||ST.error_word);
  msg.style.color = r.ok ? 'var(--grn)' : 'var(--red)';
}

async function expandStorage() {
  const gb = parseInt(document.getElementById('s-alloc').value)||0;
  if (!gb) return;
  const r = await fetch('/api/v1/admin/storage/expand', {
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({alloc_gb: gb})
  });
  const msg = document.getElementById('storage-msg');
  msg.textContent = r.ok ? '✓ Live-Erweiterung erfolgreich' : '✗ Fehler';
  msg.style.color = r.ok ? 'var(--grn)' : 'var(--red)';
  loadStorageStats();
}

async function loadStorageStats() {
  const r = await fetch('/api/v1/admin/storage/stats').catch(()=>({ok:false}));
  if (!r.ok) return;
  const d = await r.json();
  const pct = d.alloc_gb > 0 ? Math.round(d.used_gb/d.alloc_gb*100) : 0;
  document.getElementById('storage-pct').textContent = pct + '%';
  document.getElementById('storage-fill').style.width = pct + '%';
  if (d.offer_gb)      document.getElementById('s-offer').value     = d.offer_gb;
  if (d.alloc_gb)      document.getElementById('s-alloc').value     = d.alloc_gb;
  if (d.storage_dir)   document.getElementById('s-storagedir').value = d.storage_dir;
}

async function loadNAT() {
  const r = await fetch('/api/v1/nat').catch(()=>({ok:false}));
  const div = document.getElementById('nat-info');
  if (!r.ok) { div.innerHTML = '<div class="empty-hint">' + ST.nat_unavailable + '</div>'; return; }
  const d = await r.json();
  const rows = [
    [ST.reachability, d.reachability||'–'],
    [ST.public_ip, (d.addrs||[]).find(a=>a.includes('/ip4/')&&!a.includes('192.168')&&!a.includes('127.'))||ST.behind_nat],
    ['DHT-Modus', d.dht_mode||'–'],
    ['NAT-Traversal', d.nat_traversal||'–'],
    [ST.recommendation, d.recommendation||'–'],
  ];
  div.innerHTML = rows.map(([k,v]) =>
    `<div class="nat-row"><span class="nat-key">${k}</span><span>${v}</span></div>`
  ).join('');
}

loadStorageStats();
loadNAT();

// ── WLAN-Umschaltung (über den fundus-helper) ──────────────────────────────
async function loadWifiStatus() {
  try {
    const r = await fetch('/api/v1/admin/system/wifi/status');
    const d = await r.json();
    const cur = document.getElementById('wifi-current');
    if (d.ok && d.status && d.status.ssid) {
      cur.textContent = (ST.wifi_connected || 'Verbunden mit') + ': ' + d.status.ssid;
      cur.style.color = 'var(--grn)';
    } else {
      cur.textContent = ST.wifi_not_connected || 'Nicht verbunden';
      cur.style.color = 'var(--muted)';
    }
  } catch(e) {}
}

async function scanWifi() {
  const list = document.getElementById('wifi-list');
  list.innerHTML = '<div class="empty-hint">' + (ST.wifi_scanning || 'Suche WLANs…') + '</div>';
  try {
    const r = await fetch('/api/v1/admin/system/wifi');
    const d = await r.json();
    if (!d.ok || !d.networks || d.networks.length === 0) {
      list.innerHTML = '<div class="empty-hint">' + (d.error || ST.wifi_none || 'Keine WLANs gefunden') + '</div>';
      return;
    }
    // Nach Signal sortieren, aktives zuerst.
    d.networks.sort((a,b)=> (b.active?1:0)-(a.active?1:0) || b.signal-a.signal);
    list.innerHTML = d.networks.map(function(n){
      const bars = n.signal>=70?'▂▄▆█':n.signal>=45?'▂▄▆':n.signal>=20?'▂▄':'▂';
      const lock = n.secured ? '🔒' : '';
      const active = n.active ? ' <span style="color:var(--grn)">✓</span>' : '';
      return '<div class="nat-row" style="cursor:pointer" onclick="connectWifi(\''+
        n.ssid.replace(/'/g,"\\'")+'\','+(n.secured?'true':'false')+')">'+
        '<span class="nat-key">'+bars+' '+escapeHtmlS(n.ssid)+' '+lock+active+'</span>'+
        '<span style="color:var(--muted);font-size:11px">'+n.signal+'%</span></div>';
    }).join('');
  } catch(e) {
    list.innerHTML = '<div class="empty-hint">'+(ST.wifi_helper_missing||'WLAN-Dienst nicht verfügbar (fundus-helper läuft nicht)')+'</div>';
  }
}

async function connectWifi(ssid, secured) {
  const msg = document.getElementById('wifi-msg');
  let pass = '';
  if (secured) {
    pass = prompt((ST.wifi_password || 'Passwort für') + ' "' + ssid + '":');
    if (pass === null) return;
  }
  msg.textContent = (ST.wifi_connecting || 'Verbinde mit') + ' ' + ssid + '…';
  msg.style.color = 'var(--muted)';
  try {
    const r = await fetch('/api/v1/admin/system/wifi/connect', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ ssid: ssid, password: pass })
    });
    const d = await r.json();
    if (d.ok !== false && r.ok) {
      msg.textContent = '✓ ' + (ST.wifi_connected || 'Verbunden mit') + ' ' + ssid;
      msg.style.color = 'var(--grn)';
      setTimeout(function(){ loadWifiStatus(); scanWifi(); }, 3000);
    } else {
      msg.textContent = '✗ ' + (d.error || 'Verbindung fehlgeschlagen');
      msg.style.color = 'var(--red)';
    }
  } catch(e) {
    msg.textContent = '✗ ' + e.message; msg.style.color = 'var(--red)';
  }
}

// escapeHtmlS: kleine HTML-Escape-Hilfe für WLAN-Namen.
function escapeHtmlS(s){ return (s||'').replace(/[<>&"]/g,function(c){return{'<':'&lt;','>':'&gt;','&':'&amp;','"':'&quot;'}[c];}); }

loadWifiStatus();
scanWifi();

// ── Admin-Wallet festlegen (aus 30 Seed-Wörtern, nur lokal) ────────────────
async function loadAdminWallet() {
  try {
    const r = await fetch('/api/v1/admin/wallet');
    const d = await r.json();
    const cur = document.getElementById('admin-current');
    if (d.admin_wallet) {
      cur.innerHTML = (ST.admin_current || 'Aktuelle Admin-Wallet') + ': <code>' + d.admin_wallet + '</code>';
      cur.style.color = 'var(--grn)';
    } else {
      cur.textContent = ST.admin_none || 'Noch keine Admin-Wallet festgelegt';
      cur.style.color = 'var(--muted)';
    }
  } catch(e) {}
}

async function saveAdminWallet() {
  const seed = document.getElementById('admin-seed').value.trim();
  const msg = document.getElementById('admin-msg');
  if (!seed) { msg.textContent = ST.admin_need_seed || 'Bitte die Seed-Wörter eingeben.'; msg.style.color='var(--red)'; return; }
  const wordCount = seed.split(/\s+/).length;
  if (wordCount < 12) {
    msg.textContent = (ST.admin_too_few || 'Zu wenige Wörter') + ' (' + wordCount + ')';
    msg.style.color = 'var(--red)';
    return;
  }
  msg.textContent = ST.admin_saving || 'Speichere…'; msg.style.color = 'var(--muted)';
  try {
    const r = await fetch('/api/v1/admin/wallet', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ seed_words: seed })
    });
    const d = await r.json();
    if (r.ok && d.ok) {
      msg.innerHTML = '✓ ' + (ST.admin_saved || 'Admin-Wallet gesetzt') + ': <code>' + d.admin_wallet + '</code>';
      msg.style.color = 'var(--grn)';
      document.getElementById('admin-seed').value = ''; // Wörter aus dem Feld löschen
      loadAdminWallet();
    } else {
      msg.textContent = '✗ ' + (d.error || 'Fehler');
      msg.style.color = 'var(--red)';
    }
  } catch(e) { msg.textContent = '✗ ' + e.message; msg.style.color = 'var(--red)'; }
}
loadAdminWallet();
onProtoChange();
// ── Navigation-Sichtbarkeit: Tabelle aufbauen + Checkboxen verdrahten ──
(function(){
  const body = document.getElementById("nav-vis-body");
  if (!body || !window.NAV_ITEMS) return;
  const v = window.navGetVis();
  window.NAV_ITEMS.forEach(function(it){
    const tr = document.createElement("tr");
    const name = document.createElement("td");
    name.innerHTML = '<span class="nvi">'+it.icon+'</span> '+it.label+(it.beta?' <span class="wip-pill">BETA</span>':'');
    tr.appendChild(name);
    ["landing","burger","quick"].forEach(function(zone){
      const td = document.createElement("td");
      td.style.textAlign = "center";
      const cb = document.createElement("input");
      cb.type = "checkbox";
      cb.checked = !!(v[it.key] && v[it.key][zone]);
      cb.onchange = function(){
        const cur = window.navGetVis();
        if (!cur[it.key]) cur[it.key] = {};
        cur[it.key][zone] = cb.checked;
        window.navSetVis(cur);
      };
      td.appendChild(cb);
      tr.appendChild(td);
    });
    body.appendChild(tr);
  });
})();
</script>
]])

  render.footer()
end

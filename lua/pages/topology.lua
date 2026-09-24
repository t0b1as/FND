-- pages/topology.lua
-- Netz-Topologie: Node-Profil, Verbindungen, Routing, Trafo-Gebühren
-- Alle Spannungsebenen (LV/MV/HV/EHV) konfigurierbar.
local render = require "render"
local cjson  = require "cjson.safe"

return function()
  ngx.header["Content-Type"] = "text/html"
  local t    = render.header("topology.title", "topology")

  -- Eigenes Profil laden
  local own = render.api_get("/v1/topology/profile") or {}
  local node_type = own.type or "consumer"
  local voltage   = own.voltage or "lv"
  local is_sub    = (node_type == "substation")
  local is_hv     = (voltage == "hv" or voltage == "ehv")

  -- Trafostationen laden
  local sub_data = render.api_get("/v1/topology/substations") or {}
  local subs     = sub_data.substations or {}

  -- Gebühren laden
  local fees_data = render.api_get("/v1/grid/fee/rates") or {}
  local fee_rates = fees_data.rates or {}

ngx.print([[
<div class="topo-layout">

<!-- ============================================================
     Eigenes Node-Profil
     ============================================================ -->
<section class="topo-section">
  <h2>]] .. t("topology.my_node_profile") .. [[</h2>

  <div class="info-grid">
    <div class="info-box">
      <label>]] .. t("topology.peer_id") .. [[</label>
      <div class="mono small" id="own-peer-id">…wird geladen…</div>
    </div>
    <div class="info-box">
      <label>]] .. t("topology.wallet") .. [[</label>
      <div class="mono small" id="own-wallet">…</div>
    </div>
  </div>

  <!-- Node-Typ konfigurieren -->
  <form class="form-grid" onsubmit="saveProfile(event)">
    <h3>]] .. t("topology.configure_profile") .. [[</h3>

    <div class="field">
      <label>]] .. t("topology.node_type") .. [[</label>
      <select id="node-type" onchange="onTypeChange()">
        <option value="consumer"   ]] .. (node_type=="consumer"   and "selected" or "") .. [[>
          Verbraucher (Consumer)</option>
        <option value="prosumer"   ]] .. (node_type=="prosumer"   and "selected" or "") .. [[>
          Erzeuger+Verbraucher (Prosumer)</option>
        <option value="substation" ]] .. (node_type=="substation" and "selected" or "") .. [[>
          Trafostation (Substation) – eigenes Wallet</option>
        <option value="power_plant" ]] .. (node_type=="power_plant" and "selected" or "") .. [[>
          Kraftwerk / Windpark (Power Plant)</option>
      </select>
    </div>

    <div class="field">
      <label>]] .. t("topology.voltage_level") .. [[</label>
      <select id="voltage-level">
        <option value="lv"  ]] .. (voltage=="lv"  and "selected" or "") .. [[>
          LV – Niederspannung 230/400 V (Haushalt)</option>
        <option value="mv"  ]] .. (voltage=="mv"  and "selected" or "") .. [[>
          MV – Mittelspannung 10-30 kV (Gewerbe/ONT-Ausgang)</option>
        <option value="hv"  ]] .. (voltage=="hv"  and "selected" or "") .. [[>
          HV – Hochspannung 110-220 kV (Umspannwerk)</option>
        <option value="ehv" ]] .. (voltage=="ehv" and "selected" or "") .. [[>
          EHV – Höchstspannung 380 kV (ÜNB)</option>
      </select>
    </div>

    <div class="field-row">
      <div class="field">
        <label>GPS Breitengrad</label>
        <input type="number" id="node-lat" step="0.000001"
               value="]] .. (own.lat or 0) .. [[" placeholder="48.137154">
      </div>
      <div class="field">
        <label>GPS Längengrad</label>
        <input type="number" id="node-lon" step="0.000001"
               value="]] .. (own.lon or 0) .. [[" placeholder="11.575382">
      </div>
    </div>

    <div class="field">
      <label>]] .. t("topology.parent_substation") .. [[</label>
      <input type="text" id="parent-substation"
             value="]] .. (own.parent_substation_id or "") .. [["
             placeholder="12D3KooW… (Peer-ID des ONT/Umspannwerks)">
      <div class="meta">]] .. t("topology.parent_hint") .. [[</div>
    </div>

    <!-- Trafo-Durchleitungsgebühr – sichtbar für ALLE Spannungsebenen -->
    <div class="field" id="fee-section">
      <label>
        Durchleitungsgebühr
        <span class="meta" id="hv-note" style="color:var(--color-warning,#d97706)">
          (HV/EHV: wird im Routing angezeigt, aber nicht automatisch abgezogen –
          muss bilateral vereinbart werden)
        </span>
      </label>
      <div class="input-row">
        <input type="number" id="transit-fee" step="0.01" min="0" max="10"
               value="]] .. (own.transit_fee_percent or 0) .. [["
               placeholder="0.50">
        <span class="unit">% pro Transaktion</span>
      </div>
      <div class="meta">
        Beispiel: 0.3% = bei 100 FND Transaktion → 0,30 FND Gebühr an diesen Node
      </div>
    </div>

    <button class="btn btn-primary" type="submit">]] .. t("topology.save_profile") .. [[</button>
    <div id="profile-status" class="meta" style="margin-top:4px"></div>
  </form>
</section>

<!-- ============================================================
     Verbindungen zu Nachbar-Nodes
     ============================================================ -->
<section class="topo-section">
  <h2>]] .. t("topology.connected_nodes") .. [[</h2>
  <div class="meta" style="margin-bottom:8px">
    Jeder Node tradet nur mit direkt elektrisch verbundenen Peers.
    Für Handel über mehrere Hops wird die Route automatisch berechnet.
  </div>

  <div id="connections-list"></div>

  <form class="form-grid" onsubmit="addConnection(event)" style="margin-top:1rem">
    <h3>]] .. t("topology.add_connection") .. [[</h3>
    <div class="field">
      <label>]] .. t("topology.neighbor_peer") .. [[</label>
      <input type="text" id="conn-peer" placeholder="12D3KooW…">
    </div>
    <div class="field-row">
      <div class="field">
        <label>]] .. t("topology.neighbor_lat") .. [[</label>
        <input type="number" id="conn-lat" step="0.000001" placeholder="48.14">
      </div>
      <div class="field">
        <label>]] .. t("topology.neighbor_lon") .. [[</label>
        <input type="number" id="conn-lon" step="0.000001" placeholder="11.58">
      </div>
    </div>
    <div class="field">
      <label>]] .. t("topology.conn_voltage") .. [[</label>
      <select id="conn-voltage">
        <option value="lv">LV 230/400 V</option>
        <option value="mv">MV 10-30 kV</option>
        <option value="hv">HV 110-220 kV</option>
        <option value="ehv">EHV 380 kV</option>
      </select>
    </div>
    <div class="field">
      <label>]] .. t("topology.neighbor_wallet") .. [[</label>
      <input type="text" id="conn-wallet" placeholder="0x…">
    </div>
    <button class="btn" type="submit">]] .. t("topology.add_connection") .. [[</button>
    <div id="conn-status" class="meta" style="margin-top:4px"></div>
  </form>
</section>

<!-- ============================================================
     Routing-Rechner
     ============================================================ -->
<section class="topo-section">
  <h2>]] .. t("topology.calc_path") .. [[</h2>

  <div class="field-row">
    <div class="field">
      <label>]] .. t("topology.target_peer") .. [[</label>
      <input type="text" id="route-to" placeholder="12D3KooW…">
    </div>
    <div class="field">
      <label>]] .. t("topology.amount_fnd") .. [[</label>
      <input type="number" id="route-price" step="0.01" value="100">
    </div>
    <button class="btn" onclick="calcRoute()">]] .. t("topology.calc_route") .. [[</button>
  </div>

  <div id="route-result" style="margin-top:1rem"></div>
</section>

<!-- ============================================================
     Bekannte Trafostationen
     ============================================================ -->
<section class="topo-section">
  <h2>]] .. t("topology.known_substations") .. [[ (]] .. #subs .. [[)</h2>
  <div id="substations-list">]])

  if #subs == 0 then
    ngx.print([[<div class="meta">]] .. t("topology.no_substations") .. [[</div>]])
  else
    for _, sub in ipairs(subs) do
      local fee = sub.transit_fee_percent or 0
      ngx.print(string.format([[
    <div class="sub-card">
      <div class="sub-header">
        <span class="badge badge-%s">%s</span>
        <span class="mono small">%s</span>
      </div>
      <div class="sub-info">
        <span>Gebühr: <strong>%.2f%%</strong></span>
        <span>%s</span>
        <span>%.4f°N %.4f°E</span>
      </div>
      <div class="mono small muted">Wallet: %s</div>
    </div>]],
        sub.voltage or "lv",
        string.upper(sub.voltage or "lv"),
        (sub.peer_id or ""):sub(1,16) .. "…",
        fee,
        sub.operator_id or "–",
        sub.lat or 0, sub.lon or 0,
        (sub.wallet_address or "nicht konfiguriert"):sub(1,20) .. "…"
      ))
    end
  end

ngx.print([[
  </div>
</section>

</div><!-- topo-layout -->

<script>
const TT = ]] .. (require("cjson.safe").encode({
  saving = t("topology.saving"), saved = t("topology.saved"), error_prefix = t("topology.error_prefix"), error_word = t("topology.error_word"),
  unknown = t("topology.unknown"), peer_required = t("topology.peer_required"),
  calc_distance = t("topology.calc_distance"), added_prefix = t("topology.added_prefix"),
  connections_suffix = t("topology.connections_suffix"), no_connections = t("topology.no_connections"),
}) or "{}") .. [[
// ======================================================================
//  Profil laden
// ======================================================================
async function loadProfile() {
  const r = await fetch('/api/v1/topology/profile');
  if (!r.ok) return;
  const p = await r.json();
  document.getElementById('own-peer-id').textContent = p.peer_id || '–';
  document.getElementById('own-wallet').textContent  = p.wallet_address || '–';
  renderConnections(p.connections || []);
}

// ======================================================================
//  Verbindungen anzeigen
// ======================================================================
function renderConnections(conns) {
  const div = document.getElementById('connections-list');
  if (!conns.length) {
    div.innerHTML = '<div class="meta">' + TT.no_connections + '</div>';
    return;
  }
  div.innerHTML = conns.map(c => `
    <div class="conn-card">
      <div class="conn-info">
        <span class="badge badge-${c.voltage}">${(c.voltage||'lv').toUpperCase()}</span>
        <span class="mono small">${c.peer_id.slice(0,16)}…</span>
        <span class="meta">${(c.distance_m/1000).toFixed(2)} km
          <span class="muted">(${c.distance_source})</span></span>
      </div>
      <button class="btn btn-sm btn-danger"
              onclick="removeConn('${c.peer_id}')">✕</button>
    </div>`).join('');
}

// ======================================================================
//  Node-Typ Change
// ======================================================================
function onTypeChange() {
  const type    = document.getElementById('node-type').value;
  const voltage = document.getElementById('voltage-level').value;
  const isHV    = voltage === 'hv' || voltage === 'ehv';
  const note    = document.getElementById('hv-note');
  if (note) note.style.display = isHV ? 'inline' : 'none';
}

// ======================================================================
//  Profil speichern
// ======================================================================
async function saveProfile(e) {
  e.preventDefault();
  const st = document.getElementById('profile-status');
  st.textContent = TT.saving;

  const body = {
    type:                document.getElementById('node-type').value,
    voltage:             document.getElementById('voltage-level').value,
    lat:           parseFloat(document.getElementById('node-lat').value) || 0,
    lon:           parseFloat(document.getElementById('node-lon').value) || 0,
    parent_substation_id: document.getElementById('parent-substation').value.trim(),
  };

  // Profil-Update (noch kein separater PUT-Endpoint – über fee-endpoint)
  const fee = parseFloat(document.getElementById('transit-fee').value) || 0;

  // Gebühr separat setzen (funktioniert für alle Spannungsebenen)
  const feeResp = await fetch('/api/v1/topology/fee', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ transit_fee_percent: fee }),
  });
  const feeData = await feeResp.json();

  if (feeResp.ok) {
    st.textContent = TT.saved;
    if (feeData.warning) {
      st.textContent += ' – Hinweis: ' + feeData.warning;
    }
  } else {
    st.textContent = TT.error_prefix + (feeData.error || TT.unknown);
  }
}

// ======================================================================
//  Verbindung hinzufügen
// ======================================================================
async function addConnection(e) {
  e.preventDefault();
  const st = document.getElementById('conn-status');
  const body = {
    peer_id: document.getElementById('conn-peer').value.trim(),
    lat:     parseFloat(document.getElementById('conn-lat').value) || 0,
    lon:     parseFloat(document.getElementById('conn-lon').value) || 0,
    voltage: document.getElementById('conn-voltage').value,
    wallet:  document.getElementById('conn-wallet').value.trim(),
  };
  if (!body.peer_id) { st.textContent = TT.peer_required; return; }

  st.textContent = TT.calc_distance;
  const r = await fetch('/api/v1/topology/connections', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const d = await r.json();
  if (r.ok) {
    st.textContent = TT.added_prefix + d.connections + TT.connections_suffix;
    loadProfile();
  } else {
    st.textContent = '✗ ' + (d.error || TT.error_word);
  }
}

async function removeConn(peerID) {
  await fetch('/api/v1/topology/connections/' + peerID, { method: 'DELETE' });
  loadProfile();
}

// ======================================================================
//  Route berechnen
// ======================================================================
async function calcRoute() {
  const to    = document.getElementById('route-to').value.trim();
  const price = document.getElementById('route-price').value;
  if (!to) return;

  const r = await fetch(`/api/v1/topology/route?to=${encodeURIComponent(to)}&price=${price}`);
  const d = await r.json();
  const div = document.getElementById('route-result');

  if (!r.ok) {
    div.innerHTML = `<div class="error-box">${d.error}</div>`;
    return;
  }

  const route   = d.route;
  const mode_labels = {
    direct_lv:     '⚡ Direkthandel (gleiche Trafostation)',
    local_mv:      '🔌 Lokales MV-Netz (1 Trafo-Hop)',
    regional_hv:   '🗺 Regionales HV-Netz (mehrere Hops)',
    long_distance: '🌐 Fernübertragung (ÜNB)',
    unknown:       '❓ Unbekannte Topologie',
  };

  const hopsHTML = (route.hops || []).map((h,i) => `
    <div class="hop ${i===0||i===route.hops.length-1 ? 'hop-endpoint':'hop-relay'}">
      <span class="badge badge-${h.voltage||'lv'}">${(h.voltage||'lv').toUpperCase()}</span>
      <span class="mono small">${h.peer_id ? h.peer_id.slice(0,12)+'…' : '?'}</span>
      <span class="meta">${h.node_type||''}</span>
      ${h.fee_percent ? `<span class="fee-badge">${h.fee_percent.toFixed(2)}%</span>` : ''}
    </div>`).join('<div class="hop-arrow">↓</div>');

  div.innerHTML = `
    <div class="route-card">
      <div class="route-mode">${mode_labels[route.trade_mode] || route.trade_mode}</div>
      <div class="route-hops">${hopsHTML}</div>
      <div class="route-fees">
        <span>Gesamtgebühr: <strong>${(route.fees?.total_fee_percent||0).toFixed(3)}%</strong></span>
        <span>Gebührbetrag: <strong>${(route.fees?.total_fee_amount||0).toFixed(4)} FND</strong></span>
        <span>Nettobetrag:  <strong>${(route.fees?.net_amount||0).toFixed(4)} FND</strong></span>
      </div>
    </div>`;
}

// Init
window.addEventListener('DOMContentLoaded', () => {
  loadProfile();
  onTypeChange();
});
</script>

<style>
.topo-layout   { display:flex; flex-direction:column; gap:1.5rem; }
.topo-section  { background:var(--surface); border:1px solid var(--border);
                 border-radius:var(--radius); padding:1.25rem; }
.topo-section h2 { margin:0 0 1rem; font-size:1.1rem; }
.info-grid     { display:grid; grid-template-columns:1fr 1fr; gap:8px; margin-bottom:1rem; }
.form-grid     { display:flex; flex-direction:column; gap:8px; }
.field-row     { display:flex; gap:8px; }
.field-row .field { flex:1; }
.input-row     { display:flex; align-items:center; gap:8px; }
.unit          { color:var(--muted); font-size:13px; white-space:nowrap; }
.hv-note       { font-size:11px; font-style:italic; }

/* Badges für Spannungsebenen */
.badge-lv  { background:#dcfce7; color:#166534; }
.badge-mv  { background:#dbeafe; color:#1e40af; }
.badge-hv  { background:#fef3c7; color:#92400e; }
.badge-ehv { background:#fce7f3; color:#9d174d; }
.badge     { padding:2px 8px; border-radius:9999px; font-size:11px;
             font-weight:600; display:inline-block; }

/* Verbindungen */
.conn-card  { display:flex; justify-content:space-between; align-items:center;
              padding:8px; border:1px solid var(--border); border-radius:var(--radius);
              margin-bottom:4px; }
.conn-info  { display:flex; align-items:center; gap:8px; }
.btn-sm     { padding:2px 8px; font-size:12px; }
.btn-danger { background:#fee2e2; color:#991b1b; border-color:#fca5a5; }

/* Substations */
.sub-card   { background:var(--bg); border:1px solid var(--border);
              border-radius:var(--radius); padding:12px; margin-bottom:8px; }
.sub-header { display:flex; align-items:center; gap:8px; margin-bottom:6px; }
.sub-info   { display:flex; gap:12px; font-size:13px; margin-bottom:4px; }

/* Routing */
.route-card  { background:var(--bg); border:1px solid var(--border);
               border-radius:var(--radius); padding:12px; }
.route-mode  { font-weight:600; margin-bottom:8px; }
.route-hops  { display:flex; flex-direction:column; gap:0; margin:8px 0; }
.hop         { display:flex; align-items:center; gap:8px; padding:6px 8px;
               background:var(--surface); border-radius:4px; }
.hop-endpoint { font-weight:600; }
.hop-relay   { opacity:0.85; }
.hop-arrow   { text-align:center; color:var(--muted); font-size:12px; }
.fee-badge   { background:#fef3c7; color:#92400e; padding:1px 6px;
               border-radius:9999px; font-size:11px; margin-left:auto; }
.route-fees  { display:flex; gap:16px; font-size:13px; border-top:1px solid var(--border);
               padding-top:8px; margin-top:8px; }
</style>
]])

  render.footer()
end

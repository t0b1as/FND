-- pages/admin.lua – Admin-Panel
local render = require "render"

return function()
  ngx.header["Content-Type"] = "text/html"
  local t = render.header("admin.title", "admin")

  local ver,_ = render.api_get("/v1/admin/version")
  local sto,_ = render.api_get("/v1/admin/storage/stats")

  ngx.print(string.format([[
<div class="adm-layout">

<!-- Version & Update -->
<div class="adm-card">
  <div class="adm-head"><h3>]] .. t("admin.sw_version") .. [[</h3></div>
  <div class="adm-body">
    <div class="ver-row">
      <div class="ver-block">
        <span class="ver-label">]] .. t("admin.current") .. [[</span>
        <span class="ver-val">%s</span>
      </div>
      <div class="ver-block">
        <span class="ver-label">]] .. t("admin.go_build") .. [[</span>
        <span class="ver-val small-mono">%s</span>
      </div>
      <div class="ver-block">
        <span class="ver-label">]] .. t("admin.status") .. [[</span>
        <span class="ver-val" id="ver-status" style="color:var(--grn)">✓ Aktuell</span>
      </div>
    </div>
    <div class="adm-actions">
      <button class="btn-prim" onclick="checkUpdate()">]] .. t("admin.check_updates") .. [[</button>
      <div id="update-status" class="status-line"></div>
    </div>
    <details style="margin-top:12px">
      <summary class="det-sum">]] .. t("admin.manual_manifest") .. [[</summary>
      <div style="margin-top:10px;display:flex;flex-direction:column;gap:8px">
        <textarea id="update-manifest" rows="3" class="adm-ta"
          placeholder='{"version":"R002","url":"https://…","hash":"…","signature":"…"}'></textarea>
        <button class="btn-warn" onclick="applyUpdate()">]] .. t("admin.apply_update") .. [[</button>
      </div>
    </details>
  </div>
</div>

<!-- Grid-Wallets -->
<div class="adm-card">
  <div class="adm-head">
    <h3>]] .. t("admin.grid_wallets") .. [[</h3>
    <button class="btn-sm" onclick="loadGridWallets()">↻</button>
  </div>
  <div class="adm-body">
    <div id="grid-wallets"><div class="empty-hint">]] .. t("admin.loading") .. [[</div></div>
    <details style="margin-top:12px">
      <summary class="det-sum">]] .. t("admin.init_grid_wallets") .. [[</summary>
      <div style="margin-top:8px;font-size:12px;color:var(--muted);margin-bottom:8px">
        Erstellt Wallet-Signaturen für alle Netzbetreiber-PLZ. Nur einmal nötig.
      </div>
      <button class="btn-warn" onclick="initGrid()">]] .. t("admin.init_grid") .. [[</button>
      <div id="grid-init-msg" class="status-line"></div>
    </details>
  </div>
</div>

<!-- Storage -->
<div class="adm-card">
  <div class="adm-head">
    <h3>]] .. t("admin.storage_provider") .. [[</h3>
    <button class="btn-sm" onclick="loadStorage()">↻</button>
  </div>
  <div class="adm-body">
    <div class="sto-grid">
      <div class="sto-s"><span class="sto-l">]] .. t("admin.offered") .. [[</span><span class="sto-v" id="sto-offer">%d GB</span></div>
      <div class="sto-s"><span class="sto-l">]] .. t("admin.allocated") .. [[</span><span class="sto-v"  id="sto-alloc">%d GB</span></div>
      <div class="sto-s"><span class="sto-l">]] .. t("admin.used") .. [[</span><span class="sto-v"    id="sto-used">%.1f GB</span></div>
      <div class="sto-s"><span class="sto-l">]] .. t("admin.chunks_local") .. [[</span><span class="sto-v" id="sto-chunks">%d</span></div>
      <div class="sto-s"><span class="sto-l">]] .. t("admin.replica_peers") .. [[</span><span class="sto-v" id="sto-peers">%d</span></div>
      <div class="sto-s">
        <span class="sto-l">]] .. t("admin.provider_stake") .. [[</span>
        <span class="sto-v">10 FND
          <span class="stake-todo">▲ TODO: auf 100 FND erhöhen wenn &gt;100 Provider</span>
        </span>
      </div>
    </div>
    <div class="sto-bar-wrap">
      <div class="sto-bar-lbl"><span>]] .. t("admin.usage") .. [[</span><span id="sto-pct">–</span></div>
      <div class="sto-bar"><div class="sto-fill" id="sto-fill"></div></div>
    </div>
    <div class="adm-actions" style="margin-top:10px">
      <span class="status-line" style="color:var(--muted)">]] .. t("admin.expand_offer") .. [[</span>
      <input type="number" id="expand-offer" placeholder="]] .. t("admin.offer_gb_ph") .. [[" min="1" class="adm-num">
      <button class="btn-prim" onclick="expandStorage()">]] .. t("admin.set_offer") .. [[</button>
      <div id="expand-msg" class="status-line"></div>
    </div>
  </div>
</div>

<!-- Konfiguration -->
<div class="adm-card">
  <div class="adm-head">
    <h3>]] .. t("admin.config_check") .. [[</h3>
    <button class="btn-sm" onclick="loadConfig()">↻</button>
  </div>
  <div class="adm-body">
    <div id="cfg-list"><div class="empty-hint">]] .. t("admin.loading") .. [[</div></div>
  </div>
</div>

<div class="adm-card">
  <div class="adm-head">
    <h3>]] .. t("admin.maintenance") .. [[</h3>
  </div>
  <div class="adm-body">
    <p class="adm-hint">Verwaiste Angebote sind Altbestände ohne Signatur (vor
    Einführung der persistenten Identität erstellt). Sie können keiner aktuellen
    Node-ID zugeordnet werden. Hier werden sie netzweit gelöscht.</p>
    <div class="adm-actions">
      <button class="btn-warn" onclick="purgeOrphans()">]] .. t("admin.purge_orphans") .. [[</button>
      <span id="purge-status" class="status-line"></span>
    </div>
    <p class="adm-hint" style="margin-top:1rem">Unfertige Uploads (abgebrochene
    grosse Dateien/Videos) belegen Speicher auf der SSD. Hier werden sie entfernt.</p>
    <div class="adm-actions">
      <button class="btn-warn" onclick="purgeUploads()">]] .. t("admin.purge_uploads") .. [[</button>
      <span id="upload-purge-status" class="status-line"></span>
    </div>
  </div>
</div>

<div class="adm-card">
  <div class="adm-head"><h3>]] .. t("admin.external_drives") .. [[</h3></div>
  <div class="adm-body">
    <p class="adm-hint">]] .. t("admin.drives_hint") .. [[
    Aktuell: <code id="cur-datadir">…</code></p>
    <div id="drives-list"><div class="empty-hint">]] .. t("admin.loading") .. [[</div></div>
    <div id="drives-status" style="margin-top:8px;font-size:0.85rem"></div>
    <div style="margin-top:14px"><strong>]] .. t("admin.shared_volumes") .. [[</strong></div>
    <div id="volumes-list" style="margin-top:6px"><div class="empty-hint">…</div></div>
    <div style="margin-top:14px"><strong>]] .. t("admin.cleanup_title") .. [[</strong></div>
    <p class="adm-hint">]] .. t("admin.cleanup_hint") .. [[</p>
    <div class="adm-actions">
      <button class="btn-sm" onclick="cleanupStorage('scan')">]] .. t("admin.cleanup_scan") .. [[</button>
      <button class="btn-sm btn-warn" onclick="cleanupStorage('tmp')">]] .. t("admin.cleanup_tmp") .. [[</button>
      <button class="btn-sm btn-warn" onclick="cleanupStorage('orphans')">]] .. t("admin.cleanup_orphans") .. [[</button>
    </div>
    <div id="cleanup-msg" class="status-line" style="margin-top:6px"></div>
  </div>
</div>

<!-- WLAN-Verwaltung (nur wenn fundus-helper laeuft) -->
<div class="adm-card" id="wifi-card" style="display:none">
  <div class="adm-head"><h3>]] .. t("admin.wifi_title") .. [[</h3></div>
  <div class="adm-body">
    <p class="adm-hint">]] .. t("admin.wifi_hint") .. [[</p>
    <div id="wifi-current" style="margin-bottom:8px;font-size:0.9rem"></div>
    <div class="adm-actions">
      <button class="btn-sm" onclick="scanWifi()">]] .. t("admin.wifi_scan") .. [[</button>
    </div>
    <div id="wifi-list" style="margin-top:10px"></div>
    <div id="wifi-msg" class="status-line" style="margin-top:6px"></div>
  </div>
</div>

<!-- Externe Laufwerke mounten (nur wenn fundus-helper laeuft) -->
<div class="adm-card" id="mount-card" style="display:none">
  <div class="adm-head"><h3>]] .. t("admin.mount_title") .. [[</h3></div>
  <div class="adm-body">
    <p class="adm-hint">]] .. t("admin.mount_hint") .. [[</p>
    <div class="adm-actions">
      <button class="btn-sm" onclick="scanBlockDevices()">]] .. t("admin.mount_scan") .. [[</button>
    </div>
    <div id="blockdev-list" style="margin-top:10px"></div>
    <div id="mount-msg" class="status-line" style="margin-top:6px"></div>
  </div>
</div>

</div>

<style>
.adm-layout{display:flex;flex-direction:column;gap:12px}
.adm-card{background:var(--sur);border:1px solid var(--brd);border-radius:var(--r);overflow:hidden}
.adm-head{background:var(--sur2);border-bottom:1px solid var(--brd);padding:.65rem 1.1rem;
  display:flex;align-items:center;justify-content:space-between}
.adm-head h3{margin:0;font-size:13.5px;font-weight:600}
.adm-body{padding:1rem 1.1rem}
.ver-row{display:flex;gap:20px;flex-wrap:wrap}
.ver-block{display:flex;flex-direction:column;gap:3px}
.ver-label{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.07em;color:var(--muted)}
.ver-val{font-size:1.35rem;font-weight:800}
.ver-val.small-mono{font-size:.85rem;font-family:var(--mono);font-weight:400;color:var(--dim)}
.adm-actions{margin-top:12px;display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.btn-prim{padding:7px 14px;background:var(--acc);color:#fff;border:none;border-radius:var(--rs);
  cursor:pointer;font-size:13px;font-weight:500}
.btn-prim:hover{background:#3b7cf5}
.btn-warn{padding:5px 13px;background:var(--amber-bg);color:var(--amber);
  border:1px solid rgba(255,179,0,.3);border-radius:var(--rs);cursor:pointer;font-size:12.5px}
.btn-sm{padding:4px 12px;font-size:12px;background:var(--sur2);border:1px solid var(--brd2);
  border-radius:var(--rs);color:var(--dim);cursor:pointer}
.btn-sm:hover{border-color:var(--acc);color:var(--acc)}
.adm-ta{width:100%%;padding:7px 10px;background:var(--sur2);border:1px solid var(--brd2);
  border-radius:var(--rs);color:var(--txt);font-size:12px;font-family:var(--mono);
  resize:vertical;outline:none}
.adm-ta:focus{border-color:var(--acc)}
.adm-num{width:70px;padding:5px 8px;background:var(--sur2);border:1px solid var(--brd2);
  border-radius:var(--rs);color:var(--txt);font-size:12.5px;outline:none}
.det-sum{font-size:12.5px;color:var(--muted);cursor:pointer;user-select:none}
.det-sum:hover{color:var(--dim)}
.status-line{font-size:12.5px;color:var(--muted)}
.empty-hint{font-size:13px;color:var(--muted);font-style:italic}
.sto-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:8px}
.sto-s{background:var(--sur2);border:1px solid var(--brd);border-radius:var(--rs);padding:8px 10px}
.sto-l{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.07em;
  color:var(--muted);display:block;margin-bottom:2px}
.sto-v{font-size:1.1rem;font-weight:700;color:var(--txt)}
.stake-todo{display:block;font-size:9px;color:var(--amber);font-weight:600;margin-top:2px}
.sto-bar-wrap{margin-top:10px}
.sto-bar-lbl{display:flex;justify-content:space-between;font-size:11px;color:var(--muted);margin-bottom:4px}
.sto-bar{height:6px;background:var(--brd2);border-radius:3px;overflow:hidden}
.sto-fill{height:100%%;background:var(--grn);border-radius:3px;transition:width .5s}
.gw-row{display:flex;align-items:center;gap:10px;padding:7px 0;
  border-bottom:1px solid var(--brd);font-size:12.5px}
.gw-row:last-child{border-bottom:none}
.gw-plz{font-family:var(--mono);font-size:11px;color:var(--muted);width:60px;flex-shrink:0}
.gw-addr{font-family:var(--mono);font-size:10.5px;color:var(--dim);flex:1;
  overflow:hidden;text-overflow:ellipsis}
.gw-fee{font-size:11px;color:var(--amber);white-space:nowrap}
.cfg-row{display:flex;align-items:center;gap:8px;padding:6px 0;
  border-bottom:1px solid var(--brd);font-size:12.5px}
.cfg-row:last-child{border-bottom:none}
.cfg-key{width:220px;flex-shrink:0;font-family:var(--mono);font-size:11.5px;color:var(--muted)}
.ok{color:var(--grn)} .err{color:var(--red)}
@media(max-width:600px){.sto-grid{grid-template-columns:1fr 1fr}}
</style>

<script>
const AT = ]] .. (require("cjson.safe").encode({
  checking = t("admin.checking"), not_reachable = t("admin.not_reachable"),
  update_avail = t("admin.update_avail"), new_update = t("admin.new_update"),
  no_update = t("admin.no_update"), initializing = t("admin.initializing"),
  loading = t("admin.loading"), no_data = t("admin.no_data"), not_available = t("admin.not_available"),
  no_grid_wallets = t("admin.no_grid_wallets"), no_wallets_init = t("admin.no_wallets_init"),
  no_drives = t("admin.no_drives"), under = t("admin.under"), initialized = t("admin.initialized"), error_word = t("admin.error_word"),
  enter_gb = t("admin.enter_gb"), expanding = t("admin.expanding"), offer_set = t("admin.offer_set"), admin_login_needed = t("admin.admin_login_needed"), admin_auth_500 = t("admin.admin_auth_500"),
  cleanup_title = t("admin.cleanup_title"), cleanup_hint = t("admin.cleanup_hint"), cleanup_scan = t("admin.cleanup_scan"), cleanup_tmp = t("admin.cleanup_tmp"), cleanup_orphans = t("admin.cleanup_orphans"), cleanup_confirm = t("admin.cleanup_confirm"), cleanup_running = t("admin.cleanup_running"), cleanup_found = t("admin.cleanup_found"), cleanup_orphan_word = t("admin.cleanup_orphan_word"), cleanup_deleted_word = t("admin.cleanup_deleted_word"), scan_own = t("admin.scan_own"), scan_host = t("admin.scan_host"), scan_cache = t("admin.scan_cache"),
  drive_use = t("admin.drive_use"), drive_primary = t("admin.drive_primary"), drive_unshare = t("admin.drive_unshare"), drive_unshare_q = t("admin.drive_unshare_q"), drive_subfolder_q = t("admin.drive_subfolder_q"), drive_sharing = t("admin.drive_sharing"), drive_shared = t("admin.drive_shared"), drive_offline = t("admin.drive_offline"), drive_offline_hint = t("admin.drive_offline_hint"),
  confirm_purge_orphans = t("admin.confirm_purge_orphans"), purging = t("admin.purging"),
  confirm_purge_uploads = t("admin.confirm_purge_uploads"), cleaning = t("admin.cleaning"),
  wifi_scanning = t("admin.wifi_scanning"), wifi_connect = t("admin.wifi_connect"), wifi_connected_to = t("admin.wifi_connected_to"),
  wifi_not_connected = t("admin.wifi_not_connected"), wifi_pw_prompt = t("admin.wifi_pw_prompt"), wifi_connecting = t("admin.wifi_connecting"),
  wifi_ok = t("admin.wifi_ok"), wifi_fail = t("admin.wifi_fail"), wifi_secured = t("admin.wifi_secured"), wifi_open = t("admin.wifi_open"),
  wifi_none = t("admin.wifi_none"),
  mount_scanning = t("admin.mount_scanning"), mount_none = t("admin.mount_none"), mount_btn = t("admin.mount_btn"),
  unmount_btn = t("admin.unmount_btn"), mount_mounting = t("admin.mount_mounting"), mount_ok = t("admin.mount_ok"),
  mount_fail = t("admin.mount_fail"), mount_mounted_at = t("admin.mount_mounted_at"), mount_removable = t("admin.mount_removable"), mount_auto_badge = t("admin.mount_auto_badge"),
}) or "{}") .. [[;
async function checkUpdate(){
  const st=document.getElementById('update-status');
  st.textContent=AT.checking;st.style.color='var(--muted)';
  const r=await fetch('/api/v1/admin/version').catch(()=>({ok:false}));
  if(!r.ok){st.textContent=AT.not_reachable;st.style.color='var(--red)';return}
  const d=await r.json();
  if(d.update_available){
    document.getElementById('ver-status').textContent=AT.update_avail+d.latest_version;
    document.getElementById('ver-status').style.color='var(--amber)';
    st.textContent=AT.new_update+d.latest_version;st.style.color='var(--amber)';
  } else {
    st.textContent=AT.no_update;st.style.color='var(--grn)';
  }
}
async function applyUpdate(){
  const manifest=document.getElementById('update-manifest').value.trim();
  if(!manifest)return;
  const parsed=JSON.parse(manifest);
  const r=await fetch('/api/v1/admin/update',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(parsed)});
  const st=document.getElementById('update-status');
  st.textContent=r.ok?'✓ Update gestartet – Node startet neu':'✗ Fehler';
  st.style.color=r.ok?'var(--grn)':'var(--red)';
}
async function loadGridWallets(){
  const div=document.getElementById('grid-wallets');
  const r=await fetch('/api/v1/grid/wallets').catch(()=>({ok:false}));
  if(!r.ok){div.innerHTML='<div class="empty-hint">' + AT.no_grid_wallets + '</div>';return}
  const d=await r.json();const ws=d.wallets||[];
  if(!ws.length){div.innerHTML='<div class="empty-hint">' + AT.no_wallets_init + '</div>';return}
  div.innerHTML=ws.map(w=>`<div class="gw-row"><span class="gw-plz">${w.plz||'–'}</span><span class="gw-addr">${w.wallet||'–'}</span><span class="gw-fee">${w.fee_percent||0}%%/km</span></div>`).join('');
}
async function initGrid(){
  const msg=document.getElementById('grid-init-msg');
  msg.textContent=AT.initializing;msg.style.color='var(--muted)';
  const r=await fetch('/api/v1/admin/grid/init',{method:'POST'});
  const d=await r.json().catch(()=>({}));
  msg.textContent=r.ok?'✓ '+(d.message||'Initialisiert'):'✗ '+(d.error||AT.error_word);
  msg.style.color=r.ok?'var(--grn)':'var(--red)';
  if(r.ok)loadGridWallets();
}
async function loadStorage(){
  const r=await fetch('/api/v1/admin/storage/stats').catch(()=>({ok:false}));
  if(!r.ok)return;const d=await r.json();
  const set=(id,v)=>{const e=document.getElementById(id);if(e)e.textContent=v};
  set('sto-offer',(d.offer_gb||0)+' GB');set('sto-alloc',(d.alloc_gb||0)+' GB');
  set('sto-used',(d.used_gb||0).toFixed(1)+' GB');
  set('sto-chunks',d.chunks||0);set('sto-peers',d.peers||0);
  const pct=d.alloc_gb>0?Math.round((d.used_gb||0)/d.alloc_gb*100):0;
  set('sto-pct',pct+'%%');
  const f=document.getElementById('sto-fill');if(f)f.style.width=pct+'%%';
}
async function expandStorage(){
  const offerGb=parseInt(document.getElementById('expand-offer').value)||0;
  const msg=document.getElementById('expand-msg');
  if(!offerGb){msg.textContent=AT.enter_gb;return}
  // AllocGB muss ≥ 5 × OfferGB sein — automatisch berechnen, damit der Nutzer
  // nur das Netz-Angebot eingeben muss.
  const allocGb = offerGb * 5;
  msg.textContent = AT.expanding; msg.style.color='var(--muted)';
  const r=await fetch('/api/v1/admin/storage/expand',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({alloc_gb:allocGb, offer_gb:offerGb})});
  const d=await r.json().catch(()=>({}));
  msg.textContent=r.ok?('✓ '+AT.offer_set+' '+offerGb+' GB'):'✗ '+(d.error||AT.error_word);
  msg.style.color=r.ok?'var(--grn)':'var(--red)';if(r.ok){loadStorage();loadVolumes();}
}
async function loadConfig(){
  const div=document.getElementById('cfg-list');
  const r=await fetch('/api/v1/admin/config').catch(()=>({ok:false}));
  if(!r.ok){div.innerHTML='<div class="empty-hint">' + AT.not_available + '</div>';return}
  const d=await r.json();const checks=d.checks||[];
  if(!checks.length){div.innerHTML='<div class="empty-hint">' + AT.no_data + '</div>';return}
  div.innerHTML=checks.map(c=>`<div class="cfg-row"><span class="cfg-key">${c.key}</span><span class="${c.ok?'ok':'err'}">${c.ok?'✓':'✗'}</span><span style="font-size:12px;color:${c.ok?'var(--dim)':'var(--red)'}">${c.value||c.error||'–'}</span></div>`).join('');
}
async function purgeOrphans(){
  const st=document.getElementById('purge-status');
  if(!confirm(AT.confirm_purge_orphans)) return;
  st.textContent=AT.purging;
  try{
    const r=await fetch('/api/v1/admin/listings/purge-orphans',{method:'POST'});
    const d=await r.json();
    st.textContent='✓ '+(d.purged||0)+' verwaiste Angebote gelöscht';
  }catch(e){
    st.textContent='✗ '+e.message;
  }
}
async function purgeUploads(){
  const st=document.getElementById('upload-purge-status');
  if(!confirm(AT.confirm_purge_uploads)) return;
  st.textContent=AT.cleaning;
  try{
    const r=await fetch('/api/v1/admin/uploads/purge',{method:'POST'});
    const d=await r.json();
    st.textContent='✓ '+(d.purged||0)+' unfertige Uploads entfernt';
  }catch(e){ st.textContent='✗ '+e.message; }
}
async function loadDrives(){
  try{
    const r=await fetch('/api/v1/admin/storage/drives');
    const el=document.getElementById('drives-list');
    if(r.status===401 || r.status===403){
      if(el) el.innerHTML='<div class="empty-hint">' + AT.admin_login_needed + '</div>';
      return;
    }
    if(r.status===500){ if(el) el.innerHTML='<div class="empty-hint">' + AT.admin_auth_500 + '</div>'; return; }
    if(!r.ok){ if(el) el.innerHTML='<div class="empty-hint">' + AT.error_word + ' ('+r.status+')</div>'; return; }
    const ct = r.headers.get('content-type')||'';
    if(ct.indexOf('application/json')<0){
      // Keine JSON-Antwort (z.B. nginx-Login-Seite) → sauber melden statt crashen.
      if(el) el.innerHTML='<div class="empty-hint">' + AT.admin_login_needed + '</div>';
      return;
    }
    const d=await r.json();
    const cur=document.getElementById('cur-datadir');
    if(cur) cur.textContent=d.current_data_dir||'–';
    if(!d.drives||d.drives.length===0){ el.innerHTML='<div class="empty-hint">' + AT.no_drives + '</div>'; return; }
    el.innerHTML=d.drives.map(function(m){
      const free=(m.free_gb!=null?m.free_gb:0).toFixed(1);
      const total=(m.total_gb!=null?m.total_gb:0).toFixed(1);
      const mp=m.mount_point||'?';
      return '<div class="sto-s"><span class="sto-l">'+mp+' ('+(m.fs_type||'')+')</span>'+
        '<span class="sto-v">'+free+' / '+total+' GB frei</span>'+
        '<button class="btn-sm btn-prim" style="margin-left:8px" onclick="shareDrive(\''+mp+'\')">'+AT.drive_use+'</button>'+
        '</div>';
    }).join('');
  }catch(e){
    const el=document.getElementById('drives-list');
    if(el) el.innerHTML='<div class="empty-hint">' + AT.error_word + ': '+e.message+'</div>';
  }
}
loadGridWallets();loadStorage();loadConfig();loadDrives();loadVolumes();initHelperCards();

// Freigegebene Speicherorte (Volumes) laden und anzeigen.
async function loadVolumes(){
  try{
    const r=await fetch('/api/v1/admin/storage/volumes');
    const el=document.getElementById('volumes-list');
    if(!el) return;
    if(r.status===401 || r.status===403){ el.innerHTML='<div class="empty-hint">' + AT.admin_login_needed + '</div>'; return; }
    const ct = r.headers.get('content-type')||'';
    if(!r.ok || ct.indexOf('application/json')<0){ el.innerHTML='<div class="empty-hint">–</div>'; return; }
    const d=await r.json();
    if(!d.volumes||d.volumes.length===0){ el.innerHTML='<div class="empty-hint">–</div>'; return; }
    el.innerHTML=d.volumes.map(function(v){
      const status = v.primary
        ? '<span class="badge-green">'+AT.drive_primary+'</span>'
        : (v.online ? '' : '<span class="badge-lv" style="opacity:0.7">'+AT.drive_offline+'</span>');
      const info = v.online
        ? v.free_gb.toFixed(1)+' GB frei · '+v.chunks+' Chunks'
        : AT.drive_offline_hint;
      return '<div class="sto-s"><span class="sto-l">'+(v.label||v.path)+' '+status+'</span>'+
        '<span class="sto-v">'+info+'</span>'+
        (v.primary?'':'<button class="btn-sm" style="margin-left:8px" onclick="unshareVolume(\''+v.path+'\')">'+AT.drive_unshare+'</button>')+
        '</div>';
    }).join('');
  }catch(e){}
}

// Ein erkanntes Laufwerk (oder einen Unterordner) als zusätzlichen Speicher
// freigeben — Daten werden NICHT verschoben, nur zur Freigabe hinzugefügt.
async function shareDrive(mountPoint){
  let path = mountPoint;
  // Optional Unterordner anbieten, damit nicht das ganze Laufwerk genutzt wird.
  const sub = prompt(AT.drive_subfolder_q, mountPoint + '/fundus-share');
  if (sub === null) return; // abgebrochen
  path = sub.trim() || mountPoint;
  const st = document.getElementById('drives-status');
  if (st){ st.textContent = AT.drive_sharing; st.style.color='var(--muted)'; }
  try{
    const r = await fetch('/api/v1/admin/storage/volumes', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ path: path, label: path })
    });
    const d = await r.json();
    if(!r.ok){ if(st){ st.textContent='✗ '+(d.error||''); st.style.color='var(--red)'; } return; }
    if(st){ st.textContent=AT.drive_shared; st.style.color='var(--grn)'; }
    loadVolumes(); loadStorage();
  }catch(e){ if(st){ st.textContent='✗ '+e.message; st.style.color='var(--red)'; } }
}

async function cleanupStorage(mode){
  const msg=document.getElementById('cleanup-msg');
  if((mode==='orphans'||mode==='tmp') && !confirm(AT.cleanup_confirm)) return;
  msg.textContent=AT.cleanup_running; msg.style.color='var(--muted)';
  try{
    const r=await fetch('/api/v1/admin/storage/cleanup?mode='+mode,{method:'POST'});
    if(r.status===401||r.status===403){ msg.textContent=AT.admin_login_needed; msg.style.color='var(--red)'; return; }
    const ct=r.headers.get('content-type')||'';
    if(!r.ok||ct.indexOf('application/json')<0){ msg.textContent=AT.error_word; msg.style.color='var(--red)'; return; }
    const d=await r.json();
    const orphGb=(d.orphan_bytes/1e9).toFixed(2);
    const tmpGb=(d.tmp_bytes/1e9).toFixed(2);
    const delGb=(d.deleted_bytes/1e9).toFixed(2);
    if(mode==='scan'){
      const ownGb=((d.own_bytes||0)/1e9).toFixed(2);
      const hostGb=((d.host_bytes||0)/1e9).toFixed(2);
      const cacheGb=((d.cache_bytes||0)/1e9).toFixed(2);
      msg.innerHTML=AT.cleanup_found+': '+
        d.orphan_chunks+' '+AT.cleanup_orphan_word+' ('+orphGb+' GB), '+
        d.tmp_files+' tmp ('+tmpGb+' GB)<br>'+
        '<span style="color:var(--muted)">'+
        AT.scan_own+': '+(d.own_chunks||0)+' ('+ownGb+' GB) · '+
        AT.scan_host+': '+(d.host_chunks||0)+' ('+hostGb+' GB) · '+
        AT.scan_cache+': '+(d.cache_chunks||0)+' ('+cacheGb+' GB)</span>';
      msg.style.color='var(--fg)';
    }else{
      msg.textContent='✓ '+d.deleted_chunks+' '+AT.cleanup_deleted_word+' ('+delGb+' GB)';
      msg.style.color='var(--grn)';
      loadStorage(); loadVolumes();
    }
  }catch(e){ msg.textContent='✗ '+e.message; msg.style.color='var(--red)'; }
}

async function unshareVolume(path){
  if(!confirm(AT.drive_unshare_q + '\n' + path)) return;
  try{
    const r = await fetch('/api/v1/admin/storage/volumes', {
      method:'DELETE', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ path: path })
    });
    if(r.ok){ loadVolumes(); loadStorage(); }
  }catch(e){}
}

// ─── fundus-helper: WLAN + externe Laufwerke mounten ───────────────────────
// Die beiden Cards werden nur eingeblendet, wenn der privilegierte Helper laeuft.
async function initHelperCards(){
  try{
    const r = await fetch('/api/v1/admin/system/helper');
    const d = await r.json();
    if(d && d.available){
      document.getElementById('wifi-card').style.display='';
      document.getElementById('mount-card').style.display='';
      loadWifiStatus();
      scanBlockDevices();     // Geräteliste sofort laden (kein Klick nötig)
      startHelperLivePoll();  // danach live aktuell halten (ohne F5)
    }
  }catch(e){}
}

async function loadWifiStatus(){
  const el=document.getElementById('wifi-current');
  if(!el) return;
  try{
    const r=await fetch('/api/v1/admin/system/wifi/status');
    const d=await r.json();
    if(d.ok && d.status && d.status.connected){
      el.textContent=AT.wifi_connected_to+' '+d.status.ssid+(d.status.ip?(' ('+d.status.ip+')'):'');
      el.style.color='var(--grn)';
    } else {
      el.textContent=AT.wifi_not_connected; el.style.color='var(--muted)';
    }
  }catch(e){}
}

async function scanWifi(){
  const list=document.getElementById('wifi-list');
  const msg=document.getElementById('wifi-msg');
  list.innerHTML='<div class="empty-hint">'+AT.wifi_scanning+'</div>';
  msg.textContent='';
  try{
    const r=await fetch('/api/v1/admin/system/wifi');
    const d=await r.json();
    if(!d.ok || !d.networks || d.networks.length===0){
      list.innerHTML='<div class="empty-hint">'+AT.wifi_none+'</div>'; return;
    }
    // Nach Signalstaerke sortieren (stärkste zuerst).
    d.networks.sort(function(a,b){ return b.signal-a.signal; });
    list.innerHTML=d.networks.map(function(n){
      const lock = n.secured ? AT.wifi_secured : AT.wifi_open;
      const active = n.active ? ' <span class="badge-green">●</span>' : '';
      const ssidEsc = (n.ssid||'').replace(/'/g,"\\'");
      return '<div class="cfg-row">'+
        '<div style="flex:1">'+escapeHtml(n.ssid)+active+'</div>'+
        '<div style="width:44px;color:var(--muted)">'+n.signal+'%%</div>'+
        '<div style="width:80px;color:var(--dim);font-size:11px">'+lock+'</div>'+
        '<button class="btn-sm btn-prim" onclick="connectWifi(\''+ssidEsc+'\','+(n.secured?'true':'false')+')">'+AT.wifi_connect+'</button>'+
        '</div>';
    }).join('');
  }catch(e){ list.innerHTML='<div class="empty-hint">'+AT.wifi_fail+'</div>'; }
}

async function connectWifi(ssid, secured){
  const msg=document.getElementById('wifi-msg');
  let password='';
  if(secured){
    password = prompt(AT.wifi_pw_prompt+' '+ssid);
    if(password===null) return; // abgebrochen
  }
  msg.textContent=AT.wifi_connecting; msg.style.color='var(--muted)';
  try{
    const r=await fetch('/api/v1/admin/system/wifi/connect',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ ssid: ssid, password: password })
    });
    const d=await r.json();
    if(d.ok){
      msg.textContent=AT.wifi_ok; msg.style.color='var(--grn)';
      loadWifiStatus();
    } else {
      msg.textContent=AT.wifi_fail+(d.error?(': '+d.error):''); msg.style.color='var(--red)';
    }
  }catch(e){ msg.textContent=AT.wifi_fail+': '+e.message; msg.style.color='var(--red)'; }
}

// Rendert die Geräteliste. Getrennt vom Laden, damit stille Aktualisierungen
// (Live-Poll) nicht flackern.
function renderBlockDevices(devices){
  const list=document.getElementById('blockdev-list');
  if(!list) return;
  if(!devices || devices.length===0){
    list.innerHTML='<div class="empty-hint">'+AT.mount_none+'</div>'; return;
  }
  list.innerHTML=devices.map(function(dev){
    const uuidEsc=(dev.uuid||'').replace(/'/g,"\\'");
    const size=dev.size_gb?dev.size_gb.toFixed(1)+' GB':'';
    const rm = dev.removable ? ' <span class="badge-lv">'+AT.mount_removable+'</span>' : '';
    const auto = dev.auto_mount ? ' <span class="badge-green">'+AT.mount_auto_badge+'</span>' : '';
    let right;
    if(dev.mounted){
      right='<span style="color:var(--grn);font-size:11px">'+AT.mount_mounted_at+' '+escapeHtml(dev.mount_point)+'</span>'+
        '<button class="btn-sm" style="margin-left:8px" onclick="unmountDrive(\''+uuidEsc+'\')">'+AT.unmount_btn+'</button>';
    } else {
      right='<button class="btn-sm btn-prim" onclick="mountDrive(\''+uuidEsc+'\')">'+AT.mount_btn+'</button>';
    }
    return '<div class="cfg-row">'+
      '<div style="flex:1">'+escapeHtml(dev.path)+' <span style="color:var(--dim);font-size:11px">'+escapeHtml(dev.fs_type)+'</span>'+rm+auto+'</div>'+
      '<div style="width:70px;color:var(--muted)">'+size+'</div>'+
      '<div>'+right+'</div>'+
      '</div>';
  }).join('');
}

// scanBlockDevices lädt die Geräteliste. silent=true unterdrückt den
// Lade-Platzhalter (für den Live-Poll, der ohne Flackern aktualisiert).
async function scanBlockDevices(silent){
  const list=document.getElementById('blockdev-list');
  const msg=document.getElementById('mount-msg');
  if(!list) return;
  if(!silent){
    list.innerHTML='<div class="empty-hint">'+AT.mount_scanning+'</div>';
    if(msg) msg.textContent='';
  }
  try{
    const r=await fetch('/api/v1/admin/system/blockdevices');
    const d=await r.json();
    if(!d.ok){ if(!silent) list.innerHTML='<div class="empty-hint">'+AT.mount_none+'</div>'; return; }
    renderBlockDevices(d.devices);
  }catch(e){ if(!silent) list.innerHTML='<div class="empty-hint">'+AT.mount_fail+'</div>'; }
}

// Live-Aktualisierung: Solange die Mount-Karte sichtbar und der Tab aktiv ist,
// wird die Geräteliste (und die Freigabe-Liste) regelmäßig still nachgeladen —
// so erscheint eine automatisch gemountete Platte ohne manuelles Neuladen (F5).
let helperPollTimer=null;
function startHelperLivePoll(){
  if(helperPollTimer) return;
  helperPollTimer=setInterval(function(){
    if(document.hidden) return; // im Hintergrund nicht pollen
    const card=document.getElementById('mount-card');
    if(!card || card.style.display==='none') return;
    scanBlockDevices(true);
    loadDrives();
  }, 4000);
}

async function mountDrive(uuid){
  const msg=document.getElementById('mount-msg');
  msg.textContent=AT.mount_mounting; msg.style.color='var(--muted)';
  try{
    const r=await fetch('/api/v1/admin/system/mount',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ uuid: uuid })
    });
    const d=await r.json();
    if(d.ok){
      msg.textContent=AT.mount_ok+(d.mount_point?(': '+d.mount_point):''); msg.style.color='var(--grn)';
      scanBlockDevices(); loadDrives();
    } else {
      msg.textContent=AT.mount_fail+(d.error?(': '+d.error):''); msg.style.color='var(--red)';
    }
  }catch(e){ msg.textContent=AT.mount_fail+': '+e.message; msg.style.color='var(--red)'; }
}

async function unmountDrive(uuid){
  const msg=document.getElementById('mount-msg');
  msg.textContent=AT.mount_mounting; msg.style.color='var(--muted)';
  try{
    const r=await fetch('/api/v1/admin/system/unmount',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ uuid: uuid })
    });
    const d=await r.json();
    if(d.ok){
      msg.textContent=AT.mount_ok; msg.style.color='var(--grn)';
      scanBlockDevices(); loadDrives();
    } else {
      msg.textContent=AT.mount_fail+(d.error?(': '+d.error):''); msg.style.color='var(--red)';
    }
  }catch(e){ msg.textContent=AT.mount_fail+': '+e.message; msg.style.color='var(--red)'; }
}

function escapeHtml(s){
  if(s==null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
</script>
]],
    ver and ver.version or "R001",
    ver and ver.build   or "fundus/node",
    sto and sto.offer_gb   or 0,
    sto and sto.alloc_gb   or 0,
    sto and (sto.used_gb or 0.0) or 0.0,
    sto and sto.chunks  or 0,
    sto and sto.peers   or 0
  ))
  render.footer()
end

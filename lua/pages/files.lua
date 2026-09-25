-- pages/files.lua – Dezentraler Filemanager
-- Upload via XChaCha20-Poly1305, 5× redundante Speicherung im DHT
local render = require "render"
local cjson  = require "cjson.safe"

return function()
  ngx.header["Content-Type"] = "text/html"
  local t = render.header("files.title", "files")

  local stats, _ = render.api_get("/v1/files/stats")
  local storageEnabled = stats and stats.enabled

  -- GB-Werte auf max. 3 Nachkommastellen runden und nachgestellte Nullen
  -- entfernen (z.B. 0.001234 -> "0.001", 10.0 -> "10"). Verhindert, dass lange
  -- Fließkommazahlen die Seitenbreite sprengen.
  local function fmtGB(v)
    local n = tonumber(v) or 0
    local s = string.format("%.3f", n)
    -- nachgestellte Nullen + evtl. Dezimalpunkt entfernen
    s = s:gsub("(%..-)0+$", "%1"):gsub("%.$", "")
    return s
  end

ngx.print(string.format([[
<div class="files-layout">

<!-- ── Speicher-Übersicht ────────────────────────────────── -->
<div class="files-stats-row">
  <div class="fstat-card">
    <span class="fstat-label">%s</span>
    <span class="fstat-val">%s GB</span>
  </div>
  <div class="fstat-card">
    <span class="fstat-label">%s</span>
    <span class="fstat-val">%s GB</span>
  </div>
  <div class="fstat-card">
    <span class="fstat-label">%s</span>
    <span class="fstat-val">%s GB</span>
  </div>
  <div class="fstat-card">
    <span class="fstat-label">%s</span>
    <span class="fstat-val" id="fs-files">%d</span>
  </div>
  <div class="fstat-card">
    <span class="fstat-label">%s</span>
    <span class="fstat-val" id="fs-chunks">%d</span>
  </div>
</div>
]],
  t("files.offered_gb"),   fmtGB(stats and stats.offer_gb or 0),
  t("files.min_required"), fmtGB(stats and stats.fairness_min_gb or 0),
  t("files.used_gb"),      fmtGB(stats and stats.used_gb  or 0),
  t("files.file_count"),   stats and stats.files  or 0,
  t("files.chunk_count"),  stats and stats.chunks or 0
))

-- Laufwerks-Angebot: pro gemountetem Laufwerk einstellen, wie viel dem Netz zur
-- Verfügung steht. Wird per JS aus /api/v1/admin/storage/volumes befüllt.
ngx.print(string.format([[
<div class="files-drives-card" id="drives-offer-card" style="display:none">
  <div class="file-card-head"><h3>%s</h3></div>
  <p class="adm-hint">%s</p>
  <div id="drives-offer-min" class="status-line" style="margin-bottom:8px"></div>
  <div id="drives-mountable" style="margin-bottom:12px"></div>
  <div id="drives-offer-list"></div>
  <div id="drives-offer-msg" class="status-line" style="margin-top:6px"></div>
</div>
]],
  t("files.drives_offer_title"),
  t("files.drives_offer_hint")
))

if not storageEnabled then
  ngx.print(string.format([[
<div class="info-notice">
  <strong>%s</strong> %s<br>
  <code>FUNDUS_STORAGE_OFFER_GB=10</code>
</div>
]], t("files.disabled"), t("files.disabled_hint")))
end

ngx.print(string.format([[

<!-- ── Upload ───────────────────────────────────────────── -->
<div class="file-card">
  <div class="file-card-head"><h3>%s</h3></div>
  <div class="file-card-body">
    <div class="drop-zone" id="drop-zone"
         ondragover="ev.preventDefault()" ondrop="handleDrop(event)"
         onclick="document.getElementById('file-input').click()">
      <div class="drop-icon">📁</div>
      <div class="drop-title">%s</div>
      <div class="drop-sub">%s · BLAKE2b-256</div>
    </div>
    <label class="enc-row">
      <input type="checkbox" id="enc-toggle" onclick="event.stopPropagation()">
      <span>🔒 Diese Dateien verschlüsseln (Passwort, Ende-zu-Ende)</span>
    </label>
    <label class="enc-row" onclick="event.stopPropagation()">
      <span>🛡️ Redundanz (Kopien im Netz):</span>
      <select id="redundancy-select" onchange="updateRedundancyHint()" onclick="event.stopPropagation()">
        <option value="1" selected>1× — keine Redundanz</option>
        <option value="2">2× — minimal</option>
        <option value="3">3× — sparsam</option>
        <option value="5">5× — empfohlen</option>
        <option value="8">8× — hohe Sicherheit</option>
      </select>
    </label>
    <div id="redundancy-hint" class="redundancy-hint">]] .. t("files.redundancy_hint") .. [[</div>
    <input type="file" id="file-input" multiple style="display:none"
           onchange="handleFiles(this.files)">
    <input type="file" id="folder-input" webkitdirectory directory multiple style="display:none"
           onchange="handleFolderUpload(this.files)">
    <div style="display:flex;gap:8px;margin-top:8px">
      <button type="button" class="btn-sm" onclick="document.getElementById('file-input').click()">📄 ]] .. t("files.upload_files_btn") .. [[</button>
      <button type="button" class="btn-sm" onclick="document.getElementById('folder-input').click()">📁 ]] .. t("files.upload_folder_btn") .. [[</button>
    </div>
    <div id="upload-list" class="upload-list"></div>
  </div>
</div>

<!-- ── Lokales Verzeichnis freigeben (ohne Chunking) ────────── -->
<div class="file-card">
  <div class="file-card-head"><h3>]] .. t("files.share_dir_title") .. [[</h3></div>
  <div class="file-card-body">
    <p class="meta" style="margin:0 0 8px 0">]] .. t("files.share_dir_hint") .. [[</p>
    <div class="inline-form" style="flex-wrap:wrap;gap:6px">
      <input type="text" id="share-dir-name" placeholder="]] .. t("files.share_dir_name_ph") .. [[" style="flex:1;min-width:120px">
      <input type="text" id="share-dir-path" placeholder="]] .. t("files.share_dir_path_ph") .. [[" style="flex:2;min-width:180px">
      <button class="btn-sm" type="button" onclick="openDirBrowser()">📂 ]] .. t("files.browse_btn") .. [[</button>
      <button class="btn-sm btn-prim" onclick="createDirShare()">]] .. t("files.share_dir_btn") .. [[</button>
    </div>
    <div id="share-dir-msg" class="status-line" style="margin-top:6px"></div>
    <div id="share-dir-list" style="margin-top:10px"></div>
    <div style="margin-top:14px;padding-top:10px;border-top:1px solid var(--border)">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px">
        <strong style="font-size:12px;color:var(--muted)">]] .. t("files.net_shares_title") .. [[</strong>
        <span style="font-size:10px;color:var(--muted)">]] .. t("files.net_shares_auto") .. [[</span>
      </div>
      <div id="net-shares-list"><div class="empty-hint">]] .. t("files.net_shares_hint") .. [[</div></div>
    </div>
  </div>
</div>

<!-- ── Eigene Dateien ────────────────────────────────────── -->
<div class="file-card">
  <div class="file-card-head">
    <h3>%s</h3>
    <button class="btn-sm" onclick="loadFiles()">↻ %s</button>
  </div>
  <div class="file-card-body">
    <div id="file-list"><div class="empty-hint">%s</div></div>
  </div>
</div>

<!-- ── Netzwerk-Suche nach geteilten Dateien ─────────────── -->
<div class="file-card">
  <div class="file-card-head"><h3>]] .. t("files.search_net_title") .. [[</h3></div>
  <div class="file-card-body">
    <div class="inline-form">
      <input type="text" id="fs-query" placeholder="]] .. t("files.search_placeholder") .. [["
             style="flex:1" onkeydown="if(event.key==='Enter')fileSearch()">
      <button class="btn-sm btn-prim" onclick="fileSearch()">]] .. t("files.search_btn") .. [[</button>
    </div>
    <div id="fs-results" class="fs-results"></div>
  </div>
</div>

<!-- ── Hash-Download ─────────────────────────────────────── -->
<div class="file-card">
  <div class="file-card-head"><h3>%s</h3></div>
  <div class="file-card-body">
    <div class="inline-form">
      <input type="text" id="dl-hash" placeholder="]] .. t("files.hash_placeholder") .. [["
             style="flex:1">
      <button class="btn-sm btn-prim" onclick="downloadHash()">%s</button>
    </div>
    <div id="dl-status" class="status-line"></div>
  </div>
</div>

<!-- ── Blockchain verschieben (selten gebraucht, daher unten) ─── -->
<div class="file-card" id="chain-move-card" style="display:none">
  <div class="file-card-head"><h3>]] .. t("files.chain_target_title") .. [[</h3></div>
  <div class="file-card-body">
    <div style="font-size:12px;color:var(--muted);margin-bottom:6px">]] .. t("files.chain_target_hint") .. [[</div>
    <select id="chain-move-select" onchange="moveChain(this.value)" style="width:100%%;padding:6px"></select>
    <div id="chain-move-msg" class="status-line" style="margin-top:6px"></div>
  </div>
</div>

</div><!-- files-layout -->

<style>
.files-layout { display:flex; flex-direction:column; gap:12px; }
.files-stats-row { display:grid; grid-template-columns:repeat(5,1fr); gap:8px; }
.fstat-card {
  background:var(--sur); border:1px solid var(--brd); border-radius:var(--r);
  padding:.8rem 1rem; display:flex; flex-direction:column; gap:3px;
}
.fstat-label { font-size:10px; font-weight:700; text-transform:uppercase;
               letter-spacing:.07em; color:var(--muted); }
.fstat-val   { font-size:1.4rem; font-weight:800; color:var(--txt); }

.file-card { background:var(--sur); border:1px solid var(--brd); border-radius:var(--r); overflow:hidden; }
.file-card-head {
  background:var(--sur2); border-bottom:1px solid var(--brd);
  padding:.65rem 1.1rem; display:flex; align-items:center; justify-content:space-between;
}
.file-card-head h3 { margin:0; font-size:13.5px; font-weight:600; }
.file-card-body { padding:1rem 1.1rem; }

.drop-zone {
  border:2px dashed var(--brd2); border-radius:var(--r);
  padding:2rem; text-align:center; cursor:pointer;
  transition:border-color .15s, background .15s;
}
.drop-zone:hover { border-color:var(--acc); background:rgba(91,138,245,.04); }
.drop-icon  { font-size:32px; margin-bottom:8px; }
.drop-title { font-size:14px; font-weight:600; margin-bottom:4px; }
.drop-sub   { font-size:11px; color:var(--muted); }

.upload-list { margin-top:10px; display:flex; flex-direction:column; gap:6px; }
.upload-item {
  display:flex; align-items:center; gap:10px; padding:8px 10px;
  background:var(--sur2); border:1px solid var(--brd); border-radius:var(--rs);
  font-size:12.5px;
}
.upload-item .name  { flex:1; min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.upload-item .size  { color:var(--muted); font-size:11px; white-space:nowrap; }
.upload-item .prog  { width:80px; height:4px; background:var(--brd2); border-radius:2px; overflow:hidden; }
.upload-item .prog-bar { height:100%%; background:var(--grn); border-radius:2px; transition:width .3s; }
.upload-item .hash  { font-family:var(--mono); font-size:9.5px; color:var(--dim); }

.file-row {
  display:grid; grid-template-columns: minmax(0,1fr) 64px 110px 56px 96px; align-items:center; gap:8px;
  padding:5px 8px; border-bottom:1px solid var(--brd); font-size:12.5px; line-height:1.3;
}
.file-head {
  font-weight:700; color:var(--muted); text-transform:uppercase;
  font-size:10px; letter-spacing:0.04em; border-bottom:2px solid var(--brd);
  padding-bottom:5px; margin-bottom:2px;
}
.file-head span { cursor:pointer; text-align:left; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.file-head .fbtns { cursor:default; }
.file-head span:hover { color:var(--green); }
.file-row:last-child { border-bottom:none; }
.file-row .fname { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-weight:600; }
.file-row .fsize { font-size:11px; color:var(--muted); white-space:nowrap; }
.file-row .fhash { font-size:10px; color:var(--dim); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.file-row .fredundancy { font-size:11px; white-space:nowrap; }
.file-row .fbtns { display:flex; gap:4px; justify-content:flex-start; }
.file-row .fbtns .btn-sm { padding:4px 8px; font-size:11px; min-height:28px; }
/* Mobil: Grid aufbrechen, Attribute unter den Namen stapeln. */
@media (max-width:600px) {
  .file-row { grid-template-columns: 1fr auto; grid-template-areas: "name btns" "meta btns"; }
  .file-row .fname { grid-area:name; }
  .file-row .fsize, .file-row .fhash, .file-row .fredundancy { grid-area:meta; display:inline; font-size:10px; }
  .file-row .fbtns { grid-area:btns; }
  .file-head { display:none; }
}

.inline-form { display:flex; gap:8px; align-items:center; }
.inline-form input { flex:1; padding:7px 10px; background:var(--sur2); border:1px solid var(--brd2);
  border-radius:var(--rs); color:var(--txt); font-size:13px; outline:none; }
.inline-form input:focus { border-color:var(--acc); }

.btn-sm { padding:4px 12px; font-size:12px; background:var(--sur2); border:1px solid var(--brd2);
          border-radius:var(--rs); color:var(--dim); cursor:pointer; transition:all .15s; }
.btn-sm:hover { border-color:var(--acc); color:var(--acc); }
.btn-prim { background:var(--acc); color:#fff; border-color:var(--acc); }
.btn-prim:hover { background:var(--acc-h,#3b7cf5); }
.btn-danger { background:var(--red-bg); color:var(--red); border-color:rgba(240,82,82,.3); }
.status-line { margin-top:8px; font-size:12.5px; color:var(--muted); }
.empty-hint  { font-size:13px; color:var(--muted); font-style:italic; }
.info-notice {
  background:rgba(255,179,0,.07); border:1px solid rgba(255,179,0,.25);
  border-radius:var(--r); padding:12px 14px; font-size:13px; color:var(--amber);
}
.btn-shared { background:rgba(0,230,118,.15)!important; border-color:rgba(0,230,118,.4)!important; color:#00e676!important; }
.fs-results { margin-top:10px; display:flex; flex-direction:column; gap:4px; }
@media(max-width:600px){ .files-stats-row { grid-template-columns:1fr 1fr; } }
</style>

<script src="/static/media-viewer.js?v=]] .. require("render").rev() .. [["></script>
<script>
const FT = ]] .. (require("cjson.safe").encode({
  redundancy_3 = t("files.redundancy_3"), redundancy_8 = t("files.redundancy_8"),
  redundancy_1 = t("files.redundancy_1"), redundancy_2 = t("files.redundancy_2"),
  redundancy_5 = t("files.redundancy_5"), no_files = t("files.no_files"), loading = t("files.loading"),
  not_found = t("files.not_found"), cancelled = t("files.cancelled"), decrypting = t("files.decrypting"),
  wrong_password = t("files.wrong_password"), download_started = t("files.download_started"),
  searching_box = t("files.searching_box"), error_word = t("files.error_word"),
  no_shared_found = t("files.no_shared_found"), upload_failed_start = t("files.upload_failed_start"),
  unnamed = t("files.unnamed"), unshare = t("files.unshare"), make_findable = t("files.make_findable"),
  confirm_remove = t("files.confirm_remove"), local_src = t("files.local"), network = t("files.network"),
  offer_total = t("files.offer_total"), offer_min = t("files.offer_min"), offline = t("files.offline"),
  saving = t("files.saving"), saved = t("files.saved"),
  chain_target_pick = t("files.chain_target_pick"), chain_move_confirm = t("files.chain_move_confirm"),
  chain_moving = t("files.chain_moving"), chain_move_err = t("files.chain_move_err"),
  drives_mountable_hint = t("files.drives_mountable_hint"), drives_mount_btn = t("files.drives_mount_btn"),
  drives_mounting = t("files.drives_mounting"),
  redundancy_col = t("files.redundancy_col"), of = t("files.of"), chunks_word = t("files.chunks_word"),
  col_name = t("files.col_name"), col_size = t("files.col_size"), col_hash = t("files.col_hash"),
  files_word = t("files.files_word"), close = t("files.close"), creating_manifest = t("files.creating_manifest"),
  share_dir_need = t("files.share_dir_need"), share_dir_creating = t("files.share_dir_creating"), share_dir_ok = t("files.share_dir_ok"),
  hash_copied = t("files.hash_copied"), copy_hash_hint = t("files.copy_hash_hint"),
  net_shares_none = t("files.net_shares_none"), refresh = t("files.refresh"), net_shares_auto = t("files.net_shares_auto"),
}) or "{}") .. [[
// ── Upload ──────────────────────────────────────────────────
function hashString(s){let h=0;for(let i=0;i<s.length;i++){h=(h<<5)-h+s.charCodeAt(i);h|=0;}return h;}

async function resumableUpload(file, onProgress, redundancy) {
  const BLOCK = 4 * 1024 * 1024;
  const idSeed = file.name + "|" + file.size + "|" + (file.lastModified || 0);
  const uploadId = "u" + Math.abs(hashString(idSeed)).toString(36) + (file.size %% 100000).toString(36);
  if (window.FundusProgress) FundusProgress.set(uploadId, { name: file.name, state: "uploading", progress: 0 });
  const report = (p) => { if (onProgress) onProgress(p); if (window.FundusProgress) FundusProgress.progress(uploadId, p); };
  const beginRes = await fetch("/api/v1/files/upload/begin", { credentials:"same-origin",
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ upload_id: uploadId, file_name: file.name,
      mime_type: file.type || "application/octet-stream", total_size: file.size, block_size: BLOCK,
      redundancy: (redundancy || 5) }),
  });
  if (!beginRes.ok) {
    const errData = await beginRes.json().catch(() => ({}));
    const msg = errData.error || FT.upload_failed_start;
    // Progress-Leiste auf Fehler setzen, sonst bleibt sie bei null Prozent haengen.
    if (window.FundusProgress) FundusProgress.set(uploadId, { state: "error", error: msg });
    throw new Error(msg);
  }
  const begin = await beginRes.json();
  const have = new Set(begin.have_blocks || []);
  const totalBlocks = begin.total_blocks;
  for (let i = 0; i < totalBlocks; i++) {
    if (have.has(i)) { report((i+1)/totalBlocks); continue; }
    const slice = file.slice(i*BLOCK, Math.min((i+1)*BLOCK, file.size));
    let ok = false;
    for (let a = 0; a < 4 && !ok; a++) {
      // Timeout pro Block-Versuch: ein haengender Request (z.B. abgebrochene
      // Verbindung) wird nach 60s abgebrochen und wiederholt, statt den ganzen
      // Upload einzufrieren.
      const ctrl = new AbortController();
      const tmo = setTimeout(() => ctrl.abort(), 60000);
      try {
        const r = await fetch(`/api/v1/files/upload/block?id=${uploadId}&index=${i}`,
          { credentials:"same-origin", method:"POST", body: slice, signal: ctrl.signal });
        ok = r.ok;
      } catch(e){ ok = false; }
      finally { clearTimeout(tmo); }
      if (!ok) await new Promise(res => setTimeout(res, 800));
    }
    if (!ok) throw new Error("Block "+i+" konnte nicht hochgeladen werden (Verbindung?)");
    report((i+1)/totalBlocks);
  }
  const finRes = await fetch(`/api/v1/files/upload/finish?id=${uploadId}`, { credentials:"same-origin", method:"POST" });
  if (!finRes.ok) throw new Error("finish");
  if (window.FundusProgress) FundusProgress.finalizing(uploadId);
  let pollErrors = 0;
  for (;;) {
    await new Promise(res => setTimeout(res, 1500));
    let d;
    try {
      const sr = await fetch(`/api/v1/files/upload/status?id=${uploadId}`, {credentials:'same-origin'});
      if (!sr.ok) { pollErrors++; if (pollErrors > 40) return null; continue; }
      d = await sr.json();
      pollErrors = 0;
    }
    catch(e){
      // Bei Navigation/Abbruch wird fetch verworfen — nicht endlos weiter-
      // hämmern, sondern nach einigen Fehlversuchen aufgeben.
      pollErrors++;
      if (pollErrors > 40) return null;
      continue;
    }
    if (d.state === "done") { if (window.FundusProgress) FundusProgress.done(uploadId); return d.content_hash; }
    if (d.state === "error") { if (window.FundusProgress) FundusProgress.set(uploadId, { state:"error", error:d.error }); throw new Error("Verarbeitung: " + (d.error||"")); }
    // Chunking-Fortschritt anzeigen, damit man sieht, dass etwas passiert.
    if (d.chunks_total > 0 && window.FundusProgress) {
      FundusProgress.set(uploadId, {
        state: "finalizing",
        phase: d.phase || "chunking",
        chunksDone: d.chunks_done || 0,
        chunksTotal: d.chunks_total
      });
    }
  }
}

function handleDrop(e) {
  e.preventDefault();
  handleFiles(e.dataTransfer.files);
}

async function handleFiles(files) {
  // Verschlüsselung gewünscht? Dann einmal das Passwort abfragen.
  const enc = document.getElementById('enc-toggle');
  const redSel = document.getElementById('redundancy-select');
  const redundancy = redSel ? (parseInt(redSel.value, 10) || 5) : 5;
  let password = null;
  if (enc && enc.checked) {
    // Sicherstellen, dass die Krypto-Bibliothek geladen ist
    if (!window.FundusCrypto || !window.sodium) {
      alert("Verschlüsselungsbibliothek nicht geladen (libsodium.js). Verschlüsselung nicht verfügbar.");
      return;
    }
    try { await FundusCrypto.ready(); }
    catch (e) { alert("Verschlüsselung nicht verfügbar: " + e.message); return; }
    password = prompt("Passwort für die Verschlüsselung festlegen:\n(Ohne dieses Passwort sind die Dateien NICHT wiederherstellbar!)");
    if (!password) { alert("Verschlüsselung abgebrochen — kein Passwort."); return; }
    const confirm2 = prompt("Passwort wiederholen:");
    if (confirm2 !== password) { alert("Passwörter stimmen nicht überein."); return; }
  }
  for (const file of files) uploadFile(file, password, redundancy);
}

// handleFolderUpload: lädt einen ganzen Ordner (inkl. Unterordner) hoch. Jede
// Datei einzeln (chunked, mit Redundanz), sammelt Pfad+Hash+Size, erstellt am
// Ende ein Verzeichnis-Manifest. Die Ordnerstruktur bleibt so erhalten.
async function handleFolderUpload(files) {
  if (!files || !files.length) return;
  const redSel = document.getElementById('redundancy-select');
  const redundancy = redSel ? (parseInt(redSel.value, 10) || 5) : 5;
  // Ordnername aus dem ersten webkitRelativePath (erstes Segment).
  const firstPath = files[0].webkitRelativePath || files[0].name;
  const folderName = firstPath.split('/')[0] || 'Ordner';
  const list = document.getElementById('upload-list');
  const id = 'fold-' + Date.now();
  list.insertAdjacentHTML('beforeend', `
    <div class="upload-item" id="${id}">
      <span class="name">📁 ${folderName}</span>
      <span class="size">${files.length} ${FT.files_word}</span>
      <div class="prog"><div class="prog-bar" id="${id}-bar" style="width:0%%"></div></div>
      <span class="hash" id="${id}-stat">0/${files.length}</span>
    </div>`);
  const bar = document.getElementById(id + '-bar');
  const stat = document.getElementById(id + '-stat');
  const entries = [];
  let done = 0;
  try {
    for (const file of files) {
      // Relativen Pfad im Ordner ermitteln (ohne den Wurzelordner-Namen).
      const rel = (file.webkitRelativePath || file.name).split('/').slice(1).join('/') || file.name;
      // Sofort anzeigen, welche Datei gerade läuft (sonst wirkt es eingefroren).
      stat.textContent = (done+1) + '/' + files.length + ': ' + (rel.length > 24 ? '…'+rel.slice(-24) : rel);
      const hash = await resumableUpload(file, function(p){
        // Fortschritt der aktuellen Datei in den Balken einrechnen.
        const overall = (done + p) / files.length;
        bar.style.width = Math.round(overall * 100) + '%%';
      }, redundancy);
      entries.push({ path: rel, hash: hash, size: file.size, mime: file.type || '' });
      done++;
      bar.style.width = Math.round(done / files.length * 100) + '%%';
    }
    // Manifest erstellen (Ordner ins Netz + LocalIndex).
    stat.textContent = FT.creating_manifest;
    const r = await fetch('/api/v1/files/manifest', { credentials:"same-origin",
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: folderName, replicas: redundancy, entries: entries })
    });
    const d = await r.json();
    if (r.ok && d.ok) {
      stat.textContent = '✓ ' + (d.manifest_hash || '').slice(0,16) + '…';
      stat.style.color = 'var(--grn)';
      loadFiles();
    } else {
      stat.textContent = '✗ ' + (d.error || 'Fehler');
      stat.style.color = 'var(--red)';
    }
  } catch (e) {
    stat.textContent = '✗ ' + e.message;
    stat.style.color = 'var(--red)';
  }
}

// Hinweistext je nach gewählter Redundanz aktualisieren.
function updateRedundancyHint() {
  const sel = document.getElementById('redundancy-select');
  const hint = document.getElementById('redundancy-hint');
  if (!sel || !hint) return;
  const v = parseInt(sel.value, 10);
  if (v <= 1) {
    hint.textContent = FT.redundancy_1;
  } else if (v === 2) {
    hint.textContent = FT.redundancy_2;
  } else if (v === 3) {
    hint.textContent = FT.redundancy_3;
  } else if (v >= 8) {
    hint.textContent = FT.redundancy_8;
  } else {
    hint.textContent = FT.redundancy_5;
  }
}

async function uploadFile(file, password, redundancy) {
  const list = document.getElementById('upload-list');
  const id   = 'u-' + Date.now() + '-' + Math.random().toString(36).slice(2,6);
  const willEncrypt = !!password;
  list.insertAdjacentHTML('beforeend', `
    <div class="upload-item" id="${id}">
      <span class="name">${willEncrypt ? '🔒 ' : ''}${file.name}</span>
      <span class="size">${fmtSize(file.size)}</span>
      <div class="prog"><div class="prog-bar" id="${id}-bar" style="width:0%%"></div></div>
      <span class="hash" id="${id}-hash">${willEncrypt ? 'verschlüsseln…' : 'hochladen…'}</span>
    </div>`);

  const bar  = document.getElementById(id + '-bar');
  const hash = document.getElementById(id + '-hash');

  try {
    let toUpload = file;
    // Im Browser verschlüsseln (Ende-zu-Ende). Server sieht nur Chiffretext.
    if (password) {
      const encBlob = await FundusCrypto.encryptBlob(file, password);
      // .fnde-Endung markiert die Datei als verschlüsselt (für die Anzeige)
      toUpload = new File([encBlob], file.name + ".fnde", { type: "application/octet-stream" });
      hash.textContent = 'hochladen…';
    }
    const contentHash = await resumableUpload(toUpload, (p) => {
      bar.style.width = Math.round(p * 100) + '%%';
    }, redundancy);
    bar.style.width = '100%%';
    hash.textContent = contentHash ? contentHash.slice(0,24)+'…' : '✓';
    hash.style.color = 'var(--grn)';
    loadFiles();
  } catch(e) {
    hash.textContent = '✗ ' + e.message;
    hash.style.color = 'var(--red)';
  }
}

// ── Dateiliste ──────────────────────────────────────────────
async function loadFiles() {
  // Dateien aus dem persönlichen Index (eigener Endpoint, nicht stats)
  const r     = await fetch('/api/v1/files/list', {credentials:'same-origin'});
  const data  = await r.json().catch(()=>({}));
  const list  = document.getElementById('file-list');
  const files = data.files || [];

  if (!files.length) {
    list.innerHTML = '<div class="empty-hint">' + FT.no_files + '</div>';
    return;
  }
  // Dateien global halten, damit Umsortieren ohne Neuladen geht.
  window._fmFiles = files;
  await loadPartnerHashes();
  renderFileList();
}

// Hashes der Bilder/Videos im eigenen Partnerprofil (für die Markierung).
window._fmPartner = null;
async function loadPartnerHashes() {
  if (window._fmPartner) return;
  window._fmPartner = new Set();
  try {
    const r = await fetch('/api/v1/partner/profile', {credentials:'same-origin'});
    if (!r.ok) return;
    const p = await r.json();
    (p.image_hashes || []).concat(p.video_hashes || []).forEach(h => { if (h) window._fmPartner.add(String(h).toLowerCase()); });
  } catch(e) {}
}

// Sortierzustand: Spalte + Richtung.
window._fmSort = { col: 'name', dir: 1 };

function sortFiles(col) {
  if (window._fmSort.col === col) {
    window._fmSort.dir *= -1; // gleiche Spalte → Richtung umkehren
  } else {
    window._fmSort.col = col; window._fmSort.dir = 1;
  }
  renderFileList();
}


// copyHash: Hash in die Zwischenablage kopieren + kurze Info anzeigen.
function copyHash(hash) {
  if (!hash) return;
  const done = function(){ showToast((FT.hash_copied||'Hash kopiert') + ': ' + hash.slice(0,16) + '\u2026'); };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(hash).then(done).catch(function(){ fallbackCopy(hash, done); });
  } else { fallbackCopy(hash, done); }
}
function fallbackCopy(text, cb) {
  const ta = document.createElement('textarea');
  ta.value = text; ta.style.position='fixed'; ta.style.opacity='0';
  document.body.appendChild(ta); ta.select();
  try { document.execCommand('copy'); if(cb) cb(); } catch(e) {}
  document.body.removeChild(ta);
}
function showToast(msg) {
  let t = document.getElementById('fm-toast');
  if (!t) {
    t = document.createElement('div'); t.id = 'fm-toast';
    t.style.cssText = "position:fixed;bottom:24px;left:50%%;transform:translateX(-50%%);background:var(--grn);color:#062b16;padding:10px 18px;border-radius:8px;font-size:13px;font-weight:600;z-index:99999;box-shadow:0 4px 20px rgba(0,0,0,0.3);transition:opacity 0.3s";
    document.body.appendChild(t);
  }
  t.textContent = msg; t.style.opacity = '1';
  clearTimeout(window._toastTimer);
  window._toastTimer = setTimeout(function(){ t.style.opacity = '0'; }, 2000);
}

function renderFileList() {
  const list = document.getElementById('file-list');
  const files = (window._fmFiles || []).slice();
  const { col, dir } = window._fmSort;
  files.sort((a, b) => {
    let av, bv;
    if (col === 'size') { av = a.size||0; bv = b.size||0; }
    else if (col === 'redundancy') { av = a._red||0; bv = b._red||0; }
    else { av = (a.name||'').toLowerCase(); bv = (b.name||'').toLowerCase(); }
    if (av < bv) return -1*dir;
    if (av > bv) return 1*dir;
    return 0;
  });
  const arrow = (c) => window._fmSort.col===c ? (window._fmSort.dir>0?' ▲':' ▼') : '';
  let html = '<div class="file-row file-head">'+
    '<span class="fname" onclick="sortFiles(\'name\')">'+FT.col_name+arrow('name')+'</span>'+
    '<span class="fsize" onclick="sortFiles(\'size\')">'+FT.col_size+arrow('size')+'</span>'+
    '<span class="fhash">'+FT.col_hash+'</span>'+
    '<span class="fredundancy" title="'+FT.redundancy_col+'" onclick="sortFiles(\'redundancy\')">NRED'+arrow('redundancy')+'</span>'+
    '<span class="fbtns"></span></div>';
  html += files.map(f => {
    const nm = f.name || FT.unnamed;
    // Ordner-Eintrag (Manifest): anklickbar, öffnet die Baum-Ansicht.
    if (f.is_dir) {
      const cnt = f.count || 0;
      return `
    <div class="file-row">
      <span class="fname" title="${(nm||'').replace(/"/g,'&quot;')}" style="cursor:pointer;color:var(--green)" onclick="openFolder('${f.hash}','${nm.replace(/'/g,"")}')">📁 ${nm}</span>
      <span class="fsize">${fmtSize(f.size||0)}</span>
      <span class="fhash">${cnt} ${FT.files_word||'Dateien'}</span>
      <span class="fredundancy" style="font-size:12px;color:var(--muted)">📦</span>
      <div class="fbtns">
        <button class="btn-sm" onclick="openFolder('${f.hash}','${nm.replace(/'/g,"")}')">↗</button>
        <button class="btn-sm btn-danger" onclick="deleteFile('${f.hash}')">✕</button>
      </div>
    </div>`;
    }
    const isEnc = nm.endsWith('.fnde');
    const disp = isEnc ? ('🔒 ' + nm.slice(0, -5)) : nm;
    const mk = window.FundusMedia ? FundusMedia.kind(nm, f.mime_type) : null;
    const shared = !!f.shared;
    const shareLabel = shared ? '✅ geteilt' : '🔗 teilen';
    const shareCls = shared ? 'btn-sm btn-shared' : 'btn-sm';
    const rid = 'red-' + (f.hash||'').slice(0,16);
    const inPartner = !!(window._fmPartner && window._fmPartner.has(String(f.hash||'').toLowerCase()));
    return `
    <div class="file-row${inPartner ? ' fm-partner' : ''}">
      <span class="fname${mk ? ' fm-media' : ''}"${mk ? ` onclick="fmOpenMedia('${f.hash}')"` : ''} title="${(nm||'').replace(/"/g,'&quot;')}${inPartner ? ' – im Partnerprofil verwendet' : ''}${mk ? ' – klicken zum Anzeigen' : ''}">${inPartner ? '<span class="fm-partner-tag">💞 Partner</span> ' : ''}${mk ? (mk === 'image' ? '🖼 ' : mk === 'audio' ? '♪ ' : '▶ ') : ''}${disp}</span>
      <span class="fsize">${fmtSize(f.size||0)}</span>
      <span class="fhash" style="cursor:pointer" title="${FT.copy_hash_hint||'Klicken zum Kopieren'}" onclick="copyHash('${f.hash||''}')">${(f.hash||'').slice(0,14)}…</span>
      <span class="fredundancy" id="${rid}" style="color:var(--muted)" title="${FT.redundancy_col}">·</span>
      <div class="fbtns">
        <button class="${shareCls}" onclick="toggleShare('${f.hash}','${nm.replace(/'/g,"")}',${f.size||0},'${(f.mime_type||"")}',${isEnc},${shared})" title="${shared?FT.unshare:FT.make_findable}">${shared?'✅':'🔗'}</button>
        <button class="btn-sm" onclick="downloadFile('${f.hash}','${nm.replace(/'/g,"")}')">↓</button>
        <button class="btn-sm btn-danger" onclick="deleteFile('${f.hash}')">✕</button>
      </div>
    </div>`;
  }).join('');
  list.innerHTML = html;
  // Redundanz pro Datei lazy nachladen (nur echte Dateien, keine Ordner).
  (window._fmFiles || []).forEach(f => { if (!f.is_dir) loadRedundancy(f.hash); });
}

// ── Ordner-Baum-Navigation ──────────────────────────────────
// openFolder lädt das Manifest eines Ordners und zeigt seinen Inhalt als
// navigierbaren Baum (mit Breadcrumb). Unterordner sind wieder anklickbar.
window._folderNav = null; // { hash, name, manifest, path }

async function openFolder(manifestHash, name) {
  try {
    const r = await fetch('/api/v1/files/manifest/' + manifestHash, {credentials:'same-origin'});
    const m = await r.json();
    if (!r.ok) { alert(m.error || 'Ordner konnte nicht geladen werden'); return; }
    window._folderNav = { hash: manifestHash, name: name, manifest: m, path: '' };
    renderFolderView();
  } catch (e) { alert('Fehler: ' + e.message); }
}

function renderFolderView() {
  const nav = window._folderNav;
  if (!nav) return;
  const list = document.getElementById('file-list');
  const cur = nav.path; // aktueller Unterpfad, z.B. "strand/"
  // Einträge des aktuellen Pfads ermitteln: direkte Kinder (Dateien + Unterordner).
  const dirs = new Set();
  const filesHere = [];
  (nav.manifest.entries || []).forEach(e => {
    if (cur && !e.path.startsWith(cur)) return;
    const rest = cur ? e.path.slice(cur.length) : e.path;
    const slash = rest.indexOf('/');
    if (slash >= 0) {
      dirs.add(rest.slice(0, slash)); // Unterordner-Name
    } else {
      filesHere.push({ name: rest, hash: e.hash, size: e.size, mime: e.mime });
    }
  });
  // Breadcrumb aufbauen.
  const parts = cur.split('/').filter(Boolean);
  let crumb = `<span style="cursor:pointer;color:var(--green)" onclick="closeFolder()">📁 ${nav.name}</span>`;
  let acc = '';
  parts.forEach((p, i) => {
    acc += p + '/';
    const a = acc;
    crumb += ' / <span style="cursor:pointer;color:var(--green)" onclick="folderGoto(\''+a.replace(/'/g,"")+'\')">'+p+'</span>';
  });
  let html = '<div class="file-row file-head"><span style="flex:1">'+crumb+'</span>'+
    '<button class="btn-sm" onclick="closeFolder()">✕ '+(FT.close||'Schließen')+'</button></div>';
  // Unterordner zuerst.
  Array.from(dirs).sort().forEach(d => {
    const sub = (cur + d + '/').replace(/'/g,"");
    html += '<div class="file-row"><span class="fname" style="cursor:pointer;color:var(--green)" onclick="folderGoto(\''+sub+'\')">📁 '+d+'</span>'+
      '<span class="fsize"></span><span class="fhash"></span><span class="fredundancy"></span><div class="fbtns"></div></div>';
  });
  // Dann Dateien.
  filesHere.sort((a,b)=>a.name.localeCompare(b.name));
  window._fmFolderFiles = filesHere;
  filesHere.forEach(f => {
    const mk = window.FundusMedia ? FundusMedia.kind(f.name, f.mime_type) : null;
    html += '<div class="file-row"><span class="fname' + (mk ? ' fm-media' : '') + '"' +
      (mk ? ' onclick="fmOpenMedia(\'' + f.hash + '\',true)"' : '') +
      ' title="'+f.name.replace(/"/g,'&quot;')+'">' +
      (mk ? (mk === 'image' ? '🖼 ' : mk === 'audio' ? '♪ ' : '▶ ') : '') + f.name+'</span>'+
      '<span class="fsize">'+fmtSize(f.size||0)+'</span>'+
      '<span class="fhash">'+(f.hash||'').slice(0,16)+'…</span>'+
      '<span class="fredundancy"></span>'+
      '<div class="fbtns"><button class="btn-sm" onclick="downloadFile(\''+f.hash+'\',\''+f.name.replace(/'/g,"")+'\')">↓</button></div></div>';
  });
  list.innerHTML = html;
}

function folderGoto(path) {
  if (window._folderNav) { window._folderNav.path = path; renderFolderView(); }
}
function closeFolder() {
  window._folderNav = null;
  renderFileList(); // zurück zur normalen Dateiliste
}

// loadRedundancy holt die Netz-Redundanz einer Datei und zeigt sie an. Die
// Redundanz ist das Minimum der Replikatzahl über alle Chunks (schwächste Stelle).
async function loadRedundancy(hash){
  if(!hash) return;
  const el = document.getElementById('red-' + hash.slice(0,16));
  if(!el) return;
  try{
    const r = await fetch('/api/v1/files/redundancy/' + hash, {credentials:'same-origin'});
    if(!r.ok){ el.textContent=''; return; }
    const d = await r.json();
    const red = d.redundancy||0, tgt = d.target||0;
    // Wert für Sortierung merken.
    const fm = (window._fmFiles||[]).find(x => x.hash === hash);
    if (fm) fm._red = red;
    // Farbe nach Gesundheit: erfüllt = grün, teilweise = normal, keine = rot.
    let color = 'var(--muted)';
    if(red===0) color='var(--red)';
    else if(tgt>0 && red>=tgt) color='var(--grn)';
    el.style.color = color;
    el.textContent = '⛃ ' + red + (tgt>0 ? '/'+tgt : '');
    el.title = FT.redundancy_col + ' (' + red + ' ' + FT.of + ' ' + tgt + ', ' + (d.chunks||0) + ' ' + FT.chunks_word + ')';
  }catch(e){ el.textContent=''; }
}

// ── Download ─────────────────────────────────────────────────
async function downloadHash() {
  const hash = document.getElementById('dl-hash').value.trim();
  if (!hash) return;
  const st = document.getElementById('dl-status');
  st.textContent = FT.loading; st.style.color = 'var(--muted)';
  // Echten Dateinamen vom Backend erfragen (aus dem lokalen Index). Fällt auf
  // Hash+.bin zurück, wenn der Name unbekannt ist (Datei nur remote).
  let name = hash.slice(0,12) + '.bin';
  try {
    const nr = await fetch('/api/v1/files/name/' + hash, {credentials:'same-origin'});
    if (nr.ok) { const nd = await nr.json(); if (nd.name) name = nd.name; }
  } catch(e) {}
  await downloadFile(hash, name);
}

async function downloadFile(hash, name, peer) {
  // peer: Besitzer (Netzwerksuche) → Server fragt ihn zuerst nach Manifest/Chunks.
  const peerQ = (peer && peer !== 'local') ? ('?peer=' + encodeURIComponent(peer)) : '';
  const st = document.getElementById('dl-status');
  // Eindeutige Fortschritts-ID (wie beim Upload), damit der Download-Balken
  // identisch in der FundusProgress-Leiste erscheint.
  const dlId = "dl_" + hash.slice(0, 12);

  // Verfügbarkeit prüfen: Nach einem Upload kann die Replikation noch laufen.
  // Der availability-Check zählt aber nur LOKAL vorhandene Chunks — auf einem
  // anderen Pi, der die Datei per Pull holt, sind das anfangs 0. Wir warten
  // daher nur KURZ auf lokale Verfügbarkeit und starten den Download dann
  // trotzdem: Der Download-Handler holt fehlende Chunks aktiv von Peers.
  if (window.FundusProgress) FundusProgress.set(dlId, { name: (name || hash), state: "downloading", progress: 0 });
  try {
    for (let wait = 0; wait < 8; wait++) {
      let av;
      try {
        const ar = await fetch('/api/v1/files/availability/' + hash, {credentials:'same-origin'});
        if (ar.ok) av = await ar.json();
      } catch(e){}
      if (!av) break; // Endpunkt nicht verfügbar (alte Version) → direkt versuchen
      if (av.ready) break; // lokal vollständig → sofort herunterladen
      // Lokal (noch) nicht alle Chunks da. Kurz warten (frische Replikation),
      // dann aber trotzdem starten — der Download zieht den Rest von Peers.
      if (st) { st.textContent = "Datei wird geladen (" + av.have + "/" + av.total + " Teile lokal)…"; st.style.color='var(--muted)'; }
      if (av.total > 0 && av.have === 0 && wait >= 2) break; // nichts lokal → Pull-Download starten
      await new Promise(res => setTimeout(res, 1500));
    }
  } catch(e){}

  // ── Download-Strategie wählen ──────────────────────────────────────────
  // Beste Option: Streaming DIREKT auf die Festplatte via File System Access
  // API (showSaveFilePicker). Vorteile: (1) Speicherort wird VORHER abgefragt,
  // (2) jeder Chunk geht sofort auf die Platte statt in den RAM → kein
  // Speicherüberlauf bei großen Dateien. Verfügbar nur über HTTPS/localhost in
  // Chromium-Browsern. Für verschlüsselte (.fnde) Dateien geht das nicht, weil
  // die ganze Datei zum Entschlüsseln im Speicher liegen muss.
  const looksEncByName = (name && name.endsWith('.fnde'));
  const canStreamToDisk = (typeof window.showSaveFilePicker === 'function') && !looksEncByName;

  let fileHandle = null, writable = null;
  if (canStreamToDisk) {
    try {
      let suggested = name || hash;
      fileHandle = await window.showSaveFilePicker({ suggestedName: suggested });
      writable = await fileHandle.createWritable();
    } catch (e) {
      // Nutzer hat den Speicherort-Dialog abgebrochen → Download abbrechen.
      if (e && e.name === 'AbortError') {
        if (st) { st.textContent = FT.cancelled; st.style.color='var(--muted)'; }
        if (window.FundusProgress) FundusProgress.remove(dlId);
        return;
      }
      // Andere Fehler → auf RAM-Fallback zurückfallen.
      writable = null;
    }
  }

  try {
    const r = await fetch('/api/v1/files/download/' + hash + peerQ, {credentials:'same-origin'});
    if (!r.ok) {
      if (st) { st.textContent=FT.not_found; st.style.color='var(--red)'; }
      if (writable) { try { await writable.close(); } catch(_){} }
      if (window.FundusProgress) FundusProgress.set(dlId, { state: "error", error: FT.not_found });
      return;
    }

    const total = parseInt(r.headers.get('Content-Length') || '0', 10);
    const reader = r.body.getReader();
    let received = 0;

    // ── Pfad A: Streaming direkt auf die Festplatte (kein RAM-Überlauf) ────
    if (writable) {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        await writable.write(value);
        received += value.length;
        if (total > 0 && window.FundusProgress) {
          FundusProgress.set(dlId, { name: (name || hash), state: "downloading", progress: received / total });
        }
      }
      if (total > 0 && received < total) {
        try { await writable.close(); } catch(_){}
        if (st) { st.textContent = "✗ Download unvollständig (" + Math.round(received/total*100) + "%%)"; st.style.color='var(--red)'; }
        if (window.FundusProgress) FundusProgress.set(dlId, { state: "error", error: "unvollständig" });
        return;
      }
      await writable.close();
      if (window.FundusProgress) FundusProgress.done(dlId);
      if (st) { st.textContent=FT.download_started; st.style.color='var(--grn)'; }
      return;
    }

    // ── Pfad B: RAM-Fallback (HTTP, ältere Browser, oder verschlüsselt) ────
    const chunks = [];
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      received += value.length;
      if (total > 0 && window.FundusProgress) {
        FundusProgress.set(dlId, { name: (name || hash), state: "downloading", progress: received / total });
      }
    }
    // Vollständigkeit prüfen: bricht der Stream vorzeitig ab (fehlender Chunk
    // serverseitig), passt received nicht zu total → klare Fehlermeldung statt
    // stillem "content mismatch".
    if (total > 0 && received < total) {
      if (st) { st.textContent = "✗ Download unvollständig (" + Math.round(received/total*100) + "%%)"; st.style.color='var(--red)'; }
      if (window.FundusProgress) FundusProgress.set(dlId, { state: "error", error: "unvollständig" });
      return;
    }

    let buf = new Blob(chunks).arrayBuffer ? await new Blob(chunks).arrayBuffer() : null;
    let outName = name;

    // Verschlüsselt? (.fnde-Endung ODER Magic-Header) → Passwort abfragen
    const looksEnc = (name && name.endsWith('.fnde')) ||
                     (window.FundusCrypto && buf && FundusCrypto.isEncrypted(buf));
    if (looksEnc) {
      const pw = prompt("Passwort zum Entschlüsseln von:\n" + (name || hash));
      if (!pw) {
        if (st){st.textContent=FT.cancelled; st.style.color='var(--muted)';}
        if (window.FundusProgress) FundusProgress.remove(dlId);
        return;
      }
      if (st) { st.textContent=FT.decrypting; st.style.color='var(--muted)'; }
      try {
        buf = await FundusCrypto.decryptBuffer(buf, pw);
      } catch (e) {
        if (st) { st.textContent=FT.wrong_password; st.style.color='var(--red)'; }
        if (window.FundusProgress) FundusProgress.set(dlId, { state: "error", error: FT.wrong_password });
        return;
      }
      if (outName && outName.endsWith('.fnde')) outName = outName.slice(0, -5);
    }

    const blob = buf ? new Blob([buf]) : new Blob(chunks);
    const url  = URL.createObjectURL(blob);
    const a    = document.createElement('a');
    a.href = url; a.download = outName || hash; a.click();
    URL.revokeObjectURL(url);
    if (window.FundusProgress) FundusProgress.done(dlId);
    if (st) { st.textContent=FT.download_started; st.style.color='var(--grn)'; }
  } catch(e) {
    if (st) { st.textContent='✗ '+e.message; st.style.color='var(--red)'; }
    if (window.FundusProgress) FundusProgress.set(dlId, { state: "error", error: e.message });
  }
}

// Bild/Video/Audio groß anzeigen (media-viewer.js). Blättern mit ← → durch
// alle Medien der aktuellen Ansicht (Hauptliste bzw. geöffneter Ordner).
function fmOpenMedia(hash, inFolder) {
  if (!window.FundusMedia) return;
  const src = inFolder ? (window._fmFolderFiles || []) : (window._fmFiles || []);
  const list = src.filter(f => !f.is_dir && FundusMedia.kind(f.name || '', f.mime_type))
    .map(f => ({ hash: f.hash, name: f.name || '', mime: f.mime_type || '', size: f.size || 0 }));
  const idx = list.findIndex(x => x.hash === hash);
  FundusMedia.open(list, idx < 0 ? 0 : idx);
}

async function deleteFile(hash) {
  // Ohne Rückfrage; Zeile sofort entfernen, danach mit dem Server abgleichen.
  window._fmFiles = (window._fmFiles || []).filter(f => f.hash !== hash);
  if (window._fmFiles.length) { renderFileList(); }
  else { document.getElementById('file-list').innerHTML = '<div class="empty-hint">' + FT.no_files + '</div>'; }
  try {
    const r = await fetch('/api/v1/files/info/'+hash, {credentials:"same-origin", method:'DELETE'});
    if (!r.ok) { const d = await r.json().catch(()=>({})); alert('Löschen fehlgeschlagen: ' + (d.error || r.status)); }
  } catch(e) { alert('Löschen fehlgeschlagen: ' + e.message); }
  loadFiles();
}

function fmtSize(b) {
  if (b >= 1073741824) return (b/1073741824).toFixed(1)+' GB';
  if (b >= 1048576)    return (b/1048576).toFixed(1)+' MB';
  if (b >= 1024)       return (b/1024).toFixed(1)+' KB';
  return b+' B';
}

// ── Datei im Netzwerk freigeben / Freigabe aufheben ─────────
async function toggleShare(hash, name, size, mime, encrypted, currentlyShared) {
  try {
    if (currentlyShared) {
      const r = await fetch('/api/v1/files/unshare', { credentials:"same-origin",
        method:'POST', headers:{'Content-Type':'application/json'},
        body: JSON.stringify({ hash })
      });
      if (!r.ok) { alert('Aufheben fehlgeschlagen'); return; }
    } else {
      const r = await fetch('/api/v1/files/share', { credentials:"same-origin",
        method:'POST', headers:{'Content-Type':'application/json'},
        body: JSON.stringify({ hash, name, size, mime_type: mime, encrypted })
      });
      if (!r.ok) { alert('Freigabe fehlgeschlagen'); return; }
    }
    loadFiles(); // Status neu laden → Button aktualisiert sich
  } catch(e) { alert('Fehler: ' + e.message); }
}

// ── Netzwerk-Suche nach geteilten Dateien ───────────────────
let fsPollTimer = null;
async function fileSearch() {
  const q = document.getElementById('fs-query').value.trim();
  const box = document.getElementById('fs-results');
  box.innerHTML = '<div class="empty-hint">' + FT.searching_box + '</div>';
  if (fsPollTimer) { clearInterval(fsPollTimer); fsPollTimer = null; }
  try {
    const r = await fetch('/api/v1/files/search?q=' + encodeURIComponent(q), {credentials:'same-origin'});
    const d = await r.json();
    renderFsResults(d.hits || []);
    // Netzwerk-Antworten trudeln über den Rückkanal ein → kurz nachpollen
    if (d.search_id) {
      let polls = 0;
      fsPollTimer = setInterval(async () => {
        polls++;
        try {
          const rr = await fetch('/api/v1/files/search/results?id=' + d.search_id, {credentials:'same-origin'});
          const dd = await rr.json();
          renderFsResults(dd.hits || []);
        } catch(e) {}
        if (polls >= 6) { clearInterval(fsPollTimer); fsPollTimer = null; }
      }, 1500);
    }
  } catch(e) {
    box.innerHTML = '<div class="empty-hint">' + FT.error_word + ': ' + e.message + '</div>';
  }
}

function renderFsResults(hits) {
  const box = document.getElementById('fs-results');
  if (!hits.length) { box.innerHTML = '<div class="empty-hint">' + FT.no_shared_found + '</div>'; window._fsHits = []; return; }
  // Für die Anzeige (media-viewer.js): verschlüsselte Treffer tragen die
  // .fnde-Endung nicht zwingend im Namen – der Viewer erkennt sie daran.
  window._fsHits = hits.map(function(h){
    let n = h.name || h.hash;
    if (h.encrypted && !String(n).toLowerCase().endsWith('.fnde')) n = n + '.fnde';
    return { hash: h.hash, name: n, mime: h.mime_type || '', size: h.size || 0,
             peer: (h.from_peer && h.from_peer !== 'local') ? h.from_peer : '' };
  });
  box.innerHTML = hits.map(function(h){
    const lock = h.encrypted ? '🔒 ' : '';
    const nm = (h.name || h.hash);
    const src = h.from_peer === 'local' ? FT.local_src : FT.network;
    const mk = window.FundusMedia ? FundusMedia.kind(nm, h.mime_type) : null;
    const icon = mk ? (mk === 'image' ? '🖼 ' : mk === 'audio' ? '♪ ' : '▶ ') : '';
    return '<div class="file-row">'
      + '<span class="fname' + (mk ? ' fm-media' : '') + '"'
      + (mk ? ' onclick="fsOpenMedia(\'' + h.hash + '\')" title="klicken zum Anzeigen"' : '')
      + '>' + icon + lock + nm + '</span>'
      + '<span class="fsize">' + fmtSize(h.size||0) + '</span>'
      + '<span class="fhash">' + src + '</span>'
      + '<div class="fbtns"><button class="btn-sm btn-prim" onclick="downloadFile(\'' + h.hash + '\',\'' + nm.replace(/'/g,"") + '\',\'' + ((h.from_peer && h.from_peer !== 'local') ? h.from_peer : '') + '\')">↓</button></div>'
      + '</div>';
  }).join('');
}

// Treffer der Netzwerksuche groß anzeigen (Blättern mit ← → durch alle
// Bild-/Video-Treffer). Entfernte Dateien werden dabei über den eigenen Node
// gestreamt – er holt die benötigten Chunks von den Peers.
function fsOpenMedia(hash) {
  if (!window.FundusMedia) return;
  const list = (window._fsHits || []).filter(x => FundusMedia.kind(x.name, x.mime));
  const idx = list.findIndex(x => x.hash === hash);
  FundusMedia.open(list, idx < 0 ? 0 : idx);
}

// ── Laufwerks-Angebot: Regler pro gemountetem Laufwerk ──────────────────────
async function loadDrivesOffer(){
  const card=document.getElementById('drives-offer-card');
  if(!card) return;
  try{
    const r=await fetch('/api/v1/admin/storage/volumes');
    if(!r.ok) return; // z.B. kein Admin-Login → Bereich bleibt versteckt
    const d=await r.json();
    if(!d.volumes || d.volumes.length===0) return;
    card.style.display='';
    const minGb=(d.fairness_min_gb||0);
    const totalGb=(d.total_offer_gb||0);
    const minEl=document.getElementById('drives-offer-min');
    // Kleine Werte in MB darstellen, damit ein Eigenbedarf < 1 GB nicht als 0.0 GB
    // verschwindet.
    const fmtGB = (gb) => gb>0 && gb<1 ? (gb*1024).toFixed(1)+' MB' : gb.toFixed(1)+' GB';
    minEl.innerHTML=FT.offer_total+': <strong>'+fmtGB(totalGb)+'</strong> · '+
      FT.offer_min+': <strong>'+fmtGB(minGb)+'</strong>';
    minEl.style.color = totalGb>=minGb ? 'var(--grn)' : 'var(--red)';
    const list=document.getElementById('drives-offer-list');
    list.innerHTML=d.volumes.map(function(v){
      const pathEsc=(v.path||'').replace(/'/g,"\\'");
      // Obergrenze = freier Speicher + eigenes Angebot. Man kann nicht mehr
      // anbieten, als physisch frei ist (die eigenen bereits angebotenen GB zählen
      // dazu, da sie den freien Platz nicht wirklich belegen).
      const freeGb = (v.free_gb||0);
      const offerNow = (v.offer_gb||0);
      const minGb=Math.ceil(v.min_gb||0);
      // maxGb mindestens minGb (Fairness-Untergrenze), sonst free+offer.
      const maxGbNum = Math.max(minGb, Math.floor(freeGb + offerNow));
      const maxGb=maxGbNum.toFixed(0);
      const totalGb=(v.total_gb||0).toFixed(0);
      const usedGb=(v.used_gb||0).toFixed(1);
      // Regler-Wert nie unter der Fairness-Untergrenze dieses Laufwerks.
      const curGb=Math.max((v.offer_gb||0), minGb);
      const offer=curGb.toFixed(1);
      const off = v.online ? '' : ' <span style="color:var(--red)">('+FT.offline+')</span>';
      const pct = (maxGbNum>0) ? Math.round(curGb/maxGbNum*100) : 0;
      const minNote = minGb>0 ? ' <span style="color:var(--muted);font-size:11px">(min '+minGb+' GB)</span>' : '';
      return '<div class="drive-offer-row" style="margin-bottom:12px">'+
        '<div style="display:flex;justify-content:space-between;font-size:13px">'+
        '<span>'+escapeHtmlF(v.label||v.path)+off+minNote+'</span>'+
        '<span style="color:var(--muted)">'+freeGb.toFixed(0)+' GB frei / '+totalGb+' GB</span></div>'+
        '<div style="display:flex;align-items:center;gap:8px;margin-top:4px">'+
        '<input type="range" min="'+minGb+'" max="'+maxGb+'" step="1" value="'+curGb+'" '+
        'style="flex:1" oninput="this.nextElementSibling.textContent=this.value+\' GB\'" '+
        'onchange="setDriveOffer(\''+pathEsc+'\',this.value)">'+
        '<span class="drive-offer-val" style="width:64px;text-align:right;font-size:12px">'+offer+' GB</span>'+
        '<span style="width:40px;color:var(--muted);font-size:11px">'+pct+'%%</span>'+
        '</div></div>';
    }).join('');

    // Zusätzlich: erkannte Laufwerke, die für Fundus NICHT zugänglich sind
    // (per udisks2 nur für den Desktop-User gemountet). Diese bietet das Frontend
    // zum Einbinden an — der fundus-helper remountet sie zugänglich.
    loadMountableDrives();

    // Chain-Verschiebung: Wählschalter nur zeigen, wenn es mehr als eine
    // (online) Platte gibt — sonst gibt es kein sinnvolles Ziel.
    const chainSel=document.getElementById('chain-move-card');
    if(chainSel){
      const targets=d.volumes.filter(function(v){ return v.online && !v.primary; });
      if(targets.length>=1){
        chainSel.style.display='';
        let opts='<option value="">'+FT.chain_target_pick+'</option>';
        targets.forEach(function(v){
          const pe=(v.path||'').replace(/'/g,"\\'");
          opts+='<option value="'+escapeHtmlF(v.path)+'">'+escapeHtmlF(v.label||v.path)+'</option>';
        });
        document.getElementById('chain-move-select').innerHTML=opts;
      } else {
        chainSel.style.display='none';
      }
    }
  }catch(e){}
}

// Erkannte Laufwerke laden und die NICHT zugänglichen (udisks2-Mounts, für den
// Node nicht lesbar) zum Einbinden anbieten. Der fundus-helper remountet sie
// zugänglich nach /mnt/fundus-<uuid> und merkt sie für den Autostart.
async function loadMountableDrives(){
  const box=document.getElementById('drives-mountable');
  if(!box) return;
  try{
    const r=await fetch('/api/v1/admin/storage/drives');
    if(!r.ok){ box.innerHTML=''; return; }
    const d=await r.json();
    const drives=(d.drives||[]).filter(function(dr){
      // Nur Laufwerke anbieten, die NICHT zugänglich und NICHT schon für Fundus
      // eingebunden sind — und die eine UUID haben (für den Helper nötig).
      return !dr.accessible && !dr.for_fundus && dr.uuid;
    });
    if(drives.length===0){ box.innerHTML=''; return; }
    box.innerHTML='<div style="font-size:13px;color:var(--muted);margin-bottom:6px">'+
      (FT.drives_mountable_hint||'Erkannte Laufwerke, die noch nicht für Fundus zugänglich sind:')+'</div>'+
      drives.map(function(dr){
        const name=escapeHtmlF((dr.mount_point||dr.device||'').split('/').pop()||dr.device);
        return '<div class="drive-offer-row" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">'+
          '<span style="font-size:13px">💾 '+name+' <span style="color:var(--muted);font-size:11px">('+escapeHtmlF(dr.fs_type||'')+')</span></span>'+
          '<button class="btn-sm btn-prim" onclick="mountDriveForFundus(\''+dr.uuid+'\',this)">'+
          (FT.drives_mount_btn||'Für Fundus einbinden')+'</button></div>';
      }).join('');
  }catch(e){ box.innerHTML=''; }
}

async function mountDriveForFundus(uuid, btn){
  if(btn){ btn.disabled=true; btn.textContent=FT.drives_mounting||'Binde ein…'; }
  try{
    const r=await fetch('/api/v1/admin/system/mount',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ uuid: uuid })
    });
    const d=await r.json();
    if(r.ok && d.ok){
      // Wirklich erfolgreich → Laufwerksliste + Regler neu laden.
      if(btn){ btn.textContent='✓ Eingebunden'; }
      setTimeout(function(){ loadDrivesOffer(); loadMountableDrives(); }, 800);
    } else {
      // Mount fehlgeschlagen — echten Fehler des Helpers anzeigen (nicht verschlucken).
      const msg = d.error || ('HTTP '+r.status);
      if(btn){ btn.disabled=false; btn.textContent='✗ '+msg; btn.title=msg; }
      console.error('Mount fehlgeschlagen:', msg);
    }
  }catch(e){ if(btn){ btn.disabled=false; btn.textContent='✗ '+e.message; } }
}

async function moveChain(target){
  if(!target) return;
  const msg=document.getElementById('chain-move-msg');
  if(!confirm(FT.chain_move_confirm)){
    document.getElementById('chain-move-select').value='';
    return;
  }
  msg.textContent=FT.chain_moving; msg.style.color='var(--muted)';
  try{
    const r=await fetch('/api/v1/admin/storage/chain-move',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ target: target })
    });
    const d=await r.json();
    if(r.ok && d.ok){
      msg.textContent='✓ '+(d.note||FT.chain_moving);
      msg.style.color='var(--grn)';
    } else {
      msg.textContent='⚠ '+((d&&d.error)||'Fehler');
      msg.style.color='var(--red)';
    }
  }catch(e){
    msg.textContent='⚠ '+FT.chain_move_err; msg.style.color='var(--red)';
  }
  document.getElementById('chain-move-select').value='';
}

async function setDriveOffer(path, gb){
  const msg=document.getElementById('drives-offer-msg');
  msg.textContent=FT.saving; msg.style.color='var(--muted)';
  try{
    const r=await fetch('/api/v1/admin/storage/volume-offer',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ path: path, offer_gb: parseFloat(gb) })
    });
    const d=await r.json();
    if(r.ok && d.ok){
      msg.textContent='✓ '+FT.saved+' ('+FT.offer_total+': '+(d.total_offer_gb||0).toFixed(1)+' GB)';
      msg.style.color='var(--grn)';
      loadDrivesOffer();
    }else{
      msg.textContent='✗ '+(d.error||FT.error_word); msg.style.color='var(--red)';
      loadDrivesOffer();
    }
  }catch(e){ msg.textContent='✗ '+e.message; msg.style.color='var(--red)'; }
}

function escapeHtmlF(s){
  if(s==null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

loadDrivesOffer();

// ── Lokales Verzeichnis freigeben (ohne Chunking, via ShareManager) ──────────
async function createDirShare() {
  const name = document.getElementById('share-dir-name').value.trim();
  const path = document.getElementById('share-dir-path').value.trim();
  const msg  = document.getElementById('share-dir-msg');
  if (!name || !path) { msg.textContent = FT.share_dir_need; msg.style.color='var(--red)'; return; }
  msg.textContent = FT.share_dir_creating; msg.style.color='var(--muted)';
  try {
    const r = await fetch('/api/v1/shares', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({
        name: name,
        dirs: [{ virtual_name: name, local_path: path, flat_listing: true }],
        read_access: { mode: 'public' }
      })
    });
    const d = await r.json();
    if (r.ok && (d.id || d.share_id || d.ok !== false)) {
      msg.innerHTML = '\u2713 ' + FT.share_dir_ok; msg.style.color = 'var(--grn)';
      document.getElementById('share-dir-name').value = '';
      document.getElementById('share-dir-path').value = '';
      loadDirShares();
    } else {
      msg.textContent = '\u2717 ' + (d.error || 'Fehler'); msg.style.color = 'var(--red)';
    }
  } catch(e) { msg.textContent = '\u2717 ' + e.message; msg.style.color='var(--red)'; }
}

async function loadDirShares() {
  try {
    const r = await fetch('/api/v1/shares');
    const d = await r.json();
    const box = document.getElementById('share-dir-list');
    if (!box) return;
    const shares = d.shares || d || [];
    if (!shares.length) { box.innerHTML = ''; return; }
    box.innerHTML = shares.map(function(s){
      const dirs = (s.dirs||[]).map(function(dr){return dr.local_path;}).join(', ');
      const sid = s.id || s.share_id;
      return '<div class="file-row" style="grid-template-columns:1fr auto">'+
        '<span class="fname" style="cursor:pointer;color:var(--green)" onclick="openShare(\''+sid+'\',\''+(s.name||'').replace(/\'/g,"")+'\')" title="'+dirs+'">\ud83d\udcc2 '+(s.name||'')+'</span>'+
        '<button class="btn-sm btn-danger" onclick="deleteDirShare(\''+sid+'\')" title="Freigabe entfernen">\u2717</button></div>';
    }).join('');
  } catch(e) {}
}

window._shareNav = null;
async function openShare(shareID, name) { window._shareNav = { id: shareID, name: name, path: '' }; renderShareView(); }
async function renderShareView() {
  const nav = window._shareNav; if (!nav) return;
  const box = document.getElementById('share-dir-list');
  box.innerHTML = '<div class="empty-hint">Laedt...</div>';
  try {
    const url = '/api/v1/shares/' + nav.id + '/ls' + (nav.path ? '/' + nav.path : '');
    const r = await fetch(url); const d = await r.json();
    const items = d.entries || d.items || d || [];
    const parts = nav.path.split('/').filter(Boolean);
    let crumb = '<span style="cursor:pointer;color:var(--green)" onclick="closeShareView()">\ud83d\udcc2 '+nav.name+'</span>';
    let acc='';
    parts.forEach(function(p){ acc+=p+'/'; const a=acc; crumb+=' / <span style="cursor:pointer;color:var(--green)" onclick="shareGoto(\''+a.replace(/\'/g,"")+'\')">'+p+'</span>'; });
    let html = '<div class="file-row file-head" style="grid-template-columns:1fr auto"><span>'+crumb+'</span>'+
      '<span><button class="btn-sm" onclick="mirrorShare(\''+nav.id+'\',\''+nav.path.replace(/\'/g,"")+'\')">\u2b07 Spiegeln</button> '+
      '<button class="btn-sm" onclick="closeShareView()">\u2717</button></span></div>';
    items.forEach(function(it){
      const isDir = it.is_dir || it.IsDir; const nm = it.name || it.Name;
      const vp = (nav.path ? nav.path + '/' : '') + nm;
      if (isDir) {
        html += '<div class="file-row" style="grid-template-columns:1fr"><span class="fname" style="cursor:pointer;color:var(--green)" onclick="shareGoto(\''+vp.replace(/\'/g,"")+'\')">\ud83d\udcc1 '+nm+'</span></div>';
      } else {
        html += '<div class="file-row" style="grid-template-columns:1fr auto"><span class="fname">'+nm+'</span>'+
          '<button class="btn-sm" onclick="window.open(\'/api/v1/shares/'+nav.id+'/dl/'+encodeURIComponent(vp)+'\')">\u2193</button></div>';
      }
    });
    box.innerHTML = html;
  } catch(e) { box.innerHTML = '<div class="empty-hint">Fehler: '+e.message+'</div>'; }
}
function shareGoto(path){ if(window._shareNav){ window._shareNav.path=path; renderShareView(); } }
function closeShareView(){ window._shareNav=null; loadDirShares(); }
async function mirrorShare(shareID, subPath) {
  const target = prompt('Lokaler Zielpfad fuer die Spiegelung:', '/mnt/fundus-mirror');
  if (!target) return;
  const box = document.getElementById('share-dir-list'); const old = box.innerHTML;
  box.innerHTML = '<div class="empty-hint">Spiegelung laeuft...</div>';
  try {
    const r = await fetch('/api/v1/shares/' + shareID + '/mirror', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ sub_path: subPath || '', target: target })
    });
    const d = await r.json();
    if (r.ok && d.ok) { box.innerHTML = '<div class="empty-hint" style="color:var(--grn)">\u2713 '+(d.count||0)+' Dateien gespiegelt nach '+target+'</div>'; }
    else { box.innerHTML = old; alert('\u2717 ' + (d.error || 'Spiegelung fehlgeschlagen')); }
  } catch(e) { box.innerHTML = old; alert('\u2717 ' + e.message); }
}

async function deleteDirShare(id) {
  if (!id) return;
  try { await fetch('/api/v1/shares/' + id, { method:'DELETE' }); loadDirShares(); } catch(e) {}
}

// loadNetworkShares: entdeckt Verzeichnis-Freigaben anderer Nodes im Netz (via DHT).
async function loadNetworkShares(silent) {
  const box = document.getElementById('net-shares-list');
  if (!box) return;
  if(!silent) box.innerHTML = '<div class="empty-hint">' + (FT.loading||'Suche im Netz…') + '</div>';
  try {
    const r = await fetch('/api/v1/shares/network');
    const d = await r.json();
    const peers = d.peers || [];
    if (!peers.length) { box.innerHTML = '<div class="empty-hint">'+(FT.net_shares_none||'Keine Netz-Freigaben gefunden')+'</div>'; return; }
    let html = '';
    peers.forEach(function(p){
      let shares = [];
      try { shares = JSON.parse(p.shares); } catch(e) { shares = p.shares || []; }
      if (!Array.isArray(shares)) shares = [shares];
      shares.forEach(function(s){
        const pid = (p.peer_id||'');
        const pidShort = pid.slice(0,12);
        html += '<div class="file-row" style="grid-template-columns:1fr auto">'+
          '<span class="fname" style="cursor:pointer;color:var(--green)" onclick="openRemoteShare(\''+pid+'\',\''+(s.id||'')+'\',\''+(s.name||'?').replace(/'/g,"")+'\')" title="Node '+pidShort+'…">🌐 '+(s.name||'?')+' <span class="meta" style="font-size:10px">('+(s.files||0)+' '+(FT.files_word||'Dateien')+')</span></span>'+
          '<span><button class="btn-sm" onclick="openRemoteShare(\''+pid+'\',\''+(s.id||'')+'\',\''+(s.name||'?').replace(/'/g,"")+'\')">↗</button></span></div>';
      });
    });
    box.innerHTML = html || '<div class="empty-hint">'+(FT.net_shares_none||'Keine Netz-Freigaben')+'</div>';
  } catch(e) { box.innerHTML = '<div class="empty-hint">Fehler: '+e.message+'</div>'; }
}

window._remoteNav = null;
async function openRemoteShare(peer, shareID, name) {
  window._remoteNav = { peer: peer, share: shareID, name: name, path: '' };
  renderRemoteShareView();
}
async function renderRemoteShareView() {
  const nav = window._remoteNav; if (!nav) return;
  const box = document.getElementById('net-shares-list');
  box.innerHTML = '<div class="empty-hint">Laedt...</div>';
  try {
    const url = '/api/v1/shares/remote/'+nav.peer+'/'+nav.share+'/ls?path='+encodeURIComponent(nav.path);
    const r = await fetch(url); const d = await r.json();
    if (d.error) { box.innerHTML = '<div class="empty-hint">'+d.error+'</div>'; return; }
    const items = d.entries || [];
    const parts = nav.path.split('/').filter(Boolean);
    let crumb = '<span style="cursor:pointer;color:var(--green)" onclick="closeRemoteShare()">\ud83c\udf10 '+nav.name+'</span>';
    let acc='';
    parts.forEach(function(p){ acc+=p+'/'; const a=acc; crumb+=' / <span style="cursor:pointer;color:var(--green)" onclick="remoteGoto(\''+a.replace(/\'/g,"")+'\')">'+p+'</span>'; });
    let html = '<div class="file-row file-head" style="grid-template-columns:1fr auto"><span>'+crumb+'</span>'+
      '<span><button class="btn-sm" onclick="mirrorRemoteShare()">\u2b07 Spiegeln</button> '+
      '<button class="btn-sm" onclick="closeRemoteShare()">\u2717</button></span></div>';
    items.forEach(function(it){
      const isDir = it.is_dir || it.IsDir; const nm = it.name || it.Name;
      const vp = (nav.path ? nav.path + '/' : '') + nm;
      if (isDir) {
        html += '<div class="file-row" style="grid-template-columns:1fr"><span class="fname" style="cursor:pointer;color:var(--green)" onclick="remoteGoto(\''+vp.replace(/\'/g,"")+'\')">\ud83d\udcc1 '+nm+'</span></div>';
      } else {
        html += '<div class="file-row" style="grid-template-columns:1fr auto"><span class="fname">'+nm+'</span>'+
          '<button class="btn-sm" onclick="window.open(\'/api/v1/shares/remote/'+nav.peer+'/'+nav.share+'/dl?path='+encodeURIComponent(vp)+'\')">\u2193</button></div>';
      }
    });
    box.innerHTML = html;
  } catch(e) { box.innerHTML = '<div class="empty-hint">Fehler: '+e.message+'</div>'; }
}
function remoteGoto(path){ if(window._remoteNav){ window._remoteNav.path=path; renderRemoteShareView(); } }
function closeRemoteShare(){ window._remoteNav=null; loadNetworkShares(); }
async function mirrorRemoteShare() {
  const nav = window._remoteNav; if (!nav) return;
  const target = prompt('Lokaler Zielpfad fuer die Spiegelung:', '/mnt/fundus-mirror/'+nav.name);
  if (!target) return;
  const box = document.getElementById('net-shares-list');
  box.innerHTML = '<div class="empty-hint">Spiegelung laeuft... (kann bei vielen Dateien dauern)</div>';
  try {
    const r = await fetch('/api/v1/shares/remote/'+nav.peer+'/'+nav.share+'/mirror', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ sub_path: nav.path || '', target: target })
    });
    const d = await r.json();
    if (r.ok && d.ok) { box.innerHTML = '<div class="empty-hint" style="color:var(--grn)">\u2713 '+(d.count||0)+' Dateien gespiegelt nach '+target+'</div>'; }
    else { box.innerHTML = '<div class="empty-hint" style="color:var(--red)">\u2717 '+(d.error||'Fehler')+'</div>'; }
  } catch(e) { box.innerHTML = '<div class="empty-hint" style="color:var(--red)">\u2717 '+e.message+'</div>'; }
}

// Netz-Freigaben NICHT beim Seitenladen blockierend abrufen — erst verzögert,
// nachdem die Seite (und ein evtl. lokaler Ordner-Browser) da ist. So wartet der
// lokale Datei-Bereich nie auf die (P2P-abhängige) Netz-Freigaben-Abfrage.
if (document.getElementById('net-shares-list')) {
  setTimeout(function(){ loadNetworkShares(false); }, 1200);
}
window._netPollTimer = setInterval(function(){
  if (window._remoteNav) return;
  if (document.getElementById('net-shares-list')) { loadNetworkShares(true); }
}, 10000);

// openDirBrowser: Popup zum Durchsuchen lokaler Ordner (für die Freigabe-Auswahl).
async function openDirBrowser(startPath) {
  let ov = document.getElementById('dir-browser');
  if (!ov) {
    ov = document.createElement('div');
    ov.id = 'dir-browser';
    ov.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.6);display:flex;align-items:center;justify-content:center;z-index:9999";
    ov.innerHTML = '<div style="background:var(--sur);border:1px solid var(--brd);border-radius:10px;padding:16px;width:min(520px,92vw);max-height:80vh;display:flex;flex-direction:column">'+
      '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">'+
      '<strong id="db-path" style="font-size:12px;word-break:break-all"></strong>'+
      '<button class="btn-sm" onclick="document.getElementById(\'dir-browser\').remove()">✕</button></div>'+
      '<div id="db-list" style="overflow-y:auto;flex:1;font-size:13px"></div>'+
      '<div style="margin-top:10px;display:flex;gap:8px">'+
      '<button class="btn-sm btn-prim" id="db-pick" style="flex:1">Diesen Ordner freigeben</button></div></div>';
    document.body.appendChild(ov);
    ov.addEventListener('click', function(e){ if(e.target===ov) ov.remove(); });
  }
  loadDirBrowser(startPath || '');
}

async function loadDirBrowser(path) {
  const listEl = document.getElementById('db-list');
  const pathEl = document.getElementById('db-path');
  const pickBtn = document.getElementById('db-pick');
  // Ladeindikator sofort anzeigen, damit klar ist dass geladen wird.
  listEl.innerHTML = '<div style="display:flex;align-items:center;justify-content:center;padding:24px;gap:10px;color:var(--text-dim)"><span class="spinner"></span> Lade Ordner…</div>';
  const _t0 = performance.now();
  try {
    const r = await fetch('/api/v1/files/browse?path=' + encodeURIComponent(path), {credentials:'same-origin'});
    const _t1 = performance.now();
    const d = await r.json();
    const _t2 = performance.now();
    console.log('[dir-browser] fetch:', Math.round(_t1-_t0)+'ms, json:', Math.round(_t2-_t1)+'ms, path:', path||'(root)');
    pathEl.textContent = d.path || 'Laufwerke';
    let html = '';
    if (d.parent !== undefined && d.parent !== '' || (d.path && d.path !== '')) {
      const up = d.parent || '';
      html += '<div class="file-row" style="cursor:pointer;grid-template-columns:1fr" onclick="loadDirBrowser(\''+up.replace(/'/g,"")+'\')">📁 ..</div>';
    }
    (d.items||[]).forEach(function(it){
      html += '<div class="file-row" style="cursor:pointer;grid-template-columns:1fr" onclick="loadDirBrowser(\''+it.path.replace(/'/g,"")+'\')">📁 '+it.name+'</div>';
    });
    listEl.innerHTML = html || '<div class="empty-hint">Keine Unterordner</div>';
    // "Freigeben"-Button nur aktiv wenn ein echter Pfad gewählt ist.
    if (d.path && d.path !== '') {
      pickBtn.style.display = '';
      pickBtn.onclick = function(){
        document.getElementById('share-dir-path').value = d.path;
        if (!document.getElementById('share-dir-name').value) {
          document.getElementById('share-dir-name').value = d.path.split('/').pop();
        }
        document.getElementById('dir-browser').remove();
      };
    } else {
      pickBtn.style.display = 'none';
    }
  } catch(e) { listEl.innerHTML = '<div class="empty-hint">Fehler: '+e.message+'</div>'; }
}


loadFiles();
loadDirShares();
</script>
]],
  t("files.upload"),
  t("files.drop_title"), t("files.drop_sub"),
  t("files.my_files"), t("files.refresh"),
  t("files.no_files"),
  t("files.download_hash"), t("files.download")
))

  render.footer()
end

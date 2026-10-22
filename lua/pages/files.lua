-- pages/files.lua – Dezentraler Filemanager
-- Upload via XChaCha20-Poly1305, 5× redundante Speicherung im DHT
local render = require "render"
local cjson  = require "cjson.safe"

return function()
  ngx.header["Content-Type"] = "text/html"
  -- /shared: dieselbe Dateiansicht, NUR LESEND (auch von außen). Zeigt die
  -- Freigaben des ganzen Fundus-Netzes; Verwaltung ist ausgeblendet und
  -- serverseitig ohnehin nur aus dem Heimnetz erreichbar.
  local ro = (ngx.var.uri == "/shared")
  local t = render.header(ro and "nav.shared_label" or "files.title", ro and "shared" or "files")
  if ro then
    ngx.print([==[<script>document.documentElement.classList.add('fm-ro');window.FM_READONLY=true;</script>
<style>html.fm-ro .files-stats-row, html.fm-ro .file-card:not(.fm-list-card):not(.fm-share-card), html.fm-ro .info-notice, html.fm-ro .files-drives-card{display:none!important}
html.fm-ro .fm-list-card .file-card-head{gap:8px;flex-wrap:wrap}
/* R714: Freigaben SICHTBAR, aber nichts zum Anlegen oder Entfernen. */
html.fm-ro .fm-share-neu{display:none!important}
html.fm-ro .fm-share-card .btn-danger{display:none!important}
/* Filterfeld wie das Texteingabefeld im Messenger */
#fm-filter{flex:1;min-width:140px;max-width:420px;padding:11px 16px;border-radius:22px;border:1px solid var(--border);
  background:var(--bg,#0e0e1a);color:var(--text);font-size:14px;transition:border-color .18s ease, box-shadow .18s ease}
#fm-filter:focus{outline:none;border-color:var(--green-ctrl);box-shadow:0 0 0 3px rgba(23,168,98,0.18), 0 0 14px rgba(23,168,98,0.22)}</style>]==])
    -- R649: Hinweis, wo Hochladen und Löschen zu finden sind. Diese Ansicht
    -- zeigt bewusst nur die Freigaben des Netzes und ist rein lesend; wer hier
    -- landet, sucht aber oft den Dateimanager.
    ngx.print([==[<div class="info-notice" style="display:block!important;margin-bottom:12px">
  Diese Ansicht zeigt die Freigaben des gesamten Netzes, nur zum Ansehen.
  Eigene Dateien hochladen, freigeben oder löschen:
  <a href="/files" style="font-weight:600">zum Dateimanager</a>
</div>]==])
  end

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
      <button type="button" class="btn-sm" onclick="document.getElementById('file-input').click()" aria-label="Neue Datei">📄 ]] .. t("files.upload_files_btn") .. [[</button>
      <button type="button" class="btn-sm" onclick="document.getElementById('folder-input').click()" aria-label="Neuer Ordner">📁 ]] .. t("files.upload_folder_btn") .. [[</button>
    </div>
    <div id="upload-list" class="upload-list"></div>
  </div>
</div>

<!-- ── Lokales Verzeichnis freigeben (ohne Chunking) ────────── -->
<!-- R714: Klasse fm-share-card. Die Browser-Ansicht (/shared) blendet alle
     Karten außer der Dateiliste aus – dadurch fehlten dort ausgerechnet die
     Freigaben, um die es in dieser Ansicht geht. Sie wird jetzt gezeigt, nur
     die Teile zum ANLEGEN bleiben verborgen (die Ansicht ist rein lesend). -->
<div class="file-card fm-share-card">
  <div class="file-card-head"><h3>]] .. t("files.share_dir_title") .. [[</h3></div>
  <div class="file-card-body">
    <div class="fm-share-neu">
    <p class="meta" style="margin:0 0 8px 0">]] .. t("files.share_dir_hint") .. [[</p>
    <div class="inline-form" style="flex-wrap:wrap;gap:6px">
      <input type="text" id="share-dir-name" placeholder="]] .. t("files.share_dir_name_ph") .. [[" style="flex:1;min-width:120px">
      <input type="text" id="share-dir-path" placeholder="]] .. t("files.share_dir_path_ph") .. [[" style="flex:2;min-width:180px">
      <button class="btn-sm" type="button" onclick="openDirBrowser()" aria-label="Ordner öffnen">📂 ]] .. t("files.browse_btn") .. [[</button>
      <button class="btn-sm btn-prim" onclick="createDirShare()">]] .. t("files.share_dir_btn") .. [[</button>
    </div>
    <!-- R708: Lesen und Schreiben getrennt wählbar, jeweils mit der
         Reichweite. „Heimnetz" ist dabei die engste Stufe: Solche Freigaben
         werden auch nicht im Netz angekündigt, sonst sähen andere sie in der
         Liste und kämen nie hinein. -->
    <div class="inline-form" style="flex-wrap:wrap;gap:10px;margin-top:8px;align-items:center">
      <label style="font-size:12px;color:var(--muted)">Lesen
        <select id="share-dir-read" style="margin-left:6px">
          <option value="lan">nur Heimnetz</option>
          <option value="public" selected>ganzes Netz</option>
          <option value="contacts">nur meine Kontakte</option>
          <option value="specific">nur bestimmte Nodes</option>
        </select>
      </label>
      <label style="font-size:12px;color:var(--muted)">Schreiben
        <select id="share-dir-write" style="margin-left:6px">
          <option value="none" selected>niemand</option>
          <option value="lan">nur Heimnetz</option>
          <option value="public">ganzes Netz</option>
          <option value="contacts">nur meine Kontakte</option>
          <option value="specific">nur bestimmte Nodes</option>
        </select>
      </label>
      <!-- R707: Peer-IDs, NICHT Fundus-Adressen. Die Prüfung vergleicht die
           Node-Kennung (12D3Koo…), nicht die Kontoadresse (0x…). Mit Adressen
           hätte die Liste nie gepasst und die Freigabe wäre wirkungslos. -->
      <input type="text" id="share-dir-wlist" placeholder="Node-Kennungen (12D3Koo…), durch Komma getrennt"
             style="flex:2;min-width:220px;display:none">
    </div>
    <div id="share-dir-whint" class="meta" style="margin-top:4px;font-size:11px;color:#ffc107;display:none">
      Mit Schreibrecht können andere Dateien in diesen Ordner legen und vorhandene
      überschreiben. Gib das nur frei, wenn du den Ordner dafür vorgesehen hast.
    </div>
    <div id="share-dir-msg" class="status-line" style="margin-top:6px"></div>
    </div><!-- /fm-share-neu -->
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
<div class="file-card fm-list-card">
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
  /* Größe, Quelle/Redundanz und Hash nebeneinander – früher lagen alle drei im
     selben Rasterbereich und damit ÜBEREINANDER. */
  .file-row { grid-template-columns: max-content max-content minmax(0,1fr) auto; column-gap:10px; row-gap:2px;
              grid-template-areas: "name name name btns" "size red hash btns"; }
  .file-row .fname { grid-area:name; }
  .file-row .fsize { grid-area:size; font-size:10.5px; }
  .file-row .fredundancy { grid-area:red; font-size:10.5px; }
  .file-row .fhash { grid-area:hash; font-size:10.5px; min-width:0; }
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
  background:rgba(126,226,168,.07); border:1px solid rgba(126,226,168,.25);
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
// Nur-Lese-Modus (/shared): ALLE Freigaben im Fundus-Netz laden (leere Suche),
// Treffer anderer Nodes kommen nach und nach dazu. Das Filterfeld filtert und
// sortiert nur die geladene Liste – es startet keine neue Netzsuche.
async function loadFilesShared() {
  const list = document.getElementById('file-list');
  const card = list.closest('.file-card');
  const head = card && card.querySelector('.file-card-head');
  if (head && !document.getElementById('fm-filter')) {
    const h3 = head.querySelector('h3'); if (h3) h3.textContent = 'Freigaben im Fundus-Netz';
    const inp = document.createElement('input');
    inp.type = 'search'; inp.id = 'fm-filter'; inp.placeholder = 'Filtern …';
    // Aussehen per CSS (#fm-filter), wie das Messenger-Eingabefeld
    inp.oninput = function(){ window._fmFilter = this.value.trim().toLowerCase(); renderFileList(); };
    head.insertBefore(inp, head.querySelector('button'));
  }
  list.innerHTML = '<div class="empty-hint">⏳ Suche Freigaben im Netz …</div>';
  const seen = {};
  window._fmFiles = [];
  function add(hits){
    let n = 0;
    (hits || []).forEach(function(h){
      const k = String(h.hash || '').toLowerCase();
      if (!/^[0-9a-f]{64}$/.test(k) || seen[k]) return;
      seen[k] = 1; n++;
      window._fmFiles.push({ hash: k, name: h.name, size: h.size, mime_type: h.mime_type, encrypted: h.encrypted, from_peer: h.from_peer });
    });
    return n;
  }
  let sid = '';
  try {
    const r = await fetch('/api/v1/files/search?q=', {credentials:'same-origin'});
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    sid = d.search_id; add(d.hits);
    if (window._fmFiles.length) renderFileList();
  } catch(e) { list.innerHTML = '<div class="empty-hint">Freigaben nicht abrufbar: ' + e.message + '</div>'; return; }
  for (let i = 0; i < 6; i++) {
    await new Promise(function(res){ setTimeout(res, 1500); });
    try {
      const r2 = await fetch('/api/v1/files/search/results?id=' + encodeURIComponent(sid), {credentials:'same-origin'});
      if (add((await r2.json()).hits)) renderFileList();
    } catch(e) {}
  }
  if (!window._fmFiles.length) list.innerHTML = '<div class="empty-hint" data-empty="shared">Im Netz sind derzeit keine Dateien freigegeben.</div>';
}

async function loadFiles() {
  if (window.FM_READONLY) return loadFilesShared();
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
  const RO = !!window.FM_READONLY;
  const flt = window._fmFilter || '';
  const files = (window._fmFiles || []).filter(function(f){ return !flt || String(f.name || '').toLowerCase().indexOf(flt) >= 0; });
  if (!files.length && flt) { list.innerHTML = '<div class="empty-hint">Keine Datei passt zum Filter.</div>'; return; }
  const { col, dir } = window._fmSort;
  files.sort((a, b) => {
    let av, bv;
    if (col === 'size') { av = a.size||0; bv = b.size||0; }
    else if (col === 'redundancy') { av = RO ? String(a.from_peer||'') : (a._red||0); bv = RO ? String(b.from_peer||'') : (b._red||0); }
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
    '<span class="fredundancy" title="'+(RO ? 'Quelle' : FT.redundancy_col)+'" onclick="sortFiles(\'redundancy\')">'+(RO ? 'Quelle' : 'NRED')+arrow('redundancy')+'</span>'+
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
        <button class="btn-sm" onclick="openFolder('${f.hash}','${nm.replace(/'/g,"")}')" aria-label="Öffnen">↗</button>
        <button class="btn-sm btn-danger" onclick="deleteFile('${f.hash}')" aria-label="Schließen">✕</button>
      </div>
    </div>`;
    }
    const isEnc = nm.endsWith('.fnde');
    const disp = isEnc ? ('🔒 ' + nm.slice(0, -5)) : nm;
    const mk = fmKind(nm, f.mime_type);
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
      <span class="fredundancy" id="${rid}" style="color:var(--muted);font-size:12px" title="${RO ? 'Quelle' : FT.redundancy_col}">${RO ? (f.from_peer === 'local' ? 'dieser Node' : '…' + String(f.from_peer||'').slice(-6)) : '·'}</span>
      <div class="fbtns">
        ${RO ? '' : `<button class="${shareCls}" onclick="toggleShare('${f.hash}','${nm.replace(/'/g,"")}',${f.size||0},'${(f.mime_type||"")}',${isEnc},${shared})" title="${shared?FT.unshare:FT.make_findable}">${shared?'✅':'🔗'}</button>`}
        <button class="btn-sm" onclick="downloadFile('${f.hash}','${nm.replace(/'/g,"")}')" aria-label="Herunterladen">↓</button>
        ${RO ? '' : `<button class="btn-sm btn-danger" onclick="deleteFile('${f.hash}')" aria-label="Schließen">✕</button>`}
      </div>
    </div>`;
  }).join('');
  list.innerHTML = html;
  // Redundanz pro Datei lazy nachladen (nur echte Dateien, keine Ordner).
  if (!RO) (window._fmFiles || []).forEach(f => { if (!f.is_dir) loadRedundancy(f.hash); });
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
    const mk = fmKind(f.name, f.mime_type);
    html += '<div class="file-row"><span class="fname' + (mk ? ' fm-media' : '') + '"' +
      (mk ? ' onclick="fmOpenMedia(\'' + f.hash + '\',true)"' : '') +
      ' title="'+f.name.replace(/"/g,'&quot;')+'">' +
      (mk ? (mk === 'image' ? '🖼 ' : mk === 'audio' ? '♪ ' : '▶ ') : '') + f.name+'</span>'+
      '<span class="fsize">'+fmtSize(f.size||0)+'</span>'+
      '<span class="fhash">'+(f.hash||'').slice(0,16)+'…</span>'+
      '<span class="fredundancy"></span>'+
      '<div class="fbtns"><button class="btn-sm" onclick="downloadFile(\''+f.hash+'\',\''+f.name.replace(/'/g,"")+'\')" aria-label="Herunterladen">↓</button></div></div>';
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

// ── Medien erkennen OHNE media-viewer.js ──────────────────────────────────
// Die Erkennung hing früher an media-viewer.js: fehlte das Modul im Browser
// (nicht geladen/alt), blieben die Namen stumm unklickbar – ohne jede Meldung.
const FM_IMG = ['jpg','jpeg','png','gif','webp','bmp','svg','avif','heic','ico'];
const FM_VID = ['mp4','m4v','webm','mov','mkv','ogv','3gp','ts','m2ts','mts','flv','avi','wmv','mpg','mpeg','vob'];
const FM_AUD = ['mp3','wav','ogg','oga','m4a','aac','flac','opus'];
function fmKind(name, mime) {
  let n = String(name || '').toLowerCase();
  if (n.endsWith('.fnde')) n = n.slice(0, -5);
  const i = n.lastIndexOf('.'), e = i >= 0 ? n.slice(i + 1) : '';
  const m = String(mime || '').toLowerCase();
  if (FM_IMG.indexOf(e) >= 0 || m.indexOf('image/') === 0) return 'image';
  if (FM_VID.indexOf(e) >= 0 || m.indexOf('video/') === 0) return 'video';
  if (FM_AUD.indexOf(e) >= 0 || m.indexOf('audio/') === 0) return 'audio';
  return null;
}
// Anzeige-Modul sicherstellen: fehlt es, beim Klick nachladen – sonst klare Meldung.
async function fmEnsureViewer() {
  if (window.FundusMedia) return true;
  try {
    await new Promise(function(res, rej){
      const sc = document.createElement('script');
      sc.src = '/static/media-viewer.js?v=' + Date.now();
      sc.onload = res;
      sc.onerror = function(){ rej(new Error('/static/media-viewer.js konnte nicht geladen werden')); };
      document.head.appendChild(sc);
    });
    if (window.FundusMedia) return true;
    throw new Error('media-viewer.js geladen, aber ohne Anzeige-Funktion (veraltete Datei?)');
  } catch (e) {
    alert('Anzeige nicht verfügbar: ' + e.message + '\n\nBitte die Seite mit Strg+F5 neu laden. Hilft das nicht, den Node aktualisieren (Einstellungen → Software-Update).');
    return false;
  }
}
// Beim Laden prüfen und sichtbar melden, statt still zu scheitern.
document.addEventListener('DOMContentLoaded', function(){
  if (window.FundusMedia) return;
  const box = document.getElementById('fs-results');
  if (box && box.parentNode) {
    const w = document.createElement('div');
    w.className = 'empty-hint';
    w.style.color = 'var(--warning, #7ee2a8)';
    w.textContent = '⚠ Anzeige-Modul (media-viewer.js) nicht geladen – Medien öffnen es beim Klick nach. Falls das scheitert: Seite mit Strg+F5 neu laden.';
    box.parentNode.insertBefore(w, box);
  }
  if (window.console) console.warn('Fundus Dateimanager: media-viewer.js fehlt');
});

// Bild/Video/Audio groß anzeigen (media-viewer.js). Blättern mit ← → durch
// alle Medien der aktuellen Ansicht (Hauptliste bzw. geöffneter Ordner).
async function fmOpenMedia(hash, inFolder) {
  if (!(await fmEnsureViewer())) return;
  const src = inFolder ? (window._fmFolderFiles || []) : (window._fmFiles || []);
  const list = src.filter(f => !f.is_dir && fmKind(f.name || '', f.mime_type))
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
    const mk = fmKind(nm, h.mime_type);
    const icon = mk ? (mk === 'image' ? '🖼 ' : mk === 'audio' ? '♪ ' : '▶ ') : '';
    return '<div class="file-row">'
      + '<span class="fname' + (mk ? ' fm-media' : '') + '"'
      + (mk ? ' onclick="fsOpenMedia(\'' + h.hash + '\')" title="klicken zum Anzeigen"' : '')
      + '>' + icon + lock + nm + '</span>'
      + '<span class="fsize">' + fmtSize(h.size||0) + '</span>'
      + '<span class="fhash">' + src + '</span>'
      + '<div class="fbtns"><button class="btn-sm btn-prim" onclick="downloadFile(\'' + h.hash + '\',\'' + nm.replace(/'/g,"") + '\',\'' + ((h.from_peer && h.from_peer !== 'local') ? h.from_peer : '') + '\')" aria-label="Herunterladen">↓</button></div>'
      + '</div>';
  }).join('');
}

// Treffer der Netzwerksuche groß anzeigen (Blättern mit ← → durch alle
// Bild-/Video-Treffer). Entfernte Dateien werden dabei über den eigenen Node
// gestreamt – er holt die benötigten Chunks von den Peers.
async function fsOpenMedia(hash) {
  if (!(await fmEnsureViewer())) return;
  const list = (window._fsHits || []).filter(x => fmKind(x.name, x.mime));
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

// ── Schreibfreigabe (R706) ───────────────────────────────────────────────────
// Die Regel wird aus der Auswahl gebaut. "list" verlangt Adressen – ohne sie
// wäre die Freigabe wirkungslos, das wäre schlimmer als eine Fehlermeldung.
function dirRegel(feldId) {
  const sel = document.getElementById(feldId);
  const mode = sel ? sel.value : 'none';
  if (mode !== 'specific') return { mode: mode };
  // R707: Feld heißt peer_ids, nicht fundus_ids – und die Kennungen bleiben
  // unverändert, NICHT kleingeschrieben: Peer-IDs sind Base58 und
  // unterscheiden Groß- und Kleinschreibung. Kleingeschrieben hätte keine
  // einzige übereingestimmt.
  const roh = (document.getElementById('share-dir-wlist') || {}).value || '';
  const ids = roh.split(',').map(function(x){ return x.trim(); }).filter(Boolean);
  return { mode: 'specific', peer_ids: ids };
}

// Auswahl sichtbar machen: Adressfeld und Warnhinweis nur wenn nötig.
(function dirZugriffBinden(){
  const rd = document.getElementById('share-dir-read');
  const wr = document.getElementById('share-dir-write');
  if (!rd || !wr) return;
  const auffrischen = function(){
    const liste = document.getElementById('share-dir-wlist');
    const hint  = document.getElementById('share-dir-whint');
    // Adressfeld, sobald EINE der beiden Wahlen bestimmte Nodes verlangt.
    if (liste) liste.style.display =
      (rd.value === 'specific' || wr.value === 'specific') ? '' : 'none';
    // Warnhinweis nur beim Schreiben – Lesen ist nicht gefährlich.
    if (hint) hint.style.display = (wr.value === 'none') ? 'none' : '';
  };
  rd.addEventListener('change', auffrischen);
  wr.addEventListener('change', auffrischen);
  auffrischen();
})();

// ── Neuer Ordner (R706) ──────────────────────────────────────────────────────
// Legt den Ordner an, den man freigeben will. Bisher musste man ihn vorher über
// eine Konsole erzeugen – für den häufigsten Fall, einen eigens angelegten
// Ablageordner, war das ein Bruch im Ablauf.
async function dirNeuerOrdner() {
  const pfadFeld = document.getElementById('share-dir-path');
  const msg = document.getElementById('share-dir-msg');
  const basis = (pfadFeld.value || '').trim();
  const name = prompt('Name des neuen Ordners' + (basis ? '\n\nwird angelegt in: ' + basis : ''));
  if (!name) return;
  msg.textContent = 'Ordner wird angelegt …'; msg.style.color = 'var(--muted)';
  try {
    const r = await fetch('/api/v1/shares/mkdir', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ base: basis, name: name })
    });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    pfadFeld.value = d.path || basis;
    msg.textContent = '\u2713 Ordner angelegt: ' + (d.path || '');
    msg.style.color = 'var(--grn)';
    // Namensfeld vorbelegen, falls leer – spart einen Schritt.
    const nf = document.getElementById('share-dir-name');
    if (nf && !nf.value.trim()) nf.value = name;
  } catch (e) {
    msg.textContent = '\u2717 ' + e.message; msg.style.color = 'var(--red)';
  }
}

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
        read_access: dirRegel('share-dir-read'),
        write_access: dirRegel('share-dir-write')
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
    const r = await fetch('/api/v1/shares', { credentials:'same-origin' });
    const d = await r.json();
    const box = document.getElementById('share-dir-list');
    if (!box) return;
    // R710: dieselbe Falle wie beim Verzeichnislisten – ohne „shares" landete
    // das Antwortobjekt in der Liste.
    if (!r.ok || d.error) {
      box.innerHTML = '<div class="empty-hint">\u2717 ' +
        escapeHtmlF(d.error || ('HTTP ' + r.status)) + '</div>';
      return;
    }
    const shares = Array.isArray(d.shares) ? d.shares : [];
    if (!shares.length) { box.innerHTML = ''; return; }
    box.innerHTML = shares.map(function(s){
      const dirs = (s.dirs||[]).map(function(dr){return dr.local_path;}).join(', ');
      const sid = s.id || s.share_id;
      return '<div class="file-row" style="grid-template-columns:1fr auto">'+
        '<span class="fname" style="cursor:pointer;color:var(--green)" onclick="openShare(\''+sid+'\',\''+(s.name||'').replace(/\'/g,"")+'\')" title="'+dirs+'">\ud83d\udcc2 '+(s.name||'')+'</span>'+
        // R777: Umfang anzeigen. Die Zahlen liegen in der Antwort bereits vor
        // (total_size_bytes, total_files) und wurden nur nie dargestellt.
        // "85 GB in 3677 Dateien" sagt mehr als eine nackte Anzahl.
        '<span class="meta" style="font-size:11px;white-space:nowrap">'+
        (s.total_size_bytes ? fmtSize(s.total_size_bytes) : '')+
        (s.total_files ? ' \u00b7 '+s.total_files+' '+(FT.files_word||'Dateien') : '')+
        '</span>'+
        // R725: Netzlaufwerk-Zugang schon in der LISTE anbieten – man muss die
        // Freigabe dafür nicht erst öffnen.
        '<span>'+
        '<button class="btn-sm btn-danger" onclick="deleteDirShare(\''+sid+'\')" title="Freigabe entfernen">\u2717</button></span></div>';
    }).join('');
  } catch(e) {
    const box = document.getElementById('share-dir-list');
    if (box) box.innerHTML = '<div class="empty-hint">\u2717 ' + escapeHtmlF(e.message) + '</div>';
  }
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
    // R709: KEINE Ersatzkette auf das ganze Antwortobjekt. Bei leerer Liste
    // liefert der Node null, bei einem Fehler {error:…} – in beiden Fällen
    // landete vorher das Objekt selbst in items, und items.forEach schlug fehl.
    // Genau diese Meldung stand im Fehlerkasten statt des wahren Grundes.
    if (!r.ok || d.error) {
      box.innerHTML = '<div class="empty-hint">\u2717 ' +
        escapeHtmlF(d.error || ('HTTP ' + r.status)) + '</div>';
      return;
    }
    const items = Array.isArray(d.entries) ? d.entries
                : (Array.isArray(d.items) ? d.items : []);
    const parts = nav.path.split('/').filter(Boolean);
    let crumb = '<span style="cursor:pointer;color:var(--green)" onclick="closeShareView()">\ud83d\udcc2 '+nav.name+'</span>';
    let acc='';
    parts.forEach(function(p){ acc+=p+'/'; const a=acc; crumb+=' / <span style="cursor:pointer;color:var(--green)" onclick="shareGoto(\''+a.replace(/\'/g,"")+'\')">'+p+'</span>'; });
    // R721: Hochladen nur anbieten, wenn die Freigabe auch beschreibbar ist.
    // Ein Knopf, der dann an der Zugriffsregel scheitert, ist schlimmer als
    // keiner – man sucht den Fehler bei sich.
    const darfSchreiben = !!(d.can_write || (window._shareWrite && window._shareWrite[nav.id]));
    let html = '<div class="file-row file-head" style="grid-template-columns:1fr auto"><span>'+crumb+'</span>'+
      '<span>'+
      (darfSchreiben
        ? '<button class="btn-sm btn-prim" onclick="shareUploadDialog(\''+nav.id+'\',\''+
          nav.path.replace(/'/g,"")+'\')" aria-label="Datei hochladen">\u2b06 Hochladen</button> '
        : '')+
      '<button class="btn-sm" onclick="mirrorShare(\''+nav.id+'\',\''+nav.path.replace(/\'/g,"")+'\')">\u2b07 Spiegeln</button> '+
      '<button class="btn-sm" onclick="closeShareView()">\u2717</button></span></div>';
    items.forEach(function(it){
      const isDir = it.is_dir || it.IsDir; const nm = it.name || it.Name;
      const vp = (nav.path ? nav.path + '/' : '') + nm;
      if (isDir) {
        html += '<div class="file-row" style="grid-template-columns:1fr"><span class="fname" style="cursor:pointer;color:var(--green)" onclick="shareGoto(\''+vp.replace(/\'/g,"")+'\')">\ud83d\udcc1 '+nm+'</span></div>';
      } else {
        // R714: Bilder, Videos und Textdateien direkt anzeigen statt nur
        // herunterladen. Der Name ist jetzt anklickbar; der Pfeil daneben
        // bleibt fürs Herunterladen.
        const url = '/api/v1/shares/'+nav.id+'/dl/'+encodeURIComponent(vp);
        const ansehbar = istAnsehbar(nm);
        html += '<div class="file-row" style="grid-template-columns:1fr auto auto auto;gap:8px">'+
          (ansehbar
            ? '<span class="fname" style="cursor:pointer;color:var(--green)" onclick="shareAnsehen(\''+
              url.replace(/'/g,"")+'\',\''+nm.replace(/'/g,"")+'\')">'+shareIcon(nm)+' '+nm+'</span>'
            : '<span class="fname">'+nm+'</span>')+
          '<span class="meta" style="font-size:11px;white-space:nowrap">'+
            (it.size ? fmtSize(it.size) : '')+'</span>'+
          // R720: Bei Videos eine Abspiel-Verknüpfung anbieten. Der Node liefert
          // dabei nur die Bytes, dekodiert wird im Abspieler – damit läuft auch
          // 4K-HEVC, das kein Browser schafft.
          // R773: Abspiel-Knopf entfallen; Office-Formate bleiben.
          (appFuer(nm)
            ? '<button class="btn-sm" onclick="inAppOeffnen(\''+
              url.replace('/dl/', '/playlist/')+'\',\''+nm.replace(/'/g,"")+'\')" '+
              'title="In der zugehörigen Anwendung öffnen" '+
              'aria-label="In Anwendung öffnen">\u2197</button>' : '')+
          '<button class="btn-sm" onclick="dateiHolenMitFortschritt(\''+url+'?dl=1\',\''+nm.replace(/'/g,"")+'\','+(it.size||0)+')" aria-label="Herunterladen">\u2193</button></div>';
      }
    });
    box.innerHTML = html;
  } catch(e) { box.innerHTML = '<div class="empty-hint">Fehler: '+e.message+'</div>'; }
}
// ── Dateien aus einer Freigabe direkt ansehen (R714) ─────────────────────
// Die Freigabe liefert echte URLs mit Bereichsabfragen, deshalb genügt ein
// schlichtes Overlay: <img> und <video> laden selbst, Text wird geholt.
// Bewusst NICHT FundusMedia: Das arbeitet über Inhalts-Hashes aus dem
// Chunk-Speicher, eine Freigabe hat aber Pfade. Es hätte nichts gefunden.
// R775: Herunterladen OHNE leeres Fenster.
//
// window.open() öffnet erst einen leeren Tab, der dann den Download auslöst und
// sich schließt – je nach Browser bleibt er auch offen stehen. Ein unsichtbarer
// Verweis mit download-Kennzeichen erledigt dasselbe, ohne dass ein Fenster
// erscheint.
function dateiHolen(url, name){
  const a = document.createElement('a');
  a.href = url;
  if (name) a.download = name;   // Dateiname vorschlagen
  a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  setTimeout(function(){ a.remove(); }, 1000);
}

// ── Download mit Fortschritt und Durchsatz (R777) ────────────────────────
//
// Statt den Browser laden zu lassen, holen wir selbst und zeigen Fortschritt
// und MB/s. Das ist gerade nach der 32-MB-Falle aus R774 wichtig: Man sieht,
// dass eine Datei VOLLSTÄNDIG ankommt, statt es zu hoffen.
//
// Mehrere gleichzeitig sind ausdrücklich vorgesehen – jeder Download bekommt
// eine eigene Zeile und kann einzeln abgebrochen werden.
//
// Der Haken, den man kennen muss: Die Datei entsteht dabei im Arbeitsspeicher
// des Browsers, weil wir sie erst am Ende als Ganzes übergeben können. Für
// sehr große Dateien ist das zu viel, deshalb gibt es ab 2 GB den direkten
// Weg ohne Anzeige.
const DL_GRENZE = 2 * 1024 * 1024 * 1024;

function dlBereich(){
  let b = document.getElementById('dl-liste');
  if (!b) {
    b = document.createElement('div');
    b.id = 'dl-liste';
    b.className = 'dl-liste';
    document.body.appendChild(b);
  }
  return b;
}

let dlNr = 0;
async function dateiHolenMitFortschritt(url, name, groesse){
  // Sehr große Dateien: direkt, ohne Zwischenspeicher im Browser.
  if (groesse && groesse > DL_GRENZE) { dateiHolen(url, name); return; }

  const id = 'dl' + (++dlNr);
  const zeile = document.createElement('div');
  zeile.className = 'dl-eintrag';
  zeile.innerHTML =
    '<div class="dl-kopf"><span class="dl-name"></span>'+
    '<button class="btn-sm dl-ab" aria-label="Abbrechen">\u2717</button></div>'+
    '<div class="dl-bar"><div class="dl-fuell"></div></div>'+
    '<div class="dl-info">\u2026</div>';
  zeile.querySelector('.dl-name').textContent = name || 'Download';
  dlBereich().appendChild(zeile);
  const fuell = zeile.querySelector('.dl-fuell');
  const info  = zeile.querySelector('.dl-info');

  const abbr = new AbortController();
  zeile.querySelector('.dl-ab').onclick = function(){ abbr.abort(); };

  const start = Date.now();
  try {
    const r = await fetch(url, { credentials: 'same-origin', signal: abbr.signal });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    // Gesamtlänge: aus der Kopfzeile, sonst die übergebene Größe.
    const gesamt = parseInt(r.headers.get('Content-Length') || '0', 10) || groesse || 0;
    const leser = r.body && r.body.getReader ? r.body.getReader() : null;
    if (!leser) { dateiHolen(url, name); zeile.remove(); return; }

    const teile = [];
    let geladen = 0, letzteAnzeige = 0;
    for (;;) {
      const { done, value } = await leser.read();
      if (done) break;
      teile.push(value);
      geladen += value.length;
      // Höchstens fünfmal je Sekunde neu zeichnen – sonst bremst die Anzeige
      // den Download spürbar aus.
      const jetzt = Date.now();
      if (jetzt - letzteAnzeige > 200) {
        letzteAnzeige = jetzt;
        const sek = (jetzt - start) / 1000;
        const mbs = sek > 0 ? (geladen / sek / (1024*1024)) : 0;
        if (gesamt > 0) {
          const p = Math.min(100, geladen / gesamt * 100);
          fuell.style.width = p.toFixed(1) + '%%';
          const rest = mbs > 0 ? (gesamt - geladen) / (mbs * 1024*1024) : 0;
          info.textContent = fmtSize(geladen) + ' / ' + fmtSize(gesamt) +
            '  \u00b7  ' + mbs.toFixed(1) + ' MB/s' +
            (rest > 1 ? '  \u00b7  noch ' + dlRestzeit(rest) : '');
        } else {
          // Ohne bekannte Gesamtlänge kein Balken, nur Menge und Tempo.
          fuell.style.width = '100%%';
          info.textContent = fmtSize(geladen) + '  \u00b7  ' + mbs.toFixed(1) + ' MB/s';
        }
      }
    }
    // Vollständigkeit prüfen – das ist der eigentliche Gewinn gegenüber dem
    // Browser-Download: Eine zu kurze Datei fällt sofort auf.
    if (gesamt > 0 && geladen !== gesamt) {
      throw new Error('unvollständig: ' + fmtSize(geladen) + ' von ' + fmtSize(gesamt));
    }
    const blob = new Blob(teile);
    const burl = URL.createObjectURL(blob);
    dateiHolen(burl, name);
    setTimeout(function(){ URL.revokeObjectURL(burl); }, 60000);
    fuell.style.width = '100%%';
    const sek = (Date.now() - start) / 1000;
    info.textContent = '\u2713 ' + fmtSize(geladen) + ' in ' + sek.toFixed(1) + ' s  \u00b7  ' +
      (geladen / sek / (1024*1024)).toFixed(1) + ' MB/s';
    zeile.classList.add('fertig');
    setTimeout(function(){ zeile.remove(); }, 8000);
  } catch (e) {
    if (e.name === 'AbortError') {
      info.textContent = 'abgebrochen';
    } else {
      info.textContent = '\u2717 ' + e.message;
      zeile.classList.add('fehler');
    }
    zeile.classList.add('fertig');
    setTimeout(function(){ zeile.remove(); }, 12000);
  }
}

function dlRestzeit(sek){
  if (sek < 90) return Math.round(sek) + ' s';
  if (sek < 5400) return Math.round(sek/60) + ' min';
  return (sek/3600).toFixed(1) + ' h';
}

function shareDateiArt(name){
  const n = String(name||'').toLowerCase();
  if (/\.(jpe?g|png|gif|webp|bmp|avif|svg)$/.test(n)) return 'bild';
  // R716: Zwei Arten von Video. Was der Browser selbst abspielt, wird DIREKT
  // ausgeliefert – dort ist Springen verlustfrei möglich. Alles andere
  // (Matroska, AVI, TS …) packt der Node unterwegs in MP4 um; dabei wird das
  // Bild nur durchgereicht, kaum Rechenzeit.
  //
  // Meine Annahme aus R715, Chromium spiele .mkv ab, wenn es als video/webm
  // ausgewiesen wird, war falsch: Chromes Zerleger verlangt auch WebM-Codecs
  // (VP9, AV1), ein übliches .mkv enthält aber H.264 oder HEVC.
  // R773: NUR noch, was der Browser von sich aus abspielt.
  //
  // Matroska, AVI, TS und Ähnliches sind raus. Das Umpacken über ffmpeg und
  // der Umweg über einen Abspieler haben sich nicht bewährt: Bei 4K-HEVC kam
  // nur Ton, und über P2P setzt die Platte der Gegenseite die Grenze
  // (gemessen 3 bis 5 MB/s). Eine Anzeige, die meist nicht funktioniert, ist
  // schlechter als gar keine – solche Dateien werden jetzt heruntergeladen.
  if (/\.(mp4|webm|ogv|m4v)$/.test(n))                 return 'video';
  if (/\.(mp3|m4a|aac|wav|flac|ogg|opus)$/.test(n))   return 'ton';
  if (/\.(txt|md|log|csv|json|xml|ya?ml|ini|conf|nfo|srt|sh|lua|js|go|py)$/.test(n)) return 'text';
  if (/\.pdf$/.test(n)) return 'pdf';
  return '';
}
function istAnsehbar(name){ return shareDateiArt(name) !== ''; }

// ── In der zugehörigen Anwendung öffnen (R721) ───────────────────────────
// Office kennt eigene Adressschemata: ms-word:ofe|u|<Adresse> öffnet die Datei
// unmittelbar aus dem Netz, ohne sie vorher herunterzuladen. Dasselbe gibt es
// für Excel, PowerPoint und Visio.
const appSchema = {
  '.docx':'ms-word',  '.doc':'ms-word',  '.rtf':'ms-word',  '.odt':'ms-word',
  '.xlsx':'ms-excel', '.xls':'ms-excel', '.csv':'ms-excel', '.ods':'ms-excel',
  '.pptx':'ms-powerpoint', '.ppt':'ms-powerpoint', '.odp':'ms-powerpoint',
  '.vsdx':'ms-visio',
};
function appFuer(name){
  const n = String(name||'').toLowerCase();
  const punkt = n.lastIndexOf('.');
  if (punkt < 0) return '';
  return appSchema[n.slice(punkt)] || '';
}
// Muss der Node die Datei zum Abspielen umpacken?
function shareIcon(name){
  return {bild:'\ud83d\uddbc', video:'\ud83c\udfac',
          ton:'\ud83c\udfb5', text:'\ud83d\udcc4', pdf:'\ud83d\udcd5'}[shareDateiArt(name)] || '';
}

// R773: Der Umweg über eine Abspiel-Verknüpfung ist entfallen. Formate, die
// der Browser nicht beherrscht, werden heruntergeladen statt angezeigt.
function shareAnsehen(url, name){
  return shareAnsehenFenster(url, name);
}

function shareAnsehenFenster(url, name){
  // R717: let, nicht const. Weiter unten wird art auf 'hinweis' gesetzt, wenn
  // eine fremde Freigabe nicht umgepackt werden kann – mit const bricht das mit
  // "Assignment to constant variable" ab, und zwar erst beim Klick, nicht beim
  // Laden. Genau derselbe Fehler wie in R657.
  let art = shareDateiArt(name);
  // R775: Kein Fenster, direkt holen – das war der leere Tab vor dem Download.
  if (!art) { dateiHolen(url, name); return; }
  const ov = document.createElement('div');
  ov.className = 'share-view-ov';
  let inhalt;
  // R716: Beim Umpacken die Stream-Adresse statt der Datei selbst. Sie entsteht
  // aus der Download-Adresse: /dl/ wird zu /stream/ (eigene Freigabe) bzw. der
  // Fernweg bekommt /stream statt /dl.
  // R716: Umpacken gibt es nur für Freigaben DIESES Node. Bei einer fremden
  // müsste die Datei erst vollständig über das P2P-Netz geholt werden, bevor
  // ffmpeg sie anfassen kann – das wäre kein Streamen mehr. Statt eine Adresse
  // anzufragen, die es nicht gibt, wird das offen gesagt.
  // R718: Umpacken geht jetzt AUCH bei fremden Freigaben. Möglich wurde das
  // durch den strömenden Fernweg: ffmpeg holt nur die Abschnitte, die es
  // gerade braucht, statt die Datei vorher vollständig zu übertragen.
  const fremd = url.indexOf('/shares/remote/') >= 0;
  // R773: Kein Umpacken mehr. Bei fremden Freigaben bleibt der strömende Weg,
  // weil der alte /dl bei 64 MB abschnitt (R718) – das gilt unabhängig vom
  // Format und auch für Bilder und PDF.
  const quelle = fremd ? url.replace('/dl?path=', '/stream?path=') : url;
  if (art === 'bild')       inhalt = '<img src="'+quelle+'" alt="'+name+'">';
  else if (art === 'video')
    inhalt = '<video src="'+quelle+'" controls autoplay playsinline></video>'+
    '<div class="sv-hinweis" style="display:none">Wiedergabe nicht möglich. ' +
    'Wenn die Datei umgepackt werden muss, braucht der Node daf\u00fcr ffmpeg; ' +
    'bei exotischen Codecs (etwa HEVC in manchen Browsern) hilft auch das nicht. ' +
    'Der Pfeil oben l\u00e4dt die Datei herunter.</div>';
  else if (art === 'ton')   inhalt = '<audio src="'+quelle+'" controls autoplay></audio>';
  else if (art === 'pdf')   inhalt = '<iframe src="'+quelle+'" title="'+name+'"></iframe>';
  else                      inhalt = '<pre class="sv-text">lade \u2026</pre>';
  ov.innerHTML = '<div class="sv-box"><div class="sv-kopf"><strong>'+name+'</strong>'+
    '<span><a class="btn-sm" href="'+url+'" download aria-label="Herunterladen">\u2193</a> '+
    '<button class="btn-sm" aria-label="Schlie\u00dfen">\u2717</button></span></div>'+
    '<div class="sv-inhalt">'+inhalt+'</div></div>';
  document.body.appendChild(ov);
  const zu = function(){ ov.remove(); document.removeEventListener('keydown', taste); };
  const taste = function(e){ if (e.key === 'Escape') zu(); };
  document.addEventListener('keydown', taste);
  ov.querySelector('button').onclick = zu;
  // R715: Scheitert die Wiedergabe, den Grund SAGEN. Ein schwarzer Kasten
  // ohne Erklärung ist das Schlimmste, was man hier anbieten kann.
  const vid = ov.querySelector('video, audio');
  if (vid) vid.addEventListener('error', function(){
    const h = ov.querySelector('.sv-hinweis');
    if (h) h.style.display = 'block';
    vid.style.display = 'none';
  });
  ov.addEventListener('click', function(e){ if (e.target === ov) zu(); });
  // R773: Die Codec-Vorabfrage ist entfallen – sie diente nur dem Umpacken.
  if (art === 'text') {
    // Textdateien können groß sein – nur den Anfang holen, sonst blockiert
    // eine Protokolldatei mit hunderten Megabyte den Browser.
    fetch(quelle, { headers: { 'Range': 'bytes=0-262143' }, credentials: 'same-origin' })
      .then(function(r){ return r.text(); })
      .then(function(txt){
        const el = ov.querySelector('.sv-text');
        if (el) el.textContent = txt + (txt.length >= 262000 ? '\n\n\u2026 (gekürzt)' : '');
      })
      .catch(function(e){
        const el = ov.querySelector('.sv-text');
        if (el) el.textContent = '\u2717 ' + e.message;
      });
  }
}

// R773: SMB-Brücke aus der Oberfläche entfernt.
//
// Die Kette Windows → Samba → rclone → WebDAV → P2P hat am Ende funktioniert,
// aber der Weg dorthin ging über ein Dutzend Einzelursachen, und das Ergebnis
// blieb zäh: Die Platte der Gegenseite liefert 3 bis 5 MB/s, daran ändert kein
// Protokoll etwas. Für Dokumente war es brauchbar, für Filme nie.
//
// Die Schnittstellen und der Helfer bleiben bestehen – sie stören nicht, sind
// erprobt, und ein Rückbau im Go-Teil hat mir in R738 schon einmal den halben
// Protokoll-Beantworter gekostet. In der Oberfläche wird nichts mehr
// angeboten.

// R742: Netzlaufwerk-Dialog (WebDAV-Zugangsdaten) entfernt.
//
// WebDAV hat trotz vier Anläufen (R722 bis R741) nie funktioniert: Windows
// gibt über HTTP keine Basisanmeldung heraus, Digest half ebenfalls nicht,
// und zuletzt blieb ein 404, dessen Ursache ich nicht gefunden habe.
//
// Die Schnittstellen im Node bleiben bestehen (sie stören nicht und sind
// erprobt genug, um später darauf aufzusetzen), aber in der Oberfläche wird
// nichts mehr angeboten, was nachweislich nicht geht. Ein Knopf, der stets
// scheitert, ist schlimmer als keiner.
//
// Die SMB-Brücke hing daran mit: davfs2 hätte denselben Weg benutzt.
// Für lokale Ordner bleibt Samba von Hand der verlässliche Weg.

// ── Datei in ihrer Anwendung öffnen (R721) ───────────────────────────────
//
// Der Weg führt über dieselbe befristete Adresse wie bei VLC: Erst holen wir
// sie beim Node (das prüft den Zugriff), dann übergeben wir sie an Office.
//
// WICHTIG und ehrlich: Das ÖFFNET die Datei. Ob Office auch ZURÜCKSPEICHERN
// kann, hängt daran, ob der Node WebDAV spricht – tut er noch nicht. Bis dahin
// bietet Office beim Speichern ein „Speichern unter" an; die geänderte Datei
// muss dann über den Hochladen-Knopf zurück in die Freigabe.
async function inAppOeffnen(playlistUrl, name){
  const schema = appFuer(name);
  if (!schema) return;
  try {
    // Die Abspielliste enthält die signierte Adresse – daraus holen wir sie,
    // statt ein zweites Verfahren für denselben Zweck zu bauen.
    // ?url=1 liefert die nackte Adresse statt einer Abspielliste.
    const r = await fetch(playlistUrl + '?url=1', { credentials: 'same-origin' });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const adresse = (await r.text()).trim();
    if (!/^https?:\/\//.test(adresse)) throw new Error('Keine Adresse erhalten');
    // ofe = open for edit. Office öffnet damit direkt aus dem Netz.
    window.location.href = schema + ':ofe|u|' + adresse;
    setTimeout(function(){
      alert('Falls sich nichts öffnet, ist die Anwendung nicht installiert ' +
            'oder das Schema nicht angemeldet.\n\nDie Adresse liegt in der ' +
            'Zwischenablage – du kannst sie in der Anwendung unter „Öffnen" einfügen.');
      try { navigator.clipboard.writeText(adresse); } catch(e) {}
    }, 2500);
  } catch(e) {
    alert('\u2717 ' + e.message);
  }
}

// ── Hochladen in eine beschreibbare Freigabe (R721) ──────────────────────
function shareUploadDialog(shareID, unterPfad){
  const inp = document.createElement('input');
  inp.type = 'file'; inp.multiple = true;
  inp.onchange = async function(){
    const dateien = Array.from(inp.files || []);
    if (!dateien.length) return;
    const box = document.getElementById('share-dir-list');
    for (const f of dateien) {
      if (box) box.insertAdjacentHTML('afterbegin',
        '<div class="empty-hint" id="up-lauf">\u2191 ' + escapeHtmlF(f.name) + ' \u2026</div>');
      try {
        const fd = new FormData();
        fd.append('file', f);
        // Der Ordner steckt im Pfad; leer bedeutet die Wurzel der Freigabe.
        const ziel = '/api/v1/shares/' + encodeURIComponent(shareID) + '/ul/' +
                     encodeURIComponent(unterPfad || '.');
        const r = await fetch(ziel, { method:'POST', credentials:'same-origin', body: fd });
        let dd = {};
        try { dd = await r.json(); } catch(e) {}
        if (!r.ok) throw new Error(dd.error || ('HTTP ' + r.status));
      } catch(e) {
        alert('\u2717 ' + f.name + ': ' + e.message);
      }
      const l = document.getElementById('up-lauf');
      if (l) l.remove();
    }
    renderShareView();   // Liste auffrischen
  };
  inp.click();
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
  // R710: Vorher wurde JEDER Fehler verschluckt – weder r.ok geprüft noch die
  // Meldung angezeigt. Ein abgewiesener Aufruf sah deshalb aus, als passiere
  // gar nichts. Genau so war es gemeldet worden.
  const msg = document.getElementById('share-dir-msg');
  try {
    const r = await fetch('/api/v1/shares/' + encodeURIComponent(id),
                          { method:'DELETE', credentials:'same-origin' });
    let d = {};
    try { d = await r.json(); } catch(e) {}
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    if (msg) { msg.textContent = '\u2713 Freigabe entfernt.'; msg.style.color = 'var(--grn)'; }
    loadDirShares();
  } catch(e) {
    if (msg) { msg.textContent = '\u2717 ' + e.message; msg.style.color = 'var(--red)'; }
    else alert('\u2717 ' + e.message);
  }
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
          '<span class="fname" style="cursor:pointer;color:var(--green)" onclick="openRemoteShare(\''+pid+'\',\''+(s.id||'')+'\',\''+(s.name||'?').replace(/'/g,"")+'\')" title="Node '+pidShort+'…">🌐 '+(s.name||'?')+' <span class="meta" style="font-size:10px">('+(s.size_bytes ? fmtSize(s.size_bytes)+', ' : '')+(s.files||0)+' '+(FT.files_word||'Dateien')+')</span></span>'+
          '<span>'+

          '<button class="btn-sm" onclick="openRemoteShare(\''+pid+'\',\''+(s.id||'')+'\',\''+(s.name||'?').replace(/'/g,"")+'\')" aria-label="Öffnen">↗</button></span></div>';
      });
    });
    // R713: Den Stand sichtbar machen. Bisher stand entweder eine Liste da oder
    // „keine gefunden" – ohne Hinweis, ob gar nichts ankam oder ob etwas ankam
    // und beim Darstellen verlorenging. Für die Fehlersuche ist das der
    // Unterschied zwischen zwei ganz verschiedenen Ursachen.
    const gezaehlt = peers.reduce(function(n,p){
      let l = p.shares;
      if (typeof l === 'string') { try { l = JSON.parse(l); } catch(e) { l = []; } }
      return n + (Array.isArray(l) ? l.length : 0);
    }, 0);
    const kopf = '<div class="meta" style="font-size:11px;margin-bottom:6px">' +
      peers.length + ' Node(s) geantwortet, ' + gezaehlt + ' Freigabe(n) gemeldet</div>';
    box.innerHTML = html
      ? (kopf + html)
      : (kopf + '<div class="empty-hint">' +
         (gezaehlt > 0
            ? '\u26a0 ' + gezaehlt + ' Freigabe(n) gemeldet, aber keine darstellbar – ' +
              'bitte melden, das ist ein Fehler hier im Browser.'
            : (FT.net_shares_none||'Keine Netz-Freigaben')) + '</div>');
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
    const items = Array.isArray(d.entries) ? d.entries : [];   // R709
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
        // R714: auch bei FREMDEN Freigaben direkt ansehen – dieselbe Funktion,
        // nur eine andere Adresse. Sonst ginge es nur bei den eigenen, und
        // genau die fremden sieht man in der Browser-Ansicht.
        const rurl = '/api/v1/shares/remote/'+nav.peer+'/'+nav.share+'/dl?path='+encodeURIComponent(vp);
        html += '<div class="file-row" style="grid-template-columns:1fr auto auto auto;gap:8px">'+
          (istAnsehbar(nm)
            ? '<span class="fname" style="cursor:pointer;color:var(--green)" onclick="shareAnsehen(\''+
              rurl.replace(/'/g,"")+'\',\''+nm.replace(/'/g,"")+'\')">'+shareIcon(nm)+' '+nm+'</span>'
            : '<span class="fname">'+nm+'</span>')+
          '<span class="meta" style="font-size:11px;white-space:nowrap">'+
            (it.size ? fmtSize(it.size) : '')+'</span>'+
          // R725: Die Knöpfe fehlten in der NETZansicht – also genau dort, wo
          // man fremde Freigaben durchsieht. Für sie gibt es eine eigene
          // Verknüpfung, weil unsere Signatur nicht für fremde Freigabe-IDs
          // gilt; sie zeigt auf unseren eigenen Fernstrom.
          // R773: wie oben – kein Abspieler mehr, Office bleibt.
          (appFuer(nm)
            ? '<button class="btn-sm" onclick="inAppOeffnen(\''+
              rurl.replace('/dl?path=', '/playlist?path=')+'\',\''+nm.replace(/'/g,"")+'\')" '+
              'title="In der zugehörigen Anwendung öffnen" '+
              'aria-label="In Anwendung öffnen">\u2197</button>' : '')+
          // R774: Download über den STRÖMENDEN Weg, nicht über /dl.
          //
          // /dl schickt die ganze Datei als EINE P2P-Nachricht, und die ist auf
          // 32 MB begrenzt (maxResp in p2p/node.go). Alles darüber kam
          // abgeschnitten an – ohne Fehlermeldung, die Datei sah nur kaputt
          // aus. Genau das war zu beobachten: 32 MB von einer großen Datei.
          //
          // /stream holt abschnittsweise (R718) und kennt keine Obergrenze.
          '<button class="btn-sm" onclick="dateiHolenMitFortschritt(\''+
          rurl.replace('/dl?path=', '/stream?path=')+'&dl=1\',\''+nm.replace(/'/g,"")+'\','+(it.size||0)+')" '+
          'aria-label="Herunterladen">\u2193</button></div>';
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
      '<button class="btn-sm" onclick="document.getElementById(\'dir-browser\').remove()" aria-label="Schließen">✕</button></div>'+
      '<div id="db-list" style="overflow-y:auto;flex:1;font-size:13px"></div>'+
      '<div style="margin-top:10px;display:flex;gap:8px">'+
      '<button class="btn-sm" id="db-mkdir" aria-label="Neuen Ordner anlegen">\ud83d\udcc1 Neuer Ordner</button>'+
      '<button class="btn-sm btn-prim" id="db-pick" style="flex:1">Diesen Ordner freigeben</button></div></div>';
    document.body.appendChild(ov);
    ov.addEventListener('click', function(e){ if(e.target===ov) ov.remove(); });
    // R712: „Neuer Ordner" gehört hierher statt in die Hauptzeile. Dort musste
    // man den Pfad vorher von Hand eintragen; hier steht er bereits fest, und
    // man sieht sofort, wo der Ordner entsteht.
    const mk = document.getElementById('db-mkdir');
    if (mk) mk.onclick = async function(){
      const basis = (document.getElementById('db-path') || {}).textContent || '';
      const name = prompt('Name des neuen Ordners\n\nwird angelegt in: ' + basis);
      if (!name) return;
      try {
        const r = await fetch('/api/v1/shares/mkdir', {
          method:'POST', credentials:'same-origin',
          headers:{'Content-Type':'application/json'},
          body: JSON.stringify({ base: basis, name: name })
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        loadDirBrowser(d.path || basis);   // gleich hineinwechseln
      } catch(e) { alert('\u2717 ' + e.message); }
    };
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
// R782: Beim Laden prüfen, ob der Dateispeicher überhaupt läuft.
//
// Lief er nicht, bekam man auf jede Datei eine nackte 503 in der
// Browserkonsole – ohne Hinweis in der Oberfläche. Der Node kennt den Grund,
// er stand nur nirgends.
(async function speicherPruefen(){
  try {
    // /files/stats meldet enabled:false, ohne eine 503 zu erzeugen – das ist
    // der saubere Weg zu fragen, ob der Speicher läuft.
    const r = await fetch('/api/v1/files/stats', { credentials: 'same-origin' });
    if (!r.ok) return;
    const d = await r.json();
    if (d.enabled !== false) return;
    // Grund holen: den nennt der Download-Weg in seiner Fehlermeldung.
    let grund = '';
    try {
      const g = await fetch('/api/v1/files/download/' + '0'.repeat(64),
                            { credentials: 'same-origin' });
      if (g.status === 503) { const gd = await g.json(); grund = gd.error || ''; }
    } catch(e) {}
    const box = document.querySelector('.file-card-body') || document.body;
    const hin = document.createElement('div');
    hin.className = 'info-notice';
    hin.style.cssText = 'border-left:3px solid #ffc107;padding:10px 12px;margin:10px 0;'+
      'background:rgba(255,193,7,.08);border-radius:6px;color:var(--text);font-size:13px';
    hin.innerHTML = '<strong>Dateispeicher nicht aktiv.</strong><br>'+
      escapeHtmlF(grund || 'Grund unbekannt') +
      '<br><span class="meta">Dateien und Netzfreigaben bleiben bis dahin leer. '+
      'Der genaue Grund steht im Protokoll des Node: '+
      '<code>journalctl -u fundus-node | grep -i Dateispeicher</code></span>';
    box.insertBefore(hin, box.firstChild);
  } catch(e) { /* kein Netz – dann meldet sich die Seite ohnehin anders */ }
})();

if (!window.FM_READONLY) loadDirShares();
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

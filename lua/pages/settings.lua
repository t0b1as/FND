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

<!-- ── Reiter + Suche (R554) ───────────────────────────────────── -->
<div class="set-tabs-wrap">
  <div class="set-tabs" id="set-tabs"></div>
  <input type="search" id="set-search" class="set-search" placeholder="Einstellung suchen …" autocomplete="off">
</div>

<!-- ── Software-Update ───────────────────────────────────────── -->
<div class="set-card" id="update">
  <div class="set-head"><h3>Software-Update</h3></div>
  <div class="set-body">
    <div id="upd-status" class="status-line">Prüfe…</div>
    <div id="upd-last" class="status-line meta" style="margin-top:4px"></div>
    <div id="upd-helper" class="status-line meta" style="margin-top:2px"></div>
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
    <div id="upd-progress" class="upd-progress" style="display:none">
      <div class="upd-bar"><div class="upd-bar-fill" id="upd-bar-fill"></div></div>
      <ol class="upd-steps" id="upd-steps"></ol>
      <div id="upd-prog-msg" class="status-line"></div>
    </div>
  </div>
</div>

<!-- ── Solana-Zugang (RPC) ─────────────────────────────────────── -->
<div class="set-card" id="solana-rpc">
  <div class="set-head"><h3>Solana-Zugang (RPC)</h3></div>
  <div class="set-body">
    <p class="meta" style="margin:0 0 8px">Über diese Adressen spricht der Node mit Solana (Swaps, Guthaben, Zahlungen).
    Die erste ist bevorzugt; drosselt oder fällt sie aus, übernimmt automatisch die nächste.
    Ein eigener Zugang (z.B. Helius, QuickNode, kostenlos) ist zuverlässiger als der öffentliche Endpunkt –
    als letzten Eintrag den öffentlichen stehen lassen. Schlüssel werden nie angezeigt und nicht an Browser weitergegeben.</p>
    <div id="rpc-src" class="status-line meta"></div>
    <div id="rpc-list" style="margin:6px 0 10px"></div>
    <textarea id="rpc-input" rows="3" spellcheck="false" autocomplete="off"
      style="width:100%;box-sizing:border-box;font-family:monospace;font-size:12px"
      placeholder="Eine Adresse pro Zeile, z.B.&#10;https://mainnet.helius-rpc.com/?api-key=DEIN_SCHLÜSSEL&#10;https://api.mainnet-beta.solana.com"></textarea>
    <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:8px">
      <button class="btn" id="rpc-save">Speichern &amp; prüfen</button>
      <button class="btn btn-outline" id="rpc-reset">Auf fundus.env zurücksetzen</button>
    </div>
    <div id="rpc-out" class="status-line" style="margin-top:6px"></div>
  </div>
</div>
<script>
(function(){
  var list = document.getElementById('rpc-list'), src = document.getElementById('rpc-src'),
      out = document.getElementById('rpc-out'), inp = document.getElementById('rpc-input');
  function esc(t){ return String(t==null?'':t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  async function load(){
    try {
      var r = await fetch('/api/v1/admin/solana/rpc', {credentials:'same-origin'});
      var d = await r.json();
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      src.textContent = 'Quelle: ' + (d.source || '–') + (d.source === 'Einstellungen' && d.env_set ? ' (überschreibt fundus.env)' : '');
      list.innerHTML = (d.endpoints || []).map(function(e){
        var col = e.ok ? 'var(--green,#00e676)' : '#f66';
        return '<div style="display:flex;gap:8px;align-items:center;font-size:13px;padding:2px 0">' +
          '<span style="color:' + col + '">●</span>' +
          '<code style="word-break:break-all">' + esc(e.url) + '</code>' +
          (e.net ? '<span class="meta">' + esc(e.net) + '</span>' : '') +
          (e.active ? '<span class="badge badge-green">aktiv</span>' : '') +
          (!e.ok && e.last_error ? '<span class="meta" style="color:#f66">' + esc(e.last_error) + '</span>' : '') +
          '</div>';
      }).join('');
    } catch(e){ list.textContent = 'Nicht abrufbar: ' + e.message; }
  }
  async function save(val){
    out.textContent = '⏳ Wird geprüft …';
    try {
      var r = await fetch('/api/v1/admin/solana/rpc', {method:'POST', credentials:'same-origin',
        headers:{'Content-Type':'application/json'}, body: JSON.stringify({endpoints: val})});
      var d = await r.json();
      if (!r.ok || d.error) throw new Error(d.error || ('HTTP ' + r.status));
      out.textContent = d.note ? ('✓ ' + d.note) : ('✓ ' + d.count + ' Adresse(n) gespeichert – Netz: ' + (d.net || '?'));
      inp.value = '';
      setTimeout(load, 1500);
    } catch(e){ out.textContent = '✗ ' + e.message; }
  }
  document.getElementById('rpc-save').onclick = function(){
    if (!inp.value.trim()) { out.textContent = 'Bitte mindestens eine Adresse eingeben.'; return; }
    save(inp.value);
  };
  document.getElementById('rpc-reset').onclick = function(){
    if (confirm('Eigene Einstellung entfernen und wieder die Adressen aus fundus.env verwenden?')) save('');
  };
  load();
  setInterval(load, 30000);
})();
</script>

<!-- ── Chain-Zustand ──────────────────────────────────────────── -->
<div class="set-card" id="chain-health">
  <div class="set-head"><h3>Chain-Zustand</h3></div>
  <div class="set-body">
    <div id="ch-summary" class="status-line">Lade …</div>
    <div id="ch-grid" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:8px;margin:10px 0"></div>
    <div id="ch-notes"></div>
    <div style="font-size:13px;font-weight:600;margin:12px 0 4px">Peers</div>
    <div id="ch-peers" class="meta">Lade …</div>
    <p class="meta" style="margin-top:8px">„Gleiche Chain“ = Blockhash auf der gemeinsamen Höhe stimmt überein. Höhenabweichungen von 1–2 Blöcken sind normal.
    Abzweigungen löst der Node selbst (Astwahl); abweichender Genesis heißt: altes Programm auf dem Peer.</p>
  </div>
</div>
<script>
(function(){
  function esc(t){ return String(t==null?'':t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  function tile(label, val, color){
    return '<div style="background:var(--surface-2,#1c2030);border-radius:8px;padding:8px 10px">' +
      '<div class="meta" style="font-size:11px">' + esc(label) + '</div>' +
      '<div style="font-weight:700;font-size:15px;' + (color ? 'color:' + color : '') + ';word-break:break-all">' + val + '</div></div>';
  }
  function note(txt, color){
    return '<div style="margin:6px 0;padding:8px 10px;border-radius:8px;border-left:3px solid ' + color + ';background:rgba(255,255,255,.03);font-size:13px">' + esc(txt) + '</div>';
  }
  var G = 'var(--green,#00e676)', R = '#f66', Y = '#7ee2a8';
  async function load(){
    var sumEl = document.getElementById('ch-summary');
    try {
      var r = await fetch('/api/v1/chain/status', {credentials:'same-origin'});
      var d = await r.json();
      if (!r.ok) { sumEl.textContent = '✗ Chain nicht aktiv' + (d.reason ? ': ' + d.reason : ''); sumEl.style.color = R; return; }
      var val = !!d.i_am_validator;
      document.getElementById('ch-grid').innerHTML =
        tile('Höhe', esc(d.height)) +
        tile('Kopf-Hash', '<span style="font-family:monospace;font-size:12px">' + esc(String(d.head_hash||'').slice(0,16)) + '…</span>') +
        tile('Mempool', esc(d.mempool)) +
        tile('Validatoren', esc(d.validators)) +
        tile('Dieser Node', val ? 'Validator ✓' : 'kein Validator', val ? G : Y) +
        (d.my_stake_fnd !== undefined ? tile('Eigener Stake', esc(d.my_stake_fnd) + ' FND') : '') +
        (d.producer_running !== undefined ? tile('Blockproduktion', d.producer_running ? 'läuft ✓' : 'aus', d.producer_running ? G : Y) : '');
      var notes = '';
      if (d.hint && !val) notes += note(d.hint, Y);
      if (d.fork_note) notes += note('Astwahl: ' + d.fork_note, Y);
      if (d.clock_note) notes += note(d.clock_note, R);
      if (d.filestore_error) notes += note(d.filestore_error + ' – Bilder/Dateien sind nicht abrufbar. Meist falsche Dateirechte; der Helper korrigiert sie automatisch, danach Node neu starten.', R);
      document.getElementById('ch-notes').innerHTML = notes;
    } catch(e){ sumEl.textContent = '✗ Status nicht abrufbar: ' + e.message; sumEl.style.color = R; return; }
    try {
      var r2 = await fetch('/api/v1/chain/peers', {credentials:'same-origin'});
      var p = await r2.json();
      if (!r2.ok) throw new Error(p.error || ('HTTP ' + r2.status));
      var peers = (p.peers || []).filter(function(x){ return !x.error || !x.same_genesis; });
      var other = peers.filter(function(x){ return !x.same_chain; }).length;
      sumEl.textContent = (other ? '⚠ ' : '✓ ') + p.summary;
      sumEl.style.color = other ? Y : G;
      document.getElementById('ch-peers').innerHTML = (p.peers || []).map(function(x){
        var st, col;
        if (x.same_chain) { st = 'gleiche Chain'; col = G; }
        else if (x.same_genesis === false && !x.error) { st = 'andere Chain'; col = R; }
        else if (x.error && x.error.indexOf('Genesis') >= 0) { st = 'altes Programm (anderer Genesis)'; col = R; }
        else if (x.error) { st = 'keine Chain-Auskunft'; col = 'var(--muted)'; }
        else { st = 'anderer Ast'; col = Y; }
        return '<div style="display:flex;gap:8px;align-items:center;font-size:13px;padding:3px 0;flex-wrap:wrap">' +
          '<span style="color:' + col + '">●</span>' +
          '<code style="font-size:11px">' + esc(String(x.peer).slice(0,8) + '…' + String(x.peer).slice(-6)) + '</code>' +
          '<span>' + st + '</span>' +
          (x.height ? '<span class="meta">Höhe ' + esc(x.height) + '</span>' : '') + '</div>';
      }).join('') || 'Keine Peers verbunden.';
    } catch(e){ document.getElementById('ch-peers').textContent = 'Peer-Übersicht nicht abrufbar: ' + e.message; }
  }
  load(); setInterval(load, 15000);
})();
</script>

<!-- ── Node-Übersicht ─────────────────────────────────────────── -->
<div class="set-card" id="node-overview">
  <div class="set-head"><h3>Node-Übersicht</h3></div>
  <div class="set-body">
    <div id="no-summary" class="status-line">Lade …</div>
    <div style="overflow-x:auto;margin-top:8px"><table id="no-table" class="no-table"></table></div>
    <p class="meta" style="margin-top:8px">Rot: andere Revision als die Mehrheit, Uhr &gt; 2 s daneben, Dateispeicher oder Chain aus.
    Gelb: mehr als 3 Blöcke Rückstand oder kürzliche Astwahl. Grau: keine Antwort (Node vor R531 oder nicht erreichbar).</p>
  </div>
</div>
<style>
.no-table { border-collapse:collapse; width:100%; font-size:13px; }
.no-table th, .no-table td { text-align:left; padding:6px 8px; border-bottom:1px solid var(--border); white-space:nowrap; vertical-align:top; }
.no-table th { color:var(--muted); font-weight:600; font-size:12px; }
.no-table td.bad  { color:#f66; font-weight:600; }
.no-table td.warn { color:#7ee2a8; }
.no-table tr.gone td { color:var(--muted); }
.no-table .note { white-space:normal; max-width:320px; font-size:12px; }
</style>
<script>
(function(){
  function esc(t){ return String(t==null?'':t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  function dur(s){ s = Number(s)||0; if (s < 3600) return Math.floor(s/60) + ' min'; if (s < 86400) return Math.floor(s/3600) + ' h'; return Math.floor(s/86400) + ' T'; }
  function gb(v){ var n = Number(v); return isFinite(n) ? n.toFixed(1) : '–'; }
  async function load(){
    var sum = document.getElementById('no-summary');
    try {
      var r = await fetch('/api/v1/nodes/overview', {credentials:'same-origin'});
      var d = await r.json();
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      var nodes = d.nodes || [];
      var ok = nodes.filter(function(n){ return !n.error; });
      // Mehrheits-Revision und höchste Höhe als Bezug
      var cnt = {}; ok.forEach(function(n){ cnt[n.revision] = (cnt[n.revision]||0) + 1; });
      var major = Object.keys(cnt).sort(function(a,b){ return cnt[b]-cnt[a]; })[0] || '';
      var maxH = Math.max.apply(null, ok.map(function(n){ return n.height||0; }).concat([0]));
      var issues = 0;
      var rows = nodes.map(function(n){
        if (n.error) return '<tr class="gone"><td>' + esc(String(n.peer_id).slice(-8)) + '</td><td colspan="7">' + esc(n.error) + '</td></tr>';
        var revBad = n.revision !== major, hWarn = (maxH - (n.height||0)) > 3, skewBad = Math.abs(n.skew_sec||0) > 2;
        var notes = [];
        if (n.chain_error) notes.push('<span style="color:#f66">Chain: ' + esc(n.chain_error) + '</span>');
        if (n.filestore_error) notes.push('<span style="color:#f66">' + esc(n.filestore_error) + '</span>');
        if (n.clock_note) notes.push('<span style="color:#f66">' + esc(n.clock_note) + '</span>');
        if (n.fork_note) notes.push('<span style="color:#7ee2a8">' + esc(n.fork_note) + '</span>');
        if (revBad || hWarn || skewBad || n.chain_error || n.filestore_error || n.clock_note) issues++;
        var st = n.storage || {};
        return '<tr>' +
          '<td><b>' + esc(n.name || '?') + '</b>' + (n.self ? ' <span class="meta">(dieser)</span>' : '') + '<div class="meta" style="font-size:11px">…' + esc(String(n.peer_id).slice(-8)) + '</div></td>' +
          '<td class="' + (revBad ? 'bad' : '') + '">' + esc(n.revision) + '</td>' +
          '<td class="' + (hWarn ? 'warn' : '') + '">' + esc(n.height) + '<div class="meta" style="font-size:11px">' + esc(String(n.head_hash||'').slice(0,8)) + '</div></td>' +
          '<td>' + (n.producer ? '✓' : '–') + '</td>' +
          '<td class="' + (skewBad ? 'bad' : '') + '">' + (n.self ? '±0 s' : ((n.skew_sec > 0 ? '+' : '') + n.skew_sec + ' s')) + '</td>' +
          '<td>' + (n.storage ? gb(st.used_gb) + ' / ' + gb(st.offer_gb) + ' GB' : '<span class="meta">aus</span>') + '</td>' +
          '<td>' + dur(n.uptime_sec) + '</td>' +
          '<td class="note">' + (notes.join('<br>') || '<span class="meta">–</span>') + '</td></tr>';
      }).join('');
      document.getElementById('no-table').innerHTML =
        '<tr><th>Node</th><th>Revision</th><th>Höhe</th><th>Blöcke</th><th>Uhr</th><th>Speicher</th><th>Läuft seit</th><th>Hinweise</th></tr>' + rows;
      var gone = nodes.length - ok.length;
      sum.textContent = (issues ? '⚠ ' : '✓ ') + ok.length + ' Node(s) antworten' + (issues ? ', ' + issues + ' mit Auffälligkeiten' : ', alles in Ordnung') + (gone ? ' · ' + gone + ' ohne Antwort' : '');
      sum.style.color = issues ? '#7ee2a8' : 'var(--green,#00e676)';
    } catch(e){ sum.textContent = '✗ Übersicht nicht abrufbar: ' + e.message; sum.style.color = '#f66'; }
  }
  load(); setInterval(load, 20000);
})();
</script>

<!-- ── E-Mail-Versand (R557) ──────────────────────────────────── -->
<div class="set-card" id="mail-card">
  <div class="set-head"><h3>E-Mail-Versand (SMTP)</h3></div>
  <div class="set-body">
    <p class="meta">Nutzer dieses Nodes können sich zusätzlich zu Push per E-Mail benachrichtigen lassen.
    Ein Pi am Heimanschluss kann Mails nicht selbst zustellen – der Node versendet daher über ein vorhandenes Postfach, wie ein Mailprogramm.
    Bei den meisten Anbietern braucht es dafür ein eigenes App-Passwort.</p>
    <div class="set-grid">
      <label>Server <input type="text" id="ml-host" placeholder="smtp.beispiel.de" autocomplete="off"></label>
      <label>Port <input type="number" id="ml-port" placeholder="587"></label>
      <label>Verschlüsselung <select id="ml-tls"><option value="starttls">STARTTLS (587)</option><option value="tls">TLS (465)</option></select></label>
      <label>Benutzer <input type="text" id="ml-user" placeholder="postfach@beispiel.de" autocomplete="off"></label>
      <label>Passwort <input type="password" id="ml-pass" placeholder="(unverändert lassen)" autocomplete="new-password"></label>
      <label>Absender <input type="email" id="ml-from" placeholder="fundus@beispiel.de" autocomplete="off"></label>
      <label>Adresse des Nodes <input type="url" id="ml-base" placeholder="https://fnd.resolve.bar" autocomplete="off"></label>
    </div>
    <label class="set-check" style="margin-top:8px"><input type="checkbox" id="ml-en"> E-Mail-Versand aktiv</label>
    <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:10px">
      <button class="btn" id="ml-save" style="width:auto">Speichern</button>
      <button class="btn btn-outline" id="ml-test" style="width:auto">Testmail senden</button>
    </div>
    <p class="meta" id="ml-cfg-out"></p>
  </div>
</div>
<script>
(function(){
  var el = function(id){ return document.getElementById(id); };
  async function api(method, url, b){
    var r = await fetch(url, {method: method, credentials:'same-origin', headers: b ? {'Content-Type':'application/json'} : {}, body: b ? JSON.stringify(b) : undefined});
    var d = {}; try { d = await r.json(); } catch(e){}
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status)); return d;
  }
  async function load(){
    try {
      var d = await api('GET', '/api/v1/admin/mail/config');
      el('ml-host').value = d.host || ''; el('ml-port').value = d.port || ''; el('ml-user').value = d.user || '';
      el('ml-from').value = d.from || ''; el('ml-base').value = d.base_url || ''; el('ml-tls').value = d.tls_mode || 'starttls';
      el('ml-en').checked = !!d.enabled;
      el('ml-pass').placeholder = d.has_password ? '(gespeichert – leer lassen)' : 'App-Passwort';
      el('ml-cfg-out').textContent = d.subscribers ? d.subscribers + ' Nutzer haben eine Adresse hinterlegt.' : '';
    } catch(e){ el('ml-cfg-out').textContent = '✗ ' + e.message; }
  }
  el('ml-save').onclick = async function(){
    var out = el('ml-cfg-out'); this.disabled = true;
    try {
      await api('POST', '/api/v1/admin/mail/config', {enabled: el('ml-en').checked, host: el('ml-host').value.trim(),
        port: parseInt(el('ml-port').value, 10) || 0, user: el('ml-user').value.trim(), password: el('ml-pass').value,
        from: el('ml-from').value.trim(), tls_mode: el('ml-tls').value, base_url: el('ml-base').value.trim()});
      el('ml-pass').value = ''; out.textContent = '✓ Gespeichert'; load();
    } catch(e){ out.textContent = '✗ ' + e.message; }
    this.disabled = false;
  };
  el('ml-test').onclick = async function(){
    var out = el('ml-cfg-out'), to = prompt('Testmail an welche Adresse?', el('ml-from').value || '');
    if (to === null) return;
    this.disabled = true; out.textContent = '⏳ Sende …';
    try { var d = await api('POST', '/api/v1/admin/mail/test', {to: to}); out.textContent = '✓ Testmail an ' + d.to + ' versendet.'; }
    catch(e){ out.textContent = '✗ ' + e.message; }
    this.disabled = false;
  };
  load();
})();
</script>

<!-- ── Sicherung ──────────────────────────────────────────────── -->
<div class="set-card" id="backup-card">
  <div class="set-head"><h3>Sicherung</h3></div>
  <div class="set-body">
    <p class="meta">Täglich verschlüsselt: Node-Identität und -Wallet, Sitzungen, Swaps, Orders, Hinterlegungen, Datenbank und Konfiguration
    (ohne Chain – die lädt der Node neu). Abgelegt mit 3 Kopien auf anderen Nodes. Wiederherstellen auf einem neuen Pi braucht nur das Sicherungspasswort.</p>
    <div id="bk-status" class="status-line">Lade …</div>
    <div id="bk-setup" style="display:none;margin-top:10px">
      <div style="font-size:13px;font-weight:600;margin-bottom:4px" id="bk-setup-title">Sicherung einrichten</div>
      <input type="password" id="bk-pw1" placeholder="Sicherungspasswort (mind. 12 Zeichen)" autocomplete="new-password" style="width:100%;margin-bottom:6px">
      <input type="password" id="bk-pw2" placeholder="Wiederholen" autocomplete="new-password" style="width:100%;margin-bottom:6px">
      <p class="meta" style="color:#7ee2a8">Gut aufbewahren: Ohne dieses Passwort lässt sich die Sicherung nicht öffnen – auch nicht von uns.</p>
      <button class="btn" id="bk-setup-btn">Einrichten und jetzt sichern</button>
    </div>
    <div id="bk-actions" style="display:none;margin-top:10px">
      <button class="btn" id="bk-run">Jetzt sichern</button>
      <button class="btn btn-outline" id="bk-change">Passwort ändern</button>
    </div>
    <div id="bk-out" class="meta" style="margin-top:8px"></div>
    <details style="margin-top:14px">
      <summary style="cursor:pointer;font-weight:600">Wiederherstellen (z.B. auf einem neuen Pi)</summary>
      <p class="meta" style="margin-top:6px">Ersetzt Identität, Wallet und Daten DIESES Nodes durch die Sicherung. Danach startet der Node neu.
      Der neue Pi sollte vorher einige Minuten mit dem Netz verbunden sein, damit er den Verweis auf die Sicherung kennt.</p>
      <input type="password" id="bk-rpw" placeholder="Sicherungspasswort" autocomplete="off" style="width:100%;margin-bottom:6px">
      <input type="text" id="bk-rhash" placeholder="optional: Hash der Sicherung" autocomplete="off" spellcheck="false" style="width:100%;margin-bottom:6px;font-family:monospace">
      <button class="btn btn-danger" id="bk-restore">Wiederherstellen</button>
      <div id="bk-rout" class="meta" style="margin-top:6px"></div>
    </details>
  </div>
</div>
<script>
(function(){
  function el(id){ return document.getElementById(id); }
  function esc(t){ return String(t==null?'':t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  async function post(u, b){
    var r = await fetch(u, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(b||{})});
    var d = {}; try { d = await r.json(); } catch(e){}
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    return d;
  }
  async function load(){
    try {
      var r = await fetch('/api/v1/admin/backup/status', {credentials:'same-origin'}); var d = await r.json();
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      var st = el('bk-status');
      if (!d.filestore) { st.innerHTML = '<span style="color:#f66">Dateispeicher aus – die Sicherung braucht ihn zum Verteilen.</span>'; }
      else if (!d.enabled) { st.textContent = 'Noch nicht eingerichtet.'; }
      else if (d.last_time) {
        var when = new Date(d.last_time*1000).toLocaleString('de-DE');
        st.innerHTML = (d.last_error ? '<span style="color:#f66">⚠ Letzter Versuch fehlgeschlagen: ' + esc(d.last_error) + '</span><br>' : '<span style="color:var(--green,#00e676)">✓ Eingerichtet</span> · ') +
          'letzte Sicherung ' + esc(when) + ', ' + (d.last_size/1048576).toFixed(1).replace('.', ',') + ' MB, ' + d.replicas + ' Kopien' +
          '<div style="margin-top:4px;font-size:12px">Hash: <code style="word-break:break-all">' + esc(d.last_hash) + '</code></div>';
      } else { st.innerHTML = (d.last_error ? '<span style="color:#f66">⚠ ' + esc(d.last_error) + '</span>' : '⏳ Eingerichtet – erste Sicherung läuft …'); }
      el('bk-setup').style.display = d.enabled ? 'none' : '';
      el('bk-actions').style.display = d.enabled ? '' : 'none';
    } catch(e){ el('bk-status').textContent = '✗ ' + e.message; }
  }
  el('bk-setup-btn').onclick = async function(){
    var a = el('bk-pw1').value, b = el('bk-pw2').value, out = el('bk-out');
    if (a.length < 12) { out.textContent = 'Mindestens 12 Zeichen.'; return; }
    if (a !== b) { out.textContent = 'Die Passwörter stimmen nicht überein.'; return; }
    this.disabled = true;
    try { await post('/api/v1/admin/backup/setup', {password: a}); el('bk-pw1').value = el('bk-pw2').value = '';
      out.textContent = '✓ Eingerichtet – die erste Sicherung läuft im Hintergrund.'; setTimeout(load, 4000); }
    catch(e){ out.textContent = '✗ ' + e.message; }
    this.disabled = false; load();
  };
  el('bk-run').onclick = async function(){
    var out = el('bk-out'); this.disabled = true; out.textContent = '⏳ Sichere … (kann eine Minute dauern)';
    try { var d = await post('/api/v1/admin/backup/run'); out.textContent = '✓ Gesichert (' + (d.size/1048576).toFixed(1).replace('.', ',') + ' MB).'; }
    catch(e){ out.textContent = '✗ ' + e.message; }
    this.disabled = false; load();
  };
  el('bk-change').onclick = function(){ el('bk-setup').style.display = ''; el('bk-setup-title').textContent = 'Neues Sicherungspasswort'; };
  el('bk-restore').onclick = async function(){
    var pw = el('bk-rpw').value, out = el('bk-rout');
    if (!pw) { out.textContent = 'Bitte das Sicherungspasswort eingeben.'; return; }
    if (!confirm('Identität, Wallet und Daten dieses Nodes werden durch die Sicherung ersetzt. Fortfahren?')) return;
    this.disabled = true; out.textContent = '⏳ Suche und lade die Sicherung … (kann einige Minuten dauern)';
    try {
      var d = await post('/api/v1/admin/backup/restore', {password: pw, hash: el('bk-rhash').value.trim()});
      out.innerHTML = '✓ ' + d.files + ' Dateien bereitgelegt – der Node startet neu und spielt sie ein. Seite in einer Minute neu laden.<br>' +
        'Danach einmalig per SSH übernehmen, falls vorhanden:<br><code>sudo cp /opt/fundus/data/restored-etc/fundus.env /etc/fundus/fundus.env</code><br>' +
        '<code>sudo cp /opt/fundus/data/restored-etc/seed /etc/fundus/wallet.key</code><br><code>sudo systemctl restart fundus-node</code>';
    } catch(e){ out.textContent = '✗ ' + e.message; this.disabled = false; }
  };
  load();
})();
</script>
<style>
.upd-progress { margin-top:12px; padding:10px 12px; border:1px solid var(--brd); border-radius:10px; }
.upd-bar { height:8px; background:var(--sur2); border-radius:999px; overflow:hidden; }
.upd-bar-fill { height:100%; width:0; background:var(--green,#00e676); transition:width .4s ease; }
.upd-steps { list-style:none; margin:10px 0 6px; padding:0; }
.upd-steps li { padding:3px 0; color:var(--muted); }
.upd-steps li.done { color:var(--text); }
.upd-steps li.active { color:var(--text); font-weight:600; }
.upd-steps li.failed { color:#ff6b6b; font-weight:600; }
.upd-steps .ic { display:inline-block; width:1.4em; }
</style>
<script>
var UPD_STEPS = ['Signatur prüfen', 'Herunterladen', 'Prüfsumme prüfen', 'Entpacken',
                 'Programm und Oberfläche austauschen', 'Neustart'];
var updPollTimer = null, updPollFails = 0, updTarget = '';

// Fortschritt anzeigen. p = Status vom Helper (oder null), current = laufende Version.
function updShowProgress(p, current, offline) {
  var box = document.getElementById('upd-progress');
  if (!p && !offline) { return; }
  box.style.display = 'block';
  var ol = document.getElementById('upd-steps'), msg = document.getElementById('upd-prog-msg');
  var state = offline ? 'restarting' : p.state, step = offline ? 6 : (p.step || 0);
  var target = (p && p.version) || updTarget;
  if (!offline && current && target && current === target && state !== 'failed') state = 'done';
  var html = '';
  for (var i = 1; i <= UPD_STEPS.length; i++) {
    var cls = '', ic = '○', label = UPD_STEPS[i - 1];
    if (state === 'done' || i < step) { cls = 'done'; ic = '✓'; }
    else if (i === step) {
      if (state === 'failed') { cls = 'failed'; ic = '✗'; }
      else { cls = 'active'; ic = '⏳'; }
      if (i === 2 && p && p.percent >= 0 && state !== 'failed') label += ' (' + p.percent + ' %)';
    }
    html += '<li class="' + cls + '"><span class="ic">' + ic + '</span>' + label + '</li>';
  }
  ol.innerHTML = html;
  var within = (p && p.percent >= 0 && step === 2) ? p.percent / 100 : 0.5;
  var pct = state === 'done' ? 100 : Math.max(0, Math.min(100, Math.round(((step - 1) + within) / UPD_STEPS.length * 100)));
  document.getElementById('upd-bar-fill').style.width = pct + '%';
  msg.className = 'status-line';
  if (state === 'done') {
    msg.textContent = '✓ ' + target + ' ist installiert – die Seite wird neu geladen.';
  } else if (state === 'failed') {
    msg.className = 'status-line error';
    msg.textContent = '✗ Installation fehlgeschlagen: ' + ((p && p.error) || 'unbekannter Fehler') +
      ' – der vorherige Stand bleibt aktiv.';
  } else if (state === 'restarting') {
    msg.textContent = 'Node startet neu – warte auf ' + (target || 'die neue Version') + ' …';
  } else {
    msg.textContent = 'Installation läuft – bitte die Seite geöffnet lassen.';
  }
  return state;
}

// Status alle 2 s abfragen, bis fertig oder fehlgeschlagen. Während des
// Neustarts ist der Node kurz nicht erreichbar – dann einfach weiter warten.
function updStartPolling(target) {
  if (target) updTarget = target;
  if (updPollTimer) return;
  updPollFails = 0;
  updPollTimer = setInterval(async function () {
    try {
      var r = await fetch('/api/v1/admin/update/status', {cache: 'no-store', credentials: 'same-origin'});
      if (!r.ok) throw new Error('HTTP ' + r.status);
      var d = await r.json();
      updPollFails = 0;
      var st = updShowProgress(d.progress || {version: updTarget, state: 'running', step: 0}, d.current, false);
      if (st === 'done') { clearInterval(updPollTimer); updPollTimer = null; setTimeout(function(){ location.reload(); }, 2500); }
      if (st === 'failed') { clearInterval(updPollTimer); updPollTimer = null; var b = document.getElementById('upd-apply'); if (b) b.disabled = false; }
    } catch (e) {
      updPollFails++;
      updShowProgress(null, '', true);
      if (updPollFails > 300) { // ~10 min
        clearInterval(updPollTimer); updPollTimer = null;
        document.getElementById('upd-prog-msg').textContent = 'Der Node antwortet seit 10 Minuten nicht – bitte das Log prüfen (journalctl -u fundus-helper -u fundus-node).';
      }
    }
  }, 2000);
}

function updRender(d) {
  var st = document.getElementById('upd-status');
  var av = document.getElementById('upd-avail');
  if (!d || d.error) { st.textContent = (d && d.error) || 'Status nicht abrufbar.'; return; }
  st.innerHTML = 'Installiert: <b>' + (d.current || '?') + '</b>' +
    (d.enabled ? (d.auto ? ' · automatische Installation an' : '') : ' · Update-Prüfung ausgeschaltet');
  document.getElementById('upd-src').textContent = d.source ? ('Quelle: ' + d.source.replace('https://raw.githubusercontent.com/', 'github.com/').replace('/main/manifest.json', '')) : '';
  // Helper-Stand: Er fuehrt Installationen aus. Ein alter Helper (vor R435 mit
  // fehlerhafter Signaturpruefung, vor R443 ohne Uebernahme der Laufzeitdateien)
  // kann Updates nicht korrekt installieren.
  var hv = d.helper, hEl = document.getElementById('upd-helper');
  if (hEl) {
    var txt, old;
    if (hv === undefined) {
      // Das laufende Node-Programm kennt das Feld nicht -> Programm aelter als R443,
      // obwohl die Oberflaeche neuer ist (Binary wurde nicht aktualisiert).
      old = true;
      txt = 'Node-Programm veraltet: Die Oberfläche ist ' + (document.querySelector('.brand-rev') ? document.querySelector('.brand-rev').textContent : 'neu') +
        ', das laufende Programm ' + (d.current || 'älter') + '. Bitte mit deploy-fundus.ps1 -Rebuild aktualisieren – sonst schlagen Updates fehl.';
    } else if (hv === '') {
      old = false;
      txt = 'Installations-Helper: wird ermittelt …';
    } else {
      var hn = parseInt(String(hv).replace(/\D/g, ''), 10);
      old = hv === 'alt' || hv === 'unbekannt' || hv === 'nicht erreichbar' || (hn && hn < 443);
      txt = 'Installations-Helper: ' + hv +
        (old ? ' – veraltet oder nicht erreichbar. Bitte einmal per deploy-fundus.ps1 -Rebuild aktualisieren, sonst schlagen Updates fehl.' : '');
    }
    hEl.textContent = txt;
    hEl.className = 'status-line meta' + (old ? ' error' : '');
    var ab = document.getElementById('upd-apply');
    if (ab && old) ab.title = 'Helper veraltet – Installation würde fehlschlagen';
  }
  // Ergebnis der letzten GitHub-Pruefung - erklaert, warum (k)ein Update kommt.
  var lc = d.last_check, lcEl = document.getElementById('upd-last');
  if (lc && lcEl) {
    var when = new Date(lc.checked_at).toLocaleTimeString('de-DE', {hour:'2-digit', minute:'2-digit'});
    var txt = {
      not_newer: 'GitHub: ' + lc.remote + ' – installiert ist ' + (d.current || '?') + ', also gleich oder neuer. Kein Update nötig.',
      newer: 'GitHub: ' + lc.remote + ' – neuere Version gefunden.',
      bad_signature: 'GitHub: ' + (lc.remote || '?') + ' – Signatur ungültig, wird aus Sicherheitsgründen ignoriert. (' + lc.note + ')',
      unreachable: 'GitHub nicht erreichbar: ' + lc.note,
      invalid: 'Manifest auf GitHub fehlerhaft: ' + lc.note
    }[lc.status] || (lc.note || '');
    lcEl.textContent = txt + ' (geprüft ' + when + ')';
    lcEl.className = 'status-line' + ((lc.status === 'bad_signature' || lc.status === 'unreachable' || lc.status === 'invalid') ? ' error' : '');
  } else if (lcEl) {
    lcEl.textContent = d.enabled ? 'Noch keine Prüfung seit dem Start – „Jetzt prüfen" klicken.' : '';
  }
  if (d.progress && (d.progress.state === 'running' || d.progress.state === 'restarting')) {
    var st = updShowProgress(d.progress, d.current, false);
    if (st !== 'done') updStartPolling(d.progress.version);
  } else if (d.progress && d.progress.state === 'failed') {
    updShowProgress(d.progress, d.current, false);
  }
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
    msg.textContent = '';
  } catch (e) { msg.textContent = 'Prüfung fehlgeschlagen: ' + e.message; }
  btn.disabled = false;
}
async function updApply() {
  var ver = document.getElementById('upd-ver').textContent;
  var btn = document.getElementById('upd-apply'), msg = document.getElementById('upd-msg');
  btn.disabled = true; msg.textContent = '';
  try {
    var r = await fetch('/api/v1/admin/update/apply', {method:'POST', credentials:'same-origin'});
    var d = await r.json();
    if (!r.ok) { msg.textContent = 'Fehler: ' + (d.error || r.status); btn.disabled = false; return; }
    updShowProgress({version: ver, state: 'running', step: 0, percent: -1}, '', false);
    updStartPolling(ver);
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

<div class="set-card" id="wifi">
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
<script>
(function(){
  // Karten nach Überschrift einem Reiter zuordnen (deutsch + englisch).
  var TABS = [
    ['node',     'Node',        /update|chain|node|profil|profile|revision/i],
    ['security', 'Sicherheit',  /sicherung|backup|admin|passwort|password/i],
    ['network',  'Netzwerk',    /solana|rpc|nat|port|wlan|wi-?fi|netz|network/i],
    ['storage',  'Speicher',    /speicher|storage|datei|file/i],
    ['energy',   'Energie',     /smartmeter|zähler|meter|energie|energy/i],
    ['display',  'Darstellung', /navigation|anzeige|display|sprache|language/i]
  ];
  var cards = Array.prototype.slice.call(document.querySelectorAll('.set-layout > .set-card'));
  var byTab = {};
  cards.forEach(function(c){
    var h = c.querySelector('.set-head h3, h3'); var title = h ? h.textContent : '';
    var tab = 'node';
    for (var i = 0; i < TABS.length; i++) { if (TABS[i][2].test(title)) { tab = TABS[i][0]; break; } }
    c.setAttribute('data-tab', tab); (byTab[tab] = byTab[tab] || []).push(c);
  });
  var bar = document.getElementById('set-tabs'), search = document.getElementById('set-search');
  var hashTab = (location.hash.match(/tab=(\w+)/) || [])[1];
  var target = location.hash.replace('#', '');
  var targetCard = target && document.getElementById(target);
  var current = hashTab || (targetCard && targetCard.getAttribute('data-tab')) || localStorage.getItem('fundus.settings.tab') || 'node';
  if (!byTab[current]) current = 'node';
  function render(){
    var q = (search.value || '').trim().toLowerCase();
    bar.innerHTML = TABS.filter(function(t){ return byTab[ t[0] ]; }).map(function(t){
      return '<button class="set-tab' + (t[0] === current && !q ? ' on' : '') + '" data-tab="' + t[0] + '">' + t[1] + '</button>';
    }).join('');
    cards.forEach(function(c){
      var show = q ? (c.textContent.toLowerCase().indexOf(q) >= 0) : (c.getAttribute('data-tab') === current);
      c.style.display = show ? '' : 'none';
    });
  }
  bar.addEventListener('click', function(e){
    var b = e.target.closest('.set-tab'); if (!b) return;
    current = b.getAttribute('data-tab'); localStorage.setItem('fundus.settings.tab', current);
    search.value = ''; history.replaceState(null, '', '#tab=' + current); render();
  });
  search.addEventListener('input', render);
  render();
  if (targetCard) setTimeout(function(){ targetCard.scrollIntoView({behavior:'smooth', block:'start'}); }, 50);
})();
</script>

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
    ['Verbindungen', (d.connections_direct!=null)
        ? (d.connections_direct + ' direkt · ' + d.connections_relayed + ' über Relay' +
           (d.connections_relayed > 0 ? ' (Hole Punching läuft)' : ''))
        : '–'],
    ['Relay-Vermittler (Fundus)', (d.relay_candidates!=null)
        ? (d.relay_candidates > 0 ? d.relay_candidates + ' verfügbar'
           : 'keiner – mind. ein Node braucht Portweiterleitung oder FUNDUS_P2P_REACHABILITY=public')
        : '–'],
    ['Erreichbarkeit (Vorgabe)', d.reachability_setting || 'auto'],
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
    // Setup-Hotspot "FUNDUS Rnnn" (kein WLAN → Zugangsdaten per Handy eingeben)
    window._wifiSetupAP = (d.ok && d.status && d.status.setup_ap) ? d.status : null;
    if (window._wifiSetupAP) {
      cur.textContent += '  ·  Setup-Hotspot aktiv: „' + d.status.setup_ap + '“' +
        (d.status.setup_ap_parallel ? ' (Testmodus, parallel)' : ' – Adresse 10.42.0.1');
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
  // Über den EXKLUSIVEN Setup-Hotspot verbunden: der Pi braucht das Funkmodul
  // für das neue WLAN → der Hotspot (und damit diese Verbindung) endet jetzt.
  const viaAP = window._wifiSetupAP && !window._wifiSetupAP.setup_ap_parallel;
  if (viaAP && !confirm('Der Hotspot „' + window._wifiSetupAP.setup_ap + '“ schließt sich jetzt, und der Node verbindet sich mit „' + ssid + '“.\n\nDanach ist er in diesem WLAN erreichbar. Scheitert die Verbindung (z.B. falsches Passwort), öffnet sich der Hotspot nach etwa 2 Minuten wieder.')) return;
  msg.textContent = (ST.wifi_connecting || 'Verbinde mit') + ' ' + ssid + '…';
  msg.style.color = 'var(--muted)';
  if (viaAP) {
    // Antwort kommt nicht mehr an (Hotspot weg) – Hinweis sofort zeigen.
    msg.textContent = 'Hotspot wird beendet, Node verbindet sich mit „' + ssid + '“ … Handy jetzt wieder mit dem normalen WLAN verbinden.';
  }
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

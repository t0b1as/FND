-- pages/wallet.lua – Wallet & Identität
local render = require "render"
local cjson  = require "cjson.safe"

return function()
  ngx.header["Content-Type"] = "text/html"
  local t = render.header("wallet.title", "wallet")

  local identity, _ = render.api_get("/v1/identity/me")
  local addr = identity and identity.wallet_address or "–"

ngx.print(string.format([[
<div class="wallet-layout">

<!-- ── Identität ───────────────────────────────────────────── -->
<div class="w-card">
  <div class="w-head"><h3>%s</h3></div>
  <div class="w-body">
    <div class="id-row">
      <span class="id-label">]] .. t("wallet.address") .. [[</span>
      <span class="id-val mono copyable" id="top-wal-address" onclick="copy(this)">%s</span>
    </div>
    <div class="id-row">
      <span class="id-label">]] .. t("wallet.crypto_stack") .. [[</span>
      <span class="id-val">Argon2id → secp256k1 · Ed25519 · X25519 · XChaCha20-Poly1305 · BLAKE3-256</span>
    </div>
  </div>
</div>

<!-- ── FND Balance ─────────────────────────────────────────── -->
<div class="w-card">
  <div class="w-head"><h3>FND Balance</h3></div>
  <div class="w-body">
    <div class="balance-row">
      <div class="bal-block">
        <span class="bal-label">]] .. t("wallet.available") .. [[</span>
        <span class="bal-val" id="bal-free">–</span>
        <span class="bal-unit">FND</span>
      </div>
      <div class="bal-block">
        <span class="bal-label">]] .. t("wallet.in_escrow") .. [[</span>
        <span class="bal-val" id="bal-escrow">–</span>
        <span class="bal-unit">FND</span>
      </div>
    </div>
  </div>
</div>

<div class="w-card">
  <details class="w-earn">
    <summary><h3 style="display:inline">]] .. t("wallet.earn_title") .. [[</h3></summary>
    <div class="w-body">
      <p class="w-earn-intro">]] .. t("wallet.earn_intro") .. [[</p>
      <div class="w-earn-item">
        <div class="w-earn-h">💾 ]] .. t("wallet.earn_transfer_h") .. [[</div>
        <div class="w-earn-d">]] .. t("wallet.earn_transfer_d") .. [[</div>
      </div>
      <div class="w-earn-item">
        <div class="w-earn-h">📦 ]] .. t("wallet.earn_hosting_h") .. [[</div>
        <div class="w-earn-d">]] .. t("wallet.earn_hosting_d") .. [[</div>
      </div>
      <div class="w-earn-item">
        <div class="w-earn-h">⚙️ ]] .. t("wallet.earn_fee_h") .. [[</div>
        <div class="w-earn-d">]] .. t("wallet.earn_fee_d") .. [[</div>
      </div>
      <p class="w-earn-note">]] .. t("wallet.earn_note") .. [[</p>
    </div>
  </details>
</div>

</div><!-- wallet-layout -->

<style>
.wallet-layout { display:flex; flex-direction:column; gap:12px; }
.w-earn summary { cursor:pointer; padding:.65rem 1.1rem; background:var(--sur2); border-bottom:1px solid var(--brd); list-style:none; user-select:none; }
.w-earn summary::-webkit-details-marker { display:none; }
.w-earn summary h3 { margin:0; }
.w-earn summary::before { content:"▸ "; color:var(--muted); }
.w-earn[open] summary::before { content:"▾ "; }
.w-earn-intro { font-size:13px; color:var(--txt); margin:0 0 12px; line-height:1.5; }
.w-earn-item { margin:10px 0; padding:10px 12px; background:var(--sur2); border-radius:8px; }
.w-earn-h { font-size:13px; font-weight:600; color:var(--txt); margin-bottom:3px; }
.w-earn-d { font-size:12.5px; color:var(--muted); line-height:1.5; }
.w-earn-note { font-size:11.5px; color:var(--muted); margin:12px 0 0; font-style:italic; line-height:1.45; }
.w-card { background:var(--sur); border:1px solid var(--brd); border-radius:var(--r); overflow:hidden; }
.w-head { background:var(--sur2); border-bottom:1px solid var(--brd); padding:.65rem 1.1rem;
          display:flex; align-items:center; justify-content:space-between; }
.w-head h3 { margin:0; font-size:13.5px; font-weight:600; }
.w-body { padding:1rem 1.1rem; }

.id-row { display:flex; align-items:baseline; gap:10px; padding:7px 0;
          border-bottom:1px solid var(--brd); font-size:13px; }
.id-row:last-child { border-bottom:none; }
.id-label { width:120px; flex-shrink:0; font-size:11px; font-weight:700;
            text-transform:uppercase; letter-spacing:.06em; color:var(--dim); }
.id-val { flex:1; font-size:12.5px; color:var(--txt); word-break:break-all; }
.id-val.mono { font-family:var(--mono); font-size:11px; color:var(--grn); }
.copyable { cursor:pointer; }
.copyable:hover { color:var(--acc); }

.inline-form { display:flex; gap:8px; flex-wrap:wrap; }
.inline-form input, .w-body input[type=text], .w-body input[type=number], .w-body input[type=password] {
  flex:1; min-width:140px; padding:10px 12px; background:var(--bg);
  border:1px solid var(--brd2); border-radius:var(--rs); color:var(--txt);
  font-size:14px; outline:none; transition:border-color .15s, box-shadow .15s; }
.inline-form input:focus, .w-body input:focus {
  border-color:var(--grn); box-shadow:0 0 0 3px rgba(0,230,118,0.12); }
.w-body input::placeholder { color:var(--muted); }
.btn-prim { padding:10px 18px; background:var(--green-dim,#00c853); color:#062b16; border:1px solid var(--acc);
            border-radius:var(--rs); cursor:pointer; font-size:14px; font-weight:600;
            min-height:40px; transition:background .15s, transform .05s; }
.btn-prim:hover { background:var(--grn); box-shadow:0 0 16px rgba(79,142,247,0.45); }
.btn-prim:active { transform:translateY(1px); }
.btn-prim:disabled { opacity:.4; cursor:not-allowed; }
.btn-sm { padding:8px 14px; font-size:13px; background:var(--sur2); border:1px solid var(--brd2);
          border-radius:var(--rs); color:var(--txt); cursor:pointer; min-height:36px;
          font-weight:500; transition:border-color .15s, color .15s; }
.btn-sm:hover { border-color:var(--acc); color:var(--acc); }

.balance-row { display:flex; gap:12px; flex-wrap:wrap; }
.bal-block { flex:1; min-width:130px; display:flex; flex-direction:column; gap:4px;
             background:var(--sur2); border:1px solid var(--brd); border-radius:var(--rs);
             padding:12px 14px; }
.bal-label { font-size:10.5px; font-weight:700; text-transform:uppercase;
             letter-spacing:.07em; color:var(--dim); }
.bal-val   { font-size:1.35rem; font-weight:800; line-height:1.15; color:var(--txt);
             font-family:var(--mono); word-break:break-all; }
.bal-unit  { font-size:11px; color:var(--muted); }

.escrow-row { display:flex; align-items:center; gap:10px; padding:8px 0;
              border-bottom:1px solid var(--brd); font-size:12.5px; }
.escrow-row:last-child { border-bottom:none; }
.escrow-id   { font-family:var(--mono); font-size:10px; color:var(--muted); }
.escrow-amt  { font-weight:700; color:var(--amber); }
.escrow-status { font-size:11px; padding:1px 7px; border-radius:9999px; font-weight:600; }
.es-funded   { background:rgba(255,179,0,.1); color:var(--amber); border:1px solid rgba(255,179,0,.3); }
.es-released { background:rgba(0,230,118,.1); color:var(--grn);   border:1px solid rgba(0,230,118,.3); }
.es-disputed { background:rgba(240,82,82,.1); color:var(--red);   border:1px solid rgba(240,82,82,.3); }

.status-line { margin-top:8px; font-size:12.5px; color:var(--muted); }
.empty-hint  { font-size:13px; color:var(--muted); font-style:italic; }
@media(max-width:600px){ .balance-row { flex-direction:column; } }
</style>

<script>
const WT = ]] .. (require("cjson.safe").encode({
  loading = t("wallet.loading"), no_connection = t("wallet.no_connection"),
  no_open_escrows = t("wallet.no_open_escrows"),
  error = t("wallet.error"),
  enter_key_or_addr = t("wallet.enter_key_or_addr"), invalid_address = t("wallet.invalid_address"),
  opened_readonly = t("wallet.opened_readonly"), deriving = t("wallet.deriving"), opened = t("wallet.opened"), error_word = t("wallet.error_word"),
  es_funded = t("wallet.es_funded"), es_released = t("wallet.es_released"), es_disputed = t("wallet.es_disputed"),
  es_cancel_requested = t("wallet.es_cancel_requested"), es_refunded = t("wallet.es_refunded"),
  balance_loading = t("wallet.balance_loading"), readonly_hint = t("wallet.readonly_hint"),
  balance_unavailable = t("wallet.balance_unavailable"),
  seed_wrong_pass = t("wallet.seed_wrong_pass"), seed_pass_set = t("wallet.seed_pass_set"),
  seed_pass_removed = t("wallet.seed_pass_removed"), seed_recreate_confirm = t("wallet.seed_recreate_confirm"),
  seed_recreated = t("wallet.seed_recreated"),
  seed_enc_tooshort = t("wallet.seed_enc_tooshort"), seed_encrypt_confirm = t("wallet.seed_encrypt_confirm"),
  seed_encrypted_ok = t("wallet.seed_encrypted_ok"), wallet_unlocked = t("wallet.wallet_unlocked"),
  enter_recipient_amount = t("wallet.enter_recipient_amount"), sending = t("wallet.sending"),
  sent_tx = t("wallet.sent_tx"), enter_addr_amount = t("wallet.enter_addr_amount"),
  fee_added = t("wallet.fee_added"), fee_inclusive = t("wallet.fee_inclusive"),
  fee_hint_added = t("wallet.fee_hint_added"), fee_hint_inclusive = t("wallet.fee_hint_inclusive"),
  open_first = t("wallet.open_first"),
  minting = t("wallet.minting"), minted_tx = t("wallet.minted_tx"),
  payout_saving = t("wallet.payout_saving"), payout_saved = t("wallet.payout_saved"),
  stake_title = t("wallet.stake_title"), stake_validators = t("wallet.stake_validators"), stake_you_are = t("wallet.stake_you_are"), stake_you_not = t("wallet.stake_you_not"), stake_min = t("wallet.stake_min"), stake_sending = t("wallet.stake_sending"), stake_ok = t("wallet.stake_ok"), stake_unstaked = t("wallet.stake_unstaked"),
}) or "{}") .. [[;
async function copy(el) {
  await navigator.clipboard.writeText(el.textContent).catch(()=>{});
  const orig = el.style.color;
  el.style.color = 'var(--grn)';
  setTimeout(() => el.style.color = orig, 800);
}

async function loadEscrows() {
  const list = document.getElementById('escrow-list');
  list.innerHTML = '<div class="empty-hint">' + WT.loading + '</div>';
  const r = await fetch('/api/v1/escrow?all=1').catch(()=>({ok:false}));
  if (!r.ok) { list.innerHTML = '<div class="empty-hint">' + WT.no_connection + '</div>'; return; }
  const d = await r.json();
  const escrows = d.escrows || [];
  if (!escrows.length) { list.innerHTML = '<div class="empty-hint">' + WT.no_open_escrows + '</div>'; return; }
  const statusMap = {funded:'es-funded',released:'es-released',disputed:'es-disputed',refunded:'es-released'};
  const labelMap  = {funded:WT.es_funded,released:WT.es_released,disputed:WT.es_disputed,
                     cancel_requested:WT.es_cancel_requested,refunded:WT.es_refunded};
  list.innerHTML = escrows.map(e => `
    <div class="escrow-row">
      <span class="escrow-id">#${(e.escrow_id||'').slice(0,10)}…</span>
      <span class="escrow-amt">${(e.amount_fnd||0).toFixed(2)} FND</span>
      <span class="escrow-status ${statusMap[e.status]||'es-funded'}">${labelMap[e.status]||e.status}</span>
      <span style="font-size:11px;color:var(--muted);margin-left:auto">${e.listing_id||''}</span>
    </div>`).join('');
}

// Beim Laden: offene Escrows der eingeloggten Identität versuchen (still bei 401).
loadEscrows().catch(()=>{});
</script>
]],
  t("wallet.identity"),
  addr
))

  -- FND-Wallet-Sektion (Adresse aus Seed/Email+PW, Transfer, Test-Mint).
  -- Roher Block (kein string.format) → kein %-Escaping nötig.
  ngx.print([==[
<div class="wallet-layout" style="margin-top:12px">
<div class="w-card">
  <div class="w-head"><h3>]==] .. t("wallet.earnings_title") .. [==[</h3>
    <button class="btn-sm" onclick="loadEarnings()" title="Aktualisieren">&#8635;</button>
  </div>
  <div class="w-body">
    <div id="wallet-locked-banner" class="seed-warn" style="display:none">
      <strong>]==] .. t("wallet.wallet_locked_title") .. [==[</strong>
      <p>]==] .. t("wallet.wallet_locked_hint") .. [==[</p>
      <input type="password" id="unlock-pass" placeholder="]==] .. t("wallet.seed_pass_ph") .. [==[" style="margin-bottom:6px">
      <button class="btn-sm" onclick="unlockWallet()">]==] .. t("wallet.wallet_unlock_btn") .. [==[</button>
      <div id="unlock-out" class="status-line"></div>
    </div>
    <div id="earnings-box" class="earn-grid">
      <div class="earn-cell"><span class="earn-num" id="earn-pending">–</span><span class="earn-lbl">]==] .. t("wallet.earn_pending") .. [==[</span></div>
      <div class="earn-cell"><span class="earn-num" id="earn-pending-fnd">–</span><span class="earn-lbl">]==] .. t("wallet.earn_pending_fnd") .. [==[</span></div>
      <div class="earn-cell"><span class="earn-num" id="earn-pending-ufnd">–</span><span class="earn-lbl">]==] .. t("wallet.earn_pending_ufnd") .. [==[</span></div>
      <div class="earn-cell"><span class="earn-num" id="earn-fetch">–</span><span class="earn-lbl">]==] .. t("wallet.earn_fetch_mb") .. [==[</span></div>
      <div class="earn-cell"><span class="earn-num" id="earn-hosting">–</span><span class="earn-lbl">]==] .. t("wallet.earn_hosting_cnt") .. [==[</span></div>
    </div>
    <div id="earn-status" class="status-line"></div>
    <div id="seed-backup" style="display:none">
      <div class="seed-warn">
        <strong>]==] .. t("wallet.seed_backup_title") .. [==[</strong>
        <p>]==] .. t("wallet.seed_backup_hint") .. [==[</p>
        <code class="seed-words"></code>
        <button class="btn-sm" onclick="confirmSeedBackup()" style="margin-top:10px">]==] .. t("wallet.seed_confirm_btn") .. [==[</button>
      </div>
    </div>
    <button class="btn-sm" id="earn-open-node" onclick="openNodeWallet()" style="margin-top:8px;display:none">]==] .. t("wallet.open_node_wallet") .. [==[</button>
    <div id="node-wal-state" class="w-open" style="display:none;margin-top:8px"></div>
    <details class="w-payout" style="margin-top:10px">
      <summary>]==] .. t("wallet.stake_title") .. [==[</summary>
      <div style="padding:10px 0">
        <p class="w-hint">]==] .. t("wallet.stake_hint") .. [==[</p>
        <div id="stake-status" class="status-line" style="margin-bottom:8px"></div>
        <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
          <input type="number" id="stake-amount" min="10" step="1" placeholder="]==] .. t("wallet.stake_amount_ph") .. [==[" style="flex:1;min-width:120px">
          <button class="btn-sm btn-prim" onclick="doStake()">]==] .. t("wallet.stake_btn") .. [==[</button>
          <button class="btn-sm" onclick="doUnstake()">]==] .. t("wallet.unstake_btn") .. [==[</button>
        </div>
        <div id="stake-out" class="status-line" style="margin-top:6px"></div>
      </div>
    </details>
    <details class="w-payout" style="margin-top:10px">
      <summary>]==] .. t("wallet.payout_title") .. [==[</summary>
      <div style="padding:10px 0">
        <p class="w-hint">]==] .. t("wallet.payout_hint") .. [==[</p>
        <input type="text" id="payout-target" placeholder="]==] .. t("wallet.payout_target_ph") .. [==[" style="margin-bottom:6px">
        <div class="fee-mode-row" style="margin:8px 0;font-size:12.5px">
          <label style="margin-right:14px;cursor:pointer">
            <input type="radio" name="payout-mode" value="" checked onchange="payoutModeChanged()"> ]==] .. t("wallet.payout_mode_off") .. [==[
          </label>
          <label style="margin-right:14px;cursor:pointer">
            <input type="radio" name="payout-mode" value="threshold" onchange="payoutModeChanged()"> ]==] .. t("wallet.payout_mode_threshold") .. [==[
          </label>
          <label style="cursor:pointer">
            <input type="radio" name="payout-mode" value="interval" onchange="payoutModeChanged()"> ]==] .. t("wallet.payout_mode_interval") .. [==[
          </label>
        </div>
        <div id="payout-threshold-row" style="display:none;margin-bottom:6px">
          <input type="number" id="payout-threshold" min="0" step="1" placeholder="]==] .. t("wallet.payout_threshold_ph") .. [==[">
        </div>
        <div id="payout-interval-row" style="display:none;margin-bottom:6px">
          <input type="number" id="payout-interval" min="0" step="0.5" placeholder="]==] .. t("wallet.payout_interval_ph") .. [==[">
        </div>
        <button class="btn-sm btn-prim" onclick="savePayout()">]==] .. t("wallet.payout_save") .. [==[</button>
        <div id="payout-out" class="status-line"></div>
      </div>
    </details>
    <details class="seed-manage" style="margin-top:10px">
      <summary>]==] .. t("wallet.seed_manage_title") .. [==[</summary>
      <div style="padding:10px 0">
        <p class="w-hint">]==] .. t("wallet.seed_manage_hint") .. [==[</p>
        <input type="password" id="seed-reveal-pass" placeholder="]==] .. t("wallet.seed_pass_ph") .. [==[" style="margin-bottom:6px">
        <button class="btn-sm" onclick="revealSeed()">]==] .. t("wallet.seed_reveal_btn") .. [==[</button>
        <div id="seed-reveal-out" class="seed-words" style="display:none;margin-top:8px"></div>
        <hr style="border:none;border-top:1px solid var(--brd);margin:12px 0">
        <p class="w-hint">]==] .. t("wallet.seed_setpass_hint") .. [==[</p>
        <input type="password" id="seed-new-pass" placeholder="]==] .. t("wallet.seed_newpass_ph") .. [==[" style="margin-bottom:6px">
        <button class="btn-sm" onclick="setSeedPass()">]==] .. t("wallet.seed_setpass_btn") .. [==[</button>
        <div id="seed-pass-out" class="status-line"></div>
        <hr style="border:none;border-top:1px solid var(--brd);margin:12px 0">
        <p class="w-hint">]==] .. t("wallet.seed_encrypt_hint") .. [==[</p>
        <input type="password" id="seed-enc-pass" placeholder="]==] .. t("wallet.seed_encpass_ph") .. [==[" style="margin-bottom:6px">
        <button class="btn-warn" onclick="encryptSeed()">]==] .. t("wallet.seed_encrypt_btn") .. [==[</button>
        <div id="seed-enc-out" class="status-line"></div>
        <hr style="border:none;border-top:1px solid var(--brd);margin:12px 0">
        <p class="w-hint" style="color:var(--warning)">]==] .. t("wallet.seed_recreate_warn") .. [==[</p>
        <button class="btn-warn" onclick="recreateWallet()">]==] .. t("wallet.seed_recreate_btn") .. [==[</button>
        <div id="seed-recreate-out" class="status-line"></div>
      </div>
    </details>
    <p class="w-hint">]==] .. t("wallet.earnings_hint") .. [==[</p>
  </div>
</div>
<div class="w-card">
  <div class="w-head"><h3>]==] .. t("wallet.create_open") .. [==[</h3></div>
  <div class="w-body">
    <p class="w-hint">Mit Schlüssel (30 Wörter oder E-Mail+Passwort) öffnest du
    die Wallet voll — die Adresse wird daraus automatisch berechnet, Senden ist
    möglich. Ohne Schlüssel, nur mit Adresse, öffnet sie read-only (ansehen/
    empfangen). Der Seed wird nur an deinen eigenen Node gesendet, dort einmalig
    zum Signieren genutzt und sofort verworfen.</p>
    <div class="w-label">]==] .. t("wallet.with_key") .. [==[</div>
    <div class="w-tabs">
      <button id="tab-words" class="w-tab active" onclick="walTab('words')">30 Wörter</button>
      <button id="tab-emailpw" class="w-tab" onclick="walTab('emailpw')">]==] .. t("wallet.tab_emailpw") .. [==[</button>
    </div>
    <div id="pane-words" class="w-pane">
      <textarea id="wal-words" rows="3" placeholder="30 Wörter, durch Leerzeichen getrennt"></textarea>
    </div>
    <div id="pane-emailpw" class="w-pane" style="display:none">
      <input type="email" id="wal-email" placeholder="]==] .. t("wallet.ph_email") .. [==[">
      <input type="password" id="wal-pass" placeholder="]==] .. t("wallet.ph_password") .. [==[">
      <label class="w-gen"><input type="checkbox" id="wal-newwords"> Neue 30 Wörter generieren &amp; anzeigen</label>
    </div>
    <div class="w-or">— oder —</div>
    <div class="w-label">]==] .. t("wallet.addr_only") .. [==[</div>
    <input type="text" id="wal-addr" placeholder="]==] .. t("wallet.ph_address") .. [==[">
    <button class="btn-prim" onclick="walDerive()">]==] .. t("wallet.open_wallet") .. [==[</button>
    <div id="wal-derive-out" class="status-line"></div>
    <div id="wal-open-state" class="w-open" style="display:none"></div>
    <div id="wal-words-backup" class="w-backup" style="display:none"></div>
  </div>
</div>

<div class="w-card">
  <div class="w-head"><h3>]==] .. t("wallet.transfer_title") .. [==[</h3></div>
  <div class="w-body">
    <input type="text" id="wal-to" placeholder="]==] .. t("wallet.ph_recipient") .. [==[">
    <input type="number" id="wal-amount" placeholder="]==] .. t("wallet.ph_amount") .. [==[" step="0.0001" min="0" oninput="walUpdateFeeHint()">
    <div class="fee-mode-row" style="margin:6px 0;font-size:12.5px">
      <label style="margin-right:14px;cursor:pointer">
        <input type="radio" name="wal-fee-mode" value="added" checked onchange="walUpdateFeeHint()"> ]==] .. t("wallet.fee_added") .. [==[
      </label>
      <label style="cursor:pointer">
        <input type="radio" name="wal-fee-mode" value="inclusive" onchange="walUpdateFeeHint()"> ]==] .. t("wallet.fee_inclusive") .. [==[
      </label>
    </div>
    <div id="wal-fee-hint" class="w-hint" style="margin:4px 0"></div>
    <button class="btn-prim" onclick="walTransfer()">]==] .. t("wallet.send") .. [==[</button>
    <div id="wal-transfer-out" class="status-line"></div>
    <p class="w-hint">]==] .. t("wallet.sign_note") .. [==[</p>
  </div>
</div>

<div class="w-card">
  <div class="w-head">
    <h3>]==] .. t("wallet.open_escrows") .. [==[</h3>
    <button class="btn-sm" onclick="loadEscrows()">↻</button>
  </div>
  <div class="w-body">
    <div id="escrow-list"><div class="empty-hint">]==] .. t("wallet.loading") .. [==[</div></div>
  </div>
</div>
</div><!-- wallet-layout -->

<style>

.w-pane input:not([type=checkbox]), .w-pane textarea, #wal-to, #wal-amount, #wal-mint-to, #wal-mint-amount, #wal-addr {
  width:100%; margin:4px 0; padding:8px; border:1px solid var(--brd);
  border-radius:8px; background:var(--bg,#111); color:var(--txt); font-size:13px; }
.w-label { font-size:12px; color:var(--muted); margin:8px 0 4px; font-weight:600; }
.w-or { text-align:center; font-size:11.5px; color:var(--muted); margin:10px 0 2px; }
.w-gen { display:flex; align-items:center; gap:8px; font-size:12.5px; color:var(--muted); margin-top:6px; }
.w-gen input[type=checkbox] { width:16px; height:16px; flex:0 0 auto; margin:0; }
.w-tabs { display:flex; gap:6px; margin:4px 0 10px; }
.w-tab { padding:7px 14px; font-size:13px; font-weight:600; cursor:pointer;
  background:var(--sur2); color:var(--muted); border:1px solid var(--brd);
  border-radius:8px; transition:background .15s, color .15s, border-color .15s; }
.w-tab:hover { color:var(--txt); border-color:var(--brd2); }
.w-tab.active { background:var(--accent-bg,rgba(120,120,255,.12)); color:var(--txt);
  border-color:var(--green); }
.earn-grid { display:flex; gap:10px; flex-wrap:wrap; }
.earn-cell { flex:1; min-width:90px; display:flex; flex-direction:column; gap:2px;
  padding:10px 12px; background:var(--sur2); border-radius:8px; text-align:center; }
.earn-num { font-size:20px; font-weight:700; color:var(--green); font-family:var(--mono,monospace); }
.earn-lbl { font-size:11px; color:var(--muted); }
@keyframes earnFlash { 0% { transform:scale(1.25); color:var(--grn,#00e676); } 100% { transform:scale(1); } }
.earn-flash { animation:earnFlash .6s ease-out; }
.seed-warn { margin:10px 0; padding:12px 14px; background:rgba(245,166,35,.1);
  border:1px solid var(--warning); border-radius:8px; }
.seed-warn strong { color:var(--warning); font-size:13px; }
.seed-warn p { font-size:12px; color:var(--muted); margin:4px 0 8px; line-height:1.5; }
.seed-words { display:block; padding:10px; background:var(--sur2); border-radius:6px;
  font-family:monospace; font-size:13px; line-height:1.7; word-spacing:3px;
  color:var(--txt); word-break:break-word; }
.seed-manage summary { cursor:pointer; font-size:13px; font-weight:600; color:var(--txt);
  padding:6px 0; user-select:none; }
.seed-manage summary:hover { color:var(--green); }
.w-backup { margin-top:8px; padding:10px; background:rgba(245,166,35,.08);
  border:1px solid rgba(245,166,35,.3); border-radius:8px; font-size:13px;
  word-break:break-word; font-family:monospace; }
.w-open { margin-top:8px; padding:10px; background:rgba(0,230,118,.07);
  border:1px solid rgba(0,230,118,.3); border-radius:8px; font-size:12.5px;
  word-break:break-word; }
.w-hint { font-size:12px; color:var(--muted); margin:6px 0 0; }

/* Deutlich sichtbare Umrandung für Buttons + Eingabefelder im Wallet-Bereich
   (wie die Ergebnis-Karten .w-open/.w-backup). Überschreibt das border:none der
   Basis-Buttons. */
.w-body .btn-prim, .w-body .btn-warn {
  border:1px solid var(--acc) !important; margin-top:6px;
  box-shadow:0 0 0 1px rgba(56,124,255,.12); }
.w-body .btn-warn {
  background:rgba(245,166,35,.12); color:var(--txt);
  border-color:rgba(245,166,35,.55) !important;
  box-shadow:0 0 0 1px rgba(245,166,35,.12); }
.w-body .btn-warn:hover { background:rgba(245,166,35,.2); }
.w-body input:not([type=checkbox]), .w-body textarea {
  border:1px solid var(--brd2); }
.w-body input:not([type=checkbox]):focus, .w-body textarea:focus {
  border-color:var(--acc); outline:none; }
/* Jede Eingabe/Aktion klarer als Gruppe absetzen */
.w-pane { padding:8px; border:1px solid var(--brd); border-radius:8px;
  background:rgba(255,255,255,.02); margin-bottom:4px; }
</style>

<script>
function walTab(which){
  ['words','emailpw'].forEach(function(m){
    document.getElementById('tab-'+m).classList.toggle('active', which===m);
    document.getElementById('pane-'+m).style.display = which===m ? 'block':'none';
  });
}
function walKeyMode(){
  return document.getElementById('tab-words').classList.contains('active') ? 'words' : 'emailpw';
}
function walHasKey(){
  if (walKeyMode()==='words') {
    return document.getElementById('wal-words').value.trim().length > 0;
  }
  return document.getElementById('wal-email').value.trim().length > 0
      && document.getElementById('wal-pass').value.length > 0;
}
function walSeedBody(){
  if (walKeyMode()==='words') {
    const w = document.getElementById('wal-words').value.trim().split(/\s+/).filter(Boolean);
    return { words: w };
  }
  return {
    email: document.getElementById('wal-email').value.trim(),
    password: document.getElementById('wal-pass').value
  };
}
async function walDerive(){
  const out = document.getElementById('wal-derive-out');
  // Automatik: Schlüssel vorhanden → voll ableiten; sonst Adresse → read-only.
  if (!walHasKey()) {
    const a = document.getElementById('wal-addr').value.trim();
    if (!a) {
      out.textContent=WT.enter_key_or_addr;
      out.style.color='var(--red)'; return;
    }
    if (!/^0x[0-9a-fA-F]{40}$/.test(a)) {
      out.textContent=WT.invalid_address; out.style.color='var(--red)'; return;
    }
    out.innerHTML=WT.opened_readonly+'<code>'+a+'</code>'; out.style.color='var(--grn)';
    window.walOpenAddress = a;
    window.walReadOnly = true;
    { const mt = document.getElementById('wal-mint-to'); if (mt) mt.value = a; }
    await walShowBalance(a);
    return;
  }
  window.walReadOnly = false;
  out.textContent = WT.deriving; out.style.color = 'var(--muted)';
  try {
    const body = walSeedBody();
    const r = await fetch('/api/v1/wallet/derive', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify(body)
    });
    const d = await r.json();
    if (!r.ok) { out.textContent = '✗ ' + (d.error||WT.error_word); out.style.color='var(--red)'; return; }
    out.innerHTML = WT.opened + '<code>' + d.address + '</code>';
    out.style.color = 'var(--grn)';
    // Wallet als geöffnet markieren: Adresse merken, Balance laden, Felder vorbelegen.
    window.walOpenAddress = d.address;
    { const mt = document.getElementById('wal-mint-to'); if (mt) mt.value = d.address; }
    await walShowBalance(d.address);
    if (d.words && d.words.length) {
      const bk = document.getElementById('wal-words-backup');
      bk.style.display = 'block';
      bk.innerHTML = '<strong>]==] .. t("wallet.your_words") .. [==[</strong><br>' + d.words.join(' ');
    }
  } catch(e){ out.textContent = '✗ ' + e.message; out.style.color='var(--red)'; }
}
// Auto-Update: pollt die Chain-Höhe; steigt sie (= neuer Block, evtl. mit einer
// Tx an die geöffnete Wallet), wird die Balance neu geladen. Effizient, weil die
// Balance nur bei echten Blöcken neu abgefragt wird, nicht im festen Takt.
let walLastHeight = -1;
async function walHeightWatch(){
  if (!window.walOpenAddress) return;
  try {
    const r = await fetch('/api/v1/chain/status');
    if (!r.ok) return;
    const d = await r.json();
    if (typeof d.height === 'number' && d.height !== walLastHeight) {
      const first = walLastHeight < 0;
      walLastHeight = d.height;
      if (!first) { await walShowBalance(window.walOpenAddress); } // nur bei Änderung
    }
  } catch(e){ /* offline: stiller Retry beim nächsten Tick */ }
}
setInterval(walHeightWatch, 5000);

async function walShowBalance(addr){
  const el = document.getElementById('wal-open-state');
  el.style.display = 'block';
  el.innerHTML = WT.balance_loading;
  const setTop = (id,v) => { const e=document.getElementById(id); if(e) e.textContent=v; };
  // Obere Adresse sofort auf die geöffnete Wallet setzen (unabhängig vom Saldo).
  setTop('top-wal-address', addr);
  try {
    const r = await fetch('/api/v1/wallet/balance?address=' + encodeURIComponent(addr));
    const d = await r.json();
    if (r.ok) {
      const fnd = (d.fnd!==undefined && d.fnd!==null) ? d.fnd : '?';
      el.innerHTML = '<strong>]==] .. t("wallet.opened_wallet") .. [==[</strong> <code>' + addr +
        '</code><br>]==] .. t("wallet.balance_label") .. [==[ <strong>' + fnd + ' FND</strong>';
      // Obere Felder auf die geöffnete Wallet umstellen: freier Saldo aus der
      // nativen Chain. Escrow/Pending sind für eine geöffnete Fremdadresse nicht
      // ermittelbar → auf "–" setzen, damit keine irreführende 0 erscheint.
      const num = parseFloat(d.fnd);
      setTop('bal-free', isNaN(num) ? '?' : num.toLocaleString('de-DE', {minimumFractionDigits:4, maximumFractionDigits:4}));
      setTop('bal-escrow', '–');
    } else {
      el.innerHTML = '<strong>]==] .. t("wallet.opened_wallet") .. [==[</strong> <code>' + addr +
        '</code><br><span style="color:var(--muted)">' + WT.balance_unavailable + (d.error||'Chain offline') + ')</span>';
    }
  } catch(e){
    el.innerHTML = '<strong>]==] .. t("wallet.opened_wallet") .. [==[</strong> <code>' + addr + '</code>';
  }
}
function walFeeMode(){
  const el = document.querySelector('input[name="wal-fee-mode"]:checked');
  return el ? el.value : 'added';
}
function walUpdateFeeHint(){
  const hint = document.getElementById('wal-fee-hint');
  if (!hint) return;
  const amount = parseFloat(document.getElementById('wal-amount').value);
  if (!(amount>0)) { hint.textContent=''; return; }
  const FEE = 0.018;
  if (walFeeMode()==='inclusive'){
    const net = amount/(1+FEE);
    hint.textContent = WT.fee_hint_inclusive
      .replace('{total}', amount.toFixed(4))
      .replace('{net}', net.toFixed(4))
      .replace('{fee}', (amount-net).toFixed(4));
  } else {
    const fee = amount*FEE;
    hint.textContent = WT.fee_hint_added
      .replace('{net}', amount.toFixed(4))
      .replace('{total}', (amount+fee).toFixed(4))
      .replace('{fee}', fee.toFixed(4));
  }
}
async function walTransfer(){
  const out = document.getElementById('wal-transfer-out');
  if (window.walReadOnly || !walHasKey()) {
    out.textContent=WT.readonly_hint;
    out.style.color='var(--red)'; return;
  }
  const to = document.getElementById('wal-to').value.trim();
  const amount = parseFloat(document.getElementById('wal-amount').value);
  if (!to || !(amount>0)) { out.textContent=WT.enter_recipient_amount; out.style.color='var(--red)'; return; }
  out.textContent = WT.sending; out.style.color='var(--muted)';
  try {
    const body = Object.assign(walSeedBody(), { to, amount, fee_mode: walFeeMode() });
    const r = await fetch('/api/v1/wallet/transfer', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify(body)
    });
    const d = await r.json();
    if (!r.ok) { out.textContent='✗ '+(d.error||WT.error_word); out.style.color='var(--red)'; return; }
    out.innerHTML = WT.sent_tx + '<code>'+d.tx_hash+'</code>';
    out.style.color='var(--grn)';
    if (window.walOpenAddress) { await walShowBalance(window.walOpenAddress); }
  } catch(e){ out.textContent='✗ '+e.message; out.style.color='var(--red)'; }
}
async function walMint(){
  const out = document.getElementById('wal-mint-out');
  const toEl = document.getElementById('wal-mint-to');
  const amtEl = document.getElementById('wal-mint-amount');
  if (!out || !toEl || !amtEl) return; // Mint-Bereich nicht vorhanden
  if (window.walReadOnly || !walHasKey()) {
    out.textContent=WT.readonly_hint;
    out.style.color='var(--red)'; return;
  }
  const to = toEl.value.trim();
  const amount = parseFloat(amtEl.value);
  if (!to || !(amount>0)) { out.textContent=WT.enter_addr_amount; out.style.color='var(--red)'; return; }
  out.textContent = WT.minting; out.style.color='var(--muted)';
  try {
    const body = Object.assign(walSeedBody(), { to, amount });
    const r = await fetch('/api/v1/wallet/mint', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify(body)
    });
    const d = await r.json();
    if (!r.ok) { out.textContent='✗ '+(d.error||WT.error_word); out.style.color='var(--red)'; return; }
    out.innerHTML = WT.minted_tx + '<code>'+d.tx_hash+'</code>';
    out.style.color='var(--grn)';
    if (window.walOpenAddress) { await walShowBalance(window.walOpenAddress); }
  } catch(e){ out.textContent='✗ '+e.message; out.style.color='var(--red)'; }
}

// Verdienst-Übersicht laden: offene Quittungen, ausgelieferte MB, gehostete Chunks.
async function loadEarnings(){
  const status = document.getElementById('earn-status');
  try {
    const r = await fetch('/api/v1/files/earnings');
    const d = await r.json();
    if (!d.enabled) {
      if (status) { status.textContent = 'Filesharing nicht aktiv.'; status.style.color='var(--muted)'; }
      return;
    }
    // Gesperrte Wallet (verschlüsselte Seed, noch nicht entsperrt) → Banner zeigen.
    const lockBanner = document.getElementById('wallet-locked-banner');
    if (lockBanner) lockBanner.style.display = d.wallet_locked ? 'block' : 'none';
    // Wert setzen und bei Änderung kurz aufblitzen lassen (fühlt sich wie ein
    // Live-Update an, ohne echtes Server-Push).
    const setVal = (id, val) => {
      const el = document.getElementById(id);
      if (!el) return;
      const s = String(val);
      if (el.textContent !== s && el.textContent !== '–') {
        el.classList.remove('earn-flash');
        void el.offsetWidth; // Reflow erzwingen, damit die Animation neu startet
        el.classList.add('earn-flash');
      }
      el.textContent = s;
    };
    setVal('earn-pending', d.pending_count || 0);
    // Offener FND-Gegenwert: uFND → FND (1 FND = 1e9 uFND).
    const pfnd = (d.pending_ufnd || 0) / 1e9;
    setVal('earn-pending-fnd', pfnd < 1 ? pfnd.toFixed(4) : pfnd.toFixed(2));
    // Roher uFND-Wert zusätzlich (mit Tausender-Trennung, weil große Zahl).
    setVal('earn-pending-ufnd', (d.pending_ufnd || 0).toLocaleString('de-DE'));
    const mb = (d.fetch_bytes || 0) / (1024*1024);
    setVal('earn-fetch', mb.toFixed(mb < 10 ? 2 : 0));
    setVal('earn-hosting', d.hosting_entries || 0);
    // Node-Wallet-Adresse merken. Verdienst-Ansicht automatisch laden (statt nur
    // einen Button zu zeigen), damit der Betreiber sein Guthaben sofort sieht.
    if (d.node_address) {
      window.nodeWalletAddr = d.node_address;
      const btn = document.getElementById('earn-open-node');
      if (btn) btn.style.display = 'inline-block';
      openNodeWallet();       // Saldo direkt anzeigen (darf refreshen)
      loadStakeStatus();      // Validator-Status anzeigen
      // Payout-Config nur EINMAL laden, nicht bei jedem 3s-Refresh — sonst würde
      // sie überschreiben, was der Betreiber gerade eintippt.
      if (!window.payoutLoaded) {
        window.payoutLoaded = true;
        loadPayoutConfig();
      }
    }
    // Neu erzeugte Seed-Wörter: wenn der Node meldet, dass welche zur Sicherung
    // bereitstehen, über den dedizierten Endpunkt laden (verbraucht sie NICHT).
    if (d.seed_pending) {
      showSeedBackup();
    }
    if (status) {
      if (!d.signing_active) {
        status.textContent = 'Hinweis: Kein Wallet-Schlüssel konfiguriert — es werden keine Quittungen ausgestellt.';
        status.style.color='var(--warning)';
      } else {
        status.textContent = '';
      }
    }
  } catch(e){
    if (status) { status.textContent = '✗ '+e.message; status.style.color='var(--red)'; }
  }
}
loadEarnings().catch(()=>{});
setInterval(()=>loadEarnings().catch(()=>{}), 3000);

// Node-Wallet (automatisch erzeugt) read-only öffnen: zeigt Adresse + Saldo.
// Diese Wallet verdient die Storage-Rewards. Sie hat keine Seed-Wörter (der
// Node-Schlüssel ist zufällig erzeugt), daher nur Ansicht, kein Seed-Export.
// Node-Wallet (automatisch erzeugt) read-only anzeigen: Adresse + Saldo direkt
// in der Verdienst-Karte (nicht im entfernten Öffnen-Bereich).
// Validator-Status laden: bin ich im Set? Wie viele Validatoren insgesamt?
async function loadStakeStatus(){
  const el = document.getElementById('stake-status');
  if (!el) return;
  try {
    const r = await fetch('/api/v1/chain/status');
    if (!r.ok) return;
    const d = await r.json();
    const n = d.validators || 0;
    const me = window.nodeWalletAddr ? (d.validator_addrs||[]).map(a=>a.toLowerCase()).indexOf(window.nodeWalletAddr.toLowerCase()) >= 0 : false;
    el.innerHTML = WT.stake_validators + ': <strong>' + n + '</strong> &middot; ' +
      (me ? '<span style="color:var(--grn)">' + WT.stake_you_are + '</span>'
          : '<span style="color:var(--muted)">' + WT.stake_you_not + '</span>');
  } catch(e){}
}

// Staken: Node-Wallet sperrt FND als Validator-Einsatz.
async function doStake(){
  const out = document.getElementById('stake-out');
  const amt = parseFloat(document.getElementById('stake-amount').value) || 0;
  if (amt < 10){ if(out){ out.textContent = WT.stake_min; out.style.color='var(--red)'; } return; }
  if (out){ out.textContent = WT.stake_sending; out.style.color='var(--muted)'; }
  try {
    const r = await fetch('/api/v1/wallet/stake', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ amount_fnd: amt })
    });
    const d = await r.json();
    if (r.ok){
      if (out){ out.textContent = WT.stake_ok; out.style.color='var(--grn)'; }
      setTimeout(loadStakeStatus, 2000);
    } else {
      if (out){ out.textContent = '✗ ' + (d.error || WT.error_word); out.style.color='var(--red)'; }
    }
  } catch(e){ if(out){ out.textContent = '✗ ' + e.message; out.style.color='var(--red)'; } }
}

// Unstaken: gestakte FND freigeben (Sperrfrist bis zur Rückzahlung).
async function doUnstake(){
  const out = document.getElementById('stake-out');
  const amt = parseFloat(document.getElementById('stake-amount').value) || 0;
  if (amt <= 0){ if(out){ out.textContent = WT.stake_min; out.style.color='var(--red)'; } return; }
  if (out){ out.textContent = WT.stake_sending; out.style.color='var(--muted)'; }
  try {
    const r = await fetch('/api/v1/wallet/unstake', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ amount_fnd: amt })
    });
    const d = await r.json();
    if (r.ok){
      if (out){ out.textContent = WT.stake_unstaked; out.style.color='var(--grn)'; }
      setTimeout(loadStakeStatus, 2000);
    } else {
      if (out){ out.textContent = '✗ ' + (d.error || WT.error_word); out.style.color='var(--red)'; }
    }
  } catch(e){ if(out){ out.textContent = '✗ ' + e.message; out.style.color='var(--red)'; } }
}

// Node-Wallet (automatisch erzeugt) read-only anzeigen: Adresse + Saldo direkt
// in der Verdienst-Karte (nicht im entfernten Öffnen-Bereich).
async function openNodeWallet(){
  if (!window.nodeWalletAddr) return;
  const el = document.getElementById('node-wal-state');
  if (!el) return;
  const addr = window.nodeWalletAddr;
  el.style.display = 'block';
  el.innerHTML = WT.balance_loading;
  try {
    const r = await fetch('/api/v1/wallet/balance?address=' + encodeURIComponent(addr));
    const d = await r.json();
    if (r.ok) {
      const fnd = (d.fnd!==undefined && d.fnd!==null) ? d.fnd : '?';
      el.innerHTML = '<strong>]==] .. t("wallet.node_wallet_label") .. [==[</strong> <code>' + addr +
        '</code><br>]==] .. t("wallet.balance_label") .. [==[ <strong>' + fnd + ' FND</strong>';
    } else {
      el.innerHTML = '<code>' + addr + '</code><br><span style="color:var(--muted)">' +
        WT.balance_unavailable + (d.error||'Chain offline') + ')</span>';
    }
  } catch(e){
    el.innerHTML = '<code>' + addr + '</code>';
  }
}

// Auto-Payout: Modus-Wechsel blendet die passenden Felder ein.
function payoutMode(){
  const el = document.querySelector('input[name="payout-mode"]:checked');
  return el ? el.value : '';
}
function payoutModeChanged(){
  const mode = payoutMode();
  const tr = document.getElementById('payout-threshold-row');
  const ir = document.getElementById('payout-interval-row');
  if (tr) tr.style.display = (mode === 'threshold') ? 'block' : 'none';
  if (ir) ir.style.display = (mode === 'interval') ? 'block' : 'none';
}

// Auto-Payout-Einstellung vom Server laden und ins Formular schreiben.
async function loadPayoutConfig(){
  try {
    const r = await fetch('/api/v1/wallet/payout');
    if (!r.ok) return;
    const d = await r.json();
    if (!d.enabled) return;
    const t = document.getElementById('payout-target');
    const th = document.getElementById('payout-threshold');
    const iv = document.getElementById('payout-interval');
    if (t)  t.value  = d.target || '';
    const radio = document.querySelector('input[name="payout-mode"][value="' + (d.mode||'') + '"]');
    if (radio) radio.checked = true;
    if (th && d.threshold_fnd) th.value = d.threshold_fnd;
    if (iv && d.interval_hrs)  iv.value = d.interval_hrs;
    payoutModeChanged();
  } catch(e){}
}

// Auto-Payout-Einstellung speichern.
async function savePayout(){
  const out = document.getElementById('payout-out');
  const target = document.getElementById('payout-target').value.trim();
  const mode = payoutMode();
  const body = { target: target, mode: mode, threshold_fnd: 0, interval_hrs: 0, min_fnd: 0 };
  if (mode === 'threshold') body.threshold_fnd = parseFloat(document.getElementById('payout-threshold').value) || 0;
  if (mode === 'interval')  body.interval_hrs  = parseFloat(document.getElementById('payout-interval').value) || 0;
  if (out){ out.textContent = WT.payout_saving; out.style.color='var(--muted)'; }
  try {
    const r = await fetch('/api/v1/wallet/payout', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify(body)
    });
    const d = await r.json();
    if (r.ok){
      if (out){ out.textContent = WT.payout_saved; out.style.color='var(--grn)'; }
    } else {
      if (out){ out.textContent = '✗ ' + (d.error || WT.error_word); out.style.color='var(--red)'; }
    }
  } catch(e){
    if (out){ out.textContent = '✗ ' + e.message; out.style.color='var(--red)'; }
  }
}

// Vorhandene Klartext-Seed mit Passwort verschlüsseln (Migration).
async function encryptSeed(){
  const pass = document.getElementById('seed-enc-pass').value;
  const out = document.getElementById('seed-enc-out');
  if (pass.length < 8){ out.textContent = WT.seed_enc_tooshort; out.style.color='var(--warning)'; return; }
  if (!confirm(WT.seed_encrypt_confirm)) return;
  try {
    const r = await fetch('/api/v1/files/node-seed/encrypt', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({password: pass})
    });
    const d = await r.json();
    out.textContent = d.ok ? WT.seed_encrypted_ok : ('✗ '+(d.error||''));
    out.style.color = d.ok ? 'var(--grn)' : 'var(--red)';
  } catch(e){ out.textContent = '✗ ' + e.message; }
}

// Gesperrte (verschlüsselte) Wallet zur Laufzeit entsperren.
async function unlockWallet(){
  const pass = document.getElementById('unlock-pass').value;
  const out = document.getElementById('unlock-out');
  try {
    const r = await fetch('/api/v1/files/node-seed/unlock', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({password: pass})
    });
    const d = await r.json();
    if (d.ok){
      out.textContent = WT.wallet_unlocked; out.style.color='var(--grn)';
      loadEarnings().catch(()=>{}); // Banner verschwindet, Werte laden
    } else {
      out.textContent = '✗ ' + (d.error||WT.seed_wrong_pass); out.style.color='var(--red)';
    }
  } catch(e){ out.textContent = '✗ ' + e.message; }
}
async function revealSeed(){
  const pass = document.getElementById('seed-reveal-pass').value;
  const out = document.getElementById('seed-reveal-out');
  try {
    const r = await fetch('/api/v1/files/node-seed/reveal', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({password: pass})
    });
    const d = await r.json();
    if (d.words && d.words.length) {
      out.textContent = d.words.join(' ');
      out.style.display = 'block';
    } else if (d.error) {
      out.textContent = '✗ ' + (d.protected ? WT.seed_wrong_pass : d.error);
      out.style.display = 'block';
      out.style.color = 'var(--warning)';
    }
  } catch(e){ out.textContent = '✗ ' + e.message; out.style.display='block'; }
}

// Anzeige-Passwort setzen/ändern/entfernen (leer = entfernen).
async function setSeedPass(){
  const pass = document.getElementById('seed-new-pass').value;
  const out = document.getElementById('seed-pass-out');
  try {
    const r = await fetch('/api/v1/files/node-seed/password', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({password: pass})
    });
    const d = await r.json();
    out.textContent = d.ok ? (d.protected ? WT.seed_pass_set : WT.seed_pass_removed) : ('✗ '+(d.error||''));
    out.style.color = d.ok ? 'var(--grn)' : 'var(--red)';
  } catch(e){ out.textContent = '✗ ' + e.message; }
}

// Wallet komplett neu erstellen (alte Adresse + Guthaben unwiderruflich weg).
async function recreateWallet(){
  if (!confirm(WT.seed_recreate_confirm)) return;
  const out = document.getElementById('seed-recreate-out');
  try {
    const r = await fetch('/api/v1/files/node-seed/recreate', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({confirm: 'NEU ERSTELLEN'})
    });
    const d = await r.json();
    if (d.ok) {
      out.innerHTML = WT.seed_recreated + ' <code>' + d.address + '</code>';
      out.style.color = 'var(--grn)';
      document.getElementById('seed-reveal-out').textContent = d.words.join(' ');
      document.getElementById('seed-reveal-out').style.display = 'block';
    } else {
      out.textContent = '✗ ' + (d.error||''); out.style.color = 'var(--red)';
    }
  } catch(e){ out.textContent = '✗ ' + e.message; }
}
async function showSeedBackup(){
  const box = document.getElementById('seed-backup');
  if (!box || box.dataset.loaded === '1') return;
  try {
    const r = await fetch('/api/v1/files/node-seed');
    const d = await r.json();
    if (!d.available || !d.words) return;
    box.querySelector('.seed-words').textContent = d.words.join(' ');
    box.style.display = 'block';
    box.dataset.loaded = '1';
  } catch(e){ /* still ignorieren, nächster Poll versucht es erneut */ }
}

// Bestätigen, dass die Wörter gesichert wurden → Box ausblenden, Node leert sie.
async function confirmSeedBackup(){
  try { await fetch('/api/v1/files/node-seed/confirm', {method:'POST'}); } catch(e){}
  const box = document.getElementById('seed-backup');
  if (box) box.style.display = 'none';
}
</script>
]==])

  render.footer()
end

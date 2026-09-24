-- pages/swaptest.lua
-- Test-Oberfläche für den Solana-HTLC (initiate/redeem/refund). Direkte
-- Schlüssel-Eingabe (Mnemonic oder Base58) zum Testen — NICHT für Produktiv.
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("Swap-Test", "swaptest")

    ngx.print([[
<div class="page-wrap">
  <h1>Solana-HTLC — Testkonsole</h1>
  <p class="meta">Direkte Schlüssel-Eingabe zum Testen des atomaren Swaps. Nur für den lokalen Testbetrieb!</p>

  <div id="swap-status-box" class="ob-rate-box" style="display:none"></div>

  <!-- 1. Secret erzeugen -->
  <div class="ob-form-card">
    <h3>1. Geheimnis erzeugen</h3>
    <p class="meta">Der Käufer erzeugt ein Geheimnis. Der secret_hash ist öffentlich, das secret bleibt geheim bis zum Einlösen.</p>
    <button class="btn" onclick="genSecret()">Secret + Hash erzeugen</button>
    <div class="field" style="margin-top:10px">
      <label>secret (geheim halten!)</label>
      <input type="text" id="sw-secret" readonly placeholder="…">
    </div>
    <div class="field">
      <label>secret_hash (öffentlich)</label>
      <input type="text" id="sw-hash" readonly placeholder="…">
    </div>
  </div>

  <!-- 2. SOL sperren (initiate) -->
  <div class="ob-form-card">
    <h3>2. SOL sperren (initiate)</h3>
    <div class="field"><label>Solana-Schlüssel des Initiators (Mnemonic oder Base58)</label>
      <input type="text" id="init-key" placeholder="wort1 wort2 …">
      <button class="btn-sm" style="margin-top:6px" onclick="checkAddr('init-key')">Abgeleitete Adresse prüfen</button>
      <div id="init-key-addr" class="meta" style="margin-top:4px"></div></div>
    <div class="field"><label>Betrag (SOL)</label>
      <input type="number" id="init-amount" step="0.001" placeholder="0.1"></div>
    <div class="field"><label>Redeemer-Adresse (Solana, wer einlösen darf)</label>
      <input type="text" id="init-redeemer" placeholder="Solana-Adresse"></div>
    <div class="field"><label>secret_hash (aus Schritt 1)</label>
      <input type="text" id="init-hash" placeholder="hex"></div>
    <div class="field"><label>Timelock (Slots, ~400ms; leer = ~24h)</label>
      <input type="number" id="init-slots" placeholder="216000"></div>
    <button class="btn ob-buy-btn" onclick="doInitiate()">SOL sperren</button>
  </div>

  <!-- 3. SOL einlösen (redeem) -->
  <div class="ob-form-card">
    <h3>3. SOL einlösen (redeem)</h3>
    <div class="field"><label>Solana-Schlüssel des Redeemers</label>
      <input type="text" id="red-key" placeholder="wort1 wort2 …"></div>
    <div class="field"><label>Initiator-Adresse (wer gesperrt hat)</label>
      <input type="text" id="red-initiator" placeholder="Solana-Adresse"></div>
    <div class="field"><label>secret_hash</label>
      <input type="text" id="red-hash" placeholder="hex"></div>
    <div class="field"><label>secret (das Geheimnis aus Schritt 1)</label>
      <input type="text" id="red-secret" placeholder="hex"></div>
    <button class="btn ob-buy-btn" onclick="doRedeem()">Einlösen</button>
  </div>

  <!-- 4. Zurückholen (refund) -->
  <div class="ob-form-card">
    <h3>4. SOL zurückholen (refund, nach Timelock)</h3>
    <div class="field"><label>Solana-Schlüssel des Initiators</label>
      <input type="text" id="ref-key" placeholder="wort1 wort2 …"></div>
    <div class="field"><label>secret_hash</label>
      <input type="text" id="ref-hash" placeholder="hex"></div>
    <button class="btn ob-sell-btn" onclick="doRefund()">Zurückholen</button>
  </div>

  <hr style="margin:24px 0;border-color:var(--border)">
  <h2>FND-Seite (Fundus-Chain)</h2>

  <!-- 5. FND sperren (lock) -->
  <div class="ob-form-card">
    <h3>5. FND sperren (lock)</h3>
    <div class="field"><label>Seed-Wörter des Sperrers (FND-Wallet)</label>
      <input type="text" id="fl-seed" placeholder="wort1 wort2 …"></div>
    <div class="field"><label>Betrag (FND)</label>
      <input type="number" id="fl-amount" step="0.01" placeholder="10"></div>
    <div class="field"><label>Empfänger-Adresse (FND, wer einlösen darf)</label>
      <input type="text" id="fl-recipient" placeholder="0x…"></div>
    <div class="field"><label>secret_hash (derselbe wie bei SOL!)</label>
      <input type="text" id="fl-hash" placeholder="hex"></div>
    <div class="field"><label>Timelock (Blöcke, ~5s; leer = ~24h)</label>
      <input type="number" id="fl-timelock" placeholder="17280"></div>
    <button class="btn ob-buy-btn" onclick="doFndLock()">FND sperren</button>
  </div>

  <!-- 6. FND einlösen (claim) -->
  <div class="ob-form-card">
    <h3>6. FND einlösen (claim)</h3>
    <div class="field"><label>Seed-Wörter des Einlösers</label>
      <input type="text" id="fc-seed" placeholder="wort1 wort2 …"></div>
    <div class="field"><label>htlc_id (aus Schritt 5)</label>
      <input type="text" id="fc-id" placeholder="hex"></div>
    <div class="field"><label>secret (das Geheimnis)</label>
      <input type="text" id="fc-secret" placeholder="hex"></div>
    <button class="btn ob-buy-btn" onclick="doFndClaim()">FND einlösen</button>
  </div>

  <!-- 7. FND zurückholen (refund) -->
  <div class="ob-form-card">
    <h3>7. FND zurückholen (refund, nach Timelock)</h3>
    <div class="field"><label>Seed-Wörter des Sperrers</label>
      <input type="text" id="fr-seed" placeholder="wort1 wort2 …"></div>
    <div class="field"><label>htlc_id</label>
      <input type="text" id="fr-id" placeholder="hex"></div>
    <button class="btn ob-sell-btn" onclick="doFndRefund()">FND zurückholen</button>
  </div>

  <hr style="margin:24px 0;border-color:var(--border)">
  <h2>Automatischer Swap (Orchestrator)</h2>
  <p class="meta">Trägt alle Daten ein, dann starten beide Seiten den automatischen Ablauf. Käufer hat SOL/will FND, Verkäufer hat FND/will SOL. Beide nutzen denselben secret_hash (aus Schritt 1). Nur der Käufer trägt das secret ein.</p>

  <div class="ob-form-card">
    <h3>8. Auto-Swap-Daten</h3>
    <div class="field"><label>Betrag SOL</label>
      <input type="number" id="as-sol" step="0.001" placeholder="0.1"></div>
    <div class="field"><label>Betrag FND</label>
      <input type="number" id="as-fnd" step="0.01" placeholder="10"></div>

    <h4 style="margin-top:14px;color:var(--text-dim)">Käufer (SOL → FND)</h4>
    <div class="field"><label>Käufer: Solana-Schlüssel</label>
      <input type="text" id="as-buyer-solkey" placeholder="mnemonic"></div>
    <div class="field"><label>Käufer: FND-Seed</label>
      <input type="text" id="as-buyer-fndseed" placeholder="seed"></div>

    <h4 style="margin-top:14px;color:var(--text-dim)">Verkäufer (FND → SOL)</h4>
    <div class="field"><label>Verkäufer: Solana-Schlüssel</label>
      <input type="text" id="as-seller-solkey" placeholder="mnemonic"></div>
    <div class="field"><label>Verkäufer: FND-Seed</label>
      <input type="text" id="as-seller-fndseed" placeholder="seed"></div>

    <h4 style="margin-top:14px;color:var(--text-dim)">Adressen (öffentlich)</h4>
    <div class="field"><label>Käufer: Solana-Adresse</label>
      <input type="text" id="as-buyer-soladdr" placeholder="Solana-Adresse"></div>
    <div class="field"><label>Käufer: FND-Adresse</label>
      <input type="text" id="as-buyer-fndaddr" placeholder="0x…"></div>
    <div class="field"><label>Verkäufer: Solana-Adresse</label>
      <input type="text" id="as-seller-soladdr" placeholder="Solana-Adresse"></div>
    <div class="field"><label>Verkäufer: FND-Adresse</label>
      <input type="text" id="as-seller-fndaddr" placeholder="0x…"></div>

    <div class="field"><label>secret_hash (beide Seiten)</label>
      <input type="text" id="as-hash" placeholder="hex"></div>
    <div class="field"><label>secret (nur Käufer)</label>
      <input type="text" id="as-secret" placeholder="hex"></div>

    <div style="display:flex;gap:8px;margin-top:12px">
      <button class="btn ob-buy-btn" onclick="triggerSwap()" style="flex:1">Swap auslösen (Kaufmoment)</button>
    </div>
    <p class="meta" style="margin-top:8px">Simuliert den Moment, in dem der Käufer kauft: startet Verkäufer- und Käuferseite in der richtigen Reihenfolge. (Im echten Betrieb läuft der Verkäufer auf seinem eigenen Node und wird per Order-Match ausgelöst.)</p>
    <div id="as-swaps" class="meta" style="margin-top:8px"></div>
  </div>
</div>

<script>
function showStatus(msg, ok){
  const box = document.getElementById("swap-status-box");
  box.style.display = "flex";
  box.innerHTML = '<span class="'+(ok?"":"")+'" style="color:'+(ok?"var(--green)":"var(--red)")+'">'+msg+'</span>';
}

async function checkAddr(fieldId){
  try {
    const key = document.getElementById(fieldId).value.trim();
    if(!key){ return; }
    const r = await fetch("/api/v1/swap/keyaddr", {method:"POST", headers:{"Content-Type":"application/json"}, body: JSON.stringify({sol_key: key})});
    const d = await r.json();
    if(!r.ok || d.error) throw new Error(d.error||"Fehler");
    let html = 'Abgeleitete Adressen je Pfad (finde die, die zu deiner "solana address" passt):<br>';
    for(const [label, addr] of Object.entries(d.addresses||{})){
      html += '<div style="margin:2px 0"><strong>'+label+':</strong> <code style="color:var(--green);font-size:11px">'+addr+'</code></div>';
    }
    document.getElementById(fieldId+"-addr").innerHTML = html;
  } catch(e){ document.getElementById(fieldId+"-addr").textContent = "✗ "+e.message; }
}

async function genSecret(){
  try {
    const r = await fetch("/api/v1/swap/secret");
    const d = await r.json();
    document.getElementById("sw-secret").value = d.secret;
    document.getElementById("sw-hash").value = d.secret_hash;
    // Bequemlichkeit: die Hash-Felder in den anderen Schritten vorbefüllen.
    document.getElementById("init-hash").value = d.secret_hash;
    document.getElementById("red-hash").value = d.secret_hash;
    document.getElementById("red-secret").value = d.secret;
    document.getElementById("ref-hash").value = d.secret_hash;
    document.getElementById("fl-hash").value = d.secret_hash;
    document.getElementById("fc-secret").value = d.secret;
    document.getElementById("as-hash").value = d.secret_hash;
    document.getElementById("as-secret").value = d.secret;
    showStatus("✓ Secret erzeugt und in die Felder übernommen", true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

async function post(url, body){
  const r = await fetch(url, {method:"POST", headers:{"Content-Type":"application/json"}, body: JSON.stringify(body)});
  const d = await r.json();
  if(!r.ok || d.error) throw new Error(d.error || "Fehler");
  return d;
}

async function doInitiate(){
  try {
    showStatus("⏳ Sperre SOL…", true);
    const d = await post("/api/v1/swap/sol/initiate", {
      sol_key: document.getElementById("init-key").value.trim(),
      amount_sol: parseFloat(document.getElementById("init-amount").value)||0,
      redeemer: document.getElementById("init-redeemer").value.trim(),
      secret_hash: document.getElementById("init-hash").value.trim(),
      expires_in_slots: parseInt(document.getElementById("init-slots").value)||0
    });
    showStatus("✓ SOL gesperrt! Signatur: "+d.signature, true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

async function doRedeem(){
  try {
    showStatus("⏳ Löse ein…", true);
    const d = await post("/api/v1/swap/sol/redeem", {
      sol_key: document.getElementById("red-key").value.trim(),
      initiator: document.getElementById("red-initiator").value.trim(),
      secret_hash: document.getElementById("red-hash").value.trim(),
      secret: document.getElementById("red-secret").value.trim()
    });
    showStatus("✓ Eingelöst! Signatur: "+d.signature, true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

async function doRefund(){
  try {
    showStatus("⏳ Hole zurück…", true);
    const d = await post("/api/v1/swap/sol/refund", {
      sol_key: document.getElementById("ref-key").value.trim(),
      secret_hash: document.getElementById("ref-hash").value.trim()
    });
    showStatus("✓ Zurückgeholt! Signatur: "+d.signature, true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

async function doFndLock(){
  try {
    showStatus("⏳ Sperre FND…", true);
    const d = await post("/api/v1/swap/fnd/lock", {
      seed_words: document.getElementById("fl-seed").value.trim(),
      amount_fnd: parseFloat(document.getElementById("fl-amount").value)||0,
      recipient: document.getElementById("fl-recipient").value.trim(),
      secret_hash: document.getElementById("fl-hash").value.trim(),
      timelock_blocks: parseInt(document.getElementById("fl-timelock").value)||0
    });
    // htlc_id in die Claim/Refund-Felder übernehmen.
    document.getElementById("fc-id").value = d.htlc_id;
    document.getElementById("fr-id").value = d.htlc_id;
    showStatus("✓ FND gesperrt! HTLC-ID: "+d.htlc_id+" (in Felder übernommen)", true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

async function doFndClaim(){
  try {
    showStatus("⏳ Löse FND ein…", true);
    const d = await post("/api/v1/swap/fnd/claim", {
      seed_words: document.getElementById("fc-seed").value.trim(),
      htlc_id: document.getElementById("fc-id").value.trim(),
      secret: document.getElementById("fc-secret").value.trim()
    });
    showStatus("✓ FND eingelöst! Tx: "+d.tx, true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

async function doFndRefund(){
  try {
    showStatus("⏳ Hole FND zurück…", true);
    const d = await post("/api/v1/swap/fnd/refund", {
      seed_words: document.getElementById("fr-seed").value.trim(),
      htlc_id: document.getElementById("fr-id").value.trim()
    });
    showStatus("✓ FND zurückgeholt! Tx: "+d.tx, true);
  } catch(e){ showStatus("✗ "+e.message, false); }
}

let asSwapIds = [];

// triggerSwap simuliert den Kaufmoment: startet erst die Verkäuferseite
// (wartet auf SOL-Lock), dann die Käuferseite (sperrt SOL). Auf einem echten
// Setup liefe der Verkäufer bereits auf seinem Node; hier lokal beide.
async function triggerSwap(){
  asSwapIds = [];
  const amtSol = parseFloat(document.getElementById("as-sol").value)||0;
  const amtFnd = parseFloat(document.getElementById("as-fnd").value)||0;
  const hash = document.getElementById("as-hash").value.trim();
  // 1. Verkäufer.
  try {
    showStatus("⏳ Verkäufer startet (wartet auf SOL-Lock)…", true);
    const d = await post("/api/v1/swap/auto/start", {
      role: "seller",
      sol_key: document.getElementById("as-seller-solkey").value.trim(),
      fnd_seed: document.getElementById("as-seller-fndseed").value.trim(),
      counterparty_sol: document.getElementById("as-buyer-soladdr").value.trim(),
      counterparty_fnd: document.getElementById("as-buyer-fndaddr").value.trim(),
      amount_sol: amtSol, amount_fnd: amtFnd, secret_hash: hash
    });
    asSwapIds.push({role:"Verkäufer", id:d.swap_id});
  } catch(e){ showStatus("✗ Verkäufer: "+e.message, false); return; }

  // Kurz warten, damit der Verkäufer-Poll läuft, dann Käufer starten.
  await new Promise(r=>setTimeout(r, 1000));

  // 2. Käufer.
  try {
    showStatus("⏳ Käufer kauft (sperrt SOL)…", true);
    const d = await post("/api/v1/swap/auto/start", {
      role: "buyer",
      sol_key: document.getElementById("as-buyer-solkey").value.trim(),
      fnd_seed: document.getElementById("as-buyer-fndseed").value.trim(),
      counterparty_sol: document.getElementById("as-seller-soladdr").value.trim(),
      counterparty_fnd: document.getElementById("as-seller-fndaddr").value.trim(),
      amount_sol: amtSol, amount_fnd: amtFnd,
      secret: document.getElementById("as-secret").value.trim()
    });
    asSwapIds.push({role:"Käufer", id:d.swap_id});
    showStatus("✓ Swap ausgelöst — Ablauf läuft, Status unten", true);
    pollSwaps();
  } catch(e){ showStatus("✗ Käufer: "+e.message, false); }
}

// Pollt den Status aller gestarteten Swaps und zeigt die Phasen.
async function pollSwaps(){
  const box = document.getElementById("as-swaps");
  async function tick(){
    let html = "";
    for(const sw of asSwapIds){
      try {
        const r = await fetch("/api/v1/swap/"+sw.id);
        const d = await r.json();
        html += '<div>'+sw.role+' ('+sw.id.slice(0,8)+'…): <strong style="color:var(--green)">'+(d.phase||"?")+'</strong></div>';
      } catch(e){
        html += '<div>'+sw.role+': (Status nicht abrufbar)</div>';
      }
    }
    box.innerHTML = html;
  }
  tick();
  if(!window._asPoll) window._asPoll = setInterval(tick, 3000);
}
</script>
]])

    render.footer()
end

-- pages/shop.lua
-- FND/SOL-Orderbuch: dezentraler Handel per Limit- oder Markt-Order.
-- Orders werden P2P propagiert; die Abwicklung läuft (später) über HTLC-Swap.
local render = require "render"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("shop.title", "shop")

    local jsStr = cjson.encode({
        buy = t("shop.buy"), sell = t("shop.sell"),
        limit = t("shop.limit"), market = t("shop.market"),
        creating = "…", not_avail = "–",
    }) or "{}"

    ngx.print(string.format([[
<div class="page-wrap">
  <h1>%s</h1>
  <p class="meta">%s</p>

  <!-- Referenzkurs (Info) -->
  <div class="ob-rate-box">
    <span class="meta">%s</span>
    <span id="rate-display" class="value">–</span>
  </div>

  <div class="ob-layout">
    <!-- Order-Formular -->
    <div class="ob-form-card">
      <div class="ob-side-toggle">
        <button type="button" id="side-buy" class="ob-side-btn ob-buy active" onclick="setSide('buy')">%s FND</button>
        <button type="button" id="side-sell" class="ob-side-btn ob-sell" onclick="setSide('sell')">%s FND</button>
      </div>
      <div class="ob-type-toggle">
        <label class="pref-check"><input type="radio" name="ordertype" value="limit" checked onchange="setType('limit')"> %s</label>
        <label class="pref-check"><input type="radio" name="ordertype" value="market" onchange="setType('market')"> %s</label>
      </div>
      <div class="field">
        <label>%s</label>
        <input type="number" id="ord-amount" min="0" step="0.01" placeholder="0.00" oninput="updatePreview()">
      </div>
      <div class="field" id="price-field">
        <label>%s</label>
        <input type="number" id="ord-price" min="0" step="0.00000001" placeholder="0.00000000" oninput="updatePreview()">
      </div>
      <div class="field" style="border-top:1px solid var(--border);padding-top:10px;margin-top:6px">
        <label style="color:var(--text-dim)">Schlüssel hinterlegen — beide nötig: einer zum Zahlen (sperren), einer zum Einlösen des Erhaltenen. Bleiben lokal im RAM, werden nie propagiert.</label>
        <input type="text" id="ord-solkey" placeholder="leer = deine Fundus-Solana-Wallet · sonst Mnemonic/Base58" style="margin-top:6px">
        <input type="text" id="ord-fndseed" placeholder="leer = deine Fundus-Wallet · sonst FND-Seed-Wörter" style="margin-top:6px">
      </div>
      <div id="ord-preview" class="ob-preview"></div>
      <div id="ord-status" class="status-line"></div>
      <button class="btn" id="ord-submit" onclick="submitOrder()">%s</button>
    </div>

    <!-- Orderbuch -->
    <div class="ob-book-card">
      <h3>%s</h3>
      <div class="ob-book">
        <div class="ob-book-col">
          <div class="ob-book-head ob-sell-head">%s</div>
          <div id="ob-asks"></div>
        </div>
        <div class="ob-book-col">
          <div class="ob-book-head ob-buy-head">%s</div>
          <div id="ob-bids"></div>
        </div>
      </div>
    </div>
  </div>

  <!-- Eigene Orders -->
  <div class="ob-mine-card">
    <h3>%s</h3>
    <div id="ob-mine"></div>
  </div>
</div>

<script>const OB = %s;</script>
<script>
let currentSide = "buy";
let currentType = "limit";

function setSide(s){
  currentSide = s;
  document.getElementById("side-buy").classList.toggle("active", s==="buy");
  document.getElementById("side-sell").classList.toggle("active", s==="sell");
  const btn = document.getElementById("ord-submit");
  btn.textContent = (s==="buy"? OB.buy : OB.sell) + " FND";
  btn.className = "btn " + (s==="buy" ? "ob-buy-btn" : "ob-sell-btn");
  // Beide Key-Felder bleiben sichtbar: der Maker braucht einen zum Zahlen
  // (Give-Chain sperren) und einen zum Einlösen des Erhaltenen (Receive-Chain).
  updatePreview();
}
function setType(tp){
  currentType = tp;
  document.getElementById("price-field").style.display = (tp==="market") ? "none" : "";
  updatePreview();
}

// Referenzkurs (SOL/EUR) anzeigen.
async function loadRate(){
  try {
    const r = await fetch("/api/v1/shop/price");
    if (!r.ok) return;
    const d = await r.json();
    if (d.sol_eur) document.getElementById("rate-display").textContent = "1 SOL = " + d.sol_eur.toFixed(2) + " EUR";
  } catch(e){}
}

// Das gesamte Netz-Orderbuch laden und darstellen.
let bookCache = { asks: [], bids: [] };
async function loadBook(){
  try {
    const r = await fetch("/api/v1/orders/book");
    const d = await r.json();
    // Alle Orders aus allen Peers + Push + eigene zusammenführen — mit
    // Deduplizierung nach ID (eine Order kann über Poll UND Push reinkommen).
    let all = [];
    const seen = {};
    function addOrder(o, extra){
      if (!o || !o.id) return;
      if (seen[o.id]) return; // schon vorhanden → überspringen
      seen[o.id] = true;
      Object.assign(o, extra||{});
      all.push(o);
    }
    // Orders aus einem ANDEREN Solana-Netz (z.B. Devnet-Tests) ausblenden –
    // sie wären hier nicht annehmbar. Ohne Vermerk (vor R486): anzeigen.
    const myNet = d.sol_net || "";
    (d.peers||[]).forEach(function(p){
      let orders = [];
      try { orders = (typeof p.orders === "string") ? JSON.parse(p.orders) : p.orders; } catch(e){}
      (orders||[]).forEach(function(o){
        if (o && o.sol_net && myNet && o.sol_net !== myNet) return;
        addOrder(o, {_peer: p.peer_id});
      });
    });
    (d.mine||[]).forEach(function(o){ addOrder(o, {_mine: true}); });
    // Asks = Verkaufsorders (jemand verkauft FND, ich kann kaufen), aufsteigend nach Preis.
    // Bids = Kauforders (jemand kauft FND), absteigend nach Preis.
    const asks = all.filter(o => o.side==="sell" && o.type==="limit").sort((a,b)=>a.price_sol-b.price_sol);
    const bids = all.filter(o => o.side==="buy"  && o.type==="limit").sort((a,b)=>b.price_sol-a.price_sol);
    bookCache = { asks: asks, bids: bids };
    renderBook();
    updatePreview();
  } catch(e){}
}

function renderBook(){
  document.getElementById("ob-asks").innerHTML = bookCache.asks.map(rowHtml).join("") || emptyRow();
  document.getElementById("ob-bids").innerHTML = bookCache.bids.map(rowHtml).join("") || emptyRow();
}
function rowHtml(o){
  const clickable = !o._mine;
  const attrs = clickable ? ' data-order="'+encodeURIComponent(JSON.stringify(o))+'" style="cursor:pointer" onclick="takeFromRow(this)"' : '';
  return '<div class="ob-row'+(o._mine?' ob-own':'')+'"'+attrs+'>'+
    '<span class="ob-price">'+Number(o.price_sol).toFixed(8)+'</span>'+
    '<span class="ob-amt">'+Number(o.amount_fnd).toFixed(2)+'</span></div>';
}
function emptyRow(){ return '<div class="ob-row ob-empty">–</div>'; }

function takeFromRow(el){
  try { openTakeDialog(decodeURIComponent(el.getAttribute("data-order"))); } catch(e){}
}

// openTakeDialog: Order annehmen (Taker). Fragt die Taker-Keys ab und löst
// /swap/buy aus. Der Node kontaktiert den Maker-Node und startet den Swap.
function openTakeDialog(oStr){
  let o; try { o = JSON.parse(oStr); } catch(e){ return; }
  const takerGivesSol = (o.side === "sell");
  const total = (o.amount_fnd*o.price_sol).toFixed(8);
  let fields;
  if (takerGivesSol){
    fields =
      '<div class="field"><label>Menge FND (max '+Number(o.amount_fnd).toFixed(2)+')</label>'+
      '<input type="number" id="take-amount" step="0.01" value="'+Number(o.amount_fnd).toFixed(2)+'" max="'+o.amount_fnd+'"></div>'+
      '<div class="field"><label>Dein Solana-Schlüssel (zum Zahlen der SOL)</label>'+
      '<input type="text" id="take-solkey" placeholder="leer = deine Fundus-Solana-Wallet"></div>'+
      '<div class="field"><label>Dein FND-Seed (zum Einlösen der gekauften FND)</label>'+
      '<input type="text" id="take-fndseed" placeholder="leer = deine Fundus-Wallet"></div>';
  } else {
    fields =
      '<div class="field"><label>Menge FND (max '+Number(o.amount_fnd).toFixed(2)+')</label>'+
      '<input type="number" id="take-amount" step="0.01" value="'+Number(o.amount_fnd).toFixed(2)+'" max="'+o.amount_fnd+'"></div>'+
      '<div class="field"><label>Dein FND-Seed (zum Zahlen der FND)</label>'+
      '<input type="text" id="take-fndseed" placeholder="leer = deine Fundus-Wallet"></div>'+
      '<div class="field"><label>Dein Solana-Schlüssel (zum Einlösen der gekauften SOL)</label>'+
      '<input type="text" id="take-solkey" placeholder="leer = deine Fundus-Solana-Wallet"></div>';
  }
  const ov = document.createElement("div");
  ov.className = "mp-overlay";
  ov.onclick = function(e){ if(e.target===ov) ov.remove(); };
  const card = document.createElement("div");
  card.className = "mp-card";
  card.style.maxWidth = "440px";
  card.innerHTML =
    '<div class="mp-body">'+
    '<div class="mp-name">Order annehmen</div>'+
    '<div class="mp-sub">'+(takerGivesSol?"Du kaufst ":"Du verkaufst ")+Number(o.amount_fnd).toFixed(2)+' FND</div>'+
    '<div class="mp-meta">Preis: '+Number(o.price_sol).toFixed(8)+' SOL/FND · Gesamt: '+total+' SOL</div>'+
    fields+
    '<div id="take-status" class="meta" style="margin-top:8px"></div>';
  const closeBtn = document.createElement("button");
  closeBtn.className = "mp-close"; closeBtn.textContent = "✕";
  closeBtn.onclick = function(){ ov.remove(); };
  const goBtn = document.createElement("button");
  goBtn.className = "btn ob-buy-btn";
  goBtn.style.width = "100%%";
  goBtn.style.marginTop = "10px";
  goBtn.textContent = "Swap starten";
  goBtn.onclick = function(){ doTake(o.id, takerGivesSol, goBtn, ov); };
  card.querySelector(".mp-body").appendChild(goBtn);
  card.appendChild(closeBtn);
  ov.appendChild(card);
  document.body.appendChild(ov);
}

// Leeres Schlüsselfeld (SOL oder FND) + angemeldet → Wallet der Anmeldung ("session").
function solKeyOrSession(el){
  const v = el ? el.value.trim() : "";
  if (v) return v;
  return (window.WALLET && window.WALLET.fundusID) ? "session" : "";
}

// Statuszeile unter dem Swap-Knopf: vollständige Meldung (bricht um) statt
// abgeschnittenem Text im Knopf. err=true → rot.
function swapMsg(btn, text, err){
  let st = btn.parentNode ? btn.parentNode.querySelector(".swap-status") : null;
  if (!st && btn.parentNode){
    st = document.createElement("div");
    st.className = "swap-status";
    btn.parentNode.insertBefore(st, btn.nextSibling);
  }
  if (!st) return;
  st.textContent = text || "";
  st.classList.toggle("err", !!err);
}

async function doTake(orderID, takerGivesSol, btn, ov){
  const body = { order_id: orderID };
  const amtEl = document.getElementById("take-amount");
  if (amtEl) body.amount_fnd = parseFloat(amtEl.value)||0;
  const skEl = document.getElementById("take-solkey");
  const fsEl = document.getElementById("take-fndseed");
  if (skEl) body.sol_key = solKeyOrSession(skEl);
  if (fsEl) body.fnd_seed = solKeyOrSession(fsEl); // leer + angemeldet → Wallet der Anmeldung
  btn.disabled = true;
  btn.textContent = "⏳ Kontaktiere Gegenseite…";
  swapMsg(btn, "", false);
  try {
    const r = await fetch("/api/v1/swap/buy", {
      method:"POST", headers:{"Content-Type":"application/json"},
      body: JSON.stringify(body)
    });
    const d = await r.json();
    if(!r.ok || d.error) throw new Error(d.error||"Fehler");
    btn.textContent = "⏳ Swap gestartet…";
    if (d.swap_id) pollSwapStatus(d.swap_id, btn, ov);
    else finishBtn(btn, ov, true);
  } catch(e){
    btn.disabled = false;
    btn.textContent = "Erneut versuchen";
    swapMsg(btn, "✗ " + e.message, true);
  }
}

function finishBtn(btn, ov, ok){
  btn.disabled = false;
  btn.textContent = ok ? "✓ Fertig — Schließen" : "Schließen";
  btn.onclick = function(){ if(ov) ov.remove(); };
}

function pollSwapStatus(swapID, btn, ov){
  const phaseNames = {
    initiated:"gestartet", sol_locked:"SOL gesperrt", fnd_locked:"FND gesperrt",
    fnd_claimed:"eingelöst…", sol_claimed:"abgeschlossen", refunded:"zurückerstattet",
    expired:"abgelaufen",
    discarded: "verworfen (Chain-Neustart)"
  };
  const iv = setInterval(async function(){
    try {
      const r = await fetch("/api/v1/swap/"+swapID);
      if(!r.ok){ return; }
      const d = await r.json();
      const p = d.phase||"?";
      const label = phaseNames[p]||p;
      if (p==="sol_claimed" || p==="fnd_claimed"){
        clearInterval(iv); finishBtn(btn, ov, true); loadBook(); loadMine();
      } else if (p==="refunded" || p==="expired" || p==="discarded"){
        clearInterval(iv);
        btn.disabled = false;
        btn.textContent = "✗ "+label+" — Schließen";
        btn.onclick = function(){ if(ov) ov.remove(); };
        swapMsg(btn, d.note || "", true);
      } else {
        btn.textContent = "⏳ Swap läuft: "+label+"…";
        // Detailmeldung des Nodes (Netz, beobachtetes Konto, RPC-Fehler …)
        swapMsg(btn, (d.note || "") + (d.sol_lock_sig ? " · SOL-Signatur " + String(d.sol_lock_sig).slice(0,16) + "…" : ""), false);
      }
    } catch(e){}
  }, 3000);
  setTimeout(function(){ clearInterval(iv); finishBtn(btn, ov, false); }, 300000);
}
// Vorschau: Beim Markt-Kauf/Limit über mehrere Orders hinweg berechnen, welche
// Orders zu welchem Durchschnittspreis erfüllt werden.
function updatePreview(){
  const amt = parseFloat(document.getElementById("ord-amount").value)||0;
  const limitPrice = parseFloat(document.getElementById("ord-price").value)||0;
  const prev = document.getElementById("ord-preview");
  if (amt<=0){ prev.innerHTML=""; return; }
  // Gegenseite: Kaufe FND → matche gegen Asks (Verkäufe); Verkaufe FND → gegen Bids.
  let book = (currentSide==="buy") ? bookCache.asks : bookCache.bids.slice();
  // Bei Limit nur Orders, die den Preis erfüllen.
  if (currentType==="limit" && limitPrice>0){
    book = book.filter(o => currentSide==="buy" ? o.price_sol<=limitPrice : o.price_sol>=limitPrice);
  }
  let remaining = amt, filledFnd = 0, totalSol = 0, levels = 0;
  for (const o of book){
    if (remaining<=0) break;
    const take = Math.min(remaining, o.amount_fnd);
    filledFnd += take; totalSol += take*o.price_sol; remaining -= take; levels++;
  }
  if (filledFnd<=0){
    prev.innerHTML = '<span class="meta">Keine passenden Orders im Buch — deine Order wird ins Buch gestellt.</span>';
    return;
  }
  const avg = totalSol/filledFnd;
  let html = '<div class="ob-preview-line"><span>Erfüllbar:</span><strong>'+filledFnd.toFixed(2)+' FND über '+levels+' Order(s)</strong></div>';
  html += '<div class="ob-preview-line"><span>Ø-Preis:</span><strong>'+avg.toFixed(8)+' SOL/FND</strong></div>';
  html += '<div class="ob-preview-line"><span>Gesamt:</span><strong>'+totalSol.toFixed(8)+' SOL</strong></div>';
  if (remaining>0.0001) html += '<div class="ob-preview-line meta">Rest '+remaining.toFixed(2)+' FND '+(currentType==="market"?"nicht erfüllbar":"wird ins Buch gestellt")+'</div>';
  prev.innerHTML = html;
}

async function submitOrder(){
  const amt = parseFloat(document.getElementById("ord-amount").value)||0;
  const price = parseFloat(document.getElementById("ord-price").value)||0;
  let soladdr = "";
  let fndaddr = "";
  const st = document.getElementById("ord-status");
  if (amt<=0){ st.textContent="Bitte eine Menge eingeben."; st.style.color="var(--red)"; return; }
  if (currentType==="limit" && price<=0){ st.textContent="Limit-Order braucht einen Preis."; st.style.color="var(--red)"; return; }
  // Wenn Keys hinterlegt werden: Adressen daraus ableiten, damit Order-Adresse
  // und Signier-Adresse GARANTIERT übereinstimmen (sonst findet die Gegenseite
  // den Lock nicht). Die abgeleitete Adresse überschreibt die manuelle Eingabe.
  const solkeyPre = document.getElementById("ord-solkey");
  const fndseedPre = document.getElementById("ord-fndseed");
  const skv = solKeyOrSession(solkeyPre);
  const fsv = solKeyOrSession(fndseedPre);
  if (skv || fsv){
    st.textContent = "⏳ Leite Adressen aus Schlüsseln ab…"; st.style.color="var(--muted)";
    try {
      const dr = await fetch("/api/v1/swap/derive-addrs", {
        method:"POST", headers:{"Content-Type":"application/json"},
        body: JSON.stringify({ sol_key: skv, fnd_seed: fsv })
      });
      const da = await dr.json();
      if (da.sol_address) soladdr = da.sol_address;
      if (da.fnd_address) fndaddr = da.fnd_address;
    } catch(e){}
  }
  st.textContent = "⏳ Erstelle Order…"; st.style.color="var(--muted)";
  try {
    const r = await fetch("/api/v1/orders", {
      method:"POST", headers:{"Content-Type":"application/json"},
      body: JSON.stringify({ side: currentSide, type: currentType, amount_fnd: amt, price_sol: price, sol_address: soladdr, fnd_address: fndaddr })
    });
    let d;
    try { d = await r.json(); }
    catch(e){ throw new Error("Server-Antwort ungültig (Endpunkt evtl. nicht verfügbar — Node-Version prüfen)"); }
    if (r.ok && d.ok){
      // Keys hinterlegen (nur das sichtbare/relevante Feld). Gebunden an die
      // Order-ID, damit der Node autonom swappen kann, wenn jemand die Order annimmt.
      const skEl = document.getElementById("ord-solkey");
      const fsEl = document.getElementById("ord-fndseed");
      const solkey = solKeyOrSession(skEl);
      const fndseed = solKeyOrSession(fsEl);
      let depMsg = "";
      if (d.id && (solkey || fndseed)){
        st.textContent = "⏳ Hinterlege Schlüssel…"; st.style.color="var(--muted)";
        try {
          const dep = await fetch("/api/v1/swap/deposit", {
            method:"POST", headers:{"Content-Type":"application/json"},
            body: JSON.stringify({ order_id: d.id, sol_key: solkey, fnd_seed: fndseed,
              amount_sol: amt*(price||0), amount_fnd: amt })
          });
          const dd = await dep.json();
          if (dep.ok && dd.ok){ depMsg = " + Schlüssel hinterlegt"; }
          else { depMsg = " ⚠ Schlüssel-Hinterlegung fehlgeschlagen: "+(dd.error||"?"); }
        } catch(e){ depMsg = " ⚠ Schlüssel-Hinterlegung fehlgeschlagen"; }
        if (skEl) skEl.value=""; if (fsEl) fsEl.value="";
      }
      st.innerHTML = '<span style="color:var(--grn)">✓ Order platziert'+depMsg+'</span>';
      document.getElementById("ord-amount").value="";
      loadBook(); loadMine();
    } else {
      st.textContent = "✗ "+(d.error||"Fehler"); st.style.color="var(--red)";
    }
  } catch(e){ st.textContent="✗ "+e.message; st.style.color="var(--red)"; }
}

async function loadMine(){
  try {
    const r = await fetch("/api/v1/orders/mine");
    const d = await r.json();
    const box = document.getElementById("ob-mine");
    const orders = d.orders||[];
    if (!orders.length){ box.innerHTML='<div class="meta">Keine offenen Orders.</div>'; return; }
    const myNet = d.sol_net || "";
    box.innerHTML = orders.map(function(o){
      // Netz-Kennzeichen: fremdes Netz oder unbekannt (vor R486) → sichtbar
      // machen, damit veraltete Test-Orders erkannt und storniert werden.
      let net = "";
      if (o.sol_net && myNet && o.sol_net !== myNet) net = ' <span class="ob-net-tag">'+o.sol_net+'</span>';
      else if (!o.sol_net) net = ' <span class="ob-net-tag" title="vor R486 angelegt – Solana-Netz unbekannt">Netz ?</span>';
      return '<div class="ob-mine-row">'+
        '<span class="'+(o.side==="buy"?"ob-buy-head":"ob-sell-head")+'">'+(o.side==="buy"?OB.buy:OB.sell)+net+'</span>'+
        '<span class="ob-id" title="Order-ID">'+String(o.id||"").slice(0,8)+'</span>'+
        '<span>'+Number(o.amount_fnd).toFixed(2)+' FND</span>'+
        '<span>'+(o.type==="market"?OB.market:Number(o.price_sol).toFixed(8)+" SOL")+'</span>'+
        '<button class="btn-sm btn-danger" onclick="cancelOrder(\''+o.id+'\')">✕</button></div>';
    }).join("");
  } catch(e){}
}

async function cancelOrder(id){
  try { await fetch("/api/v1/orders/"+id, {method:"DELETE"}); loadBook(); loadMine(); } catch(e){}
}

// Initial + Auto-Refresh (das Backend pusht in Echtzeit; UI aktualisiert alle 5s).
setSide("buy");
loadRate(); loadBook(); loadMine();
setInterval(function(){ loadBook(); loadMine(); }, 5000);
setInterval(loadRate, 30000); // Kurs alle 30s aktualisieren
</script>
]],
        t("shop.title"), t("shop.subtitle"),
        t("shop.reference_rate"),
        t("shop.buy"), t("shop.sell"),
        t("shop.limit"), t("shop.market"),
        t("shop.amount_fnd"), t("shop.price_sol"),
        t("shop.place_order"),
        t("shop.orderbook"), t("shop.asks"), t("shop.bids"),
        t("shop.my_orders"),
        jsStr))

    -- ── Freiwillige Spende (PayPal) ─────────────────────────────────────────
    -- Empfänger aus der Node-Konfiguration (FUNDUS_DONATE_PAYPAL). Kennt das
    -- laufende Binary den Endpunkt noch nicht (vor R430, nur Frontend
    -- aktualisiert), gilt der Projekt-Standard. Ausgeblendet wird die Karte nur,
    -- wenn der Node ausdrücklich "off" meldet.
    local don = render.api_get("/v1/donate/config")
    local paypal, currency = nil, "EUR"
    if don then
        if don.enabled and don.paypal and don.paypal ~= "" then
            paypal = don.paypal; currency = don.currency or "EUR"
        end
    else
        paypal = "tobias.kornmayer@gmail.com"
    end
    if paypal then
        local don = { paypal = paypal }
        local base = "https://www.paypal.com/donate/?business=" .. ngx.escape_uri(paypal)
            .. "&currency_code=" .. ngx.escape_uri(currency)
            .. "&item_name=" .. ngx.escape_uri("Unterstuetzung FUNDUS")
        ngx.print([[
<div class="page-wrap">
  <div class="card donate-card" id="spenden">
    <h3>💚 FUNDUS unterstützen</h3>
    <p class="meta">Fundus ist ein freies Projekt ohne Werbung und ohne zentrale Server. Mit einer
      freiwilligen Spende hilfst du bei Entwicklung und Betrieb. Die Spende ist <b>ohne Gegenleistung</b> –
      FND erhältst du über den Tausch FND ⇄ SOL oben im Orderbuch.</p>
    <div class="donate-row">
      <button type="button" class="btn donate-amt" data-amt="5">5 €</button>
      <button type="button" class="btn donate-amt" data-amt="10">10 €</button>
      <button type="button" class="btn donate-amt" data-amt="25">25 €</button>
      <button type="button" class="btn donate-amt" data-amt="50">50 €</button>
      <input type="number" id="donate-amount" min="1" step="1" placeholder="Betrag €" inputmode="decimal">
    </div>
    <a id="donate-link" class="btn btn-paypal" target="_blank" rel="noopener noreferrer"
       href="]] .. render.html_escape(base) .. [[">Mit PayPal spenden</a>
    <p class="meta" style="margin-top:8px">Empfänger: ]] .. render.html_escape(don.paypal) .. [[ · Zahlung direkt bei PayPal, Fundus sieht keine Zahlungsdaten.</p>
  </div>
</div>
<script>
(function(){
  var base = document.getElementById('donate-link').getAttribute('href');
  var inp = document.getElementById('donate-amount');
  function apply(v){
    var a = Math.round(parseFloat(v) || 0);
    document.getElementById('donate-link').href = a > 0 ? base + '&amount=' + a : base;
  }
  document.querySelectorAll('.donate-amt').forEach(function(b){
    b.onclick = function(){ inp.value = b.getAttribute('data-amt'); apply(inp.value);
      document.querySelectorAll('.donate-amt').forEach(function(x){ x.classList.toggle('active', x === b); }); };
  });
  inp.oninput = function(){ apply(inp.value);
    document.querySelectorAll('.donate-amt').forEach(function(x){ x.classList.remove('active'); }); };
})();
</script>
]])
    end

    render.footer()
end

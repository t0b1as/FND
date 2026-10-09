-- pages/listing_show.lua
local render = require "render"

return function(captures)
    local id = captures and captures[1] or ""
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("listing.detail_title", "listings")

    if id == "" then
        ngx.print("<p class='error'>" .. t("general.not_found") .. "</p>")
        render.footer(); return
    end

    local record, err = render.api_get("/v1/listings/" .. id)
    if err or not record then
        ngx.print("<p class='error'>" .. t("general.not_found") .. ": " .. (err or id) .. "</p>")
        render.footer(); return
    end

    local data = record.data or {}

    -- Eigentümer-Status bestimmen: nur der Ersteller darf bearbeiten/loeschen.
    local status_data = render.api_get("/v1/status")
    local myNodeID = status_data and status_data.node_id or ""
    -- editable kommt von der API (lokaler Owner ODER unsignierter Altbestand)
    local isOwner = (record.editable == true) or
        (record.owner_id ~= nil and record.owner_id ~= "" and record.owner_id == myNodeID)

    -- Zustand lokalisieren
    local condMap = {
        neuwertig  = t("listing.condition.new"),
        gut        = t("listing.condition.good"),
        akzeptabel = t("listing.condition.acceptable"),
        defekt     = t("listing.condition.broken"),
        new        = t("listing.condition.new"),
        good       = t("listing.condition.good"),
        acceptable = t("listing.condition.acceptable"),
        broken     = t("listing.condition.broken"),
    }
    local condition = condMap[tostring(data.condition or ""):lower()]
                   or tostring(data.condition or "–")

    -- Kategorie lokalisieren
    local catMap = {
        elektronik   = t("listing.category.electronics"),
        electronics  = t("listing.category.electronics"),
        ["möbel"]    = t("listing.category.furniture"),
        furniture    = t("listing.category.furniture"),
        kleidung     = t("listing.category.clothing"),
        clothing     = t("listing.category.clothing"),
        fahrzeug     = t("listing.category.vehicle"),
        vehicle      = t("listing.category.vehicle"),
        werkzeug     = t("listing.category.tools"),
        tools        = t("listing.category.tools"),
        haushalt     = t("listing.category.household"),
        household    = t("listing.category.household"),
        sport        = t("listing.category.sports"),
        sports       = t("listing.category.sports"),
        sonstiges    = t("listing.category.other"),
        other        = t("listing.category.other"),
    }
    local category = catMap[tostring(data.category or ""):lower()]
                  or tostring(data.category or "–")

    -- Preisspanne formatieren
    local price_min = tonumber(data.price_min) or 0
    local price_max = tonumber(data.price_max) or 0
    local priceStr
    if price_max > price_min then
        priceStr = t("listing.price_range", {
            min = string.format("%.2f", price_min),
            max = string.format("%.2f", price_max),
        })
    elseif price_min > 0 then
        priceStr = string.format("%.2f FND", price_min)
    else
        priceStr = "–"
    end

    -- Übergabe: Selbstabholung oder Versand (mit Kosten).
    local deliveryStr
    if tostring(data.delivery or "") == "shipping" then
        local sc = tonumber(data.shipping_cost) or 0
        if sc > 0 then
            deliveryStr = t("upload.delivery_shipping") .. string.format(" (%.2f FND)", sc)
        else
            deliveryStr = t("upload.delivery_shipping")
        end
    else
        deliveryStr = t("upload.delivery_pickup")
    end

    -- Keywords
    local keywords = ""
    if type(data.keywords) == "table" then
        keywords = table.concat(data.keywords, ", ")
    elseif type(data.keywords) == "string" then
        keywords = data.keywords
    end

    -- Einheitliche Medien-Galerie: alle Bilder UND Videos als gleichwertige
    -- Kacheln. Bilder-Thumbnails (data.images, Base64) sofort; Vollbilder
    -- (data.image_hashes) werden per JS nachgeladen. Videos als kleine Kachel,
    -- die per Mouseover vergrößert und bei Klick abgespielt wird.
    local thumbs = type(data.images) == "table" and data.images or {}
    local hashes = type(data.image_hashes) == "table" and data.image_hashes or {}
    local videos = {}
    if type(data.video_hash) == "string" and data.video_hash ~= "" then
        videos[#videos+1] = data.video_hash
    end
    if type(data.video_hashes) == "table" then
        for _, vh in ipairs(data.video_hashes) do
            if type(vh) == "string" and vh ~= "" then videos[#videos+1] = vh end
        end
    end

    local tiles = {}
    local nImg = math.max(#thumbs, #hashes)
    for i = 1, nImg do
        local thumb = thumbs[i]
        local hash  = hashes[i]
        local src = (type(thumb) == "string" and thumb:match("^data:image/")) and thumb or ""
        if type(hash) == "string" and hash ~= "" then
            tiles[#tiles+1] = '<div class="media-tile" data-hash="' .. render.html_escape(hash) .. '">' ..
                '<img src="' .. src .. '" class="listing-photo" alt="" onclick="openLightbox(this.src)">' ..
                '<span class="photo-loadhint">🔍</span></div>'
        elseif src ~= "" then
            tiles[#tiles+1] = '<div class="media-tile"><img src="' .. src .. '" class="listing-photo" alt="" onclick="openLightbox(this.src)"></div>'
        end
    end
    for i, vh in ipairs(videos) do
        local vid = "lv-video-" .. i
        tiles[#tiles+1] = '<div class="media-tile media-video" ' ..
            'onmouseenter="lvExpand(this)" onmouseleave="lvCollapse(this)" onclick="lvPlay(this)">' ..
            '<video id="' .. vid .. '" preload="metadata" muted playsinline>' ..
            '<source src="/api/v1/files/download/' .. render.html_escape(vh) .. '" type="video/mp4">' ..
            '</video><span class="lv-play-badge">&#9654;</span></div>'
    end

    local imagesHtml = ""
    if #tiles > 0 then
        imagesHtml = '<div class="listing-gallery" id="listing-gallery">' .. table.concat(tiles) .. '</div>'
    end

    -- Owner sieht Bearbeiten/Loeschen; Fremde sehen den Kaufen-Bereich.
    local ownerActions, buySection
    -- Bestand (R579): quantity ist die VERFÜGBARE Menge; sold_count sind nur
    -- Käufe, die der Verkäufer-Node noch nicht abgezogen hat.
    local hasQty = record.data and record.data.quantity ~= nil
    local qty = tonumber(record.data and record.data.quantity) or 1
    if qty < 0 then qty = 0 end
    local pending = tonumber(record.data and record.data.sold_count) or 0
    local remaining = qty - pending
    if remaining < 0 then remaining = 0 end
    local stockRow = ""
    if remaining > 1 or (hasQty and remaining > 0 and qty > 1) then
        stockRow = string.format('<div class="stock-row">Noch <b>%d</b> verfügbar</div>', remaining)
    end
    local isSold = (remaining <= 0)
    if not hasQty then
        isSold = (record.data and record.data.sold == true) -- Altbestand
    end
    if isOwner then
        ownerActions = string.format(
            '<a href="/listings/%s/edit" class="btn">%s</a>' ..
            '<button class="btn btn-danger" onclick="deleteListing(\'%s\')">%s</button>',
            record.id, t("general.edit"), record.id, t("general.delete"))
        if isSold then
            buySection = '<div class="sold-note">' .. t("listing.sold_note_owner") .. '</div>'
        else
            buySection = stockRow .. '<div class="owner-note">' .. t("listing.own_offer_note") .. '</div>'
        end
    elseif isSold then
        -- Verkauft: kein Kauf mehr möglich, klarer Hinweis.
        ownerActions = ""
        buySection = '<div class="sold-note">' .. t("listing.sold_note") .. '</div>'
    else
        ownerActions = ""
        -- "Verkäufer kontaktieren"-Button nur, wenn der Verkäufer einen
        -- Messenger-Kontaktschlüssel ins Inserat eingebettet hat.
        local contactBtn = ""
        local cpk = record.data and record.data.contact_pub_key
        local cfid = record.data and record.data.contact_fundus_id
        if type(cpk) == "string" and cpk ~= "" then
            -- R609: %q liefert DOPPELTE Anführungszeichen – die beenden das
            -- onclick-Attribut vorzeitig, der Knopf tat deshalb nichts. Werte
            -- jetzt als HTML-Attribute, der Klick liest sie von dort.
            contactBtn = string.format(
                '<button class="btn btn-outline btn-contact-seller" ' ..
                'data-cfid="%s" data-cpk="%s" ' ..
                'onclick="contactSeller(this.dataset.cfid, this.dataset.cpk)">💬 %s</button>',
                render.html_escape(cfid or ""), render.html_escape(cpk),
                t("listing.contact_seller"))
        end
        -- Verkäufer-Empfangsadresse (Wallet) anzeigen, falls im Inserat gesetzt.
        local sellerWallet = record.data and record.data.seller_wallet
        -- Verkäuferkarte (R552): Bild + Name (aus der Ersteller-ID), "Angebot seit", Teilen.
        local creatorFid = tostring(record.data and record.data.creator_fid or "")
        local createdAt  = tostring(record.created_at or "")
        local walletRow = '<div class="seller-card" id="seller-card" data-fid="' .. render.html_escape(creatorFid) ..
            '" data-created="' .. render.html_escape(createdAt) .. '">' ..
            '<div class="sc-av" id="sc-av">' .. require("icons").svg("partner") .. '</div>' ..
            '<div class="sc-body"><div class="sc-name" id="sc-name">Verkäufer</div><div class="sc-sub" id="sc-sub"></div></div>' ..
            '<button type="button" class="btn-sm" onclick="shareListing()" title="Angebot teilen">Teilen</button>' ..
            '<button type="button" class="btn-sm" onclick="listingQR()" title="Als QR-Code zeigen">▦</button></div>'
        if type(sellerWallet) == "string" and sellerWallet ~= "" then
            walletRow = walletRow .. string.format(
                '<div class="seller-wallet-row" style="margin:8px 0;font-size:12px;color:var(--muted)">'..
                '%s: <code style="font-size:11px">%s</code> '..
                '<button class="btn-sm" style="padding:2px 8px;font-size:11px" onclick="copyWallet(\'%s\')">📋</button></div>',
                t("listing.seller_wallet"), sellerWallet, sellerWallet)
            -- Bewertungen des Verkäufers (auf diesem Node gegen die Chain geprüft)
            walletRow = walletRow .. string.format(
                '<div id="seller-rating" data-addr="%s" style="margin:4px 0 8px;font-size:13px"></div>', sellerWallet)
        end
        -- Versand-Erkennung: Bei Versand ein Adressfeld anzeigen und den
        -- Kaufbutton deaktivieren, bis eine Adresse eingegeben ist. Die Adresse
        -- wird nach dem Kauf per Messenger an den Verkäufer geschickt.
        local isShipping = tostring(record.data and record.data.delivery or "") == "shipping"
        local shipRow = ""
        local buyDisabled = ""
        if isShipping then
            local scost = tonumber(record.data and record.data.shipping_cost) or 0
            shipRow = string.format([=[
    <div class="deliv-pick">
      <label class="deliv-opt"><input type="radio" name="deliv" value="pickup" checked onchange="onDeliveryChange()"> Selbstabholung</label>
      <label class="deliv-opt"><input type="radio" name="deliv" value="shipping" onchange="onDeliveryChange()"> Versand%s</label>
    </div>
    <div class="ship-address-box" id="ship-box" style="display:none;margin:10px 0">
      <label for="ship-address" style="font-size:13px;font-weight:600;display:block;margin-bottom:4px">Versandadresse</label>
      <textarea id="ship-address" rows="4" placeholder="Name, Straße Hausnummer, PLZ Ort, Land (optional)"
                style="width:100%%;background:var(--bg);border:1px solid var(--border);color:var(--text);border-radius:8px;padding:10px;font-size:13px"
                oninput="onShipAddressChange()"></textarea>
      <div class="ship-check" id="ship-check">Format: Name, Straße Hausnummer, PLZ Ort, Land (optional)</div>
    </div>]=], (scost > 0 and string.format(" (+%.2f FND)", scost) or ""))
        end
        -- contact_pub_key für die Messenger-Benachrichtigung ans Frontend geben.
        local cpkJs = (type(cpk) == "string" and cpk ~= "") and cpk or ""
        local cfidJs = (type(cfid) == "string") and cfid or ""
        -- Mengenauswahl (R569): nur, wenn mehr als ein Stück verfügbar ist.
        local qtyPicker = ""
        if remaining > 1 then
            qtyPicker = string.format(
                '<div class="qty-pick"><label for="buy-qty">Anzahl</label>' ..
                '<input type="number" id="buy-qty" min="1" max="%d" step="1" value="1" data-unit="%s" oninput="updateBuyTotal()">' ..
                '<span class="qty-max">von %d</span>' ..
                '<span class="qty-total" id="buy-total"></span></div>',
                remaining, (price_min > 0 and string.format("%.2f", price_min) or "0"), remaining)
        end
        -- Restbestand über dem Kaufbereich (nur bei mehreren Stück)
        buySection = stockRow .. string.format([==[
  <div class="buy-box" id="buy-box" data-shipping="%s" data-ship="%s" data-cpk="%s" data-cfid="%s">
    <h3>%s</h3>
    <p class="buy-hint">
      FND werden nach Kauf fuer <strong>14 Tage</strong> eingefroren.
      Du kannst in dieser Zeit den Erhalt bestaetigen oder Storno beantragen.
    </p>
    %s
    %s
    %s
    <div class="buy-actions">
      <button class="btn btn-buy" id="buy-btn" %s onclick="startEscrow('%s','%s','%s')">
        🛒 Jetzt kaufen (Escrow)
      </button>
      <a href="/api/v1/contracts/preview?listing=%s" target="_blank"
         class="btn btn-outline btn-contract">
        📄 Kaufvertrag-Vorschau
      </a>
      %s
    </div>
    <div id="escrow-status" class="escrow-status hidden"></div>
  </div>]==],
            (isShipping and "1" or "0"), string.format("%.2f", tonumber(record.data and record.data.shipping_cost) or 0), cpkJs, cfidJs,
            t("listing.buy"), walletRow, shipRow, qtyPicker, buyDisabled,
            record.id, (sellerWallet or ""), (price_min > 0 and string.format("%.2f", price_min) or ""),
            record.id, contactBtn)
    end

    ngx.print(string.format([[
<div class="listing-detail">
  %s
  <div class="detail-meta">
    <span class="badge condition-%s">%s</span>
    <span class="badge category">%s</span>
    <span class="price">%s</span>
  </div>

  <p class="listing-desc">%s</p>

  <div class="detail-grid">
    <div><span class="label">%s</span> <span>%s</span></div>
    <div><span class="label">%s</span> <span class="mono small">%s</span></div>
    <div><span class="label">%s</span> <span>%s</span></div>
    <div><span class="label">%s</span> <span>%s</span></div>
    <div><span class="label">%s</span> <span>%s</span></div>
  </div>

  <div class="detail-actions">
    <a href="/listings" class="btn-secondary">&larr; %s</a>
    %s
  </div>

  %s
</div>

<script>
// Video-Kachel: klein bis Mouseover, dann groß; Klick spielt ab; Verlassen
// verkleinert. Nimmt das Kachel-Element (mehrere Videos möglich).
// Hover-Vergrößerung macht jetzt CSS (:hover mit transform:scale) allein — kein
// JS-Klassenwechsel mehr, sonst verschiebt sich das Layout und die Ansicht
// flackert. lvExpand/lvCollapse steuern nur noch die Wiedergabe-Steuerelemente.
function lvExpand(tile) {
  // absichtlich leer: Hover wird rein per CSS behandelt.
}
function lvCollapse(tile) {
  // absichtlich leer: die Wiedergabe läuft jetzt in einer echten Lightbox.
}
function lvPlay(tile) {
  if (!tile) return;
  const v = tile.querySelector('video');
  if (!v) return;
  const src = v.querySelector('source');
  const url = src ? src.getAttribute('src') : v.src;
  if (url) openListingVideoLightbox(url);
}

// openListingVideoLightbox: Video groß und zentriert im Vordergrund, unmuted,
// in Endlosschleife. Klick außerhalb (oder ×/Escape) schließt und stoppt.
function openListingVideoLightbox(src) {
  const ov = document.createElement("div");
  ov.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.9);display:flex;align-items:center;justify-content:center;z-index:99999;cursor:zoom-out";
  const v = document.createElement("video");
  v.src = src; v.controls = true; v.loop = true; v.muted = false; v.volume = 1.0; v.autoplay = true; v.playsInline = true;
  v.style.cssText = "max-width:90vw;max-height:90vh;border-radius:8px;cursor:default;box-shadow:0 8px 40px rgba(0,0,0,0.6)";
  v.play().catch(()=>{});
  v.addEventListener("click", (e) => e.stopPropagation());
  function closeLB() {
    try { v.pause(); v.removeAttribute("src"); v.load(); } catch(e){}
    ov.remove();
    document.removeEventListener("keydown", onKey);
  }
  const onKey = (e) => { if (e.key === "Escape") closeLB(); };
  const close = document.createElement("button");
  close.textContent = "×";
  close.style.cssText = "position:absolute;top:16px;right:20px;background:rgba(0,0,0,0.6);color:#fff;border:none;font-size:28px;width:44px;height:44px;border-radius:50%%;cursor:pointer;line-height:1;z-index:1";
  close.onclick = (e) => { e.stopPropagation(); closeLB(); };
  ov.appendChild(v); ov.appendChild(close);
  ov.addEventListener("click", closeLB);
  document.addEventListener("keydown", onKey);
  document.body.appendChild(ov);
}
// Verkäufer kontaktieren: zum Messenger mit vorausgefülltem Empfänger (ID + Key).
// Versandadresse: Kaufbutton aktivieren/deaktivieren je nach Eingabe.
// ── Übergabe und Adresse (R570) ─────────────────────────────────────────────
// Erwartet: "Name, Straße Hausnummer, PLZ Ort, Land (optional)"
function parseShipAddress(text) {
  const parts = String(text || '').split(',').map(function(p){ return p.trim(); }).filter(function(p){ return p.length; });
  if (parts.length < 3) return { ok: false, msg: 'Bitte Name, Straße mit Hausnummer und PLZ Ort – jeweils mit Komma getrennt.' };
  const name = parts[0], street = parts[1], city = parts[2], country = parts[3] || '';
  if (name.length < 2 || !/[A-Za-zÄÖÜäöüß]/.test(name)) return { ok: false, msg: 'Der Name sieht nicht vollständig aus.' };
  if (!/\d/.test(street) || street.length < 4) return { ok: false, msg: 'In der Straße fehlt die Hausnummer.' };
  const m = city.match(/^(\d{4,6})\s+(.{2,})$/);
  if (!m) return { ok: false, msg: 'Bitte PLZ und Ort angeben, z.B. „63674 Altenstadt“.' };
  return { ok: true, name: name, street: street, zip: m[1], city: m[2], country: country,
           msg: '✓ ' + name + ' · ' + street + ' · ' + m[1] + ' ' + m[2] + (country ? ' · ' + country : '') };
}
function isShippingChosen() {
  const r = document.querySelector('input[name="deliv"]:checked');
  if (r) return r.value === 'shipping';
  const box = document.getElementById('buy-box');
  return !!(box && box.dataset.shipping === '1' && document.getElementById('ship-address'));
}
function onDeliveryChange() {
  const box = document.getElementById('ship-box');
  if (box) box.style.display = isShippingChosen() ? '' : 'none';
  updateBuyTotal();
  onShipAddressChange();
}
function onShipAddressChange() {
  const ta = document.getElementById('ship-address');
  const btn = document.getElementById('buy-btn');
  const out = document.getElementById('ship-check');
  if (!btn) return;
  if (!isShippingChosen()) { btn.disabled = false; return; }
  const r = ta ? parseShipAddress(ta.value) : { ok: false, msg: '' };
  btn.disabled = !r.ok;
  if (out) {
    out.textContent = (ta && ta.value.trim()) ? r.msg : 'Format: Name, Straße Hausnummer, PLZ Ort, Land (optional)';
    out.className = 'ship-check' + ((ta && ta.value.trim()) ? (r.ok ? ' ok' : ' bad') : '');
  }
}

// sendShippingAddress: schickt die Versandadresse nach dem Kauf per Messenger an
// den Verkäufer (nutzt den contact_pub_key aus dem Inserat).
async function sendShippingAddress(buyBox, listingId, escrowId, address) {
  const cpk = buyBox.dataset.cpk;
  const cfid = buyBox.dataset.cfid;
  if (!cpk) return; // Kein Kontaktschlüssel → keine automatische Nachricht möglich
  const msg = "📦 Kauf abgeschlossen (Escrow " + (escrowId||"").slice(0,12) + "…)\n" +
              "Inserat: " + listingId + "\n\nVersandadresse des Käufers:\n" + address;
  try {
    await fetch('/api/v1/messenger/send', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ recipient_id: cfid, recipient_pub_key: cpk, text: msg })
    });
  } catch(e) { /* Nachricht best-effort; Kauf ist trotzdem gültig */ }
}

function copyWallet(addr) {
  if (navigator.clipboard) { navigator.clipboard.writeText(addr); }
  const el = event && event.target;
  if (el) { const o = el.textContent; el.textContent = '✓'; setTimeout(function(){ el.textContent = o; }, 1200); }
}

function contactSeller(fundusId, pubKey) {
    const u = new URLSearchParams();
    if (fundusId) u.set('to', fundusId);
    if (pubKey)   u.set('key', pubKey);
    window.location.href = '/messenger?' + u.toString();
}
const LS = ]] .. (require("cjson.safe").encode({ creating_escrow = t("listing.creating_escrow"), buy_now = t("listing.buy_now"),
  error_word = t("listing.error_word"), dispute_not_received = t("listing.dispute_not_received"),
  dispute_defective = t("listing.dispute_defective"), dispute_wrong = t("listing.dispute_wrong"),
  dispute_not_described = t("listing.dispute_not_described"), enter_key_prompt = t("listing.enter_key_prompt"),
  must_return = t("listing.must_return"), cancel_requested = t("listing.cancel_requested"),
  tracking_note = t("listing.tracking_note"), tracking_placeholder = t("listing.tracking_placeholder"),
  submit_return = t("listing.submit_return") }) or "{}") .. [[
// Klick auf ein Thumbnail: Vollbild aus dem FileStore laden und in einer
// Lightbox (Overlay) vergroessert anzeigen. Geladene Vollbilder werden
// gecached, damit ein erneuter Klick sofort die Lightbox oeffnet.
const fullCache = {};

function openLightbox(src) {
    let lb = document.getElementById('lightbox');
    if (!lb) {
        lb = document.createElement('div');
        lb.id = 'lightbox';
        lb.className = 'lightbox';
        lb.innerHTML = '<span class="lightbox-close">✕</span>' +
            '<span class="lightbox-zoom" title="Zoom umschalten">⤢</span>' +
            '<img class="lightbox-img" src="">';
        lb.addEventListener('click', (e) => {
            // Klick auf das Bild schaltet die Zoomstufe, daneben schließt.
            if (e.target.classList.contains('lightbox-img')) { toggleLightboxZoom(); return; }
            if (e.target.classList.contains('lightbox-zoom')) { toggleLightboxZoom(); return; }
            lb.classList.remove('open', 'zoomed');
        });
        setupLightboxPan(lb);
        document.body.appendChild(lb);
    }
    lb.classList.remove('zoomed');
    lb.querySelector('.lightbox-img').src = src;
    lb.classList.add('open');
}

// ── Zweite Zoomstufe (R604) ────────────────────────────────────────────────
// Stufe 1: ganzes Bild sichtbar. Stufe 2: auf volle Breite – ein hochkant
// aufgenommenes Bild ragt dann oben und unten hinaus und lässt sich durch
// Bewegen der Maus (bzw. Wischen) senkrecht verschieben.
function toggleLightboxZoom() {
    const lb = document.getElementById('lightbox');
    if (!lb) return;
    lb.classList.toggle('zoomed');
    const img = lb.querySelector('.lightbox-img');
    if (img) img.style.transform = '';
    lb.dataset.pan = '0';
}

function setupLightboxPan(lb) {
    const img = lb.querySelector('.lightbox-img');
    // Verschiebbarer Weg: wie viel das Bild über das Fenster hinausragt.
    function ueberstand() { return Math.max(0, img.offsetHeight - window.innerHeight); }
    function setze(y) {
        const max = ueberstand();
        const v = Math.min(0, Math.max(-max, y));
        lb.dataset.pan = String(v);
        img.style.transform = max > 0 ? 'translateY(' + v + 'px)' : '';
    }
    // Maus: Position im Fenster bestimmt den Bildausschnitt.
    lb.addEventListener('mousemove', (e) => {
        if (!lb.classList.contains('zoomed')) return;
        const max = ueberstand();
        if (max <= 0) return;
        const anteil = Math.min(1, Math.max(0, e.clientY / window.innerHeight));
        setze(-anteil * max);
    });
    // Mausrad und Wischen verschieben ebenfalls.
    lb.addEventListener('wheel', (e) => {
        if (!lb.classList.contains('zoomed')) return;
        e.preventDefault();
        setze((parseFloat(lb.dataset.pan) || 0) - e.deltaY);
    }, { passive: false });
    let y0 = null, p0 = 0;
    lb.addEventListener('touchstart', (e) => {
        if (!lb.classList.contains('zoomed')) return;
        y0 = e.touches[0].clientY; p0 = parseFloat(lb.dataset.pan) || 0;
    }, { passive: true });
    lb.addEventListener('touchmove', (e) => {
        if (!lb.classList.contains('zoomed') || y0 === null) return;
        setze(p0 + (e.touches[0].clientY - y0));
    }, { passive: true });
    lb.addEventListener('touchend', () => { y0 = null; }, { passive: true });
}

// Vollbild aus dem FileStore holen (mit Cache). Gibt die Object-URL zurück
// oder null bei Fehler/Timeout. Wird sowohl vom Auto-Preload als auch vom
// Klick-Handler genutzt.
async function fetchFullImage(hash, wrap) {
    if (fullCache[hash]) return fullCache[hash];
    if (wrap && wrap.classList.contains('loading')) return null;
    if (wrap) wrap.classList.add('loading');
    const hint = wrap ? wrap.querySelector('.photo-loadhint') : null;
    if (hint) hint.textContent = '⏳';
    try {
        // Timeout, damit ein hängender DHT-Lookup die Seite nicht ewig blockiert.
        const ctrl = new AbortController();
        const tmo = setTimeout(() => ctrl.abort(), 20000);
        const r = await fetch('/api/v1/files/download/' + encodeURIComponent(hash), { signal: ctrl.signal });
        clearTimeout(tmo);
        if (!r.ok) throw new Error('not found');
        const blob = await r.blob();
        const url = URL.createObjectURL(blob);
        fullCache[hash] = url;
        if (wrap) {
            const img = wrap.querySelector('.listing-photo');
            if (img) img.src = url;
            wrap.classList.remove('loading');
            wrap.classList.add('full-loaded');
            if (hint) hint.remove();
        }
        return url;
    } catch (e) {
        if (wrap) {
            wrap.classList.remove('loading');
            if (hint) hint.textContent = '🔍';
        }
        return null;
    }
}

document.querySelectorAll('.media-tile:not(.media-video)').forEach((wrap) => {
    // Auto-Preload: Vollbild im Hintergrund laden und das Inline-Thumbnail
    // ersetzen, sobald verfügbar. Scheitert das (kein Peer/Timeout), bleibt
    // das größere Inline-Thumbnail stehen — die Seite sieht trotzdem gut aus.
    const h0 = wrap.getAttribute('data-hash');
    if (h0) fetchFullImage(h0, wrap);

    wrap.addEventListener('click', async () => {
        const hash = wrap.getAttribute('data-hash');
        const img = wrap.querySelector('.listing-photo');
        // Kein Hash (nur Thumbnail): Thumbnail selbst vergroessern
        if (!hash) {
            if (img && img.src) openLightbox(img.src);
            return;
        }
        // Vollbild bereits geladen → direkt Lightbox
        if (fullCache[hash]) { openLightbox(fullCache[hash]); return; }
        // Sonst laden und dann Lightbox; Fallback aufs Thumbnail
        const url = await fetchFullImage(hash, wrap);
        openLightbox(url || (img && img.src) || '');
    });
});
async function deleteListing(id) {
    if (!confirm('%s')) return;
    const resp = await fetch('/api/v1/listings/' + id, { method: 'DELETE' });
    if (resp.ok || resp.status === 204) {
        location.href = '/listings';
    } else {
        alert('Fehler beim Löschen');
    }
}

// Gewählte Stückzahl (1, wenn es keine Auswahl gibt)
function buyQty() {
    const el = document.getElementById('buy-qty');
    const n = el ? parseInt(el.value, 10) : 1;
    const max = el ? parseInt(el.max, 10) || 1 : 1;
    return Math.min(Math.max(1, isNaN(n) ? 1 : n), max);
}
// Nach einem Kauf: Restmenge, Auswahl und Gesamtpreis auf der Seite anpassen.
function updateStockAfterPurchase(bought) {
    const row = document.querySelector('.stock-row');
    const qtyEl = document.getElementById('buy-qty');
    let left = null;
    if (qtyEl) {
        left = Math.max(0, (parseInt(qtyEl.max, 10) || 0) - bought);
        qtyEl.max = String(left);
        qtyEl.value = String(Math.min(parseInt(qtyEl.value, 10) || 1, Math.max(1, left)));
        const maxLbl = document.querySelector('.qty-max');
        if (maxLbl) maxLbl.textContent = 'von ' + left;
        if (left <= 1) { const p = document.querySelector('.qty-pick'); if (p) p.style.display = 'none'; }
    } else if (row) {
        const m = row.textContent.match(/(\d+)/);
        if (m) left = Math.max(0, parseInt(m[1], 10) - bought);
    }
    if (row && left !== null) {
        row.innerHTML = left > 0 ? 'Noch <b>' + left + '</b> verfügbar' : '<b>Ausverkauft</b>';
    }
    if (left === 0) {
        const btn = document.getElementById('buy-btn');
        if (btn) { btn.disabled = true; btn.textContent = 'Ausverkauft'; }
    }
    updateBuyTotal();
}
function shipCost() {
    const b = document.getElementById('buy-box');
    return parseFloat((b && b.dataset.ship) || '0') || 0;
}
function updateBuyTotal() {
    const el = document.getElementById('buy-qty');
    const out = document.getElementById('buy-total');
    if (!el || !out) return;
    el.value = buyQty();
    const unit = parseFloat(el.getAttribute('data-unit') || '0');
    if (unit <= 0) return;
    const total = unit * buyQty() + (isShippingChosen() ? shipCost() : 0);
    out.textContent = '= ' + total.toFixed(2).replace('.', ',') + ' FND';
}
async function startEscrow(listingId, sellerWallet, amountFnd) {
    // Ohne Verkäufer-Empfangsadresse kein Direktkauf möglich.
    if (!sellerWallet) {
        alert(LS.no_seller_wallet || 'Dieses Inserat hat keine Empfangsadresse. Bitte den Verkäufer kontaktieren.');
        return;
    }
    // Bei Versand: Versandadresse muss ausgefüllt sein.
    const buyBox = document.getElementById('buy-box');
    const isShipping = isShippingChosen();
    // Gesamtbetrag: Stückpreis × Anzahl + ggf. Versand (R579) – vorher stand in
    // der Bestätigung nur der Einzelpreis.
    const totalFnd = amountFnd
        ? (parseFloat(amountFnd) * buyQty() + (isShipping ? shipCost() : 0)).toFixed(2)
        : '';
    let shipAddress = '';
    if (isShipping) {
        const ta = document.getElementById('ship-address');
        shipAddress = ta ? ta.value.trim() : '';
        const chk = parseShipAddress(shipAddress);
        if (!chk.ok) {
            alert(chk.msg || 'Bitte gib deine Versandadresse ein.');
            return;
        }
    }
    // Käufer gibt seine Schlüssel-Wörter ein, um direkt zu bezahlen.
    const seed = prompt(LS.enter_key || 'Gib deine Wallet-Wörter ein, um zu bezahlen (12 oder mehr Wörter, mit Leerzeichen getrennt):');
    if (!seed) return;
    const words = seed.trim().split(/\s+/);
    if (words.length < 12) {
        alert(LS.key_too_short || 'Zu wenige Wörter — bitte die vollständige Wortliste eingeben.');
        return;
    }
    const btn = document.querySelector('.btn-buy');
    btn.disabled = true;
    btn.textContent = LS.creating_escrow;

    const st = document.getElementById('escrow-status');
    st.className = 'escrow-status';

    try {
        const r = await fetch('/api/v1/escrow', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                listing_id: listingId,
                seller: sellerWallet,
                words: words,
                quantity: buyQty(),
                delivery: isShipping ? 'shipping' : 'pickup',
                // als Zeichenkette: der Node erwartet amount_fnd als Text
                amount_fnd: totalFnd || undefined
            }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || LS.error_word);

        // Bei Versand: Adresse per Messenger an den Verkäufer schicken.
        if (isShipping && shipAddress) {
            await sendShippingAddress(buyBox, listingId, d.escrow_id, shipAddress);
        }

        const escrowId = d.escrow_id;
        // Bestand auf der Seite sofort nachführen (R580) – vorher musste man
        // neu laden, um die verringerte Menge zu sehen.
        updateStockAfterPurchase(buyQty());
        // Kauf ist mit den Wörtern bereits bezahlt (Escrow eröffnet + eingefroren).
        // Kein separater "Jetzt bezahlen"-Schritt mehr nötig.
        st.innerHTML = `
          <div class="success-box">
            <strong>✓ Kauf abgeschlossen — ${totalFnd || amountFnd || ''} FND eingefroren${buyQty() > 1 ? ' (' + buyQty() + ' Stück)' : ''} (Escrow ${(escrowId||'').slice(0,12)}…)</strong>
            <p style="margin-top:6px">Die FND sind für 14 Tage im Escrow gesichert. Bestätige den Erhalt der Ware, sobald sie da ist — dann wird die Zahlung an den Verkäufer freigegeben.</p>
            <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap">
              <button class="btn btn-buy" onclick="confirmReceipt('${escrowId}')">✓ Erhalt bestätigen</button>
              <a href="/api/v1/contracts/${escrowId}/html" target="_blank" class="btn btn-outline">📄 Kaufvertrag</a>
              <a href="/ratings?escrow=${escrowId}" class="btn btn-outline">⭐ Verkäufer bewerten</a>
            </div>
          </div>`;
    } catch (err) {
        st.innerHTML = `<div class="error-box">✗ ${err.message}</div>`;
        btn.disabled = false;
        btn.textContent = LS.buy_now;
    }
}

async function fundEscrow(escrowId) {
    const r = await fetch('/api/v1/escrow/' + escrowId + '/fund', { method: 'POST' });
    const d = await r.json();
    const st = document.getElementById('escrow-status');
    if (r.ok) {
        const deadline = new Date(d.freeze_deadline);
        st.innerHTML = `
          <div class="escrow-funded">
            <strong>✓ Bezahlt – FND eingefroren bis ${deadline.toLocaleDateString('de-DE')}</strong>
            <div class="escrow-btns" style="margin-top:8px">
              <button class="btn" onclick="confirmReceipt('${escrowId}')">
                ✅ Ware erhalten und OK
              </button>
              <button class="btn btn-warn" onclick="requestCancel('${escrowId}')">
                ↩ Storno beantragen
              </button>
              <a href="/api/v1/contracts/${escrowId}/html" target="_blank"
                 class="btn btn-outline">
                📄 Kaufvertrag PDF
              </a>
            </div>
          </div>`;
    } else {
        st.innerHTML = `<div class="error-box">✗ ${d.error}</div>`;
    }
}

async function confirmReceipt(escrowId) {
    if (!confirm('Warenerhalt bestätigen? FND werden sofort freigegeben.')) return;
    // Seed-Wörter abfragen — die Freigabe-Transaktion muss vom Käufer signiert werden.
    const seed = prompt(LS.enter_key || 'Gib deine Wallet-Wörter ein, um die Freigabe zu bestätigen:');
    if (!seed) return;
    const words = seed.trim().split(/\s+/);
    if (words.length < 12) { alert(LS.key_too_short || 'Zu wenige Wörter.'); return; }
    const r = await fetch('/api/v1/escrow/' + escrowId + '/confirm', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ words: words })
    });
    const d = await r.json();
    const st = document.getElementById('escrow-status');
    if (r.ok) {
        st.innerHTML = `<div class="success-box">✓ Kauf abgeschlossen. Zahlung an Verkäufer freigegeben.
          <a href="/api/v1/contracts/${escrowId}/html" target="_blank" style="margin-left:8px">
            📄 Kaufvertrag PDF
          </a></div>`;
    } else {
        st.innerHTML = `<div class="error-box">✗ ${d.error}</div>`;
    }
}

async function requestCancel(escrowId) {
    const reasons = {
        'not_received':     LS.dispute_not_received,
        'defective':        LS.dispute_defective,
        'wrong_item':       LS.dispute_wrong,
        'not_as_described': LS.dispute_not_described,
    };
    const reason = prompt(
        '1) not_received – ' + LS.dispute_not_received + '\n' +
        '2) defective – ' + LS.dispute_defective + '\n' +
        '3) wrong_item – ' + LS.dispute_wrong + '\n' +
        '4) not_as_described – ' + LS.dispute_not_described + '\n\n' +
        LS.enter_key_prompt,
        'not_received'
    );
    if (!reason) return;

    const r = await fetch('/api/v1/escrow/' + escrowId + '/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reason }),
    });
    const d = await r.json();
    const st = document.getElementById('escrow-status');
    if (r.ok) {
        st.innerHTML = `
          <div class="cancel-box">
            <strong>${LS.cancel_requested}${reasons[reason] || reason}</strong>
            <p>${LS.must_return}</p>
            <p>${LS.tracking_note}</p>
            <input id="tracking-input" placeholder="${LS.tracking_placeholder}"
                   style="width:100%%;padding:6px;margin:6px 0;font-family:monospace">
            <button class="btn" onclick="submitReturn('${escrowId}')">
              ${LS.submit_return}
            </button>
            <a href="/api/v1/contracts/${escrowId}/html" target="_blank"
               class="btn btn-outline" style="margin-left:8px">
              📄 Kaufvertrag PDF
            </a>
          </div>`;
    } else {
        st.innerHTML = `<div class="error-box">✗ ${d.error}</div>`;
    }
}

async function submitReturn(escrowId) {
    const tracking = document.getElementById('tracking-input').value.trim();
    if (!tracking) { alert('Tracking-Nummer eingeben'); return; }

    // SHA256 im Browser berechnen
    const enc  = new TextEncoder().encode(tracking);
    const buf  = await crypto.subtle.digest('SHA-256', enc);
    const hash = '0x' + Array.from(new Uint8Array(buf))
                              .map(b => b.toString(16).padStart(2, '0'))
                              .join('');

    const r = await fetch('/api/v1/escrow/' + escrowId + '/return', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tracking_hash: hash }),
    });
    const d = await r.json();
    const st = document.getElementById('escrow-status');
    if (r.ok) {
        st.innerHTML = `<div class="success-box">
          ✓ Rücksendebeleg eingereicht. Tracking-Hash: <code>${hash.slice(0,18)}…</code><br>
          Verkäufer hat 7 Tage um die Rücksendung zu bestätigen.
          <a href="/api/v1/contracts/${escrowId}/html" target="_blank" style="margin-left:8px">
            📄 Kaufvertrag PDF
          </a>
        </div>`;
    } else {
        st.innerHTML = `<div class="error-box">✗ ${d.error}</div>`;
    }
}
</script>

<style>
.buy-box       { background:var(--surface); border:1px solid var(--border);
                 border-radius:var(--radius); padding:1.25rem; margin-top:1rem; }
.buy-box h3    { margin:0 0 8px; }
.buy-hint      { font-size:13px; color:var(--muted); margin-bottom:12px; }
.buy-actions   { display:flex; gap:8px; flex-wrap:wrap; }
.btn-buy       { background:#166534; color:#fff; border-color:#166534; }
.btn-buy:hover { background:#14532d; }
.btn-warn      { background:#92400e; color:#fff; border-color:#92400e; }
.btn-outline   { background:transparent; border:1px solid var(--border); }
.btn-contract  { font-size:12px; }
.escrow-status { margin-top:12px; }
.escrow-created,.escrow-funded { background:#f0fdf4; border:1px solid #86efac;
                 border-radius:4px; padding:12px; }
.cancel-box    { background:#fff7ed; border:1px solid #fed7aa; border-radius:4px; padding:12px; }
.cancel-box p  { font-size:13px; margin:4px 0; }
.escrow-btns   { display:flex; gap:8px; flex-wrap:wrap; margin-top:8px; }
.success-box   { background:rgba(0,230,118,0.10); border:1px solid rgba(0,230,118,0.3); border-radius:6px;
                 padding:12px; font-size:13px; color:var(--text); }
.success-box strong { color:var(--green); }
.success-box p { color:var(--text); }
.error-box     { background:rgba(244,67,54,0.10); border:1px solid rgba(244,67,54,0.3); border-radius:6px;
                 padding:12px; font-size:13px; color:var(--text); }
.hidden        { display:none; }
code           { font-family:monospace; font-size:11px; background:var(--bg); color:var(--green); padding:1px 4px; border-radius:3px; }
</style>
]],
        imagesHtml,
        tostring(data.condition or ""):lower(), condition,
        category,
        priceStr,
        -- Beschreibung ist bereits server-seitig sanitized (nur sichere Tags),
        -- daher direkt als HTML ausgeben (erlaubt Fett/Kursiv/Listen/Absätze).
        tostring(data.description or data.listing_text or ""),
        t("listing.col_condition"), condition,
        t("listing.col_id"),        record.id,
        t("listing.keywords"),      keywords,
        t("upload.field_plz"),      render.html_escape(tostring(data.plz or "–")),
        t("upload.field_delivery"), deliveryStr,
        t("general.back"), ownerActions, buySection,
        t("general.confirm_delete")
    ))

    ngx.print([==[<script>
(function(){
  var el = document.getElementById('seller-rating'); if (!el) return;
  var a = el.getAttribute('data-addr');
  fetch('/api/v1/ratings?addr=' + encodeURIComponent(a)).then(function(r){ return r.json(); }).then(function(d){
    if (!d || !d.count) { el.innerHTML = '<span class="meta">Noch keine Bewertungen</span>'; return; }
    var full = Math.round(d.avg), stars = '';
    for (var i = 1; i <= 5; i++) stars += i <= full ? '★' : '☆';
    el.innerHTML = '<a href="/ratings?addr=' + encodeURIComponent(a) + '" style="text-decoration:none">' +
      '<span style="color:#00e676;letter-spacing:1px">' + stars + '</span> ' +
      '<b>' + d.avg.toFixed(1).replace('.', ',') + '</b> <span class="meta">(' + d.count + ' Bewertung' + (d.count === 1 ? '' : 'en') + ')</span></a>';
  }).catch(function(){});
})();
</script>]==])
    -- Ähnliche Angebote (gleiche Kategorie) und Seiten-Skripte (R552)
    ngx.print('<section id="similar" class="similar" style="display:none"><h2>Ähnliche Angebote</h2><div class="hit-grid" id="similar-grid"></div></section>')
    ngx.print('<script>window.LISTING_META=' .. (require("cjson.safe").encode({ id = record.id, title = tostring(record.data and record.data.title or ""), category = tostring(record.data and record.data.category or "") }) or "{}") .. ';</script>')
    ngx.print([==[<script>
(function(){
  function esc(t){ return String(t==null?'':t).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  var M = window.LISTING_META || {};
  // Verkäuferkarte: Name und Bild zur Ersteller-ID, "Angebot seit"
  var card = document.getElementById('seller-card');
  if (card) {
    var fid = card.getAttribute('data-fid'), created = card.getAttribute('data-created');
    var sub = [];
    if (created) { var d = new Date(created); if (!isNaN(d)) sub.push('Angebot seit ' + d.toLocaleDateString('de-DE')); }
    document.getElementById('sc-sub').textContent = sub.join(' · ');
    if (fid) {
      fetch('/api/v1/identity/names?ids=' + encodeURIComponent(fid)).then(function(r){ return r.json(); }).then(function(d){
        var n = d && d.names && d.names[fid]; if (n) document.getElementById('sc-name').textContent = n;
        var av = d && d.avatars && d.avatars[fid];
        if (av) { document.getElementById('sc-av').innerHTML = '<img src="/api/v1/identity/avatar/' + encodeURIComponent(fid) + '" alt="">'; }
      }).catch(function(){});
    }
  }
  // QR-Code des Angebots: Link zum Scannen (Aushang, Etikett, Bildschirm)
  window.listingQR = async function(){
    if (!window.fundusShowQR) return;
    var r = window.fundusPublicHref ? await fundusPublicHref(location.href) : { url: location.href, external: true };
    fundusShowQR(r.url, M.title || 'Angebot', r.external
      ? 'Scannen öffnet dieses Angebot.'
      : 'Nur im Heimnetz gültig – eine öffentliche Adresse steht unter Einstellungen → Netzwerk.');
  };
  // Teilen
  window.shareListing = async function(){
    var r = window.fundusPublicHref ? await fundusPublicHref(location.href) : { url: location.href };
    var url = r.url, title = M.title || document.title;
    if (navigator.share) { navigator.share({ title: title, url: url }).catch(function(){}); return; }
    if (navigator.clipboard) { navigator.clipboard.writeText(url); if (window.fundusToast) fundusToast('✓ Link kopiert'); }
  };
  // Lightbox: Blättern mit Pfeilen, Tasten und Wischen
  var photos = Array.prototype.slice.call(document.querySelectorAll('.listing-photo'));
  if (photos.length > 1) {
    var idx = 0;
    function show(i){ idx = (i + photos.length) % photos.length; var lb = document.getElementById('lightbox'); if (lb) lb.querySelector('.lightbox-img').src = photos[idx].src; }
    document.addEventListener('click', function(e){
      var lb = document.getElementById('lightbox'); if (!lb || !lb.classList.contains('open')) return;
      if (!lb.querySelector('.lb-nav')) {
        var p = document.createElement('button'); p.className = 'lb-nav lb-prev'; p.textContent = '‹';
        var n = document.createElement('button'); n.className = 'lb-nav lb-next'; n.textContent = '›';
        p.onclick = function(ev){ ev.stopPropagation(); show(idx - 1); }; n.onclick = function(ev){ ev.stopPropagation(); show(idx + 1); };
        lb.appendChild(p); lb.appendChild(n);
        var x0 = null;
        lb.addEventListener('touchstart', function(t){ x0 = t.touches[0].clientX; }, {passive:true});
        lb.addEventListener('touchend', function(t){ if (x0 === null) return; var dx = t.changedTouches[0].clientX - x0; x0 = null; if (Math.abs(dx) > 40) { t.stopPropagation(); show(dx < 0 ? idx + 1 : idx - 1); } }, {passive:true});
      }
      var src = lb.querySelector('.lightbox-img').src; var k = photos.findIndex(function(ph){ return ph.src === src; }); if (k >= 0) idx = k;
    }, true);
    document.addEventListener('keydown', function(e){ var lb = document.getElementById('lightbox'); if (!lb || !lb.classList.contains('open')) return; if (e.key === 'ArrowRight') show(idx + 1); if (e.key === 'ArrowLeft') show(idx - 1); if (e.key === 'Escape') lb.classList.remove('open'); });
  }
  // Ähnliche Angebote: gleiche Kategorie, bis zu 4
  if (document.getElementById('buy-btn')) { onDeliveryChange(); }
  if (M.category) {
    fetch('/api/v1/search?category=' + encodeURIComponent(M.category)).then(function(r){ return r.json(); }).then(function(d){
      var sid = d.search_id, hits = d.hits || [];
      function render(list){
        var seen = {}, out = [];
        list.forEach(function(it){ if (it.id === M.id || seen[it.id] || it.sold) return; seen[it.id] = 1; out.push(it); });
        out = out.slice(0, 4); if (!out.length) return;
        document.getElementById('similar-grid').innerHTML = out.map(function(it){
          var price = it.price_min ? (parseFloat(it.price_min).toFixed(2).replace('.', ',') + ' FND') : '–';
          // Gespeichertes 200-px-Bild als Platzhalter, scharfes vom Node nachladen.
          var big = it.image_hash ? '/api/v1/files/thumb/' + encodeURIComponent(it.image_hash) : '';
          var img = (it.thumbnail || big) ? '<img src="' + (it.thumbnail || big) + '"' + (it.thumbnail && big ? ' data-hq="' + big + '"' : '') + ' loading="lazy" alt="">' : '<div class="hc-ph"></div>';
          return '<a class="hit-card" href="/listings/' + it.id + '"><div class="hc-img">' + img + '<span class="hc-price">' + price + '</span></div><div class="hc-title">' + esc(it.title) + '</div><div class="hc-meta">' + (it.distance_km ? it.distance_km + ' km' : '') + '</div></a>';
        }).join('');
        document.getElementById('similar').style.display = '';
        // Nacheinander nachladen (R622) – alle auf einmal kamen nicht durch.
        (function(){
          var jobs = [];
          document.querySelectorAll('#similar-grid img[data-hq]').forEach(function(im){
            jobs.push({ im: im, url: im.getAttribute('data-hq') });
            im.removeAttribute('data-hq'); // nicht doppelt einreihen
          });
          var i = 0;
          (function next(){
            if (i >= jobs.length) return;
            var job = jobs[i++], im = job.im, u = job.url;
            if (!u) { next(); return; }
            var hq = new Image();
            hq.onload = function(){ im.src = u; next(); };
            hq.onerror = next;
            hq.src = u;
          })();
        })();
      }
      render(hits);
      if (sid) setTimeout(function(){ fetch('/api/v1/search/results?id=' + encodeURIComponent(sid)).then(function(r){ return r.json(); }).then(function(d2){ render(d2.hits || []); }).catch(function(){}); }, 1800);
    }).catch(function(){});
  }
})();
</script>]==])
    render.footer()
end

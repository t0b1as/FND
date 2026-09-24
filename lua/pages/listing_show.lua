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
    local isSold = (record.data and record.data.sold == true)
    if isOwner then
        ownerActions = string.format(
            '<a href="/listings/%s/edit" class="btn">%s</a>' ..
            '<button class="btn btn-danger" onclick="deleteListing(\'%s\')">%s</button>',
            record.id, t("general.edit"), record.id, t("general.delete"))
        if isSold then
            buySection = '<div class="sold-note">' .. t("listing.sold_note_owner") .. '</div>'
        else
            buySection = '<div class="owner-note">' .. t("listing.own_offer_note") .. '</div>'
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
            contactBtn = string.format(
                '<button class="btn btn-outline btn-contact-seller" ' ..
                'onclick="contactSeller(%s,%s)">💬 %s</button>',
                string.format("%q", cfid or ""), string.format("%q", cpk),
                t("listing.contact_seller"))
        end
        -- Verkäufer-Empfangsadresse (Wallet) anzeigen, falls im Inserat gesetzt.
        local sellerWallet = record.data and record.data.seller_wallet
        local walletRow = ""
        if type(sellerWallet) == "string" and sellerWallet ~= "" then
            walletRow = string.format(
                '<div class="seller-wallet-row" style="margin:8px 0;font-size:12px;color:var(--muted)">'..
                '%s: <code style="font-size:11px">%s</code> '..
                '<button class="btn-sm" style="padding:2px 8px;font-size:11px" onclick="copyWallet(\'%s\')">📋</button></div>',
                t("listing.seller_wallet"), sellerWallet, sellerWallet)
        end
        -- Versand-Erkennung: Bei Versand ein Adressfeld anzeigen und den
        -- Kaufbutton deaktivieren, bis eine Adresse eingegeben ist. Die Adresse
        -- wird nach dem Kauf per Messenger an den Verkäufer geschickt.
        local isShipping = tostring(record.data and record.data.delivery or "") == "shipping"
        local shipRow = ""
        local buyDisabled = ""
        if isShipping then
            shipRow = string.format([=[
    <div class="ship-address-box" style="margin:10px 0">
      <label style="font-size:13px;font-weight:600;display:block;margin-bottom:4px">%s</label>
      <textarea id="ship-address" rows="3" placeholder="%s"
                style="width:100%%;background:var(--bg);border:1px solid var(--border);color:var(--text);border-radius:8px;padding:10px;font-size:13px"
                oninput="onShipAddressChange()"></textarea>
    </div>]=], t("listing.ship_address"), t("listing.ship_address_ph"))
            buyDisabled = "disabled"
        end
        -- contact_pub_key für die Messenger-Benachrichtigung ans Frontend geben.
        local cpkJs = (type(cpk) == "string" and cpk ~= "") and cpk or ""
        local cfidJs = (type(cfid) == "string") and cfid or ""
        buySection = string.format([==[
  <div class="buy-box" id="buy-box" data-shipping="%s" data-cpk="%s" data-cfid="%s">
    <h3>%s</h3>
    <p class="buy-hint">
      FND werden nach Kauf fuer <strong>14 Tage</strong> eingefroren.
      Du kannst in dieser Zeit den Erhalt bestaetigen oder Storno beantragen.
    </p>
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
            (isShipping and "1" or "0"), cpkJs, cfidJs,
            t("listing.buy"), walletRow, shipRow, buyDisabled,
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
function onShipAddressChange() {
  const ta = document.getElementById('ship-address');
  const btn = document.getElementById('buy-btn');
  if (ta && btn) {
    btn.disabled = ta.value.trim().length < 5;
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
        lb.innerHTML = '<span class="lightbox-close">✕</span><img class="lightbox-img" src="">';
        lb.addEventListener('click', () => lb.classList.remove('open'));
        document.body.appendChild(lb);
    }
    lb.querySelector('.lightbox-img').src = src;
    lb.classList.add('open');
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

async function startEscrow(listingId, sellerWallet, amountFnd) {
    // Ohne Verkäufer-Empfangsadresse kein Direktkauf möglich.
    if (!sellerWallet) {
        alert(LS.no_seller_wallet || 'Dieses Inserat hat keine Empfangsadresse. Bitte den Verkäufer kontaktieren.');
        return;
    }
    // Bei Versand: Versandadresse muss ausgefüllt sein.
    const buyBox = document.getElementById('buy-box');
    const isShipping = buyBox && buyBox.dataset.shipping === '1';
    let shipAddress = '';
    if (isShipping) {
        const ta = document.getElementById('ship-address');
        shipAddress = ta ? ta.value.trim() : '';
        if (!shipAddress) {
            alert(LS.ship_required || 'Bitte gib deine Versandadresse ein.');
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
                amount_fnd: amountFnd || undefined
            }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || LS.error_word);

        // Bei Versand: Adresse per Messenger an den Verkäufer schicken.
        if (isShipping && shipAddress) {
            await sendShippingAddress(buyBox, listingId, d.escrow_id, shipAddress);
        }

        const escrowId = d.escrow_id;
        // Kauf ist mit den Wörtern bereits bezahlt (Escrow eröffnet + eingefroren).
        // Kein separater "Jetzt bezahlen"-Schritt mehr nötig.
        st.innerHTML = `
          <div class="success-box">
            <strong>✓ Kauf abgeschlossen — ${amountFnd || ''} FND eingefroren (Escrow ${(escrowId||'').slice(0,12)}…)</strong>
            <p style="margin-top:6px">Die FND sind für 14 Tage im Escrow gesichert. Bestätige den Erhalt der Ware, sobald sie da ist — dann wird die Zahlung an den Verkäufer freigegeben.</p>
            <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap">
              <button class="btn btn-buy" onclick="confirmReceipt('${escrowId}')">✓ Erhalt bestätigen</button>
              <a href="/api/v1/contracts/${escrowId}/html" target="_blank" class="btn btn-outline">📄 Kaufvertrag</a>
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

    render.footer()
end

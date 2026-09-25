// Messenger-Seite (ausgelagert aus pages/messenger.lua, gecacht über ?v=<Revision>)

// ======================================================================
//  Zustand
// ======================================================================
let myIdentity   = null;  // { fundusID, publicKey, privateKeyBytes }
let activeChat   = null;  // { fundusID, publicKey, alias }
let contacts     = {};    // fundusID → { fundusID, publicKey, alias }
let messages     = [];    // Nachrichtenverlauf
window.__inMessenger = true; // signalisiert msgnotify.js: hier läuft die volle UI
if (window.msgNotifyReset) window.msgNotifyReset(); // Badge zurücksetzen beim Öffnen
let ws           = null;  // WebSocket zur lokalen API
let wsRetry      = 0;     // Reconnect-Backoff-Zähler
let peerConn     = null;  // RTCPeerConnection
let localStream  = null;  // MediaStream
let isInitiator  = false;
let isMuted      = false;
let isCamOff     = false;

// ======================================================================
//  Identitäts-Ableitung (via lokale API – Argon2id läuft server-seitig)
// ======================================================================
async function deriveIdentity() {
    const email = document.getElementById('login-email').value.trim();
    const pass  = document.getElementById('login-pass').value;
    const st    = document.getElementById('login-status');

    if (!email || pass.length < 8) {
        st.textContent = MSGT.email_pw_required;
        return;
    }

    st.textContent = MSGT.deriving;
    document.getElementById('login-btn').disabled = true;

    try {
        const resp = await fetch('/api/v1/identity/derive', {
            method:  'POST',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({ email, password: pass }),
        });
        const data = await resp.json();
        if (!resp.ok) {
            st.textContent = data.error || MSGT.error_word;
            document.getElementById('login-btn').disabled = false;
            return;
        }

        myIdentity = data;
        onLoggedIn();
    } catch(e) {
        st.textContent = e.message;
        document.getElementById('login-btn').disabled = false;
    }
}

// onLoggedIn: gemeinsame Logik nach erfolgreichem Login (egal ob über den
// eigenen Login-Screen oder die zentrale Wallet-Session).
function onLoggedIn() {
    const lf = document.getElementById('login-form');
    if (lf) lf.style.display = 'none';
    const idStatus = document.getElementById('identity-status');
    if (idStatus) idStatus.innerHTML =
        `<strong>${MSGT.signed_in}</strong> <span class="mono" style="font-size:11px" title="${myIdentity.fundus_id}">${shortAddr(myIdentity.fundus_id)}</span>`;
    const ls = document.getElementById('login-status');
    if (ls) ls.textContent = '';
    connectWebSocket(); // Name festlegen: Menü oben rechts ("Namen festlegen…")
    publishPresence(msgVisible());
    startPresence(); // Herzschlag + Online-Liste
    renderContacts();
    loadContacts(); // gespeicherte Kontakte der Wallet wiederherstellen
    fetchMailbox(); // wartende Offline-Nachrichten abholen
    openChatFromURL(); // ?to=&name= aus Marktplatz/Partner
    // Falls ungelesene Nachrichten warten (aus msgnotify persistiert), direkt die
    // letzte Konversation öffnen — sofern nicht schon per URL ein Chat gesetzt wurde.
    if (!activeChat) {
        try {
            const st = JSON.parse(localStorage.getItem('fnd_msg_unread') || '{}');
            if (st.lastSender) {
                const c = findContact(st.lastSender.toLowerCase());
                if (c) openChat(c);
                else openChat({ fundusID: st.lastSender.toLowerCase(), publicKey:'', alias: st.lastSender.slice(0,10)+'…' });
            }
        } catch(e){}
    }
    updateSendState();
}

// initMessenger: nutzt die ZENTRALE Wallet-Session. Ist der Nutzer schon
// angemeldet (über das Wallet-Badge oben), wird der Login-Screen übersprungen.
async function initMessenger() {
    try {
        const d = await window.fundusMe();
        if (d) {
            myIdentity = { fundus_id: d.fundus_id, wallet_address: d.wallet_address,
                           ed25519_pub_key: d.ed25519_pub_key, display_name: d.display_name || '' };
            onLoggedIn();
            return;
        }
    } catch(e) {}
    // Nicht angemeldet: Hinweis auf den zentralen Login zeigen.
    updateSendState();
    const idStatus = document.getElementById('identity-status');
    if (idStatus) idStatus.innerHTML =
        '<span style="color:var(--muted)">Bitte oben rechts über „Anmelden" mit deiner Wallet einloggen – dann bist du hier und überall erkannt.</span>';
}
initMessenger();

// Wenn sich der Wallet-Login-Status ändert (Anmelden über das Badge oben),
// den Messenger-Zustand neu aufbauen — dann ist die Eingabe sofort frei.
window.addEventListener('wallet-changed', function(ev){
    if (ev.detail && ev.detail.fundusID && !myIdentity) {
        initMessenger();
    }
});

// ======================================================================
//  WebSocket – Echtzeit-Empfang
// ======================================================================
function connectWebSocket() {
    if (!myIdentity) return;
    // Schon eine OFFENE Verbindung? Dann nichts tun (mehrfache Aufrufe von
    // initMessenger/onLoggedIn sollen keine neue Verbindung erzwingen).
    if (ws && ws.readyState === WebSocket.OPEN) return;
    // Verbindung im Aufbau? Auch nicht doppelt starten.
    if (ws && ws.readyState === WebSocket.CONNECTING) return;
    // Alte (geschlossene/fehlerhafte) Verbindung sauber entfernen.
    if (ws) {
        try { ws.onclose = null; ws.close(); } catch(e){}
        ws = null;
    }
    const wsProto = (location.protocol === "https:") ? "wss:" : "ws:";
    ws = new WebSocket(`${wsProto}//${location.host}/api/v1/messenger/ws?id=${encodeURIComponent(myIdentity.fundus_id)}`);

    ws.onopen = () => { wsRetry = 0; }; // erfolgreich → Backoff zurücksetzen
    ws.onmessage = async (ev) => {
        const msg = JSON.parse(ev.data);
        await handleIncomingMessage(msg);
    };
    ws.onclose = () => {
        ws = null;
        // Vor dem Reconnect prüfen, ob die Session überhaupt noch gültig ist.
        // Nach einem Node-Neustart ist die Session weg (nur im RAM) → dann nicht
        // endlos reconnecten, sondern den Nutzer zum Neu-Anmelden auffordern.
        fetch('/api/v1/identity/me', {credentials:'same-origin'}).then(r => {
            if (!r.ok) {
                // Session weg → Reconnect stoppen, Hinweis zeigen.
                myIdentity = null;
                const idStatus = document.getElementById('identity-status');
                if (idStatus) idStatus.innerHTML =
                    '<span style="color:var(--red,#f66)">Sitzung abgelaufen – bitte oben rechts neu anmelden.</span>';
                if (window.walletRefresh) window.walletRefresh(true);
                return;
            }
            // Session noch da → mit Backoff reconnecten.
            wsRetry = Math.min((wsRetry || 0) + 1, 5);
            const delay = Math.min(3000 * Math.pow(2, wsRetry - 1), 60000);
            setTimeout(connectWebSocket, delay);
        }).catch(() => {
            wsRetry = Math.min((wsRetry || 0) + 1, 5);
            const delay = Math.min(3000 * Math.pow(2, wsRetry - 1), 60000);
            setTimeout(connectWebSocket, delay);
        });
    };
    ws.onerror = () => { try { ws.close(); } catch(e){} };
}

// ======================================================================
//  Nachricht empfangen + entschlüsseln
// ======================================================================
// showToast zeigt eine kleine Info-Bubble oben rechts (z.B. bei neuer Nachricht).
function showToast(text, sub) {
    let c = document.getElementById('msg-toast-container');
    if (!c) {
        c = document.createElement('div');
        c.id = 'msg-toast-container';
        c.style.cssText = 'position:fixed;top:70px;right:16px;z-index:9999;display:flex;flex-direction:column;gap:8px';
        document.body.appendChild(c);
    }
    const t = document.createElement('div');
    t.style.cssText = 'background:var(--surface,#1a1a2e);border:1px solid var(--green-ctrl,#17a862);border-left:4px solid var(--green,#00e676);border-radius:10px;padding:10px 14px;box-shadow:0 4px 18px rgba(0,0,0,.4);max-width:280px;cursor:pointer;animation:toastIn .2s ease';
    t.innerHTML = '<div style="font-weight:600;font-size:13px">'+escapeHtml(text)+'</div>'+(sub?'<div style="font-size:12px;color:var(--muted);margin-top:2px">'+escapeHtml(sub)+'</div>':'');
    t.onclick = () => t.remove();
    c.appendChild(t);
    setTimeout(() => { t.style.opacity='0'; t.style.transition='opacity .3s'; setTimeout(()=>t.remove(),300); }, 4000);
}

// findContact sucht einen Kontakt über eine beliebige seiner Adressen
// (fundusID ODER wallet_address/chainAddress). So wird eine eingehende
// Nachricht dem bereits gespeicherten Kontakt zugeordnet, statt ein Duplikat
// mit roher Adresse anzulegen.
function findContact(addr) {
    if (!addr) return null;
    const a = addr.toLowerCase();
    if (contacts[a]) return contacts[a];
    for (const c of Object.values(contacts)) {
        if ((c.fundusID||'').toLowerCase() === a) return c;
        if ((c.chainAddress||'').toLowerCase() === a) return c;
        if ((c.walletAddress||'').toLowerCase() === a) return c;
    }
    return null;
}

// contactName gibt den anzuzeigenden Namen für eine Adresse zurück: den Alias
// eines gespeicherten Kontakts, sonst die gekürzte Adresse.
function contactName(addr) {
    return displayName(addr, findContact(addr));
}

// ── Anzeigenamen ───────────────────────────────────────────────────────────
// Jeder kann sich einen Namen geben (signiert, netzweit verbreitet). Ein selbst
// vergebener Kontakt-Alias hat Vorrang; der übermittelte Name ersetzt nur die
// automatische Kurzadresse.
const nameCache = {}; // fundusID (klein) → Anzeigename
function isAutoAlias(c) {
    if (!c || !c.alias) return true;
    const id = String(c.fundusID || '');
    return c.alias === id.slice(0,10)+'…' || c.alias === id.slice(0,8)+'…';
}
function displayName(addr, c) {
    const a = String(addr || (c && c.fundusID) || '').toLowerCase();
    if (c && !isAutoAlias(c)) return c.alias;
    if (nameCache[a]) return nameCache[a];
    if (c && c.alias) return c.alias;
    return a.slice(0,10)+'…';
}
async function loadNames(fids) {
    const want = [...new Set((fids||[]).map(f => String(f||'').toLowerCase()).filter(f => f.length === 42 && !(f in nameCache)))];
    if (!want.length) return;
    try {
        const r = await fetch('/api/v1/identity/names?ids=' + encodeURIComponent(want.join(',')), { credentials: 'same-origin' });
        const d = await r.json();
        for (const f of want) nameCache[f] = (d.names && d.names[f]) || '';
    } catch (e) {}
}
async function saveMyName() {
    const inp = document.getElementById('my-name'), st = document.getElementById('my-name-status');
    try {
        const r = await fetch('/api/v1/identity/name', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: inp.value }) });
        const d = await r.json();
        if (!r.ok || d.error) throw new Error(d.error || ('HTTP ' + r.status));
        inp.value = d.name || '';
        st.textContent = d.name ? '✓ gespeichert – andere sehen dich als „' + d.name + '“' : '✓ Name entfernt';
    } catch (e) { st.textContent = '✗ ' + e.message; }
}
function renderMyNameRow() {
    if (document.getElementById('my-name-row')) return;
    const anchor = document.getElementById('identity-status');
    if (!anchor || !anchor.parentNode) return;
    const row = document.createElement('div');
    row.id = 'my-name-row';
    row.className = 'my-name-row';
    row.innerHTML = '<input type="text" id="my-name" maxlength="32" autocomplete="nickname" placeholder="Dein Name (für andere sichtbar)">' +
        '<button class="btn" id="my-name-save">Speichern</button><div id="my-name-status" class="my-name-status"></div>';
    anchor.parentNode.insertBefore(row, anchor.nextSibling);
    document.getElementById('my-name-save').onclick = saveMyName;
    document.getElementById('my-name').addEventListener('keydown', e => { if (e.key === 'Enter') saveMyName(); });
    if (myIdentity && myIdentity.display_name) {
        document.getElementById('my-name').value = myIdentity.display_name;
    } else if (window.fundusMe) {
        // Nach frischem Login liefert die Anmeldung den Namen nicht mit → nachladen.
        window.fundusMe(true).then(d => {
            if (d && d.display_name) {
                myIdentity.display_name = d.display_name;
                const inp = document.getElementById('my-name');
                if (inp && !inp.value) inp.value = d.display_name;
            }
        }).catch(() => {});
    }
}

async function handleIncomingMessage(msg) {
    if (msg.type === 'signal') {
        await handleSignal(msg);
        return;
    }
    // Zustell-/Lesequittung: aktualisiert die Häkchen der ausgehenden Bubble.
    if (msg.type === 'receipt') {
        applyReceipt(msg.receipt_for, msg.receipt_kind, msg.ts);
        return;
    }

    // Backend liefert die Nachricht bereits ENTSCHLÜSSELT über den WebSocket
    // (decrypted:true). Dann direkt anzeigen — kein zweiter decrypt-Roundtrip.
    let payload;
    if (msg.decrypted) {
        payload = { text: msg.text, file_hash: msg.file_hash, file_name: msg.file_name,
                    file_size: msg.file_size, mime_type: msg.mime_type };
    } else {
        // Fallback (ältere Nachrichtenform): via API entschlüsseln.
        const resp = await fetch('/api/v1/messenger/decrypt', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify(msg),
        });
        if (!resp.ok) return;
        payload = await resp.json();
    }
    if (msg.sender_name && msg.sender_id) nameCache[String(msg.sender_id).toLowerCase()] = msg.sender_name;
    appendMessage({
        id:      msg.id,
        from:    msg.sender_id,
        text:    payload.text,
        file:    payload.file_hash ? { hash: payload.file_hash, name: payload.file_name, size: payload.file_size, mime: payload.mime_type } : null,
        ts:      new Date(msg.ts),
        outgoing: false,
    });
    // Wenn dieser Chat gerade offen ist, sofort Lesequittung senden.
    if (activeChat && msg.sender_id === activeChat.fundusID) {
        sendReadReceipts([msg.id]);
    } else {
        // Nachricht für einen anderen (nicht offenen) Chat.
        const sid = (msg.sender_id||'').toLowerCase();
        let known = findContact(sid); // über fundusID ODER wallet-Adresse finden
        // Wirklich unbekannter Absender? Automatisch als Kontakt anlegen, damit
        // der Chat in der Liste auftaucht und der Verlauf abrufbar ist.
        if (!known && sid) {
            contacts[sid] = {
                fundusID: sid,
                publicKey: msg.sender_pub_x || '',
                alias: sid.slice(0,10)+'…',
                online: false,
                unread: 0
            };
            known = contacts[sid];
            syncContacts(); // neuen Kontakt speichern
        }
        if (known) { known.unread = (known.unread||0) + 1; }
        renderContacts();
        const who = known ? known.alias : sid.slice(0,10)+'…';
        showToast('Neue Nachricht von '+who, (payload.text||'').slice(0,60));
        if (window.playPling) window.playPling(); // Ton bei eingehender Nachricht
    }
}

// ======================================================================
//  Textnachricht senden
// ======================================================================
// ── Emoji-Picker ──────────────────────────────────────────────────────────
// Emoji-Daten werden beim ersten Öffnen des Panels von /static/emoji-data.json
// nachgeladen (volle Unicode-Liste, ~2400 Emojis, mehrsprachige Stichwörter).
let EMOJI_DATA = [];
let emojiFiltered = [];
let emojiLoaded = false;

async function loadEmojiData() {
    if (emojiLoaded) return true;
    try {
        const r = await fetch('/static/emoji-data.json');
        if (!r.ok) throw new Error('http ' + r.status);
        EMOJI_DATA = await r.json();
        emojiFiltered = EMOJI_DATA;
        emojiLoaded = true;
        return true;
    } catch (e) {
        return false;
    }
}

const EMOJI_RENDER_CAP = 200; // max. gleichzeitig gerenderte Buttons (Performance)

// emojiCodepoints wandelt ein Emoji-Zeichen in seinen Noto-Dateinamen um
// (@svgmoji/noto nutzt GROSSBUCHSTABEN-Codepoints, mit '-' verbunden; der
// Variation-Selector FE0F wird bei Einzel-Emojis weggelassen).
function emojiCodepoints(ch){
  const cps = [];
  for (const sym of ch){
    const cp = sym.codePointAt(0);
    if (cp === 0xFE0F) continue; // Variation Selector überspringen
    cps.push(cp.toString(16).toUpperCase());
  }
  return cps.join('-');
}
// emojiImg gibt ein <img>-Tag mit der Noto-Grafik (Google Noto Color Emoji,
// OFL/Apache — freundlicher, runder Stil) zurück; Fallback auf das native
// Zeichen, wenn das Bild nicht lädt.
const EMOJI_CDN = 'https://cdn.jsdelivr.net/npm/@svgmoji/noto@2.0.0/svg/';
function emojiImg(ch, cls){
  const code = emojiCodepoints(ch);
  return '<img class="'+(cls||'emoji-gfx')+'" draggable="false" alt="'+ch+'" '+
         'src="'+EMOJI_CDN+code+'.svg" '+
         'onerror="this.replaceWith(document.createTextNode(this.alt))">';
}

function renderEmojiGrid(list) {
    const grid = document.getElementById('emoji-grid');
    if (!grid) return;
    if (!list.length) { grid.innerHTML = '<div class="emoji-empty">'+ (MSGT.emoji_none||'—') +'</div>'; return; }
    const shown = list.slice(0, EMOJI_RENDER_CAP);
    let html = shown.map(e =>
        `<button type="button" class="emoji-item" title="${e[1].split(' ')[0]}" onclick="insertEmoji('${e[0]}')">${emojiImg(e[0],'emoji-pick')}</button>`
    ).join('');
    if (list.length > EMOJI_RENDER_CAP) {
        html += '<div class="emoji-more">+' + (list.length - EMOJI_RENDER_CAP)
              + ' · ' + (MSGT.emoji_refine||'refine search') + '</div>';
    }
    grid.innerHTML = html;
}

function filterEmojis(q) {
    q = (q||'').trim().toLowerCase();
    if (!q) { emojiFiltered = EMOJI_DATA; }
    else {
        // Alle Suchbegriffe müssen vorkommen (UND-Verknüpfung, mehrsprachig).
        const terms = q.split(/\s+/);
        emojiFiltered = EMOJI_DATA.filter(e => terms.every(tm => e[1].includes(tm)));
    }
    renderEmojiGrid(emojiFiltered);
}

async function toggleEmojiPanel() {
    const panel = document.getElementById('emoji-panel');
    if (!panel) return;
    if (panel.style.display === 'none') {
        if (!panel.dataset.filled) {
            panel.innerHTML =
                '<input type="text" id="emoji-search" class="emoji-search" placeholder="'
                + (MSGT.emoji_search||'Search…') + '" oninput="filterEmojis(this.value)" autocomplete="off">'
                + '<div id="emoji-grid" class="emoji-grid"></div>';
            panel.dataset.filled = '1';
            const grid = document.getElementById('emoji-grid');
            if (grid) grid.innerHTML = '<div class="emoji-empty">…</div>';
            const ok = await loadEmojiData();
            if (ok) { renderEmojiGrid(EMOJI_DATA); }
            else if (grid) { grid.innerHTML = '<div class="emoji-empty">' + (MSGT.emoji_none||'—') + '</div>'; }
        }
        panel.style.display = 'flex';
        const sb = document.getElementById('emoji-search');
        if (sb) { sb.value = ''; setTimeout(()=>sb.focus(), 50); }
    } else {
        panel.style.display = 'none';
    }
}
function insertEmoji(e) {
    const input = document.getElementById('msg-input');
    if (!input) return;
    input.value += e;
    input.focus();
}

// setRecipientFromAddr: macht aus einer im Adressfeld eingegebenen FundusID
// einen aktiven Chat (ohne dass vorher ein Kontakt angeklickt werden muss).
// updateSendState: aktiviert Textfeld + Senden-Knopf nur, wenn eine Empfänger-
// Adresse eingegeben ist. Ersetzt den früheren "Kontakt auswählen"-Platzhalter.
function updateSendState() {
    // Frei, sobald eine Identität ODER ein aktiver Chat da ist. Robust gegen
    // Session-Erkennungsprobleme: wer einen Kontakt gewählt hat, kann schreiben.
    const ready = !!myIdentity || !!activeChat;
    const inp = document.getElementById('msg-input');
    const btn = document.getElementById('send-btn');
    const emo = document.querySelector('.emoji-btn');
    if (inp) {
        inp.disabled = !ready;
        inp.placeholder = ready ? (MSGT.placeholder_msg||'Nachricht…')
                                : (MSGT.recipient_first||'Bitte anmelden…');
    }
    if (btn) btn.disabled = !ready;
    if (emo) emo.disabled = !ready;
}

async function setRecipientFromAddr() {
    let addr = (document.getElementById('chat-with-addr').value || '').trim().toLowerCase();
    if (!addr) { activeChat = null; updateSendState(); return; }
    // Sieht es aus wie eine Email? Dann im Verzeichnis die FundusID nachschlagen.
    if (addr.indexOf('@') > 0) {
        try {
            const r = await fetch('/api/v1/identity/email-dir/lookup?email=' + encodeURIComponent(addr));
            const d = await r.json();
            if (d.found && d.fundus_id) {
                const nameEl0 = document.getElementById('chat-with-name');
                if (nameEl0) nameEl0.textContent = addr; // Email als Anzeige-Name
                addr = d.fundus_id.toLowerCase();
                document.getElementById('chat-with-addr').dataset.resolved = addr;
                document.getElementById('chat-with-addr').dataset.resolvedKey = d.pub_key || '';
            } else {
                const nameEl1 = document.getElementById('chat-with-name');
                if (nameEl1) nameEl1.textContent = '(Email nicht im Verzeichnis)';
                activeChat = null; updateSendState(); return;
            }
        } catch(e) { activeChat = null; updateSendState(); return; }
    }
    const known = contacts[addr];
    activeChat = {
        fundusID:  addr,
        publicKey: known ? known.publicKey : (document.getElementById('chat-with-addr').dataset.resolvedKey || ''),
        alias:     known ? known.alias : addr.slice(0,10)+'…'
    };
    const nameEl = document.getElementById('chat-with-name');
    if (nameEl && !nameEl.textContent) nameEl.textContent = known ? known.alias : '';
    updateSendState();
    loadHistory(addr, true);
}

async function sendText() {
    if (!myIdentity) { alert('Bitte zuerst oben rechts anmelden.'); return; }
    // Falls kein aktiver Chat, aber eine Adresse im Feld → daraus Empfänger machen.
    // setRecipientFromAddr ist async (E-Mail-Auflösung), daher AWAIT — sonst ist
    // activeChat beim folgenden Check noch nicht gesetzt und das Senden bricht ab.
    if (!activeChat) {
        const addr = (document.getElementById('chat-with-addr').value || '').trim().toLowerCase();
        if (addr) await setRecipientFromAddr();
    }
    if (!activeChat) { alert('Bitte eine Empfänger-Adresse eingeben.'); return; }
    const input = document.getElementById('msg-input');
    const text  = input.value.trim();
    if (!text) return;

    input.value = '';

    const resp = await fetch('/api/v1/messenger/send', { method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body:    JSON.stringify({
            recipient_id:      activeChat.fundusID,
            recipient_pub_key: activeChat.publicKey,
            text:              text,
            type:              'text',
        }),
    });

    let mid = null;
    try { if (resp.ok) mid = (await resp.json()).message_id; } catch (e) {}
    appendMessage({ id: mid, from: myIdentity.fundus_id, text, ts: new Date(), outgoing: true });
    if (window.playWhoop) window.playWhoop(); // Ton bei ausgehender Nachricht
}

// ======================================================================
//  Datei senden (via FileStore)
// ======================================================================
async function sendFile() {
    const input = document.getElementById('file-input');
    const file = input.files && input.files[0];
    input.value = ''; // dasselbe Foto später erneut wählbar (sonst kein change-Event)
    if (!file) return;
    if (!myIdentity) { showToast('Bitte zuerst anmelden', ''); return; }
    if (!activeChat) { showToast('Bitte zuerst einen Chat öffnen', ''); return; }
    const chat = { fundusID: activeChat.fundusID, publicKey: activeChat.publicKey };
    const st = appendSystemMessage('Lade ' + file.name + ' hoch… 0%');

    // Upload per XHR (mit Fortschritt – mobil bei großen Fotos wichtig).
    let contentHash;
    try {
        contentHash = await new Promise((resolve, reject) => {
            const xhr = new XMLHttpRequest();
            xhr.open('POST', '/api/v1/files/upload');
            xhr.withCredentials = true;
            xhr.upload.onprogress = (e) => {
                if (e.lengthComputable) st.textContent = 'Lade ' + file.name + ' hoch… ' + Math.round(e.loaded * 100 / e.total) + '%';
            };
            xhr.onload = () => {
                let d = {};
                try { d = JSON.parse(xhr.responseText || '{}'); } catch (e) {}
                if (xhr.status >= 200 && xhr.status < 300 && d.content_hash) resolve(d.content_hash);
                else reject(new Error(d.error || ('HTTP ' + xhr.status)));
            };
            xhr.onerror = () => reject(new Error('Netzwerkfehler'));
            xhr.ontimeout = () => reject(new Error('Zeitüberschreitung'));
            const fd = new FormData();
            fd.append('file', file);
            xhr.send(fd);
        });
    } catch (e) {
        st.textContent = '✗ ' + (MSGT.upload_failed || 'Upload fehlgeschlagen') + ': ' + e.message;
        return;
    }

    st.textContent = 'Sende ' + file.name + '…';
    let fmid = null;
    try {
        const fresp = await fetch('/api/v1/messenger/send', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                recipient_id: chat.fundusID, recipient_pub_key: chat.publicKey,
                file_hash: contentHash, file_name: file.name, file_size: file.size,
                mime_type: file.type || '', type: 'file',
            }),
        });
        if (!fresp.ok) {
            const d = await fresp.json().catch(() => ({}));
            throw new Error(d.error || ('HTTP ' + fresp.status));
        }
        try { fmid = (await fresp.json()).message_id; } catch (e) {}
    } catch (e) {
        st.textContent = '✗ Datei hochgeladen, aber Nachricht nicht gesendet: ' + e.message;
        return;
    }
    appendMessage({
        id: fmid, from: myIdentity.fundus_id, outgoing: true, ts: new Date(),
        file: { hash: contentHash, name: file.name, size: file.size, mime: file.type || '' }
    });
    st.textContent = '✓ ' + file.name + ' geteilt';
}

// ======================================================================
//  WebRTC Audio/Video
// ======================================================================
let iceServers = [{ urls: 'stun:stun.l.google.com:19302' }];
let iceHasTurn = false;
// ICE-Server (STUN + ggf. TURN) aus der Node-Konfiguration laden.
async function loadIceServers() {
    try {
        const r = await fetch('/api/v1/messenger/ice', { credentials: 'same-origin' });
        if (r.ok) {
            const d = await r.json();
            if (Array.isArray(d.iceServers) && d.iceServers.length) iceServers = d.iceServers;
            iceHasTurn = !!d.turn;
        }
    } catch (e) {}
}
loadIceServers();

// Gesprächspartner des laufenden/eingehenden Anrufs (unabhängig vom offenen
// Chat – ein Anruf kann kommen, während ein anderer Chat offen ist).
let callPeer = null;          // { fundusID, publicKey }
let pendingIce = [];          // ICE-Kandidaten vor setRemoteDescription
let pendingOffer = null;      // eingehendes Angebot bis "Annehmen"

function callError(e) {
    const m = (e && e.name === 'NotAllowedError') ? 'Zugriff auf Mikrofon/Kamera wurde verweigert.'
            : (e && e.name === 'NotFoundError')   ? 'Kein Mikrofon bzw. keine Kamera gefunden.'
            : ('Anruf fehlgeschlagen: ' + ((e && e.message) || e));
    showToast('📵 ' + m, '');
    hangup(true);
}

async function getMedia(video) {
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
        throw new Error('Mikrofon/Kamera sind nur über HTTPS verfügbar.');
    }
    return navigator.mediaDevices.getUserMedia({
        audio: true,
        video: video ? { facingMode: 'user', width: { ideal: 640 }, height: { ideal: 480 } } : false,
    });
}

function showCallUI(video) {
    const vc = document.getElementById('video-container');
    vc.style.display = 'block';
    vc.classList.toggle('audio-only', !video);
}

async function startCall(mode) {
    if (!myIdentity) { showToast('Bitte zuerst anmelden', ''); return; }
    if (!activeChat) { showToast('Bitte zuerst einen Chat öffnen', ''); return; }
    if (peerConn) { showToast('Es läuft bereits ein Anruf', ''); return; }
    isInitiator = true;
    callPeer = { fundusID: activeChat.fundusID, publicKey: activeChat.publicKey };
    try {
        localStream = await getMedia(mode === 'video');
        showCallUI(mode === 'video');
        document.getElementById('local-video').srcObject = localStream;
        peerConn = createPeerConnection();
        const offer = await peerConn.createOffer();
        await peerConn.setLocalDescription(offer);
        await sendSignal({ type: mode === 'video' ? 'call_video' : 'call_audio', sdp: JSON.stringify(offer) });
        showToast(mode === 'video' ? '📹 Rufe an…' : '🎙️ Rufe an…', callPeer.fundusID.slice(0, 10) + '…');
    } catch (e) { callError(e); }
}

function createPeerConnection() {
    const pc = new RTCPeerConnection({ iceServers });
    if (localStream) {
        localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
    }
    pc.ontrack = (ev) => {
        const rv = document.getElementById('remote-video');
        rv.srcObject = ev.streams[0];
        // Mobil: Wiedergabe ausdrücklich starten (Autoplay mit Ton ist sonst blockiert).
        const p = rv.play(); if (p && p.catch) p.catch(() => {});
    };
    pc.onicecandidate = async (ev) => {
        if (ev.candidate) {
            try { await sendSignal({ type: 'ice', ice: JSON.stringify(ev.candidate) }); } catch (e) {}
        }
    };
    pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'failed') {
            showToast('📵 Verbindung fehlgeschlagen', iceHasTurn ? 'Auch über den TURN-Server keine Verbindung.' : 'Beide Seiten hinter strengem NAT – ohne TURN-Server (FUNDUS_TURN_URLS) nicht erreichbar.');
            hangup();
        }
    };
    return pc;
}

async function flushIce() {
    const q = pendingIce; pendingIce = [];
    for (const c of q) { try { await peerConn.addIceCandidate(c); } catch (e) {} }
}

// Eingehender Anruf: erst nachfragen (Mobil-Browser erlauben Kamera/Mikrofon
// und Ton nur nach einer Nutzeraktion).
function showIncomingCall(from, video) {
    let box = document.getElementById('incoming-call');
    if (!box) {
        box = document.createElement('div');
        box.id = 'incoming-call';
        box.className = 'incoming-call';
        document.body.appendChild(box);
    }
    const name = (typeof contactName === 'function') ? contactName(from) : from.slice(0, 10) + '…';
    box.innerHTML = '<div class="ic-title">' + (video ? '📹 Videoanruf' : '🎙️ Audioanruf') + '</div>' +
        '<div class="ic-from">' + escapeHtml(name) + '</div>' +
        '<div class="ic-btns"><button class="btn btn-danger" id="ic-no">Ablehnen</button>' +
        '<button class="btn" id="ic-yes">Annehmen</button></div>';
    box.style.display = 'block';
    if (window.playPling) window.playPling();
    document.getElementById('ic-no').onclick = () => { box.style.display = 'none'; sendSignal({ type: 'hangup' }).catch(()=>{}); resetCall(); };
    document.getElementById('ic-yes').onclick = () => { box.style.display = 'none'; acceptCall(video); };
}

async function acceptCall(video) {
    if (!pendingOffer) return;
    try {
        localStream = await getMedia(video);
        showCallUI(video);
        document.getElementById('local-video').srcObject = localStream;
        peerConn = createPeerConnection();
        await peerConn.setRemoteDescription(pendingOffer);
        pendingOffer = null;
        await flushIce();
        const answer = await peerConn.createAnswer();
        await peerConn.setLocalDescription(answer);
        await sendSignal({ type: 'answer', sdp: JSON.stringify(answer) });
    } catch (e) { callError(e); }
}

async function handleSignal(msg) {
    // Seit dem Backend-Umbau kommt das Signal bereits entschlüsselt über den
    // WebSocket. Älterer Weg (verschlüsselt) als Rückfall.
    let signal = msg.decrypted ? msg.signal : null;
    if (!signal && !msg.decrypted) {
        const resp = await fetch('/api/v1/messenger/decrypt', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(msg) });
        if (!resp.ok) return;
        signal = (await resp.json()).signal;
    }
    if (!signal) return;
    const from = (msg.sender_id || '').toLowerCase();

    switch (signal.type) {
        case 'call_audio':
        case 'call_video':
            if (peerConn) { // besetzt
                const busyPeer = callPeer; callPeer = { fundusID: from, publicKey: msg.sender_pub_x || '' };
                sendSignal({ type: 'hangup' }).catch(()=>{}); callPeer = busyPeer; return;
            }
            isInitiator = false;
            callPeer = { fundusID: from, publicKey: msg.sender_pub_x || '' };
            pendingOffer = JSON.parse(signal.sdp);
            pendingIce = [];
            showIncomingCall(from, signal.type === 'call_video');
            break;
        case 'answer':
            if (peerConn) {
                await peerConn.setRemoteDescription(JSON.parse(signal.sdp));
                await flushIce();
            }
            break;
        case 'ice': {
            const cand = JSON.parse(signal.ice);
            if (peerConn && peerConn.remoteDescription) {
                try { await peerConn.addIceCandidate(cand); } catch (e) {}
            } else {
                pendingIce.push(cand); // Angebot/Antwort noch nicht gesetzt
            }
            break;
        }
        case 'hangup': {
            const ic = document.getElementById('incoming-call');
            if (ic) ic.style.display = 'none';
            hangup(true);
            break;
        }
    }
}

async function sendSignal(signal) {
    const to = callPeer || (activeChat ? { fundusID: activeChat.fundusID, publicKey: activeChat.publicKey } : null);
    if (!to) return;
    const r = await fetch('/api/v1/messenger/send', { method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ recipient_id: to.fundusID, recipient_pub_key: to.publicKey || '', signal, type: 'signal' }),
    });
    if (!r.ok) throw new Error('Signal konnte nicht gesendet werden (' + r.status + ')');
}

function resetCall() {
    callPeer = null; pendingIce = []; pendingOffer = null; isMuted = false; isCamOff = false;
}

// hangup(silent): silent=true → Gegenseite nicht erneut benachrichtigen.
function hangup(silent) {
    if (peerConn) {
        if (!silent) sendSignal({ type: 'hangup' }).catch(()=>{});
        peerConn.close();
        peerConn = null;
    }
    if (localStream) {
        localStream.getTracks().forEach(t => t.stop());
        localStream = null;
    }
    const vc = document.getElementById('video-container');
    if (vc) vc.style.display = 'none';
    const rv = document.getElementById('remote-video'); if (rv) rv.srcObject = null;
    resetCall();
}

function toggleMute() {
    isMuted = !isMuted;
    if (localStream) localStream.getAudioTracks().forEach(t => t.enabled = !isMuted);
    document.getElementById('mute-btn').textContent = isMuted ? '🔇' : '🎙️';
}

function toggleCamera() {
    isCamOff = !isCamOff;
    if (localStream) localStream.getVideoTracks().forEach(t => t.enabled = !isCamOff);
    document.getElementById('cam-btn').textContent = isCamOff ? '🚫' : '📷';
}

// ======================================================================
//  Kontakte
// ======================================================================
function addContact() {
    const idOrKey = document.getElementById('new-contact-id').value.trim();
    const alias   = document.getElementById('new-contact-alias').value.trim();
    if (!idOrKey) return;

    const fundusID = idOrKey.startsWith('04') ? deriveIDFromPubKey(idOrKey) : idOrKey;
    contacts[fundusID.toLowerCase()] = {
        fundusID,
        publicKey: idOrKey.startsWith('04') ? idOrKey : '',
        alias:     alias || fundusID.slice(0, 8) + '…',
        online:    false,
    };
    renderContacts();
    syncContacts(); // verschlüsselt am Node/Netz speichern (pro Wallet)
    document.getElementById('new-contact-id').value = '';
    document.getElementById('new-contact-alias').value = '';
}

// syncContacts speichert die Kontaktliste verschlüsselt (an die eigene Wallet
// gebunden, auf dem Node/im Netz) — überall wiederherstellbar.
async function syncContacts() {
    if (!myIdentity) return;
    try {
        const list = Object.values(contacts).map(c => ({
            fundusID: c.fundusID, publicKey: c.publicKey || '', alias: c.alias || ''
        }));
        await fetch('/api/v1/messenger/contacts/sync', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ contacts: list })
        });
    } catch(e) {}
}

// fetchMailbox holt wartende Offline-Nachrichten ab. Der Server entschlüsselt
// sie und schreibt sie in die persistente History; danach laden wir den aktiven
// Chat neu, damit die neuen Nachrichten erscheinen.
async function fetchMailbox() {
    if (!myIdentity) return;
    try {
        const r = await fetch('/api/v1/messenger/mailbox/fetch', {credentials:'same-origin'});
        if (!r.ok) return;
        const d = await r.json();
        if (d.fetched > 0 && activeChat) {
            loadHistory(activeChat.fundusID, true); // Verlauf mit neuen Nachrichten neu laden
        }
    } catch(e) {}
}

// loadContacts holt die verschlüsselte Kontaktliste der eigenen Wallet.
async function loadContacts() {
    if (!myIdentity) return;
    try {
        const r = await fetch('/api/v1/messenger/contacts/load', {credentials:'same-origin'});
        if (!r.ok) return;
        const d = await r.json();
        if (Array.isArray(d.contacts)) {
            d.contacts.forEach(c => {
                if (c && c.fundusID) {
                    contacts[c.fundusID.toLowerCase()] = {
                        fundusID: c.fundusID, publicKey: c.publicKey || '',
                        alias: c.alias || (c.fundusID.slice(0,8)+'…'), online: false
                    };
                }
            });
            renderContacts();
        }
    } catch(e) {}
}

function renderContacts() {
    const list = document.getElementById('contact-list');
    list.innerHTML = '';
    for (const c of Object.values(contacts)) {
        const div = document.createElement('div');
        div.className = 'contact-item' + (activeChat?.fundusID === c.fundusID ? ' active' : '');
        div.innerHTML = `
            <span class="contact-indicator" style="color:${c.online ? 'var(--color-success,#16a34a)' : '#aaa'}">●</span>
            <span class="contact-name">${escapeHtml(displayName(c.fundusID, c))}</span>
            ${c.unread ? '<span class="contact-unread">'+c.unread+'</span>' : ''}
        `;
        div.onclick = () => openChat(c);
        list.appendChild(div);
    }
}

function openChat(contact) {
    activeChat = contact;
    if (contact) { contact.unread = 0; } // ungelesen-Zähler zurücksetzen
    if (window.msgNotifyReset) window.msgNotifyReset(); // globalen Badge-Zähler leeren
    try { localStorage.removeItem('fnd_msg_unread'); } catch(e){}
    const noChat = document.getElementById('no-chat');
    if (noChat) noChat.style.display = 'none';
    document.getElementById('chat-header').style.display = 'flex';
    document.getElementById('input-row').style.display = 'flex';
    document.getElementById('chat-with-name').textContent = displayName(contact.fundusID, contact);
    const addrEl = document.getElementById('chat-with-addr');
    if (addrEl) addrEl.value = contact.fundusID;
    // Ungespeicherter Kontakt (Alias ist nur die gekürzte Adresse)? Dann seine ID
    // ins Speicherfeld eintragen, damit man ihn mit einem Namen sichern kann.
    const isUnsaved = !contact.alias || contact.alias === (contact.fundusID||'').slice(0,10)+'…';
    const saveInput = document.getElementById('new-contact-id');
    if (saveInput && isUnsaved && contact.fundusID) {
        saveInput.value = contact.fundusID;
    }
    document.getElementById('messages').innerHTML = '';
    historyOldestTs = null;
    historyHasMore  = true;
    historyLoading  = false;
    renderContacts();
    updateSendState(); // Eingabezeile entsperren (Adressfeld ist jetzt befüllt)
    loadHistory(contact.fundusID, true); // neueste 32 laden
}

// openChatFromURL verarbeitet ?to=<fundusID>&key=<ed25519-pubkey-hex> aus einem
// Marktplatz-Inserat: legt den Kontakt an (bzw. ergänzt den Schlüssel) und öffnet
// direkt den Chat mit dem Verkäufer/Partner.
function openChatFromURL() {
    const p = new URLSearchParams(window.location.search);
    let to  = (p.get('to')  || '').trim();
    const key = (p.get('key') || '').trim();
    if (!to && !key) return;
    // Normalfall: die fundusID kommt direkt aus dem Inserat mit (contact_fundus_id).
    // Falls nur ein Schlüssel da ist, versuchen wir die Ableitung — schlägt sie
    // fehl (Funktion fehlt/Format), brechen wir sauber ab statt zu crashen.
    if (!to && key) {
        if (typeof deriveIDFromPubKey === 'function') {
            try { to = deriveIDFromPubKey(key); } catch (e) { to = ''; }
        }
    }
    if (!to) return;
    const id = to.toLowerCase();
    const existing = contacts[id];
    contacts[id] = {
        fundusID: to,
        publicKey: key || (existing && existing.publicKey) || '',
        alias: (existing && existing.alias) || (to.slice(0, 8) + '…'),
        online: existing ? existing.online : false,
    };
    renderContacts();
    openChat(contacts[id]);
    // URL säubern, damit ein Reload nicht erneut denselben Chat erzwingt.
    if (window.history && window.history.replaceState) {
        window.history.replaceState({}, '', '/messenger');
    }
}

// Verlauf-Pagination (32 pro Seite, ältere beim Hochscrollen).
let historyOldestTs = null;   // ts des aktuell ältesten geladenen Eintrags
let historyHasMore  = true;
let historyLoading  = false;

async function loadHistory(peerID, initial) {
    if (historyLoading || (!initial && !historyHasMore)) return;
    historyLoading = true;
    try {
        let url = '/api/v1/messenger/history?peer=' + encodeURIComponent(peerID) + '&limit=32';
        if (!initial && historyOldestTs) {
            url += '&before=' + encodeURIComponent(historyOldestTs);
        }
        const resp = await fetch(url);
        if (!resp.ok) return;
        const data = await resp.json();
        const msgs = data.messages || [];
        historyHasMore = !!data.has_more;
        if (msgs.length === 0) return;

        const box = document.getElementById('messages');
        // Beim Nachladen: Scroll-Position halten (Höhe vor dem Einfügen merken).
        const prevHeight = box.scrollHeight;

        // Älteste-zuerst; beim Initial ans Ende anhängen, beim Nachladen vorne
        // einfügen (chronologisch über dem bisherigen Inhalt).
        const frag = document.createDocumentFragment();
        const unreadIncoming = [];
        for (const m of msgs) {
            frag.appendChild(buildBubble({
                id: m.id,
                from: m.peer, text: m.text,
                file: m.file_hash ? { hash: m.file_hash, name: m.file_name, size: m.file_size, mime: m.mime } : null,
                ts: new Date(m.ts), outgoing: m.out,
                deliveredAt: m.delivered_at, readAt: m.read_at
            }));
            // Eingehende Nachrichten dieses Chats als gelesen quittieren.
            if (!m.out && m.id) unreadIncoming.push(m.id);
        }
        if (initial) {
            box.appendChild(frag);
            box.scrollTop = box.scrollHeight;
        } else {
            box.insertBefore(frag, box.firstChild);
            box.scrollTop = box.scrollHeight - prevHeight; // Position halten
        }
        // Ältesten Zeitstempel als Cursor merken.
        historyOldestTs = msgs[0].ts;

        // Eingehende Nachrichten dieses Chats als gelesen quittieren.
        if (unreadIncoming.length) sendReadReceipts(unreadIncoming);
    } finally {
        historyLoading = false;
    }
}

// sendReadReceipts schickt Lesequittungen für die genannten empfangenen
// Nachrichten an den aktiven Chatpartner (best effort, ohne UI-Blockade).
async function sendReadReceipts(messageIds) {
    if (!activeChat || !messageIds || !messageIds.length) return;
    try {
        await fetch('/api/v1/messenger/read', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({
                peer_id:      activeChat.fundusID,
                peer_pub_key: activeChat.publicKey,
                message_ids:  messageIds,
            }),
        });
    } catch (e) { /* still */ }
}

// ======================================================================
//  Nachrichten anzeigen
// ======================================================================
// buildBubble erzeugt nur das Nachrichten-Element (ohne Anhängen) — wird sowohl
// vom Live-Pfad (appendMessage) als auch vom Verlauf-Nachladen (loadHistory)
// genutzt.
function buildBubble({ id, from, text, file, ts, outgoing, deliveredAt, readAt }) {
    const div  = document.createElement('div');
    div.className = 'msg-bubble ' + (outgoing ? 'outgoing' : 'incoming');
    if (id) div.dataset.msgId = id;

    let content = '';
    if (text)  content = emojifyText(escapeHtml(text));
    if (file) {
        const url  = '/api/v1/files/download/' + encodeURIComponent(file.hash);
        const mime = (file.mime || '').toLowerCase();
        const name = (file.name || '').toLowerCase();
        const isImage = mime.startsWith('image/') || /\.(jpe?g|png|gif|webp|bmp|avif)$/.test(name);
        const isVideo = mime.startsWith('video/') || /\.(mp4|webm|ogg|mov|m4v)$/.test(name);
        const sizeMB  = file.size ? (file.size / 1e6).toFixed(1) : '?';
        if (isImage) {
            // Bilder & GIFs inline (GIF animiert automatisch im <img>).
            content = `<a href="${url}" target="_blank" class="msg-media-link">
                         <img src="${url}" class="msg-media-img" loading="lazy" alt="${escapeHtml(file.name||'')}">
                       </a>`;
        } else if (isVideo) {
            // Video inline mit Steuerung (nutzt HTTP-Range → Seeking).
            content = `<video class="msg-media-video" controls preload="metadata" src="${url}"></video>`;
        } else {
            content = `📎 <a href="${url}" target="_blank">
                         ${escapeHtml(file.name)} (${sizeMB} MB)
                       </a>`;
        }
    }
    // Häkchen nur bei ausgehenden Nachrichten (WhatsApp-analog).
    const ticks = outgoing
        ? `<span class="msg-ticks" data-delivered="${deliveredAt||''}" data-read="${readAt||''}">${renderTicks(deliveredAt, readAt)}</span>`
        : '';
    div.innerHTML = `
        ${!outgoing && from ? '<span class="msg-sender">'+escapeHtml(contactName(from))+'</span>' : ''}
        <span class="msg-content">${content}</span><span class="msg-time">${ts.toLocaleTimeString('de-DE',{hour:'2-digit',minute:'2-digit',hour12:false})}${ticks}</span>
    `;
    if (outgoing) updateTickTitle(div, deliveredAt, readAt);
    return div;
}

// renderTicks gibt das Häkchen-Markup je Status zurück:
//   gesendet  = ein graues Häkchen (✓)
//   empfangen = zwei graue Häkchen (✓✓)
//   gelesen   = zwei blaue Häkchen (✓✓, eingefärbt)
function renderTicks(deliveredAt, readAt) {
    if (readAt)      return '<span class="tick tick-read">✓✓</span>';
    if (deliveredAt) return '<span class="tick tick-delivered">✓✓</span>';
    return '<span class="tick tick-sent">✓</span>';
}

// updateTickTitle setzt einen Tooltip mit den Zeitstempeln für empfangen/gelesen.
function updateTickTitle(div, deliveredAt, readAt) {
    const span = div.querySelector('.msg-ticks');
    if (!span) return;
    const parts = [];
    if (deliveredAt) parts.push((MSGT.delivered_at||'Delivered') + ': ' + new Date(deliveredAt).toLocaleString('de-DE',{hour12:false}));
    if (readAt)      parts.push((MSGT.read_at||'Read') + ': ' + new Date(readAt).toLocaleString('de-DE',{hour12:false}));
    span.title = parts.join('\n');
}

// applyReceipt aktualisiert die Häkchen einer ausgehenden Bubble per Nachricht-ID.
function applyReceipt(messageId, kind, ts) {
    const div = document.querySelector('.msg-bubble.outgoing[data-msg-id="' + messageId + '"]');
    if (!div) return;
    const span = div.querySelector('.msg-ticks');
    if (!span) return;
    const when = ts || new Date().toISOString();
    let delivered = span.dataset.delivered || '';
    let read      = span.dataset.read || '';
    if (kind === 'delivered' && !delivered) delivered = when;
    if (kind === 'read') {
        if (!delivered) delivered = when;
        read = when;
    }
    span.dataset.delivered = delivered;
    span.dataset.read      = read;
    span.innerHTML = renderTicks(delivered, read);
    updateTickTitle(div, delivered || null, read || null);
}

function appendMessage({ id, from, text, file, ts, outgoing, deliveredAt, readAt }) {
    const div = buildBubble({ id, from, text, file, ts, outgoing, deliveredAt, readAt });
    document.getElementById('messages').appendChild(div);
    div.scrollIntoView({ behavior: 'smooth', block: 'end' });
    return div;
}

function appendSystemMessage(text) {
    const div = document.createElement('div');
    div.className = 'msg-system';
    div.textContent = text;
    document.getElementById('messages').appendChild(div);
    return div;
}

function shortAddr(a) {
    if (!a) return '';
    return a.length > 14 ? a.slice(0,8) + '…' + a.slice(-4) : a;
}
function escapeHtml(s) {
    return s.replace(/[<>&"']/g, c => ({
        '<':'&lt;','>':'&gt;','&':'&amp;','"':'&quot;',"'":'&#39;'
    })[c]);
}

// emojifyText ersetzt Emoji-Zeichen im (bereits escapeten) Text durch OpenMoji-
// Grafiken. Nutzt \p{Emoji}-Erkennung; Ziffern/#/* werden ausgeschlossen.
function emojifyText(html) {
    try {
        return html.replace(/(\p{Extended_Pictographic}(\uFE0F|\u200D\p{Extended_Pictographic})*)/gu, m => emojiImg(m, 'emoji-inline'));
    } catch(e) { return html; } // ältere Browser ohne Unicode-Property-Escapes
}

// ── Wer ist im Netz online? ────────────────────────────────────────────────
// Jeder offene Messenger meldet sich alle 2 min. Kontakte bekommen einen
// grünen Punkt; Nutzer, die (noch) keine Kontakte sind, erscheinen unter
// "Im Netz online". Wer das nicht möchte: "Für andere sichtbar" abschalten.
let onlineSet = new Set();
let presenceTimer = null, onlineTimer = null;
function msgVisible() {
    try { return localStorage.getItem('fundus-msg-visible') !== '0'; } catch (e) { return true; }
}
function startPresence() {
    // Der Herzschlag läuft global (walletauth.js, alle Seiten) – hier nur die Liste.
    if (onlineTimer) return;
    loadOnline();
    onlineTimer = setInterval(loadOnline, 30000);
}
async function loadOnline() {
    if (!myIdentity) return;
    try {
        const r = await fetch('/api/v1/messenger/online', { credentials: 'same-origin' });
        if (!r.ok) return;
        const d = await r.json();
        onlineSet = new Set((d.online || []).map(e => String(e.fundus_id).toLowerCase()));
        for (const e of (d.online || [])) if (e.name) nameCache[String(e.fundus_id).toLowerCase()] = e.name;
        await loadNames(Object.keys(contacts));
        window._presenceDiag = { peers: d.peers || 0, known: d.known || 0 };
        for (const c of Object.values(contacts)) c.online = onlineSet.has(String(c.fundusID).toLowerCase());
        renderContacts();
        renderOnline();
    } catch (e) {}
}
function renderOnline() {
    const anchor = document.getElementById('contact-list');
    if (!anchor) return;
    let box = document.getElementById('online-list');
    if (!box) {
        box = document.createElement('div');
        box.id = 'online-list';
        box.className = 'online-list';
        anchor.parentNode.insertBefore(box, anchor.nextSibling);
    }
    const others = [...onlineSet].filter(fid => !contacts[fid]);
    const vis = msgVisible();
    let html = '<div class="online-head"><span>Im Netz online' + (others.length ? ' (' + others.length + ')' : '') + '</span>' +
        '<label class="online-vis" title="Andere sehen dich als online und können dich anschreiben">' +
        '<input type="checkbox" id="msg-visible"' + (vis ? ' checked' : '') + '> für andere sichtbar</label></div>';
    if (!others.length) {
        html += '<div class="online-empty">Gerade niemand außer deinen Kontakten.</div>';
    }
    const dg = window._presenceDiag;
    if (dg) {
        html += '<div class="online-empty" title="Wird die Liste auf verschiedenen Nodes unterschiedlich angezeigt, hat meist ein Node wenige oder keine Verbindungen.">' +
            'über ' + dg.peers + ' verbundene Node' + (dg.peers === 1 ? '' : 's') + (dg.peers === 0 ? ' – dieser Node ist nicht verbunden!' : '') + '</div>';
    }
    for (const fid of others) {
        html += '<div class="contact-item online-item" data-fid="' + escapeHtml(fid) + '">' +
            '<span class="contact-indicator" style="color:var(--color-success,#16a34a)">●</span>' +
            (nameCache[fid]
                ? '<span class="contact-name" title="' + escapeHtml(fid) + '">' + escapeHtml(nameCache[fid]) + ' <span class="mono online-id">' + escapeHtml(shortAddr(fid)) + '</span></span>'
                : '<span class="contact-name mono" title="' + escapeHtml(fid) + '">' + escapeHtml(shortAddr(fid)) + '</span>') +
            '<button class="online-add" title="Als Kontakt speichern">+</button></div>';
    }
    box.innerHTML = html;
    const cb = document.getElementById('msg-visible');
    if (cb) cb.onchange = () => {
        try { localStorage.setItem('fundus-msg-visible', cb.checked ? '1' : '0'); } catch (e) {}
        if (window.walletPresence) window.walletPresence(cb.checked); else publishPresence(cb.checked);
    };
    box.querySelectorAll('.online-item').forEach(el => {
        const fid = el.getAttribute('data-fid');
        el.onclick = (e) => {
            if (e.target.classList.contains('online-add')) {
                // Kontakt speichern: ID eintragen, Namen abfragen
                const alias = prompt('Name für diesen Kontakt (' + shortAddr(fid) + '):', nameCache[fid] || '');
                if (alias === null) return;
                document.getElementById('new-contact-id').value = fid;
                document.getElementById('new-contact-alias').value = alias;
                addContact();
                loadOnline();
                return;
            }
            openChat({ fundusID: fid, alias: fid.slice(0, 10) + '…', online: true });
        };
    });
}

async function publishPresence(online) {
    if (!myIdentity) return;
    await fetch('/api/v1/messenger/presence', { method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ online }),
    });
}

// Kein "offline" beim Verlassen der Seite: angemeldet bleibt man auf jeder Seite
// online (Herzschlag in walletauth.js). Offline: Abmelden oder 6 min Stille.

// Beim Hochscrollen nahe an den oberen Rand → ältere Nachrichten nachladen.
(function setupHistoryScroll() {
    const box = document.getElementById('messages');
    if (!box) return;
    box.addEventListener('scroll', () => {
        if (box.scrollTop < 80 && activeChat && historyHasMore && !historyLoading) {
            loadHistory(activeChat.fundusID, false);
        }
    });
})();

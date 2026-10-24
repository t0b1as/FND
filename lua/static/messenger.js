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
const avatarCache = {}; // fundusID (klein) → Prüfsumme des Profilbilds ('' = keins)
// Profilbild (oder Initiale) als kleiner Kreis.
function avatarHTML(fid, name) {
    const f = String(fid || '').toLowerCase();
    const h = avatarCache[f];
    if (h) return '<img class="msg-av" alt="" loading="lazy" src="/api/v1/identity/avatar/' + f + '?v=' + h + '" onerror="this.style.visibility=\'hidden\'">';
    const ch = String(name || '').replace(/^0x/i, '').trim().slice(0, 1).toUpperCase() || '?';
    return '<span class="msg-av msg-av-txt">' + escapeHtml(ch) + '</span>';
}
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
        for (const f of want) {
            nameCache[f] = (d.names && d.names[f]) || '';
            avatarCache[f] = (d.avatars && d.avatars[f]) || '';
        }
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
    // Verlaufsabgleich von einem anderen eigenen Node (R597): eigene wie
    // fremde Nachrichten, die dieses Gerät noch nicht kennt.
    if (msg.type === 'hist') {
        const peer = String(msg.peer_id || '').toLowerCase();
        if (!activeChat || String(activeChat.fundusID || '').toLowerCase() !== peer) return;
        appendMessage({
            id:   msg.id,
            from: msg.outgoing ? (myIdentity && myIdentity.fundus_id) : msg.peer_id,
            text: msg.text,
            file: msg.file_hash ? { hash: msg.file_hash, name: msg.file_name, size: msg.file_size, mime: msg.mime_type } : null,
            ts:   new Date(msg.ts),
            outgoing: !!msg.outgoing,
        });
        return;
    }
    if (msg.type === 'receipt') {
        applyReceipt(msg.receipt_for, msg.receipt_kind, msg.ts);
        return;
    }
    // Das Gegenüber hat eine Nachricht geändert oder entfernt (R662).
    if (msg.type === 'msgchange') {
        if (msg.clear) {   // R663: Gegenüber hat den Verlauf gelöscht
            document.querySelectorAll('.msg-bubble').forEach(b => b.remove());
            return;
        }
        const ziel = msg.edit_for || msg.delete_for;
        const b = ziel && document.querySelector(
            '.msg-bubble[data-msg-id="' + CSS.escape(String(ziel)) + '"]');
        if (b) {
            if (msg.delete_for) b.remove();
            else msgTextSetzen(b, msg.text || '', true);
        }
        return;
    }

    // R800: NUR echte Nachrichten als Nachricht darstellen.
    //
    // Über dieselbe Live-Verbindung laufen auch Ereignisse anderer Bereiche,
    // etwa Fahrten (type: "drive"). Alles, was oben nicht abgefangen wurde,
    // galt bisher als Nachricht – ein Fahrtereignis erschien dann als leeres
    // Feld ohne Absender ("?") und ohne Zeit ("Invalid Date"). Kam dasselbe
    // Ereignis vom eigenen Node UND von den übrigen Nodes des Kontos, stand
    // es mehrfach da, weil es keine Kennung zum Zusammenführen hatte.
    //
    // Eine Nachricht hat einen Typ text oder file (ältere ohne Typ), eine
    // Kennung und einen Absender. Alles andere gehört nicht in den Verlauf.
    if (msg.type && msg.type !== 'text' && msg.type !== 'file') return;
    if (!msg.id || !msg.sender_id) return;

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
        // Anzeigenamen/Profilbild des Absenders nachladen (sonst nur Adresse).
        await loadNames([sid]).catch(function(){});
        renderContacts();
        const who = displayName(sid, known);
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
document.addEventListener('DOMContentLoaded', function(){
    const inp = document.getElementById('msg-input');
    if (inp) inp.addEventListener('input', function(){ if (inp.value) noteTyping(); });
});
function updateSendState() {
    // Ein gesperrter Senden-Knopf reagiert gar nicht – ohne Hinweis sieht das
    // aus, als ginge die Nachricht verloren (R612).
    try {
        const b = document.getElementById('send-btn');
        if (b) b.title = (!myIdentity && !activeChat)
            ? 'Bitte zuerst anmelden oder einen Empfänger wählen'
            : 'Senden';
    } catch (e) {}
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
    // R614: Knopf NICHT mehr sperren. Ein gesperrter Knopf reagiert gar nicht,
    // und auf dem Handy gibt es auch keinen Hinweistext – jeder Sendeversuch
    // verpuffte dann lautlos. Den Grund nennt jetzt sendText() als Meldung.
    if (btn) btn.disabled = false;
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
    // Unfertige/ungültige Adresse: Feld stehen lassen, kurzer Hinweis.
    if (!/^0x[0-9a-f]{40}$/.test(addr)) {
        const nameEl2 = document.getElementById('chat-with-name');
        if (nameEl2) nameEl2.textContent = 'Adresse unvollständig (0x + 40 Zeichen) oder E-Mail eingeben';
        activeChat = null; updateSendState(); return;
    }
    // Derselbe Weg wie beim Klick auf einen Kontakt: Kopf mit Bild, großem Namen
    // und kleiner Adresse (früher blieb hier das Adressfeld stehen und der Name
    // erschien klein darunter).
    const known = contacts[addr];
    openChat(known || {
        fundusID:  addr,
        publicKey: document.getElementById('chat-with-addr').dataset.resolvedKey || '',
        alias:     addr.slice(0,10)+'…'
    });
}

async function sendText() {
    if (!myIdentity) { showToast('✗ Nicht angemeldet – bitte oben rechts anmelden', ''); return; }
    // Falls kein aktiver Chat, aber eine Adresse im Feld → daraus Empfänger machen.
    // setRecipientFromAddr ist async (E-Mail-Auflösung), daher AWAIT — sonst ist
    // activeChat beim folgenden Check noch nicht gesetzt und das Senden bricht ab.
    if (!activeChat) {
        const addr = (document.getElementById('chat-with-addr').value || '').trim().toLowerCase();
        if (addr) await setRecipientFromAddr();
    }
    if (!activeChat) { showToast('✗ Kein Empfänger gewählt', 'Adresse oben eingeben oder Kontakt anklicken'); return; }
    const input = document.getElementById('msg-input');
    let text  = input.value.trim();
    if (!text) return;
    text = antwortVoranstellen(text);   // R663: Zitat voranstellen, falls geantwortet wird

    input.value = '';
    sendeKnopfAnpassen();   // R668: wieder Aufnahmepunkt zeigen

    // R612: Die Antwort wurde bisher NICHT geprüft – scheiterte das Senden
    // (abgelehnt, nicht angemeldet, Netzfehler), erschien die Nachricht
    // trotzdem im Fenster, als sei sie raus. Genau so sieht "kommt nicht an"
    // aus. Jetzt wird nur bei Erfolg angezeigt, sonst der Text zurückgegeben
    // und der Grund genannt.
    let mid = null;
    try {
        const resp = await fetch('/api/v1/messenger/send', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({
                recipient_id:      activeChat.fundusID,
                recipient_pub_key: activeChat.publicKey,
                text:              text,
                type:              'text',
            }),
        });
        let d = {};
        try { d = await resp.json(); } catch (e) {}
        if (!resp.ok) throw new Error(d.error || ('Node antwortete mit ' + resp.status));
        mid = d.message_id || null;
    } catch (e) {
        input.value = text;                       // Text nicht verlieren
        showToast('✗ Nicht gesendet: ' + e.message, '');
        return;
    }
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

// R666: Jede Stufe des Anrufaufbaus wird protokolliert. "Kameraanfrage kommt,
// sonst nichts" kann an sechs Stellen hängen – ohne Spur ist das Raten.
function callLog(stufe, zusatz) {
    const z = '[Anruf] ' + stufe + (zusatz ? ': ' + zusatz : '');
    console.log(z);
    window.__anrufSpur = (window.__anrufSpur || []);
    window.__anrufSpur.push(new Date().toISOString().slice(11, 23) + ' ' + z);
}

// R681: Schlüssel nachschlagen, falls er fehlt. Aus dem Namensmenü (R665) und
// aus der Online-Liste entstehen Kontakte OHNE öffentlichen Schlüssel – ein
// Anruf an so einen Kontakt konnte das Signal nicht verschlüsseln und scheiterte
// stumm, also ohne Klingeln auf der Gegenseite.
async function schluesselSichern(kontakt) {
    if (!kontakt || kontakt.publicKey) return kontakt;
    const bekannt = (typeof contacts === 'object' && contacts) ? contacts[String(kontakt.fundusID).toLowerCase()] : null;
    if (bekannt && bekannt.publicKey) { kontakt.publicKey = bekannt.publicKey; return kontakt; }
    try {
        const r = await fetch('/api/v1/identity/keydir/lookup?fundus_id=' +
                              encodeURIComponent(kontakt.fundusID), { credentials: 'same-origin' });
        const d = await r.json();
        if (d && d.found && d.x25519) {
            kontakt.publicKey = d.x25519;
            callLog('Schlüssel nachgeschlagen', kontakt.fundusID.slice(0, 10));
        }
    } catch (e) { callLog('Schlüssel nicht gefunden', e.message || ''); }
    return kontakt;
}

async function startCall(mode) {
    callLog('gestartet', mode);
    await schluesselSichern(activeChat);
    if (!window.isSecureContext) {
        // Ohne HTTPS gibt der Browser weder Kamera noch Mikrofon frei – auf dem
        // Rechner oft übersehen, weil localhost als sicher gilt.
        showToast('📵 Anruf nur über HTTPS möglich',
                  'Diese Seite läuft über http. Kamera und Mikrofon bleiben gesperrt.');
        callLog('abgebrochen', 'kein sicherer Kontext');
        return;
    }
    if (!myIdentity) { showToast('Bitte zuerst anmelden', ''); return; }
    if (!activeChat) { showToast('Bitte zuerst einen Chat öffnen', ''); return; }
    if (peerConn) { showToast('Es läuft bereits ein Anruf', ''); return; }
    isInitiator = true;
    callPeer = { fundusID: activeChat.fundusID, publicKey: activeChat.publicKey };
    try {
        localStream = await getMedia(mode === 'video');
        callLog('Medien erhalten', localStream.getTracks().map(t => t.kind).join('+'));
        showCallUI(mode === 'video');
        const lv = document.getElementById('local-video');
        if (!lv) throw new Error('Anruffenster fehlt in der Seite (local-video)');
        lv.srcObject = localStream;
        peerConn = createPeerConnection();
        const offer = await peerConn.createOffer();
        await peerConn.setLocalDescription(offer);
        callLog('Angebot erstellt', offer.type);
        await sendSignal({ type: mode === 'video' ? 'call_video' : 'call_audio', sdp: JSON.stringify(offer) });
        callLog('Angebot verschickt', callPeer.fundusID.slice(0, 12));
        waehltonAn();   // R666: hörbare Rückmeldung, dass es klingelt
        showToast(mode === 'video' ? '📹 Rufe an…' : '🎙️ Rufe an…', callPeer.fundusID.slice(0, 10) + '…');
    } catch (e) { callError(e); }
}

function createPeerConnection() {
    const pc = new RTCPeerConnection({ iceServers });
    if (localStream) {
        localStream.getTracks().forEach(t => pc.addTrack(t, localStream));
    }
    pc.ontrack = (ev) => {
        tonAus();   // R666: es ist verbunden – Wähl-/Anrufton beenden
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
    pc.oniceconnectionstatechange = () => callLog('ICE', pc.iceConnectionState);
    eisBeobachten(pc);   // R677: Aussetzer aussitzen statt auflegen
    pc.onicegatheringstatechange = () => callLog('ICE-Sammlung', pc.iceGatheringState);
    pc.onconnectionstatechange = () => {
        callLog('Verbindung', pc.connectionState);
        if (pc.connectionState === 'connected') {
            _eisNeu = 0;                 // wieder da: Zähler zurücksetzen
            wakeLockAnfordern();
        }
        if (pc.connectionState === 'failed') {
            // R677: Nicht sofort auflegen. Nach einem Bildschirmwechsel kommt
            // der Browser oft mit abgerissenen Kandidaten zurück – ein
            // Neuaufbau der Wege (ICE-Restart) rettet das Gespräch meist.
            // Erst wenn auch der scheitert, ist es wirklich vorbei.
            if (eisNeuaufbau()) return;
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
    anruftonAn();   // R666: dauerhafter Anrufton statt eines einzelnen Tons
    document.getElementById('ic-no').onclick = () => { box.style.display = 'none'; sendSignal({ type: 'hangup' }).catch(()=>{}); resetCall(); };
    document.getElementById('ic-yes').onclick = () => { box.style.display = 'none'; acceptCall(video); };
}

async function acceptCall(video) {
    // R681: auch hier – der Anrufer ist womöglich noch kein vollständiger
    // Kontakt, und ohne Schlüssel käme unsere Antwort nicht an.
    await schluesselSichern(callPeer);
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
            callLog('Anruf empfangen – es klingelt', from.slice(0, 12));
            showIncomingCall(from, signal.type === 'call_video');
            break;
        case 'answer':
            if (peerConn) {
                await peerConn.setRemoteDescription(JSON.parse(signal.sdp));
                await flushIce();
            }
            break;
        // R677: Die Gegenseite baut die Wege neu auf (nach Bildschirmsperre
        // oder App-Wechsel). Wir nehmen das Angebot an und antworten – ohne
        // diesen Zweig liefe der Neuaufbau ins Leere und das Gespräch wäre
        // trotzdem vorbei.
        case 'ice_restart':
            if (peerConn) {
                try {
                    await peerConn.setRemoteDescription(JSON.parse(signal.sdp));
                    const antwort = await peerConn.createAnswer();
                    await peerConn.setLocalDescription(antwort);
                    await sendSignal({ type: 'answer', sdp: JSON.stringify(antwort) });
                    await flushIce();
                    callLog('Neuaufbau beantwortet', '');
                } catch (e) {
                    callLog('Neuaufbau nicht annehmbar', e.message || '');
                }
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
        case 'typing':
            showTyping(from);
            return;
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
    tonAus();   // R666
    wakeLockFreigeben();          // R677: Bildschirm darf wieder schlafen
    clearTimeout(_eisTimer); _eisNeu = 0;
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
        div.dataset.fid = String(c.fundusID || '').toLowerCase();   // R665: fürs Kontextmenü
        div.innerHTML = `
            <span class="msg-av-wrap">${avatarHTML(c.fundusID, displayName(c.fundusID, c))}<span class="contact-indicator msg-av-dot" style="color:${c.online ? 'var(--color-success,#16a34a)' : '#aaa'}">●</span></span>
            <span class="contact-name">${escapeHtml(displayName(c.fundusID, c))}</span>
            ${c.unread ? '<span class="contact-unread">'+c.unread+'</span>' : ''}
        `;
        div.onclick = () => openChat(c);
        list.appendChild(div);
    }
}

// Chat-Kopf: Bild + Anzeigename groß, Adresse klein (Klick kopiert).
function renderChatHeader(contact) {
    const fid = String(contact.fundusID || '').toLowerCase();
    const name = displayName(fid, contact);
    const nameEl = document.getElementById('chat-with-name');
    if (nameEl) nameEl.textContent = name;
    const sub = document.getElementById('chat-with-sub');
    if (sub) {
        sub.textContent = fid ? fid.slice(0, 10) + '…' + fid.slice(-6) : '';
        sub.onclick = function(){ if (navigator.clipboard) navigator.clipboard.writeText(fid); sub.textContent = '✓ Adresse kopiert'; setTimeout(function(){ sub.textContent = fid.slice(0, 10) + '…' + fid.slice(-6); }, 1200); };
    }
    const avSlot = document.getElementById('chat-avatar');
    if (avSlot) avSlot.innerHTML = avatarHTML(fid, name);
    const hdr = document.getElementById('chat-header');
    if (hdr) hdr.classList.add('has-chat');
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
    renderChatHeader(contact);
    // Name/Bild noch unbekannt? Nachladen und Kopf + Liste aktualisieren.
    loadNames([contact.fundusID]).then(function(){
        if (activeChat === contact) renderChatHeader(contact);
        renderContacts();
    }).catch(function(){});
    // Mobil: Chat bildschirmfüllend (← führt zurück zur Liste)
    const lay = document.querySelector('.msg-layout');
    if (lay) lay.classList.add('chat-open');
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
    _lastDateKey = null; _lastFrom = null; hideTyping();
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
        // R649: ?name= wurde bisher ignoriert – ein aus Drive oder dem
        // Marktplatz geöffneter Chat zeigte nur die gekürzte Adresse, obwohl
        // der Name bekannt war.
        alias: (existing && existing.alias) ||
               ((new URLSearchParams(window.location.search).get('name') || '').trim()) ||
               (to.slice(0, 8) + '…'),
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
            // Schon im Fenster? Beim ersten Senden wird der Verlauf direkt
            // danach nachgeladen – die eben gesendete Nachricht erschien sonst
            // ein zweites Mal (R603). Der Verlauf nutzt buildBubble statt
            // appendMessage, die dortige Prüfung griff hier also nicht.
            if (m.id && document.querySelector('.msg-bubble[data-msg-id="' + CSS.escape(String(m.id)) + '"]')) continue;
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
    if (text) {
        // R663: Führende Zeilen mit "> " sind ein Zitat (Antworten-Funktion)
        // und werden abgesetzt dargestellt statt als gewöhnlicher Text.
        const zeilen = String(text).split('\n');
        let zitat = '';
        while (zeilen.length > 1 && zeilen[0].startsWith('> ')) {
            zitat += (zitat ? '\n' : '') + zeilen.shift().slice(2);
        }
        content = (zitat ? zitatHtml(zitat) : '')
                // R797: ERST verlinken, DANN Emojis. Umgekehrt fand verlinke()
                // die Bildadresse (https://cdn.jsdelivr.net/…) MITTEN im eben
                // erzeugten Emoji-<img> und machte daraus einen Link – das Tag
                // zerbrach, und der Rest stand als Text in der Nachricht. Betraf
                // jede Nachricht mit Emoji seit der Verlinkung (R663).
                + emojifyText(verlinke(escapeHtml(zeilen.join('\n'))));
    }
    if (file) {
        const url  = '/api/v1/files/download/' + encodeURIComponent(file.hash);
        const mime = (file.mime || '').toLowerCase();
        const name = (file.name || '').toLowerCase();
        const isImage = mime.startsWith('image/') || /\.(jpe?g|png|gif|webp|bmp|avif)$/.test(name);
        // R667: Ton ZUERST prüfen. Eine Sprachnachricht als .webm traf sonst auf
        // die Videoerkennung (dort steht webm ebenfalls) und wurde als Video
        // dargestellt – schwarzer Kasten mit weißer Bedienleiste.
        const isAudio = mime.startsWith('audio/') || /\.(mp3|m4a|aac|wav|flac|opus)$/.test(name) ||
                        (/\.(webm|ogg)$/.test(name) && mime.indexOf('video') < 0);
        const isVideo = !isAudio && (mime.startsWith('video/') || /\.(mp4|webm|ogg|mov|m4v)$/.test(name));
        const sizeMB  = file.size ? (file.size / 1e6).toFixed(1) : '?';
        // R675: Hintergrund nur bei Bild und Video entfernen. Vorher galt das
        // für jede Nachricht ohne Text, also auch für Sprachnachrichten – der
        // Abspieler erbt seine Farbe von der Blase und war dadurch schwarz auf
        // schwarz. Ton braucht die grüne Blase.
        if (!text && (isImage || isVideo)) div.classList.add('nur-medien');

        if (isImage) {
            // Bilder & GIFs inline (GIF animiert automatisch im <img>).
            // R687: Im Vordergrund vergrößern statt in einem neuen Fenster
            // öffnen. Ein neuer Reiter reißt aus dem Gespräch heraus und ist
            // auf dem Handy besonders lästig – man muss erst wieder
            // zurückwechseln.
            content = `<a href="#" class="msg-media-link"
                          onclick="bildGross('${file.hash}','${escapeHtml(file.name||'')}','${mime}');return false;">
                         <img src="${url}" class="msg-media-img" loading="lazy" alt="${escapeHtml(file.name||'')}">
                       </a>`;
        } else if (isAudio) {
            content = tonAbspielerHtml(url, file);
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

// ── Feinschliff (R555): Datums-Trenner, Avatare, "schreibt …" ─────────────
let _lastDateKey = null, _lastFrom = null;
function dateLabel(d) {
    const now = new Date(), y = new Date(now); y.setDate(now.getDate() - 1);
    const same = (a, b) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
    if (same(d, now)) return 'Heute';
    if (same(d, y)) return 'Gestern';
    return d.toLocaleDateString('de-DE', { weekday: 'short', day: '2-digit', month: '2-digit', year: 'numeric' });
}
function appendMessage({ id, from, text, file, ts, outgoing, deliveredAt, readAt }) {
    // Dieselbe Nachricht kann über mehrere Wege eintreffen (direkt, Postfach,
    // Verlaufsabgleich der eigenen Nodes). Einmal anzeigen genügt (R599).
    if (id && document.querySelector('.msg-bubble[data-msg-id="' + CSS.escape(String(id)) + '"]')) return null;
    // Absicherung (R600): Stammt die Nachricht von der eigenen Fundus-ID, ist
    // sie eigen – unabhängig davon, welcher Zustellweg sie geliefert hat.
    if (!outgoing && myIdentity && from &&
        String(from).toLowerCase() === String(myIdentity.fundus_id || '').toLowerCase()) {
        outgoing = true;
    }
    const box = document.getElementById('messages');
    const d = (ts instanceof Date) ? ts : new Date(ts);
    const key = isNaN(d) ? null : d.toDateString();
    if (key && key !== _lastDateKey) {
        const sep = document.createElement('div'); sep.className = 'msg-date'; sep.innerHTML = '<span>' + escapeHtml(dateLabel(d)) + '</span>';
        box.appendChild(sep); _lastDateKey = key; _lastFrom = null;
    }
    const div = buildBubble({ id, from, text, file, ts, outgoing, deliveredAt, readAt });
    const who = outgoing ? 'me' : String(from || '').toLowerCase();
    if (!outgoing) {
        const row = document.createElement('div'); row.className = 'msg-row' + (who === _lastFrom ? ' cont' : '');
        const nm = (typeof nameCache !== 'undefined' && nameCache[who]) || (activeChat && activeChat.name) || who;
        row.innerHTML = '<div class="msg-row-av">' + (who === _lastFrom ? '' : avatarHTML(who, nm)) + '</div>';
        row.appendChild(div); box.appendChild(row);
    } else {
        box.appendChild(div);
    }
    _lastFrom = who;
    div.scrollIntoView({ behavior: 'smooth', block: 'end' });
    return div;
}
// "schreibt …": beim Tippen höchstens alle 4 s ein Signal; Anzeige 6 s.
let _typingSentAt = 0, _typingTimer = null;
function noteTyping() {
    if (!activeChat || !activeChat.fundusID) return;
    const now = Date.now(); if (now - _typingSentAt < 4000) return;
    _typingSentAt = now; sendSignal({ type: 'typing' }).catch(() => {});
}
function showTyping(from) {
    if (!activeChat || String(from || '').toLowerCase() !== String(activeChat.fundusID || '').toLowerCase()) return;
    let el = document.getElementById('typing-ind');
    if (!el) {
        el = document.createElement('div'); el.id = 'typing-ind';
        el.className = 'typing-ind'; el.textContent = 'schreibt …';
        // R684: NICHT mehr in die Kopfzeile. Dort landete der Hinweis ganz
        // hinten, also hinter den Anrufknöpfen – genau der unlesbare Text, der
        // zwischen den Knöpfen durchschien. Er gehört über die Schreibzeile,
        // wo er zum Geschehen passt und niemanden verdeckt.
        const ziel = document.querySelector('.msg-input-row');
        if (ziel && ziel.parentNode) ziel.parentNode.insertBefore(el, ziel);
        else {
            const h = document.getElementById('chat-header');
            if (h) h.appendChild(el); else return;
        }
    }
    el.style.display = '';
    clearTimeout(_typingTimer); _typingTimer = setTimeout(hideTyping, 6000);
}
function hideTyping() { const el = document.getElementById('typing-ind'); if (el) el.style.display = 'none'; }

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
        for (const e of (d.online || [])) {
            const f = String(e.fundus_id).toLowerCase();
            if (e.name) nameCache[f] = e.name;
            if (e.avatar_hash) avatarCache[f] = e.avatar_hash;
        }
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
            '<span class="msg-av-wrap">' + avatarHTML(fid, nameCache[fid] || fid) + '<span class="contact-indicator msg-av-dot" style="color:var(--color-success,#16a34a)">●</span></span>' +
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

// Mobil: zurück von der Chat-Ansicht zur Kontaktliste.
function msgShowList() {
    const lay = document.querySelector('.msg-layout');
    if (lay) lay.classList.remove('chat-open');
}

// Neuen Chat per Adresse beginnen: Kopf zurück in den Eingabezustand.
function msgNewByAddr() {
    const hdr = document.getElementById('chat-header');
    if (hdr) hdr.classList.remove('has-chat');
    ['chat-with-name', 'chat-with-sub'].forEach(function(id){ const e = document.getElementById(id); if (e) e.textContent = ''; });
    const av = document.getElementById('chat-avatar'); if (av) av.innerHTML = '';
    const inp = document.getElementById('chat-with-addr');
    if (inp) { inp.value = ''; inp.focus(); }
    activeChat = null; updateSendState();
}

// ── Nachricht bearbeiten, löschen, Verlauf leeren (R662) ──────────────────
//
// Bedienung: am Rechner Rechtsklick auf die Blase, am Handy langes Antippen.
// Beides öffnet dasselbe Menü, damit es nur eine Stelle gibt, die gepflegt
// werden muss.

let _menuOffen = null;

function msgMenuSchliessen() {
    if (_menuOffen) { _menuOffen.remove(); _menuOffen = null; }
}
document.addEventListener('click', msgMenuSchliessen);
document.addEventListener('scroll', msgMenuSchliessen, true);

function msgMenuOeffnen(blase, x, y) {
    msgMenuSchliessen();
    const id = blase.dataset.msgId;
    if (!id) return;
    const eigen = blase.classList.contains('outgoing');
    const hatText = !!blase.querySelector('.msg-text, .bubble-text') ||
                    !blase.querySelector('.msg-file, a[download]');

    const punkte = [];
    punkte.push(['↩ Antworten', () => msgAntworten(blase)]);
    punkte.push(['➦ Weiterleiten', () => msgWeiterleiten(blase)]);
    punkte.push(['⧉ Kopieren', () => msgKopieren(blase)]);
    if (eigen && hatText) punkte.push(['✎ Bearbeiten', () => msgBearbeiten(blase, id)]);
    punkte.push(['🗑 Bei mir löschen', () => msgLoeschen(id, false)]);
    if (eigen) punkte.push(['🗑 Bei beiden löschen', () => msgLoeschen(id, true)]);

    const m = document.createElement('div');
    m.className = 'msg-menu';
    punkte.forEach(([beschriftung, fn]) => {
        const b = document.createElement('button');
        b.textContent = beschriftung;
        b.onclick = (ev) => { ev.stopPropagation(); msgMenuSchliessen(); fn(); };
        m.appendChild(b);
    });
    document.body.appendChild(m);
    // Innerhalb des Fensters halten – sonst ragt es am rechten oder unteren
    // Rand heraus und ist nicht erreichbar.
    const r = m.getBoundingClientRect();
    m.style.left = Math.max(8, Math.min(x, window.innerWidth  - r.width  - 8)) + 'px';
    m.style.top  = Math.max(8, Math.min(y, window.innerHeight - r.height - 8)) + 'px';
    _menuOffen = m;
}

function aktuellerPeer() {
    // R663: Vorher stand hier window.activeChat – activeChat ist aber mit let
    // angelegt und liegt damit NICHT auf window. Die Adresse war dadurch immer
    // leer und jede Aktion lief ins Leere, ohne dass man den Grund sah.
    return (activeChat && (activeChat.fundusID || activeChat.id)) || '';
}

// Reinen Text einer Blase holen – ohne Uhrzeit, Haken und Bearbeitet-Vermerk.
function blasenText(blase) {
    const kopie = blase.cloneNode(true);
    kopie.querySelectorAll('.msg-time, .bubble-time, .msg-status, .msg-edited').forEach(e => e.remove());
    return kopie.innerText.trim();
}

function msgKopieren(blase) {
    const t = blasenText(blase);
    if (!t) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(t).catch(() => msgKopierenNotfalls(t));
    } else {
        msgKopierenNotfalls(t);
    }
}

// Ohne sicheren Kontext gibt es die Zwischenablage nicht – dann über ein
// unsichtbares Feld, sonst passiert beim Kopieren wortlos nichts (R663).
function msgKopierenNotfalls(t) {
    const f = document.createElement('textarea');
    f.value = t; f.style.position = 'fixed'; f.style.opacity = '0';
    document.body.appendChild(f); f.select();
    try { document.execCommand('copy'); } catch (e) {}
    f.remove();
}

// ── Antworten (R663) ─────────────────────────────────────────────────────
// Die zitierte Stelle wird dem Text vorangestellt. Bewusst ohne Erweiterung
// des Protokolls: So sehen auch ältere Nodes das Zitat, und es gibt keine
// Nachricht, die auf eine inzwischen gelöschte verweist.
let _antwortAuf = null;

function msgAntworten(blase) {
    const t = blasenText(blase);
    if (!t) return;
    // R666: Kennung mitführen, damit das Zitat ein Sprungziel bekommt.
    const id = blase.dataset.msgId || '';
    _antwortAuf = (id ? '[' + id + '] ' : '') + t.split('\n')[0].slice(0, 120);
    antwortLeisteZeigen();
    const feld = document.getElementById('msg-input') || document.querySelector('.msg-input');
    if (feld) feld.focus();
}

function antwortAbbrechen() {
    _antwortAuf = null;
    const l = document.getElementById('antwort-leiste');
    if (l) l.remove();
}

function antwortLeisteZeigen() {
    antwortLeisteEntfernenStill();
    const anker = document.querySelector('.msg-compose, .msg-input-row, #msg-form');
    if (!anker || !_antwortAuf) return;
    const l = document.createElement('div');
    l.id = 'antwort-leiste';
    l.className = 'antwort-leiste';
    const txt = document.createElement('span');
    txt.textContent = '↩ ' + _antwortAuf.replace(/^\[[^\]]*\]\s?/, '');
    const x = document.createElement('button');
    x.textContent = '✕'; x.title = 'Antwort verwerfen';
    x.onclick = antwortAbbrechen;
    l.appendChild(txt); l.appendChild(x);
    anker.parentNode.insertBefore(l, anker);
}

function antwortLeisteEntfernenStill() {
    const l = document.getElementById('antwort-leiste');
    if (l) l.remove();
}

// Wird vor dem Senden aufgerufen: stellt das Zitat voran und räumt auf.
function antwortVoranstellen(text) {
    if (!_antwortAuf) return text;
    const zitat = '> ' + _antwortAuf + '\n';
    _antwortAuf = null;
    antwortLeisteEntfernenStill();
    return zitat + text;
}

// ── Weiterleiten (R663) ──────────────────────────────────────────────────
async function msgWeiterleiten(blase) {
    const t = blasenText(blase);
    if (!t) return;
    const ziel = await kontaktWaehlen('Weiterleiten an:');
    if (!ziel) return;
    try {
        const r = await fetch('/api/v1/messenger/send', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                recipient_id: ziel.fundusID,
                recipient_pub_key: ziel.publicKey || '',
                text: t, type: 'text',
            }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        if (typeof toast === 'function') toast('Weitergeleitet', 'an ' + (ziel.alias || ziel.fundusID));
    } catch (e) { alert('✗ ' + e.message); }
}

// Auswahl aus den eigenen Kontakten; ohne Kontakte Eingabe einer Adresse.
function kontaktWaehlen(titel) {
    return new Promise((fertig) => {
        const liste = Object.values(typeof contacts === 'object' && contacts ? contacts : {});
        if (!liste.length) {
            const a = prompt(titel + ' (Fundus-Adresse)');
            fertig(a ? { fundusID: a.trim() } : null);
            return;
        }
        msgMenuSchliessen();
        const hg = document.createElement('div');
        hg.className = 'kontakt-waehler';
        const box = document.createElement('div');
        box.className = 'kw-box';
        const h = document.createElement('div');
        h.className = 'kw-titel'; h.textContent = titel;
        box.appendChild(h);
        liste.forEach((k) => {
            const b = document.createElement('button');
            b.textContent = k.alias || k.fundusID;
            b.onclick = () => { hg.remove(); fertig(k); };
            box.appendChild(b);
        });
        const ab = document.createElement('button');
        ab.className = 'kw-ab'; ab.textContent = 'Abbrechen';
        ab.onclick = () => { hg.remove(); fertig(null); };
        box.appendChild(ab);
        hg.appendChild(box);
        hg.onclick = (ev) => { if (ev.target === hg) { hg.remove(); fertig(null); } };
        document.body.appendChild(hg);
    });
}

async function msgBearbeiten(blase, id) {
    const alt = blase.innerText.replace(/\s*\d{1,2}:\d{2}\s*$/, '').replace(/\s*\(bearbeitet\)\s*$/, '').trim();
    const neu = prompt('Nachricht bearbeiten:', alt);
    if (neu === null) return;
    const text = neu.trim();
    if (!text || text === alt) return;
    try {
        const r = await fetch('/api/v1/messenger/edit', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ peer: aktuellerPeer(), message_id: id, text })
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        msgTextSetzen(blase, text, true);
        if (!d.zugestellt) {
            toast && toast('Geändert', 'Beim Gegenüber noch nicht angekommen – wird nachgeholt, sobald er erreichbar ist.');
        }
    } catch (e) { alert('✗ ' + e.message); }
}

async function msgLoeschen(id, beimAnderen) {
    // R665: Ohne Rückfrage. Die Wahl steht bereits im Menü ("bei mir" oder
    // "bei beiden"), eine zweite Nachfrage fragt dasselbe noch einmal.
    try {
        const r = await fetch('/api/v1/messenger/delete', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ peer: aktuellerPeer(), message_id: id, beim_anderen: !!beimAnderen })
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        const b = document.querySelector('.msg-bubble[data-msg-id="' + CSS.escape(String(id)) + '"]');
        if (b) b.remove();
    } catch (e) { alert('✗ ' + e.message); }
}

// Verlauf löschen – mit Wahl, ob nur bei mir oder auch beim Gegenüber (R663).
async function verlaufLoeschen(fidOpt) {
    // R665: Auch für eine Unterhaltung, die gerade NICHT offen ist – aufgerufen
    // aus dem Kontextmenü auf dem Namen in der Kontaktliste.
    const peer = (fidOpt || aktuellerPeer() || '').toLowerCase();
    if (!peer) { alert('Keine Unterhaltung gewählt.'); return; }
    const wahl = await verlaufDialog();
    if (!wahl) return;
    try {
        const r = await fetch('/api/v1/messenger/clear', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ peer, beim_anderen: wahl === 'beide' })
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        if (peer === (aktuellerPeer() || '').toLowerCase()) {
            document.querySelectorAll('.msg-bubble').forEach(b => b.remove());
        }
        if (wahl === 'beide' && !d.zugestellt && typeof toast === 'function') {
            toast('Verlauf gelöscht', 'Beim Gegenüber noch nicht angekommen – wird nachgeholt, sobald er erreichbar ist.');
        }
    } catch (e) { alert('✗ ' + e.message); }
}

// Zwei deutlich benannte Knöpfe statt einer Ja/Nein-Frage: Der Unterschied
// zwischen „nur bei mir" und „bei beiden" ist zu wichtig für ein Häkchen.
function verlaufDialog() {
    return new Promise((fertig) => {
        const hg = document.createElement('div');
        hg.className = 'kontakt-waehler';
        const box = document.createElement('div');
        box.className = 'kw-box';
        const h = document.createElement('div');
        h.className = 'kw-titel';
        h.textContent = 'Verlauf dieser Unterhaltung löschen';
        box.appendChild(h);
        const hinweis = document.createElement('div');
        hinweis.className = 'kw-hinweis';
        hinweis.textContent = 'Das lässt sich nicht rückgängig machen.';
        box.appendChild(hinweis);
        [['Nur bei mir', 'lokal'],
         ['Bei mir und beim Gegenüber', 'beide']].forEach(([beschriftung, wert]) => {
            const b = document.createElement('button');
            b.textContent = beschriftung;
            b.onclick = () => { hg.remove(); fertig(wert); };
            box.appendChild(b);
        });
        const nach = document.createElement('div');
        nach.className = 'kw-hinweis';
        nach.textContent = 'Beim Gegenüber wirkt es nur, wenn dessen Node die ' +
                           'Mitteilung erhält. Gelesen ist gelesen.';
        box.appendChild(nach);
        const ab = document.createElement('button');
        ab.className = 'kw-ab'; ab.textContent = 'Abbrechen';
        ab.onclick = () => { hg.remove(); fertig(null); };
        box.appendChild(ab);
        hg.appendChild(box);
        hg.onclick = (ev) => { if (ev.target === hg) { hg.remove(); fertig(null); } };
        document.body.appendChild(hg);
    });
}

// Text einer Blase ersetzen und als bearbeitet kennzeichnen.
function msgTextSetzen(blase, text, bearbeitet) {
    const zeit = blase.querySelector('.msg-time, .bubble-time');
    const marke = blase.querySelector('.msg-status');
    blase.textContent = '';
    const span = document.createElement('span');
    span.innerHTML = (typeof emojifyText === 'function')
        ? emojifyText(escapeHtml(text)) : escapeHtml(text);
    blase.appendChild(span);
    if (bearbeitet) {
        const e = document.createElement('span');
        e.className = 'msg-edited';
        e.textContent = ' (bearbeitet)';
        blase.appendChild(e);
    }
    if (zeit) blase.appendChild(zeit);
    if (marke) blase.appendChild(marke);
}

// ── Kontextmenü auf dem Namen (R665) ─────────────────────────────────────
// Der Verlauf ließ sich bisher nur über das Mülleimer-Symbol im Chatkopf
// löschen – leicht zu übersehen. Jetzt auch per Rechtsklick bzw. langem
// Antippen auf den Namen, sowohl in der Kontaktliste als auch im Chatkopf.

function nameMenuOeffnen(fid, name, x, y) {
    msgMenuSchliessen();
    if (!fid) return;
    const punkte = [
        ['💬 Chat öffnen', () => {
            const k = (typeof contacts === 'object' && contacts) ? contacts[fid] : null;
            if (k) openChat(k); else openChat({ fundusID: fid, alias: name });
        }],
        ['⧉ Adresse kopieren', () => msgKopierenNotfalls(fid)],
        ['🗑 Verlauf löschen …', () => verlaufLoeschen(fid)],
    ];
    const m = document.createElement('div');
    m.className = 'msg-menu';
    const kopf = document.createElement('div');
    kopf.className = 'msg-menu-kopf';
    kopf.textContent = name || fid;
    m.appendChild(kopf);
    punkte.forEach(([beschriftung, fn]) => {
        const b = document.createElement('button');
        b.textContent = beschriftung;
        b.onclick = (ev) => { ev.stopPropagation(); msgMenuSchliessen(); fn(); };
        m.appendChild(b);
    });
    document.body.appendChild(m);
    const r = m.getBoundingClientRect();
    m.style.left = Math.max(8, Math.min(x, window.innerWidth  - r.width  - 8)) + 'px';
    m.style.top  = Math.max(8, Math.min(y, window.innerHeight - r.height - 8)) + 'px';
    _menuOffen = m;
}

// Welcher Name wurde angefasst? Kontaktliste oder Chatkopf.
function nameZiel(el) {
    if (!el || !el.closest) return null;
    const k = el.closest('.contact-item');
    if (k) {
        const fid = k.dataset.fid || (k.__fid || '');
        const nm = (k.querySelector('.contact-name') || {}).textContent || '';
        if (fid) return { fid: String(fid).toLowerCase(), name: nm.trim() };
    }
    const kopf = el.closest('#chat-with-name, #chat-with-sub, #chat-avatar');
    if (kopf) {
        const fid = aktuellerPeer();
        const nm = (document.getElementById('chat-with-name') || {}).textContent || '';
        if (fid) return { fid, name: nm.trim() };
    }
    return null;
}

// Menü an die Blasen hängen: Rechtsklick und langes Antippen.
(function msgMenuBinden() {
    let halten = null, startXY = null;
    // R663: In der AUFFANGPHASE (true), damit kein anderer Behandler den
    // Rechtsklick vorher abfängt oder das Weiterreichen stoppt.
    document.addEventListener('contextmenu', (ev) => {
        const z = nameZiel(ev.target);
        if (z) {
            ev.preventDefault(); ev.stopPropagation();
            nameMenuOeffnen(z.fid, z.name, ev.clientX, ev.clientY);
            return;
        }
        const b = ev.target.closest && ev.target.closest('.msg-bubble');
        if (!b) return;
        ev.preventDefault();
        ev.stopPropagation();
        msgMenuOeffnen(b, ev.clientX, ev.clientY);
    }, true);
    document.addEventListener('touchstart', (ev) => {
        const z = nameZiel(ev.target);
        const b = (!z && ev.target.closest) ? ev.target.closest('.msg-bubble') : null;
        if (!z && !b) return;
        const t = ev.touches[0];
        startXY = { x: t.clientX, y: t.clientY };
        halten = setTimeout(() => {
            halten = null;
            if (navigator.vibrate) navigator.vibrate(18);
            if (z) nameMenuOeffnen(z.fid, z.name, startXY.x, startXY.y);
            else msgMenuOeffnen(b, startXY.x, startXY.y);
        }, 500);
    }, { passive: true });
    const abbrechen = (ev) => {
        // Beim Wischen abbrechen, sonst springt das Menü beim Scrollen auf.
        if (halten && ev.touches && ev.touches[0] && startXY) {
            const t = ev.touches[0];
            if (Math.abs(t.clientX - startXY.x) > 12 || Math.abs(t.clientY - startXY.y) > 12) {
                clearTimeout(halten); halten = null;
            }
            return;
        }
        if (halten) { clearTimeout(halten); halten = null; }
    };
    document.addEventListener('touchmove', abbrechen, { passive: true });
    document.addEventListener('touchend', abbrechen, { passive: true });
    document.addEventListener('touchcancel', abbrechen, { passive: true });
})();

// ── Anklickbare Links (R666) ─────────────────────────────────────────────
// Wirkt auf BEREITS maskiertem Text: Die Maskierung geschieht vorher, hier
// werden nur noch Adressen erkannt. Andersherum wäre es eine Sicherheitslücke.
function verlinke(htmlSicher) {
    return String(htmlSicher).replace(
        /(https?:\/\/[^\s<"]+|www\.[^\s<"]+|[\w.+-]+@[\w-]+\.[\w.]{2,})/g,
        (treffer) => {
            const sauber = treffer.replace(/[.,;:!?)]+$/, '');
            const rest = treffer.slice(sauber.length);
            let ziel = sauber;
            if (sauber.indexOf('@') >= 0 && sauber.indexOf('//') < 0) ziel = 'mailto:' + sauber;
            else if (sauber.startsWith('www.')) ziel = 'https://' + sauber;
            return '<a href="' + ziel + '" target="_blank" rel="noopener noreferrer nofollow">' +
                   sauber + '</a>' + rest;
        });
}

// ── Antworten mit Sprung zur Nachricht (R666) ────────────────────────────
// Das Zitat trägt die Kennung der Ursprungsnachricht mit: "> [id] Text".
// Beim Anklicken springt die Ansicht dorthin und hebt sie kurz hervor.
function zitatHtml(zitat) {
    const m = zitat.match(/^\[([^\]\s]{1,64})\]\s?([\s\S]*)$/);
    // R800: Reihenfolge wie in R797 – erst verlinken, dann Emojis. Diese Stelle
    // hatte ich dort übersehen; ein zitiertes Emoji zerbrach genauso.
    if (!m) return '<span class="zitat">' + emojifyText(verlinke(escapeHtml(zitat))) + '</span>';
    const id = m[1], txt = m[2];
    return '<a href="#" class="zitat zitat-link" onclick="zuNachricht(\'' +
           escapeHtml(id).replace(/'/g, '') + '\');return false;">' +
           emojifyText(escapeHtml(txt)) + '</a>';
}

function zuNachricht(id) {
    const b = document.querySelector('.msg-bubble[data-msg-id="' + CSS.escape(String(id)) + '"]');
    if (!b) { if (typeof showToast === 'function') showToast('Nachricht nicht geladen', 'Weiter nach oben blättern.'); return; }
    b.scrollIntoView({ behavior: 'smooth', block: 'center' });
    b.classList.add('hervor');
    setTimeout(() => b.classList.remove('hervor'), 1600);
}

// ── Anruf- und Wählton (R666) ────────────────────────────────────────────
// Selbst erzeugt statt als Datei: Kein zusätzlicher Download, kein Caching-
// Problem, und der Ton lässt sich jederzeit sauber abbrechen.
let _tonCtx = null, _tonStop = null;

// R681: Browser lassen Ton erst zu, NACHDEM der Nutzer die Seite einmal
// berührt hat. Bei einem eingehenden Anruf gibt es diese Berührung nicht – der
// Anrufton wäre also genau dann stumm, wenn man ihn braucht. Deshalb wird der
// Tonkontext bei der ersten beliebigen Eingabe vorbereitet und wach gehalten.
(function tonVorbereiten() {
    const wecken = () => {
        try {
            const AC = window.AudioContext || window.webkitAudioContext;
            if (!AC) return;
            if (!_tonCtx) _tonCtx = new AC();
            if (_tonCtx.state === 'suspended') _tonCtx.resume().catch(() => {});
        } catch (e) {}
    };
    ['pointerdown', 'touchstart', 'keydown', 'click'].forEach((e) =>
        document.addEventListener(e, wecken, { once: false, passive: true }));
})();

function tonKontext() {
    if (!_tonCtx) {
        const AC = window.AudioContext || window.webkitAudioContext;
        if (!AC) return null;
        _tonCtx = new AC();
    }
    if (_tonCtx.state === 'suspended') _tonCtx.resume().catch(() => {});
    return _tonCtx;
}

// muster: [[frequenz, dauer], …] in Sekunden; pause bis zur Wiederholung.
function tonSchleife(muster, pause, lautstaerke) {
    tonAus();
    const ctx = tonKontext();
    if (!ctx) return;
    const gain = ctx.createGain();
    gain.gain.value = lautstaerke;
    gain.connect(ctx.destination);
    let timer = null, lauft = true;
    const runde = () => {
        if (!lauft) return;
        let t = ctx.currentTime;
        muster.forEach(([f, d]) => {
            const o = ctx.createOscillator();
            o.type = 'sine'; o.frequency.value = f;
            const g = ctx.createGain();
            // Weiche Flanken, sonst knackt es bei jedem Ton.
            g.gain.setValueAtTime(0, t);
            g.gain.linearRampToValueAtTime(1, t + 0.02);
            g.gain.setValueAtTime(1, t + d - 0.02);
            g.gain.linearRampToValueAtTime(0, t + d);
            o.connect(g); g.connect(gain);
            o.start(t); o.stop(t + d);
            t += d;
        });
        const gesamt = muster.reduce((a, [, d]) => a + d, 0) + pause;
        timer = setTimeout(runde, gesamt * 1000);
    };
    runde();
    _tonStop = () => { lauft = false; if (timer) clearTimeout(timer); try { gain.disconnect(); } catch (e) {} };
}

function tonAus() {
    if (_tonStop) { _tonStop(); _tonStop = null; }
    if (_vibroTimer) { clearInterval(_vibroTimer); _vibroTimer = null; }
    if (navigator.vibrate) { try { navigator.vibrate(0); } catch (e) {} }
}

// Wählton beim Anrufer: ruhiges Doppeltuten, wie man es vom Telefon kennt.
function waehltonAn()  { tonSchleife([[440, 0.9], [0, 0.0]], 3.1, 0.07); }
// Anrufton beim Angerufenen: deutlicher, zweistufig.
let _vibroTimer = null;
function anruftonAn() {
    tonSchleife([[660, 0.35], [0, 0.12], [880, 0.35]], 1.6, 0.10);
    // Vibrieren als zweiter Weg: Es braucht keine Tonfreigabe und wirkt auch,
    // wenn das Gerät stumm geschaltet ist.
    if (navigator.vibrate) {
        clearInterval(_vibroTimer);
        const muster = () => { try { navigator.vibrate([400, 200, 400, 1200]); } catch (e) {} };
        muster();
        _vibroTimer = setInterval(muster, 2200);
    }
}

// ── Sprachnachrichten (R666) ─────────────────────────────────────────────
// Senden-Knopf lange gedrückt halten = aufnehmen, loslassen = verschicken.
// Zum Abbrechen während der Aufnahme nach oben wegziehen – so kennt man es
// von anderen Messengern, und es verhindert versehentliche Nachrichten.
let _aufnahme = null, _aufChunks = [], _aufStart = 0, _aufTimer = null, _aufAbbruch = false;

async function sprachStart() {
    if (_aufnahme) return;
    if (!activeChat) { showToast('Bitte zuerst einen Chat öffnen', ''); return; }
    if (!navigator.mediaDevices || !window.MediaRecorder) {
        showToast('Aufnahme nicht möglich', 'Der Browser unterstützt keine Tonaufnahme.');
        return;
    }
    let strom;
    try {
        strom = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (e) {
        showToast('Kein Zugriff aufs Mikrofon', e.message || '');
        return;
    }
    // Opus in WebM ist das, was Browser zuverlässig können; sonst die Vorgabe.
    let typ = '';
    // R674: mp4/AAC ZUERST. WebM/Opus ist technisch besser, aber Safari auf
    // dem iPhone spielt es nicht ab – eine Nachricht, die beim Empfänger
    // stumm bleibt, nützt niemandem.
    for (const k of ['audio/mp4', 'audio/mp4;codecs=mp4a.40.2',
                     'audio/webm;codecs=opus', 'audio/webm', 'audio/ogg;codecs=opus']) {
        if (MediaRecorder.isTypeSupported && MediaRecorder.isTypeSupported(k)) { typ = k; break; }
    }
    _aufChunks = []; _aufAbbruch = false; _aufStart = Date.now();
    _aufnahme = new MediaRecorder(strom, typ ? { mimeType: typ } : undefined);
    _aufnahme.ondataavailable = (ev) => { if (ev.data && ev.data.size) _aufChunks.push(ev.data); };
    _aufnahme.onstop = async () => {
        strom.getTracks().forEach(t => t.stop());
        const dauer = (Date.now() - _aufStart) / 1000;
        const art = _aufnahme.mimeType || typ || 'audio/webm';
        _aufnahme = null;
        sprachAnzeigeAus();
        // Sehr kurze Aufnahmen sind fast immer ein Verrutschen, nicht gewollt.
        if (_aufAbbruch || dauer < 0.8 || !_aufChunks.length) { _aufChunks = []; return; }
        const blob = new Blob(_aufChunks, { type: art });
        _aufChunks = [];
        await sprachSenden(blob, dauer, art);
    };
    _aufnahme.start();
    sprachAnzeigeAn();
    if (navigator.vibrate) navigator.vibrate(18);
    // Harte Obergrenze: Eine vergessene Aufnahme soll nicht endlos laufen.
    _aufTimer = setTimeout(() => sprachStopp(false), 120000);
}

function sprachStopp(abbrechen) {
    if (_aufTimer) { clearTimeout(_aufTimer); _aufTimer = null; }
    if (!_aufnahme) { sprachAnzeigeAus(); return; }
    _aufAbbruch = !!abbrechen;
    try { _aufnahme.stop(); } catch (e) { _aufnahme = null; sprachAnzeigeAus(); }
}

function sprachAnzeigeAn() {
    sprachAnzeigeAus();
    const el = document.createElement('div');
    el.id = 'sprach-anzeige';
    el.className = 'sprach-anzeige';
    el.innerHTML = '<span class="sa-punkt"></span><span id="sa-zeit">0:00</span>' +
                   '<span class="sa-hinweis">zum Abbrechen nach oben ziehen</span>';
    document.body.appendChild(el);
    el._timer = setInterval(() => {
        const s = Math.floor((Date.now() - _aufStart) / 1000);
        const z = document.getElementById('sa-zeit');
        if (z) z.textContent = Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
    }, 250);
}

function sprachAnzeigeAus() {
    const el = document.getElementById('sprach-anzeige');
    if (el) { if (el._timer) clearInterval(el._timer); el.remove(); }
}

async function sprachSenden(blob, dauer, art) {
    // R668: nutzt dieselbe Sendefunktion wie das Einfügen aus der
    // Zwischenablage – vorher war der Ablauf hier ein zweites Mal ausgeschrieben.
    const endung = art.indexOf('ogg') >= 0 ? 'ogg' : (art.indexOf('mp4') >= 0 ? 'm4a' : 'webm');
    const name = 'sprachnachricht-' + Math.round(dauer) + 's.' + endung;
    await datenSenden(new File([blob], name, { type: art }), 'Sprachnachricht');
}

// Langes Drücken auf den Senden-Knopf startet die Aufnahme.
(function sprachBinden() {
    const knopf = document.getElementById('send-btn');
    if (!knopf) return;
    let halten = null, startY = 0, laeuft = false;

    const beginn = (y) => {
        startY = y;
        halten = setTimeout(() => {
            halten = null;
            // Steht Text im Feld, ist der Knopf ein Sendeknopf – dann nicht
            // aufnehmen, sonst ginge der getippte Text beim Halten verloren.
            const feld = document.getElementById('msg-input');
            if (feld && feld.value.trim()) return;
            laeuft = true; sprachStart();
        }, 450);
    };
    const ende = () => {
        if (halten) { clearTimeout(halten); halten = null; return; }   // kurzer Druck: normales Senden
        if (laeuft) { laeuft = false; sprachStopp(false); }
    };
    const ziehen = (y) => {
        if (laeuft && startY - y > 70) { laeuft = false; sprachStopp(true); }
    };

    knopf.addEventListener('touchstart', (ev) => beginn(ev.touches[0].clientY), { passive: true });
    knopf.addEventListener('touchmove',  (ev) => ziehen(ev.touches[0].clientY), { passive: true });
    knopf.addEventListener('touchend',   ende, { passive: true });
    knopf.addEventListener('touchcancel', () => { if (laeuft) { laeuft = false; sprachStopp(true); } }, { passive: true });
    // Am Rechner mit gedrückter Maustaste.
    knopf.addEventListener('mousedown', (ev) => beginn(ev.clientY));
    document.addEventListener('mousemove', (ev) => ziehen(ev.clientY));
    document.addEventListener('mouseup', ende);
})();

// ── Eigener Abspieler für Sprachnachrichten (R667) ───────────────────────
// Die eingebaute Bedienleiste des Browsers lässt sich nicht einfärben; sie ist
// weiß und passt nicht zur Blase. Deshalb eine eigene, schlichte Leiste in den
// Farben der Oberfläche: Knopf, Fortschritt, Dauer.
let _tonZaehler = 0;

function tonAbspielerHtml(url, file) {
    const id = 'ton' + (++_tonZaehler);
    const dauer = (file.name || '').match(/-(\d+)s\./);
    const sek = dauer ? parseInt(dauer[1], 10) : 0;
    const vorab = sek ? zeitText(sek) : '';
    // R682: Der Hinweis "Format hier nicht abspielbar" ist raus. canPlayType
    // meldet für WebM auf iOS einen leeren Wert, obwohl die Wiedergabe dort
    // funktioniert – die Auskunft war also schlicht falsch. Eine Warnung, die
    // bei funktionierenden Dateien erscheint, ist schlimmer als keine.
    const hinweis = '';
    return '<div class="ton-player" id="' + id + '">' +
           '<button class="tp-knopf" onclick="tonSchalten(\'' + id + '\')" aria-label="Abspielen">▶</button>' +
           '<div class="tp-balken" onclick="tonSpringen(event, \'' + id + '\')">' +
           '<div class="tp-fort"></div></div>' +
           '<span class="tp-zeit">' + vorab + '</span>' + hinweis +
           '<audio preload="metadata" data-sek="' + sek + '" src="' + url + '"></audio></div>';
}

function zeitText(sek) {
    if (!isFinite(sek) || sek < 0) return '0:00';
    const s = Math.round(sek);
    return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}

function tonSchalten(id) {
    const box = document.getElementById(id);
    if (!box) return;
    const a = box.querySelector('audio');
    const k = box.querySelector('.tp-knopf');
    if (!a) return;
    if (a.paused) {
        // Nur eine Sprachnachricht gleichzeitig – sonst reden zwei Stimmen.
        document.querySelectorAll('.ton-player audio').forEach((x) => {
            if (x !== a && !x.paused) { x.pause(); }
        });
        document.querySelectorAll('.ton-player .tp-knopf').forEach((x) => {
            if (x !== k) x.textContent = '▶';
        });
        if (!a._gebunden) {
            a._gebunden = true;
            const fort = box.querySelector('.tp-fort');
            const zeit = box.querySelector('.tp-zeit');

            // R674: Eine Aufnahme über MediaRecorder trägt KEINE Gesamtlänge im
            // Behälter – sie wird fortlaufend geschrieben, und am Ende steht
            // nichts nach. duration ist dann Infinity. Genau deshalb blieb der
            // Balken bei null und die Zeit zählte nur hoch.
            //
            // Zwei Wege, in dieser Reihenfolge:
            //   1. Einmal ans Ende springen. Der Browser rechnet die Länge
            //      daraufhin aus und meldet sie über durationchange.
            //   2. Scheitert das, die Länge aus dem Dateinamen nehmen – wir
            //      schreiben sie dort beim Aufnehmen hinein ("…-6s.webm").
            a._laenge = 0;
            const ausDateiname = () => {
                const q = box.querySelector('audio');
                const m = (q && q.getAttribute('data-sek')) || '';
                const v = parseInt(m, 10);
                return isFinite(v) && v > 0 ? v : 0;
            };
            const laenge = () => {
                if (isFinite(a.duration) && a.duration > 0) return a.duration;
                if (a._laenge > 0) return a._laenge;
                return ausDateiname();
            };
            a.addEventListener('durationchange', () => {
                if (isFinite(a.duration) && a.duration > 0) a._laenge = a.duration;
            });
            if (!isFinite(a.duration) || a.duration === 0) {
                // Der Sprung ans Ende zwingt den Browser, die Länge zu bestimmen.
                try {
                    a.currentTime = 1e7;
                    a.addEventListener('seeked', function zurueck() {
                        a.removeEventListener('seeked', zurueck);
                        if (isFinite(a.duration) && a.duration > 0) a._laenge = a.duration;
                        a.currentTime = 0;
                    }, { once: false });
                } catch (e) {}
            }

            a.addEventListener('timeupdate', () => {
                const d = laenge();
                if (d > 0) {
                    fort.style.width = Math.min(100, a.currentTime / d * 100) + '%';
                }
                zeit.textContent = zeitText(a.currentTime) + (d > 0 ? ' / ' + zeitText(d) : '');
            });
            a.addEventListener('ended', () => { k.textContent = '▶'; fort.style.width = '0%'; });
            a.addEventListener('error', () => { zeit.textContent = 'nicht abspielbar'; });
        }
        const pr = a.play();
        if (pr && pr.catch) pr.catch(() => { box.querySelector('.tp-zeit').textContent = 'nicht abspielbar'; });
        k.textContent = '❙❙';
    } else {
        a.pause();
        k.textContent = '▶';
    }
}

function tonSpringen(ev, id) {
    const box = document.getElementById(id);
    if (!box) return;
    const a = box.querySelector('audio');
    const b = box.querySelector('.tp-balken');
    // R674: auch mit der ersatzweise ermittelten Länge springen können.
    let d = (isFinite(a.duration) && a.duration > 0) ? a.duration : (a._laenge || 0);
    if (!d) d = parseInt(a.getAttribute('data-sek') || '0', 10);
    if (!d) return;
    const r = b.getBoundingClientRect();
    a.currentTime = Math.max(0, Math.min(1, (ev.clientX - r.left) / r.width)) * d;
}

// ── Aus der Zwischenablage einfügen (R668) ───────────────────────────────
// Bilder und GIFs lassen sich jetzt direkt ins Eingabefeld einfügen. Ein
// Bildschirmfoto landet so ohne Umweg über Datei speichern und Anhängen im
// Chat.
//
// datenSenden ist die gemeinsame Grundlage für Einfügen und Sprachaufnahme –
// beide laden hoch und verschicken dann dieselbe Art Nachricht. Zwei getrennte
// Wege wären nach dem dritten Nachbessern auseinandergelaufen.
async function datenSenden(datei, beschriftung) {
    if (!activeChat) { showToast('Bitte zuerst einen Chat öffnen', ''); return false; }
    const chat = { fundusID: activeChat.fundusID, publicKey: activeChat.publicKey };
    const st = appendSystemMessage((beschriftung || 'Datei') + ' wird gesendet…');
    try {
        const fd = new FormData();
        fd.append('file', datei);
        const up = await fetch('/api/v1/files/upload',
            { method: 'POST', credentials: 'same-origin', body: fd });
        const ud = await up.json();
        if (!up.ok || !ud.content_hash) throw new Error(ud.error || ('HTTP ' + up.status));
        const r = await fetch('/api/v1/messenger/send', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                recipient_id: chat.fundusID, recipient_pub_key: chat.publicKey || '',
                type: 'file', file_hash: ud.content_hash, file_name: datei.name,
                file_size: datei.size, mime_type: datei.type || '',
            }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        if (st && st.remove) st.remove();
        appendMessage({
            id: d.id || null, from: myIdentity, outgoing: true, ts: new Date(),
            file: { hash: ud.content_hash, name: datei.name, size: datei.size, mime: datei.type || '' },
        });
        return true;
    } catch (e) {
        if (st) st.textContent = '✗ ' + (beschriftung || 'Datei') + ': ' + e.message;
        return false;
    }
}

// Einfügen abfangen: Enthält die Zwischenablage ein Bild, wird es verschickt.
// Reiner Text wird NICHT abgefangen, sonst ließe sich nichts mehr einfügen.
(function einfuegenBinden() {
    const feld = document.getElementById('msg-input');
    if (!feld) return;
    const verarbeiten = async (ev) => {
        const cd = ev.clipboardData || window.clipboardData;
        if (!cd) return;
        const bilder = [];
        for (const it of (cd.items || [])) {
            if (it.kind === 'file' && String(it.type || '').startsWith('image/')) {
                const f = it.getAsFile();
                if (f) bilder.push(f);
            }
        }
        if (!bilder.length) return;      // nur Text: normal einfügen lassen
        ev.preventDefault();
        for (const b of bilder) {
            // Bildschirmfotos heißen oft "image.png" oder gar nichts – einen
            // sprechenden Namen vergeben, sonst steht das später so im Verlauf.
            const endung = (b.type.split('/')[1] || 'png').replace('jpeg', 'jpg');
            const name = (b.name && b.name !== 'image.png') ? b.name
                       : 'eingefuegt-' + new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-') + '.' + endung;
            await datenSenden(new File([b], name, { type: b.type }),
                              endung === 'gif' ? 'GIF' : 'Bild');
        }
    };
    feld.addEventListener('paste', verarbeiten);
    // Auch wenn der Fokus nicht im Feld liegt – im Chat eingefügt ist eindeutig.
    document.addEventListener('paste', (ev) => {
        if (document.activeElement === feld) return;   // sonst doppelt
        if (!activeChat) return;
        verarbeiten(ev);
    });
})();

// ── Knopf zeigt, was er tut (R668) ───────────────────────────────────────
// Leeres Feld: Aufnahmepunkt – langes Drücken nimmt eine Sprachnachricht auf.
// Sobald Text im Feld steht, wird daraus der Sendepfeil.
function sendeKnopfAnpassen() {
    const feld = document.getElementById('msg-input');
    const ico = document.querySelector('#send-btn .send-ico');
    const btn = document.getElementById('send-btn');
    if (!feld || !ico || !btn) return;
    const hatText = !!feld.value.trim();
    ico.textContent = hatText ? '➤' : '⏺';
    btn.classList.toggle('aufnahme-modus', !hatText);
    btn.title = hatText ? 'Senden' : 'Gedrückt halten für Sprachnachricht';
}
(function sendeKnopfBinden() {
    const feld = document.getElementById('msg-input');
    if (!feld) return;
    ['input', 'change', 'keyup'].forEach(e => feld.addEventListener(e, sendeKnopfAnpassen));
    sendeKnopfAnpassen();
})();

// ── Gespräch über Bildschirmsperre und App-Wechsel retten (R677) ─────────
//
// Was hier wirklich passiert: Wir legen NICHT selbst auf. Das Betriebssystem
// hält die Seite an, sobald der Bildschirm ausgeht oder eine andere App in den
// Vordergrund kommt. Dabei sterben die ausgehandelten Verbindungswege, und
// wenn die Seite wieder läuft, meldet WebRTC "failed".
//
// Drei Gegenmaßnahmen, in dieser Reihenfolge wirksam:
//   1. Bildschirmsperre verhindern, solange ein Gespräch läuft. Damit tritt
//      der häufigste Fall gar nicht erst ein.
//   2. Beim Zurückkommen die Wege neu aushandeln, statt aufzulegen.
//   3. Kurze Aussetzer aussitzen – "disconnected" erholt sich oft von selbst.
//
// Was NICHT geht, und das muss man wissen: Auf dem iPhone hält Safari eine
// Seite im Hintergrund vollständig an. Kein Webprogramm kann das umgehen; dafür
// bräuchte es eine eigene App mit entsprechender Berechtigung.

let _wakeLock = null;

async function wakeLockAnfordern() {
    if (!('wakeLock' in navigator) || _wakeLock) return;
    try {
        _wakeLock = await navigator.wakeLock.request('screen');
        callLog('Bildschirmsperre', 'verhindert');
        _wakeLock.addEventListener('release', () => { _wakeLock = null; });
    } catch (e) {
        // Scheitert etwa bei zu wenig Akku – dann eben ohne.
        callLog('Bildschirmsperre', 'nicht möglich: ' + (e.message || e.name));
    }
}

function wakeLockFreigeben() {
    if (!_wakeLock) return;
    try { _wakeLock.release(); } catch (e) {}
    _wakeLock = null;
}

// ── Wege neu aushandeln ──────────────────────────────────────────────────
let _eisNeu = 0;
let _eisTimer = null;

function eisNeuaufbau() {
    if (!peerConn || !callPeer) return false;
    // Höchstens dreimal: Sonst dreht sich das endlos, wenn wirklich kein Weg
    // existiert, und die Gegenseite bekommt eine Flut von Angeboten.
    if (_eisNeu >= 3) {
        callLog('Neuaufbau', 'aufgegeben nach 3 Versuchen');
        return false;
    }
    _eisNeu++;
    callLog('Neuaufbau', 'Versuch ' + _eisNeu);
    showToast('📶 Verbindung wird wiederhergestellt…', 'Bitte einen Moment');
    (async () => {
        try {
            const offer = await peerConn.createOffer({ iceRestart: true });
            await peerConn.setLocalDescription(offer);
            await sendSignal({ type: 'ice_restart', sdp: JSON.stringify(offer) });
        } catch (e) {
            callLog('Neuaufbau fehlgeschlagen', e.message || '');
        }
    })();
    return true;
}

// Kurze Aussetzer aussitzen: "disconnected" wird oft von selbst wieder
// "connected". Erst wenn es nach einigen Sekunden noch hängt, eingreifen.
function eisBeobachten(pc) {
    pc.addEventListener('iceconnectionstatechange', () => {
        const z = pc.iceConnectionState;
        if (z === 'disconnected') {
            clearTimeout(_eisTimer);
            _eisTimer = setTimeout(() => {
                if (peerConn === pc &&
                    (pc.iceConnectionState === 'disconnected' || pc.iceConnectionState === 'failed')) {
                    eisNeuaufbau();
                }
            }, 6000);
        } else if (z === 'connected' || z === 'completed') {
            clearTimeout(_eisTimer);
            _eisNeu = 0;
        }
    });
}

// Beim Zurückkommen in den Vordergrund prüfen, ob das Gespräch noch steht.
document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    if (!peerConn) return;
    wakeLockAnfordern();          // Sperre wird beim Wegschalten vom System aufgehoben
    const z = peerConn.iceConnectionState;
    callLog('zurück im Vordergrund', z);
    if (z === 'disconnected' || z === 'failed') {
        eisNeuaufbau();
    }
});

// ── Bild im Vordergrund vergrößern (R687) ────────────────────────────────
// Nutzt denselben Betrachter wie der Dateimanager: dunkler Hintergrund, Klick
// daneben oder Esc schließt, Pfeiltasten blättern. Bewusst kein eigener
// Nachbau – zwei Betrachter wären zwei Baustellen.
function bildGross(hash, name, mime) {
    if (!hash) return;
    // Alle Bilder des Gesprächs sammeln, damit man blättern kann.
    const liste = [];
    let start = 0;
    document.querySelectorAll('.msg-media-link').forEach((a) => {
        const m = (a.getAttribute('onclick') || '').match(/bildGross\('([^']+)','([^']*)','([^']*)'/);
        if (!m) return;
        if (m[1] === hash) start = liste.length;
        liste.push({ hash: m[1], name: m[2] || 'bild', mime: m[3] || '' });
    });
    if (window.FundusMedia && typeof window.FundusMedia.open === 'function') {
        if (window.FundusMedia.open(liste.length ? liste : [{ hash, name, mime }], start)) return;
    }
    // Ohne Betrachter: alter Weg, damit das Bild wenigstens erreichbar bleibt.
    window.open('/api/v1/files/download/' + encodeURIComponent(hash), '_blank');
}

// msgnotify.js — globale Messenger-Benachrichtigungen auf ALLEN Seiten.
// Hält eine WebSocket-Verbindung offen (sofern angemeldet), spielt Töne bei
// ein-/ausgehenden Nachrichten, zeigt einen grünen Zähler am Messenger-Nav-Icon
// und Toast-Popups — auch wenn der Messenger gerade nicht offen ist.
//
// Auf der Messenger-Seite selbst übernimmt messenger.lua die Anzeige; dort
// meldet sich diese Datei zurück (window.__inMessenger), um doppelte Toasts/Töne
// zu vermeiden.

(function(){
  let ws = null, wsRetry = 0;
  // Zähler persistent in localStorage (überlebt Seitenwechsel). Auch die
  // Absender der ungelesenen Nachrichten merken, damit der Messenger die
  // Konversation direkt öffnen kann.
  function loadState() {
    try {
      const raw = localStorage.getItem('fnd_msg_unread');
      if (raw) return JSON.parse(raw);
    } catch(e){}
    return { count: 0, lastSender: '', senders: {} };
  }
  function saveState(st) {
    try { localStorage.setItem('fnd_msg_unread', JSON.stringify(st)); } catch(e){}
  }
  let state = loadState();
  let unread = state.count || 0;
  window.__msgNotify = { unread: unread };

  // ---- Töne (per WebAudio erzeugt, kein externer Sound nötig) ----
  let audioCtx = null;
  function tone(freqs, durs, type) {
    try {
      if (!audioCtx) audioCtx = new (window.AudioContext||window.webkitAudioContext)();
      let t = audioCtx.currentTime;
      freqs.forEach((f, i) => {
        const o = audioCtx.createOscillator();
        const g = audioCtx.createGain();
        o.type = type || 'sine';
        o.frequency.value = f;
        g.gain.setValueAtTime(0.0001, t);
        g.gain.exponentialRampToValueAtTime(0.15, t + 0.01);
        g.gain.exponentialRampToValueAtTime(0.0001, t + durs[i]);
        o.connect(g); g.connect(audioCtx.destination);
        o.start(t); o.stop(t + durs[i]);
        t += durs[i];
      });
    } catch(e){}
  }
  // Pling: heller, freundlicher Zweiklang aufwärts (eingehend).
  window.playPling = function(){ tone([660, 990], [0.08, 0.14], 'sine'); };
  // Whoop: kurzer Aufwärts-Sweep (ausgehend).
  window.playWhoop = function(){ tone([440, 880], [0.06, 0.10], 'triangle'); };

  // ---- Nav-Badge am Messenger-Icon ----
  function applyBadge() {
    let found = false;
    document.querySelectorAll('a[href="/messenger"]').forEach(el => {
      found = true;
      let b = el.querySelector('.msg-nav-badge');
      if (unread > 0) {
        if (!b) {
          b = document.createElement('span');
          b.className = 'msg-nav-badge';
          if (getComputedStyle(el).position === 'static') el.style.position = 'relative';
          el.appendChild(b);
        }
        b.textContent = unread > 99 ? '99+' : unread;
      } else if (b) {
        b.remove();
      }
    });
    return found;
  }
  function updateBadge() {
    try {
      window.__msgNotify.unread = unread;
      // Der Messenger-Link wird von navvis.js per JS aufgebaut — er existiert
      // evtl. noch nicht. Mehrmals versuchen, bis er da ist.
      if (!applyBadge()) {
        let tries = 0;
        const iv = setInterval(() => {
          tries++;
          if (applyBadge() || tries > 20) clearInterval(iv);
        }, 250);
      }
    } catch(e){ /* darf die Seite nie brechen */ }
  }

  window.msgNotifyReset = function(){
    unread = 0;
    state = { count: 0, lastSender: '', senders: {} };
    saveState(state);
    // Alle gerenderten Badges sofort entfernen (auch die von navvis.js gebauten).
    try {
      document.querySelectorAll('.msg-nav-badge').forEach(b => b.remove());
    } catch(e){}
    updateBadge();
  };

  // ---- Toast ----
  function toast(text, sub) {
    let c = document.getElementById('global-toast-container');
    if (!c) {
      c = document.createElement('div');
      c.id = 'global-toast-container';
      c.style.cssText = 'position:fixed;top:70px;right:16px;z-index:99999;display:flex;flex-direction:column;gap:8px';
      document.body.appendChild(c);
    }
    const el = document.createElement('div');
    el.style.cssText = 'background:var(--surface,#1a1a2e);border:1px solid #17a862;border-left:4px solid #00e676;border-radius:10px;padding:10px 14px;box-shadow:0 4px 18px rgba(0,0,0,.4);max-width:280px;cursor:pointer;font-size:13px';
    // SICHERHEIT: Text stammt teils von fremden Peers (Nachricht, Absender) → escapen.
    const esc = v => String(v==null?'':v).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    el.innerHTML = '<div style="font-weight:600">'+esc(text)+'</div>'+(sub?'<div style="font-size:12px;color:var(--muted);margin-top:2px">'+esc(sub)+'</div>':'');
    el.onclick = () => { location.href = '/messenger'; };
    c.appendChild(el);
    setTimeout(() => { el.style.opacity='0'; el.style.transition='opacity .3s'; setTimeout(()=>el.remove(),300); }, 4500);
  }

  async function connect() {
    try {
      // Nicht auf der Messenger-Seite (dort läuft messenger.lua mit eigenem WS).
      // URL-Prüfung ist zuverlässiger als window.__inMessenger (Timing-unabhängig).
      if (location.pathname === '/messenger' || location.pathname.startsWith('/messenger')) return;
      if (window.__inMessenger) return;
      if (!window.WebSocket) return;
      let me;
      try {
        me = window.fundusMe ? await window.fundusMe() : null;
      } catch(e){ return; }
      if (!me || !me.fundus_id) return;
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      ws = new WebSocket(proto+'//'+location.host+'/api/v1/messenger/ws?id='+encodeURIComponent(me.fundus_id));
      ws.onopen = () => { wsRetry = 0; };
      ws.onmessage = (ev) => {
        let msg; try { msg = JSON.parse(ev.data); } catch(e){ return; }
        if (msg.type === 'receipt' || msg.type === 'signal') return; // keine Töne dafür
        unread++;
        state.count = unread;
        state.lastSender = msg.sender_id || '';
        if (msg.sender_id) state.senders[msg.sender_id.toLowerCase()] = (state.senders[msg.sender_id.toLowerCase()]||0) + 1;
        saveState(state);
        updateBadge();
        window.playPling();
        const who = msg.sender_name || (msg.sender_id||'').slice(0,10)+'…';
        toast('Neue Nachricht von '+who, (msg.text||'').slice(0,60));
      };
      ws.onclose = () => {
        ws = null;
        wsRetry = Math.min(wsRetry+1, 5);
        setTimeout(connect, Math.min(3000*Math.pow(2, wsRetry-1), 60000));
      };
      ws.onerror = () => { try { ws.close(); } catch(e){} };
    } catch(e){ /* darf die Seite nie brechen */ }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => { updateBadge(); setTimeout(connect, 500); });
  } else { updateBadge(); setTimeout(connect, 500); }
})();

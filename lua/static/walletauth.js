// walletauth.js — zentrale Wallet-Identität für alle Bereiche.
// Eine einzige Login-Session (E-Mail+Passwort ODER Wallet-Seed → FundusID), die
// über Marktplatz, Partner, Messenger und alle Nodes hinweg gilt. Der Schlüssel
// liegt nur serverseitig im RAM (Session), das Frontend hält nur einen HttpOnly-
// Cookie. Login einmal → überall erkannt.

(function(){
  window.WALLET = { fundusID: null, address: null, pubKey: null, loaded: false };

  // Session-Status vom Server holen (/identity/me nutzt den HttpOnly-Cookie).
  window.walletRefresh = async function(force){
    try {
      const d = await window.fundusMe(!!force);
      if (d){
        window.WALLET = { fundusID: d.fundus_id, address: d.wallet_address, pubKey: d.ed25519_pub_key, loaded: true };
      } else {
        window.WALLET = { fundusID: null, address: null, pubKey: null, loaded: true };
      }
    } catch(e){
      window.WALLET = { fundusID: null, address: null, pubKey: null, loaded: true };
    }
    window.walletRenderBadge();
    // Andere Seiten (z.B. Messenger) über Login/Logout informieren.
    try { window.dispatchEvent(new CustomEvent('wallet-changed', { detail: window.WALLET })); } catch(e){}
  };

  // Login: E-Mail+Passwort ODER Seed-Wörter → Session.
  window.walletLogin = async function(opts){
    const body = {};
    if (opts.email) { body.email = opts.email; body.password = opts.password; }
    else if (opts.words) { body.words = opts.words; }
    const r = await fetch("/api/v1/identity/derive", {
      method:"POST", credentials:"same-origin", headers:{"Content-Type":"application/json"}, body: JSON.stringify(body)
    });
    const d = await r.json();
    if (!r.ok || d.error) throw new Error(d.error || "Login fehlgeschlagen");
    await window.walletRefresh(true);
    return d;
  };

  window.walletLogout = async function(){
    try { await fetch("/api/v1/identity/logout", {method:"POST", credentials:"same-origin"}); } catch(e){}
    // Nachrichten-Zähler und Badge zurücksetzen (nicht mehr eingeloggt).
    try { localStorage.removeItem("fnd_msg_unread"); } catch(e){}
    if (window.msgNotifyReset) window.msgNotifyReset();
    await window.walletRefresh(true);
    // Seite neu laden, damit alle Bereiche (Messenger, Kontakte) sauber
    // zurückgesetzt werden.
    location.reload();
  };

  // Badge in der Titelleiste: eingeloggt (gekürzte Adresse) oder Login-Button.
  window.walletRenderBadge = function(){
    const el = document.getElementById("wallet-badge");
    if (!el) return;
    if (window.WALLET.fundusID){
      const a = window.WALLET.address || window.WALLET.fundusID;
      const short = a.slice(0,6) + "…" + a.slice(-4);
      el.innerHTML = '<button class="wallet-btn wallet-in" title="Angemeldet: '+a+'" onclick="walletOpenMenu()">👛 '+short+'</button>';
    } else {
      el.innerHTML = '<button class="wallet-btn wallet-out" onclick="walletOpenDialog()">Anmelden</button>';
    }
  };

  // Login-Dialog (E-Mail+Passwort oder Seed).
  window.walletOpenDialog = function(){
    let ov = document.getElementById("wallet-dialog");
    if (ov) ov.remove();
    ov = document.createElement("div");
    ov.id = "wallet-dialog";
    ov.className = "wallet-overlay";
    ov.onclick = function(e){ if(e.target===ov) ov.remove(); };
    const card = document.createElement("div");
    card.className = "wallet-card";
    card.innerHTML =
      '<div class="wallet-card-head"><strong>Anmelden</strong>'+
      '<button class="wallet-x" onclick="document.getElementById(\'wallet-dialog\').remove()">✕</button></div>'+
      '<p class="wallet-hint">Deine Wallet-Identität gilt überall: Marktplatz, Partner, Messenger — auch auf anderen Knoten. Der Schlüssel bleibt nur temporär im Speicher.</p>'+
      '<div class="wallet-tabs">'+
        '<button class="wallet-tab active" id="wt-email" onclick="walletTab(\'email\')">E-Mail + Passwort</button>'+
        '<button class="wallet-tab" id="wt-seed" onclick="walletTab(\'seed\')">Wallet-Seed</button>'+
      '</div>'+
      '<form id="wallet-loginform" onsubmit="walletDoLogin();return false;">'+
      '<div id="wallet-pane-email">'+
        '<input type="email" id="wa-email" name="username" autocomplete="username" placeholder="E-Mail" class="wallet-input">'+
        '<input type="password" id="wa-pass" name="password" autocomplete="current-password" placeholder="Passwort" class="wallet-input">'+
      '</div>'+
      '<div id="wallet-pane-seed" style="display:none">'+
        '<input type="text" id="wa-seed" placeholder="Seed-Wörter (durch Leerzeichen getrennt)" class="wallet-input" autocomplete="off">'+
      '</div>'+
      '<button type="submit" class="wallet-submit" id="wa-submit">Anmelden</button>'+
      '</form>'+
      '<div id="wa-status" class="wallet-status"></div>';
    ov.appendChild(card);
    document.body.appendChild(ov);
  };

  window.walletTab = function(which){
    document.getElementById("wt-email").classList.toggle("active", which==="email");
    document.getElementById("wt-seed").classList.toggle("active", which==="seed");
    document.getElementById("wallet-pane-email").style.display = which==="email" ? "" : "none";
    document.getElementById("wallet-pane-seed").style.display  = which==="seed"  ? "" : "none";
  };

  window.walletDoLogin = async function(){
    const st = document.getElementById("wa-status");
    const btn = document.getElementById("wa-submit");
    btn.disabled = true; st.textContent = "⏳ Melde an…";
    try {
      const seedVisible = document.getElementById("wallet-pane-seed").style.display !== "none";
      if (seedVisible){
        const words = (document.getElementById("wa-seed").value || "").trim().split(/\s+/);
        await window.walletLogin({ words: words });
      } else {
        const email = (document.getElementById("wa-email").value||"").trim();
        const pass  = document.getElementById("wa-pass").value||"";
        if (!email || !pass) throw new Error("E-Mail und Passwort nötig");
        await window.walletLogin({ email: email, password: pass });
        // Chrome die Zugangsdaten zum Speichern anbieten (Credential Management
        // API — der zuverlässige Weg bei JS-Logins ohne Seitennavigation).
        try {
          if (window.PasswordCredential && navigator.credentials) {
            const cred = new window.PasswordCredential({
              id: email, password: pass, name: email
            });
            await navigator.credentials.store(cred);
          }
        } catch(ce) { /* Speichern-Angebot ist best effort */ }
      }
      const ov = document.getElementById("wallet-dialog");
      if (ov) ov.remove();
    } catch(e){
      btn.disabled = false;
      st.innerHTML = '<span style="color:var(--red,#f66)">✗ '+e.message+'</span>';
    }
  };

  // Kleines Menü bei Klick auf die eingeloggte Adresse (Adresse kopieren, Abmelden).
  window.walletOpenMenu = function(){
    let ov = document.getElementById("wallet-menu");
    if (ov){ ov.remove(); return; }
    ov = document.createElement("div");
    ov.id = "wallet-menu";
    ov.className = "wallet-menu";
    ov.innerHTML =
      '<div class="wallet-menu-addr">'+(window.WALLET.address||'')+'</div>'+
      '<button onclick="navigator.clipboard&&navigator.clipboard.writeText(window.WALLET.address);this.textContent=\'✓ Kopiert\'">Adresse kopieren</button>'+
      '<button onclick="walletShowSeed()">Seed-Wörter anzeigen</button>'+
      '<button onclick="walletPublishEmail()">Per E-Mail auffindbar machen</button>'+
      '<button onclick="walletLogout();document.getElementById(\'wallet-menu\').remove()">Abmelden</button>';
    document.body.appendChild(ov);
    setTimeout(function(){
      document.addEventListener("click", function closer(e){
        const m = document.getElementById("wallet-menu");
        if (m && !m.contains(e.target)){ m.remove(); document.removeEventListener("click", closer); }
      });
    }, 50);
  };

  // Beim Laden Status holen.
  if (document.readyState === "loading"){
    document.addEventListener("DOMContentLoaded", window.walletRefresh);
  } else {
    window.walletRefresh();
  }
  // Per E-Mail auffindbar machen: trägt email→FundusID ins opt-in-Verzeichnis ein.
  window.walletPublishEmail = async function(){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    const email = prompt("E-Mail-Adresse, unter der dich andere finden können sollen:\n(freiwillig, wird öffentlich mit deiner Adresse verknüpft)");
    if (!email) return;
    try {
      const r = await fetch("/api/v1/identity/email-dir/publish", {
        method:"POST", headers:{"Content-Type":"application/json"},
        body: JSON.stringify({ email: email.trim() })
      });
      const d = await r.json();
      if (r.ok && d.ok) alert("✓ Du bist jetzt per E-Mail auffindbar.");
      else alert("✗ "+(d.error||"Fehler"));
    } catch(e){ alert("✗ "+e.message); }
  };

  // Seed-Wörter anzeigen (für Wallet-Sicherung / FND-Versand).
  window.walletShowSeed = async function(){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    try {
      const r = await fetch("/api/v1/identity/seed");
      const d = await r.json();
      if (!r.ok || d.error || !d.words) { alert("✗ "+(d.error||"Keine Seed-Wörter verfügbar")); return; }
      const ov = document.createElement("div");
      ov.className = "wallet-overlay";
      ov.onclick = function(e){ if(e.target===ov) ov.remove(); };
      const card = document.createElement("div");
      card.className = "wallet-card";
      card.innerHTML =
        '<div class="wallet-card-head"><strong>Deine Seed-Wörter</strong>'+
        '<button class="wallet-x" onclick="this.closest(\'.wallet-overlay\').remove()">✕</button></div>'+
        '<p class="wallet-hint" style="color:#f66">Diese Wörter sind dein Wallet-Zugang. Sicher aufbewahren, niemals teilen. Wer sie hat, kann dein FND senden.</p>'+
        '<div style="font-family:monospace;font-size:13px;line-height:1.8;background:var(--bg,#0e0e1a);padding:12px;border-radius:8px;word-spacing:6px">'+
        d.words.join(" ")+'</div>'+
        '<button class="wallet-submit" style="margin-top:12px" onclick="navigator.clipboard&&navigator.clipboard.writeText(\''+d.words.join(" ")+'\');this.textContent=\'✓ Kopiert\'">Kopieren</button>';
      ov.appendChild(card);
      document.body.appendChild(ov);
    } catch(e){ alert("✗ "+e.message); }
  };

})();

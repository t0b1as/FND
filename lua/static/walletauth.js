// walletauth.js — zentrale Wallet-Identität für alle Bereiche.
// Eine einzige Login-Session (E-Mail+Passwort ODER Wallet-Seed → FundusID), die
// über Marktplatz, Partner, Messenger und alle Nodes hinweg gilt. Der Schlüssel
// liegt nur serverseitig im RAM (Session), das Frontend hält nur einen HttpOnly-
// Cookie. Login einmal → überall erkannt.

(function(){
  window.WALLET = { fundusID: null, address: null, pubKey: null, loaded: false };

  // Session-Status vom Server holen (/identity/me nutzt den HttpOnly-Cookie).
  // ── Präsenz ("im Netz online") auf JEDER Seite, solange angemeldet ─────────
  // Früher nur auf der Messenger-Seite: wer woanders angemeldet war, fehlte in
  // den Online-Listen. "Für andere sichtbar" (Messenger) wird respektiert.
  let presenceTimer = null;
  function presenceVisible(){
    try { return localStorage.getItem("fundus-msg-visible") !== "0"; } catch(e){ return true; }
  }
  async function walletPresence(online){
    if (!window.WALLET || !window.WALLET.fundusID) return;
    try {
      await fetch("/api/v1/messenger/presence", {method:"POST", credentials:"same-origin",
        headers:{"Content-Type":"application/json"}, body: JSON.stringify({online: online})});
    } catch(e){}
  }
  window.walletPresence = walletPresence;
  function walletPresenceTick(){
    const inn = !!(window.WALLET && window.WALLET.fundusID);
    if (!inn){ if (presenceTimer){ clearInterval(presenceTimer); presenceTimer = null; } return; }
    if (presenceTimer) return;
    // Aktive Sitzungen meldet der NODE als online. Unsichtbar-Wunsch deshalb
    // bei jedem Seitenaufruf mitteilen, sonst gälte man bei Aktivität als online.
    walletPresence(presenceVisible());
    presenceTimer = setInterval(function(){ if (presenceVisible()) walletPresence(true); }, 120000);
  }

  window.walletRefresh = async function(force){
    try {
      const d = await window.fundusMe(!!force);
      if (d){
        window.WALLET = { fundusID: d.fundus_id, address: d.wallet_address || "", linked: !!d.wallet_linked,
                          displayName: d.display_name || "", pubKey: d.ed25519_pub_key, loaded: true };
      } else {
        window.WALLET = { fundusID: null, address: null, pubKey: null, loaded: true };
      }
    } catch(e){
      window.WALLET = { fundusID: null, address: null, pubKey: null, loaded: true };
    }
    window.walletRenderBadge();
    walletPresenceTick(); // angemeldet → im Netz als online melden (alle Seiten)
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
    await walletPresence(false); // vor dem Abmelden offline melden
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
      // Nie die Fundus-ID als Wallet ausgeben: gleiches Format, FND dorthin
      // wären verloren. Ohne bekannte Wallet: 👤 + Fundus-ID.
      const w = window.WALLET.address;
      const a = w || window.WALLET.fundusID;
      const short = a.slice(0,6) + "…" + a.slice(-4);
      // Mit Anzeigenamen: Name im Knopf, Adresse im Tooltip.
      const nm = window.WALLET.displayName;
      const label = nm ? walletEsc(nm) : short;
      el.innerHTML = w
        ? '<button class="wallet-btn wallet-in" title="'+(nm ? walletEsc(nm)+' · ' : '')+'Wallet: '+w+'" onclick="walletOpenMenu()">👛 '+label+'</button>'
        : '<button class="wallet-btn wallet-in" title="'+(nm ? walletEsc(nm)+' · ' : '')+'Fundus-ID: '+a+' – Wallet noch nicht geöffnet" onclick="walletOpenMenu()">👤 '+label+'</button>';
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
    if (ov){ ov.remove(); document.removeEventListener("click", walletMenuCloser); return; }
    ov = document.createElement("div");
    ov.id = "wallet-menu";
    ov.className = "wallet-menu";
    const W = window.WALLET;
    ov.innerHTML =
      '<div class="wallet-menu-addr">' +
        '<span class="meta">Name</span><br>' + (W.displayName ? '<b>' + walletEsc(W.displayName) + '</b>' : '<span class="meta">– noch keiner festgelegt</span>') + '<br>' +
        (W.address
          ? '<span class="meta">Wallet (FND)' + (W.linked ? ' · hinterlegt' : '') + '</span><br>' + W.address
          : '<span class="meta">Wallet noch nicht geöffnet</span>') +
        '<br><span class="meta">Fundus-ID (Kontakt): ' + (W.fundusID||'') + '</span></div>'+
      (W.address ? '<button onclick="navigator.clipboard&&navigator.clipboard.writeText(window.WALLET.address);this.textContent=\'✓ Kopiert\'">Wallet-Adresse kopieren</button>' : '')+
      '<button onclick="walletEditName()">' + (W.displayName ? 'Namen ändern…' : 'Namen festlegen…') + '</button>'+
      '<button onclick="walletOpenWallet()">' + (W.address ? 'Wallet &amp; Guthaben' : 'Wallet öffnen') + '</button>'+
      '<button onclick="walletSolana()">Solana-Wallet</button>'+
      '<button onclick="walletLinkOther()">Andere Wallet hinterlegen…</button>'+
      (W.linked ? '<button onclick="walletUnlink()">Hinterlegung aufheben</button>' : '')+
      '<button onclick="walletShowSeed()">Seed-Wörter anzeigen</button>'+
      '<button onclick="walletPublishEmail()">Per E-Mail auffindbar machen</button>'+
      '<button onclick="walletLogout();document.getElementById(\'wallet-menu\').remove()">Abmelden</button>';
    document.body.appendChild(ov);
    // EIN benannter Wächter (addEventListener mit derselben Funktion ist
    // idempotent). Früher blieb pro Öffnen ein anonymer Wächter hängen, wenn das
    // Menü per Knopf oder Menüpunkt schloss – beim nächsten Öffnen schloss ein
    // solcher Rest das Menü sofort wieder ("geht nur manchmal auf").
    document.addEventListener("click", walletMenuCloser);
  };

  function walletMenuCloser(e){
    const m = document.getElementById("wallet-menu");
    if (!m){ document.removeEventListener("click", walletMenuCloser); return; }
    // Klicks im Menü oder auf den Knopf selbst ignorieren (der Knopf toggelt).
    if (m.contains(e.target) || (e.target.closest && e.target.closest("#wallet-badge"))) return;
    m.remove();
    document.removeEventListener("click", walletMenuCloser);
  }

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
    walletBusy("Seed-Wörter und Wallet-Adresse werden ermittelt (~10 s) …");
    try {
      const r = await fetch("/api/v1/identity/seed");
      walletBusy(null);
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
        (d.chain_address ? '<p class="wallet-hint">Diese Wörter öffnen die Wallet <b style="font-family:monospace">'+d.chain_address+'</b> – überall gleich (auch in fnd-wallet).</p>' : '')+
        (d.linked_differs ? '<p class="wallet-hint" style="color:#fb3">⚠ Für deinen Login ist eine ANDERE Wallet hinterlegt ('+d.linked_address+'). Diese Wörter öffnen sie NICHT – sichere auch deren Seed-Wörter.</p>' : '')+
        '<div style="font-family:monospace;font-size:13px;line-height:1.8;background:var(--bg,#0e0e1a);padding:12px;border-radius:8px;word-spacing:6px">'+
        d.words.join(" ")+'</div>'+
        '<button class="wallet-submit" style="margin-top:12px" onclick="navigator.clipboard&&navigator.clipboard.writeText(\''+d.words.join(" ")+'\');this.textContent=\'✓ Kopiert\'">Kopieren</button>';
      ov.appendChild(card);
      document.body.appendChild(ov);
    } catch(e){ alert("✗ "+e.message); }
  };

  // ── Wallet öffnen / hinterlegen / umziehen (R456) ─────────────────────────
  // Die Wallet wird wie fnd-wallet abgeleitet (256 MiB, ~10 s auf dem Pi) –
  // deshalb nur auf Wunsch, nicht beim Login. Hinterlegt, steht sie danach bei
  // jedem Login sofort bereit.
  function walletEsc(v){ return String(v==null?"":v).replace(/[&<>"']/g, function(c){ return {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]; }); }
  window.walletBusy = function(msg){
    let b = document.getElementById("wallet-busy");
    if (!msg){ if (b) b.remove(); return; }
    if (!b){ b = document.createElement("div"); b.id = "wallet-busy"; b.className = "wallet-overlay";
      b.innerHTML = '<div class="wallet-card"><p class="wallet-hint" id="wallet-busy-txt"></p></div>'; document.body.appendChild(b); }
    document.getElementById("wallet-busy-txt").textContent = "⏳ " + msg;
  };
  function walletCard(title, html){
    const ov = document.createElement("div");
    ov.className = "wallet-overlay";
    ov.onclick = function(e){ if (e.target === ov) ov.remove(); };
    ov.innerHTML = '<div class="wallet-card"><div class="wallet-card-head"><strong>'+walletEsc(title)+'</strong>'+
      '<button class="wallet-x" onclick="this.closest(\'.wallet-overlay\').remove()">✕</button></div>'+html+'</div>';
    document.body.appendChild(ov);
    return ov;
  }
  async function walletPost(url, body, method){
    const r = await fetch(url, {method: method||"POST", credentials:"same-origin",
      headers:{"Content-Type":"application/json"}, body: JSON.stringify(body||{})});
    const d = await r.json().catch(function(){ return {}; });
    if (!r.ok || d.error) throw new Error(d.error || ("HTTP " + r.status));
    return d;
  }

  window.walletOpenWallet = async function(words){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    walletBusy("Wallet wird abgeleitet (Argon2id 256 MiB, ~10 s) …");
    try {
      const d = await walletPost("/api/v1/wallet/open", words ? {words: words} : {});
      walletBusy(null);
      let html = '<p class="wallet-hint">Wallet-Adresse:</p>'+
        '<div style="font-family:monospace;font-size:13px;word-break:break-all">'+walletEsc(d.address)+'</div>'+
        '<p class="wallet-hint" style="margin-top:6px">Guthaben: <b>'+walletEsc(d.fnd)+' FND</b></p>';
      if (d.old_address) {
        html += '<p class="wallet-hint" style="color:#fb3">Auf deiner alten Adresse (vor R456) liegen noch <b>'+walletEsc(d.old_fnd)+' FND</b> ('+walletEsc(d.old_address)+').</p>'+
          '<button class="wallet-submit" id="w-migrate">Guthaben auf die neue Adresse umziehen</button>';
      }
      if (!d.is_linked) {
        html += '<button class="wallet-submit" id="w-link" style="margin-top:8px">Als Login-Wallet hinterlegen</button>'+
          '<p class="wallet-hint">Hinterlegt steht die Wallet bei jedem Login sofort bereit (verschlüsselt, nur auf diesem Node).'+
          (d.linked_address ? ' Ersetzt die bisher hinterlegte '+walletEsc(d.linked_address)+'.' : '')+'</p>';
      } else {
        html += '<p class="wallet-hint">✓ Diese Wallet ist für deinen Login hinterlegt.</p>';
      }
      html += '<div class="wallet-hint" id="w-out"></div>';
      const ov = walletCard("Deine Wallet", html);
      const out = ov.querySelector("#w-out");
      const mig = ov.querySelector("#w-migrate");
      if (mig) mig.onclick = async function(){
        mig.disabled = true; out.textContent = "⏳ Umzug läuft …";
        try { const r = await walletPost("/api/v1/wallet/migrate", words ? {words: words} : {});
          out.textContent = "✓ " + r.amount + " FND an " + r.to + " übertragen (Tx " + String(r.tx_hash).slice(0,16) + "…)";
        } catch(e){ out.textContent = "✗ " + e.message; mig.disabled = false; }
      };
      const lnk = ov.querySelector("#w-link");
      if (lnk) lnk.onclick = async function(){
        lnk.disabled = true; out.textContent = "⏳ Wird hinterlegt …";
        try { const r = await walletPost("/api/v1/wallet/link", words ? {words: words} : {});
          out.textContent = "✓ Hinterlegt: " + r.address;
          if (window.walletRefresh) await walletRefresh(true);
        } catch(e){ out.textContent = "✗ " + e.message; lnk.disabled = false; }
      };
      if (window.walletRefresh) walletRefresh(true);
    } catch(e){ walletBusy(null); alert("✗ " + e.message); }
  };

  // Andere Wallet (z.B. Fee-Collector) per Seed-Wörtern öffnen und hinterlegen.
  window.walletLinkOther = function(){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    const ov = walletCard("Andere Wallet hinterlegen",
      '<p class="wallet-hint">Seed-Wörter der Wallet eingeben, die für deinen Login gelten soll (z.B. eine mit fnd-wallet erzeugte). Die Wörter werden nicht gespeichert – nur der daraus abgeleitete Schlüssel, verschlüsselt und nur auf diesem Node.</p>'+
      '<textarea id="w-other-words" rows="4" style="width:100%;font-family:monospace" autocomplete="off" spellcheck="false"></textarea>'+
      '<button class="wallet-submit" id="w-other-go" style="margin-top:8px">Wallet öffnen</button>');
    ov.querySelector("#w-other-go").onclick = function(){
      const words = ov.querySelector("#w-other-words").value.trim().split(/\s+/).filter(Boolean);
      if (words.length < 10) { alert("Bitte die Seed-Wörter vollständig eingeben."); return; }
      ov.remove();
      walletOpenWallet(words);
    };
  };

  // Anzeigename (signiert, netzweit): so sehen dich andere im Messenger.
  window.walletEditName = function(){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    const cur = (window.WALLET && window.WALLET.displayName) || "";
    const ov = walletCard("Dein Name",
      '<p class="wallet-hint">So sehen dich andere im Messenger – in Kontakten, „Im Netz online“ und bei deinen Nachrichten. 1–32 Zeichen; leer lassen zum Entfernen.</p>'+
      '<input type="text" class="wallet-input" id="w-name" maxlength="32" autocomplete="nickname" placeholder="z.B. Tobias" value="'+walletEsc(cur)+'">'+
      '<button class="wallet-submit" id="w-name-save">Speichern</button>'+
      '<p class="wallet-hint" id="w-name-out"></p>');
    const inp = ov.querySelector("#w-name"), out = ov.querySelector("#w-name-out"), btn = ov.querySelector("#w-name-save");
    async function save(){
      btn.disabled = true; out.textContent = "⏳ Wird gespeichert …";
      try {
        const d = await walletPost("/api/v1/identity/name", {name: inp.value});
        out.textContent = d.name ? "✓ Andere sehen dich jetzt als „" + d.name + "“." : "✓ Name entfernt.";
        if (window.walletRefresh) await walletRefresh(true);
        setTimeout(function(){ ov.remove(); }, 1200);
      } catch(e){ out.textContent = "✗ " + e.message; btn.disabled = false; }
    }
    btn.onclick = save;
    inp.addEventListener("keydown", function(e){ if (e.key === "Enter") save(); });
    setTimeout(function(){ inp.focus(); inp.select(); }, 50);
  };

  // ── Solana-Wallet (aus derselben Wallet abgeleitet) ───────────────────────
  window.walletSolana = async function(){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    walletBusy("Solana-Wallet wird geladen …" + (window.WALLET && window.WALLET.linked ? "" : " (Wallet nicht hinterlegt: ~10 s)"));
    let d;
    try {
      const r = await fetch("/api/v1/wallet/sol", {credentials:"same-origin"});
      d = await r.json();
      walletBusy(null);
      if (!r.ok || d.error) throw new Error(d.error || ("HTTP " + r.status));
    } catch(e){ walletBusy(null); alert("✗ " + e.message); return; }
    const bal = (d.sol != null) ? (Number(d.sol).toFixed(6).replace(".", ",") + " SOL") : ("– (" + walletEsc(d.balance_error || "nicht abrufbar") + ")");
    const ov = walletCard("Solana-Wallet",
      '<p class="wallet-hint">Aus deiner Fundus-Wallet abgeleitet – dieselben Seed-Wörter ergeben auf jedem Node dieselbe Adresse. Im Shop wird sie automatisch verwendet.</p>'+
      '<div style="font-family:monospace;font-size:13px;word-break:break-all">'+walletEsc(d.address)+'</div>'+
      '<p class="wallet-hint" style="margin-top:6px">Guthaben: <b>'+bal+'</b>'+(d.cluster ? ' <span style="color:#fb3">(' + walletEsc(d.cluster) + ' – Testnetz)</span>' : '')+'</p>'+
      '<button class="wallet-submit" id="sol-copy">Adresse kopieren</button>'+
      '<p class="wallet-hint" style="margin-top:12px"><b>SOL senden</b></p>'+
      '<input class="wallet-input" id="sol-to" placeholder="Empfänger (Solana-Adresse)" autocomplete="off" spellcheck="false">'+
      '<input class="wallet-input" id="sol-amt" type="number" min="0" step="0.000001" placeholder="Betrag in SOL">'+
      '<button class="wallet-submit" id="sol-send">Senden</button>'+
      '<p class="wallet-hint" id="sol-out"></p>'+
      '<p class="wallet-hint" style="margin-top:12px"><a href="#" id="sol-export">Für Phantom/Solflare exportieren …</a></p>');
    ov.querySelector("#sol-copy").onclick = function(){
      if (navigator.clipboard) navigator.clipboard.writeText(d.address);
      this.textContent = "✓ Kopiert";
    };
    const out = ov.querySelector("#sol-out");
    ov.querySelector("#sol-send").onclick = async function(){
      const to = ov.querySelector("#sol-to").value.trim();
      const amt = parseFloat(String(ov.querySelector("#sol-amt").value).replace(",", "."));
      if (!to || !(amt > 0)) { out.textContent = "Bitte Empfänger und Betrag eingeben."; return; }
      if (!confirm(amt + " SOL an\n" + to + "\nsenden? Solana-Überweisungen sind endgültig.")) return;
      const b = this; b.disabled = true; out.textContent = "⏳ Wird gesendet …";
      try {
        const r = await walletPost("/api/v1/wallet/sol/send", {to: to, amount_sol: amt});
        const cl = r.cluster ? "?cluster=" + encodeURIComponent(r.cluster) : "";
        out.innerHTML = "✓ Gesendet – <a href=\"https://explorer.solana.com/tx/" + encodeURIComponent(r.signature) + cl + "\" target=\"_blank\" rel=\"noopener\">im Explorer ansehen</a>";
      } catch(e){ out.textContent = "✗ " + e.message; }
      b.disabled = false;
    };
    ov.querySelector("#sol-export").onclick = async function(e){
      e.preventDefault();
      if (!confirm("Privaten Schlüssel anzeigen?\n\nWer diesen Schlüssel hat, kann über dein SOL verfügen. Nur in einer eigenen Wallet-App (Phantom, Solflare) importieren, niemals weitergeben.")) return;
      try {
        const r = await walletPost("/api/v1/wallet/sol/export", {});
        out.innerHTML = '<span style="color:#f66">Privater Schlüssel (Base58) – in Phantom unter „Privaten Schlüssel importieren“:</span><br>'+
          '<code style="word-break:break-all;font-size:12px">'+walletEsc(r.secret_base58)+'</code>';
      } catch(err){ out.textContent = "✗ " + err.message; }
    };
  };

  window.walletUnlink = async function(){
    const m = document.getElementById("wallet-menu"); if (m) m.remove();
    if (!confirm("Hinterlegte Wallet für diesen Login entfernen? Die Wallet selbst und ihr Guthaben bleiben unberührt.")) return;
    try { await walletPost("/api/v1/wallet/link", {}, "DELETE"); if (window.walletRefresh) await walletRefresh(true); alert("✓ Hinterlegung entfernt."); }
    catch(e){ alert("✗ " + e.message); }
  };

})();

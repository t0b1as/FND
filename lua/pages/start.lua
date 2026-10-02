-- pages/start.lua – Erste-Schritte-Assistent für den Betreiber (R551).
-- Führt nach der Installation durch: Admin-Passwort, Node-Wallet, Staking,
-- Sicherung, Konto & Benachrichtigungen, Domain. Jeder Schritt zeigt seinen
-- Live-Status. Betreiber-Abfragen (Sicherung) sind nur im Heimnetz möglich.
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    render.header("nav.start_label", "start")
    ngx.print([==[
<section class="st-wrap">
  <h1>Erste Schritte</h1>
  <p class="meta">Dein Node läuft. Diese Schritte machen ihn vollständig – jeder zeigt, ob er schon erledigt ist.</p>
  <div class="st-progress"><div class="st-bar"><div class="st-fill" id="st-fill"></div></div><span id="st-count" class="meta">…</span></div>

  <div class="st-step" id="step-admin">
    <div class="st-head"><span class="st-dot"></span><h3>1 · Admin-Passwort</h3></div>
    <p>Schützt Einstellungen, Dateiverwaltung und Betreiber-Aktionen. Es wurde bei der Installation gesetzt und ist nur aus dem Heimnetz erreichbar – aus dem Internet gibt es diesen Bereich gar nicht.</p>
    <p class="meta">Ändern: auf dem Pi <code>sudo bash install.sh</code> erneut ausführen (fragt das Passwort ab, Daten bleiben erhalten).</p>
  </div>

  <div class="st-step" id="step-wallet">
    <div class="st-head"><span class="st-dot"></span><h3>2 · Node-Wallet</h3></div>
    <p>Jeder Node hat eine <b>eigene Wallet</b>, getrennt von den Konten der Nutzer. Sie wurde beim ersten Start automatisch erzeugt. Mit ihr
    <b>signiert der Node Blöcke</b>, empfängt er die <b>Vergütung für angebotenen Speicher</b> und <b>stakt</b> er FND, um Validator zu werden.</p>
    <div class="st-kv"><span>Adresse</span><code id="st-node-addr">…</code></div>
    <div class="st-kv"><span>Guthaben</span><b id="st-node-bal">…</b></div>
    <div class="st-kv"><span>Signierschlüssel</span><span id="st-node-sign">…</span></div>
    <p class="meta">Wichtig: Die <b>30 Seed-Wörter</b> der Node-Wallet einmal notieren und sicher verwahren (Wallet-Seite → Node-Seed). Optional lässt sich der Seed mit einem Passwort sichern – dann muss er nach jedem Neustart entsperrt werden.</p>
    <a class="btn btn-outline" href="/wallet">Zur Wallet-Seite</a>
  </div>

  <div class="st-step" id="step-stake">
    <div class="st-head"><span class="st-dot"></span><h3>3 · Validator werden (Staking)</h3></div>
    <p>Validatoren erzeugen die Blöcke der Fundus-Chain und sichern das Netz. Dafür sperrt die Node-Wallet FND als <b>Einsatz (Stake)</b>.
    Der Mindest-Stake beträgt <b>10 FND</b>. Weil die Stake-Transaktion wie jede Transaktion <b>1,8 % Gebühr</b> kostet, müssen
    <b>10,18 FND frei auf der Node-Wallet</b> liegen – sonst lehnt die Chain die Transaktion ab.</p>
    <div class="st-kv"><span>Gestakt</span><b id="st-stake">…</b></div>
    <div class="st-kv"><span>Status</span><span id="st-validator">…</span></div>
    <p class="meta" id="st-stake-hint"></p>
    <p class="meta">FND auf die Node-Wallet bekommen: im Shop SOL gegen FND tauschen und an die Adresse oben senden, von einem anderen Node überweisen lassen, oder Speicher anbieten und die Vergütung sammeln.
    Gestakte FND bleiben gebunden, solange der Node Validator ist; „Freigeben“ holt sie zurück.</p>
    <a class="btn btn-outline" href="/wallet#stake">Staken auf der Wallet-Seite</a>
  </div>

  <div class="st-step" id="step-backup">
    <div class="st-head"><span class="st-dot"></span><h3>4 · Sicherung</h3></div>
    <p>Täglich verschlüsselt: Node-Wallet, Sitzungen, Swaps, Orders, Hinterlegungen, Datenbank und Konfiguration – abgelegt mit 3 Kopien auf anderen Nodes.
    Auf einem neuen Pi genügt das <b>Sicherungspasswort</b> zum Wiederherstellen. Ohne Passwort ist die Sicherung für niemanden lesbar – gut aufbewahren.</p>
    <div id="st-backup-form" style="display:none">
      <input type="password" id="st-bk1" placeholder="Sicherungspasswort (mind. 12 Zeichen)" autocomplete="new-password" style="width:100%;margin:6px 0">
      <input type="password" id="st-bk2" placeholder="Wiederholen" autocomplete="new-password" style="width:100%;margin:0 0 8px">
      <button class="btn" id="st-bk-btn">Einrichten und jetzt sichern</button>
    </div>
    <p class="meta" id="st-backup-out"></p>
  </div>

  <div class="st-step" id="step-account">
    <div class="st-head"><span class="st-dot"></span><h3>5 · Konto und Benachrichtigungen</h3></div>
    <p>Der Node ist die Infrastruktur, dein <b>Konto</b> ist deine Identität darauf: zum Chatten, Kaufen, Verkaufen und Tauschen. Es entsteht aus E-Mail und Passwort direkt auf dem Node – kein Server kennt den Schlüssel.
    Danach kannst du <b>Push-Benachrichtigungen</b> für dieses Gerät einschalten.</p>
    <div class="st-kv"><span>Konto</span><span id="st-acc">…</span></div>
    <div class="st-kv"><span>Push auf diesem Gerät</span><span id="st-push">…</span></div>
    <div style="display:flex;gap:8px;flex-wrap:wrap">
      <button class="btn btn-outline" id="st-login-btn" style="width:auto">Anmelden / Konto anlegen</button>
      <button class="btn btn-outline" id="st-push-btn" style="width:auto">Benachrichtigungen</button>
    </div>
  </div>

  <div class="st-step" id="step-domain">
    <div class="st-head"><span class="st-dot"></span><h3>6 · Eigene Domain und gültiges Zertifikat <span class="meta">(optional)</span></h3></div>
    <p>Für den Zugriff von unterwegs: im Router <b>Port 443</b> auf den Pi weiterleiten, einen kostenlosen DynDNS-Namen anlegen (z.B. DuckDNS) und dann auf dem Pi:</p>
    <pre class="st-code">curl -fsSL https://raw.githubusercontent.com/t0b1as/FND/main/tools/setup-letsencrypt.sh -o setup-letsencrypt.sh
sudo bash setup-letsencrypt.sh deine-domain.duckdns.org deine@mail.de</pre>
    <p class="meta">Das holt ein Let’s-Encrypt-Zertifikat über Port 443 und verlängert es automatisch. Mit gültigem Zertifikat funktionieren auch Push auf dem iPhone sowie Anrufe und Kamera.</p>
    <div class="st-kv"><span>Aktuell</span><span id="st-domain">…</span></div>
  </div>

  <p class="meta" style="margin-top:18px"><a href="#" id="st-hide">Diese Karte auf der Startseite ausblenden</a></p>
</section>
<style>
.st-wrap { max-width: 820px; margin: 0 auto; }
.st-progress { display:flex; align-items:center; gap:12px; margin: 14px 0 20px; }
.st-bar { flex:1; height:10px; background:var(--surface-2); border-radius:5px; overflow:hidden; }
.st-fill { height:100%; width:0; background:linear-gradient(90deg,#17a862,#00e676); transition:width .4s ease; }
.st-step { background:var(--surface); border:1px solid var(--border); border-radius:var(--r-lg,16px); padding:16px 18px; margin:12px 0; }
.st-step p { margin: 8px 0; line-height:1.5; }
.st-head { display:flex; align-items:center; gap:10px; }
.st-head h3 { margin:0; }
.st-dot { width:14px; height:14px; border-radius:50%; background:var(--surface-2); border:2px solid var(--border-2); flex-shrink:0; }
.st-step.done .st-dot { background:var(--green); border-color:var(--green); box-shadow:0 0 10px rgba(0,230,118,.5); }
.st-step.done .st-head h3 { color: var(--green); }
.st-kv { display:flex; justify-content:space-between; gap:12px; padding:6px 0; border-bottom:1px solid var(--border); font-size:14px; }
.st-kv span:first-child { color:var(--muted); }
.st-kv code { word-break:break-all; font-size:12px; text-align:right; }
.st-code { background:var(--bg); border:1px solid var(--border); border-radius:10px; padding:10px 12px; font-size:12px; overflow-x:auto; white-space:pre; }
.st-step .btn { margin-top: 8px; }
</style>
<script>
(function(){
  var el = function(id){ return document.getElementById(id); };
  var done = {};
  function mark(step, ok){ done[step] = !!ok; var s = el('step-' + step); if (s) s.classList.toggle('done', !!ok); progress(); }
  function progress(){
    var keys = ['admin','wallet','stake','backup','account','push'];
    var n = keys.filter(function(k){ return done[k]; }).length;
    el('st-fill').style.width = (n / keys.length * 100) + '%';
    el('st-count').textContent = n + ' von ' + keys.length + ' erledigt';
    if (n === keys.length) localStorage.setItem('fundus.start.done', '1');
  }
  async function j(u){ var r = await fetch(u, {credentials:'same-origin'}); var d = {}; try { d = await r.json(); } catch(e){} return r.ok ? d : null; }
  async function post(u, b){ var r = await fetch(u, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(b||{})}); var d = {}; try { d = await r.json(); } catch(e){} if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status)); return d; }

  mark('admin', true); // beim Installieren gesetzt

  // 2 + 3: Node-Wallet und Staking
  (async function(){
    var e = await j('/api/v1/files/earnings');
    var addr = e && e.node_address;
    if (!addr) { el('st-node-addr').textContent = 'Dateispeicher aus – Node-Wallet-Anzeige nicht verfügbar'; return; }
    el('st-node-addr').textContent = addr;
    el('st-node-sign').textContent = e.signing_active ? '✓ aktiv' : '⚠ gesperrt – auf der Wallet-Seite entsperren';
    var b = await j('/api/v1/wallet/balance?address=' + encodeURIComponent(addr));
    var bal = b ? parseFloat(b.fnd || b.balance_fnd || 0) : 0;
    el('st-node-bal').textContent = (isFinite(bal) ? String(bal).replace('.', ',') : '–') + ' FND';
    mark('wallet', !!e.signing_active);
    var c = await j('/api/v1/chain/status');
    if (c) {
      var st = parseFloat(c.my_stake_fnd) || 0;
      el('st-stake').textContent = String(st).replace('.', ',') + ' FND';
      el('st-validator').textContent = c.i_am_validator ? '✓ Validator' : 'kein Validator';
      mark('stake', !!c.i_am_validator);
      if (!c.i_am_validator) {
        var need = 10.18 - bal;
        el('st-stake-hint').textContent = bal >= 10.18 ? 'Guthaben reicht – auf der Wallet-Seite 10 FND staken.' :
          (c.hint ? c.hint + ' ' : '') + 'Es fehlen noch ' + need.toFixed(2).replace('.', ',') + ' FND auf der Node-Wallet, um 10 FND (+1,8 % Gebühr) zu staken.';
      }
    }
  })();

  // 4: Sicherung (nur Heimnetz + Admin-Passwort)
  (async function(){
    var r = await fetch('/api/v1/admin/backup/status', {credentials:'same-origin'});
    if (r.status === 403 || r.status === 401) { el('st-backup-out').textContent = 'Nur aus dem Heimnetz einrichtbar (Admin-Passwort).'; return; }
    var d = {}; try { d = await r.json(); } catch(e){}
    if (d.enabled) { mark('backup', true); el('st-backup-out').textContent = d.last_time ? '✓ Letzte Sicherung: ' + new Date(d.last_time*1000).toLocaleString('de-DE') : '✓ Eingerichtet – erste Sicherung läuft.'; return; }
    if (!d.filestore) { el('st-backup-out').textContent = 'Dateispeicher aus – die Sicherung braucht ihn (FUNDUS_STORAGE_OFFER_GB).'; return; }
    el('st-backup-form').style.display = '';
    el('st-bk-btn').onclick = async function(){
      var a = el('st-bk1').value, b2 = el('st-bk2').value, out = el('st-backup-out');
      if (a.length < 12) { out.textContent = 'Mindestens 12 Zeichen.'; return; }
      if (a !== b2) { out.textContent = 'Die Passwörter stimmen nicht überein.'; return; }
      this.disabled = true;
      try { await post('/api/v1/admin/backup/setup', {password: a}); out.textContent = '✓ Eingerichtet – die erste Sicherung läuft im Hintergrund.'; el('st-backup-form').style.display = 'none'; mark('backup', true); }
      catch(e){ out.textContent = '✗ ' + e.message; this.disabled = false; }
    };
  })();

  // 5: Konto und Push
  (async function(){
    var me = await j('/api/v1/identity/me');
    var logged = !!(me && me.fundus_id);
    el('st-acc').textContent = logged ? '✓ angemeldet' : 'nicht angemeldet';
    mark('account', logged);
    el('st-login-btn').onclick = function(){ var b = document.getElementById('wallet-badge'); if (b && b.querySelector('button')) b.querySelector('button').click(); else location.href = '/messenger'; };
    el('st-push-btn').onclick = function(){ if (window.walletPush) window.walletPush(); };
    var on = false;
    try {
      if ('serviceWorker' in navigator && 'PushManager' in window) {
        var reg = await navigator.serviceWorker.ready; var sub = await reg.pushManager.getSubscription();
        if (sub && logged) { var s = await j('/api/v1/push/status?endpoint=' + encodeURIComponent(sub.endpoint)); on = !!(s && s.subscribed); }
      }
    } catch(e){}
    el('st-push').textContent = on ? '✓ eingeschaltet' : 'aus';
    mark('push', on);
  })();

  // 6: Domain
  (function(){
    var isIP = /^\d+\.\d+\.\d+\.\d+$/.test(location.hostname) || location.hostname.indexOf(':') >= 0;
    var ok = location.protocol === 'https:' && !isIP && location.hostname !== 'localhost' && !/\.local$/.test(location.hostname);
    el('st-domain').textContent = ok ? '✓ ' + location.hostname : (location.protocol === 'https:' ? 'https über ' + location.hostname + ' (selbstsigniert)' : 'nur http');
    var s = el('step-domain'); if (s) s.classList.toggle('done', ok);
  })();

  el('st-hide').onclick = function(e){ e.preventDefault(); localStorage.setItem('fundus.start.done', '1'); this.textContent = 'Ausgeblendet. Der Assistent bleibt über das Menü erreichbar.'; };
})();
</script>
]==])
    render.footer()
end

-- Funkchat (R945): Nachrichten über LoRa/Reticulum (LXMF).
-- Gegenstellen: andere Fundus-Nodes mit Funkmodul, Sideband, MeshChat,
-- Nomad Network. Der Verlauf liegt beim Funkdienst dieses Nodes.
local render = require "render"

return function()
    render.header("Funkchat", "funkchat")
    ngx.print([==[
<style>
.fc { max-width:980px; margin:0 auto; }
.fc-kopf { display:flex; align-items:center; gap:10px; flex-wrap:wrap; margin:4px 0 10px; }
.fc-kopf h1 { font-size:20px; margin:0; }
.fc-status { font-size:12.5px; color:var(--text-dim,#9aa0b4); }
.fc-status b.ok { color:var(--green,#3ddc84); } .fc-status b.aus { color:var(--danger,#e5484d); }
.fc-karte { background:var(--surface,#161a22); border:1px solid var(--border,#2a2f3a); border-radius:12px; padding:12px 14px; margin-bottom:12px; }
.fc-karte h2 { font-size:14.5px; margin:0 0 8px; }
.fc-ich { display:flex; flex-wrap:wrap; gap:8px 14px; align-items:center; }
.fc-ich input[type=text] { width:180px; padding:7px 9px; border-radius:8px; }
.fc-adr { font-family:ui-monospace,Menlo,Consolas,monospace; font-size:13px; padding:4px 8px; border-radius:6px; background:rgba(127,127,127,.15); user-select:all; overflow-wrap:anywhere; }
.fc-hint { font-size:12.5px; color:var(--text-dim,#9aa0b4); margin:6px 0 0; line-height:1.45; }
.fc-haupt { display:grid; grid-template-columns:300px 1fr; gap:12px; min-height:440px; }
.fc-liste { display:flex; flex-direction:column; gap:12px; }
.fc-eintrag { display:block; width:100%; text-align:left; padding:8px 10px; border-radius:9px; border:1px solid transparent; background:none; color:inherit; cursor:pointer; font:inherit; }
.fc-eintrag:hover { background:rgba(127,127,127,.1); }
.fc-eintrag.aktiv { border-color:var(--green,#3ddc84); background:rgba(61,220,132,.08); }
.fc-eintrag .n { font-weight:600; font-size:14px; }
.fc-eintrag .u { font-size:12px; color:var(--text-dim,#9aa0b4); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.fc-neu { display:flex; gap:6px; margin-top:6px; }
.fc-neu input { flex:1; min-width:0; padding:7px 9px; border-radius:8px; font-family:ui-monospace,Menlo,Consolas,monospace; font-size:12.5px; }
.fc-chat { display:flex; flex-direction:column; min-height:440px; }
.fc-chat-kopf { font-size:14px; padding-bottom:8px; border-bottom:1px solid var(--border,#2a2f3a); }
.fc-chat-kopf small { display:block; color:var(--text-dim,#9aa0b4); font-family:ui-monospace,Menlo,Consolas,monospace; font-size:11.5px; }
.fc-verlauf { flex:1; overflow-y:auto; padding:10px 2px; display:flex; flex-direction:column; gap:8px; max-height:52vh; }
.fc-n { max-width:82%; padding:8px 11px; border-radius:12px; font-size:14.5px; line-height:1.45; overflow-wrap:anywhere; white-space:pre-wrap; }
.fc-n.aus { align-self:flex-end; background:rgba(61,220,132,.13); border:1px solid rgba(61,220,132,.28); }
.fc-n.ein { align-self:flex-start; background:var(--surface-2,#1b1f28); border:1px solid var(--border,#2a2f3a); }
.fc-n .z { display:block; font-size:11px; color:var(--text-dim,#9aa0b4); margin-top:3px; text-align:right; }
.fc-eingabe { display:flex; gap:8px; align-items:flex-end; padding-top:8px; border-top:1px solid var(--border,#2a2f3a); }
.fc-eingabe textarea { flex:1; resize:none; min-height:44px; max-height:140px; padding:10px 11px; border-radius:10px; font-size:16px; }
.fc-zaehler { font-size:11.5px; color:var(--text-dim,#9aa0b4); text-align:right; margin-top:3px; }
.fc-leer { margin:auto; color:var(--text-dim,#9aa0b4); text-align:center; font-size:14px; max-width:420px; line-height:1.5; }
.fc-fehler { color:var(--danger,#e5484d); font-size:13px; margin-top:6px; }
@media (max-width:760px) { .fc-haupt { grid-template-columns:1fr; } .fc-verlauf { max-height:46vh; } }
</style>
<div class="fc">
  <div class="fc-kopf"><h1>📡 Funkchat</h1><span class="fc-status" id="fc-status">Lade …</span></div>
  <div id="fc-fehlt" class="fc-karte" style="display:none"></div>

  <div class="fc-karte" id="fc-ich-karte" style="display:none">
    <h2>Deine Funkadresse</h2>
    <div class="fc-ich">
      <label style="display:flex;gap:6px;align-items:center;font-weight:600"><input type="checkbox" id="fc-erreichbar"> Über Funk erreichbar</label>
      <input type="text" id="fc-name" maxlength="40" placeholder="Anzeigename">
      <button type="button" class="btn-sm" id="fc-speichern">Übernehmen</button>
      <span class="fc-adr" id="fc-adresse" style="display:none"></span>
      <button type="button" class="btn-sm" id="fc-kopieren" style="display:none">Kopieren</button>
      <button type="button" class="btn-sm" id="fc-ankuendigen" style="display:none">Jetzt ankündigen</button>
    </div>
    <p class="fc-hint">Eingeschaltet kündigt dein Node dich alle 30 Minuten per Funk an – mit dem Anzeigenamen, für alle in Reichweite sichtbar. Die Adresse wird aus deiner Wallet abgeleitet und ist auf jedem Fundus-Node gleich. Sideband, MeshChat und Nomad Network können dir direkt schreiben. Nachrichten sind Ende-zu-Ende-verschlüsselt.</p>
    <div class="fc-fehler" id="fc-ich-fehler"></div>
  </div>

  <div class="fc-haupt" id="fc-haupt" style="display:none">
    <div class="fc-liste">
      <div class="fc-karte" style="margin:0">
        <h2>Gespräche</h2>
        <div id="fc-gespraeche"><div class="fc-hint">Noch keine.</div></div>
        <div class="fc-neu"><input id="fc-neu-adr" placeholder="Funkadresse (32 Zeichen)" maxlength="40"><button type="button" class="btn-sm" id="fc-neu-los">Schreiben</button></div>
      </div>
      <div class="fc-karte" style="margin:0">
        <h2>In Reichweite gehört</h2>
        <div id="fc-gehoert"><div class="fc-hint">Noch niemand – Ankündigungen kommen etwa alle 30 Minuten.</div></div>
      </div>
    </div>
    <div class="fc-karte fc-chat" style="margin:0">
      <div class="fc-chat-kopf" id="fc-chat-kopf">Kein Gespräch gewählt</div>
      <div class="fc-verlauf" id="fc-verlauf"><div class="fc-leer">Wähle links ein Gespräch, jemanden aus der Reichweite oder gib eine Funkadresse ein.</div></div>
      <div class="fc-eingabe"><textarea id="fc-text" rows="1" maxlength="2000" placeholder="Nachricht … (über Funk lieber kurz)" disabled></textarea><button type="button" id="fc-senden" disabled>Senden</button></div>
      <div class="fc-zaehler" id="fc-zaehler"></div>
      <div class="fc-fehler" id="fc-chat-fehler"></div>
    </div>
  </div>
</div>
<script>
(function(){
  var $ = function(id){ return document.getElementById(id); };
  function esc(t){ return String(t == null ? '' : t).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function api(pfad, daten){
    var o = { credentials: 'same-origin' };
    if (daten !== undefined) { o.method = 'POST'; o.headers = { 'Content-Type': 'application/json' }; o.body = JSON.stringify(daten); }
    return fetch('/api/v1/lora' + pfad, o).then(function(r){
      return r.json().catch(function(){ return {}; }).then(function(d){ if (!r.ok) throw new Error(d.error || ('Fehler ' + r.status)); return d; });
    });
  }
  function vor(sek){
    var d = Math.max(0, Math.round(Date.now() / 1000 - sek));
    if (d < 60) return 'gerade eben'; if (d < 3600) return 'vor ' + Math.round(d / 60) + ' min';
    if (d < 86400) return 'vor ' + Math.round(d / 3600) + ' h'; return 'vor ' + Math.round(d / 86400) + ' Tagen';
  }
  function uhr(sek){ var t = new Date(sek * 1000); return t.toLocaleDateString('de-DE') === new Date().toLocaleDateString('de-DE') ? t.toLocaleTimeString('de-DE', {hour:'2-digit', minute:'2-digit'}) : t.toLocaleString('de-DE', {day:'2-digit', month:'2-digit', hour:'2-digit', minute:'2-digit'}); }
  var STATUS = { wartet: '⏳ wartet auf Gegenstelle', unterwegs: '↗ unterwegs', zugestellt: '✓ zugestellt', gescheitert: '✗ nicht zugestellt', empfangen: '' };
  var aktiv = new URLSearchParams(location.search).get('mit') || '';
  var namen = {}, erreichbar = false;

  function statusLaden(){
    return api('/status').then(function(s){
      if (!s.installiert) {
        $('fc-status').textContent = '';
        $('fc-fehlt').style.display = '';
        $('fc-fehlt').innerHTML = '<h2>Funkdienst nicht eingerichtet</h2><p class="fc-hint">Steck ein Funkmodul mit RNode-Firmware (z. B. Heltec WiFi LoRa 32 V3) per USB an diesen Pi und führe den Deploy erneut aus – oder auf dem Pi: <code>sudo bash /opt/fundus/tools/setup-lora.sh</code>.</p>';
        return false;
      }
      var rn = (s.schnittstellen || []).filter(function(i){ return /RNode/.test(i.name); })[0];
      var teile = [];
      if (rn) {
        teile.push(rn.online ? '<b class="ok">● Funkmodul bereit</b>' : '<b class="aus">● Funkmodul offline</b>');
        if (s.funk) teile.push((s.funk.frequenz / 1e6).toFixed(3) + ' MHz · SF' + s.funk.sf + ' · ' + (s.funk.bandbreite / 1000) + ' kHz');
        if (rn.airtime_lang != null) teile.push('Sendezeit ' + rn.airtime_lang.toFixed(1) + ' % (Grenze ' + (s.funk ? s.funk.airtime_lang : '?') + ' %)');
      } else {
        teile.push('<b class="aus">● kein Funkmodul</b>' + (s.geraet_fehler ? ' – ' + esc(s.geraet_fehler) : ''));
      }
      if ((s.schnittstellen || []).some(function(i){ return /TCP/.test(i.name) && i.online; })) teile.push('TCP-Verbindung aktiv');
      $('fc-status').innerHTML = teile.join(' · ');
      $('fc-ich-karte').style.display = ''; $('fc-haupt').style.display = '';
      return true;
    }).catch(function(e){ $('fc-status').textContent = e.message; return false; });
  }

  function kontoLaden(){
    return api('/konto').then(function(k){
      erreichbar = !!k.erreichbar;
      $('fc-erreichbar').checked = erreichbar;
      if (k.name) $('fc-name').value = k.name;
      ['fc-adresse', 'fc-kopieren', 'fc-ankuendigen'].forEach(function(id){ $(id).style.display = erreichbar ? '' : 'none'; });
      $('fc-adresse').textContent = k.adresse ? '<' + k.adresse + '>' : '';
      $('fc-text').disabled = $('fc-senden').disabled = !(erreichbar && aktiv);
      if (!erreichbar) $('fc-text').placeholder = 'Zum Senden oben „Über Funk erreichbar“ einschalten';
      else $('fc-text').placeholder = 'Nachricht … (über Funk lieber kurz)';
    }).catch(function(e){ $('fc-ich-fehler').textContent = e.message; });
  }

  function listenLaden(){
    api('/gehoert').then(function(d){
      var l = d.gehoert || [];
      l.forEach(function(g){ if (g.name) namen[g.adresse] = g.name; });
      $('fc-gehoert').innerHTML = l.length ? l.slice(0, 40).map(function(g){
        return '<button type="button" class="fc-eintrag' + (g.adresse === aktiv ? ' aktiv' : '') + '" data-mit="' + esc(g.adresse) + '"><div class="n">' + esc(g.name || 'ohne Namen') + '</div><div class="u">' + vor(g.zeit) + (g.hops > 0 ? ' · ' + g.hops + ' Sprung' + (g.hops > 1 ? 'e' : '') : ' · direkt') + ' · ' + esc(g.adresse.slice(0, 12)) + '…</div></button>';
      }).join('') : '<div class="fc-hint">Noch niemand – Ankündigungen kommen etwa alle 30 Minuten.</div>';
    }).catch(function(){});
    api('/gespraeche').then(function(d){
      var l = d.gespraeche || [];
      l.forEach(function(g){ if (g.name) namen[g.mit] = g.name; });
      $('fc-gespraeche').innerHTML = l.length ? l.map(function(g){
        return '<button type="button" class="fc-eintrag' + (g.mit === aktiv ? ' aktiv' : '') + '" data-mit="' + esc(g.mit) + '"><div class="n">' + esc(g.name || namen[g.mit] || ('<' + g.mit.slice(0, 8) + '…>')) + '</div><div class="u">' + esc(g.letzte) + '</div></button>';
      }).join('') : '<div class="fc-hint">Noch keine.</div>';
      kopfZeigen();
    }).catch(function(){});
  }

  function kopfZeigen(){
    if (!aktiv) return;
    $('fc-chat-kopf').innerHTML = esc(namen[aktiv] || 'Gegenstelle') + '<small>&lt;' + esc(aktiv) + '&gt;</small>';
  }

  var letzterStand = '';
  function verlaufLaden(){
    if (!aktiv) return;
    api('/verlauf?mit=' + encodeURIComponent(aktiv)).then(function(d){
      var v = d.verlauf || [], stand = JSON.stringify(v);
      if (stand === letzterStand) return;
      letzterStand = stand;
      var box = $('fc-verlauf'), unten = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
      box.innerHTML = v.length ? v.map(function(e){
        return '<div class="fc-n ' + (e.richtung === 'aus' ? 'aus' : 'ein') + '">' + esc(e.text) + '<span class="z">' + uhr(e.zeit) + (e.richtung === 'aus' && STATUS[e.status] ? ' · ' + STATUS[e.status] : '') + '</span></div>';
      }).join('') : '<div class="fc-leer">Noch keine Nachrichten. Über Funk dauert die Zustellung einige Sekunden; ist die Gegenstelle gerade nicht zu hören, wird bis zu 24 Stunden lang zugestellt, sobald sie es ist.</div>';
      if (unten || v.length < 8) box.scrollTop = box.scrollHeight;
    }).catch(function(e){ $('fc-chat-fehler').textContent = e.message; });
  }

  function oeffnen(mit){
    mit = String(mit || '').toLowerCase().replace(/[<>:\s]/g, '');
    if (!/^[0-9a-f]{32}$/.test(mit)) { $('fc-chat-fehler').textContent = 'Eine Funkadresse hat 32 Zeichen (0–9, a–f).'; return; }
    aktiv = mit; letzterStand = ''; $('fc-chat-fehler').textContent = '';
    try { history.replaceState(null, '', '/funkchat?mit=' + mit); } catch (e) {}
    $('fc-text').disabled = $('fc-senden').disabled = !erreichbar;
    kopfZeigen(); verlaufLaden(); listenLaden();
    if (erreichbar) $('fc-text').focus();
  }

  document.addEventListener('click', function(e){
    var b = e.target.closest && e.target.closest('[data-mit]');
    if (b) oeffnen(b.dataset.mit);
  });
  $('fc-neu-los').onclick = function(){ oeffnen($('fc-neu-adr').value); };
  $('fc-neu-adr').onkeydown = function(e){ if (e.key === 'Enter') oeffnen(this.value); };

  $('fc-speichern').onclick = function(){
    var b = this; b.disabled = true; $('fc-ich-fehler').textContent = '';
    var ein = $('fc-erreichbar').checked;
    if (ein && !$('fc-name').value.trim()) { $('fc-ich-fehler').textContent = 'Bitte einen Anzeigenamen eintragen – den sehen andere in Reichweite.'; b.disabled = false; return; }
    api('/konto', { erreichbar: ein, name: $('fc-name').value.trim() }).then(function(k){
      if (k.hinweis) $('fc-ich-fehler').textContent = k.hinweis;
      return kontoLaden();
    }).catch(function(e){ $('fc-ich-fehler').textContent = e.message; }).then(function(){ b.disabled = false; });
  };
  $('fc-kopieren').onclick = function(){
    var t = $('fc-adresse').textContent.replace(/[<>]/g, '');
    (navigator.clipboard ? navigator.clipboard.writeText(t) : Promise.reject()).then(function(){ $('fc-kopieren').textContent = '✓ Kopiert'; setTimeout(function(){ $('fc-kopieren').textContent = 'Kopieren'; }, 1500); }).catch(function(){});
  };
  $('fc-ankuendigen').onclick = function(){
    var b = this; b.disabled = true;
    api('/ankuendigen', {}).then(function(){ b.textContent = '✓ Angekündigt'; }).catch(function(e){ $('fc-ich-fehler').textContent = e.message; })
      .then(function(){ setTimeout(function(){ b.textContent = 'Jetzt ankündigen'; b.disabled = false; }, 30000); });
  };

  var ta = $('fc-text');
  ta.oninput = function(){
    ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 140) + 'px';
    var n = new Blob([ta.value]).size;
    $('fc-zaehler').textContent = n ? n + ' Byte' + (n > 300 ? ' – lange Nachrichten belegen den Funkkanal länger' : '') : '';
  };
  ta.onkeydown = function(e){ if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); senden(); } };
  $('fc-senden').onclick = senden;
  function senden(){
    var text = ta.value.trim();
    if (!text || !aktiv) return;
    $('fc-senden').disabled = true; $('fc-chat-fehler').textContent = '';
    api('/senden', { an: aktiv, text: text }).then(function(){ ta.value = ''; ta.oninput(); verlaufLaden(); listenLaden(); })
      .catch(function(e){ $('fc-chat-fehler').textContent = e.message; })
      .then(function(){ $('fc-senden').disabled = false; ta.focus(); });
  }

  statusLaden().then(function(ok){
    if (!ok) return;
    kontoLaden().then(function(){ listenLaden(); if (aktiv) oeffnen(aktiv); });
    setInterval(function(){ if (document.hidden) return; verlaufLaden(); }, 4000);
    setInterval(function(){ if (document.hidden) return; listenLaden(); statusLaden(); }, 20000);
  });
})();
</script>
]==])
    render.footer()
end

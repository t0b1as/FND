-- KI-Chat (R870): Unterhaltung mit dem Modell aus Einstellungen → KI.
-- Die Antwort kommt Wort für Wort (/api/v1/ki/chat streamt). Der Verlauf
-- liegt nur im Browser (sessionStorage) – der Node speichert nichts.
local render = require "render"

return function()
    render.header("KI-Chat", "ki")
    ngx.print([==[
<style>
.kc { display:flex; flex-direction:column; height:calc(100dvh - 150px); max-width:860px; margin:0 auto; }
.kc-kopf { display:flex; align-items:center; gap:10px; padding:8px 2px 10px; flex-wrap:wrap; }
.kc-kopf h1 { font-size:20px; margin:0; }
.kc-modell { font-size:12.5px; color:var(--text-dim,#9aa0b4); }
.kc-kopf .btn-sm { margin-left:auto; }
.kc-verlauf { flex:1; overflow-y:auto; padding:6px 2px 12px; display:flex; flex-direction:column; gap:12px; }
.kc-n { max-width:86%; padding:10px 13px; border-radius:14px; line-height:1.55; font-size:14.5px; overflow-wrap:anywhere; }
.kc-n.user { align-self:flex-end; background:var(--green-bg,rgba(61,220,132,.13)); border:1px solid rgba(61,220,132,.28); }
.kc-n.assistant { align-self:flex-start; background:var(--surface-2,#1b1f28); border:1px solid var(--border,#2a2f3a); }
.kc-n img { max-width:220px; max-height:220px; border-radius:8px; display:block; margin-bottom:6px; }
.kc-n pre { background:rgba(0,0,0,.28); padding:9px 11px; border-radius:8px; overflow-x:auto; font-size:13px; margin:8px 0; }
.kc-n code { font-size:13px; background:rgba(127,127,127,.18); padding:1px 4px; border-radius:4px; }
.kc-n pre code { background:none; padding:0; }
.kc-n.fehler { border-color:var(--danger,#e5484d); }
.kc-tippt::after { content:"▍"; animation:kc-blink 1s steps(2) infinite; }
@keyframes kc-blink { 50% { opacity:0; } }
.kc-leer { margin:auto; text-align:center; color:var(--text-dim,#9aa0b4); font-size:14px; max-width:440px; }
.kc-eingabe { display:flex; gap:8px; align-items:flex-end; padding:10px 0 4px; border-top:1px solid var(--border,#2a2f3a); }
.kc-eingabe textarea { flex:1; resize:none; min-height:44px; max-height:180px; padding:11px 12px; border-radius:12px; font-size:16px; line-height:1.4; }
.kc-eingabe button { height:44px; flex:0 0 auto; }
.kc-anhang { display:flex; align-items:center; gap:8px; font-size:12.5px; color:var(--text-dim,#9aa0b4); padding:4px 0 0; }
.kc-anhang img { height:42px; border-radius:6px; }
@media (max-width:640px) { .kc { height:calc(100dvh - 190px); } .kc-n { max-width:94%; } }
</style>
<div class="kc">
  <div class="kc-kopf"><h1>KI-Chat</h1><span class="kc-modell" id="kc-modell">…</span>
    <button type="button" class="btn-sm" id="kc-neu">Neuer Chat</button></div>
  <div class="kc-verlauf" id="kc-verlauf"></div>
  <div class="kc-anhang" id="kc-anhang" style="display:none"></div>
  <div class="kc-eingabe">
    <button type="button" class="btn-sm" id="kc-bild" title="Foto anhängen" aria-label="Foto anhängen">📷</button>
    <input type="file" id="kc-datei" accept="image/*" style="display:none">
    <textarea id="kc-text" rows="1" placeholder="Nachricht an die KI …" aria-label="Nachricht"></textarea>
    <button type="button" class="btn btn-primary" id="kc-senden" style="width:auto">Senden</button>
  </div>
</div>
<script>
(function(){
  var $ = function(id){ return document.getElementById(id); };
  var SCHLUESSEL = 'fundus-ki-chat';
  var verlauf = [];          // {rolle, text, bild?}
  var bild = null;           // Base64 des angehängten Fotos
  var laufend = null;        // AbortController der laufenden Antwort
  try { verlauf = JSON.parse(sessionStorage.getItem(SCHLUESSEL) || '[]'); } catch (e) { verlauf = []; }
  function sichern(){ try { sessionStorage.setItem(SCHLUESSEL, JSON.stringify(verlauf)); } catch (e) { /* Bilder zu groß: Verlauf nur im Speicher */ } }

  function esc(t){ return String(t == null ? '' : t).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  // Schlichte Darstellung: Codeblöcke, Inline-Code, Fettschrift, Zeilenumbrüche.
  function format(t){
    var teile = String(t).split('```'), h = '';
    teile.forEach(function(teil, i){
      if (i % 2) { h += '<pre><code>' + esc(teil.replace(/^[a-zA-Z0-9_-]*\n/, '')) + '</code></pre>'; return; }
      h += esc(teil).replace(/`([^`\n]+)`/g, '<code>$1</code>').replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>').replace(/\n/g, '<br>');
    });
    return h;
  }
  function zeichnen(){
    var el = $('kc-verlauf');
    if (!verlauf.length) {
      el.innerHTML = '<div class="kc-leer">Frag die KI etwas – zum Beispiel, wie du ein Angebot beschreiben sollst, oder häng ein Foto an und frag, was darauf zu sehen ist.</div>';
      return;
    }
    el.innerHTML = verlauf.map(function(n){
      return '<div class="kc-n ' + (n.rolle === 'user' ? 'user' : 'assistant') + (n.fehler ? ' fehler' : '') + '">' +
        (n.bild ? '<img alt="angehängtes Foto" src="data:image/jpeg;base64,' + n.bild + '">' : '') + format(n.text) + '</div>';
    }).join('');
    el.scrollTop = el.scrollHeight;
  }

  fetch('/api/v1/ki/status', { credentials: 'same-origin' }).then(function(r){ return r.json(); }).then(function(d){
    $('kc-modell').textContent = d.aktiv ? 'Modell: ' + d.modell : 'KI ist aus – Einstellungen → KI';
    if (!d.fotos) $('kc-bild').style.display = 'none';
  }).catch(function(){ $('kc-modell').textContent = ''; });

  // Foto auf höchstens 1280 px verkleinern – schneller, und mehr verarbeiten Bildmodelle ohnehin nicht.
  $('kc-bild').onclick = function(){ $('kc-datei').click(); };
  $('kc-datei').onchange = function(){
    var f = this.files && this.files[0]; this.value = '';
    if (!f) return;
    var img = new Image(), url = URL.createObjectURL(f);
    img.onload = function(){
      var s = Math.min(1, 1280 / Math.max(img.width, img.height));
      var cv = document.createElement('canvas'); cv.width = Math.round(img.width * s); cv.height = Math.round(img.height * s);
      cv.getContext('2d').drawImage(img, 0, 0, cv.width, cv.height);
      bild = cv.toDataURL('image/jpeg', 0.85).split(',')[1];
      URL.revokeObjectURL(url);
      $('kc-anhang').innerHTML = '<img alt="" src="data:image/jpeg;base64,' + bild + '"> Foto angehängt · <a href="#" id="kc-weg">entfernen</a>';
      $('kc-anhang').style.display = '';
      $('kc-weg').onclick = function(e){ e.preventDefault(); bild = null; $('kc-anhang').style.display = 'none'; };
    };
    img.onerror = function(){ URL.revokeObjectURL(url); alert('Dieses Bild lässt sich nicht lesen.'); };
    img.src = url;
  };

  var ta = $('kc-text');
  ta.addEventListener('input', function(){ ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 180) + 'px'; });
  ta.addEventListener('keydown', function(e){
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); senden(); }
  });
  $('kc-senden').onclick = function(){ laufend ? laufend.abort() : senden(); };
  $('kc-neu').onclick = function(){
    if (laufend) laufend.abort();
    verlauf = []; sichern(); zeichnen(); ta.focus();
  };

  function senden(){
    var text = ta.value.trim();
    if ((!text && !bild) || laufend) return;
    verlauf.push(bild ? { rolle: 'user', text: text || 'Was ist auf diesem Foto?', bild: bild } : { rolle: 'user', text: text });
    var antwort = { rolle: 'assistant', text: '' };
    verlauf.push(antwort);
    ta.value = ''; ta.style.height = ''; bild = null; $('kc-anhang').style.display = 'none';
    zeichnen();
    var blasen = $('kc-verlauf').querySelectorAll('.kc-n'), blase = blasen[blasen.length - 1];
    blase.classList.add('kc-tippt');
    laufend = new AbortController();
    $('kc-senden').textContent = 'Stopp';
    var nachrichten = verlauf.slice(0, -1).filter(function(n){ return !n.fehler && n.text; })
      .map(function(n){ return n.bild ? { rolle: n.rolle, text: n.text, bild: n.bild } : { rolle: n.rolle, text: n.text }; });
    fetch('/api/v1/ki/chat', { method: 'POST', credentials: 'same-origin', signal: laufend.signal,
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ nachrichten: nachrichten }) })
      .then(function(r){
        if (!r.ok) return r.json().catch(function(){ return {}; }).then(function(d){ throw new Error(d.error || ('Fehler ' + r.status)); });
        var leser = r.body.getReader(), dek = new TextDecoder();
        function lesen(){
          return leser.read().then(function(x){
            if (x.done) return;
            antwort.text += dek.decode(x.value, { stream: true });
            blase.innerHTML = format(antwort.text);
            var v = $('kc-verlauf'); if (v.scrollHeight - v.scrollTop - v.clientHeight < 160) v.scrollTop = v.scrollHeight;
            return lesen();
          });
        }
        return lesen();
      })
      .catch(function(e){
        if (e.name === 'AbortError') { antwort.text += antwort.text ? '\n\n(abgebrochen)' : '(abgebrochen)'; return; }
        antwort.text = e.message; antwort.fehler = true;
      })
      .then(function(){
        laufend = null; $('kc-senden').textContent = 'Senden';
        if (!antwort.text) { antwort.text = '(keine Antwort)'; antwort.fehler = true; }
        sichern(); zeichnen(); ta.focus();
      });
  }
  zeichnen();
  ta.focus();
})();
</script>
]==])
    render.footer()
end

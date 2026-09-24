-- pages/messenger.lua
-- Ende-zu-Ende verschlüsselter Text/Audio/Video Messenger
-- Verschlüsselung: ECDH(Identity_A × Identity_B) → Argon2id → AES-256-GCM
-- Audio/Video: WebRTC (DTLS-SRTP) + verschlüsseltes Signaling
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("messenger.title", "messenger")

ngx.print([[
<div class="msg-layout">

<!-- ============================================================
     Seitenleiste: Identität + Kontakte
     ============================================================ -->
<aside class="msg-sidebar">

  <!-- Eigene Identität -->
  <div class="msg-identity" id="identity-panel">
    <div id="identity-status" class="msg-idbox">
      Noch nicht angemeldet
    </div>
    <!-- Eigener Login-Fallback (versteckt; zentraler Wallet-Login oben wird genutzt) -->
    <div id="login-form" style="display:none">
      <div class="field" style="margin-top:8px">
        ]] .. "<label>" .. t("messenger.label_email") .. "</label>" .. [[
        <input type="email" id="login-email" ]] .. 'placeholder="' .. t("messenger.placeholder_email") .. '"' .. [[>
      </div>
      <div class="field">
        ]] .. "<label>" .. t("messenger.label_password") .. "</label>" .. [[
        <input type="password" id="login-pass" ]] .. 'placeholder="' .. t("messenger.placeholder_password") .. '"' .. [[>
      </div>
      <button class="btn" id="login-btn" onclick="deriveIdentity()">
        ]] .. t("messenger.btn_derive") .. [[
      </button>
      <div id="login-status" class="meta" style="margin-top:4px"></div>
    </div>
  </div>

  <!-- Kontakte -->
  <div class="msg-contacts" style="margin-top:1rem">
    ]] .. "<h3>" .. t("messenger.contacts") .. "</h3>" .. [[
    <div class="field">
      <input type="text" id="new-contact-id"
             ]] .. 'placeholder="' .. t("messenger.placeholder_contact") .. '"' .. [[
             style="font-size:12px">
      <input type="text" id="new-contact-alias" ]] .. 'placeholder="' .. t("messenger.placeholder_alias") .. '"' .. [[
             style="font-size:12px;margin-top:4px">
      <button class="btn" style="margin-top:4px;padding:4px 10px;font-size:12px"
              onclick="addContact()"]] .. ">" .. t("messenger.btn_add_contact_full") .. "</button>" .. [[
    </div>
    <div id="contact-list" style="margin-top:8px"></div>
  </div>

</aside>

<!-- ============================================================
     Hauptbereich: Chat
     ============================================================ -->
<main class="msg-main">

  <!-- Chat-Header -->
  <div class="msg-header" id="chat-header">
    <div style="flex:1;min-width:0">
      <input type="text" id="chat-with-addr" class="msg-addr-input"
             ]] .. 'placeholder="' .. t("messenger.recipient_addr") .. '"' .. [[
             oninput="updateSendState()" onchange="setRecipientFromAddr()">
      <span class="meta" id="chat-with-name" style="font-size:11px;margin-left:4px"></span>
    </div>
    <div class="msg-call-buttons">
      <button class="btn" ]] .. 'title="' .. t("messenger.title_audio") .. '"' .. [[ onclick="startCall('audio')">🎙️</button>
      <button class="btn" ]] .. 'title="' .. t("messenger.title_video") .. '"' .. [[ onclick="startCall('video')">📹</button>
    </div>
  </div>

  <!-- Video-Bereich (WebRTC) -->
  <div id="video-container" style="display:none;position:relative;background:#111;
       border-radius:var(--radius);overflow:hidden;margin-bottom:8px">
    <video id="remote-video" autoplay playsinline
           style="width:100%;max-height:400px;object-fit:cover"></video>
    <video id="local-video"  autoplay playsinline muted
           style="position:absolute;bottom:8px;right:8px;width:120px;
                  border-radius:4px;border:2px solid #fff"></video>
    <div style="position:absolute;bottom:8px;left:8px;display:flex;gap:8px">
      <button class="btn btn-danger" onclick="hangup()" ]] .. 'title="' .. t("messenger.title_hangup") .. '"' .. [[>📵</button>
      <button class="btn" id="mute-btn" onclick="toggleMute()" ]] .. 'title="' .. t("messenger.title_mute") .. '"' .. [[>🎙️</button>
      <button class="btn" id="cam-btn"  onclick="toggleCamera()" ]] .. 'title="' .. t("messenger.title_camera") .. '"' .. [[>📷</button>
    </div>
  </div>

  <!-- Nachrichten -->
  <div id="messages" class="msg-messages"></div>

  <!-- Eingabe -->
  <div class="msg-input-row" id="input-row">
    <button class="btn emoji-btn" ]] .. 'title="' .. t("messenger.title_emoji") .. '"' .. [[ onclick="toggleEmojiPanel()">😊</button>
    <input type="text" id="msg-input" ]] .. 'placeholder="' .. t("messenger.placeholder_msg") .. '"' .. [[
           onkeydown="if(event.key==='Enter')sendText()">
    <button class="btn" id="send-btn" onclick="sendText()"]] .. ' title="' .. t("messenger.btn_send") .. '"><span class="send-ico">➤</span><span class="send-txt">' .. t("messenger.btn_send") .. "</span></button>" .. [[
    <label class="btn" ]] .. 'title="' .. t("messenger.title_send_file") .. '"' .. [[ style="cursor:pointer">
      📎 <input type="file" id="file-input" style="display:none" onchange="sendFile()">
    </label>
  </div>
  <div id="emoji-panel" class="emoji-panel" style="display:none"></div>

</main>
</div><!-- msg-layout -->

<!-- ============================================================
     Crypto + WebRTC (Client-seitig)
     ============================================================ -->
<script>
const MSGT = ]] .. (require("cjson.safe").encode({
    signed_in = t("messenger.status_online"),
    uploading = t("messenger.title_send_file"), email_pw_required = t("messenger.email_pw_required"),
    deriving = t("messenger.deriving"), upload_failed = t("messenger.upload_failed"), error_word = t("messenger.error_word"),
    emoji_search = t("messenger.emoji_search"), emoji_none = t("messenger.emoji_none"),
    emoji_refine = t("messenger.emoji_refine"),
    delivered_at = t("messenger.delivered_at"), read_at = t("messenger.read_at"),
    placeholder_msg = t("messenger.placeholder_msg"), recipient_first = t("messenger.recipient_first"),
}) or "{}") .. [[;
</script>
<script src="/static/messenger.js?v=]] .. require("render").rev() .. [["></script>

<style>
.msg-layout      { display:flex; gap:1rem; height:calc(100vh - 140px); align-items:stretch; }
.msg-sidebar     { width:260px; flex-shrink:0; display:flex; flex-direction:column; gap:8px; overflow-y:auto; }
.msg-main        { flex:1; display:flex; flex-direction:column; gap:8px; min-width:0; min-height:0; position:relative;
                   padding:0; margin:0; max-width:none; }
.msg-header      { display:flex; justify-content:space-between; align-items:center;
                   padding:0 12px; background:var(--surface); border-radius:12px;
                   border:1px solid var(--border); height:60px; flex-shrink:0; box-sizing:border-box; }
.msg-call-buttons { display:flex; gap:8px; }
.msg-messages    { flex:1; min-height:0; overflow-y:auto; padding:8px; background:var(--bg);
                   border:1px solid var(--border); border-radius:var(--radius);
                   display:flex; flex-direction:column; gap:3px; }
.msg-input-row   { display:flex; gap:8px; flex-shrink:0; }
.msg-input-row input { flex:1; }
/* Inline-Medien in Nachrichten */
.msg-media-img   { max-width:240px; max-height:240px; border-radius:8px; display:block; cursor:pointer; }
.msg-media-video { max-width:280px; max-height:280px; border-radius:8px; display:block; background:#000; }
.msg-media-link  { display:inline-block; }
/* Emoji-Picker */
.emoji-btn       { padding:6px 10px; }
.emoji-panel     { position:absolute; bottom:70px; left:12px; right:12px; z-index:50;
                   display:flex; flex-direction:column; gap:0; padding:0; background:var(--surface);
                   border:1px solid var(--border); border-radius:12px;
                   height:260px; overflow:hidden; box-shadow:0 -4px 20px rgba(0,0,0,0.35); }
/* Adress-Eingabefeld im Chat-Header */
.msg-addr-input  { width:100%; max-width:420px; padding:7px 10px; border-radius:8px;
                   border:1px solid var(--border); background:var(--bg,#0e0e1a);
                   color:var(--text); font-size:13px; font-family:monospace; }
.msg-addr-input:focus { outline:none; border-color:#00e676; }
.emoji-search    { width:100%; box-sizing:border-box; padding:8px 10px; border:none;
                   border-bottom:1px solid var(--border); background:var(--surface-2);
                   color:var(--text); font-size:14px; outline:none; }
.emoji-grid      { display:flex; flex-wrap:wrap; gap:2px; padding:8px; overflow-y:auto;
                   max-height:200px; align-content:flex-start; }
.emoji-empty     { padding:16px; color:var(--muted); font-size:13px; text-align:center; width:100%; }
.emoji-more      { width:100%; padding:10px; color:var(--muted); font-size:12px; text-align:center;
                   font-style:italic; }
.emoji-item      { background:none; border:none; font-size:20px; cursor:pointer;
                   padding:4px; border-radius:6px; line-height:1; }
.emoji-item:hover { background:var(--surface-2); }
/* Emoji-Grafiken (OpenMoji, CC BY-SA/CC BY) */
.emoji-pick      { width:26px; height:26px; display:block; }
.emoji-inline    { width:1.35em; height:1.35em; vertical-align:-0.28em; margin:0 1px; }

/* Nachrichten-Bubbles */
.msg-bubble      { max-width:72%; border-radius:12px; padding:5px 10px; line-height:1.3;
                   font-size:14px; box-shadow:0 1px 2px rgba(0,0,0,0.15); word-break:break-word; }
.incoming        { background:var(--surface); border:1px solid var(--border);
                   align-self:flex-start; border-bottom-left-radius:4px; }
.outgoing        { background:linear-gradient(135deg,#00e676,#00c853); color:#04120a;
                   align-self:flex-end; border-bottom-right-radius:4px; font-weight:500; }
.outgoing a      { color:#023; text-decoration:underline; }
.msg-content     { }
.msg-time        { font-size:10px; opacity:0.5; margin-left:8px; white-space:nowrap;
                   vertical-align:baseline; }
.msg-sender      { display:block; font-size:11px; font-weight:600; opacity:0.75; margin-bottom:1px; }
.msg-ticks       { margin-left:3px; cursor:default; }
.tick            { font-size:10px; letter-spacing:-1px; }
.tick-sent       { opacity:0.6; }
.tick-delivered  { opacity:0.6; }
.tick-read       { color:#0288d1; opacity:1; }
.msg-system      { text-align:center; color:var(--muted); font-size:12px; padding:6px; }

/* Kontaktliste */
.contact-item    { display:flex; align-items:center; gap:10px; padding:9px 10px;
                   border-radius:10px; cursor:pointer; transition:background 0.15s; }
.contact-item:hover { background:var(--surface); }
.contact-item.active { background:var(--surface); border-left:3px solid #00e676; padding-left:7px; }
.contact-item .c-avatar { width:34px; height:34px; border-radius:50%; flex-shrink:0;
                   background:linear-gradient(135deg,#00e676,#00838f); display:flex;
                   align-items:center; justify-content:center; font-weight:600; color:#04120a; font-size:14px; }
.contact-item .c-name { font-size:14px; font-weight:500; }
.contact-item .c-sub  { font-size:11px; color:var(--muted); }
.contact-unread { margin-left:auto; background:#00c853; color:#04120a; font-size:11px; font-weight:600; min-width:18px; height:18px; border-radius:9px; display:flex; align-items:center; justify-content:center; padding:0 5px; }
.msg-identity    { background:var(--surface); border:1px solid var(--border);
                   border-radius:12px; padding:0 12px; height:60px; flex-shrink:0;
                   box-sizing:border-box; display:flex; align-items:center; }
.msg-idbox       { font-size:12px; line-height:1.5; }
.msg-idbox .mono { font-family:monospace; color:var(--muted); word-break:break-all; }
/* Chat-Bereich abgerundeter, Eingabezeile abgesetzt */
.msg-messages    { border-radius:12px; }
.msg-input-row   { padding:6px; background:var(--surface); border:1px solid var(--border); border-radius:14px; }
/* Texteingabefeld: rund, mit sanftem Fokus-Glow im Fundus-Grün */
#msg-input {
  flex:1; padding:11px 16px; border-radius:22px;
  border:1px solid var(--border); background:var(--bg,#0e0e1a); color:var(--text);
  font-size:14px; transition:border-color .18s ease, box-shadow .18s ease;
}
#msg-input:focus {
  outline:none; border-color:var(--green-ctrl);
  box-shadow:0 0 0 3px rgba(23,168,98,0.18), 0 0 14px rgba(23,168,98,0.22);
}
#msg-input:disabled { opacity:.6; cursor:not-allowed; }
#send-btn:disabled, .emoji-btn:disabled {
  background:#3a3a44 !important; color:#8a8a95 !important;
  border-color:#3a3a44 !important; cursor:not-allowed; transform:none !important;
  box-shadow:none !important;
}
.msg-header      { border-radius:12px; }
#send-btn .send-ico { display:none; }

/* Schreibleiste: IMMER eine Zeile. !important, weil die globale Mobil-Regel
   (.btn → volle Breite) sonst durchschlägt – auch aus einer alten, gecachten
   style.css. Eingabefeld nimmt den Rest (flex-basis 0). */
.msg-input-row { display:flex !important; flex-wrap:nowrap !important; align-items:center;
                 min-width:0; max-width:100%; box-sizing:border-box; }
.msg-input-row > .btn, .msg-input-row > label.btn, .msg-input-row > button {
                 width:auto !important; flex:0 0 auto !important; min-width:0 !important; }
.msg-input-row > #msg-input { width:auto !important; flex:1 1 0 !important; min-width:0 !important; }
.msg-call-buttons > .btn { width:auto !important; flex:0 0 auto !important; }

/* Eingehender Anruf */
.incoming-call { display:none; position:fixed; left:50%; top:80px; transform:translateX(-50%);
                 z-index:10000; background:var(--surface); border:1px solid var(--border);
                 border-radius:16px; padding:16px 20px; box-shadow:0 8px 30px rgba(0,0,0,.5);
                 min-width:260px; text-align:center; }
.incoming-call .ic-title { font-weight:700; font-size:16px; }
.incoming-call .ic-from  { color:var(--muted); margin:6px 0 12px; word-break:break-all; }
.incoming-call .ic-btns  { display:flex; gap:10px; justify-content:center; }
.incoming-call .ic-btns .btn { flex:1; }

/* Audio-Anruf: keine Videoflächen, nur Steuerleiste */
#video-container.audio-only { min-height:64px; }
#video-container.audio-only video { display:none; }
#video-container.audio-only::before { content:"🎙️ Audioanruf läuft"; display:block; color:#fff;
                                      padding:14px 14px 0; font-weight:600; }

@media (max-width:720px){
  .msg-layout    { flex-direction:column; gap:8px;
                   height:calc(100vh - 120px); height:calc(100dvh - 120px); }
  .msg-sidebar   { width:100%; max-height:26vh; flex-shrink:0; }
  .msg-main      { min-height:0; gap:6px; }
  .msg-messages  { flex:1; min-height:0; }

  /* Kopfzeile: Adresse schrumpft, Anruf-Knöpfe bleiben sichtbar */
  .msg-header    { gap:6px; padding:6px 8px; }
  .msg-call-buttons { gap:4px; flex-shrink:0; }
  .msg-call-buttons .btn { width:auto; min-height:40px; padding:6px 10px; min-width:0; }

  /* Schreibleiste: alles in einer Zeile, Eingabefeld nimmt den Rest */
  .msg-input-row { gap:4px; padding:4px; align-items:center; flex-wrap:nowrap;
                   min-width:0; max-width:100%; box-sizing:border-box; }
  /* Globale Mobil-Regel setzt .btn auf volle Breite – hier bewusst kompakt. */
  .msg-input-row .btn { width:auto; min-height:42px; padding:8px 11px; min-width:0; flex:0 0 auto; }
  #msg-input     { min-width:0; width:auto; flex:1 1 auto; font-size:16px; height:42px;
                   box-sizing:border-box; } /* 16px: kein Auto-Zoom (iOS) */
  .msg-layout, .msg-main { max-width:100%; overflow-x:hidden; }
  #video-container .btn, .incoming-call .btn { width:auto; }
  #send-btn .send-txt { display:none; }
  #send-btn .send-ico { display:inline; }
  .emoji-panel   { left:6px; right:6px; bottom:60px; height:50vh; }

  /* Videoanruf mobil als Vollbild */
  #video-container { position:fixed !important; inset:0; z-index:9000; margin:0 !important;
                     border-radius:0 !important; }
  #remote-video    { width:100%; height:100%; max-height:none !important; object-fit:cover; }
  #local-video     { width:28vw !important; top:12px; right:12px; bottom:auto !important; }
  #video-container.audio-only { position:fixed !important; inset:auto 0 0 0; height:auto; }
}
</style>
]])

    render.footer()
end

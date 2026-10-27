-- Feed (R812): Beiträge mit Text, Bildern, GIFs und Videos – öffentlich oder
-- nur für Kontakte, mit Likes und Kommentaren. Logik in static/feed.js,
-- Server in internal/api/feed.go.
local render = require "render"
local icons = require "icons"

return function()
    ngx.header["Content-Type"] = "text/html"
    render.header("Feed", "feed")

    ngx.print([[
<style>
/* R822: Überschrift und Symbol im Violett der Feed-Kachel, mit Leuchten. */
.feed-titel { margin:0; font-size:20px; font-weight:800; color:#a763f7; display:flex; align-items:center; gap:8px;
  text-shadow:0 0 10px rgba(167,99,247,.75), 0 0 22px rgba(167,99,247,.35); }
.feed-titel-ico { display:inline-flex; width:24px; height:24px; color:#a763f7;
  filter:drop-shadow(0 0 5px rgba(167,99,247,.85)) drop-shadow(0 0 12px rgba(167,99,247,.4)); }
.feed-wrap { max-width:640px; margin:0 auto; display:flex; flex-direction:column; gap:14px; }
/* R815: Auf dem Desktop 50 % größer (640 → 960 px). */
@media (min-width: 1000px) { .feed-wrap { max-width:960px; } .feed-text { font-size:16.5px; } }
/* R813: Auch zwischen den Beiträgen derselbe Abstand wie zwischen Eingabe und Feed. */
#feed-liste { display:flex; flex-direction:column; gap:14px; }
/* R816: Beiträge und Eingabefenster zu 80 % durchsichtig – nur der Rahmen
   zeichnet die Karte. --sur ist #1a1d27; 0.2 Deckkraft = 80 % durchsichtig. */
.feed-card { background:rgba(26,29,39,.2); border:1px solid var(--brd); border-radius:var(--r); padding:14px 16px; }
.feed-composer textarea { width:100%; min-height:84px; resize:vertical; box-sizing:border-box;
  background:transparent; color:var(--text); border:1px solid var(--brd); border-radius:8px;
  padding:10px 12px; font:inherit; font-size:15px; line-height:1.45; }
.feed-composer textarea:focus { outline:none; border-color:var(--green); }
/* R816: Die allgemeine Regel für Eingabefelder setzt ihren Hintergrund mit
   Vorrang (!important) – hier ausdrücklich durchsichtig, damit das
   Eingabefenster wie die Beiträge nur seinen Rahmen zeigt. */
.feed-composer textarea, .feed-composer .feed-sicht { background-color:transparent !important; }
/* Die aufgeklappte Liste braucht einen deckenden Grund, sonst ist sie unlesbar. */
.feed-sicht option, .feed-sicht optgroup { background-color:var(--sur); color:var(--text); }
.feed-row { display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:10px; }
.feed-row .grow { flex:1; }
.feed-tool { background:transparent; border:1px solid var(--brd); color:var(--text-dim); border-radius:8px;
  padding:7px 11px; font-size:13px; cursor:pointer; display:inline-flex; align-items:center; gap:6px; }
.feed-tool:hover { border-color:var(--green); color:var(--text); }
.feed-tool .ico { width:17px; height:17px; }
/* R816: + Media genauso hoch wie Teilen und die Reichweitenauswahl. */
.feed-plus { height:46px; min-height:46px; box-sizing:border-box; padding:0 16px; font-size:14px; }
/* R817: + Media, Reichweite und Teilen in EINER Zeile – auch auf dem Handy.
   Die Auswahl nimmt den Platz dazwischen und kürzt bei Bedarf ihren Text. */
#feed-knopfzeile { flex-wrap:nowrap; gap:8px; }
#feed-knopfzeile .feed-plus, #feed-knopfzeile .feed-teilen { flex:0 0 auto; }
#feed-knopfzeile .feed-sicht { flex:1 1 auto; min-width:0; max-width:none; text-overflow:ellipsis; }
@media (max-width:480px) {
  #feed-knopfzeile .feed-plus { padding:0 11px; }
  #feed-knopfzeile .feed-teilen { padding:0 12px; min-width:0; }
  #feed-knopfzeile .feed-sicht { padding:0 8px; }
}
.feed-tonwahl { margin-left:auto; height:36px; padding:0 12px; font-size:13px; }
/* R813: Auswahl und Teilen-Knopf gleich hoch und gleich beschriftet. Die
   Seite gibt Eingabefeldern 46 px Mindesthöhe (fingerfreundlich) – der Knopf
   übernimmt dieses Maß, statt die Auswahl darunter zu drücken. */
.feed-sicht, .feed-teilen { height:46px; min-height:46px; box-sizing:border-box; border-radius:8px; font-size:14px; padding:0 14px; }
.feed-sicht { background:var(--sur2); color:var(--text); border:1px solid var(--brd); max-width:52vw; }
.feed-teilen { background:rgba(0,230,118,0.15); color:var(--green); border:1px solid rgba(0,230,118,0.4);
  font-weight:600; cursor:pointer; min-width:84px; }
.feed-teilen:hover { background:rgba(0,230,118,0.25); }
.feed-teilen:disabled { opacity:.55; cursor:default; }
.feed-beitrag.hervor { border-color:var(--green); box-shadow:0 0 0 2px rgba(0,230,118,.25); }
.feed-toast { position:fixed; left:50%; bottom:calc(24px + env(safe-area-inset-bottom,0px)); transform:translateX(-50%);
  background:var(--sur2); color:var(--text); border:1px solid var(--brd); border-radius:10px; padding:9px 16px;
  font-size:13.5px; z-index:20001; box-shadow:0 6px 24px rgba(0,0,0,.4); }
.feed-dlg { position:fixed; inset:0; z-index:15000; background:rgba(0,0,0,.6); display:flex; align-items:center; justify-content:center; padding:16px; }
.feed-dlg-karte { background:var(--sur); border:1px solid var(--brd); border-radius:12px; width:min(440px,100%); max-height:86vh;
  display:flex; flex-direction:column; }
.feed-dlg-kopf { padding:14px 16px; border-bottom:1px solid var(--brd); font-weight:600; color:var(--text); display:flex; align-items:center; }
.feed-dlg-kopf button { margin-left:auto; background:none; border:none; color:var(--text-dim); font-size:18px; cursor:pointer; }
.feed-dlg-inhalt { padding:14px 16px; overflow-y:auto; display:flex; flex-direction:column; gap:10px; }
.feed-dlg-fuss { padding:12px 16px; border-top:1px solid var(--brd); display:flex; gap:8px; justify-content:flex-end; }
.feed-dlg input[type=text] { background:var(--sur2); color:var(--text); border:1px solid var(--brd); border-radius:8px; padding:9px 11px; font-size:14px; }
.feed-mitglied { display:flex; align-items:center; gap:10px; padding:6px 4px; border-radius:8px; cursor:pointer; color:var(--text); font-size:14px; }
.feed-mitglied:hover { background:var(--sur2); }
.feed-mitglied input { width:18px; height:18px; accent-color:var(--green); }
.feed-gruppe-zeile { display:flex; align-items:center; gap:8px; padding:8px 4px; border-bottom:1px solid var(--brd); color:var(--text); }
.feed-gruppe-zeile .meta { color:var(--text-dim); font-size:12.5px; }
.feed-gruppe-zeile button { background:none; border:1px solid var(--brd); color:var(--text-dim); border-radius:7px; padding:4px 9px; cursor:pointer; font-size:12.5px; }
.feed-video-eingabe { width:100%; margin-top:8px; box-sizing:border-box; background:var(--sur2); color:var(--text);
  border:1px solid var(--brd); border-radius:8px; padding:8px 10px; font-size:13.5px; }
.feed-vorschau { display:grid; grid-template-columns:repeat(auto-fill,minmax(84px,1fr)); gap:6px; margin-top:10px; }
.feed-vorschau div { position:relative; aspect-ratio:1; border-radius:8px; overflow:hidden; background:var(--sur2); }
.feed-vorschau img, .feed-vorschau video { width:100%; height:100%; object-fit:cover; }
.feed-vorschau button { position:absolute; top:3px; right:3px; width:22px; height:22px; border-radius:50%;
  border:none; background:rgba(0,0,0,.65); color:#fff; cursor:pointer; font-size:13px; line-height:1; }
.feed-status { font-size:12.5px; color:var(--text-dim); margin-top:8px; min-height:1em; }

.feed-kopf { display:flex; align-items:center; gap:10px; }
.feed-av { width:38px; height:38px; border-radius:50%; object-fit:cover; flex-shrink:0; background:var(--sur2);
  display:flex; align-items:center; justify-content:center; font-weight:700; color:var(--green); }
.feed-name { font-weight:600; color:var(--text); font-size:14.5px; }
.feed-meta { font-size:12px; color:var(--text-dim); display:flex; align-items:center; gap:5px; }
.feed-meta .ico { width:13px; height:13px; }
.feed-menu { margin-left:auto; background:none; border:none; color:var(--text-dim); cursor:pointer; font-size:18px; padding:2px 6px; }
.feed-text { margin-top:10px; white-space:pre-wrap; word-wrap:break-word; color:var(--text); font-size:15px; line-height:1.5; }
.feed-text a { color:var(--green); }
.feed-bilder { display:grid; gap:4px; margin-top:10px; border-radius:10px; overflow:hidden; }
.feed-bilder.n1 { grid-template-columns:1fr; }
.feed-bilder.n2, .feed-bilder.n4 { grid-template-columns:1fr 1fr; }
.feed-bilder.n3, .feed-bilder.n5, .feed-bilder.n6 { grid-template-columns:1fr 1fr 1fr; }
.feed-bilder img { width:100%; height:100%; max-height:520px; object-fit:cover; display:block; cursor:zoom-in; background:var(--sur2); }
.feed-bilder:not(.n1) img { aspect-ratio:1; }
.feed-video { margin-top:10px; position:relative; width:100%; aspect-ratio:16/9; border-radius:10px; overflow:hidden; background:#000; }
.feed-video iframe, .feed-video video { position:absolute; inset:0; width:100%; height:100%; border:0; }
.feed-video video.datei { position:static; height:auto; max-height:520px; }
.feed-video.datei { aspect-ratio:auto; }
.feed-videos { display:flex; flex-direction:column; gap:6px; margin-top:10px; }
.feed-videos video { width:100%; max-height:560px; border-radius:10px; background:#000; display:block; }
/* R854: Vollbild mit Wischen */
.feed-vb-huelle { position:relative; }
.feed-vb-auf { position:absolute; top:8px; right:8px; z-index:2; width:36px; height:36px; border-radius:9px; border:none; background:rgba(0,0,0,.55); color:#fff; font-size:18px; cursor:pointer; }
.feed-vb { position:fixed; inset:0; z-index:2000; background:#000; touch-action:none; overscroll-behavior:contain; }
.feed-vb-buehne { position:absolute; inset:0; overflow:hidden; }
.feed-vb-buehne video { position:absolute; inset:0; width:100%; height:100%; object-fit:contain; transition:transform .3s ease; }
.feed-vb-ende { animation:feed-vb-huepf .35s; }
@keyframes feed-vb-huepf { 35% { transform:translateY(-3%); } }
.feed-vb-info { position:absolute; left:14px; right:72px; bottom:calc(18px + env(safe-area-inset-bottom, 0px)); color:#fff; font-size:14px; line-height:1.4; text-shadow:0 1px 3px #000; pointer-events:none; }
.feed-vb-zu, .feed-vb-ton { position:absolute; right:12px; width:44px; height:44px; border-radius:50%; border:none; background:rgba(0,0,0,.5); color:#fff; font-size:19px; cursor:pointer; }
.feed-vb-zu { top:calc(12px + env(safe-area-inset-top, 0px)); }
.feed-vb-ton { top:calc(64px + env(safe-area-inset-top, 0px)); }
.feed-vb-tipp { position:absolute; left:50%; bottom:28%; transform:translateX(-50%); color:#fff; background:rgba(0,0,0,.55); padding:6px 14px; border-radius:14px; font-size:13px; white-space:nowrap; transition:opacity .6s; pointer-events:none; }
.feed-vb-tipp.weg { opacity:0; }
.feed-ton-hinweis { position:fixed; left:50%; transform:translateX(-50%); bottom:calc(84px + env(safe-area-inset-bottom, 0px)); z-index:500; background:rgba(0,0,0,.82); color:#fff; padding:7px 14px; border-radius:16px; font-size:13px; pointer-events:none; }
.feed-bearb { font-size:11.5px; color:var(--text-dim); font-style:italic; }
.feed-kbearb textarea { width:100%; box-sizing:border-box; background:var(--sur); color:var(--text); border:1px solid var(--green);
  border-radius:8px; padding:6px 8px; font:inherit; font-size:14px; min-height:54px; resize:vertical; }
.feed-kbearb .zeile { display:flex; gap:6px; justify-content:flex-end; margin-top:5px; }
.feed-kbearb button { background:none; border:1px solid var(--brd); color:var(--text-dim); border-radius:7px; padding:4px 10px; cursor:pointer; font-size:12.5px; }
.feed-kbearb button.ok { border-color:var(--green); color:var(--green); }
.feed-dlg textarea { width:100%; box-sizing:border-box; min-height:110px; background:var(--sur2); color:var(--text); border:1px solid var(--brd);
  border-radius:8px; padding:10px 12px; font:inherit; font-size:15px; resize:vertical; }
.feed-start { position:absolute; inset:0; display:flex; flex-direction:column; align-items:center; justify-content:center;
  gap:10px; cursor:pointer; color:#fff; background:linear-gradient(135deg,#1c1f2b,#0e1016); border:0; width:100%; }
.feed-start .knopf { width:64px; height:64px; border-radius:50%; background:rgba(255,255,255,.14); display:flex;
  align-items:center; justify-content:center; font-size:26px; padding-left:5px; }
.feed-start:hover .knopf { background:var(--green); color:#04120a; }
.feed-start .hinweis { font-size:12px; opacity:.75; }
.feed-aktionen { display:flex; gap:6px; margin-top:12px; border-top:1px solid var(--brd); padding-top:8px; }
.feed-akt { background:none; border:none; color:var(--text-dim); cursor:pointer; display:inline-flex; align-items:center;
  gap:6px; padding:6px 10px; border-radius:8px; font-size:13.5px; }
.feed-akt:hover { background:var(--sur2); color:var(--text); }
.feed-akt.an { color:#ff6b9d; }
.feed-akt svg { width:18px; height:18px; }
.feed-komm { margin-top:8px; display:flex; flex-direction:column; gap:8px; }
.feed-k { display:flex; gap:8px; }
.feed-k .feed-av { width:28px; height:28px; font-size:12px; }
.feed-kblase { background:var(--sur2); border-radius:12px; padding:7px 11px; flex:1; min-width:0; }
.feed-kblase .feed-name { font-size:13px; }
.feed-kblase .feed-text { margin-top:2px; font-size:14px; }
.feed-kblase .feed-meta { margin-top:3px; display:block; line-height:1.6; } /* R820: fließt wie Text statt in Spalten zu zerfallen */
.feed-keingabe { display:flex; gap:6px; }
.feed-keingabe input { flex:1; background:var(--sur2); color:var(--text); border:1px solid var(--brd);
  border-radius:18px; padding:8px 14px; font-size:14px; }
.feed-leer { text-align:center; color:var(--text-dim); padding:30px 10px; }
.feed-leer .ico { width:44px; height:44px; color:var(--green); opacity:.7; }
.feed-mehr { align-self:center; }
/* R814: Linkvorschau */
.feed-linkkarte { display:block; margin-top:10px; border:1px solid var(--brd); border-radius:10px; overflow:hidden;
  text-decoration:none; color:inherit; background:var(--sur2); position:relative; }
.feed-linkkarte:hover { border-color:var(--green); }
.feed-linkkarte img { width:100%; max-height:300px; object-fit:cover; display:block; background:#000; }
.feed-linkkarte .lk-text { padding:10px 12px; }
.feed-linkkarte .lk-seite { font-size:11.5px; color:var(--text-dim); text-transform:uppercase; letter-spacing:.04em; }
.feed-linkkarte .lk-titel { font-weight:600; color:var(--text); margin-top:2px; line-height:1.35; }
.feed-linkkarte .lk-besch { font-size:13px; color:var(--text-dim); margin-top:3px; line-height:1.4;
  display:-webkit-box; -webkit-line-clamp:2; -webkit-box-orient:vertical; overflow:hidden; }
.feed-start.mit-bild { background-size:cover; background-position:center; }
.feed-start.mit-bild::before { content:""; position:absolute; inset:0; background:rgba(0,0,0,.35); }
.feed-start.mit-bild > * { position:relative; }
.feed-start .titel { font-size:14px; font-weight:600; padding:0 16px; text-align:center; text-shadow:0 1px 4px rgba(0,0,0,.8); }
.feed-vorschau-zu { position:absolute; top:6px; right:6px; z-index:2; width:26px; height:26px; border-radius:50%; border:none;
  background:rgba(0,0,0,.65); color:#fff; cursor:pointer; font-size:14px; }
.feed-lade { margin-top:10px; font-size:12.5px; color:var(--text-dim); }
/* R820: Hashtags und Antworten */
.feed-tag { color:var(--green); text-decoration:none; font-weight:500; }
.feed-tag:hover { text-decoration:underline; }
.feed-filter { display:flex; align-items:center; gap:10px; padding:10px 16px; }
.feed-filter .tagname { font-weight:600; color:var(--green); }
.feed-filter button { margin-left:auto; background:none; border:1px solid var(--brd); color:var(--text-dim);
  border-radius:8px; padding:5px 11px; cursor:pointer; font-size:13px; }
.feed-antworten { margin-left:36px; display:flex; flex-direction:column; gap:8px; }
.feed-k.antwort .feed-av { width:24px; height:24px; font-size:11px; }
.feed-an { font-size:12px; color:var(--text-dim); margin-left:6px; font-weight:400; }
.feed-antwortfeld { display:flex; gap:6px; margin-left:36px; }
.feed-antwortfeld input { flex:1; background:transparent; color:var(--text); border:1px solid var(--green);
  border-radius:18px; padding:7px 13px; font-size:14px; }
.feed-antwortfeld button { background:none; border:1px solid var(--brd); color:var(--text-dim); border-radius:16px; padding:5px 11px; cursor:pointer; font-size:12.5px; }
.feed-antwortfeld button.ok { border-color:var(--green); color:var(--green); }
.feed-lightbox { position:fixed; inset:0; z-index:20000; background:rgba(0,0,0,.92); display:flex;
  align-items:center; justify-content:center; cursor:zoom-out; }
.feed-lightbox img { max-width:96vw; max-height:94vh; object-fit:contain; }
</style>

<div class="top-bar" style="margin-bottom:14px"><h2 class="feed-titel">
  <span class="feed-titel-ico">]] .. icons.svg("feed") .. [[</span>Feed</h2>
  <button type="button" class="feed-tool feed-tonwahl" id="feed-ton" title="Videos starten beim Scrollen von selbst – stumm oder mit Ton">🔇 Stumm</button></div>

<div class="feed-wrap">
  <div class="feed-card feed-composer">
    <textarea id="feed-text" placeholder="Was gibt's Neues?" maxlength="5000"></textarea>
    <div id="feed-link-vorschau" style="display:none"></div>
    <div id="feed-vorschau" class="feed-vorschau" style="display:none"></div>
    <div class="feed-row" id="feed-knopfzeile">
      <button type="button" class="feed-tool feed-plus" onclick="document.getElementById('feed-media-datei').click()" title="Bilder, GIFs und Videos anhängen – auch mehrere auf einmal. Video-Links einfach in den Text einfügen.">+ Media</button>
      <input type="file" id="feed-media-datei" accept="image/*,video/mp4,video/webm,video/quicktime" multiple style="display:none">
      <select id="feed-sicht" class="feed-sicht" title="Wer sieht den Beitrag?" aria-label="Reichweite">
        <option value="public">🌐 Öffentlich</option>
        <option value="contacts">👥 Nur Kontakte</option>
      </select>
      <button type="button" class="feed-teilen" id="feed-senden">Teilen</button>
    </div>
    <div class="feed-status" id="feed-status"></div>
  </div>

  <div id="feed-filter" class="feed-card feed-filter" style="display:none"></div>
  <div id="feed-liste"></div>
  <!-- R821: Endmarke fürs Nachladen beim Scrollen; der Knopf bleibt als
       Rückfall für Browser ohne IntersectionObserver. -->
  <div id="feed-ende" class="feed-meta" style="justify-content:center;min-height:24px;padding:6px 0" aria-live="polite"></div>
  <button type="button" class="btn btn-outline feed-mehr" id="feed-mehr" style="display:none;width:auto">Ältere Beiträge laden</button>
</div>
<script>window.FEED_ICON = ]] .. require("cjson.safe").encode(icons.svg("feed")) .. [[;</script>
]])
    ngx.print([[<script src="/static/feed.js?v=]] .. render.rev() .. [["></script>]])
    render.footer()
end

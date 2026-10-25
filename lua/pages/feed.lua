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
.feed-wrap { max-width:640px; margin:0 auto; display:flex; flex-direction:column; gap:14px; }
.feed-card { background:var(--sur); border:1px solid var(--brd); border-radius:var(--r); padding:14px 16px; }
.feed-composer textarea { width:100%; min-height:84px; resize:vertical; box-sizing:border-box;
  background:var(--sur2); color:var(--text); border:1px solid var(--brd); border-radius:8px;
  padding:10px 12px; font:inherit; font-size:15px; line-height:1.45; }
.feed-composer textarea:focus { outline:none; border-color:var(--green); }
.feed-row { display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:10px; }
.feed-row .grow { flex:1; }
.feed-tool { background:transparent; border:1px solid var(--brd); color:var(--text-dim); border-radius:8px;
  padding:7px 11px; font-size:13px; cursor:pointer; display:inline-flex; align-items:center; gap:6px; }
.feed-tool:hover { border-color:var(--green); color:var(--text); }
.feed-tool .ico { width:17px; height:17px; }
.feed-sicht { background:var(--sur2); color:var(--text); border:1px solid var(--brd); border-radius:8px;
  padding:7px 9px; font-size:13px; }
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
.feed-kblase .feed-meta { margin-top:3px; }
.feed-keingabe { display:flex; gap:6px; }
.feed-keingabe input { flex:1; background:var(--sur2); color:var(--text); border:1px solid var(--brd);
  border-radius:18px; padding:8px 14px; font-size:14px; }
.feed-leer { text-align:center; color:var(--text-dim); padding:30px 10px; }
.feed-leer .ico { width:44px; height:44px; color:var(--green); opacity:.7; }
.feed-mehr { align-self:center; }
.feed-lightbox { position:fixed; inset:0; z-index:20000; background:rgba(0,0,0,.92); display:flex;
  align-items:center; justify-content:center; cursor:zoom-out; }
.feed-lightbox img { max-width:96vw; max-height:94vh; object-fit:contain; }
</style>

<div class="top-bar"><h2 style="margin:0;font-size:20px;color:var(--text);display:flex;align-items:center;gap:8px">
  <span style="display:inline-flex;width:24px;height:24px;color:#a763f7">]] .. icons.svg("feed") .. [[</span>Feed</h2></div>

<div class="feed-wrap">
  <div class="feed-card feed-composer">
    <textarea id="feed-text" placeholder="Was gibt es Neues? Links zu YouTube oder Vimeo werden als Video eingebettet." maxlength="5000"></textarea>
    <div id="feed-vorschau" class="feed-vorschau" style="display:none"></div>
    <div id="feed-video-box" style="display:none">
      <input id="feed-video-url" class="feed-video-eingabe" type="url" placeholder="Video-Link (YouTube, Vimeo oder .mp4)" inputmode="url">
      <button type="button" class="feed-tool" style="margin-top:6px" onclick="document.getElementById('feed-video-datei').click()">📁 Stattdessen Videodatei hochladen (bis 24 MB)</button>
    </div>
    <div class="feed-row">
      <button type="button" class="feed-tool" onclick="document.getElementById('feed-bilder-datei').click()" title="Bilder oder GIFs anhängen">🖼 Bild/GIF</button>
      <button type="button" class="feed-tool" onclick="feedVideoUmschalten()" title="Video-Link oder Videodatei">🎬 Video</button>
      <input type="file" id="feed-bilder-datei" accept="image/*" multiple style="display:none">
      <input type="file" id="feed-video-datei" accept="video/mp4,video/webm,video/quicktime" style="display:none">
      <span class="grow"></span>
      <select id="feed-sicht" class="feed-sicht" title="Wer sieht den Beitrag?">
        <option value="public">🌐 Öffentlich</option>
        <option value="contacts">👥 Nur Kontakte</option>
      </select>
      <button type="button" class="btn btn-buy" id="feed-senden" style="width:auto;padding:8px 18px">Teilen</button>
    </div>
    <div class="feed-status" id="feed-status"></div>
  </div>

  <div id="feed-liste"></div>
  <button type="button" class="btn btn-outline feed-mehr" id="feed-mehr" style="display:none;width:auto">Ältere Beiträge laden</button>
</div>
<script>window.FEED_ICON = ]] .. require("cjson.safe").encode(icons.svg("feed")) .. [[;</script>
]])
    ngx.print([[<script src="/static/feed.js?v=]] .. render.rev() .. [["></script>]])
    render.footer()
end

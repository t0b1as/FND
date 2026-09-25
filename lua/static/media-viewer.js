// FundusMedia – Bilder, Videos und Audio im Dateimanager groß anzeigen.
//
// Gleiche Anzeige wie Marktplatz/Partnerbörse (dunkles Overlay, Klick daneben
// oder Esc schließt), ergänzt um Kopfzeile mit Download und Blättern (← →).
//
// Streaming: Unverschlüsselte Dateien spielt das <video>-Element direkt von
// /api/v1/files/download/<hash> ab. Der Server beantwortet Range-Anfragen
// (HTTP 206) und lädt dabei nur die benötigten Chunks – auch sehr große Videos
// starten sofort, Springen lädt gezielt den passenden Bereich.
//
// Verschlüsselte Dateien (.fnde) sind nur im Browser lesbar: Sie werden
// vollständig geladen und entschlüsselt (kein Streaming möglich).
(function () {
  "use strict";

  var IMG = ["jpg", "jpeg", "png", "gif", "webp", "bmp", "svg", "avif", "heic", "ico"];
  var VID = ["mp4", "m4v", "webm", "mov", "mkv", "ogv", "3gp"];
  var AUD = ["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus"];
  var ENC_VIDEO_WARN = 300 * 1024 * 1024; // ab 300 MB vor dem Entschlüsseln fragen

  function ext(name) {
    var n = String(name || "").toLowerCase();
    if (n.slice(-5) === ".fnde") n = n.slice(0, -5);
    var i = n.lastIndexOf(".");
    return i >= 0 ? n.slice(i + 1) : "";
  }

  // kind: "image" | "video" | "audio" | null
  function kind(name, mime) {
    var e = ext(name), m = String(mime || "").toLowerCase();
    if (IMG.indexOf(e) >= 0 || m.indexOf("image/") === 0) return "image";
    if (VID.indexOf(e) >= 0 || m.indexOf("video/") === 0) return "video";
    if (AUD.indexOf(e) >= 0 || m.indexOf("audio/") === 0) return "audio";
    return null;
  }

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function fmtSize(b) {
    if (!b) return "";
    var u = ["B", "KB", "MB", "GB", "TB"], i = 0;
    while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
    return (i ? b.toFixed(1) : b) + " " + u[i];
  }

  var state = null; // { ov, list, idx, blobUrl, onKey }
  var seq = 0;       // erhöht bei jedem Anzeigen – verwirft veraltete async-Ergebnisse

  // Container, bei denen vorab geprüft wird, ob der Browser sie direkt kann
  // oder ob umverpackt werden muss (Codecs laut ffprobe).
  var PROBE_FIRST = ["mkv", "mov", "ts", "m2ts", "mts", "flv", "avi", "wmv", "mpg", "mpeg", "vob", "3gp", "ogv"];

  function fmtTime(sec) {
    sec = Math.max(0, Math.floor(sec || 0));
    var h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = sec % 60;
    return (h ? h + ":" + (m < 10 ? "0" : "") : "") + m + ":" + (s < 10 ? "0" : "") + s;
  }

  // Ton-Versatz pro Datei (ms, + = Ton später) im Browser merken.
  function getAudioOffset(hash) {
    try { return parseInt(localStorage.getItem("fundus-aoff-" + hash) || "0", 10) || 0; } catch (e) { return 0; }
  }
  function setAudioOffset(hash, ms) {
    try { if (ms) localStorage.setItem("fundus-aoff-" + hash, String(ms)); else localStorage.removeItem("fundus-aoff-" + hash); } catch (e) {}
  }

  async function probe(it) {
    try {
      var r = await fetch("/api/v1/files/probe/" + encodeURIComponent(it.hash) + (it._pq ? "?" + it._pq : ""), { credentials: "same-origin" });
      return r.ok ? await r.json() : null;
    } catch (e) { return null; }
  }

  function cleanupMedia() {
    if (!state) return;
    var v = state.ov.querySelector("video,audio");
    if (v) { try { v.pause(); v.removeAttribute("src"); v.load(); } catch (e) {} }
    if (state.blobUrl) { try { URL.revokeObjectURL(state.blobUrl); } catch (e) {} state.blobUrl = null; }
  }

  function close() {
    if (!state) return;
    cleanupMedia();
    document.removeEventListener("keydown", state.onKey);
    state.ov.remove();
    state = null;
  }

  function buildOverlay() {
    var ov = document.createElement("div");
    ov.className = "fm-viewer";
    ov.innerHTML =
      '<div class="fm-viewer-bar">' +
        '<span class="fm-viewer-name"></span>' +
        '<span class="fm-viewer-meta"></span>' +
        '<a class="fm-viewer-btn fm-viewer-dl" title="Herunterladen">↓</a>' +
        '<button class="fm-viewer-btn fm-viewer-close" title="Schließen (Esc)">×</button>' +
      "</div>" +
      '<button class="fm-viewer-nav fm-viewer-prev" title="Vorherige (←)">‹</button>' +
      '<div class="fm-viewer-stage"></div>' +
      '<button class="fm-viewer-nav fm-viewer-next" title="Nächste (→)">›</button>';
    // Klick auf den dunklen Hintergrund schließt; Inhalt/Steuerung nicht.
    ov.addEventListener("click", function (e) {
      if (e.target === ov || e.target.classList.contains("fm-viewer-stage")) close();
    });
    ov.querySelector(".fm-viewer-close").onclick = function (e) { e.stopPropagation(); close(); };
    ov.querySelector(".fm-viewer-prev").onclick = function (e) { e.stopPropagation(); step(-1); };
    ov.querySelector(".fm-viewer-next").onclick = function (e) { e.stopPropagation(); step(1); };
    document.body.appendChild(ov);
    return ov;
  }

  function step(d) {
    if (!state || state.list.length < 2) return;
    state.idx = (state.idx + d + state.list.length) % state.list.length;
    show();
  }

  function message(stage, html) {
    stage.innerHTML = '<div class="fm-viewer-msg">' + html + "</div>";
  }

  async function show() {
    cleanupMedia();
    var token = ++seq;
    var it = state.list[state.idx];
    var ov = state.ov, stage = ov.querySelector(".fm-viewer-stage");
    var enc = String(it.name || "").slice(-5).toLowerCase() === ".fnde";
    var disp = enc ? it.name.slice(0, -5) : it.name;
    var k = kind(it.name, it.mime);
    // Besitzer (Netzwerksuche) mitschicken: der Server fragt ihn zuerst nach
    // Manifest und Chunks – sonst entscheidet der Zufall, ob er gefunden wird.
    var pq = it.peer ? "peer=" + encodeURIComponent(it.peer) : "";
    it._pq = pq;
    var src = "/api/v1/files/download/" + encodeURIComponent(it.hash) + (pq ? "?" + pq : "");

    ov.querySelector(".fm-viewer-name").textContent = (enc ? "🔒 " : "") + (disp || it.hash.slice(0, 16));
    ov.querySelector(".fm-viewer-meta").textContent =
      (fmtSize(it.size) ? fmtSize(it.size) : "") + (state.list.length > 1 ? "  ·  " + (state.idx + 1) + "/" + state.list.length : "");
    var multi = state.list.length > 1;
    ov.querySelector(".fm-viewer-prev").style.display = multi ? "" : "none";
    ov.querySelector(".fm-viewer-next").style.display = multi ? "" : "none";

    var dl = ov.querySelector(".fm-viewer-dl");
    if (enc && typeof window.downloadFile === "function") {
      dl.removeAttribute("href");
      dl.onclick = function (e) { e.preventDefault(); e.stopPropagation(); window.downloadFile(it.hash, it.name, it.peer); };
    } else {
      dl.href = src + (pq ? "&" : "?") + "dl=1";
      dl.onclick = function (e) { e.stopPropagation(); };
    }

    if (enc) {
      src = await decryptToUrl(it, k, stage);
      if (!src) return;
    }

    if (k === "image") {
      message(stage, "Lädt …");
      var im = new Image();
      im.className = "fm-viewer-media";
      im.alt = disp || "";
      im.onload = function () { stage.innerHTML = ""; stage.appendChild(im); };
      im.onerror = function () {
        message(stage, "Dieses Bild kann der Browser nicht anzeigen" +
          (ext(it.name) === "heic" ? " (HEIC wird nur von Safari unterstützt)" : "") +
          ".<br><br>Über ↓ oben rechts herunterladen.");
      };
      im.src = src;
    } else if (enc) {
      playDirect(stage, it, k, src, null);
    } else {
      var e = ext(it.name);
      if (PROBE_FIRST.indexOf(e) >= 0) {
        message(stage, "Prüfe Format …");
        var p = await probe(it);
        if (token !== seq) return;
        if (p && p.available) {
          if (p.direct) playDirect(stage, it, k, src, p);
          else if (p.remux) playRemux(stage, it, k, p);
          else unplayable(stage, k, p.reason);
          return;
        }
        // Ohne ffmpeg: direkt versuchen (Chrome/Edge spielen viele MKV).
      }
      playDirect(stage, it, k, src, null);
    }
  }

  function unplayable(stage, k, reason) {
    message(stage, "Diese " + (k === "audio" ? "Audiodatei" : "Videodatei") + " kann im Browser nicht abgespielt werden" +
      (reason ? ":<br>" + esc(reason) : ".") + "<br><br>Über ↓ oben rechts herunterladen und lokal abspielen (z.B. VLC).");
  }

  // Direkt abspielen (Range-Streaming). Scheitert es, wird – falls möglich –
  // auf Umverpacken gewechselt.
  function playDirect(stage, it, k, src, knownProbe) {
    var token = seq;
    var v = document.createElement(k === "audio" ? "audio" : "video");
    v.className = "fm-viewer-media";
    v.controls = true;
    v.autoplay = true;
    v.preload = "metadata"; // nur Metadaten vorab – der Rest wird gestreamt
    v.setAttribute("playsinline", "");
    v.addEventListener("click", function (e) { e.stopPropagation(); });
    v.onerror = async function () {
      if (token !== seq) return;
      if (String(src).indexOf("blob:") === 0) { unplayable(stage, k, ""); return; }
      message(stage, "Direkte Wiedergabe nicht möglich – prüfe Umverpacken …");
      var p = knownProbe || await probe(it);
      if (token !== seq) return;
      if (p && p.available && p.remux) playRemux(stage, it, k, p);
      else unplayable(stage, k, p && p.reason);
    };
    v.src = src;
    stage.innerHTML = "";
    if (k === "video" && String(src).indexOf("blob:") !== 0) {
      // Ton versetzt? Korrektur geht nur im umverpackten Strom (Server/ffmpeg).
      var col = document.createElement("div");
      col.className = "fm-viewer-col";
      var fix = document.createElement("button");
      fix.type = "button";
      fix.className = "fm-viewer-link";
      fix.textContent = "Ton versetzt? Umverpackt abspielen (mit Ton-Korrektur)";
      fix.addEventListener("click", async function (e) {
        e.stopPropagation();
        var p = knownProbe || await probe(it);
        if (token !== seq) return;
        if (p && p.available && p.remux) playRemux(stage, it, k, p);
        else alert("Umverpacken nicht möglich" + (p && p.reason ? ": " + p.reason : " (ffmpeg fehlt auf diesem Node)"));
      });
      col.appendChild(v);
      col.appendChild(fix);
      stage.appendChild(col);
    } else {
      stage.appendChild(v);
    }
    v.play().catch(function () {}); // blockiertes Autoplay: Start per Klick
  }

  // Umverpackt abspielen: fortlaufender MP4-Strom ab Sekunde "offset".
  // Springen startet den Strom an der gewählten Stelle neu (eigene Leiste).
  function playRemux(stage, it, k, p) {
    var token = seq, offset = 0, dragging = false;
    var dur = p.duration || 0;
    var aOff = getAudioOffset(it.hash);
    var col = document.createElement("div");
    col.className = "fm-viewer-col";
    var v = document.createElement(k === "audio" ? "audio" : "video");
    v.className = "fm-viewer-media";
    v.controls = true;
    v.autoplay = true;
    v.setAttribute("playsinline", "");
    v.addEventListener("click", function (e) { e.stopPropagation(); });
    var bar = document.createElement("div");
    bar.className = "fm-viewer-seek";
    bar.innerHTML = '<input type="range" min="0" step="1" value="0">' +
      '<span class="fm-viewer-time">0:00' + (dur ? " / " + fmtTime(dur) : "") + "</span>";
    var note = document.createElement("div");
    note.className = "fm-viewer-note";
    note.textContent = "Umverpackt für den Browser" +
      (p.acodec && ["aac", "mp3", "opus", "vorbis", "flac"].indexOf(p.acodec) < 0 ? " · Ton " + p.acodec.toUpperCase() + " → AAC" : "") +
      (p.vcodec === "hevc" ? " · H.265 – nur mit Browser-Unterstützung (Safari, Edge)" : "");
    var slider = bar.querySelector("input"), label = bar.querySelector(".fm-viewer-time");
    slider.max = String(Math.max(1, Math.floor(dur)));
    if (!dur) slider.disabled = true;

    function start(at) {
      offset = Math.max(0, at || 0);
      v.src = "/api/v1/files/stream/" + encodeURIComponent(it.hash) + "?t=" + offset.toFixed(1) +
        (aOff ? "&a=" + aOff : "") + (it._pq ? "&" + it._pq : "");
      v.play().catch(function () {});
    }
    // Ton-Versatz: Strom an der aktuellen Stelle mit neuem Versatz neu starten.
    var av = document.createElement("div");
    av.className = "fm-viewer-av";
    av.innerHTML = '<button type="button" data-d="-100">◀ Ton früher</button>' +
      '<span class="fm-viewer-avval"></span>' +
      '<button type="button" data-d="100">Ton später ▶</button>' +
      '<button type="button" data-d="0" title="Versatz zurücksetzen">0</button>';
    var avVal = av.querySelector(".fm-viewer-avval");
    function showAOff() {
      avVal.textContent = "Ton-Versatz " + (aOff > 0 ? "+" : "") + (aOff / 1000).toFixed(1).replace(".", ",") + " s";
    }
    showAOff();
    av.addEventListener("click", function (e) {
      e.stopPropagation();
      var b = e.target.closest ? e.target.closest("button") : null;
      if (!b) return;
      var d = parseInt(b.getAttribute("data-d"), 10);
      aOff = d === 0 ? 0 : Math.max(-5000, Math.min(5000, aOff + d));
      setAudioOffset(it.hash, aOff);
      showAOff();
      start(offset + (v.currentTime || 0));
    });
    v.addEventListener("timeupdate", function () {
      var pos = offset + (v.currentTime || 0);
      if (!dragging) slider.value = String(Math.floor(pos));
      label.textContent = fmtTime(pos) + (dur ? " / " + fmtTime(dur) : "");
    });
    slider.addEventListener("input", function () {
      dragging = true;
      label.textContent = fmtTime(+slider.value) + (dur ? " / " + fmtTime(dur) : "");
    });
    slider.addEventListener("change", function () { dragging = false; start(+slider.value); });
    bar.addEventListener("click", function (e) { e.stopPropagation(); });
    v.onerror = async function () {
      if (token !== seq) return;
      try {
        var r = await fetch("/api/v1/files/stream/" + encodeURIComponent(it.hash) + "?t=0" + (it._pq ? "&" + it._pq : ""), { credentials: "same-origin", method: "GET" });
        var j = r.ok ? null : await r.json().catch(function () { return null; });
        try { if (r.body && r.body.cancel) r.body.cancel(); } catch (e2) {}
        unplayable(stage, k, (j && j.error) || "Umverpacken fehlgeschlagen");
      } catch (e) { unplayable(stage, k, "Umverpacken fehlgeschlagen"); }
    };
    col.appendChild(v); col.appendChild(bar);
    if (k !== "audio" || p.acodec) col.appendChild(av);
    col.appendChild(note);
    stage.innerHTML = "";
    stage.appendChild(col);
    start(0);
  }

  // Verschlüsselte Datei laden + im Browser entschlüsseln → Blob-URL.
  async function decryptToUrl(it, k, stage) {
    if (!window.FundusCrypto) {
      message(stage, "Entschlüsselung nicht verfügbar – bitte über ↓ herunterladen.");
      return null;
    }
    if (k !== "image" && it.size > ENC_VIDEO_WARN &&
        !confirm("Verschlüsselte Datei (" + fmtSize(it.size) + "): Sie muss vollständig geladen und entschlüsselt werden, bevor sie abspielt. Fortfahren?")) {
      close();
      return null;
    }
    var pw = prompt("Passwort zum Entschlüsseln von:\n" + it.name.slice(0, -5));
    if (!pw) { close(); return null; }
    message(stage, "Lädt verschlüsselte Datei …");
    try {
      var r = await fetch("/api/v1/files/download/" + encodeURIComponent(it.hash) + (it._pq ? "?" + it._pq : ""), { credentials: "same-origin" });
      if (!r.ok) throw new Error("HTTP " + r.status);
      var buf = await r.arrayBuffer();
      message(stage, "Entschlüsselt …");
      var plain = await window.FundusCrypto.decryptBuffer(buf, pw);
      var type = it.mime && it.mime !== "application/octet-stream" ? it.mime : "";
      state.blobUrl = URL.createObjectURL(new Blob([plain], type ? { type: type } : {}));
      return state.blobUrl;
    } catch (e) {
      message(stage, "✗ Entschlüsseln fehlgeschlagen: " + esc(e.message || "falsches Passwort?"));
      return null;
    }
  }

  // open(list, index): list = [{hash, name, mime, size}], zeigt list[index].
  function open(list, index) {
    list = (list || []).filter(function (x) { return x && x.hash && kind(x.name, x.mime); });
    if (!list.length) return false;
    if (state) close();
    var idx = Math.max(0, Math.min(index || 0, list.length - 1));
    var ov = buildOverlay();
    var onKey = function (e) {
      if (e.key === "Escape") close();
      else if (e.key === "ArrowLeft") step(-1);
      else if (e.key === "ArrowRight") step(1);
    };
    document.addEventListener("keydown", onKey);
    state = { ov: ov, list: list, idx: idx, blobUrl: null, onKey: onKey };
    show();
    return true;
  }

  window.FundusMedia = { kind: kind, open: open, close: close };
})();

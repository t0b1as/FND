// =============================================================================
//  upload.js – Fundus Marketplace
//  Kamera · Web Speech API · LLM-Analyse · Formular-Submit
//
//  Alle UI-Strings kommen aus FUNDUS_STRINGS (von listing_new.lua injiziert).
//  Dadurch ist kein Sprachcode im JS nötig – Lua übersetzt, JS zeigt an.
// =============================================================================

// FUNDUS_STRINGS wird von listing_new.lua als <script>const FUNDUS_STRINGS=…</script>
// vor diesem Script-Tag gesetzt.
const S = (typeof FUNDUS_STRINGS !== "undefined") ? FUNDUS_STRINGS : {};
const str = (key, params) => {
    let s = S[key] || key;
    if (params) Object.keys(params).forEach(k => { s = s.replace(`%{${k}}`, params[k]); });
    return s;
};

// -----------------------------------------------------------------------------
//  Zustand
// -----------------------------------------------------------------------------
const state = {
    images:         [],
    videos:         [],     // mehrere Videos (File[])
    videoHashes:    [],     // content_hashes nach Upload
    recording:      false,
    recognition:    null,
    transcript:     "",
    analysisResult: null,
};

// -----------------------------------------------------------------------------
//  Bild-Upload & Vorschau
// -----------------------------------------------------------------------------
const imageInput    = document.getElementById("image-input");
const imagePreviews = document.getElementById("image-previews");
const analyzeBtn    = document.getElementById("analyze-btn");

// Teilt die Auswahl automatisch in Bilder und (erstes) Video auf.
// Versandart: bei "Versand" das Kostenfeld einblenden, bei Selbstabholung aus.
function deliveryModeChanged() {
    const mode = (document.querySelector('input[name="f-delivery"]:checked')||{}).value || "pickup";
    const row = document.getElementById("f-shipping-cost-row");
    if (row) row.style.display = (mode === "shipping") ? "block" : "none";
}

// ── Media-Mechanik nach bewährtem Partner-Vorbild ───────────────────────────
// Jede Datei wird SOFORT beim Auswählen hochgeladen (eigene Kachel + Thumbnail +
// Lösch-Button), die Hashes werden in globalen Arrays gesammelt. Beim Speichern
// werden nur die Hashes gesendet. So funktionieren Multi-Upload, Thumbnails und
// das Entfernen zuverlässig — auch im Bearbeiten-Modus.
window._mktImageHashes = window._mktImageHashes || [];
window._mktImageThumbs = window._mktImageThumbs || [];
window._mktVideoHashes = window._mktVideoHashes || [];

window._mktVideoThumbs = window._mktVideoThumbs || [];

// Vorschau SOFORT aus der Datei (wie im Partnerportal): Das Thumbnail entsteht
// vor dem Upload als data-URL, die Kachel zeigt den Fortschritt und wird nach
// dem Upload scharf. Schlägt der Upload fehl, erscheint eine Meldung statt
// einer kommentarlos verschwundenen Kachel.
async function handleMediaFiles(fileList) {
    const files = Array.from(fileList);
    for (const file of files) {
        const isImg = file.type.startsWith("image/");
        const isVid = file.type.startsWith("video/");
        if (!isImg && !isVid) continue;
        if (isImg && window._mktImageHashes.length >= 4) { mktMediaMsg("Maximal 4 Bilder pro Anzeige."); continue; }
        const thumb = isImg ? await fileToThumbnail(file) : await videoFileThumb(file);
        const hashes = isImg ? window._mktImageHashes : window._mktVideoHashes;
        const thumbs = isImg ? window._mktImageThumbs : window._mktVideoThumbs;
        const token = "up:" + Math.random().toString(36).slice(2);  // Platzhalter bis zum Hash
        hashes.push(token); thumbs.push(thumb || "");
        isImg ? renderMktImages() : renderMktVideos();
        try {
            const hash = await resumableUpload(file, null, 1);
            const i = hashes.indexOf(token);
            if (i >= 0) hashes[i] = hash;
            if (isImg) state.images.push(file); // für die KI-Analyse
        } catch (e) {
            const i = hashes.indexOf(token);
            if (i >= 0) { hashes.splice(i, 1); thumbs.splice(i, 1); }
            mktMediaMsg("Upload von „" + file.name + "“ fehlgeschlagen: " + ((e && e.message) || e));
        }
        isImg ? renderMktImages() : renderMktVideos();
        if (isImg) updateAnalyzeButton();
    }
}

function mktMediaMsg(text) {
    let el = document.getElementById("mkt-media-msg");
    if (!el) {
        el = document.createElement("div");
        el.id = "mkt-media-msg";
        el.className = "error";
        el.style.cssText = "margin-top:6px;font-size:13px";
        const box = document.getElementById("video-previews") || document.getElementById("image-previews");
        if (box && box.parentNode) box.parentNode.insertBefore(el, box.nextSibling);
    }
    el.textContent = text;
    clearTimeout(el._t);
    el._t = setTimeout(() => { el.textContent = ""; }, 12000);
}

const mktPending = h => !h || String(h).indexOf("up:") === 0;

// Gemeinsame Kachel (Bild oder Video), Maße wie im Partnerportal.
function mktTile(hash, thumb, size, isVideo, onDelete) {
    const wrap = document.createElement("div");
    wrap.className = "media-tile" + (isVideo ? " media-video" : "");
    wrap.style.cssText = "position:relative;width:" + size + "px;height:" + size + "px;background:#000;border-radius:8px";
    const img = document.createElement("img");
    img.style.cssText = "width:" + size + "px;height:" + size + "px;object-fit:cover;border-radius:8px;display:block;" + (mktPending(hash) ? "opacity:0.5" : "");
    if (thumb) img.src = thumb;
    else if (!mktPending(hash)) {
        if (isVideo) mktVideoThumbFromHash(hash, img);
        else img.src = "/api/v1/files/download/" + hash;
    }
    wrap.appendChild(img);
    if (isVideo || mktPending(hash)) {
        const badge = document.createElement("span");
        badge.style.cssText = "position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);color:#fff;font-size:" + (size > 150 ? 40 : 26) + "px;text-shadow:0 2px 8px rgba(0,0,0,0.7);pointer-events:none";
        badge.textContent = mktPending(hash) ? "⏳" : "▶";
        wrap.appendChild(badge);
    }
    if (!mktPending(hash)) {
        wrap.style.cursor = "zoom-in";
        wrap.onclick = () => isVideo ? openVideoLightbox("/api/v1/files/download/" + hash)
                                     : openImageLightbox("/api/v1/files/download/" + hash);
        const del = document.createElement("button");
        del.textContent = "×"; del.type = "button"; del.className = "tile-del";
        del.onclick = (e) => { e.stopPropagation(); onDelete(); };
        wrap.appendChild(del);
    }
    return wrap;
}

// Bilder: Galerie, erstes Bild groß (Hauptbild der Anzeige).
function renderMktImages() {
    const box = document.getElementById("image-previews");
    if (!box) return;
    box.style.cssText = "display:grid;grid-template-columns:repeat(auto-fill,104px);grid-auto-rows:104px;gap:8px;justify-content:start;margin-top:0.75rem";
    box.innerHTML = "";
    window._mktImageHashes.forEach(function(hash, idx){
        const big = idx === 0;
        const t = mktTile(hash, window._mktImageThumbs[idx], big ? 216 : 104, false, function(){
            window._mktImageHashes.splice(idx, 1);
            window._mktImageThumbs.splice(idx, 1);
            renderMktImages();
            updateAnalyzeButton();
        });
        if (big) { t.style.gridColumn = "span 2"; t.style.gridRow = "span 2"; }
        box.appendChild(t);
    });
}

function renderMktVideos() {
    const box = document.getElementById("video-previews");
    if (!box) return;
    box.style.cssText = "display:flex;gap:8px;flex-wrap:wrap;margin-top:8px";
    box.innerHTML = "";
    window._mktVideoHashes.forEach(function(hash, idx){
        box.appendChild(mktTile(hash, window._mktVideoThumbs[idx], 104, true, function(){
            window._mktVideoHashes.splice(idx, 1);
            window._mktVideoThumbs.splice(idx, 1);
            renderMktVideos();
        }));
    });
}

// Standbild aus einer Videodatei (vor dem Upload). data-URL, keine blob-Reste.
function videoFileThumb(file) {
    return new Promise((resolve) => {
        let url = "", done = false;
        const finish = (v) => { if (done) return; done = true; try { URL.revokeObjectURL(url); } catch(e){} resolve(v || ""); };
        try {
            url = URL.createObjectURL(file);
            const v = document.createElement("video");
            v.preload = "metadata"; v.muted = true; v.playsInline = true; v.src = url;
            v.addEventListener("loadeddata", () => { try { v.currentTime = Math.min(1, (v.duration || 2) / 2); } catch(e){ finish(""); } });
            v.addEventListener("seeked", () => {
                try {
                    const c = document.createElement("canvas"); c.width = 200; c.height = 200;
                    const s = Math.min(v.videoWidth, v.videoHeight) || 200;
                    c.getContext("2d").drawImage(v, (v.videoWidth - s) / 2, (v.videoHeight - s) / 2, s, s, 0, 0, 200, 200);
                    finish(c.toDataURL("image/jpeg", 0.6));
                } catch(e) { finish(""); }
            }, { once: true });
            v.addEventListener("error", () => finish(""), { once: true });
            setTimeout(() => finish(""), 6000); // Format nicht abspielbar → ohne Standbild
        } catch(e) { finish(""); }
    });
}

// Standbild aus einem bereits hochgeladenen Video (Bearbeiten-Modus).
function mktVideoThumbFromHash(hash, imgEl) {
    try {
        const v = document.createElement("video"); v.preload = "metadata"; v.muted = true; v.playsInline = true;
        v.src = "/api/v1/files/download/" + hash;
        v.addEventListener("loadeddata", () => { try { v.currentTime = Math.min(1, (v.duration || 2) / 2); } catch(e){} });
        v.addEventListener("seeked", () => {
            try {
                const c = document.createElement("canvas"); c.width = 200; c.height = 200;
                const s = Math.min(v.videoWidth, v.videoHeight) || 200;
                c.getContext("2d").drawImage(v, (v.videoWidth - s) / 2, (v.videoHeight - s) / 2, s, s, 0, 0, 200, 200);
                imgEl.src = c.toDataURL("image/jpeg", 0.6);
            } catch(e) {}
        }, { once: true });
    } catch(e) {}
}

// mktFinalMedia: nur fertig hochgeladene Medien (laufende Uploads fehlen noch),
// Bild-Thumbnails passend zu den Bild-Hashes.
function mktFinalMedia() {
    const ih = [], it = [], vh = [];
    (window._mktImageHashes || []).forEach(function(h, i){
        if (!mktPending(h)) { ih.push(h); it.push((window._mktImageThumbs || [])[i] || ""); }
    });
    (window._mktVideoHashes || []).forEach(function(h){ if (!mktPending(h)) vh.push(h); });
    return { images: it, image_hashes: ih, video_hashes: vh };
}

// Bild groß anzeigen (Klick schließt).
function openImageLightbox(src) {
    const ov = document.createElement("div");
    ov.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.9);display:flex;align-items:center;justify-content:center;z-index:9999;cursor:zoom-out";
    const im = document.createElement("img");
    im.src = src;
    im.style.cssText = "max-width:92vw;max-height:92vh;border-radius:8px";
    ov.appendChild(im);
    ov.onclick = () => ov.remove();
    document.body.appendChild(ov);
}

// openVideoLightbox: Video groß abspielen (Klick außerhalb schließt).
function openVideoLightbox(src) {
    const ov = document.createElement("div");
    ov.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.88);display:flex;align-items:center;justify-content:center;z-index:9999;cursor:zoom-out";
    const v = document.createElement("video");
    v.src = src;
    v.controls = true;
    v.loop = true;      // Endlosschleife
    v.muted = false;    // mit Ton
    v.volume = 1.0;
    v.autoplay = true;
    v.style.cssText = "max-width:90vw;max-height:90vh;border-radius:8px;cursor:default;box-shadow:0 8px 40px rgba(0,0,0,0.6)";
    // Wiedergabe starten (unmuted). Falls der Browser unmuted-Autoplay blockiert,
    // startet es beim ersten Klick des Nutzers ohnehin.
    v.play().catch(() => {});
    // Klick auf das Video selbst schließt NICHT (nur Steuerung/Play).
    v.addEventListener("click", (e) => e.stopPropagation());

    // Sauberes Schließen: Video anhalten, Quelle lösen (stoppt Ton sofort),
    // Overlay entfernen, Key-Listener abmelden.
    function closeLightbox() {
        try { v.pause(); v.removeAttribute("src"); v.load(); } catch(e) {}
        ov.remove();
        document.removeEventListener("keydown", onKey);
    }
    const onKey = (e) => { if (e.key === "Escape") closeLightbox(); };

    const close = document.createElement("button");
    close.textContent = "×";
    close.style.cssText = "position:absolute;top:16px;right:20px;background:rgba(0,0,0,0.6);color:#fff;border:none;font-size:28px;width:44px;height:44px;border-radius:50%;cursor:pointer;line-height:1;z-index:1";
    close.onclick = (e) => { e.stopPropagation(); closeLightbox(); };

    ov.appendChild(v); ov.appendChild(close);
    // Klick irgendwo auf den dunklen Hintergrund (außerhalb des Videos) schließt + stoppt.
    ov.addEventListener("click", closeLightbox);
    document.addEventListener("keydown", onKey);
    document.body.appendChild(ov);
}

// fileToThumbnail: erzeugt ein kleines Base64-Thumbnail aus einer Bilddatei.
function fileToThumbnail(file) {
    return new Promise((resolve) => {
        try {
            const url = URL.createObjectURL(file);
            const im = new Image();
            im.onload = () => {
                const c = document.createElement("canvas"); c.width = 200; c.height = 200;
                const ctx = c.getContext("2d");
                const s = Math.min(im.width, im.height);
                ctx.drawImage(im, (im.width-s)/2, (im.height-s)/2, s, s, 0, 0, 200, 200);
                URL.revokeObjectURL(url);
                resolve(c.toDataURL("image/jpeg", 0.6));
            };
            im.onerror = () => resolve("");
            im.src = url;
        } catch(e) { resolve(""); }
    });
}

// showVideoThumbnail extrahiert einen Frame (bei ~1s) aus dem gewählten Video
// und zeigt ihn als kleines Vorschaubild. Rein clientseitig, ohne Upload.
function showVideoThumbnail(file, container) {
    try {
        const url = URL.createObjectURL(file);
        const v = document.createElement("video");
        v.preload = "metadata";
        v.muted = true;
        v.playsInline = true;
        v.src = url;
        v.addEventListener("loadeddata", () => {
            // Auf ~1s springen (erster Frame ist oft schwarz).
            try { v.currentTime = Math.min(1, (v.duration || 2) / 2); } catch(e) {}
        });
        v.addEventListener("seeked", () => {
            const canvas = document.createElement("canvas");
            const w = 160, h = Math.round(160 * (v.videoHeight / v.videoWidth || 0.66));
            canvas.width = w; canvas.height = h;
            canvas.getContext("2d").drawImage(v, 0, 0, w, h);
            const img = document.createElement("img");
            img.src = canvas.toDataURL("image/jpeg", 0.7);
            img.style.cssText = "display:block;margin-top:6px;border-radius:6px;max-width:160px";
            container.appendChild(img);
            URL.revokeObjectURL(url);
        }, { once: true });
    } catch(e) { /* Thumbnail ist optional */ }
}

imageInput?.addEventListener("change", (e) => {
    handleMediaFiles(e.target.files);
});

const dropZone = document.getElementById("image-drop");
dropZone?.addEventListener("dragover",  e  => { e.preventDefault(); dropZone.classList.add("drag-over"); });
dropZone?.addEventListener("dragleave", () => dropZone.classList.remove("drag-over"));
dropZone?.addEventListener("drop", e => {
    e.preventDefault();
    dropZone.classList.remove("drag-over");
    handleMediaFiles(e.dataTransfer.files);
});

function renderPreviews(files) {
    imagePreviews.innerHTML = "";
    files.forEach((file, i) => {
        const reader = new FileReader();
        reader.onload = ev => {
            const wrap = document.createElement("div");
            wrap.className = "preview-wrap";
            wrap.innerHTML = `<img src="${ev.target.result}" class="preview-img" alt="">
                <button class="preview-remove" onclick="removeImage(${i})" title="×">×</button>`;
            imagePreviews.appendChild(wrap);
        };
        reader.readAsDataURL(file);
    });
}

function removeImage(index) {
    state.images.splice(index, 1);
    renderPreviews(state.images);
    updateAnalyzeButton();
}

// Verkleinert ein Bild clientseitig auf max. 500px Kantenlaenge und gibt es
// als JPEG-DataURL zurueck. So bleiben die im Listing eingebetteten Bilder
// klein genug fuer die P2P-Propagation (kein zentraler Bildserver noetig).
// resumableUpload lädt eine grosse Datei (z.B. Video) block-weise hoch und
// kann nach einem Disconnect ODER Browser-Neustart fortsetzen. Die Upload-ID
// wird deterministisch aus Dateiname+Größe+Änderungszeit abgeleitet, sodass
// dieselbe Datei dieselbe ID bekommt und der Server die alten Blöcke wiederfindet.
async function resumableUpload(file, onProgress, redundancy) {
    const BLOCK = 4 * 1024 * 1024; // 4 MiB
    // Deterministische ID: gleiche Datei → gleiche ID → Wiederaufnahme möglich
    const idSeed = file.name + "|" + file.size + "|" + (file.lastModified || 0);
    const uploadId = "u" + Math.abs(hashString(idSeed)).toString(36) +
                     (file.size % 100000).toString(36);
    // Persistente Fortschrittsanzeige registrieren (übersteht Reload)
    if (window.FundusProgress) FundusProgress.set(uploadId, { name: file.name, state: "uploading", progress: 0 });
    const report = (p) => { if (onProgress) onProgress(p); if (window.FundusProgress) FundusProgress.progress(uploadId, p); };
    // begin
    const beginRes = await fetch("/api/v1/files/upload/begin", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            upload_id:  uploadId,
            file_name:  file.name,
            mime_type:  file.type || "application/octet-stream",
            total_size: file.size,
            block_size: BLOCK,
            redundancy: redundancy || 0,
        }),
    });
    if (!beginRes.ok) throw new Error("begin fehlgeschlagen");
    const begin = await beginRes.json();
    const have = new Set(begin.have_blocks || []);
    const totalBlocks = begin.total_blocks;

    for (let i = 0; i < totalBlocks; i++) {
        if (have.has(i)) { report((i+1)/totalBlocks); continue; }
        const slice = file.slice(i * BLOCK, Math.min((i+1) * BLOCK, file.size));
        // Bis zu 3 Versuche pro Block (Disconnect-Toleranz)
        let ok = false;
        for (let attempt = 0; attempt < 3 && !ok; attempt++) {
            try {
                const r = await fetch(`/api/v1/files/upload/block?id=${uploadId}&index=${i}`, {
                    method: "POST", body: slice,
                });
                ok = r.ok;
            } catch (e) { ok = false; }
            if (!ok) await new Promise(res => setTimeout(res, 800));
        }
        if (!ok) throw new Error("Block " + i + " fehlgeschlagen");
        report((i+1)/totalBlocks);
    }
    // finish startet die serverseitige Verarbeitung im Hintergrund (kein Timeout)
    const finRes = await fetch(`/api/v1/files/upload/finish?id=${uploadId}`, { method: "POST" });
    if (!finRes.ok) throw new Error("finish fehlgeschlagen");
    if (window.FundusProgress) FundusProgress.finalizing(uploadId);
    // Auf Abschluss der Finalisierung warten (Status pollen, je 1.5s).
    // Jeder Poll ist eine kurze Anfrage → kein HTTP-Timeout bei grossen Dateien.
    for (;;) {
        await new Promise(res => setTimeout(res, 1500));
        let d;
        try {
            const sr = await fetch(`/api/v1/files/upload/status?id=${uploadId}`);
            if (!sr.ok) continue;
            d = await sr.json();
        } catch (e) { continue; }
        if (d.state === "done") {
            if (window.FundusProgress) FundusProgress.done(uploadId);
            return d.content_hash;
        }
        if (d.state === "error") {
            if (window.FundusProgress) FundusProgress.set(uploadId, { state: "error", error: d.error });
            throw new Error("Verarbeitung fehlgeschlagen: " + (d.error || ""));
        }
    }
}

function hashString(s) {
    let h = 0;
    for (let i = 0; i < s.length; i++) { h = (h << 5) - h + s.charCodeAt(i); h |= 0; }
    return h;
}

// resizeToBlob verkleinert ein Bild und gibt einen JPEG-Blob zurück
// (für den FileStore-Upload – deutlich kleiner/schneller als das Original).
function resizeToBlob(file, maxPx = 1600, quality = 0.82) {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = ev => {
            const img = new Image();
            img.onload = () => {
                let w = img.width, h = img.height;
                if (w > h && w > maxPx) { h = Math.round(h * maxPx / w); w = maxPx; }
                else if (h > maxPx) { w = Math.round(w * maxPx / h); h = maxPx; }
                const cv = document.createElement("canvas");
                cv.width = w; cv.height = h;
                cv.getContext("2d").drawImage(img, 0, 0, w, h);
                cv.toBlob(b => b ? resolve(b) : reject(new Error("toBlob failed")),
                    "image/jpeg", quality);
            };
            img.onerror = reject;
            img.src = ev.target.result;
        };
        reader.onerror = reject;
        reader.readAsDataURL(file);
    });
}

function resizeToDataURL(file, maxPx = 500, quality = 0.7) {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = ev => {
            const img = new Image();
            img.onload = () => {
                let { width, height } = img;
                if (width > height && width > maxPx) {
                    height = Math.round(height * maxPx / width); width = maxPx;
                } else if (height > maxPx) {
                    width = Math.round(width * maxPx / height); height = maxPx;
                }
                const canvas = document.createElement("canvas");
                canvas.width = width; canvas.height = height;
                canvas.getContext("2d").drawImage(img, 0, 0, width, height);
                resolve(canvas.toDataURL("image/jpeg", quality));
            };
            img.onerror = reject;
            img.src = ev.target.result;
        };
        reader.onerror = reject;
        reader.readAsDataURL(file);
    });
}

function updateAnalyzeButton() {
    if (!analyzeBtn) return;
    const hasContent = (window._mktImageHashes || []).length > 0 ||
        state.images.length > 0 ||
        (document.getElementById("voice-text")?.value.trim().length > 0);
    analyzeBtn.disabled = !hasContent;
}

// -----------------------------------------------------------------------------
//  Web Speech API – Spracheingabe
// -----------------------------------------------------------------------------
function toggleRecording() {
    state.recording ? stopRecording() : startRecording();
}

function startRecording() {
    const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!SR) { alert(str("mic_unsupported")); return; }

    const rec = new SR();
    // Sprache aus den injizierten Strings nehmen (z.B. "de" → "de-DE")
    const langCode = S.lang || "de";
    rec.lang            = langCode.length === 2 ? langCode + "-" + langCode.toUpperCase() : langCode;
    rec.continuous      = true;
    rec.interimResults  = true;

    const micBtn   = document.getElementById("mic-btn");
    const micIcon  = document.getElementById("mic-icon");
    const micLabel = document.getElementById("mic-label");
    const micHint  = document.getElementById("mic-hint");
    const textarea = document.getElementById("voice-text");

    rec.onstart = () => {
        state.recording = true;
        micBtn?.classList.add("recording");
        if (micIcon)  micIcon.textContent  = "⏹";
        if (micLabel) micLabel.textContent = str("mic_stop");
        if (micHint)  micHint.textContent  = "…";
    };

    rec.onresult = event => {
        let interim = "";
        let final   = state.transcript;
        for (let i = event.resultIndex; i < event.results.length; i++) {
            const txt = event.results[i][0].transcript;
            if (event.results[i].isFinal) {
                final += (final ? " " : "") + txt;
            } else {
                interim = txt;
            }
        }
        state.transcript = final;
        if (textarea) textarea.value = final + (interim ? " " + interim : "");
        updateAnalyzeButton();
    };

    rec.onerror = event => {
        const msg = event.error === "not-allowed"
            ? str("mic_no_permission")
            : str("mic_error", { error: event.error });
        if (micHint) micHint.textContent = msg;
        stopRecording();
    };

    rec.onend = () => stopRecording();

    state.recognition = rec;
    rec.start();
}

function stopRecording() {
    state.recognition?.stop();
    state.recognition = null;
    state.recording   = false;

    const micBtn   = document.getElementById("mic-btn");
    const micIcon  = document.getElementById("mic-icon");
    const micLabel = document.getElementById("mic-label");
    const micHint  = document.getElementById("mic-hint");

    micBtn?.classList.remove("recording");
    if (micIcon)  micIcon.textContent  = "●";
    if (micLabel) micLabel.textContent = str("mic_start");
    if (micHint)  micHint.textContent  = str("mic_done");

    const textarea = document.getElementById("voice-text");
    if (textarea && state.transcript) textarea.value = state.transcript;
    updateAnalyzeButton();
}

function onVoiceTextChange() {
    state.transcript = document.getElementById("voice-text")?.value || "";
    updateAnalyzeButton();
}

// -----------------------------------------------------------------------------
//  LLM-Analyse
// -----------------------------------------------------------------------------
async function runAnalysis() {
    if (analyzeBtn) analyzeBtn.disabled = true;
    const statusEl   = document.getElementById("analysis-status");
    const statusText = document.getElementById("analysis-status-text");
    if (statusEl)   statusEl.style.display = "flex";
    if (statusText) statusText.textContent  = str("analyzing");

    try {
        const fd = new FormData();
        state.images.forEach(f => fd.append("images[]", f, f.name));
        fd.append("voice_text", document.getElementById("voice-text")?.value || "");
        fd.append("language",   S.lang || "de");

        const resp = await fetch("/api/v1/analyze", { method: "POST", body: fd });
        if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            throw new Error(err.error || `HTTP ${resp.status}`);
        }

        const result = await resp.json();
        state.analysisResult = result;
        applyAnalysisResult(result);

    } catch (err) {
        if (statusText) {
            statusText.textContent = str("analyze_error", { reason: err.message });
            statusText.className   = "error";
        }
    } finally {
        if (analyzeBtn) analyzeBtn.disabled = false;
    }
}

function applyAnalysisResult(r) {
    setField("f-title",       r.description   || "");
    setField("f-description", r.listing_text  || "");
    setField("f-price-min",   r.price_min  != null ? r.price_min.toFixed(2)  : "");
    setField("f-price-max",   r.price_max  != null ? r.price_max.toFixed(2)  : "");
    setField("f-keywords",    (r.keywords || []).join(", "));
    setSelect("f-condition",  r.condition || "");
    setSelect("f-category",   r.category  || "");

    document.getElementById("step-form")?.classList.add("highlighted");
    setTimeout(() => document.getElementById("step-form")?.classList.remove("highlighted"), 1500);
    document.getElementById("listing-form")?.scrollIntoView({ behavior: "smooth", block: "start" });
}

// -----------------------------------------------------------------------------
//  Formular-Submit
// -----------------------------------------------------------------------------
async function submitListing(event) {
    event.preventDefault();
    const submitBtn    = document.getElementById("submit-btn");
    const submitStatus = document.getElementById("submit-status");
    if (submitBtn) submitBtn.disabled = true;
    if (submitStatus) submitStatus.textContent = str("publishing");

    // Bilder zweistufig verarbeiten (max 4):
    //  - Thumbnail (kleine Base64-DataURL) -> bleibt im Record fuer schnelle
    //    Propagation und sofortige Vorschau auf der Detailseite.
    //  - Vollbild -> in den FileStore (P2P, chunked, 5x redundant); nur der
    //    content_hash kommt in den Record. Wird beim Oeffnen nachgeladen.
    // Bilder und Videos wurden bereits beim Auswählen hochgeladen (Partner-
    // Mechanik). Wir nutzen nur die gesammelten Hashes + Thumbnails.
    const fm = mktFinalMedia(); // laufende Uploads nicht mitschicken
    const imageHashes  = fm.image_hashes;
    const thumbnails   = fm.images;
    const videoHashes  = fm.video_hashes;

    const payload = {
        title:        getField("f-title"),
        category:     getField("f-category"),
        condition:    getField("f-condition"),
        price:        parseFloat(getField("f-price")) || 0,
        seller_wallet: getField("f-seller-wallet") || "",
        delivery:     (document.querySelector('input[name="f-delivery"]:checked')||{}).value || "pickup",
        shipping_cost: parseFloat(getField("f-shipping-cost")) || 0,
        description:  getField("f-description"),
        keywords:     getField("f-keywords").split(",").map(s => s.trim()).filter(Boolean),
        plz:          getField("f-plz"),
        images:       thumbnails,    // Base64-Thumbnails (sofortige Vorschau)
        image_hashes: imageHashes,   // FileStore-Hashes der Vollbilder
        video_hashes: videoHashes,              // FileStore-Hashes der Videos
        video_hash:   videoHashes[0] || null,   // Rückwärtskompat: erstes Video
        llm_analysis: state.analysisResult || null,
    };

    // Eigenen Messenger-Kontaktschlüssel einbetten (falls Messenger-Identität
    // aktiv), damit Käufer den Verkäufer direkt verschlüsselt anschreiben können.
    try {
        const who = await fetch("/api/v1/messenger/whoami").then(r => r.json());
        if (who && who.active && who.public_key) {
            payload.contact_pub_key = who.public_key;
            payload.contact_fundus_id = who.fundus_id;
        }
    } catch (e) { /* ohne Kontaktschlüssel inserieren ist ok */ }

    try {
        const resp = await fetch("/api/v1/listings", {
            method:  "POST",
            headers: { "Content-Type": "application/json" },
            body:    JSON.stringify(payload),
        });
        if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            throw new Error(err.error || `HTTP ${resp.status}`);
        }
        const listing = await resp.json();
        if (submitStatus) submitStatus.innerHTML = `<span class="success">${str("published")}</span>`;
        setTimeout(() => { window.location.href = "/listings/" + listing.id; }, 1500);
    } catch (err) {
        if (submitStatus) submitStatus.innerHTML = `<span class="error">${escHtml(err.message)}</span>`;
        if (submitBtn) submitBtn.disabled = false;
    }
}

// -----------------------------------------------------------------------------
//  Hilfsfunktionen
// -----------------------------------------------------------------------------
const setField  = (id, v)  => { const el = document.getElementById(id); if (el) el.value = v; };
const getField  = id       => document.getElementById(id)?.value || "";
const escHtml   = s        => String(s).replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;");

function setSelect(id, value) {
    const sel = document.getElementById(id);
    if (!sel || !value) return;
    const v = value.toLowerCase();
    for (const opt of sel.options) {
        if (opt.value.toLowerCase() === v || opt.text.toLowerCase() === v) { sel.value = opt.value; return; }
    }
}

// ── Wallet-Adressbuch (Empfangsadresse für Verkäufer) ───────────────────────
async function loadAddrBookDropdown() {
    const sel = document.getElementById("f-seller-wallet");
    if (!sel) return;
    try {
        const r = await fetch("/api/v1/addressbook");
        const d = await r.json();
        const entries = d.entries || [];
        const current = sel.value;
        // Erste Option (leer) behalten, Rest neu aufbauen.
        sel.innerHTML = sel.options[0] ? sel.options[0].outerHTML : '<option value="">—</option>';
        entries.forEach(function(e){
            const opt = document.createElement("option");
            opt.value = e.address;
            opt.textContent = (e.description ? e.description + " — " : "") + e.address.slice(0,10) + "…" + e.address.slice(-6);
            sel.appendChild(opt);
        });
        if (current) sel.value = current;
    } catch(e) {}
}

function openAddrBook() {
    let ov = document.getElementById("addrbook-modal");
    if (!ov) {
        ov = document.createElement("div");
        ov.id = "addrbook-modal";
        ov.className = "fundus-modal-overlay";
        ov.innerHTML = '<div class="fundus-modal">'+
            '<div class="fundus-modal-head">'+
            '<h3>📖 Wallet-Adressbuch</h3>'+
            '<button class="btn-sm" onclick="document.getElementById(\'addrbook-modal\').remove()">✕</button></div>'+
            '<div class="fundus-modal-body">'+
            '<div style="display:flex;gap:8px;margin-bottom:14px;flex-wrap:wrap">'+
            '<input type="text" id="ab-desc" placeholder="Beschreibung (z.B. Haupt-Wallet)" style="flex:1;min-width:140px">'+
            '<input type="text" id="ab-addr" placeholder="0x…" style="flex:2;min-width:180px">'+
            '<button class="btn-sm btn-prim" onclick="addAddrBookEntry()">Hinzufügen</button></div>'+
            '<div id="ab-msg" style="font-size:12px;margin-bottom:8px"></div>'+
            '<div id="ab-list"></div></div></div>';
        document.body.appendChild(ov);
        ov.addEventListener("click", function(e){ if(e.target===ov) ov.remove(); });
    }
    renderAddrBookList();
}

async function renderAddrBookList() {
    const box = document.getElementById("ab-list");
    if (!box) return;
    try {
        const r = await fetch("/api/v1/addressbook");
        const d = await r.json();
        const entries = d.entries || [];
        if (!entries.length) { box.innerHTML = '<div style="color:var(--muted,#888);font-size:13px">Noch keine Adressen.</div>'; return; }
        box.innerHTML = entries.map(function(e){
            return '<div class="addr-entry">'+
                '<span><span class="addr-desc">'+(e.description||'(ohne Beschreibung)')+'</span><br><span class="addr-hash">'+e.address+'</span></span>'+
                '<button class="btn-sm btn-danger" onclick="deleteAddrBookEntry(\''+e.id+'\')">✕</button></div>';
        }).join("");
    } catch(e) {}
}

async function addAddrBookEntry() {
    const addr = document.getElementById("ab-addr").value.trim();
    const desc = document.getElementById("ab-desc").value.trim();
    const msg  = document.getElementById("ab-msg");
    if (!addr) { msg.textContent = "Bitte eine Adresse eingeben."; msg.style.color = "var(--red,#e55)"; return; }
    try {
        const r = await fetch("/api/v1/addressbook", {
            method: "POST", headers: {"Content-Type":"application/json"},
            body: JSON.stringify({ address: addr, description: desc })
        });
        const d = await r.json();
        if (r.ok && d.ok) {
            msg.textContent = "✓ Hinzugefügt"; msg.style.color = "var(--grn,#2c2)";
            document.getElementById("ab-addr").value = "";
            document.getElementById("ab-desc").value = "";
            renderAddrBookList();
            loadAddrBookDropdown();
        } else {
            msg.textContent = "✗ " + (d.error || "Fehler"); msg.style.color = "var(--red,#e55)";
        }
    } catch(e) { msg.textContent = "✗ " + e.message; msg.style.color = "var(--red,#e55)"; }
}

async function deleteAddrBookEntry(id) {
    try {
        await fetch("/api/v1/addressbook/" + id, { method: "DELETE" });
        renderAddrBookList();
        loadAddrBookDropdown();
    } catch(e) {}
}

// Dropdown beim Laden der Seite befüllen.
document.addEventListener("DOMContentLoaded", loadAddrBookDropdown);

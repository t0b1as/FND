// Partner-Seite (ausgelagert aus pages/partner.lua, gecacht über ?v=<Revision>)
document.addEventListener("DOMContentLoaded", function(){
  try { loadProfileClientSide(); } catch(e){ console.error('loadProfile:', e); }
  try { loadMatches(); } catch(e){ console.error('loadMatches:', e); }
});

// loadMatches holt die Partnervorschläge per fetch (mit Session-Cookie) und
// rendert sie clientseitig — ersetzt die frühere serverseitige Schleife, die
// das Cookie bei internen Requests nicht durchreichte.
async function loadMatches() {
  const cont = document.getElementById("matches-container");
  const cnt = document.getElementById("match-count");
  let matches = [], info = null, errMsg = '';
  try {
    const r = await fetch("/api/v1/partner/matches", {credentials:"same-origin"});
    if (r.ok) { info = await r.json(); matches = (info && info.matches) || []; }
    else if (r.status === 400) { errMsg = 'Lege zuerst dein Profil an und speichere es – dann wird gematcht.'; }
    else if (r.status === 401) { errMsg = 'Bitte oben rechts anmelden.'; }
    else { errMsg = 'Matching fehlgeschlagen (' + r.status + ').'; }
  } catch(e){ errMsg = 'Keine Verbindung zum Node.'; }
  if (cnt) cnt.textContent = matches.length;
  if (!cont) return;
  if (!matches.length) {
    // Verständlich erklären, WARUM es 0 Treffer gibt.
    let why = errMsg;
    if (!why && info) {
      const n = info.profiles || 0;
      if (!n) {
        why = 'Noch keine anderen Profile im Netz bekannt. Profile anderer Nodes werden alle 5 Minuten abgeglichen.';
      } else {
        const rj = info.rejected || {};
        const label = { entfernung:'außerhalb des Suchradius',
                        geschlecht:'Geschlecht passt nicht zu deinem Suchwunsch (Reiter „Suche")',
                        geschlecht_gegen:'dein Geschlecht passt nicht zu ihrem/seinem Suchwunsch',
                        alter:'Altersklasse passt nicht zu deinem Suchwunsch',
                        alter_gegen:'deine Altersklasse passt nicht zu ihrem/seinem Suchwunsch',
                        abgelaufen:'Profil abgelaufen (Besitzer länger offline)' };
        const parts = Object.keys(rj).map(k => rj[k] + '× ' + (label[k] || k));
        why = n + (n === 1 ? ' Profil' : ' Profile') + ' bekannt, aber keines passt' + (parts.length ? ': ' + parts.join(', ') : '') + '.';
      }
    }
    cont.innerHTML = '<p class="empty">Keine passenden Profile gefunden.</p>' +
      (why ? '<p class="meta" style="margin-top:6px">' + escapeHtml(why) + '</p>' : '');
    return;
  }
  let html = '<div class="status-grid">';
  for (const m of matches) {
    const score = Math.round((m.score || 0) * 100);
    let nick = m.nickname || '';
    if (!nick) nick = (m.peer_id || '–').slice(0,12) + '…';
    let imgHtml;
    if (Array.isArray(m.image_hashes) && m.image_hashes.length > 0) {
      imgHtml = '<img src="/api/v1/files/download/' + encodeURIComponent(m.image_hashes[0]) + '" class="match-photo" alt="" loading="lazy">';
    } else {
      imgHtml = '<div class="match-photo match-noimg">👤</div>';
    }
    let hobbyHtml = '';
    if (Array.isArray(m.hobbies) && m.hobbies.length > 0) {
      hobbyHtml = '<div class="match-tags">' + m.hobbies.map(h => '<span class="match-tag">'+escapeHtml(h)+'</span>').join('') + '</div>';
    }
    const gender = m.gender || '';
    const age = (m.age != null && m.age !== 0) ? String(m.age) : '';
    let subline = gender;
    if (age) subline += (gender ? ' · ' : '') + age;
    const bio = m.bio ? '<div class="match-bio">'+escapeHtml(decodeMaybe(m.bio))+'</div>' : '';
    const dist = (m.distance_km != null && m.distance_km >= 0) ? m.distance_km.toFixed(0) : '–';
    const common = m.common_interests || 0;
    const pj = JSON.stringify(m).split("'").join("&#39;");
    html += '<div class="match-card" onclick=\'openMatchProfile(' + pj + ')\'>' +
      imgHtml +
      '<div class="match-info">' +
        '<div class="match-name">' + escapeHtml(nick) + '</div>' +
        '<div class="match-sub">' + escapeHtml(subline) + '</div>' +
        '<div class="match-meta">' + score + '% · ' + dist + ' km · ' + common + ' gemeinsam</div>' +
        bio + hobbyHtml +
      '</div></div>';
  }
  html += '</div>';
  cont.innerHTML = html;
}

// loadProfileClientSide lädt das eigene Profil per fetch (mit Session-Cookie)
// und befüllt die Formularfelder. Ersetzt die frühere serverseitige Befüllung,
// die das Session-Cookie bei internen Requests nicht zuverlässig durchreichte.
async function loadProfileClientSide() {
  let p;
  try {
    const r = await fetch("/api/v1/partner/profile", {credentials:"same-origin"});
    if (!r.ok) return; // kein Profil / nicht eingeloggt → Felder bleiben leer
    p = await r.json();
  } catch(e){ return; }
  if (!p) return;
  const set = (id, val) => { const el = document.getElementById(id); if (el && val != null) el.value = val; };
  set("p-nick", p.nickname);
  set("p-gender", p.gender);
  set("p-birthdate", p.birthdate);
  set("p-bio", p.bio);
  set("p-profession", p.profession);
  const eduEl = document.getElementById("p-edu"); if (eduEl && p.education) eduEl.value = p.education;
  const indEl = document.getElementById("p-industry"); if (indEl && p.industry) indEl.value = p.industry;
  // Radius / Suchpräferenzen
  // Suchwünsche vollständig wiederherstellen. Früher wurde nur der Radius
  // gesetzt und das Geschlecht aus einem falschen Feld (gender statt genders)
  // gelesen – das Auswahlfeld blieb auf der ersten Option ("männlich"), und
  // jedes erneute Speichern überschrieb den Wunsch stillschweigend.
  const sk = p.seeking || {};
  set("p-radius", sk.radius_km);
  const sg = Array.isArray(sk.genders) ? sk.genders.filter(Boolean) : [];
  const sgEl = document.getElementById("p-seek-gender");
  if (sgEl) sgEl.value = (sg.length === 1) ? sg[0] : ""; // mehrere/keine = egal
  const setChecks = (arr, name) => {
    document.querySelectorAll('input[name="' + name + '"]').forEach(cb => {
      cb.checked = Array.isArray(arr) && arr.indexOf(cb.value) >= 0;
    });
  };
  setChecks(sk.age_ranges, "seek_age");
  setChecks(sk.educations, "seek_edu");
  setChecks(sk.sex_pref_abbrs, "seek_sex");
  // Eigene Vorlieben inkl. Rolle wiederherstellen (sonst beim Speichern gelöscht).
  if (Array.isArray(p.sexual_prefs)) {
    for (const sp of p.sexual_prefs) {
      const abbr = sp && (sp.abbr || sp.Abbr);
      if (!abbr) continue;
      const cb = document.querySelector('.sex-abbr-cb[data-abbr="' + CSS.escape(abbr) + '"]');
      if (!cb) continue;
      if (!cb.checked) { cb.checked = true; cb.dispatchEvent(new Event("change", {bubbles: true})); }
      const role = sp.role || sp.Role;
      if (role) {
        document.querySelectorAll('#roles-' + CSS.escape(abbr) + ' .role-btn').forEach(b => {
          b.classList.toggle("role-active", b.dataset.role === role);
        });
      }
    }
  }
  // Checkboxen (Hobbys, Vorlieben, Abneigungen)
  const checkList = (arr, name) => {
    if (!Array.isArray(arr)) return;
    for (const v of arr) {
      const cb = document.querySelector('input[name="'+name+'"][value="'+CSS.escape(v)+'"]');
      if (cb) cb.checked = true;
    }
  };
  checkList(p.hobbies, "hobbies");
  checkList(p.preferences, "preferences");
  checkList(p.dislikes, "dislikes");
  // Alter neu berechnen + Medien laden
  if (typeof ageFromBirthdate === "function" && p.birthdate) {
    const disp = document.getElementById("p-age-display");
    if (disp) { const a = ageFromBirthdate(p.birthdate); disp.value = a ? (a + " Jahre") : "—"; }
  }
  // Medien-Hashes fürs Bild-/Video-Laden bereitstellen und neu rendern.
  window.PARTNER_PROFILE = { image_hashes: p.image_hashes || [], video_hashes: p.video_hashes || [] };
  if (typeof reloadPartnerMedia === "function") reloadPartnerMedia();
}

// === Tabs ===
// openMatchProfile: zeigt das volle Profil eines Matches als Popup — alle
// Bilder, Bio, Hobbys, und einen Kontakt-Button (Messenger via PeerID).
function openMatchProfile(m){
  if(!m) return;
  // Alle Medien (Bilder + Videos) in einem Karussell.
  const imgHashes = m.image_hashes || [];
  const vidHashes = m.video_hashes || [];
  const media = [];
  imgHashes.forEach(function(h){ media.push({type:'img', hash:h}); });
  vidHashes.forEach(function(h){ media.push({type:'vid', hash:h}); });

  const nick = m.nickname || (m.peer_id||'').slice(0,12)+'\u2026';
  const score = Math.round((m.score||0)*100);
  const gMap = { male:'männlich', female:'weiblich', 'non-binary':'nicht-binär', other:'divers' };
  let sub = gMap[m.gender] || (m.gender||'');
  if(m.age) sub += (sub?' \u00b7 ':'')+m.age+(/^\d+$/.test(String(m.age))?' Jahre':'');

  // Alle gefundenen Profildaten in beschrifteten Abschnitten.
  const uniq = function(arr){ const seen={}; const out=[]; (arr||[]).forEach(function(x){ if(x && !seen[x]){ seen[x]=1; out.push(x); } }); return out; };
  // Vorlieben-Abkürzungen → lesbarer Name (aus den Formular-Checkboxen).
  const sexLabel = {};
  document.querySelectorAll('.sex-abbr-cb').forEach(function(cb){
    const ab = (cb.getAttribute('data-abbr')||'').toUpperCase();
    const txt = (cb.parentElement ? cb.parentElement.textContent : '').trim();
    if (ab) sexLabel[ab] = txt || ab;
  });
  const tags = function(list){
    return '<div class="match-tags">'+list.map(function(p){ return '<span class="match-tag">'+escapeHtml(p)+'</span>'; }).join('')+'</div>';
  };
  const section = function(title, inner){
    return inner ? '<div class="mp-sec"><div class="mp-sec-h">'+title+'</div>'+inner+'</div>' : '';
  };
  const hobbiesVals = uniq([].concat(m.hobbies||[], m.matched_hobbies||[], m.preferences||[], m.matched_prefs||[]));
  const sexVals = uniq([].concat(m.sexual_prefs||[], m.matched_sex||[]).map(function(x){ const k=String(x).toUpperCase(); return sexLabel[k] || k; }));
  const eduTxt = m.education || m.education_group || '';
  const jobTxt = m.profession || '';
  const indTxt = m.industry || m.industry_group || '';
  const facts = [];
  if (eduTxt) facts.push('<div><span class="mp-k">Bildung:</span> '+escapeHtml(eduTxt)+'</div>');
  if (jobTxt) facts.push('<div><span class="mp-k">Beruf:</span> '+escapeHtml(jobTxt)+'</div>');
  if (indTxt) facts.push('<div><span class="mp-k">Branche:</span> '+escapeHtml(indTxt)+'</div>');
  const prefsHtml =
    section('Hobbies und Werte', hobbiesVals.length ? tags(hobbiesVals) : '') +
    section('Vorlieben', sexVals.length ? tags(sexVals) : '') +
    section('Ausbildung und Beruf', facts.join(''));

  const ov = document.createElement("div");
  ov.className = "mp-overlay";
  ov.onclick = function(e){ if(e.target===ov) ov.remove(); };

  // Galerie wie in der eigenen Profil-Vorschau: erstes Bild groß (2x2),
  // weitere klein, Videos als Kacheln mit ▶. Klick öffnet groß.
  const card = document.createElement("div");
  card.className = "mp-card";
  const mediaBox = document.createElement("div");
  mediaBox.className = "mp-gallery";
  if (media.length === 0) {
    mediaBox.innerHTML = '<div class="match-photo match-noimg mp-noimg">\ud83d\udc64</div>';
  }
  media.forEach(function(item, i){
    const big = (i === 0);
    const sz = big ? 216 : 104;
    const t = document.createElement("div");
    t.className = "media-tile" + (item.type === 'vid' ? " media-video" : "");
    t.style.cssText = "position:relative;width:"+sz+"px;height:"+sz+"px;cursor:zoom-in";
    if (big) { t.style.gridRow = "span 2"; t.style.gridColumn = "span 2"; }
    const im = document.createElement("img");
    im.style.cssText = "width:"+sz+"px;height:"+sz+"px;object-fit:cover;border-radius:8px;display:block;background:#000";
    im.loading = "lazy";
    t.appendChild(im);
    if (item.type === 'vid') {
      partnerVideoThumbFromHash(item.hash, im);
      const b = document.createElement("span");
      b.textContent = "\u25b6";
      b.style.cssText = "position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);color:#fff;font-size:"+(big?40:26)+"px;text-shadow:0 2px 8px rgba(0,0,0,0.7);pointer-events:none";
      t.appendChild(b);
      t.onclick = function(e){ e.stopPropagation(); openPartnerVideo(item.hash); };
    } else {
      im.src = "/api/v1/files/download/" + item.hash;
      t.onclick = function(e){ e.stopPropagation(); openPartnerLightbox("/api/v1/files/download/" + item.hash); };
    }
    mediaBox.appendChild(t);
  });

  const body = document.createElement("div");
  body.className = "mp-body";
  body.innerHTML =
    '<div class="mp-name">'+escapeHtml(nick)+'</div>'+
    '<div class="mp-sub">'+escapeHtml(sub)+'</div>'+
    '<div class="mp-meta">'+score+'% \u00dcbereinstimmung \u00b7 '+(((m.distance_km>=0)?Math.round(m.distance_km):'–'))+' km \u00b7 '+(m.common_interests||0)+' gemeinsam</div>'+
    (m.bio ? '<div class="mp-bio">'+escapeHtml(decodeMaybe(m.bio))+'</div>' : '')+
    prefsHtml+
    '<button class="btn btn-prim mp-contact">\ud83d\udcac Nachricht senden</button>';

  const closeBtn = document.createElement("button");
  closeBtn.className = "mp-close"; closeBtn.textContent = "\u2715";
  closeBtn.onclick = function(){ ov.remove(); };

  card.appendChild(closeBtn);
  card.appendChild(mediaBox);
  card.appendChild(body);
  ov.appendChild(card);
  // Kontakt-Button verdrahten.
  body.querySelector(".mp-contact").onclick = function(){
    const fid = m.fundus_id || '';
    if (!fid) {
        alert('Dieser Partner hat keine Messenger-Identität hinterlegt (Profil ohne Login erstellt). Kontaktieren nicht möglich.');
        return;
    }
    contactMatch(fid, encodeURIComponent(nick));
  };
  document.body.appendChild(ov);
}
// contactMatch: öffnet den Messenger, um mit dem Match Kontakt aufzunehmen.
function contactMatch(fundusID, nick){
  if(!fundusID){ alert("Keine Kontakt-Identität verfügbar (Profil ohne Wallet-Login erstellt)."); return; }
  // Zum Messenger mit der Wallet-/Chain-Identität (FundusID) des Partners.
  location.href = "/messenger?to=" + encodeURIComponent(fundusID) + "&name=" + nick;
}

function showTab(name) {
    document.querySelectorAll(".tab-panel").forEach(p => p.classList.add("hidden"));
    document.querySelectorAll(".tab-btn").forEach(b => b.classList.remove("active"));
    document.getElementById("tab-" + name).classList.remove("hidden");
    event.target.classList.add("active");
}

// === Erklärungen ein-/ausblenden ===
function toggleExplain(abbr) {
    const el = document.getElementById("ex-" + abbr);
    if (el) el.classList.toggle("hidden");
}

// === Checkbox → Role-Buttons ein-/ausblenden ===
document.querySelectorAll(".sex-abbr-cb").forEach(cb => {
    cb.addEventListener("change", function() {
        const abbr     = this.dataset.abbr;
        const hasRoles = this.dataset.hasRoles === "true";
        const rolesDiv = document.getElementById("roles-" + abbr);
        if (rolesDiv) {
            rolesDiv.classList.toggle("hidden", !this.checked);
            if (!this.checked) {
                rolesDiv.querySelectorAll(".role-btn").forEach(b => b.classList.remove("role-active"));
            }
        }
    });
    // Initialer Zustand
    if (cb.checked) {
        const rolesDiv = document.getElementById("roles-" + cb.dataset.abbr);
        if (rolesDiv) rolesDiv.classList.remove("hidden");
    }
});

// === Rolle setzen ===
function setRole(btn) {
    const abbr = btn.dataset.abbr;
    document.querySelectorAll(`[data-abbr="${abbr}"].role-btn`).forEach(b => b.classList.remove("role-active"));
    btn.classList.add("role-active");
}

// === Custom-Eingaben ===
document.querySelectorAll(".pref-custom-input").forEach(input => {
    input.addEventListener("keydown", function(e) {
        if (e.key !== "Enter") return;
        e.preventDefault();
        const val   = this.value.trim();
        const group = this.dataset.group;
        if (!val) return;
        const grid  = this.closest(".pref-custom").previousElementSibling;
        if (!grid) return;
        const label = document.createElement("label");
        label.className = "pref-check";
        // Für die Suche muss die Checkbox die Klasse 'srch-sex' tragen (getChecked
        // sammelt dort nach Klasse, nicht nach name). Sonst genügt der name.
        const extraCls = group === "srch_sex_custom" ? ' class="srch-sex"' : '';
        label.innerHTML = `<input type="checkbox"${extraCls} name="${group}" value="${val.toLowerCase()}" checked> ${val} <small class="meta">(eigener Wert)</small>`;
        grid.appendChild(label);
        this.value = "";
    });
});

// === Profil sammeln ===
// Partner-Profilbilder: hochladen (FileStore, Redundanz 1 wie beim Marktplatz),
// Vorschau zeigen, Hashes sammeln. Beim Speichern gehen die Hashes ins Profil.
window._partnerImageHashes = window._partnerImageHashes || [];

// hashString: einfacher Hash für die Upload-ID (wie im Marktplatz).
function hashString(s) {
    let h = 0;
    for (let i = 0; i < s.length; i++) { h = ((h << 5) - h + s.charCodeAt(i)) | 0; }
    return h;
}

// resumableUpload: block-basierter, wiederaufnehmbarer Upload mit Redundanz —
// wiederverwendet aus dem Marktplatz. Für Bilder UND Videos.
async function resumableUpload(file, onProgress, redundancy) {
    const BLOCK = 4 * 1024 * 1024;
    const idSeed = file.name + "|" + file.size + "|" + (file.lastModified || 0);
    const uploadId = "u" + Math.abs(hashString(idSeed)).toString(36) + (file.size % 100000).toString(36);
    const report = (p) => { if (onProgress) onProgress(p); };
    const beginRes = await fetch("/api/v1/files/upload/begin", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ upload_id: uploadId, file_name: file.name,
            mime_type: file.type || "application/octet-stream", total_size: file.size, block_size: BLOCK,
            redundancy: redundancy || 0 }),
    });
    if (!beginRes.ok) throw new Error("begin");
    const begin = await beginRes.json();
    const have = new Set(begin.have_blocks || []);
    const totalBlocks = begin.total_blocks;
    for (let i = 0; i < totalBlocks; i++) {
        if (have.has(i)) { report((i+1)/totalBlocks); continue; }
        const slice = file.slice(i*BLOCK, Math.min((i+1)*BLOCK, file.size));
        let ok = false;
        for (let a = 0; a < 3 && !ok; a++) {
            try { const r = await fetch(`/api/v1/files/upload/block?id=${uploadId}&index=${i}`, { method:"POST", body: slice }); ok = r.ok; }
            catch(e){ ok = false; }
            if (!ok) await new Promise(res => setTimeout(res, 800));
        }
        if (!ok) throw new Error("block "+i);
        report((i+1)/totalBlocks);
    }
    const finRes = await fetch(`/api/v1/files/upload/finish?id=${uploadId}`, { method:"POST" });
    if (!finRes.ok) throw new Error("finish");
    for (;;) {
        await new Promise(res => setTimeout(res, 1500));
        let d;
        try { const sr = await fetch(`/api/v1/files/upload/status?id=${uploadId}`); if (!sr.ok) continue; d = await sr.json(); }
        catch(e){ continue; }
        if (d.state === "done") return d.content_hash;
        if (d.state === "error") throw new Error("Verarbeitung: " + (d.error||""));
    }
}


// handlePartnerMedia: nimmt Bilder UND ein Video (Galerie wie im Marktplatz).
window._partnerVideoHashes = window._partnerVideoHashes || [];

// openPartnerVideo: Video im Vollbild-Overlay abspielen (Klick daneben schließt).
function openPartnerVideo(hash) {
    const lb = document.createElement('div');
    lb.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.9);display:flex;align-items:center;justify-content:center;z-index:10000";
    lb.innerHTML = "<video controls autoplay loop playsinline style='max-width:92%;max-height:92%;border-radius:8px' src='/api/v1/files/download/"+hash+"'></video>";
    lb.addEventListener('click', (e) => { if (e.target === lb) lb.remove(); });
    document.body.appendChild(lb);
}

// openPartnerLightbox: Bild per Klick vergrößern (wie Marktplatz).
function openPartnerLightbox(src) {
    let lb = document.getElementById('partner-lightbox');
    if (!lb) {
        lb = document.createElement('div');
        lb.id = 'partner-lightbox';
        lb.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.9);display:flex;align-items:center;justify-content:center;z-index:9999;cursor:zoom-out";
        lb.innerHTML = "<img style='max-width:92%;max-height:92%;border-radius:8px'>";
        lb.addEventListener('click', () => lb.remove());
        document.body.appendChild(lb);
    }
    lb.querySelector('img').src = src;
    document.body.appendChild(lb);
}

// Video-Thumbnail aus einer Datei erzeugen (Frame bei ~1s).
function partnerVideoThumb(file, imgEl) {
    let url;
    try {
        url = URL.createObjectURL(file);
        const v = document.createElement("video");
        v.preload = "metadata"; v.muted = true; v.playsInline = true; v.src = url;
        let done = false;
        const cleanup = () => { if (!done) { done = true; try { URL.revokeObjectURL(url); } catch(e){} } };
        v.addEventListener("loadeddata", () => { try { v.currentTime = Math.min(1, (v.duration||2)/2); } catch(e){ cleanup(); } });
        v.addEventListener("seeked", () => {
            try {
                const c = document.createElement("canvas");
                c.width = 160; c.height = Math.round(160 * (v.videoHeight/v.videoWidth || 0.66));
                c.getContext("2d").drawImage(v, 0, 0, c.width, c.height);
                imgEl.src = c.toDataURL("image/jpeg", 0.7);
            } catch(e) {}
            cleanup();
        }, { once: true });
        v.addEventListener("error", cleanup, { once: true });
        // Sicherheits-Timeout: falls kein Event feuert, URL trotzdem freigeben.
        setTimeout(cleanup, 8000);
    } catch(e) { if (url) { try { URL.revokeObjectURL(url); } catch(e2){} } }
}

// Ein Video-Kachel-Element bauen (Thumbnail, Hover-Vergrößerung, Klick zum Abspielen).
// partnerVideoThumbFromHash: erzeugt ein Thumbnail aus einem bereits hochgeladenen
// Video (per Hash), indem ein verstecktes video-Element einen Frame rendert.
// So bleibt das Thumbnail auch nach einem Reload erhalten (keine Datei mehr da).
function partnerVideoThumbFromHash(hash, imgEl) {
    try {
        const v = document.createElement("video");
        v.preload = "metadata"; v.muted = true; v.playsInline = true; v.crossOrigin = "anonymous";
        v.src = "/api/v1/files/download/" + hash;
        v.addEventListener("loadeddata", () => { try { v.currentTime = Math.min(1, (v.duration||2)/2); } catch(e){} });
        v.addEventListener("seeked", () => {
            try {
                const c = document.createElement("canvas");
                c.width = 160; c.height = Math.round(160 * (v.videoHeight/v.videoWidth || 0.66));
                c.getContext("2d").drawImage(v, 0, 0, c.width, c.height);
                imgEl.src = c.toDataURL("image/jpeg", 0.7);
            } catch(e) {
                // Falls Canvas-Export scheitert (z.B. CORS): Video direkt als Poster nutzen.
                imgEl.style.display = "none";
            }
        }, { once: true });
    } catch(e) {}
}

function partnerVideoTile(hash, thumbFile) {
    const tile = document.createElement("div");
    tile.className = "media-tile media-video";
    tile.style.cssText = "position:relative;width:110px;flex-shrink:0;cursor:pointer;transition:transform 0.2s";
    const img = document.createElement("img");
    img.style.cssText = "width:110px;height:110px;object-fit:cover;border-radius:8px;display:block;background:#000";
    if (thumbFile) {
        // Frisches Video: KEIN blob-Thumbnail (verursachte tote blob-URLs).
        // Schwarzer Platzhalter; nach dem Upload wird die Kachel per Hash ersetzt.
        img.style.background = "#000";
    }
    else if (hash) { partnerVideoThumbFromHash(hash, img); }
    const badge = document.createElement("span");
    badge.textContent = "▶";
    badge.style.cssText = "position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);color:#fff;font-size:26px;text-shadow:0 2px 8px rgba(0,0,0,0.7);pointer-events:none";
    tile.onmouseenter = () => { tile.style.transform = "scale(1.5)"; tile.style.zIndex = "5"; };
    tile.onmouseleave = () => { tile.style.transform = "none"; tile.style.zIndex = "auto"; };
    tile.onclick = () => {
        // Video im Overlay abspielen: Endlosschleife (loop), Hintergrund nur 50% dunkel.
        let lb = document.getElementById('partner-lightbox');
        if (!lb) {
            lb = document.createElement('div'); lb.id='partner-lightbox';
            lb.style.cssText="position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:9999;cursor:zoom-out";
            lb.addEventListener('click', (e) => { if (e.target === lb) lb.remove(); });
            document.body.appendChild(lb);
        }
        lb.style.background = "rgba(0,0,0,0.5)"; // 50% Abdunklung
        lb.innerHTML = "<video controls autoplay loop playsinline style='max-width:92%;max-height:92%;border-radius:8px' src='/api/v1/files/download/"+hash+"'></video>";
        document.body.appendChild(lb);
    };
    const del = document.createElement("button");
    del.textContent = "×"; del.type = "button";
    del.className = "tile-del";
    del.onclick = (e) => { e.stopPropagation(); const i = window._partnerVideoHashes.indexOf(hash); if(i>=0) window._partnerVideoHashes.splice(i,1); tile.remove(); };
    tile.appendChild(img); tile.appendChild(badge); tile.appendChild(del);
    return tile;
}

// handlePartnerMedia: mehrere Bilder UND mehrere Videos (Galerie wie Marktplatz).
async function handlePartnerMedia(files) {
    const prev = document.getElementById("p-image-previews");
    const vidBox = document.getElementById("p-video-preview");
    for (const file of files) {
        if (file.type.startsWith("image/")) {
            const wrap = document.createElement("div");
            wrap.className = "media-tile";
            wrap.style.cssText = "position:relative;width:110px;height:110px;transition:transform 0.2s";
            const img = document.createElement("img");
            // data-URL statt blob-URL: kann nicht "revoked"/"not found" werden.
            const reader = new FileReader();
            reader.onload = (ev) => { img.src = ev.target.result; };
            reader.readAsDataURL(file);
            img.style.cssText = "width:104px;height:104px;object-fit:cover;border-radius:8px;opacity:0.5;display:block;cursor:zoom-in";
            wrap.onmouseenter = () => { wrap.style.transform = "scale(1.5)"; wrap.style.zIndex = "5"; };
            wrap.onmouseleave = () => { wrap.style.transform = "none"; wrap.style.zIndex = "auto"; };
            wrap.appendChild(img); prev.appendChild(wrap);
            try {
                const hash = await resumableUpload(file, null, 1);
                window._partnerImageHashes.push(hash);
                // Nach dem Upload: Vorschau auf den Server-Pfad umstellen.
                img.src = "/api/v1/files/download/" + hash;
                img.style.opacity = "1";
                img.onclick = () => openPartnerLightbox("/api/v1/files/download/" + hash);
                const del = document.createElement("button");
                del.textContent = "×"; del.type = "button";
                del.className = "tile-del";
                del.onclick = (e) => { e.stopPropagation(); const i = window._partnerImageHashes.indexOf(hash); if(i>=0) window._partnerImageHashes.splice(i,1); wrap.remove(); };
                wrap.appendChild(del);
            } catch(e) {
                img.style.border = "2px solid var(--red)";
                img.style.opacity = "1";
                const note = document.createElement("div");
                note.style.cssText = "position:absolute;bottom:0;left:0;right:0;background:rgba(200,0,0,0.85);color:#fff;font-size:9px;padding:2px;text-align:center;border-radius:0 0 8px 8px";
                note.textContent = (e && e.message ? e.message : "Upload fehlgeschlagen").slice(0,40);
                wrap.appendChild(note);
                console.error("Partner-Bild-Upload fehlgeschlagen:", e);
            }
        } else if (file.type.startsWith("video/")) {
            // Mehrere Videos möglich — jedes bekommt eine eigene Kachel.
            const tile = partnerVideoTile("", file);
            const badge = tile.querySelector("span"); if (badge) badge.textContent = "⏳";
            vidBox.appendChild(tile);
            try {
                const hash = await resumableUpload(file, null, 1);
                window._partnerVideoHashes.push(hash);
                // Kachel mit echtem Hash neu aufbauen (Klick spielt dann ab).
                const real = partnerVideoTile(hash, file);
                vidBox.replaceChild(real, tile);
            } catch(e) {
                tile.querySelector("span").textContent = "✗";
            }
        }
    }
}

// Vorhandene Profilbilder beim Laden anzeigen.
function reloadPartnerMedia(){
    let existing = (window.PARTNER_PROFILE && window.PARTNER_PROFILE.image_hashes) || [];
    if (typeof existing === "string") { try { existing = JSON.parse(existing); } catch(e){ existing = []; } }
    if (!Array.isArray(existing)) existing = [];
    window._partnerImageHashes = existing.slice();
    const prev = document.getElementById("p-image-previews");
    if (prev) {
    existing.forEach((hash, idx) => {
        const wrap = document.createElement("div");
        wrap.className = "media-tile";
        const img = document.createElement("img");
        img.src = "/api/v1/files/download/" + hash;
        // Erstes Bild als großes Hauptbild (2x2 Grid-Zellen), weitere klein (1 Zelle).
        // Container exakt so groß wie das Bild → Löschen-× sitzt in der rechten oberen Ecke.
        if (idx === 0) {
            wrap.style.cssText = "position:relative;width:216px;height:216px";
            wrap.style.gridRow = "span 2";
            wrap.style.gridColumn = "span 2";
            img.style.cssText = "width:216px;height:216px;object-fit:cover;border-radius:8px;cursor:zoom-in;display:block";
        } else {
            wrap.style.cssText = "position:relative;width:104px;height:104px";
            img.style.cssText = "width:104px;height:104px;object-fit:cover;border-radius:8px;cursor:zoom-in;display:block";
        }
        img.onclick = () => openPartnerLightbox("/api/v1/files/download/" + hash);
        const del = document.createElement("button");
        del.textContent = "×"; del.type = "button";
        del.className = "tile-del";
        del.onclick = () => {
            const i = window._partnerImageHashes.indexOf(hash);
            if (i >= 0) window._partnerImageHashes.splice(i, 1);
            wrap.remove();
        };
        wrap.appendChild(img); wrap.appendChild(del); prev.appendChild(wrap);
    });
    }
    // Vorhandene Videos laden und als Kacheln anzeigen (mehrere möglich).
    let vhashes = (window.PARTNER_PROFILE && window.PARTNER_PROFILE.video_hashes) || [];
    if (typeof vhashes === "string") { try { vhashes = JSON.parse(vhashes); } catch(e){ vhashes = []; } }
    if (!Array.isArray(vhashes)) vhashes = [];
    if (vhashes.length) {
        window._partnerVideoHashes = vhashes.slice();
        const vidBox = document.getElementById("p-video-preview");
        if (vidBox) {
            vhashes.forEach(h => vidBox.appendChild(partnerVideoTile(h, null)));
        }
    }
}

// ageFromBirthdate berechnet das aktuelle Alter aus einem Geburtsdatum (YYYY-MM-DD).
function ageFromBirthdate(bd) {
    if (!bd) return 0;
    const d = new Date(bd);
    if (isNaN(d)) return 0;
    const now = new Date();
    let age = now.getFullYear() - d.getFullYear();
    const m = now.getMonth() - d.getMonth();
    if (m < 0 || (m === 0 && now.getDate() < d.getDate())) age--;
    return age > 0 && age < 130 ? age : 0;
}
// Live-Anzeige des berechneten Alters bei Änderung des Geburtsdatums.
(function initBirthdate(){
    const bd = document.getElementById("p-birthdate");
    const disp = document.getElementById("p-age-display");
    if (!bd || !disp) return;
    const upd = () => { const a = ageFromBirthdate(bd.value); disp.value = a ? (a + " Jahre") : "—"; };
    bd.addEventListener("change", upd);
    bd.addEventListener("input", upd);
    upd(); // initial
})();

function collectProfile() {
    const getChecked = name => Array.from(document.querySelectorAll(`input[name="${name}"]:checked`)).map(i => i.value);

    // Sexuelle Vorlieben mit Rollen
    const sexPrefs = [];
    document.querySelectorAll(".sex-abbr-cb:checked").forEach(cb => {
        const abbr     = cb.dataset.abbr;
        const roleBtn  = document.querySelector(`#roles-${abbr} .role-btn.role-active`);
        sexPrefs.push({ abbr, role: roleBtn ? roleBtn.dataset.role : "switch" });
    });

    return {
        nickname:    document.getElementById("p-nick").value,
        gender:      document.getElementById("p-gender").value,
        birthdate:   document.getElementById("p-birthdate").value,
        age:         ageFromBirthdate(document.getElementById("p-birthdate").value),
        education:   document.getElementById("p-edu")?.value || "",
        profession:  document.getElementById("p-profession")?.value || "",
        industry:    document.getElementById("p-industry")?.value || "",
        hobbies:     getChecked("hobbies"),
        preferences: getChecked("preferences"),
        dislikes:    getChecked("dislikes"),
        sexual_prefs: sexPrefs,
        bio:         document.getElementById("p-bio").value,
        image_hashes: window._partnerImageHashes || [],
        video_hashes: window._partnerVideoHashes || [],
        seeking: {
            genders:       document.getElementById("p-seek-gender").value.split(",").map(s=>s.trim()).filter(Boolean),
            age_ranges:    getChecked("seek_age"),
            educations:    getChecked("seek_edu"),
            sex_pref_abbrs: getChecked("seek_sex"),
            radius_km:     parseFloat(document.getElementById("p-radius").value) || 50,
        }
    };
}

// === Suche ===
let searchOffset = 0;
const searchLimit = 20;

async function runSearch(offset) {
    offset = offset || 0;
    searchOffset = offset;
    const st = document.getElementById("search-status");
    if (st) st.textContent = PS.searching;

    const getChecked = cls => Array.from(document.querySelectorAll("." + cls + ":checked"))
                                    .map(i => i.value).join(",");

    const params = new URLSearchParams({
        radius_km:  document.getElementById("srch-radius")?.value || "50",
        gender:     document.getElementById("srch-gender-sel")?.value || "",
        age_min:    document.getElementById("srch-age-min")?.value || "",
        age_max:    document.getElementById("srch-age-max")?.value || "",
        education:  getChecked("srch-edu"),
        hobby:      getChecked("srch-hobby"),
        sex:        getChecked("srch-sex"),
        mutual:     document.getElementById("srch-mutual")?.checked ? "true" : "false",
        sort:       document.getElementById("srch-sort")?.value || "score",
        limit:      searchLimit,
        offset:     offset,
    });

    try {
        const resp = await fetch("/api/v1/partner/search?" + params, {credentials:"same-origin"});
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        const data = await resp.json();

        renderSearchResults(data);
        if (st) st.textContent = "";
    } catch(err) {
        if (st) st.innerHTML = `<span class="error">${err.message}</span>`;
    }
}

function renderSearchResults(data) {
    const section  = document.getElementById("search-results-section");
    const grid     = document.getElementById("search-results");
    const countEl  = document.getElementById("search-count");
    const pageEl   = document.getElementById("search-pagination");

    if (!section || !grid) return;
    section.style.display = "block";

    const results = data.results || [];
    countEl.textContent = `(${data.total} gesamt, ${results.length} angezeigt)`;

    if (results.length === 0) {
        grid.innerHTML = "<p class='empty'>" + PS.no_results + "</p>";
        pageEl.innerHTML = "";
        return;
    }

    grid.innerHTML = results.map(r => {
        const score   = Math.round(r.score * 100);
        const hobbies = (r.matched_hobbies || []);
        const nick    = r.nickname || (r.peer_id||"–").slice(0,12)+"…";
        // Großes Profilbild (erstes Bild) oder Platzhalter.
        let imgHtml;
        const imgs = r.imgs || r.image_hashes || [];
        if (imgs.length > 0) {
            imgHtml = `<img src="/api/v1/files/download/${encodeURIComponent(imgs[0])}" class="srch-photo" alt="" loading="lazy">`;
        } else {
            imgHtml = `<div class="srch-photo srch-noimg">👤</div>`;
        }
        const hobbyTags = hobbies.length
            ? `<div class="match-tags">${hobbies.map(h=>`<span class="match-tag">${escapeHtml(h)}</span>`).join("")}</div>`
            : "";
        // Das ganze Result als JSON fürs Profil-Popup mitgeben.
        const pj = JSON.stringify(r).replace(/'/g, "&#39;");
        return `<div class="srch-card" onclick='openMatchProfile(${pj})'>
  ${imgHtml}
  <div class="srch-info">
    <div class="match-name">${escapeHtml(nick)}</div>
    <div class="match-sub">${escapeHtml(r.age ? r.age + " Jahre" : (r.age_range || ""))}</div>
    <div class="match-meta">${score}% · ${r.distance_km?.toFixed(1)} km</div>
    ${hobbyTags}
  </div>
</div>`;
    }).join("");

    // Paginierung
    const totalPages = Math.ceil(data.total / searchLimit);
    const curPage    = Math.floor(searchOffset / searchLimit) + 1;
    let pageHTML = "";
    if (curPage > 1) {
        pageHTML += `<button type="button" class="btn" style="padding:4px 12px"
                             onclick="runSearch(${(curPage-2)*searchLimit})">${PS.btn_prev}</button>`;
    }
    pageHTML += `<span class="meta">${PS.page_label} ${curPage} / ${totalPages}</span>`;
    if (curPage < totalPages) {
        pageHTML += `<button type="button" class="btn" style="padding:4px 12px"
                             onclick="runSearch(${curPage*searchLimit})">${PS.btn_next}</button>`;
    }
    pageEl.innerHTML = pageHTML;
}

function clearSearch() {
    document.querySelectorAll(".srch-age,.srch-edu,.srch-hobby,.srch-sex").forEach(cb => cb.checked = false);
    const gender = document.getElementById("srch-gender"); if (gender) gender.value = "";
    const mutual = document.getElementById("srch-mutual"); if (mutual) mutual.checked = false;
    const section = document.getElementById("search-results-section");
    if (section) section.style.display = "none";
    const st = document.getElementById("search-status"); if (st) st.textContent = "";
}

// === Speichern ===
async function saveProfile(e) {
    e.preventDefault();
    const st = document.getElementById("partner-status");
    st.textContent = PS.saving;
    const resp = await fetch("/api/v1/partner/profile", {credentials:"same-origin",
        method: "PUT",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(collectProfile()),
    });
    if (!resp.ok) {
        st.innerHTML = `<span class="error">${PS.error} ${resp.status}</span>`;
        return;
    }
    // Nach dem Speichern automatisch veröffentlichen (Anzeige-Ad UND Such-Index),
    // damit das Profil sowohl bei Matches als auch in der Suche aktuell ist.
    st.textContent = PS.publishing || "Veröffentliche…";
    try {
        const r1 = await fetch("/api/v1/partner/publish", {method:"POST", credentials:"same-origin"});
        const r2 = await fetch("/api/v1/partner/publish/searchable", {method:"POST", credentials:"same-origin"});
        if (r1.ok && r2.ok) {
            st.innerHTML = `<span class="success">${PS.saved} & veröffentlicht</span>`;
        } else {
            const err = await (r1.ok ? r2 : r1).json().catch(()=>({}));
            st.innerHTML = `<span class="error">Gespeichert, aber Veröffentlichen fehlgeschlagen: ${err.error || ''}</span>`;
        }
    } catch(e) {
        st.innerHTML = `<span class="error">Gespeichert, Veröffentlichen fehlgeschlagen</span>`;
    }
}

// === Publizieren ===
// publishBoth: veröffentlicht das Profil vollständig — Anzeige-Ad UND Such-Index
// in einem Schritt. Ersetzt die früher getrennten Buttons.
async function publishBoth() {
    const st = document.getElementById("partner-status");
    st.textContent = PS.publishing;
    try {
        const r1 = await fetch("/api/v1/partner/publish", {method:"POST", credentials:"same-origin"});
        const r2 = await fetch("/api/v1/partner/publish/searchable", {method:"POST", credentials:"same-origin"});
        if (r1.ok && r2.ok) {
            st.innerHTML = `<span class="success">${PS.published}</span>`;
        } else {
            const err = await (r1.ok ? r2 : r1).json().catch(()=>({}));
            st.innerHTML = `<span class="error">${err.error || PS.error}</span>`;
        }
    } catch(e) {
        st.innerHTML = `<span class="error">${PS.error}</span>`;
    }
}

async function publishAd() {
    const st = document.getElementById("partner-status");
    st.textContent = PS.publishing;
    const resp = await fetch("/api/v1/partner/publish", {method:"POST", credentials:"same-origin"});
    if (resp.ok) {
        st.innerHTML = `<span class="success">${PS.published}</span>`;
    } else {
        const err = await resp.json().catch(()=>({}));
        st.innerHTML = `<span class="error">${err.error || PS.error}</span>`;
    }
}

// publishSearchableAd: veröffentlicht das Profil für die parametrische Suche
// (Klartext-Kategorien). Opt-in, getrennt von der anonymen Anzeige.
async function publishSearchableAd() {
    const st = document.getElementById("partner-status");
    st.textContent = PS.publishing;
    const resp = await fetch("/api/v1/partner/publish/searchable", {method:"POST", credentials:"same-origin"});
    if (resp.ok) {
        st.innerHTML = `<span class="success">${PS.published}</span>`;
    } else {
        const err = await resp.json().catch(()=>({}));
        st.innerHTML = `<span class="error">${err.error || PS.error}</span>`;
    }
}

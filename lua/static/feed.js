// feed.js – Feed (R812): Beiträge erstellen und anzeigen, Bilder/GIFs/Videos,
// Likes und Kommentare. Server: internal/api/feed.go.
(function () {
  "use strict";

  const MAX_BILDER = 6;
  const MAX_BILD_MB = 15;   // GIFs werden NICHT verkleinert, sonst wäre die Animation weg
  const MAX_VIDEO_MB = 24;  // darüber reicht der Abruf über andere Nodes nicht (siehe R774)

  let anhaenge = [];        // { art:'bild'|'video', datei, vorschau, hash }
  let aeltester = 0;
  let laedt = false;

  const $ = (id) => document.getElementById(id);
  const esc = (t) => String(t == null ? '' : t).replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  // ── Zeit ──────────────────────────────────────────────────────────────────
  function wann(ts) {
    const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
    if (s < 60) return 'gerade eben';
    if (s < 3600) return 'vor ' + Math.floor(s / 60) + ' Min.';
    if (s < 86400) return 'vor ' + Math.floor(s / 3600) + ' Std.';
    if (s < 7 * 86400) return 'vor ' + Math.floor(s / 86400) + (Math.floor(s / 86400) === 1 ? ' Tag' : ' Tagen');
    return new Date(ts * 1000).toLocaleDateString('de-DE', { day: 'numeric', month: 'short', year: 'numeric' });
  }

  // ── Text: maskieren, Links anklickbar ─────────────────────────────────────
  function textHtml(t) {
    return esc(t).replace(/(https?:\/\/[^\s<]+[^\s<.,;:!?)\]'"])/g,
      '<a href="$1" target="_blank" rel="noopener noreferrer nofollow">$1</a>');
  }

  // ── Video erkennen ────────────────────────────────────────────────────────
  // YouTube und Vimeo laden ERST nach einem Klick. Ein eingebetteter Player
  // meldet sich sonst schon beim Öffnen des Feeds bei Google bzw. Vimeo –
  // ohne dass jemand das Video sehen wollte. Das ist in Deutschland auch
  // datenschutzrechtlich die übliche Lösung (Zwei-Klick).
  function videoErkennen(url) {
    if (!url) return null;
    let m = url.match(/(?:youtube\.com\/(?:watch\?(?:[^#]*&)?v=|shorts\/|embed\/|live\/)|youtu\.be\/)([A-Za-z0-9_-]{11})/);
    if (m) {
      const t = url.match(/[?&#](?:t|start)=(\d+)/);
      return { art: 'youtube', id: m[1], start: t ? t[1] : '' };
    }
    m = url.match(/vimeo\.com\/(?:video\/)?(\d{5,})/);
    if (m) return { art: 'vimeo', id: m[1] };
    if (/^https:\/\/[^\s]+\.(mp4|webm|ogg|mov)(\?[^\s]*)?$/i.test(url)) return { art: 'datei', url: url };
    return null;
  }
  function ersterVideoLink(text) {
    const links = String(text || '').match(/https?:\/\/[^\s<]+/g) || [];
    for (const l of links) if (videoErkennen(l)) return l;
    return '';
  }
  function videoHtml(p) {
    if (p.video_hash) {
      return '<div class="feed-video datei"><video class="datei" controls playsinline preload="metadata" src="/api/v1/files/download/' +
        encodeURIComponent(p.video_hash) + '"></video></div>';
    }
    const v = videoErkennen(p.video || ersterVideoLink(p.text));
    if (!v) return '';
    if (v.art === 'datei') {
      return '<div class="feed-video datei"><video class="datei" controls playsinline preload="metadata" src="' + esc(v.url) + '"></video></div>';
    }
    const anbieter = v.art === 'youtube' ? 'YouTube' : 'Vimeo';
    return '<div class="feed-video"><button type="button" class="feed-start" data-art="' + v.art + '" data-id="' + esc(v.id) +
      '" data-start="' + esc(v.start || '') + '"><span class="knopf">▶</span><span class="hinweis">Abspielen – lädt das Video von ' +
      anbieter + '</span></button></div>';
  }
  function videoStarten(btn) {
    const art = btn.dataset.art, id = btn.dataset.id, start = btn.dataset.start;
    const src = art === 'youtube'
      ? 'https://www.youtube-nocookie.com/embed/' + encodeURIComponent(id) + '?autoplay=1&playsinline=1&rel=0' + (start ? '&start=' + encodeURIComponent(start) : '')
      : 'https://player.vimeo.com/video/' + encodeURIComponent(id) + '?autoplay=1';
    const f = document.createElement('iframe');
    // YouTube verweigert eingebettete Wiedergabe ohne Herkunftsangabe
    // ("Fehler 153"). strict-origin-when-cross-origin gibt nur die Domain preis.
    // Als Attribut und VOR der Adresse – die Eigenschaft allein kam im Test
    // nicht am Element an.
    f.setAttribute('referrerpolicy', 'strict-origin-when-cross-origin');
    f.setAttribute('allow', 'autoplay; encrypted-media; picture-in-picture; fullscreen');
    f.setAttribute('allowfullscreen', '');
    f.src = src;
    f.title = art === 'youtube' ? 'YouTube-Video' : 'Vimeo-Video';
    btn.replaceWith(f);
  }

  // ── Bilder ────────────────────────────────────────────────────────────────
  function bilderHtml(hashes) {
    if (!hashes || !hashes.length) return '';
    return '<div class="feed-bilder n' + hashes.length + '">' + hashes.map((h) =>
      '<img loading="lazy" decoding="async" alt="" src="/api/v1/files/download/' + encodeURIComponent(h) + '">').join('') + '</div>';
  }
  function lightbox(src) {
    const d = document.createElement('div');
    d.className = 'feed-lightbox';
    d.innerHTML = '<img alt="">';
    d.querySelector('img').src = src;
    d.onclick = () => d.remove();
    document.addEventListener('keydown', function weg(e) { if (e.key === 'Escape') { d.remove(); document.removeEventListener('keydown', weg); } });
    document.body.appendChild(d);
  }

  // ── Avatar ────────────────────────────────────────────────────────────────
  function avatarHtml(a, klein) {
    const init = esc(((a && a.name) || '?').trim().charAt(0).toUpperCase() || '?');
    return '<img class="feed-av" alt="" data-init="' + init + '" src="' + esc(a.avatar) + '">';
  }

  const HERZ = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20s-7.5-4.6-7.5-10.2A4.3 4.3 0 0 1 12 7a4.3 4.3 0 0 1 7.5 2.8C19.5 15.4 12 20 12 20z"/></svg>';
  const HERZ_VOLL = HERZ.replace('fill="none"', 'fill="currentColor"');
  const BLASE = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20.5 11.5a8 8 0 0 1-11.7 7.1L4 20l1.3-4.4A8 8 0 1 1 20.5 11.5z"/></svg>';
  const WELT = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a13.5 13.5 0 0 1 0 18M12 3a13.5 13.5 0 0 0 0 18"/></svg>';
  const LEUTE = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="9" cy="8" r="3.2"/><path d="M3 19.5c.6-3.3 3-5 6-5s5.4 1.7 6 5"/><circle cx="17" cy="9" r="2.6"/><path d="M16.5 14.6c2.4.2 4 1.7 4.5 4.4"/></svg>';

  // ── Beitrag darstellen ────────────────────────────────────────────────────
  function beitragHtml(p) {
    const sicht = p.visibility === 'contacts'
      ? '<span title="Nur für Kontakte sichtbar">' + LEUTE + '</span>'
      : '<span title="Öffentlich">' + WELT + '</span>';
    // Ein YouTube-Link, der als Video eingebettet wird, steht nicht doppelt im Text.
    let text = p.text || '';
    const eingebettet = !p.video && !p.video_hash ? ersterVideoLink(text) : '';
    if (eingebettet && text.trim() === eingebettet) text = '';
    return '<article class="feed-card feed-beitrag" data-id="' + esc(p.id) + '">' +
      '<div class="feed-kopf">' + avatarHtml(p.author) +
        '<div><div class="feed-name">' + esc(p.author.name || 'Unbekannt') + '</div>' +
        '<div class="feed-meta">' + wann(p.ts) + ' · ' + sicht + '</div></div>' +
        (p.mine ? '<button type="button" class="feed-menu" data-akt="loeschen" title="Beitrag löschen" aria-label="Beitrag löschen">🗑</button>' : '') +
      '</div>' +
      (text ? '<div class="feed-text">' + textHtml(text) + '</div>' : '') +
      bilderHtml(p.images) + videoHtml(p) +
      '<div class="feed-aktionen">' +
        '<button type="button" class="feed-akt' + (p.liked ? ' an' : '') + '" data-akt="like" aria-pressed="' + !!p.liked + '">' +
          (p.liked ? HERZ_VOLL : HERZ) + '<span class="zahl">' + (p.likes || '') + '</span></button>' +
        '<button type="button" class="feed-akt" data-akt="kommentare">' + BLASE +
          '<span class="zahl">' + (p.comments || '') + '</span> Kommentare</button>' +
      '</div>' +
      '<div class="feed-komm" style="display:none"></div>' +
      '</article>';
  }

  // ── Laden ─────────────────────────────────────────────────────────────────
  async function laden(neu) {
    if (laedt) return;
    laedt = true;
    const liste = $('feed-liste');
    try {
      const r = await fetch('/api/v1/feed' + (!neu && aeltester ? '?before=' + aeltester : ''), { credentials: 'same-origin' });
      const d = await r.json();
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      const posts = d.posts || [];
      if (neu) liste.innerHTML = '';
      if (neu && !posts.length) {
        liste.innerHTML = '<div class="feed-card feed-leer">' + (window.FEED_ICON || '') +
          '<p>Noch keine Beiträge. Schreib den ersten!</p></div>';
      }
      posts.forEach((p) => {
        liste.insertAdjacentHTML('beforeend', beitragHtml(p));
        aeltester = p.ts;
      });
      $('feed-mehr').style.display = posts.length >= 20 ? '' : 'none';
    } catch (e) {
      if (neu) liste.innerHTML = '<div class="feed-card feed-leer">✗ ' + esc(e.message) + '</div>';
    } finally { laedt = false; }
  }

  // ── Aktionen am Beitrag ───────────────────────────────────────────────────
  async function api(methode, pfad, daten) {
    const r = await fetch(pfad, {
      method: methode, credentials: 'same-origin',
      headers: daten ? { 'Content-Type': 'application/json' } : {},
      body: daten ? JSON.stringify(daten) : undefined
    });
    let d = {};
    try { d = await r.json(); } catch (e) {}
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    return d;
  }

  async function liken(art, btn) {
    const an = btn.getAttribute('aria-pressed') !== 'true';
    const zahl = btn.querySelector('.zahl');
    const n = (parseInt(zahl.textContent, 10) || 0) + (an ? 1 : -1);
    // Sofort anzeigen, bei Fehler zurücknehmen.
    function setzen(zustand, anzahl) {
      btn.setAttribute('aria-pressed', zustand);
      btn.classList.toggle('an', zustand);
      btn.innerHTML = (zustand ? HERZ_VOLL : HERZ) + '<span class="zahl">' + (anzahl > 0 ? anzahl : '') + '</span>';
    }
    setzen(an, n);
    try { await api('POST', '/api/v1/feed/' + encodeURIComponent(art.dataset.id) + '/like', { like: an }); }
    catch (e) { setzen(!an, n + (an ? -1 : 1)); alert('✗ ' + e.message); }
  }

  async function kommentareZeigen(art, erneut) {
    const box = art.querySelector('.feed-komm');
    if (!erneut && box.style.display !== 'none') { box.style.display = 'none'; return; }
    box.style.display = '';
    box.innerHTML = '<div class="feed-meta">Lade …</div>';
    try {
      const d = await api('GET', '/api/v1/feed/' + encodeURIComponent(art.dataset.id) + '/comments');
      box.innerHTML = (d.comments || []).map((k) =>
        '<div class="feed-k" data-kid="' + esc(k.id) + '">' + avatarHtml(k.author) +
          '<div class="feed-kblase"><span class="feed-name">' + esc(k.author.name || 'Unbekannt') + '</span>' +
          '<div class="feed-text">' + textHtml(k.text) + '</div>' +
          '<div class="feed-meta">' + wann(k.ts) +
            (k.mine ? ' · <a href="#" data-akt="kloeschen">löschen</a>' : '') + '</div></div></div>').join('') +
        '<div class="feed-keingabe"><input type="text" maxlength="1500" placeholder="Kommentieren …" aria-label="Kommentar">' +
        '<button type="button" class="btn btn-buy" data-akt="ksenden" style="width:auto;padding:6px 14px">Senden</button></div>';
      const zahl = art.querySelector('[data-akt="kommentare"] .zahl');
      zahl.textContent = (d.comments || []).length || '';
      const eingabe = box.querySelector('input');
      eingabe.addEventListener('keydown', (e) => { if (e.key === 'Enter') kommentieren(art); });
      if (erneut) eingabe.focus();
    } catch (e) { box.innerHTML = '<div class="feed-meta">✗ ' + esc(e.message) + '</div>'; }
  }

  async function kommentieren(art) {
    const eingabe = art.querySelector('.feed-keingabe input');
    const text = (eingabe.value || '').trim();
    if (!text) return;
    eingabe.disabled = true;
    try {
      await api('POST', '/api/v1/feed/' + encodeURIComponent(art.dataset.id) + '/comments', { text: text });
      await kommentareZeigen(art, true);
    } catch (e) { alert('✗ ' + e.message); eingabe.disabled = false; }
  }

  async function loeschen(art, kommentarId) {
    if (!confirm(kommentarId ? 'Kommentar löschen?' : 'Beitrag löschen?')) return;
    try {
      await api('DELETE', '/api/v1/feed/' + encodeURIComponent(art.dataset.id) +
        (kommentarId ? '?kommentar=' + encodeURIComponent(kommentarId) : ''));
      if (kommentarId) kommentareZeigen(art, true); else art.remove();
    } catch (e) { alert('✗ ' + e.message); }
  }

  $('feed-liste').addEventListener('click', (e) => {
    const start = e.target.closest('.feed-start');
    if (start) { videoStarten(start); return; }
    const img = e.target.closest('.feed-bilder img');
    if (img) { lightbox(img.src); return; }
    const t = e.target.closest('[data-akt]');
    if (!t) return;
    const art = t.closest('.feed-beitrag');
    switch (t.dataset.akt) {
      case 'like': liken(art, t); break;
      case 'kommentare': kommentareZeigen(art, false); break;
      case 'ksenden': kommentieren(art); break;
      case 'loeschen': loeschen(art, ''); break;
      case 'kloeschen': e.preventDefault(); loeschen(art, t.closest('.feed-k').dataset.kid); break;
    }
  });
  $('feed-mehr').onclick = () => laden(false);
  // Fehlendes Profilbild: Anfangsbuchstaben zeigen. Ein Fehler-Ereignis steigt
  // nicht auf, deshalb in der Einfangphase abhören.
  document.addEventListener('error', (e) => {
    const img = e.target;
    if (!img || img.tagName !== 'IMG' || !img.classList.contains('feed-av')) return;
    const d = document.createElement('div');
    d.className = 'feed-av';
    d.textContent = img.dataset.init || '?';
    img.replaceWith(d);
  }, true);

  // ── Anhänge ───────────────────────────────────────────────────────────────
  function vorschauZeichnen() {
    const box = $('feed-vorschau');
    box.style.display = anhaenge.length ? '' : 'none';
    box.innerHTML = '';
    anhaenge.forEach((a, i) => {
      const d = document.createElement('div');
      d.innerHTML = (a.art === 'video' ? '<video muted playsinline></video>' : '<img alt="">') +
        '<button type="button" aria-label="Entfernen">×</button>';
      d.firstChild.src = a.vorschau;
      d.querySelector('button').onclick = () => { URL.revokeObjectURL(a.vorschau); anhaenge.splice(i, 1); vorschauZeichnen(); };
      box.appendChild(d);
    });
  }
  function status(t) { $('feed-status').textContent = t || ''; }

  $('feed-bilder-datei').addEventListener('change', (e) => {
    for (const f of Array.from(e.target.files || [])) {
      if (anhaenge.filter((a) => a.art === 'bild').length >= MAX_BILDER) { status('Höchstens ' + MAX_BILDER + ' Bilder je Beitrag.'); break; }
      if (!/^image\//.test(f.type)) continue;
      if (f.size > MAX_BILD_MB * 1048576) { status(f.name + ': größer als ' + MAX_BILD_MB + ' MB.'); continue; }
      anhaenge.push({ art: 'bild', datei: f, vorschau: URL.createObjectURL(f) });
    }
    e.target.value = '';
    vorschauZeichnen();
  });
  $('feed-video-datei').addEventListener('change', (e) => {
    const f = (e.target.files || [])[0];
    e.target.value = '';
    if (!f) return;
    if (f.size > MAX_VIDEO_MB * 1048576) {
      status('Video größer als ' + MAX_VIDEO_MB + ' MB – bitte als Link teilen (z. B. YouTube).');
      return;
    }
    anhaenge = anhaenge.filter((a) => a.art !== 'video');
    anhaenge.push({ art: 'video', datei: f, vorschau: URL.createObjectURL(f) });
    $('feed-video-url').value = '';
    $('feed-video-box').style.display = 'none';
    vorschauZeichnen();
  });
  window.feedVideoUmschalten = function () {
    const box = $('feed-video-box');
    const zu = box.style.display === 'none';
    box.style.display = zu ? '' : 'none';
    if (zu) $('feed-video-url').focus();
  };


  // Fotos auf 1600 px verkleinern (spart Speicher im Netz). GIFs NICHT – das
  // Zeichnen auf ein Canvas behielte nur das erste Bild der Animation.
  function verkleinern(f) {
    if (f.type === 'image/gif' || f.type === 'image/svg+xml') return Promise.resolve(f);
    return new Promise((ok) => {
      const img = new Image();
      img.onload = () => {
        const m = Math.max(img.naturalWidth, img.naturalHeight);
        if (m <= 1600) { URL.revokeObjectURL(img.src); ok(f); return; }
        const sk = 1600 / m, c = document.createElement('canvas');
        c.width = Math.round(img.naturalWidth * sk); c.height = Math.round(img.naturalHeight * sk);
        c.getContext('2d').drawImage(img, 0, 0, c.width, c.height);
        URL.revokeObjectURL(img.src);
        c.toBlob((b) => ok(b ? new File([b], f.name.replace(/\.\w+$/, '') + '.jpg', { type: 'image/jpeg' }) : f), 'image/jpeg', 0.85);
      };
      img.onerror = () => ok(f);
      img.src = URL.createObjectURL(f);
    });
  }

  // Hochladen wie bei Inseraten (begin → Blöcke → finish → Status), ohne die
  // inseratsspezifische upload.js mitzuladen.
  async function hochladen(f, fortschritt) {
    const BLOCK = 4 * 1024 * 1024;
    const id = 'f' + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
    let r = await fetch('/api/v1/files/upload/begin', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ upload_id: id, file_name: f.name || 'datei', mime_type: f.type || 'application/octet-stream',
        total_size: f.size, block_size: BLOCK, redundancy: 0 })
    });
    if (!r.ok) throw new Error('Hochladen nicht möglich (' + r.status + ')');
    const b = await r.json();
    const n = b.total_blocks, vorhanden = new Set(b.have_blocks || []);
    for (let i = 0; i < n; i++) {
      if (!vorhanden.has(i)) {
        let ok = false;
        for (let v = 0; v < 3 && !ok; v++) {
          try {
            ok = (await fetch('/api/v1/files/upload/block?id=' + id + '&index=' + i,
              { method: 'POST', credentials: 'same-origin', body: f.slice(i * BLOCK, Math.min((i + 1) * BLOCK, f.size)) })).ok;
          } catch (e) { ok = false; }
          if (!ok) await new Promise((res) => setTimeout(res, 800));
        }
        if (!ok) throw new Error('Hochladen abgebrochen');
      }
      fortschritt((i + 1) / n);
    }
    r = await fetch('/api/v1/files/upload/finish?id=' + id, { method: 'POST', credentials: 'same-origin' });
    if (!r.ok) throw new Error('Hochladen nicht abgeschlossen');
    for (let i = 0; i < 200; i++) {
      await new Promise((res) => setTimeout(res, 1200));
      try {
        const s = await (await fetch('/api/v1/files/upload/status?id=' + id, { credentials: 'same-origin' })).json();
        if (s.state === 'done') return s.content_hash;
        if (s.state === 'error') throw new Error(s.error || 'Verarbeitung fehlgeschlagen');
      } catch (e) { if (/Verarbeitung|fehlgeschlagen/.test(e.message)) throw e; }
    }
    throw new Error('Zeitüberschreitung beim Hochladen');
  }

  // ── Teilen ────────────────────────────────────────────────────────────────
  $('feed-senden').onclick = async function () {
    const btn = this;
    const text = $('feed-text').value.trim();
    const videoUrl = $('feed-video-url').value.trim();
    if (!text && !anhaenge.length && !videoUrl) { status('Schreib etwas oder hänge ein Bild oder Video an.'); return; }
    if (videoUrl && !videoErkennen(videoUrl)) {
      status('Dieser Video-Link wird nicht erkannt. Unterstützt: YouTube, Vimeo und direkte .mp4/.webm-Adressen (https).');
      return;
    }
    btn.disabled = true;
    try {
      const bilder = [];
      let videoHash = '';
      for (let i = 0; i < anhaenge.length; i++) {
        const a = anhaenge[i];
        const f = a.art === 'bild' ? await verkleinern(a.datei) : a.datei;
        const hash = await hochladen(f, (p) => status('Lade hoch ' + (i + 1) + '/' + anhaenge.length + ' … ' + Math.round(p * 100) + ' %'));
        if (a.art === 'bild') bilder.push(hash); else videoHash = hash;
      }
      status('Wird geteilt …');
      const d = await api('POST', '/api/v1/feed', {
        text: text, images: bilder, video: videoUrl, video_hash: videoHash, visibility: $('feed-sicht').value
      });
      $('feed-text').value = ''; $('feed-video-url').value = ''; $('feed-video-box').style.display = 'none';
      anhaenge.forEach((a) => URL.revokeObjectURL(a.vorschau));
      anhaenge = [];
      vorschauZeichnen();
      status($('feed-sicht').value === 'contacts'
        ? (d.contacts ? '✓ Geteilt mit ' + d.contacts + (d.contacts === 1 ? ' Kontakt.' : ' Kontakten.')
                      : '✓ Geteilt – aber du hast noch keine Kontakte mit bekanntem Schlüssel; außer dir sieht ihn niemand.')
        : '✓ Geteilt.');
      aeltester = 0;
      await laden(true);
    } catch (e) {
      status('✗ ' + e.message);
    } finally { btn.disabled = false; }
  };

  // Auswahl der Sichtbarkeit merken – wer nur für Kontakte schreibt, will das
  // nicht jedes Mal neu einstellen.
  try {
    const gemerkt = localStorage.getItem('fundus_feed_sicht');
    if (gemerkt) $('feed-sicht').value = gemerkt;
  } catch (e) {}
  $('feed-sicht').addEventListener('change', (e) => { try { localStorage.setItem('fundus_feed_sicht', e.target.value); } catch (x) {} });

  laden(true);
  // Beim Zurückkehren in den Tab frisch laden, wenn nichts eingegeben wird.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && !$('feed-text').value && !anhaenge.length) { aeltester = 0; laden(true); }
  });
})();

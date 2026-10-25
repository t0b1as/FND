// feed.js – Feed (R812): Beiträge erstellen und anzeigen, Bilder/GIFs/Videos,
// Likes und Kommentare. Server: internal/api/feed.go.
(function () {
  "use strict";

  const MAX_BILDER = 6;
  const MAX_VIDEOS = 4;
  const MAX_BILD_MB = 15;   // GIFs werden NICHT verkleinert, sonst wäre die Animation weg
  const MAX_VIDEO_MB = 24;  // darüber reicht der Abruf über andere Nodes nicht (siehe R774)

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
  // R815: Hochgeladene Videos laufen in Schleife (loop) – wie man es von
  // kurzen Clips in sozialen Netzen kennt. Ton und Start bleiben beim Nutzer.
  function videosHtml(liste) {
    if (!liste || !liste.length) return '';
    return '<div class="feed-videos">' + liste.map((h) =>
      '<video controls loop playsinline preload="metadata" src="/api/v1/files/download/' + encodeURIComponent(h) + '"></video>').join('') + '</div>';
  }
  function videoHtml(p) {
    const link = p.link && p.link.url && videoErkennen(p.link.url) ? p.link : null;
    const v = videoErkennen(p.video || (link && link.url) || (!p.link ? ersterVideoLink(p.text) : ''));
    if (!v) return '';
    if (v.art === 'datei') {
      return '<div class="feed-video datei"><video class="datei" controls playsinline preload="metadata" src="' + esc(v.url) + '"></video></div>';
    }
    const anbieter = v.art === 'youtube' ? 'YouTube' : 'Vimeo';
    // R814: Vorschaubild und Titel kommen aus der beim Erstellen geholten
    // Vorschau – aus dem eigenen Datei-Speicher, nicht von YouTube.
    const bild = link && link.image ? ' mit-bild" style="background-image:url(/api/v1/files/download/' + encodeURIComponent(link.image) + ')' : '';
    return '<div class="feed-video"><button type="button" class="feed-start' + bild + '" data-art="' + v.art + '" data-id="' + esc(v.id) +
      '" data-start="' + esc(v.start || '') + '"><span class="knopf">▶</span>' +
      (link && link.title ? '<span class="titel">' + esc(link.title) + '</span>' : '') +
      '<span class="hinweis">Abspielen – lädt das Video von ' + anbieter + '</span></button></div>';
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

  // ── Linkvorschau (R814) ───────────────────────────────────────────────────
  function linkKarteHtml(l) {
    if (!l || !l.url || videoErkennen(l.url)) return '';
    if (!l.title && !l.image) {
      return '<a class="feed-linkkarte" href="' + esc(l.url) + '" target="_blank" rel="noopener noreferrer nofollow">' +
        '<div class="lk-text"><div class="lk-seite">' + esc(l.site || '') + '</div><div class="lk-titel">' + esc(l.url) + '</div></div></a>';
    }
    return '<a class="feed-linkkarte" href="' + esc(l.url) + '" target="_blank" rel="noopener noreferrer nofollow">' +
      (l.image ? '<img loading="lazy" alt="" src="/api/v1/files/download/' + encodeURIComponent(l.image) + '">' : '') +
      '<div class="lk-text">' + (l.site ? '<div class="lk-seite">' + esc(l.site) + '</div>' : '') +
      (l.title ? '<div class="lk-titel">' + esc(l.title) + '</div>' : '') +
      (l.description ? '<div class="lk-besch">' + esc(l.description) + '</div>' : '') + '</div></a>';
  }
  // Die Adresse, zu der es eine Vorschau gibt, steht nicht noch einmal im
  // Text – sichtbar ist sie nur beim Schreiben.
  function ohneLink(text, url) {
    if (!url) return text;
    return text.split(url).join('').replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
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
  const TEILEN = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 15V3.5"/><path d="m7.5 8 4.5-4.5L16.5 8"/><path d="M5 13v5.5c0 .8.7 1.5 1.5 1.5h11c.8 0 1.5-.7 1.5-1.5V13"/></svg>';
  // Basisadresse für geteilte Links: die öffentliche Adresse, nicht die des
  // Nodes, auf dem man gerade ist – sonst teilte man eine 10.10.11.x-Adresse.
  const BASIS = 'https://fnd.resolve.bar';
  const WELT = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a13.5 13.5 0 0 1 0 18M12 3a13.5 13.5 0 0 0 0 18"/></svg>';
  const LEUTE = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="9" cy="8" r="3.2"/><path d="M3 19.5c.6-3.3 3-5 6-5s5.4 1.7 6 5"/><circle cx="17" cy="9" r="2.6"/><path d="M16.5 14.6c2.4.2 4 1.7 4.5 4.4"/></svg>';

  // ── Beitrag darstellen ────────────────────────────────────────────────────
  const beitraege = {};   // id → Beitrag, für das Bearbeiten
  function beitragHtml(p) {
    beitraege[p.id] = p;
    const sicht = p.visibility === 'group'
      ? '<span title="Nur für die Gruppe ' + esc(p.group_name || '') + '">' + LEUTE + ' ' + esc(p.group_name || 'Gruppe') + '</span>'
      : p.visibility === 'contacts'
        ? '<span title="Nur für Kontakte sichtbar">' + LEUTE + '</span>'
        : '<span title="Öffentlich">' + WELT + '</span>';
    // Ein YouTube-Link, der als Video eingebettet wird, steht nicht doppelt im Text.
    let text = p.text || '';
    if (p.link && p.link.url) {
      text = ohneLink(text, p.link.url);
    } else {
      // Beiträge vor R814 ohne gespeicherte Vorschau
      const eingebettet = !p.video && !p.video_hash ? ersterVideoLink(text) : '';
      if (eingebettet && text.trim() === eingebettet) text = '';
    }
    return '<article class="feed-card feed-beitrag" data-id="' + esc(p.id) + '">' +
      '<div class="feed-kopf">' + avatarHtml(p.author) +
        '<div><div class="feed-name">' + esc(p.author.name || 'Unbekannt') + '</div>' +
        '<div class="feed-meta">' + wann(p.ts) + (p.edited ? ' <span class="feed-bearb">· bearbeitet</span>' : '') + ' · ' + sicht + '</div></div>' +
        (p.mine ? '<button type="button" class="feed-menu" data-akt="bearbeiten" title="Beitrag bearbeiten" aria-label="Beitrag bearbeiten" style="margin-left:auto">✎</button>' +
                  '<button type="button" class="feed-menu" data-akt="loeschen" title="Beitrag löschen" aria-label="Beitrag löschen" style="margin-left:0">🗑</button>' : '') +
      '</div>' +
      (text ? '<div class="feed-text">' + textHtml(text) + '</div>' : '') +
      bilderHtml(p.images) + videosHtml(p.videos) + videoHtml(p) + linkKarteHtml(p.link) +
      '<div class="feed-aktionen">' +
        '<button type="button" class="feed-akt' + (p.liked ? ' an' : '') + '" data-akt="like" aria-pressed="' + !!p.liked + '">' +
          (p.liked ? HERZ_VOLL : HERZ) + '<span class="zahl">' + (p.likes || '') + '</span></button>' +
        '<button type="button" class="feed-akt" data-akt="kommentare">' + BLASE +
          '<span class="zahl">' + (p.comments || '') + '</span> Kommentare</button>' +
        '<button type="button" class="feed-akt" data-akt="teilen" data-sicht="' + esc(p.visibility) + '" style="margin-left:auto" aria-label="Beitrag teilen">' +
          TEILEN + ' Teilen</button>' +
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
          '<div class="feed-meta">' + wann(k.ts) + (k.edited ? ' <span class="feed-bearb">· bearbeitet</span>' : '') +
            (k.mine ? ' · <a href="#" data-akt="kbearbeiten">bearbeiten</a> · <a href="#" data-akt="kloeschen">löschen</a>' : '') + '</div></div></div>').join('') +
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

  // R815: Beitrag bearbeiten – Text, Medien, Linkvorschau und Reichweite.
  function beitragBearbeiten(art) {
    const p = beitraege[art.dataset.id];
    if (!p) return;
    const d = dialog('Beitrag bearbeiten',
      '<textarea id="fb-text" maxlength="5000" aria-label="Text"></textarea>' +
      '<div id="fb-link"></div>' +
      '<div id="fb-medien" class="feed-vorschau" style="display:none"></div>' +
      '<div class="feed-row" style="margin-top:0">' +
        '<button type="button" class="feed-tool feed-plus" id="fb-plus">+ Media</button>' +
        '<input type="file" id="fb-datei" accept="image/*,video/mp4,video/webm,video/quicktime" multiple style="display:none">' +
        '<span class="grow"></span>' +
        '<select id="fb-sicht" class="feed-sicht" aria-label="Reichweite">' + reichweitenHtml() + '</select>' +
      '</div>' +
      '<div class="feed-status" id="fb-status"></div>',
      '<button type="button" class="feed-tool" data-zu>Abbrechen</button><button type="button" class="feed-teilen" id="fb-ok">Speichern</button>');
    const ta = d.querySelector('#fb-text');
    ta.value = p.text || '';
    const st = (t) => { d.querySelector('#fb-status').textContent = t || ''; };
    const m = medienListe(d.querySelector('#fb-medien'), st);
    m.vorhandene(p.images, p.videos);
    d.querySelector('#fb-plus').onclick = () => d.querySelector('#fb-datei').click();
    d.querySelector('#fb-datei').addEventListener('change', (e) => { m.dateien(e.target.files); e.target.value = ''; });
    const sel = d.querySelector('#fb-sicht');
    sel.value = p.visibility === 'group' && p.group_id ? 'group:' + p.group_id : (p.visibility || 'public');
    if (!sel.value) sel.value = 'public';   // Gruppe inzwischen gelöscht

    // Linkvorschau wie beim Schreiben
    let lv = p.link || null, lvFuer = lv ? lv.url : '', lvWeg = '', lvNr = 0, lvT = null;
    const lvBox = d.querySelector('#fb-link');
    function lvZeigen() {
      if (!lv) { lvBox.innerHTML = ''; return; }
      lvBox.innerHTML = '<div style="position:relative">' + (videoErkennen(lv.url) ? videoHtml({ link: lv, text: '' }) : linkKarteHtml(lv)) +
        '<button type="button" class="feed-vorschau-zu" aria-label="Vorschau entfernen">✕</button></div>';
      lvBox.querySelector('.feed-vorschau-zu').onclick = () => { lvWeg = lv.url; lv = null; lvZeigen(); };
      lvBox.querySelectorAll('a, .feed-start').forEach((el) => el.addEventListener('click', (e) => e.preventDefault()));
    }
    async function lvPruefen() {
      const url = ersterLink(ta.value);
      if (!url) { lv = null; lvFuer = ''; lvZeigen(); return; }
      if (url === lvFuer || url === lvWeg) return;
      lvFuer = url;
      const nr = ++lvNr;
      lvBox.innerHTML = '<div class="feed-lade">Vorschau wird geladen …</div>';
      try { const r = await api('POST', '/api/v1/feed/preview', { url: url }); if (nr === lvNr) lv = r.link && r.link.url ? r.link : { url: url }; }
      catch (e) { if (nr === lvNr) lv = { url: url }; }
      if (nr === lvNr) lvZeigen();
    }
    lvZeigen();
    ta.addEventListener('input', () => { clearTimeout(lvT); lvT = setTimeout(lvPruefen, 700); });
    ta.addEventListener('paste', () => { clearTimeout(lvT); lvT = setTimeout(lvPruefen, 50); });
    ta.focus();

    d.querySelector('#fb-ok').onclick = async function () {
      const knopf = this;
      const text = ta.value.trim();
      if (!text && !m.eintraege.length) { st('Der Beitrag wäre leer – zum Entfernen bitte löschen.'); return; }
      const vorher = p.visibility === 'group' && p.group_id ? 'group:' + p.group_id : p.visibility;
      if (sel.value !== vorher && vorher === 'public' &&
          !confirm('Der Beitrag war öffentlich. Wer ihn bisher gesehen hat, kann sich den alten Stand gemerkt haben – das lässt sich nicht zurückholen. Trotzdem einschränken?')) return;
      knopf.disabled = true;
      try {
        const md = await m.hochladenAlle(st);
        st('Wird gespeichert …');
        const r = await api('PUT', '/api/v1/feed/' + encodeURIComponent(p.id), {
          text: text, images: md.bilder, videos: md.videos, visibility: sel.value,
          link: lv && text.indexOf(lv.url) >= 0 ? lv : undefined
        });
        // Beitrag frisch vom Server holen und ersetzen
        const n = await api('GET', '/api/v1/feed?id=' + encodeURIComponent(p.id));
        const neuP = (n.posts || [])[0];
        if (neuP) {
          art.insertAdjacentHTML('afterend', beitragHtml(neuP));
          art.remove();
        }
        d.remove();
        hinweis(sel.value === 'public' ? 'Gespeichert'
          : 'Gespeichert – sichtbar für ' + r.contacts + (r.contacts === 1 ? ' Person' : ' Personen'));
      } catch (e) { st('✗ ' + e.message); knopf.disabled = false; }
    };
  }

  // R815: Kommentar an Ort und Stelle bearbeiten.
  function kommentarBearbeiten(art, k) {
    const blase = k.querySelector('.feed-kblase');
    if (blase.querySelector('.feed-kbearb')) return;
    const textEl = blase.querySelector('.feed-text');
    const alt = textEl.textContent;
    const box = document.createElement('div');
    box.className = 'feed-kbearb';
    box.innerHTML = '<textarea maxlength="1500" aria-label="Kommentar bearbeiten"></textarea><div class="zeile">' +
      '<button type="button" data-k="ab">Abbrechen</button><button type="button" class="ok" data-k="ok">Speichern</button></div>';
    box.querySelector('textarea').value = alt;
    textEl.style.display = 'none';
    textEl.after(box);
    const ta = box.querySelector('textarea');
    ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length);
    box.addEventListener('click', async (e) => {
      const b = e.target.closest('[data-k]');
      if (!b) return;
      if (b.dataset.k === 'ab') { box.remove(); textEl.style.display = ''; return; }
      const neu = ta.value.trim();
      if (!neu || neu === alt) { box.remove(); textEl.style.display = ''; return; }
      b.disabled = true;
      try {
        await api('PUT', '/api/v1/feed/' + encodeURIComponent(art.dataset.id) + '/comments/' + encodeURIComponent(k.dataset.kid), { text: neu });
        await kommentareZeigen(art, true);
      } catch (x) { alert('✗ ' + x.message); b.disabled = false; }
    });
  }

  async function loeschen(art, kommentarId) {
    if (!confirm(kommentarId ? 'Kommentar löschen?' : 'Beitrag löschen?')) return;
    try {
      await api('DELETE', '/api/v1/feed/' + encodeURIComponent(art.dataset.id) +
        (kommentarId ? '?kommentar=' + encodeURIComponent(kommentarId) : ''));
      if (kommentarId) kommentareZeigen(art, true); else art.remove();
    } catch (e) { alert('✗ ' + e.message); }
  }

  function hinweis(t) {
    const d = document.createElement('div');
    d.className = 'feed-toast'; d.textContent = t; d.setAttribute('role', 'status');
    document.body.appendChild(d);
    setTimeout(() => d.remove(), 2600);
  }
  // R813: Link auf einen Beitrag. Teilen-Menü des Telefons, wo vorhanden;
  // sonst in die Zwischenablage.
  async function teilen(art, sicht) {
    const url = BASIS + '/feed?p=' + encodeURIComponent(art.dataset.id.replace(/^post:/, ''));
    const textEl = art.querySelector('.feed-text');
    const titel = (art.querySelector('.feed-name') || {}).textContent || 'Fundus';
    if (sicht !== 'public' && !confirm('Dieser Beitrag ist nicht öffentlich. Der Link öffnet ihn nur für Personen, die ihn ohnehin sehen dürfen. Trotzdem teilen?')) return;
    if (navigator.share) {
      try { await navigator.share({ title: titel + ' auf Fundus', text: textEl ? textEl.textContent.slice(0, 140) : '', url: url }); return; }
      catch (e) { if (e && e.name === 'AbortError') return; }
    }
    try { await navigator.clipboard.writeText(url); hinweis('Link kopiert'); }
    catch (e) { prompt('Link zum Kopieren:', url); }
  }

  // Ein geteilter Link (?p=…) zeigt diesen Beitrag hervorgehoben oben an.
  async function direktlink() {
    const p = new URLSearchParams(location.search).get('p');
    if (!p || !/^[0-9a-f]{8,64}$/.test(p)) return;
    try {
      const d = await api('GET', '/api/v1/feed?id=' + encodeURIComponent('post:' + p));
      const post = (d.posts || [])[0];
      const liste = $('feed-liste');
      if (!post) { liste.insertAdjacentHTML('afterbegin', '<div class="feed-card feed-leer">Dieser Beitrag ist nicht (mehr) sichtbar – gelöscht, oder nicht für dich freigegeben.</div>'); return; }
      const vorhanden = liste.querySelector('[data-id="' + CSS.escape(post.id) + '"]');
      if (vorhanden) vorhanden.remove();
      liste.insertAdjacentHTML('afterbegin', beitragHtml(post));
      const neu = liste.firstElementChild;
      neu.classList.add('hervor');
      neu.scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (e) {}
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
      case 'teilen': teilen(art, t.dataset.sicht); break;
      case 'bearbeiten': beitragBearbeiten(art); break;
      case 'kbearbeiten': e.preventDefault(); kommentarBearbeiten(art, t.closest('.feed-k')); break;
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

  // ── Linkvorschau beim Schreiben (R814) ────────────────────────────────────
  // Der erste Link im Text wird sofort als Vorschau geholt. Mit ✕ lässt sie
  // sich verwerfen; dann bleibt der Link ein gewöhnlicher Link.
  let vorschau = null, vorschauFuer = '', verworfen = '', vorschauTimer = null, vorschauAnfrage = 0;
  function ersterLink(t) { const m = String(t || '').match(/https?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)\]]/); return m ? m[0] : ''; }
  function vorschauZeigen() {
    const box = $('feed-link-vorschau');
    if (!vorschau) { box.style.display = 'none'; box.innerHTML = ''; return; }
    const inhalt = videoErkennen(vorschau.url)
      ? videoHtml({ link: vorschau, text: '' })
      : linkKarteHtml(vorschau);
    box.innerHTML = '<div style="position:relative">' + inhalt +
      '<button type="button" class="feed-vorschau-zu" aria-label="Vorschau entfernen" title="Vorschau entfernen">✕</button></div>';
    box.style.display = '';
    box.querySelector('.feed-vorschau-zu').onclick = () => { verworfen = vorschau.url; vorschau = null; vorschauZeigen(); };
    // In der Vorschau nichts abspielen und keinen Link öffnen.
    box.querySelectorAll('a, .feed-start').forEach((el) => el.addEventListener('click', (e) => e.preventDefault()));
  }
  async function vorschauPruefen() {
    const url = ersterLink($('feed-text').value);
    if (!url) { vorschau = null; vorschauFuer = ''; vorschauZeigen(); return; }
    if (url === vorschauFuer || url === verworfen) return;
    vorschauFuer = url;
    const nr = ++vorschauAnfrage;
    const box = $('feed-link-vorschau');
    box.style.display = ''; box.innerHTML = '<div class="feed-lade">Vorschau wird geladen …</div>';
    try {
      const d = await api('POST', '/api/v1/feed/preview', { url: url });
      if (nr !== vorschauAnfrage) return;   // inzwischen anderer Link
      vorschau = d.link && d.link.url ? d.link : { url: url };
    } catch (e) {
      if (nr !== vorschauAnfrage) return;
      vorschau = { url: url };
    }
    vorschauZeigen();
  }
  $('feed-text').addEventListener('input', () => { clearTimeout(vorschauTimer); vorschauTimer = setTimeout(vorschauPruefen, 700); });
  $('feed-text').addEventListener('paste', () => { clearTimeout(vorschauTimer); vorschauTimer = setTimeout(vorschauPruefen, 50); });

  // ── Anhänge ───────────────────────────────────────────────────────────────
  // ── Medien (R815): Bilder und Videos gemeinsam, auch mehrere auf einmal ──
  // Ein Baustein für das Eingabefeld und das Bearbeiten-Fenster. Einträge
  // sind entweder neue Dateien (datei) oder bereits hochgeladene (hash).
  function medienListe(box, meldung) {
    const eintraege = [];
    function zeichnen() {
      box.style.display = eintraege.length ? '' : 'none';
      box.innerHTML = '';
      eintraege.forEach((a, i) => {
        const d = document.createElement('div');
        const src = a.datei ? a.vorschau : '/api/v1/files/download/' + encodeURIComponent(a.hash);
        d.innerHTML = (a.art === 'video' ? '<video muted playsinline preload="metadata"></video>' : '<img alt="">') +
          '<button type="button" aria-label="Entfernen">×</button>';
        d.firstChild.src = src;
        d.querySelector('button').onclick = () => {
          if (a.datei) URL.revokeObjectURL(a.vorschau);
          eintraege.splice(i, 1); zeichnen();
        };
        box.appendChild(d);
      });
    }
    return {
      eintraege: eintraege,
      zeichnen: zeichnen,
      vorhandene(bilder, videos) {
        (bilder || []).forEach((h) => eintraege.push({ art: 'bild', hash: h }));
        (videos || []).forEach((h) => eintraege.push({ art: 'video', hash: h }));
        zeichnen();
      },
      dateien(liste) {
        const zu = [];
        for (const f of Array.from(liste || [])) {
          const art = /^image\//.test(f.type) ? 'bild' : /^video\//.test(f.type) ? 'video' : '';
          if (!art) { zu.push(f.name + ': kein Bild oder Video'); continue; }
          const anzahl = eintraege.filter((a) => a.art === art).length;
          if (art === 'bild' && anzahl >= MAX_BILDER) { zu.push('höchstens ' + MAX_BILDER + ' Bilder'); continue; }
          if (art === 'video' && anzahl >= MAX_VIDEOS) { zu.push('höchstens ' + MAX_VIDEOS + ' Videos'); continue; }
          const grenze = art === 'bild' ? MAX_BILD_MB : MAX_VIDEO_MB;
          if (f.size > grenze * 1048576) {
            zu.push(f.name + ': größer als ' + grenze + ' MB' + (art === 'video' ? ' – längere Videos bitte als Link teilen' : ''));
            continue;
          }
          eintraege.push({ art: art, datei: f, vorschau: URL.createObjectURL(f) });
        }
        meldung(zu.length ? 'Nicht angehängt: ' + Array.from(new Set(zu)).join(', ') + '.' : '');
        zeichnen();
      },
      leeren() { eintraege.forEach((a) => a.datei && URL.revokeObjectURL(a.vorschau)); eintraege.length = 0; zeichnen(); },
      // Neue Dateien hochladen; liefert die Prüfsummen getrennt nach Art.
      async hochladenAlle(fortschritt) {
        const bilder = [], videos = [];
        const neu = eintraege.filter((a) => a.datei).length;
        let n = 0;
        for (const a of eintraege) {
          let hash = a.hash;
          if (a.datei) {
            n++;
            const f = a.art === 'bild' ? await verkleinern(a.datei) : a.datei;
            hash = await hochladen(f, (p) => fortschritt('Lade hoch ' + n + '/' + neu + ' … ' + Math.round(p * 100) + ' %'));
            a.hash = hash;
          }
          (a.art === 'bild' ? bilder : videos).push(hash);
        }
        return { bilder: bilder, videos: videos };
      }
    };
  }
  function status(t) { $('feed-status').textContent = t || ''; }
  const medien = medienListe($('feed-vorschau'), status);
  $('feed-media-datei').addEventListener('change', (e) => { medien.dateien(e.target.files); e.target.value = ''; });

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
  // R816: Redundanz 2 statt der Vorgabe 5.
  //
  // Ohne Angabe (redundancy: 0) galt 5: Die Datei sollte fünffach im Netz
  // liegen. Dafür braucht der Node vier andere Nodes mit je mindestens 1 GB
  // freiem Angebot, und die vierfache Dateigröße muss in das eigene Angebot
  // passen. Bei mehreren Videos scheiterte das mit 507; die Prüfung fragte
  // dazu bis zu 16 Nodes nacheinander an und dauerte so lange, dass die
  // Lastverteilung mit 502 abbrach.
  //
  // Für Feed-Anhänge genügen zwei Kopien (lokal und eine im Netz). Reicht
  // auch das nicht, nur lokal – ein Beitrag soll nie am Netzspeicher scheitern.
  async function hochladenBeginnen(f, BLOCK, id, redundanz) {
    const r = await fetch('/api/v1/files/upload/begin', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      // R817: "feed-" kennzeichnet Feed-Uploads im Datei-Index. Nur solche
      // Dateien entfernt der Node, wenn der letzte Beitrag gelöscht ist.
      body: JSON.stringify({ upload_id: id, file_name: 'feed-' + (f.name || 'datei'), mime_type: f.type || 'application/octet-stream',
        total_size: f.size, block_size: BLOCK, redundancy: redundanz })
    });
    let d = {};
    try { d = await r.json(); } catch (e) {}
    return { ok: r.ok, status: r.status, d: d };
  }
  async function hochladen(f, fortschritt) {
    const BLOCK = 4 * 1024 * 1024;
    const id = 'f' + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
    let start = await hochladenBeginnen(f, BLOCK, id, 2);
    if (!start.ok && start.status === 507) start = await hochladenBeginnen(f, BLOCK, id, 1);
    if (!start.ok) throw new Error((f.name ? f.name + ': ' : '') + (start.d.error || ('Hochladen nicht möglich (' + start.status + ')')));
    let r;
    const b = start.d;
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
    // Erst schnell, dann seltener nachfragen: Bilder sind meist nach Bruchteilen
    // einer Sekunde fertig, große Videos brauchen länger (R815).
    for (let i = 0; i < 200; i++) {
      await new Promise((res) => setTimeout(res, Math.min(1200, 250 + i * 150)));
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
    if (!text && !medien.eintraege.length) { status('Schreib etwas oder hänge ein Bild oder Video an.'); return; }
    // Vorschau nur mitschicken, wenn ihr Link noch im Text steht.
    const link = vorschau && text.indexOf(vorschau.url) >= 0 ? vorschau : null;
    btn.disabled = true;
    try {
      const m = await medien.hochladenAlle(status);
      status('Wird geteilt …');
      const d = await api('POST', '/api/v1/feed', {
        text: text, images: m.bilder, videos: m.videos, visibility: $('feed-sicht').value,
        link: link || undefined
      });
      $('feed-text').value = '';
      vorschau = null; vorschauFuer = ''; verworfen = ''; vorschauZeigen();
      medien.leeren();
      const sw = $('feed-sicht').value;
      const gName = sw.indexOf('group:') === 0 ? ((gruppen.find((g) => 'group:' + g.id === sw) || {}).name || 'Gruppe') : '';
      status(sw === 'public' ? '✓ Geteilt.'
        : !d.contacts ? '✓ Geteilt – aber niemand ' + (gName ? 'in „' + gName + '“' : 'in deinen Kontakten') + ' hat einen bekannten Schlüssel; außer dir sieht ihn noch niemand.'
        : gName ? '✓ Geteilt mit „' + gName + '“ (' + d.contacts + (d.contacts === 1 ? ' Person).' : ' Personen).')
        : '✓ Geteilt mit ' + d.contacts + (d.contacts === 1 ? ' Kontakt.' : ' Kontakten.'));
      aeltester = 0;
      await laden(true);
    } catch (e) {
      status('✗ ' + e.message);
    } finally { btn.disabled = false; }
  };

  // ── Gruppen (R813) ────────────────────────────────────────────────────────
  // Eigene Gruppen erscheinen in der Reichweitenauswahl. Sie liegen
  // verschlüsselt beim eigenen Konto; Mitglieder erfahren nichts davon,
  // außer den Gruppennamen an Beiträgen, die sie sehen dürfen.
  let gruppen = [];
  let sichtVorher = 'public';
  const LS_SICHT = 'fundus_feed_sicht';

  function reichweitenHtml() {
    return '<option value="public">🌐 Öffentlich</option><option value="contacts">👥 Nur Kontakte</option>' +
      (gruppen.length ? '<optgroup label="Gruppen">' + gruppen.map((g) =>
        '<option value="group:' + esc(g.id) + '">👥 ' + esc(g.name) + ' (' + g.members.length + ')</option>').join('') + '</optgroup>' : '');
  }
  function auswahlAufbauen() {
    const sel = $('feed-sicht');
    const wert = sel.value;
    sel.innerHTML = reichweitenHtml() +
      '<option value="_neu">＋ Neue Gruppe …</option>' +
      (gruppen.length ? '<option value="_verwalten">⚙ Gruppen verwalten …</option>' : '');
    let gemerkt = wert;
    try { gemerkt = localStorage.getItem(LS_SICHT) || wert; } catch (e) {}
    sel.value = Array.from(sel.options).some((o) => o.value === gemerkt && o.value[0] !== '_') ? gemerkt : 'public';
    sichtVorher = sel.value;
  }
  async function gruppenLaden() {
    try { gruppen = (await api('GET', '/api/v1/feed/groups')).groups || []; } catch (e) { gruppen = []; }
    auswahlAufbauen();
  }
  async function gruppenSpeichern(liste) {
    const d = await api('PUT', '/api/v1/feed/groups', { groups: liste });
    gruppen = d.groups || liste;
    auswahlAufbauen();
  }

  $('feed-sicht').addEventListener('change', (e) => {
    const v = e.target.value;
    if (v === '_neu') { e.target.value = sichtVorher; gruppeBearbeiten(null); return; }
    if (v === '_verwalten') { e.target.value = sichtVorher; gruppenVerwalten(); return; }
    sichtVorher = v;
    try { localStorage.setItem(LS_SICHT, v); } catch (x) {}
  });

  function dialog(titel, inhaltHtml, fussHtml) {
    const d = document.createElement('div');
    d.className = 'feed-dlg';
    d.innerHTML = '<div class="feed-dlg-karte" role="dialog" aria-modal="true" aria-label="' + esc(titel) + '">' +
      '<div class="feed-dlg-kopf">' + esc(titel) + '<button type="button" aria-label="Schließen" data-zu>✕</button></div>' +
      '<div class="feed-dlg-inhalt">' + inhaltHtml + '</div>' +
      (fussHtml ? '<div class="feed-dlg-fuss">' + fussHtml + '</div>' : '') + '</div>';
    d.addEventListener('click', (e) => { if (e.target === d || e.target.closest('[data-zu]')) d.remove(); });
    document.body.appendChild(d);
    return d;
  }

  async function gruppeBearbeiten(g) {
    let kontakte = [];
    try {
      const d = await api('GET', '/api/v1/messenger/contacts/load');
      kontakte = (d.contacts || []).map((k) => ({ fid: String(k.fundusID || k.fundus_id || '').toLowerCase(), name: k.alias || '' }))
        .filter((k) => /^0x[0-9a-f]{40}$/.test(k.fid));
    } catch (e) {}
    const mitglied = new Set((g && g.members) || []);
    // Mitglieder, die nicht mehr in den Kontakten stehen, trotzdem anzeigen.
    mitglied.forEach((f) => { if (!kontakte.some((k) => k.fid === f)) kontakte.push({ fid: f, name: '' }); });
    kontakte.sort((a, b) => (a.name || a.fid).localeCompare(b.name || b.fid, 'de'));
    const liste = kontakte.length
      ? kontakte.map((k) => '<label class="feed-mitglied"><input type="checkbox" value="' + esc(k.fid) + '"' + (mitglied.has(k.fid) ? ' checked' : '') + '>' +
          '<span>' + esc(k.name || (k.fid.slice(0, 8) + '…' + k.fid.slice(-4))) + '</span></label>').join('')
      : '<p class="feed-meta">Du hast noch keine Kontakte. Kontakte legst du im Messenger an.</p>';
    const d = dialog(g ? 'Gruppe bearbeiten' : 'Neue Gruppe',
      '<input type="text" id="fg-name" maxlength="40" placeholder="Name der Gruppe, z. B. Familie" value="' + esc((g && g.name) || '') + '">' +
      '<div class="feed-meta">Mitglieder</div>' + liste,
      '<button type="button" class="feed-tool" data-zu>Abbrechen</button><button type="button" class="feed-teilen" id="fg-ok">Speichern</button>');
    d.querySelector('#fg-name').focus();
    d.querySelector('#fg-ok').onclick = async () => {
      const name = d.querySelector('#fg-name').value.trim();
      const members = Array.from(d.querySelectorAll('.feed-mitglied input:checked')).map((i) => i.value);
      if (!name) { d.querySelector('#fg-name').focus(); return; }
      if (!members.length) { alert('Bitte mindestens ein Mitglied wählen.'); return; }
      const liste2 = g ? gruppen.map((x) => x.id === g.id ? { id: g.id, name: name, members: members } : x)
                       : gruppen.concat([{ id: '', name: name, members: members }]);
      try {
        await gruppenSpeichern(liste2);
        d.remove();
        // Eine neu angelegte Gruppe gleich auswählen.
        if (!g) {
          const neuG = gruppen.find((x) => x.name === name);
          if (neuG) { $('feed-sicht').value = 'group:' + neuG.id; sichtVorher = $('feed-sicht').value;
                      try { localStorage.setItem(LS_SICHT, sichtVorher); } catch (x) {} }
        }
        hinweis(g ? 'Gruppe gespeichert' : 'Gruppe angelegt');
      } catch (e) { alert('✗ ' + e.message); }
    };
  }

  function gruppenVerwalten() {
    const d = dialog('Gruppen verwalten', gruppen.map((g) =>
      '<div class="feed-gruppe-zeile" data-gid="' + esc(g.id) + '"><div style="flex:1;min-width:0"><div>' + esc(g.name) + '</div>' +
      '<div class="meta">' + g.members.length + (g.members.length === 1 ? ' Mitglied' : ' Mitglieder') + '</div></div>' +
      '<button type="button" data-g="bearbeiten">Bearbeiten</button><button type="button" data-g="loeschen">Löschen</button></div>').join('') +
      '<p class="feed-meta">Wer neu in eine Gruppe kommt, sieht auch ihre älteren Beiträge. Wer entfernt wird, sieht künftige nicht mehr – bereits gesehene lassen sich nicht zurückholen.</p>',
      '<button type="button" class="feed-tool" data-g="neu">＋ Neue Gruppe</button>');
    d.addEventListener('click', async (e) => {
      const b = e.target.closest('[data-g]');
      if (!b) return;
      const zeile = b.closest('[data-gid]');
      const g = zeile ? gruppen.find((x) => x.id === zeile.dataset.gid) : null;
      if (b.dataset.g === 'neu') { d.remove(); gruppeBearbeiten(null); }
      else if (b.dataset.g === 'bearbeiten' && g) { d.remove(); gruppeBearbeiten(g); }
      else if (b.dataset.g === 'loeschen' && g) {
        if (!confirm('Gruppe „' + g.name + '“ löschen? Bisherige Beiträge bleiben für ihre Mitglieder sichtbar.')) return;
        try { await gruppenSpeichern(gruppen.filter((x) => x.id !== g.id)); zeile.remove(); }
        catch (x) { alert('✗ ' + x.message); }
      }
    });
  }

  // ── Videos starten beim Scrollen (R817) ─────────────────────────────────
  // Ein hochgeladenes Video spielt, sobald es zu 60 % im Bild ist, und hält
  // an, wenn es hinausläuft. Es läuft immer nur eines. Wahl stumm oder mit
  // Ton oben in der Titelzeile, gemerkt im Browser.
  //
  // Mit Ton: Browser erlauben hörbares Abspielen nur nach einer Berührung der
  // Seite, Safari auf dem iPhone oft gar nicht ohne direkten Tipp aufs Video.
  // Verweigert der Browser es, spielt das Video stumm weiter, statt stehen zu
  // bleiben. YouTube und Vimeo starten nicht von selbst – sie laden erst nach
  // einem Klick (Datenschutz, siehe R812).
  const LS_TON = 'fundus_feed_ton';
  let tonModus = 'stumm';
  try { tonModus = localStorage.getItem(LS_TON) === 'ton' ? 'ton' : 'stumm'; } catch (e) {}
  function tonKnopf() { $('feed-ton').textContent = tonModus === 'ton' ? '🔊 Mit Ton' : '🔇 Stumm'; }
  tonKnopf();
  $('feed-ton').onclick = () => {
    tonModus = tonModus === 'ton' ? 'stumm' : 'ton';
    try { localStorage.setItem(LS_TON, tonModus); } catch (e) {}
    tonKnopf();
    // Das gerade laufende Video sofort umstellen – dieser Tipp zählt als
    // Berührung, danach darf der Browser hörbar abspielen.
    document.querySelectorAll('.feed-videos video').forEach((v) => { if (!v.paused) v.muted = tonModus !== 'ton'; });
  };
  function abspielen(v) {
    document.querySelectorAll('.feed-videos video').forEach((x) => { if (x !== v && !x.paused) x.pause(); });
    v.muted = tonModus !== 'ton';
    const p = v.play();
    if (p && p.catch) p.catch(() => {
      if (!v.muted) { v.muted = true; v.play().catch(() => {}); }
    });
  }
  const sichtbar = 'IntersectionObserver' in window ? new IntersectionObserver((eintraege) => {
    eintraege.forEach((e) => {
      const v = e.target;
      if (e.isIntersecting && e.intersectionRatio >= 0.6) abspielen(v);
      else if (!v.paused) v.pause();
    });
  }, { threshold: [0, 0.6] }) : null;
  function videosBeobachten() {
    if (!sichtbar) return;
    document.querySelectorAll('#feed-liste .feed-videos video:not([data-beob])').forEach((v) => {
      v.dataset.beob = '1';
      v.setAttribute('playsinline', '');   // iPhone: im Beitrag statt Vollbild
      sichtbar.observe(v);
    });
  }
  // Neue Beiträge (Nachladen, Bearbeiten, Direktlink) automatisch erfassen.
  new MutationObserver(videosBeobachten).observe($('feed-liste'), { childList: true, subtree: true });

  laden(true).then(direktlink);
  gruppenLaden();
  // Beim Zurückkehren in den Tab frisch laden, wenn nichts eingegeben wird.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && !$('feed-text').value && !medien.eintraege.length && !document.querySelector('.feed-dlg')) { aeltester = 0; laden(true); }
  });
})();

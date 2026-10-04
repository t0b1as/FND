// FUNDUS Service Worker — bewusst konservativ.
//
// Ein Node zeigt sich ständig ändernde Daten (Salden, Blockhöhe, Quittungen).
// Aggressives Caching würde veraltete Finanzdaten anzeigen — das wäre schlimmer
// als gar keine App. Deshalb: NUR statische Assets (CSS/JS/Icons) werden
// gecacht; alle Seiten und alle /api/-Aufrufe gehen IMMER ans Netz (network-only).
// Der Cache dient allein dem schnellen Start und der Installierbarkeit, nicht dem
// Offline-Betrieb der Live-Daten.

const CACHE = 'fundus-static-v2'; // erhöhen, wenn Icons/Logo sich ändern

// Nur unveränderliche/statische Assets vorcachen.
const STATIC_ASSETS = [
  '/static/style.css',
  '/static/favicon.svg',
  '/static/icon.svg',
  '/static/manifest.json',
];

self.addEventListener('install', (event) => {
  // Sofort aktiv werden, nicht auf Reload warten.
  self.skipWaiting();
  event.waitUntil(
    caches.open(CACHE).then((cache) =>
      // Einzelne Fehlschläge (z.B. Datei fehlt) nicht die Installation abbrechen lassen.
      Promise.allSettled(STATIC_ASSETS.map((u) => cache.add(u)))
    )
  );
});

self.addEventListener('activate', (event) => {
  // Alte Cache-Versionen aufräumen.
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);

  // Nur GET behandeln; POST/PUT etc. immer direkt ans Netz.
  if (req.method !== 'GET') return;

  // API-Aufrufe: NIEMALS aus dem Cache — immer frische Daten.
  if (url.pathname.startsWith('/api/')) return;

  // Statische Assets: cache-first (schneller Start), im Hintergrund aktualisieren.
  if (url.pathname.startsWith('/static/')) {
    event.respondWith(
      caches.open(CACHE).then((cache) =>
        cache.match(req).then((cached) => {
          const network = fetch(req).then((res) => {
            if (res && res.status === 200) cache.put(req, res.clone());
            return res;
          }).catch(() => cached);
          return cached || network;
        })
      )
    );
    return;
  }

  // Alles andere (Seiten): network-first, damit Live-Daten aktuell sind.
  // Fällt das Netz aus, gibt es (bewusst) keinen Offline-Fallback für Live-Seiten.
});

// ── Push-Benachrichtigungen (R536) ──────────────────────────────────────────
// Der Node schickt verschlüsselte Web-Push-Nachrichten: {title, body, url, tag}.
self.addEventListener('push', (event) => {
  let d = {};
  try { d = event.data ? event.data.json() : {}; } catch (e) { d = { body: event.data ? event.data.text() : '' }; }
  const title = d.title || 'Fundus';
  // Nicht stören, wenn der Messenger auf DIESEM Gerät gerade offen und im
  // Blick ist (R596). Auf allen anderen Geräten erscheint die Meldung.
  event.waitUntil((async () => {
    try {
      const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
      const aktiv = wins.some((c) => c.focused && (c.visibilityState === 'visible') &&
        new URL(c.url).pathname.indexOf('/messenger') === 0);
      if (aktiv && (d.tag === 'msg')) return;   // Chat offen: nur Nachrichten unterdrücken
    } catch (e) {}
    return self.registration.showNotification(title, {
      body: d.body || '',
      // PNG statt SVG (R595): Android stellt SVG in Benachrichtigungen NICHT dar –
      // dort erschien bisher ein leeres Symbol. Das Abzeichen ist einfarbig,
      // so wie Android es für die kleine Statusleiste erwartet.
      icon: '/static/icon-192.png',
      badge: '/static/badge-96.png',
      tag: d.tag || undefined,         // gleiche Art ersetzt die vorige statt zu stapeln
      renotify: !!d.tag,
      // Kurze Vibration und sichtbarer Zeitstempel – auf dem Sperrbildschirm
      // erscheint die Meldung dadurch wie die einer App.
      vibrate: [60, 40, 60],
      timestamp: Date.now(),
      data: { url: d.url || '/' },
    });
  })());
});

// Antippen: vorhandenes Fundus-Fenster nach vorn holen, sonst neu öffnen.
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || '/';
  event.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const c of all) {
      if (new URL(c.url).origin === self.location.origin) {
        try { await c.focus(); if ('navigate' in c) await c.navigate(target); return; } catch (e) {}
      }
    }
    await self.clients.openWindow(target);
  })());
});

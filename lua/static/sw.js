// FUNDUS Service Worker — bewusst konservativ.
//
// Ein Node zeigt sich ständig ändernde Daten (Salden, Blockhöhe, Quittungen).
// Aggressives Caching würde veraltete Finanzdaten anzeigen — das wäre schlimmer
// als gar keine App. Deshalb: NUR statische Assets (CSS/JS/Icons) werden
// gecacht; alle Seiten und alle /api/-Aufrufe gehen IMMER ans Netz (network-only).
// Der Cache dient allein dem schnellen Start und der Installierbarkeit, nicht dem
// Offline-Betrieb der Live-Daten.

const CACHE = 'fundus-static-v1';

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

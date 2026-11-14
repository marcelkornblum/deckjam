// DeckJam Service Worker - Offline Shell Caching
const CACHE_NAME = 'deckjam-shell-v1';

const STATIC_ASSETS = [
  '/',
  '/index.html',
  '/styles.css',
  '/app.js',
  '/deck',
  '/deck.html',
  '/deck.css',
  '/deck.js',
  '/icon.svg',
  '/manifest.webmanifest'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      // Best-effort asset prefetching
      return Promise.allSettled(
        STATIC_ASSETS.map((asset) => cache.add(asset))
      );
    }).then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => {
      return Promise.all(
        keys.map((key) => {
          if (key !== CACHE_NAME) {
            return caches.delete(key);
          }
        })
      );
    }).then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);

  // NEVER cache API requests, SSE events, share target redirects, or external dynamic requests
  if (
    url.pathname.startsWith('/api/') ||
    url.pathname === '/share' ||
    event.request.method !== 'GET'
  ) {
    return;
  }

  // Network-first with fallback to cache for UI shell assets
  event.respondWith(
    fetch(event.request)
      .then((networkResponse) => {
        if (networkResponse && networkResponse.status === 200) {
          const responseToCache = networkResponse.clone();
          caches.open(CACHE_NAME).then((cache) => {
            cache.put(event.request, responseToCache);
          });
        }
        return networkResponse;
      })
      .catch(async () => {
        const cached = await caches.match(event.request);
        if (cached) {
          return cached;
        }
        // Fallback for document navigation
        if (event.request.mode === 'navigate') {
          if (url.pathname.startsWith('/deck')) {
            const deckFallback = await caches.match('/deck.html') || await caches.match('/deck');
            if (deckFallback) return deckFallback;
          }
          const indexFallback = await caches.match('/index.html') || await caches.match('/');
          if (indexFallback) return indexFallback;
        }
        return new Response('Offline - DeckJam server unreachable', {
          status: 503,
          statusText: 'Service Unavailable',
          headers: new Headers({ 'Content-Type': 'text/plain' })
        });
      })
  );
});

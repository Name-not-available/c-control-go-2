const CACHE_NAME = 'restaurant-finder-cache-v3';
const STATIC_URLS = [
  '/vwrk4DFEv1RQpl3PxmWSZUeCkSVjAc5kbDqnIIu4DqDYVdNnGiu1xBWIE8IgbJ3X.html',
  '/manifest.json'
];

// Install: cache static assets
self.addEventListener('install', (event) => {
  self.skipWaiting(); // Activate immediately
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(STATIC_URLS))
  );
});

// Activate: clean old caches and take control immediately
self.addEventListener('activate', (event) => {
  event.waitUntil(
    Promise.all([
      caches.keys().then((cacheNames) =>
        Promise.all(
          cacheNames
            .filter((name) => name !== CACHE_NAME)
            .map((name) => caches.delete(name))
        )
      ),
      self.clients.claim() // Take control of all pages immediately
    ])
  );
});

// Fetch: stale-while-revalidate for HTML, cache-first for others
self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') {
    return;
  }

  const url = new URL(event.request.url);
  
  // For API requests, always go to network
  if (url.pathname.startsWith('/api/')) {
    return;
  }

  // For HTML pages: serve cached version immediately, update cache in background
  if (event.request.headers.get('accept')?.includes('text/html') || 
      url.pathname.endsWith('.html')) {
    event.respondWith(
      caches.open(CACHE_NAME).then((cache) => {
        return cache.match(event.request).then((cachedResponse) => {
          const fetchPromise = fetch(event.request).then((networkResponse) => {
            if (networkResponse.ok) {
              cache.put(event.request, networkResponse.clone());
            }
            return networkResponse;
          }).catch(() => cachedResponse);

          // Return cached version immediately if available, otherwise wait for network
          return cachedResponse || fetchPromise;
        });
      })
    );
    return;
  }

  // For other assets: cache-first
  event.respondWith(
    caches.match(event.request).then((response) => {
      if (response) {
        return response;
      }
      return fetch(event.request).then((networkResponse) => {
        if (networkResponse.ok) {
          return caches.open(CACHE_NAME).then((cache) => {
            cache.put(event.request, networkResponse.clone());
            return networkResponse;
          });
        }
        return networkResponse;
      });
    })
  );
});


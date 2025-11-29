const CACHE_NAME = 'restaurant-finder-cache-v1';
const URLS_TO_CACHE = [
  '/',
  '/vwrk4DFEv1RQpl3PxmWSZUeCkSVjAc5kbDqnIIu4DqDYVdNnGiu1xBWIE8IgbJ3X.html',
  '/manifest.json',
  '/service-worker.js'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(URLS_TO_CACHE))
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((cacheNames) =>
      Promise.all(
        cacheNames
          .filter((name) => name !== CACHE_NAME)
          .map((name) => caches.delete(name))
      )
    )
  );
});

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') {
    return;
  }

  event.respondWith(
    caches.match(event.request).then((response) => {
      if (response) {
        return response;
      }
      return fetch(event.request)
        .then((networkResponse) => {
          return caches.open(CACHE_NAME).then((cache) => {
            cache.put(event.request, networkResponse.clone());
            return networkResponse;
          });
        })
        .catch(() => caches.match('/vwrk4DFEv1RQpl3PxmWSZUeCkSVjAc5kbDqnIIu4DqDYVdNnGiu1xBWIE8IgbJ3X.html'));
    })
  );
});


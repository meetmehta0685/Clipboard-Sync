// App-shell cache so the installed PWA opens instantly and offline.
// Clips never touch this cache; they live in memory and localStorage.
const CACHE = "clipsync-shell-v1";
const SHELL = ["/", "/pair", "/share", "/manifest.webmanifest", "/icon.svg"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== "GET" || url.origin !== location.origin || url.pathname.startsWith("/ws")) return;
  // Network first for pages (fresh deploys), cache first for hashed assets.
  if (url.pathname.startsWith("/_next/static/")) {
    e.respondWith(caches.match(e.request).then((hit) => hit || fetch(e.request).then((res) => {
      const copy = res.clone();
      caches.open(CACHE).then((c) => c.put(e.request, copy));
      return res;
    })));
    return;
  }
  e.respondWith(fetch(e.request).then((res) => {
    if (res.ok && e.request.mode === "navigate") {
      const copy = res.clone();
      caches.open(CACHE).then((c) => c.put(url.pathname, copy));
    }
    return res;
  }).catch(() => caches.match(url.pathname).then((hit) => hit || caches.match("/"))));
});

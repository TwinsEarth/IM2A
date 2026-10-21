// 模块职责：PWA Service Worker —— 离线壳（HTML/CSS/JS 缓存），数据一律网络优先、不缓存敏感内容
// 契约来源：contracts/INTERFACES.md §47（PWA 必须提供 manifest + service worker）
const CACHE = 'im2a-shell-v1';
const SHELL = ['./index.html', './app.css', './app.js', './logic.mjs', './manifest.webmanifest'];

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)))).then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  // 接口数据不做缓存：宁可离线时提示，也不展示过期的账本/审批状态
  if (url.pathname.startsWith('/api/')) return;
  event.respondWith(
    caches.match(event.request).then((cached) => cached ?? fetch(event.request).catch(() => caches.match('./index.html')))
  );
});

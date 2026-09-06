/// <reference types="@sveltejs/kit" />
/// <reference no-default-lib="true"/>
/// <reference lib="esnext" />
/// <reference lib="webworker" />

// The service worker caches the application shell and nothing else
// (ADR 0012): every cached byte of user data lives in IndexedDB, written by
// the application, so there is one invalidation mechanism rather than two.
// `/api/*` is never intercepted. Push handlers are added by the Web Push
// slice.
import { build, files, version } from "$service-worker";

const sw = self as unknown as ServiceWorkerGlobalScope;
const CACHE = `vyn-shell-${version}`;
const FALLBACK = "/200.html";
const ASSETS = [...build, ...files];

// The fallback document is what every client-side route boots from (ADR
// 0012). Caddy serves it as a file; a preview server may answer it only as
// the SPA fallback for "/", so both are tried and the result is stored under
// the fallback key either way.
async function cacheFallback(cache: Cache): Promise<void> {
  for (const candidate of [FALLBACK, "/"]) {
    const response = await fetch(candidate, { cache: "no-store" });
    if (response.ok) {
      await cache.put(FALLBACK, response);
      return;
    }
  }
  throw new Error("no fallback document to cache");
}

sw.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) =>
        Promise.all([cache.addAll(ASSETS), cacheFallback(cache)]),
      )
      .then(() => sw.skipWaiting()),
  );
});

sw.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)),
        ),
      )
      .then(() => sw.clients.claim()),
  );
});

sw.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== sw.location.origin) return;
  // The API is network-only, always. The stream would never survive a cache
  // anyway, and mutation queueing is the application's job, not the cache's.
  if (url.pathname.startsWith("/api/")) return;

  event.respondWith(
    (async () => {
      const cache = await caches.open(CACHE);
      const cached = await cache.match(request, { ignoreSearch: true });
      if (cached) return cached;
      if (request.mode === "navigate") {
        try {
          const fresh = await fetch(request);
          return fresh;
        } catch {
          const fallback = await cache.match(FALLBACK);
          if (fallback) return fallback;
        }
      }
      return fetch(request);
    })(),
  );
});

/// <reference types="@sveltejs/kit" />
/// <reference no-default-lib="true"/>
/// <reference lib="esnext" />
/// <reference lib="webworker" />

// The service worker caches the application shell and nothing else
// (ADR 0012): every cached byte of user data lives in IndexedDB, written by
// the application, so there is one invalidation mechanism rather than two.
// `/api/*` is never intercepted. Push messages carry their own copy and
// route (ADR 0014); the service worker shows them and never fetches.
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

// Web Push (ADR 0014): the payload carries the copy, the URL and the tag,
// encrypted end to end, so nothing is fetched here. The tag collapses a
// later message about the same Note onto the earlier one.
type PushPayload = {
  title: string;
  body: string;
  url: string;
  tag: string;
  kind: string;
  noteId?: string;
};

sw.addEventListener("push", (event) => {
  let payload: PushPayload | null;
  try {
    payload = event.data ? (event.data.json() as PushPayload) : null;
  } catch {
    payload = null;
  }
  const title = payload?.title ?? "Visual Notes";
  event.waitUntil(
    sw.registration.showNotification(title, {
      body: payload?.body ?? "",
      tag: payload?.tag ?? "vyn",
      data: { url: payload?.url ?? "/", kind: payload?.kind ?? "" },
      icon: "/icons/icon-192.png",
      badge: "/icons/icon-192.png",
    }),
  );
});

sw.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = new URL(
    (event.notification.data as { url?: string } | undefined)?.url ?? "/",
    sw.location.origin,
  ).href;
  event.waitUntil(
    (async () => {
      const clientList = await sw.clients.matchAll({
        type: "window",
        includeUncontrolled: true,
      });
      for (const client of clientList) {
        if ("focus" in client) {
          await client.focus();
          if ("navigate" in client) await client.navigate(target);
          return;
        }
      }
      await sw.clients.openWindow(target);
    })(),
  );
});

// The push service rotated the subscription: re-subscribe with the same
// options and hand the server both endpoints so it swaps the row.
sw.addEventListener("pushsubscriptionchange", (event) => {
  const change = event as Event & {
    oldSubscription: PushSubscription | null;
    newSubscription: PushSubscription | null;
  };
  event.waitUntil(
    (async () => {
      const old = change.oldSubscription;
      const options = old?.options;
      const fresh =
        change.newSubscription ??
        (options
          ? await sw.registration.pushManager.subscribe({
              userVisibleOnly: true,
              applicationServerKey: options.applicationServerKey,
            })
          : null);
      if (!fresh) return;
      const key = (name: PushEncryptionKeyName) => {
        const raw = fresh.getKey(name);
        if (!raw) return "";
        let text = "";
        for (const b of new Uint8Array(raw)) text += String.fromCharCode(b);
        return btoa(text)
          .replace(/\+/g, "-")
          .replace(/\//g, "_")
          .replace(/=+$/, "");
      };
      await fetch("/api/push-subscriptions", {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint: fresh.endpoint,
          keys: { p256dh: key("p256dh"), auth: key("auth") },
          expiration_time: fresh.expirationTime
            ? new Date(fresh.expirationTime).toISOString()
            : null,
          notes_enabled: true,
          ops_enabled: true,
          ...(old && old.endpoint !== fresh.endpoint
            ? { replaces_endpoint: old.endpoint }
            : {}),
        }),
      }).catch(() => undefined);
    })(),
  );
});

# Split live updates into durable and ephemeral frames, and cache only in IndexedDB

The PWA is a single-page application served from one fallback document. It receives live updates on
one leader-elected `EventSource` carrying two kinds of frame: cursored `sync` frames that replay
from the `sync_events` outbox, and uncursored `progress` frames that may be dropped. The service
worker caches the application shell and nothing else; every cached byte of user data lives in
IndexedDB, written by the application.

Ticket [#23](https://github.com/MAX-786/visual-youtube-notes/issues/23). Mechanical detail — the
route map, frame shapes, object stores and share rules — is in [`pwa-shell.md`](../design/pwa-shell.md).

## The SPA fallback was already broken for every two-segment route

`adapter-static` is configured with `fallback: "200.html"`, but Caddy served `try_files {path} /index.html`.
These are not interchangeable, and the difference is not cosmetic.

With `ssr = false` the prerendered `/` document is as empty as the fallback, so the two files look
equivalent. They are not: SvelteKit emits **relative** asset URLs into a prerendered page and
**absolute** ones into the fallback, because the fallback must work at any depth and a prerendered
page knows its own. `index.html` therefore carries `./_app/immutable/...` and computes
`base: new URL(".", location).pathname.slice(0, -1)`, while `200.html` carries `/_app/immutable/...`
and `base: ""`.

Served as the fallback for `/notes/8a41c7d3`, `./_app/...` resolves against `/notes/`, and `base`
evaluates to `/notes`. Both are wrong. The failure is also disguised: `try_files` answers the
missing asset with the fallback document itself, so the browser receives **HTML with a 200 status
where it asked for a module script**, not a 404.

One-segment routes are unaffected, which is why nothing has looked wrong: `/` and `/share` resolve
`./_app/...` back to `/_app/...`. Every two-segment route breaks — and the two-segment routes are
`/notes/<id>`, the target of every notification tap and cross-device deep link, and
`/pair/<requestId>`, the extension pairing flow.

Caddy now falls back to `/200.html`, the file the adapter actually designates, and
[`spa-fallback.test.mjs`](../design/spa-fallback.test.mjs) asserts the two configurations name the
same document, because the failure mode is a 200 and will not announce itself.

Root-level `prerender = true` also goes. It cannot survive `/notes/[id]`, and with `ssr = false` a
prerendered route only produces a second, subtly different empty shell.

## The stream carries two frame kinds, and only one of them gets a cursor

Spec section 15 requires create/update/delete events to carry monotonically increasing sync cursors,
published over SSE and replayable from a cursor after reconnect. Taken to mean _every_ observable
change, that swallows pipeline progress: `queued -> metadata -> captions -> transcribing ->
generating -> indexing -> ready` is seven transitions per Note, and `allocate_sync_cursor()` holds a
`FOR UPDATE` lock on a single per-Owner counter row until its transaction commits. Every stage
transition of every Note would serialize on that row, and a 30-day outbox sized for durable change
would fill with progress pings.

The two are different obligations. A missed Purge is unrecoverable — the outbox exists precisely
because a hard-deleted Note cannot be discovered from the `notes` table. A missed
`generating -> indexing` heals on the next frame, and in the worst case on the terminal transition,
which _is_ durable. So:

- **`sync` frames** are a verbatim tail of `sync_events`, carrying the cursor in SSE's own `id:`
  field. Only terminal processing states (`ready`, `failed`, `cancelled`) enter the outbox.
- **`progress` frames** carry `note_stages` state and deliberately **omit the `id:` field**.
  Emitting a cursor there would be worse than useless: `id:` is what populates `Last-Event-ID` on
  reconnect, so a progress frame with an id would overwrite the resume point with a value that is
  not a cursor.

Using `id:` for the cursor means the browser's own reconnect sends `Last-Event-ID` and the resume
needs no application code on the happy path. The client still persists the cursor, for cold start
and for the polling fallback.

Spec 15 has been corrected to state the distinction rather than leaving it to be inferred.

## One stream per profile, and revocation rather than token expiry closes it

`EventSource` cannot set headers, but the stream is same-origin, so `__Host-vyn_access` rides on it
under `SameSite=Lax`. Two questions follow.

_How many streams?_ One per browser profile, not one per tab. The leader is elected with Web Locks —
already the primitive [ADR 0003](0003-owner-auth-device-sessions-and-extension-pairing.md) uses to
serialize refresh — and fans frames out to other tabs over `BroadcastChannel`.

_What closes a stream whose access cookie has expired?_ Nothing. The access token's 15-minute
lifetime bounds the damage of a leaked credential; an already-established stream is not a fresh use
of one, and tearing down every stream every 15 minutes would make reconnect storms the normal case.
What must be immediate is **revocation**, so the server re-verifies the Device session on each
30-second heartbeat and closes with a terminal `revoked` frame when it is gone. The heartbeat
already exists for Cloudflare's 125-second proxy read timeout
([ADR 0008](0008-deployment-topology.md)); this gives it a second job and a 30-second revocation
bound.

## The service worker never caches an API response

Spec section 9 asks for the last seven days of the feed, notes opened within thirty days, and
transcripts only when explicitly opened. None of those are expressible as HTTP cache entries: they
are predicates over _records_ — save date, last-opened time — and `sync_events` invalidates
**entities**, not URLs.

So the line is drawn hard. The Cache API holds the application shell only, precached by
`$service-worker`'s `version`. The service worker is **network-only for `/api/*`**, with no runtime
caching whatsoever. All user data is written to IndexedDB by the application.

This keeps one invalidation mechanism instead of two racing ones, makes the retention rule a query
rather than a cache eviction policy, and lets sign-out erase user data without touching the shell.
SvelteKit's built-in `src/service-worker.ts` is enough; Workbox and `vite-plugin-pwa` exist to manage
runtime caching strategies, and there is now no runtime caching to manage.

The CSRF token stays in memory and is never persisted. ADR 0003 already provides
`GET /auth/refresh/csrf` as the recovery path across reloads, so writing it to IndexedDB would add
readable credential material to the origin in exchange for nothing.

## Every mutation writes through the queue, online included

The obvious optimization — POST directly when online, fall back to IndexedDB when not — creates two
implementations of base-revision rebasing, and the second one only runs when the network is bad.
That is the worst possible place to put the path that gets less testing.

Instead there is one path: the two-second autosave debounce enqueues, and a single drainer sends
serially per Note, applying the rebase rule from [`mutation-protocol.md`](../design/mutation-protocol.md)
to every response. Online, the queue is simply usually empty. Shared URLs use the same shape in
`pending_submissions`, which makes spec 5.2's "queue when offline" and "hold while signed out" one
mechanism rather than two.

## What a forced resync must not delete

`sync_events` is retained 30 days. A tablet that sleeps past the floor cannot replay, so the server
answers with `resync` and the client rebuilds from scratch.

Rebuilding clears `notes`, `transcripts` and `topics`. It **never** clears `mutation_queue` or
`pending_submissions`. A stale cache is a display problem; unsent edits and unsubmitted shares are
user work, and discarding them to fix a display problem is exactly the silent loss spec section 16
forbids. The queue's entries carry their own base revision and rebase on drain, so they survive the
rebuild without special handling.

Local sign-out is the opposite case and clears everything, including the queue, per spec section 9.
It broadcasts on the same `BroadcastChannel` the stream uses, because a background tab still showing
a revoked library is the failure this requirement exists to prevent.

Foregrounding while online blocks note content behind `GET /auth/session`; the shell paints
immediately and the data stays curtained until it answers. Offline, the cache is revealed unchecked
— spec section 17.9's accepted limitation, and the reason the check is on foreground rather than on
boot alone.

## Push is not in this design

Spec section 13 lists `/push-subscriptions` and spec 8.3 step 7 fires a Web Push notification when a
Note becomes ready, but no migration defines a subscription table, no ticket owns VAPID key handling
or the send trigger, and the map lists notification copy as unspecified. That is one vertical
spanning client and server, and designing half of it from the client side would fix the
`/push-subscriptions` contract without the storage model that has to satisfy it.

It moves to its own ticket. This design reserves the service-worker file so `push` and
`notificationclick` handlers drop in without restructuring.

## Consequences

- Deep links work, which `/notes/<id>` and `/pair/<requestId>` both depend on.
- Progress is best-effort by construction. The UI must never derive a durable conclusion from a
  progress frame; terminal state arrives as a `sync` frame or not at all.
- Killing SSE degrades to polling with progress intact, because the poller refetches in-flight Note
  status alongside the cursor endpoint. Spec 16 requires proving exactly this.
- One stream and one drainer per profile means multi-tab correctness depends on `BroadcastChannel`
  and Web Locks, both of which fail closed: losing the election means no stream, not two.

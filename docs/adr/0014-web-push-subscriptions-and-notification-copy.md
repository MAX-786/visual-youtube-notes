# Bind push subscriptions to Device sessions, and send from whichever process is still alive

Web Push subscriptions live in `push_subscriptions`, keyed by endpoint and bound to a PWA Device
session so revocation stops notifications by cascade. Only the private VAPID key is stored; the
public key is derived at boot and served to the PWA. Note notifications are sent by the worker as a
separate River job; operational alerts are sent by the API, because the worker may be the thing that
died.

Ticket [#27](https://github.com/MAX-786/visual-youtube-notes/issues/27). Schema:
[`000009_push_subscriptions.up.sql`](../../deploy/migrations/000009_push_subscriptions.up.sql).
Wording: [`notification-copy.md`](../design/notification-copy.md).

## The storage was the only missing piece

Compose already mounts `vapid_private_key` on `api`, `worker` and `backup` — ticket #21 put it in
the secret inventory and inside `keys.tar.age` before anything used it. Spec 13 lists
`/push-subscriptions` and spec 8.3 step 7 fires a push when a Note becomes ready. What did not exist
was a table, a trigger set, or a single sentence of copy.

## One subscription per endpoint, bound to a PWA session

The endpoint is the subscription's identity: a browser that re-subscribes without telling us
produces the same endpoint, and a second row would double every notification. So `endpoint` is
unique, not merely indexed.

Spec 16 requires that revoking a Device session stops its notifications. A foreign key to
`device_sessions` with `ON DELETE CASCADE` does that with no application code to forget, which is
the only version of this that stays true after someone adds a second revocation path.

A subscription must never belong to an **extension** session: ADR 0013 makes the extension
submit-only, and Chromium extensions do not use Web Push at all. Enforcing that requires the
session's `kind` to be reachable from a foreign key, so the migration adds `UNIQUE (id, kind)` to
`device_sessions` and carries a `device_session_kind` column pinned to `'pwa'` — the same idiom the
schema already uses for `(id, user_id)` and `(id, extension_id)`.

Both foreign keys point at the same parent row, so revoking a session deletes the subscription once.
That is deliberately unlike the trap ticket #19 hit, where two `ON DELETE SET NULL` keys on one row
made PostgreSQL revalidate a key whose target the same cascade had already removed.

The key material gets format constraints — base64url alphabet, and lengths matching RFC 8291's
65-octet point and 16-octet auth secret. A client sending standard base64 (`+`, `/`) or a truncated
secret otherwise fails at **send** time, where the only symptom is a notification that silently never
arrives.

**Two switches, not one.** `notes_enabled` and `ops_enabled` are separate, because a backup failing
at 03:00 and "your note is ready" share a transport and nothing else. Spec 16 requires only that push
can be turned off; it does not require that both go off together, and the Owner will plausibly want
alerts on a phone without a buzz for every note.

## Store one key, derive the other

The VAPID public key is what the browser subscribes with and what the server signs with. Storing
both invites them to drift, and a mismatch is invisible until a send is rejected.

So only `vapid_private_key` is stored, and the API derives the public key at boot by scalar
multiplication on P-256. It is served from `GET /api/push/vapid-public-key` rather than baked in at
build time: the PWA bundle is built in CI by `adapter-static`, and the key is generated at deploy
time, so a build-time constant would be a value that does not exist yet.

Rotating the key invalidates **every** stored subscription, because RFC 8292 binds a subscription to
the key that created it. Rotation therefore means truncating the table and re-subscribing every
device. As with ADR 0010's master key, that stays a documented consequence rather than a procedure.

## The sender is whichever process is still alive

A push send is a **separate River job**, never inline in the generation Stage and never a
`stage_kind` of its own. Inline, a flaky push service would fail a Note that is genuinely ready.
ADR 0011 already refused to add a Stage for a step that reports nothing user-visible; a push send is
that, plus it can actually fail, which is the stronger reason to keep its retries off the pipeline's
ladder.

Responsibility splits the way ADR 0009 split health checking:

- The **worker** sends note notifications, from the job that just finished the work.
- The **API** sends operational alerts, because the condition being alerted on is frequently "the
  worker is not running".

This is why Compose mounts the key on both.

### Where suppression state lives for an alert with no row

`system_health.last_notified_at` gives 24-hour suppression to every check the worker writes. But
ADR 0009 has the API evaluate two conditions from a plain ticker — stale `checked_at`, and an expired
`river_leader` lease — and it computes them rather than storing them. There is no row to stamp, so
an API restart loop would re-alert on every boot.

So the API **upserts its own `system_health` rows**, `worker_stale` and `river_leader_lost`. This
does not contradict ADR 0009's "unknown is not a stored value": that rule exists because a dead
worker cannot write a row saying it is dead. The API is alive and can. The rows buy `first_alarm_at`
and `last_notified_at` for free, so suppression survives a restart, and the condition appears in the
health view like every other check instead of only in a notification.

## What fires, and the two failures that are not one failure

Note notifications fire on `ready`, `failed`, and `held`. Not `cancelled` — the Owner caused that
and does not need telling.

`held` is there because spec 11 and spec 16 disagreed. Spec 11: "a held submission notifies the
owner and waits, and is never resumed automatically." Spec 16: push occurs "only for ready or failed
jobs." Spec 16's sentence was written to stop a buzz per pipeline stage, and a soft-cap hold is
terminal in the sense that matters — nothing moves until the Owner acts. Since the hold waits
forever by design, an in-app-only notification means the Owner may simply never look. **Spec 16 is
corrected in the same commit as this ADR**, rather than reinterpreted.

`failed` is two messages, not one. Spec 8.4 separates permanent input failures from extractor
breakage, and ADR 0009 is explicit that extractor breakage "alerts the owner rather than blaming the
video". One shared failure string would blame a perfectly good video for yt-dlp being out of date,
at the exact moment the Owner is reading. Ticket #21 created a third failure class to avoid that;
collapsing it here would throw it away.

## Collapsing, and the 32-character ceiling

RFC 8030 caps the `Topic` header at 32 characters of URL-safe base64, so a newer message replaces a
pending one. A Note UUID is 36 characters with hyphens — too long — and exactly **32 without them**,
with `[0-9a-f]` inside the base64url alphabet. So the topic is the hyphen-stripped Note id, and it
fits with nothing to spare. A later `failed` then replaces a still-pending `ready` for the same Note
rather than stacking two contradictory notifications. `showNotification`'s `tag` matches it.
Operational alerts use `check_name`, where underscores are already base64url-safe.

The payload carries `{noteId, title, url, kind}`. Including the title is deliberate: RFC 8291
encrypts end-to-end, so the push service cannot read it, and `userVisibleOnly: true` forces a
notification to be shown whether or not we have anything to say in it. A payload of bare ids would
force the service worker to fetch the title, which fails exactly when the device is offline.

## Lifecycle

A `404` or `410` deletes the row: RFC 8030 says the subscription is gone, not unwell.
`429` and `5xx` are transient and ride the job's retry ladder. `pushsubscriptionchange` is handled in
the service worker by re-subscribing from `event.oldSubscription.options` and posting both old and
new, so the server swaps the row rather than accumulating a dead one beside a live one.

Permission is requested only from an explicit Settings button, never on load — MDN requires a user
gesture, and an unprompted request on a single-user site risks Chrome's quieter-UI enrollment for no
gain. Notifications fan out to every subscription the Owner has, since spec 16's scenario is a
desktop submission landing on a tablet.

## Consequences

- ADR 0012's `/settings/backups` becomes **`/settings/health`**, covering every `system_health` row
  rather than backups alone, because operational alerts had nowhere to land. The route map in
  [`pwa-shell.md`](../design/pwa-shell.md) is updated with it.
- The runbook's claim that "push is the nudge, not the storage" now holds in both directions: every
  alert the API can raise has a durable `system_health` row behind it.
- Rotating the VAPID key is a re-subscribe event for every device, and is documented as such.
- Android's app-level `POST_NOTIFICATIONS` grant is not covered by the web permission. It stays on
  the tablet checklist, since the #7 research could not confirm from primary sources how Chrome maps
  it to a WebAPK.

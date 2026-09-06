# Notification copy

Every user-facing notification string, for ticket
[#27](https://github.com/MAX-786/visual-youtube-notes/issues/27). Decisions live in
[ADR 0014](../adr/0014-web-push-subscriptions-and-notification-copy.md); the schema is
[`000009_push_subscriptions.up.sql`](../../deploy/migrations/000009_push_subscriptions.up.sql).

This file is the single source for these strings. The same wording is used in the in-app surface, so
a notification and the screen it opens never describe the same state differently.

## Rules

1. **Every line names the next action, or says plainly that there isn't one.** Spec 15: "Processing
   and error language identifies the next available action."
2. **No status word the UI does not also use.** If the notification says "blocked", the note card
   says "blocked".
3. **Never blame the video for a tool problem.** The extractor rows go out of their way to exonerate
   the source, because the obvious reading is wrong (ADR 0009).
4. **No transcript text, note body, or provider detail.** Spec 15 keeps those out of logs; a lock
   screen is less private than a log.
5. `*italics*` below are runtime substitutions.

## Note notifications

Category `notes_enabled`. Topic and `tag`: the Note id with hyphens stripped — exactly 32
characters, RFC 8030's ceiling. Tapping opens `/notes/*id*`.

| Trigger              | Title                         | Body                                                          |
| -------------------- | ----------------------------- | ------------------------------------------------------------- |
| `ready`              | _video title_                 | Visual note ready — tap to open.                              |
| `failed` (input)     | _video title_                 | Couldn't create notes: _reason_. Open to retry or delete.     |
| `failed` (extractor) | YouTube extraction is blocked | The video is fine — yt-dlp needs updating. Tap for the steps. |
| `held`               | Monthly AI cap reached        | _video title_ is waiting for you to confirm the spend.        |

`cancelled` sends nothing: the Owner caused it.

Because the topic is the Note id, a later `failed` **replaces** a still-pending `ready` for the same
Note instead of leaving two contradictory notifications on the lock screen.

_reason_ is the pipeline's own failure reason, already written for a person by spec 8.4 — not a
provider error string.

## Operational alerts

Category `ops_enabled`. Topic and `tag`: `check_name`. `Urgency: high`. Tapping opens
`/settings/health`.

Fires on transition into alarm, then at most once per 24 hours while it persists (ADR 0009: an alert
that repeats every 15 minutes is one that gets muted).

| Check                | Title                        | Body                                                        |
| -------------------- | ---------------------------- | ----------------------------------------------------------- |
| `backup_local`       | Local backup is failing      | No fresh snapshot in _N_ days. Tap to check.                |
| `backup_offsite`     | Off-server copy is stale     | Last copy _N_ days ago. The laptop may be off.              |
| `disk_free`          | Disk is nearly full          | _N_ GiB free. Clear space before jobs start failing.        |
| `extractor_breakage` | yt-dlp needs updating        | New notes are blocked until it is updated.                  |
| `worker_stale`       | Processing has stopped       | No health check in _N_ minutes. Nothing is being processed. |
| `river_leader_lost`  | Job scheduler lost its lease | Scheduled work is not running. Tap to check.                |

`backup_offsite` names the laptop deliberately: the off-server copy goes to a machine that is
legitimately off some nights, and an alert that reads like data loss when the answer is "close the
lid tomorrow" is one that gets ignored.

The last two rows are written by the **API**, not the worker — the worker may be the thing that
died (ADR 0014).

## What has no notification at all

Stated so it stays a decision rather than becoming a discovery:

- **A total host outage.** Every check runs inside the stack and ADR 0008 took no external
  dead-man's switch. Already in the runbook's "What this stack cannot tell you".
- **A dead API.** Nothing watches it.
- **Any note reaching an intermediate stage.** Progress is the SSE stream's job (ADR 0012), and it
  is deliberately uncursored and droppable.
- **A device with no subscription.** The durable record is `system_health` and the app's health view;
  push is the nudge, not the storage.

## Permission prompt

Not a push, but the same voice, and the only place `Notification.requestPermission()` may be called
(Settings → Notifications, on an explicit press):

> **Get notified when notes are ready?**
> Also warns you if backups start failing. You can turn either off later.

Never on page load. MDN requires a user gesture, and an unprompted request risks Chrome's quieter
permission UI for no gain on a single-user site.

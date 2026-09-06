# Declare rate limits in the contract, key them per device with a global backstop, and never drop a queued write

Every operation in `docs/design/openapi.yaml` names an `x-rate-limit` class, and the classes with
their values are declared once, in the same document, under `x-rate-limit-classes`. The API builds
in-process token buckets from that table at boot, from the document it already loads for request
validation ([ADR 0015](0015-hand-written-openapi-contract-generated-both-ends.md)). Unauthenticated
operations are keyed by client address, authenticated ones by Device session, and every class also
has a global bucket, which is the actual guarantee. The values are sized so the bursts the design
makes normal -- an offline queue draining, the extension polling for approval, a holding pen of
shared URLs -- pass untouched, and a limited request is a pause the client resumes from, never a
loss.

Ticket [#29](https://github.com/MAX-786/visual-youtube-notes/issues/29). Values:
[`openapi.yaml`](../design/openapi.yaml), `x-rate-limit-classes`. Schema:
[`000010_rate_limit_events.up.sql`](../../deploy/migrations/000010_rate_limit_events.up.sql).

## In process, not in PostgreSQL, and not at the edge

The counters live in the API process. There is exactly one API replica
([ADR 0008](0008-deployment-topology.md)), so a process-local bucket is exact rather than approximate,
and the only thing a PostgreSQL-backed counter would add is surviving a restart -- at the cost of a
write per request on the path that exists to protect the database from request volume. A restart
already resets the far more expensive state, every open event stream, and a twenty-second window of
fresh buckets is not a weakness anyone can plan around.

The edge does nothing. Stock Caddy has no rate limiter without an `xcaddy` plugin build, which
ADR 0008's pull-only image discipline would have to carry forever; Cloudflare's free plan offers one
WAF rate rule, which is worth adding for `/api/auth/login` as belt and braces but cannot express a
per-session policy and is left as an optional step rather than a dependency.

## Per key for fairness, global for the guarantee

Unauthenticated operations -- login, refresh, Pairing -- are keyed by client address, taken from
`CF-Connecting-IP` when present and the socket address otherwise. That header is not trustworthy:
ticket #10 leaves the origin reachable on its public address, so a client that bypasses Cloudflare
can put anything in it. The design does not pretend otherwise. A forged address escapes the
_per-address_ bucket and runs straight into the _global_ one, so what a forger gains is a larger
share of a fixed budget, not an unbounded one. Locking the origin to Cloudflare's ranges would
close that gap and is out of scope for the same reason Full (strict) TLS is: hardening beyond the
destination.

Authenticated operations are keyed by Device session, not by Owner. There is one Owner, so an
Owner-keyed bucket _is_ the global bucket, and two devices draining their queues after a flight
would then starve each other. Per session, each gets its own burst and the global bucket is sized to
admit both.

## Sized against the design's own bursts, not against abuse

Spec 15 asks for limits on authentication and job creation. Tuned for abuse, the obvious values
would throttle behaviour the closed tickets made normal, so each class was sized against the traffic
its operations legitimately produce:

- **`mutation`: 300 burst, one per second.** [ADR 0012](0012-pwa-shell-live-updates-and-offline-cache.md)
  drains an offline editing session serially as fast as the server answers, and the two-second
  autosave debounce means a long session queues hundreds of entries. Past the burst the drainer is
  slowed, not refused: a `429` on a queued mutation is a pause of `Retry-After`, after which the same
  entry is resent. Dropping it would be the silent loss spec 16 forbids, wearing a different hat.
- **`pairing_exchange`: 60 burst, one per second.** [ADR 0013](0013-chromium-extension-structure-and-pairing.md)
  has the extension poll exchange every two seconds for up to five minutes, which is 150 calls per
  Pairing. The limit admits that with room for two browsers pairing at once.
- **`submission`: 20 burst, one per fifteen seconds.** A holding pen of shared URLs drains in one
  go; a client submitting in a loop is bounded to four a minute, which is the only case the refill
  is for.
- **`login`: 10 per address, 30 global, and at most two Argon2id verifications in flight.** The
  memory is the point. [ADR 0003](0003-owner-auth-device-sessions-and-extension-pairing.md) sets
  Argon2id at 64 MiB per verification, and the API container is capped at 1 GB; ten concurrent
  attempts would be more than half of it. The concurrency cap is what protects the process; the
  buckets bound guessing and log noise.
- **`export`: one render at a time.** Gotenberg has one CPU and two gigabytes; a second PDF waits up
  to ten seconds and is then refused rather than queued indefinitely.

Every other value is a generous ceiling on a client bug, and each carries a `note` beside it in the
table saying what it was sized against. None is a measurement.

## Login failures are not counted separately, and there is no lockout

The ticket asked whether authentication failures should be limited differently from successes.
They are not. At one Owner and a handful of devices, successes are so rare that a bucket sized for
failures never notices them, and a separate failure counter is a second mechanism whose only job is
to be slightly more precise about a number that does not matter. What would matter is a lockout,
and there is none: locking the account after N failures hands an attacker a way to lock the Owner
out with N requests. The password's strength is Argon2id plus the Owner's choice of password; the
limiter's job is to keep guessing from costing the server anything, not to be the password.

Refresh reuse is already the harshest response in the system -- the whole Device session is revoked
with no grace window (ADR 0003) -- so the `refresh` class exists only for a client stuck in a loop.

## Search degrades instead of failing

A query embedding costs money and a provider round trip, and a type-ahead client would issue one
per keystroke. Rather than answer `429` to the whole search, the `query_embedding` class is
consulted by the search handler before the embedding call; over the limit the handler skips the
embedding and returns lexical results with `semantic_unavailable: rate_limited`, the same shape
[ADR 0005](0005-hybrid-search-indexing-and-ranking.md) already uses for a provider fault and
[ADR 0007](0007-ai-spend-ledger-and-soft-cap.md) for the Soft cap. The `search` class still bounds
the request itself, which is a database query either way. Three reasons, three wordings: a fault,
a budget the Owner chose, and a pace the Owner exceeded.

## The values live in the contract, and a change is a contract change

"Recorded where the implementation will find them" is taken literally. The API loads
`openapi.yaml` at boot for request validation, so `x-rate-limit-classes` and each operation's
`x-rate-limit` are already in memory; the limiter is built from them and there is no second table
in Go or in the environment to drift. `openapi.test.mjs` asserts that every operation names a
declared class, that unauthenticated operations key by address and authenticated ones by session,
that a global bucket admits at least one key's burst, and that the degrade-mode class is never
attached to an operation directly.

There are deliberately no environment overrides. Changing a value is an edit, a review, and a
deploy, which is the right friction for a single-owner tool: a limit loosened under pressure in
`.env` is a limit nobody remembers loosening.

## The trace a refused burst leaves

A refused login is rejected authentication activity in exactly the sense ADR 0003 retains, so
migration `000010` adds `rate_limited` to the Security event types. One event is written per
`(class, key)` on the transition into the limited state, then at most once per ten minutes while it
persists -- never per refused request, since an insert per `429` would make the limiter the write
amplifier it exists to prevent. The class name travels in `reason_code`, which the existing format
constraint already confines to a safe token. The events are readable at `GET /auth/security-events`,
added to the contract in the same change because the audit table had no read at all.

## What this ADR corrects

Nothing in the spec. Spec 15's sentence asked for limits on authentication and job creation; this
design supplies those and extends the same mechanism to every operation, which the spec does not
forbid.

## What this ADR does not decide

- **Measured values.** Every number is an argued starting point. The first month of real traffic on
  the OCI host, and the `rate_limited` events it produces, are what should move them.
- **Restricting the origin to Cloudflare's address ranges**, which would make the per-address key
  trustworthy. Hardening beyond the destination, like Full (strict).
- **A Cloudflare WAF rule on login.** Cheap and worth doing at deployment; not something the design
  depends on.
- **Per-Owner limits.** Meaningless with one Owner, and the schema already carries `user_id` for
  the day that changes.

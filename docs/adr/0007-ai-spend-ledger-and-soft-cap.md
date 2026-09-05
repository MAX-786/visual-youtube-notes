# Meter AI spend in a call ledger that outlives its Notes

Every billable provider call writes one row to `ai_usage_events`, opened before the call and closed
after it. Providers report units and never money, so money is computed by us from `ai_model_prices`
— versioned per-model price sets — and snapshotted onto the row at completion. The ledger is
standalone: purging a Note nulls its links but keeps its spend, because a month's total must not
fall when notes are deleted. The Soft cap gates a run at the first moment its cost is computable
rather than at enqueue, which is the only point where the number shown to the Owner is the number
they will pay.

## Providers report units; money is always ours

Verified against all three vendors' documentation: Deepgram returns `metadata.duration` in audio
seconds, Anthropic a token breakdown, Voyage a single `usage.total_tokens`. **None returns a
monetary cost on the call.** Only Deepgram can confirm our arithmetic afterwards, through a
Management API that reports `usd` per request.

So spec section 11's "keep versioned per-model price settings" is not a convenience — it is the only
way the product can know what it spent. Two consequences follow:

- **Reconciliation is out of scope for V1.** Reconciling one provider of three produces a total that
  looks authoritative and is not, which is worse than no reconciliation. `provider_request_id` is
  recorded anyway (nullable — Voyage returns no identifier at all), because that is the half that
  keeps reconciliation possible later without a migration, and it costs nothing now.
- **Prices are what the Owner is charged, not vendor list price.** Voyage's first 200M tokens are
  free — roughly six years at this volume — and Deepgram ships $200 of credits. Tracking list price
  would trip a $10 cap on money nobody is being billed. Setting a price to `0` while inside a free
  tier is the intended use of the table.

The migration seeds list prices for the surveyed models anyway, dated to the ticket #3 survey. Without
any price set every unit is unpriced and month-to-date reads `$0.00` — a cap that does nothing until
configured. Seeding a _price_ is not hard-coding a _choice_: the table is owner-editable and
versioned, and switching to an unseeded model degrades exactly as described under "never quietly
incomplete" below. Seeded values are list prices, so a free tier makes the ledger over-count rather
than under-count, which is the safe direction for a threshold.

## One row per call, not per Attempt

The ticket asked for per-Attempt usage records. That grain is wrong in two places, and both are
already-settled parts of the design:

- Generation's in-process re-ask (ADR: ticket #15) makes a **second billable call inside one
  Attempt**, held deliberately apart from River's retry ladder.
- A semantic query embedding has **no Note, no Stage and no Attempt at all**.

So the ledger is keyed on the call. `stage_attempt_id` and `note_id` are recorded where they exist
and left null where they do not. Unit vocabularies are a `jsonb` map rather than an enum, because
they are heterogeneous _and_ open-ended — Anthropic's pricing docs already show a `server_tool_use`
key its own SDK types do not carry, and cache writes price differently per TTL. A fixed enum would
need a migration every time a vendor adds a billable thing.

## The ledger survives Purge

`ai_usage_events` is not a child of anything that gets purged. Had usage lived on `stage_attempts`,
which cascades from `notes`, deleting and purging a Note would have lowered the month's total and
**un-tripped a cap that had already fired**. Instead the links are weak (`ON DELETE SET NULL`) and
`user_id` carries ownership independently, so a purged Note leaves a still-meaningful ledger line.

This has a sharp edge that only showed up when the migration was run: two `SET NULL` foreign keys on
one row, where `stage_attempts` cascades away as part of the same `DELETE` that nulls `note_id`.
Postgres revalidates every foreign key on a row it updates, so the `note_id` update found
`stage_attempt_id` pointing at an Attempt the cascade had already removed, and the Purge failed
outright. The `stage_attempts` reference is therefore `DEFERRABLE INITIALLY DEFERRED`, which lets
both `SET NULL`s land before the check runs. The composite `(id, user_id)` foreign-key pattern used
elsewhere in this schema is deliberately not used here for a related reason: `ON DELETE SET NULL`
would null `user_id` too.

Usage rows are **never purged**. They are tens of megabytes across the product's life, and any
retention window would silently erase a month the Owner might still want to look at.

## Price sets are versioned whole, and charges are snapshotted

A price row is the complete price list for one model at one instant, not one price for one unit. The
version is the set, so a model's prices can never be half-updated.

Immutability is enforced twice over, and both halves are needed. The row carries a `price_id` for
provenance **and** a snapshotted `charge_usd`. The foreign key alone is not enough: an append-only
table with `effective_from` still lets a backdated correction move historical totals. The snapshot
alone would lose the audit trail. Together, aggregation is a plain `SUM` with no join and no price
edit can reach backwards — which the migration test asserts directly by raising a price tenfold and
checking that both the row and the month are unmoved.

Prices are stored as USD for **one** unit (`claude-sonnet-5` input is `0.000002`) in
`numeric(20,12)`. A 20-token query embedding costs $0.0000012; floating point would quietly lose it.

## The cap gates where the estimate first becomes real

Spec section 11 says to "check the estimated charge before enqueue". Taken literally this cannot
work. At enqueue the app holds only a Video ID — not the duration, not whether usable captions
exist — and those two facts are the entire cost. Measured against ticket #15's fixtures, a captioned
30-minute English note costs about **$0.038**, while a two-hour Hindi video needing STT costs about
**$0.75**: a spread near twentyfold between two submissions that look identical at enqueue. Gating on
the ceiling would demand confirmation for routine notes and trip the $10 cap after thirteen of them;
gating on the typical figure would confirm $0.04 and spend $0.75.

The `metadata` and `captions` Stages are free — `yt-dlp`, no AI call. So the gate fires **at the
first moment the estimate is computable, and always before any paid call**:

- First run, and a Retry that failed early → after `captions`, asynchronously. The Owner shared from
  a phone and walked away, so the confirmation reaches them by Web Push.
- Regeneration, and a Retry that failed at `generating` or later → synchronously at submission, since
  the Transcript is already in hand and the Owner is present in the PWA.

One confirmation per **run** (`processing_version`), never per Note: a confirmed run that later fails
and is retried is asked again, because it is new money. Confirmation **recomputes** the estimate
against the price set in force at that moment rather than replaying the stale one, so the number
approved is still the number paid.

**The spec sentence should be corrected rather than reinterpreted by the next reader** — the same
call ADR 0006 made about the attempt ladder. What section 11 wants is that no charge is incurred
without a check; what it says is a place where the check cannot yet be meaningful.

Holds do not expire and do not resume at month rollover. A Note held on 28 September is under cap
again on 1 October, and auto-resuming would mean work shared three weeks ago silently starts
spending. The hold is a question put to the Owner; rolling the month over answers it on their behalf.

## The hold is a flag, not a processing state

`cost_hold_since` and `cost_hold_estimate_usd` sit on `notes`, orthogonal to `processing_state`, for
the same reason ADR 0006 put `cancel_requested_at` there. The Stage machine is intact — the next
Stage really is `pending`, it simply has no River job — and `processing_state` keeps mirroring the
user-visible list in spec 8.4. The card honestly reads "captions done, held for cost confirmation",
because that is what happened.

The hold rides the Note's existing `sync_events` row and reaches every device through machinery that
already exists. Usage rows themselves emit **no** sync events: they change constantly during
processing and would flood an outbox designed for content changes. Clients read them on demand.

## Never quietly incomplete

The ledger may be incomplete. It may not be _quietly_ incomplete, because under-counting is precisely
the failure a spend cap exists to prevent. Three ways it can fall short, each recorded rather than
absorbed:

- **Unpriced units.** A unit the price set has no price for is costed at nothing but preserved in
  `unpriced_units`, and Settings names the model that needs a price. Silently zeroing would mean real
  money spent and the ledger saying nothing.
- **Unknown outcomes.** ADR 0006 accepted that a crash between a provider response and its
  persistence can bill twice. A row opened before such a crash is closed as `unknown` by the
  stale-attempt reaper — money probably spent that can never be attributed — rather than being
  counted as zero.
- **Concurrency overshoot.** Three runs at $9.90 month-to-date each see themselves under cap and all
  proceed. There is no reservation ledger: reserving an estimate and releasing it on completion needs
  a reaper for crashed runs, which is real machinery guarding a threshold the spec already calls
  *soft*. Overshoot is bounded by in-flight paid runs × per-run estimate — with two general workers
  and one media worker, under **$2.25**.

The `ai_monthly_usage` view puts all three counts beside the total, so a month reads "at least $X"
whenever any is non-zero.

Semantic query embeddings pause at the cap, and Search falls back to lexical with
`semantic_unavailable: soft_cap` (already designed in ADR 0005). The Owner's re-enable is a
`semantic_override_month` date holding the first day of the overridden month in the account
timezone, so expiry is an equality test against the current month — no scheduled job, nothing to
forget.

## Which month a call belongs to

`billing_month` is written by the app at insert, from the account timezone in force at that moment.
Computing it at read time from the _current_ timezone would let changing the timezone reshuffle
historical spend across month boundaries and retroactively trip or un-trip a past cap. A timezone
change must not rewrite history, for the same reason a price change must not.

It cannot be a generated column, since it depends on `users.timezone`; the app already holds the
timezone from the cap check it just performed, so it writes the value rather than hiding a
cross-table lookup in a trigger.

## What this ADR does not decide

- **AI role configuration and API key encryption** (spec section 11: provider, model, endpoint and
  encrypted key per Processing role). Price rows key on `(provider, model)` as plain text with no
  foreign key, so nothing here waits on it. Key encryption is a crypto decision that does not belong
  in a cost ADR; it has its own ticket.
- **API paths.** The design implies six operations — month-to-date usage, list held Notes, confirm a
  hold, change the cap, re-enable semantic search for the month, and manage price sets. They are
  named in `docs/design/cost-tracking.md` as inputs to the OpenAPI ticket. Writing half a contract
  into an 8-line stub would constrain that ticket without informing it.
- **Reconciliation against vendor invoices**, as above.

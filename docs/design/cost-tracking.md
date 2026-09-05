# Cost tracking, price configuration, and the Soft cap

Mechanics for wayfinder ticket
[#19](https://github.com/MAX-786/visual-youtube-notes/issues/19). The decisions and their
reasoning are in [ADR 0007](../adr/0007-ai-spend-ledger-and-soft-cap.md); the schema is
`deploy/migrations/000006_ai_usage_and_prices.up.sql`. This file is the part a worker
implementation needs: unit names, estimation formulas, and where the gate sits in the Stage chain.

## Unit vocabulary

Unit maps are `jsonb`, so the vocabulary is a convention rather than an enum (ADR 0007 explains
why). These are the keys V1 uses. A worker must map the provider's response onto them; anything it
does not recognise is passed through unchanged and lands in `unpriced_units`.

| Key                     | Role            | Provider field                                             |
| ----------------------- | --------------- | ---------------------------------------------------------- |
| `audio_seconds`         | transcription   | Deepgram `metadata.duration`                               |
| `input_tokens`          | note generation | Anthropic `usage.input_tokens`                             |
| `output_tokens`         | note generation | Anthropic `usage.output_tokens`                            |
| `cache_read_tokens`     | note generation | Anthropic `usage.cache_read_input_tokens`                  |
| `cache_write_5m_tokens` | note generation | Anthropic `usage.cache_creation.ephemeral_5m_input_tokens` |
| `cache_write_1h_tokens` | note generation | Anthropic `usage.cache_creation.ephemeral_1h_input_tokens` |
| `embedding_tokens`      | embeddings      | Voyage `usage.total_tokens`                                |

Three traps, each verified against vendor documentation:

- **`output_tokens_details.thinking_tokens` must not be added to `output_tokens`.** It is a
  decomposition of a number that already includes it; `output_tokens` is the authoritative billing
  total. Adding it would double-count every generation call.
- **Cache writes are two prices, not one.** `cache_creation_input_tokens` is the sum of the two TTL
  buckets, which bill at 1.25× and 2× base input. Read the buckets, never the sum.
- **Deepgram prices per minute but reports seconds.** The conversion lives in the seeded price
  (`0.0052 / 60`), not at the call site, so a price correction never needs a code change.

Deepgram's `metadata.request_id` and Anthropic's `request-id` header go to `provider_request_id`.
Voyage returns no identifier of any kind, so for embeddings the row's own `id` is the only handle.

## Estimating before the call

Every usage row records `estimated_units` before the call and `reported_units` after, which is what
lets a Note show both figures as spec 7.4 asks. Estimates are also what the cap gate sums.

- **Transcription.** `audio_seconds` = the Source duration from the `metadata` Stage. Effectively
  exact: Deepgram bills submitted audio.
- **Note generation.** Input tokens from the assembled request's character count through the
  script-aware ratios already used by `docs/design/generation/build-request.mjs` — Latin ≈ 3.8
  chars/token, Devanagari between 1.6 and 1.0, applied per character run. Take the **denser** end for
  a gate estimate. Output tokens ≈ 2,000, from the measured fixtures. Because thinking tokens bill as
  output and are not modelled, this estimate is a floor; reported output routinely exceeds it.
- **Embeddings.** `embedding_tokens` ≈ chunk count × 500, plus note-section tokens. Voyage's
  tokenizer has no Go binding, so chunking already runs on the same script-aware estimate
  (`docs/design/search-ranking.md`); only cost tracking uses the vendor's returned count.
- **Query embeddings.** ~20 tokens. Below the resolution of anything, but recorded, because spec 11
  puts query embeddings inside the cap.

Replace the generation ratio with a real `count_tokens` call the day provider keys exist
(ticket #14). It is the one soft number in the chain.

## Where the gate fires

The `metadata` and `captions` Stages cost nothing, so the run can reach a real estimate before it
reaches a paid call. The gate lives inside the Stage completion transaction that would otherwise
enqueue the first paid Stage — the same transaction, holding the same `FOR UPDATE` lock on the Note,
that ADR 0006 uses to settle the cancellation race.

```
in the completion transaction for `captions` (or at submission, for Regeneration):
    lock the Note FOR UPDATE
    if cancel_requested_at is set        -> stop, as ADR 0006 already requires
    estimate = projected charge for every remaining paid Stage of this run
    month     = ai_monthly_usage for (user, current billing month)
    if month.charged_usd + estimate <= soft_cap_usd:
        enqueue the next Stage           -> the common case, no interaction at all
    else:
        set cost_hold_since = now(), cost_hold_estimate_usd = estimate
        do NOT enqueue                   -> the chain stops here
        emit the Note's sync event and a Web Push notification
```

Confirmation is the mirror image: clear both hold columns, **recompute** the estimate against the
price set in force now, and enqueue the Stage that was withheld. The run continues on the same
`processing_version`, so its Stage rows and Attempt budgets are untouched.

Where the estimate is already computable at submission — Regeneration, or a Retry that failed at
`generating` or later — the gate runs synchronously in the submission handler instead, and the
client can present the confirmation inline rather than by notification.

The projection is not persisted unless it gates. A Note's "estimated cost" comes from summing
`estimated_units` on its own usage rows, priced at read time; only a hold needs a frozen figure, and
`cost_hold_estimate_usd` is it.

## Query embeddings at the cap

Search checks the cap before embedding the query. Over cap, and with no
`semantic_override_month` matching the current month, it skips the embedding entirely and returns
lexical results with `semantic_unavailable: soft_cap` — the response field ADR 0005 already defines.
The client wording must differ from `provider_error`: one is a fault, the other is a budget the
Owner chose.

Re-enabling stamps `semantic_override_month` with the first day of the current month in the account
timezone. It lapses on its own when the month changes, because the check is an equality test and
nothing needs to clean it up.

## The reaper

A row opened before a worker crash never closes on its own. The stale-attempt reaper that ADR 0006
already relies on to drive Stages terminal also closes such rows as `outcome = 'unknown'`. Those rows
carry no charge and are counted separately by `ai_monthly_usage.unknown_calls`, so the month reads
"at least $X" rather than pretending the call never happened.

`ai_usage_events_in_flight_idx` is the index it sweeps.

## Worked example

A 30-minute English video with usable captions, at seeded prices, on a cold prompt cache — which is
the normal case, since five saves spread through a day rarely fall inside the 5-minute cache window:

| Call           | Reported units                | Charge     |
| -------------- | ----------------------------- | ---------- |
| generation     | 3,059 `cache_write_5m_tokens` | $0.0076    |
| generation     | 4,915 `input_tokens`          | $0.0098    |
| generation     | 1,952 `output_tokens`         | $0.0195    |
| embeddings     | ~18,000 `embedding_tokens`    | $0.0011    |
| **Note total** |                               | **$0.038** |

The same video in Hindi with no usable captions, worst-case tokenizer band:

| Call           | Reported units             | Charge     |
| -------------- | -------------------------- | ---------- |
| transcription  | 1,800 `audio_seconds`      | $0.1560    |
| generation     | 3,059 + 11,631 + 1,952     | $0.0505    |
| embeddings     | ~18,000 `embedding_tokens` | $0.0011    |
| **Note total** |                            | **$0.208** |

The generation figures reproduce `node docs/design/generation/build-request.mjs` exactly. Note that
its headline $4.50–$5.30/month band assumes cache **reads**; on cold cache the same fixtures cost
$0.037 rather than $0.030 per note, so 150 notes is nearer $5.70. Either way an all-captions month
sits inside the $10 cap and an all-STT month does not — a 5.5× gap between two videos that look
identical at submission. That asymmetry is the whole reason the gate waits until it knows which
shape it is looking at.

## Operations this implies

Named here for the OpenAPI ticket, which owns their paths and shapes:

1. **Month-to-date usage** — `ai_monthly_usage` for the current month plus the three
   unaccounted-for counts, and a per-model breakdown for Settings.
2. **Per-note cost** — estimated and reported, from the Note's usage rows; part of the note metadata
   response in spec 7.4.
3. **List held Notes** — everything with `cost_hold_since` set.
4. **Confirm a hold** — by `(note_id, processing_version)`; recomputes, clears, enqueues.
5. **Cost settings** — read and update `soft_cap_usd`; re-enable semantic search for the month.
6. **Price sets** — list, and append a new version. Never an in-place update: a correction is a new
   row with a later `effective_from`.

## Follow-on work this leaves open

- **AI role configuration and encrypted provider keys** (spec 11) — its own ticket. Price rows key on
  `(provider, model)` as plain text, so nothing here waits on it.
- **Reconciliation against vendor billing.** Only Deepgram exposes per-request `usd`.
  `provider_request_id` is recorded so this stays possible without a migration.
- **Voyage's free-tier counter.** 200M tokens is roughly six years at this volume and the docs never
  say whether it renews. Until it matters, the seeded list price over-counts, which is the safe
  direction.

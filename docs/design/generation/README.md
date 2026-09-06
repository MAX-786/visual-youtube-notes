# Note generation: prompt, structured output, validation and repair

Prototype for [#15](https://github.com/MAX-786/visual-youtube-notes/issues/15). Throwaway
code that answers one question: **when the note model gets something wrong, can the worker
fix it locally, or does it have to pay to ask again?**

Open `generation-prototype.html` by double-clicking it. It runs the real validator against a
real transcript, with six walkthroughs covering a clean run, a free repair, and four
failures. No install, no server, no API key.

## The shape of the answer

### Two schemas, not one

[`diagram.schema.json`](../diagram.schema.json) stays the normative storage contract. It
uses `pattern`, `minLength`, `minimum`, `oneOf` and `if`/`then` — and Anthropic's
constrained decoding supports none of those. So the schema sent to the model is a **derived
decode-safe subset**, [`note-request.schema.json`](./note-request.schema.json), restricted to
`additionalProperties: false`, `enum`, `const`, `anyOf`, `$ref` and `minItems` of 0 or 1
(verified against the live structured-outputs docs, 2026-09-05).

Constrained decoding therefore guarantees the _shape_ and nothing else. Everything the
subset cannot express falls to [`validate-note.mjs`](./validate-note.mjs): the 40-word
summary, the 5–7 / 3–8 / 0–5 array ranges, the 12-visible / 24-total element caps, the 4×6
comparison cap, id shape and uniqueness, contiguous `order`, mind-map tree structure,
`char_end > char_start`, and English output.

The two schemas can drift. They are checked against each other only by
`toStoredPayload()`, which builds a storage-shaped payload from a request-shaped one; a
build-time equivalence check is worth adding when this ports to Go.

### Grounding: the model quotes, the worker locates

A source reference from the model is `{chunk, quote}` — a short handle (`c1`, `c2`, …) and a
span **copied verbatim** from that chunk. The worker string-matches the quote (whitespace
tolerant) to derive `char_start`/`char_end` and swaps the handle for the chunk's UUID.

This was the main design choice, over having the model emit character offsets directly:

- **It makes hallucinated grounding provable.** A quote that is not in the chunk it names is
  a fact about the text, not a judgement call. Offsets are unverifiable — a wrong-but-plausible
  `char_start` is indistinguishable from a right one.
- **No UUIDs in model output.** 36-character ids repeated ~34 times per note are pure token
  cost and an invitation to hallucinate. (`format: "uuid"` _is_ supported by constrained
  decoding, so this is a cost and reliability choice, not a capability limit.)
- Offsets are **chunk-relative**, so they survive anything that does not rewrite that chunk's
  text.

Cost: quotes are output tokens. The sample note carries 34 references in ~2k output tokens.

### The model supplies meaning; the worker supplies mechanics

The request schema deliberately omits `schema_version`, `provenance`, `initially_visible`,
`initially_collapsed` and `layout`. Everything the model returns is `ai` by definition, and
positions come from the client's first automatic layout. `toStoredPayload()` stamps them.

### Repair, then one re-ask, then fail

Findings carry `repairable`. Repairs are deterministic and content-preserving, so they cost
nothing:

| Repaired in process               | How                                                                         |
| --------------------------------- | --------------------------------------------------------------------------- |
| `order` not contiguous            | renumber 0..n-1, sequence preserved                                         |
| duplicate or malformed element id | rename, rewriting every reference                                           |
| more than 5 practical actions     | truncate (the spec caps it; the model already ordered them)                 |
| more than 12 visible elements     | auto-collapse the overflow behind an anchor, as spec 7.2 already prescribes |
| `group_id` not stamped            | take it from collapsed-group membership                                     |

Everything else is content only the model can supply — a missing quote, a shortfall of key
points, a note in the wrong language, a mind map that is not a tree — and costs one re-ask
with the validator's own messages fed back.

**The in-process re-ask budget is deliberately not the River attempt ladder.** Spec 8.4 makes
validation failures permanent, and its 30 s / 2 min / 10 min backoff exists for network
trouble. A schema-invalid response should not sit in that ladder, and should not consume a
budget meant for transient errors. So: repair for free, re-ask at most once inside the
generating stage (mostly cache reads, since the prefix is unchanged), then fail the stage
permanently.

### Request wiring

[`build-request.mjs`](./build-request.mjs) assembles the Anthropic-shaped request this
prototype was measured with; `fixtures/sample-request.json` is the result. Since
[ADR 0017](../../adr/0017-run-every-processing-role-on-openai.md) the worker sends the
same system prompt, transcript and schema to OpenAI's Responses API instead:
`gpt-5.6-terra`, `max_output_tokens: 16000`, `reasoning: {effort: "low"}`, and the schema
under `text.format` as `{type: "json_schema", name, schema, strict: true}`. Strict mode
rejects `minItems`, so the Go request builder strips it from `note-request.schema.json`
before sending; the counts were always the validator's job. A `refusal` output item is a
permanent failure of the Attempt.

Prompt caching is automatic on OpenAI: the system prompt plus schema are the stable prefix,
the transcript is not. Changing the schema still invalidates the cache.

## Measured cost

`node build-request.mjs`. **These are estimates, not measurements** — ticket #14 defers real
keys, so there is no `count_tokens` call. Character counts are measured; the chars-per-token
ratio is assumed, and shown as a band because Devanagari tokenizes far denser than Latin.
Thinking tokens bill as output and are **not** counted, so the output column is a floor.

| Fixture                 | Devanagari | Input tok    | Output tok | Sonnet 5     | Haiku 4.5    |
| ----------------------- | ---------- | ------------ | ---------- | ------------ | ------------ |
| English, human captions | 0%         | 4,915        | 1,952      | $0.037       | $0.019       |
| Hindi, ASR              | 77%        | 7,596–11,631 | 1,952      | $0.042–0.050 | $0.021–0.025 |
| Hinglish, ASR           | 76%        | 8,169–12,497 | 1,952      | $0.044–0.052 | $0.022–0.026 |

At the spec's five saves a day, note generation alone runs **$4.50–$5.30/month** on Sonnet 5
and about half that on Haiku 4.5 — below the $8.10 the provider survey projected, because
that assumed 12k input and 3k output per note against a measured ~5–12k and ~2k. The $10
soft cap covers STT and embeddings too, so the headroom is not all ours.

Hindi and Hinglish cost roughly 35–55% more input than English for the same video length.
Real, but not the multiple the byte-level tokenization penalty might suggest.

## Files

| File                        | What it is                                                                                                            |
| --------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `system-prompt.md`          | The system prompt, verbatim. Frozen, cached.                                                                          |
| `note-request.schema.json`  | Decode-safe schema sent as `output_config.format`.                                                                    |
| `validate-note.mjs`         | **The part worth keeping.** Pure module: validate, repair, the attempt machine, request→storage mapping. Ports to Go. |
| `build-request.mjs`         | Request assembly + the cost estimate above.                                                                           |
| `normalize-transcript.mjs`  | WebVTT → normalized chunked transcript. Provisional chunking.                                                         |
| `check.mjs`                 | `node check.mjs <transcript> <note>` — run one fixture through the cycle.                                             |
| `build-prototype.mjs`       | Derives the failure cases and builds the HTML.                                                                        |
| `generation-prototype.html` | Generated. The thing to click.                                                                                        |
| `fixtures/`                 | Three real transcripts, one hand-authored valid note, the sample request.                                             |

Transcripts came from `yt-dlp` 2026.08.19 (with Deno 2.9.6 for YouTube's JS runtime
requirement): [`aircAruvnKk`](https://www.youtube.com/watch?v=aircAruvnKk) human English
captions, [`uiZL9rK2Q_Q`](https://www.youtube.com/watch?v=uiZL9rK2Q_Q) Hindi dub via
`hi-orig`, [`HQnoYzxOHMw`](https://www.youtube.com/watch?v=HQnoYzxOHMw) Hinglish via
`hi-orig`. The Hinglish case is Devanagari carrying English loanwords (कंपनी, वैल्यूज), not
romanized Hindi — worth knowing before anyone writes a script detector.

## What this does not settle

- **The model was never called.** No keys (#14). The valid note is hand-authored against the
  real transcript — which does exercise the validator honestly, but says nothing about
  whether `claude-sonnet-5` at `effort: low` actually produces good notes, picks the right
  diagram type, or gets `speaker_claim` right. That needs a real call on day one of
  deployment, along with `count_tokens` to replace the estimates and a check that `$defs`
  (rather than `definitions`) is accepted.
- **Chunking parameters** stay open, and belong to the not-yet-specified chunking ticket.
  Generation depends only on "chunks have stable ids and chunk-relative offsets".
- **Spec 8.2.3's 40% repeated-line rule** needs a decision on the pipeline ticket, see below.

## Handover: a measured spec problem

Spec 8.2.3 rejects captions when "repeated-line content is below 40%" fails. Measured on the
two real ASR tracks, raw repetition is **66%** — YouTube's rolling captions re-emit each line
two or three times as the window scrolls. Read literally, the rule rejects every
auto-generated caption track and sends every Hindi and Hinglish video to STT, which the cost
model does not carry.

After adjacent-line dedup the same tracks measure **0.3% and 0.5%**. So the threshold has to
be evaluated _after_ rolling-caption normalization, where 40% still catches what it was meant
to catch — degenerate `[Music]`-style tracks. This belongs to
[#18](https://github.com/MAX-786/visual-youtube-notes/issues/18).

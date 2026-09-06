# Run every Processing role on OpenAI

The Owner holds an OpenAI key and no Anthropic, Deepgram or Voyage account, so all three active
Processing roles start on OpenAI: `gpt-5.6-terra` through the Responses API with structured outputs
for note generation, `gpt-transcribe` through the audio transcription API for speech to text, and
`text-embedding-3-large` shortened to 1024 dimensions for embeddings. This supersedes the provider
choices in the [#3 survey](https://github.com/MAX-786/visual-youtube-notes/issues/3), the seeded
defaults of [ADR 0010](0010-ai-role-configuration-and-key-encryption.md), and the price seeds of
[ADR 0007](0007-ai-spend-ledger-and-soft-cap.md). Nothing structural changes: a role is still one
provider and one model, keys are still sealed per row, and the ledger still prices units the provider
reports.

Ticket [#30](https://github.com/MAX-786/visual-youtube-notes/issues/30). Schema:
[`000011_openai_roles.up.sql`](../../deploy/migrations/000011_openai_roles.up.sql). Every price and
limit below was read from `developers.openai.com` on 2026-09-06, not from memory.

## The choices, per role

**Note generation: `gpt-5.6-terra`, `reasoning.effort: low`, `max_output_tokens: 16000`.** At $2 per
million input tokens, $0.20 cached and $12 output it sits where `claude-sonnet-5` sat ($2/$10), so
the cost model in `docs/design/cost-tracking.md` carries over almost unchanged: about $0.034 for a
captioned thirty-minute note. Structured outputs are supported and the reasoning effort ladder runs
`none, low, medium, high, xhigh, max`, so ticket #15's choice of low effort maps directly. The
context window is 1,050,000 tokens, which no two-hour Transcript approaches. `gpt-5.6-luna`
($0.20/$0.02/$1.20) is seeded beside it as the cheap alternative the Owner can switch to in Settings;
it was not chosen as the default because no model here has produced a note yet and the more capable
one is the safer first measurement.

**Transcription: `gpt-transcribe`, `languages: ["en", "hi"]`.** $0.0045 per minute of audio, the
cheapest OpenAI transcription model and 25% below `gpt-4o-transcribe`. It takes multiple language
hints, which is what the English, Hindi and Hinglish envelope needs; the hints are a starting value
and are the first thing to revisit if Hinglish comes back romanized. The request limit is 25 MB per
file, and the accepted containers are mp3, mp4, mpeg, mpga, m4a, wav and webm -- so yt-dlp's Opus
audio must be fetched in its webm container rather than as bare `.opus`. Spec 8.2's twenty-minute
chunks with fifteen seconds of overlap already keep every chunk far inside 25 MB.

**Embeddings: `text-embedding-3-large`, `dimensions: 1024`.** $0.13 per million tokens, 8,192-token
inputs, and a `dimensions` parameter that truncates the 3,072-dimension vector, so
`search_embeddings.embedding` stays `vector(1024)` and no migration touches the column. The
`dimensions` value must equal the column's dimension; the `000008` test now reads it under either
`dimensions` or the old `output_dimension` key so that check keeps running.

## What the switch changes in the design

- **Unit vocabulary.** The Responses API reports `usage.input_tokens`, which _includes_ cached
  tokens, with the cached count in `usage.input_tokens_details.cached_tokens`. Because cached input
  bills at a tenth of the rate, the worker records the **uncached** portion under `input_tokens` and
  the cached count under `cached_input_tokens`, so costing stays one multiply per unit.
  `usage.output_tokens` already includes reasoning tokens; `output_tokens_details.reasoning_tokens`
  is a breakdown and must never be added again -- the same trap ADR 0007 recorded for thinking
  tokens. Transcription bills per minute of audio, so `audio_seconds` is the measured duration of
  the chunk sent, taken from the response's `usage` when present and from ffprobe otherwise.
  Embeddings report `usage.total_tokens`, recorded as `embedding_tokens` as before.
- **Request shape.** The schema goes in `text.format` as `{type: "json_schema", name, schema,
strict: true}`. Strict mode requires every property to be listed in `required` and
  `additionalProperties: false` on every object, both of which `note-request.schema.json` already
  satisfies; it does **not** accept `minItems`, `maxItems`, `pattern` or `format`, so the Go request
  builder strips `minItems` from the schema it sends. Nothing is lost: the counts were never the
  schema's to enforce, `validate-note.mjs` and its Go port own them. A refusal arrives as an output
  item of type `refusal` and is treated as a permanent failure of the Attempt.
- **Prompt caching** is automatic on OpenAI rather than marked with a breakpoint, and the system
  prompt plus schema still form the stable prefix.
- **Verification** uses `GET /v1/models`, which is free, so verifying a key writes no ledger row.
- **Price seeds.** New rows for the four models, dated 2026-09-06 and citing the pricing page. No
  batch row is seeded for embeddings because the pricing page does not state a separate batch
  price; the admin reindex bills at the sync rate until one is confirmed and appended.

## What stays exactly as it was

One model per role with no fallback list, the `request_options` passthrough, AES-256-GCM sealing
with the row's identity as AAD, the boot refusal on a missing key version, the `ai_roles_blocking_submission`
view, the Soft cap gate after captions, and the ledger's grain of one row per call. ADR 0010's
argument for a single model per role was about request parameters travelling with the model, and
it holds here too: `reasoning.effort` is a GPT-5-family parameter, and switching to a model that
rejects it is a configuration change the Owner makes with the model, not a fallback the worker
improvises.

## What this ADR does not decide

- **Whether these models produce good notes.** As with the survey's choices, none has been called
  yet. The first real note on the deployed host is the measurement, and Settings → AI is where the
  answer becomes a different model.
- **Hinglish handling in `gpt-transcribe`.** The language hints are documented but their effect on
  Devanagari with English loanwords is not; the Hinglish fixture is the test.
- **The batch price for embeddings**, left unseeded rather than guessed.

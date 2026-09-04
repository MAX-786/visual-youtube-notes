# AI providers per processing role

Resolves wayfinder research ticket [#3](https://github.com/MAX-786/visual-youtube-notes/issues/3). Surveyed 2026-09-04 against official pricing pages and docs only; every price and model identifier below was read from the cited URL during this survey. Where an official page did not state a fact, the cell says **not verified**. Prices are USD and change without notice; the app keeps versioned per-model prices in Settings (spec section 11) rather than trusting this file.

Usage envelope (spec section 2): up to 5 videos/day, 30 min typical, 2 h max, EN/HI/Hinglish input, English notes. Monthly figures below assume **150 videos/month at 30 min** and a worst-case day of **5 x 120 min**.

Roles covered: transcription (STT), note generation (structured JSON), embeddings. Visual analysis is V1.1 and out of scope.

## 1. Transcription (EN / HI / Hinglish)

STT runs only when no usable caption track exists (spec 8.2), so real spend is a fraction of the "every video" figures below.

| Provider / model | Price (unit) | Upload / duration limits | Sync timeout / async | Hindi | Hinglish (code-switch) | Timestamps | Idempotency key | Data retention |
|---|---|---|---|---|---|---|---|---|
| Deepgram `nova-3`, `language=multi` [1][2][3] | $0.0052/min pay-as-you-go (monolingual `hi`: $0.0043/min) [1] | Max 2 GB per request [3] | Sync requests over 10 min of processing (Nova) return `504`; callback URL supported for async [3] | Yes, `hi` listed for `nova-3` [2] | `multi` mode covers en, es, fr, de, **hi**, ru, pt, ja, it, nl for code-switching [2]; no explicit "Hinglish" statement | Word timestamps, utterances, paragraphs: **not verified** (reference page 404'd) | **not verified** | "Deepgram does not store transcripts, so the API response is the only opportunity to retrieve the transcript" [3] |
| AssemblyAI `universal-2` (legacy alias `nano`) [4][5][6] | $0.15/hr [4] | Up to 5 GB per request via URL, 2.2 GB via `/v2/upload`; 160 ms to 10 h per file [6] | Async job: `POST /v2/transcript`, poll `GET /v2/transcript/{id}` or webhook [6] | Yes, `hi` in the 99-language list [5] | Not documented for Hindi; only "Spanglish" is named as a code-mixed variant [5] | Word and utterance `start`/`end` in ms [6] | **not verified** | Transcripts persist until deleted via the delete endpoint; no automatic TTL stated [6] |
| AssemblyAI `universal-3.5-pro` (legacy alias `best`) [4][5] | $0.21/hr [4] | As above | As above | Yes, `hi` in the 18-language list [5] | Not documented | As above | **not verified** | As above |
| OpenAI `gpt-4o-mini-transcribe` [7][8] | $0.003/min [7] | 25 MB per file; split larger files, avoid mid-sentence cuts [8] | Sync HTTP; no async job endpoint documented [8] | Language list for the gpt-4o models not enumerated on the guide; `whisper-1` points to a separate 98-language list [8] (Hindi **not verified** this run) | Not documented | Only `whisper-1` supports `timestamp_granularities` with `verbose_json` [8] | **not verified** | Abuse-monitoring logs up to 30 days; `/v1/audio/transcriptions` is ZDR-eligible; not used for training [9] |
| OpenAI `gpt-transcribe` [7][8] | $0.0045/min [7] | 25 MB [8] | Sync [8] | Uses plural `languages` param [8]; Hindi **not verified** | Not documented | Not for this model [8] | **not verified** | As above [9] |
| OpenAI `gpt-4o-transcribe` / `whisper-1` [7][8] | $0.006/min each [7] | 25 MB [8] | Sync [8] | `whisper-1`: 98-language list referenced [8] | Not documented | `whisper-1` only [8] | **not verified** | As above [9] |
| ElevenLabs Scribe v2 [10] | $0.22/hr on every tier [10] | **not verified** | **not verified** | **not verified** | **not verified** | **not verified** | **not verified** | **not verified** |
| Sarvam `saaras:v3` / `saaras:v4` [11] | Pricing page not fetched: **not verified** | **not verified** | REST and Batch APIs exist [11] | `hi-IN`, `en-IN` and 21 other Indic codes [11] | Docs mention code-mixed output (English plus native script) [11]; not labelled Hinglish | Chunk-level only; "word-level timestamps are not supported" [11] | **not verified** | **not verified** |
| Google Gemini audio input (e.g. `gemini-3.8-flash`) [12][13] | Token-priced; audio tokens-per-second **not verified** this run | 1,048,576 input tokens [13] | Sync; Batch API supported [13] | Multilingual model; STT-specific Hindi accuracy **not verified** | Not documented | Not a native STT product | **not verified** | Free tier: "Content used to improve our products" [12]; paid tier terms **not verified** |
| Groq-hosted Whisper | Pricing page fetch returned marketing content only: **not verified** | | | | | | | |

Formats: spec 8.2 prefers Opus/M4A without re-encoding. OpenAI accepts m4a but lists no Opus/OGG [8]; Sarvam lists OGG, OPUS and M4A [11]; Deepgram and AssemblyAI format lists were **not verified** this run (AssemblyAI says "most common audio and video formats" [6]).

### Estimated monthly cost, transcription (if every video needed STT)

4,500 min/month (75 h). Worst day: 600 min (10 h).

| Candidate | Arithmetic | Month | Worst day |
|---|---|---|---|
| OpenAI `gpt-4o-mini-transcribe` | 4,500 x $0.003 | $13.50 | $1.80 |
| AssemblyAI `universal-2` | 75 x $0.15 | $11.25 | $1.50 |
| AssemblyAI `universal-3.5-pro` | 75 x $0.21 | $15.75 | $2.10 |
| ElevenLabs Scribe v2 | 75 x $0.22 | $16.50 | $2.20 |
| Deepgram `nova-3` `hi` | 4,500 x $0.0043 | $19.35 | $2.58 |
| OpenAI `gpt-transcribe` | 4,500 x $0.0045 | $20.25 | $2.70 |
| Deepgram `nova-3` `multi` | 4,500 x $0.0052 | $23.40 | $3.12 |
| OpenAI `gpt-4o-transcribe` / `whisper-1` | 4,500 x $0.006 | $27.00 | $3.60 |

Free credits: Deepgram $200 [1]; AssemblyAI $50 [4].

### Recommended starting choice: Deepgram `nova-3` with `language=multi`

Reasons:
- It is the only surveyed provider whose docs explicitly list Hindi inside a code-switching mode [2], which is the Hinglish requirement. AssemblyAI supports Hindi but documents no Hindi-English mixing [5]; OpenAI's newer transcribe models do not enumerate languages on the guide [8].
- No transcript storage on the provider side [3], which suits the "audio never retained" stance in spec 8.2.
- 2 GB upload limit [3] means a 2 h Opus file never needs splitting for size.
- Cost is inside the envelope: $23.40/month even if every video needed STT; realistic spend is far lower because most saves will have captions.

Configure AssemblyAI `universal-2` as the first alternative to A/B on real Hinglish audio: cheapest per hour ($0.15/hr [4]), 10 h per-file limit [6], async job plus webhook fits the River worker naturally.

Facts later tickets need (chunking, timeouts, cost config):
- Deepgram sync requests that take more than 10 minutes of processing return `504` [3]. Either use the callback URL mode or keep chunks well under that; the spec's 20-minute chunk ceiling is fine for size but the per-attempt timeout must assume 10 min for a sync call.
- Pricing unit is minutes of audio (Deepgram, OpenAI) or hours (AssemblyAI, ElevenLabs). The price table needs a `per_audio_minute` unit alongside `per_1M_tokens`.
- OpenAI models cap at 25 MB per file [8], so the FFmpeg 20-min split path in spec 8.2 is mandatory if the owner ever switches to OpenAI STT.
- Word-level timestamps: verified for AssemblyAI [6]; only `whisper-1` at OpenAI [8]; not offered by Sarvam [11]; Deepgram **not verified**.
- No surveyed STT API documented an `Idempotency-Key` header; the at-least-once boundary in spec 8.4 stands for this role.

## 2. Note generation (structured JSON, spec 7.3)

Assumptions for cost: 150 calls/month; typical call 12k input tokens (30-min transcript plus prompt and schema) and 3k output tokens; heavy case 60k input and 4k output (2 h Hindi transcript). Token counts are estimates, not measured; Anthropic notes the 4.7+ tokenizer "produces approximately 30% more tokens for the same text" than earlier models [16], and Devanagari density was **not verified**. Measure with the token-counting endpoint before setting per-model prices.

| Provider / model | Input / cached / output $/MTok | Batch $/MTok | Context / max output | Structured output | Schema limits | Idempotency key | Data retention | Hindi |
|---|---|---|---|---|---|---|---|---|
| Anthropic `claude-sonnet-5` [14][15][16] | $2 / $0.20 cache read / $10 [16] | $1 / $5 [16] | 1M / 128K [15] | `output_config.format` `json_schema`; "guarantee schema-compliant responses through constrained decoding" [17] | `additionalProperties: false` required on every object; `enum`, `const`, `anyOf`/`allOf`, `$ref`; array `minItems` only 0 or 1; no `minimum`/`maximum`, no `minLength`/`maxLength`, no recursion, no `pattern` [17] | None documented on `POST /v1/messages` [19] | Inputs/outputs deleted within 30 days by default [20]; ZDR available; Sonnet 5 is not a Covered Model so ZDR-eligible [21]; not used for training without permission [21] | Hindi 96.7% of English on translated MMLU (Sonnet 4.5 figure; Sonnet 5 not benchmarked on that page) [22] |
| Anthropic `claude-haiku-4-5` [14][15][16] | $1 / $0.10 / $5 [16] | $0.50 / $2.50 [16] | 200K / 64K [15] | Same mechanism; `claude-haiku-4-5-20251001` in the supported list [17] | As above [17] | None documented [19] | As above [20][21] | Hindi 92.4% of English (Haiku 4.5) [22] |
| Anthropic `claude-opus-5` [14][15][16] | $5 / $0.50 / $25 [16] | $2.50 / $12.50 [16] | 1M / 128K [15] | Supported [17] | As above [17] | None documented [19] | As above [20][21] | Not benchmarked on that page |
| OpenAI `gpt-5.6-terra` [23][24] | $2 / $0.20 / $12 [24] | 50% discount, 24 h window [26] | 1,050,000 / 128,000 [24] | `structured_outputs` listed [24]; docs say the model "will always generate responses that adhere to your supplied JSON Schema" [25] | Detailed subset not enumerated on the guide page fetched (**not verified**); recursive `$ref` shown in examples [25] | **not verified** | Abuse logs up to 30 days; `/v1/responses` ZDR-eligible "with limitations"; not used for training since 2023-03-01 [9] | **not verified** |
| OpenAI `gpt-5.6-luna` [23] | $0.20 / $0.02 / $1.20 [23] | 50% [26] | **not verified** | **not verified** for this model | **not verified** | **not verified** | As above [9] | **not verified** |
| Google `gemini-3.8-flash` [12][13][27] | $0.75 / cached **not verified** / $3.75 through 2026-12-31, then $1.50 / $7.50 [12] | $0.375 / $1.875 through 2026-12-31 [12] | 1,048,576 / 65,536 [13] | Structured outputs supported [13]; docs say responses "adhere to a provided JSON Schema" [27] | Supports `properties`, `required`, `additionalProperties`, `enum`, `format`, `minimum`/`maximum`, `minItems`/`maxItems`, `anyOf`, `$ref`; "very large or deeply nested schemas may be rejected" [27] | **not verified** | Free tier: content used to improve products [12]; paid-tier retention **not verified** | **not verified** |
| Google `gemini-3.5-flash-lite` [12] | $0.30 / **not verified** / $2.50 [12] | $0.15 / $1.25 [12] | **not verified** | **not verified** | As above if supported | **not verified** | As above | **not verified** |

Anthropic-specific facts that matter for this role:
- Request size limit 32 MB on the Messages API; SDK default timeout 10 min; use streaming or the Batch API for long requests [18].
- Batch API: most batches finish within 1 hour, results available after all requests complete or 24 h, results retained 29 days, 256 MB / 100,000 requests per batch [28]. The 29-day server-side storage makes batches ineligible for ZDR [21].
- Prompt caching: cache reads are 0.1x base input on Sonnet 5 ($0.20/MTok); 5-minute cache writes cost 1.25x [16]. Only the fixed system prompt and schema are reusable here; the transcript changes every call.
- Changing `output_config.format` invalidates the prompt cache; the compiled grammar is cached for 24 h from last use [17].
- Structured outputs do not enforce `minItems` above 1 or `maxItems` [17], so the "five to seven key points" and "three to eight details" rules in spec 7.3 must be validated app-side and repaired/retried as the spec already requires.

### Estimated monthly cost, note generation

Typical: 150 x (12k in + 3k out) = 1.8M input + 0.45M output tokens. Heavy: 150 x (60k in + 4k out) = 9M input + 0.6M output.

| Candidate | Typical arithmetic | Typical | Heavy | Batch (typical) |
|---|---|---|---|---|
| `claude-haiku-4-5` | 1.8 x $1 + 0.45 x $5 | $4.05 | $12.00 | $2.03 |
| `claude-sonnet-5` | 1.8 x $2 + 0.45 x $10 | $8.10 | $24.00 | $4.05 |
| `claude-opus-5` | 1.8 x $5 + 0.45 x $25 | $20.25 | $60.00 | $10.13 |
| `gpt-5.6-terra` | 1.8 x $2 + 0.45 x $12 | $9.00 | $25.20 | $4.50 |
| `gpt-5.6-luna` | 1.8 x $0.20 + 0.45 x $1.20 | $0.90 | $2.52 | $0.45 |
| `gemini-3.8-flash` (2026 price) | 1.8 x $0.75 + 0.45 x $3.75 | $3.04 | $9.00 | $1.52 |
| `gemini-3.5-flash-lite` | 1.8 x $0.30 + 0.45 x $2.50 | $1.67 | $4.20 | $0.84 |

Caveat: Claude Sonnet 5 and Opus 5 run adaptive thinking by default and thinking tokens bill as output [14]; the 3k output assumption may be low unless `output_config.effort` is set to `low` or `medium` for this extraction-style task.

### Recommended starting choice: Anthropic `claude-sonnet-5` with structured outputs

Reasons:
- Constrained decoding guarantees a parseable, schema-valid object [17], which removes the JSON-repair loop from the common path and leaves only semantic validation (counts, grounding, language) to the app.
- 1M context and 128K output [15] mean a 2 h Hindi transcript never needs to be split for the model, so "chunk without losing section boundaries" (spec 8.3) can be a single call with chunk IDs embedded in the prompt for `source_refs`.
- Hindi holds 96.7% of English performance on the published benchmark for the prior Sonnet generation [22]; the multilingual guide advises submitting native script rather than transliteration [22], which matches the spec's "preserve original script" rule for transcripts.
- Retention terms are the cleanest of the three vendors for a personal tool: default deletion within 30 days, ZDR available, no training [20][21].
- Typical spend ($8.10/month) fits the $10 soft cap only if STT is rare; set `effort: low` and keep Haiku 4.5 (`claude-haiku-4-5`, $4.05/month) as the configured fallback the owner can switch to from Settings. Do not use the Batch API for the main path: its 24 h window breaks the under-2-minute target in spec 8.4; it is a fine option for bulk regeneration after a prompt change.

Facts later tickets need:
- Anthropic exposes no idempotency key on `POST /v1/messages` [19]; the stage/attempt record plus response fingerprint in spec 8.4 is the only dedupe. The SDK retries 408/409/429/5xx twice by default [14][18], so set `max_retries` to 0 in the worker and let the Attempt machinery own retries, or accept double billing inside one Attempt.
- Every object in the schema needs `additionalProperties: false`; enums must be plain strings; no numeric or string length constraints [17]. Write the 7.3 schema accordingly and validate word limits (40-word summary) and array lengths in Go.
- Rate limits at the Start tier: 1,000 RPM, 2,000,000 input tokens/min, 400,000 output tokens/min for Sonnet 5 [29]; monthly spend cap $500 at Start [29]. Neither will bind at 5 videos/day.
- `max_tokens` must be set explicitly; use streaming for anything long [18]. A non-streaming call with `max_tokens` around 8k-16k is safe for this schema.
- Price table units: `per_1M_input_tokens`, `per_1M_output_tokens`, `per_1M_cache_read_tokens`, `per_1M_cache_write_tokens`, with a `batch_multiplier` of 0.5.

## 3. Embeddings

Volume assumption: per video about 15k transcript tokens plus 3k note-section tokens = 18k; 150 videos = 2.7M tokens/month; plus 900 query embeddings x 20 tokens = 18k. Total about 2.72M tokens/month.

| Provider / model | $/MTok | Dimensions | Max input tokens per text | Per-request limits | Idempotency key | Data retention | Multilingual / Hindi |
|---|---|---|---|---|---|---|---|
| Voyage `voyage-4` [30][31][32] | $0.06 [31]; first 200M tokens free per account (not on Batch API) [31][36] | 1024 default; 256, 512, 2048 via `output_dimension` [30][32] | 32,000 [30][32] | 1,000 texts and 320K total tokens per request [32]; `truncation` defaults to true [32]; Batch API: 12 h window, 33% discount, 100K inputs per batch [36] | Not documented [37] | FAQ: customers "can opt-out from Voyage storing and using their data for future model training so that there is a zero-day retention"; opt-out needs a payment method on file and an org Admin [38]. Default therefore allows storage/training use until opted out | "Optimized for general-purpose and multilingual retrieval quality" [30]; Hindi not named [32] |
| Voyage `voyage-4-lite` [30][31][32] | $0.02 [31]; 200M free [31] | Same [30] | 32,000 [30] | 1,000 texts / 1M tokens [32]; Tier 1 rate limit 2000 RPM / 16M TPM [39] | Not documented | As above [38] | "Optimized for latency and cost" [30] |
| Voyage `voyage-4-large` [30][31][32] | $0.12 [31]; 200M free [31] | Same [30] | 32,000 [30] | 1,000 texts / 120K tokens [32] | Not documented | As above [38] | "best general-purpose and multilingual retrieval quality" [30] |
| OpenAI `text-embedding-3-small` [7][33][40] | $0.02 [7] | 1536 default; `dimensions` parameter shortens [33] | 8,192 [33][40] | Input array of at most 2048 items and 300,000 tokens summed per request [40]; Batch API 50% off, 24 h [26] | Not documented [40] | `/v1/embeddings` ZDR-eligible "subject to prior approval"; abuse logs up to 30 days; no training [9] | MTEB 62.3% [33]; MIRACL score not on any page fetched |
| OpenAI `text-embedding-3-large` [7][33][40] | $0.13 [7] | 3072 default; shortenable [33] | 8,192 [33] | As above [40] | Not documented | As above [9] | MTEB 64.6% [33]; model page: "for both english and non-english tasks" [41] |
| Google `gemini-embedding-2` [12][34] | $0.20 text; batch $0.10 [12] | 3072 default; recommended 768 / 1536 / 3072 via `output_dimensionality`, range 128-3072, auto-normalized when truncated [34] | 8,192 [34] | Caveat: multiple inputs in one request produce "a single, aggregated embedding" unless each is wrapped in its own Content object [34]; max texts per request **not verified** | **not verified** | Free tier content used to improve products; paid tier "not used" [12] | "across over 100 languages" [34]; Hindi not named |
| Google `gemini-embedding-001` [12][34] | $0.15; batch $0.075 [12] | 3072 default, same MRL options; manual normalization when truncated [34] | 2,048 [34] | Individual embeddings per list item [34] | **not verified** | As above | **not verified** |
| Cohere `embed-v4.0` [42][43] | Per-token API price **not verified** (pricing page shows only Model Vault instance pricing) | `output_dimension` 256, 512, 1024, 1536 (default 1536) [43] | 128k context [42] | Max 96 texts per call [43]; `input_type` required [43] | Not documented | **not verified** | "over 100 languages" [42]; Hindi only in a code sample |
| Mistral `mistral-embed-2312` [44] | $0.10 [44] | 1024 [44] | 8k [44] | **not verified** | **not verified** | **not verified** | **not verified** |
| Jina `jina-embeddings-v5-text-small` / `-nano` [45] | Per-token price **not verified** (pricing pages 404) | 1024 / 768; Matryoshka down to 32 [45] | 32,768 / 8,192 [45] | "no batch size limit" [45] | **not verified** | **not verified** | Only vendor page fetched that names Hindi explicitly (v5-text-nano) [45] |

Anthropic does not offer an embedding model and points to Voyage AI, while advising "you should assess a variety of embeddings vendors" [30].

pgvector constraints [35]: `vector` supports up to 16,000 dimensions but HNSW/IVFFlat indexes on `vector` accept at most 2,000; `halfvec` indexes accept up to 4,000. Cosine distance operator is `<=>`.

### Estimated monthly cost, embeddings

2.72M tokens/month.

| Candidate | Arithmetic | Month |
|---|---|---|
| Voyage `voyage-4-lite` | 2.72 x $0.02 | $0.05 (free for about 73 months under the 200M-token allowance) |
| OpenAI `text-embedding-3-small` | 2.72 x $0.02 | $0.05 |
| Mistral `mistral-embed-2312` | 2.72 x $0.10 | $0.27 |
| Voyage `voyage-4` | 2.72 x $0.06 | $0.16 (free for about 73 months) |
| OpenAI `text-embedding-3-large` | 2.72 x $0.13 | $0.35 |
| Voyage `voyage-4-large` | 2.72 x $0.12 | $0.33 (free for about 73 months) |
| Google `gemini-embedding-001` | 2.72 x $0.15 | $0.41 |
| Google `gemini-embedding-2` | 2.72 x $0.20 | $0.54 |

### Recommended starting choice: Voyage `voyage-4` at 1024 dimensions

Reasons:
- Anthropic's own docs name Voyage as the embeddings partner [30], keeping the AI vendor surface to two accounts.
- 1024-dimension default sits under pgvector's 2,000-dimension index limit for the `vector` type [35], so a plain HNSW cosine index works without `halfvec` or truncation.
- 32,000-token context per text [30] means chunk size is set by retrieval quality, not by the model.
- The 200M free tokens [31] cover this project's volume for years; the paid rate afterwards is $0.16/month.
- `input_type: "document"` for chunks and `"query"` for searches is required by the docs for retrieval quality [30].
- Action item: Voyage's default allows storing data for training until the account opts out [38]; opt out (needs a payment method on file) before sending transcripts.

Alternative: OpenAI `text-embedding-3-small` at 1536 dimensions if a Voyage evaluation on Devanagari and romanized Hinglish transcripts underperforms; it is ZDR-eligible [9] and costs the same $0.02/MTok [7]. Run that A/B before the search ticket freezes the vector column width, because switching models later means re-embedding every chunk.

Facts later tickets need:
- Vector column: `vector(1024)`, HNSW with `vector_cosine_ops`. If the owner later selects a 3072-dim model, the column must become `halfvec(3072)` or the model must be called with a reduced `output_dimensionality` [34][35].
- Chunking: keep transcript chunks to roughly 300-500 tokens for retrieval; the hard ceiling is 32,000 tokens per text (Voyage) or 8,192 (OpenAI, Gemini) [30][33][34]. Batch up to 1,000 texts / 320K tokens per Voyage request [32] or 2048 inputs / 300K tokens per OpenAI request [40], so one 2 h transcript embeds in a single call on either.
- Query embeddings pause at the soft cap (spec 11): at $0.06/MTok and 20 tokens per query this is $0.0000012 per search; the pause is about policy consistency, not money.
- No surveyed embeddings API documents an idempotency key; dedupe by content hash app-side.
- Price table unit: `per_1M_tokens` only; no output tokens.

## 4. Summary

| Role | Start with | Configured alternative | Typical month |
|---|---|---|---|
| Transcription | Deepgram `nova-3`, `language=multi` | AssemblyAI `universal-2` | $0 to $23.40 depending on caption availability |
| Note generation | Anthropic `claude-sonnet-5`, structured outputs, `effort: low` | Anthropic `claude-haiku-4-5` | about $8.10 |
| Embeddings | Voyage `voyage-4`, 1024 dims | OpenAI `text-embedding-3-small`, 1536 dims | about $0.16 (free under allowance) |

Combined typical spend lands near the $10 soft cap only when many saves lack captions; the cost-config ticket should default the cap to $10 as specified and expose the Haiku fallback prominently.

## Sources

1. https://deepgram.com/pricing
2. https://developers.deepgram.com/docs/models-languages-overview
3. https://developers.deepgram.com/docs/pre-recorded-audio
4. https://www.assemblyai.com/pricing
5. https://www.assemblyai.com/docs/speech-to-text/pre-recorded-audio/supported-languages
6. https://www.assemblyai.com/docs/speech-to-text/pre-recorded-audio
7. https://developers.openai.com/api/docs/pricing
8. https://developers.openai.com/api/docs/guides/speech-to-text
9. https://developers.openai.com/api/docs/guides/your-data
10. https://elevenlabs.io/pricing/api
11. https://docs.sarvam.ai/api-reference-docs/speech-to-text/transcribe
12. https://ai.google.dev/gemini-api/docs/pricing
13. https://ai.google.dev/gemini-api/docs/models/gemini-3.8-flash
14. Anthropic `claude-api` skill (SDK retry defaults, thinking billing), cross-checked against [18]
15. https://platform.claude.com/docs/en/about-claude/models/overview.md
16. https://platform.claude.com/docs/en/about-claude/pricing.md
17. https://platform.claude.com/docs/en/build-with-claude/structured-outputs.md
18. https://platform.claude.com/docs/en/api/errors.md
19. https://platform.claude.com/docs/en/api/messages/create.md
20. https://privacy.claude.com/en/articles/7996866-how-long-do-you-store-my-organization-s-data
21. https://platform.claude.com/docs/en/manage-claude/api-and-data-retention.md
22. https://platform.claude.com/docs/en/build-with-claude/multilingual-support.md
23. https://developers.openai.com/api/docs/pricing (text model table)
24. https://developers.openai.com/api/docs/models/gpt-5.6-terra
25. https://developers.openai.com/api/docs/guides/structured-outputs
26. https://developers.openai.com/api/docs/guides/batch
27. https://ai.google.dev/gemini-api/docs/structured-output
28. https://platform.claude.com/docs/en/build-with-claude/batch-processing.md
29. https://platform.claude.com/docs/en/api/rate-limits.md
30. https://platform.claude.com/docs/en/build-with-claude/embeddings.md
31. https://docs.voyageai.com/docs/pricing
32. https://docs.voyageai.com/docs/embeddings
33. https://developers.openai.com/api/docs/guides/embeddings
34. https://ai.google.dev/gemini-api/docs/embeddings
35. https://github.com/pgvector/pgvector/blob/master/README.md
36. https://docs.voyageai.com/docs/batch-api
37. https://docs.voyageai.com/reference/embeddings-api
38. https://docs.voyageai.com/docs/faq
39. https://docs.voyageai.com/docs/rate-limits
40. https://developers.openai.com/api/docs/api-reference/embeddings/create
41. https://developers.openai.com/api/docs/models/text-embedding-3-large
42. https://docs.cohere.com/docs/models
43. https://docs.cohere.com/reference/embed
44. https://docs.mistral.ai/inference/pricing and https://docs.mistral.ai/models/model-cards/mistral-embed-23-12
45. https://jina.ai/embeddings

Not fetched this run (so not cited): Deepgram pre-recorded API reference (404 at the URL tried), AssemblyAI file-limits FAQ (404), Groq pricing (page returned marketing copy), Sarvam pricing page, Gemini audio tokens-per-second, Cohere per-token pricing, Jina pricing (404), OpenAI MIRACL announcement (403).

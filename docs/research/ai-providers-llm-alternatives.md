# Appendix: non-Anthropic LLM APIs for strict-JSON-schema note generation

Companion to `ai-providers.md`. Produced 2026-09-04 by a research sub-agent; all numbers come from official pages fetched during that run (WebFetch, or browser where WebFetch truncated). Anything not found on an official page is marked **not verified**. `platform.openai.com/docs/*` now 301-redirects to `developers.openai.com/api/docs/*`; citations use the final URL.

## 1. OpenAI

**Model table (Responses API, standard tier, USD per 1M tokens)**

| Model id | Input | Cached input | Output | Batch (50% off standard) | Context window | Max output | Structured outputs | Source |
|---|---|---|---|---|---|---|---|---|
| `gpt-6-astra` | $10 | $1 | $50 | $5 / $25 | 1,050,000 ("Maximum input tokens: 922,000") | 128,000 | "structured_outputs" listed as supported; Batch "Supported" | https://developers.openai.com/api/docs/models/gpt-6-astra |
| `gpt-5.6-terra` | $2 | $0.20 | $12 | $1 / $6 | 1,050,000 | 128,000 | supported; Batch supported; `reasoning.effort` "none, low, medium (default), high, xhigh, and max" | https://developers.openai.com/api/docs/models/gpt-5.6-terra |
| `gpt-5.6-luna` | $0.20 | $0.02 | $1.20 | $0.10 / $0.60 | 1,050,000 | 128,000 | supported; Batch supported | https://developers.openai.com/api/docs/models/gpt-5.6-luna |
| `gpt-5.4-mini` (snapshot `gpt-5.4-mini-2026-03-17`) | $0.75 | $0.075 | $4.50 | $0.375 / $2.25 | 400,000 (max input 272,000) | 128,000 | supported; Batch supported | https://developers.openai.com/api/docs/models/gpt-5.4-mini |
| `gpt-5.4-nano` (snapshot `gpt-5.4-nano-2026-03-17`) | $0.20 | $0.02 | $1.25 | $0.10 / $0.625 | 400,000 (max input 272,000) | 128,000 | supported; Batch supported | https://developers.openai.com/api/docs/models/gpt-5.4-nano |
| `gpt-5-mini` | $0.25 | $0.025 | $2.00 | $0.125 / $1.00 | not verified this run | not verified | not verified | https://developers.openai.com/api/docs/pricing |
| `gpt-5-nano` | $0.05 | $0.005 | $0.40 | $0.025 / $0.20 | not verified this run | not verified | not verified | https://developers.openai.com/api/docs/pricing |

- Pricing page also lists `gpt-5.6-sol` ($4 / $0.40 / $20), `gpt-5.5` ($5 / $0.50 / $30), `gpt-5.4` ($2.50 / $0.25 / $15), `gpt-5.2`, `gpt-5.1`, `gpt-5` — https://developers.openai.com/api/docs/pricing
- Models page positioning: "use GPT-6 Astra, our flagship model for complex reasoning and coding"; GPT-5.6 Terra for balanced cost, GPT-5.6 Luna for "high-volume, cost-sensitive tasks" — https://developers.openai.com/api/docs/models
- Batch API: "50% cost discount compared to synchronous APIs"; completion window "24h"; supports `/v1/responses`; up to 50,000 requests / 200 MB per batch; batch "will not consume tokens from your standard per-model rate limits" — https://developers.openai.com/api/docs/guides/batch
- Flex: pricing page says Flex "pricing matches Batch rates" for select models; Flex availability per model **not verified** (not on the Luna/Terra model pages).
- Pricing caveat: "Regional processing (data residency) endpoints are charged a 10% uplift for models released on or after March 5, 2026" — pricing page.
- `max_output_tokens` is "including visible output tokens and reasoning tokens" (minimum 16) — https://developers.openai.com/api/reference/resources/responses/methods/create. So reasoning tokens are billed as output; for Terra you can set `reasoning.effort: "none"`. Whether Luna/Astra accept `none`: Astra page lists "low, medium, high, xhigh, and max" (no `none`); Luna effort options **not verified**.

**Structured Outputs mechanism** (https://developers.openai.com/api/docs/guides/structured-outputs)
- Enable with `text.format = { type: "json_schema", strict: true, schema: … }` (Responses API).
- Guarantee: "a feature that ensures the model will always generate responses that adhere to your supplied JSON Schema" — constrained, not best-effort. JSON mode (`json_object`) "will not guarantee the output matches any specific schema".
- Supported types: String, Number, Boolean, Integer, Object, Array, Enum, anyOf.
- Supported keywords: strings `pattern`, `format` (date-time, time, date, duration, email, hostname, ipv4, ipv6, uuid); numbers `multipleOf`, `maximum`, `exclusiveMaximum`, `minimum`, `exclusiveMinimum`; arrays `minItems`, `maxItems`. "Note these constraints are not yet supported for fine-tuned models."
- Required rules: "all fields or function parameters must be specified as required"; optional emulated via `type: ["string","null"]`; "additionalProperties: false must always be set in objects"; root "must be an object, and not use anyOf".
- Limits (verbatim): "A schema may have up to 5000 object properties total, with up to 10 levels of nesting."; "total string length of all property names, definition names, enum values, and const values cannot exceed 120,000 characters"; "up to 1000 enum values across all enum properties"; single enum property >250 values capped at 15,000 chars.
- Not supported: "Composition: allOf, not, dependentRequired, dependentSchemas, if, then, else". `oneOf` is not in the supported list (only `anyOf`). `$defs`/`$ref` and recursive schemas supported. Key ordering follows schema order. Unsupported schema with `strict: true` returns an error.
- Refusals arrive as a `refusal` content item that does not follow the schema; incomplete responses (max tokens) must be handled. "the first request you make with any schema will have additional latency".
- Third-party snippets claimed limits of 5 nesting levels / 100 properties / 500 enums; the **official page** says 10 / 5000 / 1000. Use the official numbers.

**Data retention / ZDR** (https://developers.openai.com/api/docs/guides/your-data)
- "As of March 1, 2023, data sent to the OpenAI API is not used to train or improve OpenAI models (unless you explicitly opt in)". Abuse-monitoring logs retained "up to 30 days".
- Responses API: "30 day Application State retention period by default, or when the `store` parameter is set to `true`". ZDR (requires OpenAI approval; contact sales) forces `store=false` for `/v1/responses` and `/v1/chat/completions`. ZDR-eligible endpoints include `/v1/responses` and `/v1/chat/completions`; Batch API is **not** ZDR-eligible ("Zero Data Retention eligible: No"; application state "Until deleted").

**Idempotency**: `Idempotency-Key` is documented on the OpenAI Developers site only for Ads Bulk API, Custom Audiences, Workspace Agents and Agentic Commerce; it is **not documented** on the Responses `create` reference page — treat as **not verified / not supported for /v1/responses**.

**Timeout guidance**: no explicit HTTP timeout numbers found on production-best-practices or background pages. Background mode is positioned for "long-running tasks … without having to worry about timeouts or other connectivity issues"; poll while `queued`/`in_progress`; if `store` is omitted/false "the response is deleted after roughly 10 minutes"; cancel is idempotent — https://developers.openai.com/api/docs/guides/background

**Hindi**: no text-model language-capability statement found on official docs; only audio docs mention Hindi. **Not verified** for text.

## 2. Google Gemini API

**Model table (paid tier, standard, USD per 1M tokens; from https://ai.google.dev/gemini-api/docs/pricing)**

| Model id | Input | Output (incl. thinking tokens) | Context caching | Batch | Input limit | Output limit | Free tier | Source (limits) |
|---|---|---|---|---|---|---|---|---|
| `gemini-3.8-flash` | $0.75 through Dec 31 2026; $1.50 from Jan 1 2027 | $3.75 → $7.50 | $0.075 → $0.15 + $0.50/1M tok/hr storage (→ $1.00) | $0.375 → $0.75 in; $1.875 → $3.75 out | 1,048,576 | 65,536 | Yes ("Free of charge") | https://ai.google.dev/gemini-api/docs/models/gemini-3.8-flash |
| `gemini-3.7-flash` | same as 3.8 | same | same | same | 1,048,576 | 65,536 | Yes | https://ai.google.dev/gemini-api/docs/models/gemini-3.7-flash |
| `gemini-3.6-flash` | same as 3.8 | same | same | same | not verified | not verified | Yes | pricing page |
| `gemini-3.5-flash` | $1.50 | $9.00 | $0.15 + $1.00/hr | $0.75 / $4.50 | not verified | not verified | Yes | pricing page |
| `gemini-3.5-flash-lite` | $0.30 | $2.50 | $0.03 + $1.00/hr | $0.15 / $1.25 | 1,048,576 | 65,536 | Yes | https://ai.google.dev/gemini-api/docs/models/gemini-3.5-flash-lite |
| `gemini-3.1-flash-lite` | $0.25 (text) | $1.50 | $0.025 + $1.00/hr | $0.125 / $0.75 | not verified | not verified | Yes | pricing page |
| `gemini-3.1-pro-preview` | $2.00 (≤200k) / $4.00 (>200k) | $12.00 / $18.00 | $0.20 / $0.40 + $4.50/hr | $1.00 / $6.00 (≤200k) | 1,048,576 | 65,536 | "Not available" | https://ai.google.dev/gemini-api/docs/models/gemini-3.1-pro-preview |
| `gemini-2.5-flash` | $0.30 | $2.50 | $0.03 + $1.00/hr | $0.15 / $1.25 | not verified | not verified | Yes | pricing page |
| `gemini-2.5-flash-lite` | $0.10 | $0.40 | $0.01 + $1.00/hr | $0.05 / $0.20 | not verified | not verified | Yes | pricing page |

- Structured outputs, caching, Batch, Flex and Priority are "Supported" on the 3.8/3.7 Flash, 3.5 Flash-Lite and 3.1 Pro Preview model pages. Priority tier for 3.8 Flash is $1.35 / $6.75 (through 2026).
- Thinking: 3.8/3.7 Flash "Thinking Supported (low, medium, high). Note: minimal is not supported and returns an error" (model pages). Thinking docs: default "On (medium)" for 3.8/3.7 Flash; "Response pricing is the sum of output tokens and thinking tokens" — https://ai.google.dev/gemini-api/docs/thinking. The 3k-output cost assumption will undercount on Gemini 3.x Flash unless `thinking_level: low` is set; no off switch documented for these models.
- Free tier: "Content used to improve our products" = Yes; Paid = No. Free tier has no Batch for 3.8/3.7 ("Not available"), but Batch is "Free of charge" on the free tier for 3.5 Flash-Lite and 3.1 Flash-Lite (pricing page).
- Rate limits: not published on the rate-limits page ("can be viewed in Google AI Studio"); Tier 1 requires a linked billing account; Tier 2 "Paid $100 + 3 days"; Tier 3 "Paid $1,000 + 30 days" — https://ai.google.dev/gemini-api/docs/rate-limits

**Structured output mechanism** (https://ai.google.dev/gemini-api/docs/structured-output, last updated 2026-09-02)
- Docs now show the Interactions API: `POST v1beta/interactions` with `response_format: { type: "text", mime_type: "application/json", schema: {…} }`. The `generateContent` fields `responseSchema` / `responseJsonSchema` do **not** appear on this page — **not verified** this run.
- Guarantee language: "generate responses that adhere to a provided JSON Schema. This ensures predictable, type-safe results"; best-practice note "While output is syntactically correct JSON, always validate values in your application." The page does not use the words "constrained decoding" or "guaranteed"; treat as schema-adherent by design but validate client-side.
- Supported types: string, number, integer, boolean, object, array, null (via type array). Object: `properties`, `required`, `additionalProperties` ("Can be a boolean or a schema"). String: `enum`, `format` (date-time, date, time). Number/integer: `enum`, `minimum`, `maximum`. Array: `items`, `prefixItems`, `minItems`, `maxItems`. `title`, `description`. `anyOf` shown in the Content Moderation example; `$ref: "#"` recursion shown in the Recursive Structures example. `pattern`, `oneOf`, `allOf`: not listed — **not verified**.
- Limits: "Schema subset: Not all JSON Schema features are supported." "Schema complexity: Very large or deeply nested schemas may be rejected." No numeric limits published. Structured outputs + built-in tools: "Preview: This feature is available only to Gemini 3 series models."

**Batch** (https://ai.google.dev/gemini-api/docs/batch-api): "50% of the standard cost"; "target turnaround time is 24 hours, but in majority of cases, it is much quicker"; input file max 2GB; inline requests under 20MB; structured output with response schema shown in batch examples; "The creation of a batch job is not idempotent."

**Data retention / terms** (https://ai.google.dev/gemini-api/terms): Unpaid Services — Google "uses the content you submit … to provide, improve, and develop Google products" and "human reviewers may read, annotate, and process your API input and output." Paid Services — "Google doesn't use your prompts … or responses to improve our products"; "Google logs prompts and responses for a limited period of time, solely for detecting and preventing violations of the Prohibited Use Policy". Number of days: **not verified**. ZDR/opt-out: **not mentioned**. "Paid Service" = API accessed through a Cloud Project with an active billing account.

**Idempotency**: no idempotency header documented for generateContent/interactions; batch creation explicitly non-idempotent. **Not supported / not verified.**

**Timeout guidance** (https://ai.google.dev/gemini-api/docs/troubleshooting): exponential backoff for `429 RESOURCE_EXHAUSTED` / `503 UNAVAILABLE` (1s, 2s, 4s, 8s, with jitter, max retries); Python SDK "automatically retries transient errors up to four times with an initial delay of approximately 1 second and a maximum delay of 60 seconds." No explicit timeout value published.

**Hindi**: no language-support table found on the model pages fetched — **not verified**.

## 3. Mistral (quick pass)

From https://mistral.ai/pricing/api/: `mistral-medium-latest` (Mistral Medium 3.5) $1.5 in / $7.5 out; `mistral-small-latest` (Mistral Small 4, "Multimodal. Multilingual. Apache 2.0.") $0.15 / $0.6; `mistral-large-latest` (Mistral Large 3) $0.5 / $1.5. https://mistral.ai/pricing states "Batch processing … reduces the price by 50%, and cached input tokens reduce input cost by up to 90%". Custom structured outputs use `response_format` (Pydantic/JSON schema); docs say the schema instruction "is being always prepended to the System Prompt" and do not claim constrained decoding — https://docs.mistral.ai/studio/conversations/structured-output/custom. Context windows, max output, schema-keyword limits, retention, idempotency: **not verified**.

## 4. Monthly cost estimates

Standard: 150 calls × (12k in + 3k out) = **1.8M input + 0.45M output**. Heavy: 150 × (60k in + 4k out) = **9.0M input + 0.6M output**. Formula: input_M × $in + output_M × $out. Batch = 50% of these for OpenAI and Gemini (Gemini 3.1 Pro heavy case stays in the ≤200k-per-prompt tier).

| Model | Standard ($) | Heavy ($) | Standard via Batch | Heavy via Batch |
|---|---|---|---|---|
| gpt-6-astra | **40.50** | **120.00** | 20.25 | 60.00 |
| gpt-5.6-terra | **9.00** | **25.20** | 4.50 | 12.60 |
| gpt-5.6-luna | **0.90** | **2.52** | 0.45 | 1.26 |
| gpt-5.4-mini | **3.38** | **9.45** | 1.69 | 4.73 |
| gpt-5.4-nano | **0.92** | **2.55** | 0.46 | 1.28 |
| gpt-5-mini | **1.35** | **3.45** | 0.68 | 1.73 |
| gpt-5-nano | **0.27** | **0.69** | 0.14 | 0.35 |
| gemini-3.8/3.7/3.6-flash (2026 price) | **3.04** | **9.00** | 1.52 | 4.50 |
| gemini-3.8/3.7/3.6-flash (from 2027-01-01) | **6.08** | **18.00** | 3.04 | 9.00 |
| gemini-3.5-flash | **6.75** | **18.90** | 3.38 | 9.45 |
| gemini-3.5-flash-lite | **1.67** | **4.20** | 0.83 | 2.10 |
| gemini-3.1-flash-lite | **1.13** | **3.15** | 0.56 | 1.58 |
| gemini-3.1-pro-preview | **9.00** | **25.20** | 4.50 | 12.60 |
| gemini-2.5-flash | **1.67** | **4.20** | 0.83 | 2.10 |
| gemini-2.5-flash-lite | **0.36** | **1.14** | 0.18 | 0.57 |
| Gemini free tier (any free-eligible model) | 0 | 0 | — | — (content used to improve products; rate limits not published) |
| mistral-small-latest | **0.54** | **1.71** | 0.27 | 0.86 |
| mistral-large-latest | **1.58** | **5.40** | 0.79 | 2.70 |
| mistral-medium-latest | **6.08** | **18.00** | 3.04 | 9.00 |

Caveats: (a) reasoning/thinking tokens are billed as output on both OpenAI and Gemini (default medium on Gemini 3.x Flash, no off switch documented), so real output volume may be several times the 3–4k assumed unless you set OpenAI `reasoning.effort: "none"` (Terra) or Gemini `thinking_level: low`; (b) cached-input discounts are ignored (the transcript changes every call, so only the system prompt + schema prefix would cache); (c) Gemini free tier is $0 but data is used for product improvement and human review.

## 5. Facts that could not be verified from official pages

- OpenAI: `Idempotency-Key` on `/v1/responses`; explicit HTTP timeout guidance; Flex availability per model; `gpt-5-mini`/`gpt-5-nano` context/output limits and structured-output support; Luna reasoning-effort options; any Hindi text-capability statement.
- Gemini: `responseJsonSchema`/`responseSchema` on `generateContent`; numeric schema limits; `pattern`/`oneOf`/`allOf` support; whether adherence is constrained decoding; retention period in days for paid abuse logging; ZDR; idempotency header; explicit timeout values; free-tier RPM/RPD figures; context/output limits for 3.6 Flash, 3.5 Flash, 3.1 Flash-Lite, 2.5 models; Hindi statement.
- Mistral: context windows, max output, structured-output guarantee and schema limits, retention, idempotency.

## 6. Sources fetched

- https://developers.openai.com/api/docs/pricing
- https://developers.openai.com/api/docs/models
- https://developers.openai.com/api/docs/models/gpt-6-astra
- https://developers.openai.com/api/docs/models/gpt-5.6-terra
- https://developers.openai.com/api/docs/models/gpt-5.6-luna
- https://developers.openai.com/api/docs/models/gpt-5.4-mini
- https://developers.openai.com/api/docs/models/gpt-5.4-nano
- https://developers.openai.com/api/docs/guides/structured-outputs
- https://developers.openai.com/api/docs/guides/your-data
- https://developers.openai.com/api/docs/guides/batch
- https://developers.openai.com/api/docs/guides/background
- https://developers.openai.com/api/docs/guides/production-best-practices
- https://developers.openai.com/api/reference/resources/responses/methods/create
- https://ai.google.dev/gemini-api/docs/pricing
- https://ai.google.dev/gemini-api/docs/models
- https://ai.google.dev/gemini-api/docs/models/gemini-3.8-flash
- https://ai.google.dev/gemini-api/docs/models/gemini-3.7-flash
- https://ai.google.dev/gemini-api/docs/models/gemini-3.5-flash-lite
- https://ai.google.dev/gemini-api/docs/models/gemini-3.1-pro-preview
- https://ai.google.dev/gemini-api/docs/structured-output
- https://ai.google.dev/gemini-api/terms
- https://ai.google.dev/gemini-api/docs/batch-api
- https://ai.google.dev/gemini-api/docs/troubleshooting
- https://ai.google.dev/gemini-api/docs/rate-limits
- https://ai.google.dev/gemini-api/docs/thinking
- https://mistral.ai/pricing
- https://mistral.ai/pricing/api/
- https://docs.mistral.ai/capabilities/structured-output/custom_structured_output
- https://docs.mistral.ai/studio/conversations/structured-output/custom
- https://docs.mistral.ai/getting-started/models

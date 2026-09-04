# Appendix: speech-to-text provider survey

Companion to `ai-providers.md`. Produced 2026-09-04 by a research sub-agent; all facts from pages fetched that day (URLs in Sources). Usage envelope assumed: 150 videos/month × 30 min = 4,500 min (75 h); worst day 5 × 120 min = 600 min (10 h). Audio can be chunked to ≤20 min with FFmpeg.

## 1. OpenAI Audio Transcriptions API

| Item | Finding | Source |
|---|---|---|
| Model IDs | `gpt-transcribe` (recommended: "the recommended model for transcribing recorded speech in its original language"), `gpt-4o-transcribe`, `gpt-4o-mini-transcribe`, `gpt-4o-mini-transcribe-2025-12-15`, `whisper-1`, `gpt-4o-transcribe-diarize`; `gpt-live-transcribe` (realtime only) | [S2], [S5] |
| Price | `gpt-transcribe` "$0.0045 / minute"; `gpt-4o-transcribe` "$0.006 / minute"; `gpt-4o-mini-transcribe` "$0.003 / minute"; `gpt-4o-transcribe-diarize` "$0.006 / minute"; Whisper "$0.006 / minute" | [S1] |
| Size / duration / formats | "Files can be up to 25 MB." Guide lists "mp3, mp4, mpeg, mpga, m4a, wav, and webm"; API reference lists "flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, or webm". No duration limit documented; "For larger recordings, use a compressed audio format or split the file into chunks of 25 MB or less." | [S2], [S5] |
| Sync/async | Synchronous HTTP (optionally `stream=true` SSE; "Streaming is not supported for the whisper-1 model"). No async job/polling. Request timeout: not verified. | [S2], [S5] |
| Hindi / code-switching | `whisper-1`: `"hi": "hindi"` is in the 99-entry LANGUAGES dict. `gpt-transcribe`: "supports ... multiple language hints to improve transcription of domain terms, multilingual audio, and code-switching"; `languages` param "Possible languages of the input audio, in ISO-639-1 format. Supported by gpt-transcribe." Hindi not named on OpenAI pages for gpt-transcribe. | [S2], [S3], [S5], [S6] |
| Timestamps | `timestamp_granularities[]` ("word", "segment") requires `verbose_json`; guide: "only supported for `whisper-1`"; "For gpt-4o-transcribe and gpt-4o-mini-transcribe, the only supported format is json." | [S2], [S5] |
| Idempotency key | Not documented | [S5] |
| Retention | API data not used for training; abuse-monitoring logs "retained for up to 30 days"; `/v1/audio/transcriptions` — Abuse Monitoring: None, Application State: None, ZDR Eligible: Yes | [S4] |
| Free tier | Not verified | [S1] |

## 2. Deepgram (Nova-3, pre-recorded)

| Item | Finding | Source |
|---|---|---|
| Model IDs | `model=nova-3` (or `nova-3-general`); `language=hi` or `language=multi` | [S8], [S12] |
| Price (pre-recorded, Pay As You Go) | Nova-3 Monolingual "$0.0043/min"; Nova-3 Multilingual "$0.0052/min"; Whisper Large "$0.0048/min". Growth (prepaid): $0.0036 / $0.0043 | [S7] |
| Size / duration / formats | "Maximum 2 GB." Formats: "MP3, MP4, MP2, AAC, WAV, FLAC, PCM, M4A, Ogg, Opus, WebM". Processing-time cap: "Requests exceeding 10 minutes (Nova/Base/Enhanced) or 20 minutes (Whisper) return a `504: Gateway Timeout`" | [S9], [S10] |
| Sync/async | Sync by default; async via `callback=URL` — "responds immediately with a `request_id`"; retries callback "up to 10 times with a 30 second delay" | [S11] |
| Hindi / code-switching | Nova-3 lists `hindi: hi`; "Multilingual (English, Spanish, French, German, Hindi, Russian, Portuguese, Japanese, Italian, and Dutch): `multi`"; code-switching available on Nova-3 for pre-recorded and streaming | [S8], [S12] |
| Timestamps | `words` array with `start`/`end` in seconds for every word | [S9] |
| Idempotency key | Not documented | [S9], [S11] |
| Retention | "Deepgram does not store transcripts"; MIP program stores "fractional increments of data" for training unless `mip_opt_out=true`; "Data from opted-out requests is retained only for the duration necessary to process the request." Pricing impact of opt-out: not verified. | [S9], [S13], [S7] |
| Free tier | "Free $200 Credit" | [S7] |

## 3. AssemblyAI

| Item | Finding | Source |
|---|---|---|
| Model IDs | `speech_models: ["universal-3-5-pro"]` (recommended), `["universal-2"]`; fallback pattern `["universal-3-5-pro", "universal-2"]` with `language_detection: true`. SLAM-1/best/nano deprecated. | [S14], [S16], [S17] |
| Price | Universal-3.5 Pro "$0.21/hr"; Universal-2 "$0.15/hr" (pre-recorded) | [S14] |
| Size / duration / formats | "up to 5 GB per request (`/v2/transcript`); local files uploaded via `/v2/upload` up to 2.2 GB"; "160 ms to 10 hours per file"; formats include `.m4a, .ogg, .oga, .opus, .aac ...` | [S15], [S19] |
| Sync/async | Async: "submit to `POST /v2/transcript`, then poll ... `GET /v2/transcript/{id}` until the status is `completed`", or `webhook_url` | [S15] |
| Hindi / code-switching | Hindi `hi` listed for both models; "Universal-3.5 Pro natively handles code switching across 18 languages" (Hindi included) — "Set `language_detection` as `True`"; Universal-2 needs `language_detection_options: {"code_switching": True}` | [S16], [S17], [S18] |
| Timestamps | `words` array with `text`, `start`, `end` (ms), `confidence` | [S15] |
| Idempotency key | Not documented | [S15] |
| Retention | Uploaded audio: "Deletion process begins at 24 hours and is at most 48 hours"; transcripts: "Deletion process begins at 30 days"; DELETE endpoint available; ZDR only "for our Streaming product"; some files used for training "as permitted by the applicable contract", opt-out via FAQ | [S20], [S21] |
| Free tier | "$50 in free credits, no credit card required" | [S14] |

## 4. ElevenLabs Scribe

| Item | Finding | Source |
|---|---|---|
| Model IDs | `model_id: "scribe_v2"`; also Scribe v2 Realtime | [S22], [S23] |
| Price | Scribe v2 "$0.22" per hour (additional usage); plans include hours: Starter $6 → 4 h 30 min; Creator $22 → 27 h; Pro $99 → 100 h | [S22] |
| Size / duration / formats | Capabilities page: "Files up to 3 GB"; API ref: "must be less than 5.0GB" (2 GB via URL). "Standard mode: Up to 10 hours". Formats: "AAC, AIFF, OGG, MP3, OPUS, WAV, FLAC, M4A, WebM" + video | [S23], [S24] |
| Sync/async | Sync, or `webhook=true` — request "will return early without the transcription, which will be delivered later via webhook" | [S23] |
| Hindi / code-switching | "Hindi (hin)" in "High Accuracy" tier (">5% to ≤10% WER"); `language_code` ISO-639-1/639-3. Code-switching: no statement found | [S24] |
| Timestamps | `timestamps_granularity`: "'word' provides word-level timestamps and 'character' ..." | [S23] |
| Idempotency key | Not documented | [S23] |
| Retention | Default: "ElevenLabs retains data"; Zero Retention Mode via `enable_logging=false` — "Enterprise customers can use Zero Retention Mode" | [S25] |
| Free tier | Free plan STT hours: not verified | [S22] |

## 5. Google Cloud Speech-to-Text V2 (Chirp 3)

| Item | Finding | Source |
|---|---|---|
| Model IDs | `chirp_3` (V2 only); `chirp_2` | [S27], [S28] |
| Price | V2 Standard recognition: "$0.016 / 1 minute" (0–500k min); Dynamic Batch Recognition: "$0.003 / 1 minute". Footnote names "chirp (Speech-to-Text V2 only)" — chirp_3 not named explicitly | [S26] |
| Size / duration / formats | Local file: "10 MB on all single requests"; async "~480 Minutes" via GCS; Chirp 3 BatchRecognize "good for long audio 1 minute to 1 hour in general, but up to 20 minutes with word-level timestamp enabled". AutoDetectDecodingConfig: OGG_OPUS, WEBM_OPUS, MP4_AAC, M4A_AAC, MP3, FLAC, WAV | [S29], [S27], [S31] |
| Sync/async | `BatchRecognize` = "long running operation that can be polled"; `Recognize` sync "audio shorter than one minute" | [S31], [S27] |
| Hindi / code-switching | "Hindi (India) hi-IN GA"; `language_codes=["auto"]` "transcribes in the most prevalent language" (single dominant language, not code-switching) | [S27] |
| Timestamps | Page is self-contradictory on Chirp 3 word-level timestamps; chirp_2 supports word timestamps but Hindi not listed on its page | [S27], [S28] |
| Idempotency key | Not documented | [S31] |
| Retention | "By default, Cloud Speech-to-Text does not log customer audio data or transcripts."; opt-in data logging for discount | [S32] |
| Free tier | V2 table shows no free tier; V1 shows "0 minute to 60 minute $0.00 (Free)" | [S26] |

## 6. Sarvam AI (Saaras)

| Item | Finding | Source |
|---|---|---|
| Model IDs | `saaras:v3` (default), `saaras:v4` (latest); `language_code: "hi-IN"` or `"unknown"`; `mode: codemix` = "Code-mixed text with English words in English and Indic words in native script" | [S33] |
| Price | "Speech to Text: ₹30/hour — Charged per second"; with diarization "₹45/hour" | [S36] |
| Size / duration / formats | REST: "Synchronous processing for files under 30 seconds"; Batch: "Supports files up to 2 hours long", "up to 20 audio files per job". Formats: WAV, MP3, AAC, AIFF, OGG, OPUS, FLAC, MP4/M4A, AMR, WMA, WebM, PCM. Max file size: not verified | [S33], [S34], [S35] |
| Sync/async | Batch job: create job → upload → start → poll → download | [S34] |
| Hindi / code-switching | Hindi `hi-IN`; codemix mode; "Automatic language detection" | [S33], [S34] |
| Timestamps | "Chunk-level timestamps only (not word-level)" | [S34] |
| Idempotency key | Not documented | [S33] |
| Retention | Not verified | [S37] |
| Free tier | "Every new user receives ₹100 worth of free credits" | [S36] |

## 7. Groq (Whisper)

| Item | Finding | Source |
|---|---|---|
| Model IDs | `whisper-large-v3`, `whisper-large-v3-turbo` | [S38] |
| Price | whisper-large-v3 "$0.111" per hour; whisper-large-v3-turbo "$0.04" per hour; "Minimum Billed Length: 10 seconds" | [S38], [S39] |
| Size / duration / formats | Free tier "25 MB", Dev tier "100MB"; formats "flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm". Rate limits (Free and Developer): "ASH 7.2K" (120 audio-min/h), "ASD 28.8K" (480 audio-min/day), RPM 20 | [S38], [S40] |
| Sync/async | Sync only | [S38] |
| Hindi / code-switching | "99+ languages supported"; Hindi not named on Groq pages (Whisper list has `hi`) | [S39], [S6] |
| Timestamps | "Either or both of `word` and `segment`" | [S38] |
| Idempotency key | Not documented | [S38] |
| Retention | "By default, Groq does not retain customer data for inference requests"; troubleshooting logs "up to 30 days"; "All customers may enable Zero Data Retention (ZDR) in Data Controls settings" | [S41] |
| Free tier | Free plan exists with the rate limits above | [S40] |

## 8. Gemini API

| Item | Finding | Source |
|---|---|---|
| Model IDs | `gemini-3.5-transcribe` (dedicated STT, Interactions API); general models `gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-3.5-flash-lite` | [S42], [S45] |
| Price | `gemini-3.5-transcribe`: input "$2.00 or $0.003/min* (audio)", output "$12.00 or $0.002/min* (text)", "effective blended rate of ~$0.005 per min". `gemini-2.5-flash`: "$1.00 (audio)" in, "$2.50" out; Batch "$0.50 (audio)" / "$1.25". `gemini-2.5-flash-lite`: "$0.30 (audio)" in, "$0.40" out; Batch "$0.15" / "$0.20". `gemini-3.5-flash-lite`: "$0.30" in, "$2.50" out | [S42] |
| Size / duration / formats | Transcribe: "Standard unary requests support audio files up to 1 hour. Audio processing is limited to 30 minutes when features like speaker diarization or word-level timestamps are enabled." General audio: "9.5 hours of audio per prompt", "32 tokens per second of audio", inline request "20 MB total", Files API "per-file maximum size of 2 GB". MIME types include `audio/ogg`, `audio/opus`, `audio/m4a`, `audio/aac` | [S45], [S43], [S47] |
| Sync/async | gemini-3.5-transcribe: Batch API "Not supported" (sync unary). 2.5 Flash/Flash-Lite have Batch pricing (50% off) | [S46], [S42] |
| Hindi / code-switching | Transcribe: Hindi `hi-IN` listed; "Automatically detects languages across 85+ locales. Handles intra-sentence and inter-sentential code-switching without manual configuration." | [S45] |
| Timestamps | Transcribe: `timestamp_granularities: ["word"]` → `word_info` with `start_offset`/`end_offset`. General models: prompt-based `MM:SS` only | [S45], [S43] |
| Idempotency key | Not documented | [S45] |
| Retention | Paid: prompts not used to improve products; logs "for a limited period of time" (duration not stated). Free tier: "Used to improve our products: Yes". Files API: "Files are stored for 48 hours." | [S48], [S42], [S47] |
| Free tier | gemini-3.5-transcribe Free Tier "Free of charge" (rate limits not verified) | [S42] |

## Cost estimates (4,500 min/month = 75 h; worst day 600 min = 10 h)

| Provider / model | Unit price (cited) | Monthly | Worst day |
|---|---|---|---|
| OpenAI gpt-4o-mini-transcribe | $0.003/min | $13.50 | $1.80 |
| OpenAI gpt-transcribe | $0.0045/min | $20.25 | $2.70 |
| OpenAI gpt-4o-transcribe / whisper-1 | $0.006/min | $27.00 | $3.60 |
| Deepgram Nova-3 monolingual (PAYG) | $0.0043/min | $19.35 | $2.58 |
| Deepgram Nova-3 multilingual `multi` (PAYG) | $0.0052/min | $23.40 | $3.12 |
| AssemblyAI universal-2 | $0.15/h | $11.25 | $1.50 |
| AssemblyAI universal-3-5-pro | $0.21/h | $15.75 | $2.10 |
| ElevenLabs Scribe v2 (usage rate) | $0.22/h | $16.50 (plan fee excluded; Creator $22 includes 27 h, Pro $99 includes 100 h) | $2.20 |
| Google STT V2 standard | $0.016/min | $72.00 | $9.60 |
| Google STT V2 dynamic batch | $0.003/min | $13.50 | $1.80 |
| Sarvam Saaras (no diarization) | ₹30/h | ₹2,250 (USD not verified) | ₹300 |
| Groq whisper-large-v3-turbo | $0.04/h | $3.00 | $0.40 (exceeds 480 min/day rate limit) |
| Groq whisper-large-v3 | $0.111/h | $8.33 | $1.11 (same limit) |
| Gemini 3.5 Transcribe | ≈ $0.0051/min (1,500 tok/min in at $2/M + 175 tok/min out at $12/M) | $22.95 (Google's blended ~$0.005/min → $22.50) | $3.06 |
| Gemini 2.5 Flash-Lite (standard) | ≈ $0.00065/min (1,920 tok/min in at $0.30/M; assumed 175 tok/min out at $0.40/M) | ≈ $2.91 (Batch: ≈ $1.45) | ≈ $0.39 |
| Gemini 2.5 Flash (standard) | ≈ $0.00236/min | ≈ $10.61 (Batch: ≈ $5.31) | ≈ $1.42 |

Gemini general-model output token counts borrow the Transcribe page's "175 text tokens per minute" assumption; actual output varies with transcript verbosity.

## Facts not verified

- OpenAI: request timeout; free tier; Hindi explicitly for `gpt-transcribe`; audio duration cap (only 25 MB).
- Deepgram: pricing effect of `mip_opt_out`; explicit Hindi on the code-switching page.
- ElevenLabs: Free plan STT hours; code-switching statement; conflicting file-size limits (3 GB vs 5.0 GB).
- Google: whether `chirp_3` is billed under the Standard/Dynamic-batch SKU; Chirp 3 word-timestamp support contradictory; V2 free tier.
- Sarvam: max file size; data retention; INR→USD.
- Groq: $0 free-plan pricing; Hindi named on Groq pages.
- Gemini: rate limits for `gemini-3.5-transcribe`; paid-tier log retention duration.
- Idempotency-Key header: not documented by any of the eight providers.

## Sources

- [S1] https://developers.openai.com/api/docs/pricing
- [S2] https://developers.openai.com/api/docs/guides/speech-to-text
- [S3] https://developers.openai.com/api/docs/models/gpt-transcribe
- [S4] https://developers.openai.com/api/docs/guides/your-data
- [S5] https://developers.openai.com/api/docs/api-reference/audio/createTranscription
- [S6] https://raw.githubusercontent.com/openai/whisper/main/whisper/tokenizer.py
- [S7] https://deepgram.com/pricing
- [S8] https://developers.deepgram.com/docs/models-languages-overview
- [S9] https://developers.deepgram.com/docs/pre-recorded-audio
- [S10] https://developers.deepgram.com/docs/supported-audio-formats
- [S11] https://developers.deepgram.com/docs/callback
- [S12] https://developers.deepgram.com/docs/multilingual-code-switching
- [S13] https://developers.deepgram.com/docs/the-deepgram-model-improvement-partnership-program
- [S14] https://www.assemblyai.com/pricing
- [S15] https://www.assemblyai.com/docs/speech-to-text/pre-recorded-audio
- [S16] https://www.assemblyai.com/docs/speech-to-text/pre-recorded-audio/supported-languages
- [S17] https://www.assemblyai.com/docs/pre-recorded-audio/code-switching
- [S18] https://www.assemblyai.com/docs/pre-recorded-audio/universal-3-5-pro
- [S19] https://www.assemblyai.com/docs/faq/what-audio-and-video-file-types-are-supported-by-your-api
- [S20] https://www.assemblyai.com/docs/data-retention-and-model-training
- [S21] https://www.assemblyai.com/docs/pre-recorded-audio/delete-transcripts
- [S22] https://elevenlabs.io/pricing/api
- [S23] https://elevenlabs.io/docs/api-reference/speech-to-text/convert
- [S24] https://elevenlabs.io/docs/capabilities/speech-to-text
- [S25] https://elevenlabs.io/docs/eleven-api/resources/zero-retention-mode
- [S26] https://cloud.google.com/speech-to-text/pricing
- [S27] https://docs.cloud.google.com/speech-to-text/v2/docs/chirp_3-model
- [S28] https://docs.cloud.google.com/speech-to-text/v2/docs/chirp_2-model
- [S29] https://docs.cloud.google.com/speech-to-text/quotas
- [S30] https://docs.cloud.google.com/speech-to-text/v2/docs/encoding
- [S31] https://docs.cloud.google.com/speech-to-text/v2/docs/reference/rpc/google.cloud.speech.v2
- [S32] https://docs.cloud.google.com/speech-to-text/docs/data-logging
- [S33] https://docs.sarvam.ai/api-reference-docs/speech-to-text/transcribe
- [S34] https://docs.sarvam.ai/api/api-guides-tutorials/speech-to-text/batch-api
- [S35] https://docs.sarvam.ai/api/api-guides-tutorials/speech-to-text/which-api-to-use
- [S36] https://docs.sarvam.ai/api-reference-docs/pricing
- [S37] https://www.sarvam.ai/apis/speech-to-text
- [S38] https://console.groq.com/docs/speech-to-text
- [S39] https://console.groq.com/docs/model/whisper-large-v3-turbo
- [S40] https://console.groq.com/docs/rate-limits
- [S41] https://console.groq.com/docs/your-data
- [S42] https://ai.google.dev/gemini-api/docs/pricing
- [S43] https://ai.google.dev/gemini-api/docs/audio
- [S45] https://ai.google.dev/gemini-api/docs/transcribe
- [S46] https://ai.google.dev/gemini-api/docs/models/gemini-3.5-transcribe
- [S47] https://ai.google.dev/gemini-api/docs/files
- [S48] https://ai.google.dev/gemini-api/terms

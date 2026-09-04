# yt-dlp capabilities for captions, eligibility, and audio

Research for wayfinder ticket [#4](https://github.com/MAX-786/visual-youtube-notes/issues/4), supporting feature-spec sections 8.1 (Submission and validation) and 8.2 (Metadata, captions, and transcription).

**Sources and pinning.** Everything below comes from yt-dlp's own repository: the README, the wiki, the source, `Changelog.md`, and the GitHub releases API. Source links are pinned to commit [`bbc809a`](https://github.com/yt-dlp/yt-dlp/commit/bbc809a1161d3bfca51fa36f59dda35556ee85a0) (master as of 2026-08-30) so that line anchors stay valid; the latest stable release at time of writing is **2026.08.19** (published 2026-08-19, verified via `gh api repos/yt-dlp/yt-dlp/releases/latest`). The YouTube extractor now lives in the package `yt_dlp/extractor/youtube/` (`_video.py`, `_base.py`, `_tab.py`); the old single-file `extractor/youtube.py` no longer exists. Where a fact could not be established from these sources it is marked **Unverified**.

Shorthand used for links: `README` = `https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md`, and source paths are relative to the same commit.

---

## 1. Summary for the worker

| Need (spec ref) | What yt-dlp gives us | Section |
|---|---|---|
| One metadata call per Job (8.2.2) | `yt-dlp -J --skip-download --ignore-no-formats-error --write-auto-subs ... -- <url>` prints one JSON document containing `live_status`, `availability`, `age_limit`, `duration`, `subtitles`, `automatic_captions`, and `formats` | 2, 3 |
| Caption selection order (8.2.2) | Human tracks are keys of `subtitles`; ASR tracks are keys of `automatic_captions`; the original-language ASR track is additionally keyed `<lang>-orig`. yt-dlp has no cross-language priority, so the worker orders the keys itself, then downloads with `--load-info-json` | 2 |
| Eligibility rejections (8.1) | Live / upcoming / geo / age / members-only / bot-check each map to a field value or an `expected=True` error with a recognisable prefix | 3 |
| Audio without re-encoding (8.2.5) | `-f "ba[acodec^=opus]/ba[ext=m4a]/ba"` downloads a bare Opus-in-WebM or AAC-in-M4A stream; `-x --audio-format best` is a stream copy, not a transcode | 4 |
| Non-interactive server run (8.2.1, 8.4) | A fixed argv (section 5.3) with `--no-config --no-update --no-cookies --no-playlist --abort-on-error --socket-timeout --retries ... --paths temp:<jobdir>` | 5 |
| Transient vs permanent classification (8.4) | Exit code is 1 for almost everything; classify on stderr: the literal `please report this issue on  https://github.com/yt-dlp/yt-dlp/issues` suffix marks extractor breakage, `Unable to download webpage|API page` marks network failure, everything else with `ERROR:` is a legitimate unavailability | 6 |
| Keeping yt-dlp current in a container | Pin a CalVer tag at image build, install Deno >= 2.3 and ffmpeg alongside, rebuild to update, keep the previous image tag to roll back | 7 |

---

## 2. Caption tracks

### 2.1 Listing without downloading

- `--list-subs`: "List available subtitles of each video. Simulate unless --no-simulate is used" ([README L897-898](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L897-L898)). It is documented as equivalent to `--print automatic_captions_table --print subtitles_table` ([README L2384](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L2384)). Human-readable; not for the worker.
- `-J, --dump-single-json`: "Quiet, but print JSON information for each URL or infojson passed. Simulate unless --no-simulate is used" ([README L795-799](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L795-L799)). This is the call the worker should make; the JSON carries both caption dictionaries. `-j` is the per-video variant; for a single `watch?v=` URL they are equivalent.
- `--skip-download`: "Do not download the video but write all related files (Alias: --no-download)" ([README L775-776](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L775-L776)). Needed later when writing subtitle files without fetching media.

### 2.2 Shape of `subtitles` and `automatic_captions`

The info-dict contract is the docstring in `extractor/common.py`, not the README:

> `subtitles: The available subtitles as a dictionary in the format {tag: subformats}. "tag" is usually a language code, and "subformats" is a list sorted from lower to higher preference, each element is a dictionary with the "ext" entry and one of: * "data": The subtitles file contents * "url": A URL pointing to the subtitles file. It can optionally also have: * "name" ... * "http_headers" ... * "impersonate" ...`
> `automatic_captions: Like 'subtitles'; contains automatically generated captions instead of normal subtitles`
> ([common.py L331-345](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L331-L345))

On YouTube each language entry is a list with one dict per format in `_SUBTITLE_FORMATS` order, each holding `ext`, `url` (a full `https://www.youtube.com/api/timedtext?...&fmt=<ext>` URL), `name`, and `impersonate: True` ([_video.py L4207-4218](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4207-L4218)). Internal `__yt_dlp_client` keys are stripped from JSON output by `sanitize_info` ([YoutubeDL.py L3753-3758](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3753-L3758)).

### 2.3 Human vs automatic, and the language keys

All from `yt_dlp/extractor/youtube/_video.py`:

- Tracks come from `captions.playerCaptionsTracklistRenderer.captionTracks` of each player response that also has `streamingData` ([L4241-4248](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4241-L4248)).
- **Human vs ASR**: `is_manual_subs = caption_track.get('kind') != 'asr'`; manual tracks go to `subtitles`, ASR to `automatic_captions` ([L4285-4290](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4285-L4290)).
- **Key derivation**: `get_lang_code` strips a leading `.` from YouTube's `vssId` and replaces `.` with `-`, else falls back to `languageCode`. So `.en` -> `en`, `.en-GB` -> `en-GB`, ASR `a.en` -> `a-en`, and the `a-` prefix is removed before insertion ([L4203-4205](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4203-L4205), [L4315-4328](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4315-L4328)). YouTube's codes pass through verbatim; yt-dlp does not assert they are BCP-47 (**Unverified** beyond pass-through).
- **`-orig` marks the original-language ASR track**. When the ASR track's language equals the video's original audio language the extractor adds `f'{lang}-orig'` named `"<Name> (Original)"`, with the comment "Add an "-orig" label to the original language so that it can be distinguished. The subs are returned without "-orig" as well for compatibility" ([L4302-4309](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4302-L4309)).
- **Every translation target becomes an `automatic_captions` key.** For an ASR track, each entry in `translationLanguages` is added under its own code with `tlang=<code>` on the URL ([L4291-4313](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4291-L4313)). Consequence for the spec's order: `automatic_captions["hi"]` on an English-audio video is a machine translation of English ASR, not Hindi ASR. Only `hi-orig` (or `hi` when `hi-orig` also exists) means Hindi speech.
- Translations of a *manual* track are keyed `<target>-<source>` (e.g. `hi-en`, name "Hindi from English") and are only built when `--write-auto-subs` or `--list-subs` is set and `youtube:skip=translated_subs` is not passed ([L4236-4238](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4236-L4238), [L4294-4300](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4294-L4300)). The spec does not want translations, so pass `--extractor-args "youtube:skip=translated_subs"` to keep the JSON small ([README L1861](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1861)).
- Live chat appears as `subtitles["live_chat"]` ([L4396-4403](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4396-L4403)); ignore that key.
- Only web/mweb clients return `translationLanguages` ([L4229](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4229)); current defaults are `_DEFAULT_CLIENTS = ('visionos', 'web')` ([L143](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L143)), so do not override `player_client`.

### 2.4 Formats

- YouTube exposes `_SUBTITLE_FORMATS = ('json3', 'srv1', 'srv2', 'srv3', 'ttml', 'srt', 'vtt')` ([L142](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L142)); the list is "sorted from lower to higher preference" so `best` = `vtt`. `srt` is served natively since 2025.06.09 ([Changelog L1066](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md#L1066)), so no ffmpeg is needed for SRT.
- `--sub-format FORMAT`: "Subtitle format; accepts formats preference separated by "/", e.g. "srt" or "ass/srt/best"" ([README L899-900](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L899-L900)); resolution picks the first listed ext that exists, else falls back to best with a warning ([YoutubeDL.py L3193-3221](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3193-L3221)).
- Recommendation: `--sub-format "json3/vtt/best"`. `json3` carries per-word timing and is the easiest to parse for the 8.2.3 word count and repeated-line check; `vtt` is the fallback.
- `--convert-subs` is an ffmpeg post-processor and cannot convert JSON input ([ffmpeg.py L966-980](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/postprocessor/ffmpeg.py#L966-L980)); not needed.

### 2.5 Flags and file naming

- `--write-subs` / `--write-auto-subs` (default off for both), `--sub-langs LANGS` ("can be regex ... separated by commas, e.g. --sub-langs "en.*,ja" ... prefix ... with a "-" to exclude") ([README L890-909](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L890-L909)). Matching is `re.fullmatch`, case-insensitive ([_utils.py L5323-5350](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L5323-L5350)), so `--sub-langs en` matches only `en`, not `en-orig` or `en-GB`.
- When `--sub-langs` is omitted yt-dlp picks one language: manual `en`, then manual `en*`, then auto `en`, then auto `en*`, then the first available ([YoutubeDL.py L3183-3189](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3183-L3189)). Always pass it explicitly.
- `--all-subs` is deprecated (`--sub-langs all --write-subs`) ([README L2417](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L2417)).
- Filename: `-o "subtitle:<template>"` sets the subtitle template ([README L1312](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1312)); the `%(ext)s` placeholder is replaced by `<lang>.<subext>` via `subtitles_filename` ([YoutubeDL.py L4459-4466](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L4459-L4466), [_utils.py L1322-1323](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L1322-L1323)). `-o "subtitle:%(id)s.%(ext)s"` yields `<id>.en-orig.json3`.

### 2.6 Implementing the spec's selection order

1. Run the metadata call once (section 5.3) and parse `subtitles` and `automatic_captions` from stdout.
2. Choose, in order: `subtitles` key matching `^(en|hi)(-[A-Za-z]+)?$` (human English/Hindi; `en-GB`, `en-IN`, `hi-Latn` qualify); then `automatic_captions` key `en-orig` or `hi-orig` (original-language ASR); then, only if no `-orig` key exists at all (older or non-translatable tracks), `automatic_captions["en"]` / `["hi"]`; then any remaining `subtitles` key, then any remaining `automatic_captions` `-orig` key, for the Hinglish-detection step. Skip keys that are translations (`tlang=` in the URL, or `<x>-<y>` where `y` is another language). yt-dlp itself provides no cross-language priority; `process_subtitles` merges auto captions under manual ones per language only ([YoutubeDL.py L3157-3167](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3157-L3167)).
3. Download by re-running yt-dlp against the saved JSON rather than fetching the URL yourself: `--load-info-json <file> --skip-download --write-subs --write-auto-subs --sub-langs "<chosen-key>" --sub-format "json3/vtt/best" -o "subtitle:%(id)s.%(ext)s"`. `--load-info-json` runs `process_ie_result(info, download=True)` without re-extracting ([YoutubeDL.py L3717-3737](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3717-L3737)); if the subtitle download fails it warns "The info failed to download ... trying with URL" and re-extracts.
4. Why not `http.Get` the `url` from Go: yt-dlp marks every YouTube subtitle entry `impersonate: True` and the HTTP downloader honours it ([downloader/http.py L28-30](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/downloader/http.py#L28-L30)). That change (2025.07.21, "[ie/youtube] Use impersonation for downloading subtitles", [Changelog L965](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md#L965), [PR #13786](https://github.com/yt-dlp/yt-dlp/pull/13786)) fixed `HTTP Error 429` on timedtext requests ([issue #13770](https://github.com/yt-dlp/yt-dlp/issues/13770)). A plain Go client would be back in the pre-fix state. Impersonation needs the `curl_cffi` extra ([README L227-228](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L227-L228)); it is included in most official binaries except the zipimport `yt-dlp` and the Windows x86 build.
5. **Unverified**: whether timedtext URLs expire. Nothing in yt-dlp's docs or extractor inspects an expiry for captions. Treat the JSON as valid for the duration of one Attempt only.

### 2.7 Caption-specific caveats

- **PO tokens for subtitles** exist since 2025.05.22 ([Changelog L1114](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md#L1114)). Policy for web clients is `required=False` today with the comment "In rollout, currently detected via experiment" ([_base.py L91-93](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_base.py#L91-L93)). When YouTube flags the experiment (`exp=xpe|xpv` in `baseUrl`) and no token is available, that client's captions are dropped with the warning `Some <client> client subtitles require a PO Token which was not provided. They will be discarded` ([_video.py L4264-4282](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4264-L4282), [L3210-3215](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3210-L3215)). The worker should log that warning; if it becomes common, the fix is a PO Token provider plugin per the [PO Token Guide](https://github.com/yt-dlp/yt-dlp/wiki/PO-Token-Guide), not code changes.
- The spec's "confirmed missing" case: empty `subtitles` and no `-orig`/`en`/`hi` key in `automatic_captions` after a successful `-J` run, with no PO-token warning on stderr. If the warning is present, treat as transient and retry once before falling back to STT.

---

## 3. Eligibility signals in the metadata JSON

### 3.1 Getting metadata even when the video is not downloadable

Without help, an upcoming, geo-blocked, or sign-in-gated video makes extraction fail (exit 1, no JSON). `--ignore-no-formats-error` ("Ignore "No video formats" error. Useful for extracting metadata even if the videos are not actually available for download (experimental)", [README L769-772](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L769-L772)) turns `raise_no_formats(expected=True)`, `raise_login_required(metadata_available=True)`, and `raise_geo_restricted(metadata_available=True)` into `WARNING:` lines while extraction continues with an empty `formats` list ([common.py L1245-1270](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L1245-L1270)). Geo support for this flag landed in 2025.08.11 ([Changelog L864](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md#L864)). Use it on the metadata Stage so the worker can produce the spec's "clear reason" from fields plus the warning text, then decide, rather than parsing an error alone.

Do **not** pass `--wait-for-video`; its default is off ("Do not wait for scheduled streams (default)", [README L408-411](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L408-L411)) and with it set yt-dlp sleeps until `release_timestamp` ([YoutubeDL.py L1776-1796](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L1776-L1796)).

### 3.2 Field reference

Docstrings from `extractor/common.py`:

| Field | Values | Source |
|---|---|---|
| `live_status` | `None` (unknown), `'is_live'`, `'is_upcoming'`, `'was_live'`, `'not_live'`, `'post_live'` ("was live, but VOD is not yet processed") | [L392-394](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L392-L394) |
| `is_live`, `was_live` | `True`, `False`, or `None` | [L388-391](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L388-L391) |
| `release_timestamp` | UNIX timestamp of release (for YouTube: `liveBroadcastDetails.startTimestamp`) | [L311](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L311), [_video.py L4131](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4131) |
| `availability` | `'private'`, `'premium_only'`, `'subscriber_only'`, `'needs_auth'`, `'unlisted'`, `'public'`, or `None` | [L413-416](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L413-L416), precedence in `_availability` [L4025-4036](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L4025-L4036) |
| `age_limit` | integer years; YouTube sets `18` or `0` | [L380](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L380), [_video.py L4183-4186](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4183-L4186) |
| `duration` | seconds, int or float; absent for an in-progress stream | [L346](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L346) |

YouTube computes `live_status` from `videoDetails.isLive` / `liveBroadcastDetails.isLiveNow`, `isLiveContent`, `isUpcoming`, `isPostLiveDvr`:

```python
live_status = ('post_live' if post_live
               else 'is_live' if is_live
               else 'is_upcoming' if is_upcoming
               else 'was_live' if live_content
               else 'not_live' if False in (is_live, live_content)
               else None)
```
([_video.py L3863-3875](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3863-L3875))

`availability` is set from watch-page badges and `videoDetails.isPrivate`, with `needs_auth=info['age_limit'] >= 18` ([_video.py L4570-4586](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4570-L4586)); the members-only badge is `BADGE_STYLE_TYPE_MEMBERS_ONLY -> AVAILABILITY_SUBSCRIPTION` ([_base.py L1126](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_base.py#L1126)).

### 3.3 Where the error text comes from

The YouTube extractor does not hard-code most user-facing reasons. When no formats are found it takes YouTube's own `playabilityStatus.reason` and `errorScreen.playerErrorMessageRenderer.subreason`, joins them, and calls `raise_no_formats(reason, expected=True)` ([_video.py L4047-4076](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4047-L4076)). Only three substrings are matched in code: `The uploader has not made this video available in your country` (geo), `sign in` (login/bot/age), and `This content isn't available, try again later` (rate limit). Strings such as "Sign in to confirm you're not a bot", "Join this channel to get access to members-only content", "Private video", "Video unavailable", "Premieres in ..." are YouTube's text and appear in the repo only as test `skip:` notes; their exact wording is **Unverified** from yt-dlp sources and may change without a yt-dlp release.

### 3.4 Decision table

| Condition (spec 8.1) | JSON signal (with `--ignore-no-formats-error`) | stderr signal | Raised as |
|---|---|---|---|
| Active livestream | `live_status == "is_live"`, `is_live == true`, `duration` absent | none (extraction succeeds) | n/a. Default is `--no-live-from-start`, "Download livestreams from the current time (default)" ([README L406-407](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L406-L407)); the worker must reject on the field, not rely on an error |
| Stream just ended, VOD not ready | `live_status == "post_live"` | none | Reject or defer; formats are marked "(Last 2 hours)" ([_video.py L4143-4158](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4143-L4158)) |
| Scheduled premiere / upcoming stream | `live_status == "is_upcoming"`, `release_timestamp` set, `formats == []` | `WARNING: [youtube] <id>: <YouTube reason>` (an `ERROR:` without the flag) | `raise_no_formats(expected=True)`; playability status is `LIVE_STREAM_OFFLINE` ([_video.py L3176](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3176)) |
| Completed livestream/premiere (eligible) | `live_status == "was_live"`, `duration` present | none | n/a |
| Region block | `formats == []`; no dedicated field | text begins `The uploader has not made this video available in your country`; YoutubeDL appends `This video is available in <countries>.` and `You might want to use a VPN or a proxy server (with --proxy) to workaround.` | `GeoRestrictedError` (always `expected=True`, carries `.countries`) via `raise_geo_restricted(subreason, countries, metadata_available=True)` ([_video.py L4055-4061](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4055-L4061), [common.py L1255-1262](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L1255-L1262), [YoutubeDL.py L1740-1746](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L1740-L1746)). YouTube sets `_GEO_BYPASS = False` ([_video.py L151](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L151)), so the built-in X-Forwarded-For bypass never runs; the block is final |
| Age restriction | `age_limit == 18`, `availability == "needs_auth"` | to-screen note `This video is age-restricted; some formats may be missing without authentication.` plus the cookies hint; if the `web_embedded` fallback also fails, reason contains `confirm your age` with the login hint appended | `raise_no_formats(expected=True)`. Detection via `_is_agegated` (`desktopLegacyAgeGateReason`, or reason/status containing `confirm your age`, `age-restricted`, `inappropriate`, `age_verification_required`, `age_check_required`) ([_video.py L2895-2904](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L2895-L2904), [L3156-3163](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3156-L3163)). Because the spec allows no cookies, reject on `age_limit >= 18` regardless of whether formats were found: the `web_embedded` workaround "only successfully works around the age-restriction sometimes (e.g. if the video is embeddable)" ([README L1862](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1862)) and the broken `tv_embedded` client was removed in 2026.01.31 ([Changelog L373](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md#L373)) |
| Members-only | `availability == "subscriber_only"` | YouTube's reason text (no `sign in`, so no hint appended) | `raise_no_formats(expected=True)` |
| Private / removed | `availability == "private"` when the badge is present; usually extraction fails before metadata is complete | YouTube's reason text | `raise_no_formats(expected=True)` |
| Bot / cookie / login challenge | `formats == []` | reason contains `sign in`; yt-dlp strips `This helps protect our community. Learn more` and appends `Use --cookies-from-browser or --cookies for the authentication. See https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp ... Also see https://github.com/yt-dlp/yt-dlp/wiki/Extractors#exporting-youtube-cookies ...` | `raise_no_formats(expected=True)` ([_video.py L4064-4066](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4064-L4066), hint at [_base.py L699-702](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_base.py#L699-L702), [common.py L595-605](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L595-L605)). Match on the hint URL `wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp`, which is yt-dlp's own stable text |
| Captcha | as above | reason gets `. YouTube is requiring a captcha challenge before playback` appended | same block ([_video.py L4067-4068](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4067-L4068)) |
| IP rate-limited | as above | `has been rate-limited by YouTube for up to an hour. It is recommended to use `-t sleep`` | same block ([_video.py L4069-4075](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4069-L4075)); treat as transient with a >= 1 h backoff. `All player responses are invalid. Your IP is likely being blocked by Youtube` ([L3186-3187](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3186-L3187)) is the harder variant |
| Over two hours | `duration > 7200` | none | Check in Go after `-J`; do not use `--match-filters "duration <= 7200"` because a comparison on a missing field rejects unless written `<=?`, and a rejected video exits 0 with no JSON ([_utils.py L3290-3291](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L3290-L3291), [YoutubeDL.py L1662-1665](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L1662-L1665)) |
| DRM | `formats == []` | `This video is DRM protected` | `report_drm` -> `raise_no_formats(expected=True)` ([common.py L1224-1227](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L1224-L1227)) |

Worker rule of thumb after the metadata call: reject when `live_status in {"is_live","is_upcoming","post_live"}`, `age_limit >= 18`, `availability in {"private","premium_only","subscriber_only","needs_auth"}`, `duration > 7200`, or `formats` is empty (in which case the last `WARNING:`/`ERROR:` line classifies the reason).

### 3.5 Playlist and URL forms

The `YoutubeIE` regex accepts `youtube.com/watch`, `youtu.be/<id>`, `/shorts/`, `/live/`, `/embed/`, `/v/` and more ([_video.py L86-108](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L86-L108)) but returns `suitable() == False` whenever the query has `list=` ([L1923-1929](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L1923-L1929)); such URLs go to `YoutubeTabIE`, which calls `_yes_playlist` and, with `--no-playlist`, prints `Downloading just the video <id> because of --no-playlist` and returns a plain `watch?v=` result ([_tab.py L2317-2330](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_tab.py#L2317-L2330), [common.py L4051-4066](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L4051-L4066)). Because 8.1 already normalises every URL to a Video ID, the worker should build `https://www.youtube.com/watch?v=<id>` itself and still pass `--no-playlist` as a belt-and-braces guard.

---

## 4. Audio-only download without re-encoding

### 4.1 What YouTube's audio formats look like in yt-dlp

yt-dlp no longer keeps a hard-coded itag table for these; each entry in `streamingData.adaptiveFormats` becomes a format whose `format_id` is the itag string and whose `ext`/`acodec` are derived from `mimeType` via `mimetype2ext` and `parse_codecs` ([_video.py L3446-3486](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3446-L3486)): `audio/webm; codecs="opus"` -> `ext: webm`, `acodec: opus`; `audio/mp4; codecs="mp4a.40.2"` -> `ext: m4a`, `acodec: mp4a.40.2` ([_utils.py L2982-2986](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L2982-L2986), [L3056-3081](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L3056-L3081)). Audio-only formats get `vcodec: none` and `container: webm_dash` / `m4a_dash`. The extractor iterates both `streamingData.formats` (muxed, progressive) and `streamingData.adaptiveFormats` ([L3394](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3394)); the single-stream audio entries are the adaptive ones, so `bestaudio` always resolves to a DASH-style stream, which `--fragment-retries` and `--no-part` apply to.

The static `_formats` itag table was deleted in commit [d9e3011](https://github.com/yt-dlp/yt-dlp/commit/d9e3011fd1c3a75871a50e78533afe78ad427ce3) (shipped in 2025.11.12). The last release carrying it, [2025.09.05](https://github.com/yt-dlp/yt-dlp/blob/2025.09.05/yt_dlp/extractor/youtube/_video.py#L204-L244), listed the audio-only itags 139/140/141/256/258 (m4a, aac), 171/172 (webm, vorbis) and 249/250/251 (webm, opus, 50/70/160 kbps), all tagged `DASH audio`. Treat that as historical: select by `acodec`/`ext`, never by itag. Itag 774 (Opus) does not appear in any yt-dlp source and is **Unverified**.

### 4.2 Selector

Format-selection grammar ([README L1513-1604](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1513-L1604)):

- `ba` / `bestaudio`: "Select the best quality audio-only format. Equivalent to `best*[vcodec=none]`".
- String filters `=`, `^=`, `$=`, `*=`, `~=` apply to `ext`, `acodec`, `container`, `protocol`; `/` is left-to-right fallback.
- yt-dlp's own presets use exactly this shape: `-t aac` = `-f 'ba[acodec^=aac]/ba[acodec^=mp4a.40.]/ba/b' -x --audio-format aac` ([README L1172-1174](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1172-L1174)).

Recommended: `-f "ba[acodec^=opus]/ba[ext=m4a]/ba"`. First choice is Opus (yt-dlp's `-S acodec` order also ranks `opus > ... > aac > mp4a`, [README L1619](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1619)), then AAC in M4A, then whatever audio-only stream exists. No `+` merge is involved, so ffmpeg is not required for the download itself. Add `--max-filesize` if a provider cap is known in bytes; note it aborts rather than picking a smaller format ([README L493-494](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L493-L494)).

### 4.3 Does `-x` re-encode?

`-x, --extract-audio` "Convert video files to audio-only files (requires ffmpeg and ffprobe)"; `--audio-format` supports `best (default), aac, alac, flac, m4a, mp3, opus, vorbis, wav` ([README L942-948](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L942-L948)). `FFmpegExtractAudioPP.run` probes the file's codec and then:

- `filecodec == 'aac'` and target `m4a`/`best`: stream copy (`acodec = 'copy'`, `-bsf:a aac_adtstoasc`); if the input is already `.m4a` it prints "Not converting audio ...; file is already in target format" and does nothing.
- `target == 'best'` or `target == filecodec` (e.g. Opus in WebM with `--audio-format opus` or `best`): stream copy into the codec's native container, `.opus` for Opus.
- Anything else: real transcode with `libopus`/`aac`/`libmp3lame`, subject to `--audio-quality`.
([ffmpeg.py L432-537](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/postprocessor/ffmpeg.py#L432-L537), `ACODECS` at [L50-60](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/postprocessor/ffmpeg.py#L50-L60))

So `-x --audio-format best` is lossless for both Opus and AAC inputs, but it adds an ffprobe plus ffmpeg pass and rewrites the container (`.webm` -> `.opus`). The trap is a **fixed** `--audio-format` combined with a fallback selector: `-x --audio-format opus` transcodes whenever the `ba[ext=m4a]` branch was taken, and `--audio-format m4a` transcodes Opus with `aac`. Also note that `-x` without `-f` silently sets the selector to `bestaudio/best` ([__init__.py L594-596](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L594-L596)).

Since the worker already needs ffmpeg for the 20-minute chunking in 8.2.5, the simplest path is to skip `-x` entirely, accept `.webm` (Opus) or `.m4a` (AAC) from `-f`, and hand that file to ffmpeg once for chunking with `-c copy`. If a transcription provider refuses the WebM container, add `--remux-video "webm>opus"`: `FFmpegVideoRemuxerPP` uses `stream_copy_opts()` (`-map 0 -dn -ignore_unknown -c copy`), matches rules on the source *ext*, and skips non-matching files such as `.m4a` ([ffmpeg.py L573-578](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/postprocessor/ffmpeg.py#L573-L578), [L213-221](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/postprocessor/ffmpeg.py#L213-L221); README `--remux-video` text at [L953-961](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L953-L961)). That needs ffmpeg but not ffprobe.

### 4.4 Environment dependencies for audio

- ffmpeg/ffprobe are "Required for merging separate video and audio files, as well as for various post-processing tasks" and must be the binaries, "NOT the Python package of the same name" ([README L207-211](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L207-L211)); `--ffmpeg-location PATH` points at them ([README L1043-1044](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1043-L1044)).
- Without a JS runtime the extractor warns "No supported JavaScript runtime could be found ... YouTube extraction without a JS runtime has been deprecated, and some formats may be missing" and falls back to `_DEFAULT_JSLESS_CLIENTS = ('visionos',)` ([_video.py L2994-2999](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L2994-L2999), [L144](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L144)). See section 7.
- GVS PO tokens: formats that need one and lack it are skipped ("They will be skipped as they may yield HTTP Error 403"); for the default clients this is only logged at debug level ([_video.py L3196-3208](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3196-L3208)). SABR-only sessions drop https formats with a warning referencing [issue #12482](https://github.com/yt-dlp/yt-dlp/issues/12482) ([L3538-3549](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3538-L3549)). If `-f` finds nothing, yt-dlp raises `Requested format is not available`; treat as transient once, then permanent.

---

## 5. Non-interactive server flags

### 5.1 Flag reference

README descriptions, quoted:

| Flag | README text | Line |
|---|---|---|
| `--no-config` (alias of `--ignore-config`) | "Don't load any more configuration files except those given to --config-locations" | [L344-349](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L344-L349) |
| `--no-cookies` | "Do not read/dump cookies from/to file (default)" | [L719-720](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L719-L720) |
| `--no-cookies-from-browser` | "Do not load cookies from browser (default)" | [L736](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L736) |
| `--cache-dir DIR` / `--no-cache-dir` | "Location in the filesystem where yt-dlp can store some downloaded information (such as client ids and signatures) permanently. By default ${XDG_CACHE_HOME}/yt-dlp" / "Disable filesystem caching" | [L737-741](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L737-L741) |
| `--no-update` | "Do not check for updates (default)"; also silences the 90-day-old warning | [L310](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L310), [L192-193](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L192-L193) |
| `--no-playlist` | "Download only the video, if the URL refers to a video and a playlist" | [L530-531](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L530-L531) |
| `--no-progress` | "Do not print progress bar" | [L805](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L805) |
| `--color never` | `--color [STREAM:]POLICY` ... "always", "auto" (default), "never", or "no_color"; `--no-colors` is a deprecated alias of `--color no_color` | [L414-420](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L414-L420), [L2408](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L2408) |
| `--socket-timeout SECONDS` | "Time to wait before giving up, in seconds" | [L455](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L455) |
| `-R, --retries` | "Number of retries (default is 10), or "infinite"" | [L564-565](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L564-L565) |
| `--fragment-retries` | "Number of retries for a fragment (default is 10), or "infinite" (DASH, hlsnative and ISM)" | [L568-569](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L568-L569) |
| `--file-access-retries` | "Number of times to retry on file access error (default is 3)" | [L566-567](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L566-L567) |
| `--extractor-retries` | "Number of retries for known extractor errors (default is 3), or "infinite"" | [L1148-1149](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1148-L1149) |
| `--retry-sleep [TYPE:]EXPR` | "Time to sleep between retries in seconds (optionally) prefixed by the type of retry (http (default), fragment, file_access, extractor) ... EXPR can be a number, linear=START[:END[:STEP=1]] or exp=START[:END[:BASE=2]]" | [L570-578](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L570-L578) |
| `-r, --limit-rate RATE` | "Maximum download rate in bytes per second, e.g. 50K or 4.2M" | [L559-560](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L559-L560) |
| `--throttled-rate RATE` | "Minimum download rate in bytes per second below which throttling is assumed and the video data is re-extracted" | [L561-563](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L561-L563) |
| `--sleep-requests SECONDS` | "Number of seconds to sleep between requests during data extraction" | [L840-841](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L840-L841) |
| `--max-filesize SIZE` | "Abort download if filesize is larger than SIZE, e.g. 50k or 44.6M" | [L493-494](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L493-L494) |
| `-P, --paths [TYPES:]PATH` | ""home" (default) and "temp" paths. All intermediary files are first downloaded to the temp path and then the final files are moved over to the home path ... This option is ignored if --output is an absolute path" | [L653-663](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L653-L663) |
| `-o, --output [TYPES:]TEMPLATE` | "Output filename template"; literal text is allowed, and "To use percent literals in an output template use `%%`" | [L664-665](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L664-L665), [L1466](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1466) |
| `--output-na-placeholder TEXT` | "Placeholder for unavailable fields in --output (default: "NA")" | [L666-667](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L666-L667) |
| `--restrict-filenames` | "Restrict filenames to only ASCII characters, and avoid "&" and spaces in filenames" | [L668-669](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L668-L669) |
| `--no-continue`, `--no-part`, `--no-mtime`, `-w`/`--force-overwrites` | resume / `.part` / mtime / overwrite behaviour | [L677-694](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L677-L694) |
| `--abort-on-error` | "Abort downloading of further videos if an error occurs (Alias: --no-ignore-errors)"; default is `--no-abort-on-error` | [L320-324](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L320-L324) |
| `--no-warnings`, `-q` | "Ignore warnings" / quiet mode | [L761-764](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L761-L764) |
| `--print`, `--print-to-file` | template printing; "Implies --quiet. Implies --simulate unless --no-simulate" | [L777-790](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L777-L790) |
| `--proxy`, `--source-address`, `-4` | network binding | [L450-464](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L450-L464) |
| `--user-agent` | deprecated alias of `--add-headers "User-Agent:UA"`; do not set, the extractor manages client UAs | [L2402](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L2402) |
| `--no-check-certificates` | "Suppress HTTPS certificate validation"; do not use | [L831](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L831) |

Notes:

- `--no-cookies` does exist and is the default; there is no cookie jar unless `--cookies FILE` is passed. Passing both `--no-cookies` and `--no-cookies-from-browser` explicitly makes the intent visible in logs and defends against a stray config file.
- `--no-config` matters because yt-dlp otherwise reads `/etc/yt-dlp.conf`, `~/.config/yt-dlp/config`, and a `yt-dlp.conf` in the working directory ([README, CONFIGURATION](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L1185)).
- Cache: the cache dir holds "client ids and signatures", i.e. solved player challenges ([_video.py L3339](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3339)). `--no-cache-dir` forces a Deno solve on every run; a fixed writable `--cache-dir /var/cache/yt-dlp` inside the container avoids that at no correctness cost, since the cache holds nothing user-specific. Either is acceptable; pick the fixed dir unless the container filesystem is read-only.
- `--socket-timeout` defaults to 20 s when unset (`DEFAULT_TIMEOUT = 20`, [networking/common.py L34](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/networking/common.py#L34)).
- `--abort-on-error` makes a download-phase failure raise `DownloadError` instead of being recorded in `_download_retcode` and continued past; for a single-URL job the exit code is 1 either way, but the abort form stops yt-dlp from attempting post-processing on a partial file.
- `--max-filesize` is enforced by the HTTP downloader only when a `Content-Length` is known ([downloader/http.py L216-227](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/downloader/http.py#L216-L227)); the `duration` check in Go is the reliable guard.
- Do **not** pass `--no-warnings` or `-q`: warnings are the degraded-state signal in section 6.4, and quiet mode hides `ERROR:` context. `-J` already suppresses the progress noise on stdout.
- `--socket-timeout` is per socket operation, not per run; the per-Attempt wall clock comes from Go's context (section 6.1).
- The output template can contain arbitrary literal text, so `-o "job-<jobID>.attempt-<n>.%(id)s.%(ext)s"` is legal. Only `%` needs escaping (`%%`); the worker generates the IDs itself so no user text reaches the template.

### 5.2 Retry philosophy

yt-dlp retries internally (`--retries` 10, `--fragment-retries` 10, `--extractor-retries` 3 by default) and the spec's Attempt model retries again on top (at most three Attempts per Stage). Keep yt-dlp's internal counts low (`--retries 3 --fragment-retries 3 --extractor-retries 2 --retry-sleep exp=1:8`) so that a dead network surfaces to the Attempt within the per-attempt timeout rather than being masked by ten silent retries inside one process.

### 5.3 Recommended argv per Stage

Common prefix for every call:

```
yt-dlp --no-config --cache-dir /var/cache/yt-dlp --no-update --no-cookies --no-cookies-from-browser
       --no-playlist --abort-on-error --no-progress --color never --restrict-filenames
       --socket-timeout 30 --retries 3 --fragment-retries 3 --extractor-retries 2
       --retry-sleep exp=1:8 --sleep-requests 0.5
       --paths "temp:<jobdir>" --paths "home:<jobdir>"
       --output "job-<jobID>.attempt-<n>.%(id)s.%(ext)s"
       --output "subtitle:job-<jobID>.attempt-<n>.%(id)s.%(ext)s"
```

Metadata Stage (one call, stdout is the JSON):

```
... -J --skip-download --ignore-no-formats-error --write-auto-subs
    --extractor-args "youtube:skip=translated_subs" -- https://www.youtube.com/watch?v=<id>
```

Captions Stage (after the worker picked a key from the saved JSON):

```
... --load-info-json <jobdir>/info.json --skip-download --write-subs --write-auto-subs
    --sub-langs "<key>" --sub-format "json3/vtt/best"
```

Transcribing Stage (audio only, no post-processing):

```
... -f "ba[acodec^=opus]/ba[ext=m4a]/ba" --no-part --no-mtime
    -- https://www.youtube.com/watch?v=<id>
```

`--write-auto-subs` on the metadata call is what makes the extractor enumerate ASR tracks fully; it does not write files because `-J` simulates ([README L795-799](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L795-L799)).

---

## 6. Invoking yt-dlp from Go and classifying failures

### 6.1 Process invocation

Go's `os/exec` "intentionally does not invoke the system shell and does not expand any glob patterns or handle other expansions, pipelines, or redirections typically done by shells" ([pkg.go.dev/os/exec](https://pkg.go.dev/os/exec)). Building the argument slice directly and never passing through `sh -c` is the whole injection defence; a string such as `https://youtu.be/abc; rm -rf /` is one argv element and reaches yt-dlp verbatim.

Two more rules keep the argv unambiguous:

- Pass the canonical watch URL the worker constructed from the Video ID, not the user-supplied string. 8.1's normaliser has already reduced user input to an 11-character ID.
- Put `--` before the URL. yt-dlp's parser is built on Python's `optparse` ([options.py](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/options.py)), so anything after `--` is positional even if it begins with `-`. Video IDs can start with `-`.

Illustrative shape (not executed against YouTube as part of this research):

```go
ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
defer cancel()

args := append(commonArgs(jobID, attempt, jobDir),
    "-J", "--skip-download", "--ignore-no-formats-error", "--write-auto-subs",
    "--extractor-args", "youtube:skip=translated_subs",
    "--", "https://www.youtube.com/watch?v="+videoID)

cmd := exec.CommandContext(ctx, ytdlpPath, args...)
cmd.Dir = jobDir
cmd.Env = []string{"PATH=" + binDir, "HOME=" + jobDir, "TMPDIR=" + jobDir, "LANG=C.UTF-8"}
cmd.WaitDelay = 5 * time.Second
var stdout, stderr bytes.Buffer
cmd.Stdout, cmd.Stderr = &stdout, &stderr
err := cmd.Run()
```

- `exec.CommandContext`: when the context is done, the command's `Cancel` function runs, which by default calls `os.Process.Kill` ([pkg.go.dev/os/exec#CommandContext](https://pkg.go.dev/os/exec#CommandContext)). This is the per-Attempt timeout the spec asks for in 8.2.6.
- `Cmd.WaitDelay` bounds how long `Wait` blocks after cancellation when the child fails to exit or leaves pipes open; once elapsed the child is killed and the pipes closed ([pkg.go.dev/os/exec#Cmd](https://pkg.go.dev/os/exec#Cmd)). Set it, otherwise a hung ffmpeg grandchild holding the stderr pipe can block `Wait` indefinitely.
- `Cmd.Env`: a nil `Env` inherits the parent's environment ([pkg.go.dev/os/exec#Cmd](https://pkg.go.dev/os/exec#Cmd)). Supply an explicit slice so worker secrets never reach the child. `HOME` matters because `--no-config` only skips config files; Deno and ffmpeg may still look for a home directory.
- Capture stderr to a buffer. `*exec.ExitError` only populates its own `Stderr` field when `Cmd.Stderr` was nil and the command ran via `Output()` ([pkg.go.dev/os/exec#ExitError](https://pkg.go.dev/os/exec#ExitError)); with an explicit buffer the worker owns the full text for classification and logging.
- Read the status with `errors.As(err, &exitErr)` then `exitErr.ExitCode()`; it returns -1 when the process was killed by a signal, which is what a context timeout looks like ([pkg.go.dev/os#ProcessState.ExitCode](https://pkg.go.dev/os#ProcessState.ExitCode)).
- `--paths temp:<jobdir>` and `--paths home:<jobdir>` keep every intermediate and final file inside the per-Job directory from 8.2.4, so `defer os.RemoveAll(jobDir)` satisfies 8.2.8 regardless of how the process ended. Startup cleanup can sweep any `job-*` directories left by a crashed worker.

### 6.2 Exit codes

Verified from `yt_dlp/__init__.py` and `YoutubeDL.py` at the pinned commit:

| Code | Path | Source |
|---|---|---|
| `0` | `_real_main` returns `ydl.download(...)`, which returns `_download_retcode`, initialised to `0`. A `--match-filters` rejection also exits 0 | [YoutubeDL.py L651](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L651), [L3715](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3715) |
| `1` | `main()` catches `(CookieLoadError, DownloadError, UnsafeExecExpansionError)` and calls `_exit(1)`; `trouble()` sets `_download_retcode = 1` when errors are ignored | [__init__.py L1081-1082](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L1081-L1082), [YoutubeDL.py L1097-1103](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L1097-L1103) |
| `1` | `SameFileError`, `KeyboardInterrupt`, `BrokenPipeError` call `_exit('ERROR: ...')`; `_exit(status=0, *args)` raises `SystemExit(status)` with a string, and Python exits 1 for a non-integer `SystemExit` value | [__init__.py L77-80](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L77-L80), [L1083-1091](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L1083-L1091) |
| `2` | `optparse.OptParseError` (unknown flag, or no URL via `parser.error`) | [__init__.py L1092-1093](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L1092-L1093), [L1060-1062](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L1060-L1062) |
| `100` | Self-update raised an exception (`ydl._download_retcode = 100`); a successful update restarts the process instead | [__init__.py L988-995](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L988-L995) |
| `101` | `DownloadCancelled` escaped `download()` (`--break-match-filters`, `--break-on-existing`, `--max-downloads`) | [__init__.py L1072-1074](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L1072-L1074) |

The exit code alone cannot separate "YouTube changed and yt-dlp is broken" from "the video is private" from "the network is down": all three are `1`. Classification has to come from stderr.

### 6.3 Reading stderr

Every error goes through `report_error`, which prefixes `ERROR:` and calls `trouble()`; `trouble()` writes to stderr and, because the CLI default for `ignoreerrors` is `'only_download'`, raises `DownloadError` for extraction errors so the process ends with code 1 ([YoutubeDL.py L1071-1103](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L1071-L1103); default set at [__init__.py L155](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/__init__.py#L155); "Default is 'only_download' for CLI, but False for API" in the `ignoreerrors` docstring). Warnings use the `WARNING:` prefix.

An `ExtractorError`'s text is `[<ie>] <video_id>: <msg>` + optional ` (caused by <repr>)` + **`bug_reports_message()` only when `expected` is false** ([_utils.py L1001-1008](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L1001-L1008)). `bug_reports_message` yields the literal `please report this issue on  https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U` ([_utils.py L954-964](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L954-L964)).

That suffix is the single most useful signal. Classification order for the worker:

1. **Extractor breakage (Transient failure; retry, and alert).** stderr contains `please report this issue on  https://github.com/yt-dlp/yt-dlp/issues`. Every legitimate condition in section 3 is raised with `expected=True` and never carries this suffix; unexpected parse failures (`Failed to extract any player response`, [_video.py L3189](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3189), `Unable to extract ...`) do. The fix is usually a yt-dlp upgrade, not a retry, so page an operator on the first occurrence.
2. **Network failure (Transient failure; retry).** `ExtractorError.__init__` forces `expected = True` when constructed inside an `except` for one of `network_exceptions` ([_utils.py L986-988](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L986-L988)), and `_request_webpage` builds such errors as `<errnote>: <err>` with `cause=err`, `errnote` defaulting to `Unable to download webpage` ([common.py L907-919](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/common.py#L907-L919)); YouTube's Innertube calls use `errnote='Unable to download API page'` ([_base.py L832](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_base.py#L832)). A network error therefore looks like `ERROR: [youtube] <id>: Unable to download API page: <cause> (caused by ...)` with no bug-report suffix. Match `Unable to download webpage`, `Unable to download API page`, `Unable to download video subtitles`, `HTTP Error 429`, `HTTP Error 5`, `timed out`, `Connection reset`; retry with backoff. yt-dlp already retried `--extractor-retries` times, printing `... Retrying (n/N)...` ([_utils.py L5301](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/utils/_utils.py#L5301)).
3. **Process killed.** `ExitCode() == -1` and `ctx.Err() != nil`: the per-Attempt timeout fired. Transient.
4. **Any other `ERROR:` without the suffix**: an expected unavailability. Map to a Permanent failure reason using section 3.4.
5. **Exit 2** is a worker bug (bad argv); fail permanently and page.

For parsing, take the last line that starts with `ERROR:`; yt-dlp may print several `WARNING:` lines first. With `--ignore-no-formats-error` the interesting line may be a `WARNING:` and exit 0; check `formats` in the JSON first, then the warnings.

There is no structured error output: `__download_wrapper` prints the single JSON document only in the `else:` branch after successful extraction ([YoutubeDL.py L3681-3699](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/YoutubeDL.py#L3681-L3699)); on failure stdout is empty.

### 6.4 Degraded-state warnings worth alerting on

Emitted as `WARNING:` (or to-screen) lines; they do not fail the run but mean the environment or YouTube state is drifting:

| Warning text (prefix) | Meaning | Source |
|---|---|---|
| `No supported JavaScript runtime could be found. Only deno is enabled by default` | Container lacks Deno; "YouTube extraction without a JS runtime has been deprecated, and some formats may be missing" | [_video.py L2994-2999](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L2994-L2999) |
| `Signature solving failed: Some formats may be missing` / `n challenge solving failed: Some formats may be missing` | EJS challenge solver failed; usually stale yt-dlp / yt-dlp-ejs | [_video.py L3345-3357](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3345-L3357) |
| `<id>: <client> client <proto> formats require a GVS PO Token which was not provided` | Formats skipped "as they may yield HTTP Error 403"; warned only for non-default clients | [_video.py L3196-3208](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3196-L3208) |
| `<id>: Some <client> client subtitles require a PO Token which was not provided. They will be discarded` | Caption tracks silently dropped | [_video.py L3210-3215](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3210-L3215) |
| `Some <client> client https formats have been skipped as they are missing a URL` (SABR) | YouTube forcing SABR streaming | [_video.py L3538-3549](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L3538-L3549) |
| `Your yt-dlp version (...) is older than 90 days!` | Startup warning unless `--no-update`; a rebuild reminder | [update.py L193-206](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/update.py#L193-L206) |
| `has been rate-limited by YouTube for up to an hour` | Attached to the `This content isn't available, try again later` reason | [_video.py L4069-4075](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/youtube/_video.py#L4069-L4075) |

---

## 7. Update and rollback in a container

Facts from yt-dlp's README, changelog, `pyproject.toml`, and the releases API:

- **Versioning and cadence.** Tags are CalVer dates (`2026.08.19`, `2026.07.04`, `2026.06.09`, ... in [Changelog.md](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md)). The README describes `stable` as "published on a (mostly) monthly schedule" and warns it "is often "stale" and prone to external breakage (i.e. sites changing things on their end and breaking yt-dlp)"; `nightly` is "the recommended channel for regular users" and is on PyPI as pre-releases (`pip install --pre`) ([README L156-193](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L156-L193)).
- **Install methods.** Release binaries (`yt-dlp` zipimport needs Python; `yt-dlp_linux` / `yt-dlp_musllinux` are standalone glibc 2.17+ / musl 1.2+ x86_64 binaries, with aarch64 variants and `.zip` "no auto-update" variants), pip, or third-party package managers ([README L91-130](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L91-L130)). Each release ships `SHA2-256SUMS`, `SHA2-512SUMS` and `.sig` GPG signatures (README, and confirmed in the 2026.08.19 asset list via the releases API). Neither the README nor the wiki [Installation](https://github.com/yt-dlp/yt-dlp/wiki/Installation) page mentions an official Docker image; none exists as far as yt-dlp's own docs go.
- **Self-update.** `-U` "will only update to its current channel"; `--update-to [CHANNEL@]TAG` upgrades or downgrades, e.g. `yt-dlp --update-to stable@2023.07.06` ([README L171-181](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L171-L181)). It does not apply to pip installs ("simply re-run the same command that was used to install") nor to the `.zip` variants. `--no-update` is the default; the only automatic behaviour is the 90-day-old warning ([README L310](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L310), [update.py L193-206](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/update.py#L193-L206)).
- **Runtime dependencies.** Python 3.10+ (`requires-python = ">=3.10"`, [pyproject.toml L18](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/pyproject.toml#L18)); ffmpeg and ffprobe binaries ([README L207-211](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L207-L211)); and since **2025.11.12**, "An external JavaScript runtime is now required for full YouTube support" ([Changelog L543-544](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/Changelog.md#L543-L544), [issue #15012](https://github.com/yt-dlp/yt-dlp/issues/15012)). Supported runtimes "in order of priority ...: deno, node, quickjs, bun. Only "deno" is enabled by default" ([README L367-380](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L367-L380)); the [EJS wiki](https://github.com/yt-dlp/yt-dlp/wiki/EJS) gives Deno >= 2.3.0, Node >= 22, and notes Bun is deprecated. `yt-dlp-ejs` is pinned `==0.8.0` in the `default` pip extra ([pyproject.toml L48-59](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/pyproject.toml#L48-L59)) and bundled in the official executables (EJS wiki: "No additional action required. yt-dlp-ejs is bundled with these executables"). Deno itself is never bundled; the container must install it. `--remote-components` stays at its default (none allowed) because the bundled EJS version matches.
- **Impersonation.** `curl_cffi` is included in most builds except the zipimport `yt-dlp` and Windows x86 ([README L227-228](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L227-L228)); with pip use the `curl-cffi` extra. Section 2.6 explains why captions need it.

Recommendation (ours, not yt-dlp's):

1. **Pin, never self-update.** At image build either `pip install "yt-dlp[default,curl-cffi]==2026.08.19"` on a Python 3.12 base, or download `https://github.com/yt-dlp/yt-dlp/releases/download/2026.08.19/yt-dlp_linux` and verify it against that release's `SHA2-256SUMS`. Install Deno >= 2.3.0 (the EJS wiki warns to fetch `deno`, not `denort`) and a static ffmpeg in the same layer. Always run with `--no-update`.
2. **Update means rebuild.** Bump the tag in the Dockerfile, rebuild, tag the image with the yt-dlp version (`worker:ytdlp-2026.08.19`). Rollback is redeploying the previous image tag. `--update-to stable@<tag>` is only for an ad-hoc shell inside a running container.
3. **Expect frequent bumps.** Because `stable` is admitted to be "prone to external breakage", treat the section 6.3 alert (bug-report suffix on stderr) as the trigger to rebuild against the newest stable, or nightly when stable is not yet fixed. A scheduled CI job that runs the metadata call against one known-good public video and fails on the bug-report suffix is a cheap canary.
4. **Health check.** `yt-dlp --version` prints the tag; `yt-dlp --no-update -v` lists detected dependencies at the top of the verbose output ("All the currently available dependencies are visible at the top of the `--verbose` output", [README L253](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/README.md#L253)), which confirms Deno, ffmpeg and curl_cffi wiring at container start.

---

## 8. Open questions and unverified items

- Exact wording of YouTube-originated reasons ("Sign in to confirm you're not a bot", "Join this channel ...", "Premieres in ...", "Private video", "Video unavailable") is not in yt-dlp's code; only `sign in`, the geo prefix, and the rate-limit sentence are matched there. Classifiers should key on yt-dlp's appended hints and on JSON fields, not on YouTube's prose.
- Whether `api/timedtext` caption URLs expire: not documented anywhere in yt-dlp.
- Guidance specific to datacenter/server IPs: the [PO Token Guide](https://github.com/yt-dlp/yt-dlp/wiki/PO-Token-Guide) has no such section; the closest is the FAQ entry on 429/402 (solve a captcha in a browser and pass cookies, or change IP). If bot-check rejections become common from the server's IP, the options are a PO Token provider plugin or a residential egress, both outside V1.
- Stable itag numbers for Opus/AAC streams: yt-dlp derives everything from `mimeType`, so select by `acodec`/`ext` rather than itag.
- Hinglish detection (spec 8.2.2, third preference) is entirely outside yt-dlp; it needs a language-ID step over the normalised text.

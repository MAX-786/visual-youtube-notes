# Stream every export from the API, and move diagram layout to the server

Note PDF, note Markdown ZIP, and the Library backup are produced synchronously by the API and
streamed to the client. There is no export Job, no stored artifact, and no `exports` table. The
diagram's layout is computed once, in Go, when a Revision becomes Active, so the export and the
screen draw the same picture from the same coordinates.

## Exports stream from the API; there is no export Job

Ticket #8 assumed exports would be River Jobs, because it also assumed Chromium would live in the
worker. [ADR 0008](0008-deployment-topology.md) moved Chromium into its own `gotenberg` service and
took that premise away with it, but the wiring kept the assumption: `VYN_GOTENBERG_URL` is set on
the worker and the worker declares `depends_on: gotenberg`. This ADR moves both to the `api`
service. The rule ADR 0008 actually established is that Chromium stays out of the **worker image**;
an HTTP call from the API to a separate service does not touch it.

The decisive constraint is that **no two services share a volume**, which ADR 0008 states as a
property of the whole stack. An asynchronous export therefore has nowhere to put its bytes except
PostgreSQL — `bytea`, which materializes the entire value in memory on both write and read, or a
large object with a lifecycle of its own. On top of that it needs an `exports` table, an expiry
sweep, a Purge interaction, a download endpoint, and a new SSE event type, and it opens a window in
which the Note can change between enqueue and render. Every one of those exists to serve a
resumability nobody asked for in a single-owner tool.

Streaming also happens to be forced. Manual autosave batches mint Revisions
([ADR 0004](0004-revision-mutation-and-conflict-model.md)), so a mature library's archive is
plausibly hundreds of megabytes — a few thousand Notes, each with a Transcript and a double-digit
number of Revision payloads. The API is capped at 1 GB (ADR 0008), so the archive could never be
assembled in memory whatever the transport. Once it must stream, the Job buys nothing at all.

Cloudflare's proxy timeout, which ADR 0008 already answers for SSE with a 30 s heartbeat, is a
time-to-first-byte limit rather than a total-duration limit. The Markdown ZIP and the archive emit
their first bytes immediately. The PDF waits on Gotenberg, which returns a whole document in a few
seconds. Neither approaches it.

## The server owns the layout, so there is one of it

Ticket #6 chose a custom SVG renderer with `@dagrejs/dagre` for process flows and `d3-hierarchy` for
mind maps, and noted the unresolved consequence: "export needs server-side layout without a DOM". It
left open which server.

Rendering server-side without also _laying out_ server-side does not work. Positions live in
`note_diagram_layouts`, so an export could honour them when present — but a Note the Owner has never
opened has none, and would export through a second layout implementation that will not agree with
the browser's. The Note would then change appearance the first time it was opened.

So the layout moves to Go and the PWA stops computing one. The generation Job writes positions in
the same transaction that writes the Revision; the PWA becomes a renderer of coordinates it is
given. The rule is stated once and covers every path: **whenever a Revision becomes Active, ensure
every element in its Diagram has a position, computing only the missing ones.** Generation and
Regeneration hit it with a Diagram of entirely new element IDs; restore hits it with old ones;
manual editing never hits it, because V1 cannot add graph nodes or edges and deletion only removes
keys.

Three consequences follow, and each is load-bearing:

- **Positions merge, never replace.** ADR 0004 says stale coordinates "prune themselves" because the
  renderer drops keys that do not resolve to an element in the Diagram being drawn. That is only
  true if a Regeneration _adds_ its keys rather than overwriting the map. Merging is also what makes
  restore-across-Regeneration work with no extra code: the restored Revision reintroduces element
  IDs whose positions were never deleted. The map is bounded by 24 elements per generation and costs
  a few hundred bytes.
- **A position is `{x, y, w, h}`, not `{x, y}`.** Box size comes from measuring the label, and if the
  client measures too there are two implementations of the wrap rule, disagreeing at the margins.
  Storing the box makes the client declarative. `note_diagram_layouts.positions` is constrained only
  by `jsonb_typeof(positions) = 'object'`, so this needs no migration.
- **`layout.engine` is renamed `dagre` to `layered`.** With the algorithm in Go there is no dagre.
  The generator is choosing a shape — radial or layered — and the enum should name the shape rather
  than a JavaScript package the backend does not depend on.

This does not resurrect the problem ADR 0004 solved. Positions stay out of the Revision payload and
stay keyed by Note; all that changes is who writes them first. No `stage_kind` value is added,
because a Stage that takes milliseconds and can never fail on its own would be a user-visible step
reporting nothing.

## The SVG, and the fonts it cannot carry

Only mind maps and process flows become SVG. Ticket #6 renders timelines and comparisons as HTML
lists and tables, and for export that is not a compromise: a comparison matrix exports as a Markdown
table and prints as an HTML table, both of which are more useful than a picture of a table and both
of which are vector in the PDF.

Measurement is easy here for a reason worth recording, because it is easy to assume the opposite.
The note is **always English** — `docs/design/generation/system-prompt.md` requires every part of the
note to be written in English, translating where the Transcript is not, with only the `quote` inside
a Source ref preserved in the original script, and Source refs are internal grounding data that no
export renders. Devanagari therefore appears in an export **only in Transcript body text**, which is
flowing HTML or Markdown and never inside the SVG. Ticket #8 spent most of its length on
complex-script shaping because it assumed otherwise; on the diagram path Go needs advance widths from
`sfnt` and no shaping engine at all.

Fonts are referenced by family with a fallback stack and are **not** embedded. The alternatives were
weighed and each fails on something: embedding full Noto Sans faces costs roughly a quarter-megabyte
per export; subsetting is exact but pulls a font-subsetting dependency into Go for one artifact;
converting labels to outlines is exact and self-contained but breaks spec 7.2's requirement that all
diagram text be selectable and searchable. The accepted cost is that a `diagram.svg` opened on a
machine without Noto Sans substitutes a font and may fit its boxes imperfectly, so boxes are padded
to tolerate metric drift. The PDF is unaffected: Gotenberg's pinned digest carries `fonts-noto-core`,
verified in ADR 0008.

Choosing **Noto Sans** as the note font is what makes the three measurements agree. Go measures Noto
Sans, the PWA self-hosts it, and Gotenberg's Chromium has it, so a box sized on the server is the box
the label lands in.

The SVG stays inside the static subset — no `foreignObject`, no external `use`, no scripting, no
filters — so it renders identically in a browser, in Chromium's print pipeline, and in non-browser
renderers. Export is **light-only**: the print HTML pins an explicit light palette, and the SVG
carries a `<style>` block whose `prefers-color-scheme: dark` rule fires only for on-screen viewers,
with light as the static default every other renderer sees.

## What the PDF is, and what Gotenberg is told

A4 portrait throughout. A landscape page for the diagram was considered, since the note canvas is
optimized for tablet landscape, and rejected: it produces a mixed-orientation document that is
awkward to read and to print, to solve a problem that does not arise. At the 12-visible-element
limit a radial mind map is roughly square and a layered process flow is tall and narrow; scaling the
`viewBox` to the content width fits both.

The page uses `break-inside: avoid` on the diagram block, a `@bottom-center` margin box carrying
`counter(page)` and `counter(pages)`, and `preferCssPageSize` so the CSS `@page` rule rather than the
request decides the geometry.

The `gotenberg` service currently runs with **no flags at all**. Two are added.
`--chromium-disable-javascript`, because the print document is static HTML with an inline SVG and has
no script to run, so the capability is pure attack surface over AI-generated text. And a deny-all
outbound URL list, because every asset travels in the multipart form and the render has no legitimate
reason to reach the network.

A Gotenberg failure returns 502 and leaves the Note untouched. There is no second renderer and
inventing a degraded one would be worse than the error.

## Markdown is a document, not a rendering

`note.md` opens with YAML front matter — title, channel, Video ID, canonical URL, duration,
publication date, Save time, Primary Topic, Tags, detected language, Revision ID and number, and the
export timestamp — so the file lands in a Markdown knowledge tool as a first-class note rather than
as loose prose. The body follows the spec 7.1 hierarchy.

The diagram appears **twice**: as `![Diagram](diagram.svg)` and, immediately below, as its Text
alternative. SVG-in-Markdown renders inconsistently across viewers, and the Text alternative is
deterministic derived data that `docs/design/diagram-semantics.md` already specifies and the product
already owes for accessibility. Emitting both costs nothing and makes the file legible everywhere.

With **Include transcript** on — it defaults off, since spec 12 includes only the note and source
metadata by default — the Transcript becomes a separate `transcript.md` linked from `note.md`, and in
the PDF an appendix forced onto a new page after _My takeaway_. A two-hour Transcript runs to roughly
eighteen thousand words and would bury the note it belongs to.

Exports render the Active Revision by default and accept a `revision` parameter naming any Revision
of that Note, because version history already offers preview and the renderer is parameterized by
Revision either way. A Note with no Active Revision — processing or failed — cannot be exported and
returns 409. A **trashed** Note can be: exporting before the thirty days elapse is exactly when
someone wants it.

## The archive is one streamed document, with checksums that can be checked

The Library backup is a single JSON document, streamed in one pass, described by
[`library-archive.schema.json`](../design/library-archive.schema.json).

Checksums nobody can verify are decoration, and the obstacle is that re-deriving a digest needs
byte-identical JSON. A ZIP of files with a `manifest.json` would sidestep it — digests over files are
unambiguous and `sha256sum` checks them — but the spec asks for a JSON archive and the format's value
is being readable with ordinary tools. So the archive commits to a canonical serialization instead:
each record carries a `sha256` over its **RFC 8785 (JCS)** canonical form with the `checksum` field
itself removed, and the document ends with an integrity block holding per-collection counts and a
digest over the record digests in emission order. That block catches truncation and reordering, not
merely bit rot, and every part of it is computable in the single forward pass that streaming allows.

JCS's one real hazard is number formatting, so the archive **admits no floating-point scalars**.
Layout coordinates are the only floats in the domain and the archive does not carry layout, which the
spec's contents list does not name and which is presentation state rather than content.

Emission order — `archive_version`, then topics, tags, notes, then integrity — is the only order a
single pass permits, since the integrity block digests everything before it.

What travels: every Note including trashed ones with their `trashed_at`, its Source metadata,
Transcript, Takeaway, **every** Revision of every origin with its payload stored **verbatim**
including Source refs, and a per-Note AI cost summary, which spec 7.4 lists as Note metadata. Storing
payloads verbatim rather than re-shaping them is the point: a reshaped payload is a second schema to
keep in step with the first, and the archive's whole claim is fidelity.

What does not: derived data that regenerates from the above (search children, embeddings), transport
and operational records (`sync_events`, Stages, Attempts), and every table holding a credential —
users, Device sessions, tokens, Pairing requests, Security events, and AI role configurations with
their sealed provider keys.

[`verify-archive.mjs`](../design/verify-archive.mjs) ships beside the schema as the executable
reference that recomputes the digests and checks the integrity block, following the precedent
`diagram-semantics.mjs` set. An archive whose checksums have no checker is an archive whose checksums
are untested.

## The API surface

- `GET /api/exports/notes/{noteId}.pdf`
- `GET /api/exports/notes/{noteId}.zip`
- `GET /api/exports/library`

Both note endpoints accept `?transcript=1` and `?revision=<uuid>`. The extension lives in the path
rather than in an `Accept` header or a `format` parameter so that a plain `<a download>` works and the
browser names the saved file correctly. All three are `GET`, authenticated by the session cookie, and
carry `Content-Disposition: attachment`.

Exports make no provider calls, so they touch neither the Soft cap nor `ai_usage_events`
([ADR 0007](0007-ai-spend-ledger-and-soft-cap.md)).

## What this ADR corrects

- **Spec 12** says the Markdown export is "a ZIP containing UTF-8 Markdown plus local SVG assets".
  Only mind maps and process flows produce an SVG; a timeline is a numbered list and a comparison is
  a Markdown table.
- **`docs/design/diagram.schema.json`** and `validate-note.mjs`: the `layout.engine` value `dagre`
  becomes `layered`.
- **ADR 0004**: positions are `{x, y, w, h}`, are seeded by the server rather than by the first client
  to open a Note, and merge rather than replace.
- **ADR 0008** and `deploy/compose.yaml`: `VYN_GOTENBERG_URL` and the `gotenberg` dependency move from
  the worker to the API.

## What this ADR does not decide

- The print stylesheet and the SVG's visual design — spacing, node shapes, the notebook character
  spec 7.2 asks for. This ADR fixes the page geometry, the colour policy and the static-SVG subset;
  what it looks like inside those is a design task, not an architectural one.
- The exact layered and radial algorithms. They are bounded by 24 elements and their output is
  whatever both renderers read out of `note_diagram_layouts`, so the choice is reversible without
  touching anything else in this ADR.
- Archive import. Spec 12 excludes it from V1 and database restore remains the disaster-recovery
  path; the schema is versioned so a later importer has something to target.

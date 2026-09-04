# Visual YouTube Notes

A personal, single-user tool that turns deliberately saved public YouTube videos into synchronized, searchable visual learning notes. It is a memory and review tool after watching, not a watch-history mirror.

## Language

### Capture

**Submission**:
One intentional request to turn a YouTube video into a Note, made from the extension, the Android share sheet, or the manual URL field. Deduplicated by Video ID.
_Avoid_: Save request, import, capture

**Video ID**:
The canonical YouTube identifier extracted from any supported URL form. One Note per user per Video ID.
_Avoid_: URL key, slug

**Undo window**:
The five seconds after a Submission during which it can be removed before any external work begins.
_Avoid_: Grace period, delay

**Duplicate**:
A Submission whose Video ID already has a Note for this user. It opens the existing Note and never creates a second one.
_Avoid_: Re-save, resubmit

### Source material

**Source**:
The YouTube video a Note derives from, plus its metadata (title, channel, duration, publication date).
_Avoid_: Video (when referring to the record rather than the media)

**Caption track**:
A subtitle track fetched from YouTube; human or automatic, in a supported language.
_Avoid_: Subtitles, CC

**Transcript**:
The retained, normalized text of the Source in its original language and script, produced from a usable Caption track or from STT. Private, read-only in V1, searchable.
_Avoid_: Captions (once normalized), text, script

**Transcript chunk**:
A slice of the Transcript sized for embedding and used as the unit of grounding and search matching.
_Avoid_: Segment, passage

**STT**:
External speech-to-text run on temporary audio when no usable Caption track exists.
_Avoid_: Transcription service, ASR

### The note

**Note**:
The learning artifact for one Source, created by a Submission. A ready Note has an Active Revision and is organized by Save time under one Primary Topic; a processing or failed Note may not have either yet.
_Avoid_: Card (that is the list rendering), document, page

**Revision**:
An immutable snapshot of a Note's generated and edited content. Origin is `generated`, `manual`, `conflict`, or `restore`; each records its parent Revision.
_Avoid_: Version, snapshot, draft

**Active Revision**:
The single Revision a ready Note currently points at and displays.
_Avoid_: Current version, head

**Regeneration**:
Producing a new `generated` Revision from the Transcript and an optional instruction, never from edited text. Activates only if the Note still points at the Revision it started from.
_Avoid_: Re-run, refresh, rebuild

**Conflict Revision**:
A Revision created when an edit arrives against a stale base; kept alongside the server's Active Revision for compare and restore.
_Avoid_: Merge conflict, fork

**Takeaway**:
The user-owned "My takeaway" text stored outside every Revision and never touched by AI.
_Avoid_: Personal note, reflection, comment

**Content block**:
One AI- or user-authored text unit in a Revision (summary, key point, important detail, practical action) with a stable ID, Provenance, and Source refs when AI-authored.
_Avoid_: Bullet, item, paragraph

**Provenance**:
Whether a Content block or Diagram element is `ai` or `user_edited`. Editing AI text flips it and clears its Source refs.
_Avoid_: Authorship, origin (reserved for Revisions)

**Source ref**:
An internal pointer from AI-authored content to a Transcript chunk and character span. Grounding data, not a timestamp link.
_Avoid_: Citation, timestamp, anchor

**Speaker claim**:
A Content block marked as the speaker's opinion or unsupported assertion, rendered distinctly.
_Avoid_: Opinion flag, disclaimer

**Practical actions**:
The optional "What I can use" list, present only when the Transcript contains explicit recommendations, steps, tasks, or decision rules.
_Avoid_: Action items, todos, tips

### Diagram

**Diagram**:
The one structured, JSON-canonical visual per Revision, of exactly one Diagram type. Never a bitmap.
_Avoid_: Visual, graphic, image, canvas

**Diagram type**:
One of mind map, process flow, timeline, or comparison matrix. Chosen by the generator; not user-selectable in V1.
_Avoid_: Layout, view mode

**Diagram element**:
A node, edge, event, column, row, or cell inside a Diagram, with a stable ID and Provenance.
_Avoid_: Shape, item, widget

**Collapsed group**:
A branch of overflow Diagram elements hidden behind the twelve initially visible ones.
_Avoid_: Folder, cluster

**Text alternative**:
The structured, position-free rendering of a Diagram's elements and relationships for accessibility and search.
_Avoid_: Alt text, outline view

### Organization and search

**Primary Topic**:
The single required, user-owned, normalized topic each ready, non-Trash Note files under.
_Avoid_: Category, folder, subject

**Tag**:
An optional cross-cutting label on a Note; many per Note, freely added or removed.
_Avoid_: Keyword, label (reserved for tracker labels)

**Save time**:
When the Submission was made, in the account timezone. Drives the daily timeline; publication date is metadata only.
_Avoid_: Created at, added date

**Today**:
The date-first destination showing Notes and processing cards grouped by Save time.
_Avoid_: Feed, home, inbox

**Hybrid search**:
Combined lexical (PostgreSQL full-text, `simple` config) and semantic (`pgvector` cosine) ranking over indexed Note sections and Transcript chunks, fused per child then aggregated per Note.
_Avoid_: Semantic search (alone), AI search

**Child candidate**:
One indexed Note section or Transcript chunk scored independently before Notes are aggregated.
_Avoid_: Hit, match, document

**Excerpt**:
A short highlighted Transcript span shown on a Note card when only the Transcript matched.
_Avoid_: Snippet, preview

### Processing

**Job**:
The River-backed unit of work that carries a Submission from queued to ready, one per Note and processing version.
_Avoid_: Task (reserved for tracker tickets), pipeline run

**Stage**:
One user-visible step of a Job: queued, metadata, captions, transcribing, generating, indexing, ready. Any Stage may become failed.
_Avoid_: Step, phase, state

**Attempt**:
One try at a Stage, persisted before every external call. At most three automatic Attempts per Stage.
_Avoid_: Retry (that is the act of creating another Attempt)

**Transient failure**:
A network, timeout, provider 429/5xx, or extractor failure that earns an automatic retry.
_Avoid_: Soft error

**Permanent failure**:
A validation or provider 4xx (other than 408/409/429) failure that waits for the user to Retry or Delete.
_Avoid_: Hard error, fatal

**Processing role**:
One of the four independently configured AI slots: transcription, note generation, embeddings, visual analysis.
_Avoid_: Model slot, provider

**Soft cap**:
The configurable monthly AI spend threshold that, when reached, demands explicit confirmation per Job and pauses semantic query embeddings.
_Avoid_: Budget, limit, quota

### Account and devices

**Owner**:
The single seeded account. No public signup.
_Avoid_: Admin, user (in prose)

**Device session**:
One trusted PWA or extension device and the revocation boundary for its Token family. A new authentication creates a new Device session rather than reviving an old one.
_Avoid_: Login, client

**Token family**:
The one live access token and single-use refresh-token lineage owned by a Device session. Reuse of a consumed refresh token revokes the whole family.
_Avoid_: Session tokens, credentials

**Pairing**:
The challenge/verifier flow by which the extension obtains its own Token family without ever seeing the password.
_Avoid_: Extension login, linking

**Pairing request**:
A five-minute Owner approval record binding one extension identity to a verifier challenge. Denial and successful exchange are terminal.
_Avoid_: Pairing code, login request

**Security event**:
A retained, credential-free record of rejected or administrative authentication activity.
_Avoid_: Audit log, request log

### Lifecycle

**Trash**:
Where a deleted Note waits thirty days before Purge; restorable to its original Save time and Primary Topic.
_Avoid_: Recycle bin, archive, soft delete

**Purge**:
The automatic removal of a Note and all dependent data (Revisions, Transcript, embeddings, Job data) after its Trash period.
_Avoid_: Hard delete, permanent delete

**Sync cursor**:
The monotonically increasing marker carried by every create/update/delete event, used by SSE and by polling resync.
_Avoid_: Version number, sequence, etag

**Library backup**:
The versioned JSON archive of all Notes, Transcripts, metadata, topics, Revisions, and Takeaways, excluding secrets.
_Avoid_: Export (reserved for single-Note PDF/Markdown), dump

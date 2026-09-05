# Apply edits as typed operations against an exact base Revision

Every edit to generated content travels as a typed operation set addressed to stable element IDs, carrying a client mutation ID and the exact `base_revision_id` it was composed against. The server applies the operations onto that base and writes the result as one immutable Revision. When the base is no longer the Active Revision the server never rebases and never merges: it writes a Conflict Revision on the stale branch, leaves the Active Revision untouched, and marks the new Revision as awaiting a decision. A snapshot wire format was the alternative; operations were chosen because V1's edit surface is a small closed set, which lets the two invariants that matter — no structural additions to a Diagram, and Provenance flipping to `user_edited` with Source refs cleared — be enforced by construction on the server rather than trusted from the client and re-derived by diffing.

## Operations

The permitted operations are exactly the edit surface of spec sections 5.4 and 7.2, and nothing else is representable:

- **Content blocks** — `set_block_text`, `insert_block`, `delete_block`, `reorder_blocks` over `key_points`, `important_details`, and `practical_actions`; `set_block_text` alone for `summary`.
- **Diagram** — `set_element_label` (node, edge label, timeline event, comparison column or row), `set_cell_text`, `delete_element`, `reorder_elements`.

Additions exist for Content block lists, which spec section 5.4 grants explicitly, and nowhere in the Diagram. A Diagram is a picture _of the Transcript_, so an added element would be born `user_edited` with no Source refs and no way to acquire any. Deleting a graph node deletes its incident edges; deleting a comparison column takes its cells.

`insert_block` carries a client-minted element ID matching `^[A-Za-z][A-Za-z0-9_-]{0,63}$`, so an insert is idempotent under replay without a server round-trip for identity.

Any operation that changes authored text sets that element's Provenance to `user_edited` and clears its Source refs. The server does this; the client never asserts Provenance. Reordering and deleting do not change the Provenance of the elements they touch.

## Two classes of validation rule

`validate-note.mjs` currently enforces two kinds of rule under one roof, and the split is load-bearing now that the server validates every edit:

- **Structural invariants bind always.** Unique element IDs, edge endpoints resolving, a mind map remaining a single tree rooted at `root`, contiguous `order`, the 12-visible and 24-total caps, and the 4x6 comparison cap. A breach rejects the whole operation batch atomically.
- **Generation ranges bind the generator only.** The 5-7 key points, 3-8 important details, 0-5 practical actions, and 40-word summary exist to shape model output. Applied to editing they would forbid deleting two of seven redundant key points. Editing floors are only what keeps a Note coherent: a non-empty summary and at least one key point, with the caps still holding on the way up.

Because operations apply to an exact base, an operation can never address a missing element. That failure class does not exist; only the applied result can fail validation.

## Refusals, staleness, and conflict

Four refusals precede the staleness check, each returning a typed reason so the client can roll back its optimistic state and say something true: the Note is in Trash, the Note is not `ready`, the target Revision is not the Active Revision (a Revision awaiting a decision is read-only — it is accepted or discarded, not edited), or the Note no longer exists because it was Purged while the device was offline.

If the base is the Active Revision, the result is a `manual` Revision and becomes active. If the base is stale, the result is a `conflict` Revision whose parent is the stale base, the Active Revision does not move, and `awaiting_decision_since` is set.

One Revision is written per autosave flush. Spec section 9 says a manual autosave batch creates an immutable Revision, and this takes that literally rather than introducing a sealing window or a mutable draft, both of which would make "immutable" conditional or would leave in-flight edits invisible to the other device. The client batches instead: a two-second debounce plus a flush on blur and on navigation. At V1 volume the retained payloads are affordable, and version history groups consecutive `manual` Revisions from one Device session into a single expandable entry.

## Offline replay

The offline queue drains strictly serially per Note. A client applies operations locally but mints no Revision IDs, so every queued entry would otherwise carry the same stale base and conflict with the client's own earlier work. Instead each response returns the new Revision ID and the client writes it into the next entry's base before sending. The server rule stays literal, with no session-lineage reasoning in the server.

When the head of a chain conflicts, the remaining entries rebase onto the **Conflict Revision**, not onto the server's Active Revision. An offline editing session therefore lands as one conflict branch carrying all of its edits, rather than as one Conflict Revision per flush for the Owner to reconcile individually.

Replay is idempotent through `note_mutations`, keyed by client mutation ID, which records the outcome and returns it verbatim to a client that lost the acknowledgement. Rows are retained 30 days to match the offline cache lifetime; a mutation ID older than the floor is rejected as expired rather than applied, because silently applying a months-old edit is the silent loss that spec section 16 forbids, inverted.

## Activation, restore, and Regeneration

Activation and restore stay distinct, on the line between a leaf and the past:

- **Activation moves the pointer** when a Revision has never been active — a generated Revision whose compare-and-set lost, offered under spec section 5.5. Its parent already records what it branched from, so no copy is warranted.
- **Restore copies** into a new `restore` Revision whose parent is the current Active Revision, for anything already in the past. Moving the pointer backwards would erase the fact that the Owner deliberately reached back, and would make the Active Revision's parent chain lie about what preceded it. A Conflict Revision is restored this way too: by the time it is reviewed the server's line has moved on and it is no longer a leaf.

Revision history is therefore a tree, not a line. It already had to be: a Conflict Revision branches off a stale base by definition.

Regeneration records the Active Revision as its base and activates its result only if the Note still points there. Only one Regeneration may be in flight per Note, enforced by River's unique-job tag on `(note_id, regenerate)`. Both a lost compare-and-set and a Conflict Revision produce a Revision awaiting a decision, and both can be outstanding at once — a conflict arriving over sync cannot be refused to keep a single slot free — so the state lives as `awaiting_decision_since` on the Revision rather than as a pointer on the Note.

## Layout is not content

Diagram positions move out of the Revision payload into `note_diagram_layouts`, mutable and last-write-wins, keyed by Note. Left inside the payload, the first automatic layout would manufacture a `manual` Revision on merely opening a Note, and a position nudge on a second device would become a Conflict Revision over pixel coordinates. `layout.engine` stays in the payload, because radial versus Dagre is semantic.

Keying by Note rather than by Revision avoids a copy-forward on every autosave: manual editing preserves element IDs so positions survive, and Regeneration mints fresh IDs so stale coordinates prune themselves when the renderer drops keys that do not resolve to an element in the Diagram being drawn. This supersedes the Layout paragraph in `docs/design/diagram-semantics.md`.

## Edits outside Revisions

The Takeaway, Primary Topic, and Tags share the mutation envelope — mutation ID, offline queue, Sync cursor — but have no base Revision and never produce a conflict branch. Topic and Tag writes are set membership and are last-write-wins. The Takeaway is the only user-authored prose a blind overwrite could destroy, so it carries a compare-and-set token; a stale write is rejected and both texts are surfaced for the Owner to reconcile.

## Sync cursor

Every Note-affecting write allocates a Sync cursor from a per-Owner counter row, locked `FOR UPDATE` and held to commit, so cursors become visible in allocation order. A bare `bigserial` is unsafe here: two transactions commit in an order unrelated to the order they drew their values, so a client that saw the higher value and advanced its watermark would never see the lower one. For a single Owner writing a few times a minute, serializing costs nothing.

Changes are appended to a `sync_events` outbox, retained 30 days, because Purge hard-deletes a Note and a device offline during a Purge cannot learn of it from the `notes` table. A client whose cursor predates the retention floor performs a full resync. Each event records its originating Device session so a client can skip echoes of its own writes; filtering on mutation ID instead would only work inside the idempotency window and would miss changes a device causes indirectly, such as a Job reaching `ready`. The Takeaway's compare-and-set token is a cursor value rather than a timestamp, keeping one monotonic token in the system.

This ADR fixes the cursor's origin, monotonicity guarantee, and storage. What the SSE stream carries and how the client resynchronizes belong to the PWA shell design.

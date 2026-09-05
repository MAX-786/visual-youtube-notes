# Mutation protocol, conflict, and sync

Design draft for [#16](https://github.com/MAX-786/visual-youtube-notes/issues/16). The decisions
and their rationale live in [ADR 0004](../adr/0004-revision-mutation-and-conflict-model.md);
this file is the mechanical detail — wire shapes, the server transaction, and the sequences.

Schema: [`000003_mutations_and_sync.up.sql`](../../deploy/migrations/000003_mutations_and_sync.up.sql).

## The envelope

```jsonc
{
  "mutation_id": "0f2b…", // client-minted UUID; the idempotency key
  "note_id": "8a41…",
  "base_revision_id": "c7d3…", // the exact Revision the ops were composed against
  "ops": [/* one or more operations, applied in array order */],
}
```

`base_revision_id` is omitted only by the non-Revision mutations (Takeaway, Primary Topic,
Tags), which carry their own tokens instead.

## Operation catalogue

Every operation addresses a stable element ID. The list is closed: anything absent is not
representable, which is how spec section 7.2's prohibitions are enforced.

| Operation           | Target                                                     | Payload                                   |
| ------------------- | ---------------------------------------------------------- | ----------------------------------------- |
| `set_block_text`    | `summary`, or a block in a list                            | `{ section, id?, text }`                  |
| `insert_block`      | `key_points`, `important_details`, `practical_actions`     | `{ section, id, text, after_id \| null }` |
| `delete_block`      | the same three lists                                       | `{ section, id }`                         |
| `reorder_blocks`    | the same three lists                                       | `{ section, ids[] }` — a full permutation |
| `set_element_label` | node, edge label, timeline event, comparison column or row | `{ element_id, label }`                   |
| `set_cell_text`     | comparison cell                                            | `{ row_id, column_id, text }`             |
| `delete_element`    | node, timeline event, comparison column or row             | `{ element_id }`                          |
| `reorder_elements`  | timeline events, comparison rows, comparison columns       | `{ collection, ids[] }`                   |

Notes on the edges of the catalogue:

- **No additions to a Diagram.** Content block lists get `insert_block` because spec 5.4 grants
  it explicitly; a Diagram gets nothing equivalent, because an added element would be born
  `user_edited` with no Source refs and no way to acquire any.
- **Cascades.** `delete_element` on a graph node removes its incident edges; on a comparison
  column it removes that column's cell from every row. The client confirms with the user first
  (spec 7.2) but the server performs the cascade — it is not expressed as extra operations.
- **`insert_block` mints its own ID**, matching `^[A-Za-z][A-Za-z0-9_-]{0,63}$`, so replay after a
  lost acknowledgement cannot duplicate the block.
- **Provenance is the server's.** Any operation that changes authored text sets that element's
  `provenance` to `user_edited` and clears its `source_refs`. Reorders and deletes leave the
  Provenance of surviving elements alone.

## Server transaction

One transaction per mutation, in this order. The first failure wins and nothing is written
except the ledger row.

1. **Idempotency.** Look up `mutation_id` in `note_mutations`. A hit returns the recorded
   outcome verbatim. A miss where `applied_at` would be older than the 30-day floor is
   rejected as `expired`.
2. **Refusals** (typed reason returned; client rolls its optimistic state back):
   - `note_trashed` — edits wait for restore.
   - `note_not_ready` — `processing_state <> 'ready'`; a re-processing Note has no stable content.
   - `revision_not_active` — the base is a Revision awaiting a decision. Those are read-only:
     accept or discard, do not edit a branch.
   - `note_purged` — the Note is gone. The client drops the queue entry; the `sync_events`
     outbox is how it discovers this.
3. **Lock** the Note row, then read the base Revision payload.
4. **Apply** operations in array order onto that payload.
5. **Validate** the result. Structural invariants bind; generation ranges do not
   (see ADR 0004). A breach rejects the whole batch — `validation_failed` with the offending rule.
6. **Classify.** `base_revision_id = notes.active_revision_id` gives origin `manual` and the new
   Revision becomes active. Otherwise origin `conflict`, `awaiting_decision_since = now()`, and
   `notes.active_revision_id` is left alone.
7. **Allocate a Sync cursor** via `allocate_sync_cursor(user_id)`, append the `sync_events` row
   stamped with the originating Device session, write the `note_mutations` row, and commit.

The response always carries `{ outcome, revision_id, active_revision_id, sync_cursor }`. On a
conflict, `revision_id` is the Conflict Revision and `active_revision_id` is what the client
must roll back to — and it is also the base for the _next_ entry in an offline chain.

## Sequences

### Two devices, one Note

The scenario spec section 16 demands proof of: an edit conflict on two devices creating a
recoverable Conflict Revision without data loss.

```mermaid
sequenceDiagram
    autonumber
    participant D as Desktop PWA
    participant S as API
    participant T as Tablet PWA

    Note over D,T: both hold Active Revision R3

    D->>S: mutate {m1, base: R3, ops: edit summary}
    activate S
    S->>S: base = active → manual R4, active := R4
    S-->>D: applied, revision R4, cursor 51
    deactivate S
    S--)T: sync_events cursor 51 (note updated)

    Note over T: tablet is mid-edit and has not yet applied 51

    T->>S: mutate {m2, base: R3, ops: edit key point 2}
    activate S
    S->>S: base R3 ≠ active R4 → conflict R5 (parent R3)
    S->>S: awaiting_decision_since := now; active stays R4
    S-->>T: conflicted, revision R5, active R4, cursor 52
    deactivate S

    T->>T: roll local state back to R4
    T->>S: GET note (R4 + pending R5)
    S-->>T: R4 active, R5 awaiting decision
    T->>T: show compare banner: "1 version awaits your decision"

    Note over T: Owner compares R4 and R5

    alt keep the server version
        T->>S: discard R5
        S-->>T: awaiting_decision_since := null (R5 kept in history)
    else take the tablet's edit
        T->>S: restore R5
        S->>S: copy R5 payload → restore R6, parent R4, active := R6
        S-->>T: active R6
        S--)D: sync_events (note updated)
    end
```

R5 is never lost in either branch: discarding only clears the decision flag, and the Revision
stays in version history until Purge.

### An offline chain whose head conflicts

Five flushes queued on a tablet while offline, and the desktop edited the same Note meanwhile.
The point of the sequence is that the Owner gets **one** conflict branch, not five.

```mermaid
sequenceDiagram
    autonumber
    participant Q as IndexedDB queue
    participant T as Tablet PWA
    participant S as API

    Note over T: offline. Local base is R3; ops applied locally only.
    T->>Q: enqueue m1..m5, all stamped base R3

    Note over S: meanwhile the desktop makes R4 active

    Note over T: back online — drain strictly serially

    Q->>S: m1 {base: R3}
    S->>S: R3 ≠ active R4 → conflict R5 (parent R3)
    S-->>Q: conflicted, revision R5

    Note over Q: rebase the rest onto R5, not onto R4

    Q->>S: m2 {base: R5}
    S->>S: R5 ≠ active R4 → conflict R6 (parent R5)
    S-->>Q: conflicted, revision R6

    Q->>S: m3 {base: R6} … m5 {base: R8}
    S-->>Q: conflicted, revision R9

    S->>S: clear awaiting_decision on R5..R8; only R9 awaits a decision
    S-->>T: active R4, pending R9

    T->>T: one banner: compare R4 with R9
```

Each link in the chain lands on the branch, but only its tip is offered as a decision — the
interior Revisions are history, not questions.

### Regeneration losing its compare-and-set

```mermaid
sequenceDiagram
    autonumber
    participant U as PWA
    participant S as API
    participant W as Worker (River)

    U->>S: regenerate (instruction: "simplify this")
    S->>W: insert job, unique tag (note_id, regenerate), base R4
    S-->>U: accepted

    U->>S: mutate {m9, base: R4, ops: edit summary}
    S->>S: manual R5, active := R5

    W->>W: generate from Transcript + instruction
    W->>S: complete, base was R4
    S->>S: R4 ≠ active R5 → generated R6 stays inactive
    S->>S: awaiting_decision_since := now on R6
    S--)U: sync_events (note updated)

    U->>U: banner: "a regenerated version is ready"

    alt activate
        U->>S: activate R6
        S->>S: active := R6 (pointer moves; no copy — R6 never was active)
        S->>S: awaiting_decision_since := null
    else discard
        U->>S: discard R6
        S->>S: awaiting_decision_since := null (R6 kept in history)
    end
```

Activation moves the pointer because R6 is a leaf that was never active; its parent R4 already
records where it came from. Restoring anything already in the past copies instead — that is what
the `restore` origin is for.

## Non-Revision mutations

| Entity            | Token            | On stale token                                          |
| ----------------- | ---------------- | ------------------------------------------------------- |
| Primary Topic     | none             | last write wins                                         |
| Tags              | none             | last write wins (set membership is idempotent)          |
| Takeaway          | `updated_cursor` | rejected; both texts surfaced to reconcile              |
| Diagram positions | none             | last write wins; never conflicts, never a history entry |

Each still allocates a Sync cursor and appends a `sync_events` row, so another device learns of
the change on the same channel as everything else.

## Client queue

One IndexedDB store, `mutation_queue`, ordered by insertion, drained serially per Note:

- An entry holds `{mutation_id, note_id, base_revision_id, ops, created_at}`.
- On a successful response the entry is deleted and the next entry's `base_revision_id` is set
  to the returned `revision_id` — whether that was a `manual` Revision or a Conflict Revision.
- On `note_purged` the whole queue for that Note is dropped.
- On `validation_failed` the entry is dropped and the local state resets to the server's Active
  Revision; the operations cannot become valid by waiting.
- Sign-out clears the queue with the rest of the cache, per spec section 9.

Positions are queued separately and coalesce to the latest value per Note, since they are
last-write-wins and carry no base.

## Follow-on work this leaves open

- The SSE event vocabulary and the client's resync loop — the PWA shell ticket. This design
  fixes only where cursor values come from and what the outbox stores.
- Splitting `validate-note.mjs` into the structural rule set and the generation rule set, and
  porting the structural half to Go, where it becomes the edit validator.
- Rate limits on mutation submission, still unspecified on the map.

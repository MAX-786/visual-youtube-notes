BEGIN;

-- Ticket #16: the mutation protocol, Sync cursor, and layout hoisting.

CREATE TYPE mutation_outcome AS ENUM (
    'applied',
    'conflicted',
    'rejected'
);

CREATE TYPE sync_entity_kind AS ENUM (
    'note',
    'takeaway',
    'topic',
    'tag',
    'diagram_layout'
);

CREATE TYPE sync_change AS ENUM (
    'created',
    'updated',
    'deleted'
);

-- ---------------------------------------------------------------------------
-- Sync cursor
-- ---------------------------------------------------------------------------

-- One counter row per Owner. A writer locks this row FOR UPDATE and holds the
-- lock until it commits, so cursors become visible in allocation order: a
-- reader can never observe cursor N+1 before N. A plain bigserial cannot
-- promise this, because two concurrent transactions commit in an order
-- unrelated to the order in which they drew their values, and the loser is
-- skipped by any client that has already advanced past it.
CREATE TABLE sync_cursors (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    next_cursor bigint NOT NULL DEFAULT 1,
    CONSTRAINT sync_cursors_next_positive CHECK (next_cursor > 0)
);

COMMENT ON TABLE sync_cursors IS
    'Per-Owner monotonic Sync cursor allocator. The writing transaction holds the row lock until it commits.';

CREATE FUNCTION allocate_sync_cursor(p_user_id uuid) RETURNS bigint
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_cursor bigint;
BEGIN
    INSERT INTO sync_cursors (user_id) VALUES (p_user_id)
    ON CONFLICT (user_id) DO NOTHING;

    UPDATE sync_cursors
       SET next_cursor = next_cursor + 1
     WHERE user_id = p_user_id
    RETURNING next_cursor - 1 INTO v_cursor;

    RETURN v_cursor;
END;
$fn$;

COMMENT ON FUNCTION allocate_sync_cursor(uuid) IS
    'Allocates the next Sync cursor. The row lock it takes is held until the calling transaction commits.';

-- The outbox. Needed because Purge hard-deletes a Note: a device that was
-- offline during a Purge cannot discover the removal from the notes table.
-- Retained 30 days, matching the offline cache lifetime in spec section 9; a
-- client whose cursor predates the floor performs a full resync instead.
CREATE TABLE sync_events (
    cursor bigint NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entity_kind sync_entity_kind NOT NULL,
    entity_id uuid NOT NULL,
    note_id uuid,
    change sync_change NOT NULL,
    origin_device_session_id uuid REFERENCES device_sessions(id) ON DELETE SET NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, cursor),
    CONSTRAINT sync_events_cursor_positive CHECK (cursor > 0)
);

COMMENT ON COLUMN sync_events.origin_device_session_id IS
    'Device session that caused the change, so a client can skip echoes of its own writes. Null for server-initiated changes such as Purge or a Job reaching ready.';
COMMENT ON COLUMN sync_events.note_id IS
    'Parent Note for takeaway, tag, and diagram_layout events, so a client can invalidate one Note without a second lookup.';

CREATE INDEX sync_events_user_occurred_idx ON sync_events (user_id, occurred_at);

-- ---------------------------------------------------------------------------
-- Mutation idempotency
-- ---------------------------------------------------------------------------

-- One row per client mutation ID. A replay returns the recorded outcome rather
-- than applying the operations a second time. Rows are pruned after 30 days; a
-- mutation whose ID falls past the floor is rejected as expired, because
-- silently applying a months-old edit is data loss wearing a different hat.
CREATE TABLE note_mutations (
    mutation_id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    note_id uuid NOT NULL,
    device_session_id uuid REFERENCES device_sessions(id) ON DELETE SET NULL,
    base_revision_id uuid,
    result_revision_id uuid,
    outcome mutation_outcome NOT NULL,
    rejection_reason text,
    sync_cursor bigint,
    applied_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT note_mutations_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE,
    CONSTRAINT note_mutations_result_same_note_fk
        FOREIGN KEY (result_revision_id, note_id, user_id)
        REFERENCES note_revisions(id, note_id, user_id) ON DELETE CASCADE,
    CONSTRAINT note_mutations_outcome_shape CHECK (
        (outcome IN ('applied', 'conflicted') AND result_revision_id IS NOT NULL AND rejection_reason IS NULL)
        OR (outcome = 'rejected' AND result_revision_id IS NULL AND rejection_reason IS NOT NULL)
    )
);

COMMENT ON TABLE note_mutations IS
    'Idempotency ledger keyed by client mutation ID. Retained 30 days to match the offline cache lifetime.';
COMMENT ON COLUMN note_mutations.result_revision_id IS
    'For outcome conflicted this is the Conflict Revision, not the Active Revision.';

CREATE INDEX note_mutations_applied_idx ON note_mutations (applied_at);

-- ---------------------------------------------------------------------------
-- Pending decisions
-- ---------------------------------------------------------------------------

-- A Revision that sits off the live line and awaits a decision: a Conflict
-- Revision, or a generated Revision whose compare-and-set lost. Held on the
-- Revision rather than as a pointer on notes, because both kinds can be
-- outstanding at once and a Conflict Revision arriving over sync cannot be
-- refused in order to keep a single slot free.
ALTER TABLE note_revisions
    ADD COLUMN awaiting_decision_since timestamptz;

COMMENT ON COLUMN note_revisions.awaiting_decision_since IS
    'Set when a Revision is created off the active line; cleared when it is activated, restored, or discarded.';

CREATE INDEX note_revisions_awaiting_decision_idx
    ON note_revisions (note_id)
    WHERE awaiting_decision_since IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Diagram layout, hoisted out of the Revision payload
-- ---------------------------------------------------------------------------

-- Positions are presentation state, not content. Left inside the immutable
-- payload they would manufacture a manual Revision on first automatic layout,
-- that is, on merely opening a Note, and would turn a position nudge on a
-- second device into a Conflict Revision over pixel coordinates.
--
-- Keyed by Note, not by Revision: manual editing preserves element IDs so
-- positions survive every autosave Revision without a copy-forward, while
-- Regeneration mints fresh IDs so stale coordinates prune themselves when the
-- renderer drops keys that do not resolve to an element in the Diagram.
CREATE TABLE note_diagram_layouts (
    note_id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    positions jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT note_diagram_layouts_positions_object CHECK (jsonb_typeof(positions) = 'object'),
    CONSTRAINT note_diagram_layouts_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE
);

COMMENT ON TABLE note_diagram_layouts IS
    'Mutable, last-write-wins Diagram positions. Excluded from version history and never a source of conflict.';

CREATE TRIGGER note_diagram_layouts_set_updated_at
BEFORE UPDATE ON note_diagram_layouts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Takeaway compare-and-set
-- ---------------------------------------------------------------------------

-- The Takeaway is the only user-authored prose a blind overwrite could destroy,
-- so it carries an optimistic token. The token is a Sync cursor value rather
-- than updated_at: one monotonic token in the system, and no clock that can run
-- backwards.
ALTER TABLE note_takeaways
    ADD COLUMN updated_cursor bigint NOT NULL DEFAULT 0;

COMMENT ON COLUMN note_takeaways.updated_cursor IS
    'Compare-and-set token. A write carrying a stale value is rejected so both texts can be reconciled.';

COMMIT;

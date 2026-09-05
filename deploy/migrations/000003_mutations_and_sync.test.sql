\set ON_ERROR_STOP on

BEGIN;

INSERT INTO users (id, username, display_name, password_hash)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'owner',
    'Owner',
    '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI'
);

INSERT INTO sources (id, user_id, video_id, canonical_url)
VALUES (
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001',
    'dQw4w9WgXcQ',
    'https://www.youtube.com/watch?v=dQw4w9WgXcQ'
);

INSERT INTO notes (id, user_id, source_id, processing_state)
VALUES (
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-0000000000a1',
    'generating'
);

INSERT INTO transcripts (id, user_id, note_id, source_kind, language_code, normalized_text)
VALUES (
    '00000000-0000-0000-0000-0000000000e1',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-0000000000b1',
    'caption_human',
    'en',
    'a normalized transcript'
);

INSERT INTO note_revisions (id, user_id, note_id, revision_number, origin, payload)
VALUES (
    '00000000-0000-0000-0000-0000000000c1',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-0000000000b1',
    1,
    'manual',
    '{"schema_version": 1}'::jsonb
);

-- ---------------------------------------------------------------------------
-- Sync cursor allocation is gapless and strictly increasing
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    a bigint;
    b bigint;
    c bigint;
BEGIN
    a := allocate_sync_cursor('00000000-0000-0000-0000-000000000001');
    b := allocate_sync_cursor('00000000-0000-0000-0000-000000000001');
    c := allocate_sync_cursor('00000000-0000-0000-0000-000000000001');

    IF a <> 1 OR b <> 2 OR c <> 3 THEN
        RAISE EXCEPTION 'expected cursors 1,2,3 but got %,%,%', a, b, c;
    END IF;

    IF (SELECT next_cursor FROM sync_cursors
         WHERE user_id = '00000000-0000-0000-0000-000000000001') <> 4 THEN
        RAISE EXCEPTION 'expected the allocator row to advance to 4';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- The outbox refuses a duplicate cursor for one Owner
-- ---------------------------------------------------------------------------

INSERT INTO sync_events (cursor, user_id, entity_kind, entity_id, note_id, change)
VALUES (
    1,
    '00000000-0000-0000-0000-000000000001',
    'note',
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-0000000000b1',
    'updated'
);

DO $$
BEGIN
    BEGIN
        INSERT INTO sync_events (cursor, user_id, entity_kind, entity_id, change)
        VALUES (
            1,
            '00000000-0000-0000-0000-000000000001',
            'note',
            '00000000-0000-0000-0000-0000000000b1',
            'updated'
        );
        RAISE EXCEPTION 'expected a duplicate Sync cursor to be rejected';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- The mutation ledger enforces its outcome shape
-- ---------------------------------------------------------------------------

INSERT INTO note_mutations (
    mutation_id, user_id, note_id, base_revision_id, result_revision_id, outcome, sync_cursor
) VALUES (
    '00000000-0000-0000-0000-0000000000d1',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-0000000000c1',
    '00000000-0000-0000-0000-0000000000c1',
    'applied',
    1
);

DO $$
BEGIN
    -- applied without a resulting Revision
    BEGIN
        INSERT INTO note_mutations (mutation_id, user_id, note_id, outcome)
        VALUES (
            '00000000-0000-0000-0000-0000000000d2',
            '00000000-0000-0000-0000-000000000001',
            '00000000-0000-0000-0000-0000000000b1',
            'applied'
        );
        RAISE EXCEPTION 'expected an applied mutation without a Revision to be rejected';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    -- rejected, yet carrying a Revision and no reason
    BEGIN
        INSERT INTO note_mutations (
            mutation_id, user_id, note_id, result_revision_id, outcome
        ) VALUES (
            '00000000-0000-0000-0000-0000000000d3',
            '00000000-0000-0000-0000-000000000001',
            '00000000-0000-0000-0000-0000000000b1',
            '00000000-0000-0000-0000-0000000000c1',
            'rejected'
        );
        RAISE EXCEPTION 'expected a rejected mutation carrying a Revision to be rejected';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    -- a replay of a known mutation ID is refused at the primary key
    BEGIN
        INSERT INTO note_mutations (
            mutation_id, user_id, note_id, result_revision_id, outcome
        ) VALUES (
            '00000000-0000-0000-0000-0000000000d1',
            '00000000-0000-0000-0000-000000000001',
            '00000000-0000-0000-0000-0000000000b1',
            '00000000-0000-0000-0000-0000000000c1',
            'conflicted'
        );
        RAISE EXCEPTION 'expected a duplicate mutation ID to be rejected';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- Several Revisions may await a decision on one Note at the same time
-- ---------------------------------------------------------------------------

INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin, parent_revision_id,
    generated_from_transcript_id, payload, awaiting_decision_since
) VALUES (
    '00000000-0000-0000-0000-0000000000c2',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-0000000000b1',
    2,
    'conflict',
    '00000000-0000-0000-0000-0000000000c1',
    NULL,
    '{"schema_version": 1}'::jsonb,
    now()
), (
    '00000000-0000-0000-0000-0000000000c3',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-0000000000b1',
    3,
    'generated',
    '00000000-0000-0000-0000-0000000000c1',
    '00000000-0000-0000-0000-0000000000e1',
    '{"schema_version": 1}'::jsonb,
    now()
);

DO $$
BEGIN
    IF (SELECT count(*) FROM note_revisions
         WHERE note_id = '00000000-0000-0000-0000-0000000000b1'
           AND awaiting_decision_since IS NOT NULL) <> 2 THEN
        RAISE EXCEPTION 'expected two Revisions awaiting a decision on one Note';
    END IF;
END;
$$;

-- A generated Revision still needs its Transcript, so the origin shape from
-- migration 000001 must not have been loosened by this migration.
DO $$
BEGIN
    BEGIN
        UPDATE note_revisions
           SET generated_from_transcript_id = NULL
         WHERE id = '00000000-0000-0000-0000-0000000000c3';
        RAISE EXCEPTION 'expected a generated Revision to require a Transcript';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- Layout rows and the Takeaway compare-and-set token
-- ---------------------------------------------------------------------------

INSERT INTO note_diagram_layouts (note_id, user_id, positions)
VALUES (
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-000000000001',
    '{"root": {"x": 0, "y": 0}}'::jsonb
);

DO $$
BEGIN
    BEGIN
        INSERT INTO note_diagram_layouts (note_id, user_id, positions)
        VALUES (
            '00000000-0000-0000-0000-0000000000b1',
            '00000000-0000-0000-0000-000000000001',
            '[]'::jsonb
        );
        RAISE EXCEPTION 'expected a non-object positions value to be rejected';
    EXCEPTION WHEN check_violation OR unique_violation THEN
        NULL;
    END;
END;
$$;

INSERT INTO note_takeaways (note_id, user_id, text)
VALUES (
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-000000000001',
    'first thought'
);

DO $$
BEGIN
    IF (SELECT updated_cursor FROM note_takeaways
         WHERE note_id = '00000000-0000-0000-0000-0000000000b1') <> 0 THEN
        RAISE EXCEPTION 'expected a new Takeaway to start at cursor 0';
    END IF;

    -- compare-and-set: the write lands only while the token still matches
    UPDATE note_takeaways
       SET text = 'second thought', updated_cursor = 7
     WHERE note_id = '00000000-0000-0000-0000-0000000000b1'
       AND updated_cursor = 0;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'expected the compare-and-set write to land';
    END IF;

    UPDATE note_takeaways
       SET text = 'stale device overwrites', updated_cursor = 9
     WHERE note_id = '00000000-0000-0000-0000-0000000000b1'
       AND updated_cursor = 0;

    IF FOUND THEN
        RAISE EXCEPTION 'expected a stale Takeaway write to match no row';
    END IF;

    IF (SELECT text FROM note_takeaways
         WHERE note_id = '00000000-0000-0000-0000-0000000000b1') <> 'second thought' THEN
        RAISE EXCEPTION 'expected the stale write to have left the Takeaway alone';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- Purge cascades into the new tables
-- ---------------------------------------------------------------------------

DELETE FROM notes WHERE id = '00000000-0000-0000-0000-0000000000b1';

DO $$
BEGIN
    IF (SELECT count(*) FROM note_mutations) <> 0
       OR (SELECT count(*) FROM note_diagram_layouts) <> 0 THEN
        RAISE EXCEPTION 'expected Purge to cascade into mutation and layout rows';
    END IF;
END;
$$;

ROLLBACK;

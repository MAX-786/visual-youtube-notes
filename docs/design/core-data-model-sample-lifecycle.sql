-- Design fixture for the Core data model draft.
-- It demonstrates one Note through save, initial generation, regeneration,
-- manual edit, and Trash. IDs and timestamps are fixed so the relationships
-- can be inspected without mentally resolving generated values.

BEGIN;

INSERT INTO users (id, username, display_name, timezone)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'owner',
    'Owner',
    'Asia/Calcutta'
);

INSERT INTO sources (
    id, user_id, video_id, canonical_url, title, channel_id, channel_name,
    duration_seconds, published_at, metadata_fetched_at
)
VALUES (
    '10000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'dQw4w9WgXcQ',
    'https://www.youtube.com/watch?v=dQw4w9WgXcQ',
    'Example learning video',
    'UC-example',
    'Example Teacher',
    732,
    '2026-08-20T09:00:00Z',
    '2026-09-05T04:30:10Z'
);

INSERT INTO topics (id, user_id, name, normalized_name)
VALUES (
    '20000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'Learning Systems',
    'learning systems'
);

INSERT INTO tags (id, user_id, name, normalized_name)
VALUES
    (
        '21000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-000000000001',
        'Review',
        'review'
    ),
    (
        '21000000-0000-0000-0000-000000000002',
        '00000000-0000-0000-0000-000000000001',
        'Memory',
        'memory'
    );

-- Save: the Note exists before metadata/transcript/generation complete.
INSERT INTO notes (
    id, user_id, source_id, processing_state, saved_at
)
VALUES (
    '30000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    '10000000-0000-0000-0000-000000000001',
    'queued',
    '2026-09-05T04:30:00Z'
);

INSERT INTO note_takeaways (note_id, user_id, text)
VALUES (
    '30000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'I should review difficult ideas after one day and again after one week.'
);

INSERT INTO transcripts (
    id, user_id, note_id, source_kind, language_code, normalized_text
)
VALUES (
    '40000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    '30000000-0000-0000-0000-000000000001',
    'caption_automatic',
    'en',
    'Memory improves when recall is spaced. Reviewing after one day and one week strengthens retrieval.'
);

INSERT INTO transcript_chunks (
    id, transcript_id, ordinal, char_start, char_end, token_count, chunk_text
)
VALUES
    (
        '41000000-0000-0000-0000-000000000001',
        '40000000-0000-0000-0000-000000000001',
        0,
        0,
        39,
        8,
        'Memory improves when recall is spaced.'
    ),
    (
        '41000000-0000-0000-0000-000000000002',
        '40000000-0000-0000-0000-000000000001',
        1,
        40,
        101,
        12,
        'Reviewing after one day and one week strengthens retrieval.'
    );

-- Initial generated Revision becomes active.
INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin,
    generated_from_transcript_id, diagram, created_at
)
VALUES (
    '50000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    '30000000-0000-0000-0000-000000000001',
    1,
    'generated',
    '40000000-0000-0000-0000-000000000001',
    '{"schema_version":1,"type":"process_flow","nodes":[{"id":"n1","label":"Learn","provenance":"ai"},{"id":"n2","label":"Recall later","provenance":"ai"}],"edges":[{"id":"e1","from":"n1","to":"n2"}]}'::jsonb,
    '2026-09-05T04:32:00Z'
);

INSERT INTO revision_content_blocks (
    id, revision_id, kind, position, text, provenance
)
VALUES
    (
        '51000000-0000-0000-0000-000000000001',
        '50000000-0000-0000-0000-000000000001',
        'summary',
        0,
        'Spaced recall strengthens memory.',
        'ai'
    ),
    (
        '51000000-0000-0000-0000-000000000002',
        '50000000-0000-0000-0000-000000000001',
        'key_point',
        0,
        'Review after increasing intervals.',
        'ai'
    );

INSERT INTO content_block_source_refs (
    content_block_id, transcript_chunk_id, char_start, char_end
)
VALUES
    (
        '51000000-0000-0000-0000-000000000001',
        '41000000-0000-0000-0000-000000000001',
        0,
        39
    ),
    (
        '51000000-0000-0000-0000-000000000002',
        '41000000-0000-0000-0000-000000000002',
        0,
        61
    );

UPDATE notes
SET processing_state = 'ready',
    primary_topic_id = '20000000-0000-0000-0000-000000000001',
    active_revision_id = '50000000-0000-0000-0000-000000000001'
WHERE id = '30000000-0000-0000-0000-000000000001';

INSERT INTO note_tags (note_id, tag_id, user_id)
VALUES
    (
        '30000000-0000-0000-0000-000000000001',
        '21000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-000000000001'
    ),
    (
        '30000000-0000-0000-0000-000000000001',
        '21000000-0000-0000-0000-000000000002',
        '00000000-0000-0000-0000-000000000001'
    );

-- Regeneration reads the Transcript, points to the prior Active Revision,
-- creates a generated Revision, and activates it after the compare-and-set.
INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin, parent_revision_id,
    generated_from_transcript_id, generation_instruction, diagram, created_at
)
VALUES (
    '50000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    '30000000-0000-0000-0000-000000000001',
    2,
    'generated',
    '50000000-0000-0000-0000-000000000001',
    '40000000-0000-0000-0000-000000000001',
    'Make the practical timing more explicit.',
    '{"schema_version":1,"type":"timeline","events":[{"id":"d0","label":"Learn","order":0,"provenance":"ai"},{"id":"d1","label":"Review after one day","order":1,"provenance":"ai"},{"id":"d7","label":"Review after one week","order":2,"provenance":"ai"}]}'::jsonb,
    '2026-09-05T04:40:00Z'
);

INSERT INTO revision_content_blocks (
    id, revision_id, kind, position, text, provenance
)
VALUES
    (
        '52000000-0000-0000-0000-000000000001',
        '50000000-0000-0000-0000-000000000002',
        'summary',
        0,
        'Use a one-day and one-week recall schedule.',
        'ai'
    ),
    (
        '52000000-0000-0000-0000-000000000002',
        '50000000-0000-0000-0000-000000000002',
        'practical_action',
        0,
        'Schedule the first review for tomorrow.',
        'ai'
    );

UPDATE notes
SET active_revision_id = '50000000-0000-0000-0000-000000000002'
WHERE id = '30000000-0000-0000-0000-000000000001'
  AND active_revision_id = '50000000-0000-0000-0000-000000000001';

-- Manual edit copies the full active payload into a new immutable Revision.
-- The edited block flips provenance and therefore has no source ref.
INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin, parent_revision_id,
    diagram, created_at
)
VALUES (
    '50000000-0000-0000-0000-000000000003',
    '00000000-0000-0000-0000-000000000001',
    '30000000-0000-0000-0000-000000000001',
    3,
    'manual',
    '50000000-0000-0000-0000-000000000002',
    '{"schema_version":1,"type":"timeline","events":[{"id":"d0","label":"Learn deeply","order":0,"provenance":"user_edited"},{"id":"d1","label":"Review after one day","order":1,"provenance":"ai"},{"id":"d7","label":"Review after one week","order":2,"provenance":"ai"}]}'::jsonb,
    '2026-09-05T04:45:00Z'
);

INSERT INTO revision_content_blocks (
    id, revision_id, kind, position, text, provenance
)
VALUES
    (
        '53000000-0000-0000-0000-000000000001',
        '50000000-0000-0000-0000-000000000003',
        'summary',
        0,
        'Use deliberate recall after one day and one week.',
        'user_edited'
    ),
    (
        '53000000-0000-0000-0000-000000000002',
        '50000000-0000-0000-0000-000000000003',
        'practical_action',
        0,
        'Schedule the first review for tomorrow.',
        'ai'
    );

INSERT INTO content_block_source_refs (
    content_block_id, transcript_chunk_id, char_start, char_end
)
VALUES (
    '53000000-0000-0000-0000-000000000002',
    '41000000-0000-0000-0000-000000000002',
    0,
    61
);

UPDATE notes
SET active_revision_id = '50000000-0000-0000-0000-000000000003'
WHERE id = '30000000-0000-0000-0000-000000000001'
  AND active_revision_id = '50000000-0000-0000-0000-000000000002';

-- Trash preserves Save time, Primary Topic, Active Revision, Transcript, and
-- Takeaway. Purge will cascade-delete the full aggregate after 30 days.
UPDATE notes
SET trashed_at = '2026-09-05T05:00:00Z',
    purge_after = '2026-10-05T05:00:00Z'
WHERE id = '30000000-0000-0000-0000-000000000001';

COMMIT;

-- Inspect the final shape. The Takeaway is unchanged and independent of the
-- three immutable Revisions; Trash did not alter Save time or organization.
SELECT
    n.id AS note_id,
    n.saved_at,
    n.processing_state,
    n.active_revision_id,
    n.primary_topic_id,
    n.trashed_at,
    n.purge_after,
    t.text AS takeaway,
    count(r.id) AS revision_count
FROM notes n
JOIN note_takeaways t ON t.note_id = n.id
JOIN note_revisions r ON r.note_id = n.id
WHERE n.id = '30000000-0000-0000-0000-000000000001'
GROUP BY n.id, t.text;

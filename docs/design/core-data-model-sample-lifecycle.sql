-- Design fixture for the Core data model draft.
-- It demonstrates one Note through save, initial generation, regeneration,
-- manual edit, and Trash. IDs and timestamps are fixed for easy inspection.

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

-- Save: a processing Note deliberately has no Active Revision or Primary
-- Topic yet. Source metadata is ingestion-owned and user-read-only.
INSERT INTO notes (id, user_id, source_id, processing_state, saved_at)
VALUES (
    '30000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    '10000000-0000-0000-0000-000000000001',
    'queued',
    '2026-09-05T04:30:00Z'
);

-- The Takeaway is mutable user-owned state outside every Revision.
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

-- Initial generation: all immutable content is one versioned JSON payload.
INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin,
    generated_from_transcript_id, payload, created_at
)
VALUES (
    '50000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    '30000000-0000-0000-0000-000000000001',
    1,
    'generated',
    '40000000-0000-0000-0000-000000000001',
    $json$
    {
      "schema_version": 1,
      "detected_source_language": "en",
      "summary": {"id":"summary","text":"Spaced recall strengthens memory.","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000001","char_start":0,"char_end":39}]},
      "key_points": [{"id":"kp-1","text":"Review after increasing intervals.","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]}],
      "important_details": [],
      "practical_actions": [],
      "diagram": {"schema_version":1,"type":"process_flow","nodes":[{"id":"n1","label":"Learn","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000001","char_start":0,"char_end":39}]},{"id":"n2","label":"Recall later","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]}],"edges":[{"id":"e1","from":"n1","to":"n2"}]}
    }
    $json$::jsonb,
    '2026-09-05T04:32:00Z'
);

UPDATE notes
SET processing_state = 'ready',
    primary_topic_id = '20000000-0000-0000-0000-000000000001',
    active_revision_id = '50000000-0000-0000-0000-000000000001'
WHERE id = '30000000-0000-0000-0000-000000000001';

INSERT INTO note_tags (note_id, tag_id, user_id)
VALUES
    ('30000000-0000-0000-0000-000000000001', '21000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001'),
    ('30000000-0000-0000-0000-000000000001', '21000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001');

-- Regeneration reads the retained Transcript, points to the prior Active
-- Revision, and activates only after the compare-and-set succeeds.
INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin, parent_revision_id,
    generated_from_transcript_id, generation_instruction, payload, created_at
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
    $json$
    {
      "schema_version": 1,
      "detected_source_language": "en",
      "summary": {"id":"summary","text":"Use a one-day and one-week recall schedule.","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]},
      "key_points": [],
      "important_details": [],
      "practical_actions": [{"id":"action-1","text":"Schedule the first review for tomorrow.","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]}],
      "diagram": {"schema_version":1,"type":"timeline","events":[{"id":"d0","label":"Learn","order":0,"provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000001","char_start":0,"char_end":39}]},{"id":"d1","label":"Review after one day","order":1,"provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]},{"id":"d7","label":"Review after one week","order":2,"provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]}]}
    }
    $json$::jsonb,
    '2026-09-05T04:40:00Z'
);

UPDATE notes
SET active_revision_id = '50000000-0000-0000-0000-000000000002'
WHERE id = '30000000-0000-0000-0000-000000000001'
  AND active_revision_id = '50000000-0000-0000-0000-000000000001';

-- Manual edit copies the active payload into a new immutable Revision. The
-- changed block becomes user_edited and loses its Source refs.
INSERT INTO note_revisions (
    id, user_id, note_id, revision_number, origin, parent_revision_id,
    payload, created_at
)
VALUES (
    '50000000-0000-0000-0000-000000000003',
    '00000000-0000-0000-0000-000000000001',
    '30000000-0000-0000-0000-000000000001',
    3,
    'manual',
    '50000000-0000-0000-0000-000000000002',
    $json$
    {
      "schema_version": 1,
      "detected_source_language": "en",
      "summary": {"id":"summary","text":"Use deliberate recall after one day and one week.","provenance":"user_edited","source_refs":[]},
      "key_points": [],
      "important_details": [],
      "practical_actions": [{"id":"action-1","text":"Schedule the first review for tomorrow.","provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]}],
      "diagram": {"schema_version":1,"type":"timeline","events":[{"id":"d0","label":"Learn deeply","order":0,"provenance":"user_edited","source_refs":[]},{"id":"d1","label":"Review after one day","order":1,"provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]},{"id":"d7","label":"Review after one week","order":2,"provenance":"ai","source_refs":[{"chunk_id":"41000000-0000-0000-0000-000000000002","char_start":0,"char_end":61}]}]}
    }
    $json$::jsonb,
    '2026-09-05T04:45:00Z'
);

UPDATE notes
SET active_revision_id = '50000000-0000-0000-0000-000000000003'
WHERE id = '30000000-0000-0000-0000-000000000001'
  AND active_revision_id = '50000000-0000-0000-0000-000000000002';

-- Trash preserves Save time, Primary Topic, Active Revision, Transcript, and
-- Takeaway. Purge will cascade-delete the aggregate after 30 days.
UPDATE notes
SET trashed_at = '2026-09-05T05:00:00Z',
    purge_after = '2026-10-05T05:00:00Z'
WHERE id = '30000000-0000-0000-0000-000000000001';

COMMIT;

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

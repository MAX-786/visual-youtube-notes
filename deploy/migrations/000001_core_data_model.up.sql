BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE note_processing_state AS ENUM (
    'queued',
    'metadata',
    'captions',
    'transcribing',
    'generating',
    'indexing',
    'ready',
    'failed',
    'cancelling',
    'cancelled'
);

CREATE TYPE transcript_source_kind AS ENUM (
    'caption_human',
    'caption_automatic',
    'stt'
);

CREATE TYPE revision_origin AS ENUM (
    'generated',
    'manual',
    'conflict',
    'restore'
);

CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username text NOT NULL,
    display_name text NOT NULL,
    timezone text NOT NULL DEFAULT 'Asia/Calcutta',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_username_nonempty CHECK (btrim(username) <> ''),
    CONSTRAINT users_display_name_nonempty CHECK (btrim(display_name) <> '')
);

CREATE UNIQUE INDEX users_username_normalized_uq ON users (lower(btrim(username)));

CREATE TABLE sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    video_id text NOT NULL,
    canonical_url text NOT NULL,
    title text,
    channel_id text,
    channel_name text,
    duration_seconds integer,
    published_at timestamptz,
    metadata_fetched_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sources_video_id_format CHECK (video_id ~ '^[A-Za-z0-9_-]{11}$'),
    CONSTRAINT sources_duration_nonnegative CHECK (duration_seconds IS NULL OR duration_seconds >= 0),
    CONSTRAINT sources_id_user_uq UNIQUE (id, user_id),
    CONSTRAINT sources_user_video_uq UNIQUE (user_id, video_id)
);

COMMENT ON TABLE sources IS
    'YouTube metadata is written by ingestion and exposed read-only to the owner in V1.';

CREATE TABLE topics (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    normalized_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT topics_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT topics_normalized_name_nonempty CHECK (btrim(normalized_name) <> ''),
    CONSTRAINT topics_id_user_uq UNIQUE (id, user_id),
    CONSTRAINT topics_user_normalized_name_uq UNIQUE (user_id, normalized_name)
);

CREATE TABLE tags (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    normalized_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tags_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT tags_normalized_name_nonempty CHECK (btrim(normalized_name) <> ''),
    CONSTRAINT tags_id_user_uq UNIQUE (id, user_id),
    CONSTRAINT tags_user_normalized_name_uq UNIQUE (user_id, normalized_name)
);

CREATE TABLE notes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_id uuid NOT NULL,
    processing_state note_processing_state NOT NULL DEFAULT 'queued',
    failure_stage text,
    saved_at timestamptz NOT NULL DEFAULT now(),
    primary_topic_id uuid,
    active_revision_id uuid,
    trashed_at timestamptz,
    purge_after timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notes_id_user_uq UNIQUE (id, user_id),
    CONSTRAINT notes_user_source_uq UNIQUE (user_id, source_id),
    CONSTRAINT notes_source_owner_fk
        FOREIGN KEY (source_id, user_id) REFERENCES sources(id, user_id) ON DELETE CASCADE,
    CONSTRAINT notes_primary_topic_owner_fk
        FOREIGN KEY (primary_topic_id, user_id) REFERENCES topics(id, user_id),
    CONSTRAINT notes_failure_stage_consistent CHECK (
        (processing_state = 'failed' AND failure_stage IS NOT NULL)
        OR (processing_state <> 'failed' AND failure_stage IS NULL)
    ),
    CONSTRAINT notes_trash_window_consistent CHECK (
        (trashed_at IS NULL AND purge_after IS NULL)
        OR (trashed_at IS NOT NULL AND purge_after > trashed_at)
    ),
    CONSTRAINT notes_ready_has_revision CHECK (
        processing_state <> 'ready' OR active_revision_id IS NOT NULL
    ),
    CONSTRAINT notes_ready_has_topic_unless_trashed CHECK (
        processing_state <> 'ready' OR trashed_at IS NOT NULL OR primary_topic_id IS NOT NULL
    )
);

COMMENT ON COLUMN notes.saved_at IS
    'Submission time; never changes on duplicate submission, regeneration, restore, or Trash restore.';
COMMENT ON COLUMN notes.trashed_at IS
    'Trash is orthogonal to processing_state so restore reveals the state the Note had before deletion.';

CREATE TABLE transcripts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    note_id uuid NOT NULL,
    source_kind transcript_source_kind NOT NULL,
    language_code text NOT NULL,
    normalized_text text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT transcripts_text_nonempty CHECK (btrim(normalized_text) <> ''),
    CONSTRAINT transcripts_id_note_user_uq UNIQUE (id, note_id, user_id),
    CONSTRAINT transcripts_note_uq UNIQUE (note_id),
    CONSTRAINT transcripts_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE
);

COMMENT ON TABLE transcripts IS
    'Private retained source text. It is immutable and exposed read-only in V1.';

CREATE TABLE transcript_chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transcript_id uuid NOT NULL REFERENCES transcripts(id) ON DELETE CASCADE,
    ordinal integer NOT NULL,
    char_start integer NOT NULL,
    char_end integer NOT NULL,
    token_count integer NOT NULL,
    chunk_text text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT transcript_chunks_ordinal_nonnegative CHECK (ordinal >= 0),
    CONSTRAINT transcript_chunks_span_valid CHECK (char_start >= 0 AND char_end > char_start),
    CONSTRAINT transcript_chunks_token_count_positive CHECK (token_count > 0),
    CONSTRAINT transcript_chunks_text_nonempty CHECK (btrim(chunk_text) <> ''),
    CONSTRAINT transcript_chunks_transcript_ordinal_uq UNIQUE (transcript_id, ordinal)
);

CREATE TABLE note_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    note_id uuid NOT NULL,
    revision_number integer NOT NULL,
    origin revision_origin NOT NULL,
    parent_revision_id uuid,
    generated_from_transcript_id uuid,
    generation_instruction text,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT note_revisions_number_positive CHECK (revision_number > 0),
    CONSTRAINT note_revisions_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT note_revisions_id_note_user_uq UNIQUE (id, note_id, user_id),
    CONSTRAINT note_revisions_note_number_uq UNIQUE (note_id, revision_number),
    CONSTRAINT note_revisions_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE,
    CONSTRAINT note_revisions_parent_same_note_fk
        FOREIGN KEY (parent_revision_id, note_id, user_id)
        REFERENCES note_revisions(id, note_id, user_id),
    CONSTRAINT note_revisions_transcript_same_note_fk
        FOREIGN KEY (generated_from_transcript_id, note_id, user_id)
        REFERENCES transcripts(id, note_id, user_id),
    CONSTRAINT note_revisions_origin_shape CHECK (
        (origin = 'generated' AND generated_from_transcript_id IS NOT NULL)
        OR (origin IN ('manual', 'conflict', 'restore') AND generated_from_transcript_id IS NULL)
    ),
    CONSTRAINT note_revisions_parent_shape CHECK (
        (revision_number = 1 AND parent_revision_id IS NULL)
        OR (revision_number > 1 AND parent_revision_id IS NOT NULL)
    )
);

ALTER TABLE notes
    ADD CONSTRAINT notes_active_revision_same_note_fk
    FOREIGN KEY (active_revision_id, id, user_id)
    REFERENCES note_revisions(id, note_id, user_id)
    DEFERRABLE INITIALLY DEFERRED;

COMMENT ON COLUMN note_revisions.payload IS
    'Atomic, immutable, schema-versioned generated or edited content, including Content blocks, Diagram, Provenance, and Source refs. Validated by the application before insert.';

CREATE TABLE note_takeaways (
    note_id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    text text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT note_takeaways_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE
);

COMMENT ON TABLE note_takeaways IS
    'User-owned Takeaway stored outside immutable Revisions and never modified by generation.';

CREATE TABLE note_tags (
    note_id uuid NOT NULL,
    tag_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (note_id, tag_id),
    CONSTRAINT note_tags_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE,
    CONSTRAINT note_tags_tag_owner_fk
        FOREIGN KEY (tag_id, user_id) REFERENCES tags(id, user_id) ON DELETE CASCADE
);

CREATE INDEX notes_user_saved_idx ON notes (user_id, saved_at DESC, id);
CREATE INDEX notes_user_topic_saved_idx ON notes (user_id, primary_topic_id, saved_at DESC, id);
CREATE INDEX notes_purge_due_idx ON notes (purge_after) WHERE purge_after IS NOT NULL;
CREATE INDEX note_revisions_note_created_idx ON note_revisions (note_id, created_at DESC);
CREATE INDEX transcript_chunks_transcript_span_idx ON transcript_chunks (transcript_id, char_start);
CREATE INDEX note_tags_user_tag_idx ON note_tags (user_id, tag_id, note_id);

CREATE TRIGGER users_set_updated_at
BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER topics_set_updated_at
BEFORE UPDATE ON topics
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER tags_set_updated_at
BEFORE UPDATE ON tags
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER notes_set_updated_at
BEFORE UPDATE ON notes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER note_takeaways_set_updated_at
BEFORE UPDATE ON note_takeaways
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMIT;

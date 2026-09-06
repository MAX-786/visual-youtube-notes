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
    '00000000-0000-0000-0000-0000000000c1',
    '00000000-0000-0000-0000-000000000001',
    'aircAruvnKk',
    'https://www.youtube.com/watch?v=aircAruvnKk'
);

-- The track columns are all set or all null: half a track is refused.
DO $$
BEGIN
    BEGIN
        UPDATE sources SET caption_track_lang = 'en'
         WHERE id = '00000000-0000-0000-0000-0000000000c1';
        RAISE EXCEPTION 'expected a language without its track bytes to be refused';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
    BEGIN
        UPDATE sources SET caption_track_vtt = 'WEBVTT'::bytea, caption_track_automatic = false
         WHERE id = '00000000-0000-0000-0000-0000000000c1';
        RAISE EXCEPTION 'expected track bytes without a language to be refused';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END;
$$;

-- The language is a yt-dlp track key, not free text.
DO $$
BEGIN
    UPDATE sources SET caption_track_lang = 'en; drop', caption_track_automatic = false, caption_track_vtt = 'WEBVTT'::bytea
     WHERE id = '00000000-0000-0000-0000-0000000000c1';
    RAISE EXCEPTION 'expected a malformed track key to be refused';
EXCEPTION WHEN check_violation THEN
    NULL;
END;
$$;

-- A whole track is accepted, with the -orig variant a dubbed video carries.
UPDATE sources SET caption_track_lang = 'hi-orig', caption_track_automatic = true, caption_track_vtt = 'WEBVTT'::bytea
 WHERE id = '00000000-0000-0000-0000-0000000000c1';

DO $$
DECLARE
    stored text;
BEGIN
    SELECT caption_track_lang INTO stored FROM sources WHERE id = '00000000-0000-0000-0000-0000000000c1';
    IF stored IS DISTINCT FROM 'hi-orig' THEN
        RAISE EXCEPTION 'expected the track to be stored, got %', stored;
    END IF;
END;
$$;

-- Clearing the track is the "no track in a supported language" signal.
UPDATE sources SET caption_track_lang = NULL, caption_track_automatic = NULL, caption_track_vtt = NULL
 WHERE id = '00000000-0000-0000-0000-0000000000c1';

ROLLBACK;

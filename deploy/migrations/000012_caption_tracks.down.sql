BEGIN;

ALTER TABLE sources
    DROP CONSTRAINT sources_caption_track_lang_format,
    DROP CONSTRAINT sources_caption_track_all_or_none,
    DROP COLUMN caption_track_vtt,
    DROP COLUMN caption_track_automatic,
    DROP COLUMN caption_track_lang;

COMMIT;

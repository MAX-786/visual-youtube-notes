BEGIN;

-- Ticket #35: the caption track travels with the Source.
--
-- Ticket #4 fixed one yt-dlp call per Note for the metadata Stage. Running
-- the pipeline against YouTube showed why that has to hold for the captions
-- Stage as well: every extraction is several requests, and three of them in
-- a row from one address (metadata, the captions Stage's own lookup, then the
-- track download) were answered with HTTP 429 and a "confirm you're not a
-- bot" sign-in wall. So the metadata extraction now also writes the English
-- or Hindi caption tracks, chooses one in the spec 8.2 order, and keeps it
-- here. The captions Stage normalizes what is stored and never touches the
-- network; a later run of the same Note reuses it too.
--
-- The three columns are all set or all null: a Source whose metadata has been
-- fetched and whose track columns are null is a video with no track in a
-- supported language, which is the signal that hands the run to speech to
-- text. A track is a few tens of kilobytes of WebVTT; it stays with the Source.
ALTER TABLE sources
    ADD COLUMN caption_track_lang text,
    ADD COLUMN caption_track_automatic boolean,
    ADD COLUMN caption_track_vtt bytea,
    ADD CONSTRAINT sources_caption_track_all_or_none CHECK (
        (caption_track_lang IS NULL) = (caption_track_automatic IS NULL)
        AND (caption_track_lang IS NULL) = (caption_track_vtt IS NULL)
    ),
    ADD CONSTRAINT sources_caption_track_lang_format CHECK (
        caption_track_lang IS NULL OR caption_track_lang ~ '^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$'
    );

COMMENT ON COLUMN sources.caption_track_lang IS
    'yt-dlp track key of the caption track the metadata extraction chose (en, hi, en-orig, ...); null once metadata is fetched means no track in a supported language.';
COMMENT ON COLUMN sources.caption_track_automatic IS
    'Whether the chosen track is YouTube automatic captioning rather than a human one.';
COMMENT ON COLUMN sources.caption_track_vtt IS
    'The chosen track as fetched, WebVTT bytes. The captions Stage normalizes from here without a second extraction.';

COMMIT;

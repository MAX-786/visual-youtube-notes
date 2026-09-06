package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// RegisterWorkers adds every Stage worker to a River worker set. Stages a
// later slice implements are registered as placeholders that fail the run
// permanently with a message naming what is missing, so a Note never sits
// in a state nothing will move.
func RegisterWorkers(workers *river.Workers, d *Deps) {
	river.AddWorker(workers, &stageWorker[MetadataArgs]{deps: d, stage: StageMetadata, do: metadataWork, args: func(a MetadataArgs) Run { return a.Run }})
	river.AddWorker(workers, &stageWorker[CaptionsArgs]{deps: d, stage: StageCaptions, do: captionsWork, args: func(a CaptionsArgs) Run { return a.Run }})
	transcribing := d.Hooks.Transcribing
	if transcribing == nil && d.AI != nil && d.Media != nil {
		transcribing = transcribingWork
	}
	if transcribing == nil {
		transcribing = placeholder("Speech to text is not available in this build.")
	}
	generating := d.Hooks.Generating
	if generating == nil && d.AI != nil {
		generating = generatingWork
	}
	if generating == nil {
		generating = placeholder("Note generation is not available in this build.")
	}
	indexing := d.Hooks.Indexing
	if indexing == nil && d.AI != nil {
		indexing = indexingWork
	}
	if indexing == nil {
		indexing = placeholder("Search indexing is not available in this build.")
	}
	river.AddWorker(workers, &stageWorker[TranscribingArgs]{deps: d, stage: StageTranscribing, do: transcribing, args: func(a TranscribingArgs) Run { return a.Run }})
	river.AddWorker(workers, &stageWorker[GeneratingArgs]{deps: d, stage: StageGenerating, do: generating, args: func(a GeneratingArgs) Run { return a.Run }})
	river.AddWorker(workers, &stageWorker[IndexingArgs]{deps: d, stage: StageIndexing, do: indexing, args: func(a IndexingArgs) Run { return a.Run }})
}

func placeholder(message string) work {
	return func(context.Context, *Deps, *NoteRow, *StageRun) (*result, error) {
		return nil, Fail(Permanent, "not_built", message, nil)
	}
}

// metadataWork runs yt-dlp's one extraction, applies eligibility, fetches
// the caption track it chose with a single request, and writes the Source
// row with the track, so the captions Stage never asks YouTube again
// (spec 8.1, 8.2; ticket #4).
func metadataWork(ctx context.Context, d *Deps, note *NoteRow, _ *StageRun) (*result, error) {
	info, err := d.Extractor.Metadata(ctx, note.VideoID)
	if err != nil {
		return nil, err
	}
	if se := Eligibility(info); se != nil {
		return nil, se
	}
	var track *CaptionTrack
	if choice := ChooseCaptions(info); choice != nil {
		format := info.TrackFormat(choice)
		if format == nil {
			return nil, Fail(Permanent, "captions_url", "YouTube listed a caption track without a way to fetch it.", nil)
		}
		raw, err := d.Extractor.FetchCaptions(ctx, *format)
		if err != nil {
			return nil, err
		}
		track = &CaptionTrack{Lang: choice.Lang, Automatic: choice.Automatic, VTT: raw}
	}
	raw, _ := json.Marshal(info)
	sum := sha256.Sum256(raw)
	duration := int(info.Duration)
	var published *time.Time
	if len(info.UploadDate) == 8 {
		if t, err := time.Parse("20060102", info.UploadDate); err == nil {
			published = &t
		}
	}
	return &result{
		fingerprint: sum[:],
		size:        int64(len(raw)),
		next:        StageCaptions,
		apply: func(ctx context.Context, tx pgx.Tx, note *NoteRow) error {
			if err := storeSource(ctx, tx, note.SourceID, info, duration, published, track); err != nil {
				return err
			}
			// The Source changed, which the metadata child of search and every
			// card reads; a Sync event carries it to the other devices.
			return appendNoteUpdated(ctx, tx, note)
		},
	}, nil
}

// captionsWork normalizes the caption track the metadata Stage stored, and
// either stores the Transcript and skips transcribing, or hands the run to
// speech to text (spec 8.2). It touches no network.
func captionsWork(ctx context.Context, d *Deps, note *NoteRow, _ *StageRun) (*result, error) {
	handoff := func(reason string) (*result, error) {
		d.Logger.Info("captions handed to speech to text", "note", note.ID, "reason", reason)
		return toSTT(reason), nil
	}
	track, err := loadCaptionTrack(ctx, d.Pool, note.SourceID)
	if err != nil {
		return nil, err
	}
	if track == nil {
		return handoff("no caption track in a supported language")
	}
	normalized := Normalize(ParseVTT(string(track.VTT)))
	duration := 0.0
	if note.DurationSeconds != nil {
		duration = float64(*note.DurationSeconds)
	}
	if ok, why := Usable(normalized, duration); !ok {
		return handoff("captions unusable: " + why)
	}
	sum := sha256.Sum256(track.VTT)
	sourceKind := "caption_human"
	if track.Automatic {
		sourceKind = "caption_automatic"
	}
	language := Language(track.Lang, normalized.Text)
	return &result{
		fingerprint: sum[:],
		size:        int64(len(track.VTT)),
		next:        StageGenerating,
		skip:        StageTranscribing,
		apply: func(ctx context.Context, tx pgx.Tx, note *NoteRow) error {
			return StoreTranscript(ctx, tx, note, sourceKind, language, normalized)
		},
	}, nil
}

func toSTT(reason string) *result {
	sum := sha256.Sum256([]byte(reason))
	return &result{fingerprint: sum[:], size: 0, next: StageTranscribing}
}

// StoreTranscript writes the Transcript and its chunks, replacing any earlier
// run's.
func StoreTranscript(ctx context.Context, tx pgx.Tx, note *NoteRow, sourceKind, language string, n *Normalized) error {
	if _, err := tx.Exec(ctx, `DELETE FROM transcripts WHERE note_id = $1`, note.ID); err != nil {
		return err
	}
	var transcriptID [16]byte
	if err := tx.QueryRow(ctx, `
		INSERT INTO transcripts (user_id, note_id, source_kind, language_code, normalized_text)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, note.UserID, note.ID, sourceKind, language, n.Text).Scan(&transcriptID); err != nil {
		return err
	}
	for _, c := range n.Chunks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO transcript_chunks (transcript_id, ordinal, char_start, char_end, token_count, chunk_text)
			VALUES ($1, $2, $3, $4, $5, $6)`, transcriptID, c.Ordinal, c.CharStart, c.CharEnd, max(c.TokenCount, 1), c.Text); err != nil {
			return err
		}
	}
	return nil
}

package pipeline

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
)

// perChunkTimeout bounds one transcription request; the Stage as a whole
// has its own 45-minute timeout (docs/design/pipeline.md).
const perChunkTimeout = 10 * time.Minute

// transcribingWork is the transcribing Stage (spec 8.2, ADR 0006, ADR 0008,
// ADR 0017), alone on the media queue: the free-space preflight, the audio
// download without re-encoding, an FFmpeg split only when the file exceeds
// the provider's limit, one transcription call per piece with a ledger row
// each, deterministic overlap removal, and the Transcript stored with its
// script untouched. The runner removes the Attempt's directory afterwards
// whatever happens, so no temporary media outlives the job.
func transcribingWork(ctx context.Context, d *Deps, note *NoteRow, sr *StageRun) (*result, error) {
	if d.Media == nil {
		return nil, Fail(Permanent, "not_built", "Speech to text is not available in this build.", nil)
	}
	if err := preflightFreeSpace(d); err != nil {
		return nil, err
	}
	creds, err := d.AI.Credentials(ctx, d.Pool, note.UserID, ai.RoleTranscription)
	if err != nil {
		return nil, credentialsError(ai.RoleTranscription, err)
	}
	client := ai.NewClient(*creds, d.AITransport)

	audio, err := d.Extractor.Audio(ctx, note.VideoID, sr.Dir)
	if err != nil {
		return nil, err
	}
	pieces, err := pieces(ctx, d, audio, sr.Dir)
	if err != nil {
		return nil, err
	}

	texts := make([]string, 0, len(pieces))
	var lastRequestID string
	var billedSeconds float64
	for i, piece := range pieces {
		duration, err := d.Media.DurationSeconds(ctx, piece)
		if err != nil {
			return nil, err
		}
		text, requestID, err := transcribePiece(ctx, d, note, sr, creds, client, piece, duration)
		if err != nil {
			return nil, err
		}
		d.Logger.Info("transcribed a piece", "note", note.ID, "piece", i+1, "of", len(pieces), "seconds", duration)
		texts = append(texts, text)
		lastRequestID = requestID
		billedSeconds += duration
		// Each piece is removed as soon as it has been sent, so disk use
		// never exceeds the download plus one piece's copy.
		if piece != audio {
			_ = os.Remove(piece)
		}
	}
	_ = os.Remove(audio)

	merged := MergeTranscriptions(texts)
	normalized := NormalizeText(merged)
	if normalized.Words == 0 {
		return nil, Fail(Permanent, "transcript_empty", "Speech to text returned no words for this video.", nil)
	}
	language := Language("", normalized.Text)
	sum := sha256.Sum256([]byte(merged))
	return &result{
		providerRequestID: lastRequestID,
		fingerprint:       sum[:],
		size:              int64(len(merged)),
		next:              StageGenerating,
		apply: func(ctx context.Context, tx pgx.Tx, note *NoteRow) error {
			d.Logger.Info("transcript stored", "note", note.ID, "source", "stt", "words", normalized.Words, "chunks", len(normalized.Chunks), "billed_seconds", billedSeconds)
			return StoreTranscript(ctx, tx, note, "stt", language, normalized)
		},
	}, nil
}

// preflightFreeSpace refuses to begin below the configured floor (ADR 0008),
// naming the number so the Owner knows what to clear.
func preflightFreeSpace(d *Deps) error {
	if d.TempMinFreeBytes <= 0 {
		return nil
	}
	free, err := freeBytes(d.TempDir)
	if err != nil {
		return Fail(Transient, "disk_check_failed", "The worker could not check its free disk space; it will be retried.", err)
	}
	if free < d.TempMinFreeBytes {
		return Fail(Transient, "disk_full",
			fmt.Sprintf("The worker has %.1f GiB free, below the %.1f GiB it needs before downloading audio. Clear space on the temporary volume; the run retries on its own.",
				float64(free)/(1<<30), float64(d.TempMinFreeBytes)/(1<<30)), nil)
	}
	return nil
}

// pieces is the whole file when it fits the provider's limit, else the
// FFmpeg split of spec 8.2.
func pieces(ctx context.Context, d *Deps, audio, dir string) ([]string, error) {
	info, err := os.Stat(audio)
	if err != nil {
		return nil, err
	}
	limit := d.MaxUploadBytes
	if limit <= 0 {
		limit = maxUploadBytes
	}
	if info.Size() <= limit {
		return []string{audio}, nil
	}
	splitDir := filepath.Join(dir, "pieces")
	if err := os.MkdirAll(splitDir, 0o750); err != nil {
		return nil, err
	}
	out, err := d.Media.Split(ctx, audio, splitDir, chunkLength, chunkOverlap)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, Fail(Permanent, "audio_unreadable", "The downloaded audio could not be split.", nil)
	}
	return out, nil
}

// transcribePiece sends one piece with its own ledger row and timeout.
func transcribePiece(ctx context.Context, d *Deps, note *NoteRow, sr *StageRun, creds *ai.Credentials, client *ai.Client, piece string, duration float64) (string, string, error) {
	file, err := os.Open(piece)
	if err != nil {
		return "", "", err
	}
	defer file.Close() //nolint:errcheck // read-only handle
	usageID, err := openCall(ctx, d, note, sr, creds, ai.UnitMap{"audio_seconds": duration})
	if err != nil {
		return "", "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, perChunkTimeout)
	defer cancel()
	res, callErr := client.Transcribe(callCtx, ai.TranscribeRequest{Audio: file, Filename: filepath.Base(piece), DurationSeconds: duration})
	if callErr != nil {
		closeCall(ctx, d, usageID, nil, "", callErr)
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", providerFailure(ctx, d, note.UserID, ai.RoleTranscription, callErr)
	}
	closeCall(ctx, d, usageID, res.Units, res.RequestID, nil)
	return res.Text, res.RequestID, nil
}

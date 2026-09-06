// Package pipeline is the processing pipeline of ADR 0006: each Stage is its
// own River job, chained with JobCompleteTx, with per-Stage Attempts persisted
// before every external call and cooperative cancellation checked twice.
package pipeline

import (
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// Queue names (ADR 0006): two general workers, and FFmpeg plus speech to
// text serialized by living alone on the media queue.
const (
	QueueDefault = "default"
	QueueMedia   = "media"
)

// MaxAttempts is one initial try plus three automatic retries (ADR 0006).
const MaxAttempts = 4

// Stage names, spelled as the stage_kind enum spells them.
const (
	StageMetadata     = "metadata"
	StageCaptions     = "captions"
	StageTranscribing = "transcribing"
	StageGenerating   = "generating"
	StageIndexing     = "indexing"
)

// Run identifies one processing run of one Note.
type Run struct {
	NoteID            uuid.UUID `json:"note_id"`
	ProcessingVersion int       `json:"processing_version"`
}

// stageOpts is the insert configuration every Stage job shares: the queue,
// the attempt ladder, and uniqueness on (note, version) so a duplicate
// enqueue of the same Stage cannot run twice.
func stageOpts(queue string) river.InsertOpts {
	return river.InsertOpts{
		Queue:       queue,
		MaxAttempts: MaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// MetadataArgs runs the metadata Stage.
type MetadataArgs struct{ Run }

// Kind implements river.JobArgs.
func (MetadataArgs) Kind() string { return StageMetadata }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (MetadataArgs) InsertOpts() river.InsertOpts { return stageOpts(QueueDefault) }

// CaptionsArgs runs the captions Stage.
type CaptionsArgs struct{ Run }

// Kind implements river.JobArgs.
func (CaptionsArgs) Kind() string { return StageCaptions }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (CaptionsArgs) InsertOpts() river.InsertOpts { return stageOpts(QueueDefault) }

// TranscribingArgs runs the transcribing Stage on the media queue.
type TranscribingArgs struct{ Run }

// Kind implements river.JobArgs.
func (TranscribingArgs) Kind() string { return StageTranscribing }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (TranscribingArgs) InsertOpts() river.InsertOpts { return stageOpts(QueueMedia) }

// GeneratingArgs runs the generating Stage.
type GeneratingArgs struct{ Run }

// Kind implements river.JobArgs.
func (GeneratingArgs) Kind() string { return StageGenerating }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (GeneratingArgs) InsertOpts() river.InsertOpts { return stageOpts(QueueDefault) }

// IndexingArgs runs the indexing Stage.
type IndexingArgs struct{ Run }

// Kind implements river.JobArgs.
func (IndexingArgs) Kind() string { return StageIndexing }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (IndexingArgs) InsertOpts() river.InsertOpts { return stageOpts(QueueDefault) }

// StageTimeouts are per Stage (ADR 0006).
var StageTimeouts = map[string]time.Duration{
	StageMetadata:     60 * time.Second,
	StageCaptions:     2 * time.Minute,
	StageTranscribing: 45 * time.Minute,
	StageGenerating:   5 * time.Minute,
	StageIndexing:     10 * time.Minute,
}

// RetryDelays is the ladder of spec 8.4: about 30 seconds, two minutes and
// ten minutes.
var RetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// NextRetry maps River's incremented attempt counter onto the ladder.
func NextRetry(attempt int, now time.Time) time.Time {
	i := attempt - 1
	if i < 0 {
		i = 0
	}
	if i >= len(RetryDelays) {
		i = len(RetryDelays) - 1
	}
	return now.Add(RetryDelays[i])
}

// UndoDelay is how long the first Stage stays scheduled so an Undo can remove
// it before any external work begins (spec 5.1).
const UndoDelay = 5 * time.Second

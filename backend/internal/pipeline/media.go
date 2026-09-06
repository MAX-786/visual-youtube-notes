package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Media is what the transcribing Stage needs from FFmpeg: the length of an
// audio file and a split into overlapping pieces. An interface so the tests
// run without FFmpeg on the machine.
type Media interface {
	// DurationSeconds measures the file.
	DurationSeconds(ctx context.Context, path string) (float64, error)
	// Split cuts the file into pieces of at most chunk seconds, each after
	// the first starting overlap seconds before the previous one ended,
	// without re-encoding. It returns the pieces in order.
	Split(ctx context.Context, path, dir string, chunk, overlap time.Duration) ([]string, error)
}

// Spec 8.2: at most twenty-minute chunks with fifteen seconds of overlap.
const (
	chunkLength  = 20 * time.Minute
	chunkOverlap = 15 * time.Second
	// maxUploadBytes is OpenAI's per-file limit (ADR 0017).
	maxUploadBytes = 25 << 20
)

// FFmpeg drives the ffmpeg and ffprobe binaries with the same environment
// hygiene as yt-dlp (ADR 0010).
type FFmpeg struct {
	ffmpeg  string
	ffprobe string
}

// NewFFmpeg names the binaries; empty means "ffmpeg" and "ffprobe" on PATH.
func NewFFmpeg(ffmpeg, ffprobe string) *FFmpeg {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	return &FFmpeg{ffmpeg: ffmpeg, ffprobe: ffprobe}
}

// DurationSeconds asks ffprobe for the container duration.
func (f *FFmpeg) DurationSeconds(ctx context.Context, path string) (float64, error) {
	cmd := exec.CommandContext(ctx, f.ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
	cmd.Env = scrubbedEnv()
	out, err := cmd.Output()
	if err != nil {
		return 0, toolError("ffprobe", err)
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || seconds <= 0 {
		return 0, Fail(Permanent, "audio_unreadable", "The downloaded audio could not be read.", fmt.Errorf("ffprobe duration %q", strings.TrimSpace(string(out))))
	}
	return seconds, nil
}

// Split cuts with stream copy (no re-encoding): one ffmpeg run per piece,
// seeking to the piece's start. Pieces keep the source container so the
// transcription API accepts them as it accepts the whole.
func (f *FFmpeg) Split(ctx context.Context, path, dir string, chunk, overlap time.Duration) ([]string, error) {
	total, err := f.DurationSeconds(ctx, path)
	if err != nil {
		return nil, err
	}
	ext := filepath.Ext(path)
	var pieces []string
	for i, start := range chunkStarts(total, chunk.Seconds(), overlap.Seconds()) {
		out := filepath.Join(dir, fmt.Sprintf("chunk-%03d%s", i, ext))
		args := []string{"-v", "error", "-y", "-ss", formatSeconds(start), "-i", path, "-t", formatSeconds(chunk.Seconds()), "-c", "copy", "-map", "0:a:0", out}
		cmd := exec.CommandContext(ctx, f.ffmpeg, args...)
		cmd.Env = scrubbedEnv()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, toolError("ffmpeg", fmt.Errorf("%w: %s", err, lastLine(stderr.String())))
		}
		pieces = append(pieces, out)
	}
	return pieces, nil
}

// chunkStarts is the deterministic cut plan: the second and later pieces
// begin overlap seconds before the previous piece ends.
func chunkStarts(total, chunk, overlap float64) []float64 {
	if total <= chunk {
		return []float64{0}
	}
	step := chunk - overlap
	var starts []float64
	for start := 0.0; start < total; start += step {
		starts = append(starts, start)
		if start+chunk >= total {
			break
		}
	}
	return starts
}

func formatSeconds(s float64) string {
	return strconv.FormatFloat(s, 'f', 3, 64)
}

// scrubbedEnv is the allowlisted environment every media child gets.
func scrubbedEnv() []string {
	var env []string
	for _, key := range passThrough {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// toolError classifies a failed FFmpeg run: a missing binary is the
// deployment's fault and permanent; a cancelled context is the caller's;
// anything else is treated as a bad file, which a retry will not fix.
func toolError(tool string, err error) error {
	var exit *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return Fail(Permanent, "media_tool_missing", "The audio tool "+tool+" is not installed in the worker image.", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &exit):
		return Fail(Permanent, "audio_unreadable", "The downloaded audio could not be processed.", err)
	default:
		return Fail(Transient, "media_tool_failed", "The audio tool failed; it will be retried.", err)
	}
}

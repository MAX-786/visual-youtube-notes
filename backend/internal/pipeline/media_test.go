package pipeline

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestChunkStartsOverlapByFifteenSeconds(t *testing.T) {
	// 50 minutes of audio in twenty-minute pieces overlapping fifteen
	// seconds: 0, 19:45, 39:30, and the last piece reaches the end.
	starts := chunkStarts(50*60, chunkLength.Seconds(), chunkOverlap.Seconds())
	want := []float64{0, 1185, 2370}
	if len(starts) != len(want) {
		t.Fatalf("starts %v", starts)
	}
	for i := range want {
		if starts[i] != want[i] {
			t.Fatalf("starts %v", starts)
		}
	}
	if s := chunkStarts(600, chunkLength.Seconds(), chunkOverlap.Seconds()); len(s) != 1 || s[0] != 0 {
		t.Fatalf("a short file is one piece: %v", s)
	}
	// Exactly two pieces when the file just exceeds one chunk.
	if s := chunkStarts(chunkLength.Seconds()+1, chunkLength.Seconds(), chunkOverlap.Seconds()); len(s) != 2 {
		t.Fatalf("just over one chunk: %v", s)
	}
}

func TestRemoveAllTempEmptiesTheRoot(t *testing.T) {
	root := t.TempDir()
	stray := filepath.Join(root, "note", "1", "transcribing", "attempt")
	if err := os.MkdirAll(stray, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "audio.webm"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAllTemp(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("root not emptied: %v %v", entries, err)
	}
}

// TestFFmpegSplitsWithoutReencoding runs the real binaries when they are on
// PATH (the worker image always has them): a 50-second tone in Opus/webm
// is cut into twenty-second pieces overlapping five seconds.
func TestFFmpegSplitsWithoutReencoding(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	src := filepath.Join(dir, "audio.webm")
	gen := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=50", "-c:a", "libopus", "-b:a", "32k", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("could not synthesize a test tone: %v %s", err, out)
	}
	media := NewFFmpeg("", "")
	total, err := media.DurationSeconds(ctx, src)
	if err != nil || math.Abs(total-50) > 1 {
		t.Fatalf("duration %v %v", total, err)
	}
	pieces, err := media.Split(ctx, src, dir, 20*time.Second, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Starts at 0, 15, 30, 45: four pieces, the last one five seconds long.
	if len(pieces) != 4 {
		t.Fatalf("pieces %v", pieces)
	}
	wantLengths := []float64{20, 20, 20, 5}
	for i, piece := range pieces {
		d, err := media.DurationSeconds(ctx, piece)
		if err != nil {
			t.Fatal(err)
		}
		// Stream copy cuts on packet boundaries, so allow a fraction of a second.
		if math.Abs(d-wantLengths[i]) > 0.5 {
			t.Fatalf("piece %d is %.2fs, want %.0fs", i, d, wantLengths[i])
		}
		if filepath.Ext(piece) != ".webm" {
			t.Fatalf("piece %s changed container", piece)
		}
	}
}

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Extractor is what the pipeline needs from yt-dlp. The real one shells out;
// tests substitute a fake, since a live YouTube in CI is a test that fails on
// a redesign rather than on a change (ADR 0013 made the same call).
type Extractor interface {
	// Metadata runs the one extraction of ticket #4. Each extraction costs
	// several requests, and YouTube answers a burst of them from one address
	// with a bot check, so no later Stage runs another.
	Metadata(ctx context.Context, videoID string) (*VideoInfo, error)
	// FetchCaptions downloads one caption track from the signed URL the
	// extraction listed: a single request, no extraction.
	FetchCaptions(ctx context.Context, format CaptionFormat) ([]byte, error)
	// Audio downloads the best Opus or M4A audio without re-encoding into dir
	// and returns the file path (used by the transcribing Stage).
	Audio(ctx context.Context, videoID, dir string) (string, error)
}

// VideoInfo is the slice of yt-dlp's JSON the pipeline reads.
type VideoInfo struct {
	ID                string                     `json:"id"`
	Title             string                     `json:"title"`
	ChannelID         string                     `json:"channel_id"`
	Channel           string                     `json:"channel"`
	Uploader          string                     `json:"uploader"`
	Duration          float64                    `json:"duration"`
	UploadDate        string                     `json:"upload_date"`
	LiveStatus        string                     `json:"live_status"`
	Availability      string                     `json:"availability"`
	AgeLimit          int                        `json:"age_limit"`
	Subtitles         map[string][]CaptionFormat `json:"subtitles"`
	AutomaticCaptions map[string][]CaptionFormat `json:"automatic_captions"`
}

// CaptionFormat is one downloadable form of a caption track.
type CaptionFormat struct {
	Ext string `json:"ext"`
	URL string `json:"url"`
}

// ChannelName prefers the channel field and falls back to the uploader.
func (v *VideoInfo) ChannelName() string {
	if v.Channel != "" {
		return v.Channel
	}
	return v.Uploader
}

// Verdict classifies a failure (ADR 0006, ADR 0009).
type Verdict string

// The three verdicts. Extractor breakage is permanent under the current
// yt-dlp pin and cleared by moving it; it is the Owner's to fix.
const (
	Transient         Verdict = "transient"
	Permanent         Verdict = "permanent"
	ExtractorBreakage Verdict = "extractor_breakage"
)

// StageError carries a classified failure with a message written for a
// person, as spec 8.4 asks.
type StageError struct {
	Verdict Verdict
	Code    string
	Message string
	Cause   error
}

func (e *StageError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s (%s): %s: %v", e.Code, e.Verdict, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Verdict, e.Message)
}

func (e *StageError) Unwrap() error { return e.Cause }

// Fail builds a StageError.
func Fail(verdict Verdict, code, message string, cause error) *StageError {
	return &StageError{Verdict: verdict, Code: code, Message: message, Cause: cause}
}

// classified is what stderr says about a failed run: the verdict, the code
// stored on the Attempt, and the sentence shown to the Owner (spec 8.4).
type classified struct {
	verdict Verdict
	code    string
	message string
}

// classifyStderr applies ticket #4's stderr rules: yt-dlp exits 1 for every
// failure, so its text is the only discriminator. A bot check is YouTube
// rate-limiting the address, seen here after a burst of extractions; it is
// transient, retried on the ladder, and never a reason to pay for speech to
// text.
func classifyStderr(stderr string) classified {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "not a bot"), strings.Contains(lower, "too many requests"), strings.Contains(lower, "http error 429"):
		return classified{Transient, "youtube_rate_limited", "YouTube asked the server's address to sign in before serving this video. It will be retried; if it keeps happening, the server needs a YouTube cookies file (runbook: YouTube's bot check)."}
	case strings.Contains(lower, "[pyi-"):
		// The exact code notes_blocked_by_tooling selects on (ADR 0009).
		return classified{ExtractorBreakage, string(ExtractorBreakage), "The extraction tool could not start on the server."}
	case strings.Contains(lower, "please report this issue"):
		return classified{ExtractorBreakage, string(ExtractorBreakage), "The video is fine; the extraction tool needs updating."}
	case strings.Contains(lower, "unable to download webpage"), strings.Contains(lower, "unable to download api page"),
		strings.Contains(lower, "connection reset"), strings.Contains(lower, "timed out"), strings.Contains(lower, "temporary failure"):
		return classified{Transient, "ytdlp_transient", "YouTube could not be reached. It will be retried."}
	default:
		return classified{Permanent, "ytdlp_permanent", "YouTube did not provide this video."}
	}
}

// ClassifyStderr is the verdict alone.
func ClassifyStderr(stderr string) Verdict { return classifyStderr(stderr).verdict }

// ytdlp is the real extractor.
type ytdlp struct {
	bin      string
	cacheDir string
	cookies  string
	once     sync.Once
	http     *http.Client
}

// YtDlpOption configures the real extractor.
type YtDlpOption func(*ytdlp)

// YtDlpCookies names a Netscape cookies file yt-dlp presents to YouTube, the
// remedy for a datacenter address YouTube meets with "Sign in to confirm
// you're not a bot". An absent or empty file means no cookies.
func YtDlpCookies(path string) YtDlpOption {
	return func(y *ytdlp) { y.cookies = path }
}

// NewYtDlp builds the real extractor. bin is the binary to run; cacheDir is
// yt-dlp's cache, persisted across runs so Deno is not re-solving player
// challenges every time.
func NewYtDlp(bin, cacheDir string, opts ...YtDlpOption) Extractor {
	if bin == "" {
		bin = "yt-dlp"
	}
	y := &ytdlp{bin: bin, cacheDir: cacheDir}
	for _, opt := range opts {
		opt(y)
	}
	return y
}

// cookiesArgs copies the operator's cookies file, when one is configured and
// non-empty, to a private per-run copy and points yt-dlp at it: the secret
// mount is read-only and yt-dlp rewrites the jar it is given, and two
// concurrent runs must not share one. cleanup removes the copy.
func (y *ytdlp) cookiesArgs() (args []string, cleanup func()) {
	cleanup = func() {}
	if y.cookies == "" {
		return nil, cleanup
	}
	data, err := os.ReadFile(y.cookies)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil, cleanup
	}
	dir := y.cacheDir
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "cookies-*.txt")
	if err != nil {
		return nil, cleanup
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, cleanup
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, cleanup
	}
	return []string{"--cookies", path}, func() { _ = os.Remove(path) }
}

// passThrough is the allowlist of environment variables yt-dlp's process
// receives: where to find Deno and its own cache, a home directory, and the
// temporary directory the PyInstaller bootstrap unpacks into (TMPDIR on
// Linux, TEMP/TMP and SYSTEMROOT on Windows for development). Nothing here
// is a secret; everything else in the worker's environment stays out.
var passThrough = []string{"PATH", "HOME", "USERPROFILE", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "LOCALAPPDATA", "APPDATA", "DENO_DIR"}

// run executes yt-dlp with an explicit, secret-free environment. The URL is
// always built from a validated Video ID and passed after "--", so nothing
// user-controlled reaches a shell (spec 15). Configuration files are ignored
// so the flags here are the whole of the behaviour. Warnings stay on stderr:
// the 429 that precedes a bot check is one.
func (y *ytdlp) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := []string{"--ignore-config", "--no-playlist", "--no-progress"}
	if y.cacheDir != "" {
		full = append(full, "--cache-dir", y.cacheDir)
	}
	cookies, cleanup := y.cookiesArgs()
	defer cleanup()
	full = append(full, cookies...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, y.bin, full...)
	cmd.Dir = dir
	env := []string{"LANG=C.UTF-8"}
	for _, name := range passThrough {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, Fail(Transient, "timeout", "The video service took too long to answer.", ctx.Err())
	}
	if err != nil {
		text := strings.TrimSpace(stderr.String())
		c := classifyStderr(text)
		return nil, Fail(c.verdict, c.code, c.message, fmt.Errorf("yt-dlp: %s", lastLine(text)))
	}
	return stdout.Bytes(), nil
}

// Metadata is the one extraction, in simulate mode: nothing is downloaded.
func (y *ytdlp) Metadata(ctx context.Context, videoID string) (*VideoInfo, error) {
	out, err := y.run(ctx, "", "-J", "--skip-download", "--ignore-no-formats-error", "--write-auto-subs", "--", CanonicalURL(videoID))
	if err != nil {
		return nil, err
	}
	var info VideoInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, Fail(Permanent, "metadata_parse", "YouTube answered with something this version cannot read.", err)
	}
	return &info, nil
}

// maxCaptionBytes bounds a caption track: a two-hour video's WebVTT is well
// under a megabyte.
const maxCaptionBytes = 8 << 20

// FetchCaptions is one GET of the signed timedtext URL the extraction
// listed, which is all yt-dlp itself would do for --write-subs.
func (y *ytdlp) FetchCaptions(ctx context.Context, format CaptionFormat) ([]byte, error) {
	u, err := url.Parse(format.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, Fail(Permanent, "captions_url", "The caption track address YouTube listed is not usable.", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, format.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", captionUserAgent)
	// The signed URL was minted by an extraction carrying the operator's
	// cookies, and YouTube answers the same address 429 when the fetch that
	// follows carries none (seen on the OCI host the first time captions
	// were listed at all). This is the only request that reaches YouTube
	// without going through yt-dlp, so it presents the cookies itself.
	for _, c := range y.cookiesFor(u.Host) {
		req.AddCookie(c)
	}
	resp, err := y.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, Fail(Transient, "timeout", "The caption track took too long to arrive.", err)
		}
		return nil, Fail(Transient, "captions_fetch", "The caption track could not be fetched. It will be retried.", err)
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do with a close error on a read body
	if resp.StatusCode != http.StatusOK {
		verdict, code := Transient, "captions_fetch"
		if resp.StatusCode == http.StatusTooManyRequests {
			code = "youtube_rate_limited"
		}
		return nil, Fail(verdict, code, "The caption track could not be fetched. It will be retried.", fmt.Errorf("timedtext: HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCaptionBytes+1))
	if err != nil {
		return nil, Fail(Transient, "captions_fetch", "The caption track could not be fetched. It will be retried.", err)
	}
	if len(body) > maxCaptionBytes {
		return nil, Fail(Permanent, "captions_too_large", "The caption track is larger than this version accepts.", nil)
	}
	return body, nil
}

// cookiesFor is the operator's cookies that apply to host, or none when no
// file is configured, it is absent, or it is empty. A file that cannot be
// read is not an error here: the fetch simply goes out bare, exactly as it
// did before a cookies file existed, and YouTube's answer decides the
// Attempt.
func (y *ytdlp) cookiesFor(host string) []*http.Cookie {
	if y.cookies == "" {
		return nil
	}
	data, err := os.ReadFile(y.cookies)
	if err != nil {
		return nil
	}
	return netscapeCookies(data, host, time.Now())
}

// netscapeCookies reads a Netscape cookies.txt -- the format every browser
// extension exports and yt-dlp's --cookies takes -- and returns the cookies
// whose domain covers host and whose expiry has not passed. Values are
// never logged; a malformed line is skipped rather than failing the file,
// since one bad row should not cost the whole jar.
func netscapeCookies(data []byte, host string, now time.Time) []*http.Cookie {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	var out []*http.Cookie
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		// "#HttpOnly_" prefixes a real row; every other comment is a comment.
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain, name, value := strings.ToLower(fields[0]), fields[5], fields[6]
		if name == "" || !domainCovers(domain, host) {
			continue
		}
		// Field 4 is the expiry as a Unix time; 0 means a session cookie,
		// which yt-dlp and browsers both keep.
		if expiry, err := strconv.ParseInt(fields[4], 10, 64); err == nil && expiry > 0 && !time.Unix(expiry, 0).After(now) {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: value})
	}
	return out
}

// domainCovers is the cookie-domain rule the file's first field spells: a
// leading dot, or the browser's implicit subdomain match, covers every
// subdomain of the name that follows.
func domainCovers(domain, host string) bool {
	domain = strings.TrimPrefix(strings.TrimSuffix(domain, "."), ".")
	if domain == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// captionUserAgent is what yt-dlp's requests carry, so the track fetch looks
// like the extraction that listed it.
const captionUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

func (y *ytdlp) client() *http.Client {
	y.once.Do(func() { y.http = &http.Client{Timeout: 60 * time.Second} })
	return y.http
}

func (y *ytdlp) Audio(ctx context.Context, videoID, dir string) (string, error) {
	// Lossless Opus in its webm container first, then m4a, without -x, so
	// nothing is re-encoded (ticket #4). The transcription API accepts webm
	// and m4a directly (ADR 0017).
	args := []string{"-f", "ba[acodec^=opus]/ba[ext=m4a]/ba", "-o", "audio.%(ext)s", "--", CanonicalURL(videoID)}
	if _, err := y.run(ctx, dir, args...); err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "audio.*"))
	if len(matches) == 0 {
		return "", Fail(Permanent, "audio_missing", "The audio could not be fetched.", errors.New("no audio written"))
	}
	sort.Strings(matches)
	return matches[0], nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 300 {
				l = l[:300]
			}
			return l
		}
	}
	return ""
}

// Eligibility applies spec 8.1 to the metadata: public standard videos,
// Shorts, and completed livestreams or premieres, reachable without an
// account and no longer than two hours.
func Eligibility(info *VideoInfo) *StageError {
	switch info.LiveStatus {
	case "is_live":
		return Fail(Permanent, "live_now", "This is a live stream. Save it once the stream has ended.", nil)
	case "is_upcoming":
		return Fail(Permanent, "scheduled", "This premiere has not happened yet. Save it after it airs.", nil)
	}
	switch info.Availability {
	case "", "public", "unlisted":
	case "private":
		return Fail(Permanent, "private", "This video is private.", nil)
	case "premium_only", "subscriber_only", "needs_auth":
		return Fail(Permanent, "restricted", "This video needs an account to watch, which the note maker does not have.", nil)
	default:
		return Fail(Permanent, "unavailable", "This video is not available.", nil)
	}
	if info.AgeLimit >= 18 {
		return Fail(Permanent, "age_restricted", "This video is age-restricted and needs a signed-in viewer.", nil)
	}
	if info.Duration > 2*60*60 {
		return Fail(Permanent, "too_long", "This video is longer than two hours. Only videos up to two hours are supported.", nil)
	}
	return nil
}

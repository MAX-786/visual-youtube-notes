package pipeline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testJar = "# Netscape HTTP Cookie File\n" +
	"# This is a generated file. Do not edit.\n" +
	"\n" +
	".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tsession-value\n" +
	"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t2000000000\tHSID\thttp-only-value\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1000000000\tEXPIRED\tstale-value\n" +
	".google.com\tTRUE\t/\tTRUE\t0\tOTHER\telsewhere\n" +
	"malformed line without tabs\n"

func TestNetscapeCookiesReadsTheJarTheBrowserExports(t *testing.T) {
	now := time.Unix(1_500_000_000, 0)
	got := map[string]string{}
	for _, c := range netscapeCookies([]byte(testJar), "www.youtube.com", now) {
		got[c.Name] = c.Value
	}
	// A session cookie (expiry 0) and an unexpired one, both from a
	// dot-domain that covers the host; the #HttpOnly_ prefix is a real row.
	if got["SID"] != "session-value" || got["HSID"] != "http-only-value" {
		t.Fatalf("expected the youtube cookies, got %v", got)
	}
	// An expired row, another site's row, comments and a malformed line.
	for _, name := range []string{"EXPIRED", "OTHER"} {
		if _, ok := got[name]; ok {
			t.Fatalf("%s should not have been sent: %v", name, got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("cookies: %v", got)
	}

	// The host must be covered by the domain, port and trailing dot aside.
	for _, host := range []string{"youtube.com", "www.youtube.com:443", "www.youtube.com."} {
		if len(netscapeCookies([]byte(testJar), host, now)) != 2 {
			t.Fatalf("%s should be covered by .youtube.com", host)
		}
	}
	for _, host := range []string{"notyoutube.com", "youtube.com.evil.test", "example.test"} {
		if n := len(netscapeCookies([]byte(testJar), host, now)); n != 0 {
			t.Fatalf("%s must not receive youtube cookies, got %d", host, n)
		}
	}
}

func TestFetchCaptionsPresentsTheCookiesAndTheBrowserUserAgent(t *testing.T) {
	var seen *http.Request
	var status int
	// TLS, because the fetcher refuses anything but https.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte("WEBVTT\n\nsomething said\n"))
	}))
	defer server.Close()

	dir := t.TempDir()
	jar := filepath.Join(dir, "youtube_cookies")
	// The test server answers on 127.0.0.1, so that is the domain the jar
	// has to cover for the request to carry anything.
	if err := os.WriteFile(jar, []byte("127.0.0.1\tFALSE\t/\tFALSE\t0\tSID\tsession-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	format := CaptionFormat{Ext: "vtt", URL: server.URL + "/api/timedtext?v=abc"}

	// Without a cookies file the fetch goes out bare, as it always did.
	bare := NewYtDlp("yt-dlp", dir).(*ytdlp)
	bare.once.Do(func() { bare.http = server.Client() })
	if _, err := bare.FetchCaptions(context.Background(), format); err != nil {
		t.Fatal(err)
	}
	if got := seen.Header.Get("Cookie"); got != "" {
		t.Fatalf("no cookies file, yet the request carried %q", got)
	}

	// With one, the cookies ride along with the browser User-Agent.
	y := NewYtDlp("yt-dlp", dir, YtDlpCookies(jar)).(*ytdlp)
	y.once.Do(func() { y.http = server.Client() })
	body, err := y.FetchCaptions(context.Background(), format)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "WEBVTT") {
		t.Fatalf("body %q", body)
	}
	if got := seen.Header.Get("Cookie"); got != "SID=session-value" {
		t.Fatalf("cookie header %q", got)
	}
	if ua := seen.Header.Get("User-Agent"); !strings.Contains(ua, "Chrome/") {
		t.Fatalf("user agent %q", ua)
	}

	// The 429 that started all this stays a transient youtube_rate_limited,
	// so the ladder retries it rather than failing the Note.
	status = http.StatusTooManyRequests
	_, err = y.FetchCaptions(context.Background(), format)
	var stage *StageError
	if !errors.As(err, &stage) || stage.Code != "youtube_rate_limited" || stage.Verdict != Transient {
		t.Fatalf("429 gave %v", err)
	}
}

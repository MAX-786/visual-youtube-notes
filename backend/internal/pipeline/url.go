package pipeline

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ErrUnsupportedURL is returned for anything that is not a YouTube video URL
// in one of the forms spec 8.1 accepts.
var ErrUnsupportedURL = errors.New("not a supported YouTube URL")

var videoIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

var acceptedHosts = map[string]bool{
	"youtube.com":       true,
	"www.youtube.com":   true,
	"m.youtube.com":     true,
	"music.youtube.com": true,
	"youtu.be":          true,
}

// VideoID extracts the canonical Video ID from any supported URL form:
// /watch?v=, youtu.be/, /shorts/, /live/, /embed/, a playlist URL carrying v,
// or a bare id. A list parameter is ignored whenever v is present.
func VideoID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if videoIDShape.MatchString(raw) {
		return raw, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrUnsupportedURL
	}
	host := strings.ToLower(u.Hostname())
	if !acceptedHosts[host] {
		return "", ErrUnsupportedURL
	}
	var id string
	switch {
	case host == "youtu.be":
		id = strings.Trim(u.Path, "/")
		if i := strings.Index(id, "/"); i >= 0 {
			id = id[:i]
		}
	case u.Path == "/watch" || u.Path == "/watch/":
		id = u.Query().Get("v")
	default:
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 {
			switch parts[0] {
			case "shorts", "live", "embed", "v":
				id = parts[1]
			}
		}
	}
	if !videoIDShape.MatchString(id) {
		return "", ErrUnsupportedURL
	}
	return id, nil
}

// CanonicalURL is the one URL form stored for a Video ID.
func CanonicalURL(videoID string) string {
	return "https://www.youtube.com/watch?v=" + videoID
}

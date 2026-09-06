package pipeline

import (
	"strings"
	"testing"
)

const rollingVTT = `WEBVTT
Kind: captions
Language: en

00:00:00.000 --> 00:00:02.000
This is a 3. It's sloppily written

00:00:02.000 --> 00:00:04.000
This is a 3. It's sloppily written
and rendered at an extremely low resolution.

00:00:04.000 --> 00:00:06.000
and rendered at an extremely low resolution.
But your brain has no trouble recognizing it.

00:00:06.000 --> 00:00:08.000
But your brain has no trouble recognizing it.
<00:00:06.500><c>I want you</c> to appreciate that.
`

func TestParseAndDedupeRollingCaptions(t *testing.T) {
	cues := ParseVTT(rollingVTT)
	if len(cues) != 4 {
		t.Fatalf("cues %d", len(cues))
	}
	lines, ratio := DedupeRolling(cues)
	joined := make([]string, 0, len(lines))
	for _, l := range lines {
		joined = append(joined, l.Text)
	}
	want := []string{
		"This is a 3. It's sloppily written",
		"and rendered at an extremely low resolution.",
		"But your brain has no trouble recognizing it.",
		"I want you to appreciate that.",
	}
	if strings.Join(joined, "|") != strings.Join(want, "|") {
		t.Fatalf("lines %q", joined)
	}
	if ratio != 0 {
		t.Fatalf("rolling repeats must not count as repeated content, got %v", ratio)
	}
	if lines[1].Start != "00:00:02.000" {
		t.Fatalf("second line should keep the cue it first appeared in, got %s", lines[1].Start)
	}
}

func TestDegenerateTrackIsUnusable(t *testing.T) {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i := 0; i < 40; i++ {
		b.WriteString("00:00:00.000 --> 00:00:01.000\n[Music]\n\nno\n\n00:00:01.000 --> 00:00:02.000\n[Music]\n\n")
	}
	n := Normalize(ParseVTT(b.String()))
	if ok, why := Usable(n, 60); ok {
		t.Fatalf("expected unusable, ratio %v words %d", n.RepeatedRatio, n.Words)
	} else if why == "" {
		t.Fatal("no reason given")
	}
}

func TestChunkingRespectsTargetsAndOverlaps(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("Sentence number ")
		b.WriteString(strings.Repeat("x", i%7+3))
		b.WriteString(" says something worth keeping about neural networks. ")
	}
	text := strings.TrimSpace(b.String())
	chunks := chunkText(text)
	if len(chunks) < 4 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.TokenCount > chunkTargetTokens+60 {
			t.Errorf("chunk %d is %d tokens", i, c.TokenCount)
		}
		if text[c.CharStart:c.CharEnd] != c.Text && strings.TrimSpace(text[c.CharStart:c.CharEnd]) != c.Text {
			t.Errorf("chunk %d offsets do not address its text", i)
		}
		if i > 0 && c.CharStart >= chunks[i-1].CharEnd {
			t.Errorf("chunk %d does not overlap its predecessor", i)
		}
		if i > 0 && c.CharStart <= chunks[i-1].CharStart {
			t.Errorf("chunk %d does not advance", i)
		}
	}
}

func TestDevanagariEstimatesDenser(t *testing.T) {
	latin := EstimateTokens("neural networks learn weights and biases from data")
	hindi := EstimateTokens("न्यूरल नेटवर्क डेटा से वेट और बायस सीखते हैं")
	if hindi <= latin/2 {
		t.Fatalf("Devanagari should estimate denser: latin %d hindi %d", latin, hindi)
	}
	if Language("en", "न्यूरल नेटवर्क डेटा से वेट और बायस सीखते हैं") != "hi" {
		t.Fatal("Devanagari text should read as Hindi")
	}
	if Language("hi-orig", "plain english words only") != "hi" {
		t.Fatal("the declared track language should win when the script is ambiguous")
	}
}

func TestChooseCaptionsFollowsSpecOrder(t *testing.T) {
	info := &VideoInfo{
		Subtitles:         map[string][]CaptionFormat{"hi": {{Ext: "vtt"}}},
		AutomaticCaptions: map[string][]CaptionFormat{"en": {{Ext: "vtt"}}, "hi-orig": {{Ext: "vtt"}}},
	}
	if c := ChooseCaptions(info); c == nil || c.Lang != "hi" || c.Automatic {
		t.Fatalf("human Hindi should beat automatic English, got %+v", c)
	}
	info.Subtitles = nil
	if c := ChooseCaptions(info); c == nil || c.Lang != "en" || !c.Automatic {
		t.Fatalf("automatic English next, got %+v", c)
	}
	info.AutomaticCaptions = map[string][]CaptionFormat{"hi-orig": {{Ext: "vtt"}}, "de": {{Ext: "vtt"}}}
	if c := ChooseCaptions(info); c == nil || c.Lang != "hi-orig" {
		t.Fatalf("a Hindi variant should be found, got %+v", c)
	}
	info.AutomaticCaptions = map[string][]CaptionFormat{"de": {{Ext: "vtt"}}}
	if c := ChooseCaptions(info); c != nil {
		t.Fatalf("no supported track should mean nil, got %+v", c)
	}
}

func TestEligibilityAndClassification(t *testing.T) {
	if Eligibility(&VideoInfo{LiveStatus: "is_live"}) == nil {
		t.Fatal("live stream accepted")
	}
	if Eligibility(&VideoInfo{Duration: 7201}) == nil {
		t.Fatal("over two hours accepted")
	}
	if Eligibility(&VideoInfo{Availability: "needs_auth"}) == nil {
		t.Fatal("account-only accepted")
	}
	if Eligibility(&VideoInfo{LiveStatus: "was_live", Duration: 3600, Availability: "public"}) != nil {
		t.Fatal("a completed livestream was rejected")
	}
	if ClassifyStderr("ERROR: [youtube] abc: Unable to extract yt initial data; please report this issue on https://github.com/yt-dlp/yt-dlp/issues") != ExtractorBreakage {
		t.Fatal("breakage not classified")
	}
	if ClassifyStderr("ERROR: Unable to download webpage: <urlopen error [Errno -3]>") != Transient {
		t.Fatal("network not transient")
	}
	if ClassifyStderr("WARNING: [youtube] abc: Unable to download webpage: HTTP Error 429: Too Many Requests\nERROR: [youtube] abc: Sign in to confirm you\u2019re not a bot.") != Transient {
		t.Fatal("bot challenge not transient")
	}
	if c := classifyStderr("ERROR: [youtube] abc: Sign in to confirm you\u2019re not a bot."); c.code != "youtube_rate_limited" {
		t.Fatalf("bot challenge code %q", c.code)
	}
}

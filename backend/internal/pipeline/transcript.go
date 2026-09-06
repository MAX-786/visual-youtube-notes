package pipeline

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// CaptionChoice is the caption track to try (spec 8.2): human English or
// Hindi, then automatic English or Hindi, then any other track whose
// language is a variant of those, such as the hi-orig track a dubbed video
// carries.
type CaptionChoice struct {
	Lang      string
	Automatic bool
}

// ChooseCaptions picks the track to try, or nil when the video has none.
func ChooseCaptions(info *VideoInfo) *CaptionChoice {
	for _, lang := range []string{"en", "hi"} {
		if key := matchTrack(info.Subtitles, lang, true); key != "" {
			return &CaptionChoice{Lang: key}
		}
	}
	for _, lang := range []string{"en", "hi"} {
		if key := matchTrack(info.AutomaticCaptions, lang, true); key != "" {
			return &CaptionChoice{Lang: key, Automatic: true}
		}
	}
	for _, lang := range []string{"hi", "en"} {
		if key := matchTrack(info.Subtitles, lang, false); key != "" {
			return &CaptionChoice{Lang: key}
		}
		if key := matchTrack(info.AutomaticCaptions, lang, false); key != "" {
			return &CaptionChoice{Lang: key, Automatic: true}
		}
	}
	return nil
}

func matchTrack(tracks map[string][]CaptionFormat, lang string, exact bool) string {
	if tracks == nil {
		return ""
	}
	if exact {
		if _, ok := tracks[lang]; ok {
			return lang
		}
		return ""
	}
	best := ""
	for key := range tracks {
		if strings.HasPrefix(key, lang+"-") && (best == "" || key < best) {
			best = key
		}
	}
	return best
}

// Cue is one WebVTT cue after inline markup is stripped.
type Cue struct {
	Start string
	Lines []string
}

var (
	inlineTiming = regexp.MustCompile(`<\d{2}:\d{2}:\d{2}\.\d{3}>`)
	inlineTags   = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	spaces       = regexp.MustCompile(`\s+`)
)

// ParseVTT reads cues from a WebVTT document.
func ParseVTT(raw string) []Cue {
	raw = strings.ReplaceAll(raw, "\r", "")
	var cues []Cue
	for _, block := range regexp.MustCompile(`\n{2,}`).Split(raw, -1) {
		lines := strings.Split(block, "\n")
		timing := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue
		}
		start := strings.TrimSpace(strings.SplitN(lines[timing], "-->", 2)[0])
		if i := strings.Index(start, " "); i >= 0 {
			start = start[:i]
		}
		cue := Cue{Start: start}
		for _, line := range lines[timing+1:] {
			text := strings.TrimSpace(inlineTags.ReplaceAllString(inlineTiming.ReplaceAllString(line, ""), ""))
			if text != "" {
				cue.Lines = append(cue.Lines, text)
			}
		}
		if len(cue.Lines) > 0 {
			cues = append(cues, cue)
		}
	}
	return cues
}

// Line is one caption line with the cue it first appeared in.
type Line struct {
	Text  string
	Start string
}

// Normalized is a Transcript ready to store: the text, its chunks, and the
// numbers the usability rule needs.
type Normalized struct {
	Text          string
	Chunks        []Chunk
	Words         int
	RepeatedRatio float64
	Cues          int
}

// Chunk is a slice of the Transcript sized for embedding (spec 10).
type Chunk struct {
	Ordinal    int
	CharStart  int
	CharEnd    int
	TokenCount int
	Text       string
}

// DedupeRolling keeps the first occurrence of each line where YouTube's
// rolling captions re-emit it as the window scrolls, and reports how much of
// what remains is still a repeat of an earlier line: the degenerate "[Music]"
// tracks the 40% rule was written for (ticket #15's measurement).
func DedupeRolling(cues []Cue) (lines []Line, repeatedRatio float64) {
	seen := map[string]int{}
	repeated := 0
	for _, cue := range cues {
		for _, text := range cue.Lines {
			if len(lines) > 0 && lines[len(lines)-1].Text == text {
				continue
			}
			key := strings.ToLower(spaces.ReplaceAllString(text, " "))
			if seen[key] > 0 {
				repeated++
			}
			seen[key]++
			lines = append(lines, Line{Text: text, Start: cue.Start})
		}
	}
	if len(lines) == 0 {
		return nil, 0
	}
	return lines, float64(repeated) / float64(len(lines))
}

// Usable applies spec 8.2's rule: at least max(30, 10 x minutes) words and
// repeated-line content below 40% after rolling-caption normalization.
func Usable(n *Normalized, durationSeconds float64) (bool, string) {
	minWords := int(math.Max(30, 10*durationSeconds/60))
	if n.Words < minWords {
		return false, "too few words"
	}
	if n.RepeatedRatio >= 0.4 {
		return false, "mostly repeated lines"
	}
	return true, ""
}

// chunk targets from spec 10: about 500 embedding tokens with a 75-token
// overlap, preferring sentence boundaries.
const (
	chunkTargetTokens  = 500
	chunkOverlapTokens = 75
)

// EstimateTokens is the script-aware estimate of docs/design/cost-tracking.md:
// Latin runs at about 3.8 characters per token, Devanagari far denser. The
// vendor's own count replaces it once a call has been made.
func EstimateTokens(text string) int {
	var latin, devanagari, other float64
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Devanagari, r):
			devanagari++
		case r < 0x250:
			latin++
		default:
			other++
		}
	}
	tokens := latin/3.8 + devanagari/1.3 + other/2.0
	return int(math.Ceil(tokens))
}

var sentenceEnd = regexp.MustCompile(`([.!?।]+["')\]]?)(\s+)`)

// Normalize turns cues into the stored Transcript and its chunks.
func Normalize(cues []Cue) *Normalized {
	lines, ratio := DedupeRolling(cues)
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, l.Text)
	}
	text := strings.TrimSpace(spaces.ReplaceAllString(strings.Join(parts, " "), " "))
	n := &Normalized{Text: text, RepeatedRatio: ratio, Cues: len(cues)}
	if text == "" {
		return n
	}
	n.Words = len(strings.Fields(text))
	n.Chunks = chunkText(text)
	return n
}

// chunkText splits on sentence boundaries into ~500-token chunks with a
// ~75-token overlap carried from the tail of the previous chunk.
func chunkText(text string) []Chunk {
	type sentence struct{ start, end int }
	var sentences []sentence
	last := 0
	for _, m := range sentenceEnd.FindAllStringSubmatchIndex(text, -1) {
		end := m[3]
		sentences = append(sentences, sentence{last, end})
		last = m[5]
	}
	if last < len(text) {
		sentences = append(sentences, sentence{last, len(text)})
	}

	var chunks []Chunk
	i := 0
	for i < len(sentences) {
		start := sentences[i].start
		end := start
		tokens := 0
		j := i
		for j < len(sentences) {
			t := EstimateTokens(text[sentences[j].start:sentences[j].end])
			if tokens > 0 && tokens+t > chunkTargetTokens {
				break
			}
			tokens += t
			end = sentences[j].end
			j++
		}
		chunkStr := strings.TrimSpace(text[start:end])
		if chunkStr != "" {
			chunks = append(chunks, Chunk{Ordinal: len(chunks), CharStart: start, CharEnd: end, TokenCount: EstimateTokens(chunkStr), Text: chunkStr})
		}
		if j >= len(sentences) {
			break
		}
		// Overlap: step back over trailing sentences worth about 75 tokens,
		// but always advance by at least one sentence.
		back := j
		overlap := 0
		for back > i+1 && overlap < chunkOverlapTokens {
			back--
			overlap += EstimateTokens(text[sentences[back].start:sentences[back].end])
		}
		if back <= i {
			back = i + 1
		}
		i = back
	}
	return chunks
}

// Language guesses the Transcript's language code from its script and the
// track it came from. Hinglish is Devanagari carrying English loanwords, so
// script alone cannot tell it from Hindi; the note model decides that, and
// this only records what the track declared.
func Language(trackLang, text string) string {
	var devanagari, letters int
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.Is(unicode.Devanagari, r) {
				devanagari++
			}
		}
	}
	if letters > 0 && float64(devanagari)/float64(letters) > 0.3 {
		return "hi"
	}
	if strings.HasPrefix(trackLang, "hi") {
		return "hi"
	}
	return "en"
}

// TrackFormat is the WebVTT form of the chosen track, or the first form
// listed when there is none; nil when the track has no fetchable form.
func (v *VideoInfo) TrackFormat(choice *CaptionChoice) *CaptionFormat {
	tracks := v.Subtitles
	if choice.Automatic {
		tracks = v.AutomaticCaptions
	}
	formats := tracks[choice.Lang]
	for i := range formats {
		if formats[i].Ext == "vtt" && formats[i].URL != "" {
			return &formats[i]
		}
	}
	for i := range formats {
		if formats[i].URL != "" {
			return &formats[i]
		}
	}
	return nil
}

package pipeline

import "strings"

// Chunk transcriptions overlap by fifteen seconds of audio (spec 8.2). The
// words spoken in that window appear at the tail of one piece and the head
// of the next, and removing them must be deterministic: the same texts
// always join the same way.
//
// The rule: among the last overlapWindow words of the earlier piece and
// the first overlapWindow words of the later one, find the longest run of
// identical consecutive words (compared without punctuation and case). If
// the run has at least minOverlapWords words, the later piece continues
// from just after that run; otherwise nothing is removed. Ties go to the
// earliest position in the later piece, so a repeated phrase cannot swallow
// more than the overlap.
const (
	overlapWindow   = 80
	minOverlapWords = 4
)

// MergeTranscriptions joins the chunk texts in order with overlap removed.
func MergeTranscriptions(parts []string) string {
	var out []string
	for _, part := range parts {
		words := strings.Fields(part)
		if len(words) == 0 {
			continue
		}
		if len(out) > 0 {
			words = words[overlapCut(out, words):]
		}
		out = append(out, words...)
	}
	return strings.Join(out, " ")
}

// overlapCut returns how many words to drop from the head of next given
// what has been merged so far.
func overlapCut(merged, next []string) int {
	tail := merged[max(0, len(merged)-overlapWindow):]
	head := next[:min(len(next), overlapWindow)]
	bestLen, bestEnd := 0, 0
	for i := range head {
		for j := range tail {
			n := 0
			for i+n < len(head) && j+n < len(tail) && sameWord(head[i+n], tail[j+n]) {
				n++
			}
			// Only a run reaching the end of the tail is an overlap: the
			// earlier piece ends inside the shared window.
			if j+n == len(tail) && n > bestLen {
				bestLen, bestEnd = n, i+n
			}
		}
	}
	if bestLen < minOverlapWords {
		return 0
	}
	return bestEnd
}

func sameWord(a, b string) bool {
	return foldWord(a) == foldWord(b)
}

func foldWord(w string) string {
	return strings.ToLower(strings.Trim(w, ".,;:!?\"'()[]{}‘’“”।"))
}

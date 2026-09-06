package pipeline

import (
	"strings"
	"testing"
)

func TestMergeTranscriptionsRemovesTheOverlapDeterministically(t *testing.T) {
	first := "The network has seven hundred eighty four neurons in the first layer, one for each pixel of the image."
	second := "one for each pixel of the image. Each neuron holds a number between zero and one called its activation."
	third := "one called its activation. The last layer has ten neurons, one per digit."
	got := MergeTranscriptions([]string{first, second, third})
	want := "The network has seven hundred eighty four neurons in the first layer, one for each pixel of the image. Each neuron holds a number between zero and one called its activation. The last layer has ten neurons, one per digit."
	if got != want {
		t.Fatalf("merged:\n%s\nwant:\n%s", got, want)
	}
	// Running it again on the same input is the same output, and a merge
	// of the merge changes nothing.
	if again := MergeTranscriptions([]string{first, second, third}); again != got {
		t.Fatal("merge is not deterministic")
	}
}

func TestMergeTranscriptionsIgnoresCaseAndPunctuationInTheOverlap(t *testing.T) {
	first := "We train the model on labelled examples, adjusting weights and biases"
	second := "Adjusting weights, and biases, until the cost is small."
	got := MergeTranscriptions([]string{first, second})
	want := "We train the model on labelled examples, adjusting weights and biases until the cost is small."
	if got != want {
		t.Fatalf("merged: %s", got)
	}
}

func TestMergeTranscriptionsKeepsShortCoincidencesAndDevanagari(t *testing.T) {
	// Three shared words are a coincidence, not an overlap.
	first := "the model learns and the"
	second := "and the model predicts digits"
	if got := MergeTranscriptions([]string{first, second}); got != first+" "+second {
		t.Fatalf("short run removed: %s", got)
	}
	// Hindi pieces merge by their words and keep their script.
	hindiFirst := "यह एक तीन है यह लापरवाही से लिखा गया है सिर्फ अट्ठाईस पिक्सेल के रेजोल्यूशन"
	hindiSecond := "सिर्फ अट्ठाईस पिक्सेल के रेजोल्यूशन लेकिन आपके दिमाग को इसे पहचानने में कोई परेशानी नहीं होती।"
	got := MergeTranscriptions([]string{hindiFirst, hindiSecond})
	if strings.Count(got, "रेजोल्यूशन") != 1 || !strings.HasSuffix(got, "नहीं होती।") {
		t.Fatalf("hindi merge: %s", got)
	}
}

func TestMergeTranscriptionsSkipsEmptyPieces(t *testing.T) {
	if got := MergeTranscriptions([]string{"", "  ", "only words here"}); got != "only words here" {
		t.Fatalf("got %q", got)
	}
	if got := MergeTranscriptions(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

package ai

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
)

// The Processing roles of spec 11, in the order Settings shows them.
const (
	RoleNoteGeneration = "note_generation"
	RoleEmbeddings     = "embeddings"
	RoleTranscription  = "transcription"
	RoleVisualAnalysis = "visual_analysis"
)

// Roles is every role, active or not.
var Roles = []string{RoleNoteGeneration, RoleEmbeddings, RoleTranscription, RoleVisualAnalysis}

// IsRole reports whether s names a role.
func IsRole(s string) bool { return slices.Contains(Roles, s) }

// ProviderOpenAI is the one provider this build speaks (ADR 0017).
const ProviderOpenAI = "openai"

// ErrProviderUnsupported is returned for any other provider name.
var ErrProviderUnsupported = errors.New("provider not supported by this build")

// OptionError names the request option that failed validation.
type OptionError struct {
	Key    string
	Reason string
}

func (e *OptionError) Error() string { return "request option " + e.Key + ": " + e.Reason }

var (
	reasoningEfforts = []string{"none", "low", "medium", "high", "xhigh", "max"}
	verbosities      = []string{"low", "medium", "high"}
	languageCode     = regexp.MustCompile(`^[a-z]{2,3}$`)
)

// ValidateOptions is the per-provider validator ADR 0010 asks for: it runs
// on save, where the Owner is present to be told, and the options are then
// passed through unchanged at call time. Unknown keys are refused rather
// than forwarded, since a mistyped option would otherwise fail every Note.
// embeddingDimension is what the search index stores; the embeddings
// dimension must equal it or every indexing Stage would fail.
func ValidateOptions(provider, role string, opts map[string]any, embeddingDimension int) error {
	if provider != ProviderOpenAI {
		return ErrProviderUnsupported
	}
	for key, value := range opts {
		var err error
		switch role {
		case RoleNoteGeneration:
			err = validateGeneration(key, value)
		case RoleTranscription:
			err = validateTranscription(key, value)
		case RoleEmbeddings:
			err = validateEmbeddings(key, value, embeddingDimension)
		default:
			err = &OptionError{key, "this role takes no options"}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateGeneration(key string, value any) error {
	switch key {
	case "reasoning_effort":
		return oneOf(key, value, reasoningEfforts)
	case "verbosity":
		return oneOf(key, value, verbosities)
	case "max_output_tokens":
		return integerBetween(key, value, 1, 400000)
	case "temperature":
		return numberBetween(key, value, 0, 2)
	default:
		return &OptionError{key, "unknown option for note generation"}
	}
}

func validateTranscription(key string, value any) error {
	switch key {
	case "languages":
		list, ok := value.([]any)
		if !ok || len(list) == 0 || len(list) > 8 {
			return &OptionError{key, "must be a list of one to eight language codes"}
		}
		for _, item := range list {
			code, ok := item.(string)
			if !ok || !languageCode.MatchString(code) {
				return &OptionError{key, "each entry must be a two- or three-letter language code"}
			}
		}
		return nil
	case "language":
		code, ok := value.(string)
		if !ok || !languageCode.MatchString(code) {
			return &OptionError{key, "must be a two- or three-letter language code"}
		}
		return nil
	case "prompt":
		text, ok := value.(string)
		if !ok || len(text) > 2000 {
			return &OptionError{key, "must be text of at most 2000 characters"}
		}
		return nil
	case "temperature":
		return numberBetween(key, value, 0, 1)
	default:
		return &OptionError{key, "unknown option for transcription"}
	}
}

func validateEmbeddings(key string, value any, dimension int) error {
	switch key {
	case "dimensions":
		if err := integerBetween(key, value, 1, 3072); err != nil {
			return err
		}
		if int(value.(float64)) != dimension {
			return &OptionError{key, fmt.Sprintf("must be %d to match the search index", dimension)}
		}
		return nil
	default:
		return &OptionError{key, "unknown option for embeddings"}
	}
}

func oneOf(key string, value any, allowed []string) error {
	s, ok := value.(string)
	if !ok || !slices.Contains(allowed, s) {
		return &OptionError{key, "must be one of " + fmt.Sprint(allowed)}
	}
	return nil
}

func integerBetween(key string, value any, lo, hi float64) error {
	f, ok := value.(float64)
	if !ok || f != math.Trunc(f) || f < lo || f > hi {
		return &OptionError{key, fmt.Sprintf("must be a whole number from %v to %v", lo, hi)}
	}
	return nil
}

func numberBetween(key string, value any, lo, hi float64) error {
	f, ok := value.(float64)
	if !ok || f < lo || f > hi {
		return &OptionError{key, fmt.Sprintf("must be a number from %v to %v", lo, hi)}
	}
	return nil
}

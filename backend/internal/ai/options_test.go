package ai

import (
	"errors"
	"testing"
)

func TestValidateOptions(t *testing.T) {
	ok := []struct {
		role string
		opts map[string]any
	}{
		{RoleNoteGeneration, map[string]any{"reasoning_effort": "low", "max_output_tokens": float64(16000)}},
		{RoleNoteGeneration, map[string]any{}},
		{RoleTranscription, map[string]any{"languages": []any{"en", "hi"}}},
		{RoleTranscription, map[string]any{"language": "hi", "prompt": "Devanagari with English loanwords"}},
		{RoleEmbeddings, map[string]any{"dimensions": float64(1024)}},
		{RoleVisualAnalysis, map[string]any{}},
	}
	for _, c := range ok {
		if err := ValidateOptions(ProviderOpenAI, c.role, c.opts, 1024); err != nil {
			t.Errorf("%s %v: %v", c.role, c.opts, err)
		}
	}
	bad := []struct {
		role string
		opts map[string]any
		key  string
	}{
		{RoleNoteGeneration, map[string]any{"reasoning_effort": "extreme"}, "reasoning_effort"},
		{RoleNoteGeneration, map[string]any{"max_output_tokens": 15.5}, "max_output_tokens"},
		{RoleNoteGeneration, map[string]any{"budget_tokens": float64(1000)}, "budget_tokens"},
		{RoleTranscription, map[string]any{"languages": []any{"english"}}, "languages"},
		{RoleTranscription, map[string]any{"languages": "en"}, "languages"},
		{RoleEmbeddings, map[string]any{"dimensions": float64(512)}, "dimensions"},
		{RoleVisualAnalysis, map[string]any{"detail": "high"}, "detail"},
	}
	for _, c := range bad {
		var oe *OptionError
		err := ValidateOptions(ProviderOpenAI, c.role, c.opts, 1024)
		if !errors.As(err, &oe) || oe.Key != c.key {
			t.Errorf("%s %v: expected an error on %s, got %v", c.role, c.opts, c.key, err)
		}
	}
	if err := ValidateOptions("anthropic", RoleNoteGeneration, map[string]any{}, 1024); !errors.Is(err, ErrProviderUnsupported) {
		t.Errorf("other provider: %v", err)
	}
}

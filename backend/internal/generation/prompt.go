package generation

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The frozen prefix: the system prompt and the request schema, copied from
// docs/design/generation by go generate and asserted equal by a test so
// the two cannot drift.

//go:generate go run copy_assets.go

// SystemPrompt is the frozen system prompt every generating call sends.
//
//go:embed assets/system-prompt.md
var SystemPrompt string

//go:embed assets/note-request.schema.json
var requestSchema []byte

// SchemaName is the name the strict json_schema format carries.
const SchemaName = "visual_note"

// RequestSchema is the decode-safe schema as sent to OpenAI's Responses API
// in strict mode: minItems stripped (the counts were always the validator's
// job), every property required, the properties the design left optional
// made nullable, which is how strict mode spells optional (ADR 0017), and
// every node carrying the type strict mode insists on: a const becomes a
// one-value enum, and an enum without a type gets the type of its values.
// The first live call found the diagram's {"const": "mind_map"} refused for
// exactly that.
func RequestSchema() json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(requestSchema, &doc); err != nil {
		panic("request schema: " + err.Error())
	}
	delete(doc, "$schema")
	delete(doc, "$id")
	delete(doc, "title")
	delete(doc, "description")
	strict(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		panic("request schema: " + err.Error())
	}
	return out
}

// strict rewrites one schema node in place.
func strict(node map[string]any) {
	delete(node, "minItems")
	inferType(node)
	if props, ok := node["properties"].(map[string]any); ok {
		required := map[string]bool{}
		if list, ok := node["required"].([]any); ok {
			for _, r := range list {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		all := make([]any, 0, len(names))
		for _, name := range names {
			all = append(all, name)
			if !required[name] {
				props[name] = nullable(props[name])
			}
		}
		node["required"] = all
		node["additionalProperties"] = false
	}
	for _, key := range []string{"properties", "$defs"} {
		if m, ok := node[key].(map[string]any); ok {
			for _, v := range m {
				if child, ok := v.(map[string]any); ok {
					strict(child)
				}
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		strict(items)
	}
	if list, ok := node["anyOf"].([]any); ok {
		for _, v := range list {
			if child, ok := v.(map[string]any); ok {
				strict(child)
			}
		}
	}
}

// inferType gives a const or enum node the type strict mode insists on,
// derived from its values, spelling a const as a one-member enum on the
// way (strict mode knows enum; const is not on its list).
func inferType(node map[string]any) {
	if c, ok := node["const"]; ok {
		delete(node, "const")
		node["enum"] = []any{c}
	}
	if _, ok := node["type"]; ok {
		return
	}
	enum, ok := node["enum"].([]any)
	if !ok || len(enum) == 0 {
		return
	}
	var kinds []string
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			kinds = append(kinds, k)
		}
	}
	for _, v := range enum {
		switch x := v.(type) {
		case string:
			add("string")
		case float64:
			if x == float64(int64(x)) {
				add("integer")
			} else {
				add("number")
			}
		case bool:
			add("boolean")
		case nil:
			add("null")
		}
	}
	if len(kinds) == 1 {
		node["type"] = kinds[0]
		return
	}
	list := make([]any, 0, len(kinds))
	for _, k := range kinds {
		list = append(list, k)
	}
	node["type"] = list
}

// nullable lets a property be null, which is strict mode's optional. An
// enum admits null explicitly as well, since a nullable type alone would
// leave null outside the values the enum allows.
func nullable(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	if c, ok := m["const"]; ok {
		delete(m, "const")
		m["enum"] = []any{c}
	}
	if enum, ok := m["enum"].([]any); ok {
		hasNull := false
		for _, v := range enum {
			if v == nil {
				hasNull = true
			}
		}
		if !hasNull {
			m["enum"] = append(enum, nil)
		}
	}
	if t, ok := m["type"].(string); ok {
		m["type"] = []any{t, "null"}
		return m
	}
	if _, ok := m["enum"]; ok {
		// The type, null included, is inferred from the values by strict().
		return m
	}
	return map[string]any{"anyOf": []any{m, map[string]any{"type": "null"}}}
}

// Meta is what the request says about the video.
type Meta struct {
	Title    string
	Channel  string
	Duration int
}

// RenderInput is the user message: metadata, an optional instruction, and
// the transcript as handled chunks (build-request.mjs, renderUserMessage).
func RenderInput(chunks []Chunk, meta Meta, instruction string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Video: %s\nChannel: %s\nDuration: %d minutes\n", meta.Title, meta.Channel, (meta.Duration+30)/60)
	if strings.TrimSpace(instruction) != "" {
		fmt.Fprintf(&b, "\nThe person saving this note asked: %s\n", strings.TrimSpace(instruction))
	}
	b.WriteString("\nTranscript, split into chunks. Cite a chunk by the handle in brackets.\n\n")
	for i, c := range chunks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[%s]\n%s", Handle(c.Ordinal), c.Text)
	}
	return b.String()
}

// RenderReAsk is the input of the single re-ask: the original input, the
// previous answer, and the validator's own messages.
func RenderReAsk(input, previousJSON string, feedback []string) string {
	var b strings.Builder
	b.WriteString(input)
	b.WriteString("\n\nA previous answer to this request was:\n")
	b.WriteString(previousJSON)
	b.WriteString("\n\nIt failed these checks. Return a corrected, complete note that passes every one of them:\n")
	for _, f := range feedback {
		b.WriteString("- " + f + "\n")
	}
	return b.String()
}

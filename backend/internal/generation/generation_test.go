package generation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const fixtures = "../../../docs/design/generation/fixtures"

type transcriptFixture struct {
	Chunks []struct {
		ChunkID string `json:"chunk_id"`
		Index   int    `json:"index"`
		Text    string `json:"text"`
	} `json:"chunks"`
}

func loadFixture(t *testing.T) ([]Chunk, *Note, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, "en-neural-network.transcript.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tf transcriptFixture
	if err := json.Unmarshal(raw, &tf); err != nil {
		t.Fatal(err)
	}
	chunks := make([]Chunk, 0, len(tf.Chunks))
	for _, c := range tf.Chunks {
		chunks = append(chunks, Chunk{ID: uuid.MustParse(c.ChunkID), Ordinal: c.Index, Text: c.Text})
	}
	noteRaw, err := os.ReadFile(filepath.Join(fixtures, "en-neural-network.note-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var note Note
	if err := json.Unmarshal(noteRaw, &note); err != nil {
		t.Fatal(err)
	}
	return chunks, &note, noteRaw
}

func TestValidFixtureHasNoFindingsAndStores(t *testing.T) {
	chunks, note, _ := loadFixture(t)
	if findings := Validate(note, chunks); len(findings) != 0 {
		t.Fatalf("findings on the valid fixture: %v", findings)
	}
	stored := ToStored(note, chunks)
	if stored.SchemaVersion != 1 || stored.Diagram.SchemaVersion != 1 || stored.Summary.Provenance != "ai" {
		t.Fatalf("stored stamps missing: %+v", stored.Summary)
	}
	raw, _ := json.Marshal(stored)
	if n := strings.Count(string(raw), `"char_start"`); n < 30 {
		t.Fatalf("only %d source refs resolved", n)
	}
	first := stored.Summary.SourceRefs[0]
	// The summary's first quote sits in chunk c2 at a real span.
	if first.ChunkID != chunks[1].ID || first.CharEnd <= first.CharStart {
		t.Fatalf("first ref %+v", first)
	}
	text := []rune(chunks[1].Text)
	if got := string(text[first.CharStart:first.CharEnd]); got != "put together a neural network that can learn to recognize handwritten digits" {
		t.Fatalf("span reads %q", got)
	}
	if gl, ok := stored.Diagram.Layout.(GraphLayout); !ok || gl.Engine == "" {
		t.Fatalf("layout engine missing: %#v", stored.Diagram.Layout)
	}
	for _, n := range stored.Diagram.Nodes {
		if !n.InitiallyVisible && n.GroupID == "" {
			t.Fatalf("hidden node %s without a group", n.ID)
		}
	}
	if alt := TextAlternative(&stored.Diagram); !strings.Contains(alt, "\n") {
		t.Fatalf("text alternative too short: %q", alt)
	}
}

func TestHallucinatedQuoteIsProvenAndNotRepairable(t *testing.T) {
	chunks, note, _ := loadFixture(t)
	note.KeyPoints[0].SourceRefs[0].Quote = "the moon is made of cheese"
	findings := Validate(note, chunks)
	if len(findings) != 1 || findings[0].Code != "quote_not_found" || findings[0].Repairable || findings[0].Path != "key_points[0]" {
		t.Fatalf("findings %v", findings)
	}
	cycle := Cycle(note, chunks, 0, 1)
	if cycle.Outcome != ReAsk || len(cycle.Feedback) != 1 || !strings.Contains(cycle.Feedback[0], "quote not present in c3") {
		t.Fatalf("first cycle %+v", cycle)
	}
	if again := Cycle(note, chunks, 1, 1); again.Outcome != Failed {
		t.Fatalf("second cycle should fail, got %s", again.Outcome)
	}
}

func TestRepairsAreFreeAndContentPreserving(t *testing.T) {
	chunks, note, _ := loadFixture(t)
	// Six practical actions: one over the cap, truncated.
	extra := note.KeyPoints[0]
	extra.ID = "pa_extra"
	for len(note.PracticalActions) < 6 {
		note.PracticalActions = append(note.PracticalActions, extra)
	}
	// A malformed id, a duplicate id, and orders out of sequence.
	nodes := note.Diagram.elements()
	orig := nodes[1].ID
	nodes[1].ID = "bad id!"
	edgesBefore := len(note.Diagram.Edges)
	for i := range note.Diagram.Edges {
		if note.Diagram.Edges[i].To == orig {
			note.Diagram.Edges[i].To = "bad id!"
		}
		if note.Diagram.Edges[i].From == orig {
			note.Diagram.Edges[i].From = "bad id!"
		}
	}
	findings := Validate(note, chunks)
	codes := map[string]bool{}
	for _, f := range findings {
		codes[f.Code] = true
		if f.Code == "practical_actions_too_many" && !f.Repairable {
			t.Fatal("truncation should be repairable")
		}
	}
	if !codes["practical_actions_too_many"] || !codes["bad_element_id"] {
		t.Fatalf("expected repairable findings, got %v", findings)
	}
	repaired, applied := Repair(note, findings)
	if len(applied) < 2 {
		t.Fatalf("applied %v", applied)
	}
	if len(repaired.PracticalActions) != 5 || len(note.PracticalActions) != 6 {
		t.Fatal("truncation must not touch the input")
	}
	if len(repaired.Diagram.Edges) != edgesBefore {
		t.Fatal("repair changed the edge count")
	}
	for _, n := range repaired.Diagram.elements() {
		if !idShape.MatchString(n.ID) {
			t.Fatalf("id %q still malformed", n.ID)
		}
	}
	// Edges follow the rename: the mind map must still be a tree.
	after := Validate(repaired, chunks)
	for _, f := range after {
		if f.Code == "mind_map_not_tree" || f.Code == "edge_endpoint_missing" || f.Code == "bad_element_id" {
			t.Fatalf("repair left %v", f)
		}
	}
}

func TestOverflowCollapsesBehindAnAnchor(t *testing.T) {
	chunks, note, _ := loadFixture(t)
	root := note.Diagram.Nodes[0]
	for _, n := range note.Diagram.Nodes {
		if n.Kind == "root" {
			root = n
		}
	}
	// Add leaves until 16 are visible.
	for i := 0; len(note.Diagram.Nodes) < 16; i++ {
		leaf := note.Diagram.Nodes[1]
		leaf.ID = "extra_" + string(rune('a'+i))
		leaf.Kind = "extra"
		note.Diagram.Nodes = append(note.Diagram.Nodes, leaf)
		note.Diagram.Edges = append(note.Diagram.Edges, Edge{ID: "edge_" + leaf.ID, From: root.ID, To: leaf.ID})
	}
	findings := Validate(note, chunks)
	var visibleFinding *Finding
	for i := range findings {
		if findings[i].Code == "too_many_visible" {
			visibleFinding = &findings[i]
		}
	}
	if visibleFinding == nil || !visibleFinding.Repairable {
		t.Fatalf("expected a repairable overflow, got %v", findings)
	}
	repaired, applied := Repair(note, findings)
	if len(applied) == 0 || len(repaired.Diagram.CollapsedGroups) == 0 {
		t.Fatalf("no collapse applied: %v", applied)
	}
	if after := Validate(repaired, chunks); len(after) != 0 {
		t.Fatalf("collapse left findings: %v", after)
	}
	stored := ToStored(repaired, chunks)
	visible := 0
	for _, n := range stored.Diagram.Nodes {
		if n.InitiallyVisible {
			visible++
		}
	}
	if visible != Limits.VisibleElements {
		t.Fatalf("%d visible after collapse", visible)
	}
	positions := Layout(&stored.Diagram, nil)
	if len(positions) != len(stored.Diagram.Nodes) {
		t.Fatalf("%d positions for %d nodes", len(positions), len(stored.Diagram.Nodes))
	}
}

func TestTextAlternativeFollowsTheSemanticsDoc(t *testing.T) {
	chunks, note, _ := loadFixture(t)
	stored := ToStored(note, chunks)
	alt := TextAlternative(&stored.Diagram)
	lines := strings.Split(alt, "\n")
	if len(lines) != len(stored.Diagram.Nodes) {
		t.Fatalf("%d lines for %d nodes:\n%s", len(lines), len(stored.Diagram.Nodes), alt)
	}
	if strings.HasPrefix(lines[0], " ") || !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("indentation wrong:\n%s", alt)
	}
	one := 1
	comparison := &StoredDiagram{Type: Comparison,
		Columns: []StoredColumn{{ID: "b", Label: "B", Order: 1}, {ID: "a", Label: "A", Order: 0}},
		Rows:    []StoredRow{{ID: "r", Label: "Speed", Order: 0, SpeakerClaim: true, Cells: map[string]StoredCell{"a": {Text: "fast"}, "b": {Text: "slow"}}}}}
	if got := TextAlternative(comparison); got != "Criterion | A | B\nSpeaker claim: Speed | fast | slow" {
		t.Fatalf("comparison text %q", got)
	}
	timeline := &StoredDiagram{Type: Timeline, Events: []StoredElement{{ID: "y", Label: "Second", Order: &one}, {ID: "x", Label: "First", Order: new(int)}}}
	if got := TextAlternative(timeline); got != "1. First\n2. Second" {
		t.Fatalf("timeline text %q", got)
	}
}

func TestLayoutsGiveEveryNodeABoxWithoutOverlap(t *testing.T) {
	chunks, note, _ := loadFixture(t)
	stored := ToStored(note, chunks)
	positions := Layout(&stored.Diagram, nil)
	if len(positions) != len(stored.Diagram.Nodes) {
		t.Fatalf("%d positions for %d nodes", len(positions), len(stored.Diagram.Nodes))
	}
	assertNoOverlap(t, positions)
	// Existing positions are kept: only the missing ones come back.
	first := stored.Diagram.Nodes[0].ID
	partial := Layout(&stored.Diagram, map[string]Position{first: {X: 1, Y: 2, W: 3, H: 4}})
	if _, ok := partial[first]; ok || len(partial) != len(positions)-1 {
		t.Fatalf("partial layout returned %d positions", len(partial))
	}

	zero, one, two, three := 0, 1, 2, 3
	flow := &StoredDiagram{Type: ProcessFlow, Layout: GraphLayout{Engine: "layered"},
		Nodes: []StoredElement{{ID: "a", Label: "Collect the data", Order: &zero}, {ID: "b", Label: "Clean it", Order: &one}, {ID: "c", Label: "Train the model on the cleaned data set", Order: &two}, {ID: "d", Label: "Evaluate", Order: &three}},
		Edges: []StoredEdge{{ID: "e1", From: "a", To: "b"}, {ID: "e2", From: "b", To: "c"}, {ID: "e3", From: "b", To: "d"}, {ID: "e4", From: "d", To: "a"}}}
	fp := Layout(flow, nil)
	if len(fp) != 4 {
		t.Fatalf("flow positions %v", fp)
	}
	if !(fp["a"].Y < fp["b"].Y && fp["b"].Y < fp["c"].Y && fp["c"].Y == fp["d"].Y) {
		t.Fatalf("layers wrong: %v", fp)
	}
	assertNoOverlap(t, fp)
	if w, h := Measure("Train the model on the cleaned data set"); w > wrapWidth+2*padX+1 || h < 2*lineHeight {
		t.Fatalf("long label should wrap: %v x %v", w, h)
	}
	if TextWidth("iii") >= TextWidth("mmm") {
		t.Fatal("measurement is not proportional; the font did not load")
	}
}

func assertNoOverlap(t *testing.T, positions map[string]Position) {
	t.Helper()
	ids := make([]string, 0, len(positions))
	for id := range positions {
		ids = append(ids, id)
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			a, b := positions[ids[i]], positions[ids[j]]
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Errorf("%s and %s overlap: %+v %+v", ids[i], ids[j], a, b)
			}
		}
	}
}

func TestAssetsMatchTheDesignFilesAndStrictSchema(t *testing.T) {
	for _, pair := range [][2]string{
		{"../../../docs/design/generation/system-prompt.md", "assets/system-prompt.md"},
		{"../../../docs/design/generation/note-request.schema.json", "assets/note-request.schema.json"},
	} {
		want, err := os.ReadFile(filepath.FromSlash(pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.FromSlash(pair[1]))
		if err != nil {
			t.Fatal(err)
		}
		if strings.ReplaceAll(string(want), "\r\n", "\n") != strings.ReplaceAll(string(got), "\r\n", "\n") {
			t.Fatalf("%s drifted from %s; run go generate ./internal/generation", pair[1], pair[0])
		}
	}
	if !strings.Contains(SystemPrompt, "detected_language") {
		t.Fatal("system prompt not embedded")
	}
	schema := RequestSchema()
	if idx := strings.Index(string(schema), "minItems"); idx >= 0 {
		t.Fatalf("strict schema still carries minItems near: %s", schema[max(0, idx-160):min(len(schema), idx+40)])
	}
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatal(err)
	}
	var check func(node map[string]any, path string)
	check = func(node map[string]any, path string) {
		if props, ok := node["properties"].(map[string]any); ok {
			required, _ := node["required"].([]any)
			if len(required) != len(props) {
				t.Errorf("%s: %d of %d properties required", path, len(required), len(props))
			}
			if ap, ok := node["additionalProperties"].(bool); !ok || ap {
				t.Errorf("%s: additionalProperties must be false", path)
			}
			for name, v := range props {
				if child, ok := v.(map[string]any); ok {
					check(child, path+"."+name)
				}
			}
		}
		for _, key := range []string{"$defs"} {
			if m, ok := node[key].(map[string]any); ok {
				for name, v := range m {
					if child, ok := v.(map[string]any); ok {
						check(child, path+"."+name)
					}
				}
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			check(items, path+"[]")
		}
		if list, ok := node["anyOf"].([]any); ok {
			for i, v := range list {
				if child, ok := v.(map[string]any); ok {
					check(child, path+"|"+string(rune('0'+i)))
				}
			}
		}
	}
	check(doc, "root")
	// The optional group_id became nullable rather than dropped.
	node := doc["$defs"].(map[string]any)["mindMapNode"].(map[string]any)["properties"].(map[string]any)["group_id"].(map[string]any)
	if types, ok := node["type"].([]any); !ok || len(types) != 2 || types[1] != "null" {
		t.Fatalf("group_id should be nullable: %v", node)
	}
	input := RenderInput([]Chunk{{Ordinal: 0, Text: "hello"}, {Ordinal: 1, Text: "world"}}, Meta{Title: "T", Channel: "C", Duration: 1120}, "")
	if !strings.Contains(input, "[c1]\nhello") || !strings.Contains(input, "[c2]\nworld") || !strings.Contains(input, "Duration: 19 minutes") {
		t.Fatalf("input:\n%s", input)
	}
}

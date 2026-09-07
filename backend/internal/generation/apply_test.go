package generation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storedFixture(t *testing.T) *StoredPayload {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, "en-neural-network.stored.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p StoredPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestEditsFlipProvenanceOnTheServer(t *testing.T) {
	p := storedFixture(t)
	first := p.KeyPoints[0]
	if first.Provenance != "ai" || len(first.SourceRefs) == 0 {
		t.Fatal("fixture block should be AI-authored with refs")
	}
	if err := Apply(p, []Op{{Kind: OpSetBlockText, Section: "key_points", ID: first.ID, Text: "  My own words.  "}}); err != nil {
		t.Fatal(err)
	}
	got := p.KeyPoints[0]
	if got.Text != "My own words." || got.Provenance != "user_edited" || len(got.SourceRefs) != 0 || got.SpeakerClaim != first.SpeakerClaim {
		t.Fatalf("edited block %+v", got)
	}
	if p.KeyPoints[1].Provenance != "ai" {
		t.Fatal("a neighbour's provenance moved")
	}
	// Reordering leaves provenance alone.
	ids := []string{}
	for _, b := range p.KeyPoints {
		ids = append(ids, b.ID)
	}
	ids[0], ids[1] = ids[1], ids[0]
	if err := Apply(p, []Op{{Kind: OpReorderBlocks, Section: "key_points", IDs: ids}}); err != nil {
		t.Fatal(err)
	}
	if p.KeyPoints[1].ID != first.ID || p.KeyPoints[0].Provenance != "ai" {
		t.Fatalf("reorder wrong: %s %s", p.KeyPoints[0].ID, p.KeyPoints[1].ID)
	}
	if err := ValidateEdited(p); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDeleteAndFloors(t *testing.T) {
	p := storedFixture(t)
	after := p.KeyPoints[0].ID
	if err := Apply(p, []Op{{Kind: OpInsertBlock, Section: "key_points", ID: "kp_new", Text: "Inserted", AfterID: &after}}); err != nil {
		t.Fatal(err)
	}
	if p.KeyPoints[1].ID != "kp_new" || p.KeyPoints[1].Provenance != "user_edited" {
		t.Fatalf("insert landed at %s", p.KeyPoints[1].ID)
	}
	if err := Apply(p, []Op{{Kind: OpInsertBlock, Section: "key_points", ID: "kp_new", Text: "again"}}); err == nil || !strings.Contains(err.Error(), "duplicate_element_id") {
		t.Fatalf("duplicate id accepted: %v", err)
	}
	if err := Apply(p, []Op{{Kind: OpInsertBlock, Section: "key_points", ID: "bad id", Text: "x"}}); err == nil || !strings.Contains(err.Error(), "bad_element_id") {
		t.Fatalf("bad id accepted: %v", err)
	}
	// The editing floor: the last key point cannot go, the summary cannot empty.
	var ops []Op
	for _, b := range p.KeyPoints {
		ops = append(ops, Op{Kind: OpDeleteBlock, Section: "key_points", ID: b.ID})
	}
	if err := Apply(p, ops); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEdited(p); err == nil || err.Rule != "key_point_required" {
		t.Fatalf("floor not enforced: %v", err)
	}
	q := storedFixture(t)
	if err := Apply(q, []Op{{Kind: OpSetBlockText, Section: "summary", Text: "   "}}); err == nil || !strings.Contains(err.Error(), "empty_text") {
		t.Fatalf("empty summary accepted: %v", err)
	}
	// Generation ranges do not bind: two key points is fine when editing.
	r := storedFixture(t)
	var extra []string
	for _, b := range r.KeyPoints[2:] {
		extra = append(extra, b.ID)
	}
	for _, id := range extra {
		if err := Apply(r, []Op{{Kind: OpDeleteBlock, Section: "key_points", ID: id}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateEdited(r); err != nil {
		t.Fatalf("two key points rejected: %v", err)
	}
}

func TestDiagramEditsCascadeAndKeepTheTree(t *testing.T) {
	p := storedFixture(t)
	if p.Diagram.Type != MindMap {
		t.Skip("fixture is not a mind map")
	}
	var root, leaf, interior string
	children := map[string]int{}
	for _, e := range p.Diagram.Edges {
		children[e.From]++
	}
	for _, n := range p.Diagram.Nodes {
		switch {
		case n.Kind == "root":
			root = n.ID
		case children[n.ID] == 0 && leaf == "":
			leaf = n.ID
		case children[n.ID] > 0 && interior == "":
			interior = n.ID
		}
	}
	if err := Apply(p, []Op{{Kind: OpDeleteElement, ElementID: root}}); err == nil || !strings.Contains(err.Error(), "root_immutable") {
		t.Fatalf("root deleted: %v", err)
	}
	before := len(p.Diagram.Nodes)
	if err := Apply(p, []Op{{Kind: OpDeleteElement, ElementID: leaf}}); err != nil {
		t.Fatal(err)
	}
	if len(p.Diagram.Nodes) != before-1 {
		t.Fatalf("leaf deletion removed %d nodes", before-len(p.Diagram.Nodes))
	}
	for _, e := range p.Diagram.Edges {
		if e.From == leaf || e.To == leaf {
			t.Fatal("incident edge survived")
		}
	}
	if err := ValidateEdited(p); err != nil {
		t.Fatalf("after leaf deletion: %v", err)
	}
	if interior != "" {
		count := len(p.Diagram.Nodes)
		if err := Apply(p, []Op{{Kind: OpDeleteElement, ElementID: interior}}); err != nil {
			t.Fatal(err)
		}
		if len(p.Diagram.Nodes) >= count-1 && children[interior] > 0 {
			t.Fatal("the subtree of an interior node should go with it")
		}
		if err := ValidateEdited(p); err != nil {
			t.Fatalf("after interior deletion: %v", err)
		}
	}
	// A label edit flips provenance on the node only.
	target := p.Diagram.Nodes[1].ID
	if err := Apply(p, []Op{{Kind: OpSetElementLabel, ElementID: target, Label: "Renamed"}}); err != nil {
		t.Fatal(err)
	}
	if p.Diagram.Nodes[1].Label != "Renamed" || p.Diagram.Nodes[1].Provenance != "user_edited" || len(p.Diagram.Nodes[1].SourceRefs) != 0 {
		t.Fatalf("label edit: %+v", p.Diagram.Nodes[1])
	}
	if err := ValidateEdited(p); err != nil {
		t.Fatal(err)
	}
}

func TestComparisonAndTimelineOperations(t *testing.T) {
	c := &StoredPayload{SchemaVersion: 1, DetectedLanguage: "en", Summary: StoredBlock{ID: "s", Text: "sum", Provenance: "ai", SourceRefs: []SourceRef{}},
		KeyPoints: []StoredBlock{{ID: "k1", Text: "one", Provenance: "ai", SourceRefs: []SourceRef{}}},
		Diagram: StoredDiagram{SchemaVersion: 1, Type: Comparison,
			Columns: []StoredColumn{{ID: "a", Label: "A", Order: 0, Provenance: "ai"}, {ID: "b", Label: "B", Order: 1, Provenance: "ai"}, {ID: "c", Label: "C", Order: 2, Provenance: "ai"}},
			Rows: []StoredRow{
				{ID: "r1", Label: "R1", Order: 0, Provenance: "ai", Cells: map[string]StoredCell{"a": {Text: "1", Provenance: "ai"}, "b": {Text: "2", Provenance: "ai"}, "c": {Text: "3", Provenance: "ai"}}},
				{ID: "r2", Label: "R2", Order: 1, Provenance: "ai", Cells: map[string]StoredCell{"a": {Text: "4", Provenance: "ai"}, "b": {Text: "5", Provenance: "ai"}, "c": {Text: "6", Provenance: "ai"}}},
			}}}
	if err := Apply(c, []Op{
		{Kind: OpSetCellText, RowID: "r1", ColumnID: "b", Text: "edited"},
		{Kind: OpDeleteElement, ElementID: "a"},
		{Kind: OpReorderElements, Collection: "rows", IDs: []string{"r2", "r1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if c.Diagram.Rows[0].Cells["b"].Text != "edited" || c.Diagram.Rows[0].Cells["b"].Provenance != "user_edited" {
		t.Fatalf("cell edit: %+v", c.Diagram.Rows[0].Cells["b"])
	}
	if len(c.Diagram.Columns) != 2 || c.Diagram.Columns[0].Order != 0 || c.Diagram.Columns[1].Order != 1 {
		t.Fatalf("column cascade/renumber: %+v", c.Diagram.Columns)
	}
	if _, ok := c.Diagram.Rows[0].Cells["a"]; ok {
		t.Fatal("deleted column's cell survived")
	}
	if c.Diagram.Rows[0].Order != 1 || c.Diagram.Rows[1].Order != 0 {
		t.Fatalf("row reorder: %d %d", c.Diagram.Rows[0].Order, c.Diagram.Rows[1].Order)
	}
	if err := ValidateEdited(c); err != nil {
		t.Fatal(err)
	}
	if err := Apply(c, []Op{{Kind: OpDeleteElement, ElementID: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEdited(c); err == nil || err.Rule != "comparison_columns" {
		t.Fatalf("one column accepted: %v", err)
	}

	zero, one, two := 0, 1, 2
	tl := &StoredPayload{SchemaVersion: 1, DetectedLanguage: "en", Summary: StoredBlock{ID: "s", Text: "sum", Provenance: "ai", SourceRefs: []SourceRef{}},
		KeyPoints: []StoredBlock{{ID: "k1", Text: "one", Provenance: "ai", SourceRefs: []SourceRef{}}},
		Diagram: StoredDiagram{SchemaVersion: 1, Type: Timeline, Layout: LinearLayout{Orientation: "vertical"},
			Events:          []StoredElement{{ID: "e1", Label: "first", Order: &zero, InitiallyVisible: true, Provenance: "ai"}, {ID: "e2", Label: "second", Order: &one, InitiallyVisible: true, Provenance: "ai"}, {ID: "e3", Label: "third", Order: &two, InitiallyVisible: false, GroupID: "g", Provenance: "ai"}},
			CollapsedGroups: []StoredGroup{{ID: "g", AnchorID: "e2", MemberIDs: []string{"e3"}, InitiallyCollapsed: true}}}}
	if err := ValidateEdited(tl); err != nil {
		t.Fatal(err)
	}
	if err := Apply(tl, []Op{{Kind: OpDeleteElement, ElementID: "e2"}}); err != nil {
		t.Fatal(err)
	}
	// The anchor went, so the group dissolved and its member is shown.
	if len(tl.Diagram.CollapsedGroups) != 0 || !tl.Diagram.Events[1].InitiallyVisible || tl.Diagram.Events[1].GroupID != "" {
		t.Fatalf("group dissolve: %+v %+v", tl.Diagram.CollapsedGroups, tl.Diagram.Events[1])
	}
	if *tl.Diagram.Events[0].Order != 0 || *tl.Diagram.Events[1].Order != 1 {
		t.Fatalf("renumber: %d %d", *tl.Diagram.Events[0].Order, *tl.Diagram.Events[1].Order)
	}
	if err := ValidateEdited(tl); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDiagramElements(t *testing.T) {
	root := "root"
	p := &StoredPayload{
		SchemaVersion:    1,
		DetectedLanguage: "en",
		Summary:          StoredBlock{ID: "s", Text: "sum", Provenance: "ai"},
		KeyPoints:        []StoredBlock{{ID: "k", Text: "point", Provenance: "ai"}},
		Diagram: StoredDiagram{
			SchemaVersion: 1,
			Type:          MindMap,
			Layout:        GraphLayout{Engine: "radial"},
			Nodes: []StoredElement{
				{ID: root, Label: "Root", Kind: "root", InitiallyVisible: true, Provenance: "ai"},
				{ID: "existing", Label: "Existing", Kind: "branch", InitiallyVisible: true, Provenance: "ai"},
			},
			Edges: []StoredEdge{{ID: "edge_existing", From: root, To: "existing"}},
		},
	}
	if err := Apply(p, []Op{{Kind: OpInsertElement, Collection: "nodes", ID: "node_personal", Label: "Personal branch", ParentID: root}}); err != nil {
		t.Fatal(err)
	}
	inserted := p.Diagram.Nodes[len(p.Diagram.Nodes)-1]
	if inserted.ID != "node_personal" || inserted.Kind != "branch" || inserted.Provenance != "user_edited" {
		t.Fatalf("inserted mind-map node: %+v", inserted)
	}
	edge := p.Diagram.Edges[len(p.Diagram.Edges)-1]
	if edge.From != root || edge.To != inserted.ID {
		t.Fatalf("inserted structural edge: %+v", edge)
	}
	if err := ValidateEdited(p); err != nil {
		t.Fatal(err)
	}

	comparison := &StoredPayload{
		SchemaVersion:    1,
		DetectedLanguage: "en",
		Summary:          StoredBlock{ID: "s", Text: "sum", Provenance: "ai"},
		KeyPoints:        []StoredBlock{{ID: "k", Text: "point", Provenance: "ai"}},
		Diagram: StoredDiagram{
			SchemaVersion: 1,
			Type:          Comparison,
			Columns: []StoredColumn{
				{ID: "a", Label: "A", Order: 0, Provenance: "ai"},
				{ID: "b", Label: "B", Order: 1, Provenance: "ai"},
			},
			Rows: []StoredRow{{ID: "r", Label: "R", Order: 0, Provenance: "ai", Cells: map[string]StoredCell{
				"a": {Text: "1", Provenance: "ai"},
				"b": {Text: "2", Provenance: "ai"},
			}}},
		},
	}
	if err := Apply(comparison, []Op{
		{Kind: OpInsertElement, Collection: "columns", ID: "col_new", Label: "C"},
		{Kind: OpInsertElement, Collection: "rows", ID: "row_new", Label: "Second"},
	}); err != nil {
		t.Fatal(err)
	}
	if comparison.Diagram.Rows[0].Cells["col_new"].Text != "New value" || len(comparison.Diagram.Rows[1].Cells) != 3 {
		t.Fatalf("comparison placeholders missing: %+v", comparison.Diagram.Rows)
	}
	if err := ValidateEdited(comparison); err != nil {
		t.Fatal(err)
	}
}

func TestProcessFlowConnectionsRemainAcyclic(t *testing.T) {
	zero, one := 0, 1
	p := &StoredPayload{
		SchemaVersion:    1,
		DetectedLanguage: "en",
		Summary:          StoredBlock{ID: "s", Text: "sum", Provenance: "ai"},
		KeyPoints:        []StoredBlock{{ID: "k", Text: "point", Provenance: "ai"}},
		Diagram: StoredDiagram{
			SchemaVersion: 1,
			Type:          ProcessFlow,
			Layout:        GraphLayout{Engine: "layered", Direction: "TB"},
			Nodes: []StoredElement{
				{ID: "first", Label: "First", Order: &zero, InitiallyVisible: true, Provenance: "ai"},
				{ID: "second", Label: "Second", Order: &one, InitiallyVisible: true, Provenance: "ai"},
			},
			Edges: []StoredEdge{{ID: "forward", From: "first", To: "second"}},
		},
	}
	if err := Apply(p, []Op{{Kind: OpInsertElement, Collection: "edges", ID: "back", From: "second", To: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEdited(p); err == nil || err.Rule != "process_flow_cycle" {
		t.Fatalf("cycle accepted: %v", err)
	}
	if err := Apply(p, []Op{{Kind: OpDeleteElement, ElementID: "back"}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEdited(p); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, []Op{{Kind: OpInsertElement, Collection: "nodes", ID: "third", Label: "Third", ParentID: "second"}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEdited(p); err != nil {
		t.Fatal(err)
	}
}

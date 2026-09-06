package generation

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Limits are spec 7.3's generation ranges and spec 7.2's caps.
var Limits = struct {
	SummaryWords      int
	KeyPoints         [2]int
	ImportantDetails  [2]int
	PracticalActions  [2]int
	VisibleElements   int
	TotalElements     int
	ComparisonColumns int
	ComparisonRows    int
}{40, [2]int{5, 7}, [2]int{3, 8}, [2]int{0, 5}, 12, 24, 4, 6}

var idShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// Finding is one validation failure. Repairable findings are fixed in
// process for free; the rest are content only the model can fix.
type Finding struct {
	Code       string
	Message    string
	Repairable bool
	Path       string
}

func (f Finding) String() string { return f.Path + ": " + f.Message }

// Resolved is a quote located in its chunk.
type Resolved struct {
	ChunkID   [16]byte
	CharStart int
	CharEnd   int
}

// index is a whitespace-tolerant view of a chunk: runs of whitespace fold
// to one space, and every folded position maps back to a code-point offset
// in the original.
type index struct {
	norm []rune
	back []int
}

func buildIndex(text string) index {
	var ix index
	prevSpace := false
	for i, r := range []rune(text) {
		if unicode.IsSpace(r) {
			if prevSpace {
				continue
			}
			ix.norm = append(ix.norm, ' ')
			ix.back = append(ix.back, i)
			prevSpace = true
			continue
		}
		ix.norm = append(ix.norm, r)
		ix.back = append(ix.back, i)
		prevSpace = false
	}
	return ix
}

func normalizeQuote(q string) []rune {
	return []rune(strings.TrimSpace(strings.Join(strings.Fields(q), " ")))
}

func runesIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j := range needle {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// Resolve turns {chunk, quote} into a chunk id and a code-point span. A quote
// the chunk does not contain is the one hallucination this design can
// prove, so it is reported, never guessed at.
func Resolve(ref Quote, chunks []Chunk) (*Resolved, *Finding) {
	var chunk *Chunk
	for i := range chunks {
		if Handle(chunks[i].Ordinal) == ref.Chunk || chunks[i].ID.String() == ref.Chunk {
			chunk = &chunks[i]
			break
		}
	}
	if chunk == nil {
		return nil, &Finding{Code: "unknown_chunk", Message: "no chunk " + ref.Chunk}
	}
	needle := normalizeQuote(ref.Quote)
	if len(needle) == 0 {
		return nil, &Finding{Code: "empty_quote", Message: "quote is empty"}
	}
	ix := buildIndex(chunk.Text)
	at := runesIndex(ix.norm, needle)
	if at < 0 {
		short := []rune(ref.Quote)
		if len(short) > 48 {
			short = short[:48]
		}
		return nil, &Finding{Code: "quote_not_found", Message: fmt.Sprintf("quote not present in %s: %q", ref.Chunk, string(short))}
	}
	return &Resolved{ChunkID: chunk.ID, CharStart: ix.back[at], CharEnd: ix.back[at+len(needle)-1] + 1}, nil
}

// grounded is anything that carries source refs, with the path used in
// findings.
type grounded struct {
	path string
	refs []Quote
}

func groundedParts(d *Diagram) []grounded {
	var out []grounded
	if d.Type == Comparison {
		for i, c := range d.Columns {
			out = append(out, grounded{fmt.Sprintf("diagram.columns[%d]", i), c.SourceRefs})
		}
		for i, r := range d.Rows {
			out = append(out, grounded{fmt.Sprintf("diagram.rows[%d]", i), r.SourceRefs})
			for j, cell := range r.Cells {
				out = append(out, grounded{fmt.Sprintf("diagram.rows[%d].cells[%d]", i, j), cell.SourceRefs})
			}
		}
		return out
	}
	for i, e := range d.elements() {
		out = append(out, grounded{fmt.Sprintf("diagram[%d]", i), e.SourceRefs})
	}
	for i, e := range d.Edges {
		if e.Label != "" {
			out = append(out, grounded{fmt.Sprintf("diagram.edges[%d]", i), e.SourceRefs})
		}
	}
	return out
}

func memberIDs(d *Diagram) map[string]bool {
	hidden := map[string]bool{}
	for _, g := range d.CollapsedGroups {
		for _, m := range g.MemberIDs {
			hidden[m] = true
		}
	}
	return hidden
}

func hasDevanagari(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Devanagari, r) {
			return true
		}
	}
	return false
}

// Validate applies everything the request schema cannot express: counts and
// lengths, output language, grounding, and diagram structure.
func Validate(note *Note, chunks []Chunk) []Finding {
	var findings []Finding
	add := func(code, message string, repairable bool, path string) {
		findings = append(findings, Finding{code, message, repairable, path})
	}

	if words := len(strings.Fields(note.Summary.Text)); words > Limits.SummaryWords {
		add("summary_too_long", fmt.Sprintf("summary is %d words, limit %d", words, Limits.SummaryWords), false, "summary")
	}
	ranges := []struct {
		name  string
		n     int
		limit [2]int
	}{
		{"key_points", len(note.KeyPoints), Limits.KeyPoints},
		{"important_details", len(note.ImportantDetails), Limits.ImportantDetails},
		{"practical_actions", len(note.PracticalActions), Limits.PracticalActions},
	}
	for _, r := range ranges {
		// Too few is content only the model can supply. Too many in
		// practical_actions is a safe truncation because the spec caps it;
		// elsewhere truncation would drop a point the model judged worth
		// making, so it is re-asked instead.
		if r.n < r.limit[0] {
			add(r.name+"_too_few", fmt.Sprintf("%d < %d", r.n, r.limit[0]), false, r.name)
		}
		if r.n > r.limit[1] {
			add(r.name+"_too_many", fmt.Sprintf("%d > %d", r.n, r.limit[1]), r.name == "practical_actions", r.name)
		}
	}

	var text strings.Builder
	text.WriteString(note.Summary.Text)
	for _, list := range [][]Block{note.KeyPoints, note.ImportantDetails, note.PracticalActions} {
		for _, b := range list {
			text.WriteString(" " + b.Text)
		}
	}
	text.WriteString(" " + note.PrimaryTopic)
	for _, e := range note.Diagram.elements() {
		text.WriteString(" " + e.Label)
	}
	if hasDevanagari(text.String()) {
		add("not_english", "note text is not English; the transcript script leaked into the note", false, "note")
	}

	blocks := []grounded{{"summary", note.Summary.SourceRefs}}
	for i, b := range note.KeyPoints {
		blocks = append(blocks, grounded{fmt.Sprintf("key_points[%d]", i), b.SourceRefs})
	}
	for i, b := range note.ImportantDetails {
		blocks = append(blocks, grounded{fmt.Sprintf("important_details[%d]", i), b.SourceRefs})
	}
	for i, b := range note.PracticalActions {
		blocks = append(blocks, grounded{fmt.Sprintf("practical_actions[%d]", i), b.SourceRefs})
	}
	blocks = append(blocks, groundedParts(&note.Diagram)...)
	for _, g := range blocks {
		if len(g.refs) == 0 {
			add("missing_source_refs", "AI content without grounding", false, g.path)
			continue
		}
		for _, ref := range g.refs {
			if _, f := Resolve(ref, chunks); f != nil {
				findings = append(findings, Finding{f.Code, f.Message, false, g.path})
			}
		}
	}

	validateDiagram(&note.Diagram, &findings)
	return findings
}

func validateDiagram(d *Diagram, findings *[]Finding) {
	add := func(code, message string, repairable bool, path string) {
		*findings = append(*findings, Finding{code, message, repairable, path})
	}
	switch d.Type {
	case MindMap, ProcessFlow, Timeline, Comparison:
	default:
		add("missing_diagram", "no diagram", false, "diagram")
		return
	}

	if d.Type == Comparison {
		if len(d.Columns) > Limits.ComparisonColumns {
			add("comparison_too_wide", fmt.Sprintf("%d columns > %d", len(d.Columns), Limits.ComparisonColumns), false, "diagram.columns")
		}
		if len(d.Rows) > Limits.ComparisonRows {
			add("comparison_too_tall", fmt.Sprintf("%d rows > %d", len(d.Rows), Limits.ComparisonRows), false, "diagram.rows")
		}
		colIDs := map[string]bool{}
		for _, c := range d.Columns {
			colIDs[c.ID] = true
		}
		for i, r := range d.Rows {
			seen := map[string]bool{}
			for _, cell := range r.Cells {
				seen[cell.ColumnID] = true
				if !colIDs[cell.ColumnID] {
					add("cell_unknown_column", "cell names missing column "+cell.ColumnID, false, fmt.Sprintf("diagram.rows[%d]", i))
				}
			}
			for _, c := range d.Columns {
				if !seen[c.ID] {
					add("row_missing_cell", "row has no cell for column "+c.ID, false, fmt.Sprintf("diagram.rows[%d]", i))
				}
			}
		}
		colOrders := make([]int, len(d.Columns))
		for i, c := range d.Columns {
			colOrders[i] = c.Order
		}
		rowOrders := make([]int, len(d.Rows))
		for i, r := range d.Rows {
			rowOrders[i] = r.Order
		}
		checkOrders("columns", colOrders, findings)
		checkOrders("rows", rowOrders, findings)
		ids := make([]string, 0, len(d.Columns)+len(d.Rows))
		for _, c := range d.Columns {
			ids = append(ids, c.ID)
		}
		for _, r := range d.Rows {
			ids = append(ids, r.ID)
		}
		checkIDs(ids, findings)
		return
	}

	elements := d.elements()
	hidden := memberIDs(d)
	visible := 0
	for _, e := range elements {
		if !hidden[e.ID] {
			visible++
		}
	}
	if len(elements) > Limits.TotalElements {
		add("too_many_total", fmt.Sprintf("%d elements > %d", len(elements), Limits.TotalElements), false, "diagram")
	} else if visible > Limits.VisibleElements {
		// Repairable: the spec already says overflow is grouped into collapsed
		// branches, so the worker can do that deterministically.
		add("too_many_visible", fmt.Sprintf("%d visible > %d", visible, Limits.VisibleElements), true, "diagram")
	}

	byID := map[string]*Element{}
	for i := range elements {
		byID[elements[i].ID] = &elements[i]
	}
	for _, g := range d.CollapsedGroups {
		if byID[g.AnchorID] == nil {
			add("group_anchor_missing", fmt.Sprintf("anchor %s is not an element", g.AnchorID), false, "group "+g.ID)
		}
		if hidden[g.AnchorID] {
			add("group_anchor_hidden", fmt.Sprintf("anchor %s is also a member", g.AnchorID), false, "group "+g.ID)
		}
		for _, m := range g.MemberIDs {
			el := byID[m]
			switch {
			case el == nil:
				add("group_member_missing", fmt.Sprintf("member %s is not an element", m), false, "group "+g.ID)
			case el.GroupID != g.ID:
				// Membership is the source of truth; stamping group_id from it is safe.
				unset := el.GroupID
				if unset == "" {
					unset = "unset"
				}
				add("group_id_mismatch", fmt.Sprintf("%s is in %s but group_id is %s", m, g.ID, unset), true, "group "+g.ID)
			}
		}
	}

	ids := make([]string, 0, len(elements)+len(d.Edges))
	for _, e := range elements {
		ids = append(ids, e.ID)
	}
	for _, e := range d.Edges {
		ids = append(ids, e.ID)
	}
	checkIDs(ids, findings)

	if d.Type == Timeline {
		checkOrders("events", elementOrders(elements), findings)
		return
	}

	for _, e := range d.Edges {
		if byID[e.From] == nil {
			add("edge_endpoint_missing", fmt.Sprintf("edge %s from %s", e.ID, e.From), false, "diagram.edges")
		}
		if byID[e.To] == nil {
			add("edge_endpoint_missing", fmt.Sprintf("edge %s to %s", e.ID, e.To), false, "diagram.edges")
		}
		if e.Label != "" && len(e.SourceRefs) == 0 {
			add("labelled_edge_ungrounded", fmt.Sprintf("edge %s has a label but no grounding", e.ID), false, "diagram.edges")
		}
	}

	if d.Type == ProcessFlow {
		checkOrders("nodes", elementOrders(elements), findings)
		return
	}

	// A mind map is one tree rooted at the single root node.
	var roots []string
	for _, n := range elements {
		if n.Kind == "root" {
			roots = append(roots, n.ID)
		}
	}
	if len(roots) != 1 {
		add("mind_map_root_count", fmt.Sprintf("%d root nodes, expected 1", len(roots)), false, "diagram")
		return
	}
	incoming := map[string]int{}
	for _, e := range elements {
		incoming[e.ID] = 0
	}
	for _, e := range d.Edges {
		if _, ok := incoming[e.To]; ok {
			incoming[e.To]++
		}
	}
	for _, e := range elements {
		expected := 1
		if e.ID == roots[0] {
			expected = 0
		}
		if incoming[e.ID] != expected {
			add("mind_map_not_tree", fmt.Sprintf("%s has %d incoming edges, expected %d", e.ID, incoming[e.ID], expected), false, "diagram")
		}
	}
	seen := map[string]bool{roots[0]: true}
	queue := []string{roots[0]}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range d.Edges {
			if e.From == cur && !seen[e.To] {
				seen[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	if len(seen) != len(elements) {
		add("mind_map_unreachable", fmt.Sprintf("%d node(s) unreachable from root", len(elements)-len(seen)), false, "diagram")
	}
}

func elementOrders(elements []Element) []int {
	orders := make([]int, len(elements))
	for i, e := range elements {
		if e.Order != nil {
			orders[i] = *e.Order
		} else {
			orders[i] = -1
		}
	}
	return orders
}

func checkIDs(ids []string, findings *[]Finding) {
	seen := map[string]bool{}
	for _, id := range ids {
		if !idShape.MatchString(id) {
			*findings = append(*findings, Finding{"bad_element_id", fmt.Sprintf("id %q is not a valid id", id), true, id})
		}
		if seen[id] {
			*findings = append(*findings, Finding{"duplicate_element_id", fmt.Sprintf("id %s repeats", id), true, id})
		}
		seen[id] = true
	}
}

func checkOrders(label string, orders []int, findings *[]Finding) {
	sorted := append([]int(nil), orders...)
	sortInts(sorted)
	ok := true
	for i, o := range sorted {
		if o != i {
			ok = false
			break
		}
	}
	if !ok {
		*findings = append(*findings, Finding{"order_not_contiguous", fmt.Sprintf("%s order is %v, expected a permutation of 0..%d", label, orders, len(orders)-1), true, "diagram." + label})
	}
}

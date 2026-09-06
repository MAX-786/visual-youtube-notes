package generation

import (
	"fmt"
	"sort"
	"strings"
)

// Op is one operation of the closed catalogue (docs/design/mutation-protocol.md),
// flattened: the fields a kind does not use are empty.
type Op struct {
	Kind       string
	Section    string
	ID         string
	Text       string
	AfterID    *string
	IDs        []string
	ElementID  string
	Label      string
	RowID      string
	ColumnID   string
	Collection string
}

// The operation kinds.
const (
	OpSetBlockText    = "set_block_text"
	OpInsertBlock     = "insert_block"
	OpDeleteBlock     = "delete_block"
	OpReorderBlocks   = "reorder_blocks"
	OpSetElementLabel = "set_element_label"
	OpSetCellText     = "set_cell_text"
	OpDeleteElement   = "delete_element"
	OpReorderElements = "reorder_elements"
)

// RuleError is a structural invariant an applied result broke, or an
// operation that cannot apply to this payload. Rule is the contract's
// `rule` detail; ElementID names what it concerns when there is one.
type RuleError struct {
	Rule      string
	ElementID string
	Message   string
}

func (e *RuleError) Error() string { return e.Rule + ": " + e.Message }

func rule(name, element, format string, args ...any) *RuleError {
	return &RuleError{Rule: name, ElementID: element, Message: fmt.Sprintf(format, args...)}
}

// Apply runs the operations in order on the payload. Any operation that
// changes authored text flips that element's Provenance to user_edited and
// clears its Source refs; the server does this, never the client (ADR 0004).
// Reorders and deletes leave surviving Provenance alone. Deletes cascade:
// a node takes its incident edges (and, on a mind map, the subtree it
// alone connected), a column takes its cells.
func Apply(p *StoredPayload, ops []Op) error {
	for i := range ops {
		if err := applyOne(p, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(p *StoredPayload, op *Op) error {
	switch op.Kind {
	case OpSetBlockText:
		text := strings.TrimSpace(op.Text)
		if text == "" {
			return rule("empty_text", op.ID, "a block cannot be emptied; delete it instead")
		}
		if op.Section == "summary" {
			p.Summary.Text, p.Summary.Provenance, p.Summary.SourceRefs = text, "user_edited", []SourceRef{}
			return nil
		}
		list, err := p.list(op.Section)
		if err != nil {
			return err
		}
		for i := range *list {
			if (*list)[i].ID == op.ID {
				b := &(*list)[i]
				b.Text, b.Provenance, b.SourceRefs = text, "user_edited", []SourceRef{}
				return nil
			}
		}
		return rule("unknown_element", op.ID, "no block %s in %s", op.ID, op.Section)
	case OpInsertBlock:
		list, err := p.list(op.Section)
		if err != nil {
			return err
		}
		if !idShape.MatchString(op.ID) {
			return rule("bad_element_id", op.ID, "id %q is not a valid id", op.ID)
		}
		if p.hasID(op.ID) {
			return rule("duplicate_element_id", op.ID, "id %s already exists", op.ID)
		}
		text := strings.TrimSpace(op.Text)
		if text == "" {
			return rule("empty_text", op.ID, "an inserted block needs text")
		}
		block := StoredBlock{ID: op.ID, Text: text, Provenance: "user_edited", SourceRefs: []SourceRef{}}
		at := 0
		if op.AfterID != nil {
			at = -1
			for i := range *list {
				if (*list)[i].ID == *op.AfterID {
					at = i + 1
				}
			}
			if at < 0 {
				return rule("unknown_element", *op.AfterID, "no block %s to insert after", *op.AfterID)
			}
		}
		*list = append((*list)[:at], append([]StoredBlock{block}, (*list)[at:]...)...)
		return nil
	case OpDeleteBlock:
		list, err := p.list(op.Section)
		if err != nil {
			return err
		}
		for i := range *list {
			if (*list)[i].ID == op.ID {
				*list = append((*list)[:i], (*list)[i+1:]...)
				return nil
			}
		}
		return rule("unknown_element", op.ID, "no block %s in %s", op.ID, op.Section)
	case OpReorderBlocks:
		list, err := p.list(op.Section)
		if err != nil {
			return err
		}
		byID := map[string]StoredBlock{}
		for _, b := range *list {
			byID[b.ID] = b
		}
		if len(op.IDs) != len(*list) {
			return rule("not_a_permutation", "", "%s has %d blocks, %d ids given", op.Section, len(*list), len(op.IDs))
		}
		out := make([]StoredBlock, 0, len(op.IDs))
		seen := map[string]bool{}
		for _, id := range op.IDs {
			b, ok := byID[id]
			if !ok || seen[id] {
				return rule("not_a_permutation", id, "ids are not a permutation of %s", op.Section)
			}
			seen[id] = true
			out = append(out, b)
		}
		*list = out
		return nil
	case OpSetElementLabel:
		label := strings.TrimSpace(op.Label)
		if label == "" {
			return rule("empty_text", op.ElementID, "a label cannot be emptied; delete the element instead")
		}
		d := &p.Diagram
		for _, list := range [][]StoredElement{d.Nodes, d.Events} {
			for i := range list {
				if list[i].ID == op.ElementID {
					list[i].Label, list[i].Provenance, list[i].SourceRefs = label, "user_edited", []SourceRef{}
					return nil
				}
			}
		}
		for i := range d.Edges {
			if d.Edges[i].ID == op.ElementID {
				e := &d.Edges[i]
				e.Label, e.Provenance, e.SourceRefs = label, "user_edited", []SourceRef{}
				if e.SpeakerClaim == nil {
					claim := false
					e.SpeakerClaim = &claim
				}
				return nil
			}
		}
		for i := range d.Columns {
			if d.Columns[i].ID == op.ElementID {
				c := &d.Columns[i]
				c.Label, c.Provenance, c.SourceRefs = label, "user_edited", []SourceRef{}
				return nil
			}
		}
		for i := range d.Rows {
			if d.Rows[i].ID == op.ElementID {
				r := &d.Rows[i]
				r.Label, r.Provenance, r.SourceRefs = label, "user_edited", []SourceRef{}
				return nil
			}
		}
		return rule("unknown_element", op.ElementID, "no element %s", op.ElementID)
	case OpSetCellText:
		text := strings.TrimSpace(op.Text)
		if text == "" {
			return rule("empty_text", op.RowID, "a cell cannot be emptied")
		}
		for i := range p.Diagram.Rows {
			r := &p.Diagram.Rows[i]
			if r.ID != op.RowID {
				continue
			}
			cell, ok := r.Cells[op.ColumnID]
			if !ok {
				return rule("unknown_element", op.ColumnID, "row %s has no cell for column %s", op.RowID, op.ColumnID)
			}
			cell.Text, cell.Provenance, cell.SourceRefs = text, "user_edited", []SourceRef{}
			r.Cells[op.ColumnID] = cell
			return nil
		}
		return rule("unknown_element", op.RowID, "no row %s", op.RowID)
	case OpDeleteElement:
		return deleteElement(&p.Diagram, op.ElementID)
	case OpReorderElements:
		return reorderElements(&p.Diagram, op.Collection, op.IDs)
	default:
		return rule("unknown_operation", "", "operation %q is not in the catalogue", op.Kind)
	}
}

func (p *StoredPayload) list(section string) (*[]StoredBlock, error) {
	switch section {
	case "key_points":
		return &p.KeyPoints, nil
	case "important_details":
		return &p.ImportantDetails, nil
	case "practical_actions":
		return &p.PracticalActions, nil
	default:
		return nil, rule("unknown_section", "", "%q is not a block list", section)
	}
}

func (p *StoredPayload) hasID(id string) bool {
	for _, ids := range [][]string{p.blockIDs(), p.Diagram.ids()} {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
	}
	return false
}

func (p *StoredPayload) blockIDs() []string {
	ids := []string{p.Summary.ID}
	for _, list := range [][]StoredBlock{p.KeyPoints, p.ImportantDetails, p.PracticalActions} {
		for _, b := range list {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

func (d *StoredDiagram) ids() []string {
	var ids []string
	for _, n := range d.Nodes {
		ids = append(ids, n.ID)
	}
	for _, e := range d.Events {
		ids = append(ids, e.ID)
	}
	for _, e := range d.Edges {
		ids = append(ids, e.ID)
	}
	for _, g := range d.CollapsedGroups {
		ids = append(ids, g.ID)
	}
	for _, c := range d.Columns {
		ids = append(ids, c.ID)
	}
	for _, r := range d.Rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func deleteElement(d *StoredDiagram, id string) error {
	switch d.Type {
	case MindMap, ProcessFlow:
		idx := indexOfElement(d.Nodes, id)
		if idx < 0 {
			return rule("unknown_element", id, "no node %s", id)
		}
		if d.Type == MindMap && d.Nodes[idx].Kind == "root" {
			return rule("root_immutable", id, "the root of a mind map cannot be deleted")
		}
		gone := map[string]bool{id: true}
		if d.Type == MindMap {
			// The subtree this node alone connected goes with it, since a
			// mind map must stay one tree rooted at the root.
			for changed := true; changed; {
				changed = false
				for _, e := range d.Edges {
					if gone[e.From] && !gone[e.To] {
						gone[e.To] = true
						changed = true
					}
				}
			}
		}
		kept := d.Nodes[:0]
		for _, n := range d.Nodes {
			if !gone[n.ID] {
				kept = append(kept, n)
			}
		}
		d.Nodes = kept
		edges := d.Edges[:0]
		for _, e := range d.Edges {
			if !gone[e.From] && !gone[e.To] {
				edges = append(edges, e)
			}
		}
		d.Edges = edges
		dropFromGroups(d, gone)
		if d.Type == ProcessFlow {
			renumberElements(d.Nodes)
		}
		return nil
	case Timeline:
		idx := indexOfElement(d.Events, id)
		if idx < 0 {
			return rule("unknown_element", id, "no event %s", id)
		}
		d.Events = append(d.Events[:idx], d.Events[idx+1:]...)
		dropFromGroups(d, map[string]bool{id: true})
		renumberElements(d.Events)
		return nil
	case Comparison:
		for i, c := range d.Columns {
			if c.ID == id {
				d.Columns = append(d.Columns[:i], d.Columns[i+1:]...)
				for r := range d.Rows {
					delete(d.Rows[r].Cells, id)
				}
				renumberColumns(d.Columns)
				return nil
			}
		}
		for i, r := range d.Rows {
			if r.ID == id {
				d.Rows = append(d.Rows[:i], d.Rows[i+1:]...)
				renumberRows(d.Rows)
				return nil
			}
		}
		return rule("unknown_element", id, "no column or row %s", id)
	}
	return rule("unknown_element", id, "no element %s", id)
}

// dropFromGroups removes deleted members from collapsed groups; a group that
// loses its anchor or its last member is dissolved and its members shown.
func dropFromGroups(d *StoredDiagram, gone map[string]bool) {
	kept := d.CollapsedGroups[:0]
	show := map[string]bool{}
	for _, g := range d.CollapsedGroups {
		members := g.MemberIDs[:0]
		for _, m := range g.MemberIDs {
			if !gone[m] {
				members = append(members, m)
			}
		}
		g.MemberIDs = members
		if gone[g.AnchorID] || len(members) == 0 {
			for _, m := range members {
				show[m] = true
			}
			continue
		}
		kept = append(kept, g)
	}
	d.CollapsedGroups = kept
	for _, list := range [][]StoredElement{d.Nodes, d.Events} {
		for i := range list {
			if show[list[i].ID] {
				list[i].InitiallyVisible = true
				list[i].GroupID = ""
			}
		}
	}
}

func indexOfElement(list []StoredElement, id string) int {
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

func renumberElements(list []StoredElement) {
	idx := make([]int, len(list))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return elementOrder(list[idx[a]]) < elementOrder(list[idx[b]]) })
	for rank, i := range idx {
		r := rank
		list[i].Order = &r
	}
}

func renumberColumns(list []StoredColumn) {
	sort.SliceStable(list, func(a, b int) bool { return list[a].Order < list[b].Order })
	for i := range list {
		list[i].Order = i
	}
}

func renumberRows(list []StoredRow) {
	sort.SliceStable(list, func(a, b int) bool { return list[a].Order < list[b].Order })
	for i := range list {
		list[i].Order = i
	}
}

func reorderElements(d *StoredDiagram, collection string, ids []string) error {
	permute := func(have []string) (map[string]int, error) {
		if len(ids) != len(have) {
			return nil, rule("not_a_permutation", "", "%s has %d elements, %d ids given", collection, len(have), len(ids))
		}
		pos := map[string]int{}
		known := map[string]bool{}
		for _, h := range have {
			known[h] = true
		}
		for i, id := range ids {
			if _, dup := pos[id]; dup || !known[id] {
				return nil, rule("not_a_permutation", id, "ids are not a permutation of %s", collection)
			}
			pos[id] = i
		}
		return pos, nil
	}
	switch collection {
	case "events":
		if d.Type != Timeline {
			return rule("unknown_section", "", "no events on a %s", d.Type)
		}
		have := make([]string, 0, len(d.Events))
		for _, e := range d.Events {
			have = append(have, e.ID)
		}
		pos, err := permute(have)
		if err != nil {
			return err
		}
		for i := range d.Events {
			o := pos[d.Events[i].ID]
			d.Events[i].Order = &o
		}
		return nil
	case "columns":
		if d.Type != Comparison {
			return rule("unknown_section", "", "no columns on a %s", d.Type)
		}
		have := make([]string, 0, len(d.Columns))
		for _, c := range d.Columns {
			have = append(have, c.ID)
		}
		pos, err := permute(have)
		if err != nil {
			return err
		}
		for i := range d.Columns {
			d.Columns[i].Order = pos[d.Columns[i].ID]
		}
		return nil
	case "rows":
		if d.Type != Comparison {
			return rule("unknown_section", "", "no rows on a %s", d.Type)
		}
		have := make([]string, 0, len(d.Rows))
		for _, r := range d.Rows {
			have = append(have, r.ID)
		}
		pos, err := permute(have)
		if err != nil {
			return err
		}
		for i := range d.Rows {
			d.Rows[i].Order = pos[d.Rows[i].ID]
		}
		return nil
	default:
		return rule("unknown_section", "", "%q is not a reorderable collection", collection)
	}
}

// ValidateEdited applies the structural invariants that bind always
// (ADR 0004): unique ids, resolving references, a mind map that is one
// tree, contiguous order, the visible and total caps, the comparison cap,
// and the editing floors of a non-empty summary and at least one key point.
// Generation ranges do not bind here.
func ValidateEdited(p *StoredPayload) *RuleError {
	if strings.TrimSpace(p.Summary.Text) == "" {
		return rule("summary_required", p.Summary.ID, "the summary cannot be empty")
	}
	if len(p.KeyPoints) == 0 {
		return rule("key_point_required", "", "a note keeps at least one key point")
	}
	if len(p.PracticalActions) > Limits.PracticalActions[1] {
		return rule("practical_actions_cap", "", "at most %d practical actions", Limits.PracticalActions[1])
	}
	seen := map[string]bool{}
	for _, id := range append(p.blockIDs(), p.Diagram.ids()...) {
		if !idShape.MatchString(id) {
			return rule("bad_element_id", id, "id %q is not a valid id", id)
		}
		if seen[id] {
			return rule("duplicate_element_id", id, "id %s repeats", id)
		}
		seen[id] = true
	}
	for _, list := range [][]StoredBlock{p.KeyPoints, p.ImportantDetails, p.PracticalActions} {
		for _, b := range list {
			if strings.TrimSpace(b.Text) == "" {
				return rule("empty_text", b.ID, "block %s is empty", b.ID)
			}
		}
	}
	return validateStoredDiagram(&p.Diagram)
}

func validateStoredDiagram(d *StoredDiagram) *RuleError {
	switch d.Type {
	case Comparison:
		if len(d.Columns) < 2 || len(d.Columns) > Limits.ComparisonColumns {
			return rule("comparison_columns", "", "a comparison keeps 2 to %d columns", Limits.ComparisonColumns)
		}
		if len(d.Rows) < 1 || len(d.Rows) > Limits.ComparisonRows {
			return rule("comparison_rows", "", "a comparison keeps 1 to %d rows", Limits.ComparisonRows)
		}
		cols := map[string]bool{}
		orders := make([]int, 0, len(d.Columns))
		for _, c := range d.Columns {
			cols[c.ID] = true
			orders = append(orders, c.Order)
		}
		if !contiguous(orders) {
			return rule("order_not_contiguous", "", "column order is not contiguous from zero")
		}
		orders = orders[:0]
		for _, r := range d.Rows {
			orders = append(orders, r.Order)
			if len(r.Cells) != len(cols) {
				return rule("row_missing_cell", r.ID, "row %s does not have one cell per column", r.ID)
			}
			for id, cell := range r.Cells {
				if !cols[id] {
					return rule("cell_unknown_column", r.ID, "row %s names column %s", r.ID, id)
				}
				if strings.TrimSpace(cell.Text) == "" {
					return rule("empty_text", r.ID, "row %s has an empty cell", r.ID)
				}
			}
		}
		if !contiguous(orders) {
			return rule("order_not_contiguous", "", "row order is not contiguous from zero")
		}
		return nil
	case Timeline, MindMap, ProcessFlow:
	default:
		return rule("missing_diagram", "", "unknown diagram type %q", d.Type)
	}
	elements := d.Elements()
	if len(elements) == 0 || (d.Type == ProcessFlow && len(elements) < 2) {
		return rule("too_few_elements", "", "the diagram needs its elements")
	}
	if len(elements) > Limits.TotalElements {
		return rule("too_many_total", "", "%d elements > %d", len(elements), Limits.TotalElements)
	}
	visible := 0
	byID := map[string]*StoredElement{}
	for i := range elements {
		byID[elements[i].ID] = &elements[i]
		if elements[i].InitiallyVisible {
			visible++
		}
		if strings.TrimSpace(elements[i].Label) == "" {
			return rule("empty_text", elements[i].ID, "element %s has no label", elements[i].ID)
		}
	}
	if visible > Limits.VisibleElements {
		return rule("too_many_visible", "", "%d visible > %d", visible, Limits.VisibleElements)
	}
	members := map[string]string{}
	for _, g := range d.CollapsedGroups {
		anchor := byID[g.AnchorID]
		if anchor == nil {
			return rule("group_anchor_missing", g.ID, "group %s anchors on a missing element", g.ID)
		}
		if !anchor.InitiallyVisible {
			return rule("group_anchor_hidden", g.ID, "group %s anchors on a hidden element", g.ID)
		}
		if len(g.MemberIDs) == 0 {
			return rule("group_empty", g.ID, "group %s has no members", g.ID)
		}
		for _, m := range g.MemberIDs {
			el := byID[m]
			if el == nil || el.InitiallyVisible || el.GroupID != g.ID {
				return rule("group_member_invalid", m, "member %s of group %s is missing, visible, or mislabelled", m, g.ID)
			}
			if _, dup := members[m]; dup {
				return rule("group_member_invalid", m, "element %s is in two groups", m)
			}
			members[m] = g.ID
		}
	}
	for i := range elements {
		if !elements[i].InitiallyVisible && members[elements[i].ID] == "" {
			return rule("hidden_without_group", elements[i].ID, "hidden element %s belongs to no group", elements[i].ID)
		}
	}
	if d.Type == Timeline || d.Type == ProcessFlow {
		orders := make([]int, 0, len(elements))
		for _, e := range elements {
			orders = append(orders, elementOrder(e))
		}
		if !contiguous(orders) {
			return rule("order_not_contiguous", "", "order is not contiguous from zero")
		}
	}
	if d.Type == Timeline {
		return nil
	}
	for _, e := range d.Edges {
		if byID[e.From] == nil || byID[e.To] == nil {
			return rule("edge_endpoint_missing", e.ID, "edge %s does not resolve", e.ID)
		}
		if e.Label != "" && e.Provenance == "" {
			return rule("labelled_edge_ungrounded", e.ID, "edge %s has a label without provenance", e.ID)
		}
	}
	if d.Type == MindMap {
		var root string
		roots := 0
		for _, n := range d.Nodes {
			if n.Kind == "root" {
				roots++
				root = n.ID
			}
		}
		if roots != 1 {
			return rule("mind_map_root_count", "", "%d root nodes, expected 1", roots)
		}
		incoming := map[string]int{}
		for _, e := range d.Edges {
			incoming[e.To]++
		}
		for _, n := range d.Nodes {
			expected := 1
			if n.ID == root {
				expected = 0
			}
			if incoming[n.ID] != expected {
				return rule("mind_map_not_tree", n.ID, "%s has %d incoming edges, expected %d", n.ID, incoming[n.ID], expected)
			}
		}
		seen := map[string]bool{root: true}
		queue := []string{root}
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
		if len(seen) != len(d.Nodes) {
			return rule("mind_map_unreachable", "", "%d node(s) unreachable from the root", len(d.Nodes)-len(seen))
		}
	}
	return nil
}

func contiguous(orders []int) bool {
	sorted := append([]int(nil), orders...)
	sort.Ints(sorted)
	for i, o := range sorted {
		if o != i {
			return false
		}
	}
	return true
}

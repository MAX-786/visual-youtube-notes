package generation

import (
	"fmt"
	"sort"
	"strings"
)

// ToStored maps a validated request-shaped note onto the normative stored
// payload: provenance ai, resolved source refs, initially_visible from group
// membership, initially_collapsed on every group, and the layout engine
// (ADR 0002, ADR 0004, ADR 0011). Call it only after Validate found nothing,
// since an unresolvable quote is dropped here rather than reported.
func ToStored(note *Note, chunks []Chunk) *StoredPayload {
	resolve := func(refs []Quote) []SourceRef {
		out := make([]SourceRef, 0, len(refs))
		seen := map[SourceRef]bool{}
		for _, r := range refs {
			res, f := Resolve(r, chunks)
			if f != nil {
				continue
			}
			ref := SourceRef{ChunkID: res.ChunkID, CharStart: res.CharStart, CharEnd: res.CharEnd}
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
		return out
	}
	block := func(b Block) StoredBlock {
		return StoredBlock{ID: b.ID, Text: b.Text, Provenance: "ai", SpeakerClaim: b.SpeakerClaim, SourceRefs: resolve(b.SourceRefs)}
	}
	blocks := func(list []Block) []StoredBlock {
		out := make([]StoredBlock, 0, len(list))
		for _, b := range list {
			out = append(out, block(b))
		}
		return out
	}

	d := &note.Diagram
	hidden := memberIDs(d)
	groups := make([]StoredGroup, 0, len(d.CollapsedGroups))
	for _, g := range d.CollapsedGroups {
		groups = append(groups, StoredGroup{ID: g.ID, AnchorID: g.AnchorID, MemberIDs: append([]string{}, g.MemberIDs...), InitiallyCollapsed: true})
	}
	stored := StoredDiagram{SchemaVersion: 1, Type: d.Type}
	switch d.Type {
	case Comparison:
		stored.Columns = make([]StoredColumn, 0, len(d.Columns))
		for _, c := range d.Columns {
			stored.Columns = append(stored.Columns, StoredColumn{ID: c.ID, Label: c.Label, Order: c.Order, Provenance: "ai", SpeakerClaim: c.SpeakerClaim, SourceRefs: resolve(c.SourceRefs)})
		}
		stored.Rows = make([]StoredRow, 0, len(d.Rows))
		for _, r := range d.Rows {
			cells := map[string]StoredCell{}
			for _, c := range r.Cells {
				cells[c.ColumnID] = StoredCell{Text: c.Text, Provenance: "ai", SpeakerClaim: c.SpeakerClaim, SourceRefs: resolve(c.SourceRefs)}
			}
			stored.Rows = append(stored.Rows, StoredRow{ID: r.ID, Label: r.Label, Order: r.Order, Provenance: "ai", SpeakerClaim: r.SpeakerClaim, SourceRefs: resolve(r.SourceRefs), Cells: cells})
		}
	case Timeline:
		stored.Events = storedElements(d.Events, hidden, false, resolve)
		stored.CollapsedGroups = groups
		orientation := d.Orientation
		if orientation == "" {
			orientation = "vertical"
		}
		stored.Layout = LinearLayout{Orientation: orientation}
	default:
		stored.Nodes = storedElements(d.Nodes, hidden, d.Type == MindMap, resolve)
		stored.Edges = make([]StoredEdge, 0, len(d.Edges))
		for _, e := range d.Edges {
			se := StoredEdge{ID: e.ID, From: e.From, To: e.To, Order: e.Order}
			if e.Label != "" {
				claim := e.SpeakerClaim
				se.Label = e.Label
				se.Provenance = "ai"
				se.SpeakerClaim = &claim
				se.SourceRefs = resolve(e.SourceRefs)
			}
			stored.Edges = append(stored.Edges, se)
		}
		stored.CollapsedGroups = groups
		engine := "layered"
		if d.Type == MindMap {
			engine = "radial"
		}
		stored.Layout = GraphLayout{Engine: engine}
	}

	return &StoredPayload{
		SchemaVersion:    1,
		DetectedLanguage: note.DetectedLanguage,
		PrimaryTopic:     note.PrimaryTopic,
		Tags:             nonNil(note.Tags),
		Summary:          block(note.Summary),
		KeyPoints:        blocks(note.KeyPoints),
		ImportantDetails: blocks(note.ImportantDetails),
		PracticalActions: blocks(note.PracticalActions),
		Diagram:          stored,
	}
}

func storedElements(list []Element, hidden map[string]bool, mindMap bool, resolve func([]Quote) []SourceRef) []StoredElement {
	out := make([]StoredElement, 0, len(list))
	for _, e := range list {
		se := StoredElement{ID: e.ID, Label: e.Label, GroupID: e.GroupID, InitiallyVisible: !hidden[e.ID], Provenance: "ai", SpeakerClaim: e.SpeakerClaim, SourceRefs: resolve(e.SourceRefs)}
		if mindMap {
			se.Kind = e.Kind
		} else {
			se.Order = e.Order
		}
		out = append(out, se)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// TextAlternative is the deterministic derived text of a stored diagram
// (docs/design/diagram-semantics.md): never stored, used by the search
// child, exports and accessibility. Hidden content is included; Provenance,
// Source refs and layout are not.
func TextAlternative(d *StoredDiagram) string {
	spoken := func(claim bool, text string) string {
		if claim {
			return "Speaker claim: " + text
		}
		return text
	}
	byOrderThenID := func(orders map[string]int, ids []string) {
		sort.SliceStable(ids, func(a, b int) bool {
			oa, ob := orders[ids[a]], orders[ids[b]]
			if oa != ob {
				return oa < ob
			}
			return ids[a] < ids[b]
		})
	}
	switch d.Type {
	case MindMap, ProcessFlow:
		byID := map[string]StoredElement{}
		for _, n := range d.Nodes {
			byID[n.ID] = n
		}
		outgoing := map[string][]StoredEdge{}
		for _, e := range d.Edges {
			outgoing[e.From] = append(outgoing[e.From], e)
		}
		sortEdges := func(edges []StoredEdge) {
			sort.SliceStable(edges, func(a, b int) bool {
				oa, ob := edgeOrder(edges[a]), edgeOrder(edges[b])
				if oa != ob {
					return oa < ob
				}
				return edges[a].ID < edges[b].ID
			})
		}
		if d.Type == MindMap {
			var root *StoredElement
			for i := range d.Nodes {
				if d.Nodes[i].Kind == "root" {
					root = &d.Nodes[i]
					break
				}
			}
			var lines []string
			var visit func(n StoredElement, depth int, relationship string)
			visit = func(n StoredElement, depth int, relationship string) {
				prefix := strings.Repeat("  ", depth)
				if relationship != "" {
					prefix += relationship + ": "
				}
				lines = append(lines, prefix+spoken(n.SpeakerClaim, n.Label))
				edges := append([]StoredEdge{}, outgoing[n.ID]...)
				sortEdges(edges)
				for _, e := range edges {
					if target, ok := byID[e.To]; ok {
						visit(target, depth+1, e.Label)
					}
				}
			}
			if root != nil {
				visit(*root, 0, "")
			}
			return strings.Join(lines, "\n")
		}
		ids := make([]string, 0, len(d.Nodes))
		orders := map[string]int{}
		for _, n := range d.Nodes {
			ids = append(ids, n.ID)
			orders[n.ID] = elementOrder(n)
		}
		byOrderThenID(orders, ids)
		var lines []string
		for i, id := range ids {
			n := byID[id]
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, spoken(n.SpeakerClaim, n.Label)))
			edges := append([]StoredEdge{}, outgoing[id]...)
			sortEdges(edges)
			for _, e := range edges {
				if e.Label == "" {
					continue
				}
				target := byID[e.To]
				claim := e.SpeakerClaim != nil && *e.SpeakerClaim
				lines = append(lines, fmt.Sprintf("   Branch: %s → %s", spoken(claim, e.Label), spoken(target.SpeakerClaim, target.Label)))
			}
		}
		return strings.Join(lines, "\n")
	case Timeline:
		ids := make([]string, 0, len(d.Events))
		orders := map[string]int{}
		byID := map[string]StoredElement{}
		for _, e := range d.Events {
			ids = append(ids, e.ID)
			orders[e.ID] = elementOrder(e)
			byID[e.ID] = e
		}
		byOrderThenID(orders, ids)
		lines := make([]string, 0, len(ids))
		for i, id := range ids {
			e := byID[id]
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, spoken(e.SpeakerClaim, e.Label)))
		}
		return strings.Join(lines, "\n")
	case Comparison:
		columns := append([]StoredColumn{}, d.Columns...)
		sort.SliceStable(columns, func(a, b int) bool {
			if columns[a].Order != columns[b].Order {
				return columns[a].Order < columns[b].Order
			}
			return columns[a].ID < columns[b].ID
		})
		rows := append([]StoredRow{}, d.Rows...)
		sort.SliceStable(rows, func(a, b int) bool {
			if rows[a].Order != rows[b].Order {
				return rows[a].Order < rows[b].Order
			}
			return rows[a].ID < rows[b].ID
		})
		header := []string{"Criterion"}
		for _, c := range columns {
			header = append(header, spoken(c.SpeakerClaim, c.Label))
		}
		lines := []string{strings.Join(header, " | ")}
		for _, r := range rows {
			cells := []string{spoken(r.SpeakerClaim, r.Label)}
			for _, c := range columns {
				cell := r.Cells[c.ID]
				cells = append(cells, spoken(cell.SpeakerClaim, cell.Text))
			}
			lines = append(lines, strings.Join(cells, " | "))
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

const maxOrder = int(^uint(0) >> 1)

func elementOrder(e StoredElement) int {
	if e.Order == nil {
		return maxOrder
	}
	return *e.Order
}

func edgeOrder(e StoredEdge) int {
	if e.Order == nil {
		return maxOrder
	}
	return *e.Order
}

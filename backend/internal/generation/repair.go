package generation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Repair applies the deterministic, content-preserving fixes for the
// repairable findings and reports what it did. The note is copied; the input
// is untouched.
func Repair(input *Note, findings []Finding) (*Note, []string) {
	note := clone(input)
	var applied []string
	codes := map[string]bool{}
	for _, f := range findings {
		if f.Repairable {
			codes[f.Code] = true
		}
	}
	if codes["practical_actions_too_many"] {
		dropped := len(note.PracticalActions) - Limits.PracticalActions[1]
		note.PracticalActions = note.PracticalActions[:Limits.PracticalActions[1]]
		applied = append(applied, fmt.Sprintf("dropped %d practical action(s) beyond the cap of %d", dropped, Limits.PracticalActions[1]))
	}
	if codes["bad_element_id"] || codes["duplicate_element_id"] {
		applied = append(applied, renameIDs(&note.Diagram)...)
	}
	if codes["order_not_contiguous"] {
		applied = append(applied, renumber(&note.Diagram)...)
	}
	if codes["group_id_mismatch"] {
		elements := note.Diagram.elements()
		for _, g := range note.Diagram.CollapsedGroups {
			for _, m := range g.MemberIDs {
				for i := range elements {
					if elements[i].ID == m {
						elements[i].GroupID = g.ID
					}
				}
			}
		}
		applied = append(applied, "stamped group_id from collapsed-group membership")
	}
	if codes["too_many_visible"] {
		applied = append(applied, autoCollapse(&note.Diagram)...)
	}
	return note, applied
}

func clone(n *Note) *Note {
	raw, _ := json.Marshal(n)
	var out Note
	_ = json.Unmarshal(raw, &out)
	return &out
}

var idStrip = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// renameIDs gives malformed or duplicate ids a valid unique one and rewrites
// every reference to them.
func renameIDs(d *Diagram) []string {
	var applied []string
	taken := map[string]bool{}
	rewrite := map[string]string{}
	fix := func(id *string, i int) {
		old := *id
		if idShape.MatchString(old) && !taken[old] {
			taken[old] = true
			return
		}
		base := idStrip.ReplaceAllString(old, "")
		if base == "" {
			base = "e"
		}
		next := base
		if !regexp.MustCompile(`^[A-Za-z]`).MatchString(next) {
			next = "e" + next
		}
		for taken[next] {
			next = fmt.Sprintf("%s_%d", next, i)
		}
		rewrite[old] = next
		applied = append(applied, fmt.Sprintf("renamed id %q to %s", old, next))
		*id = next
		taken[next] = true
	}
	i := 0
	elements := d.elements()
	for k := range elements {
		fix(&elements[k].ID, i)
		i++
	}
	for k := range d.Edges {
		fix(&d.Edges[k].ID, i)
		i++
	}
	for k := range d.Columns {
		fix(&d.Columns[k].ID, i)
		i++
	}
	for k := range d.Rows {
		fix(&d.Rows[k].ID, i)
		i++
	}
	if len(rewrite) == 0 {
		return applied
	}
	for k := range d.Edges {
		if n, ok := rewrite[d.Edges[k].From]; ok {
			d.Edges[k].From = n
		}
		if n, ok := rewrite[d.Edges[k].To]; ok {
			d.Edges[k].To = n
		}
	}
	for k := range d.CollapsedGroups {
		g := &d.CollapsedGroups[k]
		if n, ok := rewrite[g.AnchorID]; ok {
			g.AnchorID = n
		}
		for m := range g.MemberIDs {
			if n, ok := rewrite[g.MemberIDs[m]]; ok {
				g.MemberIDs[m] = n
			}
		}
	}
	for k := range elements {
		if n, ok := rewrite[elements[k].GroupID]; ok {
			elements[k].GroupID = n
		}
	}
	for k := range d.Rows {
		for c := range d.Rows[k].Cells {
			if n, ok := rewrite[d.Rows[k].Cells[c].ColumnID]; ok {
				d.Rows[k].Cells[c].ColumnID = n
			}
		}
	}
	return applied
}

// renumber makes order contiguous from zero, preserving the sequence.
func renumber(d *Diagram) []string {
	var applied []string
	if d.Type == Comparison {
		before := make([]int, len(d.Columns))
		for i, c := range d.Columns {
			before[i] = c.Order
		}
		idx := sortedIndex(before)
		for rank, i := range idx {
			d.Columns[i].Order = rank
		}
		if changed(before, d.Columns, func(c Column) int { return c.Order }) {
			applied = append(applied, fmt.Sprintf("renumbered columns order to 0..%d, sequence preserved", len(d.Columns)-1))
		}
		beforeRows := make([]int, len(d.Rows))
		for i, r := range d.Rows {
			beforeRows[i] = r.Order
		}
		idx = sortedIndex(beforeRows)
		for rank, i := range idx {
			d.Rows[i].Order = rank
		}
		if changed(beforeRows, d.Rows, func(r Row) int { return r.Order }) {
			applied = append(applied, fmt.Sprintf("renumbered rows order to 0..%d, sequence preserved", len(d.Rows)-1))
		}
		return applied
	}
	elements := d.elements()
	label := "nodes"
	if d.Type == Timeline {
		label = "events"
	}
	before := elementOrders(elements)
	idx := sortedIndex(before)
	moved := false
	for rank, i := range idx {
		if elements[i].Order == nil || *elements[i].Order != rank {
			moved = true
		}
		r := rank
		elements[i].Order = &r
	}
	if moved {
		applied = append(applied, fmt.Sprintf("renumbered %s order to 0..%d, sequence preserved", label, len(elements)-1))
	}
	return applied
}

func changed[T any](before []int, items []T, order func(T) int) bool {
	for i, item := range items {
		if order(item) != before[i] {
			return true
		}
	}
	return false
}

// sortedIndex returns indexes ordered by value, stable.
func sortedIndex(values []int) []int {
	idx := make([]int, len(values))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return values[idx[a]] < values[idx[b]] })
	return idx
}

func sortInts(v []int) { sort.Ints(v) }

// autoCollapse keeps the first twelve visible elements and hides the rest
// behind the twelfth, as spec 7.2 prescribes for overflow (the prototype
// kept eleven; the spec says twelve).
func autoCollapse(d *Diagram) []string {
	elements := d.elements()
	hidden := memberIDs(d)
	var visible []*Element
	for i := range elements {
		if !hidden[elements[i].ID] {
			visible = append(visible, &elements[i])
		}
	}
	if len(visible) <= Limits.VisibleElements {
		return nil
	}
	overflow := visible[Limits.VisibleElements:]
	anchor := visible[Limits.VisibleElements-1]
	id := "grp_overflow"
	members := make([]string, 0, len(overflow))
	for _, el := range overflow {
		el.GroupID = id
		members = append(members, el.ID)
	}
	d.CollapsedGroups = append(d.CollapsedGroups, Group{ID: id, AnchorID: anchor.ID, MemberIDs: members})
	return []string{fmt.Sprintf("collapsed %d overflow element(s) behind %s", len(overflow), anchor.ID)}
}

// Outcome of one validation cycle.
type Outcome string

// The three outcomes of Cycle.
const (
	Ready  Outcome = "ready"
	ReAsk  Outcome = "re-ask"
	Failed Outcome = "failed"
)

// CycleResult is what one pass of spec 8.3 steps 3-4 produced.
type CycleResult struct {
	Outcome  Outcome
	Note     *Note
	Applied  []string
	Findings []Finding
	// Feedback is fed back to the model verbatim on a re-ask.
	Feedback []string
}

// Cycle validates, repairs, revalidates, and decides: ready, re-ask once
// (the in-process budget, deliberately not the River ladder: a schema-
// invalid answer is a permanent failure under spec 8.4 and must not spend
// the transient budget), or failed.
func Cycle(note *Note, chunks []Chunk, reasksUsed, maxReasks int) CycleResult {
	first := Validate(note, chunks)
	if len(first) == 0 {
		return CycleResult{Outcome: Ready, Note: note}
	}
	repaired, applied := Repair(note, first)
	second := first
	if len(applied) > 0 {
		second = Validate(repaired, chunks)
	}
	if len(second) == 0 {
		return CycleResult{Outcome: Ready, Note: repaired, Applied: applied}
	}
	feedback := make([]string, 0, len(second))
	for _, f := range second {
		feedback = append(feedback, f.String())
	}
	if reasksUsed < maxReasks {
		return CycleResult{Outcome: ReAsk, Note: repaired, Applied: applied, Findings: second, Feedback: feedback}
	}
	return CycleResult{Outcome: Failed, Note: repaired, Applied: applied, Findings: second, Feedback: feedback}
}

// Summarize joins findings for an error message a person can read.
func Summarize(findings []Finding) string {
	parts := make([]string, 0, len(findings))
	for i, f := range findings {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("and %d more", len(findings)-4))
			break
		}
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "; ")
}

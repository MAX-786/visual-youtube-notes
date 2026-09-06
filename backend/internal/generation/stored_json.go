package generation

import "encoding/json"

// MarshalJSON writes exactly the keys diagram.schema.json allows for the
// diagram's type: a mind map or process flow carries nodes, edges,
// collapsed_groups and layout; a timeline events, collapsed_groups and
// layout; a comparison columns and rows. Empty lists are written as [] since
// the schema requires the keys, and nothing else is written since it
// forbids additional properties.
func (d StoredDiagram) MarshalJSON() ([]byte, error) {
	type common struct {
		SchemaVersion int    `json:"schema_version"`
		Type          string `json:"type"`
	}
	base := common{SchemaVersion: d.SchemaVersion, Type: d.Type}
	switch d.Type {
	case Comparison:
		return json.Marshal(struct {
			common
			Columns []StoredColumn `json:"columns"`
			Rows    []StoredRow    `json:"rows"`
		}{base, nonNilColumns(d.Columns), nonNilRows(d.Rows)})
	case Timeline:
		return json.Marshal(struct {
			common
			Events          []StoredElement `json:"events"`
			CollapsedGroups []StoredGroup   `json:"collapsed_groups"`
			Layout          any             `json:"layout"`
		}{base, nonNilElements(d.Events), nonNilGroups(d.CollapsedGroups), d.Layout})
	default:
		return json.Marshal(struct {
			common
			Nodes           []StoredElement `json:"nodes"`
			Edges           []StoredEdge    `json:"edges"`
			CollapsedGroups []StoredGroup   `json:"collapsed_groups"`
			Layout          any             `json:"layout"`
		}{base, nonNilElements(d.Nodes), nonNilEdges(d.Edges), nonNilGroups(d.CollapsedGroups), d.Layout})
	}
}

func nonNilElements(s []StoredElement) []StoredElement {
	if s == nil {
		return []StoredElement{}
	}
	return s
}

func nonNilEdges(s []StoredEdge) []StoredEdge {
	if s == nil {
		return []StoredEdge{}
	}
	return s
}

func nonNilGroups(s []StoredGroup) []StoredGroup {
	if s == nil {
		return []StoredGroup{}
	}
	return s
}

func nonNilColumns(s []StoredColumn) []StoredColumn {
	if s == nil {
		return []StoredColumn{}
	}
	return s
}

func nonNilRows(s []StoredRow) []StoredRow {
	if s == nil {
		return []StoredRow{}
	}
	return s
}

// Package generation turns a model's answer into a Revision: the decode-safe
// request schema and the frozen system prompt, the validator, repairs and
// re-ask machine ported from docs/design/generation/validate-note.mjs, the
// request-to-storage mapping, the Text alternative, and the server-side
// layout (ADR 0011). It is pure: no network, no database.
package generation

import (
	"fmt"

	"github.com/google/uuid"
)

// Chunk is one Transcript chunk as the model sees it. Handles are printed as
// c1, c2, ... in the request and resolved back to the chunk's UUID.
type Chunk struct {
	ID      uuid.UUID
	Ordinal int
	Text    string
}

// Handle is the short name of a chunk in the request.
func Handle(ordinal int) string { return fmt.Sprintf("c%d", ordinal+1) }

// The request-shaped note: what the model returns, mirroring
// note-request.schema.json. Meaning only; the worker adds the mechanics.

// Quote is a source reference as the model writes it: a chunk handle and a
// span copied verbatim out of that chunk.
type Quote struct {
	Chunk string `json:"chunk"`
	Quote string `json:"quote"`
}

// Block is a Content block.
type Block struct {
	ID           string  `json:"id"`
	Text         string  `json:"text"`
	SpeakerClaim bool    `json:"speaker_claim"`
	SourceRefs   []Quote `json:"source_refs"`
}

// Element is a graph node or a timeline event.
type Element struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Kind         string  `json:"kind,omitempty"`
	Order        *int    `json:"order,omitempty"`
	GroupID      string  `json:"group_id,omitempty"`
	SpeakerClaim bool    `json:"speaker_claim"`
	SourceRefs   []Quote `json:"source_refs"`
}

// Edge joins two nodes; a labelled edge is grounded like an element.
type Edge struct {
	ID           string  `json:"id"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	Label        string  `json:"label,omitempty"`
	Order        *int    `json:"order,omitempty"`
	SpeakerClaim bool    `json:"speaker_claim,omitempty"`
	SourceRefs   []Quote `json:"source_refs,omitempty"`
}

// Group is a collapsed group: one visible anchor hiding its members.
type Group struct {
	ID        string   `json:"id"`
	AnchorID  string   `json:"anchor_id"`
	MemberIDs []string `json:"member_ids"`
}

// Column is a comparison column.
type Column struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Order        int     `json:"order"`
	SpeakerClaim bool    `json:"speaker_claim"`
	SourceRefs   []Quote `json:"source_refs"`
}

// Cell is one comparison cell, naming its column.
type Cell struct {
	ColumnID     string  `json:"column_id"`
	Text         string  `json:"text"`
	SpeakerClaim bool    `json:"speaker_claim"`
	SourceRefs   []Quote `json:"source_refs"`
}

// Row is a comparison row with its cells.
type Row struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Order        int     `json:"order"`
	SpeakerClaim bool    `json:"speaker_claim"`
	SourceRefs   []Quote `json:"source_refs"`
	Cells        []Cell  `json:"cells"`
}

// Diagram is the one diagram of a note, whichever type it is.
type Diagram struct {
	Type            string    `json:"type"`
	Nodes           []Element `json:"nodes,omitempty"`
	Edges           []Edge    `json:"edges,omitempty"`
	Events          []Element `json:"events,omitempty"`
	CollapsedGroups []Group   `json:"collapsed_groups,omitempty"`
	Orientation     string    `json:"orientation,omitempty"`
	Columns         []Column  `json:"columns,omitempty"`
	Rows            []Row     `json:"rows,omitempty"`
}

// The diagram types.
const (
	MindMap     = "mind_map"
	ProcessFlow = "process_flow"
	Timeline    = "timeline"
	Comparison  = "comparison"
)

// Note is the model's whole answer.
type Note struct {
	DetectedLanguage string   `json:"detected_language"`
	PrimaryTopic     string   `json:"primary_topic"`
	Tags             []string `json:"tags"`
	Summary          Block    `json:"summary"`
	KeyPoints        []Block  `json:"key_points"`
	ImportantDetails []Block  `json:"important_details"`
	PracticalActions []Block  `json:"practical_actions"`
	Diagram          Diagram  `json:"diagram"`
}

// elements are the countable elements of a graph or timeline diagram.
func (d *Diagram) elements() []Element {
	if d.Type == Timeline {
		return d.Events
	}
	if d.Type == Comparison {
		return nil
	}
	return d.Nodes
}

// The stored payload: the normative shape of docs/design/diagram.schema.json
// and the contract's RevisionPayload. Everything here is what the worker
// stamped onto the model's meaning.

// SourceRef is a resolved grounding span, chunk-relative in code points.
type SourceRef struct {
	ChunkID   uuid.UUID `json:"chunk_id"`
	CharStart int       `json:"char_start"`
	CharEnd   int       `json:"char_end"`
}

// StoredBlock is a stored Content block.
type StoredBlock struct {
	ID           string      `json:"id"`
	Text         string      `json:"text"`
	Provenance   string      `json:"provenance"`
	SpeakerClaim bool        `json:"speaker_claim"`
	SourceRefs   []SourceRef `json:"source_refs"`
}

// StoredElement is a stored node or event.
type StoredElement struct {
	ID               string      `json:"id"`
	Label            string      `json:"label"`
	Kind             string      `json:"kind,omitempty"`
	Order            *int        `json:"order,omitempty"`
	GroupID          string      `json:"group_id,omitempty"`
	InitiallyVisible bool        `json:"initially_visible"`
	Provenance       string      `json:"provenance"`
	SpeakerClaim     bool        `json:"speaker_claim"`
	SourceRefs       []SourceRef `json:"source_refs"`
}

// StoredEdge is a stored edge; grounding fields travel only with a label.
type StoredEdge struct {
	ID           string      `json:"id"`
	From         string      `json:"from"`
	To           string      `json:"to"`
	Label        string      `json:"label,omitempty"`
	Order        *int        `json:"order,omitempty"`
	Provenance   string      `json:"provenance,omitempty"`
	SpeakerClaim *bool       `json:"speaker_claim,omitempty"`
	SourceRefs   []SourceRef `json:"source_refs,omitempty"`
}

// StoredGroup is a stored collapsed group.
type StoredGroup struct {
	ID                 string   `json:"id"`
	AnchorID           string   `json:"anchor_id"`
	MemberIDs          []string `json:"member_ids"`
	InitiallyCollapsed bool     `json:"initially_collapsed"`
}

// StoredColumn is a stored comparison column.
type StoredColumn struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	Order        int         `json:"order"`
	Provenance   string      `json:"provenance"`
	SpeakerClaim bool        `json:"speaker_claim"`
	SourceRefs   []SourceRef `json:"source_refs"`
}

// StoredCell is a stored comparison cell.
type StoredCell struct {
	Text         string      `json:"text"`
	Provenance   string      `json:"provenance"`
	SpeakerClaim bool        `json:"speaker_claim"`
	SourceRefs   []SourceRef `json:"source_refs"`
}

// StoredRow is a stored comparison row; cells are keyed by column id.
type StoredRow struct {
	ID           string                `json:"id"`
	Label        string                `json:"label"`
	Order        int                   `json:"order"`
	Provenance   string                `json:"provenance"`
	SpeakerClaim bool                  `json:"speaker_claim"`
	SourceRefs   []SourceRef           `json:"source_refs"`
	Cells        map[string]StoredCell `json:"cells"`
}

// GraphLayout names the engine (ADR 0011: a shape, not a library).
type GraphLayout struct {
	Engine    string `json:"engine"`
	Direction string `json:"direction,omitempty"`
}

// LinearLayout is a timeline's orientation.
type LinearLayout struct {
	Orientation string `json:"orientation"`
}

// StoredDiagram is the stored diagram of any type.
type StoredDiagram struct {
	SchemaVersion   int             `json:"schema_version"`
	Type            string          `json:"type"`
	Nodes           []StoredElement `json:"nodes,omitempty"`
	Edges           []StoredEdge    `json:"edges,omitempty"`
	Events          []StoredElement `json:"events,omitempty"`
	CollapsedGroups []StoredGroup   `json:"collapsed_groups,omitempty"`
	Layout          any             `json:"layout,omitempty"`
	Columns         []StoredColumn  `json:"columns,omitempty"`
	Rows            []StoredRow     `json:"rows,omitempty"`
}

// Elements are the stored nodes or events.
func (d *StoredDiagram) Elements() []StoredElement {
	if d.Type == Timeline {
		return d.Events
	}
	return d.Nodes
}

// StoredPayload is one Revision's payload (ADR 0002).
type StoredPayload struct {
	SchemaVersion    int           `json:"schema_version"`
	DetectedLanguage string        `json:"detected_language"`
	PrimaryTopic     string        `json:"primary_topic"`
	Tags             []string      `json:"tags"`
	Summary          StoredBlock   `json:"summary"`
	KeyPoints        []StoredBlock `json:"key_points"`
	ImportantDetails []StoredBlock `json:"important_details"`
	PracticalActions []StoredBlock `json:"practical_actions"`
	Diagram          StoredDiagram `json:"diagram"`
}

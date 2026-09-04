# Diagram v1 semantic rules

[`diagram.schema.json`](./diagram.schema.json) is the normative structural contract. The application must also run the semantic checks below because JSON Schema cannot express cross-item identity, references, ordering, or comparisons between two numeric fields.

## Identity and references

- Element IDs are unique across a Diagram. For graph Diagrams this includes nodes, edges, and collapsed groups; for timelines it includes events and collapsed groups; for comparisons it includes columns and rows.
- Every graph edge `from` and `to` resolves to a node in the same Diagram.
- A mind map has one `root` node and its edges form one directed tree rooted there: every other node has exactly one incoming edge and all nodes are reachable.
- `order` values within process nodes, timeline events, comparison columns, and comparison rows are unique and contiguous from zero.
- Every Source ref has `char_end > char_start`. The referenced Transcript chunk must belong to the Note whose Revision contains the Diagram; that database lookup happens during Revision validation.

## Visibility and collapsed groups

- The generic 12-visible/24-total limit counts graph nodes or timeline events. Structural edges and collapsed-group metadata do not count. A comparison uses its independent limit of four columns by six rows.
- A collapsed group names one visible anchor and one or more hidden members. Its `anchor_id` and every `member_id` resolve to nodes/events in the same Diagram.
- Each hidden node/event belongs to exactly one collapsed group and has `group_id` equal to that group's ID. Visible elements cannot be group members.
- Collapsed content is presentation state only: export, search, and the Text alternative include it.

## Layout

- Mind maps use radial layout; process flows use Dagre; timelines declare horizontal or vertical orientation.
- Graph `positions` may be empty before the first automatic layout and may contain a subset while an edit is in progress. Every stored key must resolve to a node in the Diagram.
- Coordinates are part of the immutable Revision payload. Persisting a layout adjustment therefore creates a manual Revision; the Revision/conflict design owns the compare-and-set behavior.
- Layout never changes semantic order and is ignored by the Text alternative.

## Text alternative derivation

The Text alternative is deterministic derived data and is never stored in a Revision.

- **Mind map:** start at the root and traverse outgoing edges depth-first, sorting by edge `order` and then target ID. Emit one indented line per node; prefix a labeled relationship before the target label.
- **Process flow:** emit nodes by `order`. Under each node, emit its labeled outgoing branches ordered by edge `order` and then edge ID.
- **Timeline:** emit events by `order` as a numbered list.
- **Comparison:** emit the ordered column labels as a header, then each ordered row label and its cells in column order.
- Prefix content marked `speaker_claim` with `Speaker claim:`. Include hidden content. Omit Provenance, Source refs, layout coordinates, and collapsed-group metadata from spoken/rendered text.

The companion [`diagram-semantics.mjs`](./diagram-semantics.mjs) is the executable reference implementation used by example tests and may be ported to Go and TypeScript.

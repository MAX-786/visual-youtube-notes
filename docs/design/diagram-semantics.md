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

- Mind maps use `radial` layout; process flows use `layered`; timelines declare horizontal or vertical orientation. The engine choice is semantic and stays in the Revision payload. It names a shape rather than a library: [ADR 0011](../adr/0011-export-and-library-archive.md) moved both algorithms into Go, which is why the value is `layered` and not `dagre`.
- Coordinates are **not** in the payload. [ADR 0004](../adr/0004-revision-mutation-and-conflict-model.md) hoists them into the mutable `note_diagram_layouts` table, keyed by Note and last-write-wins, so the first automatic layout does not manufacture a manual Revision on merely opening a Note and a position nudge on a second device does not become a Conflict Revision. `additionalProperties: false` on the layout object rejects a payload that still carries them.
- A position is `{x, y, w, h}`: the box, not just its corner. Box size comes from measuring the label, and storing it leaves exactly one implementation of that measurement (ADR 0011).
- The server seeds positions. Whenever a Revision becomes Active, every element in its Diagram is given a position and only the missing ones are computed; the write **merges** into the existing map rather than replacing it, which is what lets a Regeneration's stale coordinates survive long enough for a later restore to reuse them.
- Positions may still be empty or partial for a Diagram the server has not yet laid out. The renderer drops keys that do not resolve to an element in the Diagram it is drawing, which is how coordinates left behind by a Regeneration prune themselves.
- Layout never changes semantic order and is ignored by the Text alternative.

## Text alternative derivation

The Text alternative is deterministic derived data and is never stored in a Revision.

- **Mind map:** start at the root and traverse outgoing edges depth-first, sorting by edge `order` and then target ID. Emit one indented line per node; prefix a labeled relationship before the target label.
- **Process flow:** emit nodes by `order`. Under each node, emit its labeled outgoing branches ordered by edge `order` and then edge ID.
- **Timeline:** emit events by `order` as a numbered list.
- **Comparison:** emit the ordered column labels as a header, then each ordered row label and its cells in column order.
- Prefix content marked `speaker_claim` with `Speaker claim:`. Include hidden content. Omit Provenance, Source refs, layout coordinates, and collapsed-group metadata from spoken/rendered text.

The companion [`diagram-semantics.mjs`](./diagram-semantics.mjs) is the executable reference implementation used by example tests and may be ported to Go and TypeScript.

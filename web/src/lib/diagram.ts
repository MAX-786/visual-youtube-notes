// Diagram helpers shared by the note page and its renderers: the Text
// alternative of docs/design/diagram-semantics.md, derived on the client from
// the stored Diagram exactly as the server derives it for search, and the
// visibility rules for collapsed groups.
import type { components } from "@visual-notes/api-client";

export type Diagram = components["schemas"]["Diagram"];
export type MindMapDiagram = components["schemas"]["MindMapDiagram"];
export type ProcessFlowDiagram = components["schemas"]["ProcessFlowDiagram"];
export type TimelineDiagram = components["schemas"]["TimelineDiagram"];
export type ComparisonDiagram = components["schemas"]["ComparisonDiagram"];
export type GraphDiagram = MindMapDiagram | ProcessFlowDiagram;
export type Position = components["schemas"]["Position"];
export type PositionMap = Record<string, Position>;

type Ordered = { id: string; order?: number | null };

const last = Number.MAX_SAFE_INTEGER;

export function byOrderThenId(a: Ordered, b: Ordered): number {
  return (a.order ?? last) - (b.order ?? last) || a.id.localeCompare(b.id);
}

function spoken(value: {
  speaker_claim?: boolean;
  label?: string;
  text?: string;
}): string {
  return `${value.speaker_claim ? "Speaker claim: " : ""}${value.label ?? value.text ?? ""}`;
}

/** Hidden element ids, keyed to the group that hides them. */
export function groupOf(diagram: Diagram): Map<string, string> {
  const out = new Map<string, string>();
  if (diagram.type === "comparison") return out;
  for (const group of diagram.collapsed_groups) {
    for (const member of group.member_ids) out.set(member, group.id);
  }
  return out;
}

/** Member counts by anchor id, for the "+N" badge on a collapsed anchor. */
export function membersByAnchor(diagram: Diagram): Map<string, number> {
  const out = new Map<string, number>();
  if (diagram.type === "comparison") return out;
  for (const group of diagram.collapsed_groups) {
    out.set(
      group.anchor_id,
      (out.get(group.anchor_id) ?? 0) + group.member_ids.length,
    );
  }
  return out;
}

export function deriveTextAlternative(diagram: Diagram): string {
  if (diagram.type === "mind_map") {
    const root = diagram.nodes.find((node) => node.kind === "root");
    const byId = new Map(diagram.nodes.map((node) => [node.id, node]));
    const outgoing = new Map<string, typeof diagram.edges>();
    for (const node of diagram.nodes) outgoing.set(node.id, []);
    for (const edge of diagram.edges) outgoing.get(edge.from)?.push(edge);
    const lines: string[] = [];
    const visit = (
      node: (typeof diagram.nodes)[number],
      depth: number,
      relationship?: string,
    ) => {
      lines.push(
        `${"  ".repeat(depth)}${relationship ? `${relationship}: ` : ""}${spoken(node)}`,
      );
      for (const edge of [...(outgoing.get(node.id) ?? [])].sort(
        byOrderThenId,
      )) {
        const target = byId.get(edge.to);
        if (target) visit(target, depth + 1, edge.label ?? undefined);
      }
    };
    if (root) visit(root, 0);
    return lines.join("\n");
  }
  if (diagram.type === "process_flow") {
    const byId = new Map(diagram.nodes.map((node) => [node.id, node]));
    const outgoing = new Map<string, typeof diagram.edges>();
    for (const node of diagram.nodes) outgoing.set(node.id, []);
    for (const edge of diagram.edges) outgoing.get(edge.from)?.push(edge);
    return [...diagram.nodes]
      .sort(byOrderThenId)
      .flatMap((node, index) => [
        `${index + 1}. ${spoken(node)}`,
        ...[...(outgoing.get(node.id) ?? [])]
          .filter((edge) => edge.label)
          .sort(byOrderThenId)
          .map((edge) => {
            const target = byId.get(edge.to);
            return `   Branch: ${spoken({ speaker_claim: edge.speaker_claim ?? false, label: edge.label ?? "" })} → ${target ? spoken(target) : ""}`;
          }),
      ])
      .join("\n");
  }
  if (diagram.type === "timeline") {
    return [...diagram.events]
      .sort(byOrderThenId)
      .map((event, index) => `${index + 1}. ${spoken(event)}`)
      .join("\n");
  }
  const columns = [...diagram.columns].sort(byOrderThenId);
  const header = ["Criterion", ...columns.map(spoken)].join(" | ");
  const rows = [...diagram.rows].sort(byOrderThenId).map((row) =>
    [
      spoken(row),
      ...columns.map((column) => {
        const cell = row.cells[column.id];
        return cell ? spoken(cell) : "";
      }),
    ].join(" | "),
  );
  return [header, ...rows].join("\n");
}

/** Greedy word wrap with a measured or estimated width, like the server's. */
export function wrapLabel(
  label: string,
  maxWidth: number,
  measure: (text: string) => number,
): string[] {
  const words = label.split(/\s+/).filter(Boolean);
  if (words.length === 0) return [""];
  const lines: string[] = [];
  let line = "";
  for (const word of words) {
    const candidate = line ? `${line} ${word}` : word;
    if (line && measure(candidate) > maxWidth) {
      lines.push(line);
      line = word;
    } else {
      line = candidate;
    }
  }
  lines.push(line);
  return lines;
}

function byOrderThenId(a, b) {
  return (
    (a.order ?? Number.MAX_SAFE_INTEGER) -
      (b.order ?? Number.MAX_SAFE_INTEGER) || a.id.localeCompare(b.id)
  );
}

function groundedValues(diagram) {
  if (diagram.type === "mind_map" || diagram.type === "process_flow") {
    return [...diagram.nodes, ...diagram.edges.filter((edge) => edge.label)];
  }
  if (diagram.type === "timeline") return diagram.events;
  if (diagram.type === "comparison") {
    return [
      ...diagram.columns,
      ...diagram.rows,
      ...diagram.rows.flatMap((row) => Object.values(row.cells)),
    ];
  }
  return [];
}

function checkUniqueIds(diagram, errors) {
  let elements = [];
  if (diagram.type === "mind_map" || diagram.type === "process_flow") {
    elements = [
      ...diagram.nodes,
      ...diagram.edges,
      ...diagram.collapsed_groups,
    ];
  } else if (diagram.type === "timeline") {
    elements = [...diagram.events, ...diagram.collapsed_groups];
  } else if (diagram.type === "comparison") {
    elements = [...diagram.columns, ...diagram.rows];
  }

  const seen = new Set();
  for (const element of elements) {
    if (seen.has(element.id))
      errors.push(`duplicate element id: ${element.id}`);
    seen.add(element.id);
  }
}

function checkOrders(label, values, errors) {
  const orders = values.map((value) => value.order).sort((a, b) => a - b);
  if (orders.some((order, index) => order !== index)) {
    errors.push(
      `${label} order values must be unique and contiguous from zero`,
    );
  }
}

function checkSourceRefs(diagram, errors) {
  for (const value of groundedValues(diagram)) {
    for (const ref of value.source_refs ?? []) {
      if (ref.char_end <= ref.char_start) {
        errors.push(`source ref char_end must exceed char_start`);
      }
    }
  }
}

function checkCollapsedGroups(elements, groups, errors) {
  const byId = new Map(elements.map((element) => [element.id, element]));
  const claimedMembers = new Set();

  for (const group of groups) {
    const anchor = byId.get(group.anchor_id);
    if (!anchor)
      errors.push(
        `collapsed group ${group.id} has unknown anchor ${group.anchor_id}`,
      );
    else if (!anchor.initially_visible)
      errors.push(`collapsed group ${group.id} anchor must be visible`);

    for (const memberId of group.member_ids) {
      const member = byId.get(memberId);
      if (!member)
        errors.push(
          `collapsed group ${group.id} has unknown member ${memberId}`,
        );
      else {
        if (member.initially_visible)
          errors.push(`collapsed group member ${memberId} must be hidden`);
        if (member.group_id !== group.id)
          errors.push(
            `collapsed group member ${memberId} must name group ${group.id}`,
          );
      }
      if (claimedMembers.has(memberId))
        errors.push(
          `collapsed group member ${memberId} appears more than once`,
        );
      claimedMembers.add(memberId);
    }
  }

  for (const element of elements) {
    if (!element.initially_visible && !claimedMembers.has(element.id)) {
      errors.push(
        `hidden element ${element.id} must belong to a collapsed group`,
      );
    }
    if (element.initially_visible && claimedMembers.has(element.id)) {
      errors.push(
        `visible element ${element.id} cannot be a collapsed group member`,
      );
    }
  }
}

function checkGraph(diagram, errors) {
  const nodeIds = new Set(diagram.nodes.map((node) => node.id));
  for (const edge of diagram.edges) {
    if (!nodeIds.has(edge.from))
      errors.push(`edge ${edge.id} has unknown source ${edge.from}`);
    if (!nodeIds.has(edge.to))
      errors.push(`edge ${edge.id} has unknown target ${edge.to}`);
  }

  for (const id of Object.keys(diagram.layout.positions)) {
    if (!nodeIds.has(id)) errors.push(`layout position has unknown node ${id}`);
  }

  checkCollapsedGroups(diagram.nodes, diagram.collapsed_groups, errors);

  if (diagram.type === "process_flow")
    checkOrders("process node", diagram.nodes, errors);

  if (diagram.type === "mind_map") {
    const root = diagram.nodes.find((node) => node.kind === "root");
    if (!root) return;
    const incoming = new Map(diagram.nodes.map((node) => [node.id, 0]));
    const outgoing = new Map(diagram.nodes.map((node) => [node.id, []]));
    for (const edge of diagram.edges) {
      if (incoming.has(edge.to))
        incoming.set(edge.to, incoming.get(edge.to) + 1);
      if (outgoing.has(edge.from)) outgoing.get(edge.from).push(edge.to);
    }
    for (const node of diagram.nodes) {
      const expected = node.id === root.id ? 0 : 1;
      if (incoming.get(node.id) !== expected)
        errors.push(
          `mind-map node ${node.id} must have ${expected} incoming edge(s)`,
        );
    }
    const visited = new Set();
    const visit = (id) => {
      if (visited.has(id)) return;
      visited.add(id);
      for (const next of outgoing.get(id) ?? []) visit(next);
    };
    visit(root.id);
    for (const node of diagram.nodes) {
      if (!visited.has(node.id))
        errors.push(
          `mind-map node ${node.id} is unreachable from root ${root.id}`,
        );
    }
  }
}

function checkComparison(diagram, errors) {
  checkOrders("comparison column", diagram.columns, errors);
  checkOrders("comparison row", diagram.rows, errors);
  const expected = diagram.columns.map((column) => column.id).sort();
  for (const row of diagram.rows) {
    const actual = Object.keys(row.cells).sort();
    if (
      actual.length !== expected.length ||
      actual.some((id, index) => id !== expected[index])
    ) {
      errors.push(
        `comparison row ${row.id} must contain exactly one cell for every column`,
      );
    }
  }
}

export function validateSemantics(diagram) {
  const errors = [];
  checkUniqueIds(diagram, errors);
  checkSourceRefs(diagram, errors);
  if (diagram.type === "mind_map" || diagram.type === "process_flow")
    checkGraph(diagram, errors);
  if (diagram.type === "timeline") {
    checkOrders("timeline event", diagram.events, errors);
    checkCollapsedGroups(diagram.events, diagram.collapsed_groups, errors);
  }
  if (diagram.type === "comparison") checkComparison(diagram, errors);
  return errors;
}

function spoken(value) {
  return `${value.speaker_claim ? "Speaker claim: " : ""}${value.label ?? value.text}`;
}

export function deriveTextAlternative(diagram) {
  if (diagram.type === "mind_map") {
    const root = diagram.nodes.find((node) => node.kind === "root");
    const byId = new Map(diagram.nodes.map((node) => [node.id, node]));
    const outgoing = new Map(diagram.nodes.map((node) => [node.id, []]));
    for (const edge of diagram.edges) outgoing.get(edge.from)?.push(edge);
    const lines = [];
    const visit = (node, depth, relationship) => {
      lines.push(
        `${"  ".repeat(depth)}${relationship ? `${relationship}: ` : ""}${spoken(node)}`,
      );
      for (const edge of (outgoing.get(node.id) ?? []).sort(byOrderThenId)) {
        const target = byId.get(edge.to);
        if (target) visit(target, depth + 1, edge.label);
      }
    };
    if (root) visit(root, 0);
    return lines.join("\n");
  }

  if (diagram.type === "process_flow") {
    const byId = new Map(diagram.nodes.map((node) => [node.id, node]));
    const outgoing = new Map(diagram.nodes.map((node) => [node.id, []]));
    for (const edge of diagram.edges) outgoing.get(edge.from)?.push(edge);
    return [...diagram.nodes]
      .sort(byOrderThenId)
      .flatMap((node, index) => [
        `${index + 1}. ${spoken(node)}`,
        ...(outgoing.get(node.id) ?? [])
          .filter((edge) => edge.label)
          .sort(byOrderThenId)
          .map(
            (edge) =>
              `   Branch: ${spoken(edge)} → ${spoken(byId.get(edge.to))}`,
          ),
      ])
      .join("\n");
  }

  if (diagram.type === "timeline") {
    return [...diagram.events]
      .sort(byOrderThenId)
      .map((event, index) => `${index + 1}. ${spoken(event)}`)
      .join("\n");
  }

  if (diagram.type === "comparison") {
    const columns = [...diagram.columns].sort(byOrderThenId);
    const header = ["Criterion", ...columns.map(spoken)].join(" | ");
    const rows = [...diagram.rows]
      .sort(byOrderThenId)
      .map((row) =>
        [
          spoken(row),
          ...columns.map((column) => spoken(row.cells[column.id])),
        ].join(" | "),
      );
    return [header, ...rows].join("\n");
  }

  throw new Error(`Unsupported diagram type: ${diagram.type}`);
}

// PROTOTYPE (ticket #15). Pure module — no DOM, no I/O. This is the part meant to survive
// the prototype and be ported to Go in the worker.
//
// The request schema (note-request.schema.json) is all constrained decoding can enforce.
// Everything the spec asks for beyond that lands here: word limits, array count ranges,
// element caps, id shapes, order contiguity, graph structure, output language, and the
// grounding check that a quote really does appear in the chunk it names.
//
// Every finding carries `repairable`. Repairable findings are fixed in process for free.
// Anything left is a content problem only the model can fix, and costs a re-ask.

const LIMITS = {
  summaryWords: 40,
  keyPoints: [5, 7],
  importantDetails: [3, 8],
  practicalActions: [0, 5],
  visibleElements: 12,
  totalElements: 24,
  comparisonColumns: 4,
  comparisonRows: 6,
};

const ID_SHAPE = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/;
const DEVANAGARI = /[ऀ-ॿ]/;

const finding = (code, message, repairable, path) => ({
  code,
  message,
  repairable,
  path,
});

// ---------------------------------------------------------------- grounding

/**
 * Whitespace-tolerant index over a chunk, so a quote that differs from the chunk only in
 * spacing still resolves. Returns the normalized text plus a map back to real offsets.
 */
function buildIndex(text) {
  let norm = "";
  const map = [];
  let prevSpace = false;
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    if (/\s/.test(ch)) {
      if (prevSpace) continue;
      norm += " ";
      map.push(i);
      prevSpace = true;
    } else {
      norm += ch;
      map.push(i);
      prevSpace = false;
    }
  }
  return { norm, map };
}

const normalizeQuote = (q) => q.replace(/\s+/g, " ").trim();

/**
 * Turn {chunk, quote} into {chunk_id, char_start, char_end}. A quote the chunk does not
 * contain is the one hallucination this design can prove, so it is reported, never guessed at.
 */
export function resolveSourceRef(ref, transcript) {
  const chunk = transcript.chunks.find(
    (c) => handleFor(c) === ref.chunk || c.chunk_id === ref.chunk,
  );
  if (!chunk) {
    return { error: finding("unknown_chunk", `no chunk ${ref.chunk}`, false) };
  }
  const { norm, map } = buildIndex(chunk.text);
  const needle = normalizeQuote(ref.quote);
  if (!needle) {
    return { error: finding("empty_quote", "quote is empty", false) };
  }
  const at = norm.indexOf(needle);
  if (at === -1) {
    return {
      error: finding(
        "quote_not_found",
        `quote not present in ${ref.chunk}: ${JSON.stringify(ref.quote.slice(0, 48))}`,
        false,
      ),
    };
  }
  return {
    resolved: {
      chunk_id: chunk.chunk_id,
      char_start: map[at],
      char_end: map[at + needle.length - 1] + 1,
    },
  };
}

export const handleFor = (chunk) => `c${chunk.index + 1}`;

// ------------------------------------------------------------ diagram shape

function diagramElements(diagram) {
  if (!diagram) return [];
  if (diagram.type === "timeline") return diagram.events ?? [];
  if (diagram.type === "comparison") return [];
  return diagram.nodes ?? [];
}

function groundedParts(diagram) {
  // Everything that carries source_refs, paired with a readable path.
  const out = [];
  if (!diagram) return out;
  if (diagram.type === "comparison") {
    (diagram.columns ?? []).forEach((c, i) =>
      out.push([c, `diagram.columns[${i}]`]),
    );
    (diagram.rows ?? []).forEach((r, i) => {
      out.push([r, `diagram.rows[${i}]`]);
      (r.cells ?? []).forEach((cell, j) =>
        out.push([cell, `diagram.rows[${i}].cells[${j}]`]),
      );
    });
    return out;
  }
  diagramElements(diagram).forEach((e, i) => out.push([e, `diagram[${i}]`]));
  (diagram.edges ?? []).forEach((e, i) => {
    if (e.label) out.push([e, `diagram.edges[${i}]`]);
  });
  return out;
}

const memberIds = (diagram) =>
  new Set((diagram?.collapsed_groups ?? []).flatMap((g) => g.member_ids ?? []));

// ----------------------------------------------------------------- validate

export function validateNote(note, transcript) {
  const findings = [];
  const add = (...args) => findings.push(finding(...args));

  // --- counts and lengths the request schema cannot express
  const words = (note.summary?.text ?? "").split(/\s+/).filter(Boolean).length;
  if (words > LIMITS.summaryWords) {
    add(
      "summary_too_long",
      `summary is ${words} words, limit ${LIMITS.summaryWords}`,
      false,
      "summary",
    );
  }
  const ranges = [
    ["key_points", note.key_points, LIMITS.keyPoints],
    ["important_details", note.important_details, LIMITS.importantDetails],
    ["practical_actions", note.practical_actions, LIMITS.practicalActions],
  ];
  for (const [name, arr, [lo, hi]] of ranges) {
    const n = (arr ?? []).length;
    // Too few is content only the model can supply. Too many in practical_actions is a
    // safe truncation because the spec caps it; elsewhere truncation would drop a point
    // the model judged worth making, so it is re-asked instead.
    if (n < lo) add(`${name}_too_few`, `${n} < ${lo}`, false, name);
    if (n > hi) {
      add(
        `${name}_too_many`,
        `${n} > ${hi}`,
        name === "practical_actions",
        name,
      );
    }
  }

  // --- output language
  const noteText = [
    note.summary?.text ?? "",
    ...(note.key_points ?? []).map((b) => b.text),
    ...(note.important_details ?? []).map((b) => b.text),
    ...(note.practical_actions ?? []).map((b) => b.text),
    note.primary_topic ?? "",
    ...diagramElements(note.diagram).map((e) => e.label ?? ""),
  ].join(" ");
  if (DEVANAGARI.test(noteText)) {
    add(
      "not_english",
      "note text is not English; the transcript script leaked into the note",
      false,
      "note",
    );
  }

  // --- grounding
  const blocks = [
    ["summary", note.summary],
    ...(note.key_points ?? []).map((b, i) => [`key_points[${i}]`, b]),
    ...(note.important_details ?? []).map((b, i) => [
      `important_details[${i}]`,
      b,
    ]),
    ...(note.practical_actions ?? []).map((b, i) => [
      `practical_actions[${i}]`,
      b,
    ]),
    ...groundedParts(note.diagram).map(([el, path]) => [path, el]),
  ];
  for (const [path, item] of blocks) {
    if (!item) continue;
    const refs = item.source_refs ?? [];
    if (refs.length === 0) {
      add("missing_source_refs", "AI content without grounding", false, path);
      continue;
    }
    for (const ref of refs) {
      const { error } = resolveSourceRef(ref, transcript);
      if (error) findings.push({ ...error, path });
    }
  }

  validateDiagram(note.diagram, findings);
  return findings;
}

function validateDiagram(diagram, findings) {
  const add = (...args) => findings.push(finding(...args));
  if (!diagram) {
    add("missing_diagram", "no diagram", false, "diagram");
    return;
  }

  if (diagram.type === "comparison") {
    const cols = diagram.columns ?? [];
    const rows = diagram.rows ?? [];
    if (cols.length > LIMITS.comparisonColumns) {
      add(
        "comparison_too_wide",
        `${cols.length} columns > ${LIMITS.comparisonColumns}`,
        false,
        "diagram.columns",
      );
    }
    if (rows.length > LIMITS.comparisonRows) {
      add(
        "comparison_too_tall",
        `${rows.length} rows > ${LIMITS.comparisonRows}`,
        false,
        "diagram.rows",
      );
    }
    const colIds = new Set(cols.map((c) => c.id));
    rows.forEach((r, i) => {
      const seen = new Set((r.cells ?? []).map((c) => c.column_id));
      for (const cell of r.cells ?? []) {
        if (!colIds.has(cell.column_id)) {
          add(
            "cell_unknown_column",
            `cell names missing column ${cell.column_id}`,
            false,
            `diagram.rows[${i}]`,
          );
        }
      }
      for (const id of colIds) {
        if (!seen.has(id)) {
          add(
            "row_missing_cell",
            `row has no cell for column ${id}`,
            false,
            `diagram.rows[${i}]`,
          );
        }
      }
    });
    checkOrders("columns", cols, findings);
    checkOrders("rows", rows, findings);
    checkIds([...cols, ...rows], findings);
    return;
  }

  const elements = diagramElements(diagram);
  const hidden = memberIds(diagram);
  const visible = elements.filter((e) => !hidden.has(e.id));
  if (elements.length > LIMITS.totalElements) {
    add(
      "too_many_total",
      `${elements.length} elements > ${LIMITS.totalElements}`,
      false,
      "diagram",
    );
  } else if (visible.length > LIMITS.visibleElements) {
    // Repairable: the spec already says overflow is grouped into collapsed branches, so
    // the worker can do that deterministically without asking the model again.
    add(
      "too_many_visible",
      `${visible.length} visible > ${LIMITS.visibleElements}`,
      true,
      "diagram",
    );
  }

  // group integrity
  const byId = new Map(elements.map((e) => [e.id, e]));
  for (const g of diagram.collapsed_groups ?? []) {
    if (!byId.has(g.anchor_id)) {
      add(
        "group_anchor_missing",
        `anchor ${g.anchor_id} is not an element`,
        false,
        `group ${g.id}`,
      );
    }
    if (hidden.has(g.anchor_id)) {
      add(
        "group_anchor_hidden",
        `anchor ${g.anchor_id} is also a member`,
        false,
        `group ${g.id}`,
      );
    }
    for (const m of g.member_ids ?? []) {
      const el = byId.get(m);
      if (!el) {
        add(
          "group_member_missing",
          `member ${m} is not an element`,
          false,
          `group ${g.id}`,
        );
      } else if (el.group_id !== g.id) {
        // Membership is the source of truth; stamping group_id from it is safe.
        add(
          "group_id_mismatch",
          `${m} is in ${g.id} but group_id is ${el.group_id ?? "unset"}`,
          true,
          `group ${g.id}`,
        );
      }
    }
  }

  checkIds([...elements, ...(diagram.edges ?? [])], findings);

  if (diagram.type === "timeline") {
    checkOrders("events", elements, findings);
    return;
  }

  // graph: edges must resolve
  for (const e of diagram.edges ?? []) {
    if (!byId.has(e.from)) {
      add(
        "edge_endpoint_missing",
        `edge ${e.id} from ${e.from}`,
        false,
        "diagram.edges",
      );
    }
    if (!byId.has(e.to)) {
      add(
        "edge_endpoint_missing",
        `edge ${e.id} to ${e.to}`,
        false,
        "diagram.edges",
      );
    }
    if (e.label && (e.source_refs ?? []).length === 0) {
      add(
        "labelled_edge_ungrounded",
        `edge ${e.id} has a label but no grounding`,
        false,
        "diagram.edges",
      );
    }
  }

  if (diagram.type === "process_flow") {
    checkOrders("nodes", elements, findings);
    return;
  }

  // mind map must be one tree rooted at the single root node
  const roots = elements.filter((n) => n.kind === "root");
  if (roots.length !== 1) {
    add(
      "mind_map_root_count",
      `${roots.length} root nodes, expected 1`,
      false,
      "diagram",
    );
    return;
  }
  const incoming = new Map(elements.map((e) => [e.id, 0]));
  for (const e of diagram.edges ?? []) {
    if (incoming.has(e.to)) incoming.set(e.to, incoming.get(e.to) + 1);
  }
  for (const [id, n] of incoming) {
    const expected = id === roots[0].id ? 0 : 1;
    if (n !== expected) {
      add(
        "mind_map_not_tree",
        `${id} has ${n} incoming edges, expected ${expected}`,
        false,
        "diagram",
      );
    }
  }
  const seen = new Set([roots[0].id]);
  const queue = [roots[0].id];
  while (queue.length) {
    const cur = queue.shift();
    for (const e of diagram.edges ?? []) {
      if (e.from === cur && !seen.has(e.to)) {
        seen.add(e.to);
        queue.push(e.to);
      }
    }
  }
  if (seen.size !== elements.length) {
    add(
      "mind_map_unreachable",
      `${elements.length - seen.size} node(s) unreachable from root`,
      false,
      "diagram",
    );
  }
}

function checkIds(items, findings) {
  const seen = new Set();
  for (const item of items) {
    if (!ID_SHAPE.test(item.id ?? "")) {
      findings.push(
        finding(
          "bad_element_id",
          `id ${JSON.stringify(item.id)} is not a valid id`,
          true,
          item.id,
        ),
      );
    }
    if (seen.has(item.id)) {
      findings.push(
        finding("duplicate_element_id", `id ${item.id} repeats`, true, item.id),
      );
    }
    seen.add(item.id);
  }
}

function checkOrders(label, items, findings) {
  const orders = items.map((i) => i.order);
  const expected = items.map((_, i) => i);
  const sorted = [...orders].sort((a, b) => a - b);
  if (JSON.stringify(sorted) !== JSON.stringify(expected)) {
    findings.push(
      finding(
        "order_not_contiguous",
        `${label} order is ${JSON.stringify(orders)}, expected a permutation of 0..${items.length - 1}`,
        true,
        `diagram.${label}`,
      ),
    );
  }
}

// ------------------------------------------------------------------ repairs

/** Deterministic, content-preserving fixes. Returns a new note plus what was done. */
export function repairNote(input, findings) {
  const note = structuredClone(input);
  const applied = [];
  const codes = new Set(
    findings.filter((f) => f.repairable).map((f) => f.code),
  );

  if (codes.has("practical_actions_too_many")) {
    const dropped = note.practical_actions.length - LIMITS.practicalActions[1];
    note.practical_actions = note.practical_actions.slice(
      0,
      LIMITS.practicalActions[1],
    );
    applied.push(`dropped ${dropped} practical action(s) beyond the cap of 5`);
  }

  if (codes.has("bad_element_id") || codes.has("duplicate_element_id")) {
    applied.push(...renameIds(note.diagram));
  }

  if (codes.has("order_not_contiguous")) {
    applied.push(...renumber(note.diagram));
  }

  if (codes.has("group_id_mismatch")) {
    for (const g of note.diagram.collapsed_groups ?? []) {
      for (const m of g.member_ids ?? []) {
        const el = diagramElements(note.diagram).find((e) => e.id === m);
        if (el) el.group_id = g.id;
      }
    }
    applied.push("stamped group_id from collapsed-group membership");
  }

  if (codes.has("too_many_visible")) {
    applied.push(...autoCollapse(note.diagram));
  }

  return { note, applied };
}

function renameIds(diagram) {
  const applied = [];
  const elements = diagramElements(diagram);
  const all = [
    ...elements,
    ...(diagram.edges ?? []),
    ...(diagram.columns ?? []),
    ...(diagram.rows ?? []),
  ];
  const taken = new Set();
  const rewrite = new Map();
  all.forEach((item, i) => {
    let id = item.id ?? "";
    if (!ID_SHAPE.test(id) || taken.has(id)) {
      const base = (id || "e").replace(/[^A-Za-z0-9_-]/g, "") || "e";
      let next = /^[A-Za-z]/.test(base) ? base : `e${base}`;
      while (taken.has(next)) next = `${next}_${i}`;
      rewrite.set(id, next);
      applied.push(`renamed id ${JSON.stringify(id)} to ${next}`);
      item.id = next;
      id = next;
    }
    taken.add(id);
  });
  if (rewrite.size) {
    for (const e of diagram.edges ?? []) {
      if (rewrite.has(e.from)) e.from = rewrite.get(e.from);
      if (rewrite.has(e.to)) e.to = rewrite.get(e.to);
    }
    for (const g of diagram.collapsed_groups ?? []) {
      if (rewrite.has(g.anchor_id)) g.anchor_id = rewrite.get(g.anchor_id);
      g.member_ids = (g.member_ids ?? []).map((m) => rewrite.get(m) ?? m);
    }
    for (const r of diagram.rows ?? []) {
      for (const c of r.cells ?? []) {
        if (rewrite.has(c.column_id)) c.column_id = rewrite.get(c.column_id);
      }
    }
  }
  return applied;
}

function renumber(diagram) {
  const groups =
    diagram.type === "comparison"
      ? [
          ["columns", diagram.columns ?? []],
          ["rows", diagram.rows ?? []],
        ]
      : [
          [
            diagram.type === "timeline" ? "events" : "nodes",
            diagramElements(diagram),
          ],
        ];
  const applied = [];
  for (const [label, items] of groups) {
    const before = items.map((i) => i.order);
    [...items]
      .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
      .forEach((item, i) => {
        item.order = i;
      });
    const after = items.map((i) => i.order);
    if (JSON.stringify(before) !== JSON.stringify(after)) {
      applied.push(
        `renumbered ${label} order to 0..${items.length - 1}, sequence preserved`,
      );
    }
  }
  return applied;
}

/** Spec 7.2: overflow is grouped into collapsed branches. Keep the first 12, hide the rest. */
function autoCollapse(diagram) {
  const elements = diagramElements(diagram);
  const hidden = memberIds(diagram);
  const visible = elements.filter((e) => !hidden.has(e.id));
  const overflow = visible.slice(LIMITS.visibleElements - 1);
  const anchor = visible[LIMITS.visibleElements - 2];
  if (!anchor || overflow.length === 0) return [];
  const id = `grp_overflow`;
  for (const el of overflow) el.group_id = id;
  diagram.collapsed_groups = [
    ...(diagram.collapsed_groups ?? []),
    { id, anchor_id: anchor.id, member_ids: overflow.map((e) => e.id) },
  ];
  return [
    `collapsed ${overflow.length} overflow element(s) behind ${anchor.id}`,
  ];
}

// ------------------------------------------------------- the attempt machine

export const OUTCOME = {
  READY: "ready",
  REASK: "re-ask",
  FAILED: "failed",
};

/**
 * One pass of spec 8.3 steps 3-4. `maxReasks` is the in-process budget and is deliberately
 * NOT the River attempt ladder: a schema-invalid response is a permanent validation failure
 * under spec 8.4, so re-asking here must not consume the 30s/2min/10min transient budget.
 */
export function runValidationCycle(
  note,
  transcript,
  { reasksUsed = 0, maxReasks = 1 } = {},
) {
  const first = validateNote(note, transcript);
  const steps = [{ stage: "validate", findings: first }];
  if (first.length === 0) {
    return { outcome: OUTCOME.READY, note, steps, reasksUsed };
  }

  const { note: repaired, applied } = repairNote(note, first);
  if (applied.length) steps.push({ stage: "repair", applied });

  const second = applied.length ? validateNote(repaired, transcript) : first;
  if (applied.length) steps.push({ stage: "revalidate", findings: second });

  if (second.length === 0) {
    return { outcome: OUTCOME.READY, note: repaired, steps, reasksUsed };
  }
  if (reasksUsed < maxReasks) {
    return {
      outcome: OUTCOME.REASK,
      note: repaired,
      steps,
      reasksUsed: reasksUsed + 1,
      // Fed back to the model verbatim; the cached prefix means a re-ask is mostly cache reads.
      feedback: second.map((f) => `${f.path}: ${f.message}`),
    };
  }
  return {
    outcome: OUTCOME.FAILED,
    note: repaired,
    steps,
    reasksUsed,
    findings: second,
  };
}

// --------------------------------------------------- request-to-storage map

/** Proves the request schema maps onto the normative diagram.schema.json shape. */
export function toStoredPayload(note, transcript) {
  const stamp = (item) => {
    const refs = (item.source_refs ?? [])
      .map((r) => resolveSourceRef(r, transcript).resolved)
      .filter(Boolean);
    return {
      provenance: "ai",
      speaker_claim: item.speaker_claim === true,
      source_refs: refs,
    };
  };
  const d = note.diagram;
  const hidden = memberIds(d);
  const groups = (d.collapsed_groups ?? []).map((g) => ({
    ...g,
    initially_collapsed: true,
  }));

  let diagram;
  if (d.type === "comparison") {
    diagram = {
      schema_version: 1,
      type: "comparison",
      columns: d.columns.map((c) => ({
        id: c.id,
        label: c.label,
        order: c.order,
        ...stamp(c),
      })),
      rows: d.rows.map((r) => ({
        id: r.id,
        label: r.label,
        order: r.order,
        ...stamp(r),
        cells: Object.fromEntries(
          (r.cells ?? []).map((c) => [
            c.column_id,
            { text: c.text, ...stamp(c) },
          ]),
        ),
      })),
    };
  } else if (d.type === "timeline") {
    diagram = {
      schema_version: 1,
      type: "timeline",
      events: d.events.map((e) => ({
        id: e.id,
        label: e.label,
        order: e.order,
        ...(e.group_id ? { group_id: e.group_id } : {}),
        initially_visible: !hidden.has(e.id),
        ...stamp(e),
      })),
      collapsed_groups: groups,
      layout: { orientation: d.orientation ?? "vertical" },
    };
  } else {
    diagram = {
      schema_version: 1,
      type: d.type,
      nodes: d.nodes.map((n) => ({
        id: n.id,
        label: n.label,
        ...(d.type === "mind_map" ? { kind: n.kind } : { order: n.order }),
        ...(n.group_id ? { group_id: n.group_id } : {}),
        initially_visible: !hidden.has(n.id),
        ...stamp(n),
      })),
      edges: d.edges.map((e) => ({
        id: e.id,
        from: e.from,
        to: e.to,
        ...(e.label ? { label: e.label, ...stamp(e) } : {}),
        ...(e.order !== undefined ? { order: e.order } : {}),
      })),
      collapsed_groups: groups,
      // Positions stay empty until the client runs its first automatic layout.
      layout: {
        engine: d.type === "mind_map" ? "radial" : "dagre",
        positions: {},
      },
    };
  }

  const block = (b) => ({ id: b.id, text: b.text, ...stamp(b) });
  return {
    schema_version: 1,
    detected_language: note.detected_language,
    primary_topic: note.primary_topic,
    tags: note.tags ?? [],
    summary: block(note.summary),
    key_points: (note.key_points ?? []).map(block),
    important_details: (note.important_details ?? []).map(block),
    practical_actions: (note.practical_actions ?? []).map(block),
    diagram,
  };
}

export { LIMITS };

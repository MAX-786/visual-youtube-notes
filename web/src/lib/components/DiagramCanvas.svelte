<script lang="ts">
  // Mind maps and process flows: an SVG drawn from the server's boxes
  // (ADR 0011), with pan and zoom, collapsed groups behind their anchors,
  // and selectable text. The client never lays out; it renders coordinates.
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import {
    groupOf,
    membersByAnchor,
    wrapLabel,
    type GraphDiagram,
    type Position,
    type PositionMap,
  } from "$lib/diagram";

  let {
    diagram,
    positions,
  }: { diagram: GraphDiagram; positions: PositionMap } = $props();

  const LINE = 20;
  const PAD_X = 14;
  const PAD_Y = 10;
  const MARGIN = 32;

  let expanded = new SvelteSet<string>();
  let scale = $state(1);
  let tx = $state(0);
  let ty = $state(0);
  let host = $state<HTMLDivElement | null>(null);

  const hidden = $derived(groupOf(diagram));
  const badges = $derived(membersByAnchor(diagram));
  const isFlow = $derived(diagram.type === "process_flow");

  const visibleNodes = $derived(
    diagram.nodes.filter((node) => {
      if (!positions[node.id]) return false;
      const group = hidden.get(node.id);
      return node.initially_visible || (group ? expanded.has(group) : false);
    }),
  );
  const visibleIds = $derived(new SvelteSet(visibleNodes.map((n) => n.id)));
  const edges = $derived(
    diagram.edges.filter((e) => visibleIds.has(e.from) && visibleIds.has(e.to)),
  );
  const bounds = $derived.by(() => {
    let minX = Infinity;
    let minY = Infinity;
    let maxX = -Infinity;
    let maxY = -Infinity;
    for (const node of visibleNodes) {
      const p = positions[node.id];
      minX = Math.min(minX, p.x);
      minY = Math.min(minY, p.y);
      maxX = Math.max(maxX, p.x + p.w);
      maxY = Math.max(maxY, p.y + p.h);
    }
    if (!Number.isFinite(minX)) return { x: 0, y: 0, w: 320, h: 200 };
    return {
      x: minX - MARGIN,
      y: minY - MARGIN,
      w: maxX - minX + 2 * MARGIN,
      h: maxY - minY + 2 * MARGIN,
    };
  });

  let context: CanvasRenderingContext2D | null = null;
  function measure(text: string): number {
    if (!context && typeof document !== "undefined") {
      context = document.createElement("canvas").getContext("2d");
      if (context) context.font = '14px "Noto Sans", system-ui, sans-serif';
    }
    return context ? context.measureText(text).width : text.length * 7.7;
  }
  function lines(label: string, box: Position): string[] {
    return wrapLabel(label, box.w - 2 * PAD_X, measure);
  }

  function center(p: Position) {
    return { x: p.x + p.w / 2, y: p.y + p.h / 2 };
  }
  /** Where a segment towards `to` leaves the box. */
  function exitPoint(box: Position, to: { x: number; y: number }) {
    const c = center(box);
    const dx = to.x - c.x;
    const dy = to.y - c.y;
    if (dx === 0 && dy === 0) return c;
    const sx = box.w / 2 / Math.max(Math.abs(dx), 1e-9);
    const sy = box.h / 2 / Math.max(Math.abs(dy), 1e-9);
    const t = Math.min(sx, sy);
    return { x: c.x + dx * t, y: c.y + dy * t };
  }
  function segment(fromId: string, toId: string) {
    const a = positions[fromId];
    const b = positions[toId];
    const start = exitPoint(a, center(b));
    const end = exitPoint(b, center(a));
    return {
      start,
      end,
      mid: { x: (start.x + end.x) / 2, y: (start.y + end.y) / 2 },
    };
  }

  function toggle(nodeId: string) {
    for (const group of diagram.collapsed_groups) {
      if (group.anchor_id !== nodeId) continue;
      if (expanded.has(group.id)) expanded.delete(group.id);
      else expanded.add(group.id);
    }
  }

  function zoomBy(factor: number) {
    scale = Math.min(4, Math.max(0.25, scale * factor));
  }
  function fit() {
    scale = 1;
    tx = 0;
    ty = 0;
  }

  // Pan with one pointer, pinch with two. Coordinates are in viewBox units,
  // so movement is divided by the on-screen scale of the SVG.
  const pointers = new SvelteMap<number, { x: number; y: number }>();
  let pinchDistance = 0;
  function unitsPerPixel(): number {
    if (!host) return 1;
    return bounds.w / host.clientWidth;
  }
  // Capture only once a drag has begun, so a plain tap still reaches the
  // badge or node underneath it.
  let captured = false;
  function onPointerDown(event: PointerEvent) {
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (pointers.size === 2) pinchDistance = distance();
  }
  function distance(): number {
    const [a, b] = [...pointers.values()];
    return Math.hypot(a.x - b.x, a.y - b.y);
  }
  function onPointerMove(event: PointerEvent) {
    const previous = pointers.get(event.pointerId);
    if (!previous) return;
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (!captured) {
      captured = true;
      (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
    }
    if (pointers.size === 1) {
      const k = unitsPerPixel();
      tx += (event.clientX - previous.x) * k;
      ty += (event.clientY - previous.y) * k;
    } else if (pointers.size === 2) {
      const d = distance();
      if (pinchDistance > 0) zoomBy(d / pinchDistance);
      pinchDistance = d;
    }
  }
  function onPointerUp(event: PointerEvent) {
    pointers.delete(event.pointerId);
    pinchDistance = 0;
    if (pointers.size === 0) captured = false;
  }
  function onWheel(event: WheelEvent) {
    event.preventDefault();
    zoomBy(event.deltaY < 0 ? 1.1 : 0.9);
  }
</script>

<div
  class="canvas"
  bind:this={host}
  role="application"
  aria-label={isFlow ? "Process flow diagram" : "Mind map diagram"}
  onpointerdown={onPointerDown}
  onpointermove={onPointerMove}
  onpointerup={onPointerUp}
  onpointercancel={onPointerUp}
  onwheel={onWheel}
>
  <svg viewBox={`${bounds.x} ${bounds.y} ${bounds.w} ${bounds.h}`}>
    <defs>
      <marker
        id="arrow"
        viewBox="0 0 10 10"
        refX="9"
        refY="5"
        markerWidth="8"
        markerHeight="8"
        orient="auto-start-reverse"
      >
        <path d="M 0 0 L 10 5 L 0 10 z" class="arrowhead" />
      </marker>
    </defs>
    <g transform={`translate(${tx} ${ty}) scale(${scale})`}>
      {#each edges as edge (edge.id)}
        {@const s = segment(edge.from, edge.to)}
        <line
          x1={s.start.x}
          y1={s.start.y}
          x2={s.end.x}
          y2={s.end.y}
          class="edge"
          marker-end={isFlow ? "url(#arrow)" : undefined}
        />
        {#if edge.label}
          <text
            x={s.mid.x}
            y={s.mid.y}
            class="edge-label"
            class:claim={edge.speaker_claim}
            text-anchor="middle"
            dominant-baseline="middle">{edge.label}</text
          >
        {/if}
      {/each}
      {#each visibleNodes as node (node.id)}
        {@const p = positions[node.id]}
        {@const badge = badges.get(node.id) ?? 0}
        <g
          class="node"
          class:root={"kind" in node && node.kind === "root"}
          class:claim={node.speaker_claim}
          class:anchor={badge > 0}
          data-element-id={node.id}
        >
          <rect x={p.x} y={p.y} width={p.w} height={p.h} rx="8" />
          <text
            x={p.x + p.w / 2}
            y={p.y + PAD_Y}
            text-anchor="middle"
            dominant-baseline="hanging"
          >
            {#each lines(node.label, p) as line, i (i)}
              <tspan x={p.x + p.w / 2} dy={i === 0 ? 0 : LINE}>{line}</tspan>
            {/each}
          </text>
          {#if badge > 0}
            <g
              class="badge"
              role="button"
              tabindex="0"
              aria-label={`Show ${badge} more`}
              onclick={() => toggle(node.id)}
              onkeydown={(e) => {
                if (e.key === "Enter" || e.key === " ") toggle(node.id);
              }}
            >
              <circle cx={p.x + p.w} cy={p.y} r="11" />
              <text
                x={p.x + p.w}
                y={p.y}
                text-anchor="middle"
                dominant-baseline="middle"
                >{hidden.size &&
                diagram.collapsed_groups.some(
                  (g) => g.anchor_id === node.id && expanded.has(g.id),
                )
                  ? "−"
                  : `+${badge}`}</text
              >
            </g>
          {/if}
        </g>
      {/each}
    </g>
  </svg>
  <div class="controls" role="group" aria-label="Zoom">
    <button type="button" onclick={() => zoomBy(1.25)} aria-label="Zoom in"
      >+</button
    >
    <button type="button" onclick={() => zoomBy(0.8)} aria-label="Zoom out"
      >−</button
    >
    <button type="button" onclick={fit}>Fit</button>
  </div>
</div>

<style>
  .canvas {
    position: relative;
    width: 100%;
    height: min(70vh, 640px);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background:
      radial-gradient(var(--line) 1px, transparent 1px) 0 0 / 24px 24px,
      var(--surface);
    touch-action: none;
    overflow: hidden;
    cursor: grab;
  }
  .canvas:active {
    cursor: grabbing;
  }
  svg {
    width: 100%;
    height: 100%;
    font-family: "Noto Sans", system-ui, sans-serif;
    font-size: 14px;
  }
  .edge {
    stroke: var(--muted);
    stroke-width: 1.5;
    fill: none;
  }
  .arrowhead {
    fill: var(--muted);
  }
  .edge-label {
    fill: var(--muted);
    font-size: 12px;
    paint-order: stroke;
    stroke: var(--surface);
    stroke-width: 4px;
  }
  .node rect {
    fill: var(--surface);
    stroke: var(--line);
    stroke-width: 1.5;
  }
  .node.root rect {
    fill: var(--accent);
    stroke: var(--accent);
  }
  .node.root text {
    fill: var(--accent-ink);
    font-weight: 600;
  }
  .node text {
    fill: var(--ink);
  }
  .node.claim rect {
    stroke-dasharray: 5 4;
  }
  .node.anchor {
    cursor: pointer;
  }
  .badge circle {
    fill: var(--accent);
  }
  .badge text {
    fill: var(--accent-ink);
    font-size: 12px;
    font-weight: 600;
  }
  .controls {
    position: absolute;
    right: 0.5rem;
    bottom: 0.5rem;
    display: flex;
    gap: 0.25rem;
  }
  .controls button {
    min-height: 36px;
    min-width: 36px;
    padding: 0.25rem 0.6rem;
  }
</style>

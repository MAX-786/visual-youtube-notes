<script lang="ts">
  import {
    Background,
    BackgroundVariant,
    Controls,
    MarkerType,
    Panel,
    Position as FlowPosition,
    SvelteFlow,
    type Connection,
    type Edge,
    type Node,
  } from "@xyflow/svelte";
  import "@xyflow/svelte/dist/style.css";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import type { components } from "@visual-notes/api-client";
  import { elementId } from "$lib/uuid";
  import {
    groupOf,
    membersByAnchor,
    type GraphDiagram,
    type PositionMap,
  } from "$lib/diagram";
  import GraphEdge from "./GraphEdge.svelte";
  import GraphNode from "./GraphNode.svelte";
  import type { GraphEdgeData, GraphNodeData } from "./graph-types";

  type MutationOp = components["schemas"]["MutationOp"];

  let {
    diagram,
    positions,
    editable = false,
    query = "",
    caseSensitive = false,
    onmove,
    emit,
  }: {
    diagram: GraphDiagram;
    positions: PositionMap;
    editable?: boolean;
    query?: string;
    caseSensitive?: boolean;
    onmove?: (positions: PositionMap) => void;
    emit?: (ops: MutationOp[]) => void;
  } = $props();

  const nodeTypes = { noteNode: GraphNode };
  const edgeTypes = { noteEdge: GraphEdge };
  let expanded = new SvelteSet<string>();
  let nodes = $state.raw<Node[]>([]);
  let edges = $state.raw<Edge[]>([]);
  let connectionMessage = $state<string | null>(null);

  const hidden = $derived(groupOf(diagram));
  const badges = $derived(membersByAnchor(diagram));
  const isFlow = $derived(diagram.type === "process_flow");

  function endpoints(): { target: FlowPosition; source: FlowPosition } {
    switch (diagram.layout.direction ?? "TB") {
      case "BT":
        return { target: FlowPosition.Bottom, source: FlowPosition.Top };
      case "LR":
        return { target: FlowPosition.Left, source: FlowPosition.Right };
      case "RL":
        return { target: FlowPosition.Right, source: FlowPosition.Left };
      default:
        return { target: FlowPosition.Top, source: FlowPosition.Bottom };
    }
  }

  function isExpanded(nodeId: string): boolean {
    return diagram.collapsed_groups.some(
      (group) => group.anchor_id === nodeId && expanded.has(group.id),
    );
  }

  function toggle(nodeId: string): void {
    for (const group of diagram.collapsed_groups) {
      if (group.anchor_id !== nodeId) continue;
      if (expanded.has(group.id)) expanded.delete(group.id);
      else expanded.add(group.id);
    }
  }

  function commitLabel(id: string, label: string): void {
    emit?.([{ op: "set_element_label", element_id: id, label }]);
  }

  function removeNode(id: string): void {
    if (
      !confirm(
        isFlow
          ? "Delete this step and its connections?"
          : "Delete this branch and everything beneath it?",
      )
    )
      return;
    emit?.([{ op: "delete_element", element_id: id }]);
  }

  function removeEdge(id: string): void {
    if (!confirm("Delete this connection?")) return;
    emit?.([{ op: "delete_element", element_id: id }]);
  }

  function addAfter(parentId: string): void {
    const id = elementId(isFlow ? "step" : "node");
    emit?.([
      {
        op: "insert_element",
        collection: "nodes",
        id,
        label: isFlow ? "New step" : "New branch",
        parent_id: parentId,
      },
    ]);
  }

  function wouldCycle(source: string, target: string): boolean {
    if (source === target) return true;
    const outgoing = new SvelteMap<string, string[]>();
    for (const edge of diagram.edges) {
      const list = outgoing.get(edge.from) ?? [];
      list.push(edge.to);
      outgoing.set(edge.from, list);
    }
    const queue = [target];
    const seen = new SvelteSet<string>();
    while (queue.length) {
      const current = queue.shift()!;
      if (current === source) return true;
      if (seen.has(current)) continue;
      seen.add(current);
      queue.push(...(outgoing.get(current) ?? []));
    }
    return false;
  }

  function connect(connection: Connection): void {
    if (!editable || !isFlow || !connection.source || !connection.target)
      return;
    const duplicate = diagram.edges.some(
      (edge) =>
        edge.from === connection.source && edge.to === connection.target,
    );
    if (duplicate || wouldCycle(connection.source, connection.target)) {
      connectionMessage = duplicate
        ? "That connection already exists."
        : "That connection would create a cycle, so it was not added.";
      return;
    }
    connectionMessage = null;
    emit?.([
      {
        op: "insert_element",
        collection: "edges",
        id: elementId("edge"),
        from: connection.source,
        to: connection.target,
      },
    ]);
  }

  function dragged(event: { nodes: Node[] }): void {
    const changes: PositionMap = {};
    for (const node of event.nodes) {
      const old = positions[node.id];
      if (!old) continue;
      changes[node.id] = {
        x: Math.round(node.position.x),
        y: Math.round(node.position.y),
        w: old.w,
        h: old.h,
      };
    }
    if (Object.keys(changes).length) onmove?.(changes);
  }

  $effect(() => {
    const direction = endpoints();
    const shown = diagram.nodes.filter((node) => {
      if (!positions[node.id]) return false;
      const group = hidden.get(node.id);
      return node.initially_visible || (group ? expanded.has(group) : false);
    });
    const shownIds = new Set(shown.map((node) => node.id));

    nodes = shown.map((item) => {
      const position = positions[item.id];
      const data: GraphNodeData = {
        label: item.label,
        root: "kind" in item && item.kind === "root",
        claim: item.speaker_claim,
        editable,
        addable:
          diagram.nodes.filter((node) => node.initially_visible).length < 12 &&
          diagram.nodes.length < 24,
        deletable:
          !("kind" in item && item.kind === "root") &&
          diagram.nodes.length > (isFlow ? 2 : 1),
        connectable: editable && isFlow,
        addLabel: isFlow ? "Add next step" : "Add branch",
        hiddenCount: badges.get(item.id) ?? 0,
        expanded: isExpanded(item.id),
        query,
        caseSensitive,
        onCommit: commitLabel,
        onDelete: removeNode,
        onAdd: addAfter,
        onExpand: toggle,
      };
      return {
        id: item.id,
        type: "noteNode",
        position: { x: position.x, y: position.y },
        sourcePosition: direction.source,
        targetPosition: direction.target,
        data,
        style: `width: ${Math.max(position.w, 150)}px; min-height: ${Math.max(position.h, 54)}px`,
        draggable: editable,
        selectable: true,
        focusable: true,
        ariaLabel: `${item.label}${editable ? ", editable diagram element" : ""}`,
        domAttributes: { "data-element-id": item.id },
      } satisfies Node;
    });

    edges = diagram.edges
      .filter((item) => shownIds.has(item.from) && shownIds.has(item.to))
      .map((item) => {
        const data: GraphEdgeData = {
          editable,
          deletable: editable && isFlow,
          query,
          caseSensitive,
          onCommit: commitLabel,
          onDelete: removeEdge,
        };
        return {
          id: item.id,
          source: item.from,
          target: item.to,
          label: item.label ?? "",
          type: "noteEdge",
          data,
          selectable: editable,
          focusable: editable,
          markerEnd: isFlow ? { type: MarkerType.ArrowClosed } : undefined,
          ariaLabel: item.label ? `Connection: ${item.label}` : "Connection",
        } satisfies Edge;
      });
  });
</script>

<div
  class:editable
  class="canvas"
  role="application"
  aria-label={isFlow ? "Process flow diagram" : "Mind map diagram"}
>
  <SvelteFlow
    bind:nodes
    bind:edges
    {nodeTypes}
    {edgeTypes}
    fitView
    fitViewOptions={{ padding: 0.2 }}
    minZoom={0.25}
    maxZoom={2.5}
    nodesDraggable={editable}
    nodesConnectable={editable && isFlow}
    elementsSelectable
    deleteKey={null}
    selectionKey={editable ? "Shift" : null}
    preventScrolling={false}
    zoomOnScroll={false}
    zoomOnPinch
    panOnDrag
    onconnect={connect}
    onnodedragstop={dragged}
    colorMode="system"
    colorModeSSR="light"
    proOptions={{ hideAttribution: true }}
  >
    <Background variant={BackgroundVariant.Dots} gap={22} size={1} />
    <Controls showLock={false} position="bottom-right" />
    {#if editable}
      <Panel position="top-left" class="edit-hint">
        <strong>Direct edit</strong>
        <span
          >{isFlow
            ? "Edit text, drag steps, or pull a handle to connect."
            : "Edit text, drag branches, or select one to add below."}</span
        >
      </Panel>
    {/if}
    {#if connectionMessage}
      <Panel position="bottom-center" class="connection-message">
        <span role="status">{connectionMessage}</span>
      </Panel>
    {/if}
  </SvelteFlow>
</div>

<style>
  .canvas {
    width: 100%;
    height: clamp(32rem, 68vh, 46rem);
    overflow: hidden;
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
    background: var(--surface);
  }
  .canvas.editable {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  :global(.svelte-flow) {
    --xy-background-color: var(--surface);
    --xy-edge-stroke: var(--line-strong);
    --xy-edge-stroke-selected: var(--accent);
    --xy-connectionline-stroke: var(--accent);
    --xy-controls-button-background-color: var(--surface-raised);
    --xy-controls-button-background-color-hover: var(--surface-muted);
    --xy-controls-button-color: var(--ink);
    --xy-controls-button-border-color: var(--line);
    --xy-minimap-background-color: var(--surface-raised);
  }
  :global(.svelte-flow__node-noteNode) {
    border: 0;
    background: transparent;
  }
  :global(.svelte-flow__node.selected .node-card) {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px var(--accent-soft);
  }
  :global(.svelte-flow__edge-path) {
    stroke-width: 1.6;
  }
  :global(.edit-hint) {
    display: grid;
    max-width: 19rem;
    padding: 0.55rem 0.7rem;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: color-mix(in oklch, var(--surface-raised) 92%, transparent);
    box-shadow: var(--shadow);
    color: var(--muted);
    font-size: 0.75rem;
    backdrop-filter: blur(10px);
  }
  :global(.edit-hint strong) {
    color: var(--accent);
  }
  :global(.connection-message) {
    padding: 0.45rem 0.7rem;
    border-radius: var(--radius-sm);
    background: var(--danger-soft);
    color: var(--danger);
    font-size: 0.78rem;
    font-weight: 600;
  }
  @media (max-width: 620px) {
    .canvas {
      height: 31rem;
    }
  }
</style>

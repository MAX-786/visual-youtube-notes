<script lang="ts">
  import {
    BaseEdge,
    EdgeLabel,
    getSmoothStepPath,
    type EdgeProps,
  } from "@xyflow/svelte";
  import Highlight from "./Highlight.svelte";
  import type { GraphEdgeData } from "./graph-types";

  let {
    id,
    label = "",
    data,
    selected,
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    markerEnd,
  }: EdgeProps = $props();

  let edge = $derived(data as unknown as GraphEdgeData);
  let path = $derived(
    getSmoothStepPath({
      sourceX,
      sourceY,
      targetX,
      targetY,
      sourcePosition,
      targetPosition,
      borderRadius: 14,
    }),
  );

  function commit(input: HTMLInputElement): void {
    const value = input.value.trim();
    if (value === String(label ?? "")) return;
    edge.onCommit(id, value);
  }
</script>

<BaseEdge path={path[0]} {markerEnd} />

{#if label || (edge.editable && selected)}
  <EdgeLabel x={path[1]} y={path[2]} selectEdgeOnClick class="edge-label-wrap">
    {#if edge.editable && selected}
      <div class="edge-editor nodrag nopan nowheel">
        <input
          type="text"
          value={String(label ?? "")}
          aria-label="Edge label"
          placeholder="Relationship"
          onblur={(event) => commit(event.currentTarget)}
          onkeydown={(event) => {
            if (event.key === "Enter") event.currentTarget.blur();
          }}
        />
        {#if edge.deletable}<button
            type="button"
            class="danger small"
            aria-label="Delete edge"
            onclick={() => edge.onDelete(id)}>×</button
          >{/if}
      </div>
    {:else if label}
      <span class="edge-label"
        ><Highlight
          text={String(label)}
          query={edge.query}
          caseSensitive={edge.caseSensitive}
        /></span
      >
    {/if}
  </EdgeLabel>
{/if}

<style>
  :global(.edge-label-wrap) {
    pointer-events: all;
  }
  .edge-label {
    padding: 0.15rem 0.38rem;
    border: 1px solid var(--line);
    border-radius: 4px;
    background: var(--surface);
    color: var(--muted);
    font-size: 0.72rem;
  }
  .edge-editor {
    display: flex;
    gap: 0.25rem;
    padding: 0.28rem;
    border: 1px solid var(--accent);
    border-radius: var(--radius-sm);
    background: var(--surface-raised);
    box-shadow: var(--shadow);
  }
  input {
    width: 9rem;
    min-height: 34px;
    font-size: 0.78rem;
  }
</style>

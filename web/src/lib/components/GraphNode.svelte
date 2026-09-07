<script lang="ts">
  import {
    Handle,
    NodeToolbar,
    Position,
    type NodeProps,
  } from "@xyflow/svelte";
  import Highlight from "./Highlight.svelte";
  import type { GraphNodeData } from "./graph-types";

  let {
    id,
    data,
    selected,
    targetPosition = Position.Top,
    sourcePosition = Position.Bottom,
  }: NodeProps = $props();

  let node = $derived(data as unknown as GraphNodeData);

  function commit(input: HTMLInputElement): void {
    const label = input.value.trim();
    if (!label || label === node.label) return;
    node.onCommit(id, label);
  }

  function keydown(event: KeyboardEvent): void {
    const input = event.currentTarget as HTMLInputElement;
    if (event.key === "Enter") input.blur();
    if (event.key === "Escape") {
      input.value = node.label;
      input.blur();
    }
  }
</script>

<Handle
  type="target"
  position={targetPosition}
  isConnectable={node.connectable}
/>

<div
  class:root={node.root}
  class:claim={node.claim}
  class:editing={node.editable}
  class="node-card"
>
  {#if node.editable}
    <input
      class="nodrag nopan nowheel"
      type="text"
      value={node.label}
      aria-label={`Diagram element ${node.label}`}
      onblur={(event) => commit(event.currentTarget)}
      onkeydown={keydown}
    />
  {:else}
    <span
      ><Highlight
        text={node.label}
        query={node.query}
        caseSensitive={node.caseSensitive}
      /></span
    >
  {/if}

  {#if node.hiddenCount > 0}
    <button
      type="button"
      class="group-count nodrag nopan"
      aria-label={node.expanded
        ? "Hide grouped elements"
        : `Show ${node.hiddenCount} more`}
      onclick={(event) => {
        event.stopPropagation();
        node.onExpand(id);
      }}>{node.expanded ? "−" : `+${node.hiddenCount}`}</button
    >
  {/if}
</div>

{#if node.editable}
  <NodeToolbar
    isVisible={selected}
    position={Position.Top}
    offset={10}
    class="node-toolbar"
  >
    <button
      type="button"
      class="small nodrag nopan"
      disabled={!node.addable}
      title={node.addable
        ? undefined
        : "The diagram is at its visible element limit"}
      onclick={() => node.onAdd(id)}>{node.addLabel}</button
    >
    {#if node.deletable}
      <button
        type="button"
        class="small danger nodrag nopan"
        onclick={() => node.onDelete(id)}>Delete</button
      >
    {/if}
  </NodeToolbar>
{/if}

<Handle
  type="source"
  position={sourcePosition}
  isConnectable={node.connectable}
/>

<style>
  .node-card {
    position: relative;
    display: grid;
    place-items: center;
    width: 100%;
    min-width: 150px;
    min-height: 54px;
    padding: 0.7rem 0.8rem;
    border: 1px solid var(--line-strong);
    border-radius: 10px 10px 10px 3px;
    background: color-mix(in oklch, var(--surface-raised) 96%, transparent);
    color: var(--ink);
    box-shadow: 0 8px 22px oklch(0.2 0.02 175 / 0.07);
    text-align: center;
    font-size: 0.88rem;
    line-height: 1.3;
  }
  .node-card.root {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-ink);
    font-weight: 700;
  }
  .node-card.claim {
    border-style: dashed;
  }
  input {
    min-width: 0;
    min-height: 34px;
    padding: 0.32rem 0.45rem;
    border-color: color-mix(in oklch, var(--line-strong) 70%, transparent);
    background: color-mix(in oklch, var(--surface) 88%, transparent);
    text-align: center;
    font-size: inherit;
  }
  .root input {
    color: var(--ink);
  }
  .group-count {
    position: absolute;
    top: -10px;
    right: -10px;
    min-width: 27px;
    min-height: 27px;
    padding: 0.1rem;
    border: 2px solid var(--surface);
    border-radius: 50%;
    background: var(--accent);
    color: var(--accent-ink);
    font-size: 0.7rem;
  }
  :global(.node-toolbar) {
    display: flex;
    gap: 0.35rem;
    padding: 0.3rem;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--surface-raised);
    box-shadow: var(--shadow);
  }
  :global(.svelte-flow__handle) {
    width: 9px;
    height: 9px;
    border: 2px solid var(--surface);
    background: var(--accent);
  }
</style>

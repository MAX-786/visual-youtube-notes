<script lang="ts">
  import { SvelteSet } from "svelte/reactivity";
  import type { components } from "@visual-notes/api-client";
  import {
    byOrderThenId,
    groupOf,
    membersByAnchor,
    type TimelineDiagram,
  } from "$lib/diagram";
  import { elementId } from "$lib/uuid";
  import Highlight from "./Highlight.svelte";

  type MutationOp = components["schemas"]["MutationOp"];

  let {
    diagram,
    editable = false,
    query = "",
    caseSensitive = false,
    emit,
  }: {
    diagram: TimelineDiagram;
    editable?: boolean;
    query?: string;
    caseSensitive?: boolean;
    emit?: (ops: MutationOp[]) => void;
  } = $props();

  let expanded = new SvelteSet<string>();
  const hidden = $derived(groupOf(diagram));
  const badges = $derived(membersByAnchor(diagram));
  const allEvents = $derived([...diagram.events].sort(byOrderThenId));
  const events = $derived(
    allEvents.filter((event) => {
      const group = hidden.get(event.id);
      return event.initially_visible || (group ? expanded.has(group) : false);
    }),
  );

  function toggle(anchorId: string): void {
    for (const group of diagram.collapsed_groups) {
      if (group.anchor_id !== anchorId) continue;
      if (expanded.has(group.id)) expanded.delete(group.id);
      else expanded.add(group.id);
    }
  }
  function label(id: string, input: HTMLInputElement): void {
    const value = input.value.trim();
    if (value)
      emit?.([{ op: "set_element_label", element_id: id, label: value }]);
  }
  function add(): void {
    emit?.([
      {
        op: "insert_element",
        collection: "events",
        id: elementId("event"),
        label: "New event",
      },
    ]);
  }
  function remove(id: string): void {
    if (allEvents.length <= 1 || !confirm("Delete this event?")) return;
    emit?.([{ op: "delete_element", element_id: id }]);
  }
  function move(id: string, amount: number): void {
    const from = allEvents.findIndex((event) => event.id === id);
    const to = from + amount;
    if (from < 0 || to < 0 || to >= allEvents.length) return;
    const reordered = [...allEvents];
    [reordered[from], reordered[to]] = [reordered[to], reordered[from]];
    emit?.([
      {
        op: "reorder_elements",
        collection: "events",
        ids: reordered.map((event) => event.id),
      },
    ]);
  }
</script>

{#if editable}
  <div class="timeline-tools">
    <span>Edit events in place</span>
    <button
      class="small"
      type="button"
      disabled={allEvents.length >= 24}
      onclick={add}>Add event</button
    >
  </div>
{/if}

<ol
  class:editable
  class:horizontal={diagram.layout.orientation === "horizontal"}
  class="timeline"
  aria-label="Timeline"
>
  {#each events as event (event.id)}
    {@const badge = badges.get(event.id) ?? 0}
    {@const index = allEvents.findIndex((item) => item.id === event.id)}
    <li class:claim={event.speaker_claim} data-element-id={event.id}>
      <span class="marker" aria-hidden="true"></span>
      {#if editable}
        <input
          type="text"
          value={event.label}
          aria-label={`Timeline event ${index + 1}`}
          onblur={(inputEvent) => label(event.id, inputEvent.currentTarget)}
          onkeydown={(keyEvent) => {
            if (keyEvent.key === "Enter") keyEvent.currentTarget.blur();
          }}
        />
        <div class="event-tools">
          <button
            class="ghost small"
            type="button"
            aria-label="Move event earlier"
            disabled={index === 0}
            onclick={() => move(event.id, -1)}>←</button
          >
          <button
            class="ghost small"
            type="button"
            aria-label="Move event later"
            disabled={index === allEvents.length - 1}
            onclick={() => move(event.id, 1)}>→</button
          >
          <button
            class="ghost danger small"
            type="button"
            aria-label="Delete event"
            disabled={allEvents.length <= 1}
            onclick={() => remove(event.id)}>×</button
          >
        </div>
      {:else}
        <span class="label"
          ><Highlight text={event.label} {query} {caseSensitive} /></span
        >
      {/if}
      {#if event.speaker_claim}<span class="claim-note">speaker claim</span
        >{/if}
      {#if badge > 0}
        <button
          type="button"
          class="more small"
          onclick={() => toggle(event.id)}
        >
          {diagram.collapsed_groups.some(
            (group) => group.anchor_id === event.id && expanded.has(group.id),
          )
            ? "Hide grouped"
            : `+${badge} more`}
        </button>
      {/if}
    </li>
  {/each}
</ol>

<style>
  .timeline-tools {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.65rem;
    color: var(--muted);
    font-size: 0.8rem;
  }
  .timeline {
    display: flex;
    flex-direction: column;
    gap: 0;
    margin: 0;
    padding: 0 0 0 1.4rem;
    border-left: 1px solid var(--line-strong);
    list-style: none;
  }
  .timeline.horizontal {
    flex-direction: row;
    gap: 0.8rem;
    overflow-x: auto;
    padding: 1.4rem 0 0;
    border-top: 1px solid var(--line-strong);
    border-left: 0;
  }
  .timeline.editable {
    padding: 1rem;
    border: 1px solid var(--accent);
    border-radius: var(--radius);
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  li {
    position: relative;
    display: grid;
    grid-template-columns: minmax(10rem, 1fr) auto;
    align-items: center;
    gap: 0.5rem;
    min-width: 15rem;
    padding: 0.8rem;
    border-bottom: 1px solid var(--line);
    background: color-mix(in oklch, var(--surface) 92%, transparent);
  }
  li:last-child {
    border-bottom: 0;
  }
  li.claim {
    border-style: dashed;
  }
  .horizontal li {
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
  }
  .marker {
    position: absolute;
    left: -1.72rem;
    top: 1.15rem;
    width: 9px;
    height: 9px;
    border: 2px solid var(--surface);
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }
  .horizontal .marker {
    left: 1rem;
    top: -1.72rem;
  }
  .editable .marker {
    display: none;
  }
  .label {
    font-weight: 600;
  }
  .claim-note {
    color: var(--muted);
    font-size: 0.72rem;
    font-style: italic;
  }
  .event-tools {
    display: flex;
    gap: 0.15rem;
  }
  .event-tools button {
    min-width: 32px;
    min-height: 32px;
    padding: 0.2rem;
  }
  .more {
    justify-self: start;
  }
</style>

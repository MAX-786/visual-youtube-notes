<script lang="ts">
  // A timeline is an ordered list, horizontal or vertical as the payload
  // says; hidden events sit behind their anchor until it is expanded.
  import { SvelteSet } from "svelte/reactivity";
  import {
    byOrderThenId,
    groupOf,
    membersByAnchor,
    type TimelineDiagram,
  } from "$lib/diagram";

  let { diagram }: { diagram: TimelineDiagram } = $props();

  let expanded = new SvelteSet<string>();
  const hidden = $derived(groupOf(diagram));
  const badges = $derived(membersByAnchor(diagram));
  const events = $derived(
    [...diagram.events].sort(byOrderThenId).filter((event) => {
      const group = hidden.get(event.id);
      return event.initially_visible || (group ? expanded.has(group) : false);
    }),
  );

  function toggle(anchorId: string) {
    for (const group of diagram.collapsed_groups) {
      if (group.anchor_id !== anchorId) continue;
      if (expanded.has(group.id)) expanded.delete(group.id);
      else expanded.add(group.id);
    }
  }
</script>

<ol
  class="timeline"
  class:horizontal={diagram.layout.orientation === "horizontal"}
  aria-label="Timeline"
>
  {#each events as event (event.id)}
    {@const badge = badges.get(event.id) ?? 0}
    <li class:claim={event.speaker_claim} data-element-id={event.id}>
      <span class="marker" aria-hidden="true"></span>
      <span class="label">{event.label}</span>
      {#if event.speaker_claim}<span class="badge">speaker claim</span>{/if}
      {#if badge > 0}
        <button type="button" class="more" onclick={() => toggle(event.id)}>
          {diagram.collapsed_groups.some(
            (g) => g.anchor_id === event.id && expanded.has(g.id),
          )
            ? "hide"
            : `+${badge} more`}
        </button>
      {/if}
    </li>
  {/each}
</ol>

<style>
  .timeline {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    border-left: 2px solid var(--line);
    padding-left: 1rem;
  }
  .timeline.horizontal {
    flex-direction: row;
    overflow-x: auto;
    border-left: 0;
    border-top: 2px solid var(--line);
    padding: 1rem 0 0;
  }
  li {
    position: relative;
    min-width: 12rem;
    padding: 0.5rem 0.75rem;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  li.claim {
    border-style: dashed;
  }
  .marker {
    position: absolute;
    left: -1.45rem;
    top: 0.9rem;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--accent);
  }
  .horizontal .marker {
    left: 0.9rem;
    top: -1.45rem;
  }
  .more {
    min-height: 32px;
    padding: 0.1rem 0.6rem;
    margin-left: 0.5rem;
    font-size: 0.85rem;
  }
</style>

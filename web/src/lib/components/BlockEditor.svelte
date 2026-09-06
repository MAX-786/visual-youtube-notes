<script lang="ts">
  // Edits one Content block section: text with a two-second debounce and a
  // flush on blur and unmount, insert, delete and reorder. The component only
  // emits operations; Provenance is the server's to change (ADR 0004).
  import { onDestroy, untrack } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import type { components } from "@visual-notes/api-client";
  import { elementId } from "$lib/uuid";

  type ContentBlock = components["schemas"]["ContentBlock"];
  type MutationOp = components["schemas"]["MutationOp"];
  type Section =
    "summary" | "key_points" | "important_details" | "practical_actions";

  let {
    title,
    section,
    blocks,
    emit,
    minimum = 0,
  }: {
    title: string;
    section: Section;
    blocks: ContentBlock[];
    emit: (ops: MutationOp[]) => void;
    /** Blocks the section must keep (one key point, the summary). */
    minimum?: number;
  } = $props();

  const DEBOUNCE_MS = 2000;
  const prefix: Record<Section, string> = {
    summary: "sum",
    key_points: "kp",
    important_details: "d",
    practical_actions: "pa",
  };

  let items = $state<ContentBlock[]>(
    untrack(() => structuredClone($state.snapshot(blocks))),
  );
  const dirty = new SvelteSet<string>();
  const timers = new SvelteMap<string, ReturnType<typeof setTimeout>>();

  // Take the server's version whenever it changes and nothing is mid-edit.
  $effect(() => {
    const incoming = blocks;
    if (dirty.size === 0 && timers.size === 0) {
      items = structuredClone($state.snapshot(incoming));
    }
  });

  function flush(id: string) {
    const timer = timers.get(id);
    if (!timer) return;
    clearTimeout(timer);
    timers.delete(id);
    const block = items.find((b) => b.id === id);
    if (!block) return;
    const text = block.text.trim();
    dirty.delete(id);
    if (!text) return;
    emit([
      section === "summary"
        ? { op: "set_block_text", section: "summary", text }
        : { op: "set_block_text", section, id, text },
    ]);
  }

  function typed(id: string) {
    dirty.add(id);
    const existing = timers.get(id);
    if (existing) clearTimeout(existing);
    timers.set(
      id,
      setTimeout(() => flush(id), DEBOUNCE_MS),
    );
  }

  function insertAfter(afterId: string | null) {
    if (section === "summary") return;
    const id = elementId(prefix[section]);
    const block: ContentBlock = {
      id,
      text: "New point",
      provenance: "user_edited",
      speaker_claim: false,
      source_refs: [],
    };
    const at = afterId ? items.findIndex((b) => b.id === afterId) + 1 : 0;
    items = [...items.slice(0, at), block, ...items.slice(at)];
    emit([
      { op: "insert_block", section, id, text: block.text, after_id: afterId },
    ]);
  }

  function remove(id: string) {
    if (section === "summary" || items.length <= minimum) return;
    if (!confirm("Delete this point?")) return;
    const timer = timers.get(id);
    if (timer) clearTimeout(timer);
    timers.delete(id);
    dirty.delete(id);
    items = items.filter((b) => b.id !== id);
    emit([{ op: "delete_block", section, id }]);
  }

  let reorderTimer: ReturnType<typeof setTimeout> | null = null;
  function move(id: string, delta: number) {
    if (section === "summary") return;
    const from = items.findIndex((b) => b.id === id);
    const to = from + delta;
    if (from < 0 || to < 0 || to >= items.length) return;
    const next = [...items];
    [next[from], next[to]] = [next[to], next[from]];
    items = next;
    if (reorderTimer) clearTimeout(reorderTimer);
    reorderTimer = setTimeout(() => {
      reorderTimer = null;
      emit([{ op: "reorder_blocks", section, ids: items.map((b) => b.id) }]);
    }, DEBOUNCE_MS);
  }

  export function flushAll() {
    for (const id of [...timers.keys()]) flush(id);
    if (reorderTimer && section !== "summary") {
      clearTimeout(reorderTimer);
      reorderTimer = null;
      emit([{ op: "reorder_blocks", section, ids: items.map((b) => b.id) }]);
    }
  }

  onDestroy(flushAll);
</script>

<section class="card stack" data-section={section}>
  <div class="row between">
    <h2>{title}</h2>
    {#if section !== "summary"}
      <button type="button" onclick={() => insertAfter(null)}>Add at top</button
      >
    {/if}
  </div>
  {#each items as block, i (block.id)}
    <div class="block" data-element-id={block.id}>
      <textarea
        rows={section === "summary" ? 3 : 2}
        aria-label={`${title} ${i + 1}`}
        bind:value={block.text}
        oninput={() => typed(block.id)}
        onblur={() => flush(block.id)}></textarea>
      <div class="row tools">
        <span class="muted small">
          {block.provenance === "user_edited"
            ? "edited by you"
            : "from the video"}
          {#if block.speaker_claim}
            · speaker claim{/if}
        </span>
        {#if section !== "summary"}
          <button
            type="button"
            aria-label="Move up"
            onclick={() => move(block.id, -1)}>↑</button
          >
          <button
            type="button"
            aria-label="Move down"
            onclick={() => move(block.id, 1)}>↓</button
          >
          <button type="button" onclick={() => insertAfter(block.id)}
            >Add below</button
          >
          <button
            type="button"
            class="danger"
            disabled={items.length <= minimum}
            onclick={() => remove(block.id)}>Delete</button
          >
        {/if}
      </div>
    </div>
  {/each}
</section>

<style>
  .between {
    justify-content: space-between;
  }
  h2 {
    margin: 0;
  }
  .block {
    display: grid;
    gap: 0.35rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
  }
  .tools {
    gap: 0.4rem;
    justify-content: flex-end;
  }
  .tools button {
    min-height: 36px;
    padding: 0.2rem 0.6rem;
  }
  .small {
    font-size: 0.85rem;
    margin-right: auto;
  }
</style>

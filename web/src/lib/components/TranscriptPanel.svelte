<script lang="ts">
  import { tick } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import { getTranscript, putTranscript, type Transcript } from "$lib/db";
  import Highlight from "./Highlight.svelte";

  let {
    noteId,
    query = "",
    caseSensitive = false,
    targetChunkId = null,
    openInitially = false,
  }: {
    noteId: string;
    query?: string;
    caseSensitive?: boolean;
    targetChunkId?: string | null;
    openInitially?: boolean;
  } = $props();

  let open = $state(false);
  let transcript = $state<Transcript | null>(null);
  let loading = $state(false);
  let message = $state<string | null>(null);
  let root: HTMLDetailsElement;

  function chunkText(value: Transcript, start: number, end: number): string {
    return Array.from(value.text).slice(start, end).join("");
  }

  async function revealTarget(): Promise<void> {
    if (!targetChunkId) return;
    await tick();
    const target = root?.querySelector<HTMLElement>(
      `[data-chunk-id="${CSS.escape(targetChunkId)}"]`,
    );
    target?.scrollIntoView({ behavior: "smooth", block: "center" });
    target?.classList.add("search-arrival");
  }

  async function load(): Promise<void> {
    if (transcript || loading) {
      await revealTarget();
      return;
    }
    loading = true;
    message = null;
    const cached = await getTranscript(noteId);
    if (cached) {
      transcript = cached;
      loading = false;
      await revealTarget();
      return;
    }

    const { data, error } = await api.GET("/notes/{noteId}/transcript", {
      params: { path: { noteId } },
    });
    loading = false;
    if (!data) {
      message = errorMessage(error, "The transcript could not be loaded.");
      return;
    }
    transcript = data;
    await putTranscript(data);
    await revealTarget();
  }

  function toggle(event: Event): void {
    open =
      event.currentTarget instanceof HTMLDetailsElement &&
      event.currentTarget.open;
    if (open) void load();
  }

  $effect(() => {
    if (openInitially && !open) open = true;
    if (openInitially) void load();
  });
</script>

<details class="transcript-panel" bind:this={root} bind:open ontoggle={toggle}>
  <summary>
    <span class="summary-copy">
      <svg viewBox="0 0 24 24" aria-hidden="true"
        ><path d="M5 5h14v11H9l-4 3V5Z" /><path d="M8 9h8M8 12h5" /></svg
      >
      <span
        ><strong>Transcript</strong><small
          >Load the source captions only when you need them</small
        ></span
      >
    </span>
    <svg class="chevron" viewBox="0 0 24 24" aria-hidden="true"
      ><path d="m8 10 4 4 4-4" /></svg
    >
  </summary>

  <div class="transcript-body">
    {#if loading}
      <p class="muted" aria-live="polite">Loading transcript…</p>
    {:else if message}
      <div class="load-error">
        <p class="error" role="alert">{message}</p>
        <button class="small" type="button" onclick={load}>Try again</button>
      </div>
    {:else if transcript}
      <div class="transcript-meta">
        <span>{transcript.language_code.toUpperCase()}</span>
        <span>{transcript.source_kind.replaceAll("_", " ")}</span>
        <span>{transcript.chunks.length} sections</span>
      </div>
      <ol>
        {#each transcript.chunks as chunk (chunk.id)}
          <li data-chunk-id={chunk.id}>
            <span class="ordinal"
              >{String(chunk.ordinal + 1).padStart(2, "0")}</span
            >
            <p>
              <Highlight
                text={chunkText(transcript, chunk.char_start, chunk.char_end)}
                {query}
                {caseSensitive}
              />
            </p>
          </li>
        {/each}
      </ol>
    {/if}
  </div>
</details>

<style>
  .transcript-panel {
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }
  summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    min-height: 72px;
    list-style: none;
    color: var(--ink);
  }
  summary::-webkit-details-marker {
    display: none;
  }
  .summary-copy {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .summary-copy > svg {
    width: 23px;
    fill: none;
    stroke: var(--accent);
    stroke-width: 1.6;
  }
  .summary-copy span {
    display: grid;
  }
  small {
    color: var(--muted);
    font-weight: 400;
  }
  .chevron {
    width: 20px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.8;
    transition: transform 180ms ease;
  }
  details[open] .chevron {
    transform: rotate(180deg);
  }
  .transcript-body {
    padding: 0 0 1.2rem;
  }
  .transcript-meta {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 0.8rem;
  }
  .transcript-meta span {
    padding: 0.16rem 0.45rem;
    border-radius: 4px;
    background: var(--surface-muted);
    color: var(--muted);
    font-size: 0.72rem;
  }
  ol {
    display: grid;
    gap: 0;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: grid;
    grid-template-columns: 2.4rem 1fr;
    gap: 0.8rem;
    padding: 0.72rem 0.45rem;
    border-top: 1px solid color-mix(in oklch, var(--line) 60%, transparent);
    border-radius: var(--radius-sm);
  }
  li:first-child {
    border-top: 0;
  }
  :global(li.search-arrival) {
    background: var(--accent-soft);
  }
  .ordinal {
    padding-top: 0.13rem;
    color: var(--faint);
    font-size: 0.72rem;
    font-variant-numeric: tabular-nums;
  }
  li p {
    margin: 0;
  }
  .load-error {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .load-error p {
    margin: 0;
  }
</style>

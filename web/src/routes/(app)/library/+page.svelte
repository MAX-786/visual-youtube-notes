<script lang="ts">
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { SvelteMap, SvelteURLSearchParams } from "svelte/reactivity";
  import Highlight from "$lib/components/Highlight.svelte";
  import NoteCardView from "$lib/components/NoteCard.svelte";
  import { api, errorMessage } from "$lib/api";
  import type { NoteCard } from "$lib/db";
  import type { components } from "@visual-notes/api-client";

  type SearchResult = components["schemas"]["SearchResult"];
  type Reason = components["schemas"]["SemanticUnavailableReason"];
  type Topic = components["schemas"]["Topic"];

  const kindLabels: Record<string, string> = {
    metadata: "title or channel",
    summary: "summary",
    key_point: "key point",
    diagram: "diagram",
    takeaway: "takeaway",
    important_detail: "detail",
    practical_action: "action",
    transcript_chunk: "transcript",
  };

  let q = $state("");
  let caseSensitive = $state(false);
  let from = $state("");
  let to = $state("");
  let topicId = $state("");
  let channelId = $state("");
  let minMinutes = $state("");
  let maxMinutes = $state("");
  let cards = $state<NoteCard[]>([]);
  let results = $state<SearchResult[]>([]);
  let nextCursor = $state<string | null>(null);
  let exhausted = $state(false);
  let unavailable = $state<Reason | null>(null);
  let topics = $state<Topic[]>([]);
  let channels = $state<{ id: string; name: string }[]>([]);
  let message = $state<string | null>(null);
  let loading = $state(false);
  let searched = $state(false);
  let filtersOpen = $state(false);
  let debounce: ReturnType<typeof setTimeout> | undefined;
  let requestSerial = 0;

  const searching = $derived(q.trim().length > 0);
  const activeFilters = $derived(Object.keys(filters()).length);

  function readUrl(): void {
    const params = page.url.searchParams;
    q = params.get("q") ?? "";
    caseSensitive = params.get("case_sensitive") === "1";
    from = params.get("from") ?? "";
    to = params.get("to") ?? "";
    topicId = params.get("topic_id") ?? "";
    channelId = params.get("channel_id") ?? "";
    minMinutes = params.get("min_duration")
      ? String(Number(params.get("min_duration")) / 60)
      : "";
    maxMinutes = params.get("max_duration")
      ? String(Number(params.get("max_duration")) / 60)
      : "";
    filtersOpen = Object.keys(filters()).length > 0;
  }

  function filters(): Record<string, string | number> {
    return {
      ...(from ? { from } : {}),
      ...(to ? { to } : {}),
      ...(topicId ? { topic_id: topicId } : {}),
      ...(channelId ? { channel_id: channelId } : {}),
      ...(minMinutes
        ? { min_duration: Math.round(Number(minMinutes) * 60) }
        : {}),
      ...(maxMinutes
        ? { max_duration: Math.round(Number(maxMinutes) * 60) }
        : {}),
    };
  }

  function writeUrl(): void {
    const params = new SvelteURLSearchParams();
    if (q.trim()) params.set("q", q.trim());
    if (q.trim() && caseSensitive) params.set("case_sensitive", "1");
    for (const [key, value] of Object.entries(filters()))
      params.set(key, String(value));
    const search = params.toString();
    void goto(search ? `/library?${search}` : "/library", {
      replaceState: true,
      keepFocus: true,
      noScroll: true,
    });
  }

  async function load(cursor: string | null = null): Promise<void> {
    const serial = ++requestSerial;
    loading = true;
    message = null;
    if (searching) {
      const { data, error } = await api.GET("/search", {
        params: {
          query: {
            q: q.trim(),
            case_sensitive: caseSensitive || undefined,
            ...filters(),
            ...(cursor ? { cursor } : {}),
          },
        },
      });
      if (serial !== requestSerial) return;
      loading = false;
      if (!data) {
        message = errorMessage(error, "Search failed.");
        return;
      }
      results = cursor ? [...results, ...data.results] : data.results;
      nextCursor = data.next_cursor;
      exhausted = data.exhausted;
      unavailable = data.semantic_unavailable;
      searched = true;
      return;
    }

    const { data, error } = await api.GET("/notes", {
      params: {
        query: { ...filters(), limit: 50, ...(cursor ? { cursor } : {}) },
      },
    });
    if (serial !== requestSerial) return;
    loading = false;
    if (!data) {
      message = errorMessage(error, "The library could not be loaded.");
      return;
    }
    cards = cursor ? [...cards, ...data.notes] : data.notes;
    nextCursor = data.next_cursor ?? null;
    exhausted = !data.next_cursor;
    unavailable = null;
    searched = false;
  }

  async function loadOptions(): Promise<void> {
    const [topicResponse, noteResponse] = await Promise.all([
      api.GET("/topics"),
      api.GET("/notes", { params: { query: { limit: 100 } } }),
    ]);
    if (topicResponse.data) topics = topicResponse.data.topics;
    if (noteResponse.data) {
      const seen = new SvelteMap<string, string>();
      for (const card of noteResponse.data.notes) {
        if (card.source.channel_id && !seen.has(card.source.channel_id)) {
          seen.set(
            card.source.channel_id,
            card.source.channel_name ?? card.source.channel_id,
          );
        }
      }
      channels = [...seen]
        .map(([id, name]) => ({ id, name }))
        .sort((a, b) => a.name.localeCompare(b.name));
    }
  }

  function apply(event?: SubmitEvent): void {
    event?.preventDefault();
    if (debounce) clearTimeout(debounce);
    writeUrl();
    void load();
  }

  function searchSoon(): void {
    if (debounce) clearTimeout(debounce);
    debounce = setTimeout(() => apply(), 320);
  }

  function chooseTopic(id: string): void {
    topicId = topicId === id ? "" : id;
    apply();
  }

  function clear(): void {
    q = "";
    caseSensitive = false;
    from = "";
    to = "";
    topicId = "";
    channelId = "";
    minMinutes = "";
    maxMinutes = "";
    apply();
  }

  function resultHref(result: SearchResult): string {
    const params = new SvelteURLSearchParams({ find: q.trim() });
    if (caseSensitive) params.set("case_sensitive", "1");
    const kind = result.excerpts.length
      ? "transcript_chunk"
      : result.matched_kinds[0];
    if (kind) params.set("match", kind);
    if (result.excerpts[0]) params.set("chunk", result.excerpts[0].chunk_id);
    return `/notes/${result.note.id}?${params}`;
  }

  onMount(() => {
    readUrl();
    void loadOptions();
    void load();
  });

  const unavailableText = $derived(
    unavailable === "soft_cap"
      ? "Semantic search is paused at your monthly cap. These are exact keyword matches."
      : unavailable === "rate_limited"
        ? "Semantic search is catching up. These are exact keyword matches."
        : unavailable === "provider_error"
          ? "Semantic search is unavailable right now. These are exact keyword matches."
          : null,
  );
</script>

<svelte:head><title>Library · Visual Notes</title></svelte:head>

<header class="library-head">
  <div>
    <p class="eyebrow">Your knowledge library</p>
    <h1>Find the idea, not just the video.</h1>
  </div>
  {#if cards.length || results.length}
    <p class="count">{searched ? results.length : cards.length} shown</p>
  {/if}
</header>

<form
  class="search-shell"
  role="search"
  aria-label="Search and filters"
  onsubmit={apply}
>
  <div class="search-row">
    <svg viewBox="0 0 24 24" aria-hidden="true"
      ><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></svg
    >
    <input
      type="search"
      placeholder="Search summaries, diagrams, details, and transcripts"
      aria-label="Search"
      bind:value={q}
      oninput={searchSoon}
    />
    {#if q}<button
        type="button"
        class="ghost small clear-query"
        aria-label="Clear query"
        onclick={() => {
          q = "";
          apply();
        }}>×</button
      >{/if}
    <button type="submit" class="primary" disabled={loading}>Search</button>
  </div>
  <div class="search-options">
    <label class="case-toggle">
      <input
        type="checkbox"
        bind:checked={caseSensitive}
        onchange={() => apply()}
      />
      <span aria-hidden="true">Aa</span>
      Case sensitive
    </label>
    <details bind:open={filtersOpen}>
      <summary
        >Filters{#if activeFilters}
          <span>{activeFilters}</span>{/if}</summary
      >
      <div class="filter-panel">
        <label class="field"
          ><span>From</span><input type="date" bind:value={from} /></label
        >
        <label class="field"
          ><span>To</span><input type="date" bind:value={to} /></label
        >
        <label class="field">
          <span>Topic</span>
          <select bind:value={topicId}>
            <option value="">Any topic</option>
            {#each topics as topic (topic.id)}<option value={topic.id}
                >{topic.name}</option
              >{/each}
          </select>
        </label>
        <label class="field">
          <span>Channel</span>
          <select bind:value={channelId}>
            <option value="">Any channel</option>
            {#each channels as channel (channel.id)}<option value={channel.id}
                >{channel.name}</option
              >{/each}
          </select>
        </label>
        <label class="field"
          ><span>At least (minutes)</span><input
            type="number"
            min="0"
            bind:value={minMinutes}
          /></label
        >
        <label class="field"
          ><span>At most (minutes)</span><input
            type="number"
            min="0"
            bind:value={maxMinutes}
          /></label
        >
        <div class="filter-actions">
          <button type="submit" disabled={loading}>Apply filters</button>
          <button type="button" class="ghost" onclick={clear}>Clear all</button>
        </div>
      </div>
    </details>
  </div>
</form>

{#if topics.length}
  <div class="topic-strip">
    <span class="topic-label">Topics</span>
    <div>
      {#each topics as topic (topic.id)}
        <button
          type="button"
          class:active={topicId === topic.id}
          aria-pressed={topicId === topic.id}
          onclick={() => chooseTopic(topic.id)}
          >{topic.name} <span>{topic.note_count}</span></button
        >
      {/each}
    </div>
  </div>
{/if}

{#if message}<p class="error notice" role="alert">{message}</p>{/if}
{#if unavailableText}<p class="warn notice" role="status">
    {unavailableText}
  </p>{/if}

{#if loading && (searched ? results.length === 0 : cards.length === 0)}
  <div class="loading-list" aria-label="Loading notes">
    {#each [1, 2, 3] as row (row)}
      <div>
        <span class="skeleton line"></span><span class="skeleton copy"></span>
      </div>
    {/each}
  </div>
{:else if searched}
  <ol class="results" aria-label="Search results">
    {#each results as result (result.note.id)}
      <li class="result">
        <NoteCardView
          card={result.note}
          href={resultHref(result)}
          query={q}
          {caseSensitive}
        />
        <div class="match-context">
          <p class="matched-in">
            Found in
            {#each result.matched_kinds as kind, index (kind)}
              {#if index},
              {/if}<span>{kindLabels[kind] ?? kind}</span>
            {/each}
          </p>
          {#each result.excerpts as excerpt (excerpt.chunk_id)}
            <blockquote>
              <Highlight text={excerpt.text} query={q} {caseSensitive} />
            </blockquote>
          {/each}
        </div>
      </li>
    {/each}
  </ol>
  {#if results.length === 0}<div class="empty">
      <h2>No match yet</h2>
      <p>Try fewer words, turn off case sensitivity, or remove a filter.</p>
    </div>{/if}
{:else}
  <ul class="notes" aria-label="Notes">
    {#each cards as card (card.id)}<li><NoteCardView {card} /></li>{/each}
  </ul>
  {#if cards.length === 0}<div class="empty">
      <h2>Your library is clear</h2>
      <p>Save a video from Today, or loosen the filters above.</p>
    </div>{/if}
{/if}

{#if nextCursor}
  <div class="load-more">
    <button type="button" disabled={loading} onclick={() => load(nextCursor)}
      >Load more</button
    >
  </div>
{:else if searched && exhausted && results.length > 0}
  <p class="end">That is everything that matched.</p>
{/if}

<style>
  .library-head {
    display: flex;
    align-items: end;
    justify-content: space-between;
    gap: 1rem;
    margin: 0.5rem 0 1.6rem;
  }
  .eyebrow {
    margin: 0 0 0.3rem;
    color: var(--accent);
    font-size: 0.78rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .count {
    margin: 0;
    color: var(--muted);
    font-size: 0.82rem;
  }
  .search-shell {
    position: relative;
    z-index: 1;
    border: 1px solid var(--line-strong);
    border-radius: var(--radius-lg);
    background: color-mix(in oklch, var(--surface-raised) 92%, transparent);
    box-shadow: var(--shadow);
  }
  .search-row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto auto;
    align-items: center;
    gap: 0.45rem;
    padding: 0.55rem;
  }
  .search-row > svg {
    width: 22px;
    margin-left: 0.55rem;
    fill: none;
    stroke: var(--muted);
    stroke-width: 1.7;
  }
  .search-row input {
    border: 0;
    background: transparent;
    box-shadow: none;
    font-size: 1rem;
  }
  .search-options {
    display: flex;
    align-items: center;
    gap: 1.2rem;
    min-height: 48px;
    padding: 0 0.9rem;
    border-top: 1px solid var(--line);
    color: var(--muted);
    font-size: 0.82rem;
  }
  .case-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    cursor: pointer;
  }
  .case-toggle span {
    display: grid;
    place-items: center;
    width: 27px;
    height: 24px;
    border: 1px solid var(--line);
    border-radius: 5px;
    font-weight: 700;
  }
  .case-toggle input {
    width: 1px;
    height: 1px;
    opacity: 0;
    position: absolute;
  }
  .case-toggle:has(input:checked) span {
    border-color: var(--accent);
    background: var(--accent-soft);
    color: var(--accent);
  }
  details {
    position: relative;
  }
  summary {
    list-style: none;
    font-weight: 600;
  }
  summary::-webkit-details-marker {
    display: none;
  }
  summary span {
    padding: 0.05rem 0.35rem;
    border-radius: 9px;
    background: var(--accent-soft);
    color: var(--accent);
  }
  .filter-panel {
    position: absolute;
    top: calc(100% + 0.75rem);
    left: 0;
    width: min(42rem, calc(100vw - 2rem));
    display: grid;
    grid-template-columns: repeat(3, minmax(9rem, 1fr));
    gap: 0.8rem;
    padding: 1rem;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background: var(--surface-raised);
    box-shadow: var(--shadow);
  }
  .filter-actions {
    grid-column: 1 / -1;
    display: flex;
    gap: 0.5rem;
  }
  .topic-strip {
    display: grid;
    grid-template-columns: 5rem 1fr;
    align-items: start;
    gap: 1rem;
    margin: 1.4rem 0 0.3rem;
  }
  .topic-label {
    padding-top: 0.5rem;
    color: var(--muted);
    font-size: 0.8rem;
    font-weight: 650;
  }
  .topic-strip div {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }
  .topic-strip button {
    min-height: 34px;
    padding: 0.3rem 0.65rem;
    border-radius: 99px;
    font-size: 0.8rem;
    font-weight: 600;
  }
  .topic-strip button span {
    color: var(--faint);
    font-variant-numeric: tabular-nums;
  }
  .topic-strip button.active {
    border-color: var(--accent);
    background: var(--accent-soft);
    color: var(--accent);
  }
  .notice {
    margin: 1rem 0 0;
    padding: 0.75rem 0.9rem;
    border-left: 3px solid currentColor;
    background: var(--surface);
  }
  .results,
  .notes {
    list-style: none;
    padding: 0;
    margin: 1.2rem 0 0;
  }
  .result {
    padding-bottom: 0.7rem;
  }
  .match-context {
    margin: -0.5rem 0 0.8rem 1.3rem;
    padding-left: 0.85rem;
    border-left: 2px solid var(--accent-soft);
  }
  .matched-in {
    margin: 0;
    color: var(--faint);
    font-size: 0.76rem;
  }
  .matched-in span {
    color: var(--accent);
    font-weight: 650;
  }
  blockquote {
    max-width: 82ch;
    margin: 0.45rem 0 0;
    color: var(--muted);
    font-size: 0.9rem;
  }
  .loading-list div {
    display: grid;
    gap: 0.65rem;
    padding: 1.3rem 0;
    border-bottom: 1px solid var(--line);
  }
  .loading-list span {
    display: block;
    height: 0.9rem;
  }
  .loading-list .line {
    width: 42%;
  }
  .loading-list .copy {
    width: 75%;
  }
  .empty {
    max-width: 34rem;
    margin: 4rem auto;
    text-align: center;
  }
  .empty p,
  .end {
    color: var(--muted);
  }
  .load-more {
    display: grid;
    place-items: center;
    margin: 1.5rem 0;
  }
  .end {
    text-align: center;
    font-size: 0.82rem;
  }
  @media (max-width: 680px) {
    .library-head {
      align-items: start;
    }
    .search-row {
      grid-template-columns: auto minmax(0, 1fr) auto;
    }
    .search-row .primary {
      grid-column: 1 / -1;
      width: 100%;
    }
    .filter-panel {
      left: -8rem;
      grid-template-columns: 1fr 1fr;
    }
    .topic-strip {
      grid-template-columns: 1fr;
      gap: 0.4rem;
    }
  }
  @media (max-width: 460px) {
    .filter-panel {
      left: -7rem;
      grid-template-columns: 1fr;
    }
    .filter-actions {
      grid-column: 1;
    }
    .search-options {
      justify-content: space-between;
    }
  }
</style>

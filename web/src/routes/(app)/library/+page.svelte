<script lang="ts">
  // Library: browse by filters, or search with them (spec 5.6, ADR 0005).
  // The query and every filter live in the URL, so a search is a link.
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { SvelteMap, SvelteURLSearchParams } from "svelte/reactivity";
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

  const searching = $derived(q.trim().length > 0);

  function readUrl() {
    const p = page.url.searchParams;
    q = p.get("q") ?? "";
    from = p.get("from") ?? "";
    to = p.get("to") ?? "";
    topicId = p.get("topic_id") ?? "";
    channelId = p.get("channel_id") ?? "";
    minMinutes = p.get("min_duration")
      ? String(Number(p.get("min_duration")) / 60)
      : "";
    maxMinutes = p.get("max_duration")
      ? String(Number(p.get("max_duration")) / 60)
      : "";
  }

  function filters() {
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

  function writeUrl() {
    const p = new SvelteURLSearchParams();
    if (q.trim()) p.set("q", q.trim());
    for (const [k, v] of Object.entries(filters())) p.set(k, String(v));
    const search = p.toString();
    void goto(search ? `/library?${search}` : "/library", {
      replaceState: true,
      keepFocus: true,
      noScroll: true,
    });
  }

  async function load(cursor: string | null = null) {
    loading = true;
    message = null;
    if (searching) {
      const { data, error } = await api.GET("/search", {
        params: {
          query: { q: q.trim(), ...filters(), ...(cursor ? { cursor } : {}) },
        },
      });
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
    } else {
      const { data, error } = await api.GET("/notes", {
        params: {
          query: { ...filters(), limit: 50, ...(cursor ? { cursor } : {}) },
        },
      });
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
  }

  async function loadOptions() {
    const [t, n] = await Promise.all([
      api.GET("/topics"),
      api.GET("/notes", { params: { query: { limit: 100 } } }),
    ]);
    if (t.data) topics = t.data.topics;
    if (n.data) {
      const seen = new SvelteMap<string, string>();
      for (const card of n.data.notes) {
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

  function apply(event?: SubmitEvent) {
    event?.preventDefault();
    writeUrl();
    void load();
  }

  function clear() {
    q = "";
    from = "";
    to = "";
    topicId = "";
    channelId = "";
    minMinutes = "";
    maxMinutes = "";
    apply();
  }

  onMount(() => {
    readUrl();
    void loadOptions();
    void load();
  });

  const unavailableText = $derived(
    unavailable === "soft_cap"
      ? "Semantic search is paused because the monthly cap is reached; these are keyword matches only. Re-enable it for the month in Settings → Usage."
      : unavailable === "rate_limited"
        ? "Semantic search was skipped for this query because searches are coming in faster than the limit; these are keyword matches only."
        : unavailable === "provider_error"
          ? "Semantic search is unavailable right now (the embedding call failed); these are keyword matches only."
          : null,
  );
</script>

<svelte:head>
  <title>Library · Visual Notes</title>
</svelte:head>

<h1>Library</h1>

<form
  class="card stack"
  role="search"
  aria-label="Search and filters"
  onsubmit={apply}
>
  <div class="row">
    <input
      type="search"
      class="grow"
      placeholder="Search notes and transcripts"
      aria-label="Search"
      bind:value={q}
    />
    <button type="submit" class="primary" disabled={loading}>Search</button>
  </div>
  <details open={Object.keys(filters()).length > 0}>
    <summary>Filters</summary>
    <div class="row wrap">
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
    </div>
    <div class="row">
      <button type="submit" disabled={loading}>Apply filters</button>
      <button type="button" onclick={clear}>Clear</button>
    </div>
  </details>
</form>

{#if message}<p class="error" role="alert">{message}</p>{/if}
{#if unavailableText}
  <p class="card warn" role="status">{unavailableText}</p>
{/if}

{#if searched}
  <ol class="stack results" aria-label="Search results">
    {#each results as result (result.note.id)}
      <li class="stack result">
        <NoteCardView card={result.note} />
        <p class="row wrap why">
          {#each result.matched_kinds as kind (kind)}
            <span class="badge">{kindLabels[kind] ?? kind}</span>
          {/each}
        </p>
        {#each result.excerpts as excerpt (excerpt.chunk_id)}
          <blockquote class="excerpt">{excerpt.text}</blockquote>
        {/each}
      </li>
    {/each}
  </ol>
  {#if results.length === 0 && !loading}
    <p class="muted">Nothing matched.</p>
  {/if}
{:else}
  <ul class="stack plain" aria-label="Notes">
    {#each cards as card (card.id)}
      <li><NoteCardView {card} /></li>
    {/each}
  </ul>
  {#if cards.length === 0 && !loading}
    <p class="muted">No notes match these filters.</p>
  {/if}
{/if}

{#if nextCursor}
  <div class="row">
    <button type="button" disabled={loading} onclick={() => load(nextCursor)}
      >Load more</button
    >
  </div>
{:else if searched && exhausted && results.length > 0}
  <p class="muted small">That is everything that matched.</p>
{/if}

<style>
  .grow {
    flex: 1 1 12rem;
  }
  .wrap {
    flex-wrap: wrap;
  }
  summary {
    cursor: pointer;
    font-weight: 600;
  }
  .results,
  .plain {
    list-style: none;
    padding: 0;
    margin: 1rem 0 0;
  }
  .result {
    gap: 0.35rem;
  }
  .why {
    margin: 0;
    gap: 0.3rem;
  }
  .excerpt {
    margin: 0;
    padding: 0.4rem 0.8rem;
    border-left: 3px solid var(--accent);
    color: var(--muted);
  }
  .small {
    font-size: 0.85rem;
  }
</style>

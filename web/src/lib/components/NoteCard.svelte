<script lang="ts">
  import type { NoteCard } from "$lib/db";
  import Highlight from "./Highlight.svelte";

  let {
    card,
    href = `/notes/${card.id}`,
    query = "",
    caseSensitive = false,
  }: {
    card: NoteCard;
    href?: string;
    query?: string;
    caseSensitive?: boolean;
  } = $props();

  const title = $derived(card.source.title ?? `Video ${card.source.video_id}`);
  const state = $derived(card.processing.state);
  const tone = $derived(
    state === "ready"
      ? "ready"
      : state === "failed" || state === "cancelled"
        ? "failed"
        : "processing",
  );
  const label = $derived(
    card.processing.cost_hold
      ? "awaiting approval"
      : card.processing.failure_kind === "extractor_breakage"
        ? "extractor update needed"
        : state,
  );

  function duration(seconds: number | null): string {
    if (seconds == null) return "";
    const minutes = Math.round(seconds / 60);
    return minutes >= 60
      ? `${Math.floor(minutes / 60)} h ${minutes % 60} min`
      : `${minutes} min`;
  }

  function saved(iso: string): string {
    const value = new Date(iso);
    const today = new Date();
    if (value.toDateString() === today.toDateString()) {
      return value.toLocaleTimeString(undefined, {
        hour: "2-digit",
        minute: "2-digit",
      });
    }
    return value.toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
    });
  }
</script>

<a class="note-card" {href} aria-label={title}>
  <article>
    <div class="title-row">
      <h2><Highlight text={title} {query} {caseSensitive} /></h2>
      <span class={`badge ${tone}`}>{label}</span>
    </div>
    {#if card.summary_text}
      <p class="summary">
        <Highlight text={card.summary_text} {query} {caseSensitive} />
      </p>
    {:else}
      <p class="summary muted">
        A summary will appear when processing is complete.
      </p>
    {/if}
    <footer>
      <span>{card.source.channel_name ?? "Channel pending"}</span>
      {#if card.source.duration_seconds != null}<span
          >{duration(card.source.duration_seconds)}</span
        >{/if}
      <span>Saved {saved(card.saved_at)}</span>
      {#if card.primary_topic}<span class="topic"
          >{card.primary_topic.name}</span
        >{/if}
      <svg viewBox="0 0 24 24" aria-hidden="true"
        ><path d="m9 18 6-6-6-6" /></svg
      >
    </footer>
  </article>
</a>

<style>
  .note-card {
    display: block;
    border-bottom: 1px solid var(--line);
    color: inherit;
    text-decoration: none;
  }
  article {
    padding: 1.15rem 0.25rem;
    transition: transform 180ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .note-card:hover article {
    transform: translateX(4px);
  }
  .title-row {
    display: flex;
    align-items: start;
    justify-content: space-between;
    gap: 1rem;
  }
  h2 {
    font-size: 1.05rem;
  }
  .summary {
    max-width: 76ch;
    margin: 0.45rem 0 0;
    display: -webkit-box;
    overflow: hidden;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    color: var(--muted);
  }
  footer {
    display: flex;
    align-items: center;
    gap: 0.45rem 0.8rem;
    margin-top: 0.75rem;
    color: var(--faint);
    font-size: 0.78rem;
  }
  footer span + span::before {
    content: "";
    display: inline-block;
    width: 3px;
    height: 3px;
    margin: 0 0.7rem 0.16rem 0;
    border-radius: 50%;
    background: var(--line-strong);
  }
  .topic {
    color: var(--accent);
    font-weight: 600;
  }
  svg {
    width: 17px;
    margin-left: auto;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.7;
  }
  @media (max-width: 560px) {
    footer {
      flex-wrap: wrap;
    }
    .title-row {
      display: grid;
    }
    .badge {
      justify-self: start;
    }
  }
</style>

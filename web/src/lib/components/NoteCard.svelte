<script lang="ts">
  import type { NoteCard } from "$lib/db";

  let { card }: { card: NoteCard } = $props();

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
      ? "waiting for cost confirmation"
      : card.processing.failure_kind === "extractor_breakage"
        ? "blocked until yt-dlp is updated"
        : state,
  );

  function duration(seconds: number | null): string {
    if (seconds == null) return "";
    const m = Math.round(seconds / 60);
    return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
  }

  function time(iso: string): string {
    return new Date(iso).toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    });
  }
</script>

<a class="card note-card" href={`/notes/${card.id}`}>
  <div class="row between">
    <strong>{title}</strong>
    <span class={`badge ${tone}`}>{label}</span>
  </div>
  <p class="muted meta">
    {card.source.channel_name ?? "channel pending"}
    {#if card.source.duration_seconds != null}
      · {duration(card.source.duration_seconds)}
    {/if}
    · saved {time(card.saved_at)}
    {#if card.primary_topic}
      · {card.primary_topic.name}
    {/if}
  </p>
  {#if card.summary_text}
    <p class="summary">{card.summary_text}</p>
  {/if}
</a>

<style>
  .note-card {
    display: block;
    color: inherit;
    text-decoration: none;
  }
  .between {
    justify-content: space-between;
  }
  .meta {
    margin: 0.25rem 0 0;
    font-size: 0.9rem;
  }
  .summary {
    margin: 0.5rem 0 0;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>

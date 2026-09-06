<script lang="ts">
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import { getNote, putNoteDetail, type Note } from "$lib/db";
  import { onSync } from "$lib/live.svelte";
  import { deriveTextAlternative } from "$lib/diagram";
  import DiagramCanvas from "$lib/components/DiagramCanvas.svelte";
  import TimelineList from "$lib/components/TimelineList.svelte";
  import ComparisonTable from "$lib/components/ComparisonTable.svelte";

  let note = $state<Note | null>(null);
  let error = $state<string | null>(null);

  const revision = $derived(note?.active_revision ?? null);
  const payload = $derived(revision?.payload ?? null);
  const textAlternative = $derived(
    payload ? deriveTextAlternative(payload.diagram) : "",
  );
  const diagramName = $derived(
    payload
      ? {
          mind_map: "Mind map",
          process_flow: "Process flow",
          timeline: "Timeline",
          comparison: "Comparison",
        }[payload.diagram.type]
      : "",
  );

  async function load() {
    const id = page.params.id ?? "";
    const cached = await getNote(id);
    if (cached?.detail) note = cached.detail;
    const {
      data,
      error: failure,
      response,
    } = await api.GET("/notes/{noteId}", { params: { path: { noteId: id } } });
    if (data) {
      note = data;
      await putNoteDetail(data);
    } else if (!note) {
      error =
        response.status === 404
          ? "This note no longer exists."
          : errorMessage(failure);
    }
  }

  onMount(() => {
    void load();
    return onSync((event) => {
      if (
        event.note_id === page.params.id ||
        event.entity_id === page.params.id
      )
        void load();
    });
  });

  function duration(seconds: number | null | undefined): string {
    if (seconds == null) return "";
    const m = Math.round(seconds / 60);
    return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
  }
  function when(iso: string | null | undefined): string {
    return iso ? new Date(iso).toLocaleString() : "";
  }
  function day(iso: string | null | undefined): string {
    return iso ? new Date(iso).toLocaleDateString() : "";
  }
  const stageLabel: Record<string, string> = {
    queued: "Queued",
    metadata: "Reading the video's details",
    captions: "Fetching captions",
    transcribing: "Transcribing the audio",
    generating: "Writing the note",
    indexing: "Indexing for search",
  };
</script>

<svelte:head>
  <title>{note?.source.title ?? "Note"} · Visual Notes</title>
</svelte:head>

{#if error}
  <p class="error">{error}</p>
{:else if !note}
  <p class="muted">Loading…</p>
{:else}
  <article class="note stack">
    <header>
      <h1>{note.source.title ?? `Video ${note.source.video_id}`}</h1>
      <p class="muted meta">
        {#if note.source.channel_name}{note.source.channel_name} ·
        {/if}
        {#if note.source.duration_seconds != null}{duration(
            note.source.duration_seconds,
          )} ·
        {/if}
        saved {when(note.saved_at)}
        {#if note.source.published_at}
          · published {day(note.source.published_at)}{/if}
        ·
        <a href={note.source.canonical_url} rel="noreferrer noopener"
          >watch on YouTube</a
        >
      </p>
      <p class="row tags">
        {#if note.primary_topic}
          <a class="badge topic" href={`/topics/${note.primary_topic.id}`}
            >{note.primary_topic.name}</a
          >
        {/if}
        {#each note.tags as tag (tag.id)}
          <span class="badge">{tag.name}</span>
        {/each}
        {#if note.processing.state !== "ready"}
          <span class="badge processing"
            >{stageLabel[note.processing.state] ?? note.processing.state}</span
          >
        {/if}
      </p>
    </header>

    {#if payload}
      <section class="card">
        <p class="summary" class:claim={payload.summary.speaker_claim}>
          {payload.summary.text}
        </p>
      </section>

      <section class="stack">
        <div class="row between">
          <h2>{diagramName}</h2>
          <span class="muted small">
            {#if payload.diagram.type === "mind_map" || payload.diagram.type === "process_flow"}
              drag to pan, pinch or scroll to zoom, tap a “+” to expand
            {/if}
          </span>
        </div>
        {#if payload.diagram.type === "mind_map" || payload.diagram.type === "process_flow"}
          <DiagramCanvas
            diagram={payload.diagram}
            positions={note.layout.positions}
          />
        {:else if payload.diagram.type === "timeline"}
          <TimelineList diagram={payload.diagram} />
        {:else}
          <ComparisonTable diagram={payload.diagram} />
        {/if}
        <details class="card">
          <summary>Text version of the diagram</summary>
          <pre class="alternative">{textAlternative}</pre>
        </details>
      </section>

      <section class="card stack">
        <h2>Key points</h2>
        <ul class="blocks">
          {#each payload.key_points as block (block.id)}
            <li class:claim={block.speaker_claim} data-element-id={block.id}>
              {block.text}
              {#if block.speaker_claim}<span class="badge">speaker claim</span
                >{/if}
            </li>
          {/each}
        </ul>
      </section>

      {#if payload.important_details.length > 0}
        <section class="card stack">
          <h2>Important details</h2>
          <ul class="blocks">
            {#each payload.important_details as block (block.id)}
              <li class:claim={block.speaker_claim} data-element-id={block.id}>
                {block.text}
                {#if block.speaker_claim}<span class="badge">speaker claim</span
                  >{/if}
              </li>
            {/each}
          </ul>
        </section>
      {/if}

      {#if payload.practical_actions.length > 0}
        <section class="card stack">
          <h2>What I can use</h2>
          <ul class="blocks">
            {#each payload.practical_actions as block (block.id)}
              <li class:claim={block.speaker_claim} data-element-id={block.id}>
                {block.text}
              </li>
            {/each}
          </ul>
        </section>
      {/if}

      <section class="card stack">
        <h2>My takeaway</h2>
        {#if note.takeaway.text}
          <p>{note.takeaway.text}</p>
        {:else}
          <p class="muted">
            Nothing written yet. Editing arrives with the next release of this
            app.
          </p>
        {/if}
      </section>

      <details class="card">
        <summary>
          Version history · revision {revision?.revision_number}
          {#if note.pending_revisions.length > 0}
            · {note.pending_revisions.length} awaiting a decision{/if}
        </summary>
        <ul class="blocks">
          <li>
            Revision {revision?.revision_number} ({revision?.origin}), active,
            {when(revision?.created_at)}
          </li>
          {#each note.pending_revisions as pending (pending.id)}
            <li>
              Revision {pending.revision_number} ({pending.origin}), awaiting a
              decision since {when(pending.awaiting_decision_since)}
            </li>
          {/each}
        </ul>
      </details>

      <details class="card">
        <summary>Processing and cost</summary>
        <ul class="blocks">
          {#each note.processing.stages as stage (stage.stage)}
            <li>
              {stage.stage}: {stage.status}
              {#if stage.last_error}
                · {stage.last_error.message}{/if}
            </li>
          {/each}
          <li>
            Estimated ${note.cost.estimated_usd}, reported ${note.cost
              .reported_usd}{#if note.cost.incomplete}
              (at least){/if}
          </li>
          {#each note.roles_used as role (role.role)}
            <li>{role.role}: {role.provider} / {role.model}</li>
          {/each}
        </ul>
      </details>
    {:else}
      <section class="card stack">
        {#if note.processing.state === "failed"}
          <p class="error">
            The note could not be made. {note.processing.stages.find(
              (s) => s.last_error,
            )?.last_error?.message ?? ""}
          </p>
        {:else if note.processing.cost_hold}
          <p class="warn">
            This note is waiting for cost confirmation (about ${note.processing
              .cost_hold.estimate_usd}).
          </p>
        {:else}
          <p class="muted">
            {stageLabel[note.processing.state] ?? "Working"}… the note appears
            here as soon as it is ready.
          </p>
        {/if}
        <ul class="blocks">
          {#each note.processing.stages as stage (stage.stage)}
            <li>{stage.stage}: {stage.status}</li>
          {/each}
        </ul>
      </section>
    {/if}
  </article>
{/if}

<style>
  .meta {
    margin: 0.25rem 0 0;
  }
  .tags {
    margin: 0.5rem 0 0;
    gap: 0.4rem;
  }
  .topic {
    text-decoration: none;
    color: var(--accent);
    border-color: var(--accent);
  }
  .summary {
    font-size: 1.1rem;
    margin: 0;
  }
  .between {
    justify-content: space-between;
  }
  .small {
    font-size: 0.85rem;
  }
  .blocks {
    margin: 0;
    padding-left: 1.25rem;
  }
  .blocks li + li {
    margin-top: 0.4rem;
  }
  .claim {
    font-style: italic;
  }
  .alternative {
    white-space: pre-wrap;
    font-family: inherit;
    margin: 0.5rem 0 0;
  }
  details > summary {
    cursor: pointer;
    font-weight: 600;
  }
  h2 {
    margin: 0;
  }
</style>

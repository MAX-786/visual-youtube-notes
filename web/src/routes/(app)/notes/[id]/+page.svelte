<script lang="ts">
  // The Note page: the Active Revision rendered and, in editing mode, edited
  // in place. Every change goes through the write-through queue (ADR 0012):
  // applied locally, stored, and drained against the Revision this page
  // last saw as Active. Conflicts and stale takeaways are surfaced, never
  // silently resolved (ADR 0004).
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import type { components } from "@visual-notes/api-client";
  import { api, errorMessage } from "$lib/api";
  import { getNote, putNoteDetail, type Note } from "$lib/db";
  import { onSync } from "$lib/live.svelte";
  import { deriveTextAlternative, type PositionMap } from "$lib/diagram";
  import {
    enqueueLayout,
    enqueueMutation,
    enqueueTags,
    enqueueTakeaway,
    enqueueTopic,
    onQueueResult,
    queue,
  } from "$lib/queue.svelte";
  import BlockEditor from "$lib/components/BlockEditor.svelte";
  import ComparisonTable from "$lib/components/ComparisonTable.svelte";
  import ConflictBanner from "$lib/components/ConflictBanner.svelte";
  import DiagramCanvas from "$lib/components/DiagramCanvas.svelte";
  import DiagramEditor from "$lib/components/DiagramEditor.svelte";
  import TimelineList from "$lib/components/TimelineList.svelte";
  import VersionHistory from "$lib/components/VersionHistory.svelte";

  type MutationOp = components["schemas"]["MutationOp"];

  const TAKEAWAY_DEBOUNCE_MS = 2000;

  let note = $state<Note | null>(null);
  let error = $state<string | null>(null);
  let editing = $state(false);
  /** The Revision the next edit is based on: the Active one as last seen. */
  let base = $state("");
  let takeawayDraft = $state("");
  let takeawayCursor = $state(0);
  let takeawayDirty = $state(false);
  let takeawayTimer: ReturnType<typeof setTimeout> | null = null;
  let topicDraft = $state("");
  let topicDirty = $state(false);
  let tagsDraft = $state("");
  let tagsDirty = $state(false);
  let topicNames = $state<string[]>([]);
  let history: ReturnType<typeof VersionHistory> | undefined = $state();

  const noteId = $derived(page.params.id ?? "");
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
  const stale = $derived(queue.staleTakeaways.get(noteId) ?? null);
  const dropped = $derived(queue.dropped.get(noteId) ?? null);
  const conflict = $derived(queue.conflicts.get(noteId) ?? null);

  const dropReasons: Record<string, string> = {
    validation_failed:
      "the change would have left the note in an invalid shape",
    mutation_expired: "it waited more than thirty days to sync",
    note_trashed: "the note is in the trash",
    note_purged: "the note has been deleted for good",
    revision_not_active:
      "a regenerated version was waiting for a decision at the time",
    takeaway_stale: "another device wrote a takeaway first",
  };

  function apply(fresh: Note) {
    note = fresh;
    base = fresh.active_revision_id ?? "";
    if (!takeawayDirty) {
      takeawayDraft = fresh.takeaway.text;
      takeawayCursor = fresh.takeaway.updated_cursor;
    }
    if (!topicDirty) topicDraft = fresh.primary_topic?.name ?? "";
    if (!tagsDirty) tagsDraft = fresh.tags.map((t) => t.name).join(", ");
  }

  async function load() {
    const id = noteId;
    const cached = await getNote(id);
    if (cached?.detail) apply(cached.detail);
    const {
      data,
      error: failure,
      response,
    } = await api.GET("/notes/{noteId}", { params: { path: { noteId: id } } });
    if (data) {
      apply(data);
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
    const offSync = onSync((event) => {
      if (event.note_id === noteId || event.entity_id === noteId) void load();
    });
    const offQueue = onQueueResult((id, body) => {
      if (id !== noteId) return;
      const result = body as {
        outcome?: string;
        revision_id?: string;
        takeaway?: { updated_cursor?: number };
      } | null;
      if (result?.outcome === "applied" && result.revision_id) {
        base = result.revision_id;
      }
      if (result?.takeaway?.updated_cursor != null) {
        takeawayCursor = result.takeaway.updated_cursor;
        takeawayDirty = false;
      }
      void load();
      void history?.load();
    });
    return () => {
      offSync();
      offQueue();
    };
  });

  async function toggleEditing() {
    editing = !editing;
    if (editing && topicNames.length === 0) {
      const { data } = await api.GET("/topics");
      if (data) topicNames = data.topics.map((t) => t.name);
    }
    if (!editing) flushTakeaway();
  }

  function emit(ops: MutationOp[]) {
    if (!note || ops.length === 0) return;
    void enqueueMutation(note.id, base, ops);
  }

  function moved(positions: PositionMap) {
    if (!note) return;
    void enqueueLayout(note.id, positions);
  }

  function takeawayTyped() {
    takeawayDirty = true;
    if (takeawayTimer) clearTimeout(takeawayTimer);
    takeawayTimer = setTimeout(flushTakeaway, TAKEAWAY_DEBOUNCE_MS);
  }
  function flushTakeaway() {
    if (takeawayTimer) clearTimeout(takeawayTimer);
    takeawayTimer = null;
    if (!note || !takeawayDirty) return;
    if (takeawayDraft === note.takeaway.text) {
      takeawayDirty = false;
      return;
    }
    void enqueueTakeaway(note.id, takeawayDraft, takeawayCursor);
  }
  function keepMyTakeaway() {
    if (!note || !stale) return;
    const cursor = stale.server_cursor;
    queue.staleTakeaways.delete(note.id);
    queue.dropped.delete(note.id);
    takeawayCursor = cursor;
    takeawayDirty = true;
    void enqueueTakeaway(note.id, takeawayDraft, cursor);
  }
  function takeTheirTakeaway() {
    if (!note || !stale) return;
    takeawayDraft = stale.server_text;
    takeawayCursor = stale.server_cursor;
    takeawayDirty = false;
    queue.staleTakeaways.delete(note.id);
    queue.dropped.delete(note.id);
  }

  function commitTopic() {
    if (!note) return;
    const name = topicDraft.trim();
    topicDirty = false;
    if (!name || name === (note.primary_topic?.name ?? "")) return;
    void enqueueTopic(note.id, { name });
  }
  function commitTags() {
    if (!note) return;
    const tags = [
      ...new Set(
        tagsDraft
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
      ),
    ];
    tagsDirty = false;
    const current = note.tags.map((t) => t.name);
    if (
      tags.length === current.length &&
      tags.every((t, i) => t === current[i])
    )
      return;
    void enqueueTags(note.id, tags);
  }

  // Lifecycle actions (spec 5.5, 8.4): Regenerate from the Transcript with
  // an optional instruction, Retry a failed run, Delete to Trash.
  let actionMessage = $state<string | null>(null);
  let actionFailed = $state(false);
  let actionBusy = $state(false);
  let instruction = $state("");
  let showRegenerate = $state(false);

  async function regenerate(event: SubmitEvent) {
    event.preventDefault();
    if (!note) return;
    actionBusy = true;
    const { data, error } = await api.POST("/notes/{noteId}/regenerate", {
      params: { path: { noteId: note.id } },
      body: instruction.trim() ? { instruction: instruction.trim() } : {},
    });
    actionBusy = false;
    if (!data) {
      actionFailed = true;
      actionMessage = errorMessage(error, "The note could not be regenerated.");
      return;
    }
    actionFailed = false;
    actionMessage =
      data.outcome === "held"
        ? `Held for cost confirmation (about $${data.estimate_usd ?? "?"}); confirm it in Settings → Usage.`
        : "Regenerating. Your takeaway is kept; the current version stays in the history.";
    showRegenerate = false;
    instruction = "";
    await load();
  }

  async function retry() {
    if (!note) return;
    actionBusy = true;
    const { data, error } = await api.POST("/notes/{noteId}/retry", {
      params: { path: { noteId: note.id } },
    });
    actionBusy = false;
    if (!data) {
      actionFailed = true;
      actionMessage = errorMessage(error, "The note could not be retried.");
      return;
    }
    actionFailed = false;
    actionMessage =
      data.outcome === "held"
        ? `Held for cost confirmation (about $${data.estimate_usd ?? "?"}); confirm it in Settings → Usage.`
        : "Retrying from the step that failed. New AI charges may apply.";
    await load();
  }

  async function trash() {
    if (!note) return;
    if (
      !confirm(
        "Move this note to the trash? You can restore it within thirty days.",
      )
    )
      return;
    actionBusy = true;
    const { data, error } = await api.DELETE("/notes/{noteId}", {
      params: { path: { noteId: note.id } },
    });
    actionBusy = false;
    if (!data) {
      actionFailed = true;
      actionMessage = errorMessage(error, "The note could not be deleted.");
      return;
    }
    await goto("/trash");
  }

  const failure = $derived(
    note?.processing.stages.find((s) => s.last_error)?.last_error ?? null,
  );
  const failureText = $derived(
    note?.processing.failure_kind === "extractor_breakage"
      ? "The video is fine; the download tool needs an update before this note can be made. It will be retried once the tool is updated."
      : failure
        ? `The note could not be made. ${failure.message}${failure.is_permanent ? " Retry starts again from the step that failed." : ""}`
        : "The note could not be made.",
  );

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
      <div class="row between top">
        <h1>{note.source.title ?? `Video ${note.source.video_id}`}</h1>
        <div class="row wrap actions">
          {#if payload}
            <button
              type="button"
              class:primary={!editing}
              aria-pressed={editing}
              onclick={toggleEditing}
              >{editing ? "Done editing" : "Edit"}</button
            >
            {#if note.processing.state === "ready" && !note.trashed_at}
              <button
                type="button"
                disabled={actionBusy}
                aria-expanded={showRegenerate}
                onclick={() => (showRegenerate = !showRegenerate)}
                >Regenerate</button
              >
            {/if}
          {/if}
          {#if !note.trashed_at}
            <button
              type="button"
              class="danger"
              disabled={actionBusy}
              onclick={trash}>Delete</button
            >
          {/if}
        </div>
      </div>
      {#if showRegenerate}
        <form class="card stack" aria-label="Regenerate" onsubmit={regenerate}>
          <label class="field">
            <span>Instruction (optional), such as “simplify this”</span>
            <input type="text" maxlength="500" bind:value={instruction} />
          </label>
          <p class="muted">
            Regeneration starts from the transcript, never from edited text.
            Your takeaway is kept, and the current version stays in the history.
            New AI charges apply.
          </p>
          <div class="row">
            <button type="submit" class="primary" disabled={actionBusy}
              >Regenerate now</button
            >
            <button type="button" onclick={() => (showRegenerate = false)}
              >Cancel</button
            >
          </div>
        </form>
      {/if}
      {#if actionMessage}
        <p class={actionFailed ? "error" : "ok"} role="status">
          {actionMessage}
        </p>
      {/if}
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
        {#if queue.pending > 0}
          <span class="badge processing" role="status"
            >{queue.pending === 1
              ? "1 edit waiting to sync"
              : `${queue.pending} edits waiting to sync`}</span
          >
        {/if}
      </p>
    </header>

    {#if editing}
      <section class="card stack" aria-label="Topic and tags">
        <label class="field">
          <span>Topic</span>
          <input
            type="text"
            list="topic-names"
            maxlength="120"
            bind:value={topicDraft}
            oninput={() => (topicDirty = true)}
            onchange={commitTopic}
          />
        </label>
        <datalist id="topic-names">
          {#each topicNames as name (name)}<option value={name}></option>{/each}
        </datalist>
        <label class="field">
          <span>Tags, separated by commas</span>
          <input
            type="text"
            bind:value={tagsDraft}
            oninput={() => (tagsDirty = true)}
            onchange={commitTags}
          />
        </label>
      </section>
    {/if}

    <ConflictBanner {note} onchange={load} />

    {#if conflict}
      <div class="card warn row between" role="status">
        <span>
          Another device changed this note first, so your edit was kept as a
          separate version for you to decide on.
        </span>
        <button type="button" onclick={() => queue.conflicts.delete(noteId)}
          >Dismiss</button
        >
      </div>
    {/if}
    {#if dropped && dropped !== "takeaway_stale"}
      <div class="card warn row between" role="alert">
        <span>
          An edit could not be saved because {dropReasons[dropped] ??
            "the server refused it"}. The note shows the server's version.
        </span>
        <button type="button" onclick={() => queue.dropped.delete(noteId)}
          >Dismiss</button
        >
      </div>
    {/if}

    {#if payload}
      {#if editing}
        <BlockEditor
          title="Summary"
          section="summary"
          blocks={[payload.summary]}
          {emit}
        />
      {:else}
        <section class="card">
          <p class="summary" class:claim={payload.summary.speaker_claim}>
            {payload.summary.text}
          </p>
        </section>
      {/if}

      <section class="stack">
        <div class="row between">
          <h2>{diagramName}</h2>
          <span class="muted small">
            {#if payload.diagram.type === "mind_map" || payload.diagram.type === "process_flow"}
              {editing
                ? "drag a box to move it, tap a “+” to expand"
                : "drag to pan, pinch or scroll to zoom, tap a “+” to expand"}
            {/if}
          </span>
        </div>
        {#if payload.diagram.type === "mind_map" || payload.diagram.type === "process_flow"}
          <DiagramCanvas
            diagram={payload.diagram}
            positions={note.layout.positions}
            editable={editing}
            onmove={moved}
          />
        {:else if payload.diagram.type === "timeline"}
          <TimelineList diagram={payload.diagram} />
        {:else}
          <ComparisonTable diagram={payload.diagram} />
        {/if}
        {#if editing}
          <DiagramEditor diagram={payload.diagram} {emit} />
        {/if}
        <details class="card">
          <summary>Text version of the diagram</summary>
          <pre class="alternative">{textAlternative}</pre>
        </details>
      </section>

      {#if editing}
        <BlockEditor
          title="Key points"
          section="key_points"
          blocks={payload.key_points}
          minimum={1}
          {emit}
        />
        <BlockEditor
          title="Important details"
          section="important_details"
          blocks={payload.important_details}
          {emit}
        />
        <BlockEditor
          title="What I can use"
          section="practical_actions"
          blocks={payload.practical_actions}
          {emit}
        />
      {:else}
        <section class="card stack">
          <h2>Key points</h2>
          <ul class="blocks">
            {#each payload.key_points as block (block.id)}
              <li class:claim={block.speaker_claim} data-element-id={block.id}>
                {block.text}
                {#if block.speaker_claim}<span class="badge">speaker claim</span
                  >{/if}
                {#if block.provenance === "user_edited"}<span class="badge"
                    >edited</span
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
                <li
                  class:claim={block.speaker_claim}
                  data-element-id={block.id}
                >
                  {block.text}
                  {#if block.speaker_claim}<span class="badge"
                      >speaker claim</span
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
                <li
                  class:claim={block.speaker_claim}
                  data-element-id={block.id}
                >
                  {block.text}
                </li>
              {/each}
            </ul>
          </section>
        {/if}
      {/if}

      <section class="card stack">
        <h2>My takeaway</h2>
        <textarea
          rows="3"
          aria-label="My takeaway"
          maxlength="20000"
          placeholder="What will you do with this?"
          bind:value={takeawayDraft}
          oninput={takeawayTyped}
          onblur={flushTakeaway}></textarea>
        {#if stale}
          <div class="card warn stack" role="alert">
            <p>
              Another device wrote a takeaway first. Theirs reads:
              <q>{stale.server_text}</q>
            </p>
            <div class="row">
              <button type="button" class="primary" onclick={keepMyTakeaway}
                >Keep mine</button
              >
              <button type="button" onclick={takeTheirTakeaway}
                >Use theirs</button
              >
            </div>
          </div>
        {/if}
      </section>

      <VersionHistory bind:this={history} {noteId} onchange={load} />

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
        {#if note.processing.state === "failed" || note.processing.state === "cancelled"}
          <p class="error">{failureText}</p>
          <div class="row wrap">
            {#if note.processing.failure_kind !== "extractor_breakage" && !note.trashed_at}
              <button
                type="button"
                class="primary"
                disabled={actionBusy}
                onclick={retry}>Retry</button
              >
            {/if}
          </div>
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
  .top {
    align-items: flex-start;
    gap: 1rem;
  }
  .top h1 {
    margin: 0;
  }
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
  textarea {
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
  }
  q {
    font-style: italic;
  }
  .actions {
    gap: 0.4rem;
    justify-content: flex-end;
  }
  .ok {
    color: var(--accent);
  }
</style>

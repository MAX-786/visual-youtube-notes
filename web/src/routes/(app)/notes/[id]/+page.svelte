<script lang="ts">
  // The Note page: the Active Revision rendered and, in editing mode, edited
  // in place. Every change goes through the write-through queue (ADR 0012):
  // applied locally, stored, and drained against the Revision this page
  // last saw as Active. Conflicts and stale takeaways are surfaced, never
  // silently resolved (ADR 0004).
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { onMount, tick } from "svelte";
  import type { components } from "@visual-notes/api-client";
  import { api, errorMessage } from "$lib/api";
  import { getNote, putNoteDetail, type Note } from "$lib/db";
  import { onSync } from "$lib/live.svelte";
  import { type PositionMap } from "$lib/diagram";
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
  import Highlight from "$lib/components/Highlight.svelte";
  import TimelineList from "$lib/components/TimelineList.svelte";
  import TranscriptPanel from "$lib/components/TranscriptPanel.svelte";
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
  const findQuery = $derived(page.url.searchParams.get("find") ?? "");
  const findCaseSensitive = $derived(
    page.url.searchParams.get("case_sensitive") === "1",
  );
  const matchKind = $derived(page.url.searchParams.get("match") ?? "");
  const targetChunk = $derived(page.url.searchParams.get("chunk"));
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

  const sectionForKind: Record<string, string> = {
    metadata: "metadata",
    summary: "summary",
    key_point: "key_points",
    diagram: "diagram",
    takeaway: "takeaway",
    important_detail: "important_details",
    practical_action: "practical_actions",
  };

  async function focusMatch(): Promise<void> {
    if (
      !findQuery ||
      matchKind === "transcript_chunk" ||
      typeof document === "undefined"
    )
      return;
    await tick();
    const section = sectionForKind[matchKind] ?? "metadata";
    const container = document.querySelector<HTMLElement>(
      `[data-search-section="${section}"]`,
    );
    const target = container?.querySelector<HTMLElement>("mark") ?? container;
    target?.scrollIntoView({ behavior: "smooth", block: "center" });
    container?.classList.add("search-arrival");
  }

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

  $effect(() => {
    if (payload && findQuery && matchKind !== "transcript_chunk")
      void focusMatch();
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

  // Exports are plain downloads streamed by the API (ADR 0011); the browser
  // names the file from Content-Disposition. Transcript defaults off.
  let exportTranscript = $state(false);
  function exportHref(ext: "pdf" | "zip"): string {
    return `/api/exports/notes/${noteId}.${ext}${exportTranscript ? "?transcript=true" : ""}`;
  }

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
    <header class="note-header" data-search-section="metadata">
      <a class="back-link" href="/library">← Library</a>
      <div class="row between top">
        <h1 class="note-title">
          <Highlight
            text={note.source.title ?? `Video ${note.source.video_id}`}
            query={findQuery}
            caseSensitive={findCaseSensitive}
          />
        </h1>
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
      {#if payload}
        <details class="export">
          <summary>Export</summary>
          <div class="row wrap export-options">
            <label class="row">
              <input type="checkbox" bind:checked={exportTranscript} />
              Include transcript
            </label>
            <a class="button" href={exportHref("pdf")} download>PDF</a>
            <a class="button" href={exportHref("zip")} download
              >Markdown (ZIP)</a
            >
          </div>
        </details>
      {/if}
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
        {#if note.source.channel_name}<Highlight
            text={note.source.channel_name}
            query={findQuery}
            caseSensitive={findCaseSensitive}
          />
          <span aria-hidden="true">•</span>
        {/if}
        {#if note.source.duration_seconds != null}{duration(
            note.source.duration_seconds,
          )}
          <span aria-hidden="true">•</span>
        {/if}
        saved {when(note.saved_at)}
        {#if note.source.published_at}
          <span aria-hidden="true">•</span> published {day(
            note.source.published_at,
          )}{/if}
        <span aria-hidden="true">•</span>
        <a href={note.source.canonical_url} rel="noreferrer noopener"
          >watch on YouTube</a
        >
      </p>
      <p class="row tags">
        {#if note.primary_topic}
          <a
            class="badge topic"
            href={`/library?topic_id=${note.primary_topic.id}`}
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
      <section
        class="surface stack metadata-editor"
        aria-label="Topic and tags"
      >
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
        <section class="lead-summary" data-search-section="summary">
          <span class="section-index">Summary</span>
          <p class="summary" class:claim={payload.summary.speaker_claim}>
            <Highlight
              text={payload.summary.text}
              query={findQuery}
              caseSensitive={findCaseSensitive}
            />
          </p>
        </section>
      {/if}

      <section class="stack diagram-workspace" data-search-section="diagram">
        <div class="diagram-heading">
          <div>
            <span class="section-index">Visual map</span>
            <h2>{diagramName}</h2>
          </div>
          <span class="muted diagram-help">
            {#if payload.diagram.type === "mind_map" || payload.diagram.type === "process_flow"}
              {editing
                ? "Select to edit or add. Drag to arrange."
                : "Drag to explore. Use the controls to zoom."}
            {/if}
          </span>
        </div>
        {#if payload.diagram.type === "mind_map" || payload.diagram.type === "process_flow"}
          <DiagramCanvas
            diagram={payload.diagram}
            positions={note.layout.positions}
            editable={editing}
            query={findQuery}
            caseSensitive={findCaseSensitive}
            onmove={moved}
            {emit}
          />
        {:else if payload.diagram.type === "timeline"}
          <TimelineList
            diagram={payload.diagram}
            editable={editing}
            query={findQuery}
            caseSensitive={findCaseSensitive}
            {emit}
          />
        {:else}
          <ComparisonTable
            diagram={payload.diagram}
            editable={editing}
            query={findQuery}
            caseSensitive={findCaseSensitive}
            {emit}
          />
        {/if}
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
        <section class="content-section stack" data-search-section="key_points">
          <div class="section-heading">
            <span class="section-index">01</span>
            <h2>Key points</h2>
          </div>
          <ul class="blocks">
            {#each payload.key_points as block (block.id)}
              <li class:claim={block.speaker_claim} data-element-id={block.id}>
                <Highlight
                  text={block.text}
                  query={findQuery}
                  caseSensitive={findCaseSensitive}
                />
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
          <section
            class="content-section stack"
            data-search-section="important_details"
          >
            <div class="section-heading">
              <span class="section-index">02</span>
              <h2>Important details</h2>
            </div>
            <ul class="blocks">
              {#each payload.important_details as block (block.id)}
                <li
                  class:claim={block.speaker_claim}
                  data-element-id={block.id}
                >
                  <Highlight
                    text={block.text}
                    query={findQuery}
                    caseSensitive={findCaseSensitive}
                  />
                  {#if block.speaker_claim}<span class="badge"
                      >speaker claim</span
                    >{/if}
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        {#if payload.practical_actions.length > 0}
          <section
            class="content-section stack"
            data-search-section="practical_actions"
          >
            <div class="section-heading">
              <span class="section-index">03</span>
              <h2>What I can use</h2>
            </div>
            <ul class="blocks">
              {#each payload.practical_actions as block (block.id)}
                <li
                  class:claim={block.speaker_claim}
                  data-element-id={block.id}
                >
                  <Highlight
                    text={block.text}
                    query={findQuery}
                    caseSensitive={findCaseSensitive}
                  />
                </li>
              {/each}
            </ul>
          </section>
        {/if}
      {/if}

      <section class="takeaway-panel stack" data-search-section="takeaway">
        <div class="section-heading">
          <span class="section-index">Personal</span>
          <h2>My takeaway</h2>
        </div>
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

      <TranscriptPanel
        {noteId}
        query={findQuery}
        caseSensitive={findCaseSensitive}
        targetChunkId={targetChunk}
        openInitially={matchKind === "transcript_chunk"}
      />

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
  .note {
    gap: clamp(1.5rem, 4vw, 3.2rem);
  }
  .note-header {
    position: relative;
    padding-top: 0.35rem;
  }
  .back-link {
    display: inline-flex;
    margin-bottom: 1.1rem;
    color: var(--muted);
    font-size: 0.82rem;
    font-weight: 600;
    text-decoration: none;
  }
  .back-link:hover {
    color: var(--accent);
  }
  .top {
    align-items: flex-start;
    gap: 1.5rem;
  }
  .note-title {
    max-width: 28ch;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem 0.55rem;
    margin: 0.8rem 0 0;
    font-size: 0.82rem;
  }
  .meta span {
    color: var(--line-strong);
  }
  .tags {
    flex-wrap: wrap;
    margin: 0.65rem 0 0;
    gap: 0.4rem;
  }
  .topic {
    border-color: color-mix(in oklch, var(--accent) 50%, var(--line));
    color: var(--accent);
    text-decoration: none;
  }
  .actions {
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.4rem;
  }
  .export {
    position: relative;
    width: max-content;
    margin-top: 0.7rem;
    color: var(--muted);
    font-size: 0.82rem;
  }
  .export > summary {
    font-weight: 650;
  }
  .export-options {
    position: absolute;
    z-index: var(--z-popover);
    top: calc(100% + 0.45rem);
    min-width: 20rem;
    padding: 0.65rem;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background: var(--surface-raised);
    box-shadow: var(--shadow);
  }
  .metadata-editor {
    padding: 1rem;
    grid-template-columns: 1fr 2fr;
  }
  .section-index {
    display: block;
    margin-bottom: 0.32rem;
    color: var(--accent);
    font-size: 0.7rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .lead-summary {
    display: grid;
    grid-template-columns: minmax(5rem, 2fr) minmax(0, 8fr);
    gap: 1rem;
    padding: 1.2rem 0;
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }
  .lead-summary .summary {
    grid-column: 2;
    margin: 0;
    color: var(--ink);
    font-size: clamp(1.1rem, 2vw, 1.35rem);
    line-height: 1.55;
  }
  .lead-summary > .section-index {
    grid-column: 1;
    grid-row: 1;
  }
  .diagram-workspace {
    gap: 0.8rem;
  }
  .diagram-heading {
    display: flex;
    align-items: end;
    justify-content: space-between;
    gap: 1rem;
  }
  .diagram-heading h2 {
    font-size: clamp(1.25rem, 2.2vw, 1.65rem);
  }
  .diagram-help {
    max-width: 28rem;
    font-size: 0.78rem;
    text-align: right;
  }
  .content-section {
    display: grid;
    grid-template-columns: minmax(5rem, 2fr) minmax(0, 8fr);
    gap: 1rem;
    padding: 1.35rem 0;
    border-top: 1px solid var(--line);
  }
  .content-section .section-heading {
    grid-column: 1;
  }
  .content-section .blocks {
    grid-column: 2;
  }
  .section-heading {
    align-self: start;
  }
  .blocks {
    margin: 0;
    padding: 0;
    list-style: none;
    counter-reset: note-item;
  }
  .blocks > li {
    position: relative;
    padding-left: 1.3rem;
  }
  .blocks > li::before {
    position: absolute;
    top: 0.62em;
    left: 0;
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--accent);
    content: "";
  }
  .blocks li + li {
    margin-top: 0.7rem;
  }
  .claim {
    font-style: italic;
  }
  .takeaway-panel {
    padding: clamp(1rem, 3vw, 1.5rem);
    border: 1px solid color-mix(in oklch, var(--accent) 35%, var(--line));
    border-radius: var(--radius-lg) var(--radius-lg) var(--radius-lg) 5px;
    background: color-mix(in oklch, var(--accent-soft) 46%, var(--surface));
  }
  .takeaway-panel textarea {
    min-height: 7rem;
    border-color: transparent;
    background: color-mix(in oklch, var(--surface) 85%, transparent);
    resize: vertical;
  }
  details > summary {
    cursor: pointer;
    font-weight: 600;
  }
  textarea {
    width: 100%;
    resize: vertical;
  }
  q {
    font-style: italic;
  }
  .ok {
    color: var(--ok);
  }
  :global(.search-arrival) {
    animation: arrival 1.8s ease-out;
  }
  @keyframes arrival {
    0%,
    45% {
      outline: 3px solid var(--accent);
      outline-offset: 5px;
    }
    100% {
      outline-color: transparent;
    }
  }
  @media (max-width: 700px) {
    .top {
      display: grid;
    }
    .actions {
      justify-content: start;
    }
    .lead-summary,
    .content-section {
      grid-template-columns: 1fr;
      gap: 0.5rem;
    }
    .lead-summary .summary,
    .lead-summary > .section-index,
    .content-section .section-heading,
    .content-section .blocks {
      grid-column: 1;
    }
    .lead-summary > .section-index {
      grid-row: auto;
    }
    .diagram-heading {
      align-items: start;
    }
    .diagram-help {
      max-width: 14rem;
    }
    .metadata-editor {
      grid-template-columns: 1fr;
    }
  }
</style>

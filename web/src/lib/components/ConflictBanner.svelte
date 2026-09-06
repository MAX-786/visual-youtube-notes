<script lang="ts">
  // "N versions await your decision": compare each pending Revision with the
  // Active one and keep, take or activate it (ADR 0004). A Conflict or manual
  // Revision is taken by restore; a generated one that lost its
  // compare-and-set is activated without a copy.
  import type { components } from "@visual-notes/api-client";
  import { api, errorMessage } from "$lib/api";

  type Note = components["schemas"]["Note"];
  type RevisionSummary = components["schemas"]["RevisionSummary"];
  type RevisionPayload = components["schemas"]["RevisionPayload"];

  let { note, onchange }: { note: Note; onchange: () => void } = $props();

  let open = $state<string | null>(null);
  let payload = $state<RevisionPayload | null>(null);
  let message = $state<string | null>(null);
  let busy = $state(false);

  async function compare(revision: RevisionSummary) {
    open = revision.id;
    payload = null;
    const { data, error } = await api.GET(
      "/notes/{noteId}/revisions/{revisionId}",
      {
        params: { path: { noteId: note.id, revisionId: revision.id } },
      },
    );
    if (data) payload = data.payload;
    else message = errorMessage(error, "That version could not be loaded.");
  }

  async function decide(
    revision: RevisionSummary,
    action: "restore" | "activate" | "discard",
  ) {
    busy = true;
    const path = {
      params: { path: { noteId: note.id, revisionId: revision.id } },
    };
    const { error } =
      action === "restore"
        ? await api.POST("/notes/{noteId}/revisions/{revisionId}/restore", path)
        : action === "activate"
          ? await api.POST(
              "/notes/{noteId}/revisions/{revisionId}/activate",
              path,
            )
          : await api.POST(
              "/notes/{noteId}/revisions/{revisionId}/discard",
              path,
            );
    busy = false;
    if (error) {
      message = errorMessage(error, "That decision could not be saved.");
      return;
    }
    open = null;
    payload = null;
    message = null;
    onchange();
  }

  function takeLabel(revision: RevisionSummary): "restore" | "activate" {
    return revision.origin === "generated" ? "activate" : "restore";
  }
</script>

{#if note.pending_revisions.length > 0}
  <div class="card warn banner" role="status">
    <p>
      <strong>
        {note.pending_revisions.length === 1
          ? "1 version awaits your decision."
          : `${note.pending_revisions.length} versions await your decision.`}
      </strong>
      {#if note.pending_revisions.some((r) => r.origin === "conflict")}
        Another device edited this note at the same time; nothing was lost.
      {:else}
        A regenerated version is ready.
      {/if}
    </p>
    {#each note.pending_revisions as revision (revision.id)}
      <div class="row wrap">
        <span>
          Revision {revision.revision_number} ({revision.origin}) since
          {revision.awaiting_decision_since
            ? new Date(revision.awaiting_decision_since).toLocaleString()
            : ""}
        </span>
        <button type="button" onclick={() => compare(revision)}>Compare</button>
        <button
          type="button"
          class="primary"
          disabled={busy}
          onclick={() => decide(revision, takeLabel(revision))}
        >
          {revision.origin === "generated"
            ? "Use the regenerated version"
            : "Use this version"}
        </button>
        <button
          type="button"
          disabled={busy}
          onclick={() => decide(revision, "discard")}
        >
          Keep the current version
        </button>
      </div>
      {#if open === revision.id}
        <div class="compare">
          <div>
            <h3>Current</h3>
            {#if note.active_revision}
              <p>{note.active_revision.payload.summary.text}</p>
              <ul>
                {#each note.active_revision.payload.key_points as b (b.id)}<li>
                    {b.text}
                  </li>{/each}
              </ul>
            {/if}
          </div>
          <div>
            <h3>Revision {revision.revision_number}</h3>
            {#if payload}
              <p>{payload.summary.text}</p>
              <ul>
                {#each payload.key_points as b (b.id)}<li>{b.text}</li>{/each}
              </ul>
            {:else}
              <p class="muted">Loading…</p>
            {/if}
          </div>
        </div>
      {/if}
    {/each}
    {#if message}<p class="error">{message}</p>{/if}
  </div>
{/if}

<style>
  .banner p {
    margin: 0 0 0.5rem;
  }
  .wrap {
    flex-wrap: wrap;
    margin-top: 0.5rem;
  }
  .compare {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(16rem, 1fr));
    gap: 1rem;
    margin-top: 0.75rem;
    padding-top: 0.75rem;
    border-top: 1px solid var(--line);
  }
  h3 {
    margin: 0 0 0.25rem;
    font-size: 1rem;
  }
</style>

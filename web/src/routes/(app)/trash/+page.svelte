<script lang="ts">
  // Trash: Notes deleted within the last thirty days, restorable with the
  // state they had, or purged now. A purge leaves no dependent rows; the
  // month's spend stays (ADR 0007).
  import { onMount } from "svelte";
  import NoteCardView from "$lib/components/NoteCard.svelte";
  import { api, errorMessage } from "$lib/api";
  import { deleteNote, putNoteCard, type NoteCard } from "$lib/db";

  let cards = $state<NoteCard[]>([]);
  let message = $state<string | null>(null);
  let failed = $state(false);
  let busy = $state<string | null>(null);

  async function load() {
    const { data, error } = await api.GET("/notes", {
      params: { query: { trashed: true, limit: 100 } },
    });
    if (!data) {
      failed = true;
      message = errorMessage(error, "The trash could not be loaded.");
      return;
    }
    cards = data.notes;
  }

  function purgeDate(card: NoteCard): string {
    return card.purge_after
      ? new Date(card.purge_after).toLocaleDateString()
      : "";
  }

  async function restore(card: NoteCard) {
    busy = card.id;
    const { data, error } = await api.POST("/notes/{noteId}/restore", {
      params: { path: { noteId: card.id } },
    });
    busy = null;
    if (!data) {
      failed = true;
      message = errorMessage(error, "The note could not be restored.");
      return;
    }
    await putNoteCard(data);
    failed = false;
    message = `Restored “${data.source.title ?? data.source.video_id}” with the state it had.`;
    await load();
  }

  async function purge(card: NoteCard) {
    if (
      !confirm(
        "Delete this note for good? Its versions, transcript and search entries go with it. This cannot be undone.",
      )
    )
      return;
    busy = card.id;
    const { error, response } = await api.POST("/notes/{noteId}/purge", {
      params: { path: { noteId: card.id } },
    });
    busy = null;
    if (!response.ok) {
      failed = true;
      message = errorMessage(error, "The note could not be purged.");
      return;
    }
    await deleteNote(card.id);
    failed = false;
    message = "Deleted for good.";
    await load();
  }

  onMount(() => void load());
</script>

<svelte:head>
  <title>Trash · Visual Notes</title>
</svelte:head>

<h1>Trash</h1>
<p class="muted">
  Deleted notes stay here for thirty days, then are removed for good. Restoring
  brings a note back with the state it had, at its original place in Today.
</p>
{#if message}
  <p class={failed ? "error" : "ok"} role="status">{message}</p>
{/if}

{#if cards.length === 0}
  <p class="muted">The trash is empty.</p>
{:else}
  <ul class="stack plain" aria-label="Trashed notes">
    {#each cards as card (card.id)}
      <li class="stack entry">
        <NoteCardView {card} />
        <div class="row wrap tools">
          <span class="muted small">Removed for good on {purgeDate(card)}</span>
          <button
            type="button"
            class="primary"
            disabled={busy === card.id}
            onclick={() => restore(card)}>Restore</button
          >
          <button
            type="button"
            class="danger"
            disabled={busy === card.id}
            onclick={() => purge(card)}>Delete for good</button
          >
        </div>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .plain {
    list-style: none;
    padding: 0;
    margin: 1rem 0 0;
  }
  .entry {
    gap: 0.35rem;
  }
  .tools {
    gap: 0.5rem;
    justify-content: flex-end;
  }
  .small {
    font-size: 0.85rem;
    margin-right: auto;
  }
  .ok {
    color: var(--accent);
  }
</style>

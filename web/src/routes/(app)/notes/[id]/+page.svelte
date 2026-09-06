<script lang="ts">
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import { getNote, putNoteDetail, type Note } from "$lib/db";
  import { onSync } from "$lib/live.svelte";

  let note = $state<Note | null>(null);
  let error = $state<string | null>(null);

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
</script>

<svelte:head>
  <title>{note?.source.title ?? "Note"} · Visual Notes</title>
</svelte:head>

{#if error}
  <p class="error">{error}</p>
{:else if !note}
  <p class="muted">Loading…</p>
{:else}
  <h1>{note.source.title ?? `Video ${note.source.video_id}`}</h1>
  <p class="muted">
    {note.source.channel_name ?? ""} ·
    <a href={note.source.canonical_url} rel="noreferrer">watch on YouTube</a>
  </p>
  <p><span class="badge">{note.processing.state}</span></p>
  {#if note.active_revision}
    <section class="card stack">
      <h2>Summary</h2>
      <p>{note.active_revision.payload.summary.text}</p>
      <h2>Key points</h2>
      <ul>
        {#each note.active_revision.payload.key_points as point (point.id)}
          <li>{point.text}</li>
        {/each}
      </ul>
    </section>
  {:else}
    <p class="muted">The note is still being made. Progress shows on Today.</p>
  {/if}
{/if}

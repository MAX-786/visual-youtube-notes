<script lang="ts">
  // Today: the date-first feed with processing cards (spec 6). Cards come
  // from the cache first and the server when online; sync and progress
  // frames update them in place.
  import { onMount } from "svelte";
  import NoteCardView from "$lib/components/NoteCard.svelte";
  import { api, errorMessage } from "$lib/api";
  import {
    deleteNote,
    listCachedNotes,
    putNoteCard,
    type NoteCard,
  } from "$lib/db";
  import { onProgress, onResync, onSync } from "$lib/live.svelte";
  import { session } from "$lib/session.svelte";

  let notes = $state<NoteCard[]>([]);
  let url = $state("");
  let message = $state<string | null>(null);
  let busy = $state(false);

  const groups = $derived(groupByDay(notes));

  function groupByDay(cards: NoteCard[]): { day: string; cards: NoteCard[] }[] {
    const out: { day: string; cards: NoteCard[] }[] = [];
    for (const card of cards) {
      const day = new Date(card.saved_at).toLocaleDateString(undefined, {
        weekday: "long",
        day: "numeric",
        month: "long",
      });
      const group = out.find((g) => g.day === day);
      if (group) group.cards.push(card);
      else out.push({ day, cards: [card] });
    }
    return out;
  }

  async function loadFromCache() {
    notes = (await listCachedNotes()).map((row) => row.card);
  }

  async function loadFromServer() {
    if (session.status !== "signed-in") return;
    const { data } = await api.GET("/notes", {
      params: { query: { limit: 100 } },
    });
    if (!data) return;
    for (const card of data.notes) await putNoteCard(card);
    await loadFromCache();
  }

  async function refreshOne(noteId: string) {
    const { data, response } = await api.GET("/notes/{noteId}", {
      params: { path: { noteId } },
    });
    if (response.status === 404) {
      await deleteNote(noteId);
    } else if (data) {
      await putNoteCard(data);
    }
    await loadFromCache();
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    busy = true;
    message = null;
    const { data, error } = await api.POST("/submissions", { body: { url } });
    busy = false;
    if (!data) {
      message = errorMessage(error, "Could not submit that link.");
      return;
    }
    url = "";
    await putNoteCard(data.note);
    await loadFromCache();
    message =
      data.outcome === "created"
        ? "Added."
        : data.outcome === "duplicate"
          ? "Already saved."
          : "Saved. It needs your cost confirmation in Settings → Usage.";
  }

  onMount(() => {
    void loadFromCache().then(loadFromServer);
    const offSync = onSync((event) => {
      if (event.entity_kind !== "note") return;
      if (event.change === "deleted") {
        void deleteNote(event.entity_id).then(loadFromCache);
      } else {
        void refreshOne(event.entity_id);
      }
    });
    const offProgress = onProgress((event) => {
      const card = notes.find((n) => n.id === event.note_id);
      if (card) {
        card.processing.state = event.processing_state;
        notes = [...notes];
      } else {
        void refreshOne(event.note_id);
      }
    });
    const offResync = onResync(() => void loadFromServer());
    return () => {
      offSync();
      offProgress();
      offResync();
    };
  });
</script>

<svelte:head>
  <title>Today · Visual Notes</title>
</svelte:head>

<h1>Today</h1>

<form class="card row" onsubmit={submit}>
  <label class="field grow">
    <span>Add YouTube link</span>
    <input
      type="url"
      bind:value={url}
      placeholder="https://www.youtube.com/watch?v=…"
      required
    />
  </label>
  <button
    class="primary"
    type="submit"
    disabled={busy || session.status !== "signed-in"}>Add</button
  >
  {#if message}
    <p class="muted" role="status">{message}</p>
  {/if}
</form>

{#if groups.length === 0}
  <p class="muted">
    Nothing saved yet. Share a video from YouTube or paste a link above.
  </p>
{/if}

{#each groups as group (group.day)}
  <section class="day">
    <h2>{group.day}</h2>
    <div class="stack">
      {#each group.cards as card (card.id)}
        <NoteCardView {card} />
      {/each}
    </div>
  </section>
{/each}

<style>
  .grow {
    flex: 1;
    min-width: 16rem;
  }
  .day {
    margin-top: 1.5rem;
  }
</style>

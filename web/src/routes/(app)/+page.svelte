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
  let loading = $state(true);

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
    loading = false;
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

<header class="today-head">
  <div>
    <p class="eyebrow">Daily capture</p>
    <h1>Today</h1>
  </div>
  <p>
    Save the signal. The summary, visual map, and useful details arrive
    together.
  </p>
</header>

<section class="capture">
  <div class="capture-copy">
    <span class="step">01</span>
    <div>
      <h2>Add a video</h2>
      <p>
        Paste one YouTube link. You can keep browsing while the note is made.
      </p>
    </div>
  </div>
  <form onsubmit={submit}>
    <label class="field grow">
      <span class="visually-hidden">Add YouTube link</span>
      <input
        type="url"
        bind:value={url}
        placeholder="Paste a YouTube link"
        required
      />
    </label>
    <button
      class="primary"
      type="submit"
      disabled={busy || session.status !== "signed-in"}
      >{busy ? "Adding…" : "Add note"}</button
    >
  </form>
  {#if message}<p class="capture-message" role="status">{message}</p>{/if}
</section>

{#if loading}
  <div class="today-loading">
    <span class="skeleton"></span><span class="skeleton"></span>
  </div>
{:else if groups.length === 0}
  <div class="empty">
    <h2>No notes yet</h2>
    <p>Share a video from YouTube or paste its link above.</p>
  </div>
{/if}

{#each groups as group (group.day)}
  <section class="day">
    <div class="day-heading">
      <h2>{group.day}</h2>
      <span
        >{group.cards.length}
        {group.cards.length === 1 ? "note" : "notes"}</span
      >
    </div>
    <div class="notes">
      {#each group.cards as card (card.id)}
        <NoteCardView {card} />
      {/each}
    </div>
  </section>
{/each}

<style>
  .today-head {
    display: grid;
    grid-template-columns: 1fr minmax(16rem, 30rem);
    align-items: end;
    gap: 2rem;
    margin: 0.5rem 0 2rem;
  }
  .today-head > p {
    margin: 0;
    color: var(--muted);
    font-size: 1rem;
  }
  .eyebrow {
    margin: 0 0 0.25rem;
    color: var(--accent);
    font-size: 0.75rem;
    font-weight: 700;
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  .capture {
    display: grid;
    grid-template-columns: 4fr 6fr;
    gap: clamp(1rem, 4vw, 3rem);
    padding: clamp(1rem, 3vw, 1.5rem);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius-lg);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .capture-copy {
    display: grid;
    grid-template-columns: auto 1fr;
    align-items: start;
    gap: 0.8rem;
  }
  .capture-copy h2,
  .capture-copy p {
    margin: 0;
  }
  .capture-copy p {
    margin-top: 0.25rem;
    color: var(--muted);
    font-size: 0.84rem;
  }
  .step {
    color: var(--accent);
    font-size: 0.72rem;
    font-weight: 700;
  }
  form {
    display: grid;
    grid-template-columns: 1fr auto;
    align-items: center;
    gap: 0.5rem;
  }
  .grow {
    min-width: 0;
  }
  .capture-message {
    grid-column: 2;
    margin: -1rem 0 0;
    color: var(--accent);
    font-size: 0.82rem;
  }
  .day {
    margin-top: clamp(2rem, 5vw, 3.6rem);
  }
  .day-heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    padding-bottom: 0.55rem;
    border-bottom: 1px solid var(--line-strong);
  }
  .day-heading h2 {
    font-size: 0.9rem;
  }
  .day-heading span {
    color: var(--muted);
    font-size: 0.75rem;
  }
  .notes {
    display: grid;
  }
  .empty {
    margin: 4rem auto;
    text-align: center;
  }
  .empty p {
    color: var(--muted);
  }
  .today-loading {
    display: grid;
    gap: 1rem;
    margin-top: 2rem;
  }
  .today-loading span {
    display: block;
    width: 70%;
    height: 1rem;
  }
  @media (max-width: 720px) {
    .today-head,
    .capture {
      grid-template-columns: 1fr;
      gap: 1rem;
    }
    form {
      grid-template-columns: 1fr;
    }
    form button {
      width: 100%;
    }
    .capture-message {
      grid-column: 1;
      margin: 0;
    }
  }
</style>

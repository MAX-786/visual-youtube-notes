<script lang="ts">
  // The share_target action (spec 5.2). Reachable signed out so a share is
  // never lost: url, text and title are scanned in that order for every
  // YouTube link; one link submits, several offer a picker, none shows the
  // manual field pre-filled. Offline or signed out, the link waits in the
  // holding pen and drains once a session is live.
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { session } from "$lib/session.svelte";
  import {
    submitOrQueue,
    videoIdsFromShare,
    watchUrl,
    type ShareOutcome,
  } from "$lib/share";

  type Status =
    "parsing" | "picker" | "manual" | "submitting" | "queued" | "rejected";
  let status = $state<Status>("parsing");
  let ids = $state<string[]>([]);
  let manual = $state("");
  let message = $state("");

  function afterOutcome(outcome: ShareOutcome) {
    switch (outcome.kind) {
      case "created":
      case "duplicate":
        void goto(`/notes/${outcome.noteId}`);
        break;
      case "held":
        void goto("/");
        break;
      case "queued":
        status = "queued";
        if (session.status !== "signed-in") {
          session.pendingPath = "/";
          void goto("/sign-in");
        }
        break;
      case "rejected":
        status = "rejected";
        message = outcome.message;
    }
  }

  async function submit(videoId: string) {
    status = "submitting";
    afterOutcome(await submitOrQueue(watchUrl(videoId)));
  }

  async function submitManual(event: SubmitEvent) {
    event.preventDefault();
    const found = videoIdsFromShare({ text: manual });
    if (found.length === 0) {
      status = "rejected";
      message = "That is not a YouTube video link.";
      return;
    }
    await submit(found[0]);
  }

  onMount(async () => {
    // The session gate must answer first, so a signed-out share is queued
    // without a server round trip and survives the sign-in.
    while (session.status === "unknown") {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    const params = page.url.searchParams;
    ids = videoIdsFromShare({
      url: params.get("url"),
      text: params.get("text"),
      title: params.get("title"),
    });
    if (ids.length === 1) {
      await submit(ids[0]);
    } else if (ids.length > 1) {
      status = "picker";
    } else {
      manual = [params.get("url"), params.get("text"), params.get("title")]
        .filter(Boolean)
        .join("\n");
      status = "manual";
    }
  });
</script>

<svelte:head>
  <title>Share · Visual Notes</title>
</svelte:head>

<main class="page">
  <div class="card stack" aria-live="polite">
    {#if status === "parsing" || status === "submitting"}
      <p>Saving the shared link…</p>
    {:else if status === "picker"}
      <h1>Which video?</h1>
      <p class="muted">The share carried several YouTube links.</p>
      <ul class="stack plain" aria-label="Shared videos">
        {#each ids as id (id)}
          <li class="row between">
            <a href={watchUrl(id)} rel="noreferrer noopener" target="_blank"
              >{watchUrl(id)}</a
            >
            <button type="button" class="primary" onclick={() => submit(id)}
              >Save this one</button
            >
          </li>
        {/each}
      </ul>
    {:else if status === "manual"}
      <h1>No YouTube link found</h1>
      <form class="stack" aria-label="Paste a link" onsubmit={submitManual}>
        <label class="field">
          <span>What was shared; paste or fix the link</span>
          <textarea rows="3" bind:value={manual}></textarea>
        </label>
        <div class="row">
          <button type="submit" class="primary">Save</button>
          <a class="button" href="/">Go to Today</a>
        </div>
      </form>
    {:else if status === "queued"}
      <p role="status">
        Saved for later. It will be submitted as soon as you are signed in and
        online.
      </p>
      <a class="button" href="/">Go to Today</a>
    {:else}
      <p class="error" role="alert">{message}</p>
      <a class="button" href="/">Go to Today</a>
    {/if}
  </div>
</main>

<style>
  .plain {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .between {
    justify-content: space-between;
    gap: 1rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
</style>

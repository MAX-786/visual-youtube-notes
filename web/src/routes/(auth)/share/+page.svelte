<script lang="ts">
  // The share_target action (spec 5.2). Reachable signed out so a share is
  // never lost: the URL lands in the holding pen and drains once a session is
  // live. URL selection and the picker are completed by the share slice.
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { db } from "$lib/db";
  import { session } from "$lib/session.svelte";

  let status = $state<"parsing" | "queued" | "empty">("parsing");

  const hosts = new Set([
    "youtube.com",
    "www.youtube.com",
    "m.youtube.com",
    "youtu.be",
  ]);

  function videoIds(text: string): string[] {
    const found: string[] = [];
    for (const match of text.matchAll(/https?:\/\/[^\s<>"']+/g)) {
      try {
        const url = new URL(match[0]);
        if (!hosts.has(url.hostname)) continue;
        const id =
          url.hostname === "youtu.be"
            ? url.pathname.slice(1).split("/")[0]
            : (url.searchParams.get("v") ??
              url.pathname.match(
                /^\/(?:shorts|live|embed)\/([A-Za-z0-9_-]{11})/,
              )?.[1] ??
              "");
        if (/^[A-Za-z0-9_-]{11}$/.test(id) && !found.includes(id))
          found.push(id);
      } catch {
        // not a URL
      }
    }
    return found;
  }

  onMount(async () => {
    const params = page.url.searchParams;
    const text = [params.get("url"), params.get("text"), params.get("title")]
      .filter(Boolean)
      .join("\n");
    const ids = videoIds(text);
    if (ids.length === 0) {
      status = "empty";
      return;
    }
    const database = await db();
    for (const id of ids) {
      await database.put("pending_submissions", {
        submission_id: crypto.randomUUID(),
        url: `https://www.youtube.com/watch?v=${id}`,
        created_at: new Date().toISOString(),
      });
    }
    status = "queued";
    if (session.status === "signed-in") await goto("/");
    else {
      session.pendingPath = "/";
      await goto("/sign-in");
    }
  });
</script>

<main class="page">
  <div class="card">
    {#if status === "parsing"}
      <p>Reading the shared link…</p>
    {:else if status === "queued"}
      <p>
        Saved. It will be submitted as soon as you are signed in and online.
      </p>
    {:else}
      <p>No YouTube link was found in what was shared.</p>
      <a class="button" href="/">Go to Today</a>
    {/if}
  </div>
</main>

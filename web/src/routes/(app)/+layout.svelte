<script lang="ts">
  // The signed-in shell. Note content stays curtained until the session gate
  // answers; offline reveals the cache unchecked (ADR 0012, spec 17.9).
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import Nav from "$lib/components/Nav.svelte";
  import { startLive, stopLive } from "$lib/live.svelte";
  import { startQueue } from "$lib/queue.svelte";
  import { session } from "$lib/session.svelte";

  let { children } = $props();

  $effect(() => {
    if (session.status === "signed-out") {
      session.pendingPath = page.url.pathname + page.url.search;
      void goto("/sign-in");
    }
  });

  let queueStarted = false;
  $effect(() => {
    if (session.status === "signed-in") {
      startLive();
      if (!queueStarted) {
        queueStarted = true;
        startQueue();
      }
    } else stopLive();
  });
</script>

{#if session.status === "unknown"}
  <div class="curtain" aria-live="polite">Checking your session…</div>
{:else if session.status === "signed-in" || session.status === "offline"}
  <a class="button skip-link" href="#main-content">Skip to content</a>
  <Nav />
  <main class="page" id="main-content" tabindex="-1">
    {@render children()}
  </main>
{/if}

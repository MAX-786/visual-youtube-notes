<script lang="ts">
  // The signed-in shell. Note content stays curtained until the session gate
  // answers; offline reveals the cache unchecked (ADR 0012, spec 17.9).
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import Nav from "$lib/components/Nav.svelte";
  import { startLive, stopLive } from "$lib/live.svelte";
  import { session } from "$lib/session.svelte";

  let { children } = $props();

  $effect(() => {
    if (session.status === "signed-out") {
      session.pendingPath = page.url.pathname + page.url.search;
      void goto("/sign-in");
    }
  });

  $effect(() => {
    if (session.status === "signed-in") startLive();
    else stopLive();
  });
</script>

{#if session.status === "unknown"}
  <div class="curtain" aria-live="polite">Checking your session…</div>
{:else if session.status === "signed-in" || session.status === "offline"}
  <Nav />
  <main class="page">
    {@render children()}
  </main>
{/if}

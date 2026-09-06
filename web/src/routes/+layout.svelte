<script lang="ts">
  import "@fontsource/noto-sans/400.css";
  import "@fontsource/noto-sans/600.css";
  import "@fontsource/noto-sans/700.css";
  import "../app.css";
  import { onMount } from "svelte";
  import { bootSession } from "$lib/session.svelte";

  let { children } = $props();

  onMount(() => {
    void bootSession();
  });
</script>

{@render children()}

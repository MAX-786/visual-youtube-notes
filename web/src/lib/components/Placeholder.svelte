<script lang="ts">
  let { title, slice }: { title: string; slice: string } = $props();
</script>

<svelte:head>
  <title>{title} · Visual Notes</title>
</svelte:head>

<h1>{title}</h1>
<p class="muted">This screen arrives with the "{slice}" slice.</p>

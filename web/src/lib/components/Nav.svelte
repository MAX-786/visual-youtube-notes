<script lang="ts">
  import { page } from "$app/state";
  import { live } from "$lib/live.svelte";
  import { session } from "$lib/session.svelte";

  const items = [
    { href: "/", label: "Today" },
    { href: "/library", label: "Library" },
    { href: "/topics", label: "Topics" },
    { href: "/settings", label: "Settings" },
    { href: "/trash", label: "Trash" },
  ];

  function active(href: string): boolean {
    return (
      page.url.pathname === href || page.url.pathname.startsWith(href + "/")
    );
  }
</script>

<nav class="nav" aria-label="Primary">
  <a class="brand" href="/">Visual Notes</a>
  <ul>
    {#each items as item (item.href)}
      <li>
        <a
          href={item.href}
          aria-current={active(item.href) ? "page" : undefined}>{item.label}</a
        >
      </li>
    {/each}
  </ul>
  <span class="status muted" title="Live updates">
    {#if session.status === "offline"}
      offline
    {:else if live.mode === "stream"}
      live
    {:else if live.mode === "polling"}
      polling
    {:else}
      &nbsp;
    {/if}
  </span>
</nav>

<style>
  .nav {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.5rem 1rem;
    border-bottom: 1px solid var(--line);
    background: var(--surface);
    position: sticky;
    top: 0;
    z-index: 10;
  }
  .brand {
    font-weight: 700;
    text-decoration: none;
    color: var(--ink);
  }
  ul {
    display: flex;
    gap: 0.25rem;
    list-style: none;
    margin: 0;
    padding: 0;
    flex: 1;
    overflow-x: auto;
  }
  li a {
    display: inline-flex;
    align-items: center;
    min-height: var(--touch);
    padding: 0 0.75rem;
    border-radius: var(--radius);
    text-decoration: none;
    color: var(--ink);
  }
  li a[aria-current="page"] {
    background: var(--bg);
    font-weight: 600;
  }
  .status {
    font-size: 0.85rem;
  }
</style>

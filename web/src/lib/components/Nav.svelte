<script lang="ts">
  import { page } from "$app/state";
  import { live } from "$lib/live.svelte";
  import { session } from "$lib/session.svelte";
  import ThemeControl from "./ThemeControl.svelte";

  const items = [
    { href: "/", label: "Today" },
    { href: "/library", label: "Library" },
    { href: "/settings", label: "Settings" },
    { href: "/trash", label: "Trash" },
  ];

  function active(href: string): boolean {
    return (
      page.url.pathname === href || page.url.pathname.startsWith(href + "/")
    );
  }

  let status = $derived(
    session.status === "offline"
      ? "Offline"
      : live.mode === "stream"
        ? "Live"
        : live.mode === "polling"
          ? "Syncing"
          : "",
  );
</script>

<nav class="nav" aria-label="Primary">
  <div class="nav-inner">
    <a class="brand" href="/" aria-label="Visual Notes home">
      <span class="brand-mark" aria-hidden="true">VN</span>
      <span>Visual Notes</span>
    </a>
    <ul>
      {#each items as item (item.href)}
        <li>
          <a
            href={item.href}
            aria-current={active(item.href) ? "page" : undefined}
          >
            {item.label}
          </a>
        </li>
      {/each}
    </ul>
    <div class="utilities">
      {#if status}
        <span class:offline={session.status === "offline"} class="status">
          <span aria-hidden="true"></span>{status}
        </span>
      {/if}
      <ThemeControl />
    </div>
  </div>
</nav>

<style>
  .nav {
    position: sticky;
    top: 0;
    z-index: var(--z-nav);
    border-bottom: 1px solid color-mix(in oklch, var(--line) 82%, transparent);
    background: color-mix(in oklch, var(--surface) 86%, transparent);
    backdrop-filter: blur(18px) saturate(1.15);
  }
  .nav-inner {
    width: min(100%, 86rem);
    min-height: var(--nav-height);
    margin: 0 auto;
    padding: 0.55rem clamp(1rem, 3vw, 2rem);
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    gap: clamp(0.8rem, 3vw, 2.4rem);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 0.65rem;
    color: var(--ink);
    font-weight: 700;
    letter-spacing: -0.02em;
    text-decoration: none;
  }
  .brand-mark {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border-radius: 9px 9px 9px 3px;
    background: var(--accent);
    color: var(--accent-ink);
    font-size: 0.67rem;
    letter-spacing: 0.04em;
  }
  ul {
    display: flex;
    gap: 0.15rem;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li a {
    display: inline-flex;
    align-items: center;
    min-height: 38px;
    padding: 0 0.7rem;
    border-radius: var(--radius-sm);
    text-decoration: none;
    color: var(--muted);
    font-size: 0.9rem;
    font-weight: 600;
  }
  li a:hover {
    color: var(--ink);
    background: var(--surface-muted);
  }
  li a[aria-current="page"] {
    color: var(--ink);
    background: var(--accent-soft);
  }
  .utilities {
    display: flex;
    align-items: center;
    gap: 0.65rem;
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--muted);
    font-size: 0.78rem;
    font-weight: 600;
  }
  .status span {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--ok);
    box-shadow: 0 0 0 3px var(--ok-soft);
  }
  .status.offline span {
    background: var(--warn);
    box-shadow: 0 0 0 3px var(--warn-soft);
  }
  @media (max-width: 760px) {
    .nav-inner {
      gap: 0.4rem;
      padding-inline: 0.7rem;
    }
    .brand > span:last-child,
    .status {
      display: none;
    }
    ul {
      justify-self: center;
      max-width: 100%;
      overflow-x: auto;
    }
    li a {
      padding-inline: 0.5rem;
      font-size: 0.82rem;
    }
  }
  @media (max-width: 440px) {
    .brand-mark {
      width: 29px;
      height: 29px;
    }
    li:last-child {
      display: none;
    }
  }
</style>

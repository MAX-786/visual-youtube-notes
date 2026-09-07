<script lang="ts">
  import { onMount } from "svelte";

  type Theme = "system" | "light" | "dark";
  let theme: Theme = $state("system");

  function apply(next: Theme): void {
    theme = next;
    if (next === "system") {
      document.documentElement.removeAttribute("data-theme");
    } else {
      document.documentElement.dataset.theme = next;
    }
    localStorage.setItem("vyn-theme", next);
  }

  onMount(() => {
    const saved = localStorage.getItem("vyn-theme");
    if (saved === "light" || saved === "dark" || saved === "system") {
      theme = saved;
      apply(saved);
    }
  });
</script>

<label class="theme-control" title="Color theme">
  <span class="visually-hidden">Color theme</span>
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <path
      d="M12 3v2m0 14v2M3 12h2m14 0h2M5.6 5.6 7 7m10 10 1.4 1.4M18.4 5.6 17 7M7 17l-1.4 1.4"
    />
    <circle cx="12" cy="12" r="4" />
  </svg>
  <select
    value={theme}
    onchange={(event) => apply(event.currentTarget.value as Theme)}
  >
    <option value="system">System</option>
    <option value="light">Light</option>
    <option value="dark">Dark</option>
  </select>
</label>

<style>
  .theme-control {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    min-height: 36px;
    padding-left: 0.55rem;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--surface);
  }
  svg {
    width: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.7;
    stroke-linecap: round;
  }
  select {
    width: auto;
    min-height: 34px;
    padding: 0.25rem 1.8rem 0.25rem 0.25rem;
    border: 0;
    background-color: transparent;
    font-size: 0.82rem;
  }
  @media (max-width: 700px) {
    .theme-control {
      padding: 0;
      border: 0;
    }
    svg {
      display: none;
    }
  }
</style>

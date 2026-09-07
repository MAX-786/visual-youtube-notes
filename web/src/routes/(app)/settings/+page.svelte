<svelte:head>
  <title>Settings · Visual Notes</title>
</svelte:head>

<header class="settings-head">
  <p>Workspace</p>
  <h1>Settings</h1>
  <span>Keep the system quiet, private, and predictable.</span>
</header>
<ul class="sections">
  <li>
    <a href="/settings/account"
      ><span
        ><strong>Account and devices</strong><small
          >Sessions, password, and trusted browsers</small
        ></span
      ><b aria-hidden="true">→</b></a
    >
  </li>
  <li>
    <a href="/settings/ai"
      ><span
        ><strong>AI providers and models</strong><small
          >Private keys and role-by-role model choices</small
        ></span
      ><b aria-hidden="true">→</b></a
    >
  </li>
  <li>
    <a href="/settings/usage"
      ><span
        ><strong>Usage and the monthly cap</strong><small
          >Costs, limits, and held notes</small
        ></span
      ><b aria-hidden="true">→</b></a
    >
  </li>
  <li>
    <a href="/settings/notifications"
      ><span
        ><strong>Notifications</strong><small
          >Choose the events worth interrupting you for</small
        ></span
      ><b aria-hidden="true">→</b></a
    >
  </li>
  <li>
    <a href="/settings/health"
      ><span
        ><strong>Health, backups and export</strong><small
          >System checks and library portability</small
        ></span
      ><b aria-hidden="true">→</b></a
    >
  </li>
</ul>

<style>
  .settings-head {
    display: grid;
    gap: 0.2rem;
    margin: 0.5rem 0 2rem;
  }
  .settings-head p {
    margin: 0;
    color: var(--accent);
    font-size: 0.75rem;
    font-weight: 700;
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  .settings-head span {
    color: var(--muted);
  }
  .sections {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--line-strong);
    list-style: none;
  }
  .sections li {
    border-bottom: 1px solid var(--line);
  }
  .sections li:nth-child(odd) {
    border-right: 1px solid var(--line);
  }
  .sections a {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    min-height: 7.5rem;
    padding: 1.2rem;
    color: inherit;
    text-decoration: none;
    transition:
      background-color 160ms ease,
      padding 180ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .sections a:hover {
    padding-left: 1.5rem;
    background: color-mix(in oklch, var(--accent-soft) 45%, transparent);
  }
  .sections span {
    display: grid;
    gap: 0.28rem;
  }
  .sections small {
    color: var(--muted);
    font-weight: 400;
  }
  .sections b {
    color: var(--accent);
    font-size: 1.25rem;
    font-weight: 400;
  }
  @media (max-width: 650px) {
    .sections {
      grid-template-columns: 1fr;
    }
    .sections li:nth-child(odd) {
      border-right: 0;
    }
    .sections a {
      min-height: 6rem;
    }
  }
</style>

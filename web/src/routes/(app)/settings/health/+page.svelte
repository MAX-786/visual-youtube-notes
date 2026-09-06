<script lang="ts">
  // Settings → Health: the Library backup is a plain download the API
  // streams (ADR 0011). Backups, health and the restore drill arrive with
  // the "Backups, health, alerts, and the first restore drill" slice.
</script>

<svelte:head>
  <title>Health · Visual Notes</title>
</svelte:head>

<h1>Health</h1>

<section class="card stack" aria-label="Library backup">
  <h2>Library backup</h2>
  <p>
    One JSON document with every note, transcript, revision, topic, tag and
    takeaway, and a checksum on each record. Nothing that could sign in travels
    with it.
  </p>
  <div class="row wrap">
    <a class="button primary" href="/api/exports/library" download
      >Download library archive</a
    >
  </div>
  <p class="muted small">
    Check a downloaded archive with <code
      >node docs/design/verify-archive.mjs library-YYYY-MM-DD.json</code
    > from the repository.
  </p>
</section>

<section class="card stack" aria-label="Backups and health">
  <h2>Backups and health</h2>
  <p class="muted">
    This screen arrives with the "Backups, health, alerts, and the first restore
    drill" slice.
  </p>
</section>

<style>
  h2 {
    margin: 0;
  }
  .small {
    font-size: 0.85rem;
    margin: 0;
  }
  code {
    font-size: 0.85em;
  }
</style>

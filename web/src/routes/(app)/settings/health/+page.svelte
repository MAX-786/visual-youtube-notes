<script lang="ts">
  // Settings → Health (ADR 0009): every system_health row, read as unknown
  // once its writer has gone quiet; the Notes blocked by extractor breakage
  // and the retry that ends the yt-dlp update; the restore drill log and
  // which repository is due; and the Library backup download (ADR 0011).
  import { onMount } from "svelte";
  import type { components } from "@visual-notes/api-client";
  import { api, errorMessage } from "$lib/api";

  type Report = components["schemas"]["HealthReport"];
  type Check = components["schemas"]["HealthCheck"];

  let report = $state<Report | null>(null);
  let message = $state<string | null>(null);
  let failed = $state(false);
  let busy = $state(false);

  // The drill form: what the runbook's step 8 records.
  let repository = $state<"local" | "offsite">("local");
  let snapshotId = $state("");
  let restoreSeconds = $state("");
  let keyRecovery = $state(false);
  let postgresVersion = $state("");
  let pgvectorVersion = $state("");
  let drillNotes = $state("");

  const labels: Record<string, string> = {
    backup_local: "Local backup",
    backup_offsite: "Off-server copy",
    disk_free: "Disk free",
    extractor_breakage: "yt-dlp",
    worker_stale: "Worker",
    river_leader_lost: "Job scheduler",
  };

  function label(check: Check): string {
    return labels[check.check_name] ?? check.check_name;
  }

  function ago(iso: string): string {
    const minutes = Math.max(
      0,
      Math.round((Date.now() - new Date(iso).getTime()) / 60_000),
    );
    if (minutes < 60) return `${minutes} min ago`;
    const hours = Math.round(minutes / 60);
    if (hours < 48) return `${hours} h ago`;
    return `${Math.round(hours / 24)} days ago`;
  }

  function mmss(seconds: number): string {
    const m = Math.floor(seconds / 60);
    const s = seconds % 60;
    return `${m}:${String(s).padStart(2, "0")}`;
  }

  function repositoryName(r: string): string {
    return r === "offsite" ? "off-server repository" : "local repository";
  }

  async function load() {
    const { data, error } = await api.GET("/settings/health");
    if (data) {
      report = data;
      repository = data.next_drill_repository;
    } else {
      failed = true;
      message = errorMessage(error, "Health could not be loaded.");
    }
  }

  async function retryBlocked() {
    busy = true;
    const { data, error } = await api.POST("/settings/health/retry-blocked");
    busy = false;
    if (data) {
      failed = false;
      message =
        data.retried === 0
          ? "No notes were waiting on yt-dlp."
          : `Retried ${data.retried} ${data.retried === 1 ? "note" : "notes"} from the step that failed.`;
      await load();
    } else {
      failed = true;
      message = errorMessage(error, "The blocked notes could not be retried.");
    }
  }

  async function recordDrill(event: SubmitEvent) {
    event.preventDefault();
    busy = true;
    const { data, error } = await api.POST("/settings/health/drills", {
      body: {
        repository,
        snapshot_id: snapshotId.trim(),
        restore_seconds: Number(restoreSeconds),
        key_recovery_verified: keyRecovery,
        postgres_version: postgresVersion.trim(),
        pgvector_version: pgvectorVersion.trim(),
        ...(drillNotes.trim() ? { notes: drillNotes.trim() } : {}),
      },
    });
    busy = false;
    if (data) {
      failed = false;
      message = `Drill recorded: ${repositoryName(data.repository)}, restored in ${mmss(data.restore_seconds)}.`;
      snapshotId = "";
      restoreSeconds = "";
      keyRecovery = false;
      drillNotes = "";
      await load();
    } else {
      failed = true;
      message = errorMessage(error, "The drill could not be recorded.");
    }
  }

  onMount(() => void load());
</script>

<svelte:head>
  <title>Health · Visual Notes</title>
</svelte:head>

<h1>Health</h1>
{#if message}
  <p class={failed ? "error" : "ok"} role="status">{message}</p>
{/if}

<section class="card stack" aria-label="Checks">
  <h2>Checks</h2>
  {#if !report}
    <p class="muted">Loading…</p>
  {:else if report.checks.length === 0}
    <p class="muted">
      No check has been written yet. The worker writes the first ones when it
      starts.
    </p>
  {:else}
    <ul class="stack plain">
      {#each report.checks as check (check.check_name)}
        <li class="check" data-check={check.check_name}>
          <div class="row between wrap">
            <strong>{label(check)}</strong>
            <span class="badge status-{check.status}">{check.status}</span>
          </div>
          <p class="small">
            {#if check.status === "unknown"}
              No check written since {ago(check.checked_at)}; the worker may be
              stopped.
            {:else}
              {check.message ?? ""}
            {/if}
          </p>
          <p class="muted small">
            Checked {ago(check.checked_at)}
            {#if check.first_alarm_at}
              · in alarm since {new Date(check.first_alarm_at).toLocaleString()}
            {/if}
          </p>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<section class="card stack" aria-label="Blocked by tooling">
  <h2>Blocked by tooling</h2>
  {#if report}
    <p>
      {#if report.blocked_by_tooling === 0}
        No note is waiting on a yt-dlp update.
      {:else}
        {report.blocked_by_tooling}
        {report.blocked_by_tooling === 1 ? "note is" : "notes are"} blocked until
        yt-dlp is updated. After the update, retry them here.
      {/if}
    </p>
    <div class="row">
      <button
        type="button"
        disabled={busy || report.blocked_by_tooling === 0}
        onclick={retryBlocked}>Retry blocked notes</button
      >
    </div>
  {/if}
</section>

<section class="card stack" aria-label="Restore drills">
  <h2>Restore drills</h2>
  {#if report}
    <p>
      Next drill: <strong>{repositoryName(report.next_drill_repository)}</strong
      >. Key recovery is tested every time.
    </p>
    {#if report.drills.length === 0}
      <p class="muted">
        No drill has been run yet. The first one sets the restore time this
        project reports.
      </p>
    {:else}
      <ul class="stack plain">
        {#each report.drills as drill (drill.id)}
          <li class="drill">
            <strong
              >{new Date(drill.completed_at).toLocaleDateString()} · {repositoryName(
                drill.repository,
              )}</strong
            >
            <p class="small">
              Snapshot {drill.snapshot_id} restored in {mmss(
                drill.restore_seconds,
              )} · key recovery {drill.key_recovery_verified
                ? "verified"
                : "not verified"} · PostgreSQL {drill.postgres_version} · pgvector
              {drill.pgvector_version}
            </p>
            {#if drill.notes}
              <p class="muted small">{drill.notes}</p>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
    <form class="stack" aria-label="Record a drill" onsubmit={recordDrill}>
      <h3>Record a drill</h3>
      <label>
        Repository
        <select bind:value={repository}>
          <option value="local">Local</option>
          <option value="offsite">Off-server</option>
        </select>
      </label>
      <label>
        Snapshot id
        <input type="text" required maxlength="64" bind:value={snapshotId} />
      </label>
      <label>
        Restore time (seconds)
        <input type="number" required min="1" bind:value={restoreSeconds} />
      </label>
      <label class="row switch">
        <input type="checkbox" bind:checked={keyRecovery} />
        Key recovery verified with the recovered master key
      </label>
      <label>
        PostgreSQL version
        <input
          type="text"
          required
          maxlength="64"
          bind:value={postgresVersion}
        />
      </label>
      <label>
        pgvector version
        <input
          type="text"
          required
          maxlength="64"
          bind:value={pgvectorVersion}
        />
      </label>
      <label>
        Notes
        <textarea rows="3" maxlength="4000" bind:value={drillNotes}></textarea>
      </label>
      <div class="row">
        <button type="submit" class="primary" disabled={busy}
          >Record drill</button
        >
      </div>
    </form>
  {/if}
</section>

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

<style>
  h2,
  h3 {
    margin: 0;
  }
  .plain {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .check,
  .drill {
    padding-bottom: 0.6rem;
    border-bottom: 1px solid var(--line);
  }
  .check:last-child,
  .drill:last-child {
    border-bottom: 0;
    padding-bottom: 0;
  }
  .between {
    justify-content: space-between;
  }
  .wrap {
    flex-wrap: wrap;
  }
  .small {
    font-size: 0.9rem;
    margin: 0.2rem 0 0;
  }
  .badge {
    font-size: 0.8rem;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    border: 1px solid var(--line);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .status-ok {
    color: var(--accent);
  }
  .status-warn,
  .status-unknown {
    color: var(--warn);
  }
  .status-alarm {
    color: var(--danger);
  }
  .switch {
    gap: 0.5rem;
    align-items: flex-start;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .ok {
    color: var(--accent);
  }
  code {
    font-size: 0.85em;
  }
</style>

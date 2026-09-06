<script lang="ts">
  // Settings → Notifications (ADR 0014): permission on an explicit press
  // only, one subscription per browser bound to this session, and the two
  // switches, note and operational, independent of each other.
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import {
    currentSubscription,
    disablePush,
    enablePush,
    pushSupported,
    type PushSubscriptionRow,
  } from "$lib/push";
  import { session } from "$lib/session.svelte";

  let rows = $state<PushSubscriptionRow[]>([]);
  let message = $state<string | null>(null);
  let failed = $state(false);
  let busy = $state(false);
  let supported = $state(true);
  let permission = $state<string>("default");
  let browserEndpoint = $state<string | null>(null);

  const mine = $derived(
    rows.find((r) => r.endpoint === browserEndpoint) ?? null,
  );

  async function load() {
    const { data, error } = await api.GET("/push-subscriptions");
    if (data) rows = data.subscriptions;
    else {
      failed = true;
      message = errorMessage(error, "Subscriptions could not be loaded.");
    }
    supported = pushSupported();
    permission = supported ? Notification.permission : "unsupported";
    browserEndpoint = (await currentSubscription())?.endpoint ?? null;
  }

  async function enable() {
    busy = true;
    const result = await enablePush();
    busy = false;
    if (!result.ok) {
      failed = true;
      message = result.reason;
      return;
    }
    failed = false;
    message =
      "This browser will be notified when notes are ready and if backups start failing.";
    await load();
  }

  async function disable() {
    busy = true;
    await disablePush(mine);
    busy = false;
    failed = false;
    message = "Notifications are off for this browser.";
    await load();
  }

  async function toggle(
    row: PushSubscriptionRow,
    which: "notes" | "ops",
    value: boolean,
  ) {
    const { data, error } = await api.PATCH(
      "/push-subscriptions/{subscriptionId}",
      {
        params: { path: { subscriptionId: row.id } },
        body:
          which === "notes" ? { notes_enabled: value } : { ops_enabled: value },
      },
    );
    if (data) rows = rows.map((r) => (r.id === data.id ? data : r));
    else {
      failed = true;
      message = errorMessage(error, "The switch could not be saved.");
    }
  }

  async function remove(row: PushSubscriptionRow) {
    if (row.endpoint === browserEndpoint) return disable();
    const { error, response } = await api.DELETE(
      "/push-subscriptions/{subscriptionId}",
      {
        params: { path: { subscriptionId: row.id } },
      },
    );
    if (!response.ok) {
      failed = true;
      message = errorMessage(error, "The subscription could not be removed.");
      return;
    }
    await load();
  }

  function when(iso: string | null | undefined): string {
    return iso ? new Date(iso).toLocaleString() : "never";
  }

  onMount(() => void load());
</script>

<svelte:head>
  <title>Notifications · Visual Notes</title>
</svelte:head>

<h1>Notifications</h1>
{#if message}
  <p class={failed ? "error" : "ok"} role="status">{message}</p>
{/if}

<section class="card stack" aria-label="This browser">
  <h2>This browser</h2>
  {#if !supported}
    <p class="muted">
      This browser cannot receive push notifications. Install the app from
      Chrome on the tablet, or use a desktop browser.
    </p>
  {:else if mine}
    <p>
      Notifications are on for this browser{session.device
        ? ` (${session.device.display_name})`
        : ""}.
    </p>
    <div class="row">
      <button type="button" disabled={busy} onclick={disable}
        >Turn off for this browser</button
      >
    </div>
  {:else}
    <p>
      <strong>Get notified when notes are ready?</strong>
      Also warns you if backups start failing. You can turn either off later.
    </p>
    {#if permission === "denied"}
      <p class="warn">
        Notifications are blocked for this site in the browser's settings. Allow
        them there first.
      </p>
    {/if}
    <div class="row">
      <button
        type="button"
        class="primary"
        disabled={busy || permission === "denied"}
        onclick={enable}
      >
        Turn on notifications
      </button>
    </div>
  {/if}
</section>

<section class="card stack" aria-label="Subscribed browsers">
  <h2>Subscribed browsers</h2>
  {#if rows.length === 0}
    <p class="muted">No browser is subscribed yet.</p>
  {:else}
    <ul class="stack plain">
      {#each rows as row (row.id)}
        <li class="stack entry">
          <div class="row between wrap">
            <strong
              >{row.device_display_name}{row.endpoint === browserEndpoint
                ? " (this browser)"
                : ""}</strong
            >
            <button type="button" class="danger" onclick={() => remove(row)}
              >Remove</button
            >
          </div>
          <label class="row switch">
            <input
              type="checkbox"
              checked={row.notes_enabled}
              onchange={(e) => toggle(row, "notes", e.currentTarget.checked)}
            />
            <span
              >Note notifications: ready, failed, or waiting for your cost
              confirmation</span
            >
          </label>
          <label class="row switch">
            <input
              type="checkbox"
              checked={row.ops_enabled}
              onchange={(e) => toggle(row, "ops", e.currentTarget.checked)}
            />
            <span
              >Operational alerts: backups, disk, the download tool, the worker</span
            >
          </label>
          <p class="muted small">
            Last delivered {when(row.last_success_at)}
            {#if row.consecutive_failures > 0}
              · {row.consecutive_failures} recent failures{/if}
          </p>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  h2 {
    margin: 0;
  }
  .plain {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .entry {
    gap: 0.35rem;
    padding-bottom: 0.75rem;
    border-bottom: 1px solid var(--line);
  }
  .entry:last-child {
    border-bottom: 0;
    padding-bottom: 0;
  }
  .between {
    justify-content: space-between;
  }
  .wrap {
    flex-wrap: wrap;
  }
  .switch {
    gap: 0.5rem;
    align-items: flex-start;
  }
  .small {
    font-size: 0.85rem;
    margin: 0;
  }
  .ok {
    color: var(--accent);
  }
</style>

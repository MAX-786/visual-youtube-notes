<script lang="ts">
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import {
    adoptGrant,
    session,
    signOut,
    type DeviceSession,
  } from "$lib/session.svelte";

  let sessions = $state<DeviceSession[]>([]);
  let message = $state<string | null>(null);
  let currentPassword = $state("");
  let newPassword = $state("");
  let busy = $state(false);

  async function load() {
    const { data } = await api.GET("/auth/sessions");
    if (data) sessions = data.sessions;
  }

  async function revoke(id: string) {
    const { error } = await api.DELETE("/auth/sessions/{sessionId}", {
      params: { path: { sessionId: id } },
    });
    message = error ? errorMessage(error) : "Signed that device out.";
    if (sessions.find((s) => s.id === id)?.is_current) {
      await signOut({ local: true });
      return;
    }
    await load();
  }

  async function revokeAll() {
    await api.POST("/auth/sessions/revoke-all");
    await signOut({ local: true });
  }

  async function changePassword(event: SubmitEvent) {
    event.preventDefault();
    busy = true;
    const { data, error } = await api.POST("/auth/account/password", {
      body: { current_password: currentPassword, new_password: newPassword },
    });
    busy = false;
    if (!data) {
      message = errorMessage(error, "Password change failed.");
      return;
    }
    adoptGrant(data);
    currentPassword = "";
    newPassword = "";
    message = "Password changed. Every other device was signed out.";
    await load();
  }

  onMount(() => void load());

  function when(iso: string): string {
    return new Date(iso).toLocaleString();
  }
</script>

<svelte:head>
  <title>Account · Visual Notes</title>
</svelte:head>

<h1>Account</h1>

{#if session.account}
  <div class="card stack">
    <p>
      <strong>{session.account.display_name}</strong>
      <span class="muted">({session.account.username})</span>
    </p>
    <p class="muted">Timezone {session.account.timezone}</p>
  </div>
{/if}

<h2>Devices</h2>
<div class="stack">
  {#each sessions as device (device.id)}
    <div class="card row between">
      <div>
        <strong>{device.display_name}</strong>
        {#if device.is_current}<span class="badge">this device</span>{/if}
        <span class="badge">{device.kind}</span>
        <p class="muted meta">
          last seen {when(device.last_seen_at)} · expires {when(
            device.inactive_expires_at,
          )}
        </p>
      </div>
      <button class="danger" onclick={() => revoke(device.id)}>Sign out</button>
    </div>
  {/each}
  <button class="danger" onclick={revokeAll}>Sign out all devices</button>
</div>

<h2>Password</h2>
<form class="card stack" onsubmit={changePassword}>
  <label class="field">
    <span>Current password</span>
    <input
      type="password"
      autocomplete="current-password"
      bind:value={currentPassword}
      required
    />
  </label>
  <label class="field">
    <span>New password (12 characters or more)</span>
    <input
      type="password"
      autocomplete="new-password"
      minlength="12"
      bind:value={newPassword}
      required
    />
  </label>
  <button class="primary" type="submit" disabled={busy}>Change password</button>
</form>

{#if message}
  <p role="status" class="muted">{message}</p>
{/if}

<div class="stack">
  <button onclick={() => signOut()}>Sign out on this device</button>
</div>

<style>
  .between {
    justify-content: space-between;
  }
  .meta {
    margin: 0.25rem 0 0;
    font-size: 0.9rem;
  }
  h2 {
    margin-top: 1.5rem;
  }
</style>

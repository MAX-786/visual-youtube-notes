<script lang="ts">
  // The approval page the extension opens: the named browser asks to be
  // paired; the Owner approves or denies within five minutes (ADR 0003,
  // ADR 0013). The extension learns the answer by polling the exchange.
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import type { components } from "@visual-notes/api-client";

  type PairingRequest = components["schemas"]["PairingRequest"];

  let request = $state<PairingRequest | null>(null);
  let message = $state<string | null>(null);
  let busy = $state(false);

  const requestId = $derived(page.params.requestId ?? "");

  async function load() {
    const { data, error } = await api.GET("/pairing/requests/{requestId}", {
      params: { path: { requestId } },
    });
    if (data) request = data;
    else
      message = errorMessage(
        error,
        "This pairing request could not be found. It may have expired.",
      );
  }

  async function decide(action: "approve" | "deny") {
    busy = true;
    const path = { params: { path: { requestId } } };
    const { data, error } =
      action === "approve"
        ? await api.POST("/pairing/requests/{requestId}/approve", path)
        : await api.POST("/pairing/requests/{requestId}/deny", path);
    busy = false;
    if (data) {
      request = data;
      message = null;
    } else {
      message = errorMessage(error, "That could not be done.");
    }
  }

  function expiresIn(iso: string): string {
    const seconds = Math.max(
      0,
      Math.round((new Date(iso).getTime() - Date.now()) / 1000),
    );
    return seconds >= 60 ? `${Math.ceil(seconds / 60)} min` : `${seconds} s`;
  }

  onMount(() => void load());
</script>

<svelte:head>
  <title>Pair this browser · Visual Notes</title>
</svelte:head>

<h1>Pair this browser</h1>
{#if message}<p class="error" role="alert">{message}</p>{/if}

{#if request}
  <section class="card stack" aria-label="Pairing request">
    <p>
      <strong>{request.display_name}</strong> asks to save YouTube videos to your
      notes from its extension.
    </p>
    <p class="muted small">
      Extension {request.extension_id} · requested {new Date(
        request.created_at,
      ).toLocaleTimeString()}
    </p>
    {#if request.status === "pending"}
      <p class="muted">
        This request expires in {expiresIn(request.expires_at)}.
      </p>
      <div class="row">
        <button
          type="button"
          class="primary"
          disabled={busy}
          onclick={() => decide("approve")}
        >
          Approve this browser
        </button>
        <button
          type="button"
          class="danger"
          disabled={busy}
          onclick={() => decide("deny")}
        >
          Deny
        </button>
      </div>
    {:else if request.status === "approved"}
      <p class="ok" role="status">
        Approved. The extension finishes pairing on its own; you can close this
        tab. The browser appears under Settings → Account, where you can revoke
        it later.
      </p>
    {:else if request.status === "consumed"}
      <p class="ok" role="status">This browser is paired.</p>
    {:else if request.status === "denied"}
      <p role="status">Denied. The extension stays unpaired.</p>
    {:else}
      <p role="status">
        This request expired. Start pairing again from the extension.
      </p>
    {/if}
  </section>
{:else if !message}
  <p class="muted">Loading…</p>
{/if}

<style>
  .small {
    font-size: 0.85rem;
  }
  .ok {
    color: var(--accent);
  }
</style>

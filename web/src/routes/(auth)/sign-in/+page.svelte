<script lang="ts">
  import { goto } from "$app/navigation";
  import { session, signIn } from "$lib/session.svelte";

  let username = $state("");
  let password = $state("");
  let error = $state<string | null>(null);
  let busy = $state(false);

  $effect(() => {
    if (session.status === "signed-in") {
      const next = session.pendingPath ?? "/";
      session.pendingPath = null;
      void goto(next);
    }
  });

  function deviceName(): string {
    const ua = navigator.userAgent;
    if (/Android/.test(ua)) return "Android tablet";
    if (/Edg\//.test(ua)) return "Edge";
    if (/Brave/.test(ua)) return "Brave";
    if (/Chrome\//.test(ua)) return "Chrome";
    if (/Firefox\//.test(ua)) return "Firefox";
    if (/Safari\//.test(ua)) return "Safari";
    return "Browser";
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    busy = true;
    error = await signIn(username, password, deviceName());
    busy = false;
  }
</script>

<svelte:head>
  <title>Sign in · Visual Notes</title>
</svelte:head>

<main class="page sign-in">
  <form class="card stack" onsubmit={submit}>
    <h1>Visual Notes</h1>
    <p class="muted">Sign in with the Owner account created on the server.</p>
    <label class="field">
      <span>Username</span>
      <input
        type="text"
        name="username"
        autocomplete="username"
        bind:value={username}
        required
      />
    </label>
    <label class="field">
      <span>Password</span>
      <input
        type="password"
        name="password"
        autocomplete="current-password"
        bind:value={password}
        required
      />
    </label>
    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}
    <button class="primary" type="submit" disabled={busy}>
      {busy ? "Signing in…" : "Sign in"}
    </button>
  </form>
</main>

<style>
  .sign-in {
    max-width: 26rem;
    padding-top: 10vh;
  }
</style>

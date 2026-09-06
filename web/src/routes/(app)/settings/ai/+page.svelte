<script lang="ts">
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import type { components } from "@visual-notes/api-client";

  type AiSettings = components["schemas"]["AiSettings"];
  type AiRoleConfig = components["schemas"]["AiRoleConfig"];
  type AiRole = components["schemas"]["AiRole"];

  const labels: Record<AiRole, { title: string; blurb: string }> = {
    note_generation: {
      title: "Note generation",
      blurb: "Writes the note and draws its diagram from the transcript.",
    },
    embeddings: {
      title: "Embeddings",
      blurb:
        "Indexes transcripts and notes so search can find them by meaning.",
    },
    transcription: {
      title: "Transcription",
      blurb:
        "Speech to text, used only for videos without usable captions. The one paid step that can cost more than cents.",
    },
    visual_analysis: {
      title: "Visual analysis",
      blurb: "Inactive until a later version.",
    },
  };

  type Draft = {
    provider: string;
    model: string;
    endpoint: string;
    options: string;
    apiKey: string;
    busy: boolean;
    message: string | null;
    failed: boolean;
  };

  let settings = $state<AiSettings | null>(null);
  let loadError = $state<string | null>(null);
  let drafts = $state<Record<string, Draft>>({});

  function draftFrom(role: AiRoleConfig): Draft {
    return {
      provider: role.provider,
      model: role.model,
      endpoint: role.endpoint ?? "",
      options: JSON.stringify(role.request_options, null, 2),
      apiKey: "",
      busy: false,
      message: null,
      failed: false,
    };
  }

  async function load() {
    const { data, error } = await api.GET("/settings/ai");
    if (!data) {
      loadError = errorMessage(error, "AI settings could not be loaded.");
      return;
    }
    settings = data;
    for (const role of data.roles) {
      const previous = drafts[role.role];
      drafts[role.role] = {
        ...draftFrom(role),
        message: previous?.message ?? null,
        failed: previous?.failed ?? false,
      };
    }
  }

  function replaceRole(updated: AiRoleConfig) {
    if (!settings) return;
    settings.roles = settings.roles.map((r) =>
      r.role === updated.role ? updated : r,
    );
  }

  function parseOptions(text: string): Record<string, unknown> | null {
    try {
      const parsed: unknown = JSON.parse(text.trim() === "" ? "{}" : text);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      /* reported below */
    }
    return null;
  }

  function verdict(role: AiRoleConfig): string {
    if (role.verified_at) {
      return `Verified ${new Date(role.verified_at).toLocaleString()}.`;
    }
    if (role.verification_error) {
      return `Verification failed: ${role.verification_error}`;
    }
    return role.key_configured ? "Not verified yet." : "";
  }

  async function save(event: SubmitEvent, role: AiRole) {
    event.preventDefault();
    const d = drafts[role];
    const options = parseOptions(d.options);
    if (!options) {
      d.message =
        'Request options must be a JSON object, like {"reasoning_effort": "low"}.';
      d.failed = true;
      return;
    }
    d.busy = true;
    const apiKey = d.apiKey.trim();
    const { data, error } = await api.PUT("/settings/ai/{role}", {
      params: { path: { role } },
      body: {
        provider: d.provider.trim(),
        model: d.model.trim(),
        endpoint: d.endpoint.trim() === "" ? null : d.endpoint.trim(),
        request_options: options,
        ...(apiKey ? { api_key: apiKey } : {}),
      },
    });
    d.busy = false;
    if (!data) {
      d.message = errorMessage(error, "The change was not saved.");
      d.failed = true;
      return;
    }
    d.apiKey = "";
    replaceRole(data);
    d.failed = !!data.verification_error;
    d.message = data.verified_at
      ? "Saved. The key works."
      : data.verification_error
        ? `Saved, but the key did not verify: ${data.verification_error}`
        : "Saved.";
    await load();
  }

  async function verify(role: AiRole) {
    const d = drafts[role];
    d.busy = true;
    const { data, error } = await api.POST("/settings/ai/{role}/verify", {
      params: { path: { role } },
    });
    d.busy = false;
    if (!data) {
      d.message = errorMessage(error, "Verification could not run.");
      d.failed = true;
      return;
    }
    d.failed = !!data.verification_error;
    d.message = data.verified_at
      ? "The key works."
      : `The key did not verify: ${data.verification_error}`;
    await load();
  }

  async function clearKey(role: AiRole) {
    const d = drafts[role];
    d.busy = true;
    const { data, error } = await api.DELETE("/settings/ai/{role}/key", {
      params: { path: { role } },
    });
    d.busy = false;
    if (!data) {
      d.message = errorMessage(error, "The key was not removed.");
      d.failed = true;
      return;
    }
    d.message = "Key removed.";
    d.failed = false;
    await load();
  }

  function useRecommended(role: AiRole) {
    const recommended = settings?.defaults.find((d) => d.role === role);
    if (!recommended) return;
    const d = drafts[role];
    d.provider = recommended.provider;
    d.model = recommended.model;
    d.endpoint = "";
    d.options = JSON.stringify(recommended.request_options, null, 2);
    d.message = "Recommended values filled in. Save to apply them.";
    d.failed = false;
  }

  onMount(() => void load());
  // The search index: which model search reads, and the reindex that moves
  // it to another (ADR 0005). Search keeps reading the old model until the
  // flip, which is gated on every ready Note having vectors.
  type SearchIndexState = components["schemas"]["SearchIndexState"];
  let index = $state<SearchIndexState | null>(null);
  let indexMessage = $state<string | null>(null);
  let indexFailed = $state(false);
  let reindexModel = $state("");
  let indexBusy = $state(false);

  async function loadIndex() {
    const { data } = await api.GET("/settings/search-index");
    if (data) index = data;
  }

  async function indexAction(
    action: "reindex" | "activate" | "abandon",
    okText: string,
  ) {
    indexBusy = true;
    indexMessage = null;
    const { data, error, response } =
      action === "reindex"
        ? await api.POST("/settings/search-index/reindex", {
            body: { model: reindexModel.trim() },
          })
        : action === "activate"
          ? await api.POST("/settings/search-index/activate")
          : await api.DELETE("/settings/search-index/reindex");
    indexBusy = false;
    if (!data) {
      const details = (
        error as { error?: { details?: Record<string, unknown> } } | undefined
      )?.error?.details;
      const missing = details?.incomplete_notes;
      indexFailed = true;
      indexMessage =
        response.status === 409 && missing != null
          ? `Not yet: ${String(missing)} notes still lack vectors under the incoming model. Wait for the reindex to finish.`
          : errorMessage(error, "That could not be done.");
      return;
    }
    index = data;
    indexFailed = false;
    indexMessage = okText;
  }

  onMount(() => void loadIndex());
</script>

<svelte:head>
  <title>AI providers · Visual Notes</title>
</svelte:head>

<h1>AI providers and models</h1>
<p class="muted">
  Each step of processing calls one model with your own API key. Keys are stored
  encrypted and never shown again; only their last four characters are.
</p>

{#if loadError}
  <p class="error" role="alert">{loadError}</p>
{:else if !settings}
  <p class="muted">Loading…</p>
{:else}
  {#if settings.blocking_submission.length > 0}
    <div class="card warn" role="status">
      <strong>Saving videos is paused.</strong>
      Add a key for
      {settings.blocking_submission
        .map((r) => labels[r].title.toLowerCase())
        .join(" and ")}
      below; those steps are on every video's path to a finished note.
    </div>
  {/if}

  <div class="stack roles">
    {#each settings.roles.filter((r) => r.role !== "visual_analysis") as role (role.role)}
      {@const d = drafts[role.role]}
      {#if d}
        <section class="card stack">
          <div class="row between">
            <div>
              <h2>{labels[role.role].title}</h2>
              <p class="muted blurb">{labels[role.role].blurb}</p>
            </div>
            <div class="badges">
              {#if role.key_configured}
                <span class="badge ok">key ····{role.key_hint}</span>
              {:else}
                <span class="badge warn">no key</span>
              {/if}
              {#if role.verified_at}
                <span class="badge ok">verified</span>
              {:else if role.verification_error}
                <span class="badge error">not verified</span>
              {/if}
            </div>
          </div>

          {#if verdict(role)}
            <p class="muted small">{verdict(role)}</p>
          {/if}

          <form class="stack" onsubmit={(e) => save(e, role.role)}>
            <div class="row wrap">
              <label class="field grow">
                <span>Provider</span>
                <input type="text" bind:value={d.provider} required />
              </label>
              <label class="field grow">
                <span>Model</span>
                <input type="text" bind:value={d.model} required />
              </label>
            </div>
            <label class="field">
              <span>Endpoint (optional, https, includes /v1)</span>
              <input
                type="url"
                placeholder="https://api.openai.com/v1"
                bind:value={d.endpoint}
              />
            </label>
            <label class="field">
              <span>Request options (JSON)</span>
              <textarea rows="4" spellcheck="false" bind:value={d.options}
              ></textarea>
            </label>
            <label class="field">
              <span>
                API key
                {#if role.key_configured}(leave empty to keep the stored key){/if}
              </span>
              <input
                type="password"
                autocomplete="off"
                placeholder={role.key_configured ? "unchanged" : "sk-…"}
                bind:value={d.apiKey}
              />
            </label>
            <div class="row wrap">
              <button class="primary" type="submit" disabled={d.busy}>
                Save
              </button>
              {#if role.key_configured}
                <button
                  type="button"
                  disabled={d.busy}
                  onclick={() => verify(role.role)}
                >
                  Verify key
                </button>
                <button
                  type="button"
                  class="danger"
                  disabled={d.busy}
                  onclick={() => clearKey(role.role)}
                >
                  Remove key
                </button>
              {/if}
              <button
                type="button"
                disabled={d.busy}
                onclick={() => useRecommended(role.role)}
              >
                Use recommended
              </button>
            </div>
          </form>

          {#if d.message}
            <p role="status" class={d.failed ? "error" : "muted"}>
              {d.message}
            </p>
          {/if}
        </section>
      {/if}
    {/each}
  </div>
{/if}

{#if index}
  <section class="card stack" aria-label="Search index">
    <h2>Search index</h2>
    <p class="muted">
      Search reads <strong>{index.active_embedding_model}</strong>
      ({index.embedding_dimension}
      dimensions).
      {#if index.children_needing_embedding > 0}
        {index.children_needing_embedding} recently edited sections are waiting for
        vectors; the hourly sweep embeds them.
      {/if}
    </p>
    {#if index.reindexing_into_model}
      <p>
        Reindexing into <strong>{index.reindexing_into_model}</strong> since
        {new Date(index.reindex_started_at ?? "").toLocaleString()}.
        {#if index.incomplete_notes > 0}
          {index.incomplete_notes} notes still lack vectors.
        {:else}
          Every note is covered; the flip can go ahead.
        {/if}
      </p>
      <div class="row">
        <button
          type="button"
          class="primary"
          disabled={indexBusy}
          onclick={() =>
            indexAction("activate", "Search now reads the new model.")}
        >
          Activate the new model
        </button>
        <button
          type="button"
          disabled={indexBusy}
          onclick={() =>
            indexAction(
              "abandon",
              "Reindex abandoned; the incoming vectors were removed.",
            )}
        >
          Abandon
        </button>
      </div>
    {:else}
      <form
        class="row wrap"
        aria-label="Start a reindex"
        onsubmit={(e) => {
          e.preventDefault();
          void indexAction(
            "reindex",
            "Reindex started. Search keeps reading the current model until you activate the new one.",
          );
        }}
      >
        <label class="field">
          <span>Reindex into model</span>
          <input
            type="text"
            bind:value={reindexModel}
            required
            maxlength="128"
            placeholder="text-embedding-3-small"
          />
        </label>
        <button type="submit" disabled={indexBusy}>Start reindex</button>
      </form>
      <p class="muted">
        Set the embeddings role to that model first; the reindex calls with that
        role&rsquo;s key and options, and the model must produce {index.embedding_dimension}-dimensional
        vectors.
      </p>
    {/if}
    {#if indexMessage}
      <p class={indexFailed ? "error" : "ok"} role="status">{indexMessage}</p>
    {/if}
  </section>
{/if}

<style>
  .between {
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
  }
  .wrap {
    flex-wrap: wrap;
  }
  .grow {
    flex: 1 1 12rem;
  }
  .badges {
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  .blurb {
    margin: 0.25rem 0 0;
  }
  .small {
    font-size: 0.9rem;
  }
  h2 {
    margin: 0;
  }
  textarea {
    font-family: ui-monospace, monospace;
    width: 100%;
    box-sizing: border-box;
  }
</style>

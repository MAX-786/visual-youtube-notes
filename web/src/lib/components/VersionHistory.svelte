<script lang="ts">
  // Version history is a tree of Revisions. Consecutive manual Revisions
  // from one Device session collapse into one expandable entry (ADR 0004).
  // Any past Revision can be restored; restoring copies, never rewinds.
  import { SvelteSet } from "svelte/reactivity";
  import type { components } from "@visual-notes/api-client";
  import { api, errorMessage } from "$lib/api";

  type RevisionSummary = components["schemas"]["RevisionSummary"];

  let { noteId, onchange }: { noteId: string; onchange: () => void } = $props();

  let revisions = $state<RevisionSummary[] | null>(null);
  let message = $state<string | null>(null);
  const expanded = new SvelteSet<number>();

  type Group = { key: number; head: RevisionSummary; rest: RevisionSummary[] };

  const groups = $derived.by((): Group[] => {
    const out: Group[] = [];
    for (const r of revisions ?? []) {
      const last = out[out.length - 1];
      if (
        last &&
        r.origin === "manual" &&
        last.head.origin === "manual" &&
        r.origin_device_session_id &&
        r.origin_device_session_id === last.head.origin_device_session_id &&
        !r.is_active &&
        !last.head.is_active
      ) {
        last.rest.push(r);
      } else {
        out.push({ key: r.revision_number, head: r, rest: [] });
      }
    }
    return out;
  });

  export async function load() {
    const { data, error } = await api.GET("/notes/{noteId}/revisions", {
      params: { path: { noteId } },
    });
    if (data) revisions = data.revisions;
    else message = errorMessage(error, "History could not be loaded.");
  }

  async function restore(revision: RevisionSummary) {
    if (
      !confirm(
        `Restore revision ${revision.revision_number}? A copy becomes the current version.`,
      )
    )
      return;
    const { error } = await api.POST(
      "/notes/{noteId}/revisions/{revisionId}/restore",
      {
        params: { path: { noteId, revisionId: revision.id } },
      },
    );
    if (error) {
      message = errorMessage(error, "The version could not be restored.");
      return;
    }
    await load();
    onchange();
  }

  function toggle(key: number) {
    if (expanded.has(key)) expanded.delete(key);
    else expanded.add(key);
  }

  function when(iso: string) {
    return new Date(iso).toLocaleString();
  }
</script>

<details
  class="card"
  ontoggle={(e) =>
    (e.currentTarget as HTMLDetailsElement).open && !revisions && void load()}
>
  <summary>Version history</summary>
  {#if !revisions}
    <p class="muted">Loading…</p>
  {:else}
    <ul class="history">
      {#each groups as group (group.key)}
        <li>
          <div class="row wrap">
            <span>
              <strong>Revision {group.head.revision_number}</strong>
              ({group.head.origin}{group.rest.length > 0
                ? ` · ${group.rest.length + 1} edits`
                : ""})
              {when(group.head.created_at)}
              {#if group.head.is_active}<span class="badge ready">current</span
                >{/if}
              {#if group.head.awaiting_decision_since}<span
                  class="badge processing">awaiting a decision</span
                >{/if}
            </span>
            {#if !group.head.is_active}
              <button type="button" onclick={() => restore(group.head)}
                >Restore</button
              >
            {/if}
            {#if group.rest.length > 0}
              <button type="button" onclick={() => toggle(group.key)}>
                {expanded.has(group.key) ? "Hide edits" : "Show edits"}
              </button>
            {/if}
          </div>
          {#if expanded.has(group.key)}
            <ul>
              {#each group.rest as r (r.id)}
                <li class="row wrap">
                  <span>Revision {r.revision_number} {when(r.created_at)}</span>
                  <button type="button" onclick={() => restore(r)}
                    >Restore</button
                  >
                </li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  {#if message}<p class="error">{message}</p>{/if}
</details>

<style>
  summary {
    cursor: pointer;
    font-weight: 600;
  }
  .history {
    margin: 0.5rem 0 0;
    padding-left: 1rem;
  }
  .history li + li {
    margin-top: 0.4rem;
  }
  .wrap {
    flex-wrap: wrap;
  }
  button {
    min-height: 36px;
    padding: 0.2rem 0.6rem;
  }
</style>

<script lang="ts">
  // Settings → Usage: the month as the ledger has it, beside everything the
  // total cannot account for; the Notes held for cost confirmation; the
  // Soft cap and the semantic override; and the price sets, appended, never
  // edited (ADR 0007).
  import { onMount } from "svelte";
  import { api, errorMessage } from "$lib/api";
  import type { components } from "@visual-notes/api-client";

  type MonthlyUsage = components["schemas"]["MonthlyUsage"];
  type CostSettings = components["schemas"]["CostSettings"];
  type PriceSet = components["schemas"]["PriceSet"];
  type NoteCard = components["schemas"]["NoteCard"];

  let usage = $state<MonthlyUsage | null>(null);
  let settings = $state<CostSettings | null>(null);
  let priceSets = $state<PriceSet[]>([]);
  let held = $state<NoteCard[]>([]);
  let message = $state<string | null>(null);
  let failed = $state(false);
  let capDraft = $state("");
  let busy = $state<string | null>(null);

  let priceDraft = $state({
    provider: "openai",
    model: "",
    billing_mode: "sync" as components["schemas"]["BillingMode"],
    effective_from: "",
    unit_prices: "",
    source_note: "",
  });

  async function load() {
    const [u, c, p, h] = await Promise.all([
      api.GET("/settings/usage"),
      api.GET("/settings/cost"),
      api.GET("/settings/prices"),
      api.GET("/notes", { params: { query: { held: true } } }),
    ]);
    if (u.data) usage = u.data;
    if (c.data) {
      settings = c.data;
      capDraft = c.data.soft_cap_usd;
    }
    if (p.data) priceSets = p.data.price_sets;
    if (h.data) held = h.data.notes;
    if (!u.data) {
      failed = true;
      message = errorMessage(u.error, "Usage could not be loaded.");
    }
  }

  onMount(() => void load());

  function say(text: string, isFailure = false) {
    message = text;
    failed = isFailure;
  }

  async function confirm(note: NoteCard) {
    busy = note.id;
    const { data, error } = await api.POST(
      "/notes/{noteId}/cost-confirmation",
      {
        params: { path: { noteId: note.id } },
        body: { processing_version: note.processing.version },
      },
    );
    busy = null;
    if (error) {
      say(errorMessage(error, "The run could not be resumed."), true);
      return;
    }
    say(`Resumed. This run is expected to cost about $${data.estimate_usd}.`);
    await load();
  }

  async function saveCap(event: SubmitEvent) {
    event.preventDefault();
    busy = "cap";
    const { data, error } = await api.PUT("/settings/cost", {
      body: { soft_cap_usd: normalizeMoney(capDraft) },
    });
    busy = null;
    if (error) {
      say(errorMessage(error, "The cap could not be saved."), true);
      return;
    }
    settings = data;
    capDraft = data.soft_cap_usd;
    say(
      "Cap saved. It applies at the next gate; notes already held stay held.",
    );
    await load();
  }

  async function enableSemantic() {
    busy = "semantic";
    const { data, error } = await api.POST("/settings/cost/semantic-override");
    busy = null;
    if (error) {
      say(
        errorMessage(error, "Semantic search could not be re-enabled."),
        true,
      );
      return;
    }
    settings = data;
    say("Semantic search is back on for this month.");
    await load();
  }

  function normalizeMoney(raw: string): string {
    const n = Number(raw);
    if (!Number.isFinite(n) || n < 0) return raw;
    return n.toFixed(2);
  }

  function parseUnitPrices(raw: string): Record<string, number> | null {
    const out: Record<string, number> = {};
    for (const line of raw.split(/\n|,/)) {
      const t = line.trim();
      if (!t) continue;
      const [unit, price] = t.split("=").map((s) => s.trim());
      const n = Number(price);
      if (!unit || !Number.isFinite(n) || n < 0) return null;
      out[unit] = n;
    }
    return Object.keys(out).length ? out : null;
  }

  async function addPriceSet(event: SubmitEvent) {
    event.preventDefault();
    const unit_prices = parseUnitPrices(priceDraft.unit_prices);
    if (!unit_prices) {
      say(
        "Unit prices are lines of unit=price, such as input_tokens=0.0000025.",
        true,
      );
      return;
    }
    busy = "price";
    const { error } = await api.POST("/settings/prices", {
      body: {
        provider: priceDraft.provider.trim(),
        model: priceDraft.model.trim(),
        billing_mode: priceDraft.billing_mode,
        ...(priceDraft.effective_from
          ? {
              effective_from: new Date(priceDraft.effective_from).toISOString(),
            }
          : {}),
        unit_prices,
        ...(priceDraft.source_note.trim()
          ? { source_note: priceDraft.source_note.trim() }
          : {}),
      },
    });
    busy = null;
    if (error) {
      say(errorMessage(error, "The price set could not be added."), true);
      return;
    }
    priceDraft = {
      ...priceDraft,
      model: "",
      unit_prices: "",
      source_note: "",
      effective_from: "",
    };
    say("Price set added. Charges already recorded keep their snapshot.");
    await load();
  }

  function when(iso: string | null | undefined): string {
    return iso ? new Date(iso).toLocaleString() : "";
  }
  function units(map: Record<string, number>): string {
    return Object.entries(map)
      .map(([k, v]) => `${Math.round(v).toLocaleString()} ${k}`)
      .join(", ");
  }
  const incomplete = $derived(
    usage
      ? usage.unknown_calls + usage.unpriced_calls + usage.in_flight_calls > 0
      : false,
  );
</script>

<svelte:head>
  <title>Usage · Visual Notes</title>
</svelte:head>

<h1>Usage and the monthly cap</h1>
{#if message}
  <p class={failed ? "error" : "ok"} role="status">{message}</p>
{/if}

{#if usage}
  <section class="card stack" aria-label="This month">
    <h2>{usage.month}</h2>
    <p class="big">
      {incomplete ? "At least " : ""}${usage.charged_usd}
      <span class="muted">of a ${usage.soft_cap_usd} cap</span>
    </p>
    <p class="muted">
      {usage.billed_calls} billed calls
      {#if usage.unknown_calls > 0}
        · {usage.unknown_calls} unknown (a worker crashed mid-call){/if}
      {#if usage.unpriced_calls > 0}
        · {usage.unpriced_calls} with unpriced units{/if}
      {#if usage.in_flight_calls > 0}
        · {usage.in_flight_calls} in flight{/if}
    </p>
    {#if usage.cap_reached}
      <p class="warn">
        The cap is reached. New runs stop after captions and wait for your
        confirmation.
        {#if usage.semantic_paused}
          Semantic search is paused; results are lexical until you re-enable it.
          <button
            type="button"
            disabled={busy === "semantic"}
            onclick={enableSemantic}
          >
            Re-enable semantic search this month
          </button>
        {:else}
          Semantic search stays on this month.
        {/if}
      </p>
    {/if}
  </section>

  {#if held.length > 0}
    <section class="card stack" aria-label="Held notes">
      <h2>Waiting for your confirmation</h2>
      <ul class="plain stack">
        {#each held as note (note.id)}
          <li class="row between wrap">
            <span>
              <a href={`/notes/${note.id}`}
                >{note.source.title ?? note.source.video_id}</a
              >
              <span class="muted">
                · about ${note.processing.cost_hold?.estimate_usd} · since {when(
                  note.processing.cost_hold?.since,
                )}
              </span>
            </span>
            <button
              type="button"
              class="primary"
              disabled={busy === note.id}
              onclick={() => confirm(note)}
            >
              Confirm and continue
            </button>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <section class="card stack" aria-label="By model">
    <h2>By model</h2>
    {#if usage.by_model.length === 0}
      <p class="muted">No billed calls yet this month.</p>
    {:else}
      <div class="scroll">
        <table>
          <thead>
            <tr
              ><th>Role</th><th>Model</th><th>Calls</th><th>Units</th><th
                >Charge</th
              ></tr
            >
          </thead>
          <tbody>
            {#each usage.by_model as line (line.role + line.provider + line.model + line.billing_mode)}
              <tr>
                <td>{line.role}</td>
                <td
                  >{line.provider} / {line.model}{line.billing_mode !== "sync"
                    ? ` (${line.billing_mode})`
                    : ""}</td
                >
                <td>{line.calls}</td>
                <td>
                  {units(line.reported_units)}
                  {#if Object.keys(line.unpriced_units).length > 0}
                    <span class="badge failed"
                      >unpriced: {units(line.unpriced_units)}</span
                    >
                  {/if}
                </td>
                <td>${line.charge_usd}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    {#if usage.unpriced_models.length > 0}
      <p class="warn">
        Add a price for:
        {usage.unpriced_models
          .map((m) => `${m.provider} / ${m.model}`)
          .join(", ")}. Until then their calls count as $0.
      </p>
    {/if}
  </section>
{:else if !failed}
  <p class="muted">Loading…</p>
{/if}

{#if settings}
  <form class="card stack" aria-label="Soft cap" onsubmit={saveCap}>
    <h2>Monthly cap</h2>
    <label class="field">
      <span>Soft cap, USD per month</span>
      <input type="number" min="0" step="0.01" bind:value={capDraft} required />
    </label>
    <p class="muted">
      Applies at the next gate. Notes already held stay held until you confirm
      them.
      {#if settings.semantic_override_month}
        Semantic search is overridden for {settings.semantic_override_month}.
      {/if}
    </p>
    <div class="row">
      <button type="submit" class="primary" disabled={busy === "cap"}
        >Save cap</button
      >
    </div>
  </form>
{/if}

<section class="card stack" aria-label="Price sets">
  <h2>Price sets</h2>
  <p class="muted">
    A charge is snapshotted against the price in force when the call was made. A
    correction is a new version with a later start; nothing recorded ever
    changes.
  </p>
  {#if priceSets.length > 0}
    <div class="scroll">
      <table>
        <thead>
          <tr
            ><th>Model</th><th>Effective from</th><th>Unit prices</th><th
              >Source</th
            ></tr
          >
        </thead>
        <tbody>
          {#each priceSets as set (set.id)}
            <tr>
              <td
                >{set.provider} / {set.model}{set.billing_mode !== "sync"
                  ? ` (${set.billing_mode})`
                  : ""}</td
              >
              <td>{when(set.effective_from)}</td>
              <td>
                {Object.entries(set.unit_prices)
                  .map(([k, v]) => `${k}=${v}`)
                  .join(", ")}
              </td>
              <td class="muted">{set.source_note ?? ""}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
  <details>
    <summary>Add a price set</summary>
    <form class="stack" aria-label="New price set" onsubmit={addPriceSet}>
      <div class="row wrap">
        <label class="field">
          <span>Provider</span>
          <input
            type="text"
            bind:value={priceDraft.provider}
            required
            maxlength="64"
          />
        </label>
        <label class="field">
          <span>Model</span>
          <input
            type="text"
            bind:value={priceDraft.model}
            required
            maxlength="128"
          />
        </label>
        <label class="field">
          <span>Billing mode</span>
          <select bind:value={priceDraft.billing_mode}>
            <option value="sync">sync</option>
            <option value="batch">batch</option>
          </select>
        </label>
        <label class="field">
          <span>Effective from (blank for now)</span>
          <input type="datetime-local" bind:value={priceDraft.effective_from} />
        </label>
      </div>
      <label class="field">
        <span>Unit prices, one unit=price per line, USD per unit</span>
        <textarea
          rows="3"
          bind:value={priceDraft.unit_prices}
          required
          placeholder="input_tokens=0.0000025"></textarea>
      </label>
      <label class="field">
        <span>Source note</span>
        <input
          type="text"
          bind:value={priceDraft.source_note}
          maxlength="1000"
          placeholder="Where the price came from and when"
        />
      </label>
      <div class="row">
        <button type="submit" class="primary" disabled={busy === "price"}
          >Add price set</button
        >
      </div>
    </form>
  </details>
</section>

<style>
  h2 {
    margin: 0;
  }
  .big {
    font-size: 1.6rem;
    margin: 0;
  }
  .plain {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .between {
    justify-content: space-between;
  }
  .wrap {
    flex-wrap: wrap;
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: 0.35rem 0.5rem;
    border-bottom: 1px solid var(--line);
    vertical-align: top;
  }
  .ok {
    color: var(--accent);
  }
  summary {
    cursor: pointer;
    font-weight: 600;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
</style>

import { expect, test, type Page } from "@playwright/test";
import { account, device } from "./fixtures/note";

// Settings → Usage shows the month beside what the total cannot account
// for, lists the Notes held for cost confirmation and resumes one, changes
// the cap, and appends a price set (ADR 0007).

type Json = Record<string, unknown>;
type Call = { method: string; path: string; body: Json | null };

const heldId = "6b6b6b6b-6b6b-4b6b-8b6b-6b6b6b6b6b6b";

function heldCard() {
  return {
    id: heldId,
    source: {
      video_id: "aircAruvnKk",
      canonical_url: "https://www.youtube.com/watch?v=aircAruvnKk",
      title: "Two hours of Hindi lectures",
      channel_id: "UCx",
      channel_name: "Lecture Hall",
      duration_seconds: 7200,
      published_at: null,
      metadata_fetched_at: "2026-09-07T01:00:00Z",
    },
    saved_at: "2026-09-07T01:00:00Z",
    processing: {
      version: 1,
      state: "captions",
      failure_stage: null,
      failure_kind: null,
      cancel_requested: false,
      cost_hold: { since: "2026-09-07T01:02:00Z", estimate_usd: "0.75" },
      stages: [],
    },
    primary_topic: null,
    tags: [],
    summary_text: null,
    detected_language: null,
    transcript_source: null,
    active_revision_id: null,
    awaiting_decisions: 0,
    trashed_at: null,
    purge_after: null,
    created_at: "2026-09-07T01:00:00Z",
    updated_at: "2026-09-07T01:02:00Z",
  };
}

async function mockUsage(page: Page): Promise<Call[]> {
  const calls: Call[] = [];
  let held: Json[] = [heldCard()];
  let cap = "10.00";
  const priceSets: Json[] = [
    {
      id: "7c7c7c7c-7c7c-4c7c-8c7c-7c7c7c7c7c7c",
      provider: "openai",
      model: "gpt-transcribe",
      billing_mode: "sync",
      effective_from: "2026-09-06T00:00:00Z",
      unit_prices: { audio_seconds: 0.000075 },
      source_note: "model page, 2026-09-06",
      created_at: "2026-09-06T00:00:00Z",
    },
  ];
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const p = url.pathname;
    const method = request.method();
    const body = method === "GET" ? null : (request.postDataJSON() as Json);
    if (method !== "GET") calls.push({ method, path: p, body });
    if (p === "/api/auth/session")
      return route.fulfill(
        json({
          session: device,
          account,
          access_expires_at: "2026-09-06T10:15:00Z",
          sync_cursor: 0,
        }),
      );
    if (p === "/api/events/since")
      return route.fulfill(
        json({ events: [], next_cursor: 0, resync_required: false }),
      );
    if (p === "/api/events") return route.abort();
    if (p === "/api/settings/usage")
      return route.fulfill(
        json({
          month: "2026-09",
          charged_usd: "10.04",
          billed_calls: 41,
          unknown_calls: 1,
          unpriced_calls: 0,
          in_flight_calls: 0,
          soft_cap_usd: cap,
          cap_reached: Number(cap) <= 10.04,
          semantic_paused: Number(cap) <= 10.04,
          held_notes: held.length,
          by_model: [
            {
              role: "note_generation",
              provider: "openai",
              model: "gpt-5.6-terra",
              billing_mode: "sync",
              calls: 20,
              charge_usd: "8.10",
              reported_units: { input_tokens: 98000, output_tokens: 40000 },
              unpriced_units: {},
            },
            {
              role: "transcription",
              provider: "openai",
              model: "gpt-transcribe",
              billing_mode: "sync",
              calls: 21,
              charge_usd: "1.94",
              reported_units: { audio_seconds: 25866 },
              unpriced_units: {},
            },
          ],
          unpriced_models: [],
        }),
      );
    if (p === "/api/settings/cost" && method === "GET")
      return route.fulfill(
        json({
          soft_cap_usd: cap,
          semantic_override_month: null,
          updated_at: "2026-09-06T00:00:00Z",
        }),
      );
    if (p === "/api/settings/cost" && method === "PUT") {
      cap = String(body?.soft_cap_usd);
      return route.fulfill(
        json({
          soft_cap_usd: cap,
          semantic_override_month: null,
          updated_at: "2026-09-07T00:00:00Z",
        }),
      );
    }
    if (p === "/api/settings/cost/semantic-override")
      return route.fulfill(
        json({
          soft_cap_usd: cap,
          semantic_override_month: "2026-09",
          updated_at: "2026-09-07T00:00:00Z",
        }),
      );
    if (p === "/api/settings/prices" && method === "GET")
      return route.fulfill(json({ price_sets: priceSets }));
    if (p === "/api/settings/prices" && method === "POST") {
      const created = {
        id: "8d8d8d8d-8d8d-4d8d-8d8d-8d8d8d8d8d8d",
        billing_mode: "sync",
        effective_from: "2026-09-07T00:00:00Z",
        source_note: null,
        created_at: "2026-09-07T00:00:00Z",
        ...body,
      };
      priceSets.push(created);
      return route.fulfill(json(created, 201));
    }
    if (p === "/api/notes" && url.searchParams.get("held") === "true")
      return route.fulfill(json({ notes: held, next_cursor: null }));
    if (p === `/api/notes/${heldId}/cost-confirmation`) {
      held = [];
      return route.fulfill(
        json({
          processing_version: 1,
          estimate_usd: "0.75",
          note: {
            ...heldCard(),
            processing: {
              ...heldCard().processing,
              cost_hold: null,
              state: "transcribing",
            },
          },
        }),
      );
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return calls;
}

test("the month, the held note and the breakdown are shown, and confirmation resumes the run", async ({
  page,
}) => {
  const calls = await mockUsage(page);
  await page.goto("/settings/usage");
  await expect(page.getByText("At least $10.04")).toBeVisible();
  await expect(page.getByText("The cap is reached.")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Re-enable semantic search this month" }),
  ).toBeVisible();
  const held = page.getByRole("region", { name: "Held notes" });
  await expect(held).toContainText("Two hours of Hindi lectures");
  await expect(held).toContainText("about $0.75");
  await expect(page.locator("table").first().locator("tbody tr")).toHaveCount(
    2,
  );
  await held.getByRole("button", { name: "Confirm and continue" }).click();
  await expect(page.getByRole("status")).toContainText("about $0.75");
  const confirmation = calls.find((c) => c.path.endsWith("/cost-confirmation"));
  expect(confirmation?.body).toEqual({ processing_version: 1 });
  await expect(held).toBeHidden();
});

test("the cap is saved and a price set is appended", async ({ page }) => {
  const calls = await mockUsage(page);
  await page.goto("/settings/usage");
  const capForm = page.getByRole("form", { name: "Soft cap" });
  await capForm.getByLabel("Soft cap, USD per month").fill("25");
  await capForm.getByRole("button", { name: "Save cap" }).click();
  await expect(page.getByRole("status")).toContainText("Cap saved");
  expect(calls.find((c) => c.path === "/api/settings/cost")?.body).toEqual({
    soft_cap_usd: "25.00",
  });
  await expect(page.getByText("of a $25.00 cap")).toBeVisible();

  await page.getByText("Add a price set").click();
  const form = page.getByRole("form", { name: "New price set" });
  await form.getByLabel("Model").fill("gpt-5.6-terra");
  await form
    .getByLabel("Unit prices, one unit=price per line, USD per unit")
    .fill("input_tokens=0.0000025\noutput_tokens=0.00001");
  await form.getByLabel("Source note").fill("pricing page, 2026-09-07");
  await form.getByRole("button", { name: "Add price set" }).click();
  await expect(page.getByRole("status")).toContainText("Price set added");
  const posted = calls.find((c) => c.path === "/api/settings/prices");
  expect(posted?.body).toEqual({
    provider: "openai",
    model: "gpt-5.6-terra",
    billing_mode: "sync",
    unit_prices: { input_tokens: 0.0000025, output_tokens: 0.00001 },
    source_note: "pricing page, 2026-09-07",
  });
  await expect(page.locator("table").last().locator("tbody tr")).toHaveCount(2);
});

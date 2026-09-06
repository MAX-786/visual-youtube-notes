import { expect, test, type Page } from "@playwright/test";

// Settings → AI against a mocked API: a key entered is read back as a hint
// and its verification verdict, a failed verification warns without
// blocking the save, an explicit verify can succeed later, and removing the
// key leaves the role unconfigured.

const account = {
  id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  username: "owner",
  display_name: "The Owner",
  timezone: "Asia/Calcutta",
  created_at: "2026-09-06T10:00:00Z",
  password_changed_at: "2026-09-06T10:00:00Z",
};
const device = {
  id: "0d57fed9-8642-4f48-b1fb-8fb65147cca0",
  kind: "pwa",
  display_name: "Chrome",
  extension_id: null,
  created_at: "2026-09-06T10:00:00Z",
  last_seen_at: "2026-09-06T10:00:00Z",
  last_refreshed_at: "2026-09-06T10:00:00Z",
  inactive_expires_at: "2026-10-06T10:00:00Z",
  is_current: true,
  push_subscriptions: 0,
};

type RoleState = {
  role: string;
  provider: string;
  model: string;
  endpoint: string | null;
  request_options: Record<string, unknown>;
  key_configured: boolean;
  key_hint: string | null;
  verified_at: string | null;
  verification_error: string | null;
  updated_at: string;
};

function role(
  name: string,
  model: string,
  options: Record<string, unknown>,
): RoleState {
  return {
    role: name,
    provider: "openai",
    model,
    endpoint: null,
    request_options: options,
    key_configured: false,
    key_hint: null,
    verified_at: null,
    verification_error: null,
    updated_at: "2026-09-06T10:00:00Z",
  };
}

async function mockApi(page: Page) {
  const roles: Record<string, RoleState> = {
    note_generation: role("note_generation", "gpt-5.6-terra", {
      reasoning_effort: "low",
      max_output_tokens: 16000,
    }),
    embeddings: role("embeddings", "text-embedding-3-large", {
      dimensions: 1024,
    }),
    transcription: role("transcription", "gpt-transcribe", {
      languages: ["en", "hi"],
    }),
  };
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  const settings = () => ({
    roles: Object.values(roles),
    blocking_submission: ["note_generation", "embeddings"].filter(
      (r) => !roles[r].key_configured,
    ),
    defaults: Object.values(roles).map((r) => ({
      role: r.role,
      provider: r.provider,
      model: r.model,
      request_options: r.request_options,
      source_note: null,
    })),
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const method = route.request().method();
    if (path === "/api/auth/session") {
      return route.fulfill(
        json({
          session: device,
          account,
          access_expires_at: "2026-09-06T10:15:00Z",
          sync_cursor: 0,
        }),
      );
    }
    if (path === "/api/settings/ai" && method === "GET") {
      return route.fulfill(json(settings()));
    }
    const put = path.match(/^\/api\/settings\/ai\/([a-z_]+)$/);
    if (put && method === "PUT") {
      const r = roles[put[1]];
      const body = route.request().postDataJSON() as {
        model: string;
        api_key?: string;
        request_options: Record<string, unknown>;
      };
      r.model = body.model;
      r.request_options = body.request_options;
      if (body.api_key) {
        r.key_configured = true;
        r.key_hint = body.api_key.slice(-4);
        r.verified_at = null;
        r.verification_error = "provider HTTP 401: Incorrect API key provided";
      }
      return route.fulfill(json(r));
    }
    const verify = path.match(/^\/api\/settings\/ai\/([a-z_]+)\/verify$/);
    if (verify && method === "POST") {
      const r = roles[verify[1]];
      r.verified_at = "2026-09-07T01:00:00Z";
      r.verification_error = null;
      return route.fulfill(
        json({
          role: r.role,
          verified_at: r.verified_at,
          verification_error: null,
          charged: false,
        }),
      );
    }
    const clear = path.match(/^\/api\/settings\/ai\/([a-z_]+)\/key$/);
    if (clear && method === "DELETE") {
      const r = roles[clear[1]];
      r.key_configured = false;
      r.key_hint = null;
      r.verified_at = null;
      r.verification_error = null;
      return route.fulfill(json(r));
    }
    if (path === "/api/events/since") {
      return route.fulfill(
        json({ events: [], next_cursor: 0, resync_required: false }),
      );
    }
    if (path === "/api/events") return route.abort();
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
}

test("a key is saved as a hint, verified on demand, and removable", async ({
  page,
}) => {
  await mockApi(page);
  await page.goto("/settings/ai");
  await expect(
    page.getByRole("heading", { name: "AI providers and models" }),
  ).toBeVisible();
  await expect(page.getByText("Saving videos is paused.")).toBeVisible();

  const generation = page
    .locator("section")
    .filter({ has: page.getByRole("heading", { name: "Note generation" }) });
  await expect(generation.getByText("no key")).toBeVisible();
  await generation.getByLabel(/API key/).fill("sk-proj-notarealkey-wxyz");
  await generation.getByRole("button", { name: "Save" }).click();
  await expect(generation.getByText("key ····wxyz")).toBeVisible();
  await expect(
    generation.getByText(
      /Saved, but the key did not verify: provider HTTP 401/,
    ),
  ).toBeVisible();
  await expect(generation.getByLabel(/API key/)).toHaveValue("");

  await generation.getByRole("button", { name: "Verify key" }).click();
  await expect(generation.getByText("The key works.")).toBeVisible();
  await expect(generation.getByText("verified", { exact: true })).toBeVisible();

  await generation.getByRole("button", { name: "Remove key" }).click();
  await expect(generation.getByText("Key removed.")).toBeVisible();
  await expect(generation.getByText("no key")).toBeVisible();
  await expect(page.getByText("Saving videos is paused.")).toBeVisible();
});

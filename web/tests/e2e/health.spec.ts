import { expect, test, type Page } from "@playwright/test";
import { account, device } from "./fixtures/note";

// Settings → Health (ADR 0009): checks with unknown computed by the server,
// the blocked-by-tooling retry, and the drill log with its append-only form.

type Json = Record<string, unknown>;
type Call = { method: string; path: string; body: Json | null };

function check(name: string, status: string, message: string, minutesAgo = 5) {
  return {
    check_name: name,
    status,
    message,
    observed: {},
    cadence_seconds: 900,
    checked_at: new Date(Date.now() - minutesAgo * 60_000).toISOString(),
    last_ok_at: null,
    first_alarm_at: status === "alarm" ? "2026-09-07T05:00:00Z" : null,
    last_notified_at: null,
  };
}

async function mockHealth(page: Page): Promise<Call[]> {
  const calls: Call[] = [];
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  let blocked = 2;
  const drills: Json[] = [
    {
      id: "6d6d6d6d-6d6d-4d6d-8d6d-6d6d6d6d6d6d",
      completed_at: "2026-06-01T10:00:00Z",
      repository: "local",
      snapshot_id: "ab12cd34",
      restore_seconds: 754,
      key_recovery_verified: true,
      postgres_version: "17.6",
      pgvector_version: "0.8.0",
      notes: null,
    },
  ];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const p = new URL(request.url()).pathname;
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
    if (p === "/api/settings/health" && method === "GET")
      return route.fulfill(
        json({
          checks: [
            check(
              "backup_local",
              "ok",
              "The newest complete local set is 3h old.",
            ),
            check(
              "disk_free",
              "unknown",
              "42.0 GiB free on /var/lib/vyn/tmp (61%).",
              50,
            ),
            check(
              "worker_stale",
              "alarm",
              "The worker wrote its last health check 50m ago.",
            ),
          ],
          drills,
          next_drill_repository:
            drills[0].repository === "local" ? "offsite" : "local",
          blocked_by_tooling: blocked,
          generated_at: new Date().toISOString(),
        }),
      );
    if (p === "/api/settings/health/retry-blocked") {
      const retried = blocked;
      blocked = 0;
      return route.fulfill(json({ retried, note_ids: [] }));
    }
    if (p === "/api/settings/health/drills") {
      const drill = {
        id: "7e7e7e7e-7e7e-4e7e-8e7e-7e7e7e7e7e7e",
        completed_at: new Date().toISOString(),
        ...body,
        notes: body?.notes ?? null,
      };
      drills.unshift(drill);
      return route.fulfill(json(drill, 201));
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return calls;
}

test("checks, the blocked retry and the drill log", async ({ page }) => {
  const calls = await mockHealth(page);
  await page.goto("/settings/health");
  const checks = page.getByRole("region", { name: "Checks" });
  await expect(checks.locator("[data-check=backup_local]")).toContainText("ok");
  await expect(checks.locator("[data-check=disk_free]")).toContainText(
    "unknown",
  );
  await expect(checks.locator("[data-check=disk_free]")).toContainText(
    "the worker may be stopped",
  );
  await expect(checks.locator("[data-check=worker_stale]")).toContainText(
    "alarm",
  );
  await expect(checks.locator("[data-check=worker_stale]")).toContainText(
    "in alarm since",
  );

  const blocked = page.getByRole("region", { name: "Blocked by tooling" });
  await expect(blocked).toContainText("2 notes are blocked");
  await blocked.getByRole("button", { name: "Retry blocked notes" }).click();
  await expect(page.getByRole("status")).toContainText("Retried 2 notes");
  expect(
    calls.some(
      (c) =>
        c.method === "POST" && c.path === "/api/settings/health/retry-blocked",
    ),
  ).toBe(true);
  await expect(blocked).toContainText("No note is waiting");

  const drills = page.getByRole("region", { name: "Restore drills" });
  await expect(drills).toContainText("Next drill: off-server repository");
  await expect(drills).toContainText("Snapshot ab12cd34 restored in 12:34");
  const form = drills.getByRole("form", { name: "Record a drill" });
  await expect(form.getByLabel("Repository")).toHaveValue("offsite");
  await form.getByLabel("Snapshot id").fill("ef56ab78");
  await form.getByLabel("Restore time (seconds)").fill("901");
  await form.getByLabel(/Key recovery verified/).check();
  await form.getByLabel("PostgreSQL version").fill("17.6");
  await form.getByLabel("pgvector version").fill("0.8.0");
  await form.getByLabel("Notes").fill("The laptop needed waking.");
  await form.getByRole("button", { name: "Record drill" }).click();
  await expect(page.getByRole("status")).toContainText(
    "Drill recorded: off-server repository, restored in 15:01",
  );
  const recorded = calls.find((c) => c.path === "/api/settings/health/drills");
  expect(recorded?.body).toEqual({
    repository: "offsite",
    snapshot_id: "ef56ab78",
    restore_seconds: 901,
    key_recovery_verified: true,
    postgres_version: "17.6",
    pgvector_version: "0.8.0",
    notes: "The laptop needed waking.",
  });
  await expect(drills).toContainText("Next drill: local repository");
  await expect(drills).toContainText("Snapshot ef56ab78 restored in 15:01");
});

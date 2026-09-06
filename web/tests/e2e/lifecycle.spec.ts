import { expect, test, type Page } from "@playwright/test";
import {
  account,
  device,
  example,
  grid,
  noteFor,
  noteId,
} from "./fixtures/note";

// The Note page regenerates with an instruction, retries a failed run, and
// deletes to Trash; the Trash restores or purges; a Note blocked by
// extractor breakage says the video is fine (spec 5.5, 8.4, ADR 0009).

type Json = Record<string, unknown>;
type Call = { method: string; path: string; body: Json | null };

async function mockLifecycle(page: Page, note: Json): Promise<Call[]> {
  const calls: Call[] = [];
  let trashed: Json[] = [];
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
    const body =
      method === "GET" || method === "DELETE"
        ? null
        : (request.postDataJSON() as Json | null);
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
    if (p === "/api/topics") return route.fulfill(json({ topics: [] }));
    if (p === `/api/notes/${noteId}` && method === "GET")
      return route.fulfill(json(note));
    if (p === `/api/notes/${noteId}` && method === "DELETE") {
      const card = {
        ...note,
        trashed_at: "2026-09-07T03:00:00Z",
        purge_after: "2026-10-07T03:00:00Z",
      };
      trashed = [card];
      return route.fulfill(json(card));
    }
    if (p === "/api/notes" && url.searchParams.get("trashed") === "true")
      return route.fulfill(json({ notes: trashed, next_cursor: null }));
    if (p === `/api/notes/${noteId}/regenerate`)
      return route.fulfill(
        json({
          outcome: "started",
          processing_version: 2,
          estimate_usd: "0.04",
          note: {
            ...note,
            processing: {
              ...(note.processing as Json),
              state: "generating",
              version: 2,
            },
          },
        }),
      );
    if (p === `/api/notes/${noteId}/retry`)
      return route.fulfill(
        json({
          outcome: "held",
          processing_version: 2,
          estimate_usd: "0.21",
          note: {
            ...note,
            processing: {
              ...(note.processing as Json),
              state: "queued",
              version: 2,
            },
          },
        }),
      );
    if (p === `/api/notes/${noteId}/restore`) {
      trashed = [];
      return route.fulfill(
        json({ ...note, trashed_at: null, purge_after: null }),
      );
    }
    if (p === `/api/notes/${noteId}/purge`) {
      trashed = [];
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return calls;
}

function readyNote(): Json {
  const { diagram } = example("mind-map");
  return noteFor(diagram, grid(diagram.nodes)) as unknown as Json;
}

function failedNote(
  kind: "permanent" | "extractor_breakage",
  message: string,
): Json {
  const note = readyNote();
  const processing = note.processing as Json;
  const stages = processing.stages as Json[];
  return {
    ...note,
    active_revision: null,
    active_revision_id: null,
    processing: {
      ...processing,
      state: "failed",
      failure_stage: "metadata",
      failure_kind: kind,
      stages: stages.map((s) =>
        s.stage === "metadata"
          ? {
              ...s,
              status: "failed",
              last_error: { code: kind, message, is_permanent: true },
            }
          : { ...s, status: "pending", started_at: null, finished_at: null },
      ),
    },
  };
}

test("regenerating sends the instruction and reports the run", async ({
  page,
}) => {
  const calls = await mockLifecycle(page, readyNote());
  await page.goto(`/notes/${noteId}`);
  await page.getByRole("button", { name: "Regenerate" }).click();
  await page
    .getByLabel("Instruction (optional), such as “simplify this”")
    .fill("simplify this");
  await page.getByRole("button", { name: "Regenerate now" }).click();
  await expect(page.getByRole("status")).toContainText("Regenerating");
  expect(calls.find((c) => c.path.endsWith("/regenerate"))?.body).toEqual({
    instruction: "simplify this",
  });
});

test("a failed note offers retry and delete, and a held retry says so", async ({
  page,
}) => {
  const calls = await mockLifecycle(
    page,
    failedNote("permanent", "This video is private."),
  );
  await page.goto(`/notes/${noteId}`);
  await expect(
    page.getByText("The note could not be made. This video is private."),
  ).toBeVisible();
  await page.getByRole("button", { name: "Retry" }).click();
  await expect(page.getByRole("status")).toContainText(
    "Held for cost confirmation (about $0.21)",
  );
  expect(calls.some((c) => c.path.endsWith("/retry"))).toBe(true);
});

test("extractor breakage says the video is fine and hides retry", async ({
  page,
}) => {
  await mockLifecycle(
    page,
    failedNote("extractor_breakage", "please report this issue"),
  );
  await page.goto(`/notes/${noteId}`);
  await expect(
    page.getByText("The video is fine; the download tool needs an update"),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry" })).toBeHidden();
  await expect(page.getByRole("button", { name: "Delete" })).toBeVisible();
});

test("delete moves the note to the trash, where it can be restored or purged", async ({
  page,
}) => {
  const calls = await mockLifecycle(page, readyNote());
  await page.goto(`/notes/${noteId}`);
  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(/\/trash$/);
  const list = page.getByRole("list", { name: "Trashed notes" });
  await expect(list).toContainText("Spaced recall, explained");
  await expect(list).toContainText("Removed for good on");
  await list.getByRole("button", { name: "Restore" }).click();
  await expect(page.getByRole("status")).toContainText("Restored");
  await expect(page.getByText("The trash is empty.")).toBeVisible();
  expect(
    calls.some(
      (c) => c.method === "DELETE" && c.path === `/api/notes/${noteId}`,
    ),
  ).toBe(true);
  expect(calls.some((c) => c.path.endsWith("/restore"))).toBe(true);
});

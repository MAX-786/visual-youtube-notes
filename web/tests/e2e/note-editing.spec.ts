import { expect, test, type Page } from "@playwright/test";
import {
  account,
  device,
  example,
  grid,
  noteFor,
  noteId,
} from "./fixtures/note";

// Editing goes through the write-through queue: an edit is applied locally,
// stored, and sent against the Revision the page saw as Active. Conflicts
// and stale takeaways are surfaced for a decision (ADR 0004, ADR 0012).

type Json = Record<string, unknown>;
type Call = { method: string; path: string; body: Json | null };

const revision2 = "4f4f4f4f-4f4f-4f4f-8f4f-4f4f4f4f4f4f";
const conflictId = "5a5a5a5a-5a5a-4a5a-8a5a-5a5a5a5a5a5a";

function conflictSummary() {
  return {
    id: conflictId,
    revision_number: 2,
    origin: "conflict",
    parent_revision_id: "3e3e3e3e-3e3e-4e3e-8e3e-3e3e3e3e3e3e",
    generation_instruction: null,
    awaiting_decision_since: "2026-09-07T02:00:00Z",
    is_active: false,
    origin_device_session_id: "0d57fed9-8642-4f48-b1fb-8fb65147cca0",
    created_at: "2026-09-07T02:00:00Z",
  };
}

/** A server whose Note can be edited; every write is recorded in `calls`. */
async function mockEditing(
  page: Page,
  note: Json,
  options: { staleTakeawayOnce?: boolean } = {},
): Promise<Call[]> {
  const calls: Call[] = [];
  let staleLeft = options.staleTakeawayOnce ? 1 : 0;
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const p = new URL(request.url()).pathname;
    const method = request.method();
    const body = method === "GET" ? null : (request.postDataJSON() as Json);
    if (method !== "GET") calls.push({ method, path: p, body });

    if (p === "/api/auth/session") {
      return route.fulfill(
        json({
          session: device,
          account,
          access_expires_at: "2026-09-06T10:15:00Z",
          sync_cursor: 0,
        }),
      );
    }
    if (p === "/api/events/since")
      return route.fulfill(
        json({ events: [], next_cursor: 0, resync_required: false }),
      );
    if (p === "/api/events") return route.abort();
    if (p === "/api/topics")
      return route.fulfill(
        json({
          topics: [
            {
              id: "1c1c1c1c-1c1c-4c1c-8c1c-1c1c1c1c1c1c",
              name: "Learning",
              normalized_name: "learning",
              note_count: 1,
              created_at: "2026-09-07T01:00:00Z",
              updated_at: "2026-09-07T01:00:00Z",
            },
          ],
        }),
      );
    if (p === `/api/notes/${noteId}`) return route.fulfill(json(note));

    if (p === `/api/notes/${noteId}/mutations`) {
      const active = note.active_revision as Json;
      const payload = active.payload as Record<string, Json[] | Json>;
      for (const op of (body?.ops as Json[]) ?? []) {
        if (op.op !== "set_block_text") continue;
        const section = payload[op.section as string];
        const blocks = Array.isArray(section) ? section : [section];
        const target = blocks.find((b) => b.id === op.id) ?? blocks[0];
        target.text = op.text;
        target.provenance = "user_edited";
      }
      active.id = revision2;
      active.revision_number = 2;
      active.origin = "manual";
      note.active_revision_id = revision2;
      return route.fulfill(
        json({
          mutation_id: body?.mutation_id,
          outcome: "applied",
          revision_id: revision2,
          active_revision_id: revision2,
          sync_cursor: 3,
          replayed: false,
        }),
      );
    }
    if (p === `/api/notes/${noteId}/takeaway`) {
      if (staleLeft > 0) {
        staleLeft--;
        return route.fulfill(
          json(
            {
              error: {
                code: "takeaway_stale",
                message: "Another device wrote first.",
                details: { server_text: "Their takeaway.", server_cursor: 7 },
              },
            },
            409,
          ),
        );
      }
      const takeaway = {
        text: body?.text,
        updated_cursor: 8,
        updated_at: "2026-09-07T02:05:00Z",
      };
      note.takeaway = takeaway;
      return route.fulfill(json({ takeaway, sync_cursor: 8 }));
    }
    if (p === `/api/notes/${noteId}/revisions`) {
      const active = note.active_revision as Json;
      const { payload: _payload, ...summary } = active;
      void _payload;
      return route.fulfill(
        json({
          revisions: [summary, ...((note.pending_revisions as Json[]) ?? [])],
        }),
      );
    }
    if (p === `/api/notes/${noteId}/revisions/${conflictId}`) {
      const active = note.active_revision as Json;
      const payload = structuredClone(active.payload) as Json;
      (payload.summary as Json).text = "The other device's summary.";
      return route.fulfill(json({ ...conflictSummary(), payload }));
    }
    if (p === `/api/notes/${noteId}/revisions/${conflictId}/discard`) {
      note.pending_revisions = [];
      note.awaiting_decisions = 0;
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return calls;
}

test("editing a key point sends set_block_text against the Active Revision", async ({
  page,
}) => {
  const { diagram } = example("mind-map");
  const note = noteFor(diagram, grid(diagram.nodes)) as unknown as Json;
  const calls = await mockEditing(page, note);
  await page.goto(`/notes/${noteId}`);
  await page.getByRole("button", { name: "Edit" }).click();
  const field = page.getByLabel("Key points 1");
  await field.fill("Recall after a day, a week, then a month.");
  await field.press("Tab");
  await expect
    .poll(() => calls.filter((c) => c.path.endsWith("/mutations")).length)
    .toBe(1);
  const sent = calls.find((c) => c.path.endsWith("/mutations"))!;
  expect(sent.body).toMatchObject({
    base_revision_id: "3e3e3e3e-3e3e-4e3e-8e3e-3e3e3e3e3e3e",
    ops: [
      {
        op: "set_block_text",
        section: "key_points",
        id: "kp1",
        text: "Recall after a day, a week, then a month.",
      },
    ],
  });
  expect(typeof (sent.body as Json).mutation_id).toBe("string");
  await page.getByRole("button", { name: "Done editing" }).click();
  const point = page.locator('[data-element-id="kp1"]');
  await expect(point).toContainText(
    "Recall after a day, a week, then a month.",
  );
  await expect(point.getByText("edited")).toBeVisible();
});

test("a Conflict Revision can be compared with the current one and discarded", async ({
  page,
}) => {
  const { diagram } = example("mind-map");
  const note = noteFor(diagram, grid(diagram.nodes)) as unknown as Json;
  note.pending_revisions = [conflictSummary()];
  note.awaiting_decisions = 1;
  const calls = await mockEditing(page, note);
  await page.goto(`/notes/${noteId}`);
  const banner = page.getByRole("status").filter({
    hasText: "1 version awaits your decision.",
  });
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("Another device edited this note");
  await banner.getByRole("button", { name: "Compare" }).click();
  await expect(banner.getByRole("heading", { name: "Current" })).toBeVisible();
  await expect(banner).toContainText("The other device's summary.");
  await banner
    .getByRole("button", { name: "Keep the current version" })
    .click();
  await expect
    .poll(() => calls.filter((c) => c.path.endsWith("/discard")).length)
    .toBe(1);
  await expect(banner).toBeHidden();
});

test("a stale takeaway surfaces the other device's text and retries on its cursor", async ({
  page,
}) => {
  const { diagram } = example("mind-map");
  const note = noteFor(diagram, grid(diagram.nodes)) as unknown as Json;
  const calls = await mockEditing(page, note, { staleTakeawayOnce: true });
  await page.goto(`/notes/${noteId}`);
  const field = page.getByLabel("My takeaway");
  await field.fill("Mine.");
  await field.press("Tab");
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("Their takeaway.");
  expect(
    calls.filter((c) => c.path.endsWith("/takeaway"))[0].body,
  ).toMatchObject({
    text: "Mine.",
    base_cursor: 0,
  });
  await alert.getByRole("button", { name: "Use theirs" }).click();
  await expect(field).toHaveValue("Their takeaway.");
  await expect(alert).toBeHidden();
  await field.fill("Their takeaway. And mine.");
  await field.press("Tab");
  await expect
    .poll(() => calls.filter((c) => c.path.endsWith("/takeaway")).length)
    .toBe(2);
  expect(
    calls.filter((c) => c.path.endsWith("/takeaway"))[1].body,
  ).toMatchObject({
    text: "Their takeaway. And mine.",
    base_cursor: 7,
  });
});

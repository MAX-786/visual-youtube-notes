import { expect, test, type Page } from "@playwright/test";
import {
  account,
  device,
  example,
  grid,
  noteFor,
  noteId,
} from "./fixtures/note";

// Exports are plain downloads the API streams (ADR 0011): the note page
// links the PDF and the Markdown ZIP with the transcript off by default,
// and Settings → Health links the Library archive.

async function mockNote(page: Page): Promise<void> {
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  const diagram = example("mind-map").diagram;
  const note = noteFor(
    diagram,
    grid((diagram as { nodes: { id: string }[] }).nodes),
  );
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const p = new URL(request.url()).pathname;
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
    if (p === `/api/notes/${noteId}` && request.method() === "GET")
      return route.fulfill(json(note));
    if (p === "/api/topics") return route.fulfill(json({ topics: [] }));
    if (p === "/api/tags") return route.fulfill(json({ tags: [] }));
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
}

test("the note page links both exports and the transcript is opt-in", async ({
  page,
}) => {
  await mockNote(page);
  await page.goto(`/notes/${noteId}`);
  await page.getByText("Export", { exact: true }).click();
  const pdf = page.getByRole("link", { name: "PDF" });
  const zip = page.getByRole("link", { name: "Markdown (ZIP)" });
  await expect(pdf).toHaveAttribute("href", `/api/exports/notes/${noteId}.pdf`);
  await expect(zip).toHaveAttribute("href", `/api/exports/notes/${noteId}.zip`);
  await expect(pdf).toHaveAttribute("download", "");
  await page.getByLabel("Include transcript").check();
  await expect(pdf).toHaveAttribute(
    "href",
    `/api/exports/notes/${noteId}.pdf?transcript=true`,
  );
  await expect(zip).toHaveAttribute(
    "href",
    `/api/exports/notes/${noteId}.zip?transcript=true`,
  );
});

test("Settings → Health links the library archive", async ({ page }) => {
  await mockNote(page);
  await page.goto("/settings/health");
  const archive = page.getByRole("link", { name: "Download library archive" });
  await expect(archive).toHaveAttribute("href", "/api/exports/library");
  await expect(archive).toHaveAttribute("download", "");
});

import { expect, test, type Page } from "@playwright/test";
import {
  account,
  device,
  example,
  grid,
  noteFor,
  noteId,
} from "./fixtures/note";

// The Library searches with its filters in the URL, shows why a Note
// surfaced and a Transcript excerpt when that is all that matched, and
// words each way semantic search can be unavailable (ADR 0005, ADR 0007).

type Json = Record<string, unknown>;

async function mockSearch(
  page: Page,
  semanticUnavailable: string | null,
): Promise<URL[]> {
  const searches: URL[] = [];
  const { diagram } = example("mind-map");
  const card = noteFor(diagram, grid(diagram.nodes)) as unknown as Json;
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
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
    if (p === "/api/notes")
      return route.fulfill(json({ notes: [card], next_cursor: null }));
    if (p === "/api/search") {
      searches.push(url);
      return route.fulfill(
        json({
          results: [
            {
              note: card,
              score: 0.0328,
              matched_kinds: ["transcript_chunk"],
              excerpts: [
                {
                  chunk_id: "a3c250c0-47ea-535e-8c29-ed9f3487a082",
                  text: "The forgetting curve was measured by Ebbinghaus with nonsense syllables in the nineteenth century.",
                },
              ],
            },
          ],
          next_cursor: null,
          exhausted: true,
          semantic_unavailable: semanticUnavailable,
        }),
      );
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return searches;
}

test("a search with filters lands in the URL and shows the transcript excerpt", async ({
  page,
}) => {
  const searches = await mockSearch(page, null);
  await page.goto("/library");
  await expect(page.getByRole("list", { name: "Notes" })).toContainText(
    "Spaced recall, explained",
  );
  await page.getByRole("searchbox", { name: "Search" }).fill("Ebbinghaus");
  await page.locator("summary", { hasText: "Filters" }).click();
  await page.getByLabel("From").fill("2026-09-01");
  await page
    .getByLabel("Topic")
    .selectOption("1c1c1c1c-1c1c-4c1c-8c1c-1c1c1c1c1c1c");
  await page.getByLabel("At most (minutes)").fill("30");
  await page.getByRole("button", { name: "Search" }).click();
  const results = page.getByRole("list", { name: "Search results" });
  await expect(results).toContainText("Spaced recall, explained");
  await expect(results.getByText("transcript")).toBeVisible();
  await expect(results.locator("blockquote")).toContainText("Ebbinghaus");
  await expect(page).toHaveURL(/q=Ebbinghaus/);
  await expect(page).toHaveURL(/from=2026-09-01/);
  await expect(page).toHaveURL(/max_duration=1800/);
  const sent = searches[searches.length - 1];
  expect(sent.searchParams.get("q")).toBe("Ebbinghaus");
  expect(sent.searchParams.get("topic_id")).toBe(
    "1c1c1c1c-1c1c-4c1c-8c1c-1c1c1c1c1c1c",
  );
  expect(sent.searchParams.get("max_duration")).toBe("1800");
  await expect(
    page.getByText("That is everything that matched."),
  ).toBeVisible();
  await expect(page.getByRole("status")).toBeHidden();

  // The URL alone reproduces the search.
  await page.goto(`/library?q=Ebbinghaus&from=2026-09-01`);
  await expect(page.getByRole("searchbox", { name: "Search" })).toHaveValue(
    "Ebbinghaus",
  );
  await expect(results).toContainText("Spaced recall, explained");
});

test("the cap and a fault are worded differently", async ({ page }) => {
  await mockSearch(page, "soft_cap");
  await page.goto("/library?q=recall");
  await expect(page.getByRole("status")).toContainText("monthly cap");
  await mockSearch(page, "provider_error");
  await page.goto("/library?q=recall");
  await expect(page.getByRole("status")).toContainText("unavailable right now");
});

test("the note page still opens from a result", async ({ page }) => {
  await mockSearch(page, null);
  await page.goto("/library?q=recall");
  await page
    .getByRole("list", { name: "Search results" })
    .getByRole("link", { name: /Spaced recall/ })
    .first()
    .click();
  await expect(page).toHaveURL(new RegExp(`/notes/${noteId}`));
});

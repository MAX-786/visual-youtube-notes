import { expect, test } from "@playwright/test";
import { example, grid, mockApi, noteFor, noteId } from "./fixtures/note";

// The note page renders every Diagram type from the server's positions
// (ADR 0011): the four valid design examples are served as a Note's Active
// Revision and drawn with direct, accessible editing controls.

test("a mind map is drawn from server boxes and expands its collapsed group", async ({
  page,
}) => {
  const { diagram } = example("mind-map");
  await mockApi(page, noteFor(diagram, grid(diagram.nodes)));
  await page.goto(`/notes/${noteId}`);
  await expect(
    page.getByRole("heading", { name: "Spaced recall, explained" }),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Mind map" })).toBeVisible();
  const canvas = page.getByRole("application", { name: "Mind map diagram" });
  await expect(canvas.locator(".svelte-flow__node")).toHaveCount(2);
  await expect(
    canvas.getByRole("group", { name: "Spaced recall" }),
  ).toBeVisible();
  await canvas.getByRole("button", { name: "Show 2 more" }).click();
  await expect(canvas.locator(".svelte-flow__node")).toHaveCount(4);
  await expect(canvas.locator(".svelte-flow__edge")).toHaveCount(3);
  await expect(page.getByText("Text version of the diagram")).toHaveCount(0);
  await page.getByRole("button", { name: "Edit" }).click();
  await expect(
    canvas.getByRole("textbox", { name: /Diagram element/ }),
  ).toHaveCount(4);
  await expect(
    page.getByRole("heading", { name: "What I can use" }),
  ).toBeVisible();
});

test("a process flow draws arrows and labelled branches", async ({ page }) => {
  const { diagram } = example("process-flow");
  await mockApi(page, noteFor(diagram, grid(diagram.nodes)));
  await page.goto(`/notes/${noteId}`);
  const canvas = page.getByRole("application", {
    name: "Process flow diagram",
  });
  await expect(canvas.locator(".svelte-flow__node")).toHaveCount(3);
  await expect(canvas.locator(".svelte-flow__edge")).toHaveCount(2);
  await expect(canvas.locator(".edge-label")).toHaveText("When ready");
  await expect(page.getByText("Text version of the diagram")).toHaveCount(0);
});

test("a timeline is an ordered list that reveals hidden events", async ({
  page,
}) => {
  const { diagram } = example("timeline");
  await mockApi(page, noteFor(diagram, {}));
  await page.goto(`/notes/${noteId}`);
  const list = page.getByRole("list", { name: "Timeline" });
  await expect(list).toHaveClass(/horizontal/);
  await expect(list.locator("li")).toHaveCount(2);
  await list.getByRole("button", { name: "+1 more" }).click();
  await expect(list.locator("li")).toHaveCount(3);
  await expect(list.locator("li").nth(2)).toContainText(
    "Review after one week",
  );
});

test("a comparison is a table with one cell per column", async ({ page }) => {
  const { diagram } = example("comparison");
  await mockApi(page, noteFor(diagram, {}));
  await page.goto(`/notes/${noteId}`);
  await expect(page.getByRole("heading", { name: "Comparison" })).toBeVisible();
  const table = page.locator("table");
  await expect(table.locator("thead th")).toHaveText([
    "Criterion",
    "Cramming",
    "Spaced recall",
  ]);
  await expect(table.locator("tbody tr")).toHaveCount(2);
  await expect(table.locator("tbody tr").first().locator("td")).toHaveCount(2);
  await page.getByRole("button", { name: "Edit" }).click();
  await expect(
    page.getByRole("textbox", { name: "Timing, Cramming" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Add option" })).toBeVisible();
});

test("the transcript stays lazy and is cached after it is opened", async ({
  page,
}) => {
  const { diagram } = example("mind-map");
  let transcriptReads = 0;
  await mockApi(page, noteFor(diagram, grid(diagram.nodes)), {
    transcript: {
      note_id: noteId,
      source_kind: "caption_human",
      language_code: "en",
      text: "Spaced recall works.",
      chunks: [
        {
          id: "41000000-0000-0000-0000-000000000001",
          ordinal: 0,
          char_start: 0,
          char_end: 20,
          token_count: 4,
        },
      ],
      created_at: "2026-09-07T01:00:00Z",
    },
    onTranscript: () => transcriptReads++,
  });
  await page.goto(`/notes/${noteId}`);
  expect(transcriptReads).toBe(0);
  await page.getByText("Transcript", { exact: true }).click();
  await expect(page.getByText("Spaced recall works.")).toBeVisible();
  expect(transcriptReads).toBe(1);
  await page.getByText("Transcript", { exact: true }).click();
  await page.getByText("Transcript", { exact: true }).click();
  await expect(page.getByText("Spaced recall works.")).toBeVisible();
  expect(transcriptReads).toBe(1);
});

test("a search link opens the matched transcript section and highlights it", async ({
  page,
}) => {
  const { diagram } = example("mind-map");
  await mockApi(page, noteFor(diagram, grid(diagram.nodes)), {
    transcript: {
      note_id: noteId,
      source_kind: "caption_human",
      language_code: "en",
      text: "Spaced recall works.",
      chunks: [
        {
          id: "41000000-0000-0000-0000-000000000001",
          ordinal: 0,
          char_start: 0,
          char_end: 20,
          token_count: 4,
        },
      ],
      created_at: "2026-09-07T01:00:00Z",
    },
  });
  await page.goto(
    `/notes/${noteId}?find=recall&match=transcript_chunk&chunk=41000000-0000-0000-0000-000000000001`,
  );
  await expect(page.getByText("Spaced recall works.")).toBeVisible();
  await expect(page.locator("[data-chunk-id] mark")).toHaveText("recall");
  await expect(page.locator("details.transcript-panel")).toHaveAttribute(
    "open",
    "",
  );
});

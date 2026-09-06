import { expect, test } from "@playwright/test";
import { example, grid, mockApi, noteFor, noteId } from "./fixtures/note";

// The note page renders every Diagram type from the server's positions
// (ADR 0011): the four valid design examples are served as a Note's Active
// Revision and drawn, expanded, and read back as their Text alternative.

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
  await expect(canvas.locator("g.node")).toHaveCount(2);
  await expect(
    canvas.locator("g.node text").filter({ hasText: "Spaced recall" }),
  ).toBeVisible();
  await canvas.getByRole("button", { name: "Show 2 more" }).click();
  await expect(canvas.locator("g.node")).toHaveCount(4);
  await expect(canvas.locator("line.edge")).toHaveCount(3);
  await page.getByText("Text version of the diagram").click();
  await expect(page.locator("pre.alternative")).toHaveText(
    "Spaced recall\n  Review schedule\n    After one day\n    After one week",
  );
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
  await expect(canvas.locator("g.node")).toHaveCount(3);
  await expect(canvas.locator("line.edge[marker-end]")).toHaveCount(2);
  await expect(canvas.locator("text.edge-label")).toHaveText("When ready");
  await page.getByText("Text version of the diagram").click();
  await expect(page.locator("pre.alternative")).toContainText(
    "2. Wait one day\n   Branch: When ready → Recall without notes",
  );
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
  await page.getByText("Text version of the diagram").click();
  await expect(page.locator("pre.alternative")).toContainText(
    "Criterion | Cramming | Spaced recall",
  );
});

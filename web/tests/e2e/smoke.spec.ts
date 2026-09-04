import { expect, test } from "@playwright/test";

test("serves the application shell", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveTitle("Visual YouTube Notes");
});

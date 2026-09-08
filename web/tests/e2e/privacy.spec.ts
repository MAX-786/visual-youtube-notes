import { expect, test } from "@playwright/test";

test("the extension privacy policy is public and complete", async ({
  page,
}) => {
  await page.route("**/api/**", (route) =>
    route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "unauthenticated", message: "Sign in." },
      }),
    }),
  );

  await page.goto("/privacy");

  await expect(page).toHaveURL(/\/privacy$/);
  await expect(
    page.getByRole("heading", { name: "Your notes stay yours." }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Chrome Web Store Limited Use" }),
  ).toBeVisible();
  await expect(page.getByText("Last updated 8 September 2026")).toBeVisible();
});

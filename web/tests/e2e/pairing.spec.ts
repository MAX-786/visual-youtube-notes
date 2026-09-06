import { expect, test, type Page } from "@playwright/test";
import { account, device } from "./fixtures/note";

// The approval page names the browser, approves or denies within the five
// minute window, and says what happens next (ADR 0003, ADR 0013).

const requestId = "Xk3vT9p1QmR4sZ7wL2nB8cD5fG0hJ6kM1oP3qS5tU7v";

async function mockPairing(page: Page): Promise<string[]> {
  const decisions: string[] = [];
  let status = "pending";
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  const request = () => ({
    request_id: requestId,
    extension_id: "fjodmlmkfifedoamachdbgigfpaldicm",
    display_name: "Edge",
    status,
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + 4 * 60 * 1000).toISOString(),
  });
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
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
    if (p === `/api/pairing/requests/${requestId}`)
      return route.fulfill(json(request()));
    if (p === `/api/pairing/requests/${requestId}/approve`) {
      decisions.push("approve");
      status = "approved";
      return route.fulfill(json(request()));
    }
    if (p === `/api/pairing/requests/${requestId}/deny`) {
      decisions.push("deny");
      status = "denied";
      return route.fulfill(json(request()));
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return decisions;
}

test("the owner approves the named browser", async ({ page }) => {
  const decisions = await mockPairing(page);
  await page.goto(`/pair/${requestId}`);
  await expect(page.getByText("Edge", { exact: true })).toBeVisible();
  await expect(page.getByText("This request expires in")).toBeVisible();
  await page.getByRole("button", { name: "Approve this browser" }).click();
  await expect(page.getByRole("status")).toContainText("Approved");
  expect(decisions).toEqual(["approve"]);
});

test("the owner denies the request", async ({ page }) => {
  const decisions = await mockPairing(page);
  await page.goto(`/pair/${requestId}`);
  await page.getByRole("button", { name: "Deny" }).click();
  await expect(page.getByRole("status")).toContainText("Denied");
  expect(decisions).toEqual(["deny"]);
});

test("a signed-out owner is sent to sign in and back", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/auth/session" || p === "/api/auth/refresh/csrf")
      return route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "unauthenticated", message: "Sign in." },
        }),
      });
    return route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "not_found", message: "no" } }),
    });
  });
  await page.goto(`/pair/${requestId}`);
  await expect(page).toHaveURL(/\/sign-in/);
});

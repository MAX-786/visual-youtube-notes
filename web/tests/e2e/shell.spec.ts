import { expect, test, type Page } from "@playwright/test";

// The shell against a mocked API: the gate sends a signed-out visitor to
// sign-in, a login lands on Today, and the shell still paints offline once
// the service worker has installed.

const account = {
  id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  username: "owner",
  display_name: "The Owner",
  timezone: "Asia/Calcutta",
  created_at: "2026-09-06T10:00:00Z",
  password_changed_at: "2026-09-06T10:00:00Z",
};
const device = {
  id: "0d57fed9-8642-4f48-b1fb-8fb65147cca0",
  kind: "pwa",
  display_name: "Chrome",
  extension_id: null,
  created_at: "2026-09-06T10:00:00Z",
  last_seen_at: "2026-09-06T10:00:00Z",
  last_refreshed_at: "2026-09-06T10:00:00Z",
  inactive_expires_at: "2026-10-06T10:00:00Z",
  is_current: true,
  push_subscriptions: 0,
};
const unauthenticated = {
  status: 401,
  contentType: "application/json",
  body: JSON.stringify({
    error: { code: "unauthenticated", message: "Sign in to continue." },
  }),
};

async function mockApi(page: Page, signedIn: { current: boolean }) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const method = route.request().method();
    if (path === "/api/auth/session") {
      return signedIn.current
        ? route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              session: device,
              account,
              access_expires_at: "2026-09-06T10:15:00Z",
              sync_cursor: 0,
            }),
          })
        : route.fulfill(unauthenticated);
    }
    if (path === "/api/auth/refresh/csrf")
      return route.fulfill(unauthenticated);
    if (path === "/api/auth/login" && method === "POST") {
      signedIn.current = true;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          session: device,
          account,
          csrf_token: "csrf",
          access_expires_at: "2026-09-06T10:15:00Z",
          sync_cursor: 0,
        }),
      });
    }
    if (path === "/api/notes") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ notes: [], next_cursor: null, sync_cursor: 0 }),
      });
    }
    if (path === "/api/events/since") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          events: [],
          next_cursor: 0,
          resync_required: false,
        }),
      });
    }
    if (path === "/api/events") return route.abort();
    return route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "not_found", message: "no" } }),
    });
  });
}

test("a signed-out visitor is sent to sign-in and lands on Today after signing in", async ({
  page,
}) => {
  const signedIn = { current: false };
  await mockApi(page, signedIn);
  await page.goto("/");
  await expect(page).toHaveURL(/\/sign-in$/);
  await page.getByLabel("Username").fill("owner");
  await page.getByLabel("Password").fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Today" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
});

test("the shell paints offline once the service worker is installed", async ({
  page,
  context,
  browserName,
}) => {
  test.skip(
    browserName !== "chromium",
    "service workers are exercised in Chromium",
  );
  const signedIn = { current: true };
  await mockApi(page, signedIn);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Today" })).toBeVisible();
  await page.evaluate(async () => {
    const registration = await navigator.serviceWorker.ready;
    if (!registration.active) throw new Error("no active service worker");
  });
  await context.setOffline(true);
  await page.reload();
  // The shell came from the service worker's cache: nothing else could have
  // served the document with the network off.
  await expect(page.getByRole("heading", { name: "Today" })).toBeVisible({
    timeout: 15000,
  });
  await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
  await context.setOffline(false);
});

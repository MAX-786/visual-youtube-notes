import { expect, test, type Page } from "@playwright/test";
import { account, device } from "./fixtures/note";

// A full page load has no CSRF token in memory. ADR 0003's recovery path,
// GET /auth/refresh/csrf, is taken at boot, and a csrf_rejected answer is
// recovered from and retried once, so the Owner never sees "reload the app"
// for a token the app can fetch itself. The pairing approval is the
// mutation most often reached by a fresh load, from the extension's link.

const requestId = "_N-klx0E83j_gvZv_2Trx4Ifsr_2MQdl";

async function mockPairingWithCsrf(
  page: Page,
  options: { recoveryFailsFirst?: boolean } = {},
): Promise<{ approvals: string[]; recoveries: number }> {
  const state = { approvals: [] as string[], recoveries: 0 };
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  let status = "pending";
  const request = () => ({
    request_id: requestId,
    extension_id: "fjodmlmkfifedoamachdbgigfpaldicm",
    display_name: "Chromium",
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
    if (p === "/api/auth/refresh/csrf") {
      state.recoveries += 1;
      if (options.recoveryFailsFirst && state.recoveries === 1)
        return route.fulfill(
          json({ error: { code: "unauthenticated", message: "no" } }, 401),
        );
      return route.fulfill(json({ csrf_token: "recovered-token" }));
    }
    if (p === "/api/events/since")
      return route.fulfill(
        json({ events: [], next_cursor: 0, resync_required: false }),
      );
    if (p === "/api/events") return route.abort();
    if (p === `/api/pairing/requests/${requestId}`)
      return route.fulfill(json(request()));
    if (p === `/api/pairing/requests/${requestId}/approve`) {
      const token = route.request().headers()["x-csrf-token"] ?? "";
      state.approvals.push(token);
      // The server's rule: a cookie-authenticated mutation without the token.
      if (!token)
        return route.fulfill(
          json(
            {
              error: {
                code: "csrf_rejected",
                message:
                  "The request is missing its CSRF token. Reload the app and try again.",
              },
            },
            403,
          ),
        );
      status = "approved";
      return route.fulfill(json(request()));
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return state;
}

test("a fresh load recovers the CSRF token before the first mutation", async ({
  page,
}) => {
  const state = await mockPairingWithCsrf(page);
  await page.goto(`/pair/${requestId}`);
  await page.getByRole("button", { name: "Approve this browser" }).click();
  await expect(page.getByText(/approved/i).first()).toBeVisible();
  expect(state.recoveries).toBeGreaterThanOrEqual(1);
  expect(state.approvals).toEqual(["recovered-token"]);
});

test("a csrf_rejected answer is recovered from and the mutation retried once", async ({
  page,
}) => {
  const state = await mockPairingWithCsrf(page, { recoveryFailsFirst: true });
  await page.goto(`/pair/${requestId}`);
  await page.getByRole("button", { name: "Approve this browser" }).click();
  await expect(page.getByText(/approved/i).first()).toBeVisible();
  // Boot's recovery failed, so the first approve went out bare and was
  // refused; the middleware recovered and retried with the token.
  expect(state.approvals).toEqual(["", "recovered-token"]);
  expect(state.recoveries).toBe(2);
  await expect(page.getByText("missing its CSRF token")).toHaveCount(0);
});

import { expect, test, type Page } from "@playwright/test";
import { account, device } from "./fixtures/note";

// Settings → Notifications asks permission only on the press, registers the
// browser's subscription against the session, and toggles the two switches
// independently (ADR 0014). The browser's push machinery is faked: no push
// service is reachable from a test.

type Json = Record<string, unknown>;
type Call = { method: string; path: string; body: Json | null };

const endpoint = "https://push.example/sub/abc";

async function mockPush(page: Page): Promise<Call[]> {
  const calls: Call[] = [];
  const rows: Json[] = [];
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  // A fake PushManager and Notification on the page: subscribe yields a
  // subscription with keys in the browser's shape.
  await page.addInitScript(() => {
    const fakeSubscription = {
      endpoint: "https://push.example/sub/abc",
      expirationTime: null,
      options: { userVisibleOnly: true, applicationServerKey: null },
      getKey: (name: string) =>
        name === "p256dh"
          ? new Uint8Array(65).fill(4).buffer
          : new Uint8Array(16).fill(7).buffer,
      unsubscribe: async () => true,
    };
    let current: typeof fakeSubscription | null = null;
    const fakeManager = {
      getSubscription: async () => current,
      subscribe: async () => {
        current = fakeSubscription;
        return fakeSubscription;
      },
    };
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: {
        ready: Promise.resolve({ pushManager: fakeManager }),
        register: async () => undefined,
      },
    });
    Object.defineProperty(window, "PushManager", {
      configurable: true,
      value: function PushManager() {},
    });
    let permission = "default";
    Object.defineProperty(window, "Notification", {
      configurable: true,
      value: {
        get permission() {
          return permission;
        },
        requestPermission: async () => {
          permission = "granted";
          return "granted";
        },
      },
    });
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const p = new URL(request.url()).pathname;
    const method = request.method();
    const body =
      method === "GET" || method === "DELETE"
        ? null
        : (request.postDataJSON() as Json);
    if (method !== "GET") calls.push({ method, path: p, body });
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
    if (p === "/api/push/vapid-public-key")
      return route.fulfill(json({ public_key: "B" + "A".repeat(86) }));
    if (p === "/api/push-subscriptions" && method === "GET")
      return route.fulfill(json({ subscriptions: rows }));
    if (p === "/api/push-subscriptions" && method === "PUT") {
      const row = {
        id: "9e9e9e9e-9e9e-4e9e-8e9e-9e9e9e9e9e9e",
        device_session_id: device.id,
        device_display_name: device.display_name,
        endpoint: body?.endpoint,
        expiration_time: null,
        notes_enabled: true,
        ops_enabled: true,
        created_at: "2026-09-07T05:00:00Z",
        last_success_at: null,
        last_failure_at: null,
        consecutive_failures: 0,
      };
      rows.splice(0, rows.length, row);
      return route.fulfill(json(row));
    }
    const patch = p.match(/^\/api\/push-subscriptions\/([0-9a-f-]+)$/);
    if (patch && method === "PATCH") {
      const row = rows[0];
      Object.assign(row, body);
      return route.fulfill(json(row));
    }
    if (patch && method === "DELETE") {
      rows.splice(0, rows.length);
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return calls;
}

test("the explicit press subscribes this browser and the switches toggle independently", async ({
  page,
}) => {
  const calls = await mockPush(page);
  await page.goto("/settings/notifications");
  await expect(
    page.getByText("Get notified when notes are ready?"),
  ).toBeVisible();
  await expect(page.getByText("No browser is subscribed yet.")).toBeVisible();
  await page.getByRole("button", { name: "Turn on notifications" }).click();
  await expect(page.getByRole("status")).toContainText(
    "This browser will be notified",
  );
  const registered = calls.find(
    (c) => c.method === "PUT" && c.path === "/api/push-subscriptions",
  );
  expect(registered?.body).toMatchObject({
    endpoint,
    keys: {
      p256dh: expect.stringMatching(/^[A-Za-z0-9_-]{86,88}$/),
      auth: expect.stringMatching(/^[A-Za-z0-9_-]{22,24}$/),
    },
    notes_enabled: true,
    ops_enabled: true,
  });
  const list = page.getByRole("region", { name: "Subscribed browsers" });
  await expect(list).toContainText("Chrome (this browser)");
  const notes = list.getByLabel(/Note notifications/);
  const ops = list.getByLabel(/Operational alerts/);
  await expect(notes).toBeChecked();
  await expect(ops).toBeChecked();
  await ops.uncheck();
  await expect(notes).toBeChecked();
  const toggled = calls.find((c) => c.method === "PATCH");
  expect(toggled?.body).toEqual({ ops_enabled: false });
  await page.getByRole("button", { name: "Turn off for this browser" }).click();
  await expect(page.getByRole("status")).toContainText("Notifications are off");
  expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  await expect(page.getByText("No browser is subscribed yet.")).toBeVisible();
});

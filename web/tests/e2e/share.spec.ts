import { expect, test, type Page } from "@playwright/test";
import {
  account,
  device,
  example,
  grid,
  noteFor,
  noteId,
} from "./fixtures/note";

// The share target scans url, text and title for every YouTube link, submits
// one, offers a picker for several, and keeps a share that cannot be sent
// now until the session is live and online (spec 5.2, ADR 0012).

type Json = Record<string, unknown>;

async function mockShare(
  page: Page,
  options: { signedIn: boolean; submissions: "ok" | "down" },
) {
  const state = {
    signedIn: options.signedIn,
    submissions: options.submissions,
    posted: [] as string[],
  };
  const { diagram } = example("mind-map");
  const card = noteFor(diagram, grid(diagram.nodes)) as unknown as Json;
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const p = new URL(request.url()).pathname;
    const grant = {
      session: device,
      account,
      access_expires_at: "2026-09-06T10:15:00Z",
      sync_cursor: 0,
    };
    if (p === "/api/auth/session" || p === "/api/auth/refresh/csrf") {
      if (!state.signedIn)
        return route.fulfill(
          json(
            { error: { code: "unauthenticated", message: "Sign in." } },
            401,
          ),
        );
      return route.fulfill(json(grant));
    }
    if (p === "/api/auth/login") {
      state.signedIn = true;
      return route.fulfill(json({ ...grant, csrf_token: "csrf" }));
    }
    if (p === "/api/events/since")
      return route.fulfill(
        json({ events: [], next_cursor: 0, resync_required: false }),
      );
    if (p === "/api/events") return route.abort();
    if (p === "/api/notes")
      return route.fulfill(
        json({ notes: [card], next_cursor: null, sync_cursor: 0 }),
      );
    if (p === `/api/notes/${noteId}`) return route.fulfill(json(card));
    if (p === "/api/submissions") {
      if (!state.signedIn)
        return route.fulfill(
          json(
            { error: { code: "unauthenticated", message: "Sign in." } },
            401,
          ),
        );
      if (state.submissions === "down") return route.abort("failed");
      const body = request.postDataJSON() as { url: string };
      state.posted.push(body.url);
      return route.fulfill(
        json(
          {
            outcome: "created",
            note: card,
            undo_until: new Date(Date.now() + 5000).toISOString(),
          },
          202,
        ),
      );
    }
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
  return state;
}

test("a shared text with one link is submitted and opens the note", async ({
  page,
}) => {
  const state = await mockShare(page, { signedIn: true, submissions: "ok" });
  await page.goto(
    "/share?title=Spaced+recall&text=" +
      encodeURIComponent(
        "Check this out https://youtu.be/aircAruvnKk?si=abc from the app",
      ),
  );
  await expect(page).toHaveURL(new RegExp(`/notes/${noteId}`));
  expect(state.posted).toEqual(["https://www.youtube.com/watch?v=aircAruvnKk"]);
});

test("several links offer a picker", async ({ page }) => {
  const state = await mockShare(page, { signedIn: true, submissions: "ok" });
  await page.goto(
    "/share?text=" +
      encodeURIComponent(
        "https://www.youtube.com/watch?v=aircAruvnKk&list=PLx and https://youtu.be/dQw4w9WgXcQ",
      ),
  );
  const list = page.getByRole("list", { name: "Shared videos" });
  await expect(list.locator("li")).toHaveCount(2);
  await list
    .locator("li")
    .nth(1)
    .getByRole("button", { name: "Save this one" })
    .click();
  await expect(page).toHaveURL(new RegExp(`/notes/${noteId}`));
  expect(state.posted).toEqual(["https://www.youtube.com/watch?v=dQw4w9WgXcQ"]);
});

test("a share with no link shows the manual field pre-filled", async ({
  page,
}) => {
  await mockShare(page, { signedIn: true, submissions: "ok" });
  await page.goto("/share?text=" + encodeURIComponent("just some words"));
  await expect(
    page.getByRole("heading", { name: "No YouTube link found" }),
  ).toBeVisible();
  await expect(
    page.getByLabel("What was shared; paste or fix the link"),
  ).toHaveValue("just some words");
});

test("a share while the server is unreachable waits and submits on reconnect", async ({
  page,
}) => {
  const state = await mockShare(page, { signedIn: true, submissions: "down" });
  await page.goto(
    "/share?url=" +
      encodeURIComponent("https://www.youtube.com/watch?v=aircAruvnKk"),
  );
  await expect(page.getByRole("status")).toContainText("Saved for later");
  expect(state.posted).toEqual([]);
  state.submissions = "ok";
  await page.goto("/");
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await expect.poll(() => state.posted.length).toBe(1);
  expect(state.posted[0]).toBe("https://www.youtube.com/watch?v=aircAruvnKk");
});

test("a share while signed out survives the sign-in", async ({ page }) => {
  const state = await mockShare(page, { signedIn: false, submissions: "ok" });
  await page.goto(
    "/share?text=" + encodeURIComponent("https://youtu.be/aircAruvnKk"),
  );
  await expect(page).toHaveURL(/\/sign-in/);
  expect(state.posted).toEqual([]);
  await page.getByLabel("Username").fill("owner");
  await page.getByLabel("Password").fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in" }).click();
  // The drain runs on the queue timer once the session lands; CI machines
  // take longer than the default five seconds to get there.
  await expect.poll(() => state.posted.length, { timeout: 15_000 }).toBe(1);
});

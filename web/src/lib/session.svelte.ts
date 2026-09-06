// The session gate (ADR 0012). Foregrounding while online blocks note content
// behind GET /auth/session; offline reveals the cache unchecked (spec 17.9).
// Sign-out, local or forced, clears every store and tells every other tab.
import { goto } from "$app/navigation";
import type { components } from "@visual-notes/api-client";
import {
  api,
  recoverCsrfToken,
  setCsrfToken,
  setSessionLostHandler,
} from "./api";
import { clearAll, prune, setCursor } from "./db";

export type Account = components["schemas"]["Account"];
export type DeviceSession = components["schemas"]["DeviceSession"];

export type SessionStatus = "unknown" | "signed-out" | "signed-in" | "offline";

export const session = $state({
  status: "unknown" as SessionStatus,
  account: null as Account | null,
  device: null as DeviceSession | null,
  syncCursor: 0,
  /** Where to go after sign-in, when a route captured an intent. */
  pendingPath: null as string | null,
});

export const CHANNEL = "vyn:sync";
let channel: BroadcastChannel | null = null;

function broadcast(message: unknown): void {
  channel?.postMessage(message);
}

/** Boots the gate: recovers the CSRF token and checks the session once. */
export async function bootSession(): Promise<void> {
  if (typeof window === "undefined") return;
  if (!channel && "BroadcastChannel" in window) {
    channel = new BroadcastChannel(CHANNEL);
    channel.addEventListener("message", (event) => {
      if (event.data?.type === "sign-out")
        void signOut({ local: true, broadcast: false });
    });
  }
  setSessionLostHandler(() => void signOut({ local: true, broadcast: true }));
  await checkSession();
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") void checkSession();
  });
  window.addEventListener("online", () => void checkSession());
}

/** GET /auth/session while online; reveals the cache unchecked offline. */
export async function checkSession(): Promise<void> {
  if (!navigator.onLine) {
    if (session.status === "unknown") session.status = "offline";
    return;
  }
  const { data, error, response } = await api.GET("/auth/session");
  if (data) {
    session.status = "signed-in";
    session.account = data.account;
    session.device = data.session;
    session.syncCursor = data.sync_cursor;
    await prune();
    return;
  }
  if (response.status === 401 || (error && response.status === 403)) {
    await recoverCsrfToken();
    // The recovery path proves the refresh cookie; a second gate check tells
    // whether the access cookie was merely expired.
    const retry = await api.GET("/auth/session");
    if (retry.data) {
      session.status = "signed-in";
      session.account = retry.data.account;
      session.device = retry.data.session;
      session.syncCursor = retry.data.sync_cursor;
      return;
    }
    await signOut({ local: true, broadcast: true, silent: true });
    return;
  }
  // The server is unreachable or failing; treat it like offline rather than
  // clearing a library over a transient fault.
  if (session.status === "unknown") session.status = "offline";
}

export async function signIn(
  username: string,
  password: string,
  deviceName?: string,
): Promise<string | null> {
  const { data, error } = await api.POST("/auth/login", {
    body: { username, password, device_name: deviceName },
  });
  if (!data) {
    return error?.error.message ?? "Sign-in failed.";
  }
  setCsrfToken(data.csrf_token);
  session.status = "signed-in";
  session.account = data.account;
  session.device = data.session;
  session.syncCursor = data.sync_cursor;
  await setCursor(data.sync_cursor);
  const next = session.pendingPath ?? "/";
  session.pendingPath = null;
  await goto(next);
  return null;
}

/** Signs out. `local` skips the server call, for a session already gone. */
export async function signOut(
  options: { local?: boolean; broadcast?: boolean; silent?: boolean } = {},
): Promise<void> {
  if (!options.local) {
    await api.POST("/auth/logout").catch(() => undefined);
  }
  setCsrfToken(null);
  session.status = "signed-out";
  session.account = null;
  session.device = null;
  session.syncCursor = 0;
  await clearAll().catch(() => undefined);
  if ("caches" in window) {
    const keys = await caches.keys();
    await Promise.all(keys.map((key) => caches.delete(key)));
  }
  if (options.broadcast !== false) broadcast({ type: "sign-out" });
  if (!options.silent) await goto("/sign-in");
  else await goto("/sign-in");
}

export function adoptGrant(grant: components["schemas"]["SessionGrant"]): void {
  setCsrfToken(grant.csrf_token);
  session.account = grant.account;
  session.device = grant.session;
  session.syncCursor = grant.sync_cursor;
}

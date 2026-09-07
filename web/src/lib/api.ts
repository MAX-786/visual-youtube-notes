// The typed API client (ADR 0015). Same-origin cookies carry the session; the
// CSRF token lives in memory only (ADR 0012) and is attached to every
// non-GET request. A 401 on an authenticated call triggers one serialized
// refresh and a retry; a second 401 means the session is gone.
import type { Middleware } from "@visual-notes/api-client";
import { createApiClient, type components } from "@visual-notes/api-client";

export type ErrorResponse = components["schemas"]["ErrorResponse"];
export type ErrorCode = components["schemas"]["ErrorCode"];

let csrfToken: string | null = null;
let refreshInFlight: Promise<boolean> | null = null;
let onSessionLost: (() => void) | null = null;

/** Remembers the CSRF token a login, refresh or password change returned. */
export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

/** Whether a token is in memory; a fresh page load has none until recovered. */
export function hasCsrfToken(): boolean {
  return csrfToken !== null;
}

/** Registers what to do when a refresh fails: the session is gone. */
export function setSessionLostHandler(handler: (() => void) | null): void {
  onSessionLost = handler;
}

export const api = createApiClient("/api");

const csrf: Middleware = {
  async onRequest({ request }) {
    if (request.method !== "GET" && request.method !== "HEAD" && csrfToken) {
      request.headers.set("X-CSRF-Token", csrfToken);
    }
    return request;
  },
};

const refreshOn401: Middleware = {
  async onResponse({ request, response }) {
    if (response.status !== 401) return response;
    const url = new URL(request.url);
    // The auth endpoints answer 401 on their own terms; never loop on them.
    if (
      url.pathname.startsWith("/api/auth/login") ||
      url.pathname.startsWith("/api/auth/refresh")
    ) {
      return response;
    }
    const refreshed = await refreshSession();
    if (!refreshed) {
      onSessionLost?.();
      return response;
    }
    const retry = request.clone();
    if (csrfToken && retry.method !== "GET" && retry.method !== "HEAD") {
      retry.headers.set("X-CSRF-Token", csrfToken);
    }
    return fetch(retry);
  },
};

// A csrf_rejected after a full page load means memory lost the token; ADR
// 0003's recovery path exists for exactly this. Recover it from the refresh
// cookie once and retry, rather than telling the user to reload.
const recoverOnCsrfRejected: Middleware = {
  async onResponse({ request, response }) {
    if (response.status !== 403) return response;
    const url = new URL(request.url);
    if (url.pathname.startsWith("/api/auth/")) return response;
    let code: string | undefined;
    try {
      code = ((await response.clone().json()) as ErrorResponse).error.code;
    } catch {
      return response;
    }
    if (code !== "csrf_rejected") return response;
    if (!(await recoverCsrfToken()) || !csrfToken) return response;
    const retry = request.clone();
    retry.headers.set("X-CSRF-Token", csrfToken);
    return fetch(retry);
  },
};

api.use(csrf);
api.use(refreshOn401);
api.use(recoverOnCsrfRejected);

/**
 * Rotates the Token family once, serialized across tabs with a Web Lock:
 * reuse of a consumed refresh token revokes the whole session (ADR 0003), so
 * two tabs must never refresh at once.
 */
export function refreshSession(): Promise<boolean> {
  if (refreshInFlight) return refreshInFlight;
  refreshInFlight = withLock("vyn:refresh", async () => {
    const response = await fetch("/api/auth/refresh", {
      method: "POST",
      credentials: "same-origin",
    });
    if (!response.ok) return false;
    const body =
      (await response.json()) as components["schemas"]["SessionGrant"];
    csrfToken = body.csrf_token;
    return true;
  }).finally(() => {
    refreshInFlight = null;
  });
  return refreshInFlight;
}

/** Recovers the CSRF token after a reload, from the refresh cookie alone. */
export async function recoverCsrfToken(): Promise<boolean> {
  const response = await fetch("/api/auth/refresh/csrf", {
    credentials: "same-origin",
    cache: "no-store",
  });
  if (!response.ok) return false;
  const body = (await response.json()) as components["schemas"]["CsrfToken"];
  csrfToken = body.csrf_token;
  return true;
}

/** Runs fn under an exclusive Web Lock, or directly where locks are absent. */
export async function withLock<T>(
  name: string,
  fn: () => Promise<T>,
): Promise<T> {
  if (typeof navigator !== "undefined" && navigator.locks) {
    return navigator.locks.request(name, { mode: "exclusive" }, fn);
  }
  return fn();
}

/** The typed error code inside a failed response, or null. */
export function errorCode(error: unknown): ErrorCode | null {
  const envelope = error as Partial<ErrorResponse> | undefined;
  return envelope?.error?.code ?? null;
}

/** The human message inside a failed response, with a fallback. */
export function errorMessage(
  error: unknown,
  fallback = "Something went wrong.",
): string {
  const envelope = error as Partial<ErrorResponse> | undefined;
  return envelope?.error?.message ?? fallback;
}

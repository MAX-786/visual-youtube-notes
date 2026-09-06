// The service worker owns the tokens, the refresh lock and Pairing (ADR
// 0013). It is ephemeral: nothing lives in memory that is not also in
// chrome.storage.local, and every refresh re-reads the stored token inside
// a navigator.locks lock, because a reused refresh token revokes the
// session with no grace window (ADR 0003).
import {
  base64url,
  type PairResponse,
  type Request,
  type StatusResponse,
  type SubmitResponse,
  type UndoResponse,
} from "./lib/messages.js";
import { videoIdFromUrl } from "./lib/video-id.js";

declare const __APP_ORIGIN__: string;
const APP_ORIGIN = __APP_ORIGIN__;
const API = `${APP_ORIGIN}/api`;

type Stored = {
  accessToken: string;
  refreshToken: string;
  accessExpiresAt: string;
  displayName: string;
};
type Pending = {
  requestId: string;
  verifier: string;
  approvalUrl: string;
  expiresAt: string;
};

async function stored(): Promise<Stored | null> {
  const { tokens } = await chrome.storage.local.get("tokens");
  return (tokens as Stored | undefined) ?? null;
}
async function store(tokens: Stored | null): Promise<void> {
  if (tokens) await chrome.storage.local.set({ tokens });
  else await chrome.storage.local.remove("tokens");
}
async function pending(): Promise<Pending | null> {
  const { pairing } = await chrome.storage.local.get("pairing");
  const p = (pairing as Pending | undefined) ?? null;
  if (p && new Date(p.expiresAt).getTime() < Date.now()) {
    await chrome.storage.local.remove("pairing");
    return null;
  }
  return p;
}

type ApiResult<T> = {
  status: number;
  body: T | null;
  error?: { code: string; message: string };
};

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
  token?: string,
): Promise<ApiResult<T>> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers.Authorization = `Bearer ${token}`;
  let response: globalThis.Response;
  try {
    response = await fetch(`${API}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    return {
      status: 0,
      body: null,
      error: { code: "offline", message: "The server could not be reached." },
    };
  }
  const text = await response.text();
  let parsed: unknown;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    parsed = null;
  }
  if (!response.ok) {
    const envelope = parsed as {
      error?: { code: string; message: string };
    } | null;
    return {
      status: response.status,
      body: null,
      error: envelope?.error ?? {
        code: "rejected",
        message: response.statusText,
      },
    };
  }
  return { status: response.status, body: parsed as T };
}

// Every refresh runs under the lock and re-reads the token inside it, so
// two concurrent callers rotate once and share the result.
async function refresh(): Promise<Stored | null> {
  return navigator.locks.request("vyn:extension-refresh", async () => {
    const current = await stored();
    if (!current) return null;
    const res = await call<{
      access_token: string;
      refresh_token: string;
      access_expires_at: string;
      session: { display_name: string };
    }>("POST", "/pairing/refresh", { refresh_token: current.refreshToken });
    if (!res.body) {
      if (res.status === 401 || res.status === 403) await store(null);
      return null;
    }
    const next: Stored = {
      accessToken: res.body.access_token,
      refreshToken: res.body.refresh_token,
      accessExpiresAt: res.body.access_expires_at,
      displayName: res.body.session.display_name,
    };
    await store(next);
    return next;
  });
}

// A call with the access token; one refresh on 401, then unpaired.
async function authed<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<ApiResult<T> | "unpaired"> {
  let tokens = await stored();
  if (!tokens) return "unpaired";
  if (new Date(tokens.accessExpiresAt).getTime() - Date.now() < 30_000) {
    tokens = await refresh();
    if (!tokens) return "unpaired";
  }
  let res = await call<T>(method, path, body, tokens.accessToken);
  if (res.status === 401) {
    tokens = await refresh();
    if (!tokens) return "unpaired";
    res = await call<T>(method, path, body, tokens.accessToken);
    if (res.status === 401) {
      await store(null);
      return "unpaired";
    }
  }
  return res;
}

function browserName(): string {
  const brands =
    (
      navigator as Navigator & {
        userAgentData?: { brands?: { brand: string }[] };
      }
    ).userAgentData?.brands ?? [];
  const named = brands
    .map((b) => b.brand)
    .find(
      (b) =>
        /Edge|Brave|Chrome|Chromium|Opera|Vivaldi/i.test(b) && !/Not/i.test(b),
    );
  if ((navigator as Navigator & { brave?: unknown }).brave) return "Brave";
  if (named) return named.replace(/^Microsoft /, "");
  return "Chromium browser";
}

// Pairing: mint the verifier, register its challenge, open the approval
// page, then poll the exchange endpoint every two seconds until decided.
async function startPairing(): Promise<PairResponse> {
  const existing = await pending();
  if (existing) {
    await chrome.tabs.create({ url: existing.approvalUrl });
    return { ok: true, approvalUrl: existing.approvalUrl };
  }
  const raw = crypto.getRandomValues(new Uint8Array(32));
  const verifier = base64url(raw);
  const challenge = base64url(await crypto.subtle.digest("SHA-256", raw));
  const res = await call<{
    request_id: string;
    expires_at: string;
    approval_url: string;
  }>("POST", "/pairing/requests", {
    challenge,
    extension_id: chrome.runtime.id,
    display_name: browserName(),
  });
  if (!res.body) {
    return {
      ok: false,
      detail: res.error?.message ?? "Pairing could not start.",
    };
  }
  const p: Pending = {
    requestId: res.body.request_id,
    verifier,
    approvalUrl: res.body.approval_url,
    expiresAt: res.body.expires_at,
  };
  await chrome.storage.local.set({ pairing: p });
  await chrome.tabs.create({ url: p.approvalUrl });
  void pollExchange();
  return { ok: true, approvalUrl: p.approvalUrl };
}

async function pollExchange(): Promise<void> {
  for (;;) {
    const p = await pending();
    if (!p) return;
    const res = await call<{
      access_token: string;
      refresh_token: string;
      access_expires_at: string;
      session: { display_name: string };
    }>("POST", "/pairing/exchange", {
      request_id: p.requestId,
      verifier: p.verifier,
    });
    if (res.body) {
      await store({
        accessToken: res.body.access_token,
        refreshToken: res.body.refresh_token,
        accessExpiresAt: res.body.access_expires_at,
        displayName: res.body.session.display_name,
      });
      await chrome.storage.local.remove("pairing");
      return;
    }
    if (res.error?.code !== "pairing_not_approved" && res.status !== 0) {
      await chrome.storage.local.remove("pairing");
      return;
    }
    await new Promise((r) => setTimeout(r, 2000));
  }
}

// The video ID comes from sender.url, never from the claim alone (ADR 0013).
async function submit(
  claimed: string,
  sender: chrome.runtime.MessageSender,
): Promise<SubmitResponse> {
  const derived = sender.url ? videoIdFromUrl(sender.url) : null;
  if (!derived || derived !== claimed) {
    return { ok: false, reason: "video_id_mismatch" };
  }
  const res = await authed<{
    outcome: "created" | "duplicate" | "held";
    note: { id: string };
    undo_until?: string | null;
  }>("POST", "/submissions", {
    url: `https://www.youtube.com/watch?v=${derived}`,
  });
  if (res === "unpaired") return { ok: false, reason: "unpaired" };
  if (!res.body) {
    if (res.status === 0)
      return { ok: false, reason: "offline", detail: res.error?.message };
    return { ok: false, reason: "rejected", detail: res.error?.message };
  }
  const noteUrl = `${APP_ORIGIN}/notes/${res.body.note.id}`;
  switch (res.body.outcome) {
    case "created":
      return {
        ok: true,
        outcome: "created",
        submissionId: res.body.note.id,
        noteUrl,
        undoUntil:
          res.body.undo_until ?? new Date(Date.now() + 5000).toISOString(),
      };
    case "duplicate":
      return { ok: true, outcome: "duplicate", noteUrl };
    default:
      return { ok: true, outcome: "held", noteUrl };
  }
}

async function undo(noteId: string): Promise<UndoResponse> {
  const res = await authed<{ result: "undone" | "trashed" | "too_late" }>(
    "DELETE",
    `/submissions/${noteId}`,
  );
  if (res === "unpaired") return { ok: false, detail: "Not paired." };
  if (!res.body) return { ok: false, detail: res.error?.message };
  return { ok: true, result: res.body.result };
}

async function status(): Promise<StatusResponse> {
  const tokens = await stored();
  if (tokens) return { paired: true, displayName: tokens.displayName };
  const p = await pending();
  return {
    paired: false,
    pairing: p
      ? { approvalUrl: p.approvalUrl, expiresAt: p.expiresAt }
      : undefined,
  };
}

// Sign-out revokes on the server first and clears locally afterwards, so
// server and local state cannot drift.
async function signOut(): Promise<{ ok: true }> {
  const tokens = await stored();
  if (tokens)
    await call("POST", "/pairing/logout", undefined, tokens.accessToken);
  await store(null);
  await chrome.storage.local.remove("pairing");
  return { ok: true };
}

chrome.runtime.onMessage.addListener(
  (message: Request, sender, sendResponse) => {
    const reply = (p: Promise<unknown>) => {
      p.then(sendResponse, (err) =>
        sendResponse({ ok: false, reason: "rejected", detail: String(err) }),
      );
    };
    switch (message.type) {
      case "submit":
        reply(submit(message.videoId, sender));
        return true;
      case "undo":
        reply(undo(message.submissionId));
        return true;
      case "status":
        reply(status());
        return true;
      case "pair":
        reply(startPairing());
        return true;
      case "sign-out":
        reply(signOut());
        return true;
      default:
        return false;
    }
  },
);

// A pending Pairing survives the worker being unloaded; resume polling.
chrome.runtime.onStartup.addListener(() => void pollExchange());
void pollExchange();

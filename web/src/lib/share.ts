// The share target's URL selection and holding pen (spec 5.2,
// docs/design/pwa-shell.md "Share target"). Each shared field is scanned
// for every YouTube link in field order; a share that cannot be submitted
// now waits in pending_submissions and drains on reconnect or sign-in.
import { api } from "./api";
import { db, putNoteCard } from "./db";
import { session } from "./session.svelte";

const HOSTS = new Set([
  "youtube.com",
  "www.youtube.com",
  "m.youtube.com",
  "youtu.be",
]);
const ID = /^[A-Za-z0-9_-]{11}$/;

/** The video ID of one YouTube URL, or null. A `list` is ignored when `v` is present. */
export function videoIdOf(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (!HOSTS.has(url.hostname.toLowerCase())) return null;
  let id: string | null;
  if (url.hostname === "youtu.be") {
    id = url.pathname.split("/")[1] ?? null;
  } else if (url.pathname === "/watch") {
    id = url.searchParams.get("v");
  } else {
    id = url.pathname.match(/^\/(?:shorts|live|embed)\/([^/?#]+)/)?.[1] ?? null;
  }
  return id && ID.test(id) ? id : null;
}

/** Every distinct video ID across the shared fields, url then text then title. */
export function videoIdsFromShare(fields: {
  url?: string | null;
  text?: string | null;
  title?: string | null;
}): string[] {
  const found: string[] = [];
  for (const field of [fields.url, fields.text, fields.title]) {
    if (!field) continue;
    for (const match of field.matchAll(
      /(?:https?:\/\/)?(?:www\.|m\.)?(?:youtube\.com|youtu\.be)\/[^\s<>"']*/gi,
    )) {
      const candidate = match[0].startsWith("http")
        ? match[0]
        : `https://${match[0]}`;
      const id = videoIdOf(candidate);
      if (id && !found.includes(id)) found.push(id);
    }
  }
  return found;
}

export function watchUrl(videoId: string): string {
  return `https://www.youtube.com/watch?v=${videoId}`;
}

export type ShareOutcome =
  | { kind: "created" | "duplicate" | "held"; noteId: string }
  | { kind: "queued" }
  | { kind: "rejected"; message: string };

/** Submits one URL now, or leaves it in the holding pen when the network is away. */
export async function submitOrQueue(url: string): Promise<ShareOutcome> {
  const database = await db();
  const submission_id = crypto.randomUUID();
  await database.put("pending_submissions", {
    submission_id,
    url,
    created_at: new Date().toISOString(),
  });
  // Without a live session the link only waits: calling the server would
  // answer 401 and tear the shell down around the entry.
  if (session.status !== "signed-in") return { kind: "queued" };
  const result = await trySubmit(url);
  if (result.kind === "queued") return result;
  await database.delete("pending_submissions", submission_id);
  return result;
}

async function trySubmit(url: string): Promise<ShareOutcome> {
  let response: Response;
  let data:
    | { outcome: "created" | "duplicate" | "held"; note: { id: string } }
    | undefined;
  let error: { error?: { message?: string } } | undefined;
  try {
    ({ data, error, response } = await api.POST("/submissions", {
      body: { url },
    }));
  } catch {
    return { kind: "queued" };
  }
  if (data) {
    await putNoteCard(data.note as never);
    return { kind: data.outcome, noteId: data.note.id };
  }
  if (
    response.status === 0 ||
    response.status >= 500 ||
    response.status === 429 ||
    response.status === 401
  ) {
    return { kind: "queued" };
  }
  return {
    kind: "rejected",
    message: error?.error?.message ?? "That link could not be saved.",
  };
}

let draining = false;

/** Drains the holding pen: after sign-in, on reconnect, and at start. */
export async function drainShares(): Promise<number> {
  if (draining || typeof window === "undefined") return 0;
  draining = true;
  let submitted = 0;
  try {
    const database = await db();
    for (const pending of await database.getAll("pending_submissions")) {
      const result = await trySubmit(pending.url);
      if (result.kind === "queued") break;
      await database.delete("pending_submissions", pending.submission_id);
      if (result.kind !== "rejected") submitted++;
    }
  } finally {
    draining = false;
  }
  return submitted;
}

export async function pendingShareCount(): Promise<number> {
  return (await db()).count("pending_submissions");
}

// The write-through queue (ADR 0012, mutation-protocol.md): every edit is
// applied locally, stored in IndexedDB, and drained strictly serially per
// Note under a Web Lock so one tab at a time talks to the server. The
// decisions live in queue-core.ts; this file is the plumbing.
import { SvelteMap, SvelteSet } from "svelte/reactivity";
import { api, withLock } from "./api";
import { db, type QueuedMutation } from "./db";
import { drainNote, forNote, nowIso, type Sent } from "./queue-core";
import { drainShares } from "./share";
import { uuidv7 } from "./uuid";
import { session } from "./session.svelte";

export type Conflict = { revision_id: string; active_revision_id: string };

export const queue = $state({
  pending: 0,
  draining: false,
  /** Notes whose queue produced a Conflict Revision, by Note id. */
  conflicts: new SvelteMap<string, Conflict>(),
  /** Notes whose entries were dropped, with the reason, for the page to explain. */
  dropped: new SvelteMap<string, string>(),
  /** Takeaway writes refused as stale, with the server's text. */
  staleTakeaways: new SvelteMap<
    string,
    { server_text: string; server_cursor: number }
  >(),
});

type Listener = (noteId: string, body: unknown) => void;
const listeners = new SvelteSet<Listener>();

/** Called after any entry for a Note lands (applied or conflicted). */
export function onQueueResult(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

async function refreshPending(): Promise<void> {
  queue.pending = await (await db()).count("mutation_queue");
}

async function enqueue(
  entry: Omit<QueuedMutation, "seq" | "created_at" | "mutation_id"> & {
    mutation_id?: string;
  },
): Promise<string> {
  const database = await db();
  const seq = Date.now() * 1000 + Math.floor(Math.random() * 1000);
  const mutation_id = entry.mutation_id ?? uuidv7();
  await database.put("mutation_queue", {
    ...entry,
    mutation_id,
    created_at: nowIso(),
    seq,
  });
  await refreshPending();
  void drainAll();
  return mutation_id;
}

/** Queues an operation set against the base the client last saw. */
export function enqueueMutation(
  noteId: string,
  baseRevisionId: string,
  ops: unknown[],
): Promise<string> {
  const mutation_id = uuidv7();
  return enqueue({
    mutation_id,
    note_id: noteId,
    kind: "mutation",
    base_revision_id: baseRevisionId,
    body: { mutation_id, ops },
  });
}

export function enqueueTakeaway(
  noteId: string,
  text: string,
  baseCursor: number,
): Promise<string> {
  const mutation_id = uuidv7();
  return enqueue({
    mutation_id,
    note_id: noteId,
    kind: "takeaway",
    body: { mutation_id, text, base_cursor: baseCursor },
  });
}

export function enqueueTopic(
  noteId: string,
  body: { topic_id?: string; name?: string },
): Promise<string> {
  const mutation_id = uuidv7();
  return enqueue({
    mutation_id,
    note_id: noteId,
    kind: "topic",
    body: { mutation_id, ...body },
  });
}

export function enqueueTags(noteId: string, tags: string[]): Promise<string> {
  const mutation_id = uuidv7();
  return enqueue({
    mutation_id,
    note_id: noteId,
    kind: "tags",
    body: { mutation_id, tags },
  });
}

/** Positions coalesce to the latest per Note and go out after the queue. */
export async function enqueueLayout(
  noteId: string,
  positions: Record<string, { x: number; y: number; w: number; h: number }>,
): Promise<void> {
  const database = await db();
  const existing = await database.get("position_queue", noteId);
  const merged = {
    ...((existing?.positions as Record<string, unknown>) ?? {}),
    ...positions,
  };
  await database.put("position_queue", {
    note_id: noteId,
    positions: merged,
    updated_at: nowIso(),
  });
  void drainAll();
}

async function send(entry: QueuedMutation): Promise<Sent> {
  const path = { params: { path: { noteId: entry.note_id } } };
  let response: Response;
  let data: unknown;
  let error: unknown;
  try {
    switch (entry.kind) {
      case "mutation": {
        const body = entry.body as { mutation_id: string; ops: never[] };
        ({ data, error, response } = await api.POST(
          "/notes/{noteId}/mutations",
          {
            ...path,
            body: {
              mutation_id: body.mutation_id,
              base_revision_id: entry.base_revision_id ?? "",
              ops: body.ops,
            },
          },
        ));
        break;
      }
      case "takeaway":
        ({ data, error, response } = await api.PUT("/notes/{noteId}/takeaway", {
          ...path,
          body: entry.body as {
            mutation_id: string;
            text: string;
            base_cursor: number;
          },
        }));
        break;
      case "topic":
        ({ data, error, response } = await api.PUT("/notes/{noteId}/topic", {
          ...path,
          body: entry.body as {
            mutation_id: string;
            topic_id?: string;
            name?: string;
          },
        }));
        break;
      case "tags":
        ({ data, error, response } = await api.PUT("/notes/{noteId}/tags", {
          ...path,
          body: entry.body as { mutation_id: string; tags: string[] },
        }));
        break;
    }
  } catch {
    return { status: 0, body: null };
  }
  const retryAfter = response.headers.get("Retry-After");
  return {
    status: response.status,
    body: data ?? error,
    retryAfterSeconds: retryAfter ? Number(retryAfter) : null,
  };
}

async function drainPositions(): Promise<void> {
  const database = await db();
  for (const row of await database.getAll("position_queue")) {
    const { response } = await api.PATCH("/notes/{noteId}/layout", {
      params: { path: { noteId: row.note_id } },
      body: {
        positions: row.positions as Record<
          string,
          { x: number; y: number; w: number; h: number }
        >,
      },
    });
    if (
      response.ok ||
      (response.status >= 400 &&
        response.status < 500 &&
        response.status !== 429)
    ) {
      await database.delete("position_queue", row.note_id);
    }
  }
}

let draining = false;
let again = false;

async function drainOnce(): Promise<void> {
  const database = await db();
  const entries = await database.getAll("mutation_queue");
  const noteIds = [...new Set(entries.map((e) => e.note_id))];
  for (const noteId of noteIds) {
    await drainNote(forNote(entries, noteId), {
      send,
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      remove: async (e) => {
        await database.delete("mutation_queue", e.mutation_id);
        await refreshPending();
      },
      update: (e) => database.put("mutation_queue", e).then(() => undefined),
      onDone: (e, body) => {
        queue.conflicts.delete(e.note_id);
        for (const l of listeners) l(e.note_id, body);
      },
      onConflict: (e, verdict) => {
        queue.conflicts.set(e.note_id, {
          revision_id: verdict.nextBase,
          active_revision_id: verdict.activeRevisionId,
        });
        for (const l of listeners) l(e.note_id, verdict.body);
      },
      onDrop: (e, verdict) => {
        queue.dropped.set(e.note_id, verdict.reason);
        const details = (
          verdict.body as { error?: { details?: Record<string, unknown> } }
        )?.error?.details;
        if (verdict.reason === "takeaway_stale" && details) {
          queue.staleTakeaways.set(e.note_id, {
            server_text: String(details.server_text ?? ""),
            server_cursor: Number(details.server_cursor ?? 0),
          });
        }
        for (const l of listeners) l(e.note_id, verdict.body);
      },
      alive: () => session.status === "signed-in",
    });
  }
  await drainPositions();
  await drainShares();
}

/**
 * Drains every Note's entries, one Note at a time. An entry queued while a
 * drain is running is picked up by one more pass, never left behind.
 */
export async function drainAll(): Promise<void> {
  if (typeof window === "undefined") return;
  if (draining) {
    again = true;
    return;
  }
  if (session.status !== "signed-in") return;
  draining = true;
  queue.draining = true;
  try {
    do {
      again = false;
      await withLock("vyn:drain", drainOnce);
    } while (again && session.status === "signed-in");
  } finally {
    draining = false;
    queue.draining = false;
    await refreshPending();
  }
}

/** Starts draining whatever survived a reload and again whenever the browser comes back online. */
export function startQueue(): void {
  if (typeof window === "undefined") return;
  void refreshPending();
  void drainAll();
  window.addEventListener("online", () => void drainAll());
}

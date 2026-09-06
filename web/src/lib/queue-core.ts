// The write-through queue's decisions, kept free of Svelte and IndexedDB so
// they can be tested as functions (docs/design/mutation-protocol.md, "Client
// queue"): what one response means for the entry that produced it, and how
// the next entry is rebased.
import type { QueuedMutation } from "./db";

export type Sent = {
  status: number;
  body: unknown;
  retryAfterSeconds?: number | null;
};

export type Verdict =
  /** The server answered; the entry is done. */
  | { kind: "done"; body: unknown; nextBase?: string }
  /** The base was stale; a Conflict Revision was written and becomes the next base. */
  | {
      kind: "conflict";
      body: unknown;
      nextBase: string;
      activeRevisionId: string;
    }
  /** Try the same entry again later. */
  | { kind: "retry"; afterMs: number }
  /** The entry can never succeed; drop it (and, for a purged Note, the rest). */
  | { kind: "drop"; reason: string; wholeNote: boolean; body: unknown };

const DEFAULT_RETRY_MS = 5000;

/** The current time as an ISO string, for queue rows. */
export function nowIso(): string {
  return new Date().toISOString();
}

type ErrorBody = { error?: { code?: string } };
type MutationBody = {
  outcome?: string;
  revision_id?: string;
  active_revision_id?: string;
};

/** Classifies one response for one queue entry. */
export function classify(entry: QueuedMutation, sent: Sent): Verdict {
  const { status, body } = sent;
  if (status >= 200 && status < 300) {
    if (entry.kind === "mutation") {
      const result = body as MutationBody;
      if (result.outcome === "conflicted" && result.revision_id) {
        return {
          kind: "conflict",
          body,
          nextBase: result.revision_id,
          activeRevisionId: result.active_revision_id ?? result.revision_id,
        };
      }
      return { kind: "done", body, nextBase: result.revision_id };
    }
    return { kind: "done", body };
  }
  if (status === 429) {
    const seconds = sent.retryAfterSeconds ?? DEFAULT_RETRY_MS / 1000;
    return { kind: "retry", afterMs: Math.max(1, seconds) * 1000 };
  }
  if (status === 0 || status >= 500) {
    return { kind: "retry", afterMs: DEFAULT_RETRY_MS };
  }
  if (status === 401) {
    // The session is being refreshed or is gone; the drainer waits.
    return { kind: "retry", afterMs: DEFAULT_RETRY_MS };
  }
  const code = (body as ErrorBody)?.error?.code ?? "rejected";
  if (code === "note_purged") {
    return { kind: "drop", reason: code, wholeNote: true, body };
  }
  // validation_failed, mutation_expired, note_trashed, takeaway_stale and the
  // rest: the operations cannot become valid by waiting.
  return { kind: "drop", reason: code, wholeNote: false, body };
}

/**
 * Rebases the next Revision-bearing entry of the same Note onto the
 * Revision the last response produced -- manual or Conflict alike, so an
 * offline chain lands as one branch.
 */
export function rebase(
  entries: QueuedMutation[],
  noteId: string,
  nextBase: string | undefined,
): QueuedMutation[] {
  if (!nextBase) return entries;
  let done = false;
  return entries.map((e) => {
    if (done || e.note_id !== noteId || e.kind !== "mutation") return e;
    done = true;
    return { ...e, base_revision_id: nextBase };
  });
}

/** Entries of one Note in queue order. */
export function forNote(
  entries: QueuedMutation[],
  noteId: string,
): QueuedMutation[] {
  return entries
    .filter((e) => e.note_id === noteId)
    .sort((a, b) => a.seq - b.seq);
}

export interface DrainHooks {
  send: (entry: QueuedMutation) => Promise<Sent>;
  sleep: (ms: number) => Promise<void>;
  /** The entry is done or dropped; remove it from storage. */
  remove: (entry: QueuedMutation) => Promise<void>;
  /** Persist a rebased entry. */
  update: (entry: QueuedMutation) => Promise<void>;
  onDone?: (entry: QueuedMutation, body: unknown) => void;
  onConflict?: (
    entry: QueuedMutation,
    verdict: Extract<Verdict, { kind: "conflict" }>,
  ) => void;
  onDrop?: (
    entry: QueuedMutation,
    verdict: Extract<Verdict, { kind: "drop" }>,
  ) => void;
  /** Stops the drain (sign-out, page hidden). */
  alive?: () => boolean;
}

/**
 * Drains one Note's entries strictly serially: the head is sent until it
 * is done or dropped, the next entry is rebased onto what came back, and a
 * 429 waits for Retry-After and resends the same entry.
 */
export async function drainNote(
  entries: QueuedMutation[],
  hooks: DrainHooks,
): Promise<QueuedMutation[]> {
  let pending = [...entries].sort((a, b) => a.seq - b.seq);
  while (pending.length > 0 && (hooks.alive?.() ?? true)) {
    const head = pending[0];
    const verdict = classify(head, await hooks.send(head));
    if (verdict.kind === "retry") {
      await hooks.sleep(verdict.afterMs);
      continue;
    }
    pending = pending.slice(1);
    await hooks.remove(head);
    if (verdict.kind === "drop") {
      hooks.onDrop?.(head, verdict);
      if (verdict.wholeNote) {
        for (const rest of pending) await hooks.remove(rest);
        return [];
      }
      continue;
    }
    const nextBase =
      verdict.kind === "conflict" ? verdict.nextBase : verdict.nextBase;
    if (verdict.kind === "conflict") hooks.onConflict?.(head, verdict);
    else hooks.onDone?.(head, verdict.body);
    if (head.kind === "mutation" && nextBase) {
      const before = pending;
      pending = rebase(pending, head.note_id, nextBase);
      for (let i = 0; i < pending.length; i++) {
        if (pending[i] !== before[i]) await hooks.update(pending[i]);
      }
    }
  }
  return pending;
}

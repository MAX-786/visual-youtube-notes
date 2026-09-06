// IndexedDB, the only place user data is cached (ADR 0012). One database,
// the seven stores of docs/design/pwa-shell.md. A resync clears the display
// stores and never the queues; sign-out clears everything.
import { openDB, type DBSchema, type IDBPDatabase } from "idb";
import type { components } from "@visual-notes/api-client";

export type NoteCard = components["schemas"]["NoteCard"];
export type Note = components["schemas"]["Note"];
export type Topic = components["schemas"]["Topic"];

export interface CachedNote {
  note_id: string;
  card: NoteCard;
  /** The full Note once it has been opened. */
  detail?: Note;
  saved_at: string;
  opened_at?: string;
}

export interface QueuedMutation {
  mutation_id: string;
  note_id: string;
  kind: "mutation" | "takeaway" | "topic" | "tags";
  base_revision_id?: string;
  body: unknown;
  created_at: string;
  seq: number;
}

export interface PendingSubmission {
  submission_id: string;
  url: string;
  created_at: string;
}

interface VynDB extends DBSchema {
  notes: { key: string; value: CachedNote; indexes: { saved_at: string } };
  transcripts: {
    key: string;
    value: { note_id: string; transcript: unknown; opened_at: string };
  };
  topics: { key: string; value: Topic };
  mutation_queue: {
    key: string;
    value: QueuedMutation;
    indexes: { seq: number };
  };
  position_queue: {
    key: string;
    value: { note_id: string; positions: unknown; updated_at: string };
  };
  pending_submissions: { key: string; value: PendingSubmission };
  meta: { key: string; value: unknown };
}

let handle: Promise<IDBPDatabase<VynDB>> | null = null;

export function db(): Promise<IDBPDatabase<VynDB>> {
  if (!handle) {
    handle = openDB<VynDB>("vyn", 1, {
      upgrade(database) {
        const notes = database.createObjectStore("notes", {
          keyPath: "note_id",
        });
        notes.createIndex("saved_at", "saved_at");
        database.createObjectStore("transcripts", { keyPath: "note_id" });
        database.createObjectStore("topics", { keyPath: "id" });
        const queue = database.createObjectStore("mutation_queue", {
          keyPath: "mutation_id",
        });
        queue.createIndex("seq", "seq");
        database.createObjectStore("position_queue", { keyPath: "note_id" });
        database.createObjectStore("pending_submissions", {
          keyPath: "submission_id",
        });
        database.createObjectStore("meta");
      },
    });
  }
  return handle;
}

export async function getMeta<T>(key: string): Promise<T | undefined> {
  return (await (await db()).get("meta", key)) as T | undefined;
}

export async function setMeta(key: string, value: unknown): Promise<void> {
  await (await db()).put("meta", value, key);
}

/** The last Sync cursor this device applied; 0 before the first sign-in. */
export async function getCursor(): Promise<number> {
  return (await getMeta<number>("cursor")) ?? 0;
}

export async function setCursor(cursor: number): Promise<void> {
  await setMeta("cursor", cursor);
}

export async function putNoteCard(card: NoteCard): Promise<void> {
  const database = await db();
  const existing = await database.get("notes", card.id);
  await database.put("notes", {
    note_id: card.id,
    card,
    detail: existing?.detail,
    saved_at: card.saved_at,
    opened_at: existing?.opened_at,
  });
}

export async function putNoteDetail(note: Note): Promise<void> {
  const database = await db();
  const existing = await database.get("notes", note.id);
  await database.put("notes", {
    note_id: note.id,
    card: note,
    detail: note,
    saved_at: note.saved_at,
    opened_at: new Date().toISOString(),
  });
  if (!existing) return;
}

export async function getNote(noteId: string): Promise<CachedNote | undefined> {
  return (await db()).get("notes", noteId);
}

export async function deleteNote(noteId: string): Promise<void> {
  const database = await db();
  await database.delete("notes", noteId);
  await database.delete("transcripts", noteId);
}

export async function listCachedNotes(): Promise<CachedNote[]> {
  const rows = await (await db()).getAllFromIndex("notes", "saved_at");
  return rows.reverse();
}

/**
 * Spec 9 retention: keep a Note while its Save time is within seven days or
 * it was opened within thirty; otherwise drop it and its Transcript.
 */
export async function prune(now = new Date()): Promise<number> {
  const database = await db();
  const sevenDays = now.getTime() - 7 * 24 * 3600 * 1000;
  const thirtyDays = now.getTime() - 30 * 24 * 3600 * 1000;
  let removed = 0;
  for (const row of await database.getAll("notes")) {
    const saved = Date.parse(row.saved_at);
    const opened = row.opened_at ? Date.parse(row.opened_at) : 0;
    if (saved >= sevenDays || opened >= thirtyDays) continue;
    await database.delete("notes", row.note_id);
    await database.delete("transcripts", row.note_id);
    removed++;
  }
  return removed;
}

/** A forced resync rebuilds the display stores and never touches the queues. */
export async function clearForResync(): Promise<void> {
  const database = await db();
  await Promise.all([
    database.clear("notes"),
    database.clear("transcripts"),
    database.clear("topics"),
    database.delete("meta", "cursor"),
  ]);
}

/**
 * Sign-out clears everything, queues included (spec 9). A session that is
 * merely gone keeps the share holding pen: a link shared while signed out
 * must survive the sign-in it triggers (spec 5.2).
 */
export async function clearAll(
  options: { keepPendingSubmissions?: boolean } = {},
): Promise<void> {
  const database = await db();
  const stores = [
    "notes",
    "transcripts",
    "topics",
    "mutation_queue",
    "position_queue",
    "meta",
  ] as const;
  await Promise.all(stores.map((store) => database.clear(store)));
  if (!options.keepPendingSubmissions) {
    await database.clear("pending_submissions");
  }
}

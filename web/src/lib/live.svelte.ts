// The live-update client (ADR 0012). One EventSource per browser profile,
// held by whichever tab wins the Web Lock; every frame is rebroadcast to the
// other tabs. After three consecutive connect failures the leader polls
// GET /events/since and re-attempts the stream every five minutes.
import { SvelteSet } from "svelte/reactivity";
import type { components } from "@visual-notes/api-client";
import { api, withLock } from "./api";
import { CHANNEL, session, signOut } from "./session.svelte";
import { clearForResync, getCursor, setCursor } from "./db";

export type SyncEvent = components["schemas"]["SyncEvent"];
export type ProgressEvent = components["schemas"]["ProgressEvent"];

type SyncListener = (event: SyncEvent) => void;
type ProgressListener = (event: ProgressEvent) => void;
type ResyncListener = () => void;

const syncListeners = new SvelteSet<SyncListener>();
const progressListeners = new SvelteSet<ProgressListener>();
const resyncListeners = new SvelteSet<ResyncListener>();

export const live = $state({
  mode: "idle" as "idle" | "stream" | "polling",
  leader: false,
  /** Notes known to be in flight, so the poller refetches their status. */
  inFlight: new SvelteSet<string>(),
});

export function onSync(listener: SyncListener): () => void {
  syncListeners.add(listener);
  return () => syncListeners.delete(listener);
}
export function onProgress(listener: ProgressListener): () => void {
  progressListeners.add(listener);
  return () => progressListeners.delete(listener);
}
export function onResync(listener: ResyncListener): () => void {
  resyncListeners.add(listener);
  return () => resyncListeners.delete(listener);
}

let channel: BroadcastChannel | null = null;
let started = false;
let stop: (() => void) | null = null;

const STREAM_FAILURES_BEFORE_POLLING = 3;
const STREAM_RETRY_MS = 5 * 60 * 1000;
const POLL_BUSY_MS = 10 * 1000;
const POLL_IDLE_MS = 60 * 1000;

/** Starts the client for a signed-in session. Idempotent per tab. */
export function startLive(): void {
  if (started || typeof window === "undefined") return;
  started = true;
  if ("BroadcastChannel" in window) {
    channel = new BroadcastChannel(CHANNEL);
    channel.addEventListener("message", (event) => {
      const message = event.data;
      if (message?.type === "sync")
        dispatchSync(message.event as SyncEvent, false);
      if (message?.type === "progress")
        dispatchProgress(message.event as ProgressEvent);
      if (message?.type === "resync") dispatchResync(false);
    });
  }
  void withLock("vyn:sse-leader", async () => {
    live.leader = true;
    await new Promise<void>((resolve) => {
      stop = resolve;
      void lead(() => stop === null);
    });
    live.leader = false;
  });
}

/** Stops the client, releasing leadership. */
export function stopLive(): void {
  started = false;
  const release = stop;
  stop = null;
  release?.();
  live.mode = "idle";
}

async function lead(alive: () => boolean): Promise<void> {
  let failures = 0;
  while (alive() && session.status === "signed-in") {
    const outcome = await streamOnce();
    if (!alive()) return;
    if (outcome === "revoked") {
      await signOut({ local: true, broadcast: true });
      return;
    }
    if (outcome === "resync") {
      await handleResync();
      continue;
    }
    failures++;
    if (failures >= STREAM_FAILURES_BEFORE_POLLING) {
      await pollUntil(Date.now() + STREAM_RETRY_MS, alive);
      failures = 0;
    } else {
      await sleep(1000 * failures);
    }
  }
}

function streamOnce(): Promise<"closed" | "revoked" | "resync"> {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (outcome: "closed" | "revoked" | "resync") => {
      if (settled) return;
      settled = true;
      source.close();
      live.mode = "idle";
      resolve(outcome);
    };
    void getCursor().then((cursor) => {
      if (settled) return;
      // The browser resumes with Last-Event-ID on its own reconnects; the
      // cursor parameter covers the cold start.
      source.close();
      startSource(cursor);
    });
    let source = new EventSource("/api/events");
    source.close();
    const startSource = (cursor: number) => {
      source = new EventSource(
        cursor > 0 ? `/api/events?cursor=${cursor}` : "/api/events",
      );
      source.addEventListener("open", () => {
        live.mode = "stream";
      });
      source.addEventListener("sync", (event) => {
        const frame = JSON.parse((event as MessageEvent).data) as SyncEvent;
        void setCursor(frame.cursor);
        dispatchSync(frame, true);
      });
      source.addEventListener("progress", (event) => {
        const frame = JSON.parse((event as MessageEvent).data) as ProgressEvent;
        channel?.postMessage({ type: "progress", event: frame });
        dispatchProgress(frame);
      });
      source.addEventListener("resync", () => finish("resync"));
      source.addEventListener("revoked", () => finish("revoked"));
      source.addEventListener("error", () => {
        // EventSource reconnects on its own; a closed source means the
        // connection failed outright.
        if (source.readyState === EventSource.CLOSED) finish("closed");
      });
    };
  });
}

async function pollUntil(
  deadline: number,
  alive: () => boolean,
): Promise<void> {
  live.mode = "polling";
  while (alive() && Date.now() < deadline && session.status === "signed-in") {
    const cursor = await getCursor();
    const { data, response } = await api.GET("/events/since", {
      params: { query: { cursor } },
    });
    if (response.status === 401) {
      await signOut({ local: true, broadcast: true });
      return;
    }
    if (data) {
      if (data.resync_required) {
        await handleResync();
      } else {
        for (const event of data.events) dispatchSync(event, true);
        await setCursor(data.next_cursor);
      }
    }
    await sleep(live.inFlight.size > 0 ? POLL_BUSY_MS : POLL_IDLE_MS);
  }
  live.mode = "idle";
}

async function handleResync(): Promise<void> {
  await clearForResync();
  channel?.postMessage({ type: "resync" });
  dispatchResync(true);
}

function dispatchSync(event: SyncEvent, rebroadcast: boolean): void {
  if (rebroadcast) channel?.postMessage({ type: "sync", event });
  for (const listener of syncListeners) listener(event);
}

function dispatchProgress(event: ProgressEvent): void {
  if (
    event.processing_state === "ready" ||
    event.processing_state === "failed" ||
    event.processing_state === "cancelled"
  ) {
    live.inFlight.delete(event.note_id);
  } else {
    live.inFlight.add(event.note_id);
  }
  for (const listener of progressListeners) listener(event);
}

function dispatchResync(rebroadcast: boolean): void {
  if (rebroadcast) channel?.postMessage({ type: "resync" });
  for (const listener of resyncListeners) listener();
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

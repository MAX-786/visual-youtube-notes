import { describe, expect, it } from "vitest";
import type { QueuedMutation } from "./db";
import { classify, drainNote, rebase, type Sent } from "./queue-core";

function entry(seq: number, note = "n1", base = "r3"): QueuedMutation {
  return {
    mutation_id: `m${seq}`,
    note_id: note,
    kind: "mutation",
    base_revision_id: base,
    body: { ops: [] },
    created_at: new Date().toISOString(),
    seq,
  };
}

function hooks(
  responses: Sent[] | ((entry: QueuedMutation, attempt: number) => Sent),
) {
  const sent: { id: string; base?: string }[] = [];
  const slept: number[] = [];
  const removed: string[] = [];
  const updated: QueuedMutation[] = [];
  const conflicts: string[] = [];
  const drops: string[] = [];
  let calls = 0;
  return {
    sent,
    slept,
    removed,
    updated,
    conflicts,
    drops,
    hooks: {
      send: async (e: QueuedMutation) => {
        sent.push({ id: e.mutation_id, base: e.base_revision_id });
        const r =
          typeof responses === "function"
            ? responses(e, calls)
            : responses[Math.min(calls, responses.length - 1)];
        calls++;
        return r;
      },
      sleep: async (ms: number) => {
        slept.push(ms);
      },
      remove: async (e: QueuedMutation) => {
        removed.push(e.mutation_id);
      },
      update: async (e: QueuedMutation) => {
        updated.push(e);
      },
      onConflict: (e: QueuedMutation) => {
        conflicts.push(e.mutation_id);
      },
      onDrop: (e: QueuedMutation) => {
        drops.push(e.mutation_id);
      },
    },
  };
}

describe("classify", () => {
  it("treats a conflicted outcome as the next base", () => {
    const v = classify(entry(1), {
      status: 200,
      body: {
        outcome: "conflicted",
        revision_id: "r5",
        active_revision_id: "r4",
      },
    });
    expect(v).toEqual({
      kind: "conflict",
      body: expect.anything(),
      nextBase: "r5",
      activeRevisionId: "r4",
    });
  });
  it("retries a 429 after Retry-After", () => {
    expect(
      classify(entry(1), { status: 429, body: {}, retryAfterSeconds: 7 }),
    ).toEqual({
      kind: "retry",
      afterMs: 7000,
    });
  });
  it("drops the whole queue of a purged Note", () => {
    const v = classify(entry(1), {
      status: 404,
      body: { error: { code: "note_purged" } },
    });
    expect(v.kind).toBe("drop");
    expect((v as { wholeNote: boolean }).wholeNote).toBe(true);
  });
  it("drops a validation failure but keeps the rest", () => {
    const v = classify(entry(1), {
      status: 422,
      body: { error: { code: "validation_failed" } },
    });
    expect(v).toMatchObject({
      kind: "drop",
      reason: "validation_failed",
      wholeNote: false,
    });
  });
});

describe("drainNote", () => {
  it("pauses on a 429 for Retry-After and resends the same entry", async () => {
    const h = hooks([
      { status: 429, body: {}, retryAfterSeconds: 3 },
      {
        status: 200,
        body: {
          outcome: "applied",
          revision_id: "r4",
          active_revision_id: "r4",
        },
      },
    ]);
    const left = await drainNote([entry(1)], h.hooks);
    expect(h.sent.map((s) => s.id)).toEqual(["m1", "m1"]);
    expect(h.slept).toEqual([3000]);
    expect(h.removed).toEqual(["m1"]);
    expect(left).toEqual([]);
  });

  it("rebases an offline chain onto each Conflict Revision so it lands as one branch", async () => {
    let n = 4;
    const h = hooks(() => ({
      status: 200,
      body: {
        outcome: "conflicted",
        revision_id: `r${++n}`,
        active_revision_id: "r4",
      },
    }));
    const queue = [entry(1), entry(2), entry(3), entry(4), entry(5)];
    await drainNote(queue, h.hooks);
    expect(h.sent.map((s) => s.base)).toEqual(["r3", "r5", "r6", "r7", "r8"]);
    expect(h.conflicts).toEqual(["m1", "m2", "m3", "m4", "m5"]);
    expect(
      h.updated.map((u) => `${u.mutation_id}@${u.base_revision_id}`),
    ).toEqual(["m2@r5", "m3@r6", "m4@r7", "m5@r8"]);
  });

  it("rebases onto a manual Revision after an applied entry", async () => {
    const h = hooks([
      {
        status: 200,
        body: {
          outcome: "applied",
          revision_id: "r4",
          active_revision_id: "r4",
        },
      },
      {
        status: 200,
        body: {
          outcome: "applied",
          revision_id: "r5",
          active_revision_id: "r5",
        },
      },
    ]);
    await drainNote([entry(1), entry(2)], h.hooks);
    expect(h.sent.map((s) => s.base)).toEqual(["r3", "r4"]);
  });

  it("drops everything for a purged Note", async () => {
    const h = hooks([
      { status: 404, body: { error: { code: "note_purged" } } },
    ]);
    const left = await drainNote([entry(1), entry(2), entry(3)], h.hooks);
    expect(left).toEqual([]);
    expect(h.removed).toEqual(["m1", "m2", "m3"]);
    expect(h.sent).toHaveLength(1);
  });

  it("drops only the failing entry on validation_failed", async () => {
    const h = hooks([
      { status: 422, body: { error: { code: "validation_failed" } } },
      {
        status: 200,
        body: {
          outcome: "applied",
          revision_id: "r4",
          active_revision_id: "r4",
        },
      },
    ]);
    await drainNote([entry(1), entry(2)], h.hooks);
    expect(h.drops).toEqual(["m1"]);
    expect(h.sent.map((s) => s.id)).toEqual(["m1", "m2"]);
  });

  it("leaves other Notes' entries alone", () => {
    const rebased = rebase(
      [entry(1, "n2"), entry(2, "n1"), entry(3, "n1")],
      "n1",
      "r9",
    );
    expect(rebased.map((e) => e.base_revision_id)).toEqual(["r3", "r9", "r3"]);
  });
});

// Shared Note fixtures for the page specs: the four valid Diagram examples
// served as a ready Note, plus a mock API that answers the shell's calls.
import type { Page } from "@playwright/test";
import comparison from "../../../../docs/design/diagram-examples/valid/comparison.json" with { type: "json" };
import mindMap from "../../../../docs/design/diagram-examples/valid/mind-map.json" with { type: "json" };
import processFlow from "../../../../docs/design/diagram-examples/valid/process-flow.json" with { type: "json" };
import timeline from "../../../../docs/design/diagram-examples/valid/timeline.json" with { type: "json" };

type Example = Record<string, unknown> & {
  type: string;
  nodes?: { id: string }[];
};

const examples: Record<string, Example> = {
  "mind-map": mindMap as Example,
  "process-flow": processFlow as Example,
  timeline: timeline as Example,
  comparison: comparison as Example,
};

export function example(name: string): { diagram: Example } {
  return { diagram: examples[name] };
}

export type Positions = Record<
  string,
  { x: number; y: number; w: number; h: number }
>;

/** Boxes on a grid: the page draws what it is given, it never lays out. */
export function grid(nodes: { id: string }[] = []): Positions {
  const out: Positions = {};
  nodes.forEach((node, i) => {
    out[node.id] = {
      x: (i % 3) * 220,
      y: Math.floor(i / 3) * 120,
      w: 180,
      h: 60,
    };
  });
  return out;
}

export const account = {
  id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  username: "owner",
  display_name: "The Owner",
  timezone: "Asia/Calcutta",
  created_at: "2026-09-06T10:00:00Z",
  password_changed_at: "2026-09-06T10:00:00Z",
};
export const device = {
  id: "0d57fed9-8642-4f48-b1fb-8fb65147cca0",
  kind: "pwa",
  display_name: "Chrome",
  extension_id: null,
  created_at: "2026-09-06T10:00:00Z",
  last_seen_at: "2026-09-06T10:00:00Z",
  last_refreshed_at: "2026-09-06T10:00:00Z",
  inactive_expires_at: "2026-10-06T10:00:00Z",
  is_current: true,
  push_subscriptions: 0,
};

export const noteId = "2f5a1d7e-6c4b-4f0e-9a3c-1b2c3d4e5f60";

export function block(id: string, text: string) {
  return {
    id,
    text,
    provenance: "ai",
    speaker_claim: false,
    source_refs: [
      {
        chunk_id: "a3c250c0-47ea-535e-8c29-ed9f3487a082",
        char_start: 0,
        char_end: 5,
      },
    ],
  };
}

export function noteFor(
  diagram: Record<string, unknown>,
  positions: Positions,
) {
  const stage = (name: string) => ({
    stage: name,
    status: name === "transcribing" ? "skipped" : "ready",
    attempts: 1,
    started_at: "2026-09-07T01:00:00Z",
    finished_at: "2026-09-07T01:00:10Z",
    last_error: null,
  });
  return {
    id: noteId,
    source: {
      video_id: "aircAruvnKk",
      canonical_url: "https://www.youtube.com/watch?v=aircAruvnKk",
      title: "Spaced recall, explained",
      channel_id: "UCx",
      channel_name: "Learning Lab",
      duration_seconds: 1120,
      published_at: "2017-10-05T00:00:00Z",
      metadata_fetched_at: "2026-09-07T01:00:00Z",
    },
    saved_at: "2026-09-07T01:00:00Z",
    processing: {
      version: 1,
      state: "ready",
      failure_stage: null,
      failure_kind: null,
      cancel_requested: false,
      cost_hold: null,
      stages: [
        "metadata",
        "captions",
        "transcribing",
        "generating",
        "indexing",
      ].map(stage),
    },
    primary_topic: {
      id: "1c1c1c1c-1c1c-4c1c-8c1c-1c1c1c1c1c1c",
      name: "Learning",
    },
    tags: [{ id: "2d2d2d2d-2d2d-4d2d-8d2d-2d2d2d2d2d2d", name: "memory" }],
    summary_text: "Spacing reviews out beats cramming.",
    detected_language: "en",
    transcript_source: "caption_human",
    active_revision_id: "3e3e3e3e-3e3e-4e3e-8e3e-3e3e3e3e3e3e",
    awaiting_decisions: 0,
    trashed_at: null,
    purge_after: null,
    created_at: "2026-09-07T01:00:00Z",
    updated_at: "2026-09-07T01:00:10Z",
    active_revision: {
      id: "3e3e3e3e-3e3e-4e3e-8e3e-3e3e3e3e3e3e",
      revision_number: 1,
      origin: "generated",
      parent_revision_id: null,
      generation_instruction: null,
      awaiting_decision_since: null,
      is_active: true,
      origin_device_session_id: null,
      created_at: "2026-09-07T01:00:10Z",
      payload: {
        schema_version: 1,
        detected_language: "en",
        primary_topic: "Learning",
        tags: ["memory"],
        summary: block("sum", "Spacing reviews out beats cramming."),
        key_points: [
          block("kp1", "Recall after a day, then a week."),
          block("kp2", "Effort is the point."),
        ],
        important_details: [
          block("d1", "The first review comes one day later."),
        ],
        practical_actions: [
          block("a1", "Schedule the second review a week out."),
        ],
        diagram,
      },
    },
    pending_revisions: [],
    takeaway: { text: "", updated_cursor: 0, updated_at: null },
    layout: { positions, updated_at: "2026-09-07T01:00:10Z" },
    cost: {
      estimated_usd: "0.03",
      reported_usd: "0.03",
      incomplete: false,
      calls: [],
    },
    roles_used: [
      { role: "note_generation", provider: "openai", model: "gpt-5.6-terra" },
    ],
  };
}

export async function mockApi(
  page: Page,
  note: unknown,
  options: { transcript?: unknown; onTranscript?: () => void } = {},
) {
  const json = (body: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/auth/session") {
      return route.fulfill(
        json({
          session: device,
          account,
          access_expires_at: "2026-09-06T10:15:00Z",
          sync_cursor: 0,
        }),
      );
    }
    if (p === `/api/notes/${noteId}`) return route.fulfill(json(note));
    if (p === `/api/notes/${noteId}/transcript` && options.transcript) {
      options.onTranscript?.();
      return route.fulfill(json(options.transcript));
    }
    if (p === "/api/events/since")
      return route.fulfill(
        json({ events: [], next_cursor: 0, resync_required: false }),
      );
    if (p === "/api/events") return route.abort();
    return route.fulfill(
      json({ error: { code: "not_found", message: "no" } }, 404),
    );
  });
}

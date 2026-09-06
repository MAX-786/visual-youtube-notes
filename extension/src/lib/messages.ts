// The one-shot message protocol between the content script, the popup and
// the service worker (docs/design/extension.md). The content script is a
// renderer: it never holds a credential.

export type Request =
  | { type: "submit"; videoId: string } // a claim, checked against sender.url
  | { type: "undo"; submissionId: string }
  | { type: "status" }
  | { type: "pair" }
  | { type: "sign-out" };

export type SubmitResponse =
  | {
      ok: true;
      outcome: "created";
      submissionId: string;
      noteUrl: string;
      undoUntil: string;
    }
  | { ok: true; outcome: "duplicate"; noteUrl: string }
  | { ok: true; outcome: "held"; noteUrl: string }
  | {
      ok: false;
      reason: "unpaired" | "video_id_mismatch" | "offline" | "rejected";
      detail?: string;
    };

export type UndoResponse =
  | { ok: true; result: "undone" | "trashed" | "too_late" }
  | { ok: false; detail?: string };

export type StatusResponse = {
  paired: boolean;
  displayName?: string;
  pairing?: { approvalUrl: string; expiresAt: string };
};

export type PairResponse =
  { ok: true; approvalUrl: string } | { ok: false; detail: string };

export type Response =
  SubmitResponse | UndoResponse | StatusResponse | PairResponse | { ok: true };

/** Base64url without padding, the form the contract's Base64Url256 takes. */
export function base64url(bytes: ArrayBuffer | Uint8Array): string {
  const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let s = "";
  for (const b of view) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

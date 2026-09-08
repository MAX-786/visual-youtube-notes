// The in-page button: a renderer with no credentials (ADR 0013). It
// re-resolves the video ID on every YouTube navigation and hands the
// service worker a claim the worker checks against sender.url.
import { resolveAnchor } from "./lib/anchor.js";
import type { Request, SubmitResponse, UndoResponse } from "./lib/messages.js";
import { videoIdFromUrl } from "./lib/video-id.js";

const BUTTON_ID = "vyn-save-button";
const TOAST_ID = "vyn-toast";

function send<T>(message: Request): Promise<T> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(message, (response: T) => {
      if (chrome.runtime.lastError)
        reject(new Error(chrome.runtime.lastError.message));
      else resolve(response);
    });
  });
}

function ensureButton(): void {
  const videoId = videoIdFromUrl(location.href);
  const existing = document.getElementById(BUTTON_ID);
  if (!videoId) {
    existing?.remove();
    return;
  }
  const anchor = resolveAnchor();
  if (!anchor) return;
  if (
    existing &&
    existing.parentElement === anchor.host &&
    existing.dataset.videoId === videoId
  )
    return;
  existing?.remove();
  const button = document.createElement("button");
  button.id = BUTTON_ID;
  button.type = "button";
  button.className =
    anchor.kind === "actions"
      ? "vyn-button vyn-button-inline"
      : "vyn-button vyn-button-overlay";
  button.dataset.videoId = videoId;
  button.textContent = "Save to Visual Notes";
  button.addEventListener("click", () => void save(button));
  anchor.host.appendChild(button);
}

async function save(button: HTMLButtonElement): Promise<void> {
  // Re-read at click time: the page may have navigated since attachment.
  const videoId = videoIdFromUrl(location.href);
  if (!videoId) return;
  button.disabled = true;
  try {
    const response = await send<SubmitResponse>({ type: "submit", videoId });
    show(response);
  } catch (err) {
    show({ ok: false, reason: "rejected", detail: String(err) });
  } finally {
    button.disabled = false;
  }
}

let countdown: ReturnType<typeof setInterval> | null = null;

function show(response: SubmitResponse): void {
  document.getElementById(TOAST_ID)?.remove();
  if (countdown) clearInterval(countdown);
  const toast = document.createElement("div");
  toast.id = TOAST_ID;
  toast.className = "vyn-toast";
  toast.setAttribute("role", "status");
  const text = document.createElement("span");
  toast.appendChild(text);
  let dismissAfter = 6000;
  if (response.ok) {
    switch (response.outcome) {
      case "created": {
        text.textContent = "Added to Visual Notes";
        const undo = document.createElement("button");
        undo.type = "button";
        undo.className = "vyn-toast-action";
        const endsAt = new Date(response.undoUntil).getTime();
        const label = () =>
          `Undo (${Math.max(0, Math.ceil((endsAt - Date.now()) / 1000))})`;
        undo.textContent = label();
        countdown = setInterval(() => {
          if (Date.now() >= endsAt) {
            undo.remove();
            if (countdown) clearInterval(countdown);
          } else undo.textContent = label();
        }, 250);
        undo.addEventListener("click", () => {
          void send<UndoResponse>({
            type: "undo",
            submissionId: response.submissionId,
          }).then((r) => {
            text.textContent =
              r.ok && r.result === "undone"
                ? "Removed"
                : r.ok && r.result === "trashed"
                  ? "Moved to Trash"
                  : "Too late to undo";
            undo.remove();
          });
        });
        toast.appendChild(undo);
        dismissAfter = 8000;
        break;
      }
      case "duplicate":
        text.textContent = "Already saved. ";
        toast.appendChild(link("Open note", response.noteUrl));
        break;
      case "held":
        text.textContent = "Saved — needs your cost confirmation ";
        toast.appendChild(link("Open", response.noteUrl));
        break;
    }
  } else {
    switch (response.reason) {
      case "unpaired": {
        text.textContent = "Pair this browser ";
        const pair = document.createElement("button");
        pair.type = "button";
        pair.className = "vyn-toast-action";
        pair.textContent = "Pair";
        pair.addEventListener("click", () => void send({ type: "pair" }));
        toast.appendChild(pair);
        dismissAfter = 15000;
        break;
      }
      case "video_id_mismatch":
        text.textContent = "Page changed — try again";
        break;
      default:
        text.textContent =
          response.detail ??
          (response.reason === "offline"
            ? "You are offline"
            : "The server refused this video");
    }
  }
  document.body.appendChild(toast);
  setTimeout(() => toast.remove(), dismissAfter);
}

function link(label: string, href: string): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "vyn-toast-action";
  a.href = href;
  a.target = "_blank";
  a.rel = "noreferrer noopener";
  a.textContent = label;
  return a;
}

// YouTube never reloads between videos: attach on its navigation event,
// with a MutationObserver as the backstop when the actions row renders late.
document.addEventListener("yt-navigate-finish", () => ensureButton());
const observer = new MutationObserver(() => ensureButton());
observer.observe(document.documentElement, { childList: true, subtree: true });
ensureButton();

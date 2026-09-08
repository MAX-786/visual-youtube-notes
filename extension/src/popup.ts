// The popup shows pairing status and offers pairing or sign-out. It holds no
// credentials; every answer comes from the service worker.
import type { PairResponse, Request, StatusResponse } from "./lib/messages.js";

function send<T>(message: Request): Promise<T> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(message, (response: T) => {
      if (chrome.runtime.lastError)
        reject(new Error(chrome.runtime.lastError.message));
      else resolve(response);
    });
  });
}

const statusEl = document.getElementById("status") as HTMLParagraphElement;
const pairButton = document.getElementById("pair") as HTMLButtonElement;
const signOutButton = document.getElementById("sign-out") as HTMLButtonElement;

async function render(): Promise<void> {
  try {
    const s = await send<StatusResponse>({ type: "status" });
    if (s.paired) {
      statusEl.textContent = `Connected as “${s.displayName ?? "this browser"}”. Use Save to Visual Notes on any YouTube video.`;
      pairButton.hidden = true;
      signOutButton.hidden = false;
    } else if (s.pairing) {
      statusEl.textContent =
        "Waiting for approval. Complete pairing in the Visual Notes tab.";
      pairButton.textContent = "Open approval again";
      pairButton.hidden = false;
      signOutButton.hidden = true;
    } else {
      statusEl.textContent =
        "Connect this browser before saving your first video.";
      pairButton.textContent = "Pair this browser";
      pairButton.hidden = false;
      signOutButton.hidden = true;
    }
  } catch {
    statusEl.textContent =
      "The extension could not start. Reload it from Chrome and try again.";
    pairButton.hidden = true;
    signOutButton.hidden = true;
  }
}

pairButton.addEventListener("click", async () => {
  pairButton.disabled = true;
  pairButton.setAttribute("aria-busy", "true");
  try {
    const response = await send<PairResponse>({ type: "pair" });
    if (!response.ok) statusEl.textContent = response.detail;
    else await render();
  } catch {
    statusEl.textContent = "Pairing could not start. Check your connection.";
  } finally {
    pairButton.disabled = false;
    pairButton.removeAttribute("aria-busy");
  }
});
signOutButton.addEventListener("click", async () => {
  signOutButton.disabled = true;
  signOutButton.setAttribute("aria-busy", "true");
  try {
    await send({ type: "sign-out" });
    await render();
  } catch {
    statusEl.textContent = "This browser could not be unpaired. Try again.";
  } finally {
    signOutButton.disabled = false;
    signOutButton.removeAttribute("aria-busy");
  }
});

void render();

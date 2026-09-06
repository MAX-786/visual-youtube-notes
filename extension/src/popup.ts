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
  const s = await send<StatusResponse>({ type: "status" });
  if (s.paired) {
    statusEl.textContent = `Paired as “${s.displayName ?? "this browser"}”. Open a YouTube video and press Save to Visual Notes.`;
    pairButton.hidden = true;
    signOutButton.hidden = false;
  } else if (s.pairing) {
    statusEl.textContent =
      "Waiting for you to approve this browser in Visual Notes.";
    pairButton.textContent = "Open the approval page again";
    pairButton.hidden = false;
    signOutButton.hidden = true;
  } else {
    statusEl.textContent = "This browser is not paired yet.";
    pairButton.textContent = "Pair this browser";
    pairButton.hidden = false;
    signOutButton.hidden = true;
  }
}

pairButton.addEventListener("click", () => {
  void send<PairResponse>({ type: "pair" }).then((r) => {
    if (!r.ok) statusEl.textContent = r.detail;
    else void render();
  });
});
signOutButton.addEventListener("click", () => {
  void send({ type: "sign-out" }).then(() => render());
});

void render();

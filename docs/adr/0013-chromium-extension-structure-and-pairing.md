# Keep the token in the service worker and pin the extension ID with a committed key

> **Partially superseded by [ADR 0018](0018-chrome-web-store-owns-production-extension-id.md).**
> The service-worker and pairing boundaries remain current. The Chrome Web Store now owns the
> production ID; the committed manifest-key and unpacked-only distribution decision below is
> retained as historical context.

The Chromium extension is submit-only. A declared content script renders the in-page action on
YouTube and holds no credential; the Manifest V3 service worker owns every network call, every
token, and the refresh lock. The extension's identity is pinned by a `key` committed to the
manifest, so one allowlisted origin works across Chrome, Edge and Brave on every machine.

Ticket [#24](https://github.com/MAX-786/visual-youtube-notes/issues/24). Mechanical detail — the
manifest draft, message protocol, and flows — is in [`extension.md`](../design/extension.md).

## The content script is a renderer, not a client

[ADR 0003](0003-owner-auth-device-sessions-and-extension-pairing.md) says the extension "requests
host permission only for `https://yt-track.mkhismkh.com/*`". That is right about what it was
deciding — where bearer credentials may travel — and incomplete about what the product requires.
Spec 5.1 step 2 and spec 16 both demand an **in-page** "Create visual notes" action, and a button
that is already on the page needs a declared content script matched on YouTube. `activeTab` cannot
produce one: it grants access only _after_ a toolbar click, which is the other action.

So host access widens, and the invariant is preserved a different way: **the content script never
holds a token and never calls the API.** It renders the button, and messages the service worker,
which owns the credential. A hostile or compromised YouTube page therefore reads nothing worth
having — which is the property ADR 0003's sentence was protecting, stated in terms that survive the
in-page requirement. This ADR refines ADR 0003 rather than rewriting it, per ADR 0001's supersession
rule.

Matches cover `/watch` and `/shorts` on `www.youtube.com` and `m.youtube.com`. Shorts are eligible
under spec 8.1, so a watch-only match would silently drop half the capture surface; matching all of
`youtube.com` would take the whole site to reach two paths.

## The service worker derives the video ID, and does not take one on trust

The content script reads the video ID from `location`, which the page controls through the History
API. If the service worker accepted that value, a page could make the extension submit a video the
owner never chose — spending real AI budget under the owner's session.

The service worker therefore derives the ID from `sender.url`, which the browser populates on the
message and no script can set. It is available without the `tabs` permission, so no ambient tab
access is requested to get it.

The content script's parsed ID still travels in the message, and a mismatch **refuses the
submission** rather than picking a winner. This is deliberately belt-and-braces: the guard costs
nothing, and it converts an unverified assumption — that `sender.url` tracks History API navigation
promptly on a SPA like YouTube — from a silent wrong-video submission into a visible refusal. That
behaviour is on the browser checklist rather than asserted here, because it cannot be established
from the desk.

## One key in the manifest, three browsers, one origin

ADR 0003 allowlists exactly one `chrome-extension://<id>` origin, and
`device_sessions.extension_id` is constrained to `^[a-p]{32}$`. An unpacked extension's ID is
derived from its install path, so it differs per browser and per machine — the allowlist would break
on the second browser it was loaded into.

Committing the manifest's `key` — the base64 **public** key, which is not a secret — makes Chromium
derive the same ID everywhere. Three browsers then pair as three Device sessions sharing one
`extension_id`, which the schema already permits. The derived ID is
`fjodmlmkfifedoamachdbgigfpaldicm`, and
[`extension-manifest.test.mjs`](../design/extension-manifest.test.mjs) re-derives it and asserts it
against the migration's constraint, because a key that produces an ID the column cannot store would
otherwise surface for the first time at pairing.

Distribution is an unpacked `dist/` loaded in developer mode, documented per browser. Publishing to
the Web Store is rejected: it is an outward-facing review process for a single-user tool, and buys
nothing that a committed `key` does not already give.

The **private** half is retained, not discarded. It is only needed to pack a CRX, which this
decision avoids — but regenerating it changes the extension ID, and that invalidates the server's
allowlisted origin and every paired Device session at once. It lives at
`deploy/secrets/extension_signing_key.pem`, gitignored, on the developer machine only; it is never
copied to the host and is not a Compose secret.

## Approval is discovered by polling the endpoint that already exists

ADR 0003's pairing flow has the extension create a request, open the PWA approval page, and then
exchange its verifier — but nothing in it tells the extension _when_ the owner approved.

The extension polls the **exchange endpoint itself**, which ADR 0003 already permits under CORS. It
answers `pairing_not_approved` until approval and returns the token family on success, so learning
about approval needs no new endpoint and no new CORS entry.

`externally_connectable` is rejected. Letting the PWA page message the extension would mean
embedding the extension ID in the web app and opening an inbound message surface into the extension,
to solve a problem that polling solves in a few lines for the request's five-minute life.

## Refresh is serialized, because reuse has no grace window

ADR 0003 revokes the entire Device session on refresh reuse, with no grace window. The Manifest V3
service worker is torn down when idle and restarted per event, so two YouTube tabs clicking at once
could issue two refreshes with the same token and the extension would revoke itself.

Every refresh runs under a `navigator.locks` lock inside the service worker, re-reading the stored
token _inside_ the lock — the discipline ADR 0003 already gives PWA tabs. Because all network calls
route through the service worker, there is exactly one refresher by construction rather than by
convention.

Tokens live in `chrome.storage.local`. Never `storage.sync`, per spec 11: syncing would replicate
the token family to every machine on the Google account, which is precisely the blast radius Device
sessions exist to bound.

Any `401` that survives one refresh attempt clears local storage and drops to the unpaired state. No
silent retry loop, and no cached token outliving the revocation that spec 16 requires to take
effect.

## Submit-only, and honest about what the server said

The extension has no live channel. Processing status belongs to the PWA (spec 5.1 step 6) and
terminal notifications to Web Push. A second live-update client inside an ephemeral service worker
would duplicate the cursor logic of [ADR 0012](0012-pwa-shell-live-updates-and-offline-cache.md) and
keep the worker alive for no product gain.

That makes the submission response the extension's only source of truth, so it renders exactly what
came back and nothing more:

- `created` — "Added", with the five-second Undo.
- `duplicate` — "Already saved", linking to the existing Note. Never a second row (spec 8.1).
- `held` — "Saved, needs your cost confirmation", linking into the PWA, **with no Undo**. Spec 11
  holds a submission at the soft cap and never resumes it automatically; "Added" would be a lie.

Most spec 8.1 rejections — active livestream, over two hours, region-blocked — cannot be known at
submission, because they need the metadata stage. Those surface later in the PWA. The extension
reports only what the synchronous call returned.

The Undo countdown lives in the injected in-page toast, because that is where the click happened and
it survives as long as the tab does; the popup mirrors it when open. Undo is explicitly best-effort:
closing the tab inside five seconds lets the delayed job run, which is what a five-second River delay
already implies.

## Verification stops at the edge of YouTube's markup

The pure logic is unit-tested: video-ID extraction across every URL form spec 8.1 accepts, and anchor
resolution against a captured watch-page DOM fixture including the overlay fallback.

There is deliberately **no CI test against live YouTube**. Loading a real watch page in CI produces a
test that fails when YouTube ships a redesign — red for a reason unrelated to the change, which
trains people to ignore it. Browser-level confirmation across Chrome, Edge and Brave is a runbook
checklist instead, which spec 16 already requires as an acceptance criterion regardless.

## Consequences

- YouTube's DOM can change without breaking capture: the button falls back to an overlay anchored to
  the player when the actions row cannot be resolved. A fragile selector degrading to a working
  button beats one degrading to no button.
- The extension is useless without the PWA, by design — pairing, status, cost confirmation and
  failure handling all live there.
- Losing `extension_signing_key.pem` costs a new ID, a server allowlist change, and re-pairing every
  browser. It belongs in the runbook's secret inventory.
- `sender.url` behaviour under History API navigation is unverified from the desk. The mismatch guard
  makes the failure visible rather than silent, and the browser checklist settles it.

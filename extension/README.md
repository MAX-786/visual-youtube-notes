# Visual Notes extension

The Chromium extension of `docs/design/extension.md` (ADR 0013): a button on every YouTube watch and Shorts page that saves the video as a note, a service worker that holds the tokens, and a popup for pairing status. The same `dist/` loads in Chrome, Edge and Brave and reports the one pinned ID.

## Build

```bash
pnpm --filter @visual-notes/extension build
```

`build.mjs` bundles the three entry points with esbuild, copies `manifest.json`, the popup, the stylesheet and the icons into `dist/`, and writes `dist.zip`. The app origin is compiled in from the manifest's host permission, so the two cannot drift. `pnpm test` runs the vitest suites over `src/lib` (video-ID extraction and anchor resolution against a captured watch-page DOM).

## Load it

Developer mode on, then **Load unpacked** and pick `extension/dist`:

| Browser | Page                  |
| ------- | --------------------- |
| Chrome  | `chrome://extensions` |
| Edge    | `edge://extensions`   |
| Brave   | `brave://extensions`  |

Every browser must report the ID `fjodmlmkfifedoamachdbgigfpaldicm`. A different ID means the manifest lost its `key`; the server rejects any other origin.

## Pair it

Open a YouTube video, press **Save to Visual Notes**, then **Pair** on the toast (or **Pair this browser** in the popup). The extension opens the app's approval page, where the Owner approves the named browser; the extension finishes pairing on its own within a few seconds and the browser appears under Settings → Account, where it can be revoked.

## Browser checklist (spec 16)

Run per browser after any manifest change, and record the outcome on the ticket:

1. The extension ID reads `fjodmlmkfifedoamachdbgigfpaldicm`.
2. Pairing completes; Settings → Account lists the new Device session with the right browser name.
3. The in-page button appears on a `/watch` page and on a `/shorts` page.
4. Navigate watch → watch without reloading, then submit: the note created matches the video on screen.
5. Undo inside five seconds removes the note; after five seconds it does not.
6. A duplicate opens the existing note and creates no second row.
7. Revoking the session from Settings → Account drops the extension to unpaired on its next submit.

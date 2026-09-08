# Visual Notes extension

The production Manifest V3 extension for saving a YouTube watch or Shorts page to Visual Notes.
The content script renders the in-page control and holds no credentials. The service worker owns
pairing tokens and makes requests only to `https://yt-track.mkhismkh.com`.

## Build the Web Store package

```bash
pnpm --filter @visual-notes/extension build
```

The build validates the release manifest, bundles every executable file locally, and writes:

```text
extension/release/visual-notes-1.0.0.zip
```

Upload that ZIP directly. `manifest.json` is at the archive root. The release manifest deliberately
contains no `key` or `update_url`; the Chrome Web Store owns the production extension identity and
update channel.

## First Web Store upload and backend identity

The Store assigns the extension's 32-character Item ID after the first successful upload. Copy it
from the Developer Dashboard, set it in the server's `deploy/.env`, then recreate the API:

```dotenv
CHROME_EXTENSION_ID=abcdefghijklmnopqrstuvwxyzabcdef
```

```bash
docker compose up -d --force-recreate api
```

This value becomes the API's only allowed `chrome-extension://<id>` origin. Pairing cannot work from
the Store-installed extension until the deployed value matches its Item ID.

## Stable local development after upload

In the Developer Dashboard, open **Package → View public key** and copy only the base64 body. Supply
it when building an unpacked development copy:

```powershell
$env:VYN_EXTENSION_PUBLIC_KEY = "<public key from the Developer Dashboard>"
pnpm --filter @visual-notes/extension build:dev
```

Load `extension/dist-dev` from `chrome://extensions`. The development manifest receives the key only
inside this ignored output directory, so it has the Store Item ID without putting a rejected field
back into the upload package.

## Browser acceptance checklist

Run this using the Store-installed build before submitting for review:

1. Confirm its ID matches `CHROME_EXTENSION_ID` on the server.
2. Pair the browser; confirm Settings → Account lists the Device session.
3. Confirm the in-page control appears on both a `/watch` page and a `/shorts` page.
4. Navigate watch → watch without reloading, then save. The created note must match the video shown.
5. Confirm Undo works within five seconds, and a duplicate opens the existing note.
6. Revoke the session in Settings, then confirm the next submission asks to pair again.
7. Confirm the popup's **Privacy** link opens the public policy.

The complete Developer Dashboard copy and submission sequence lives in
[`STORE_SUBMISSION.md`](STORE_SUBMISSION.md).

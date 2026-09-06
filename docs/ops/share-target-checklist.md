# Android share target: the tablet checklist

Spec 5.2 and 16 make the share sheet the tablet's way in, and only the real device can say whether Chrome minted a WebAPK. Run this once on the target tablet after the first deploy, and again after any change to `web/static/manifest.webmanifest` (a WebAPK re-mints on a 1-to-30-day schedule, so a manifest change lags until then). Record the outcome on the share-target ticket; it decides whether the signed thin Trusted Web Activity fallback of spec 5.2 step 6 is required.

## Install

1. In Chrome on the tablet, open the app's origin, sign in, and use **Add to Home screen** (or the install prompt). Wait for the install to finish.
2. Open `chrome://webapks` in Chrome. The app must be listed with a package name and **Manifest URL** pointing at `/manifest.webmanifest`. If it is absent after a minute, or the entry shows no `share_target`, note it: this is the case that needs the TWA fallback.
3. Open the installed app from the home screen. It must open standalone (no browser chrome).

## Share

4. In the YouTube app, open a video, **Share → Visual Notes**. The app must appear in the share sheet; picking it opens `/share`, the note is created, and the note page opens (a duplicate opens the existing note).
5. Share a video whose link only appears in the text field (the YouTube app puts it there). The note must still be created from that link.
6. Turn on airplane mode and share again. The app must say the link was saved for later. Turn networking back on and open the app: the note must appear on Today within a few seconds.
7. Sign out in the app, then share a video. The app must ask you to sign in and, once signed in, submit the shared link on its own.

## Decide

- Steps 2, 4 and 5 pass: the WebAPK share target works; no TWA is needed. Record "WebAPK minted" with Chrome's version.
- Step 2 fails, or the app never appears in the share sheet: record "no WebAPK" and open the TWA fallback as its own ticket; the web app itself is unchanged by it.

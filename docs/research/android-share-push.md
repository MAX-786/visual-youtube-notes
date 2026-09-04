# Android share sheet (WebAPK / TWA) and Web Push for the SvelteKit PWA

Research for wayfinder ticket #7 (part of #1). Answers, in order: what an installed SvelteKit PWA needs to appear in Android's share sheet and receive YouTube URLs from the YouTube app; the minimal signed Trusted Web Activity (TWA) wrapper if the WebAPK route fails (spec 5.2 step 6, risk 17.2); and how Web Push (VAPID) is registered from the SvelteKit service worker and delivered from Go (spec 8.3 step 7). Ends with the tablet share-sheet test checklist.

Every claim is followed by the primary source it came from. Where a fact could not be verified from a primary source it is marked **UNVERIFIED**. Researched 2026-09-04.

Related spec sections: 5.2 (Save from Android), 8.3 step 7 (Web Push on ready), 9 (Offline behavior), 13 (Frontend), 17 risk 2 (Android sharing).

---

## 1. Summary of decisions this research supports

1. Use the **GET** form of `share_target` with `params: { title, text, url }`, and have the `/share` route extract the YouTube URL from `text` first: Chrome on Android never fills `url` (section 3.3). This matches spec 5.2 step 3 (`url` → `text` → `title`) in effect, but the realistic order of arrival is `text`, then `title`, never `url`.
2. The share target only exists when Chrome has minted a **WebAPK**; a home-screen shortcut carries no `SEND` intent filter (section 3.2). Onboarding must therefore verify that the tablet actually got a WebAPK (`chrome://webapks`), not just an icon.
3. The share request is a normal top-level navigation, so the SvelteKit service worker can serve the `/share` page offline and the page can queue the URL in IndexedDB (section 3.5). With `adapter-static`, GET is the only viable method; POST needs a server adapter.
4. The TWA fallback is a Bubblewrap-generated, self-signed APK sideloaded with `adb install`; it needs `/.well-known/assetlinks.json` on the PWA origin listing the signing certificate's SHA-256, and it gets its share-sheet entry from its own `ACTION_SEND` intent filter (section 4), so it does not depend on Chrome's WebAPK minting server at all.
5. Web Push: VAPID keys generated once with `webpush.GenerateVAPIDKeys()` and stored as server secrets; subscription stored by `endpoint`; `SendNotification` from Go with a `TTL`; delete the subscription on 404/410; `notificationclick` focuses an existing window or opens the note URL (section 5).

---

## 2. Terminology used below

- **WebAPK**: an APK that Chrome has a trusted "minting server" build and sign for a PWA when the site meets the install criteria; it is what gives the PWA an app-drawer entry and Android intent filters ([web.dev/learn/pwa/installation](https://web.dev/learn/pwa/installation), [web.dev/articles/webapks](https://web.dev/articles/webapks)).
- **TWA (Trusted Web Activity)**: "a new way to open your web-app content such as your Progressive Web App (PWA) from your Android app using a protocol based on Custom Tabs"; content is rendered by the user's browser full-screen, and trust between app and site "is verified using Digital Asset Links" ([developer.chrome.com/docs/android/trusted-web-activity](https://developer.chrome.com/docs/android/trusted-web-activity)).
- **Digital Asset Links (DAL)**: the `/.well-known/assetlinks.json` statement on the website that names the Android package and certificate fingerprint allowed to claim it ([developers.google.com/digital-asset-links/v1/getting-started](https://developers.google.com/digital-asset-links/v1/getting-started)).
- **VAPID**: "Voluntary Application Server Identification"; the application server signs a JWT with a P-256 key so the push service can identify it ([RFC 8292](https://www.rfc-editor.org/rfc/rfc8292)).

---

## 3. Web Share Target via WebAPK

### 3.1 Manifest `share_target` shape

Spec (W3C Web Share Target, editor's draft): `share_target` "declares this application to be a web share target, and describes how the application receives share data". Members: `action` (URL of the target), `method` (default `"GET"`), `enctype` (default `"application/x-www-form-urlencoded"`, only meaningful for POST), and `params` whose `title`, `text`, `url` members name the query/form parameters that carry the shared title, arbitrary text, and URL ([w3c.github.io/web-share-target](https://w3c.github.io/web-share-target/)).

Rules from the same spec:

- `action` is parsed relative to the manifest URL and must be within the manifest `scope` ("If action is not within scope of the manifest["scope"], return."), so it is same-origin by construction.
- `method` is `GET` or `POST`. For POST only `application/x-www-form-urlencoded` or `multipart/form-data` are allowed; `multipart/form-data` is only valid with POST. For GET, enctype is ignored.
- For GET the user agent runs the urlencoded serializer over the (name, value) entries and appends the result as the query string of `action`. Members missing from the ShareData are omitted from the query; spaces are encoded as `+` (form encoding), not `%20`.
- The spec notes that Android's share system has no dedicated URL field and that the user agent "can" detect a URL inside `text` and move it to `url`; this is permissive, not required ([w3c.github.io/web-share-target](https://w3c.github.io/web-share-target/)).

Chrome's guide repeats the enctype rule: "For GET requests, enctype defaults to 'application/x-www-form-urlencoded' and any other value is disregarded" ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target)).

**GET form** (Chrome's own example, with `method` shown although it is the default) ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target)):

```json
"share_target": {
  "action": "/share-target/",
  "method": "GET",
  "params": {
    "title": "title",
    "text": "text",
    "url": "url"
  }
}
```

Chrome then "opens a new window at the action URL" and "generates a query string using the URL-encoded values", e.g. `/share-target/?title=...&text=...&url=...` ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target); MDN shows `?name=a+shared+name&description=...&link=https%3A%2F%2Fexample.com%2F` for a renamed-params example, [MDN share_target](https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps/Manifest/Reference/share_target)).

**POST form** (spec example) ([w3c.github.io/web-share-target](https://w3c.github.io/web-share-target/)):

```json
"share_target": {
  "action": "/bookmark",
  "method": "POST",
  "enctype": "multipart/form-data",
  "params": { "url": "link" }
}
```

With POST, MDN advises the handler "should ideally respond with a 303 See Other redirect to prevent duplicate submissions" ([MDN share_target](https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps/Manifest/Reference/share_target)).

Proposed manifest fragment for Visual Notes (GET; `action` inside scope; names kept as the defaults so the TWA share-target JSON in section 4.3 can be identical):

```json
{
  "id": "/",
  "name": "Visual Notes",
  "short_name": "Visual Notes",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "icons": [
    { "src": "/icons/icon-192.png", "sizes": "192x192", "type": "image/png" },
    { "src": "/icons/icon-512.png", "sizes": "512x512", "type": "image/png" }
  ],
  "share_target": {
    "action": "/share",
    "method": "GET",
    "params": { "title": "title", "text": "text", "url": "url" }
  }
}
```

### 3.2 WebAPK minting conditions

**Chrome versions.** Web Share Target (text/title/url via GET) shipped in Chrome 71 for Android (chromestatus feature 5662315307335680, [chromestatus.com/feature/5662315307335680](https://chromestatus.com/feature/5662315307335680)); the Intent to Ship said "We are hoping to ship this in M71 (it is enabled on the WebAPK server)" and "This feature will initially be shipped on Android" ([groups.google.com chromium-dev Intent to Ship: Web Share Target](https://groups.google.com/a/chromium.org/g/chromium-dev/c/WJDfhllALb8)). Level 2 (POST, files) shipped in Chrome 76 for Android ([chromestatus.com/feature/6124071381106688](https://chromestatus.com/feature/6124071381106688)). Chrome's guide states the API needs "Chrome 76 or later for Android, or Chrome 89 or later on desktop" and that "your web app has to be installed before it will show up as a potential target for receiving shared data" ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target)). Any tablet with a current Chrome is far past these.

**Installability criteria (current).** Served over HTTPS; manifest with `short_name` or `name`; `icons` "must include a 192px and a 512px icon"; `start_url`; `display` "must be one of fullscreen, standalone, minimal-ui, or window-controls-overlay"; `prefer_related_applications` absent or `false`. The install *prompt* additionally uses engagement heuristics (the user tapped the page at least once and spent at least 30 seconds), which do not affect installing from the Chrome menu ([web.dev/articles/install-criteria](https://web.dev/articles/install-criteria)).

**Service worker no longer required for install.** Chrome "removed the requirement to have a service worker that implements the fetch() method for installation from the menu, since version 108 on mobile and 112 on Desktop"; the `beforeinstallprompt` algorithm "still requires a fetch handler currently" ([developer.chrome.com/blog/update-install-criteria](https://developer.chrome.com/blog/update-install-criteria)). Visual Notes ships a service worker anyway (spec 13), so both paths are satisfied.

**`id`.** The manifest `id` "represents the identity of the PWA to the browser"; supported since Chrome 96; when absent it is derived from `start_url`; setting it lets `start_url` and the manifest path change later without Chrome treating the site as a different app ([developer.chrome.com/blog/pwa-manifest-id](https://developer.chrome.com/blog/pwa-manifest-id)). Set it once and never change it.

**Why "Install" (WebAPK) vs a home-screen shortcut matters.** "A WebAPK is an Android package (APK) created by a trusted provider of the user's device, typically in the cloud, on a WebAPK minting server"; this is supported by Google Chrome on devices with Google Mobile Services (and Samsung Internet on Samsung devices). Browsers without a trusted minting server "create shortcuts. Google Chrome will too if the minting service is unavailable or your PWA doesn't meet installation requirements." Shortcuts have "No capability support requiring installation" ([web.dev/learn/pwa/installation](https://web.dev/learn/pwa/installation)). Being installed via an APK is what lets the app "register a set of intent filters" ([web.dev/articles/webapks](https://web.dev/articles/webapks)). The share target is literally compiled into the minted APK: the WebAPK shell's `AndroidManifest.xml` declares a `ShareActivity` alias with `<action android:name="android.intent.action.SEND" />` (and conditionally `SEND_MULTIPLE`) plus `<meta-data>` keys `org.chromium.webapk.shell_apk.shareAction`, `shareMethod`, `shareEnctype`, `shareParamTitle`, `shareParamText`, `shareParamUrl` ([chromium shell_apk/AndroidManifest.xml](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/webapk/shell_apk/AndroidManifest.xml)). Consequently a plain shortcut has no intent filter and cannot appear in the share sheet (inference from the two sources above; no doc states the negative in one sentence).

**Minting requires Google's server.** The Chromium README describes a "shell APK generated server-side"; the WebAPK manifest is generated in a pass "to be uploaded to the WebAPK Minting Server", and locally built "UnboundWebApk.apk" test APKs exist only for Chromium development ([chromium webapk/README.md](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/webapk/README.md), [shell_apk/AndroidManifest.xml](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/webapk/shell_apk/AndroidManifest.xml)). A tablet without Google Mobile Services, or Chrome failing to reach the minting server, gets a shortcut, not a WebAPK ([web.dev/learn/pwa/installation](https://web.dev/learn/pwa/installation)). That is exactly the failure mode risk 17.2 anticipates; the TWA path in section 4 bypasses it. Whether an "unbound" WebAPK fallback exists for end users: **UNVERIFIED** (no primary source found; the only documented fallback is the shortcut).

**Package name.** WebAPKs are installed under package names of the form `org.chromium.webapk.<id>` (the shell manifest templates the package as `{{{manifest_package}}}` with the `org.chromium.webapk.shell_apk` components; [shell_apk/AndroidManifest.xml](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/webapk/shell_apk/AndroidManifest.xml)). The exact hash format is **UNVERIFIED** beyond that template.

**When manifest changes reach the installed WebAPK.** "When the PWA is launched, Chrome determines the last time the local manifest was checked for changes. If the manifest hasn't been checked in the last 24 hours, Chrome will schedule a network request for the manifest." Changes to `name`, `short_name`, `icons`, `background_color`, `display`, `orientation`, `scope`, `shortcuts`, `start_url`, `theme_color` and the share target trigger a WebAPK update; if the server is unreachable Chrome "may increase the time between checks to 30 days"; `about://webapks` shows when the manifest was last checked and updated ([web.dev/articles/manifest-updates](https://web.dev/articles/manifest-updates)). Chromium's `WebappDataStorage` uses `UPDATE_INTERVAL = 1 day` and `RELAXED_UPDATE_INTERVAL = 30 days`, and `WebApkUpdateManager` schedules the re-mint as a background task applied when "the WebAPK is next launched" ([WebappDataStorage.java](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/java/src/org/chromium/chrome/browser/webapps/WebappDataStorage.java), [WebApkUpdateManager.java](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/java/src/org/chromium/chrome/browser/webapps/WebApkUpdateManager.java)). Practical rule for the tablet test: ship the final `share_target` **before** installing, or uninstall and reinstall after changing it, rather than waiting for the daily check plus background re-mint.

### 3.3 Known quirk: the URL arrives in `text`

Chrome's guide, verbatim: "On Android, the `url` field will be empty because it's not supported in Android's share system. Instead, URLs will often appear in the `text` field, or occasionally in the `title` field." ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target)).

Chromium's implementation confirms there is no URL extra at all: the WebAPK intent is turned into `new ShareData(subject, text, files)` from `Intent.EXTRA_SUBJECT`, `Intent.EXTRA_TEXT` and `Intent.EXTRA_STREAM` ([WebApkIntentDataProviderFactory.java](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/java/src/org/chromium/chrome/browser/webapps/WebApkIntentDataProviderFactory.java)), and the share-target request builder only maps title, text and files ([WebApkShareTargetUtil.java](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/java/src/org/chromium/chrome/browser/webapps/WebApkShareTargetUtil.java)). The same is true of the TWA helper library, which reads only `EXTRA_SUBJECT`, `EXTRA_TEXT` and `EXTRA_STREAM` ([android-browser-helper SharingUtils.java](https://github.com/GoogleChrome/android-browser-helper/blob/main/androidbrowserhelper/src/main/java/com/google/androidbrowserhelper/trusted/SharingUtils.java)).

What the YouTube app specifically puts in `EXTRA_TEXT` (bare URL vs "Title https://youtu.be/…") is **UNVERIFIED** from primary sources; no Chromium bug or doc names YouTube. The handler must therefore treat `text` as free text and extract the first valid YouTube video URL from it (spec 5.2 step 3 already requires this), with `title` as a secondary carrier and `url` kept for non-Android platforms.

Text vs text files: Android intent filters "don't distinguish between text selections and text files", so a target that only declares a text-file param receives a text selection "as if it's a text file" ([WebApkShareTargetUtil.java](https://chromium.googlesource.com/chromium/src/+/main/chrome/android/java/src/org/chromium/chrome/browser/webapps/WebApkShareTargetUtil.java)). Visual Notes declares only `title`/`text`/`url`, so this does not apply. `text/uri-list` handling: **UNVERIFIED**, nothing in the fetched sources.

### 3.4 The SvelteKit handler route

Two constraints decide the shape: the PWA is "static/installable" (spec 13), and Chrome's data arrives as a GET query string.

**GET with a static build (recommended).** In `load`, `url` is "An instance of URL, containing properties like the origin, hostname, pathname and searchParams" ([svelte.dev/docs/kit/load](https://svelte.dev/docs/kit/load)), but "Accessing url.searchParams during prerendering is forbidden" and a prerenderable page must give "any two users hitting it directly ... the same content" ([svelte.dev/docs/kit/page-options](https://svelte.dev/docs/kit/page-options)). `adapter-static` requires `prerender = true` in the root layout and optionally a `fallback` page for SPA routing ([svelte.dev/docs/kit/adapter-static](https://svelte.dev/docs/kit/adapter-static)). So the route is a prerendered shell that reads the query in the browser, which is also what Chrome's own GET sample does with `DOMContentLoaded` and `parsedUrl.searchParams.get(...)` ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target)):

```ts
// src/routes/share/+page.ts
export const prerender = true;
export const ssr = false; // query is read client-side only
```

```svelte
<!-- src/routes/share/+page.svelte -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { extractYouTubeUrls } from '$lib/youtube';   // Video ID extraction, spec 8.1
  import { enqueueSubmission } from '$lib/offline-queue'; // IndexedDB, spec 9

  onMount(async () => {
    const q = new URL(location.href).searchParams;
    // Android puts the URL in text (sometimes title); url is empty on Android.
    const candidates = extractYouTubeUrls([q.get('url'), q.get('text'), q.get('title')].filter(Boolean).join('\n'));
    if (candidates.length === 0) { /* show "no YouTube link found" */ return; }
    if (candidates.length > 1)  { /* ask the user to pick one, spec 5.2 step 3 */ return; }
    await enqueueSubmission(candidates[0]);              // works offline
    await goto('/today', { replaceState: true });       // avoid re-submitting on back
  });
</script>
```

**POST (only with a server adapter).** Form actions live in `+page.server.js`: `export const actions = { default: async ({ request }) => { const data = await request.formData(); ... } }`, and finish with `redirect(303, ...)` from `@sveltejs/kit` ([svelte.dev/docs/kit/form-actions](https://svelte.dev/docs/kit/form-actions)). Alternatively `+server.js` exports `GET`/`POST`/... handlers "that take a RequestEvent argument and return a Response object" ([svelte.dev/docs/kit/routing](https://svelte.dev/docs/kit/routing)). But "Pages with actions cannot be prerendered, because a server must be able to handle the action POST requests" ([svelte.dev/docs/kit/page-options](https://svelte.dev/docs/kit/page-options)), and adapter-static prerenders the whole site ([svelte.dev/docs/kit/adapter-static](https://svelte.dev/docs/kit/adapter-static)). A POST share target is therefore only an option if the frontend is served by a Node/adapter-backed SvelteKit server or the Go backend hosts the `action` URL itself. Nothing in the ticket's scope needs files, so GET is sufficient.

### 3.5 Offline: does the share target work, and how to queue

The spec launches a target by creating "a new top-level browsing context" and navigating it to the action URL (GET with query, or POST with a form body) ([w3c.github.io/web-share-target](https://w3c.github.io/web-share-target/)). A navigation to a controlled URL fires the service worker's `fetch` event like any other request ([MDN Using Service Workers](https://developer.mozilla.org/en-US/docs/Web/API/Service_Worker_API/Using_Service_Workers)), which is why Chrome's guide shows intercepting a POST share in the service worker (`event.request.method === 'POST' && url.pathname === '/bookmark'`, `await event.request.formData()`, `Response.redirect(responseUrl, 303)`) and recommends precaching the action page "for reliable offline functionality" ([developer.chrome.com/docs/capabilities/web-apis/web-share-target](https://developer.chrome.com/docs/capabilities/web-apis/web-share-target)). The Intent to Ship likewise expected apps to respond "ideally through a service worker to handle requests received while offline" ([Intent to Ship: Web Share Target](https://groups.google.com/a/chromium.org/g/chromium-dev/c/WJDfhllALb8)).

So with GET: the SW serves the precached `/share` shell offline; the page parses the query and writes the Submission to the IndexedDB queue (spec 9 "Queue edits and shared/manual URLs in IndexedDB and sync when online"). Flushing the queue:

- Background Sync: `const registration = await navigator.serviceWorker.ready; await registration.sync.register("sync-messages")`, and in the SW `self.addEventListener("sync", (event) => { if (event.tag === "sync-messages") event.waitUntil(sendOutboxMessages()); })`. Secure context only; Chromium-only (no Firefox/Safari), which is fine for an Android Chrome tablet ([MDN Background Synchronization API](https://developer.mozilla.org/en-US/docs/Web/API/Background_Synchronization_API)).
- Plus a foreground retry on app open / `online` event as the portable path, since the SW `sync` event is not guaranteed on every device.

**SvelteKit service worker facts.** Put the file at `src/service-worker.js` (or `.ts`, or `src/service-worker/index.js`); SvelteKit "automatically bundles and registers it", disableable via `kit.serviceWorker.register` for manual registration. `$service-worker` exports `build` (Vite output URLs), `files` (static dir), `version` (for cache names), `base` and `prerendered`. In dev, `build`/`prerendered` are empty and the worker is not bundled; when registering manually use `type: 'module'` in dev and `'classic'` in production. TypeScript needs `/// <reference no-default-lib="true"/>`, `/// <reference lib="esnext" />`, `/// <reference lib="webworker" />`, `/// <reference types="@sveltejs/kit" />` and `declare const self: ServiceWorkerGlobalScope`. SvelteKit only checks for SW updates on error recovery; call `registration.update()` from `afterNavigate()` for more frequent checks ([svelte.dev/docs/kit/service-workers](https://svelte.dev/docs/kit/service-workers)). The `/share` route's prerendered HTML is in `prerendered`, so include it in the install-time cache.

---

## 4. Minimal TWA wrapper (required fallback, spec 5.2 step 6)

### 4.1 What a TWA is and needs

"Trusted Web Activity is available in Chrome on Android, version 72 and above." Content is rendered by the browser full-screen; the site/app association "is verified using Digital Asset Links"; "If the user's version of Chrome doesn't support Trusted Web activities, Chrome will fall back to a simple toolbar using a Custom Tab" ([developer.chrome.com/docs/android/trusted-web-activity](https://developer.chrome.com/docs/android/trusted-web-activity)). "Trusted Web Activities require an association between the Android application and the website to be established to remove the URL bar" ([Integration guide](https://developer.chrome.com/docs/android/trusted-web-activity/integration-guide)); if verification fails, "the browser falls back to displaying your website as a Custom Tab" ([Quick start](https://developer.chrome.com/docs/android/trusted-web-activity/quick-start)). Browser support: Chrome since 72, Edge, Samsung Internet, Brave, Vivaldi, Firefox Nightly; notification delegation only in Chrome and Brave ([android-browser-helper browser-support.md](https://github.com/GoogleChrome/android-browser-helper/blob/main/docs/trusted-web-activity-browser-support.md)).

Web Share Target inside a TWA: "Starting with Chrome 86, Web Share Target is now also available to applications using Trusted Web Activity"; requires `android-browser-helper` 2.0.1+; the share-target JSON goes in `res/values/strings.xml` with the `action` as "the full Url, including the origin" and quotes escaped; the `DelegationService` must be `android:exported="true"`; an `intent-filter` with `SEND`/`SEND_MULTIPLE` and mime types is added; "Digital Asset Links validation must succeed for data transfer to work" ([developer.chrome.com/docs/android/trusted-web-activity/web-share-target](https://developer.chrome.com/docs/android/trusted-web-activity/web-share-target)).

### 4.2 What Bubblewrap generates

Requirements: Node 14.15+ and JDK 17 exactly ("Using a version lower than 17 will make it impossible to compile the project and higher versions are incompatible with the Android command line tools"); Android command-line tools (Bubblewrap offers to download JDK and SDK on first run). Install with `npm i -g @bubblewrap/cli` or use `docker run --rm -ti ghcr.io/googlechromelabs/bubblewrap:latest`; check with `bubblewrap doctor` ([Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md), [Quick start](https://developer.chrome.com/docs/android/trusted-web-activity/quick-start)).

Commands ([Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md)):

```
bubblewrap init --manifest=https://notes.example.com/manifest.webmanifest
bubblewrap build          # -> app-release-signed.apk, app-release-bundle.aab, assetlinks.json
bubblewrap install        # adb install ./app-release-signed.apk to the connected device
bubblewrap update         # regenerate the Android project from twa-manifest.json
bubblewrap fingerprint list | add <sha256> | generateAssetLinks --output=assetlinks.json
```

`init` "reads the Web Manifest, asks developers to confirm values to be used in the Android project, and generates the project using those values" ([Quick start](https://developer.chrome.com/docs/android/trusted-web-activity/quick-start)); it prompts for package id, host, launcher name, icons and the signing key path/alias, and writes `twa-manifest.json`. `build` outputs a signed APK "for testing", a signed AAB "for Play Store submission", and the DAL file; passwords come from prompts or `BUBBLEWRAP_KEYSTORE_PASSWORD` / `BUBBLEWRAP_KEY_PASSWORD`. Manual edits to the generated Android project are overwritten by `update` ([Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md)).

`twa-manifest.json` fields relevant here (from the README table and `TwaManifest.ts`): `packageId`, `host`, `startUrl`, `name`/`launcherName`, `display`, `iconUrl`/`maskableIconUrl`/`monochromeIconUrl`, `enableNotifications` (default `true`), `signingKey: { path, alias }` (defaults `./android.keystore`, `android`), `webManifestUrl`, `fallbackType: 'customtabs' | 'webview'` (default `customtabs`), `shareTarget` (typed as the spec's ShareTarget), `fingerprints`, `additionalTrustedOrigins`, `minSdkVersion`, `shortcuts`, `protocolHandlers`, `fileHandlers` ([Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md), [TwaManifest.ts](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/core/src/lib/TwaManifest.ts)).

**Share target is read from the web manifest.** `TwaManifest.verifyShareTarget()` takes the web manifest's `share_target`, resolves `action` to an absolute URL via `new URL(shareTarget.action, webManifestUrl)`, validates any `files[].accept`, and stores it as `shareTarget` ([TwaManifest.ts](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/core/src/lib/TwaManifest.ts)). The template `AndroidManifest.xml` then emits `<meta-data android:name="android.support.customtabs.trusted.METADATA_SHARE_TARGET" android:resource="@string/shareTarget"/>` and an `<intent-filter>` with the generated actions (`SEND`, `SEND_MULTIPLE`) and mime types, plus the `DelegationService` with the `android.support.customtabs.trusted.TRUSTED_WEB_ACTIVITY_SERVICE` intent filter ([Bubblewrap template AndroidManifest.xml](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/core/template_project/app/src/main/AndroidManifest.xml)). So the TWA appears in the share sheet because of its own Android intent filter, independent of Chrome's minting server.

**How the share reaches the web app.** `LauncherActivity.addShareDataIfPresent()` calls `SharingUtils.retrieveShareDataFromIntent(getIntent())`, parses the `METADATA_SHARE_TARGET` JSON with `parseShareTargetJson()` (reads `action`, `method`, `enctype`, `params.title`/`text`/`files`) and calls `twaBuilder.setShareParams(shareTarget, shareData)`; it logs "Failed to share: share target not defined in the AndroidManifest" otherwise ([LauncherActivity.java](https://github.com/GoogleChrome/android-browser-helper/blob/main/androidbrowserhelper/src/main/java/com/google/androidbrowserhelper/trusted/LauncherActivity.java), [SharingUtils.java](https://github.com/GoogleChrome/android-browser-helper/blob/main/androidbrowserhelper/src/main/java/com/google/androidbrowserhelper/trusted/SharingUtils.java)). Chrome (86+) then performs the GET/POST to `action` exactly as for a WebAPK, so the same `/share` route serves both. The demo's text share-target JSON is the GET form with `title`/`text`/`url` params ([twa-web-share-target strings.xml](https://github.com/GoogleChrome/android-browser-helper/blob/main/demos/twa-web-share-target/src/main/res/values/strings.xml)):

```json
{ "action": "https://notes.example.com/share", "method": "GET",
  "params": { "title": "title", "text": "text", "url": "url" } }
```

### 4.3 Digital Asset Links

Statement shape for an Android app ([developers.google.com/digital-asset-links/v1/getting-started](https://developers.google.com/digital-asset-links/v1/getting-started), [developer.android.com configure-assetlinks](https://developer.android.com/training/app-links/configure-assetlinks)):

```json
[{
  "relation": ["delegate_permission/common.handle_all_urls"],
  "target": {
    "namespace": "android_app",
    "package_name": "com.example.visualnotes.twa",
    "sha256_cert_fingerprints": ["14:6D:E9:83:...:44:E5"]
  }
}]
```

Hosting rules: it must live at `https://<host>/.well-known/assetlinks.json` ("statement lists in any other location, or with any other name, are not valid for this site"), served over HTTPS with `Content-Type: application/json`, "accessible without any redirects" (no 301/302), publicly reachable (not VPN-only, not blocked by robots.txt) ([digital-asset-links getting-started](https://developers.google.com/digital-asset-links/v1/getting-started), [configure-assetlinks](https://developer.android.com/training/app-links/configure-assetlinks)). `sha256_cert_fingerprints` accepts multiple entries, e.g. debug and release keys ([configure-assetlinks](https://developer.android.com/training/app-links/configure-assetlinks)). Get the fingerprint with `keytool -list -v -keystore [path] -alias [alias] -storepass [password] -keypass [password]` (printed under "Certificate fingerprints") ([Integration guide](https://developer.chrome.com/docs/android/trusted-web-activity/integration-guide)), from an existing APK with `keytool -printcert -jarfile app-release-signed.apk | grep SHA256` ([Android for web devs](https://developer.chrome.com/docs/android/trusted-web-activity/android-for-web-devs)), or let `bubblewrap build` write `assetlinks.json` for you ([Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md)). The Go backend (or static host) must serve this path from the same origin as the PWA.

Debugging verification: enable `chrome://flags` → "Enable command line on non-rooted devices", then `adb shell "echo '_ --disable-digital-asset-link-verification-for-url=\"https://notes.example.com\"' > /data/local/tmp/chrome-command-line"` temporarily bypasses verification; inspect failures with `adb logcat | grep -e OriginVerifier -e digital_asset_links`; check which browser was picked with `adb logcat -v brief | grep -e TWAProviderPicker` ([Integration guide](https://developer.chrome.com/docs/android/trusted-web-activity/integration-guide), [Quick start](https://developer.chrome.com/docs/android/trusted-web-activity/quick-start)).

### 4.4 Signing and installing without the Play Store

"Android requires that all APKs be digitally signed with a certificate before they are installed on a device or updated" ([developer.android.com app-signing](https://developer.android.com/studio/publish/app-signing)). A self-generated keystore is sufficient for sideloading; generate one with `keytool -genkey -v -keystore my-release-key.jks -keyalg RSA -keysize 2048 -validity 10000 -alias my-alias` ([developer.android.com build/building-cmdline](https://developer.android.com/build/building-cmdline)) or accept Bubblewrap's offer to create `./android.keystore` during `init`. DAL only requires that the fingerprint in `assetlinks.json` match the certificate that signed the installed APK; nothing in the DAL or TWA docs requires a Play-signed key (the Play App Signing note in [configure-assetlinks](https://developer.android.com/training/app-links/configure-assetlinks) applies only when Play re-signs the app). Keep the keystore: a different key means a different fingerprint and an uninstall/reinstall for updates.

Install: enable Developer options (tap Build number seven times) and USB debugging, accept the RSA-key dialog on the tablet, then `adb install app-release-signed.apk` (or `bubblewrap install`) ([developer.android.com tools/adb](https://developer.android.com/tools/adb), [Quick start](https://developer.chrome.com/docs/android/trusted-web-activity/quick-start)). Only the `.apk` is sideloadable; the `.aab` is for Play submission ([Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md)). Verify signing with `apksigner verify app-release-signed.apk` ([building-cmdline](https://developer.android.com/build/building-cmdline)).

Fallback if the device has no TWA-capable browser: `fallbackType` `customtabs` (default) or `webview`; `LauncherActivity.getFallbackStrategy()` returns a Custom Tabs strategy by default or a WebView strategy when configured ([LauncherActivity.java](https://github.com/GoogleChrome/android-browser-helper/blob/main/androidbrowserhelper/src/main/java/com/google/androidbrowserhelper/trusted/LauncherActivity.java), [Bubblewrap CLI README](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/cli/README.md)). For Visual Notes the tablet runs Chrome, so the default is fine.

### 4.5 Can WebAPK and TWA coexist?

They are distinct Android packages (the WebAPK is `org.chromium.webapk.*`, the TWA is whatever `packageId` Bubblewrap was given), so Android can hold both; both would then show in the share sheet under their own names. That is an inference from the package naming above; no primary source discusses running both for one origin: **UNVERIFIED**. Chrome's installability rule "prefer_related_applications must not be present, or be false" ([web.dev/articles/install-criteria](https://web.dev/articles/install-criteria)) means listing the TWA under `related_applications` with `prefer_related_applications: true` would stop Chrome offering the WebAPK install at all, which is a way to make the TWA the single install once the fallback is chosen. Recommendation: treat them as mutually exclusive in onboarding (uninstall the WebAPK before sideloading the TWA) to avoid two "Visual Notes" entries in the share sheet.

### 4.6 Notifications in a TWA

`enableNotifications` (default `true`) enables notification delegation: the `DelegationService` is declared with the `TRUSTED_WEB_ACTIVITY_SERVICE` intent filter, and an optional `android.support.customtabs.trusted.SMALL_ICON` meta-data supplies the status-bar icon ([Bubblewrap template AndroidManifest.xml](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/core/template_project/app/src/main/AndroidManifest.xml), [TwaManifest.ts](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/core/src/lib/TwaManifest.ts)). android-browser-helper's `NotificationDelegationExtraCommandHandler` "handles extra commands related to notification delegation such as checking and requesting permission" (`checkNotificationPermission` returning ALLOW/BLOCK/ASK, and a `PendingIntent` to request it) ([NotificationDelegationExtraCommandHandler.java](https://github.com/GoogleChrome/android-browser-helper/blob/main/androidbrowserhelper/src/main/java/com/google/androidbrowserhelper/trusted/NotificationDelegationExtraCommandHandler.java)). Notification delegation is supported by Chrome and Brave only ([browser-support.md](https://github.com/GoogleChrome/android-browser-helper/blob/main/docs/trusted-web-activity-browser-support.md)). Web Push itself is unchanged: the subscription still belongs to the site's service worker in Chrome; delegation only changes which app the notification is attributed to.

---

## 5. Web Push (spec 8.3 step 7)

### 5.1 Subscribing from the SvelteKit service worker

Flow on the client (all in a user-gesture handler):

1. Ask permission: `Notification.requestPermission()` resolves to `'granted'`, `'denied'` or `'default'` (treated as denied); MDN's example gates the call on a button click and notes it should be made "only in response to user interaction" ([MDN Notification.requestPermission()](https://developer.mozilla.org/en-US/docs/Web/API/Notification/requestPermission_static)). MDN on `subscribe()`: calls "should be done in response to a user gesture ... going forward browsers will explicitly disallow notifications not triggered in response to a user gesture. Firefox is already doing this from version 72" ([MDN PushManager.subscribe()](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)).
2. `const reg = await navigator.serviceWorker.ready;` then `reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey })`. `userVisibleOnly` "is required in some browsers like Chrome and Edge. They will reject the Promise if userVisibleOnly is not set to true." `applicationServerKey` is "A Base64-encoded string or ArrayBuffer containing an ECDSA P-256 public key that the push server will use to authenticate your application server"; it "IS NOT the same ECDH key that you use to encrypt the data" ([MDN PushManager.subscribe()](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)). The key is the 65-byte uncompressed X9.62 point, base64url-encoded ([RFC 8292 §3.2](https://www.rfc-editor.org/rfc/rfc8292)).
3. `subscription.toJSON()` yields `{ endpoint, expirationTime, keys: { p256dh, auth } }` ([MDN PushSubscription.toJSON()](https://developer.mozilla.org/en-US/docs/Web/API/PushSubscription/toJSON)); POST it to the Go API tied to the current Device session.
4. On later loads, `reg.pushManager.getSubscription()` returns the existing subscription or null ([MDN PushSubscription.toJSON() example](https://developer.mozilla.org/en-US/docs/Web/API/PushSubscription/toJSON)); re-send it if the server does not know it. `subscription.unsubscribe()` on sign-out.

SvelteKit specifics: the SW is `src/service-worker.ts`, auto-registered; in dev the worker is not bundled and must be loaded as a module ([svelte.dev/docs/kit/service-workers](https://svelte.dev/docs/kit/service-workers)). Push testing therefore needs a production build over HTTPS (or localhost).

Service-worker side, handling rotation: the `pushsubscriptionchange` event fires when the subscription changes "outside the application's control" ("refreshed by the browser, but it may also happen if the subscription has been revoked or lost"); the handler re-subscribes with `event.oldSubscription.options` and posts old and new payloads to the server inside `event.waitUntil(...)` ([MDN pushsubscriptionchange](https://developer.mozilla.org/en-US/docs/Web/API/ServiceWorkerGlobalScope/pushsubscriptionchange_event)).

### 5.2 VAPID keys

VAPID lets "an application server ... voluntarily identify itself to a push service". The request carries `Authorization: vapid t=<JWT>, k=<public key>`; the JWT is signed with ES256 (ECDSA on P-256); claims: `aud` = the push service origin, `exp` "MUST NOT be more than 24 hours from the time of the request", optional `sub` as a `mailto:` or `https:` URI ([RFC 8292](https://www.rfc-editor.org/rfc/rfc8292)). The same public key is what the browser receives as `applicationServerKey`, which restricts the subscription to this server ([RFC 8292](https://www.rfc-editor.org/rfc/rfc8292), [MDN PushManager.subscribe()](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)).

Generate once in Go: `privateKey, publicKey, err := webpush.GenerateVAPIDKeys()` returns base64url strings ([webpush-go README](https://github.com/SherClockHolmes/webpush-go), [pkg.go.dev webpush-go](https://pkg.go.dev/github.com/SherClockHolmes/webpush-go)). Store both as deployment secrets/config; expose only the public key to the frontend (e.g. `GET /api/push/vapid-public-key` or a build-time env var). Because the subscription is bound to the key, rotating the key invalidates every stored subscription (consequence of RFC 8292 restricted subscriptions; the client must re-subscribe).

### 5.3 Payload encryption and limits

Payloads use `Content-Encoding: aes128gcm` ("exactly one value ... Multiple 'aes128gcm' values are not permitted"); the browser's `p256dh` key is used for ECDH and `auth` is "a hard-to-guess sequence of 16 octets"; the server's ephemeral public key rides in the `keyid`/header as a 65-octet uncompressed point ([RFC 8291](https://www.rfc-editor.org/rfc/rfc8291)). Push services "MUST NOT return a 413 status code in responses to an entity body that is 4096 bytes or less" ([RFC 8030 §7.2](https://www.rfc-editor.org/rfc/rfc8030)); after the aes128gcm header, padding and AEAD tag that leaves roughly 3.99 KB of plaintext ([RFC 8291](https://www.rfc-editor.org/rfc/rfc8291)). webpush-go performs the encryption; keep the JSON payload small (note id, title, URL).

### 5.4 Storing subscriptions and lifecycle

Persist per Device session: `endpoint` (unique key), `p256dh`, `auth`, `expiration_time` (nullable), `created_at`, `last_success_at`, and the owning device/session id so revoking a Device session (spec 11) also drops its subscription. Delivery rules from the protocol: the push service returns `201 Created` when it accepts a message; `TTL` is mandatory; `Urgency` is `very-low|low|normal|high` (default `normal`); `Topic` (max 32 URL-safe base64 characters) makes a newer message replace a pending one; "A push service MUST return a 404 (Not Found) status code if an application server attempts to send a push message to an expired push message subscription", and 410 (Gone) is used when the subscription was removed ([RFC 8030 §5, §5.2, §5.3, §5.4, §7.3](https://www.rfc-editor.org/rfc/rfc8030)). Delete the row on 404 or 410; treat 429/5xx as transient per the spec's retry policy (spec 8.4).

### 5.5 Sending from Go

Library: `github.com/SherClockHolmes/webpush-go` (v1.4.0, Jan 2025; MIT; Go 1.11+). Signatures ([pkg.go.dev webpush-go](https://pkg.go.dev/github.com/SherClockHolmes/webpush-go)):

```go
func GenerateVAPIDKeys() (privateKey, publicKey string, err error)
func SendNotification(message []byte, s *Subscription, options *Options) (*http.Response, error)
func SendNotificationWithContext(ctx context.Context, message []byte, s *Subscription, options *Options) (*http.Response, error)

type Subscription struct { Endpoint string; Keys Keys }
type Keys struct { Auth string; P256dh string }
type Options struct {
    HTTPClient      HTTPClient
    RecordSize      uint32
    Subscriber      string      // sub claim: mailto: or https URL
    Topic           string
    TTL             int
    Urgency         Urgency     // "very-low" | "low" | "normal" | "high"
    VAPIDPublicKey  string
    VAPIDPrivateKey string
    VapidExpiration time.Time
}
```

Minimal send, adapted from the README example ([webpush-go README](https://github.com/SherClockHolmes/webpush-go)):

```go
resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
    Endpoint: row.Endpoint,
    Keys:     webpush.Keys{Auth: row.Auth, P256dh: row.P256dh},
}, &webpush.Options{
    Subscriber:      "mailto:owner@example.com",
    VAPIDPublicKey:  cfg.VAPIDPublic,
    VAPIDPrivateKey: cfg.VAPIDPrivate,
    TTL:             24 * 60 * 60,        // seconds; "note ready" is still useful a day later
    Urgency:         webpush.UrgencyNormal,
    Topic:           "note-" + noteID,    // collapse repeated "ready" pushes for one Note
})
if err != nil { return err }
defer resp.Body.Close()
switch {
case resp.StatusCode == 201: // accepted
case resp.StatusCode == 404 || resp.StatusCode == 410: // delete subscription row
default: // 429/5xx transient, else log
}
```

Send it from the River job's final step (spec 8.3 step 7) alongside the SSE event, only if the owner has at least one stored subscription.

### 5.6 Notification display and click handling in the service worker

```ts
self.addEventListener('push', (event) => {
  const data = event.data?.json() ?? {};
  event.waitUntil(self.registration.showNotification(data.title ?? 'Note ready', {
    body: data.body, icon: '/icons/icon-192.png', badge: '/icons/badge-96.png',
    tag: data.tag ?? `note-${data.noteId}`, data: { url: data.url }
  }));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = new URL(event.notification.data?.url ?? '/', self.location.origin).href;
  event.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const w of wins) { if ('focus' in w) { await w.focus(); return w.navigate(url); } }
    return self.clients.openWindow(url);
  })());
});
```

Grounding: MDN's `notificationclick` sample closes the notification, calls `clients.matchAll({ type: "window" })`, focuses a matching client or calls `clients.openWindow(...)`, all inside `event.waitUntil` ([MDN notificationclick](https://developer.mozilla.org/en-US/docs/Web/API/ServiceWorkerGlobalScope/notificationclick_event)). `WindowClient.navigate(url)` "loads a specified URL into a controlled client page" and resolves to the client only for same-origin URLs ([MDN WindowClient.navigate()](https://developer.mozilla.org/en-US/docs/Web/API/WindowClient/navigate)). `showNotification` options: `tag` identifies a notification so a later one with the same tag replaces it; `renotify` (requires `tag`) controls whether the user is alerted again; `data` carries structured-clonable payload; `badge` should be about 96x96 px for Android's status bar; `requireInteraction` keeps it until dismissed ([MDN showNotification()](https://developer.mozilla.org/en-US/docs/Web/API/ServiceWorkerRegistration/showNotification)). Use `tag` = the Note id so a retry does not stack duplicates.

### 5.7 Android Chrome permission behaviour

- **User gesture**: see 5.1; request permission from the onboarding "Enable notifications" button, never on load ([MDN PushManager.subscribe()](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)).
- **Quieter permission UI**: introduced in Chrome 80 (Chromium Blog, 7 January 2020, "Introducing quieter permission UI for notifications", [blog.chromium.org](https://blog.chromium.org/2020/01/introducing-quieter-permission-ui-for.html)); the body text could not be retrieved, so the exact enrollment conditions (users who repeatedly block, sites with very low accept rates, an opt-in setting) are **UNVERIFIED** here, though Chrome's CrUX post confirms Chrome "automatically enrolls sites with very low Accept rates into the quieter UI" ([developer.chrome.com/blog/notification-permission-data-in-crux](https://developer.chrome.com/blog/notification-permission-data-in-crux)). A single-user site with a one-time explicit opt-in will not hit the low-accept-rate trigger; the user-level trigger depends on the owner's own history.
- **Android 13 runtime permission**: "Android 13 (API level 33) and higher requires the POST_NOTIFICATIONS runtime permission"; for newly installed apps "Notifications are OFF by default" until granted; apps targeting 12L or lower get the system dialog when they create their first notification channel and start an activity ([developer.android.com notification-permission](https://developer.android.com/develop/ui/views/notifications/notification-permission)). How Chrome maps this to a WebAPK's or TWA's app-level permission is **UNVERIFIED** from Chrome docs; the TWA helper library exposes a permission check/request flow for delegated notifications ([NotificationDelegationExtraCommandHandler.java](https://github.com/GoogleChrome/android-browser-helper/blob/main/androidbrowserhelper/src/main/java/com/google/androidbrowserhelper/trusted/NotificationDelegationExtraCommandHandler.java)). Test step: after granting the web permission, confirm in Android Settings → Apps → Visual Notes → Notifications that the app-level toggle is on.
- **Attribution**: whether WebAPK notifications display the web app's name/icon rather than Chrome's is **UNVERIFIED** from the fetched primary sources (web.dev's WebAPK article does not say). For the TWA, delegation exists precisely so notifications are attributed to the app ([Bubblewrap template AndroidManifest.xml](https://github.com/GoogleChromeLabs/bubblewrap/blob/main/packages/core/template_project/app/src/main/AndroidManifest.xml)).
- **Push service dependency**: Chrome subscriptions use Google's push service (endpoints under `fcm.googleapis.com`) and therefore need Google Play services on the tablet; this is common knowledge but **UNVERIFIED** from a primary source in this pass. The same GMS dependency already applies to WebAPK minting ([web.dev/learn/pwa/installation](https://web.dev/learn/pwa/installation)), so a tablet without GMS fails both and needs the TWA plus in-app SSE only.

---

## 6. Tablet share-sheet test checklist (onboarding, spec 5.2 step 6)

Preconditions

- [ ] PWA served over HTTPS from its final origin; manifest has `id`, `name`/`short_name`, `start_url`, `scope`, `display: standalone`, 192 px and 512 px icons, no `prefer_related_applications`, and the final `share_target` (section 3.1). Validate with Chrome DevTools → Application → Manifest on desktop.
- [ ] Service worker registered in a production build; `/share` is prerendered and in the precache; IndexedDB queue and Background Sync tag wired.
- [ ] Tablet: Chrome updated (any version ≥ 86 also covers the TWA path); Google Play services present; signed in to the app once so the Device session exists.

WebAPK path

- [ ] Open the site in Chrome, menu → **Install app** (not "Add to Home screen" wording for a shortcut); wait for install to finish.
- [ ] Visit `chrome://webapks` on the tablet: the entry for the origin exists, shows the package name and "Last Update Check Time"; if absent, Chrome created a shortcut, so the WebAPK path failed → go to the TWA path.
- [ ] Android Settings → Apps shows "Visual Notes" as an app.
- [ ] In the YouTube app, share a video → **Visual Notes** appears in the share sheet.
- [ ] Online share: the PWA opens at `/share?…`, extracts the video URL from `text`, creates the Submission, lands on Today with the processing card. Log the raw `title`/`text`/`url` values on this first run to record what YouTube actually sends (the fact this doc could not verify).
- [ ] Share a YouTube comment/text containing two video links → the picker appears (spec 5.2 step 3).
- [ ] Share non-YouTube text → clear "no YouTube link" message, nothing queued.
- [ ] Airplane mode share: `/share` page still opens (SW-served); the Submission is queued; returning online submits it automatically (Background Sync or foreground retry) and Today shows it.
- [ ] Expired session share: the URL stays pending; after sign-in it submits (spec 5.2 step 5).
- [ ] Change nothing further in `share_target`; if it must change, uninstall and reinstall rather than waiting for the daily update check.

TWA fallback path (only if the WebAPK checks fail)

- [ ] `bubblewrap doctor` passes (JDK 17, Android SDK); `bubblewrap init --manifest=<manifest URL>` picks up `share_target`; keystore created and backed up.
- [ ] `bubblewrap build` produces `app-release-signed.apk` and `assetlinks.json`; the JSON is served at `https://<origin>/.well-known/assetlinks.json` with `Content-Type: application/json`, no redirect (curl -I to confirm); fingerprint matches `keytool -printcert -jarfile app-release-signed.apk`.
- [ ] Uninstall the shortcut/WebAPK first to avoid duplicate share-sheet entries.
- [ ] USB debugging on; `adb install app-release-signed.apk` (or `bubblewrap install`).
- [ ] Launch: full-screen with no URL bar (if a Custom Tab toolbar shows, DAL failed → `adb logcat | grep -e OriginVerifier -e digital_asset_links`).
- [ ] Repeat the YouTube share tests above through the TWA entry; the same `/share` route must handle it.

Web Push (both paths)

- [ ] Tap "Enable notifications" in onboarding → permission `granted`; subscription POSTed; row visible in the DB with the device id.
- [ ] Android Settings → Apps → Visual Notes → Notifications is on (Android 13+).
- [ ] Submit a video; when the Job reaches ready, the notification arrives; tapping it opens/focuses the app on that Note.
- [ ] Revoke the Device session server-side, resend a push → 404/410 → row deleted; subsequent pushes skipped.

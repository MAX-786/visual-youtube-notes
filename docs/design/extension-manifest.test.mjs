// Checks the Manifest V3 release manifest against ADR 0013 and the Chrome Web
// Store packaging contract. The Store owns the production extension ID, so a
// release manifest must never carry a development `key`.
//
// Run: node docs/design/extension-manifest.test.mjs

import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import assert from "node:assert/strict";

const repo = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
);
const read = (p) => readFileSync(path.join(repo, p), "utf8");
const manifest = JSON.parse(read("extension/manifest.json"));

const APP_ORIGIN = "https://yt-track.mkhismkh.com/*";

const tests = [];
const test = (name, fn) => tests.push([name, fn]);

test("is Manifest V3 with a module service worker", () => {
  assert.equal(manifest.manifest_version, 3);
  assert.equal(manifest.background.service_worker, "service-worker.js");
  assert.equal(manifest.background.type, "module");
});

test("contains no fields rejected from a Web Store package", () => {
  assert.ok(!("key" in manifest), "manifest.key is development-only");
  assert.ok(!("update_url" in manifest), "the Chrome Web Store owns updates");
  assert.match(manifest.version, /^\d+(?:\.\d+){0,3}$/);
  assert.ok(manifest.description.length <= 132);
  assert.equal(manifest.homepage_url, "https://yt-track.mkhismkh.com");
});

test("bearer credentials can only reach the app origin", () => {
  assert.deepEqual(
    manifest.host_permissions,
    [APP_ORIGIN],
    "ADR 0003 allows exactly one origin for extension credentials",
  );
});

test("content scripts cover watch and shorts on both YouTube hosts", () => {
  const matches = manifest.content_scripts.flatMap((cs) => cs.matches);

  for (const host of ["www.youtube.com", "m.youtube.com"]) {
    for (const shape of ["watch", "shorts"]) {
      assert.ok(
        matches.some((m) => m.includes(host) && m.includes(shape)),
        `no content script match for ${shape} on ${host}`,
      );
    }
  }

  // Spec 8.1 makes Shorts eligible, so a /watch-only match would silently drop
  // half the capture surface.
  assert.ok(
    !matches.includes("https://www.youtube.com/*"),
    "match the watch and shorts paths, not all of YouTube",
  );
});

test("no permission grants ambient access to tabs or all sites", () => {
  const forbidden = [
    "tabs",
    "webNavigation",
    "<all_urls>",
    "activeTab",
    "scripting",
  ];

  for (const p of forbidden) {
    assert.ok(
      !(manifest.permissions ?? []).includes(p),
      `permission "${p}" is not needed: the service worker reads sender.url, and the ` +
        "content script is declared rather than injected",
    );
  }

  assert.deepEqual(manifest.permissions, ["storage"]);
});

test("the toolbar action exists alongside the in-page one", () => {
  // Spec 16 requires both surfaces.
  assert.ok(
    manifest.action?.default_popup,
    "spec 16 requires a toolbar action",
  );
  assert.ok(
    manifest.content_scripts?.length,
    "spec 16 requires an in-page action",
  );
});

test("every packaged entry point and icon exists", () => {
  const files = [
    "extension/src/service-worker.ts",
    "extension/src/content-script.ts",
    "extension/src/content-script.css",
    "extension/src/popup.ts",
    "extension/src/popup.html",
    "extension/src/popup.css",
    ...Object.values(manifest.icons).map((icon) => `extension/${icon}`),
  ];
  for (const file of files) {
    assert.ok(existsSync(path.join(repo, file)), `missing ${file}`);
  }
});

let failed = 0;
for (const [name, fn] of tests) {
  try {
    fn();
    console.log(`  ok  ${name}`);
  } catch (error) {
    failed += 1;
    console.error(`fail  ${name}`);
    console.error(`      ${error.message.split("\n").join("\n      ")}`);
  }
}

console.log(
  `\nExtension manifest: ${tests.length - failed}/${tests.length} passed`,
);
if (failed > 0) process.exit(1);

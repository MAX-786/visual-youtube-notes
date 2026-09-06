// Checks the Manifest V3 draft against the decisions in ADR 0013, and against
// the one constraint that lives in another ticket's migration: the extension ID
// the server allowlists is derived from this manifest's `key`, and the auth
// schema constrains it to ^[a-p]{32}$. A manifest whose key derives an ID that
// column cannot store is a defect nothing else would catch until pairing.
//
// Run: node docs/design/extension-manifest.test.mjs

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
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

/** Chromium derives an extension ID from SHA-256 of the DER public key: the
 *  first 16 bytes, hex, with each nibble mapped 0-f onto a-p. */
function deriveExtensionId(base64Key) {
  const der = Buffer.from(base64Key, "base64");
  const hex = createHash("sha256").update(der).digest("hex").slice(0, 32);
  return [...hex]
    .map((c) => String.fromCharCode(97 + parseInt(c, 16)))
    .join("");
}

const tests = [];
const test = (name, fn) => tests.push([name, fn]);

test("is Manifest V3 with a module service worker", () => {
  assert.equal(manifest.manifest_version, 3);
  assert.equal(manifest.background.service_worker, "service-worker.js");
  assert.equal(manifest.background.type, "module");
});

test("the key derives an ID the auth schema can store", () => {
  assert.ok(
    manifest.key,
    "manifest.key pins the extension ID across browsers and machines",
  );

  const id = deriveExtensionId(manifest.key);
  const constraint = /\^\[a-p\]\{32\}\$/;
  const migration = read("deploy/migrations/000002_auth_sessions.up.sql");

  assert.ok(
    constraint.test(migration),
    "device_sessions no longer constrains extension_id to ^[a-p]{32}$; this check is stale",
  );
  assert.match(
    id,
    /^[a-p]{32}$/,
    `derived id ${id} does not satisfy the device_sessions.extension_id constraint`,
  );
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
  `\nExtension manifest: ${tests.length - failed}/${tests.length} passed` +
    (failed ? "" : `, id ${deriveExtensionId(manifest.key)}`),
);
if (failed > 0) process.exit(1);

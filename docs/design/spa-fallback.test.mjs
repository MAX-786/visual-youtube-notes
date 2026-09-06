// Guards the SPA fallback agreement described in ADR 0012.
//
// `adapter-static` designates one fallback document and Caddy has to serve that
// same file. When they disagree, SvelteKit's prerendered `index.html` gets
// served at depth, its relative `./_app/...` URLs resolve into a directory that
// does not exist, and `try_files` answers those with the HTML document itself
// under a 200 status. The browser reports a module-script MIME error, not a 404,
// and nothing in the build or the e2e suite fails. Hence a static check.
//
// Run: node docs/design/spa-fallback.test.mjs

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import assert from "node:assert/strict";

const repo = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
);
const read = (p) => readFileSync(path.join(repo, p), "utf8");

const tests = [];
const test = (name, fn) => tests.push([name, fn]);

test("the adapter designates a fallback document", () => {
  const config = read("web/svelte.config.js");
  const match = config.match(/fallback:\s*["']([^"']+)["']/);
  assert.ok(match, "no adapter fallback found in web/svelte.config.js");
  assert.equal(match[1], "200.html");
});

test("Caddy serves the file the adapter designates", () => {
  const caddyfile = read("deploy/caddy/Caddyfile");
  const adapterFallback = read("web/svelte.config.js").match(
    /fallback:\s*["']([^"']+)["']/,
  )[1];

  const match = caddyfile.match(/try_files\s+\{path\}\s+(\S+)/);
  assert.ok(match, "no try_files directive found in the Caddyfile");

  assert.equal(
    match[1],
    `/${adapterFallback}`,
    `Caddy falls back to ${match[1]} but the adapter emits /${adapterFallback}. ` +
      "Serving a prerendered page as the fallback breaks every two-segment route " +
      "(/notes/<id>, /pair/<requestId>, /topics/<slug>) with a 200, not a 404.",
  );
});

test("the root layout does not re-enable prerendering", () => {
  const layout = read("web/src/routes/+layout.ts");

  assert.match(
    layout,
    /export const ssr = false/,
    "the PWA is client-rendered; ssr must stay false",
  );
  assert.doesNotMatch(
    layout,
    /export const prerender = true/,
    "prerender = true cannot survive /notes/[id], and only emits a second, " +
      "subtly different empty shell alongside the fallback",
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

console.log(`\n${tests.length - failed}/${tests.length} passed`);
if (failed > 0) process.exit(1);

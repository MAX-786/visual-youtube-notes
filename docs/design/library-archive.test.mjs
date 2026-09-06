// Tests for the library archive (ADR 0011): the fixture is schema-valid and
// self-consistent, and each way an archive can rot is actually detected.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020.js";
import {
  buildIntegrity,
  canonicalize,
  recordChecksum,
  verifyArchive,
} from "./verify-archive.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const schema = JSON.parse(
  await readFile(path.join(here, "library-archive.schema.json"), "utf8"),
);
const ajv = new Ajv2020({ allErrors: true, strict: true });
const validateStructure = ajv.compile(schema);

const fixture = JSON.parse(
  await readFile(path.join(here, "library-archive-example.json"), "utf8"),
);

// A deep clone per case, so one mutation cannot leak into the next.
const clone = (value) => structuredClone(value);

// --- canonicalization -------------------------------------------------------

assert.equal(
  canonicalize({ b: 1, a: 2 }),
  '{"a":2,"b":1}',
  "object keys canonicalize in UTF-16 code unit order",
);
assert.equal(
  canonicalize([{ z: null }, true, "x"]),
  '[{"z":null},true,"x"]',
  "arrays keep their order and scalars serialize without whitespace",
);
assert.equal(
  canonicalize({ é: "café\n" }),
  '{"é":"café\\n"}',
  "strings serialize as ECMAScript JSON, per RFC 8785 section 3.2.2.2",
);
assert.throws(
  () => canonicalize({ x: 1.5 }),
  /floating-point/,
  "a float is refused rather than approximated",
);

// --- the fixture ------------------------------------------------------------

assert.ok(
  validateStructure(fixture),
  `fixture should satisfy the archive schema: ${ajv.errorsText(validateStructure.errors)}`,
);

const clean = verifyArchive(fixture);
assert.deepEqual(clean.failures, [], "fixture should verify with no failures");
assert.ok(clean.ok);
assert.deepEqual(clean.counts, {
  topics: 1,
  tags: 2,
  notes: 2,
  revisions: 3,
});

assert.deepEqual(
  buildIntegrity(fixture),
  fixture.integrity,
  "the fixture's integrity block should be the one buildIntegrity derives",
);

// --- what rot looks like ----------------------------------------------------

// 1. A field edited in place. The record's own checksum no longer covers it.
const edited = clone(fixture);
edited.notes[0].source.title = "A title nobody wrote";
const editedResult = verifyArchive(edited);
assert.ok(!editedResult.ok, "an edited field must not verify");
assert.match(
  editedResult.failures.join("\n"),
  /\$\.notes\[0\]: checksum .* does not match/,
  "the failure should name the record that was edited",
);

// 2. An edit whose record checksum was recomputed to cover the lie. Only the
//    whole-document digest is left to catch it, which is why it exists.
const resealed = clone(fixture);
resealed.notes[0].source.title = "A title nobody wrote";
resealed.notes[0].checksum = recordChecksum(resealed.notes[0]);
const resealedResult = verifyArchive(resealed);
assert.ok(!resealedResult.ok, "a resealed record must still not verify");
assert.match(
  resealedResult.failures.join("\n"),
  /\$\.integrity\.digest: declared .* computed/,
  "the integrity digest should be what catches a resealed record",
);

// 3. Truncation. The counts catch it even where the surviving records are sound.
const truncated = clone(fixture);
truncated.notes.pop();
const truncatedResult = verifyArchive(truncated);
assert.ok(!truncatedResult.ok, "a truncated archive must not verify");
assert.match(
  truncatedResult.failures.join("\n"),
  /\$\.integrity\.counts\.notes: declared 2, found 1/,
  "the counts should name what went missing",
);

// 4. Reordering. Every record is intact and every count is right; only the
//    digest's dependence on emission order detects it.
const reordered = clone(fixture);
reordered.notes.reverse();
const reorderedResult = verifyArchive(reordered);
assert.ok(!reorderedResult.ok, "a reordered archive must not verify");
assert.deepEqual(
  reorderedResult.failures.filter((f) => !f.startsWith("$.integrity.digest")),
  [],
  "reordering should fail on the digest alone — every record still matches itself",
);

// 5. A float smuggled into a verbatim Revision payload. The schema cannot see
//    inside `payload`, so the canonicalizer is the only thing standing there.
const floated = clone(fixture);
floated.notes[0].revisions[0].payload.confidence = 0.5;
const floatedResult = verifyArchive(floated);
assert.ok(!floatedResult.ok, "a float in a payload must not verify");
assert.match(
  floatedResult.failures.join("\n"),
  /floating-point/,
  "the failure should say why a float is refused",
);

// --- the CLI ----------------------------------------------------------------
// It once exited 0 while printing nothing, because its entrypoint guard compared
// import.meta.url against process.argv[1] and this repository's path contains a
// space, which the URL percent-encodes. Silence and success looked identical.

const verifier = path.join(here, "verify-archive.mjs");
const fixturePath = path.join(here, "library-archive-example.json");
const tmp = mkdtempSync(path.join(os.tmpdir(), "vyn-archive-"));

const stdout = execFileSync(process.execPath, [verifier, fixturePath], {
  encoding: "utf8",
});
assert.match(
  stdout,
  /^ok — 2 notes, 3 revisions, 1 topics, 2 tags$/m,
  "the CLI must report on success, not merely exit 0",
);

const tamperedPath = path.join(tmp, "tampered.json");
const tampered = clone(fixture);
tampered.topics[0].name = "Renamed behind the checksum";
writeFileSync(tamperedPath, JSON.stringify(tampered), "utf8");
assert.throws(
  () =>
    execFileSync(process.execPath, [verifier, tamperedPath], {
      encoding: "utf8",
      stdio: "pipe",
    }),
  (error) => error.status === 1 && /does not match/.test(error.stderr),
  "the CLI must exit non-zero and say what failed",
);

console.log(
  `Library archive v1: fixture schema-valid (${clean.counts.notes} notes, ` +
    `${clean.counts.revisions} revisions), 4 canonicalization rules checked, ` +
    `5 corruption modes detected, CLI exercised both ways.`,
);

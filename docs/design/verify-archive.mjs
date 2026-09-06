// Executable reference for the library archive's checksums (ADR 0011), and the
// definition of record — its algorithm, not its prose, is what an archive must
// satisfy. Mirrors the role diagram-semantics.mjs plays for the Diagram schema.
//
// Usage: node verify-archive.mjs <archive.json>
//   exit 0 — every checksum and the integrity block agree
//   exit 1 — at least one does not; each failure is printed
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

/**
 * RFC 8785 (JSON Canonicalization Scheme), restricted to what the archive
 * admits. Object keys sort by UTF-16 code unit, arrays keep their order, and
 * there is no whitespace anywhere.
 *
 * The archive carries no floating-point scalar by construction, so number
 * serialization — RFC 8785's one genuinely subtle rule — reduces to integers.
 * A float reaching this function is thrown rather than approximated: the
 * schema's promise is only worth something if breaking it is loud.
 */
export function canonicalize(value, path = "$") {
  if (value === null) return "null";

  const type = typeof value;

  if (type === "boolean") return value ? "true" : "false";

  if (type === "number") {
    if (!Number.isFinite(value)) {
      throw new Error(`${path}: ${value} is not representable in JSON`);
    }
    if (!Number.isInteger(value)) {
      throw new Error(
        `${path}: ${value} is a floating-point number; the archive admits none, ` +
          `because RFC 8785 number formatting would then be load-bearing for every checksum`,
      );
    }
    if (!Number.isSafeInteger(value)) {
      throw new Error(`${path}: ${value} exceeds the safe integer range`);
    }
    return String(value);
  }

  // RFC 8785 section 3.2.2.2 defers string serialization to ECMAScript's own.
  if (type === "string") return JSON.stringify(value);

  if (Array.isArray(value)) {
    const items = value.map((item, i) => canonicalize(item, `${path}[${i}]`));
    return `[${items.join(",")}]`;
  }

  if (type === "object") {
    // Default sort compares UTF-16 code units, which is the order JCS asks for.
    const keys = Object.keys(value).sort();
    const members = keys.map(
      (key) =>
        `${JSON.stringify(key)}:${canonicalize(value[key], `${path}.${key}`)}`,
    );
    return `{${members.join(",")}}`;
  }

  throw new Error(`${path}: ${type} has no JSON representation`);
}

const sha256Hex = (text) =>
  createHash("sha256").update(text, "utf8").digest("hex");

/** The checksum a record should carry: sha256 over its canonical form with its own `checksum` removed. */
export function recordChecksum(record, path = "$") {
  const withoutChecksum = { ...record };
  delete withoutChecksum.checksum;
  return `sha256:${sha256Hex(canonicalize(withoutChecksum, path))}`;
}

/**
 * Every checksummed record in emission order: topics, then tags, then notes.
 * This order is the integrity digest's input, so it is defined in exactly one
 * place and both the writer and the verifier read it from here.
 */
function* checksummedRecords(archive) {
  for (const [i, topic] of (archive.topics ?? []).entries()) {
    yield { path: `$.topics[${i}]`, record: topic };
  }
  for (const [i, tag] of (archive.tags ?? []).entries()) {
    yield { path: `$.tags[${i}]`, record: tag };
  }
  for (const [i, note] of (archive.notes ?? []).entries()) {
    yield { path: `$.notes[${i}]`, record: note };
  }
}

/**
 * Verify an archive. Returns `{ ok, failures, counts }`; `failures` is a list of
 * human-readable strings, empty when `ok`.
 */
export function verifyArchive(archive) {
  const failures = [];
  const digests = [];

  if (archive?.archive_version !== 1) {
    failures.push(
      `$.archive_version: expected 1, found ${JSON.stringify(archive?.archive_version)}`,
    );
  }

  const integrity = archive?.integrity;
  if (!integrity) {
    return {
      ok: false,
      failures: [...failures, "$.integrity: missing"],
      counts: null,
    };
  }
  if (integrity.algorithm !== "sha256") {
    failures.push(`$.integrity.algorithm: unsupported ${integrity.algorithm}`);
  }
  if (integrity.canonicalization !== "RFC8785") {
    failures.push(
      `$.integrity.canonicalization: unsupported ${integrity.canonicalization}`,
    );
  }

  for (const { path, record } of checksummedRecords(archive)) {
    let expected;
    try {
      expected = recordChecksum(record, path);
    } catch (error) {
      failures.push(`${path}: ${error.message}`);
      continue;
    }
    if (record.checksum !== expected) {
      failures.push(
        `${path}: checksum ${record.checksum ?? "(absent)"} does not match ${expected}`,
      );
    }
    // The record's own claim is digested, not the recomputed one, so a tampered
    // record fails its own line above and the whole-document digest below only
    // if the integrity block was not rewritten to match. Both are reported.
    digests.push((record.checksum ?? "").replace(/^sha256:/, ""));
  }

  const counts = {
    topics: (archive.topics ?? []).length,
    tags: (archive.tags ?? []).length,
    notes: (archive.notes ?? []).length,
    revisions: (archive.notes ?? []).reduce(
      (total, note) => total + (note.revisions ?? []).length,
      0,
    ),
  };

  for (const key of ["topics", "tags", "notes", "revisions"]) {
    if (integrity.counts?.[key] !== counts[key]) {
      failures.push(
        `$.integrity.counts.${key}: declared ${integrity.counts?.[key]}, found ${counts[key]}`,
      );
    }
  }

  const digest = `sha256:${sha256Hex(digests.join(""))}`;
  if (integrity.digest !== digest) {
    failures.push(
      `$.integrity.digest: declared ${integrity.digest}, computed ${digest}`,
    );
  }

  return { ok: failures.length === 0, failures, counts };
}

/** Build the integrity block for an archive whose records already carry checksums. */
export function buildIntegrity(archive) {
  const digests = [];
  for (const { record } of checksummedRecords(archive)) {
    digests.push((record.checksum ?? "").replace(/^sha256:/, ""));
  }
  return {
    algorithm: "sha256",
    canonicalization: "RFC8785",
    counts: {
      topics: (archive.topics ?? []).length,
      tags: (archive.tags ?? []).length,
      notes: (archive.notes ?? []).length,
      revisions: (archive.notes ?? []).reduce(
        (total, note) => total + (note.revisions ?? []).length,
        0,
      ),
    },
    digest: `sha256:${sha256Hex(digests.join(""))}`,
  };
}

// Compare resolved filesystem paths, not the URL: import.meta.url percent-encodes
// characters the argv path leaves literal, so a directory whose name contains a
// space makes a string comparison of the two silently never match -- and the CLI
// then does nothing at all while still exiting 0.
const invokedDirectly =
  process.argv[1] &&
  path.resolve(fileURLToPath(import.meta.url)) ===
    path.resolve(process.argv[1]);

if (invokedDirectly) {
  const file = process.argv[2];
  if (!file) {
    console.error("usage: node verify-archive.mjs <archive.json>");
    process.exit(2);
  }
  const { ok, failures, counts } = verifyArchive(
    JSON.parse(readFileSync(file, "utf8")),
  );
  if (ok) {
    console.log(
      `ok — ${counts.notes} notes, ${counts.revisions} revisions, ` +
        `${counts.topics} topics, ${counts.tags} tags`,
    );
    process.exit(0);
  }
  console.error(`FAILED — ${failures.length} problem(s):`);
  for (const failure of failures) console.error(`  ${failure}`);
  process.exit(1);
}

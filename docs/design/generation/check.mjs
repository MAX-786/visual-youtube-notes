// PROTOTYPE (ticket #15). Dev runner: push a model-output fixture through the cycle.
// Usage: node check.mjs <transcript.json> <note.json>
import { readFileSync } from "node:fs";
import {
  validateNote,
  runValidationCycle,
  toStoredPayload,
} from "./validate-note.mjs";

const [, , tPath, nPath] = process.argv;
const t = JSON.parse(readFileSync(tPath, "utf8"));
const n = JSON.parse(readFileSync(nPath, "utf8"));

const findings = validateNote(n, t);
console.log(`findings: ${findings.length}`);
for (const f of findings) {
  console.log(
    `  [${f.repairable ? "repairable" : "re-ask   "}] ${f.code} @ ${f.path}: ${f.message}`,
  );
}
const r = runValidationCycle(n, t);
for (const s of r.steps) {
  if (s.applied) s.applied.forEach((a) => console.log(`  repair: ${a}`));
}
console.log(`outcome: ${r.outcome}`);
if (r.outcome === "ready") {
  const stored = toStoredPayload(r.note, t);
  const refs = (JSON.stringify(stored).match(/"char_start"/g) ?? []).length;
  console.log(
    `stored payload built; ${refs} source refs resolved to character spans`,
  );
  console.log(`  sample: ${JSON.stringify(stored.summary.source_refs[0])}`);
}

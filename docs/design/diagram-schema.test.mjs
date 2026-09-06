import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020.js";
import {
  deriveTextAlternative,
  validateSemantics,
} from "./diagram-semantics.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const schema = JSON.parse(
  await readFile(path.join(here, "diagram.schema.json"), "utf8"),
);
const ajv = new Ajv2020({ allErrors: true, strict: true });
const validateStructure = ajv.compile(schema);

async function loadExamples(kind) {
  const directory = path.join(here, "diagram-examples", kind);
  const names = (await readdir(directory))
    .filter((name) => name.endsWith(".json"))
    .sort();
  return Promise.all(
    names.map(async (name) => ({
      name,
      document: JSON.parse(await readFile(path.join(directory, name), "utf8")),
    })),
  );
}

function errorsFor(document) {
  const structuralValid = validateStructure(document);
  const structuralErrors = structuralValid
    ? []
    : validateStructure.errors.map(
        (error) => `${error.instancePath || "/"} ${error.message}`,
      );
  return [...structuralErrors, ...validateSemantics(document)];
}

const valid = await loadExamples("valid");
const invalid = await loadExamples("invalid");

for (const example of valid) {
  assert.deepEqual(
    errorsFor(example.document),
    [],
    `${example.name} should be valid`,
  );
}

// The diagram the Go generating Stage stored for the design fixture is
// structurally and semantically valid, like every hand-written example.
const goStored = JSON.parse(
  await readFile(
    path.join(here, "generation", "fixtures", "en-neural-network.stored.json"),
    "utf8",
  ),
);
assert.deepEqual(
  errorsFor(goStored.diagram),
  [],
  "the Go pipeline's stored diagram should be valid",
);

const expectedInvalidReasons = new Map([
  ["ai-content-without-source-ref.json", "fewer than 1"],
  ["duplicate-element-id.json", "duplicate element id"],
  ["edge-to-missing-node.json", "unknown target"],
  ["five-comparison-columns.json", "more than 4"],
  ["hidden-event-without-group.json", "must belong to a collapsed group"],
  ["reversed-source-span.json", "char_end must exceed char_start"],
  ["thirteen-visible-process-nodes.json", "no more than 12"],
  ["twenty-five-timeline-events.json", "more than 24"],
]);

for (const example of invalid) {
  const errors = errorsFor(example.document);
  assert.notEqual(errors.length, 0, `${example.name} should be invalid`);
  assert.ok(
    errors.some((error) =>
      error.includes(expectedInvalidReasons.get(example.name)),
    ),
    `${example.name} should fail for its documented reason; got: ${errors.join("; ")}`,
  );
}

const expectedTextAlternatives = new Map([
  [
    "mind-map.json",
    [
      "Spaced recall",
      "  Review schedule",
      "    After one day",
      "    After one week",
    ].join("\n"),
  ],
  [
    "process-flow.json",
    [
      "1. Learn",
      "2. Wait one day",
      "   Branch: When ready → Recall without notes",
      "3. Recall without notes",
    ].join("\n"),
  ],
  [
    "timeline.json",
    [
      "1. Learn the idea",
      "2. Review after one day",
      "3. Review after one week",
    ].join("\n"),
  ],
  [
    "comparison.json",
    [
      "Criterion | Cramming | Spaced recall",
      "Timing | One long session | Several separated sessions",
      "Recall effort | Low between repetitions | Higher after a delay",
    ].join("\n"),
  ],
]);

for (const example of valid) {
  assert.equal(
    deriveTextAlternative(example.document),
    expectedTextAlternatives.get(example.name),
    `${example.name} should have a deterministic, position-free Text alternative`,
  );
}

console.log(
  `Diagram schema v1: ${valid.length} valid examples accepted, ${invalid.length} invalid examples rejected, ${valid.length} Text alternatives verified.`,
);

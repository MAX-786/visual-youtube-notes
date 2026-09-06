// Tests for the OpenAPI contract (ADR 0015). The contract carries a structural
// mirror of the Revision payload; the normative rules live in
// diagram.schema.json and validate-note.mjs. This file keeps the two in step:
// every normatively valid diagram must pass the mirror, the mirror must still
// catch the cardinality caps it claims to carry, and the stored payload that
// the generation prototype produces must be what the contract says a Revision
// holds. It also pins the contract-level invariants the ADR relies on.
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020.js";
import { parse as parseYaml } from "yaml";
import { toStoredPayload } from "./generation/validate-note.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const document = parseYaml(
  await readFile(path.join(here, "openapi.yaml"), "utf8"),
);

// --- the document itself ------------------------------------------------------

assert.equal(document.openapi, "3.1.0");
assert.equal(
  document.servers?.[0]?.url,
  "https://yt-track.mkhismkh.com/api",
  "every path mounts under /api (ADR 0008)",
);

const operations = [];
for (const [route, item] of Object.entries(document.paths)) {
  for (const method of ["get", "post", "put", "patch", "delete"]) {
    if (item[method]) operations.push({ route, method, op: item[method] });
  }
}
assert.ok(operations.length >= 50, "the contract covers every V1 resource");

const operationIds = operations.map(({ op }) => op.operationId);
assert.equal(
  new Set(operationIds).size,
  operationIds.length,
  "operationIds are unique",
);

const errorCodes = new Set(document.components.schemas.ErrorCode.enum);
for (const { route, method, op } of operations) {
  assert.ok(op.operationId, `${method} ${route} has an operationId`);
  assert.ok(op.tags?.length, `${method} ${route} is tagged`);
  assert.ok(
    op.responses.default?.$ref === "#/components/responses/Error",
    `${method} ${route} routes every non-2xx through the error envelope`,
  );
  for (const code of op["x-error-codes"] ?? []) {
    assert.ok(
      errorCodes.has(code),
      `${method} ${route} names an error code the enum knows: ${code}`,
    );
  }
}

// Every code in the enum has a row in the status table, so a new code cannot
// be added without saying what status it carries.
const codeTable = document.components.schemas.ErrorCode.description;
for (const code of errorCodes) {
  assert.ok(
    codeTable.includes(`| \`${code}\` |`),
    `error code ${code} has a status row in the ErrorCode table`,
  );
}

// The extension may call exactly the operations the CORS allowlist admits
// (ADR 0003 as refined by ADR 0013 and ADR 0015): Pairing, its own refresh and
// logout, and creating or undoing a Submission.
const extensionCallable = operations
  .filter(({ op }) => op["x-extension-callable"] === true)
  .map(({ route, method }) => `${method.toUpperCase()} ${route}`)
  .sort();
assert.deepEqual(extensionCallable, [
  "DELETE /submissions/{noteId}",
  "POST /pairing/exchange",
  "POST /pairing/logout",
  "POST /pairing/refresh",
  "POST /pairing/requests",
  "POST /submissions",
]);

// Cookie-authenticated mutations are exactly the ones the CSRF middleware
// guards. Any non-GET operation that accepts the cookie must therefore not
// also be extension-callable without the bearer scheme being listed, and every
// extension-callable operation must either be public or accept the bearer.
for (const { route, method, op } of operations) {
  if (op["x-extension-callable"] !== true) continue;
  const schemes = (op.security ?? document.security).flatMap((entry) =>
    Object.keys(entry),
  );
  assert.ok(
    schemes.length === 0 || schemes.includes("extensionBearer"),
    `${method} ${route} is extension-callable but only accepts the cookie`,
  );
}

// The SSE stream is documented in the contract and excluded from Go codegen,
// and the exclusion list names the real operationId.
const codegen = parseYaml(
  await readFile(
    path.join(here, "../../backend/internal/httpapi/oapi-codegen.yaml"),
    "utf8",
  ),
);
assert.deepEqual(codegen["output-options"]["exclude-operation-ids"], [
  "streamEvents",
]);
assert.equal(document.paths["/events"].get.operationId, "streamEvents");
const frames = document.paths["/events"].get["x-sse-frames"];
for (const ref of Object.values(frames)) {
  const name = ref.replace("#/components/schemas/", "");
  assert.ok(document.components.schemas[name], `SSE frame schema ${name}`);
}

// --- the structural mirror against the normative examples --------------------

// OpenAPI 3.1 schema objects are JSON Schema 2020-12 plus a few OpenAPI-only
// keywords, which strict mode would reject.
const ajv = new Ajv2020({
  allErrors: true,
  strict: false,
  validateFormats: false,
});
ajv.addSchema(document, "openapi");
const compile = (name) =>
  ajv.getSchema(`openapi#/components/schemas/${name}`) ??
  ajv.compile({ $ref: `openapi#/components/schemas/${name}` });
const validateDiagram = compile("Diagram");
const validatePayload = compile("RevisionPayload");

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

// Everything the normative schema accepts, the mirror must accept: a client
// typed from the contract can never be handed a stored Diagram it cannot read.
for (const example of await loadExamples("valid")) {
  assert.ok(
    validateDiagram(example.document),
    `valid/${example.name} passes the contract mirror: ${ajv.errorsText(validateDiagram.errors)}`,
  );
}

// The mirror keeps the cardinality caps it claims. The rest of the invalid
// examples break semantic rules -- tree structure, grounding, visibility --
// that the mirror deliberately leaves to the server's validator.
const invalid = await loadExamples("invalid");
const caughtByMirror = new Set([
  "five-comparison-columns.json",
  "twenty-five-timeline-events.json",
]);
for (const example of invalid) {
  const passes = validateDiagram(example.document);
  if (caughtByMirror.has(example.name)) {
    assert.ok(!passes, `invalid/${example.name} is rejected by the mirror`);
  }
}
assert.equal(
  invalid.filter((example) => caughtByMirror.has(example.name)).length,
  caughtByMirror.size,
  "every cap example the mirror claims to catch exists",
);

// The stored payload the generation prototype produces is what the contract
// says a Revision holds. This is the one place request-shaped output, the
// storage mapping, and the wire type meet.
const transcript = JSON.parse(
  await readFile(
    path.join(here, "generation/fixtures/en-neural-network.transcript.json"),
    "utf8",
  ),
);
const note = JSON.parse(
  await readFile(
    path.join(here, "generation/fixtures/en-neural-network.note-valid.json"),
    "utf8",
  ),
);
const payload = toStoredPayload(note, transcript);
assert.ok(
  validatePayload(payload),
  `the stored fixture payload matches RevisionPayload: ${ajv.errorsText(validatePayload.errors)}`,
);
assert.equal(payload.schema_version, 1);

// A payload that still carries coordinates is rejected by the mirror as it is
// by the normative schema (ADR 0004 hoisted layout out of the Revision).
const withCoordinates = structuredClone(payload);
withCoordinates.diagram.layout.positions = { n_root: { x: 0, y: 0 } };
assert.ok(
  !validatePayload(withCoordinates),
  "coordinates inside a Revision payload are rejected",
);

console.log(
  `openapi contract: ${operations.length} operations, ${errorCodes.size} error codes, mirror holds`,
);

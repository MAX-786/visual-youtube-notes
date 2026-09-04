// PROTOTYPE (ticket #15). Assembles the Messages API request for the generating stage and
// estimates what one note costs. Run: node build-request.mjs
//
// NO PROVIDER CALL IS MADE. Ticket #14 defers real keys to deployment, so token counts here
// are ESTIMATED from measured character counts, not measured with /v1/messages/count_tokens.
// The chars-per-token ratios below are the one soft number in this file; replace them with a
// real count_tokens call the day keys exist. Every other input is measured.

import { readFileSync, writeFileSync } from "node:fs";

const MODEL = "claude-sonnet-5"; // fallback: claude-haiku-4-5 (ticket #3)

const PRICES = {
  // $ per million tokens (ticket #3 research, Anthropic first-party rates)
  "claude-sonnet-5": { in: 2, cacheRead: 0.2, cacheWrite: 2.5, out: 10 },
  "claude-haiku-4-5": { in: 1, cacheRead: 0.1, cacheWrite: 1.25, out: 5 },
};

// Latin text tokenizes at roughly 3.8 chars/token. Devanagari is far denser in tokens:
// each character is 3 UTF-8 bytes and byte-level BPE merges them poorly, so a Hindi
// transcript of the same length costs several times more input than an English one.
// Treated as a band because it is the estimate, not a measurement.
const CHARS_PER_TOKEN = { latin: 3.8, devanagari: { low: 1.6, high: 1.0 } };

const DEVANAGARI = /[ऀ-ॿ]/gu;

function estimateTokens(text, devRatio) {
  const dev = (text.match(DEVANAGARI) ?? []).length;
  const latin = text.length - dev;
  return Math.round(latin / CHARS_PER_TOKEN.latin + dev / devRatio);
}

/** The user message: what the model sees below the frozen system prompt. */
export function renderUserMessage(transcript, meta, instruction) {
  const chunks = transcript.chunks
    .map((c) => `[c${c.index + 1}]\n${c.text}`)
    .join("\n\n");
  return [
    `Video: ${meta.title}`,
    `Channel: ${meta.channel}`,
    `Duration: ${Math.round(meta.duration / 60)} minutes`,
    instruction ? `\nThe person saving this note asked: ${instruction}` : "",
    `\nTranscript, split into chunks. Cite a chunk by the handle in brackets.\n`,
    chunks,
  ]
    .filter(Boolean)
    .join("\n");
}

export function buildRequest({
  transcript,
  meta,
  schema,
  system,
  instruction,
}) {
  return {
    model: MODEL,
    max_tokens: 16000,
    // Frozen prefix: system prompt + schema never vary per note, so they cache.
    // The transcript sits after the breakpoint because it changes every call.
    system: [
      { type: "text", text: system, cache_control: { type: "ephemeral" } },
    ],
    // Sonnet 5 takes adaptive thinking only; budget_tokens is rejected. effort low keeps
    // thinking tokens (billed as output) down on what is an extraction task.
    thinking: { type: "adaptive" },
    output_config: {
      effort: "low",
      format: { type: "json_schema", schema },
    },
    messages: [
      {
        role: "user",
        content: renderUserMessage(transcript, meta, instruction),
      },
    ],
  };
}

// ------------------------------------------------------------------ estimate

const system = readFileSync("./system-prompt.md", "utf8");
const schema = JSON.parse(readFileSync("./note-request.schema.json", "utf8"));
const sampleNote = readFileSync(
  "./fixtures/en-neural-network.note-valid.json",
  "utf8",
);

const FIXTURES = [
  [
    "English, human captions",
    "./fixtures/en-neural-network.transcript.json",
    {
      title: "But what is a neural network?",
      channel: "3Blue1Brown",
      duration: 1120,
    },
  ],
  [
    "Hindi, ASR",
    "./fixtures/hi-neural-network.transcript.json",
    {
      title: "[Hindi Dub] But what is a neural network?",
      channel: "3Blue1Brown",
      duration: 1120,
    },
  ],
  [
    "Hinglish, ASR",
    "./fixtures/hinglish-python-oops.transcript.json",
    {
      title: "Introduction to OOPs in Python",
      channel: "CodeWithHarry",
      duration: 953,
    },
  ],
];

const schemaText = JSON.stringify(schema);
// Output is the serialized JSON object as the model emits it: compact, not the indented
// form the fixture is stored in. The note is written in English whatever the transcript's
// language, so output size barely moves between the three fixtures.
// NOT counted here: thinking tokens, which bill as output. effort:low keeps them small but
// not zero, so treat the output column as a floor.
const compactNote = JSON.stringify(JSON.parse(sampleNote));

const rows = [];
for (const [label, path, meta] of FIXTURES) {
  const transcript = JSON.parse(readFileSync(path, "utf8"));
  const user = renderUserMessage(transcript, meta);
  const cached = system + schemaText;
  for (const [band, ratio] of [
    ["high", CHARS_PER_TOKEN.devanagari.high],
    ["low", CHARS_PER_TOKEN.devanagari.low],
  ]) {
    rows.push({
      label,
      band,
      cachedTokens: estimateTokens(cached, ratio),
      userTokens: estimateTokens(user, ratio),
      outTokens: estimateTokens(compactNote, ratio),
      chars: user.length,
      devShare: (user.match(DEVANAGARI) ?? []).length / user.length,
    });
  }
}

const cost = (r, model, { cacheHit }) => {
  const p = PRICES[model];
  const cached = cacheHit
    ? r.cachedTokens * p.cacheRead
    : r.cachedTokens * p.cacheWrite;
  return (cached + r.userTokens * p.in + r.outTokens * p.out) / 1e6;
};

const fmt = (n) => "$" + n.toFixed(4);
console.log(
  `Frozen prefix: system ${system.length} chars + schema ${schemaText.length} chars`,
);
console.log(
  `Output (sample note, compact, always English): ${compactNote.length} chars\n`,
);
console.log(
  "Per note, first call (cache write) / repeat within 5 min (cache read):\n",
);
console.log(
  "fixture".padEnd(26) +
    "band".padEnd(6) +
    "devanagari".padEnd(12) +
    "in tok".padEnd(9) +
    "out tok".padEnd(9) +
    "sonnet-5".padEnd(11) +
    "haiku-4-5",
);
for (const r of rows) {
  console.log(
    r.label.padEnd(26) +
      r.band.padEnd(6) +
      (r.devShare * 100).toFixed(0).padStart(3) +
      "%".padEnd(9) +
      String(r.userTokens).padEnd(9) +
      String(r.outTokens).padEnd(9) +
      fmt(cost(r, "claude-sonnet-5", { cacheHit: false })).padEnd(11) +
      fmt(cost(r, "claude-haiku-4-5", { cacheHit: false })),
  );
}

console.log(
  "\nMonthly, at the spec's 5 saves/day (150 notes), worst-case band:",
);
for (const model of ["claude-sonnet-5", "claude-haiku-4-5"]) {
  for (const label of ["English, human captions", "Hindi, ASR"]) {
    const r = rows.find((x) => x.label === label && x.band === "low");
    const perNote = cost(r, model, { cacheHit: true });
    console.log(
      `  ${model.padEnd(18)} all-${label.split(",")[0].padEnd(8)} ${fmt(perNote * 150).padStart(9)}  (${fmt(perNote)}/note)`,
    );
  }
}

const req = buildRequest({
  transcript: JSON.parse(readFileSync(FIXTURES[0][1], "utf8")),
  meta: FIXTURES[0][2],
  schema,
  system,
});
writeFileSync(
  "./fixtures/sample-request.json",
  JSON.stringify(req, null, 2) + "\n",
  "utf8",
);
console.log("\nfixtures/sample-request.json written");

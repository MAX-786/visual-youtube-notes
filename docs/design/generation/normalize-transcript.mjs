// PROTOTYPE (ticket #15). Design-time tool: WebVTT -> normalized, chunked Transcript fixture.
//
// Chunking parameters here are PROVISIONAL. Chunk size, overlap, and boundary rules are
// owned by the not-yet-specified transcript-chunking ticket. Note generation depends only
// on the contract "chunks have stable IDs and chunk-relative character offsets", never on
// the specific numbers below.
//
// Usage: node normalize-transcript.mjs <in.vtt> <videoId> <language> <out.json>

import { readFileSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";

const TARGET_CHARS = 1200; // provisional

/** Deterministic UUID so re-running produces byte-identical fixtures. */
function stableUuid(seed) {
  const h = createHash("sha256").update(seed).digest("hex").slice(0, 32);
  const b = h.split("");
  b[12] = "5"; // version
  b[16] = ((parseInt(b[16], 16) & 0x3) | 0x8).toString(16); // variant
  const s = b.join("");
  return `${s.slice(0, 8)}-${s.slice(8, 12)}-${s.slice(12, 16)}-${s.slice(16, 20)}-${s.slice(20)}`;
}

/** Strip inline word-timing markup: <00:00:10.759> and <c>...</c>. */
function stripInline(text) {
  return text
    .replace(/<\d{2}:\d{2}:\d{2}\.\d{3}>/g, "")
    .replace(/<\/?c[^>]*>/g, "")
    .replace(/<\/?[a-z][^>]*>/gi, "");
}

function parseVtt(raw) {
  const cues = [];
  for (const block of raw.replace(/\r/g, "").split(/\n{2,}/)) {
    const lines = block.split("\n");
    const tIdx = lines.findIndex((l) => l.includes("-->"));
    if (tIdx === -1) continue;
    const [start] = lines[tIdx].split("-->").map((s) => s.trim().split(" ")[0]);
    const text = lines
      .slice(tIdx + 1)
      .map((l) => stripInline(l).trim())
      .filter(Boolean);
    if (text.length) cues.push({ start, lines: text });
  }
  return cues;
}

/** YouTube ASR re-emits each line as the caption scrolls; keep first occurrence only. */
function dedupeRolling(cues) {
  const out = [];
  let repeated = 0;
  let total = 0;
  for (const cue of cues) {
    for (const line of cue.lines) {
      total += 1;
      if (out.length && out[out.length - 1].text === line) {
        repeated += 1;
        continue;
      }
      out.push({ text: line, start: cue.start });
    }
  }
  return { lines: out, repeatedRatio: total ? repeated / total : 0 };
}

function chunk(lines, videoId) {
  const chunks = [];
  let buf = [];
  let startCue = lines.length ? lines[0].start : "00:00:00.000";
  const flush = () => {
    if (!buf.length) return;
    const text = buf.join(" ").replace(/\s+/g, " ").trim();
    chunks.push({
      chunk_id: stableUuid(`${videoId}:${chunks.length}`),
      index: chunks.length,
      starts_at: startCue,
      char_count: text.length, // offsets in source_refs are CHUNK-RELATIVE
      text,
    });
    buf = [];
  };
  for (const line of lines) {
    if (!buf.length) startCue = line.start;
    buf.push(line.text);
    if (buf.join(" ").length >= TARGET_CHARS) flush();
  }
  flush();
  return chunks;
}

const [, , inPath, videoId, language, outPath] = process.argv;
const cues = parseVtt(readFileSync(inPath, "utf8"));
const { lines, repeatedRatio } = dedupeRolling(cues);
const chunks = chunk(lines, videoId);
const words = chunks.reduce(
  (n, c) => n + c.text.split(/\s+/).filter(Boolean).length,
  0,
);

writeFileSync(
  outPath,
  JSON.stringify(
    {
      video_id: videoId,
      language,
      source: "captions",
      eligibility: {
        words,
        repeated_line_ratio: Number(repeatedRatio.toFixed(3)),
        cues: cues.length,
      },
      chunks,
    },
    null,
    2,
  ) + "\n",
  "utf8",
);
console.log(
  `${videoId}: ${cues.length} cues -> ${lines.length} lines -> ${chunks.length} chunks, ${words} words, repeated ${(repeatedRatio * 100).toFixed(1)}%`,
);

// PROTOTYPE (ticket #15). Derives the failure cases from the one hand-authored valid note
// and inlines everything into a single self-contained HTML file that opens by double-click.
// Usage: node build-prototype.mjs

import { readFileSync, writeFileSync } from "node:fs";

const transcript = JSON.parse(
  readFileSync("./fixtures/en-neural-network.transcript.json", "utf8"),
);
const valid = JSON.parse(
  readFileSync("./fixtures/en-neural-network.note-valid.json", "utf8"),
);
const clone = () => structuredClone(valid);

// Each case perturbs exactly one thing, so the walkthroughs isolate one failure family each.
const cases = {};

cases.happy = {
  label: "a clean note",
  blurb:
    "Everything the model returned is within limits and every quote is real.",
  note: valid,
};

cases.repairable = (() => {
  const n = clone();
  // 1. six copies of the one real action, over the cap of five
  n.practical_actions = Array.from({ length: 6 }, (_, i) => ({
    ...structuredClone(valid.practical_actions[0]),
    id: `pa${i + 1}`,
  }));
  // 2. an id that does not start with a letter
  const edges = n.diagram.nodes.find((x) => x.id === "n_edges");
  edges.id = "9edges";
  n.diagram.edges.find((e) => e.id === "e11").to = "9edges";
  // 3. a collapsed group whose members never got their group_id stamped
  n.diagram.collapsed_groups = [
    { id: "grp_params", anchor_id: "n_params", member_ids: ["n_learning"] },
  ];
  return {
    label: "small mechanical mistakes",
    blurb:
      "Six practical actions instead of five, an element id starting with a digit, and a collapsed group whose member was never tagged. None of this needs the model.",
    note: n,
  };
})();

cases.hallucinated = (() => {
  const n = clone();
  n.key_points[2].source_refs[0].quote =
    "each layer doubles the accuracy of the one before it";
  return {
    label: "a made-up quote",
    blurb:
      "The model wrote a plausible sentence and cited it as if the speaker said it. He did not.",
    note: n,
  };
})();

cases.notEnglish = (() => {
  const n = clone();
  n.summary.text =
    "यह वीडियो बताता है कि न्यूरल नेटवर्क हस्तलिखित अंकों को कैसे पहचानता है।";
  return {
    label: "the wrong language",
    blurb:
      "The note itself came back in the transcript's language instead of English.",
    note: n,
  };
})();

cases.brokenTree = (() => {
  const n = clone();
  n.diagram.edges = n.diagram.edges.filter((e) => e.id !== "e11");
  return {
    label: "a broken mind map",
    blurb:
      "One branch was left dangling, so the mind map is no longer a single tree from the root.",
    note: n,
  };
})();

cases.tooFewPoints = (() => {
  const n = clone();
  n.key_points = n.key_points.slice(0, 4);
  return {
    label: "not enough key points",
    blurb: "Four key points came back where the note needs five to seven.",
    note: n,
  };
})();

const logic = readFileSync("./validate-note.mjs", "utf8").replace(
  /^export /gm,
  "",
);

const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Note generation: what happens when the model gets it wrong</title>
<style>
  :root {
    --ink: #16181d; --dim: #6b7280; --line: #e3e6ec; --bg: #f7f8fa;
    --card: #ffffff; --accent: #3b5bdb; --good: #1a7f5a; --bad: #b4232c;
    --warn: #96601b;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 40px 24px 80px; background: var(--bg); color: var(--ink);
    font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
  }
  main { max-width: 900px; margin: 0 auto; }
  h1 { font-size: 26px; margin: 0 0 8px; letter-spacing: -0.01em; }
  .lede { color: var(--dim); margin: 0 0 32px; max-width: 66ch; }
  h2 { font-size: 13px; text-transform: uppercase; letter-spacing: 0.07em;
       color: var(--dim); margin: 36px 0 12px; font-weight: 600; }
  .card { background: var(--card); border: 1px solid var(--line);
          border-radius: 10px; padding: 20px 22px; }
  .row { display: flex; flex-wrap: wrap; gap: 8px; }
  button {
    font: inherit; font-size: 14px; padding: 8px 14px; border-radius: 7px;
    border: 1px solid var(--line); background: #fff; color: var(--ink); cursor: pointer;
  }
  button:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
  button:disabled { opacity: 0.4; cursor: not-allowed; }
  button.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
  button.primary:hover:not(:disabled) { color: #fff; filter: brightness(1.08); }
  button.done { background: #eef1fb; border-color: #c7d0f2; color: var(--dim); }
  .state { display: grid; grid-template-columns: 150px 1fr; gap: 10px 18px; }
  .state dt { color: var(--dim); font-size: 13px; }
  .state dd { margin: 0; font-variant-numeric: tabular-nums; }
  .pill { display: inline-block; padding: 2px 10px; border-radius: 20px;
          font-size: 13px; font-weight: 600; }
  .pill.ready { background: #e4f4ec; color: var(--good); }
  .pill.reask { background: #fdf1de; color: var(--warn); }
  .pill.failed { background: #fbe6e7; color: var(--bad); }
  .pill.idle { background: #eef0f4; color: var(--dim); }
  ul.findings { list-style: none; margin: 0; padding: 0; }
  ul.findings li { padding: 7px 0; border-top: 1px solid var(--line); font-size: 14px; }
  ul.findings li:first-child { border-top: 0; }
  .tag { font-size: 11px; font-weight: 700; text-transform: uppercase;
         letter-spacing: 0.05em; padding: 2px 7px; border-radius: 4px; margin-right: 8px; }
  .tag.fix { background: #e4f4ec; color: var(--good); }
  .tag.ask { background: #fdf1de; color: var(--warn); }
  .where { color: var(--dim); }
  .tabs { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: -1px; }
  .tabs button { border-radius: 8px 8px 0 0; border-bottom-color: transparent; }
  .tabs button[aria-selected="true"] { background: var(--card); color: var(--accent);
    border-color: var(--line); border-bottom-color: var(--card); font-weight: 600; }
  .panel { border-radius: 0 10px 10px 10px; }
  .panel p { margin: 0 0 16px; color: var(--dim); max-width: 62ch; }
  ol.steps { margin: 0; padding: 0; list-style: none; counter-reset: s; }
  ol.steps li { counter-increment: s; display: flex; align-items: center;
                gap: 12px; padding: 5px 0; }
  ol.steps li::before { content: counter(s); flex: none; width: 22px; height: 22px;
    border-radius: 50%; background: #eef0f4; color: var(--dim); font-size: 12px;
    font-weight: 700; display: grid; place-items: center; }
  .note { color: var(--dim); font-size: 13px; margin-top: 14px; }
  code { font: 13px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
         background: #f0f2f6; padding: 1px 5px; border-radius: 4px; }
</style>
</head>
<body>
<main>
  <h1>Note generation: what happens when the model gets it wrong</h1>
  <p class="lede">
    The note model is asked for one JSON object. Constrained decoding guarantees the
    <em>shape</em> is right, but nothing more &mdash; not the word limits, not how many key
    points, not whether the diagram is a real tree, and above all not whether the quotes it
    cites were ever actually said. This demo asks: <strong>when the model gets one of those
    wrong, can we fix it ourselves, or do we have to pay to ask again?</strong>
    Everything below runs against a real transcript of a real 18-minute video.
  </p>

  <h2>Where things stand</h2>
  <div class="card">
    <dl class="state">
      <dt>Model returned</dt><dd id="s-case">nothing yet</dd>
      <dt>Verdict</dt><dd id="s-outcome"><span class="pill idle">not checked</span></dd>
      <dt>Re-asks used</dt><dd id="s-reasks">0 of 1</dd>
      <dt>Quotes checked</dt><dd id="s-refs">&mdash;</dd>
    </dl>
    <div id="s-detail"></div>
  </div>

  <h2>Try anything</h2>
  <div class="card">
    <div class="row" id="free"></div>
    <div class="row" style="margin-top:12px" id="free2"></div>
  </div>

  <h2>Walkthroughs</h2>
  <div class="tabs" id="tabs"></div>
  <div class="card panel" id="panel"></div>
</main>

<script>
${logic}

const TRANSCRIPT = ${JSON.stringify(transcript)};
const CASES = ${JSON.stringify(
  Object.fromEntries(
    Object.entries(cases).map(([k, v]) => [
      k,
      { label: v.label, blurb: v.blurb, note: v.note },
    ]),
  ),
)};

// ---- page state (throwaway shell; the logic above is the part worth keeping)
let S = { caseKey: null, note: null, result: null, reasksUsed: 0 };

function reset() { S = { caseKey: null, note: null, result: null, reasksUsed: 0 }; render(); }

function receive(key) {
  S.caseKey = key;
  S.note = structuredClone(CASES[key].note);
  S.result = null;
  render();
}

function check() {
  if (!S.note) return;
  S.result = runValidationCycle(S.note, TRANSCRIPT, { reasksUsed: S.reasksUsed, maxReasks: 1 });
  S.reasksUsed = S.result.reasksUsed;
  if (S.result.outcome === "ready") S.note = S.result.note;
  render();
}

function reask(key) {
  S.caseKey = key;
  S.note = structuredClone(CASES[key].note);
  S.result = null;
  render();
}

function countRefs(note) {
  let n = 0;
  const walk = (o) => {
    if (Array.isArray(o)) return o.forEach(walk);
    if (o && typeof o === "object") {
      if (Array.isArray(o.source_refs)) n += o.source_refs.length;
      Object.values(o).forEach(walk);
    }
  };
  walk(note);
  return n;
}

function render() {
  const c = S.caseKey ? CASES[S.caseKey] : null;
  document.getElementById("s-case").textContent = c ? c.label : "nothing yet";

  const out = document.getElementById("s-outcome");
  if (!S.result) {
    out.innerHTML = '<span class="pill idle">not checked</span>';
  } else if (S.result.outcome === "ready") {
    out.innerHTML = '<span class="pill ready">ready to save</span>';
  } else if (S.result.outcome === "re-ask") {
    out.innerHTML = '<span class="pill reask">must ask the model again</span>';
  } else {
    out.innerHTML = '<span class="pill failed">generation failed</span>';
  }

  document.getElementById("s-reasks").textContent = S.reasksUsed + " of 1";
  document.getElementById("s-refs").textContent = S.note
    ? countRefs(S.note) + " quotes in this note"
    : "\\u2014";

  const detail = document.getElementById("s-detail");
  detail.innerHTML = "";
  if (c && !S.result) {
    const p = document.createElement("p");
    p.className = "note";
    p.textContent = c.blurb;
    detail.appendChild(p);
  }
  if (S.result) {
    const repairs = S.result.steps.flatMap((s) => s.applied ?? []);
    const last = [...S.result.steps].reverse().find((s) => s.findings);
    const findings = S.result.outcome === "ready" ? [] : (last?.findings ?? []);
    if (repairs.length) {
      detail.innerHTML += '<h2 style="margin-top:22px">Fixed without asking</h2>';
      const ul = document.createElement("ul");
      ul.className = "findings";
      for (const r of repairs) {
        ul.innerHTML += '<li><span class="tag fix">fixed</span>' + r + "</li>";
      }
      detail.appendChild(ul);
    }
    if (findings.length) {
      detail.innerHTML += '<h2 style="margin-top:22px">Only the model can fix these</h2>';
      const ul = document.createElement("ul");
      ul.className = "findings";
      for (const f of findings) {
        ul.innerHTML +=
          '<li><span class="tag ask">re-ask</span>' + f.message +
          ' <span class="where">(' + f.path + ")</span></li>";
      }
      detail.appendChild(ul);
    }
    if (S.result.outcome === "ready" && !repairs.length) {
      detail.innerHTML += '<p class="note">Nothing to fix. Every quote was found in the chunk it named.</p>';
    }
    if (S.result.outcome === "failed") {
      detail.innerHTML +=
        '<p class="note">The re-ask budget is spent. Under spec 8.4 this is a <em>validation</em> ' +
        "failure, which is permanent: the stage fails now rather than waiting out the " +
        "30 second, 2 minute and 10 minute retry ladder, which exists for network trouble.</p>";
    }
  }

  // free play
  const free = document.getElementById("free");
  free.innerHTML = "";
  for (const [k, v] of Object.entries(CASES)) {
    const b = document.createElement("button");
    b.textContent = "Model returns " + v.label;
    b.onclick = () => receive(k);
    free.appendChild(b);
  }
  const free2 = document.getElementById("free2");
  free2.innerHTML = "";
  const checkBtn = document.createElement("button");
  checkBtn.className = "primary";
  checkBtn.textContent = "Run the checks";
  checkBtn.disabled = !S.note;
  checkBtn.onclick = check;
  free2.appendChild(checkBtn);
  const resetBtn = document.createElement("button");
  resetBtn.textContent = "Start over";
  resetBtn.onclick = reset;
  free2.appendChild(resetBtn);

  renderPanel();
}

// ---- walkthroughs
const WALKS = [
  {
    id: "clean",
    name: "Clean run",
    text:
      "The ordinary case. The model returns a note that is already within every limit, and " +
      "all 34 of its quotes are found verbatim in the chunks they cite. Watch the quote " +
      "count: that number is the thing nobody has to take on trust.",
    steps: [
      ["Model returns a clean note", () => receive("happy")],
      ["Run the checks", check],
    ],
  },
  {
    id: "free",
    name: "Fixed for free",
    text:
      "Three mechanical mistakes at once: too many practical actions, an element id starting " +
      "with a digit, and a collapsed group whose member was never tagged. Every one of these " +
      "has exactly one correct fix, so the worker applies it and moves on. No second call, no cost.",
    steps: [
      ["Model returns small mechanical mistakes", () => receive("repairable")],
      ["Run the checks", check],
    ],
  },
  {
    id: "quote",
    name: "The made-up quote",
    text:
      "The interesting one. The model attributes a sentence to the speaker that he never said. " +
      "No amount of local cleverness can repair an invented claim, but because the model had to " +
      "quote rather than just point at a chunk, the worker can prove the quote is absent " +
      "instead of trusting it. That proof is the whole reason grounding is quote-based.",
    steps: [
      ["Model returns a made-up quote", () => receive("hallucinated")],
      ["Run the checks", check],
      ["Ask the model again", () => reask("happy")],
      ["Run the checks", check],
    ],
  },
  {
    id: "lang",
    name: "Wrong language",
    text:
      "The transcript is Hindi or Hinglish and the note comes back in the source language " +
      "instead of English. Translation is a content act, so it costs a re-ask.",
    steps: [
      ["Model returns the wrong language", () => receive("notEnglish")],
      ["Run the checks", check],
      ["Ask the model again", () => reask("happy")],
      ["Run the checks", check],
    ],
  },
  {
    id: "shape",
    name: "Broken diagram",
    text:
      "A mind map has to be one tree growing out of a single root. Here a branch was left " +
      "dangling. Reattaching it would mean guessing where the model meant it to go, so this " +
      "is a re-ask, not a repair.",
    steps: [
      ["Model returns a broken mind map", () => receive("brokenTree")],
      ["Run the checks", check],
    ],
  },
  {
    id: "budget",
    name: "Out of budget",
    text:
      "What failure looks like. A made-up quote, then a second attempt that is still wrong. " +
      "The budget is one re-ask, so the stage fails immediately rather than sitting in the " +
      "transient-error retry ladder that exists for network problems.",
    steps: [
      ["Model returns a made-up quote", () => receive("hallucinated")],
      ["Run the checks", check],
      ["Ask again, still wrong", () => reask("tooFewPoints")],
      ["Run the checks", check],
    ],
  },
];

let walk = WALKS[0].id;
let walkStep = 0;

function renderPanel() {
  const tabs = document.getElementById("tabs");
  tabs.innerHTML = "";
  for (const w of WALKS) {
    const b = document.createElement("button");
    b.textContent = w.name;
    b.setAttribute("aria-selected", String(w.id === walk));
    b.onclick = () => { walk = w.id; walkStep = 0; reset(); };
    tabs.appendChild(b);
  }
  const w = WALKS.find((x) => x.id === walk);
  const panel = document.getElementById("panel");
  panel.innerHTML = "<p>" + w.text + "</p>";
  const ol = document.createElement("ol");
  ol.className = "steps";
  w.steps.forEach(([label, fn], i) => {
    const li = document.createElement("li");
    const b = document.createElement("button");
    b.textContent = label;
    if (i < walkStep) b.className = "done";
    else if (i === walkStep) b.className = "primary";
    b.disabled = i !== walkStep;
    b.onclick = () => { walkStep = i + 1; fn(); };
    li.appendChild(b);
    ol.appendChild(li);
  });
  panel.appendChild(ol);
  if (walkStep >= w.steps.length) {
    panel.innerHTML += '<p class="note">Walkthrough finished. Pick another tab, or poke at it yourself above.</p>';
  }
}

render();
</script>
</body>
</html>
`;

writeFileSync("./generation-prototype.html", html, "utf8");
console.log(
  `generation-prototype.html written (${(html.length / 1024).toFixed(0)} KB, ${Object.keys(cases).length} cases)`,
);

// Quick sanity pass so a broken case never ships silently.
const { validateNote, runValidationCycle } =
  await import("./validate-note.mjs");
for (const [key, c] of Object.entries(cases)) {
  const r = runValidationCycle(c.note, transcript, { maxReasks: 1 });
  const codes = [
    ...new Set(validateNote(c.note, transcript).map((f) => f.code)),
  ];
  console.log(
    `  ${key.padEnd(13)} -> ${r.outcome.padEnd(7)} ${codes.join(", ") || "(clean)"}`,
  );
}

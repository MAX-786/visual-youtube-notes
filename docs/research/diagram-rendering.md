# Diagram rendering in Svelte 5: library comparison and recommendation

Resolves wayfinder research ticket [#6](https://github.com/MAX-786/visual-youtube-notes/issues/6). Facts checked on 2026-09-04 against primary sources only (official docs, GitHub repos, the npm registry, MDN). Every claim carries a URL; anything that could not be verified is marked **unverified**. npm facts were read with `npm view` (the npmjs.com HTML pages return 403 to fetchers; the registry is the same data).

## 1. What the spec demands of the renderer

From `docs/feature-spec.md` §7.2, §12, §13, §15:

- Four Diagram types, one per Revision: mind map (default), process flow, timeline, comparison matrix. The canonical form is JSON (`nodes`/`edges`/`layout{element_id:{x,y}}` for graphs; `events[]` for timelines; `columns`/`rows`/`cells` for comparisons). Never a bitmap.
- Mind maps and process flows need touch pan/zoom, automatic layout, and persisted positions. Timelines need ordered event editing; comparisons need row order and cell editing.
- All diagram text is selectable and searchable. A structured Text alternative exposes the same hierarchy without canvas position.
- Hard limits: 12 initially visible elements, 24 total including Collapsed groups, comparison at most 4 columns x 6 rows.
- Pan, zoom, and node movement must stay responsive on the target Android tablet; touch targets >= 44 CSS px; keyboard navigation on desktop; light/dark theme.
- Export: PDF with diagrams as vector graphics; Markdown export is a ZIP with local SVG assets.
- Stack: Svelte 5 + SvelteKit + TypeScript, installable PWA.

Two consequences shape everything below:

1. The element count is tiny (<= 24). Layout and rendering cost is a non-issue for any candidate; what matters is interaction quality, text handling, export, and how much library surface goes unused.
2. Export needs a **standalone SVG with real `<text>`**, because non-browser SVG consumers do not render HTML embedded via `<foreignObject>` (see §3.11). Whatever draws nodes on screen as HTML must be paired with a second, pure-SVG renderer for export.

## 2. Candidates

| Candidate | Role | Version (published) | License | Svelte 5 / runes | Size |
|---|---|---|---|---|---|
| [`@xyflow/svelte`](https://github.com/xyflow/xyflow) (Svelte Flow) | Interactive node canvas: pan/zoom, drag, selection, keyboard a11y | 1.6.6 (2026-09-01); `2.0.0-next.3` prerelease exists, no public notes (**what v2 changes: unverified**) | MIT | Yes: peer `svelte ^5.25.0`; v1.0 (2025-05-14) "is based on Svelte 5" ([whats-new](https://svelteflow.dev/whats-new)); component declares `nodes = $bindable([])` ([source](https://github.com/xyflow/xyflow/blob/main/packages/svelte/src/lib/container/SvelteFlow/SvelteFlow.svelte)) | Package unpacked 333 KB; runtime dep `@xyflow/system@0.0.82` = 34.6 KB min+gzip incl. d3-zoom/drag/selection/interpolate/transition ([bundlephobia](https://bundlephobia.com/package/@xyflow/system@0.0.82)); the Svelte package's own gzip size **unverified** (bundlephobia returned 429) |
| [`@dagrejs/dagre`](https://github.com/dagrejs/dagre) | Layered (Sugiyama) layout for process flows and trees | 3.1.1 (2026-08-08) | MIT | Plain JS, sync; framework-agnostic | 15.8 KB min+gzip ([bundlephobia](https://bundlephobia.com/package/@dagrejs/dagre@3.1.1)) |
| [`elkjs`](https://github.com/kieler/elkjs) | Layered, radial, mrtree, force, stress layouts (GWT build of Eclipse ELK) | 0.12.0 (2026-07-17) | `EPL-2.0 OR GPL-3.0-or-later` ([package.json](https://github.com/kieler/elkjs/blob/master/package.json), [LICENSE.md](https://github.com/kieler/elkjs/blob/master/LICENSE.md)) | Plain JS, Promise API; optional Web Worker | 1.6 MB minified worker; ~433 KB min+gzip ([bundlephobia](https://bundlephobia.com/package/elkjs@0.12.0)) |
| [`d3-hierarchy`](https://github.com/d3/d3-hierarchy) (+ [`d3-shape`](https://github.com/d3/d3-shape) for link paths) | Tidy tree / radial tree for mind maps | 3.1.2 (2022-04-02); last repo push 2025-04-08 | ISC | Plain JS, sync | 5.7 KB + 5.7 KB min+gzip ([bundlephobia](https://bundlephobia.com/package/d3-hierarchy@3.1.2)) |
| [`d3-zoom`](https://github.com/d3/d3-zoom) (+ [`d3-drag`](https://github.com/d3/d3-drag)) | Pan/zoom behaviour (what Svelte Flow uses internally) | 3.0.0 (2021); last repo push 2024-04-30 | ISC | Plain JS; needs d3-selection | 15.5 KB min+gzip with deps ([bundlephobia](https://bundlephobia.com/package/d3-zoom@3.0.0)) |
| [`@panzoom/panzoom`](https://github.com/timmywil/panzoom) | Dependency-free pan/zoom for HTML or SVG, Pointer Events | 4.6.2 (2026-04-02); last push 2026-09-02 | MIT | Plain JS | README says ~3.7 KB gzip (**not independently measured**) |
| [`panzoom`](https://github.com/anvaka/panzoom) (anvaka) | Pan/zoom with pinch, SVG-aware | 9.4.4 (2026-03-26) | MIT | Plain JS; deps amator, ngraph.events, wheel | **unverified** (429) |
| [`svg-pan-zoom`](https://github.com/bumbu/svg-pan-zoom) | SVG-only pan/zoom | 3.6.2 (2024-10-20) | BSD-2-Clause | Plain JS | **unverified** |
| [`svelvet`](https://github.com/open-source-labs/Svelvet) | Svelte node editor | 11.0.5 (2025-02-18); last push 2025-02-13 | MIT | **No**: peer `svelte >=3.59.2 \|\| ^4.0.0`; [issue #520 "Svelte 5 / Runes support"](https://github.com/open-source-labs/Svelvet/issues/520) open since 2024 | n/a |
| Custom SVG renderer (Svelte 5 components) | Draw nodes/edges from JSON as `<svg>`; layout and pan/zoom from the rows above | Svelte 5.57.0 (2026-08-28) | MIT | Native | Only what we write |

Other layout engines checked and set aside: [`d3-dag`](https://github.com/erikbrinkman/d3-dag) 1.2.2 (MIT, active, 41.9 KB gzip) is DAG-only, so it cannot take a process flow with a loop; [`@antv/hierarchy`](https://github.com/antvis/hierarchy) 0.7.1 (MIT, 3.8 KB gzip) has an XMind-style `mindmap` layout with per-node `getWidth/getHeight`, which d3-hierarchy lacks, but the project is small (300 stars, 3 open issues) and trees-only; [`@antv/layout`](https://github.com/antvis/layout) 2.0.0 wraps the unmaintained `dagre@0.8.5` and weighs 96 KB gzip. No Svelte-5-native (runes-based) diagram library other than Svelte Flow was found on npm or GitHub.

## 3. Criterion-by-criterion

### 3.1 Svelte 5 runes compatibility

- **Svelte Flow 1.x** is Svelte-5-only. The migration guide replaces writable stores with `let nodes = $state.raw([...])` and requires two-way binding `<SvelteFlow bind:nodes bind:edges />`; nodes and edges "are to be treated as immutable from now on" (update by reassigning a new array), and hooks return objects with `.current`. Source: https://svelteflow.dev/learn/troubleshooting/migrate-to-v1
- **Svelvet** targets Svelte 3/4 only; its Svelte 5 issue is open with no maintainer response since 2024 and the repo's last push was 2025-02-13. Not a candidate.
- **Custom renderer**: Svelte 5 provides what a small canvas needs. Readonly `bind:clientWidth`/`bind:clientHeight`/`bind:contentRect` are "measured with a `ResizeObserver`" (https://svelte.dev/docs/svelte/bind); `{@attach}` attachments "run in an effect when an element is mounted to the DOM" and can return a teardown, replacing `use:` actions for wiring pan/zoom libraries to an element (Svelte >= 5.29; https://svelte.dev/docs/svelte/@attach); `<svelte:options namespace="svg">` exists for components that render inside an `<svg>` (https://svelte.dev/docs/svelte/svelte-options). `render()` from `svelte/server` "takes a component and returns an object with `body` and `head`" and is "only available on the server" (https://svelte.dev/docs/svelte/svelte-server), which is how the same component can produce export SVG on the server.
- Svelte 5 still runs legacy (non-runes) components alongside runes components per component (https://svelte.dev/docs/svelte/legacy-overview), so dagre/d3 code needs nothing special either way.

### 3.2 Automatic layout

Svelte Flow does **no layout itself**: "While we could build some basic layouting into Svelte Flow, we believe that you know your app's requirements best" (https://svelteflow.dev/learn/layouting/overview). Its guide recommends dagre for trees, d3-hierarchy for single-root trees (with the caveat that it "assigns the same width and height to all nodes"), and describes ELK as "the most configurable option available, but it's also the most complicated". The "Auto Layout" example is Pro-gated (https://svelteflow.dev/examples/layout/auto-layout). So the layout question is the same whether or not Svelte Flow is used.

| Need | `@dagrejs/dagre` | `elkjs` | `d3-hierarchy` |
|---|---|---|---|
| Process flow, LR/TB, may contain cycles | `rankdir: TB\|BT\|LR\|RL`, `ranker: network-simplex\|tight-tree\|longest-path`; cycles are always broken (DFS feedback-arc set by default, `acyclicer: 'greedy'` optional) then restored ([wiki](https://github.com/dagrejs/dagre/wiki)) | `org.eclipse.elk.layered` with `elk.direction`; "Cycle Breaking Strategy" default GREEDY ([reference](https://eclipse.dev/elk/reference/algorithms/org-eclipse-elk-layered.html)) | Not applicable (trees only) |
| Mind map, radial or tree from a root | Tree via `layered` LR/TB; no radial | `org.eclipse.elk.radial` ("places the nodes in radial order around the root"), `org.eclipse.elk.mrtree` ([algorithms](https://eclipse.dev/elk/reference/algorithms.html)) | `tree()` (Reingold-Tilford); radial by `size([2*Math.PI, radius])` plus a depth-scaled `separation`, then polar-to-Cartesian ([docs](https://d3js.org/d3-hierarchy/tree), [gallery example](https://observablehq.com/@d3/radial-tree/2)) |
| Sync/async | Sync, in place: `dagre.layout(g)`; output `x`/`y` is the node **centre** | Promise: `elk.layout(graph)`; optional Web Worker via `workerUrl` so layout does not "freeze your UI" ([README](https://github.com/kieler/elkjs)) | Sync |
| Per-node width/height input | Required (`NodeLabel { width; height }`) | Yes (`width`/`height` on each node) | No: one uniform `nodeSize` plus `separation()`; variable-width labels are handled by choosing a large enough node size |
| Types | Bundled (3.x) | Bundled | `@types/d3-hierarchy` |
| Maintenance | 5.8k stars, last push 2026-08-08; README: "only the one in the DagreJs org is receiving updates" (legacy `dagre@0.8.5` is from 2019) | 2.8k stars, last push 2026-08-13; releases track upstream ELK minor versions; README FAQ lists recurring bundler issues (`web-worker` resolution, GWT globals) | 1.3k stars, last push 2025-04-08, 18 open issues; stable rather than active |

Verdict: dagre covers the process flow (and a non-radial mind map) at 16 KB; d3-hierarchy adds a radial mind map at 6 KB. elkjs is the only single library that does both radial and layered, but costs ~433 KB gzip, an EPL/GPL dual license, and a worker setup, for graphs of at most 24 nodes. All three need node dimensions first, which means measuring label boxes before layout (§3.9).

### 3.3 Persisting node positions

- **Svelte Flow**: `Node.position` is "Position of a node on the pane" (https://svelteflow.dev/api-reference/types/node); with `bind:nodes` a drag writes the new position back into the bound `$state.raw` array, and `onnodedragstop` fires "when a user stops dragging a node" (https://svelteflow.dev/api-reference/svelte-flow). Serialising the array gives the spec's `layout{element_id:{x,y}}`.
- **Custom renderer**: positions live in our own `$state` mirror of `layout`; a drag updates it and an `$effect` or explicit save writes it back. Nothing library-specific.
- Either way the layout engine's output must be normalised: dagre returns centre coordinates, elkjs and Svelte Flow use top-left (the official dagre example converts with `x - nodeWidth / 2`, https://svelteflow.dev/examples/layout/dagre). Persist one convention (top-left of the node box is the least surprising for SVG `<rect>`).
- Open product question (not a library question): the first auto-layout produces positions that the generator did not; writing them back means either a `manual` Revision on first view or storing `layout` outside the immutable Revision. Flag for the spec owner.

### 3.4 Touch pan/zoom on Android Chrome

- **Svelte Flow** delegates to d3-zoom (`XYPanZoom.ts` imports `zoom, zoomTransform` from `d3-zoom`; [source](https://github.com/xyflow/xyflow/blob/main/packages/system/src/xypanzoom/XYPanZoom.ts)) and node drag to d3-drag, which aborts when a second touch appears ([XYDrag.ts](https://github.com/xyflow/xyflow/blob/main/packages/system/src/xydrag/XYDrag.ts)). Props: `panOnDrag` (default true), `zoomOnPinch` ("Controls if the viewport should zoom by pinching on a touch screen", default true), `zoomOnScroll`, `panOnScroll`, `minZoom` 0.5, `maxZoom` 2 (https://svelteflow.dev/api-reference/svelte-flow). The pane carries `touch-action: none` ([init.css](https://github.com/xyflow/xyflow/blob/main/packages/system/src/styles/init.css)). Touch fixes keep landing (1.6.3: "Fix selection box appearing when dragging the pane via touch"; earlier: "prevent zooming on mobile if zoomOnPinch is false", "abort drag when multiple touches are detected"; [CHANGELOG](https://github.com/xyflow/xyflow/blob/main/packages/svelte/CHANGELOG.md)). There is no tablet-specific guide, and no Android-specific statement was found (**unverified beyond the above**).
- **d3-zoom** on its own: uses touch events (touchstart/move/end), supports "pinch with touch" and double-tap, and only registers touch listeners when `navigator.maxTouchPoints || ("ontouchstart" in this)`; the docs warn "Chrome's mobile device emulator, for example, fails detection", so test on the real tablet (https://d3js.org/d3-zoom). Transforms are read with `zoomTransform(node)` (`{k, x, y}`) and set with `selection.call(zoom.transform, zoomIdentity.translate(x, y).scale(k))`, which is enough to persist and restore the viewport.
- **@panzoom/panzoom**: "uses pointer events by default wherever supported", supports pinch, pans/zooms SVG elements directly, applies CSS transforms, and exposes a `touchAction` option because "native scroll on mobile interferes with panning and pinch zooming"; `excludeClass`/`exclude` keep node drag handles out of the pan gesture; events `panzoomstart/change/zoom/end` (https://github.com/timmywil/panzoom). Dependency-free, MIT, pushed 2026-09-02.
- **Hand-rolled**: MDN documents the two primitives. `touch-action: none` "disables browser handling of all panning and zooming gestures", and "an application using Pointer events will receive a `pointercancel` event when the browser starts handling a touch gesture" unless `touch-action` says otherwise (https://developer.mozilla.org/en-US/docs/Web/CSS/touch-action). Pointer Events give each finger a `pointerId` and `isPrimary`, and `setPointerCapture()` keeps a drag on its element (https://developer.mozilla.org/en-US/docs/Web/API/Pointer_events). Two tracked pointers plus a distance/midpoint calculation is a pinch. This is roughly 150 lines and has no dependency, but reimplements what the libraries above already test on real devices.

Verdict: all three routes work on Android Chrome in principle; the quality difference is in the details (momentum, double-tap, drag-vs-pan disambiguation on a node). Svelte Flow has the longest track record here; @panzoom/panzoom is the lightest maintained option for a custom canvas; d3-zoom is proven but adds d3-selection/transition and touch-event quirks.

### 3.5 Selectable and searchable text in the DOM

- **Svelte Flow** renders nodes as absolutely positioned HTML `<div>`s and edges as SVG ([NodeRenderer](https://github.com/xyflow/xyflow/blob/main/packages/svelte/src/lib/container/NodeRenderer/NodeRenderer.svelte), [EdgeRenderer](https://github.com/xyflow/xyflow/blob/main/packages/svelte/src/lib/container/EdgeRenderer/EdgeRenderer.svelte)). Text is in the DOM, so find-in-page and screen readers reach it, but it is **not selectable by default**: `.xy-flow__node { user-select: none }`, `.xy-flow__edge-text { user-select: none }`, `.xy-flow__edgelabel-renderer { user-select: none }` ([init.css](https://github.com/xyflow/xyflow/blob/main/packages/system/src/styles/init.css)) and `.svelte-flow__container { user-select: none }` ([base.css](https://github.com/xyflow/xyflow/blob/main/packages/svelte/src/styles/base.css)). Meeting the spec means overriding `user-select` on label elements and marking them `nodrag`/`nopan` so a text-selection gesture does not start a drag (https://svelteflow.dev/api-reference/types/node-props). On a touch screen a long-press then becomes ambiguous between "select text" and "drag node"; that interaction needs a design decision regardless of library.
- **Custom SVG renderer**: text is SVG `<text>`/`<tspan>` in the DOM, indexed by find-in-page and selectable in desktop browsers; behaviour of SVG text selection on Android Chrome touch was **not verified from a primary source** and should be part of the spike. SVG has no automatic line wrapping in the widely supported subset, so labels must be wrapped into `<tspan>`s by measurement (§3.9). The alternative, HTML labels inside `<foreignObject>`, wraps for free but breaks the export path (§3.11) and adds a second text technology.
- The spec's search requirement is satisfied server-side by Hybrid search over the Text alternative, not by DOM text, so DOM searchability is a nicety for find-in-page only.

### 3.6 Collapsed groups

- **Svelte Flow** has sub flows (`parentId`, children "positioned relative to their parent", `extent: 'parent'`, parents must precede children in the array; https://svelteflow.dev/learn/layouting/sub-flows) and a per-node `hidden` flag (https://svelteflow.dev/api-reference/types/node), and 1.6.6 hides edges whose nodes are hidden ([CHANGELOG](https://github.com/xyflow/xyflow/blob/main/packages/svelte/CHANGELOG.md)). There is **no built-in collapse/expand**; the React Flow expand/collapse example is React-only.
- **Custom renderer**: collapse is a filter over the JSON model (`group_id` on nodes, spec §7.2) before rendering. Same amount of application code either way.
- Layout advice for both: lay out all 24 elements once and persist all positions; collapsing then only hides, so expanding a group never moves already-placed nodes. Running layout only on the visible subset would reshuffle the canvas on every expand.

### 3.7 Keyboard navigation

- **Svelte Flow** ships it: since 1.0 "you can now tab through nodes and edges and move nodes with the arrow keys" (https://svelteflow.dev/whats-new); `nodesFocusable`/`edgesFocusable` ("focus between nodes can be cycled with the Tab key and selected with the Enter key"), `disableKeyboardA11y`, `autoPanOnNodeFocus`, `ariaLabelConfig` for localisable ARIA strings (https://svelteflow.dev/api-reference/svelte-flow). Node divs get `tabindex=0`, `role="group"`, `aria-roledescription="node"`, `aria-describedby` and an aria-live movement announcement; Enter/Space/Escape select and deselect; arrow keys move 1 px (4 px with Shift) ([NodeWrapper](https://github.com/xyflow/xyflow/blob/main/packages/svelte/src/lib/components/NodeWrapper/NodeWrapper.svelte), [constants](https://github.com/xyflow/xyflow/blob/main/packages/system/src/constants.ts)).
- **Custom renderer**: must implement roving `tabindex` over nodes, arrow-key traversal along edges or in reading order, Enter to edit, Delete with confirmation, and focus-follows-viewport. For 24 nodes this is small, and the spec's Text alternative (a structured list) already provides a fully keyboard-accessible view of the same content, so canvas keyboard support can be minimal in V1.

### 3.8 Bundle size

Numbers are min+gzip from bundlephobia unless stated; the target is a PWA that must be "interactive within two seconds on a typical tablet connection" (spec §15).

| Option | Diagram-related JS |
|---|---|
| Svelte Flow + dagre + d3-hierarchy | `@xyflow/system` 34.6 KB (includes d3-zoom/drag/selection/interpolate/transition) + `@xyflow/svelte` (**unverified**, estimate 15-35 KB) + dagre 15.8 KB + d3-hierarchy/shape 11.4 KB = roughly 75-95 KB |
| Custom SVG + @panzoom/panzoom + dagre + d3-hierarchy | ~3.7 KB (README figure) + 15.8 KB + 11.4 KB + our code = roughly 35-45 KB |
| Custom SVG + d3-zoom + dagre + d3-hierarchy | 15.5 KB + 15.8 KB + 11.4 KB + our code = roughly 45-55 KB |
| Any option + elkjs instead of dagre/d3-hierarchy | +433 KB (worker file 1.6 MB minified); must be lazy-loaded and run in a worker |

### 3.9 Measuring labels before layout

Every layout engine wants node width/height (dagre requires it; ELK expects it; d3-hierarchy needs a uniform `nodeSize`). Labels are AI-generated text of unknown length, so the renderer must measure before laying out. Two workable approaches, both framework-native:

- Render the labels first (hidden or at 0,0), read sizes with Svelte's `bind:contentRect`/`bind:clientWidth` (ResizeObserver-backed; https://svelte.dev/docs/svelte/bind), then run layout in an `$effect` once all sizes are known. Svelte Flow does the same internally via `node.measured` ([whats-new 1.0](https://svelteflow.dev/whats-new)).
- Fix a max label width (e.g. 180 px), wrap text into lines by measuring with a canvas `measureText` or an offscreen `<text>` `getComputedTextLength()`, and derive height from line count. Deterministic, works on the server too, and gives consistent node boxes across screen and export.

The second approach is recommended because export (§3.11) needs server-side layout without a DOM.

### 3.10 License and maintenance summary

| Library | License | Activity (as of 2026-09-04) |
|---|---|---|
| @xyflow/svelte | MIT | 38.3k stars (monorepo), last push 2026-09-02, releases every 1-3 weeks in lockstep with React Flow (1.6.6 on 2026-09-01); 130 open issues+PRs |
| @dagrejs/dagre | MIT | 5.8k stars, last push 2026-08-08, 3.x releases through 2026 |
| d3-hierarchy / d3-zoom / d3-drag | ISC | Stable d3 modules; last pushes 2025-04-08 / 2024-04-30; no unmaintained notice |
| elkjs | EPL-2.0 OR GPL-3.0-or-later | 2.8k stars, last push 2026-08-13, tracks upstream ELK |
| @panzoom/panzoom | MIT | 2.5k stars, last push 2026-09-02 |
| panzoom (anvaka) | MIT | 2.0k stars, last push 2026-03-30 |
| svg-pan-zoom | BSD-2-Clause | 2.0k stars, last push 2026-01-16, last release 2024-10-20 |
| svelvet | MIT | 2.8k stars, last push 2025-02-13, Svelte 5 unsupported |

EPL-2.0 is a weak copyleft licence; for a personal single-user tool that does not distribute the software this is not a practical problem, but it is the only non-permissive licence in the set.

### 3.11 Export path to standalone SVG (PDF and Markdown)

The spec wants vector diagrams in PDF and SVG files in the Markdown ZIP. Constraints found:

- A standalone SVG needs real vector text. `<foreignObject>` (HTML inside SVG) is "Baseline widely available" in browsers since 2015 (https://developer.mozilla.org/en-US/docs/Web/SVG/Reference/Element/foreignObject), but non-browser renderers do not honour it: resvg (Rust; used by many PDF and image pipelines) supports "only the static SVG subset" and its [support table](https://linebender.org/resvg-test-suite/svg-support-table.html) has no `foreignObject` row; `svg2pdf.js` lists "foreignObject support" under unsupported features ([issue #82](https://github.com/yWorks/svg2pdf.js/issues/82)). Only a browser engine (e.g. Playwright's `page.pdf()`, which "generates a pdf of the page with `print` css media", https://playwright.dev/docs/api/class-page#page-pdf) renders foreignObject into PDF.
- `XMLSerializer.serializeToString()` "constructs a string representing the specified DOM tree in XML form" and serialises elements "with their `namespaceURI` intact" (https://developer.mozilla.org/en-US/docs/Web/API/XMLSerializer/serializeToString), so an in-DOM `<svg>` built from `<rect>/<path>/<text>` becomes a valid `.svg` file once styles are inlined (CSS custom properties and theme classes must be resolved to literal attributes, and fonts either referenced by family with fallbacks or accepted as renderer-substituted).
- **Svelte Flow** cannot produce this: nodes are HTML divs and only edges are SVG (§3.5). Its official export example uses `html-to-image@1.11.11` to produce a **PNG** (https://svelteflow.dev/examples/misc/download-image); `html-to-image`'s `toSvg` works by wrapping the HTML in a `<foreignObject>` ([README](https://github.com/bubkoo/html-to-image/blob/master/README.md)), which hits the renderer limitation above. A Svelte Flow UI therefore needs a **second, pure-SVG renderer** for export that duplicates the node visuals.
- **Custom SVG renderer**: one Svelte component draws the diagram from JSON as pure SVG. On screen it is mounted inside a pan/zoom wrapper; for export the same component is rendered with `render()` from `svelte/server` (https://svelte.dev/docs/svelte/svelte-server) or serialised from the DOM. One visual, two outputs. PDF can then be produced either by embedding the SVG in the HTML that a headless Chromium prints, or by a server-side SVG-to-PDF tool that only needs the static subset.

## 4. Timeline and comparison matrix need no graph library

- A timeline is an ordered list (`events[{order}]`); a comparison matrix is a table (<= 4 x 6). Both are best rendered as semantic HTML (`<ol>`, `<table>`) styled to the notebook look: text is natively selectable, searchable, keyboard-navigable and screen-reader friendly, editing is ordinary form handling, and touch targets are plain CSS. Neither needs pan/zoom at these limits; a 4-column table fits tablet landscape and can scroll horizontally in portrait.
- Export: a timeline can be drawn as a simple SVG (a line with labelled ticks) by the same tiny renderer approach; a comparison matrix exports naturally as a Markdown table and as a printed HTML table in PDF, with an SVG rendition only if the ZIP format insists on one asset per diagram.

## 5. Recommendation

**Build a custom SVG renderer in Svelte 5 for mind maps and process flows, with `@dagrejs/dagre` for layered layout, `d3-hierarchy` (+ `d3-shape`) for radial/tree mind-map layout, and `@panzoom/panzoom` (or a ~150-line Pointer Events implementation) for touch pan/zoom. Render timelines and comparison matrices as HTML lists and tables. Do not adopt Svelte Flow or elkjs for V1.**

Why:

1. **Export drives the architecture.** The spec requires vector SVG for PDF and Markdown, and non-browser SVG renderers do not support `<foreignObject>`. A pure-SVG renderer is therefore mandatory for export; making it the on-screen renderer too means one component, one visual, and free selectable/searchable `<text>`. Svelte Flow would add a second renderer to maintain.
2. **The problem is small.** At most 24 nodes, no node/edge creation in V1, no handles, no connection lines, no minimap: most of Svelte Flow's surface (and most of its 35+ KB) would go unused, while the parts we do need (drag, focus, arrow keys) are small on a fixed-size graph.
3. **Layout is bring-your-own anyway.** Svelte Flow's own docs point at dagre/d3-hierarchy/ELK, so the layout decision and the label-measurement work are identical for both routes. dagre + d3-hierarchy cost 27 KB gzip and are MIT/ISC; elkjs costs 433 KB and an EPL/GPL licence for a radial layout that d3-hierarchy provides in 6 KB.
4. **Svelte 5 has the primitives.** `bind:contentRect` for measurement, `{@attach}` for wiring pan/zoom to an element, `$state`/`$effect` for positions, and `svelte/server` `render()` for server-side SVG.

Trade-offs accepted:

- **We own the interaction code.** Node drag vs pan disambiguation, pinch maths (if hand-rolled), double-tap, keyboard focus and ARIA on the canvas, and text wrapping into `<tspan>`s are ours to write and test on the tablet. Svelte Flow has years of touch bug-fixes we forgo. Mitigation: use `@panzoom/panzoom` (Pointer Events, `excludeClass` for node handles, active maintenance) rather than hand-rolling; keep the canvas keyboard model minimal and lean on the Text alternative for full keyboard access in V1.
- **SVG text does not wrap.** Labels must be measured and wrapped by code; a fixed max width and canvas `measureText` keep this deterministic and server-runnable.
- **d3-hierarchy uses a uniform node size.** Mind-map nodes get one box size (or a per-depth size); with <= 24 short labels this is acceptable, and `@antv/hierarchy` or dagre-LR are fallbacks if radial quality disappoints.
- **d3 modules are stable, not busy.** d3-hierarchy and d3-zoom last changed in 2022-2024; that is maturity rather than abandonment, but there is no active roadmap.

When to revisit Svelte Flow: if V1.1 adds node/edge creation, reconnection, multi-select or grouping gestures, Svelte Flow's editor features become worth their weight, and the custom SVG renderer would remain as the export renderer. Its `2.0.0-next` prerelease should be checked then.

## 6. Suggested spike (before committing)

1. Render a 24-node mind map and a 12-node process flow from spec-shaped JSON as SVG; dagre LR/TB and d3-hierarchy radial; persist `layout{}`.
2. Wire `@panzoom/panzoom` and node drag; test on the target Android tablet: pinch, two-finger pan, drag a node without panning, long-press text selection on `<text>`.
3. Serialise the same component with `svelte/server` `render()` and open the SVG in resvg or Inkscape to confirm the export is standalone and text-correct.
4. Measure the resulting bundle with the SvelteKit build analyser and compare against the ~40 KB estimate above.

Unverified items to close during the spike: SVG `<text>` selection behaviour on Android Chrome; `@xyflow/svelte`'s own gzip size (only relevant if the recommendation is overturned); what `@xyflow/svelte@2.0.0-next` changes.

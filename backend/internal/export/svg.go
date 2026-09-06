package export

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

// The canvas typography the client draws with (DiagramCanvas.svelte), so the
// export and the screen draw the same picture from the same boxes.
const (
	svgLineHeight = 20.0
	svgPadY       = 10.0
	svgMargin     = 32.0
)

// svgLight is the static palette every renderer sees. svgDark fires only in
// an on-screen viewer that prefers it, and only when the caller asks for
// it; the print document never carries it (ADR 0011: export is light-only).
const svgLight = `svg{font-family:"Noto Sans",system-ui,sans-serif;font-size:14px}` +
	`.edge{stroke:#6b7280;stroke-width:1.5;fill:none}` +
	`.arrowhead{fill:#6b7280}` +
	`.edge-label{fill:#6b7280;font-size:12px;paint-order:stroke;stroke:#ffffff;stroke-width:4px}` +
	`.node rect{fill:#ffffff;stroke:#d1d5db;stroke-width:1.5}` +
	`.node text{fill:#111827}` +
	`.node.root rect{fill:#1d4ed8;stroke:#1d4ed8}` +
	`.node.root text{fill:#ffffff;font-weight:600}` +
	`.node.claim rect{stroke-dasharray:5 4}` +
	`.node.claim text,.edge-label.claim{font-style:italic}`

const svgDark = `@media (prefers-color-scheme:dark){` +
	`.edge{stroke:#9ca3af}.arrowhead{fill:#9ca3af}` +
	`.edge-label{fill:#9ca3af;stroke:#111827}` +
	`.node rect{fill:#1f2937;stroke:#4b5563}.node text{fill:#f3f4f6}` +
	`.node.root rect{fill:#3b82f6;stroke:#3b82f6}}`

type point struct{ x, y float64 }

func center(p generation.Position) point {
	return point{p.X + p.W/2, p.Y + p.H/2}
}

// exitPoint is where a segment towards to leaves the box.
func exitPoint(box generation.Position, to point) point {
	c := center(box)
	dx, dy := to.x-c.x, to.y-c.y
	if dx == 0 && dy == 0 {
		return c
	}
	sx := box.W / 2 / math.Max(math.Abs(dx), 1e-9)
	sy := box.H / 2 / math.Max(math.Abs(dy), 1e-9)
	t := math.Min(sx, sy)
	return point{c.x + dx*t, c.y + dy*t}
}

func num(x float64) string {
	return strconv.FormatFloat(math.Round(x*100)/100, 'f', -1, 64)
}

func esc(s string) string {
	return html.EscapeString(s)
}

// DiagramSVG draws a mind map or process flow from the stored boxes, in the
// static SVG subset every renderer agrees on: no foreignObject, no external
// use, no script, no filter, text as text so it stays selectable (ADR 0011).
// Hidden members of collapsed groups are drawn, because collapse is
// presentation state and export includes it (diagram-semantics.md). A node
// the layout has no box for yet gets one the way the server would give it.
// ok is false for a timeline or a comparison, which export as a list and a
// table.
func DiagramSVG(d *generation.StoredDiagram, positions map[string]generation.Position, onScreenDark bool) (svg string, ok bool) {
	if d.Type != generation.MindMap && d.Type != generation.ProcessFlow {
		return "", false
	}
	boxes := make(map[string]generation.Position, len(d.Nodes))
	for id, p := range positions {
		boxes[id] = p
	}
	for id, p := range generation.Layout(d, boxes) {
		boxes[id] = p
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, n := range d.Nodes {
		p, found := boxes[n.ID]
		if !found {
			continue
		}
		minX, minY = math.Min(minX, p.X), math.Min(minY, p.Y)
		maxX, maxY = math.Max(maxX, p.X+p.W), math.Max(maxY, p.Y+p.H)
	}
	if math.IsInf(minX, 1) {
		minX, minY, maxX, maxY = 0, 0, 320, 200
	}
	x, y := minX-svgMargin, minY-svgMargin
	w, h := maxX-minX+2*svgMargin, maxY-minY+2*svgMargin
	name := diagramName(d.Type)
	flow := d.Type == generation.ProcessFlow

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s %s %s %s" width="%s" height="%s" role="img" aria-label="%s">`,
		num(x), num(y), num(w), num(h), num(w), num(h), esc(name))
	b.WriteString("\n<title>" + esc(name) + "</title>\n<style>" + svgLight)
	if onScreenDark {
		b.WriteString(svgDark)
	}
	b.WriteString("</style>\n")
	if flow {
		b.WriteString(`<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" class="arrowhead"/></marker></defs>` + "\n")
	}
	for _, e := range d.Edges {
		from, ok1 := boxes[e.From]
		to, ok2 := boxes[e.To]
		if !ok1 || !ok2 {
			continue
		}
		start, end := exitPoint(from, center(to)), exitPoint(to, center(from))
		marker := ""
		if flow {
			marker = ` marker-end="url(#arrow)"`
		}
		fmt.Fprintf(&b, `<line class="edge" x1="%s" y1="%s" x2="%s" y2="%s"%s/>`+"\n", num(start.x), num(start.y), num(end.x), num(end.y), marker)
		if e.Label != "" {
			class := "edge-label"
			if e.SpeakerClaim != nil && *e.SpeakerClaim {
				class += " claim"
			}
			fmt.Fprintf(&b, `<text class="%s" x="%s" y="%s" text-anchor="middle" dominant-baseline="middle">%s</text>`+"\n",
				class, num((start.x+end.x)/2), num((start.y+end.y)/2), esc(e.Label))
		}
	}
	for _, n := range d.Nodes {
		p, found := boxes[n.ID]
		if !found {
			continue
		}
		class := "node"
		if n.Kind == "root" {
			class += " root"
		}
		if n.SpeakerClaim {
			class += " claim"
		}
		cx := p.X + p.W/2
		fmt.Fprintf(&b, `<g class="%s" data-element-id="%s"><rect x="%s" y="%s" width="%s" height="%s" rx="8"/>`,
			class, esc(n.ID), num(p.X), num(p.Y), num(p.W), num(p.H))
		fmt.Fprintf(&b, `<text x="%s" y="%s" text-anchor="middle" dominant-baseline="hanging">`, num(cx), num(p.Y+svgPadY))
		for i, line := range generation.Wrap(n.Label) {
			dy := "0"
			if i > 0 {
				dy = num(svgLineHeight)
			}
			fmt.Fprintf(&b, `<tspan x="%s" dy="%s">%s</tspan>`, num(cx), dy, esc(line))
		}
		b.WriteString("</text></g>\n")
	}
	b.WriteString("</svg>\n")
	return b.String(), true
}

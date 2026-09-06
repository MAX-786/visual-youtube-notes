package generation

import (
	_ "embed"
	"math"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Noto Sans is the note font everywhere (ADR 0011): Go measures it, the PWA
// self-hosts it, Gotenberg's Chromium has it, so a box sized here is the box
// the label lands in.
//
//go:embed assets/NotoSans-Regular.ttf
var notoSans []byte

// Position is an element's box in CSS pixels: top-left corner and size.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Typography and spacing of the diagram canvas. The boxes are padded to
// tolerate metric drift on a machine that substitutes the font.
const (
	fontSize      = 14.0
	lineHeight    = 20.0
	padX          = 14.0
	padY          = 10.0
	wrapWidth     = 190.0
	minBoxWidth   = 64.0
	nodeGapX      = 36.0
	layerGap      = 56.0
	ringGap       = 64.0
	minFirstRing  = 150.0
	fallbackWidth = 0.55 * fontSize
)

type measurer struct {
	font   *sfnt.Font
	mu     sync.Mutex
	buf    sfnt.Buffer
	widths map[rune]float64
}

var theMeasurer = sync.OnceValue(func() *measurer {
	m := &measurer{widths: map[rune]float64{}}
	if f, err := sfnt.Parse(notoSans); err == nil {
		m.font = f
	}
	return m
})

func (m *measurer) advance(r rune) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.widths[r]; ok {
		return w
	}
	w := fallbackWidth
	if m.font != nil {
		if idx, err := m.font.GlyphIndex(&m.buf, r); err == nil && idx != 0 {
			if adv, err := m.font.GlyphAdvance(&m.buf, idx, fixed.I(fontSize), font.HintingNone); err == nil {
				w = float64(adv) / 64
			}
		}
	}
	m.widths[r] = w
	return w
}

// TextWidth is the advance width of s at the canvas font size.
func TextWidth(s string) float64 {
	m := theMeasurer()
	total := 0.0
	for _, r := range s {
		total += m.advance(r)
	}
	return total
}

// Wrap breaks a label into lines no wider than the wrap width, on spaces.
func Wrap(label string) []string {
	words := strings.Fields(label)
	if len(words) == 0 {
		return []string{""}
	}
	space := TextWidth(" ")
	var lines []string
	line := ""
	width := 0.0
	for _, w := range words {
		ww := TextWidth(w)
		if line != "" && width+space+ww > wrapWidth {
			lines = append(lines, line)
			line, width = w, ww
			continue
		}
		if line == "" {
			line, width = w, ww
		} else {
			line += " " + w
			width += space + ww
		}
	}
	return append(lines, line)
}

// Measure is the box a label needs.
func Measure(label string) (w, h float64) {
	lines := Wrap(label)
	widest := 0.0
	for _, l := range lines {
		widest = math.Max(widest, TextWidth(l))
	}
	return math.Max(minBoxWidth, math.Ceil(widest+2*padX)), math.Ceil(float64(len(lines))*lineHeight + 2*padY)
}

// Layout gives every node of a mind map or process flow a box, in the
// engine the payload names, and returns only the positions missing from
// existing (ADR 0011: compute only the missing ones, merge). Timelines and
// comparisons are lists and tables and need none.
func Layout(d *StoredDiagram, existing map[string]Position) map[string]Position {
	var computed map[string]Position
	switch d.Type {
	case MindMap:
		computed = radial(d)
	case ProcessFlow:
		computed = layered(d)
	default:
		return map[string]Position{}
	}
	out := map[string]Position{}
	for id, p := range computed {
		if _, ok := existing[id]; !ok {
			out[id] = p
		}
	}
	return out
}

type treeNode struct {
	id       string
	w, h     float64
	children []*treeNode
	leaves   int
	depth    int
	angle    float64
}

// radial places the root at the centre and each depth on a ring wide
// enough for the boxes on it, sectors proportional to subtree size.
func radial(d *StoredDiagram) map[string]Position {
	nodes := map[string]*treeNode{}
	var order []string
	var root *treeNode
	for _, n := range d.Nodes {
		w, h := Measure(n.Label)
		t := &treeNode{id: n.ID, w: w, h: h}
		nodes[n.ID] = t
		order = append(order, n.ID)
		if n.Kind == "root" && root == nil {
			root = t
		}
	}
	if root == nil && len(order) > 0 {
		root = nodes[order[0]]
	}
	if root == nil {
		return map[string]Position{}
	}
	edges := append([]StoredEdge{}, d.Edges...)
	sort.SliceStable(edges, func(a, b int) bool {
		oa, ob := edgeOrder(edges[a]), edgeOrder(edges[b])
		if oa != ob {
			return oa < ob
		}
		return edges[a].ID < edges[b].ID
	})
	attached := map[string]bool{root.id: true}
	for _, e := range edges {
		from, to := nodes[e.From], nodes[e.To]
		if from == nil || to == nil || attached[to.id] {
			continue
		}
		from.children = append(from.children, to)
		attached[to.id] = true
	}
	// Nodes the edges never reached hang off the root so they still get a box.
	for _, id := range order {
		if !attached[id] {
			root.children = append(root.children, nodes[id])
			attached[id] = true
		}
	}
	var count func(t *treeNode, depth int) int
	count = func(t *treeNode, depth int) int {
		t.depth = depth
		if len(t.children) == 0 {
			t.leaves = 1
			return 1
		}
		total := 0
		for _, c := range t.children {
			total += count(c, depth+1)
		}
		t.leaves = total
		return total
	}
	count(root, 0)

	// Angles first: each subtree takes a sector proportional to its leaves,
	// and a node sits in the middle of its sector.
	var assign func(t *treeNode, from, to float64)
	assign = func(t *treeNode, from, to float64) {
		t.angle = (from + to) / 2
		start := from
		for _, c := range t.children {
			span := (to - from) * float64(c.leaves) / float64(t.leaves)
			assign(c, start, start+span)
			start += span
		}
	}
	start := -math.Pi / 2
	for _, c := range root.children {
		span := 2 * math.Pi * float64(c.leaves) / float64(root.leaves)
		assign(c, start, start+span)
		start += span
	}

	// Then radii: a ring sits clear of the one inside it, and every pair of
	// neighbours on it is far enough apart for their boxes, given their
	// angles. The distance between two centres on a circle is 2r·sin(dθ/2).
	byDepth := map[int][]*treeNode{}
	var walk func(t *treeNode)
	walk = func(t *treeNode) {
		byDepth[t.depth] = append(byDepth[t.depth], t)
		for _, c := range t.children {
			walk(c)
		}
	}
	walk(root)
	maxDepth := 0
	for depth := range byDepth {
		maxDepth = max(maxDepth, depth)
	}
	radius := make([]float64, maxDepth+1)
	for depth := 1; depth <= maxDepth; depth++ {
		ring := byDepth[depth]
		sort.Slice(ring, func(a, b int) bool { return ring[a].angle < ring[b].angle })
		// Clear of the ring inside: two boxes on adjacent rings cannot
		// overlap once their centres are further apart than the diagonal of
		// their combined half-sizes, whatever the angle.
		innerW, innerH := 0.0, 0.0
		for _, t := range byDepth[depth-1] {
			innerW = math.Max(innerW, t.w)
			innerH = math.Max(innerH, t.h)
		}
		need := 0.0
		for _, t := range ring {
			need = math.Max(need, radius[depth-1]+math.Hypot((t.w+innerW)/2, (t.h+innerH)/2)+ringGap/2)
		}
		if depth == 1 {
			need = math.Max(need, minFirstRing)
		}
		if len(ring) > 1 {
			for i := range ring {
				a, b := ring[i], ring[(i+1)%len(ring)]
				gap := b.angle - a.angle
				if i == len(ring)-1 {
					gap += 2 * math.Pi
				}
				gap = math.Max(gap, 0.05)
				apart := (a.w+b.w)/2 + (a.h+b.h)/2 + nodeGapX
				need = math.Max(need, apart/(2*math.Sin(math.Min(gap, math.Pi)/2)))
			}
		}
		radius[depth] = need
	}

	positions := map[string]Position{}
	positions[root.id] = Position{X: -root.w / 2, Y: -root.h / 2, W: root.w, H: root.h}
	for depth := 1; depth <= maxDepth; depth++ {
		for _, t := range byDepth[depth] {
			r := radius[depth]
			cx, cy := r*math.Cos(t.angle), r*math.Sin(t.angle)
			positions[t.id] = Position{X: cx - t.w/2, Y: cy - t.h/2, W: t.w, H: t.h}
		}
	}
	return shiftToOrigin(positions)
}

// layered ranks nodes by longest path along forward edges, ties broken by
// order, and centres each layer; TB unless the layout says LR.
func layered(d *StoredDiagram) map[string]Position {
	index := map[string]int{}
	orderOf := map[string]int{}
	sizes := map[string][2]float64{}
	for i, n := range d.Nodes {
		index[n.ID] = i
		orderOf[n.ID] = elementOrder(n)
		w, h := Measure(n.Label)
		sizes[n.ID] = [2]float64{w, h}
	}
	ids := make([]string, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		ids = append(ids, n.ID)
	}
	sort.SliceStable(ids, func(a, b int) bool {
		if orderOf[ids[a]] != orderOf[ids[b]] {
			return orderOf[ids[a]] < orderOf[ids[b]]
		}
		return index[ids[a]] < index[ids[b]]
	})
	rank := map[string]int{}
	for _, id := range ids {
		rank[id] = 0
	}
	// Forward edges only, walked in order, so a back edge cannot loop.
	for _, id := range ids {
		for _, e := range d.Edges {
			if e.From != id {
				continue
			}
			if _, ok := rank[e.To]; !ok || orderOf[e.To] < orderOf[id] {
				continue
			}
			rank[e.To] = max(rank[e.To], rank[id]+1)
		}
	}
	layers := map[int][]string{}
	maxRank := 0
	for _, id := range ids {
		layers[rank[id]] = append(layers[rank[id]], id)
		maxRank = max(maxRank, rank[id])
	}
	horizontal := false
	if gl, ok := d.Layout.(GraphLayout); ok && (gl.Direction == "LR" || gl.Direction == "RL") {
		horizontal = true
	}
	positions := map[string]Position{}
	along := 0.0
	for r := 0; r <= maxRank; r++ {
		members := layers[r]
		total, thick := 0.0, 0.0
		for i, id := range members {
			s := sizes[id]
			if horizontal {
				total += s[1]
				thick = math.Max(thick, s[0])
			} else {
				total += s[0]
				thick = math.Max(thick, s[1])
			}
			if i > 0 {
				total += nodeGapX
			}
		}
		across := -total / 2
		for _, id := range members {
			s := sizes[id]
			if horizontal {
				positions[id] = Position{X: along, Y: across, W: s[0], H: s[1]}
				across += s[1] + nodeGapX
			} else {
				positions[id] = Position{X: across, Y: along, W: s[0], H: s[1]}
				across += s[0] + nodeGapX
			}
		}
		along += thick + layerGap
	}
	return shiftToOrigin(positions)
}

func shiftToOrigin(positions map[string]Position) map[string]Position {
	minX, minY := math.Inf(1), math.Inf(1)
	for _, p := range positions {
		minX = math.Min(minX, p.X)
		minY = math.Min(minY, p.Y)
	}
	out := make(map[string]Position, len(positions))
	for id, p := range positions {
		out[id] = Position{X: math.Round(p.X - minX), Y: math.Round(p.Y - minY), W: p.W, H: p.H}
	}
	return out
}

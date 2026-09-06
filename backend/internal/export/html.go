package export

import (
	"fmt"
	"strings"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

// printCSS pins an explicit light palette and A4 portrait throughout: the
// diagram block never splits across pages, and the Transcript appendix
// starts on a new one (ADR 0011). The @page rule, not the request, decides
// the geometry, which is why Gotenberg is told preferCssPageSize.
const printCSS = `html { color-scheme: light only; }
body { margin: 0; font-family: "Noto Sans", system-ui, sans-serif; font-size: 11pt; line-height: 1.45; color: #111827; background: #ffffff; }
h1 { font-size: 20pt; line-height: 1.2; margin: 0 0 4pt; }
h2 { font-size: 13pt; margin: 14pt 0 6pt; padding-bottom: 2pt; border-bottom: 1px solid #e5e7eb; break-after: avoid; }
.meta { color: #6b7280; font-size: 10pt; margin: 0 0 6pt; }
.meta a { color: #1d4ed8; text-decoration: none; }
.summary { font-size: 12pt; margin: 0; }
.diagram { break-inside: avoid; margin: 6pt 0; }
.diagram svg { display: block; width: 100%; height: auto; max-height: 220mm; }
ul, ol { margin: 0; padding-left: 18pt; }
li { margin: 2pt 0; }
.claim { font-style: italic; }
.badge { display: inline-block; font-size: 8pt; font-style: normal; color: #6b7280; border: 1px solid #d1d5db; border-radius: 4px; padding: 0 4pt; margin-left: 4pt; vertical-align: middle; }
table { border-collapse: collapse; width: 100%; font-size: 10pt; break-inside: auto; }
th, td { border: 1px solid #d1d5db; padding: 4pt 6pt; text-align: left; vertical-align: top; }
thead th { background: #f3f4f6; }
.timeline { list-style: none; padding-left: 12pt; border-left: 2px solid #d1d5db; }
.takeaway { white-space: pre-wrap; margin: 0; }
.transcript { break-before: page; }
.transcript p { margin: 0 0 8pt; }
`

func cssString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.Join(strings.Fields(s), " ")
	return `"` + s + `"`
}

func claimBadge(claim bool) string {
	if claim {
		return ` <span class="badge">speaker claim</span>`
	}
	return ""
}

func classAttr(claim bool) string {
	if claim {
		return ` class="claim"`
	}
	return ""
}

// PrintHTML is the static print document Gotenberg renders: A4 portrait, an
// explicit light palette, the diagram inline as vector SVG whose text stays
// selectable and searchable, a page footer from the @page margin box, and,
// when asked for, the Transcript as an appendix forced onto a new page after
// My takeaway (ADR 0011). No script and no external asset: everything the
// render needs travels in the multipart form.
func PrintHTML(doc *Document, withTranscript bool) []byte {
	p := &doc.Payload
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>" + esc(doc.Title) + "</title>\n<style>\n")
	fmt.Fprintf(&b, "@page { size: A4 portrait; margin: 18mm 16mm 22mm; @bottom-center { content: %s \" · Page \" counter(page) \" of \" counter(pages); font-family: \"Noto Sans\", sans-serif; font-size: 9px; color: #6b7280; } }\n",
		cssString(doc.Title))
	b.WriteString(printCSS)
	b.WriteString("</style>\n</head>\n<body>\n")

	fmt.Fprintf(&b, "<h1>%s</h1>\n", esc(doc.Title))
	fmt.Fprintf(&b, "<p class=\"meta\">%s · <a href=\"%s\">%s</a></p>\n", esc(metaLine(doc)), esc(doc.CanonicalURL), esc(doc.CanonicalURL))
	if doc.PrimaryTopic != "" || len(doc.Tags) > 0 {
		var parts []string
		if doc.PrimaryTopic != "" {
			parts = append(parts, "Topic: "+esc(doc.PrimaryTopic))
		}
		if len(doc.Tags) > 0 {
			parts = append(parts, "Tags: "+esc(strings.Join(doc.Tags, ", ")))
		}
		fmt.Fprintf(&b, "<p class=\"meta\">%s</p>\n", strings.Join(parts, " · "))
	}

	b.WriteString("<h2>Summary</h2>\n")
	fmt.Fprintf(&b, "<p class=\"summary%s\">%s%s</p>\n", map[bool]string{true: " claim", false: ""}[p.Summary.SpeakerClaim], esc(p.Summary.Text), claimBadge(p.Summary.SpeakerClaim))

	fmt.Fprintf(&b, "<h2>%s</h2>\n", diagramName(p.Diagram.Type))
	switch p.Diagram.Type {
	case generation.MindMap, generation.ProcessFlow:
		if svg, ok := DiagramSVG(&p.Diagram, doc.Positions, false); ok {
			b.WriteString("<figure class=\"diagram\">\n" + svg + "</figure>\n")
		}
	case generation.Timeline:
		b.WriteString("<ol class=\"timeline\">\n")
		for _, e := range sortedElements(p.Diagram.Events) {
			fmt.Fprintf(&b, "<li%s>%s%s</li>\n", classAttr(e.SpeakerClaim), esc(e.Label), claimBadge(e.SpeakerClaim))
		}
		b.WriteString("</ol>\n")
	case generation.Comparison:
		cols, rows := sortedColumns(&p.Diagram), sortedRows(&p.Diagram)
		b.WriteString("<table>\n<thead>\n<tr><th scope=\"col\">Criterion</th>")
		for _, c := range cols {
			fmt.Fprintf(&b, "<th scope=\"col\"%s>%s%s</th>", classAttr(c.SpeakerClaim), esc(c.Label), claimBadge(c.SpeakerClaim))
		}
		b.WriteString("</tr>\n</thead>\n<tbody>\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "<tr><th scope=\"row\"%s>%s%s</th>", classAttr(r.SpeakerClaim), esc(r.Label), claimBadge(r.SpeakerClaim))
			for _, c := range cols {
				cell := r.Cells[c.ID]
				fmt.Fprintf(&b, "<td%s>%s%s</td>", classAttr(cell.SpeakerClaim), esc(cell.Text), claimBadge(cell.SpeakerClaim))
			}
			b.WriteString("</tr>\n")
		}
		b.WriteString("</tbody>\n</table>\n")
	}

	section := func(title string, blocks []generation.StoredBlock) {
		if len(blocks) == 0 {
			return
		}
		fmt.Fprintf(&b, "<h2>%s</h2>\n<ul>\n", title)
		for _, blk := range blocks {
			fmt.Fprintf(&b, "<li%s>%s%s</li>\n", classAttr(blk.SpeakerClaim), esc(blk.Text), claimBadge(blk.SpeakerClaim))
		}
		b.WriteString("</ul>\n")
	}
	section("Key points", p.KeyPoints)
	section("Important details", p.ImportantDetails)
	section("What I can use", p.PracticalActions)
	if takeaway := strings.TrimSpace(doc.Takeaway); takeaway != "" {
		fmt.Fprintf(&b, "<h2>My takeaway</h2>\n<p class=\"takeaway\">%s</p>\n", esc(takeaway))
	}

	if withTranscript && doc.Transcript != nil {
		t := doc.Transcript
		fmt.Fprintf(&b, "<section class=\"transcript\" lang=\"%s\">\n<h2>Transcript</h2>\n", esc(t.LanguageCode))
		fmt.Fprintf(&b, "<p class=\"meta\">%s · language %s</p>\n", esc(sourceKindLabel(t.SourceKind)), esc(t.LanguageCode))
		for _, para := range t.Paragraphs() {
			if s := strings.TrimSpace(para); s != "" {
				fmt.Fprintf(&b, "<p>%s</p>\n", esc(s))
			}
		}
		b.WriteString("</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

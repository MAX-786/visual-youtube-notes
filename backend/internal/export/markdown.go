package export

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

func diagramName(t string) string {
	switch t {
	case generation.MindMap:
		return "Mind map"
	case generation.ProcessFlow:
		return "Process flow"
	case generation.Timeline:
		return "Timeline"
	case generation.Comparison:
		return "Comparison"
	}
	return "Diagram"
}

func sourceKindLabel(kind string) string {
	switch kind {
	case "caption_human":
		return "Human captions"
	case "caption_automatic":
		return "Automatic captions"
	case "stt":
		return "Speech-to-text"
	}
	return kind
}

func formatDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	h, m, s := seconds/3600, seconds%3600/60, seconds%60
	switch {
	case h > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case m > 0:
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d s", s)
}

func humanDate(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

func isoTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// yamlString double-quotes s for the front matter.
func yamlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// metaLine is the spec 7.1 header line: channel, duration, save date and
// publication date.
func metaLine(doc *Document) string {
	var parts []string
	if doc.Channel != "" {
		parts = append(parts, doc.Channel)
	}
	if d := formatDuration(doc.DurationSeconds); d != "" {
		parts = append(parts, d)
	}
	parts = append(parts, "Saved "+humanDate(doc.SavedAt))
	if doc.PublishedAt != nil {
		parts = append(parts, "Published "+humanDate(*doc.PublishedAt))
	}
	return strings.Join(parts, " · ")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func blockLine(text string, claim bool) string {
	t := oneLine(text)
	if claim {
		t += " *(speaker claim)*"
	}
	return t
}

func mdCell(s string) string { return strings.ReplaceAll(oneLine(s), "|", `\|`) }

func orderOf(o *int) int {
	if o == nil {
		return int(^uint(0) >> 1)
	}
	return *o
}

func sortedElements(list []generation.StoredElement) []generation.StoredElement {
	out := append([]generation.StoredElement{}, list...)
	sort.SliceStable(out, func(a, b int) bool {
		oa, ob := orderOf(out[a].Order), orderOf(out[b].Order)
		if oa != ob {
			return oa < ob
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func sortedColumns(d *generation.StoredDiagram) []generation.StoredColumn {
	out := append([]generation.StoredColumn{}, d.Columns...)
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Order != out[b].Order {
			return out[a].Order < out[b].Order
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func sortedRows(d *generation.StoredDiagram) []generation.StoredRow {
	out := append([]generation.StoredRow{}, d.Rows...)
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Order != out[b].Order {
			return out[a].Order < out[b].Order
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func comparisonTable(d *generation.StoredDiagram) string {
	cols, rows := sortedColumns(d), sortedRows(d)
	var b strings.Builder
	b.WriteString("| Criterion |")
	for _, c := range cols {
		b.WriteString(" " + mdCell(blockLine(c.Label, c.SpeakerClaim)) + " |")
	}
	b.WriteString("\n|---|")
	for range cols {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString("| " + mdCell(blockLine(r.Label, r.SpeakerClaim)) + " |")
		for _, c := range cols {
			cell := r.Cells[c.ID]
			b.WriteString(" " + mdCell(blockLine(cell.Text, cell.SpeakerClaim)) + " |")
		}
		b.WriteString("\n")
	}
	return b.String() + "\n"
}

// NoteMarkdown is note.md: YAML front matter, so the file lands in a
// Markdown knowledge tool as a first-class note, then the spec 7.1
// hierarchy. A mind map or process flow appears twice, as the local SVG and
// as its Text alternative; a timeline is a numbered list and a comparison a
// table (ADR 0011). Source refs and Provenance are never rendered.
func NoteMarkdown(doc *Document, withTranscript, hasSVG bool) string {
	p := &doc.Payload
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlString(doc.Title))
	fmt.Fprintf(&b, "channel: %s\n", yamlString(doc.Channel))
	fmt.Fprintf(&b, "video_id: %s\n", yamlString(doc.VideoID))
	fmt.Fprintf(&b, "canonical_url: %s\n", yamlString(doc.CanonicalURL))
	fmt.Fprintf(&b, "duration_seconds: %d\n", doc.DurationSeconds)
	if doc.PublishedAt != nil {
		fmt.Fprintf(&b, "published_at: %s\n", doc.PublishedAt.UTC().Format("2006-01-02"))
	} else {
		b.WriteString("published_at: null\n")
	}
	fmt.Fprintf(&b, "saved_at: %s\n", isoTime(doc.SavedAt))
	fmt.Fprintf(&b, "primary_topic: %s\n", yamlString(doc.PrimaryTopic))
	tags := make([]string, 0, len(doc.Tags))
	for _, t := range doc.Tags {
		tags = append(tags, yamlString(t))
	}
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(tags, ", "))
	fmt.Fprintf(&b, "detected_language: %s\n", yamlString(p.DetectedLanguage))
	fmt.Fprintf(&b, "revision_id: %s\n", doc.Revision.ID)
	fmt.Fprintf(&b, "revision_number: %d\n", doc.Revision.Number)
	fmt.Fprintf(&b, "exported_at: %s\n", isoTime(doc.ExportedAt))
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", oneLine(doc.Title))
	fmt.Fprintf(&b, "%s · [Watch on YouTube](%s)\n\n", metaLine(doc), doc.CanonicalURL)
	b.WriteString("## Summary\n\n" + blockLine(p.Summary.Text, p.Summary.SpeakerClaim) + "\n\n")

	fmt.Fprintf(&b, "## %s\n\n", diagramName(p.Diagram.Type))
	switch p.Diagram.Type {
	case generation.MindMap, generation.ProcessFlow:
		if hasSVG {
			b.WriteString("![Diagram](diagram.svg)\n\n")
		}
		b.WriteString("```text\n" + generation.TextAlternative(&p.Diagram) + "\n```\n\n")
	case generation.Timeline:
		for i, e := range sortedElements(p.Diagram.Events) {
			fmt.Fprintf(&b, "%d. %s\n", i+1, blockLine(e.Label, e.SpeakerClaim))
		}
		b.WriteString("\n")
	case generation.Comparison:
		b.WriteString(comparisonTable(&p.Diagram))
	}

	section := func(title string, blocks []generation.StoredBlock) {
		if len(blocks) == 0 {
			return
		}
		b.WriteString("## " + title + "\n\n")
		for _, blk := range blocks {
			b.WriteString("- " + blockLine(blk.Text, blk.SpeakerClaim) + "\n")
		}
		b.WriteString("\n")
	}
	section("Key points", p.KeyPoints)
	section("Important details", p.ImportantDetails)
	section("What I can use", p.PracticalActions)
	if takeaway := strings.TrimSpace(doc.Takeaway); takeaway != "" {
		b.WriteString("## My takeaway\n\n" + takeaway + "\n\n")
	}
	if withTranscript {
		b.WriteString("## Transcript\n\nThe transcript is in [transcript.md](transcript.md).\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// TranscriptMarkdown is transcript.md: the Transcript in its original
// language and script, one paragraph per chunk, linked back to note.md.
func TranscriptMarkdown(doc *Document) string {
	t := doc.Transcript
	var b strings.Builder
	fmt.Fprintf(&b, "# Transcript: %s\n\n", oneLine(doc.Title))
	fmt.Fprintf(&b, "%s · language %s · [Watch on YouTube](%s) · back to [note.md](note.md)\n\n",
		sourceKindLabel(t.SourceKind), t.LanguageCode, doc.CanonicalURL)
	for _, para := range t.Paragraphs() {
		if s := strings.TrimSpace(para); s != "" {
			b.WriteString(s + "\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

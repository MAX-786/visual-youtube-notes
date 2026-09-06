package export

import (
	"archive/zip"
	"io"
)

// WriteMarkdownZip writes the note's Markdown export: note.md, diagram.svg
// beside it for a mind map or process flow, and transcript.md when asked
// for and the Note has one. UTF-8 throughout, no remote reference (spec 12,
// ADR 0011).
func WriteMarkdownZip(w io.Writer, doc *Document, withTranscript bool) error {
	zw := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: doc.ExportedAt})
		if err != nil {
			return err
		}
		_, err = fw.Write(data)
		return err
	}
	svg, hasSVG := DiagramSVG(&doc.Payload.Diagram, doc.Positions, true)
	transcript := withTranscript && doc.Transcript != nil
	if err := add("note.md", []byte(NoteMarkdown(doc, transcript, hasSVG))); err != nil {
		return err
	}
	if hasSVG {
		if err := add("diagram.svg", []byte(svg)); err != nil {
			return err
		}
	}
	if transcript {
		if err := add("transcript.md", []byte(TranscriptMarkdown(doc))); err != nil {
			return err
		}
	}
	return zw.Close()
}

package export

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// ErrRendererUnavailable is a Gotenberg that could not be reached or did not
// render: the export answers renderer_unavailable and the Note is untouched.
// There is no second renderer (ADR 0011).
var ErrRendererUnavailable = errors.New("export: the PDF renderer is unavailable")

// Renderer turns a print document into a PDF.
type Renderer interface {
	RenderPDF(ctx context.Context, document []byte) (pdf io.ReadCloser, size int64, err error)
}

// Gotenberg renders through Gotenberg's Chromium route. The document is the
// only file in the form; the service runs with JavaScript disabled and its
// network closed, so nothing else could load anyway (ADR 0008, ADR 0011).
type Gotenberg struct {
	url    string
	client *http.Client
}

// NewGotenberg points at the service. A nil client gets one with a timeout
// a few-second render never reaches.
func NewGotenberg(url string, client *http.Client) *Gotenberg {
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	return &Gotenberg{url: strings.TrimRight(url, "/"), client: client}
}

// gotenbergFields are the render options: the CSS @page rule decides the
// geometry, backgrounds print, and the print media query applies.
var gotenbergFields = [][2]string{
	{"preferCssPageSize", "true"},
	{"printBackground", "true"},
	{"emulatedMediaType", "print"},
}

// RenderPDF posts the document as index.html and returns the PDF stream.
func (g *Gotenberg) RenderPDF(ctx context.Context, document []byte) (io.ReadCloser, int64, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, 0, err
	}
	if _, err := part.Write(document); err != nil {
		return nil, 0, err
	}
	for _, f := range gotenbergFields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return nil, 0, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrRendererUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("%w: status %d", ErrRendererUnavailable, resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

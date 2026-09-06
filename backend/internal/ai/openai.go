package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ProviderError is a non-2xx answer or a transport failure from a provider,
// with what the Stage needs to classify it. Status 0 means the provider was
// never reached. The message is the provider's own, which beats ours
// (ADR 0010), truncated so a page of HTML cannot become an error.
type ProviderError struct {
	Status    int
	Type      string
	Code      string
	Message   string
	RequestID string
}

func (e *ProviderError) Error() string {
	if e.Status == 0 {
		return "provider unreachable: " + e.Message
	}
	return fmt.Sprintf("provider HTTP %d: %s", e.Status, e.Message)
}

// Unauthorized is a 401 or 403: the key is wrong or revoked, and the role's
// verification is cleared (ADR 0010).
func (e *ProviderError) Unauthorized() bool { return e.Status == 401 || e.Status == 403 }

// Transient is worth a retry on the ladder: unreachable, a timeout, a rate
// limit, or a provider-side failure.
func (e *ProviderError) Transient() bool {
	return e.Status == 0 || e.Status == 408 || e.Status == 409 || e.Status == 429 || e.Status >= 500
}

// Client speaks OpenAI's API for one set of Credentials. The transport is
// injectable because the endpoint field cannot reach a test double
// (ADR 0010); tests substitute a RoundTripper and no network is touched.
type Client struct {
	creds Credentials
	http  *http.Client
}

// NewClient builds a client; a nil transport uses the default.
func NewClient(creds Credentials, transport http.RoundTripper) *Client {
	return &Client{creds: creds, http: &http.Client{Transport: transport}}
}

// Model is the configured model.
func (c *Client) Model() string { return c.creds.Model }

// Verify is the cheapest authenticated read: GET /models, which is free, so
// it writes no ledger row (ADR 0017).
func (c *Client) Verify(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_, err := c.do(ctx, http.MethodGet, "/models", "", nil, &out)
	return err
}

// ResponsesRequest is one structured-output call to the Responses API.
type ResponsesRequest struct {
	Instructions string
	Input        string
	SchemaName   string
	Schema       json.RawMessage
}

// ResponsesResult is what came back: the JSON text, or a refusal, with the
// units in cost-tracking.md's vocabulary.
type ResponsesResult struct {
	Text       string
	Refusal    string
	Incomplete string
	Units      UnitMap
	RequestID  string
}

// Respond calls POST /responses with the schema in text.format as strict
// json_schema and the role's request options mapped onto the request.
func (c *Client) Respond(ctx context.Context, req ResponsesRequest) (*ResponsesResult, error) {
	body := map[string]any{
		"model":        c.creds.Model,
		"instructions": req.Instructions,
		"input":        req.Input,
		"store":        false,
		"text": map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   req.SchemaName,
				"schema": req.Schema,
				"strict": true,
			},
		},
	}
	for k, v := range c.creds.RequestOptions {
		switch k {
		case "reasoning_effort":
			body["reasoning"] = map[string]any{"effort": v}
		case "verbosity":
			body["text"].(map[string]any)["verbosity"] = v
		default:
			body[k] = v
		}
	}
	var out struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens        float64 `json:"input_tokens"`
			InputTokensDetails struct {
				CachedTokens float64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens float64 `json:"output_tokens"`
		} `json:"usage"`
	}
	requestID, err := c.doJSON(ctx, "/responses", body, &out)
	if err != nil {
		return nil, err
	}
	res := &ResponsesResult{RequestID: firstNonEmpty(out.ID, requestID)}
	for _, item := range out.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				res.Text += part.Text
			case "refusal":
				res.Refusal = part.Refusal
			}
		}
	}
	if out.IncompleteDetails != nil {
		res.Incomplete = out.IncompleteDetails.Reason
	}
	// usage.input_tokens includes the cached tokens, which bill at a tenth of
	// the rate, so the uncached remainder and the cached count are recorded
	// apart; output_tokens already includes reasoning tokens (ADR 0017).
	cached := out.Usage.InputTokensDetails.CachedTokens
	res.Units = UnitMap{
		"input_tokens":        max(out.Usage.InputTokens-cached, 0),
		"cached_input_tokens": cached,
		"output_tokens":       out.Usage.OutputTokens,
	}
	return res, nil
}

// TranscribeRequest is one audio chunk for the transcription API.
type TranscribeRequest struct {
	Audio    io.Reader
	Filename string
	// DurationSeconds is the measured length of the chunk, the unit the
	// price is in, used when the response carries no usage of its own.
	DurationSeconds float64
}

// TranscribeResult is the text and the seconds billed.
type TranscribeResult struct {
	Text      string
	Units     UnitMap
	RequestID string
}

// Transcribe calls POST /audio/transcriptions as multipart. Options are
// form fields; a list option becomes repeated <key>[] fields.
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (*TranscribeResult, error) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	part, err := form.CreateFormFile("file", req.Filename)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, req.Audio); err != nil {
		return nil, err
	}
	fields := map[string]string{"model": c.creds.Model, "response_format": "json"}
	for k, v := range fields {
		if err := form.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	for k, v := range c.creds.RequestOptions {
		switch value := v.(type) {
		case []any:
			for _, item := range value {
				if err := form.WriteField(k+"[]", fmt.Sprint(item)); err != nil {
					return nil, err
				}
			}
		default:
			if err := form.WriteField(k, fmt.Sprint(value)); err != nil {
				return nil, err
			}
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	var out struct {
		Text  string `json:"text"`
		Usage *struct {
			Type    string  `json:"type"`
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	requestID, err := c.do(ctx, http.MethodPost, "/audio/transcriptions", form.FormDataContentType(), &buf, &out)
	if err != nil {
		return nil, err
	}
	seconds := req.DurationSeconds
	if out.Usage != nil && out.Usage.Type == "duration" && out.Usage.Seconds > 0 {
		seconds = out.Usage.Seconds
	}
	return &TranscribeResult{Text: out.Text, Units: UnitMap{"audio_seconds": seconds}, RequestID: requestID}, nil
}

// EmbedResult is one vector per input, in order, and the tokens billed.
type EmbedResult struct {
	Vectors   [][]float32
	Units     UnitMap
	RequestID string
}

// Embed calls POST /embeddings for a batch of inputs.
func (c *Client) Embed(ctx context.Context, inputs []string) (*EmbedResult, error) {
	body := map[string]any{"model": c.creds.Model, "input": inputs, "encoding_format": "float"}
	for k, v := range c.creds.RequestOptions {
		body[k] = v
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			TotalTokens float64 `json:"total_tokens"`
		} `json:"usage"`
	}
	requestID, err := c.doJSON(ctx, "/embeddings", body, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Data) != len(inputs) {
		return nil, &ProviderError{Status: 502, Message: fmt.Sprintf("expected %d embeddings, got %d", len(inputs), len(out.Data)), RequestID: requestID}
	}
	vectors := make([][]float32, len(inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vectors) {
			return nil, &ProviderError{Status: 502, Message: "embedding index out of range", RequestID: requestID}
		}
		vectors[d.Index] = d.Embedding
	}
	return &EmbedResult{Vectors: vectors, Units: UnitMap{"embedding_tokens": out.Usage.TotalTokens}, RequestID: requestID}, nil
}

func (c *Client) doJSON(ctx context.Context, path string, body any, out any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return c.do(ctx, http.MethodPost, path, "application/json", bytes.NewReader(raw), out)
}

// maxErrorBody bounds what is read from a failed response.
const maxErrorBody = 64 << 10

// do performs one request. The key travels in the Authorization header and
// nowhere else; it is never part of an error.
func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.creds.BaseURL, "/")+path, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.creds.Key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "visual-youtube-notes")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		message := "network error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "timed out"
		}
		return "", &ProviderError{Status: 0, Message: message}
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do with a close error on a read body
	requestID := resp.Header.Get("x-request-id")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		pe := &ProviderError{Status: resp.StatusCode, RequestID: requestID}
		var envelope struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
			pe.Message = envelope.Error.Message
			pe.Type = envelope.Error.Type
			if envelope.Error.Code != nil {
				pe.Code = fmt.Sprint(envelope.Error.Code)
			}
		} else {
			pe.Message = http.StatusText(resp.StatusCode)
		}
		if len(pe.Message) > 300 {
			pe.Message = pe.Message[:300]
		}
		pe.Message = redactKeys(pe.Message)
		return requestID, pe
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return requestID, &ProviderError{Status: 502, Message: "unreadable response body", RequestID: requestID}
		}
	}
	return requestID, nil
}

// keyShape matches anything that looks like a provider key inside a message,
// including the partly masked echo OpenAI itself sends back, so no more of a
// key than the stored hint ever reaches a row or a page.
var keyShape = regexp.MustCompile(`sk-[A-Za-z0-9_*.-]{4,}`)

func redactKeys(s string) string { return keyShape.ReplaceAllString(s, "sk-…") }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

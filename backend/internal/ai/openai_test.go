package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTrip is the no-network double: it records the request and answers
// with a canned response.
type roundTrip struct {
	status   int
	body     string
	requests []*http.Request
	bodies   []string
}

func (r *roundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests = append(r.requests, req)
	var body string
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	r.bodies = append(r.bodies, body)
	return &http.Response{
		StatusCode: r.status,
		Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_123"}},
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Request:    req,
	}, nil
}

func testCreds(opts map[string]any) Credentials {
	return Credentials{Role: RoleNoteGeneration, Provider: ProviderOpenAI, Model: "gpt-5.6-terra", BaseURL: "https://api.openai.com/v1", RequestOptions: opts, Key: "sk-secret-key-value"}
}

func TestVerifySendsBearerAndNothingElseCarriesTheKey(t *testing.T) {
	rt := &roundTrip{status: 200, body: `{"data":[{"id":"gpt-5.6-terra"}]}`}
	if err := NewClient(testCreds(nil), rt).Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := rt.requests[0]
	if req.Method != http.MethodGet || req.URL.String() != "https://api.openai.com/v1/models" {
		t.Fatalf("request %s %s", req.Method, req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer sk-secret-key-value" {
		t.Fatal("bearer header missing")
	}
	if strings.Contains(req.URL.String(), "sk-secret") {
		t.Fatal("key in URL")
	}
}

func TestUnauthorizedIsClassifiedAndNeverEchoesTheKey(t *testing.T) {
	rt := &roundTrip{status: 401, body: `{"error":{"message":"Incorrect API key provided: sk-sec***lue","type":"invalid_request_error","code":"invalid_api_key"}}`}
	err := NewClient(testCreds(nil), rt).Verify(context.Background())
	var pe *ProviderError
	if !errors.As(err, &pe) || !pe.Unauthorized() || pe.Transient() || pe.Code != "invalid_api_key" || pe.RequestID != "req_123" {
		t.Fatalf("classification: %#v", err)
	}
	if strings.Contains(err.Error(), "sk-secret-key-value") || strings.Contains(err.Error(), "sk-sec") {
		t.Fatalf("the key leaked into the error: %s", err)
	}
	rt = &roundTrip{status: 429, body: `{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`}
	err = NewClient(testCreds(nil), rt).Verify(context.Background())
	if !errors.As(err, &pe) || !pe.Transient() || pe.Unauthorized() {
		t.Fatalf("429 should be transient: %#v", err)
	}
}

func TestRespondBuildsStrictSchemaAndSplitsCachedTokens(t *testing.T) {
	rt := &roundTrip{status: 200, body: `{
		"id":"resp_abc","status":"completed",
		"output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"{\"title\":\"x\"}"}]}],
		"usage":{"input_tokens":5000,"input_tokens_details":{"cached_tokens":3000},"output_tokens":1200,"output_tokens_details":{"reasoning_tokens":400}}}`}
	client := NewClient(testCreds(map[string]any{"reasoning_effort": "low", "max_output_tokens": float64(16000)}), rt)
	res, err := client.Respond(context.Background(), ResponsesRequest{Instructions: "sys", Input: "transcript", SchemaName: "note", Schema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"title":"x"}` || res.Refusal != "" || res.RequestID != "resp_abc" {
		t.Fatalf("result %+v", res)
	}
	if res.Units["input_tokens"] != 2000 || res.Units["cached_input_tokens"] != 3000 || res.Units["output_tokens"] != 1200 {
		t.Fatalf("units %v", res.Units)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(rt.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	format := sent["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "note" {
		t.Fatalf("format %v", format)
	}
	if sent["reasoning"].(map[string]any)["effort"] != "low" || sent["max_output_tokens"] != float64(16000) || sent["store"] != false || sent["model"] != "gpt-5.6-terra" {
		t.Fatalf("request %v", sent)
	}
}

func TestRespondReportsRefusal(t *testing.T) {
	rt := &roundTrip{status: 200, body: `{"id":"resp_r","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I cannot help with that."}]}],"usage":{"input_tokens":10,"output_tokens":5}}`}
	res, err := NewClient(testCreds(nil), rt).Respond(context.Background(), ResponsesRequest{SchemaName: "note", Schema: json.RawMessage(`{}`)})
	if err != nil || res.Refusal == "" || res.Text != "" {
		t.Fatalf("refusal: %+v %v", res, err)
	}
}

func TestEmbedOrdersVectorsAndCountsTokens(t *testing.T) {
	rt := &roundTrip{status: 200, body: `{"data":[{"index":1,"embedding":[0.5,0.5]},{"index":0,"embedding":[0.1,0.2]}],"usage":{"total_tokens":42}}`}
	creds := testCreds(map[string]any{"dimensions": float64(2)})
	creds.Role, creds.Model = RoleEmbeddings, "text-embedding-3-large"
	res, err := NewClient(creds, rt).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Vectors[0][0] != 0.1 || res.Vectors[1][0] != 0.5 || res.Units["embedding_tokens"] != 42 {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(rt.bodies[0], `"dimensions":2`) || !strings.Contains(rt.bodies[0], `"model":"text-embedding-3-large"`) {
		t.Fatalf("body %s", rt.bodies[0])
	}
}

func TestTranscribeSendsMultipartAndUsesMeasuredDuration(t *testing.T) {
	rt := &roundTrip{status: 200, body: `{"text":"hello world"}`}
	creds := testCreds(map[string]any{"languages": []any{"en", "hi"}})
	creds.Role, creds.Model = RoleTranscription, "gpt-transcribe"
	res, err := NewClient(creds, rt).Transcribe(context.Background(), TranscribeRequest{Audio: strings.NewReader("RIFF"), Filename: "chunk.webm", DurationSeconds: 1200})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hello world" || res.Units["audio_seconds"] != 1200 {
		t.Fatalf("result %+v", res)
	}
	body := rt.bodies[0]
	for _, want := range []string{`name="file"; filename="chunk.webm"`, `name="model"`, "gpt-transcribe", `name="languages[]"`} {
		if !strings.Contains(body, want) {
			t.Errorf("multipart body lacks %s", want)
		}
	}
	if !strings.HasPrefix(rt.requests[0].Header.Get("Content-Type"), "multipart/form-data") {
		t.Fatal("not multipart")
	}
}

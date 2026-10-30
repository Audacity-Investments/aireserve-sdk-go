package aireserveruntime_test

// compat_test.go — conformance checklist items 18 and 19 (spec §9): the
// OpenAI-format and Anthropic-format pass-through surfaces.  Hermetic —
// httptest servers only, no network.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// ─────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────

// capturedRequest records everything the mock gateway saw for one request.
type capturedRequest struct {
	Path   string
	Header http.Header
	Body   map[string]interface{}
}

// cannedResponse is one scripted mock-gateway reply.
type cannedResponse struct {
	status int
	header map[string]string
	body   string
}

// captureServer replays canned responses in order (the last one repeats) and
// records each request it receives.
func captureServer(t *testing.T, responses ...cannedResponse) (*httptest.Server, *[]capturedRequest) {
	t.Helper()
	var requests []capturedRequest
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		requests = append(requests, capturedRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})

		resp := responses[i]
		if i < len(responses)-1 {
			i++
		}
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		status := resp.status
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		fmt.Fprint(w, resp.body)
	}))
	return srv, &requests
}

// newCompatClient creates a zero-retry client pointed at a test server URL.
func newCompatClient(serverURL string) *aireserveruntime.Client {
	return aireserveruntime.New(aireserveruntime.Options{
		APIKey:     "test-key",
		BaseURL:    serverURL,
		MaxRetries: aireserveruntime.NoRetries,
	})
}

// collectRawEvents drains all events from a RawEventStream with a timeout.
func collectRawEvents(t *testing.T, stream *aireserveruntime.RawEventStream) []map[string]interface{} {
	t.Helper()
	var events []map[string]interface{}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-stream.Events():
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("timed out waiting for raw stream events")
			return events
		}
	}
}

func bodyKeys(body map[string]interface{}) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const openAIResponseBody = `{
	"id": "chatcmpl-1", "object": "chat.completion", "model": "gpt-5.4-mini",
	"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}],
	"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
	"system_fingerprint": "fp_new_gateway_field"
}`

const anthropicResponseBody = `{
	"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
	"content": [{"type": "text", "text": "Hello!"}],
	"stop_reason": "end_turn",
	"usage": {"input_tokens": 3, "output_tokens": 2},
	"future_field": {"nested": true}
}`

func chatParams() *aireserveruntime.ChatCompletionCreateParams {
	return &aireserveruntime.ChatCompletionCreateParams{
		Model:    "gpt-5.4-mini",
		Messages: []map[string]interface{}{{"role": "user", "content": "Hi"}},
	}
}

func messageParams() *aireserveruntime.MessageCreateParams {
	return &aireserveruntime.MessageCreateParams{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 64,
		Messages:  []map[string]interface{}{{"role": "user", "content": "Hi"}},
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 18 — OpenAI pass-through, non-streaming
// ─────────────────────────────────────────────────────────────

func TestChatCompletionsCreatePassthrough(t *testing.T) {
	srv, requests := captureServer(t, cannedResponse{body: openAIResponseBody})
	defer srv.Close()

	params := chatParams()
	params.MaxTokens = aireserve.Int32(64)
	params.Temperature = aireserve.Float64(0.5)
	params.Extra = map[string]interface{}{
		"seed":            7,
		"unknown_feature": map[string]interface{}{"enabled": true},
	}

	resp, err := newCompatClient(srv.URL).Chat.Completions.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// Raw response returned untranslated.
	if got := resp.Choices[0].Message["content"]; got != "Hello!" {
		t.Errorf("Choices[0].Message.content = %v, want Hello!", got)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.Choices[0].FinishReason)
	}
	if got := resp.Usage["total_tokens"]; got != float64(5) {
		t.Errorf("Usage.total_tokens = %v, want 5", got)
	}
	if got := resp.Raw["system_fingerprint"]; got != "fp_new_gateway_field" {
		t.Errorf("Raw.system_fingerprint = %v — unknown response fields must be preserved", got)
	}

	// Request sent verbatim: path, auth, and exact body keys.
	req := (*requests)[0]
	if req.Path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", req.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", got)
	}
	if got := req.Header.Get("Anthropic-Version"); got != "" {
		t.Errorf("anthropic-version = %q — must not be sent on the OpenAI route", got)
	}
	wantKeys := []string{"max_tokens", "messages", "model", "seed", "temperature", "unknown_feature"}
	if gotKeys := bodyKeys(req.Body); fmt.Sprint(gotKeys) != fmt.Sprint(wantKeys) {
		t.Errorf("body keys = %v, want %v (verbatim, no additions or strips)", gotKeys, wantKeys)
	}
	if got := req.Body["seed"]; got != float64(7) {
		t.Errorf("body.seed = %v, want 7 — unknown request fields must flow through", got)
	}
	if got := req.Body["max_tokens"]; got != float64(64) {
		t.Errorf("body.max_tokens = %v, want 64", got)
	}
}

func TestChatCompletionsCreateValidation(t *testing.T) {
	client := newCompatClient("http://unused.invalid")

	if _, err := client.Chat.Completions.Create(context.Background(), &aireserveruntime.ChatCompletionCreateParams{
		Messages: []map[string]interface{}{{"role": "user", "content": "Hi"}},
	}); err == nil {
		t.Error("expected error for missing model")
	}
	if _, err := client.Chat.Completions.Create(context.Background(), &aireserveruntime.ChatCompletionCreateParams{
		Model: "gpt-5.4-mini",
	}); err == nil {
		t.Error("expected error for missing messages")
	}

	t.Setenv("AIRESERVE_API_KEY", "")
	t.Setenv("AUDACITY_API_KEY", "")
	missing := aireserveruntime.New(aireserveruntime.Options{BaseURL: "http://unused.invalid"})
	var missingKey *types.MissingAPIKeyError
	if _, err := missing.Chat.Completions.Create(context.Background(), chatParams()); !errors.As(err, &missingKey) {
		t.Errorf("missing API key error = %v, want *types.MissingAPIKeyError", err)
	}
	if _, err := missing.Messages.Create(context.Background(), messageParams()); !errors.As(err, &missingKey) {
		t.Errorf("missing API key error = %v, want *types.MissingAPIKeyError", err)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 18 — OpenAI pass-through, streaming
// ─────────────────────────────────────────────────────────────

func TestChatCompletionsCreateStreamRawChunks(t *testing.T) {
	srv, requests := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\n\n" +
			": comment line ignored\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"total_tokens\":5}}\n\n" +
			"data: [DONE]\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Chat.Completions.CreateStream(context.Background(), chatParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	events := collectRawEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// Raw chunks, unre-shaped, [DONE] not yielded.
	if len(events) != 3 {
		t.Fatalf("expected 3 raw chunks, got %d: %#v", len(events), events)
	}
	delta := events[0]["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta["content"] != "Hel" {
		t.Errorf("chunk[0] delta.content = %v, want Hel", delta["content"])
	}
	usage := events[2]["usage"].(map[string]interface{})
	if usage["total_tokens"] != float64(5) {
		t.Errorf("chunk[2] usage.total_tokens = %v, want 5", usage["total_tokens"])
	}

	// The SDK's only body addition on the streaming path is stream: true.
	req := (*requests)[0]
	if req.Body["stream"] != true {
		t.Errorf("body.stream = %v, want true", req.Body["stream"])
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
}

func TestChatCompletionsStreamInlineErrorAborts(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n" +
			"data: {\"error\":{\"message\":\"boom\",\"code\":\"STREAM_ERROR\"}}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Chat.Completions.CreateStream(context.Background(), chatParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	events := collectRawEvents(t, stream)
	if len(events) != 1 {
		t.Errorf("expected 1 chunk before the error, got %d", len(events))
	}
	var streamErr *types.ModelStreamErrorException
	if err := stream.Err(); !errors.As(err, &streamErr) {
		t.Fatalf("stream.Err() = %v, want *types.ModelStreamErrorException", err)
	}
}

func TestChatCompletionsStreamInlineErrorMapsCode(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body:   "data: {\"error\":{\"message\":\"slow down\",\"code\":\"RATE_LIMIT_EXCEEDED\"}}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Chat.Completions.CreateStream(context.Background(), chatParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	collectRawEvents(t, stream)
	var throttle *types.ThrottlingException
	if err := stream.Err(); !errors.As(err, &throttle) {
		t.Fatalf("stream.Err() = %v, want *types.ThrottlingException (§4 code table)", err)
	}
}

func TestChatCompletionsStreamEOFWithoutDone(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Chat.Completions.CreateStream(context.Background(), chatParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	collectRawEvents(t, stream)
	var streamErr *types.ModelStreamErrorException
	if err := stream.Err(); !errors.As(err, &streamErr) {
		t.Fatalf("stream.Err() = %v, want *types.ModelStreamErrorException (EOF without [DONE])", err)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 18 — §4 retry policy on the OpenAI route
// ─────────────────────────────────────────────────────────────

func TestChatCompletionsRetries429ThenSucceeds(t *testing.T) {
	srv, requests := captureServer(t,
		cannedResponse{
			status: 429,
			header: map[string]string{"Retry-After": "0"},
			body:   `{"error":{"message":"slow down","code":"RATE_LIMIT_EXCEEDED"}}`,
		},
		cannedResponse{body: openAIResponseBody},
	)
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: 1,
	})
	resp, err := client.Chat.Completions.Create(context.Background(), chatParams())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := resp.Choices[0].Message["content"]; got != "Hello!" {
		t.Errorf("content = %v, want Hello!", got)
	}
	if len(*requests) != 2 {
		t.Errorf("request count = %d, want 2 (429 retried once)", len(*requests))
	}
}

func TestChatCompletions401NotRetried(t *testing.T) {
	srv, requests := captureServer(t, cannedResponse{
		status: 401,
		body:   `{"error":{"message":"bad key","code":"INVALID_API_KEY"}}`,
	})
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: 2,
	})
	_, err := client.Chat.Completions.Create(context.Background(), chatParams())
	var denied *types.AccessDeniedException
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *types.AccessDeniedException", err)
	}
	if len(*requests) != 1 {
		t.Errorf("request count = %d, want 1 (401 never retried)", len(*requests))
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 19 — Anthropic pass-through, non-streaming
// ─────────────────────────────────────────────────────────────

func TestMessagesCreatePassthrough(t *testing.T) {
	srv, requests := captureServer(t, cannedResponse{body: anthropicResponseBody})
	defer srv.Close()

	params := messageParams()
	params.System = "Be brief."
	params.Extra = map[string]interface{}{
		"stop_sequences": []string{"END"},
		"unknown_field":  "flows through",
	}

	resp, err := newCompatClient(srv.URL).Messages.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// Raw response returned untranslated.
	if got := resp.Content[0]["text"]; got != "Hello!" {
		t.Errorf("Content[0].text = %v, want Hello!", got)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if got := resp.Usage["input_tokens"]; got != float64(3) {
		t.Errorf("Usage.input_tokens = %v, want 3", got)
	}
	if _, ok := resp.Raw["future_field"]; !ok {
		t.Error("Raw.future_field missing — unknown response fields must be preserved")
	}

	// Request sent verbatim to /v1/messages with the anthropic-version header.
	req := (*requests)[0]
	if req.Path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", req.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	wantKeys := []string{"max_tokens", "messages", "model", "stop_sequences", "system", "unknown_field"}
	if gotKeys := bodyKeys(req.Body); fmt.Sprint(gotKeys) != fmt.Sprint(wantKeys) {
		t.Errorf("body keys = %v, want %v (verbatim, no additions or strips)", gotKeys, wantKeys)
	}
	if got := req.Body["max_tokens"]; got != float64(64) {
		t.Errorf("body.max_tokens = %v, want 64", got)
	}
	if got := req.Body["unknown_field"]; got != "flows through" {
		t.Errorf("body.unknown_field = %v — unknown request fields must flow through", got)
	}
}

func TestMessagesAnthropicErrorEnvelopeMapping(t *testing.T) {
	srv, _ := captureServer(t,
		cannedResponse{
			status: 429,
			body:   `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded."}}`,
		},
		cannedResponse{
			status: 402,
			body:   `{"type":"error","error":{"type":"billing_error","message":"Spend cap exceeded."}}`,
		},
	)
	defer srv.Close()
	client := newCompatClient(srv.URL)

	_, err := client.Messages.Create(context.Background(), messageParams())
	var throttle *types.ThrottlingException
	if !errors.As(err, &throttle) {
		t.Fatalf("429 rate_limit_error → %v, want *types.ThrottlingException", err)
	}

	_, err = client.Messages.Create(context.Background(), messageParams())
	var quota *types.ServiceQuotaExceededException
	if !errors.As(err, &quota) {
		t.Fatalf("402 billing_error → %v, want *types.ServiceQuotaExceededException (HTTP-status fallback)", err)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 19 — Anthropic pass-through, streaming
// ─────────────────────────────────────────────────────────────

func TestMessagesCreateStreamRawEvents(t *testing.T) {
	eventTypes := []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	srv, requests := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body:   body,
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Messages.CreateStream(context.Background(), messageParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	events := collectRawEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// All events yielded raw and in order, message_stop included.
	if len(events) != len(eventTypes) {
		t.Fatalf("expected %d events, got %d", len(eventTypes), len(events))
	}
	for i, want := range eventTypes {
		if got := events[i]["type"]; got != want {
			t.Errorf("event[%d].type = %v, want %s", i, got, want)
		}
	}
	delta := events[2]["delta"].(map[string]interface{})
	if delta["text"] != "Hello" {
		t.Errorf("content_block_delta text = %v, want Hello", delta["text"])
	}

	req := (*requests)[0]
	if req.Body["stream"] != true {
		t.Errorf("body.stream = %v, want true", req.Body["stream"])
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
}

func TestMessagesStreamErrorEventAborts(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n" +
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Messages.CreateStream(context.Background(), messageParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	events := collectRawEvents(t, stream)
	if len(events) != 1 {
		t.Errorf("expected 1 event before the error, got %d", len(events))
	}
	var streamErr *types.ModelStreamErrorException
	if err := stream.Err(); !errors.As(err, &streamErr) {
		t.Fatalf("stream.Err() = %v, want *types.ModelStreamErrorException", err)
	}
}

func TestMessagesStreamErrorEventMapsCode(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body:   "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Messages.CreateStream(context.Background(), messageParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	collectRawEvents(t, stream)
	var throttle *types.ThrottlingException
	if err := stream.Err(); !errors.As(err, &throttle) {
		t.Fatalf("stream.Err() = %v, want *types.ThrottlingException (§4 code table)", err)
	}
}

func TestMessagesStreamEOFBeforeMessageStop(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Messages.CreateStream(context.Background(), messageParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	events := collectRawEvents(t, stream)
	if len(events) != 2 {
		t.Errorf("expected the 2 delivered events, got %d", len(events))
	}
	var streamErr *types.ModelStreamErrorException
	if err := stream.Err(); !errors.As(err, &streamErr) {
		t.Fatalf("stream.Err() = %v, want *types.ModelStreamErrorException (EOF before message_stop)", err)
	}
}

func TestMessagesStreamCleanEOFAfterMessageStop(t *testing.T) {
	srv, _ := captureServer(t, cannedResponse{
		header: map[string]string{"Content-Type": "text/event-stream"},
		body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	})
	defer srv.Close()

	stream, err := newCompatClient(srv.URL).Messages.CreateStream(context.Background(), messageParams())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	events := collectRawEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error after healthy message_stop+EOF: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 19 — count_tokens
// ─────────────────────────────────────────────────────────────

func TestMessagesCountTokens(t *testing.T) {
	srv, requests := captureServer(t, cannedResponse{body: `{"input_tokens": 42}`})
	defer srv.Close()

	resp, err := newCompatClient(srv.URL).Messages.CountTokens(context.Background(), &aireserveruntime.CountTokensParams{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "Hi"}},
		Extra:    map[string]interface{}{"unknown_field": true},
	})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if resp.InputTokens != 42 {
		t.Errorf("InputTokens = %d, want 42", resp.InputTokens)
	}
	if resp.Raw["input_tokens"] != float64(42) {
		t.Errorf("Raw.input_tokens = %v, want 42", resp.Raw["input_tokens"])
	}

	req := (*requests)[0]
	if req.Path != "/v1/messages/count_tokens" {
		t.Errorf("path = %q, want /v1/messages/count_tokens", req.Path)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if req.Body["unknown_field"] != true {
		t.Errorf("body.unknown_field = %v — unknown request fields must flow through", req.Body["unknown_field"])
	}
}

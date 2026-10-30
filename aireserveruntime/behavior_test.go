package aireserveruntime_test

// behavior_test.go — regression tests for review findings: timeout semantics,
// NoRetries/NoTimeout sentinels, stream lifecycle (leaks, cancellation,
// connection drop, oversized lines), amended §3 step-5 rules, and request
// serialization gaps (toolChoice variants, inference params, merge override).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// ─────────────────────────────────────────────────────────────
// Critical #1 — streams must survive longer than Options.Timeout
// ─────────────────────────────────────────────────────────────

func TestStreamOutlivesTimeout(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		// Dribble events well past the client's 100ms timeout.
		for i := 0; i < 4; i++ {
			time.Sleep(100 * time.Millisecond)
			writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		}
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL,
		MaxRetries: aireserveruntime.NoRetries,
		Timeout:    100 * time.Millisecond,
	})
	streamOut, err := client.ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if serr := stream.Err(); serr != nil {
		t.Fatalf("stream killed by timeout mid-body (spec §1 violation): %v", serr)
	}
	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "xxxx" {
		t.Errorf("text = %q, want xxxx", gotText)
	}
}

// Converse per-attempt timeout covers the body read: a server that stalls
// past the timeout produces a retryable SdkError wrapping DeadlineExceeded,
// not a hang.
func TestConverseTimeoutCoversBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // headers + status out, then stall the body
		}
		time.Sleep(500 * time.Millisecond)
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL,
		MaxRetries: aireserveruntime.NoRetries,
		Timeout:    80 * time.Millisecond,
	})
	start := time.Now()
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var sdkErr *types.SdkError
	if !errors.As(err, &sdkErr) {
		t.Fatalf("got %T, want *SdkError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("Converse blocked %v; timeout did not cover the body read", elapsed)
	}
}

// ─────────────────────────────────────────────────────────────
// Critical #2 — NoRetries means one attempt; negatives never panic
// ─────────────────────────────────────────────────────────────

func TestNoRetriesSingleAttempt(t *testing.T) {
	var attempts int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"boom","type":"internal_error"}}`)
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("NoRetries should mean exactly 1 attempt, got %d", n)
	}
}

func TestNegativeMaxRetriesDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	// Any negative value (not just the sentinel) must behave like NoRetries.
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: -7, Timeout: -3,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error, not panic/success")
	}
}

// ─────────────────────────────────────────────────────────────
// Connection drop before [DONE] → ModelStreamErrorException
// ─────────────────────────────────────────────────────────────

func TestStreamConnectionDropBeforeDone(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		// Handler returns without [DONE]; server closes the connection.
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	for range stream.Events() {
	}
	var streamErr *types.ModelStreamErrorException
	if !errors.As(stream.Err(), &streamErr) {
		t.Fatalf("expected ModelStreamErrorException on connection drop, got %T: %v", stream.Err(), stream.Err())
	}
}

// ─────────────────────────────────────────────────────────────
// Oversized SSE lines — 2 MiB (over the old 1 MiB cap) must work
// ─────────────────────────────────────────────────────────────

func TestStreamLargeSSELine(t *testing.T) {
	bigArgs := strings.Repeat("a", 2<<20) // 2 MiB of tool-call arguments
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		writeLine(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"`+bigArgs+`"}}]},"index":0}]}`+"\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"tool_calls\"}]}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	var gotLen int
	for ev := range stream.Events() {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if tu, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberToolUse); ok {
				gotLen += len(tu.Value.Input)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("2 MiB SSE line failed (hidden scanner cap?): %v", err)
	}
	if gotLen != len(bigArgs) {
		t.Errorf("tool input length = %d, want %d", gotLen, len(bigArgs))
	}
}

// ─────────────────────────────────────────────────────────────
// Context cancellation mid-stream → errors.Is(…, context.Canceled)
// ─────────────────────────────────────────────────────────────

func TestStreamContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		<-release // hold the stream open until the test finishes
	})
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	streamOut, err := newStreamClient(srv.URL).ConverseStream(ctx, streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()

	// Read the first event, then cancel the caller's context.
	select {
	case <-stream.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first event")
	}
	cancel()

	// The stream must terminate (channel closes) rather than hang.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-stream.Events():
			if !ok {
				goto drained
			}
		case <-timeout:
			t.Fatal("stream did not terminate after context cancellation")
		}
	}
drained:
	streamErr := stream.Err()
	if streamErr == nil {
		t.Fatal("expected a stream error after cancellation")
	}
	var mse *types.ModelStreamErrorException
	if !errors.As(streamErr, &mse) {
		t.Errorf("expected ModelStreamErrorException, got %T: %v", streamErr, streamErr)
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; cause chain broken: %v", streamErr)
	}
}

// ─────────────────────────────────────────────────────────────
// Early abandon + Close() must release the pump goroutine
// ─────────────────────────────────────────────────────────────

func TestStreamCloseReleasesPump(t *testing.T) {
	release := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter) {
		// Emit far more events than the 64-slot buffer so an abandoned pump
		// would block on send() forever without the Close/ctx fix.
		for i := 0; i < 500; i++ {
			writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		}
		<-release
	})
	defer func() { close(release); srv.Close() }()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()

	// Consume one event, then abandon + Close.
	select {
	case <-stream.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first event")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The events channel must close promptly — proof the pump exited.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-stream.Events():
			if !ok {
				return // pump exited; no goroutine leak
			}
		case <-timeout:
			t.Fatal("pump did not exit after Close(); goroutine leak")
		}
	}
}

// ─────────────────────────────────────────────────────────────
// Amended §3 step 5 — delta-less finish, duplicate finish, usage-first
// ─────────────────────────────────────────────────────────────

func TestStreamDeltaLessFinishReason(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		// finish_reason arrives in a chunk with no delta key at all.
		writeLine(w, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if serr := stream.Err(); serr != nil {
		t.Fatalf("stream error: %v", serr)
	}
	if len(events) != 2 {
		t.Fatalf("expected [messageStart, messageStop], got %d events: %#v", len(events), events)
	}
	if _, ok := events[0].(*types.ConverseStreamOutputMemberMessageStart); !ok {
		t.Errorf("event[0] = %T, want *MessageStart (must precede messageStop)", events[0])
	}
	stop, ok := events[1].(*types.ConverseStreamOutputMemberMessageStop)
	if !ok {
		t.Fatalf("event[1] = %T, want *MessageStop", events[1])
	}
	if stop.Value.StopReason != types.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want end_turn", stop.Value.StopReason)
	}
}

func TestStreamDuplicateFinishReason(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if serr := stream.Err(); serr != nil {
		t.Fatalf("stream error: %v", serr)
	}
	var stops int
	for _, ev := range events {
		if _, ok := ev.(*types.ConverseStreamOutputMemberMessageStop); ok {
			stops++
		}
	}
	if stops != 1 {
		t.Errorf("messageStop emitted %d times, want exactly 1", stops)
	}
}

func TestStreamUsageBeforeFinishReason(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		// usage arrives BEFORE the finish chunk (spec §3 step 6 parenthetical)
		writeLine(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if serr := stream.Err(); serr != nil {
		t.Fatalf("stream error: %v", serr)
	}
	last, ok := events[len(events)-1].(*types.ConverseStreamOutputMemberMetadata)
	if !ok {
		t.Fatalf("last event = %T, want *Metadata (metadata must be emitted last)", events[len(events)-1])
	}
	if last.Value.Usage == nil || last.Value.Usage.TotalTokens != 4 {
		t.Errorf("usage = %+v, want totalTokens 4", last.Value.Usage)
	}
}

// ─────────────────────────────────────────────────────────────
// Request serialization — toolChoice variants, inference params, merge override
// ─────────────────────────────────────────────────────────────

// captureRequest returns a client whose stub server records the parsed JSON
// request body into got.
func captureRequest(t *testing.T, got *map[string]interface{}) *aireserveruntime.Client {
	t.Helper()
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("parse request: %v", err)
			return
		}
		*got = req
		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]interface{}{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	})
	return client
}

func TestToolChoiceVariantsSerialization(t *testing.T) {
	cases := []struct {
		name   string
		choice types.ToolChoice
		want   interface{}
	}{
		{"any", &types.ToolChoiceMemberAny{}, "required"},
		{"tool", &types.ToolChoiceMemberTool{Value: types.SpecificToolChoice{Name: "get_weather"}},
			map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "get_weather"}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]interface{}
			client := captureRequest(t, &got)
			desc := "weather"
			_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
				ModelId:  aireserve.String("gpt-5.4-mini"),
				Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
				ToolConfig: &types.ToolConfiguration{
					Tools: []types.Tool{{ToolSpec: &types.ToolSpecification{
						Name: "get_weather", Description: &desc,
						InputSchema: &types.ToolInputSchema{Json: map[string]interface{}{"type": "object"}},
					}}},
					ToolChoice: tc.choice,
				},
			})
			if err != nil {
				t.Fatalf("Converse error: %v", err)
			}
			if fmt.Sprintf("%v", got["tool_choice"]) != fmt.Sprintf("%v", tc.want) {
				t.Errorf("tool_choice = %#v, want %#v", got["tool_choice"], tc.want)
			}
		})
	}
}

func TestInferenceConfigSerialization(t *testing.T) {
	var got map[string]interface{}
	client := captureRequest(t, &got)
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:     aireserve.Int32(500),
			Temperature:   aireserve.Float32(0.2),
			TopP:          aireserve.Float32(0.9),
			StopSequences: []string{"END"},
		},
	})
	if err != nil {
		t.Fatalf("Converse error: %v", err)
	}
	if got["max_tokens"] != float64(500) {
		t.Errorf("max_tokens = %v, want 500", got["max_tokens"])
	}
	if temp, _ := got["temperature"].(float64); temp < 0.19 || temp > 0.21 {
		t.Errorf("temperature = %v, want 0.2", got["temperature"])
	}
	if topP, _ := got["top_p"].(float64); topP < 0.89 || topP > 0.91 {
		t.Errorf("top_p = %v, want 0.9", got["top_p"])
	}
	stop, _ := got["stop"].([]interface{})
	if len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop = %#v, want [END]", got["stop"])
	}
}

func TestAdditionalFieldsOverridePrecedence(t *testing.T) {
	var got map[string]interface{}
	client := captureRequest(t, &got)
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens: aireserve.Int32(500),
		},
		// Shallow-merged LAST (spec §3 rule 6): must override max_tokens.
		AdditionalModelRequestFields: map[string]interface{}{
			"max_tokens": 42,
			"seed":       7,
		},
	})
	if err != nil {
		t.Fatalf("Converse error: %v", err)
	}
	if got["max_tokens"] != float64(42) {
		t.Errorf("max_tokens = %v, want 42 (additionalModelRequestFields must win)", got["max_tokens"])
	}
	if got["seed"] != float64(7) {
		t.Errorf("seed = %v, want 7", got["seed"])
	}
}

// ─────────────────────────────────────────────────────────────
// Image block with no source → client-side validation error
// ─────────────────────────────────────────────────────────────

func TestImageBlockNilSourceValidation(t *testing.T) {
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: "http://127.0.0.1:0", MaxRetries: aireserveruntime.NoRetries,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId: aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberImage{Value: types.ImageBlock{Format: types.ImageFormatPng, Source: nil}},
			},
		}},
	})
	var vErr *types.ValidationException
	if !errors.As(err, &vErr) {
		t.Fatalf("expected ValidationException for nil image source, got %T: %v", err, err)
	}
}

// ─────────────────────────────────────────────────────────────
// Unknown inline stream-error code → ModelStreamErrorException (amended §1)
// ─────────────────────────────────────────────────────────────

func TestStreamInlineErrorUnknownCode(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		writeLine(w, `data: {"error":{"message":"mystery failure","type":"totally_new_error","code":"SOMETHING_UNMAPPED"}}`+"\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	for range stream.Events() {
	}
	var mse *types.ModelStreamErrorException
	if !errors.As(stream.Err(), &mse) {
		t.Fatalf("unknown inline code should map to ModelStreamErrorException, got %T: %v", stream.Err(), stream.Err())
	}
	if mse.ErrorCode != "SOMETHING_UNMAPPED" {
		t.Errorf("ErrorCode = %q, want SOMETHING_UNMAPPED preserved", mse.ErrorCode)
	}
}

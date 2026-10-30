package aireserveruntime_test

// stream_test.go — conformance checklist items 2, 3 (streaming), 8.

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// SSE test server helpers
// ─────────────────────────────────────────────────────────────

// sseServer creates an httptest.Server that serves canned SSE events.
func sseServer(t *testing.T, writeFunc func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		writeFunc(w)
	}))
}

// writeLine writes a string and flushes if possible.
func writeLine(w http.ResponseWriter, line string) {
	fmt.Fprint(w, line)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// collectStreamEvents drains all events from the stream with a 5-second timeout.
func collectStreamEvents(t *testing.T, stream *aireserveruntime.ConverseStreamEventStream) []types.ConverseStreamOutput {
	t.Helper()
	var events []types.ConverseStreamOutput
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-stream.Events():
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("timed out waiting for stream events")
			return events
		}
	}
}

// standardSSEResponse produces the canonical 6-event sequence for a text response.
func standardSSEResponse(w http.ResponseWriter) {
	for _, l := range []string{
		// messageStart (role delta)
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0,\"finish_reason\":null}]}\n\n",
		// contentBlockDelta (text)
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"index\":0,\"finish_reason\":null}]}\n\n",
		// contentBlockDelta continued
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"index\":0,\"finish_reason\":null}]}\n\n",
		// finish
		"data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n",
		// usage chunk
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n",
		// DONE
		"data: [DONE]\n\n",
	} {
		writeLine(w, l)
	}
}

// newStreamClient creates a zero-retry client pointed at a test server URL.
func newStreamClient(serverURL string) *aireserveruntime.Client {
	return aireserveruntime.New(aireserveruntime.Options{
		APIKey:     "test-key",
		BaseURL:    serverURL,
		MaxRetries: aireserveruntime.NoRetries,
	})
}

func streamInput() *aireserveruntime.ConverseStreamInput {
	return &aireserveruntime.ConverseStreamInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 2 — ConverseStream happy path event order
// ─────────────────────────────────────────────────────────────

func TestConverseStreamHappyPath(t *testing.T) {
	srv := sseServer(t, standardSSEResponse)
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// Expected order: messageStart, contentBlockDelta×2,
	//                 contentBlockStop, messageStop, metadata
	if len(events) < 6 {
		t.Fatalf("expected ≥6 events, got %d: %#v", len(events), events)
	}

	// Event 0: messageStart
	ms, ok := events[0].(*types.ConverseStreamOutputMemberMessageStart)
	if !ok {
		t.Fatalf("event[0] = %T, want *MessageStart", events[0])
	}
	if ms.Value.Role != "assistant" {
		t.Errorf("messageStart.Role = %q, want assistant", ms.Value.Role)
	}

	// Events 1–2: contentBlockDelta (text)
	if _, ok := events[1].(*types.ConverseStreamOutputMemberContentBlockDelta); !ok {
		t.Fatalf("event[1] = %T, want *ContentBlockDelta", events[1])
	}
	if _, ok := events[2].(*types.ConverseStreamOutputMemberContentBlockDelta); !ok {
		t.Fatalf("event[2] = %T, want *ContentBlockDelta", events[2])
	}

	// Assemble text
	var sb strings.Builder
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				sb.WriteString(td.Value)
			}
		}
	}
	if sb.String() != "Hello world" {
		t.Errorf("assembled text = %q, want Hello world", sb.String())
	}

	// contentBlockStop present
	var foundStop bool
	for _, ev := range events {
		if _, ok := ev.(*types.ConverseStreamOutputMemberContentBlockStop); ok {
			foundStop = true
		}
	}
	if !foundStop {
		t.Error("expected contentBlockStop event")
	}

	// messageStop
	var msgStop *types.ConverseStreamOutputMemberMessageStop
	for _, ev := range events {
		if s, ok := ev.(*types.ConverseStreamOutputMemberMessageStop); ok {
			msgStop = s
		}
	}
	if msgStop == nil {
		t.Fatal("expected messageStop event")
	}
	if msgStop.Value.StopReason != "end_turn" {
		t.Errorf("messageStop.StopReason = %q, want end_turn", msgStop.Value.StopReason)
	}

	// Last event: metadata
	last := events[len(events)-1]
	meta, ok := last.(*types.ConverseStreamOutputMemberMetadata)
	if !ok {
		t.Fatalf("last event = %T, want *Metadata", last)
	}
	if meta.Value.Usage == nil {
		t.Fatal("metadata.Usage is nil")
	}
	if meta.Value.Usage.InputTokens != 5 || meta.Value.Usage.OutputTokens != 2 {
		t.Errorf("Usage = %+v, want {5 2 7}", meta.Value.Usage)
	}
	if meta.Value.Metrics == nil || meta.Value.Metrics.LatencyMs < 0 {
		t.Error("metadata.Metrics missing or negative")
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 3 (streaming) — tool round-trip via stream
// ─────────────────────────────────────────────────────────────

func TestConverseStreamToolRoundTrip(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		// messageStart
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		// contentBlockStart for tool (first delta with id + name)
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"index\":0}]}\n\n")
		// contentBlockDelta (toolUse input fragments)
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\"}}]},\"index\":0}]}\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"London\\\"}\"}}]},\"index\":0}]}\n\n")
		// finish
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"tool_calls\"}]}\n\n")
		// usage
		writeLine(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// contentBlockStart with toolUse
	var blockStart *types.ConverseStreamOutputMemberContentBlockStart
	for _, ev := range events {
		if s, ok := ev.(*types.ConverseStreamOutputMemberContentBlockStart); ok {
			blockStart = s
		}
	}
	if blockStart == nil {
		t.Fatal("expected contentBlockStart for tool use")
	}
	tuStart, ok := blockStart.Value.Start.(*types.ContentBlockStartMemberToolUse)
	if !ok {
		t.Fatalf("blockStart.Start = %T, want *ContentBlockStartMemberToolUse", blockStart.Value.Start)
	}
	if tuStart.Value.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", tuStart.Value.Name)
	}
	if tuStart.Value.ToolUseId != "call_1" {
		t.Errorf("toolUseId = %q, want call_1", tuStart.Value.ToolUseId)
	}

	// Assemble tool input fragments
	var toolInputFragments []string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if tu, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberToolUse); ok {
				toolInputFragments = append(toolInputFragments, tu.Value.Input)
			}
		}
	}
	assembled := strings.Join(toolInputFragments, "")
	if assembled != `{"city":"London"}` {
		t.Errorf("assembled tool input = %q, want {\"city\":\"London\"}", assembled)
	}

	// messageStop with tool_use stop reason
	var msgStop *types.ConverseStreamOutputMemberMessageStop
	for _, ev := range events {
		if s, ok := ev.(*types.ConverseStreamOutputMemberMessageStop); ok {
			msgStop = s
		}
	}
	if msgStop == nil || msgStop.Value.StopReason != "tool_use" {
		t.Errorf("messageStop.StopReason = %v, want tool_use", msgStop)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 8a — chunk split mid-line (bufio handles naturally)
// ─────────────────────────────────────────────────────────────

func TestSSEParserSplitMidLine(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		f, _ := w.(http.Flusher)
		// Write the first half of a data line with no newline, then flush.
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hel`) //nolint:errcheck
		if f != nil {
			f.Flush()
		}
		// Write the rest of the line + separator.
		io.WriteString(w, `lo"},"index":0,"finish_reason":null}]}`) //nolint:errcheck
		io.WriteString(w, "\n\n")                                   //nolint:errcheck
		if f != nil {
			f.Flush()
		}
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
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error after split-mid-line: %v", err)
	}

	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "Hello" {
		t.Errorf("text from split-mid-line = %q, want Hello", gotText)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 8a (UTF-8) — multi-byte rune split across network reads
// ─────────────────────────────────────────────────────────────

func TestSSEParserMidUTF8Split(t *testing.T) {
	// "café": the 'é' (U+00E9) is 2 bytes: 0xC3 0xA9.
	// Split after 0xC3 so the two network packets carry incomplete UTF-8.
	// bufio.Scanner reads and buffers bytes until '\n', so by the time the
	// line is emitted to json.Unmarshal the bytes are complete and valid.
	srv := sseServer(t, func(w http.ResponseWriter) {
		f, _ := w.(http.Flusher)

		prefix := []byte(`data: {"choices":[{"delta":{"content":"caf`)
		suffix := []byte(`"},"index":0,"finish_reason":null}]}` + "\n\n")
		eFirst := []byte{0xC3}  // first byte of é
		eSecond := []byte{0xA9} // second byte of é

		w.Write(prefix) //nolint:errcheck
		w.Write(eFirst) //nolint:errcheck
		if f != nil {
			f.Flush()
		}
		w.Write(eSecond) //nolint:errcheck
		w.Write(suffix)  //nolint:errcheck
		if f != nil {
			f.Flush()
		}
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
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error for mid-UTF-8: %v", err)
	}

	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "café" {
		t.Errorf("text = %q, want café (mid-UTF-8 split?)", gotText)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 8b — data: without space
// ─────────────────────────────────────────────────────────────

func TestSSEParserDataWithoutSpace(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		// No space after "data:"
		writeLine(w, "data:{\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		writeLine(w, "data:{\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data:[DONE]\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "Hi" {
		t.Errorf("text = %q, want Hi (data: without space not handled?)", gotText)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 8c — comment lines are ignored
// ─────────────────────────────────────────────────────────────

func TestSSEParserCommentLines(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, ": this is a comment — must be ignored\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Yes\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		writeLine(w, ": another comment\n\n")
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
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "Yes" {
		t.Errorf("text = %q, want Yes (comment lines not filtered?)", gotText)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 8d — missing final newline before [DONE]
// ─────────────────────────────────────────────────────────────

func TestSSEParserMissingFinalNewline(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Ok\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		// [DONE] without trailing \n — handler returns → EOF
		f, _ := w.(http.Flusher)
		io.WriteString(w, "data: [DONE]") //nolint:errcheck
		if f != nil {
			f.Flush()
		}
		// No trailing newline. Server closes the connection at handler return.
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream error: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error for missing-final-newline: %v", err)
	}

	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "Ok" {
		t.Errorf("text = %q, want Ok", gotText)
	}
}

// ─────────────────────────────────────────────────────────────
// Inline stream error ({"error":…} in SSE payload)
// ─────────────────────────────────────────────────────────────

func TestSSEInlineStreamError(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		writeLine(w, `data: {"error":{"message":"Model overloaded","type":"upstream_error","code":"UPSTREAM_ERROR"}}`+"\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream returned early error: %v", err)
	}
	stream := streamOut.GetStream()
	for range stream.Events() {
	}
	streamErr := stream.Err()
	if streamErr == nil {
		t.Fatal("expected stream error from inline error chunk, got nil")
	}
	// UPSTREAM_ERROR with status 0 → either ServiceUnavailableException or ModelErrorException
	var modelErr *types.ModelErrorException
	var svcErr *types.ServiceUnavailableException
	if !errors.As(streamErr, &modelErr) && !errors.As(streamErr, &svcErr) {
		t.Errorf("expected ModelErrorException or ServiceUnavailableException, got %T: %v", streamErr, streamErr)
	}
}

// Shape-B inline stream error must preserve request_id (spec §4, shape-B first).
func TestSSEInlineStreamErrorShapeBKeepsRequestID(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter) {
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n")
		writeLine(w, `data: {"success":false,"error":{"code":"STREAM_ERROR","message":"upstream died mid-stream","request_id":"req-stream-42","details":{}}}`+"\n\n")
	})
	defer srv.Close()

	streamOut, err := newStreamClient(srv.URL).ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("ConverseStream returned early error: %v", err)
	}
	stream := streamOut.GetStream()
	for range stream.Events() {
	}
	streamErr := stream.Err()
	if streamErr == nil {
		t.Fatal("expected stream error from inline shape-B error chunk, got nil")
	}
	var modelStreamErr *types.ModelStreamErrorException
	if !errors.As(streamErr, &modelStreamErr) {
		t.Fatalf("expected ModelStreamErrorException, got %T: %v", streamErr, streamErr)
	}
	if modelStreamErr.RequestID == nil || *modelStreamErr.RequestID != "req-stream-42" {
		t.Errorf("RequestID = %v, want req-stream-42 (shape-B request_id dropped?)", modelStreamErr.RequestID)
	}
	if modelStreamErr.ErrorCode != "STREAM_ERROR" {
		t.Errorf("ErrorCode = %q, want STREAM_ERROR", modelStreamErr.ErrorCode)
	}
}

// ─────────────────────────────────────────────────────────────
// Stream retry on 429 (before first SSE byte)
// ─────────────────────────────────────────────────────────────

func TestStreamRetryBefore429(t *testing.T) {
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			fmt.Fprintln(w, `{"error":{"message":"throttled","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		writeLine(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"index\":0,\"finish_reason\":null}]}\n\n")
		writeLine(w, "data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
		writeLine(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: 2,
	})
	streamOut, err := client.ConverseStream(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("expected success after stream retry, got: %v", err)
	}
	stream := streamOut.GetStream()
	events := collectStreamEvents(t, stream)
	if serr := stream.Err(); serr != nil {
		t.Fatalf("stream error: %v", serr)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("expected 2 attempts, got %d", n)
	}

	var gotText string
	for _, ev := range events {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if td, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText += td.Value
			}
		}
	}
	if gotText != "ok" {
		t.Errorf("text = %q, want ok", gotText)
	}
}

// ─────────────────────────────────────────────────────────────
// Missing API key for stream
// ─────────────────────────────────────────────────────────────

func TestMissingAPIKeyStream(t *testing.T) {
	t.Setenv("AIRESERVE_API_KEY", "")
	t.Setenv("AUDACITY_API_KEY", "")
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey:  "",
		BaseURL: "http://127.0.0.1:0",
	})
	_, err := client.ConverseStream(context.Background(), streamInput())
	var missingKey *types.MissingAPIKeyError
	if !errors.As(err, &missingKey) {
		t.Errorf("expected MissingAPIKeyError, got %T: %v", err, err)
	}
}

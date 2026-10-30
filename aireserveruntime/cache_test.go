package aireserveruntime_test

// cache_test.go — conformance checklist item 12: cachePoint content blocks
// and prompt-cache usage counters.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

var cachePoint = &types.ContentBlockMemberCachePoint{
	Value: types.CachePointBlock{Type: types.CachePointTypeDefault},
}

// captureConverse sends one Converse call against a capturing server and
// returns the raw request body and the parsed response.
func captureConverse(t *testing.T, input *aireserveruntime.ConverseInput, usage map[string]interface{}) (map[string]interface{}, *aireserveruntime.ConverseOutput) {
	t.Helper()

	if usage == nil {
		usage = map[string]interface{}{
			"prompt_tokens":     10,
			"completion_tokens": 2,
			"total_tokens":      12,
		}
	}

	var captured map[string]interface{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("parse request body: %v", err)
		}
		jsonResponse(t, w, 200, map[string]interface{}{
			"id":    "chatcmpl-cache",
			"model": "claude-sonnet",
			"choices": []map[string]interface{}{{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "ok",
				},
			}},
			"usage": usage,
		})
	}

	client, _ := newTestClient(t, handler)
	resp, err := client.Converse(context.Background(), input)
	if err != nil {
		t.Fatalf("Converse returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("no request captured")
	}
	return captured, resp
}

func requestMessages(t *testing.T, body map[string]interface{}) []interface{} {
	t.Helper()
	msgs, ok := body["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages is not an array: %T", body["messages"])
	}
	return msgs
}

func contentParts(t *testing.T, msg interface{}) []interface{} {
	t.Helper()
	m, ok := msg.(map[string]interface{})
	if !ok {
		t.Fatalf("message is not an object: %T", msg)
	}
	parts, ok := m["content"].([]interface{})
	if !ok {
		t.Fatalf("content is not a parts array: %T", m["content"])
	}
	return parts
}

func assertEphemeralMarker(t *testing.T, part interface{}) {
	t.Helper()
	p := part.(map[string]interface{})
	cc, ok := p["cache_control"].(map[string]interface{})
	if !ok {
		t.Fatalf("part has no cache_control: %v", p)
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", cc["type"])
	}
}

func userTextInput(blocks ...types.ContentBlock) *aireserveruntime.ConverseInput {
	return &aireserveruntime.ConverseInput{
		ModelId: aireserve.String("claude-sonnet"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: blocks,
		}},
	}
}

func TestCachePointMarksPrecedingPart(t *testing.T) {
	body, _ := captureConverse(t, userTextInput(
		&types.ContentBlockMemberText{Value: "Big stable document."},
		cachePoint,
		&types.ContentBlockMemberText{Value: "Question?"},
	), nil)

	msgs := requestMessages(t, body)
	parts := contentParts(t, msgs[0])
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	first := parts[0].(map[string]interface{})
	if first["type"] != "text" || first["text"] != "Big stable document." {
		t.Errorf("unexpected first part: %v", first)
	}
	assertEphemeralMarker(t, parts[0])
	second := parts[1].(map[string]interface{})
	if _, hasMarker := second["cache_control"]; hasMarker {
		t.Errorf("second part must not carry cache_control: %v", second)
	}

	// cachePoint blocks never appear on the wire
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "cachePoint") {
		t.Error("cachePoint block leaked onto the wire")
	}
}

func TestMultipleCachePointsMarkEachPrecedingPart(t *testing.T) {
	body, _ := captureConverse(t, userTextInput(
		&types.ContentBlockMemberText{Value: "part one"},
		cachePoint,
		&types.ContentBlockMemberText{Value: "part two"},
		cachePoint,
	), nil)

	parts := contentParts(t, requestMessages(t, body)[0])
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	assertEphemeralMarker(t, parts[0])
	assertEphemeralMarker(t, parts[1])
}

func TestLeadingCachePointSilentlyIgnored(t *testing.T) {
	body, _ := captureConverse(t, userTextInput(
		cachePoint,
		&types.ContentBlockMemberText{Value: "Hi"},
	), nil)

	parts := contentParts(t, requestMessages(t, body)[0])
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	p := parts[0].(map[string]interface{})
	if _, hasMarker := p["cache_control"]; hasMarker {
		t.Errorf("no marker expected on any part: %v", p)
	}
}

func TestTextOnlyTurnWithoutCachePointStaysString(t *testing.T) {
	body, _ := captureConverse(t, userTextInput(
		&types.ContentBlockMemberText{Value: "a"},
		&types.ContentBlockMemberText{Value: "b"},
	), nil)

	msg := requestMessages(t, body)[0].(map[string]interface{})
	if msg["content"] != "a\nb" {
		t.Errorf("content = %v, want plain string a\\nb", msg["content"])
	}
}

func TestCachePointInSystem(t *testing.T) {
	input := userTextInput(&types.ContentBlockMemberText{Value: "Hi"})
	input.System = []types.SystemContentBlock{
		{Text: "You are a helpful assistant."},
		{CachePoint: &types.CachePointBlock{Type: types.CachePointTypeDefault}},
	}
	body, _ := captureConverse(t, input, nil)

	msgs := requestMessages(t, body)
	sys := msgs[0].(map[string]interface{})
	if sys["role"] != "system" {
		t.Fatalf("first message role = %v, want system", sys["role"])
	}
	parts := contentParts(t, msgs[0])
	assertEphemeralMarker(t, parts[0])
}

func TestSystemWithoutCachePointUnchanged(t *testing.T) {
	input := userTextInput(&types.ContentBlockMemberText{Value: "Hi"})
	input.System = []types.SystemContentBlock{{Text: "A"}, {Text: "B"}}
	body, _ := captureConverse(t, input, nil)

	sys := requestMessages(t, body)[0].(map[string]interface{})
	if sys["content"] != "A\n\nB" {
		t.Errorf("system content = %v, want joined string", sys["content"])
	}
}

func TestCachePointAfterToolResultIgnored(t *testing.T) {
	body, _ := captureConverse(t, userTextInput(
		&types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
			ToolUseId: "c1",
			Content:   []types.ToolResultContentBlock{&types.ToolResultContentMemberText{Value: "42"}},
		}},
		cachePoint,
		&types.ContentBlockMemberText{Value: "next"},
	), nil)

	msgs := requestMessages(t, body)
	toolMsg := msgs[0].(map[string]interface{})
	if toolMsg["role"] != "tool" {
		t.Fatalf("first message role = %v, want tool", toolMsg["role"])
	}
	raw, _ := json.Marshal(toolMsg)
	if strings.Contains(string(raw), "cache_control") {
		t.Error("tool message must not carry cache_control")
	}
	parts := contentParts(t, msgs[1])
	p := parts[0].(map[string]interface{})
	if _, hasMarker := p["cache_control"]; hasMarker {
		t.Errorf("no marker expected: cachePoint had no preceding part")
	}
}

func TestCachePointInAssistantTurn(t *testing.T) {
	input := &aireserveruntime.ConverseInput{
		ModelId: aireserve.String("claude-sonnet"),
		Messages: []types.Message{
			{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Hi"}},
			},
			{
				Role: types.ConversationRoleAssistant,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: "Long answer."},
					cachePoint,
				},
			},
			{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Go on"}},
			},
		},
	}
	body, _ := captureConverse(t, input, nil)

	parts := contentParts(t, requestMessages(t, body)[1])
	assertEphemeralMarker(t, parts[0])
}

func TestUsageCacheCountersMapped(t *testing.T) {
	_, resp := captureConverse(t,
		userTextInput(&types.ContentBlockMemberText{Value: "Hi"}),
		map[string]interface{}{
			"prompt_tokens":     2048,
			"completion_tokens": 50,
			"total_tokens":      2098,
			"prompt_tokens_details": map[string]interface{}{
				"cached_tokens": 1024,
			},
			"cache_creation_input_tokens": 512,
		})

	if resp.Usage.CacheReadInputTokens != 1024 {
		t.Errorf("CacheReadInputTokens = %d, want 1024", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.CacheWriteInputTokens != 512 {
		t.Errorf("CacheWriteInputTokens = %d, want 512", resp.Usage.CacheWriteInputTokens)
	}
}

func TestUsageCacheCountersDefaultZero(t *testing.T) {
	_, resp := captureConverse(t,
		userTextInput(&types.ContentBlockMemberText{Value: "Hi"}), nil)

	if resp.Usage.CacheReadInputTokens != 0 || resp.Usage.CacheWriteInputTokens != 0 {
		t.Errorf("cache counters = %d/%d, want 0/0",
			resp.Usage.CacheReadInputTokens, resp.Usage.CacheWriteInputTokens)
	}
}

func TestStreamMetadataCacheCounters(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,` +
			`"prompt_tokens_details":{"cached_tokens":90},"cache_creation_input_tokens":10}}`,
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, c := range chunks {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}

	client, _ := newTestClient(t, handler)
	resp, err := client.ConverseStream(context.Background(), &aireserveruntime.ConverseStreamInput{
		ModelId: aireserve.String("claude-sonnet"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Hi"}},
		}},
	})
	if err != nil {
		t.Fatalf("ConverseStream returned error: %v", err)
	}

	stream := resp.GetStream()
	var metadata *types.ConverseStreamMetadataEvent
	for event := range stream.Events() {
		if m, ok := event.(*types.ConverseStreamOutputMemberMetadata); ok {
			metadata = &m.Value
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if metadata == nil {
		t.Fatal("no metadata event received")
	}
	if metadata.Usage.CacheReadInputTokens != 90 {
		t.Errorf("CacheReadInputTokens = %d, want 90", metadata.Usage.CacheReadInputTokens)
	}
	if metadata.Usage.CacheWriteInputTokens != 10 {
		t.Errorf("CacheWriteInputTokens = %d, want 10", metadata.Usage.CacheWriteInputTokens)
	}
}

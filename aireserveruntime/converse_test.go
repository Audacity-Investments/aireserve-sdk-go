package aireserveruntime_test

// converse_test.go — conformance checklist items 1, 3 (non-streaming), 4, 7, 9, 10.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// ─────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────

func newTestClient(t *testing.T, handler http.HandlerFunc) (*aireserveruntime.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		MaxRetries: aireserveruntime.NoRetries,
	})
	return client, srv
}

func jsonResponse(t *testing.T, w http.ResponseWriter, status int, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("failed to encode response: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 1 — Converse happy path
// ─────────────────────────────────────────────────────────────

func TestConverseHappyPath(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Validate headers
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "aireserve-sdk-go/") {
			t.Errorf("User-Agent = %q, want aireserve-sdk-go/…", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		// Validate request body includes the model field
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("parse request body: %v", err)
			return
		}
		if req["model"] != "gpt-5.4-mini" {
			t.Errorf("model = %v, want gpt-5.4-mini", req["model"])
		}
		// stream must not be set for non-streaming
		if _, ok := req["stream"]; ok {
			t.Errorf("expected no stream field in Converse request")
		}

		jsonResponse(t, w, 200, map[string]interface{}{
			"id":    "chatcmpl-1",
			"model": "gpt-5.4-mini",
			"choices": []map[string]interface{}{{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello there!",
				},
			}},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 3,
				"total_tokens":      13,
			},
		})
	}

	client, _ := newTestClient(t, handler)
	resp, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId: aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Hi"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aireserve.Int32(500)},
	})
	if err != nil {
		t.Fatalf("Converse returned error: %v", err)
	}

	// Output
	msgOut, ok := resp.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		t.Fatalf("Output is not *ConverseOutputMemberMessage, got %T", resp.Output)
	}
	if len(msgOut.Value.Content) == 0 {
		t.Fatal("expected at least one content block")
	}
	textBlock, ok := msgOut.Value.Content[0].(*types.ContentBlockMemberText)
	if !ok {
		t.Fatalf("content[0] is not text, got %T", msgOut.Value.Content[0])
	}
	if textBlock.Value != "Hello there!" {
		t.Errorf("text = %q, want Hello there!", textBlock.Value)
	}

	// StopReason
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}

	// Usage
	if resp.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 13 {
		t.Errorf("Usage = %+v, want {10 3 13}", resp.Usage)
	}

	// Latency is measured client-side — just check it's positive
	if resp.Metrics == nil || resp.Metrics.LatencyMs < 0 {
		t.Errorf("Metrics = %v, want non-nil and non-negative latency", resp.Metrics)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 3 (non-streaming) — tool round-trip
// ─────────────────────────────────────────────────────────────

func TestConverseToolRoundTrip(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("parse request: %v", err)
			return
		}

		// Verify tools were serialised correctly
		tools, ok := req["tools"].([]interface{})
		if !ok || len(tools) == 0 {
			t.Errorf("expected tools array in request, got %v", req["tools"])
		} else {
			tool := tools[0].(map[string]interface{})
			fn := tool["function"].(map[string]interface{})
			if fn["name"] != "get_weather" {
				t.Errorf("tool name = %v, want get_weather", fn["name"])
			}
		}

		// Verify tool_choice
		if req["tool_choice"] != "auto" {
			t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
		}

		// Return a tool_calls response
		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index":         0,
				"finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]interface{}{{
						"id":   "call_abc",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "get_weather",
							"arguments": `{"city":"London"}`,
						},
					}},
				},
			}},
			"usage": map[string]interface{}{
				"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30,
			},
		})
	}

	client, _ := newTestClient(t, handler)
	resp, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId: aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Weather in London?"}},
		}},
		ToolConfig: &types.ToolConfiguration{
			Tools: []types.Tool{{
				ToolSpec: &types.ToolSpecification{
					Name:        "get_weather",
					Description: aireserve.String("Get weather"),
					InputSchema: &types.ToolInputSchema{
						Json: map[string]interface{}{
							"type":       "object",
							"properties": map[string]interface{}{"city": map[string]interface{}{"type": "string"}},
						},
					},
				},
			}},
			ToolChoice: &types.ToolChoiceMemberAuto{},
		},
	})
	if err != nil {
		t.Fatalf("Converse error: %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}

	msgOut := resp.Output.(*types.ConverseOutputMemberMessage)
	if len(msgOut.Value.Content) == 0 {
		t.Fatal("expected content blocks")
	}
	tuBlock, ok := msgOut.Value.Content[0].(*types.ContentBlockMemberToolUse)
	if !ok {
		t.Fatalf("content[0] is %T, want *ContentBlockMemberToolUse", msgOut.Value.Content[0])
	}
	if tuBlock.Value.ToolUseId != "call_abc" {
		t.Errorf("ToolUseId = %q, want call_abc", tuBlock.Value.ToolUseId)
	}
	if tuBlock.Value.Name != "get_weather" {
		t.Errorf("Name = %q, want get_weather", tuBlock.Value.Name)
	}
	inputMap, ok := tuBlock.Value.Input.(map[string]interface{})
	if !ok {
		t.Fatalf("Input is %T, want map", tuBlock.Value.Input)
	}
	if inputMap["city"] != "London" {
		t.Errorf("input.city = %v, want London", inputMap["city"])
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 4 — toolResult user turn → role:tool message
// ─────────────────────────────────────────────────────────────

func TestToolResultSerialization(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("parse request: %v", err)
			return
		}

		messages, _ := req["messages"].([]interface{})
		// Expected: system (none), user text → messages[0] = user "Weather?"
		// assistant tool_call → messages[1]
		// tool result → messages[2] (role:tool)
		// user text (none in this case — only toolResult) → no extra user message
		var toolMsg map[string]interface{}
		for _, m := range messages {
			mm := m.(map[string]interface{})
			if mm["role"] == "tool" {
				toolMsg = mm
				break
			}
		}
		if toolMsg == nil {
			t.Error("expected a role:tool message, got none")
			t.Logf("messages: %+v", messages)
		} else {
			if toolMsg["tool_call_id"] != "call_xyz" {
				t.Errorf("tool_call_id = %v, want call_xyz", toolMsg["tool_call_id"])
			}
			if !strings.Contains(toolMsg["content"].(string), "sunny") {
				t.Errorf("tool content = %v, want to contain 'sunny'", toolMsg["content"])
			}
		}

		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "It is sunny in London.",
				},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}

	client, _ := newTestClient(t, handler)
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId: aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{
			{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Weather?"}},
			},
			{
				Role: types.ConversationRoleAssistant,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
						ToolUseId: "call_xyz",
						Name:      "get_weather",
						Input:     map[string]interface{}{"city": "London"},
					}},
				},
			},
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
						ToolUseId: "call_xyz",
						Content: []types.ToolResultContentBlock{
							&types.ToolResultContentMemberText{Value: "It is sunny"},
						},
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Converse error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 7 — defensive unwrap (envelope regression)
// ─────────────────────────────────────────────────────────────

func TestDefensiveUnwrap(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Server returns a {success, data:{...}} envelope
		jsonResponse(t, w, 200, map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"id":    "chatcmpl-wrapped",
				"model": "gpt-5.4-mini",
				"choices": []map[string]interface{}{{
					"index":         0,
					"finish_reason": "stop",
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Unwrapped correctly",
					},
				}},
				"usage": map[string]interface{}{
					"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7,
				},
			},
		})
	}

	client, _ := newTestClient(t, handler)
	resp, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Converse error: %v", err)
	}
	msgOut := resp.Output.(*types.ConverseOutputMemberMessage)
	text := msgOut.Value.Content[0].(*types.ContentBlockMemberText).Value
	if text != "Unwrapped correctly" {
		t.Errorf("text = %q, want Unwrapped correctly", text)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 9 — missing API key
// ─────────────────────────────────────────────────────────────

func TestMissingAPIKey(t *testing.T) {
	// Ensure neither API-key env var satisfies the key requirement.
	t.Setenv("AIRESERVE_API_KEY", "")
	t.Setenv("AUDACITY_API_KEY", "")
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey:  "",
		BaseURL: "http://127.0.0.1:0",
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	var missingKey *types.MissingAPIKeyError
	if !errors.As(err, &missingKey) {
		t.Errorf("expected MissingAPIKeyError, got %T: %v", err, err)
	}
}

// ─────────────────────────────────────────────────────────────
// Configuration resolution — AIRESERVE_* preferred, legacy AUDACITY_* fallback
// ─────────────────────────────────────────────────────────────

func TestEnvVarPrecedence(t *testing.T) {
	newAuthCaptureServer := func(t *testing.T) (*httptest.Server, func() string) {
		t.Helper()
		var mu sync.Mutex
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotAuth = r.Header.Get("Authorization")
			mu.Unlock()
			jsonResponse(t, w, 200, map[string]interface{}{
				"choices": []map[string]interface{}{{
					"index": 0, "finish_reason": "stop",
					"message": map[string]interface{}{"role": "assistant", "content": "ok"},
				}},
				"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		}))
		t.Cleanup(srv.Close)
		return srv, func() string {
			mu.Lock()
			defer mu.Unlock()
			return gotAuth
		}
	}

	converse := func(t *testing.T, client *aireserveruntime.Client) {
		t.Helper()
		if _, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
			ModelId:  aireserve.String("gpt-5.4-mini"),
			Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
		}); err != nil {
			t.Fatalf("Converse error: %v", err)
		}
	}

	t.Run("AIRESERVE_API_KEY wins over legacy AUDACITY_API_KEY", func(t *testing.T) {
		srv, gotAuth := newAuthCaptureServer(t)
		t.Setenv("AIRESERVE_API_KEY", "new-key")
		t.Setenv("AUDACITY_API_KEY", "legacy-key")
		converse(t, aireserveruntime.New(aireserveruntime.Options{BaseURL: srv.URL}))
		if got := gotAuth(); got != "Bearer new-key" {
			t.Errorf("Authorization = %q, want Bearer new-key", got)
		}
	})

	t.Run("legacy AUDACITY_API_KEY still works", func(t *testing.T) {
		srv, gotAuth := newAuthCaptureServer(t)
		t.Setenv("AIRESERVE_API_KEY", "")
		t.Setenv("AUDACITY_API_KEY", "legacy-key")
		converse(t, aireserveruntime.New(aireserveruntime.Options{BaseURL: srv.URL}))
		if got := gotAuth(); got != "Bearer legacy-key" {
			t.Errorf("Authorization = %q, want Bearer legacy-key", got)
		}
	})

	t.Run("explicit APIKey wins over both env vars", func(t *testing.T) {
		srv, gotAuth := newAuthCaptureServer(t)
		t.Setenv("AIRESERVE_API_KEY", "new-key")
		t.Setenv("AUDACITY_API_KEY", "legacy-key")
		converse(t, aireserveruntime.New(aireserveruntime.Options{APIKey: "explicit-key", BaseURL: srv.URL}))
		if got := gotAuth(); got != "Bearer explicit-key" {
			t.Errorf("Authorization = %q, want Bearer explicit-key", got)
		}
	})

	t.Run("AIRESERVE_BASE_URL wins over legacy AUDACITY_BASE_URL", func(t *testing.T) {
		srv, _ := newAuthCaptureServer(t)
		t.Setenv("AIRESERVE_BASE_URL", srv.URL)
		t.Setenv("AUDACITY_BASE_URL", "http://127.0.0.1:1") // nothing listens here
		converse(t, aireserveruntime.New(aireserveruntime.Options{APIKey: "k", MaxRetries: aireserveruntime.NoRetries}))
	})

	t.Run("legacy AUDACITY_BASE_URL still works", func(t *testing.T) {
		srv, _ := newAuthCaptureServer(t)
		t.Setenv("AIRESERVE_BASE_URL", "")
		t.Setenv("AUDACITY_BASE_URL", srv.URL)
		converse(t, aireserveruntime.New(aireserveruntime.Options{APIKey: "k", MaxRetries: aireserveruntime.NoRetries}))
	})

	t.Run("explicit BaseURL wins over both env vars", func(t *testing.T) {
		srv, _ := newAuthCaptureServer(t)
		t.Setenv("AIRESERVE_BASE_URL", "http://127.0.0.1:1")
		t.Setenv("AUDACITY_BASE_URL", "http://127.0.0.1:1")
		converse(t, aireserveruntime.New(aireserveruntime.Options{APIKey: "k", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries}))
	})
}

// ─────────────────────────────────────────────────────────────
// Checklist item 10 — User-Agent header
// ─────────────────────────────────────────────────────────────

func TestUserAgent(t *testing.T) {
	var mu sync.Mutex
	var gotUA string
	handler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUA = r.Header.Get("User-Agent")
		mu.Unlock()
		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}
	client, _ := newTestClient(t, handler)
	client.Converse(context.Background(), &aireserveruntime.ConverseInput{ //nolint:errcheck
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(gotUA, "aireserve-sdk-go/") {
		t.Errorf("User-Agent = %q, want prefix aireserve-sdk-go/", gotUA)
	}
}

// ─────────────────────────────────────────────────────────────
// Additional — system message serialisation
// ─────────────────────────────────────────────────────────────

func TestSystemMessages(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req) //nolint:errcheck
		messages := req["messages"].([]interface{})
		first := messages[0].(map[string]interface{})
		if first["role"] != "system" {
			t.Errorf("first message role = %v, want system", first["role"])
		}
		if !strings.Contains(first["content"].(string), "helpful") {
			t.Errorf("system content = %v, want to contain 'helpful'", first["content"])
		}

		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]interface{}{"role": "assistant", "content": "Sure"},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
		})
	}
	client, _ := newTestClient(t, handler)
	client.Converse(context.Background(), &aireserveruntime.ConverseInput{ //nolint:errcheck
		ModelId: aireserve.String("gpt-5.4-mini"),
		System:  []types.SystemContentBlock{{Text: "You are helpful"}, {Text: "Be concise"}},
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}},
		}},
	})
}

// ─────────────────────────────────────────────────────────────
// Additional — additionalModelRequestFields shallow merge
// ─────────────────────────────────────────────────────────────

func TestAdditionalModelRequestFields(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req) //nolint:errcheck
		if req["custom_field"] != "custom_value" {
			t.Errorf("custom_field = %v, want custom_value", req["custom_field"])
		}
		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]interface{}{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}
	client, _ := newTestClient(t, handler)
	client.Converse(context.Background(), &aireserveruntime.ConverseInput{ //nolint:errcheck
		ModelId:                      aireserve.String("gpt-5.4-mini"),
		Messages:                     []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
		AdditionalModelRequestFields: map[string]interface{}{"custom_field": "custom_value"},
	})
}

// ─────────────────────────────────────────────────────────────
// Checklist item 15 — mediaResolution request field
// ─────────────────────────────────────────────────────────────

func TestMediaResolution(t *testing.T) {
	captureBody := func(t *testing.T, input *aireserveruntime.ConverseInput) map[string]interface{} {
		t.Helper()
		var got map[string]interface{}
		handler := func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("parse request body: %v", err)
			}
			jsonResponse(t, w, 200, map[string]interface{}{
				"choices": []map[string]interface{}{{
					"index": 0, "finish_reason": "stop",
					"message": map[string]interface{}{"role": "assistant", "content": "ok"},
				}},
				"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		}
		client, _ := newTestClient(t, handler)
		if _, err := client.Converse(context.Background(), input); err != nil {
			t.Fatalf("Converse error: %v", err)
		}
		return got
	}

	messages := []types.Message{{
		Role:    types.ConversationRoleUser,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Hi"}},
	}}

	t.Run("present", func(t *testing.T) {
		for _, res := range []types.MediaResolution{
			types.MediaResolutionLow,
			types.MediaResolutionMedium,
			types.MediaResolutionHigh,
			types.MediaResolutionUltraHigh,
		} {
			body := captureBody(t, &aireserveruntime.ConverseInput{
				ModelId:         aireserve.String("gemini-2.5-flash"),
				Messages:        messages,
				MediaResolution: res,
			})
			if body["media_resolution"] != string(res) {
				t.Errorf("media_resolution = %v, want %q", body["media_resolution"], res)
			}
		}
	})

	t.Run("absent", func(t *testing.T) {
		body := captureBody(t, &aireserveruntime.ConverseInput{
			ModelId:  aireserve.String("gemini-2.5-flash"),
			Messages: messages,
		})
		if v, ok := body["media_resolution"]; ok {
			t.Errorf("media_resolution present = %v, want key absent", v)
		}
	})
}

// ─────────────────────────────────────────────────────────────
// Checklist item 20 — guardrailConfig passthrough (AIR-731)
// ─────────────────────────────────────────────────────────────

func TestGuardrailConfig(t *testing.T) {
	captureBody := func(t *testing.T, input *aireserveruntime.ConverseInput) map[string]interface{} {
		t.Helper()
		var got map[string]interface{}
		handler := func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("parse request body: %v", err)
			}
			jsonResponse(t, w, 200, map[string]interface{}{
				"choices": []map[string]interface{}{{
					"index": 0, "finish_reason": "stop",
					"message": map[string]interface{}{"role": "assistant", "content": "ok"},
				}},
				"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		}
		client, _ := newTestClient(t, handler)
		if _, err := client.Converse(context.Background(), input); err != nil {
			t.Fatalf("Converse error: %v", err)
		}
		return got
	}

	messages := []types.Message{{
		Role:    types.ConversationRoleUser,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Hi"}},
	}}

	t.Run("forwarded verbatim", func(t *testing.T) {
		body := captureBody(t, &aireserveruntime.ConverseInput{
			ModelId:  aireserve.String("claude-sonnet-4-6"),
			Messages: messages,
			GuardrailConfig: &types.GuardrailConfiguration{
				GuardrailIdentifier:  aireserve.String("gr-abc123"),
				GuardrailVersion:     aireserve.String("1"),
				Trace:                types.GuardrailTraceEnabled,
				StreamProcessingMode: types.GuardrailStreamProcessingModeSync,
			},
		})
		want := map[string]interface{}{
			"guardrailIdentifier":  "gr-abc123",
			"guardrailVersion":     "1",
			"trace":                "enabled",
			"streamProcessingMode": "sync",
		}
		got, ok := body["guardrailConfig"].(map[string]interface{})
		if !ok {
			t.Fatalf("guardrailConfig = %v (%T), want object", body["guardrailConfig"], body["guardrailConfig"])
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("guardrailConfig = %v, want %v", got, want)
		}
	})

	t.Run("absent optional members omitted", func(t *testing.T) {
		body := captureBody(t, &aireserveruntime.ConverseInput{
			ModelId:  aireserve.String("claude-sonnet-4-6"),
			Messages: messages,
			GuardrailConfig: &types.GuardrailConfiguration{
				GuardrailIdentifier: aireserve.String("gr-abc123"),
				GuardrailVersion:    aireserve.String("1"),
			},
		})
		got, ok := body["guardrailConfig"].(map[string]interface{})
		if !ok {
			t.Fatalf("guardrailConfig = %v (%T), want object", body["guardrailConfig"], body["guardrailConfig"])
		}
		for _, key := range []string{"trace", "streamProcessingMode"} {
			if v, present := got[key]; present {
				t.Errorf("%s present = %v, want omitted", key, v)
			}
		}
	})

	t.Run("absent when unset", func(t *testing.T) {
		body := captureBody(t, &aireserveruntime.ConverseInput{
			ModelId:  aireserve.String("claude-sonnet-4-6"),
			Messages: messages,
		})
		if v, ok := body["guardrailConfig"]; ok {
			t.Errorf("guardrailConfig present = %v, want key absent", v)
		}
	})
}

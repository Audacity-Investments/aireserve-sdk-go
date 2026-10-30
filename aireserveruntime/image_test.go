package aireserveruntime_test

// image_test.go — conformance checklist item 11: image content blocks.
// Mirrors the Python reference tests (TestImageBlocks in test_sdk.py).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// captureMessages runs Converse with the given messages against a stub server
// and returns the "messages" array of the OpenAI request body it produced.
func captureMessages(t *testing.T, messages []types.Message) []interface{} {
	t.Helper()
	var got []interface{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("parse request body: %v", err)
			return
		}
		got, _ = req["messages"].([]interface{})
		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]interface{}{"role": "assistant", "content": "A cat."},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 50, "completion_tokens": 3, "total_tokens": 53},
		})
	}
	client, _ := newTestClient(t, handler)
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.5"),
		Messages: messages,
	})
	if err != nil {
		t.Fatalf("Converse error: %v", err)
	}
	return got
}

// partTypes returns the "type" field of each content part in order.
func partTypes(t *testing.T, content interface{}) []string {
	t.Helper()
	parts, ok := content.([]interface{})
	if !ok {
		t.Fatalf("content is %T, want array of parts", content)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.(map[string]interface{})["type"].(string))
	}
	return out
}

func imageURL(t *testing.T, part interface{}) string {
	t.Helper()
	m, ok := part.(map[string]interface{})
	if !ok || m["type"] != "image_url" {
		t.Fatalf("part = %v, want image_url part", part)
	}
	return m["image_url"].(map[string]interface{})["url"].(string)
}

func userMessage(msgs []interface{}, i int) map[string]interface{} {
	return msgs[i].(map[string]interface{})
}

func TestImageBlocks(t *testing.T) {
	pngRaw := []byte("\x89PNG\r\n\x1a\nfakebytes")
	pngB64 := base64.StdEncoding.EncodeToString(pngRaw)
	httpsURL := "https://example.com/cat.png"

	imageBlock := func(format types.ImageFormat, source types.ImageSource) types.ContentBlock {
		return &types.ContentBlockMemberImage{Value: types.ImageBlock{Format: format, Source: source}}
	}
	textBlock := func(s string) types.ContentBlock {
		return &types.ContentBlockMemberText{Value: s}
	}

	cases := []struct {
		name     string
		messages []types.Message
		verify   func(t *testing.T, msgs []interface{})
	}{
		{
			name: "bytes to data URL part",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					textBlock("What is in this image?"),
					imageBlock(types.ImageFormatPng, &types.ImageSourceMemberBytes{Value: pngRaw}),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				content := userMessage(msgs, 0)["content"]
				got := partTypes(t, content)
				want := []string{"text", "image_url"}
				if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
					t.Fatalf("part types = %v, want %v", got, want)
				}
				parts := content.([]interface{})
				textPart := parts[0].(map[string]interface{})
				if textPart["text"] != "What is in this image?" {
					t.Errorf("text = %v, want What is in this image?", textPart["text"])
				}
				wantURL := "data:image/png;base64," + pngB64
				if url := imageURL(t, parts[1]); url != wantURL {
					t.Errorf("url = %q, want %q", url, wantURL)
				}
			},
		},
		{
			name: "jpeg media type",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					imageBlock(types.ImageFormatJpeg, &types.ImageSourceMemberBytes{Value: []byte("jpegdata")}),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				parts := userMessage(msgs, 0)["content"].([]interface{})
				wantURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("jpegdata"))
				if url := imageURL(t, parts[0]); url != wantURL {
					t.Errorf("url = %q, want %q", url, wantURL)
				}
			},
		},
		{
			name: "url source passed verbatim",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					textBlock("Describe"),
					imageBlock(types.ImageFormatPng, &types.ImageSourceMemberUrl{Value: httpsURL}),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				parts := userMessage(msgs, 0)["content"].([]interface{})
				if url := imageURL(t, parts[1]); url != httpsURL {
					t.Errorf("url = %q, want %q", url, httpsURL)
				}
			},
		},
		{
			name: "block order preserved",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					imageBlock(types.ImageFormatPng, &types.ImageSourceMemberBytes{Value: []byte("a")}),
					textBlock("middle"),
					imageBlock(types.ImageFormatGif, &types.ImageSourceMemberBytes{Value: []byte("b")}),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				content := userMessage(msgs, 0)["content"]
				got := partTypes(t, content)
				want := []string{"image_url", "text", "image_url"}
				if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
					t.Fatalf("part types = %v, want %v", got, want)
				}
				middle := content.([]interface{})[1].(map[string]interface{})
				if middle["text"] != "middle" {
					t.Errorf("middle text = %v, want middle", middle["text"])
				}
			},
		},
		{
			name: "text-only turn stays plain string",
			messages: []types.Message{{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{textBlock("a"), textBlock("b")},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				content := userMessage(msgs, 0)["content"]
				if content != "a\nb" {
					t.Errorf("content = %v (%T), want plain string \"a\\nb\"", content, content)
				}
			},
		},
		{
			name: "toolResult message emitted before user parts array",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
						ToolUseId: "call_1",
						Content: []types.ToolResultContentBlock{
							&types.ToolResultContentMemberText{Value: "42"},
						},
					}},
					textBlock("And this image?"),
					imageBlock(types.ImageFormatPng, &types.ImageSourceMemberBytes{Value: []byte("x")}),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				if len(msgs) != 2 {
					t.Fatalf("len(messages) = %d, want 2", len(msgs))
				}
				toolMsg := userMessage(msgs, 0)
				if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" {
					t.Errorf("messages[0] = %v, want role:tool tool_call_id:call_1", toolMsg)
				}
				userMsg := userMessage(msgs, 1)
				if userMsg["role"] != "user" {
					t.Errorf("messages[1] role = %v, want user", userMsg["role"])
				}
				got := partTypes(t, userMsg["content"])
				if len(got) != 2 || got[0] != "text" || got[1] != "image_url" {
					t.Errorf("part types = %v, want [text image_url]", got)
				}
			},
		},
		{
			name: "image in assistant turn ignored",
			messages: []types.Message{
				{
					Role:    types.ConversationRoleUser,
					Content: []types.ContentBlock{textBlock("Hi")},
				},
				{
					Role: types.ConversationRoleAssistant,
					Content: []types.ContentBlock{
						textBlock("Sure"),
						imageBlock(types.ImageFormatPng, &types.ImageSourceMemberBytes{Value: []byte("x")}),
					},
				},
				{
					Role:    types.ConversationRoleUser,
					Content: []types.ContentBlock{textBlock("Go on")},
				},
			},
			verify: func(t *testing.T, msgs []interface{}) {
				if len(msgs) != 3 {
					t.Fatalf("len(messages) = %d, want 3", len(msgs))
				}
				assistant := userMessage(msgs, 1)
				if assistant["role"] != "assistant" {
					t.Fatalf("messages[1] role = %v, want assistant", assistant["role"])
				}
				if assistant["content"] != "Sure" {
					t.Errorf("assistant content = %v (%T), want plain string \"Sure\"", assistant["content"], assistant["content"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := captureMessages(t, tc.messages)
			tc.verify(t, msgs)
		})
	}
}

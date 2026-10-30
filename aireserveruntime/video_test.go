package aireserveruntime_test

// video_test.go — conformance checklist item 13: video content blocks.
// Mirrors image_test.go, reusing its captureMessages/partTypes helpers.

import (
	"encoding/base64"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

func videoBlock(format types.VideoFormat, data []byte) types.ContentBlock {
	return &types.ContentBlockMemberVideo{Value: types.VideoBlock{
		Format: format,
		Source: &types.VideoSourceMemberBytes{Value: data},
	}}
}

func videoURIBlock(format types.VideoFormat, uri string) types.ContentBlock {
	return &types.ContentBlockMemberVideo{Value: types.VideoBlock{
		Format: format,
		Source: &types.VideoSourceMemberURI{Value: uri},
	}}
}

// filePart asserts the part is a file content part and returns its "file" object.
func filePart(t *testing.T, part interface{}) map[string]interface{} {
	t.Helper()
	m, ok := part.(map[string]interface{})
	if !ok || m["type"] != "file" {
		t.Fatalf("part = %v, want file part", part)
	}
	f, ok := m["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("file part has no file object: %v", m)
	}
	return f
}

// assertVideoFile checks the file part's format and data-URL against the
// expected MIME type and raw bytes (valid base64 round-trip).
func assertVideoFile(t *testing.T, part interface{}, wantMIME string, wantRaw []byte) {
	t.Helper()
	f := filePart(t, part)
	if f["format"] != wantMIME {
		t.Errorf("file.format = %v, want %v", f["format"], wantMIME)
	}
	wantData := "data:" + wantMIME + ";base64," + base64.StdEncoding.EncodeToString(wantRaw)
	if f["file_data"] != wantData {
		t.Errorf("file.file_data = %v, want %v", f["file_data"], wantData)
	}
}

func TestVideoBlocks(t *testing.T) {
	mp4Raw := []byte("\x00\x00\x00\x18ftypmp42fakevideobytes")
	textBlock := func(s string) types.ContentBlock {
		return &types.ContentBlockMemberText{Value: s}
	}

	cases := []struct {
		name     string
		messages []types.Message
		verify   func(t *testing.T, msgs []interface{})
	}{
		{
			name: "bytes to file part",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					textBlock("What happens in this video?"),
					videoBlock(types.VideoFormatMp4, mp4Raw),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				content := userMessage(msgs, 0)["content"]
				got := partTypes(t, content)
				if len(got) != 2 || got[0] != "text" || got[1] != "file" {
					t.Fatalf("part types = %v, want [text file]", got)
				}
				parts := content.([]interface{})
				textPart := parts[0].(map[string]interface{})
				if textPart["text"] != "What happens in this video?" {
					t.Errorf("text = %v, want What happens in this video?", textPart["text"])
				}
				assertVideoFile(t, parts[1], "video/mp4", mp4Raw)
			},
		},
		{
			name: "block order preserved",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					textBlock("before"),
					videoBlock(types.VideoFormatWebm, []byte("v")),
					textBlock("after"),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				content := userMessage(msgs, 0)["content"]
				got := partTypes(t, content)
				if len(got) != 3 || got[0] != "text" || got[1] != "file" || got[2] != "text" {
					t.Fatalf("part types = %v, want [text file text]", got)
				}
				parts := content.([]interface{})
				if parts[0].(map[string]interface{})["text"] != "before" ||
					parts[2].(map[string]interface{})["text"] != "after" {
					t.Errorf("text parts out of order: %v", parts)
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
			name: "video in assistant turn ignored",
			messages: []types.Message{
				{
					Role:    types.ConversationRoleUser,
					Content: []types.ContentBlock{textBlock("Hi")},
				},
				{
					Role: types.ConversationRoleAssistant,
					Content: []types.ContentBlock{
						textBlock("Sure"),
						videoBlock(types.VideoFormatMp4, []byte("x")),
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
					textBlock("And this video?"),
					videoBlock(types.VideoFormatMp4, []byte("x")),
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
				if len(got) != 2 || got[0] != "text" || got[1] != "file" {
					t.Errorf("part types = %v, want [text file]", got)
				}
			},
		},
		{
			name: "mixed image and video turn",
			messages: []types.Message{{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					textBlock("Compare these"),
					&types.ContentBlockMemberImage{Value: types.ImageBlock{
						Format: types.ImageFormatPng,
						Source: &types.ImageSourceMemberBytes{Value: []byte("img")},
					}},
					videoBlock(types.VideoFormatMov, []byte("vid")),
				},
			}},
			verify: func(t *testing.T, msgs []interface{}) {
				content := userMessage(msgs, 0)["content"]
				got := partTypes(t, content)
				if len(got) != 3 || got[0] != "text" || got[1] != "image_url" || got[2] != "file" {
					t.Fatalf("part types = %v, want [text image_url file]", got)
				}
				parts := content.([]interface{})
				wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("img"))
				if url := imageURL(t, parts[1]); url != wantURL {
					t.Errorf("image url = %q, want %q", url, wantURL)
				}
				assertVideoFile(t, parts[2], "video/mov", []byte("vid"))
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

// assertVideoFileURI checks the file part carries the verbatim file_id and
// MIME format, with no file_data (spec §3 uri mapping, checklist item 14).
func assertVideoFileURI(t *testing.T, part interface{}, wantURI, wantMIME string) {
	t.Helper()
	f := filePart(t, part)
	if f["file_id"] != wantURI {
		t.Errorf("file.file_id = %v, want %v", f["file_id"], wantURI)
	}
	if f["format"] != wantMIME {
		t.Errorf("file.format = %v, want %v", f["format"], wantMIME)
	}
	if _, hasData := f["file_data"]; hasData {
		t.Errorf("file part with uri source must not carry file_data: %v", f)
	}
}

func TestVideoURISource(t *testing.T) {
	const uri = "audacity://files/abc-123"
	textBlock := func(s string) types.ContentBlock {
		return &types.ContentBlockMemberText{Value: s}
	}

	t.Run("uri to file part with verbatim file_id", func(t *testing.T) {
		msgs := captureMessages(t, []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				textBlock("What happens in this video?"),
				videoURIBlock(types.VideoFormatMp4, uri),
			},
		}})
		content := userMessage(msgs, 0)["content"]
		got := partTypes(t, content)
		if len(got) != 2 || got[0] != "text" || got[1] != "file" {
			t.Fatalf("part types = %v, want [text file]", got)
		}
		assertVideoFileURI(t, content.([]interface{})[1], uri, "video/mp4")
	})

	t.Run("uri video in assistant turn ignored", func(t *testing.T) {
		msgs := captureMessages(t, []types.Message{
			{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{textBlock("Hi")},
			},
			{
				Role: types.ConversationRoleAssistant,
				Content: []types.ContentBlock{
					textBlock("Sure"),
					videoURIBlock(types.VideoFormatMp4, uri),
				},
			},
			{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{textBlock("Go on")},
			},
		})
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
	})

	t.Run("mixed bytes and uri turn", func(t *testing.T) {
		raw := []byte("inlinevid")
		msgs := captureMessages(t, []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				textBlock("Compare these clips"),
				videoBlock(types.VideoFormatWebm, raw),
				videoURIBlock(types.VideoFormatMov, uri),
			},
		}})
		content := userMessage(msgs, 0)["content"]
		got := partTypes(t, content)
		if len(got) != 3 || got[0] != "text" || got[1] != "file" || got[2] != "file" {
			t.Fatalf("part types = %v, want [text file file]", got)
		}
		parts := content.([]interface{})
		assertVideoFile(t, parts[1], "video/webm", raw)
		assertVideoFileURI(t, parts[2], uri, "video/mov")
	})
}

func TestVideoFormatMIMETable(t *testing.T) {
	raw := []byte("videodata")
	cases := []struct {
		format types.VideoFormat
		mime   string
	}{
		{types.VideoFormatMp4, "video/mp4"},
		{types.VideoFormatMov, "video/mov"},
		{types.VideoFormatMkv, "video/x-matroska"},
		{types.VideoFormatWebm, "video/webm"},
		{types.VideoFormatFlv, "video/x-flv"},
		{types.VideoFormatMpeg, "video/mpeg"},
		{types.VideoFormatMpg, "video/mpg"},
		{types.VideoFormatWmv, "video/wmv"},
		{types.VideoFormatThreeGp, "video/3gpp"},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			msgs := captureMessages(t, []types.Message{{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{videoBlock(tc.format, raw)},
			}})
			parts := userMessage(msgs, 0)["content"].([]interface{})
			assertVideoFile(t, parts[0], tc.mime, raw)
		})
	}
}

func TestCachePointAfterVideoPartMarksIt(t *testing.T) {
	body, _ := captureConverse(t, userTextInput(
		&types.ContentBlockMemberText{Value: "Watch this."},
		videoBlock(types.VideoFormatMp4, []byte("vid")),
		cachePoint,
		&types.ContentBlockMemberText{Value: "What happens?"},
	), nil)

	parts := contentParts(t, requestMessages(t, body)[0])
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	if got := parts[1].(map[string]interface{})["type"]; got != "file" {
		t.Fatalf("parts[1].type = %v, want file", got)
	}
	assertEphemeralMarker(t, parts[1])
	for _, i := range []int{0, 2} {
		p := parts[i].(map[string]interface{})
		if _, hasMarker := p["cache_control"]; hasMarker {
			t.Errorf("parts[%d] must not carry cache_control: %v", i, p)
		}
	}
}

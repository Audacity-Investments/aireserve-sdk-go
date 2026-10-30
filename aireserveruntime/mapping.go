package aireserveruntime

// mapping.go — normative §3 request/response translation between the
// Bedrock-shaped public API and the OpenAI wire format.

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// ─────────────────────────────────────────────────────────────
// Internal OpenAI wire types
// ─────────────────────────────────────────────────────────────

type oaiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OAI response/chunk structs

type oaiResponse struct {
	Choices []oaiChoice  `json:"choices"`
	Usage   *oaiUsage    `json:"usage,omitempty"`
	Data    *oaiResponse `json:"data,omitempty"` // defensive unwrap
}

type oaiChoice struct {
	Index        int     `json:"index"`
	Message      oaiMsg  `json:"message"`
	FinishReason *string `json:"finish_reason"`
}

type oaiMsg struct {
	Role      string        `json:"role"`
	Content   *string       `json:"content"` // nullable
	ToolCalls []oaiToolCall `json:"tool_calls,omitempty"`
}

type oaiUsage struct {
	PromptTokens        int32                   `json:"prompt_tokens"`
	CompletionTokens    int32                   `json:"completion_tokens"`
	TotalTokens         int32                   `json:"total_tokens"`
	PromptTokensDetails *oaiPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// Anthropic-style top-level cache counters (LiteLLM surfaces cache-write
	// tokens top-level; cache_read_input_tokens is a fallback spelling).
	CacheReadInputTokens     *int32 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int32 `json:"cache_creation_input_tokens,omitempty"`
}

type oaiPromptTokensDetails struct {
	CachedTokens *int32 `json:"cached_tokens,omitempty"`
}

// mapTokenUsage maps an OpenAI usage object to the Bedrock TokenUsage shape
// (spec §3), including the prompt-cache counters.
func mapTokenUsage(u *oaiUsage) *types.TokenUsage {
	usage := &types.TokenUsage{}
	if u == nil {
		return usage
	}
	usage.InputTokens = u.PromptTokens
	usage.OutputTokens = u.CompletionTokens
	usage.TotalTokens = u.TotalTokens
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		usage.CacheReadInputTokens = *u.PromptTokensDetails.CachedTokens
	} else if u.CacheReadInputTokens != nil {
		usage.CacheReadInputTokens = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		usage.CacheWriteInputTokens = *u.CacheCreationInputTokens
	}
	return usage
}

// Stream-specific chunk types

type oaiChunk struct {
	Choices []oaiStreamChoice `json:"choices"`
	Usage   *oaiUsage         `json:"usage,omitempty"`
	Error   json.RawMessage   `json:"error,omitempty"`
}

type oaiStreamChoice struct {
	Index        int             `json:"index"`
	Delta        *oaiStreamDelta `json:"delta"` // nil when the chunk has no delta key (or "delta": null)
	FinishReason *string         `json:"finish_reason"`
}

type oaiStreamDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   *string             `json:"content,omitempty"`
	ToolCalls []oaiStreamToolCall `json:"tool_calls,omitempty"`
}

type oaiStreamToolCall struct {
	Index    *int              `json:"index,omitempty"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function oaiStreamFunction `json:"function"`
}

type oaiStreamFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ─────────────────────────────────────────────────────────────
// Bedrock → OpenAI request mapping  (spec §3 "Converse input → OpenAI request")
// ─────────────────────────────────────────────────────────────

// buildRequestBody translates a Converse-shaped input (ConverseStreamInput is
// converted by the caller — the two types are identical) into the OpenAI wire body.
func buildRequestBody(input *ConverseInput, stream bool) ([]byte, error) {
	modelId := ""
	if input.ModelId != nil {
		modelId = *input.ModelId
	}

	messages, err := buildMessages(input)
	if err != nil {
		return nil, err
	}

	// Build the body as a map so additionalModelRequestFields can be shallow-merged last.
	body := map[string]interface{}{
		"model":    modelId,
		"messages": messages,
	}

	// §3 rule 4 — inferenceConfig
	if ic := input.InferenceConfig; ic != nil {
		if ic.MaxTokens != nil {
			body["max_tokens"] = *ic.MaxTokens
		}
		if ic.Temperature != nil {
			body["temperature"] = *ic.Temperature
		}
		if ic.TopP != nil {
			body["top_p"] = *ic.TopP
		}
		if len(ic.StopSequences) > 0 {
			body["stop"] = ic.StopSequences
		}
	}

	// §3 rule 4 — mediaResolution → top-level media_resolution (omit when
	// absent; no client-side model gating)
	if input.MediaResolution != "" {
		body["media_resolution"] = string(input.MediaResolution)
	}

	// §3 rule 4 — guardrailConfig forwarded verbatim (omit absent members,
	// omit the key when unset): the gateway rejects Bedrock guardrails with
	// an honest 400, and that rejection must reach the caller.
	if gc := input.GuardrailConfig; gc != nil {
		guardrail := map[string]interface{}{}
		if gc.GuardrailIdentifier != nil {
			guardrail["guardrailIdentifier"] = *gc.GuardrailIdentifier
		}
		if gc.GuardrailVersion != nil {
			guardrail["guardrailVersion"] = *gc.GuardrailVersion
		}
		if gc.Trace != "" {
			guardrail["trace"] = string(gc.Trace)
		}
		if gc.StreamProcessingMode != "" {
			guardrail["streamProcessingMode"] = string(gc.StreamProcessingMode)
		}
		body["guardrailConfig"] = guardrail
	}

	// §3 rule 5 — toolConfig
	if tc := input.ToolConfig; tc != nil && len(tc.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(tc.Tools))
		for _, t := range tc.Tools {
			if t.ToolSpec == nil {
				continue
			}
			fn := map[string]interface{}{"name": t.ToolSpec.Name}
			if t.ToolSpec.Description != nil {
				fn["description"] = *t.ToolSpec.Description
			}
			if t.ToolSpec.InputSchema != nil {
				fn["parameters"] = t.ToolSpec.InputSchema.Json
			}
			tools = append(tools, map[string]interface{}{
				"type":     "function",
				"function": fn,
			})
		}
		body["tools"] = tools

		if tc.ToolChoice != nil {
			body["tool_choice"] = buildToolChoice(tc.ToolChoice)
		}
	}

	// §3 rule 7 — stream flag
	if stream {
		body["stream"] = true
	}

	// §3 rule 6 — additionalModelRequestFields shallow-merged last
	for k, v := range input.AdditionalModelRequestFields {
		body[k] = v
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, &types.SdkError{Message: "failed to build request body", Err: err}
	}
	return b, nil
}

func buildMessages(input *ConverseInput) ([]interface{}, error) {
	var out []interface{}

	// §3 rule 2 — system blocks
	if len(input.System) > 0 {
		hasCachePoint := false
		for _, s := range input.System {
			if s.CachePoint != nil {
				hasCachePoint = true
				break
			}
		}
		if hasCachePoint {
			// Cache-point mode: content becomes a parts array (spec §3).
			var sysParts []map[string]interface{}
			for _, s := range input.System {
				if s.CachePoint != nil {
					applyCachePoint(sysParts)
				} else {
					sysParts = append(sysParts, map[string]interface{}{
						"type": "text",
						"text": s.Text,
					})
				}
			}
			if len(sysParts) > 0 {
				out = append(out, map[string]interface{}{
					"role":    "system",
					"content": sysParts,
				})
			}
		} else {
			parts := make([]string, 0, len(input.System))
			for _, s := range input.System {
				parts = append(parts, s.Text)
			}
			out = append(out, map[string]interface{}{
				"role":    "system",
				"content": strings.Join(parts, "\n\n"),
			})
		}
	}

	// §3 rule 3 — conversation messages
	for _, msg := range input.Messages {
		switch msg.Role {
		case types.ConversationRoleUser:
			// tool-result blocks first, then the user message
			for _, block := range msg.Content {
				tr, ok := block.(*types.ContentBlockMemberToolResult)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": tr.Value.ToolUseId,
					"content":      joinToolResultContent(tr.Value.Content),
				})
			}
			// Ordered text/image/video content parts so the multimodal path
			// preserves original block order (spec §3).
			var userParts []map[string]interface{}
			hasMedia := false
			hasCachePoint := false
			for _, block := range msg.Content {
				switch b := block.(type) {
				case *types.ContentBlockMemberText:
					userParts = append(userParts, map[string]interface{}{
						"type": "text",
						"text": b.Value,
					})
				case *types.ContentBlockMemberImage:
					hasMedia = true
					url, err := imageBlockURL(b.Value)
					if err != nil {
						return nil, err
					}
					userParts = append(userParts, map[string]interface{}{
						"type":      "image_url",
						"image_url": map[string]interface{}{"url": url},
					})
				case *types.ContentBlockMemberVideo:
					hasMedia = true
					part, err := videoFilePart(b.Value)
					if err != nil {
						return nil, err
					}
					userParts = append(userParts, part)
				case *types.ContentBlockMemberCachePoint:
					hasCachePoint = true
					applyCachePoint(userParts)
				}
			}
			if (hasMedia || hasCachePoint) && len(userParts) > 0 {
				out = append(out, map[string]interface{}{
					"role":    "user",
					"content": userParts,
				})
			} else if len(userParts) > 0 && !hasCachePoint {
				// Text-only turn keeps the plain-string form (spec §3).
				textParts := make([]string, 0, len(userParts))
				for _, p := range userParts {
					textParts = append(textParts, p["text"].(string))
				}
				out = append(out, map[string]interface{}{
					"role":    "user",
					"content": strings.Join(textParts, "\n"),
				})
			}

		case types.ConversationRoleAssistant:
			var textParts []string
			var assistantParts []map[string]interface{}
			var toolCalls []map[string]interface{}
			hasCachePoint := false
			for _, block := range msg.Content {
				switch b := block.(type) {
				case *types.ContentBlockMemberText:
					textParts = append(textParts, b.Value)
					assistantParts = append(assistantParts, map[string]interface{}{
						"type": "text",
						"text": b.Value,
					})
				case *types.ContentBlockMemberToolUse:
					args, _ := json.Marshal(b.Value.Input)
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   b.Value.ToolUseId,
						"type": "function",
						"function": map[string]interface{}{
							"name":      b.Value.Name,
							"arguments": string(args),
						},
					})
				case *types.ContentBlockMemberCachePoint:
					hasCachePoint = true
					applyCachePoint(assistantParts)
				}
			}
			// content is the joined text (a parts array in cache-point mode),
			// or null if no text blocks
			var content interface{}
			if hasCachePoint && len(assistantParts) > 0 {
				content = assistantParts
			} else if len(textParts) > 0 {
				content = strings.Join(textParts, "\n")
			}
			m := map[string]interface{}{
				"role":    "assistant",
				"content": content,
			}
			if len(toolCalls) > 0 {
				m["tool_calls"] = toolCalls
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// applyCachePoint attaches the ephemeral cache_control marker to the last
// emitted content part (spec §3 cache points). A cache point with no preceding
// part is silently ignored.
func applyCachePoint(parts []map[string]interface{}) {
	if len(parts) > 0 {
		parts[len(parts)-1]["cache_control"] = map[string]interface{}{"type": "ephemeral"}
	}
}

// imageBlockURL maps an image block to the URL of its OpenAI image_url
// content part (spec §3): source.url is passed through verbatim; source.bytes
// is base64-encoded into a data URL.  A missing/unknown source is a
// client-side validation error rather than an empty URL on the wire.
func imageBlockURL(img types.ImageBlock) (string, error) {
	switch src := img.Source.(type) {
	case *types.ImageSourceMemberUrl:
		return src.Value, nil
	case *types.ImageSourceMemberBytes:
		b64 := base64.StdEncoding.EncodeToString(src.Value)
		return "data:image/" + string(img.Format) + ";base64," + b64, nil
	default:
		return "", &types.ValidationException{APIError: types.APIError{
			Message: "image block has no source: set ImageSourceMemberBytes or ImageSourceMemberUrl",
		}}
	}
}

// videoFormatMIME is the §3 VideoFormat → MIME type table (data-URL media
// type and file.format).
var videoFormatMIME = map[types.VideoFormat]string{
	types.VideoFormatMp4:     "video/mp4",
	types.VideoFormatMov:     "video/mov",
	types.VideoFormatMkv:     "video/x-matroska",
	types.VideoFormatWebm:    "video/webm",
	types.VideoFormatFlv:     "video/x-flv",
	types.VideoFormatMpeg:    "video/mpeg",
	types.VideoFormatMpg:     "video/mpg",
	types.VideoFormatWmv:     "video/wmv",
	types.VideoFormatThreeGp: "video/3gpp",
}

// videoFilePart maps a video block to an OpenAI-protocol file content part
// (spec §3): source.bytes are base64-encoded into a data URL with the MIME
// type from the format table; source.uri is passed through verbatim in
// file.file_id (the gateway resolves it server-side). A missing/unknown
// source or format is a client-side validation error rather than a
// malformed part on the wire.
func videoFilePart(video types.VideoBlock) (map[string]interface{}, error) {
	mime, ok := videoFormatMIME[video.Format]
	if !ok {
		return nil, &types.ValidationException{APIError: types.APIError{
			Message: "video block has unknown format " + string(video.Format),
		}}
	}
	switch src := video.Source.(type) {
	case *types.VideoSourceMemberBytes:
		b64 := base64.StdEncoding.EncodeToString(src.Value)
		return map[string]interface{}{
			"type": "file",
			"file": map[string]interface{}{
				"file_data": "data:" + mime + ";base64," + b64,
				"format":    mime,
			},
		}, nil
	case *types.VideoSourceMemberURI:
		return map[string]interface{}{
			"type": "file",
			"file": map[string]interface{}{
				"file_id": src.Value,
				"format":  mime,
			},
		}, nil
	default:
		return nil, &types.ValidationException{APIError: types.APIError{
			Message: "video block has no source: set VideoSourceMemberBytes or VideoSourceMemberURI",
		}}
	}
}

func joinToolResultContent(content []types.ToolResultContentBlock) string {
	var sb strings.Builder
	for _, c := range content {
		switch v := c.(type) {
		case *types.ToolResultContentMemberText:
			sb.WriteString(v.Value)
		case *types.ToolResultContentMemberJson:
			b, _ := json.Marshal(v.Value)
			sb.Write(b)
		}
	}
	return sb.String()
}

func buildToolChoice(tc types.ToolChoice) interface{} {
	if tool, ok := tc.(*types.ToolChoiceMemberTool); ok {
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": tool.Value.Name,
			},
		}
	}
	switch tc.(type) {
	case *types.ToolChoiceMemberAny:
		return "required"
	default: // *types.ToolChoiceMemberAuto and anything unrecognised
		return "auto"
	}
}

// ─────────────────────────────────────────────────────────────
// OpenAI response → Bedrock output mapping  (spec §3 "OpenAI response → Converse output")
// ─────────────────────────────────────────────────────────────

func parseConverseResponse(body []byte, latencyMs int64) (*ConverseOutput, error) {
	var resp oaiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &types.SdkError{Message: "failed to decode response", Err: err}
	}

	// Defensive unwrap rule (§1): if no choices but data.choices exists, use data.
	if len(resp.Choices) == 0 && resp.Data != nil && len(resp.Data.Choices) > 0 {
		resp = *resp.Data
	}

	if len(resp.Choices) == 0 {
		return nil, &types.SdkError{Message: "response contained no choices"}
	}

	choice := resp.Choices[0]
	var contentBlocks []types.ContentBlock

	if choice.Message.Content != nil && *choice.Message.Content != "" {
		contentBlocks = append(contentBlocks, &types.ContentBlockMemberText{Value: *choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		var input interface{}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = tc.Function.Arguments // raw string fallback
		}
		contentBlocks = append(contentBlocks, &types.ContentBlockMemberToolUse{
			Value: types.ToolUseBlock{
				ToolUseId: tc.ID,
				Name:      tc.Function.Name,
				Input:     input,
			},
		})
	}

	msg := types.Message{
		Role:    types.ConversationRoleAssistant,
		Content: contentBlocks,
	}

	usage := mapTokenUsage(resp.Usage)

	return &ConverseOutput{
		Output:     &types.ConverseOutputMemberMessage{Value: msg},
		StopReason: mapFinishReason(choice.FinishReason),
		Usage:      usage,
		Metrics:    &types.ConverseMetrics{LatencyMs: latencyMs},
	}, nil
}

func mapFinishReason(reason *string) types.StopReason {
	if reason == nil {
		return types.StopReasonEndTurn
	}
	switch *reason {
	case "stop":
		return types.StopReasonEndTurn
	case "length":
		return types.StopReasonMaxTokens
	case "tool_calls", "function_call":
		return types.StopReasonToolUse
	case "content_filter":
		return types.StopReasonContentFiltered
	default:
		return types.StopReasonEndTurn
	}
}

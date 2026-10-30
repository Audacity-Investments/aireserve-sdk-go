package aireserveruntime

// compat.go — provider-native pass-through surfaces (spec §9).
//
// The gateway natively serves the OpenAI Chat Completions wire format and the
// Anthropic Messages wire format.  These surfaces expose both directly, with
// the SDK's auth, retry, and §4 error handling but no shape translation —
// request bodies are sent verbatim, responses returned untranslated:
//
//	client.Chat.Completions.Create(...)        → POST /v1/chat/completions
//	client.Chat.Completions.CreateStream(...)  → POST /v1/chat/completions (SSE)
//	client.Messages.Create(...)                → POST /v1/messages
//	client.Messages.CreateStream(...)          → POST /v1/messages (SSE)
//	client.Messages.CountTokens(...)           → POST /v1/messages/count_tokens
//
// Both wire formats work with every gateway model — the gateway bridges the
// format (e.g. GPT models answer /v1/messages calls).

import (
	"context"
	"encoding/json"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// anthropicVersion is the Anthropic API version header sent on the two
// Anthropic-format endpoints.  The gateway forwards Anthropic-format bodies
// as-is; the header keeps parity with the official anthropic SDKs.
const anthropicVersion = "2023-06-01"

// ─────────────────────────────────────────────────────────────
// Service namespaces
// ─────────────────────────────────────────────────────────────

// ChatService groups the OpenAI-format pass-through operations
// (Client.Chat.Completions).
type ChatService struct {
	Completions *ChatCompletionsService
}

// ChatCompletionsService is the OpenAI-format pass-through surface (spec §9).
type ChatCompletionsService struct {
	client *Client
}

// MessagesService is the Anthropic-format pass-through surface (spec §9) —
// the wire format used by the anthropic SDKs and Claude Code.
type MessagesService struct {
	client *Client
}

// ─────────────────────────────────────────────────────────────
// Request params
// ─────────────────────────────────────────────────────────────

// ChatCompletionCreateParams is an OpenAI-format chat-completion request.
// It is serialized and sent verbatim — the SDK adds, renames, and strips
// nothing.  Fields the struct does not model go in Extra, which is
// shallow-merged into the request body last, so any field the gateway
// supports works with no SDK release.
type ChatCompletionCreateParams struct {
	// Model is the model identifier (e.g. "gpt-5.4-mini").  Required.
	Model string `json:"model"`

	// Messages is the OpenAI-format conversation, passed through raw
	// (e.g. {"role": "user", "content": "Hi"}).  Required.
	Messages []map[string]interface{} `json:"messages"`

	// MaxTokens caps the completion length (wire key max_tokens).
	MaxTokens *int32 `json:"max_tokens,omitempty"`

	// Temperature is the sampling temperature.
	Temperature *float64 `json:"temperature,omitempty"`

	// Tools is the OpenAI-format tool list, passed through raw.
	Tools []map[string]interface{} `json:"tools,omitempty"`

	// Extra is shallow-merged into the request body last — the escape hatch
	// for every other OpenAI request field (response_format, stop, seed, …).
	Extra map[string]interface{} `json:"-"`
}

// MessageCreateParams is an Anthropic-format message request.  It is
// serialized and sent verbatim; unmodeled fields go in Extra, which is
// shallow-merged into the request body last.
type MessageCreateParams struct {
	// Model is the model identifier.  Required — and not limited to Claude:
	// the gateway bridges the wire format for every model.
	Model string `json:"model"`

	// MaxTokens caps the completion length (wire key max_tokens).
	// Required by the Anthropic Messages format.
	MaxTokens int32 `json:"max_tokens"`

	// Messages is the Anthropic-format conversation, passed through raw.
	// Required.
	Messages []map[string]interface{} `json:"messages"`

	// System is the system prompt: a string or a raw list of content
	// blocks, passed through as given.
	System interface{} `json:"system,omitempty"`

	// Temperature is the sampling temperature.
	Temperature *float64 `json:"temperature,omitempty"`

	// Tools is the Anthropic-format tool list, passed through raw.
	Tools []map[string]interface{} `json:"tools,omitempty"`

	// Extra is shallow-merged into the request body last — the escape hatch
	// for every other Anthropic request field (stop_sequences, thinking, …).
	Extra map[string]interface{} `json:"-"`
}

// CountTokensParams is an Anthropic-format token-counting request
// (POST /v1/messages/count_tokens), passed through verbatim.
type CountTokensParams struct {
	// Model is the model identifier.  Required.
	Model string `json:"model"`

	// Messages is the Anthropic-format conversation, passed through raw.
	// Required.
	Messages []map[string]interface{} `json:"messages"`

	// System is the system prompt: a string or a raw list of content blocks.
	System interface{} `json:"system,omitempty"`

	// Tools is the Anthropic-format tool list, passed through raw.
	Tools []map[string]interface{} `json:"tools,omitempty"`

	// Extra is shallow-merged into the request body last.
	Extra map[string]interface{} `json:"-"`
}

// ─────────────────────────────────────────────────────────────
// Responses (raw, untranslated)
// ─────────────────────────────────────────────────────────────

// ChatCompletion is the raw OpenAI-shaped response of /v1/chat/completions.
// The typed fields are conveniences over the same bytes; Raw always carries
// the full untranslated body.
type ChatCompletion struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   map[string]interface{} `json:"usage"`

	// Raw is the complete response body, untranslated.
	Raw map[string]interface{} `json:"-"`
}

// ChatCompletionChoice is one entry of ChatCompletion.Choices.
type ChatCompletionChoice struct {
	Index        int                    `json:"index"`
	Message      map[string]interface{} `json:"message"`
	FinishReason string                 `json:"finish_reason"`
}

// AnthropicMessage is the raw Anthropic-shaped response of /v1/messages.
// The typed fields are conveniences over the same bytes; Raw always carries
// the full untranslated body.
type AnthropicMessage struct {
	ID         string                   `json:"id"`
	Type       string                   `json:"type"`
	Role       string                   `json:"role"`
	Model      string                   `json:"model"`
	Content    []map[string]interface{} `json:"content"`
	StopReason string                   `json:"stop_reason"`
	Usage      map[string]interface{}   `json:"usage"`

	// Raw is the complete response body, untranslated.
	Raw map[string]interface{} `json:"-"`
}

// MessageTokenCount is the raw response of /v1/messages/count_tokens.
type MessageTokenCount struct {
	InputTokens int64 `json:"input_tokens"`

	// Raw is the complete response body, untranslated.
	Raw map[string]interface{} `json:"-"`
}

// ─────────────────────────────────────────────────────────────
// Shared plumbing
// ─────────────────────────────────────────────────────────────

// buildPassthroughBody serializes params, shallow-merges extra last, and —
// for streaming calls — sets "stream": true (the only field the SDK writes;
// everything else is passed through verbatim per spec §9).
func buildPassthroughBody(params interface{}, extra map[string]interface{}, stream bool) ([]byte, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, &types.SdkError{Message: "failed to build request body", Err: err}
	}
	if len(extra) == 0 && !stream {
		return raw, nil
	}

	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, &types.SdkError{Message: "failed to build request body", Err: err}
	}
	for k, v := range extra {
		body[k] = v
	}
	if stream {
		body["stream"] = true
	}
	merged, err := json.Marshal(body)
	if err != nil {
		return nil, &types.SdkError{Message: "failed to build request body", Err: err}
	}
	return merged, nil
}

// decodePassthroughResponse decodes a raw JSON body into both the typed
// convenience struct and the untranslated Raw map.
func decodePassthroughResponse(respBody []byte, out interface{}, endpoint string) (map[string]interface{}, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, &types.SdkError{Message: "failed to decode " + endpoint + " response", Err: err}
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return nil, &types.SdkError{Message: "failed to decode " + endpoint + " response", Err: err}
	}
	return raw, nil
}

// requirePassthroughFields runs the minimal client-side validation (spec §9):
// model and messages must be present before any network call.  messages may
// arrive via the typed field or the Extra escape hatch.
func requirePassthroughFields(model string, messagesLen int, extra map[string]interface{}, endpoint string) error {
	if model == "" {
		return &types.SdkError{Message: "model is required for " + endpoint}
	}
	if messagesLen == 0 && extra["messages"] == nil {
		return &types.SdkError{Message: "messages is required for " + endpoint}
	}
	return nil
}

func anthropicHeaders() map[string]string {
	return map[string]string{"anthropic-version": anthropicVersion}
}

// ─────────────────────────────────────────────────────────────
// OpenAI-format operations
// ─────────────────────────────────────────────────────────────

// Create sends an OpenAI-format chat completion (POST /v1/chat/completions).
// The request body is sent verbatim and the raw response returned
// untranslated.  Standard auth, retry, and §4 error taxonomy apply.
func (s *ChatCompletionsService) Create(ctx context.Context, params *ChatCompletionCreateParams) (*ChatCompletion, error) {
	c := s.client
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	if err := requirePassthroughFields(params.Model, len(params.Messages), params.Extra, "/v1/chat/completions"); err != nil {
		return nil, err
	}

	body, err := buildPassthroughBody(params, params.Extra, false)
	if err != nil {
		return nil, err
	}

	respBody, _, err := c.doJSONWithRetry(ctx, "/v1/chat/completions", body, nil)
	if err != nil {
		return nil, err
	}

	out := &ChatCompletion{}
	raw, err := decodePassthroughResponse(respBody, out, "/v1/chat/completions")
	if err != nil {
		return nil, err
	}
	out.Raw = raw
	return out, nil
}

// CreateStream sends a streaming OpenAI-format chat completion.  It sets
// "stream": true on the otherwise-verbatim body and returns a RawEventStream
// of raw chat-completion chunk objects, exactly as the gateway sent them.
//
// The stream terminates on the gateway's `data: [DONE]` sentinel (which is
// not yielded); EOF without [DONE] or an in-stream error payload surfaces
// via RawEventStream.Err after the events channel closes.  Retries stop at
// the first streamed byte.
func (s *ChatCompletionsService) CreateStream(ctx context.Context, params *ChatCompletionCreateParams) (*RawEventStream, error) {
	c := s.client
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	if err := requirePassthroughFields(params.Model, len(params.Messages), params.Extra, "/v1/chat/completions"); err != nil {
		return nil, err
	}

	body, err := buildPassthroughBody(params, params.Extra, true)
	if err != nil {
		return nil, err
	}

	resp, sctx, cancel, _, err := c.doStreamWithRetry(ctx, "/v1/chat/completions", body, nil)
	if err != nil {
		return nil, err
	}
	return newRawEventStream(resp, sctx, cancel, rawStreamOpenAI), nil
}

// ─────────────────────────────────────────────────────────────
// Anthropic-format operations
// ─────────────────────────────────────────────────────────────

// Create sends an Anthropic-format message (POST /v1/messages) — the wire
// format used by the anthropic SDKs and Claude Code.  The request body is
// sent verbatim (plus the anthropic-version header) and the raw response
// returned untranslated.  Works with every gateway model, not just Claude.
func (s *MessagesService) Create(ctx context.Context, params *MessageCreateParams) (*AnthropicMessage, error) {
	c := s.client
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	if err := requirePassthroughFields(params.Model, len(params.Messages), params.Extra, "/v1/messages"); err != nil {
		return nil, err
	}

	body, err := buildPassthroughBody(params, params.Extra, false)
	if err != nil {
		return nil, err
	}

	respBody, _, err := c.doJSONWithRetry(ctx, "/v1/messages", body, anthropicHeaders())
	if err != nil {
		return nil, err
	}

	out := &AnthropicMessage{}
	raw, err := decodePassthroughResponse(respBody, out, "/v1/messages")
	if err != nil {
		return nil, err
	}
	out.Raw = raw
	return out, nil
}

// CreateStream sends a streaming Anthropic-format message.  It sets
// "stream": true on the otherwise-verbatim body and returns a RawEventStream
// of raw Anthropic stream events (message_start … message_stop), exactly as
// the gateway sent them.
//
// Anthropic streams carry no [DONE] terminator: a healthy stream ends with a
// message_stop event followed by EOF.  EOF before message_stop or an `error`
// event surfaces via RawEventStream.Err after the events channel closes.
// Retries stop at the first streamed byte.
func (s *MessagesService) CreateStream(ctx context.Context, params *MessageCreateParams) (*RawEventStream, error) {
	c := s.client
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	if err := requirePassthroughFields(params.Model, len(params.Messages), params.Extra, "/v1/messages"); err != nil {
		return nil, err
	}

	body, err := buildPassthroughBody(params, params.Extra, true)
	if err != nil {
		return nil, err
	}

	resp, sctx, cancel, _, err := c.doStreamWithRetry(ctx, "/v1/messages", body, anthropicHeaders())
	if err != nil {
		return nil, err
	}
	return newRawEventStream(resp, sctx, cancel, rawStreamAnthropic), nil
}

// CountTokens counts the tokens of an Anthropic-format request
// (POST /v1/messages/count_tokens).  Token counting is free — no inference
// happens.  The request body is sent verbatim; the raw response is returned
// (MessageTokenCount.InputTokens plus the untranslated Raw map).
func (s *MessagesService) CountTokens(ctx context.Context, params *CountTokensParams) (*MessageTokenCount, error) {
	c := s.client
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	if err := requirePassthroughFields(params.Model, len(params.Messages), params.Extra, "/v1/messages/count_tokens"); err != nil {
		return nil, err
	}

	body, err := buildPassthroughBody(params, params.Extra, false)
	if err != nil {
		return nil, err
	}

	respBody, _, err := c.doJSONWithRetry(ctx, "/v1/messages/count_tokens", body, anthropicHeaders())
	if err != nil {
		return nil, err
	}

	out := &MessageTokenCount{}
	raw, err := decodePassthroughResponse(respBody, out, "/v1/messages/count_tokens")
	if err != nil {
		return nil, err
	}
	out.Raw = raw
	return out, nil
}

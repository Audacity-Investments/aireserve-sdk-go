package aireserveruntime

import (
	"context"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// ConverseInput is the input to the Converse operation.
type ConverseInput struct {
	// ModelId is the model identifier (e.g. "gpt-5.4-mini").  Required.
	ModelId *string

	// Messages is the ordered conversation history.  Required; min 1 element.
	Messages []types.Message

	// System is zero or more system-prompt content blocks.
	System []types.SystemContentBlock

	// InferenceConfig overrides inference parameters.
	InferenceConfig *types.InferenceConfiguration

	// MediaResolution controls how video/image input is tokenized (Gemini
	// models; types.MediaResolutionLow cuts video token cost ~4x).
	// Serialized as the top-level media_resolution field; omitted when empty.
	MediaResolution types.MediaResolution

	// ToolConfig provides tool definitions and the tool-choice policy.
	ToolConfig *types.ToolConfiguration

	// GuardrailConfig mirrors Bedrock's guardrail configuration. Forwarded
	// verbatim as the top-level guardrailConfig field — the gateway rejects
	// Bedrock guardrails with an honest 400 rather than the SDK silently
	// dropping the field.
	GuardrailConfig *types.GuardrailConfiguration

	// AdditionalModelRequestFields is shallow-merged into the request body last,
	// allowing passthrough of model-specific parameters.
	AdditionalModelRequestFields map[string]interface{}
}

// ConverseOutput is the output of the Converse operation.
type ConverseOutput struct {
	// Output contains the assistant's response (currently always *types.ConverseOutputMemberMessage).
	Output types.ConverseOutput

	// StopReason is one of: types.StopReasonEndTurn, types.StopReasonMaxTokens,
	// types.StopReasonToolUse, types.StopReasonStopSequence,
	// types.StopReasonContentFiltered.
	StopReason types.StopReason

	// Usage reports prompt/completion/total token counts.
	Usage *types.TokenUsage

	// Metrics reports the end-to-end client-side latency.
	Metrics *types.ConverseMetrics
}

// Converse sends a non-streaming request to the AI Reserve API and returns the
// assistant's complete response.
//
// Returns types.MissingAPIKeyError if no API key has been configured.
// Retries transient failures up to Options.MaxRetries additional times.
func (c *Client) Converse(ctx context.Context, input *ConverseInput) (*ConverseOutput, error) {
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}

	body, err := buildRequestBody(input, false)
	if err != nil {
		return nil, err
	}

	respBody, latencyMs, err := c.doJSONWithRetry(ctx, "/v1/chat/completions", body, nil)
	if err != nil {
		return nil, err
	}

	return parseConverseResponse(respBody, latencyMs)
}

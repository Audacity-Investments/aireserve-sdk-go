package aireserveruntime

// generate_image.go — the image-generation helper (spec §8): a JSON POST to
// /v1/images/generations with the same auth, error-mapping, and retry policy
// as Converse.  The endpoint is OpenAI-compatible; generated images come back
// as signed download URLs (default) or inline base64 (`b64_json`).

import (
	"context"
	"encoding/json"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// GenerateImageInput is the input to the GenerateImage operation.
type GenerateImageInput struct {
	// Model is the image model id (e.g. "gpt-image-1",
	// "gemini-3.1-flash-image").  Required.
	Model *string

	// Prompt is the text description of the desired image(s);
	// 1–32000 characters.  Required.
	Prompt *string

	// N is the number of images to generate (1–10, model-dependent).
	N *int32

	// Size is the image dimensions as a "WxH" string (e.g. "1024x1024");
	// supported values differ per model.
	Size *string

	// Quality is the provider-specific quality tier (e.g. "standard", "hd",
	// "low", "high").
	Quality *string

	// ResponseFormat selects output delivery: "url" (default, signed
	// download link expiring after ~24 h) or "b64_json" (inline base64).
	ResponseFormat *string

	// User is an end-user identifier forwarded to the provider.
	User *string
}

// GeneratedImage is one generated image: exactly one of Url or B64Json is
// populated (empty string when absent).
type GeneratedImage struct {
	// Url is the signed download URL (response_format "url"); expires ~24 h.
	Url string

	// B64Json is the base64-encoded image bytes (wire key b64_json).
	B64Json string

	// RevisedPrompt is the provider's rewritten prompt, when it rewrites one
	// (wire key revised_prompt).
	RevisedPrompt string
}

// ImageGenerationUsage reports token consumption for an image generation
// (zero for fields the provider does not report).
type ImageGenerationUsage struct {
	InputTokens  int32
	OutputTokens int32
	TotalTokens  int32
}

// GenerateImageOutput is the output of the GenerateImage operation.
type GenerateImageOutput struct {
	// Created is the Unix timestamp (seconds) of generation.
	Created int64

	// Data holds the generated image(s).
	Data []GeneratedImage

	// Usage reports token consumption, when the provider reports it.
	Usage *ImageGenerationUsage
}

// imageGenerationWire is the wire shape of the /v1/images/generations response.
type imageGenerationWire struct {
	Created int64 `json:"created"`
	Data    []struct {
		Url           string `json:"url"`
		B64Json       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	Usage *struct {
		InputTokens  int32 `json:"input_tokens"`
		OutputTokens int32 `json:"output_tokens"`
		TotalTokens  int32 `json:"total_tokens"`
	} `json:"usage"`
}

// GenerateImage generates image(s) from a text prompt via
// POST {baseUrl}/v1/images/generations (spec §8).  The request follows the
// same auth, error-mapping, and retry policy as Converse: 401 maps to
// *types.AccessDeniedException, 429 to *types.ThrottlingException (retried,
// honouring Retry-After), and a spend cap to
// *types.ServiceQuotaExceededException.
//
// Returns types.MissingAPIKeyError if no API key has been configured.
func (c *Client) GenerateImage(ctx context.Context, input *GenerateImageInput) (*GenerateImageOutput, error) {
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	if input.Model == nil || *input.Model == "" {
		return nil, &types.SdkError{Message: "GenerateImageInput.Model is required"}
	}
	if input.Prompt == nil || *input.Prompt == "" {
		return nil, &types.SdkError{Message: "GenerateImageInput.Prompt is required"}
	}

	// Omit absent optionals entirely (never send nulls).
	bodyMap := map[string]interface{}{
		"model":  *input.Model,
		"prompt": *input.Prompt,
	}
	if input.N != nil {
		bodyMap["n"] = *input.N
	}
	if input.Size != nil {
		bodyMap["size"] = *input.Size
	}
	if input.Quality != nil {
		bodyMap["quality"] = *input.Quality
	}
	if input.ResponseFormat != nil {
		bodyMap["response_format"] = *input.ResponseFormat
	}
	if input.User != nil {
		bodyMap["user"] = *input.User
	}

	reqBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, &types.SdkError{Message: "failed to build request body", Err: err}
	}

	var respBody []byte
	err = c.doWithRetry(ctx, func(ctx context.Context) error {
		var err error
		respBody, err = c.jsonPostAttempt(ctx, "/v1/images/generations", reqBody)
		return err
	})
	if err != nil {
		return nil, err
	}

	// NOTE: the §1 defensive data-envelope unwrap is deliberately NOT applied
	// here — `data` is a legitimate top-level key of the images response.
	var wire imageGenerationWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, &types.SdkError{Message: "failed to decode image generation response", Err: err}
	}
	if wire.Data == nil {
		return nil, &types.SdkError{Message: "malformed image generation response: missing data array"}
	}

	out := &GenerateImageOutput{
		Created: wire.Created,
		Data:    make([]GeneratedImage, len(wire.Data)),
	}
	for i, img := range wire.Data {
		out.Data[i] = GeneratedImage{
			Url:           img.Url,
			B64Json:       img.B64Json,
			RevisedPrompt: img.RevisedPrompt,
		}
	}
	if wire.Usage != nil {
		out.Usage = &ImageGenerationUsage{
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
			TotalTokens:  wire.Usage.TotalTokens,
		}
	}
	return out, nil
}

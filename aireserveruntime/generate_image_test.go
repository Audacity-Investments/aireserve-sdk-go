package aireserveruntime_test

// generate_image_test.go — image generation (spec §8): POST
// /v1/images/generations request serialization, url/b64_json response
// mapping, and §4 error mapping (401/402/429).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

func imageURLResponse() map[string]interface{} {
	return map[string]interface{}{
		"created": 1752000000,
		"data": []map[string]interface{}{
			{"url": "https://storage.example.com/img-1.png?sig=abc", "revised_prompt": "A fluffy cat"},
		},
		"usage": map[string]interface{}{"input_tokens": 12, "output_tokens": 1024, "total_tokens": 1036},
	}
}

func TestGenerateImageURLHappyPath(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("parse request body: %v", err)
		}
		jsonResponse(t, w, 200, imageURLResponse())
	})

	out, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model:  aireserve.String("gpt-image-1"),
		Prompt: aireserve.String("A fluffy cat"),
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotPath != "/v1/images/generations" {
		t.Errorf("path = %q, want /v1/images/generations", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if len(gotBody) != 2 || gotBody["model"] != "gpt-image-1" || gotBody["prompt"] != "A fluffy cat" {
		t.Errorf("request body = %v, want just model+prompt", gotBody)
	}

	if out.Created != 1752000000 {
		t.Errorf("Created = %d, want 1752000000", out.Created)
	}
	if len(out.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(out.Data))
	}
	if out.Data[0].Url != "https://storage.example.com/img-1.png?sig=abc" {
		t.Errorf("Url = %q", out.Data[0].Url)
	}
	if out.Data[0].B64Json != "" {
		t.Errorf("B64Json = %q, want empty", out.Data[0].B64Json)
	}
	if out.Data[0].RevisedPrompt != "A fluffy cat" {
		t.Errorf("RevisedPrompt = %q", out.Data[0].RevisedPrompt)
	}
	if out.Usage == nil || out.Usage.InputTokens != 12 || out.Usage.OutputTokens != 1024 || out.Usage.TotalTokens != 1036 {
		t.Errorf("Usage = %+v, want {12 1024 1036}", out.Usage)
	}
}

func TestGenerateImageB64JsonResponse(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]interface{}{
			"created": 1752000001,
			"data": []map[string]interface{}{
				{"b64_json": "aGVsbG8="},
				{"b64_json": "d29ybGQ="},
			},
		})
	})

	out, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model:          aireserve.String("gpt-image-1"),
		Prompt:         aireserve.String("Two words"),
		N:              aireserve.Int32(2),
		ResponseFormat: aireserve.String("b64_json"),
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(out.Data) != 2 || out.Data[0].B64Json != "aGVsbG8=" || out.Data[1].B64Json != "d29ybGQ=" {
		t.Errorf("Data = %+v", out.Data)
	}
	if out.Data[0].Url != "" {
		t.Errorf("Url = %q, want empty", out.Data[0].Url)
	}
	if out.Usage != nil {
		t.Errorf("Usage = %+v, want nil", out.Usage)
	}
}

func TestGenerateImageSerializesAllParams(t *testing.T) {
	var gotBody map[string]interface{}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("parse request body: %v", err)
		}
		jsonResponse(t, w, 200, imageURLResponse())
	})

	_, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model:          aireserve.String("dall-e-3"),
		Prompt:         aireserve.String("A watercolor fox"),
		N:              aireserve.Int32(3),
		Size:           aireserve.String("1792x1024"),
		Quality:        aireserve.String("hd"),
		ResponseFormat: aireserve.String("b64_json"),
		User:           aireserve.String("user-123"),
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	want := map[string]interface{}{
		"model":           "dall-e-3",
		"prompt":          "A watercolor fox",
		"n":               float64(3),
		"size":            "1792x1024",
		"quality":         "hd",
		"response_format": "b64_json",
		"user":            "user-123",
	}
	if len(gotBody) != len(want) {
		t.Errorf("body has %d keys, want %d: %v", len(gotBody), len(want), gotBody)
	}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, gotBody[k], v)
		}
	}
}

func TestGenerateImage401MapsToAccessDenied(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 401, map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Invalid API key.",
				"type":    "authentication_error",
				"param":   nil,
				"code":    "invalid_api_key",
			},
		})
	})

	_, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model: aireserve.String("gpt-image-1"), Prompt: aireserve.String("x"),
	})
	var accessDenied *types.AccessDeniedException
	if !errors.As(err, &accessDenied) {
		t.Fatalf("err = %v, want *types.AccessDeniedException", err)
	}
	if accessDenied.StatusCode != 401 || accessDenied.ErrorCode != "invalid_api_key" {
		t.Errorf("StatusCode=%d ErrorCode=%q", accessDenied.StatusCode, accessDenied.ErrorCode)
	}
}

func TestGenerateImage402MapsToServiceQuotaExceeded(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 402, map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Spend cap exceeded.",
				"type":    "usage_cap_error",
				"param":   nil,
				"code":    "usage_cap_exceeded",
			},
		})
	})

	_, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model: aireserve.String("gpt-image-1"), Prompt: aireserve.String("x"),
	})
	var quota *types.ServiceQuotaExceededException
	if !errors.As(err, &quota) {
		t.Fatalf("err = %v, want *types.ServiceQuotaExceededException", err)
	}
	if quota.StatusCode != 402 {
		t.Errorf("StatusCode = %d, want 402", quota.StatusCode)
	}
}

func TestGenerateImage429ThrottlingWithRetryAfter(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		jsonResponse(t, w, 429, map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Rate limit exceeded (rpm).",
				"type":    "rate_limit_error",
				"param":   nil,
				"code":    "rate_limit_exceeded",
			},
		})
	})

	_, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model: aireserve.String("gpt-image-1"), Prompt: aireserve.String("x"),
	})
	var throttled *types.ThrottlingException
	if !errors.As(err, &throttled) {
		t.Fatalf("err = %v, want *types.ThrottlingException", err)
	}
	if throttled.RetryAfterSeconds == nil || *throttled.RetryAfterSeconds != 7 {
		t.Errorf("RetryAfterSeconds = %v, want 7", throttled.RetryAfterSeconds)
	}
}

func TestGenerateImageRetries429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			jsonResponse(t, w, 429, map[string]interface{}{
				"error": map[string]interface{}{
					"message": "slow down", "type": "rate_limit_error",
					"param": nil, "code": "rate_limit_exceeded",
				},
			})
			return
		}
		jsonResponse(t, w, 200, imageURLResponse())
	}
	_, s := newTestClient(t, srv)
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: s.URL, MaxRetries: 2,
	})

	out, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model: aireserve.String("gpt-image-1"), Prompt: aireserve.String("retry me"),
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if out.Data[0].Url == "" {
		t.Errorf("expected a url in the retried response")
	}
}

func TestGenerateImageMissingRequiredFields(t *testing.T) {
	client := aireserveruntime.New(aireserveruntime.Options{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"})

	if _, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Prompt: aireserve.String("x"),
	}); err == nil {
		t.Error("expected an error for a missing Model")
	}
	if _, err := client.GenerateImage(context.Background(), &aireserveruntime.GenerateImageInput{
		Model: aireserve.String("gpt-image-1"),
	}); err == nil {
		t.Error("expected an error for a missing Prompt")
	}
}

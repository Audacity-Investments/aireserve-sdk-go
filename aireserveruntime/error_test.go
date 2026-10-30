package aireserveruntime_test

// error_test.go — conformance checklist items 5 and 6 (error shapes, retry policy).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// ─────────────────────────────────────────────────────────────
// Checklist item 5 — error shape A
// ─────────────────────────────────────────────────────────────

func TestErrorShapeA_AccessDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprintln(w, `{"error":{"message":"Invalid API key","type":"authentication_error","param":null,"code":"invalid_api_key"}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "bad-key", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var accessDenied *types.AccessDeniedException
	if !errors.As(err, &accessDenied) {
		t.Errorf("expected AccessDeniedException, got %T: %v", err, err)
	}
	if accessDenied.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", accessDenied.StatusCode)
	}
	if accessDenied.ErrorCode != "invalid_api_key" {
		t.Errorf("ErrorCode = %q, want invalid_api_key", accessDenied.ErrorCode)
	}
}

func TestErrorShapeA_Throttling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		fmt.Fprintln(w, `{"error":{"message":"Rate limit exceeded","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	var throttle *types.ThrottlingException
	if !errors.As(err, &throttle) {
		t.Errorf("expected ThrottlingException, got %T: %v", err, err)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 5 — error shape B with requestId
// ─────────────────────────────────────────────────────────────

func TestErrorShapeB_ModelNotAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprintln(w, `{"success":false,"error":{"code":"MODEL_NOT_ALLOWED","message":"Model not permitted","request_id":"req-123","details":{}}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	var accessDenied *types.AccessDeniedException
	if !errors.As(err, &accessDenied) {
		t.Errorf("expected AccessDeniedException, got %T: %v", err, err)
	}
	if accessDenied.RequestID == nil || *accessDenied.RequestID != "req-123" {
		t.Errorf("RequestID = %v, want req-123", accessDenied.RequestID)
	}
	if accessDenied.ErrorCode != "MODEL_NOT_ALLOWED" {
		t.Errorf("ErrorCode = %q, want MODEL_NOT_ALLOWED", accessDenied.ErrorCode)
	}
}

func TestErrorShapeB_BudgetExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429) // 429 with BUDGET_EXCEEDED → ServiceQuotaExceededException
		fmt.Fprintln(w, `{"success":false,"error":{"code":"BUDGET_EXCEEDED","message":"Budget exhausted","request_id":"req-999","details":{"binding_cap":"team"}}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	var quota *types.ServiceQuotaExceededException
	if !errors.As(err, &quota) {
		t.Fatalf("expected ServiceQuotaExceededException for BUDGET_EXCEEDED, got %T: %v", err, err)
	}
	// Spec §4: shape-B error.details must be carried on the exception.
	details, ok := quota.Details.(map[string]interface{})
	if !ok {
		t.Fatalf("Details = %#v, want map with binding_cap", quota.Details)
	}
	if details["binding_cap"] != "team" {
		t.Errorf("Details[binding_cap] = %v, want team", details["binding_cap"])
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 6 — 429 + Retry-After → retries then succeeds
// ─────────────────────────────────────────────────────────────

func TestRetryAfterThenSuccess(t *testing.T) {
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0") // 0s so the test is fast
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprintln(w, `{"error":{"message":"Rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
			return
		}
		// Second attempt succeeds
		jsonResponse(t, w, 200, map[string]interface{}{
			"choices": []map[string]interface{}{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]interface{}{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: 2,
	})
	resp, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("expected 2 attempts, got %d", n)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 6 — 401 must NOT retry
// ─────────────────────────────────────────────────────────────

func TestNoRetryOn401(t *testing.T) {
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprintln(w, `{"error":{"message":"Unauthorized","type":"authentication_error","code":"invalid_api_key"}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "bad-key", BaseURL: srv.URL, MaxRetries: 3,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("401 should not be retried; attempts = %d, want 1", n)
	}
}

// ─────────────────────────────────────────────────────────────
// Checklist item 6 — BUDGET_EXCEEDED (429) must NOT retry
// ─────────────────────────────────────────────────────────────

func TestNoBudgetExceededRetry(t *testing.T) {
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		fmt.Fprintln(w, `{"success":false,"error":{"code":"BUDGET_EXCEEDED","message":"Budget exhausted"}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: 3,
	})
	_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
		ModelId:  aireserve.String("gpt-5.4-mini"),
		Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var quota *types.ServiceQuotaExceededException
	if !errors.As(err, &quota) {
		t.Errorf("expected ServiceQuotaExceededException, got %T", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("BUDGET_EXCEEDED should not be retried; attempts = %d, want 1", n)
	}
}

// ─────────────────────────────────────────────────────────────
// errors.As chain — typed error hierarchy
// ─────────────────────────────────────────────────────────────

func TestErrorsAsChain(t *testing.T) {
	// ModelStreamErrorException should unwrap to ModelErrorException
	streamErr := &types.ModelStreamErrorException{
		ModelErrorException: types.ModelErrorException{
			APIError: types.APIError{Message: "stream died", StatusCode: 0, ErrorCode: "STREAM_ERROR"},
		},
	}

	var modelErr *types.ModelErrorException
	if !errors.As(streamErr, &modelErr) {
		t.Error("ModelStreamErrorException should unwrap to ModelErrorException via errors.As")
	}

	var apiErr *types.APIError
	if !errors.As(streamErr, &apiErr) {
		t.Error("ModelStreamErrorException should unwrap to APIError via errors.As")
	}
}

// ─────────────────────────────────────────────────────────────
// HTTP status fallback mapping
// ─────────────────────────────────────────────────────────────

func TestHTTPStatusFallbackMapping(t *testing.T) {
	cases := []struct {
		status  int
		wantErr interface{ error }
	}{
		{400, &types.ValidationException{}},
		{402, &types.ServiceQuotaExceededException{}},
		{404, &types.ResourceNotFoundException{}},
		{408, &types.ModelTimeoutException{}},
		{500, &types.InternalServerException{}},
		{502, &types.ServiceUnavailableException{}},
		{503, &types.ServiceUnavailableException{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("HTTP_%d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprintln(w, `{"message":"error"}`)
			}))
			defer srv.Close()

			client := aireserveruntime.New(aireserveruntime.Options{
				APIKey: "test-key", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries,
			})
			_, err := client.Converse(context.Background(), &aireserveruntime.ConverseInput{
				ModelId:  aireserve.String("gpt-5.4-mini"),
				Messages: []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}}},
			})
			if err == nil {
				t.Fatalf("HTTP %d: expected error", tc.status)
			}
			// Use errors.As with the expected concrete type
			switch tc.status {
			case 400:
				var e *types.ValidationException
				if !errors.As(err, &e) {
					t.Errorf("HTTP %d: got %T, want *ValidationException", tc.status, err)
				}
			case 402:
				var e *types.ServiceQuotaExceededException
				if !errors.As(err, &e) {
					t.Errorf("HTTP %d: got %T, want *ServiceQuotaExceededException", tc.status, err)
				}
			case 404:
				var e *types.ResourceNotFoundException
				if !errors.As(err, &e) {
					t.Errorf("HTTP %d: got %T, want *ResourceNotFoundException", tc.status, err)
				}
			case 408:
				var e *types.ModelTimeoutException
				if !errors.As(err, &e) {
					t.Errorf("HTTP %d: got %T, want *ModelTimeoutException", tc.status, err)
				}
			case 500:
				var e *types.InternalServerException
				if !errors.As(err, &e) {
					t.Errorf("HTTP %d: got %T, want *InternalServerException", tc.status, err)
				}
			case 502, 503:
				var e *types.ServiceUnavailableException
				if !errors.As(err, &e) {
					t.Errorf("HTTP %d: got %T, want *ServiceUnavailableException", tc.status, err)
				}
			}
		})
	}
}

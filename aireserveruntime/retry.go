package aireserveruntime

// retry.go — retry policy per spec §4.

import (
	"errors"
	"math"
	"math/rand"
	"time"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// isRetryableError returns true if the error should trigger a retry attempt.
// Matches spec §4: only network-level SdkErrors are retryable (decode and
// validation failures are not); server-derived errors carry a Retryable flag
// stamped at classification time (which already encodes "429 + BUDGET_EXCEEDED
// never retries").
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var sdkErr *types.SdkError
	if errors.As(err, &sdkErr) {
		return sdkErr.Retryable
	}
	var apiErr *types.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	return false
}

// retryAfterFromErr extracts the Retry-After seconds from a typed API error, if present.
func retryAfterFromErr(err error) *int {
	if err == nil {
		return nil
	}
	var apiErr *types.APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfterSeconds
	}
	return nil
}

// backoffDuration computes the jittered exponential backoff for a given attempt
// number (1-based for the first retry, per spec §4: first-retry cap 1.0s, then
// 2.0s, …), honouring any Retry-After header value.
//
//	base = min(20s, 0.5s × 2^attempt)
//	jitter = rand(0, base)          — full jitter
//	result = max(jitter, retryAfter)
func backoffDuration(attempt int, retryAfterSecs *int) time.Duration {
	base := time.Duration(float64(500*time.Millisecond) * math.Pow(2, float64(attempt)))
	if base > 20*time.Second {
		base = 20 * time.Second
	}
	// Full jitter: uniform in [0, base].
	jittered := time.Duration(rand.Int63n(int64(base) + 1))

	if retryAfterSecs != nil {
		ra := time.Duration(*retryAfterSecs) * time.Second
		if ra > jittered {
			return ra
		}
	}
	return jittered
}

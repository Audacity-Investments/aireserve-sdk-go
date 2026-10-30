package aireserveruntime

// export_test.go — test-only hooks for the resumable-upload internals,
// reachable from the external aireserveruntime_test package.

import "context"

// SetUploadChunkSizeForTest overrides the resumable-upload chunk size so
// tests can exercise the multi-chunk path with small fixtures.  The override
// must stay a multiple of 256 KiB.  Returns a restore func.
func SetUploadChunkSizeForTest(n int) (restore func()) {
	old := uploadChunkSize
	uploadChunkSize = n
	return func() { uploadChunkSize = old }
}

// DisableUploadBackoffForTest replaces the recovery backoff with a no-op so
// retry-exhaustion tests run instantly.  Returns a restore func.
func DisableUploadBackoffForTest() (restore func()) {
	old := uploadBackoff
	uploadBackoff = func(context.Context, int, error) error { return nil }
	return func() { uploadBackoff = old }
}

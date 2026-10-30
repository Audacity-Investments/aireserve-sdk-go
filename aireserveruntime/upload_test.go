package aireserveruntime_test

// upload_test.go — conformance checklist item 14 (upload half): the
// UploadFile helper's POST /v1/files ticket step plus the GCS
// resumable-session protocol (spec §6) — initiation, chunked PUTs with
// Content-Range, 308-confirmed progress, offset-query recovery, and the
// bounded retry budget.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// testChunkSize is the shrunken chunk size (512 KiB — must stay a multiple
// of 256 KiB) used to exercise the multi-chunk path with small fixtures.
const testChunkSize = 262144 * 2

// patternedBytes builds n bytes of non-repeating-ish content so reassembly
// errors (wrong offset, dropped chunk) can't cancel out.
func patternedBytes(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

// resumableUploadServer builds a mock server that handles the POST /v1/files
// ticket and the resumable-initiation POST, delegating session PUTs to put.
func resumableUploadServer(t *testing.T, put http.HandlerFunc) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/files":
			jsonResponse(t, w, 200, map[string]interface{}{
				"file_id":    "file-123",
				"upload_url": srv.URL + "/upload/init",
				"uri":        "audacity://files/file-123",
				"expires_at": "2026-07-07T12:00:00Z",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/upload/init":
			w.Header().Set("Location", srv.URL+"/upload/session")
			w.WriteHeader(201)
		case r.Method == http.MethodPut && r.URL.Path == "/upload/session":
			put(w, r)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newUploadClient(srvURL string) *aireserveruntime.Client {
	return aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srvURL, MaxRetries: aireserveruntime.NoRetries,
	})
}

// TestUploadFileResumableHappyPath drives a 3-chunk upload end to end,
// asserting the ticket request, the initiation headers (x-goog-resumable,
// Content-Type, no Authorization, empty body), every Content-Range, the
// 308→200 protocol, and byte-exact reassembly.
func TestUploadFileResumableHappyPath(t *testing.T) {
	restore := aireserveruntime.SetUploadChunkSizeForTest(testChunkSize)
	defer restore()

	total := 262144 * 5 // 1.25 MiB → chunks of 512 KiB, 512 KiB, 256 KiB
	data := patternedBytes(total)

	var (
		mu            sync.Mutex
		received      []byte
		contentRanges []string
		initHeaders   http.Header
		initBodyLen   int = -1
	)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/files":
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("POST /v1/files Authorization = %q, want Bearer test-key", got)
			}
			body, _ := io.ReadAll(r.Body)
			var req map[string]interface{}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("parse POST body: %v", err)
			}
			if req["content_type"] != "video/mp4" {
				t.Errorf("content_type = %v, want video/mp4", req["content_type"])
			}
			if got := req["size_bytes"].(float64); int(got) != total {
				t.Errorf("size_bytes = %v, want %d", got, total)
			}
			jsonResponse(t, w, 200, map[string]interface{}{
				"file_id":    "file-123",
				"upload_url": srv.URL + "/upload/init",
				"uri":        "audacity://files/file-123",
				"expires_at": "2026-07-07T12:00:00Z",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/upload/init":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			initHeaders = r.Header.Clone()
			initBodyLen = len(body)
			mu.Unlock()
			w.Header().Set("Location", srv.URL+"/upload/session")
			w.WriteHeader(201)

		case r.Method == http.MethodPut && r.URL.Path == "/upload/session":
			body, _ := io.ReadAll(r.Body)
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("session PUT Authorization = %q, want none", got)
			}
			mu.Lock()
			contentRanges = append(contentRanges, r.Header.Get("Content-Range"))
			received = append(received, body...)
			n := len(received)
			mu.Unlock()
			if n < total {
				// Non-final chunk: 308 Resume Incomplete + confirmed Range.
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
				w.WriteHeader(308)
			} else {
				w.WriteHeader(200)
			}

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	out, err := newUploadClient(srv.URL).UploadFile(context.Background(), &aireserveruntime.UploadFileInput{
		Data:        data,
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if initHeaders == nil {
		t.Fatal("initiation POST was never performed")
	}
	if got := initHeaders.Get("x-goog-resumable"); got != "start" {
		t.Errorf("initiation x-goog-resumable = %q, want start", got)
	}
	if got := initHeaders.Get("Content-Type"); got != "video/mp4" {
		t.Errorf("initiation Content-Type = %q, want video/mp4", got)
	}
	if got := initHeaders.Get("Authorization"); got != "" {
		t.Errorf("initiation Authorization = %q, want none", got)
	}
	if initBodyLen != 0 {
		t.Errorf("initiation body = %d bytes, want empty", initBodyLen)
	}

	wantRanges := []string{
		fmt.Sprintf("bytes 0-524287/%d", total),
		fmt.Sprintf("bytes 524288-1048575/%d", total),
		fmt.Sprintf("bytes 1048576-%d/%d", total-1, total),
	}
	if len(contentRanges) != len(wantRanges) {
		t.Fatalf("session PUTs = %d (%v), want %d", len(contentRanges), contentRanges, len(wantRanges))
	}
	for i, want := range wantRanges {
		if contentRanges[i] != want {
			t.Errorf("chunk %d Content-Range = %q, want %q", i, contentRanges[i], want)
		}
	}
	if string(received) != string(data) {
		t.Errorf("reassembled bytes differ from input (%d vs %d bytes)", len(received), len(data))
	}

	if out.FileId != "file-123" {
		t.Errorf("FileId = %q, want file-123", out.FileId)
	}
	if out.Uri != "audacity://files/file-123" {
		t.Errorf("Uri = %q, want audacity://files/file-123", out.Uri)
	}
	if out.ExpiresAt != "2026-07-07T12:00:00Z" {
		t.Errorf("ExpiresAt = %q, want 2026-07-07T12:00:00Z", out.ExpiresAt)
	}
}

// TestUploadFileResumesAfterTransientFailure kills a mid-upload chunk with a
// 500 after persisting part of it, serves the offset query with the partial
// Range, and asserts the upload resumes from the confirmed offset and
// completes byte-exact.
func TestUploadFileResumesAfterTransientFailure(t *testing.T) {
	restoreChunk := aireserveruntime.SetUploadChunkSizeForTest(testChunkSize)
	defer restoreChunk()
	restoreBackoff := aireserveruntime.DisableUploadBackoffForTest()
	defer restoreBackoff()

	total := 262144 * 4 // 1 MiB → two 512 KiB chunks
	partial := 262144   // bytes of chunk 2 the server keeps before failing
	data := patternedBytes(total)

	var (
		mu            sync.Mutex
		received      []byte
		contentRanges []string
		queries       int
		failedOnce    bool
	)
	srv := resumableUploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		cr := r.Header.Get("Content-Range")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()

		if strings.HasPrefix(cr, "bytes */") {
			queries++
			if len(body) != 0 {
				t.Errorf("offset query body = %d bytes, want empty", len(body))
			}
			if cr != fmt.Sprintf("bytes */%d", total) {
				t.Errorf("offset query Content-Range = %q, want bytes */%d", cr, total)
			}
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(received)-1))
			w.WriteHeader(308)
			return
		}

		contentRanges = append(contentRanges, cr)
		if strings.HasPrefix(cr, "bytes 524288-") && !failedOnce {
			// Persist only part of the chunk, then die.
			failedOnce = true
			received = append(received, body[:partial]...)
			w.WriteHeader(500)
			return
		}
		received = append(received, body...)
		if len(received) < total {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(received)-1))
			w.WriteHeader(308)
			return
		}
		w.WriteHeader(200)
	})

	_, err := newUploadClient(srv.URL).UploadFile(context.Background(), &aireserveruntime.UploadFileInput{
		Data:        data,
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if queries != 1 {
		t.Errorf("offset queries = %d, want 1", queries)
	}
	// Chunk 1, failed chunk 2, then resumption from the confirmed offset
	// (524288 + partial).
	wantResume := fmt.Sprintf("bytes %d-%d/%d", 524288+partial, total-1, total)
	wantRanges := []string{
		fmt.Sprintf("bytes 0-524287/%d", total),
		fmt.Sprintf("bytes 524288-1048575/%d", total),
		wantResume,
	}
	if len(contentRanges) != len(wantRanges) {
		t.Fatalf("chunk Content-Ranges = %v, want %v", contentRanges, wantRanges)
	}
	for i, want := range wantRanges {
		if contentRanges[i] != want {
			t.Errorf("chunk %d Content-Range = %q, want %q", i, contentRanges[i], want)
		}
	}
	if string(received) != string(data) {
		t.Errorf("reassembled bytes differ from input (%d vs %d bytes)", len(received), len(data))
	}
}

// TestUploadFileRecoveryExhaustion keeps every session PUT failing with 500
// and asserts the upload gives up after the bounded budget: the initial
// chunk plus 5 recovery attempts (offset queries), then the mapped error.
func TestUploadFileRecoveryExhaustion(t *testing.T) {
	restoreChunk := aireserveruntime.SetUploadChunkSizeForTest(testChunkSize)
	defer restoreChunk()
	restoreBackoff := aireserveruntime.DisableUploadBackoffForTest()
	defer restoreBackoff()

	var (
		mu          sync.Mutex
		sessionPuts int
	)
	srv := resumableUploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		mu.Lock()
		sessionPuts++
		mu.Unlock()
		w.WriteHeader(500)
	})

	_, err := newUploadClient(srv.URL).UploadFile(context.Background(), &aireserveruntime.UploadFileInput{
		Data:        patternedBytes(1000),
		ContentType: "video/mp4",
	})
	var internal *types.InternalServerException
	if !errors.As(err, &internal) {
		t.Fatalf("expected InternalServerException, got %T: %v", err, err)
	}
	if internal.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", internal.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	// 1 failed chunk PUT + 5 failed recovery (offset query) attempts.
	if sessionPuts != 6 {
		t.Errorf("session PUTs = %d, want 6 (1 chunk + 5 recovery attempts)", sessionPuts)
	}
}

// TestUploadFileSmallFileSingleShot uploads a file smaller than one chunk:
// a single PUT covering the whole range, answered directly with 200.
func TestUploadFileSmallFileSingleShot(t *testing.T) {
	data := patternedBytes(1000) // far below the default 8 MiB chunk

	var (
		mu            sync.Mutex
		received      []byte
		contentRanges []string
	)
	srv := resumableUploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		contentRanges = append(contentRanges, r.Header.Get("Content-Range"))
		received = append(received, body...)
		mu.Unlock()
		w.WriteHeader(200)
	})

	_, err := newUploadClient(srv.URL).UploadFile(context.Background(), &aireserveruntime.UploadFileInput{
		Data:        data,
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("UploadFile error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(contentRanges) != 1 {
		t.Fatalf("session PUTs = %d (%v), want exactly 1", len(contentRanges), contentRanges)
	}
	if want := "bytes 0-999/1000"; contentRanges[0] != want {
		t.Errorf("Content-Range = %q, want %q", contentRanges[0], want)
	}
	if string(received) != string(data) {
		t.Errorf("uploaded bytes differ from input")
	}
}

// TestUploadFileChunk403FailsImmediately asserts a 403 on a chunk maps to
// AccessDeniedException at once — no offset query, no retry.
func TestUploadFileChunk403FailsImmediately(t *testing.T) {
	var (
		mu          sync.Mutex
		sessionPuts int
	)
	srv := resumableUploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		mu.Lock()
		sessionPuts++
		mu.Unlock()
		w.WriteHeader(403)
		fmt.Fprintln(w, "signature expired")
	})

	// Retries enabled — a chunk 4xx must still fail on the first attempt.
	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: 3,
	})
	_, err := client.UploadFile(context.Background(), &aireserveruntime.UploadFileInput{
		Data:        patternedBytes(100),
		ContentType: "video/mp4",
	})
	var accessDenied *types.AccessDeniedException
	if !errors.As(err, &accessDenied) {
		t.Fatalf("expected AccessDeniedException, got %T: %v", err, err)
	}
	if accessDenied.StatusCode != 403 {
		t.Errorf("StatusCode = %d, want 403", accessDenied.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if sessionPuts != 1 {
		t.Errorf("session PUTs = %d, want 1 (4xx is permanent)", sessionPuts)
	}
}

func TestUploadFileStep1ValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprintln(w, `{"success":false,"error":{"code":"VALIDATION_ERROR","message":"size_bytes exceeds 1 GB","request_id":"req-42"}}`)
	}))
	defer srv.Close()

	client := aireserveruntime.New(aireserveruntime.Options{
		APIKey: "test-key", BaseURL: srv.URL, MaxRetries: aireserveruntime.NoRetries,
	})
	_, err := client.UploadFile(context.Background(), &aireserveruntime.UploadFileInput{
		Data:        []byte("x"),
		ContentType: "video/mp4",
	})
	var validation *types.ValidationException
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationException, got %T: %v", err, err)
	}
	if validation.RequestID == nil || *validation.RequestID != "req-42" {
		t.Errorf("RequestID = %v, want req-42", validation.RequestID)
	}
}

package aireserveruntime

// upload.go — the file-upload helper (spec §6): a POST to /v1/files for a
// presigned upload ticket, then the GCS resumable-session protocol against
// the returned URL — an initiation POST that yields a session URI, chunked
// PUTs with Content-Range headers whose progress is confirmed by 308
// responses, and offset-query recovery after transient failures.  The
// resulting Uri is referenced from a video block via
// types.VideoSourceMemberURI.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

const (
	// uploadChunkAlign is the GCS resumable-upload granularity: every
	// non-final chunk must be a multiple of 256 KiB (spec §6).
	uploadChunkAlign = 256 * 1024

	// defaultUploadChunkSize is the default chunk size (8 MiB).
	defaultUploadChunkSize = 32 * uploadChunkAlign

	// maxUploadRecoveryAttempts bounds the §6 recovery budget: at most 5
	// recovery attempts, reset whenever a chunk is confirmed (progress).
	maxUploadRecoveryAttempts = 5
)

// uploadChunkSize is a var (not const) so tests can shrink it to exercise
// the multi-chunk path without megabytes of fixture data.  Any override must
// stay a multiple of uploadChunkAlign.
var uploadChunkSize = defaultUploadChunkSize

// uploadBackoff waits out the jittered §4 backoff between recovery attempts;
// a var so tests can replace it with a no-op.
var uploadBackoff = sleepBackoff

// UploadFileInput is the input to the UploadFile operation.
type UploadFileInput struct {
	// Data is the raw file bytes.  Required; size_bytes is computed from it.
	Data []byte

	// ContentType is the file's MIME type (e.g. "video/mp4").  Required;
	// must be one of the §3 video MIME types.
	ContentType string
}

// UploadFileOutput is the ticket returned by POST /v1/files (spec §6).
type UploadFileOutput struct {
	// FileId is the server-assigned file identifier.
	FileId string

	// UploadUrl is the presigned resumable-initiation URL the bytes were
	// uploaded through (~15 min expiry).
	UploadUrl string

	// Uri is the audacity://files/… reference to use in
	// types.VideoSourceMemberURI.
	Uri string

	// ExpiresAt is when the upload URL expires (RFC 3339).
	ExpiresAt string
}

// fileTicket is the wire shape of the POST /v1/files response.
type fileTicket struct {
	FileId    string `json:"file_id"`
	UploadUrl string `json:"upload_url"`
	Uri       string `json:"uri"`
	ExpiresAt string `json:"expires_at"`
}

// UploadFile uploads a file for use as a transient inference input (spec §6):
// it requests a presigned upload ticket from POST {baseUrl}/v1/files, then
// runs the GCS resumable-session protocol against the returned URL —
// chunked, with automatic resumption from the last confirmed byte after
// transient failures — and returns the ticket.  Reference the returned Uri
// from a video block via types.VideoSourceMemberURI.
//
// Uploaded files auto-delete after ~24 h and are capped at 1 GB.  The ticket
// request follows the same auth, error-mapping, and retry policy as Converse;
// the resumable steps carry no Authorization header and map failures by HTTP
// status alone, with a bounded budget of 5 recovery attempts that resets
// whenever a chunk is confirmed.
func (c *Client) UploadFile(ctx context.Context, input *UploadFileInput) (*UploadFileOutput, error) {
	if c.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}

	reqBody, err := json.Marshal(map[string]interface{}{
		"content_type": input.ContentType,
		"size_bytes":   len(input.Data),
	})
	if err != nil {
		return nil, &types.SdkError{Message: "failed to build request body", Err: err}
	}

	// Step 1 — POST /v1/files with the Converse retry policy.
	var respBody []byte
	err = c.doWithRetry(ctx, func(ctx context.Context) error {
		var err error
		respBody, err = c.jsonPostAttempt(ctx, "/v1/files", reqBody)
		return err
	})
	if err != nil {
		return nil, err
	}

	var ticket fileTicket
	if err := json.Unmarshal(respBody, &ticket); err != nil {
		return nil, &types.SdkError{Message: "failed to decode file ticket", Err: err}
	}
	if ticket.UploadUrl == "" {
		return nil, &types.SdkError{Message: "file ticket contained no upload_url"}
	}

	// Step 2 — resumable upload (spec §6): initiate a session, then stream
	// the bytes in chunks with bounded recovery.
	sessionURI, err := c.initiateResumableUpload(ctx, ticket.UploadUrl, input.ContentType)
	if err != nil {
		return nil, err
	}
	if err := c.uploadChunks(ctx, sessionURI, input.Data); err != nil {
		return nil, err
	}

	return &UploadFileOutput{
		FileId:    ticket.FileId,
		UploadUrl: ticket.UploadUrl,
		Uri:       ticket.Uri,
		ExpiresAt: ticket.ExpiresAt,
	}, nil
}

// jsonPostAttempt performs one JSON POST attempt against a gateway path
// (e.g. /v1/files, /v1/images/generations), bounded end-to-end by the
// configured timeout, with the same headers and error parsing as Converse.
func (c *Client) jsonPostAttempt(ctx context.Context, path string, body []byte) ([]byte, error) {
	actx := ctx
	var cancel context.CancelFunc = func() {}
	if c.options.Timeout > 0 {
		actx, cancel = context.WithTimeout(ctx, c.options.Timeout)
	}
	defer cancel()

	url := c.options.BaseURL + path
	req, err := http.NewRequestWithContext(actx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &types.SdkError{Message: "failed to create HTTP request", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.options.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &types.SdkError{Message: "HTTP request failed", Err: err, Retryable: true}
	}

	respBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, parseErrorBody(respBody, resp.StatusCode, resp.Header)
	}
	if readErr != nil {
		return nil, &types.SdkError{Message: "failed to read response body", Err: readErr, Retryable: true}
	}
	return respBody, nil
}

// initiateResumableUpload performs the §6a initiation POST against the
// presigned URL (x-goog-resumable: start, empty body, no Authorization — the
// URL is self-authorizing) and returns the session URI from the Location
// header.  Transient failures are retried with the client's retry policy;
// non-2xx maps by HTTP status alone.
func (c *Client) initiateResumableUpload(ctx context.Context, uploadURL, contentType string) (string, error) {
	var sessionURI string
	err := c.doWithRetry(ctx, func(ctx context.Context) error {
		var err error
		sessionURI, err = c.initiateAttempt(ctx, uploadURL, contentType)
		return err
	})
	if err != nil {
		return "", err
	}
	return sessionURI, nil
}

// initiateAttempt performs one resumable-initiation POST.
func (c *Client) initiateAttempt(ctx context.Context, uploadURL, contentType string) (string, error) {
	actx := ctx
	var cancel context.CancelFunc = func() {}
	if c.options.Timeout > 0 {
		actx, cancel = context.WithTimeout(ctx, c.options.Timeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(actx, http.MethodPost, uploadURL, http.NoBody)
	if err != nil {
		return "", &types.SdkError{Message: "failed to create upload initiation request", Err: err}
	}
	req.Header.Set("x-goog-resumable", "start")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", &types.SdkError{Message: "upload initiation failed", Err: err, Retryable: true}
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", storageStatusError("upload initiation rejected", resp.StatusCode, respBody)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", &types.SdkError{Message: "upload initiation response carried no Location header"}
	}
	return loc, nil
}

// uploadChunks streams data to the resumable session in uploadChunkSize
// pieces (spec §6b–d).  308 responses confirm progress via their Range
// header; a transient failure (network error, 5xx, 429) triggers an offset
// query and resumption from the confirmed byte; other 4xx fail immediately.
// The recovery budget (5 attempts, reset on progress) bounds the loop.
func (c *Client) uploadChunks(ctx context.Context, sessionURI string, data []byte) error {
	total := len(data)
	offset := 0
	recoveries := 0
	needQuery := false

	// spendRecovery enforces the bounded §6d budget around one transient
	// failure: a nil return means "retry after backoff"; non-nil aborts the
	// upload with the error to surface.
	spendRecovery := func(err error) error {
		if !isRetryableError(err) {
			return err
		}
		if ctx.Err() != nil {
			return &types.SdkError{Message: "upload aborted by caller context", Err: err}
		}
		recoveries++
		if recoveries > maxUploadRecoveryAttempts {
			return err
		}
		return uploadBackoff(ctx, recoveries, err)
	}

	for {
		if needQuery {
			next, done, err := c.queryUploadOffset(ctx, sessionURI, total)
			if err != nil {
				if abort := spendRecovery(err); abort != nil {
					return abort
				}
				continue
			}
			if done {
				return nil
			}
			if next > offset {
				// The failed chunk was partially persisted server-side.
				recoveries = 0
			}
			offset = next
			needQuery = false
			continue
		}

		next, done, err := c.putUploadChunk(ctx, sessionURI, data, offset, total)
		if err != nil {
			if abort := spendRecovery(err); abort != nil {
				return abort
			}
			needQuery = true
			continue
		}
		if done {
			return nil
		}
		if next > offset {
			offset = next
			recoveries = 0
			continue
		}
		// 308 confirming no forward progress (missing or stale Range):
		// resend from the confirmed offset, spending recovery budget so a
		// stuck session cannot loop forever.
		if abort := spendRecovery(&types.SdkError{
			Message:   "upload session confirmed no progress",
			Retryable: true,
		}); abort != nil {
			return abort
		}
		offset = next
	}
}

// putUploadChunk PUTs one chunk to the session with its §6b Content-Range.
func (c *Client) putUploadChunk(ctx context.Context, sessionURI string, data []byte, offset, total int) (next int, done bool, err error) {
	end := offset + uploadChunkSize
	if end > total {
		end = total
	}
	contentRange := fmt.Sprintf("bytes %d-%d/%d", offset, end-1, total)
	if total == 0 {
		contentRange = "bytes */0"
	}
	return c.resumableSessionRequest(ctx, sessionURI, bytes.NewReader(data[offset:end]), contentRange, "file chunk upload")
}

// queryUploadOffset asks the session for its confirmed offset (spec §6c)
// with an empty-body PUT and Content-Range: bytes */<total>.
func (c *Client) queryUploadOffset(ctx context.Context, sessionURI string, total int) (next int, done bool, err error) {
	return c.resumableSessionRequest(ctx, sessionURI, http.NoBody, fmt.Sprintf("bytes */%d", total), "upload offset query")
}

// resumableSessionRequest performs one PUT against the resumable session and
// interprets the §6 response protocol: 2xx → done; 308 → next offset from
// the Range header (0 when absent); anything else → typed error by HTTP
// status alone (storage bodies are XML, not gateway payloads).  The session
// URI is self-authorizing, so no Authorization header is sent.
func (c *Client) resumableSessionRequest(ctx context.Context, sessionURI string, body io.Reader, contentRange, what string) (next int, done bool, err error) {
	actx := ctx
	var cancel context.CancelFunc = func() {}
	if c.options.Timeout > 0 {
		actx, cancel = context.WithTimeout(ctx, c.options.Timeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(actx, http.MethodPut, sessionURI, body)
	if err != nil {
		return 0, false, &types.SdkError{Message: "failed to create " + what + " request", Err: err}
	}
	req.Header.Set("Content-Range", contentRange)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, false, &types.SdkError{Message: what + " failed", Err: err, Retryable: true}
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return 0, true, nil
	case resp.StatusCode == 308:
		if n, ok := parseUploadRange(resp.Header); ok {
			return n, false, nil
		}
		return 0, false, nil // 308 without Range: nothing persisted yet
	default:
		return 0, false, storageStatusError(what+" rejected", resp.StatusCode, respBody)
	}
}

// parseUploadRange extracts N from a "Range: bytes=0-N" header and returns
// the next unconfirmed offset N+1.
func parseUploadRange(h http.Header) (int, bool) {
	const prefix = "bytes=0-"
	v := h.Get("Range")
	if !strings.HasPrefix(v, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v[len(prefix):]))
	if err != nil || n < 0 {
		return 0, false
	}
	return n + 1, true
}

// storageStatusError maps a non-2xx storage-provider response to the typed
// exception by HTTP status alone (spec §6), stamping retryability for
// transient statuses via the shared §4 fallback table.
func storageStatusError(fallbackMsg string, statusCode int, body []byte) error {
	base := types.APIError{
		Message:    fallbackMsg,
		StatusCode: statusCode,
		RawBody:    string(body),
	}
	if len(body) > 0 {
		base.Message = string(body)
	}
	return mapStatusToException(statusCode, base)
}

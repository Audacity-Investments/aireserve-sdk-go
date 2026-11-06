package aireserveruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

// GpuService is `client.Gpu`: the provider-hidden GPU infrastructure API
// (spec §10). A request names an accelerator, a count, a region and a price
// ceiling; AI Reserve places it on whichever backing provider has stock and
// nothing here accepts or returns a provider identity.
type GpuService struct {
	Offers     *GpuOffersService
	Instances  *GpuInstancesService
	Clusters   *GpuClustersService
	Volumes    *GpuVolumesService
	SshKeys    *GpuSshKeysService
	Registries *GpuRegistriesService
	Templates  *GpuTemplatesService
	Operations *GpuOperationsService
	Usage      *GpuUsageService
}

func newGpuService(c *Client) *GpuService {
	t := &gpuTransport{client: c}
	return &GpuService{
		Offers:     &GpuOffersService{t},
		Instances:  &GpuInstancesService{t},
		Clusters:   &GpuClustersService{t},
		Volumes:    &GpuVolumesService{t},
		SshKeys:    &GpuSshKeysService{t},
		Registries: &GpuRegistriesService{t},
		Templates:  &GpuTemplatesService{t},
		Operations: &GpuOperationsService{t},
		Usage:      &GpuUsageService{t},
	}
}

// ---------------------------------------------------------------------------
// Transport: any method, optional body, optional Idempotency-Key. Reads
// (GET) ride the client's retry policy; mutations are sent exactly once (a
// replay with the same key is the caller's decision, spec §10).
// ---------------------------------------------------------------------------

type gpuTransport struct{ client *Client }

type acceptedOperation struct {
	OperationID string `json:"operationId"`
	ResourceID  string `json:"resourceId"`
}

func newIdempotencyKey(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func (t *gpuTransport) do(ctx context.Context, method, path string, body interface{}, idempotencyKey string, out interface{}) error {
	if t.client.options.APIKey == "" {
		return &types.MissingAPIKeyError{}
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return &types.SdkError{Message: "failed to encode request body", Err: err}
		}
	}
	attempt := func(ctx context.Context) error {
		actx := ctx
		cancel := context.CancelFunc(func() {})
		if t.client.options.Timeout > 0 {
			actx, cancel = context.WithTimeout(ctx, t.client.options.Timeout)
		}
		defer cancel()
		req, err := http.NewRequestWithContext(actx, method, t.client.options.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return &types.SdkError{Message: "failed to create HTTP request", Err: err}
		}
		req.Header.Set("Authorization", "Bearer "+t.client.options.APIKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		resp, err := t.client.httpClient.Do(req)
		if err != nil {
			return &types.SdkError{Message: "HTTP request failed", Err: err, Retryable: true}
		}
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return parseErrorBody(respBody, resp.StatusCode, resp.Header)
		}
		if readErr != nil {
			return &types.SdkError{Message: "failed to read response body", Err: readErr, Retryable: true}
		}
		if out != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return &types.SdkError{Message: "failed to decode response body", Err: err}
			}
		}
		return nil
	}
	if method == http.MethodGet {
		return t.client.doWithRetry(ctx, attempt)
	}
	return attempt(ctx)
}

func (t *gpuTransport) get(ctx context.Context, path string, out interface{}) error {
	return t.do(ctx, http.MethodGet, path, nil, "", out)
}

func (t *gpuTransport) mutate(ctx context.Context, method, path string, body interface{}, key, prefix string) (*acceptedOperation, error) {
	if key == "" {
		key = newIdempotencyKey(prefix)
	}
	var out acceptedOperation
	if err := t.do(ctx, method, path, body, key, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Operations and Wait
// ---------------------------------------------------------------------------

type gpuResource string

const (
	resourceInstances  gpuResource = "instances"
	resourceClusters   gpuResource = "clusters"
	resourceVolumes    gpuResource = "volumes"
	resourceTemplates  gpuResource = "templates"
	resourceRegistries gpuResource = "registries"
)

var defaultWaitMs = map[gpuResource]int64{resourceInstances: 600_000, resourceClusters: 1_200_000, resourceVolumes: 300_000, resourceTemplates: 120_000, resourceRegistries: 120_000}
var defaultPollMs = map[gpuResource]int64{resourceInstances: 1_000, resourceClusters: 2_000, resourceVolumes: 1_000, resourceTemplates: 1_000, resourceRegistries: 1_000}

// GpuOperation is the handle for an accepted asynchronous mutation (202).
type GpuOperation struct {
	ID         string
	ResourceID string
	t          *gpuTransport
	resource   gpuResource
	deleted    bool
}

func (t *gpuTransport) operation(a *acceptedOperation, resource gpuResource, deleted bool) *GpuOperation {
	return &GpuOperation{ID: a.OperationID, ResourceID: a.ResourceID, t: t, resource: resource, deleted: deleted}
}

// poll polls GET /v1/gpu/operations/{id} until terminal (backoff ×1.5 capped at 5 s).
func (t *gpuTransport) poll(ctx context.Context, operationID string, timeoutMs, pollMs int64) (*GpuOperationRecord, error) {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	delay := time.Duration(pollMs) * time.Millisecond
	if delay < 10*time.Millisecond {
		delay = 10 * time.Millisecond
	}
	for time.Now().Before(deadline) {
		var record GpuOperationRecord
		if err := t.get(ctx, "/v1/gpu/operations/"+operationID, &record); err != nil {
			return nil, err
		}
		switch record.Status {
		case "succeeded":
			return &record, nil
		case "failed", "cancelled":
			msg := "GPU operation " + record.Status
			var failure *types.OperationFailure
			if record.Failure != nil {
				failure = &types.OperationFailure{Code: record.Failure.Code, Actor: record.Failure.Actor, Message: record.Failure.Message, Next: record.Failure.Next}
				msg = record.Failure.Message
			} else if record.ErrorMessage != nil {
				msg = *record.ErrorMessage
			}
			return nil, &types.OperationFailedError{OperationID: operationID, Message: msg, Failure: failure}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = time.Duration(float64(delay) * 1.5)
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
	return nil, &types.OperationTimeoutError{OperationID: operationID, Timeout: (time.Duration(timeoutMs) * time.Millisecond).String()}
}

func (op *GpuOperation) waitRaw(ctx context.Context, opts *WaitOptions, out interface{}) error {
	timeout, poll := defaultWaitMs[op.resource], defaultPollMs[op.resource]
	if opts != nil {
		if opts.Timeout > 0 {
			timeout = opts.Timeout
		}
		if opts.PollInterval > 0 {
			poll = opts.PollInterval
		}
	}
	if _, err := op.t.poll(ctx, op.ID, timeout, poll); err != nil {
		return err
	}
	if op.deleted || out == nil {
		return nil
	}
	if op.resource == resourceRegistries {
		var list struct {
			Data []GpuRegistryCredential `json:"data"`
		}
		if err := op.t.get(ctx, "/v1/gpu/registries", &list); err != nil {
			return err
		}
		for _, row := range list.Data {
			if row.ID == op.ResourceID {
				*(out.(*GpuRegistryCredential)) = row
				return nil
			}
		}
		return nil
	}
	return op.t.get(ctx, fmt.Sprintf("/v1/gpu/%s/%s", op.resource, op.ResourceID), out)
}

// Wait polls until the operation is terminal and returns the instance.
// Failed/cancelled → *types.OperationFailedError; elapsed → *types.OperationTimeoutError.
func (op *GpuOperation) Wait(ctx context.Context, opts *WaitOptions) (*GpuInstance, error) {
	var out GpuInstance
	if err := op.waitRaw(ctx, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WaitCluster is Wait for a cluster operation.
func (op *GpuOperation) WaitCluster(ctx context.Context, opts *WaitOptions) (*GpuCluster, error) {
	var out GpuCluster
	if err := op.waitRaw(ctx, opts, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WaitVolume is Wait for a volume operation; nil after a delete.
func (op *GpuOperation) WaitVolume(ctx context.Context, opts *WaitOptions) (*GpuVolume, error) {
	var out GpuVolume
	if err := op.waitRaw(ctx, opts, &out); err != nil {
		return nil, err
	}
	if op.deleted {
		return nil, nil
	}
	return &out, nil
}

// WaitTemplate is Wait for a template operation; nil after a delete.
func (op *GpuOperation) WaitTemplate(ctx context.Context, opts *WaitOptions) (*GpuTemplate, error) {
	var out GpuTemplate
	if err := op.waitRaw(ctx, opts, &out); err != nil {
		return nil, err
	}
	if op.deleted {
		return nil, nil
	}
	return &out, nil
}

// WaitRegistry is Wait for a registry-credential operation; nil after a delete.
func (op *GpuOperation) WaitRegistry(ctx context.Context, opts *WaitOptions) (*GpuRegistryCredential, error) {
	var out GpuRegistryCredential
	if err := op.waitRaw(ctx, opts, &out); err != nil {
		return nil, err
	}
	if op.deleted || out.ID == "" {
		return nil, nil
	}
	return &out, nil
}

// WaitDone polls until terminal and discards the resource (delete operations).
func (op *GpuOperation) WaitDone(ctx context.Context, opts *WaitOptions) error {
	return op.waitRaw(ctx, opts, nil)
}

// GpuOperationsService reads or waits on any accepted mutation by id.
type GpuOperationsService struct{ t *gpuTransport }

// Get reads one operation record.
func (s *GpuOperationsService) Get(ctx context.Context, operationID string) (*GpuOperationRecord, error) {
	var out GpuOperationRecord
	if err := s.t.get(ctx, "/v1/gpu/operations/"+operationID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Wait polls until terminal and returns the record.
func (s *GpuOperationsService) Wait(ctx context.Context, operationID string, opts *WaitOptions) (*GpuOperationRecord, error) {
	timeout, poll := int64(600_000), int64(1_000)
	if opts != nil {
		if opts.Timeout > 0 {
			timeout = opts.Timeout
		}
		if opts.PollInterval > 0 {
			poll = opts.PollInterval
		}
	}
	return s.t.poll(ctx, operationID, timeout, poll)
}

// ---------------------------------------------------------------------------
// Logs (SSE)
// ---------------------------------------------------------------------------

func logQuery(opts *GpuLogOptions, withNode bool) (string, error) {
	q := url.Values{}
	tail, source, since, node := 100, "", "", 0
	if opts != nil {
		if opts.Tail > 0 {
			tail = opts.Tail
		}
		source, since, node = opts.Source, opts.Since, opts.Node
	}
	if withNode {
		q.Set("node", strconv.Itoa(node))
	}
	q.Set("tail", strconv.Itoa(tail))
	if source != "" {
		if source != "container" && source != "system" {
			return "", &types.SdkError{Message: "log source must be \"container\" or \"system\""}
		}
		q.Set("source", source)
	}
	if since != "" {
		ts, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return "", &types.SdkError{Message: "log since must be an RFC 3339 timestamp", Err: err}
		}
		q.Set("since", ts.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	}
	return q.Encode(), nil
}

// LogStream yields relayed log events until the provider ends the stream,
// reconnecting with Last-Event-ID across the gateway's relay windows. Close
// releases the connection; Err reports the failure that ended the stream.
type LogStream struct {
	events chan GpuLogEvent
	err    error
	cancel context.CancelFunc
	done   chan struct{}
}

// Events is the channel of log lines; it closes when the stream ends.
func (s *LogStream) Events() <-chan GpuLogEvent { return s.events }

// Err is the error that ended the stream, if any (read after Events closes).
func (s *LogStream) Err() error {
	<-s.done
	return s.err
}

// Close stops the stream.
func (s *LogStream) Close() { s.cancel() }

func (t *gpuTransport) followLogs(ctx context.Context, path string) (*LogStream, error) {
	if t.client.options.APIKey == "" {
		return nil, &types.MissingAPIKeyError{}
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &LogStream{events: make(chan GpuLogEvent, 64), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(s.events)
		lastEventID := ""
		for {
			retryMs, err := t.logWindow(sctx, path, lastEventID, &lastEventID, s.events)
			if err != nil {
				if sctx.Err() == nil {
					s.err = err
				}
				return
			}
			if retryMs < 0 || sctx.Err() != nil {
				return
			}
			if retryMs > 5000 {
				retryMs = 5000
			}
			select {
			case <-sctx.Done():
				return
			case <-time.After(time.Duration(retryMs) * time.Millisecond):
			}
		}
	}()
	return s, nil
}

// logWindow reads one relay window; returns the `retry:` hint (ms) or -1 for a clean end.
func (t *gpuTransport) logWindow(ctx context.Context, path, lastEventID string, cursor *string, out chan<- GpuLogEvent) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.client.options.BaseURL+path, nil)
	if err != nil {
		return -1, &types.SdkError{Message: "failed to create HTTP request", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+t.client.options.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := t.client.httpClient.Do(req)
	if err != nil {
		return -1, &types.SdkError{Message: "HTTP request failed", Err: err, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return -1, parseErrorBody(body, resp.StatusCode, resp.Header)
	}
	retryMs := -1
	reader := bufio.NewReader(resp.Body)
	eventID, eventType := "", "message"
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		var payload struct {
			TS, Source, Line, Code string
		}
		_ = json.Unmarshal([]byte(strings.Join(data, "\n")), &payload)
		data = data[:0]
		typ, id := eventType, eventID
		eventType, eventID = "message", ""
		if typ == "error" {
			code := payload.Code
			if code == "" {
				code = "provider_stream_unavailable"
			}
			return &types.ModelStreamErrorException{ModelErrorException: types.ModelErrorException{APIError: types.APIError{Message: code, ErrorCode: code}}}
		}
		if id != "" {
			*cursor = id
		}
		select {
		case out <- GpuLogEvent{Timestamp: payload.TS, Source: payload.Source, Line: payload.Line, EventID: id}:
		case <-ctx.Done():
		}
		return nil
	}
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return -1, err
			}
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(line[3:])
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line[5:], " "), ""))
		case strings.HasPrefix(line, "retry:"):
			if ms, err := strconv.Atoi(strings.TrimSpace(line[6:])); err == nil {
				retryMs = ms
			}
		}
		if readErr != nil {
			if err := flush(); err != nil {
				return -1, err
			}
			if readErr == io.EOF || ctx.Err() != nil {
				return retryMs, nil
			}
			return -1, &types.ModelStreamErrorException{ModelErrorException: types.ModelErrorException{APIError: types.APIError{Message: "log stream failed", Err: readErr}}}
		}
	}
}

// ---------------------------------------------------------------------------
// Offers
// ---------------------------------------------------------------------------

// GpuOffersService is GET /v1/gpu/offers: what can be provided right now.
type GpuOffersService struct{ t *gpuTransport }

// List folds every backing provider per accelerator; never names one.
func (s *GpuOffersService) List(ctx context.Context, input *ListGpuOffersInput) ([]GpuOffer, error) {
	q := url.Values{}
	if input != nil {
		if input.Count > 0 {
			q.Set("count", strconv.Itoa(input.Count))
		}
		if input.Region != "" {
			q.Set("region", input.Region)
		}
		if input.Accelerator != "" {
			q.Set("accelerator", input.Accelerator)
		}
		if input.DiskGb > 0 {
			q.Set("diskGb", strconv.Itoa(input.DiskGb))
		}
		if input.PersistentGb > 0 {
			q.Set("persistentGb", strconv.Itoa(input.PersistentGb))
		}
	}
	path := "/v1/gpu/offers"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out struct {
		Data []GpuOffer `json:"data"`
	}
	if err := s.t.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ---------------------------------------------------------------------------
// Instances
// ---------------------------------------------------------------------------

// GpuInstancesService is /v1/gpu/instances.
type GpuInstancesService struct{ t *gpuTransport }

// Quote prices a request without creating anything; *types.NoCapacityError when it cannot be placed.
func (s *GpuInstancesService) Quote(ctx context.Context, input *InstanceRequest) (*GpuInstanceQuote, error) {
	if input == nil || input.Accelerator == "" {
		return nil, &types.SdkError{Message: "quote needs an accelerator"}
	}
	body := *input
	body.QuoteID = ""
	var out GpuInstanceQuote
	if err := s.t.do(ctx, http.MethodPost, "/v1/gpu/instances/quote", &body, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Launch launches from a QuoteID (workload fields only are sent beside it) or
// from Accelerator + MaxHourlyPrice. Bills hourly from placement until stopped or terminated.
func (s *GpuInstancesService) Launch(ctx context.Context, input *InstanceRequest) (*GpuOperation, error) {
	if input == nil || (input.QuoteID == "" && (input.Accelerator == "" || input.MaxHourlyPrice == "")) {
		return nil, &types.SdkError{Message: "launch needs a QuoteID, or Accelerator and MaxHourlyPrice"}
	}
	body := *input
	if body.QuoteID != "" {
		// Priced fields travel inside the quote token; repeating them would have to match exactly.
		body.Accelerator, body.MaxHourlyPrice, body.Count, body.Region, body.Image, body.DiskGb = "", "", 0, "", "", 0
		body.VolumeID, body.VolumeMountPath, body.Storage = "", "", nil
	}
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/instances", &body, input.IdempotencyKey, "sdk-instance-launch")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceInstances, false), nil
}

// List returns every instance on the organization, including terminated ones.
func (s *GpuInstancesService) List(ctx context.Context) ([]GpuInstance, error) {
	var out struct {
		Data []GpuInstance `json:"data"`
	}
	if err := s.t.get(ctx, "/v1/gpu/instances", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Get reads one instance.
func (s *GpuInstancesService) Get(ctx context.Context, instanceID string) (*GpuInstance, error) {
	var out GpuInstance
	if err := s.t.get(ctx, "/v1/gpu/instances/"+instanceID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Connections is the SSH command and HTTP URLs of a running instance.
func (s *GpuInstancesService) Connections(ctx context.Context, instanceID string) (*GpuConnections, error) {
	var out GpuConnections
	if err := s.t.get(ctx, "/v1/gpu/instances/"+instanceID+"/connections", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Logs streams relayed workload output (transient; no archive).
func (s *GpuInstancesService) Logs(ctx context.Context, instanceID string, opts *GpuLogOptions) (*LogStream, error) {
	q, err := logQuery(opts, false)
	if err != nil {
		return nil, err
	}
	return s.t.followLogs(ctx, "/v1/gpu/instances/"+instanceID+"/logs?"+q)
}

func (s *GpuInstancesService) action(ctx context.Context, instanceID, action, key string) (*GpuOperation, error) {
	method, path := http.MethodPost, "/v1/gpu/instances/"+instanceID+"/"+action
	if action == "terminate" {
		method, path = http.MethodDelete, "/v1/gpu/instances/"+instanceID
	}
	accepted, err := s.t.mutate(ctx, method, path, nil, key, "sdk-instance-"+action)
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceInstances, false), nil
}

// Stop releases the GPU; the root disk survives only where Capabilities.StopPreservesRootDisk.
func (s *GpuInstancesService) Stop(ctx context.Context, instanceID string, idempotencyKey *string) (*GpuOperation, error) {
	return s.action(ctx, instanceID, "stop", deref(idempotencyKey))
}

// Start brings a stopped instance back; capacity is rechecked.
func (s *GpuInstancesService) Start(ctx context.Context, instanceID string, idempotencyKey *string) (*GpuOperation, error) {
	return s.action(ctx, instanceID, "start", deref(idempotencyKey))
}

// Terminate is irreversible; an attached volume survives.
func (s *GpuInstancesService) Terminate(ctx context.Context, instanceID string, idempotencyKey *string) (*GpuOperation, error) {
	return s.action(ctx, instanceID, "terminate", deref(idempotencyKey))
}

// SetRecovery chooses what happens when the instance stops on its own.
func (s *GpuInstancesService) SetRecovery(ctx context.Context, instanceID string, policy RecoveryPolicy, idempotencyKey *string) (*GpuOperation, error) {
	if policy.Mode != "none" && policy.Mode != "restart" {
		return nil, &types.SdkError{Message: "recovery mode must be \"none\" or \"restart\""}
	}
	accepted, err := s.t.mutate(ctx, http.MethodPut, "/v1/gpu/instances/"+instanceID+"/recovery", &policy, deref(idempotencyKey), "sdk-instance-set_recovery")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceInstances, false), nil
}

// Recover makes one recovery attempt now.
func (s *GpuInstancesService) Recover(ctx context.Context, instanceID string, idempotencyKey *string) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/instances/"+instanceID+"/recover", nil, deref(idempotencyKey), "sdk-instance-recover")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceInstances, false), nil
}

// ---------------------------------------------------------------------------
// Clusters
// ---------------------------------------------------------------------------

// GpuClustersService is /v1/gpu/clusters (multi-node, AIR-1528).
type GpuClustersService struct{ t *gpuTransport }

// Quote prices a cluster; *types.NoCapacityError when no provider can place it.
func (s *GpuClustersService) Quote(ctx context.Context, input *ClusterRequest) (*GpuClusterQuote, error) {
	if input == nil || input.Accelerator == "" || input.Nodes == 0 {
		return nil, &types.SdkError{Message: "cluster quote needs an accelerator and nodes"}
	}
	body := *input
	body.QuoteID = ""
	var out GpuClusterQuote
	if err := s.t.do(ctx, http.MethodPost, "/v1/gpu/clusters/quote", &body, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Launch launches from a QuoteID or from Accelerator + Nodes + MaxHourlyPrice (the whole cluster's ceiling).
func (s *GpuClustersService) Launch(ctx context.Context, input *ClusterRequest) (*GpuOperation, error) {
	if input == nil || (input.QuoteID == "" && (input.Accelerator == "" || input.Nodes == 0 || input.MaxHourlyPrice == "")) {
		return nil, &types.SdkError{Message: "cluster launch needs a QuoteID, or Accelerator, Nodes and MaxHourlyPrice"}
	}
	body := *input
	if body.QuoteID != "" {
		body.Accelerator, body.Nodes, body.GpusPerNode, body.MaxHourlyPrice, body.Region, body.Orchestrator, body.Image, body.DiskGbPerNode = "", 0, 0, "", "", "", "", 0
		body.VolumeID, body.VolumeMountPath, body.Storage = "", "", nil
	}
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/clusters", &body, input.IdempotencyKey, "sdk-cluster-launch")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceClusters, false), nil
}

// List returns every cluster, newest first, including terminated ones.
func (s *GpuClustersService) List(ctx context.Context) ([]GpuCluster, error) {
	var out struct {
		Data []GpuCluster `json:"data"`
	}
	if err := s.t.get(ctx, "/v1/gpu/clusters", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Get reads one cluster with its nodes.
func (s *GpuClustersService) Get(ctx context.Context, clusterID string) (*GpuCluster, error) {
	var out GpuCluster
	if err := s.t.get(ctx, "/v1/gpu/clusters/"+clusterID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Connections is SSH for the primary and every member plus interconnect addresses.
func (s *GpuClustersService) Connections(ctx context.Context, clusterID string) (*GpuClusterConnections, error) {
	var out GpuClusterConnections
	if err := s.t.get(ctx, "/v1/gpu/clusters/"+clusterID+"/connections", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Logs streams one node's output (opts.Node is the rank; 0 by default).
func (s *GpuClustersService) Logs(ctx context.Context, clusterID string, opts *GpuLogOptions) (*LogStream, error) {
	q, err := logQuery(opts, true)
	if err != nil {
		return nil, err
	}
	return s.t.followLogs(ctx, "/v1/gpu/clusters/"+clusterID+"/logs?"+q)
}

// Terminate releases every node; the shared volume is kept.
func (s *GpuClustersService) Terminate(ctx context.Context, clusterID string, idempotencyKey *string) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodDelete, "/v1/gpu/clusters/"+clusterID, nil, deref(idempotencyKey), "sdk-cluster-terminate")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceClusters, false), nil
}

// SetRecovery chooses what happens when the cluster goes degraded.
func (s *GpuClustersService) SetRecovery(ctx context.Context, clusterID string, policy RecoveryPolicy, idempotencyKey *string) (*GpuOperation, error) {
	if policy.Mode != "none" && policy.Mode != "restart" {
		return nil, &types.SdkError{Message: "recovery mode must be \"none\" or \"restart\""}
	}
	accepted, err := s.t.mutate(ctx, http.MethodPut, "/v1/gpu/clusters/"+clusterID+"/recovery", &policy, deref(idempotencyKey), "sdk-cluster-set_recovery")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceClusters, false), nil
}

// Recover makes one recovery attempt now on every node that is not running.
func (s *GpuClustersService) Recover(ctx context.Context, clusterID string, idempotencyKey *string) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/clusters/"+clusterID+"/recover", nil, deref(idempotencyKey), "sdk-cluster-recover")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceClusters, false), nil
}

// ---------------------------------------------------------------------------
// Volumes
// ---------------------------------------------------------------------------

// GpuVolumesService is /v1/gpu/volumes.
type GpuVolumesService struct{ t *gpuTransport }

// Create makes a standalone volume in a region; AcknowledgeHourlyBilling must be true.
func (s *GpuVolumesService) Create(ctx context.Context, input *CreateVolumeInput) (*GpuOperation, error) {
	if input == nil || !input.AcknowledgeHourlyBilling {
		return nil, &types.SdkError{Message: "AcknowledgeHourlyBilling must be true: a volume bills hourly until deleted"}
	}
	body := struct {
		CreateVolumeInput
		StorageBillingAcknowledged bool `json:"storageBillingAcknowledged"`
	}{*input, true}
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/volumes", &body, input.IdempotencyKey, "sdk-volume-create")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceVolumes, false), nil
}

// List returns every volume on the organization.
func (s *GpuVolumesService) List(ctx context.Context) ([]GpuVolume, error) {
	var out struct {
		Data []GpuVolume `json:"data"`
	}
	if err := s.t.get(ctx, "/v1/gpu/volumes", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Get reads one volume.
func (s *GpuVolumesService) Get(ctx context.Context, volumeID string) (*GpuVolume, error) {
	var out GpuVolume
	if err := s.t.get(ctx, "/v1/gpu/volumes/"+volumeID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Grow grows a volume without a quote; MaxHourlyPrice caps the new storage rate
// (*types.NoCapacityError, price_ceiling, when above it; nothing changes).
func (s *GpuVolumesService) Grow(ctx context.Context, volumeID string, input *GrowVolumeInput) (*GpuOperation, error) {
	if input == nil || input.SizeGb <= 0 {
		return nil, &types.SdkError{Message: "grow needs a SizeGb"}
	}
	accepted, err := s.t.mutate(ctx, http.MethodPatch, "/v1/gpu/volumes/"+volumeID, input, input.IdempotencyKey, "sdk-volume-grow")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceVolumes, false), nil
}

// Delete is irreversible and refused while attached; WaitVolume resolves to nil.
func (s *GpuVolumesService) Delete(ctx context.Context, volumeID string, idempotencyKey *string) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodDelete, "/v1/gpu/volumes/"+volumeID, nil, deref(idempotencyKey), "sdk-volume-delete")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceVolumes, true), nil
}

// ---------------------------------------------------------------------------
// SSH keys, registries, templates, usage
// ---------------------------------------------------------------------------

// GpuSshKeysService is /v1/gpu/ssh-keys (both mutations are immediate).
type GpuSshKeysService struct{ t *gpuTransport }

// Add registers a public key and returns it.
func (s *GpuSshKeysService) Add(ctx context.Context, name, publicKey string, idempotencyKey *string) (*GpuSSHKey, error) {
	body := map[string]string{"name": name, "publicKey": publicKey}
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/ssh-keys", body, deref(idempotencyKey), "sdk-ssh-key-add")
	if err != nil {
		return nil, err
	}
	keys, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].ID == accepted.ResourceID {
			return &keys[i], nil
		}
	}
	return nil, &types.SdkError{Message: "SSH key was accepted but is not listed yet", Retryable: true}
}

// List returns the organization's keys.
func (s *GpuSshKeysService) List(ctx context.Context) ([]GpuSSHKey, error) {
	var out struct {
		Data []GpuSSHKey `json:"data"`
	}
	if err := s.t.get(ctx, "/v1/gpu/ssh-keys", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Revoke removes a key from future starts and launches.
func (s *GpuSshKeysService) Revoke(ctx context.Context, keyID string, idempotencyKey *string) error {
	_, err := s.t.mutate(ctx, http.MethodDelete, "/v1/gpu/ssh-keys/"+keyID, nil, deref(idempotencyKey), "sdk-ssh-key-revoke")
	return err
}

// GpuRegistriesService is /v1/gpu/registries. Secrets are write-only.
type GpuRegistriesService struct{ t *gpuTransport }

// Add stores a credential; WaitRegistry returns it.
func (s *GpuRegistriesService) Add(ctx context.Context, input *AddRegistryInput) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/registries", input, input.IdempotencyKey, "sdk-registry-add")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceRegistries, false), nil
}

// List returns credential metadata.
func (s *GpuRegistriesService) List(ctx context.Context) ([]GpuRegistryCredential, error) {
	var out struct {
		Data []GpuRegistryCredential `json:"data"`
	}
	if err := s.t.get(ctx, "/v1/gpu/registries", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Rotate replaces the secret (and optionally the username) in place.
func (s *GpuRegistriesService) Rotate(ctx context.Context, registryID, password string, username *string, idempotencyKey *string) (*GpuOperation, error) {
	body := map[string]string{"password": password}
	if username != nil {
		body["username"] = *username
	}
	accepted, err := s.t.mutate(ctx, http.MethodPatch, "/v1/gpu/registries/"+registryID, body, deref(idempotencyKey), "sdk-registry-rotate")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceRegistries, false), nil
}

// Delete removes a credential; blocked while an active instance references it.
func (s *GpuRegistriesService) Delete(ctx context.Context, registryID string, idempotencyKey *string) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodDelete, "/v1/gpu/registries/"+registryID, nil, deref(idempotencyKey), "sdk-registry-delete")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceRegistries, true), nil
}

// GpuTemplatesService is /v1/gpu/templates.
type GpuTemplatesService struct{ t *gpuTransport }

// Create saves a private template. Env values are never returned.
func (s *GpuTemplatesService) Create(ctx context.Context, input *TemplateInput) (*GpuOperation, error) {
	if input == nil || input.Name == "" || input.ImageName == "" {
		return nil, &types.SdkError{Message: "template create needs Name and ImageName"}
	}
	body := *input
	body.IsActive = nil
	if body.ContainerDiskGb == 0 {
		body.ContainerDiskGb = 10
	}
	accepted, err := s.t.mutate(ctx, http.MethodPost, "/v1/gpu/templates", &body, input.IdempotencyKey, "sdk-template-create")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceTemplates, false), nil
}

// List returns curated plus private templates.
func (s *GpuTemplatesService) List(ctx context.Context) ([]GpuTemplate, error) {
	var out struct {
		Data []GpuTemplate `json:"data"`
	}
	if err := s.t.get(ctx, "/v1/gpu/templates", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Get reads one template.
func (s *GpuTemplatesService) Get(ctx context.Context, templateID string) (*GpuTemplate, error) {
	var out GpuTemplate
	if err := s.t.get(ctx, "/v1/gpu/templates/"+templateID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update patches a private template (at least one field).
func (s *GpuTemplatesService) Update(ctx context.Context, templateID string, input *TemplateInput) (*GpuOperation, error) {
	if input == nil || (input.Name == "" && input.ImageName == "" && input.ContainerDiskGb == 0 && input.Env == nil && input.Ports == nil && input.DockerStartCmd == nil && input.IsActive == nil) {
		return nil, &types.SdkError{Message: "template update needs at least one field"}
	}
	accepted, err := s.t.mutate(ctx, http.MethodPatch, "/v1/gpu/templates/"+templateID, input, input.IdempotencyKey, "sdk-template-update")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceTemplates, false), nil
}

// Delete removes a private template.
func (s *GpuTemplatesService) Delete(ctx context.Context, templateID string, idempotencyKey *string) (*GpuOperation, error) {
	accepted, err := s.t.mutate(ctx, http.MethodDelete, "/v1/gpu/templates/"+templateID, nil, deref(idempotencyKey), "sdk-template-delete")
	if err != nil {
		return nil, err
	}
	return s.t.operation(accepted, resourceTemplates, true), nil
}

// GpuUsageService is GET /v1/gpu/usage.
type GpuUsageService struct{ t *gpuTransport }

// List returns one page of billing windows; pass NextCursor as cursor for the next.
func (s *GpuUsageService) List(ctx context.Context, cursor string) (*GpuUsagePage, error) {
	path := "/v1/gpu/usage"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	var out struct {
		Data       []GpuUsageCharge `json:"data"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := s.t.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &GpuUsagePage{Charges: out.Data, NextCursor: out.NextCursor}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

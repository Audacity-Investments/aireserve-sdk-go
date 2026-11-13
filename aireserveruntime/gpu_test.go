package aireserveruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime"
	"github.com/Audacity-Investments/aireserve-sdk-go/aireserveruntime/types"
)

type seenRequest struct {
	Method, Path string
	Query        map[string]string
	Headers      http.Header
	Body         map[string]interface{}
}

type routeServer struct {
	mu       sync.Mutex
	requests []seenRequest
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func newRouteServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) (*aireserveruntime.Client, *routeServer) {
	t.Helper()
	rs := &routeServer{routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		q := map[string]string{}
		for k, v := range r.URL.Query() {
			q[k] = v[0]
		}
		rs.mu.Lock()
		rs.requests = append(rs.requests, seenRequest{Method: r.Method, Path: r.URL.Path, Query: q, Headers: r.Header.Clone(), Body: body})
		rs.mu.Unlock()
		if h, ok := routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return aireserveruntime.New(aireserveruntime.Options{APIKey: "k", BaseURL: srv.URL, MaxRetries: -1}), rs
}

func gpuJSON(status int, body interface{}) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func gpuSSE(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}
}

func (rs *routeServer) last() seenRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.requests[len(rs.requests)-1]
}

func TestGpuLaunchIsProviderHiddenAndIdempotent(t *testing.T) {
	client, rs := newRouteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/gpu/instances": gpuJSON(202, map[string]string{"operationId": "op-1", "resourceId": "i-1"}),
	})
	op, err := client.Gpu.Instances.Launch(context.Background(), &aireserveruntime.InstanceRequest{Accelerator: "H100_80GB", MaxHourlyPrice: "3.00", Storage: &aireserveruntime.GpuStorageRequest{PersistentGb: 500}})
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != "op-1" || op.ResourceID != "i-1" {
		t.Fatalf("unexpected handle %+v", op)
	}
	req := rs.last()
	if !strings.HasPrefix(req.Headers.Get("Idempotency-Key"), "sdk-instance-launch-") {
		t.Fatalf("idempotency key %q", req.Headers.Get("Idempotency-Key"))
	}
	for _, forbidden := range []string{"provider", "dataCenterId", "cloud", "tenancy"} {
		if _, ok := req.Body[forbidden]; ok {
			t.Fatalf("body carries %s", forbidden)
		}
	}
	if req.Body["accelerator"] != "H100_80GB" || req.Body["maxHourlyPrice"] != "3.00" {
		t.Fatalf("body %v", req.Body)
	}
	if _, err := client.Gpu.Instances.Launch(context.Background(), &aireserveruntime.InstanceRequest{Accelerator: "H100_80GB"}); err == nil {
		t.Fatal("launch without a ceiling or quote must fail client-side")
	}
}

func TestGpuQuotedLaunchSendsWorkloadFieldsOnly(t *testing.T) {
	client, rs := newRouteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/gpu/instances": gpuJSON(202, map[string]string{"operationId": "op-1", "resourceId": "i-1"}),
	})
	_, err := client.Gpu.Instances.Launch(context.Background(), &aireserveruntime.InstanceRequest{QuoteID: "q", Accelerator: "H100_80GB", DiskGb: 50, SSHPublicKey: "ssh-ed25519 AAAA", Env: map[string]string{"A": "1"}, IdempotencyKey: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	req := rs.last()
	if req.Headers.Get("Idempotency-Key") != "mine" {
		t.Fatalf("caller key not honoured")
	}
	if _, ok := req.Body["accelerator"]; ok {
		t.Fatal("priced fields must not accompany a quoteId")
	}
	if req.Body["quoteId"] != "q" || req.Body["sshPublicKey"] != "ssh-ed25519 AAAA" {
		t.Fatalf("body %v", req.Body)
	}
}

func TestGpuErrorsMapToTypedExceptions(t *testing.T) {
	client, _ := newRouteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/gpu/instances/quote": gpuJSON(409, map[string]interface{}{"error": map[string]interface{}{"message": "No capacity.", "type": "invalid_request_error", "code": "no_capacity",
			"details": map[string]interface{}{"reason": "price_ceiling", "cheapestEligibleUsdPerHour": "3.420000", "alternatives": []map[string]interface{}{{"region": "united_states", "totalRunningUsdPerHour": "2.890000"}}}}}),
		"POST /v1/gpu/instances":        gpuJSON(409, map[string]interface{}{"error": map[string]interface{}{"message": "Changed.", "type": "invalid_request_error", "code": "price_changed"}}),
		"DELETE /v1/gpu/volumes/v-1":    gpuJSON(409, map[string]interface{}{"error": map[string]interface{}{"message": "In use.", "type": "invalid_request_error", "code": "volume_in_use"}}),
		"GET /v1/gpu/instances/missing": gpuJSON(404, map[string]interface{}{"error": map[string]interface{}{"message": "Nope.", "type": "invalid_request_error", "code": "not_found"}}),
		"POST /v1/gpu/clusters/quote":   gpuJSON(409, map[string]interface{}{"error": map[string]interface{}{"message": "Expired.", "type": "invalid_request_error", "code": "quote_expired"}}),
	})
	ctx := context.Background()
	_, err := client.Gpu.Instances.Quote(ctx, &aireserveruntime.InstanceRequest{Accelerator: "H100_80GB", MaxHourlyPrice: "0.50"})
	var nc *types.NoCapacityError
	if !errors.As(err, &nc) || nc.Reason != "price_ceiling" || nc.CheapestEligibleUsdPerHour != "3.420000" || len(nc.Alternatives) != 1 {
		t.Fatalf("no_capacity mapping: %#v", err)
	}
	_, err = client.Gpu.Instances.Launch(ctx, &aireserveruntime.InstanceRequest{QuoteID: "q"})
	var pc *types.PriceChangedError
	if !errors.As(err, &pc) {
		t.Fatalf("price_changed mapping: %#v", err)
	}
	var qe *types.QuoteExpiredError
	if errors.As(err, &qe) {
		t.Fatal("price_changed must not be a QuoteExpiredError")
	}
	_, err = client.Gpu.Volumes.Delete(ctx, "v-1", nil)
	var ve *types.ValidationException
	if !errors.As(err, &ve) {
		t.Fatalf("volume_in_use mapping: %#v", err)
	}
	_, err = client.Gpu.Instances.Get(ctx, "missing")
	var nf *types.ResourceNotFoundException
	if !errors.As(err, &nf) {
		t.Fatalf("not_found mapping: %#v", err)
	}
	_, err = client.Gpu.Clusters.Quote(ctx, &aireserveruntime.ClusterRequest{Accelerator: "H200_141GB", Nodes: 2})
	if !errors.As(err, &qe) {
		t.Fatalf("quote_expired mapping: %#v", err)
	}
}

func TestGpuWaitFollowsTheOperationThenReadsTheResource(t *testing.T) {
	polls := 0
	client, _ := newRouteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/gpu/volumes": gpuJSON(202, map[string]string{"operationId": "op-v", "resourceId": "v-1"}),
		"GET /v1/gpu/operations/op-v": func(w http.ResponseWriter, r *http.Request) {
			polls++
			status := "in_progress"
			if polls > 1 {
				status = "succeeded"
			}
			gpuJSON(200, map[string]string{"id": "op-v", "status": status})(w, r)
		},
		"GET /v1/gpu/volumes/v-1": gpuJSON(200, map[string]interface{}{"id": "v-1", "name": "w", "status": "ready", "region": "europe", "sizeGb": 500, "hourlyRateUsd": "0.048000", "createdAt": "t"}),
		"GET /v1/gpu/operations/op-f": gpuJSON(200, map[string]interface{}{"id": "op-f", "status": "failed", "errorMessage": "growth_rejected:validation",
			"failure": map[string]string{"code": "growth_rejected:validation", "actor": "provider", "message": "cannot grow", "next": "make a new one"}}),
	})
	ctx := context.Background()
	op, err := client.Gpu.Volumes.Create(ctx, &aireserveruntime.CreateVolumeInput{Name: "w", Region: "europe", SizeGb: 500, AcknowledgeHourlyBilling: true})
	if err != nil {
		t.Fatal(err)
	}
	vol, err := op.WaitVolume(ctx, &aireserveruntime.WaitOptions{PollInterval: 1})
	if err != nil || vol.SizeGb != 500 {
		t.Fatalf("wait: %v %+v", err, vol)
	}
	if _, err := client.Gpu.Volumes.Create(ctx, &aireserveruntime.CreateVolumeInput{Name: "w", Region: "europe", SizeGb: 500}); err == nil {
		t.Fatal("create without the billing acknowledgement must fail client-side")
	}
	_, err = client.Gpu.Operations.Wait(ctx, "op-f", &aireserveruntime.WaitOptions{PollInterval: 1})
	var failed *types.OperationFailedError
	if !errors.As(err, &failed) || failed.Failure == nil || failed.Failure.Actor != "provider" || failed.Message != "cannot grow" {
		t.Fatalf("failure: %#v", err)
	}
}

func TestGpuGrowLogsRecoveryClusterSurface(t *testing.T) {
	client, rs := newRouteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"PATCH /v1/gpu/volumes/v-1":          gpuJSON(202, map[string]string{"operationId": "op-g", "resourceId": "v-1"}),
		"GET /v1/gpu/instances/i-1/logs":     gpuSSE("id: 1\ndata: {\"ts\":\"t\",\"source\":\"system\",\"line\":\"pull\"}\n\n"),
		"GET /v1/gpu/clusters/c-1/logs":      gpuSSE("id: 1\ndata: {\"ts\":\"t\",\"source\":\"container\",\"line\":\"rank 3\"}\n\n"),
		"PUT /v1/gpu/instances/i-1/recovery": gpuJSON(202, map[string]string{"operationId": "op-p", "resourceId": "i-1"}),
		"POST /v1/gpu/clusters":              gpuJSON(202, map[string]string{"operationId": "op-c", "resourceId": "c-1"}),
		"POST /v1/gpu/clusters/c-1/recover":  gpuJSON(202, map[string]string{"operationId": "op-cr", "resourceId": "c-1"}),
		"DELETE /v1/gpu/clusters/c-1":        gpuJSON(202, map[string]string{"operationId": "op-ct", "resourceId": "c-1"}),
		"POST /v1/gpu/ssh-keys":              gpuJSON(202, map[string]string{"operationId": "op-k", "resourceId": "key-1"}),
		"GET /v1/gpu/ssh-keys":               gpuJSON(200, map[string]interface{}{"data": []map[string]string{{"id": "key-1", "name": "laptop", "fingerprint": "SHA256:abc", "publicKey": "ssh-ed25519 AAAA", "state": "active", "createdAt": "t"}}}),
		"POST /v1/gpu/templates":             gpuJSON(202, map[string]string{"operationId": "op-t", "resourceId": "tpl-1"}),
		"GET /v1/gpu/usage":                  gpuJSON(200, map[string]interface{}{"data": []map[string]string{{"id": "ch-1", "resourceType": "pod", "resourceId": "i-1", "kind": "compute", "status": "captured", "amountUsd": "3.420000"}}, "nextCursor": "c2"}),
	})
	ctx := context.Background()
	if _, err := client.Gpu.Volumes.Grow(ctx, "v-1", &aireserveruntime.GrowVolumeInput{SizeGb: 750, MaxHourlyPrice: "0.06"}); err != nil {
		t.Fatal(err)
	}
	if req := rs.last(); req.Body["sizeGb"] != float64(750) || req.Body["maxHourlyPrice"] != "0.06" || req.Body["quoteId"] != nil {
		t.Fatalf("grow body %v", req.Body)
	}

	stream, err := client.Gpu.Instances.Logs(ctx, "i-1", &aireserveruntime.GpuLogOptions{Tail: 10, Source: "system", Since: "2026-10-12T20:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for e := range stream.Events() {
		lines = append(lines, e.Line)
	}
	if stream.Err() != nil || len(lines) != 1 || lines[0] != "pull" {
		t.Fatalf("logs: %v %v", stream.Err(), lines)
	}
	if q := rs.last().Query; q["tail"] != "10" || q["source"] != "system" || q["since"] != "2026-10-12T20:00:00.000Z" {
		t.Fatalf("log query %v", q)
	}
	if _, err := client.Gpu.Instances.Logs(ctx, "i-1", &aireserveruntime.GpuLogOptions{Source: "kernel"}); err == nil {
		t.Fatal("unknown source must be refused client-side")
	}
	cstream, err := client.Gpu.Clusters.Logs(ctx, "c-1", &aireserveruntime.GpuLogOptions{Node: 3, Tail: 5})
	if err != nil {
		t.Fatal(err)
	}
	for range cstream.Events() {
	}
	if q := rs.last().Query; q["node"] != "3" || q["tail"] != "5" {
		t.Fatalf("cluster log query %v", q)
	}

	if _, err := client.Gpu.Instances.SetRecovery(ctx, "i-1", aireserveruntime.RecoveryPolicy{Mode: "restart", MaxAttempts: 3}, nil); err != nil {
		t.Fatal(err)
	}
	if req := rs.last(); req.Body["mode"] != "restart" || req.Body["maxAttempts"] != float64(3) {
		t.Fatalf("recovery body %v", req.Body)
	}
	if _, err := client.Gpu.Instances.SetRecovery(ctx, "i-1", aireserveruntime.RecoveryPolicy{Mode: "sometimes"}, nil); err == nil {
		t.Fatal("bad recovery mode must be refused")
	}

	if _, err := client.Gpu.Clusters.Launch(ctx, &aireserveruntime.ClusterRequest{Accelerator: "H200_141GB", Nodes: 2, MaxHourlyPrice: "100", SSHPublicKey: "ssh-ed25519 AAAA"}); err != nil {
		t.Fatal(err)
	}
	if req := rs.last(); req.Body["nodes"] != float64(2) || req.Body["registryCredentialId"] != nil || !strings.HasPrefix(req.Headers.Get("Idempotency-Key"), "sdk-cluster-launch-") {
		t.Fatalf("cluster launch %v", req.Body)
	}
	if _, err := client.Gpu.Clusters.Launch(ctx, &aireserveruntime.ClusterRequest{Accelerator: "H200_141GB", Nodes: 2}); err == nil {
		t.Fatal("cluster launch without a ceiling or quote must fail client-side")
	}
	if _, err := client.Gpu.Clusters.Recover(ctx, "c-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Gpu.Clusters.Terminate(ctx, "c-1", nil); err != nil {
		t.Fatal(err)
	}

	key, err := client.Gpu.SshKeys.Add(ctx, "laptop", "ssh-ed25519 AAAA", nil)
	if err != nil || key.Fingerprint != "SHA256:abc" {
		t.Fatalf("ssh key: %v %+v", err, key)
	}
	if _, err := client.Gpu.Templates.Create(ctx, &aireserveruntime.TemplateInput{Name: "vllm", ImageName: "vllm/vllm-openai:v0.11.0", Env: map[string]string{"HF_TOKEN": "x"}}); err != nil {
		t.Fatal(err)
	}
	if req := rs.last(); req.Body["containerDiskGb"] != float64(10) || req.Body["imageName"] != "vllm/vllm-openai:v0.11.0" {
		t.Fatalf("template body %v", req.Body)
	}
	usage, err := client.Gpu.Usage.List(ctx, "")
	if err != nil || usage.Charges[0].AmountUsd != "3.420000" || usage.NextCursor != "c2" {
		t.Fatalf("usage: %v %+v", err, usage)
	}
}

// --- Fixture corpus replay (SPEC §10) -------------------------------------

type exchange struct {
	Scenario string `json:"scenario"`
	Request  struct {
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Query  map[string]string `json:"query"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"response"`
}

func loadScenarios(t *testing.T, names ...string) []exchange {
	t.Helper()
	// aireserve-sdk keeps the corpus at <repo>/fixtures (two levels up from
	// go/aireserveruntime); the aireserve-sdk-go mirror at <repo>/fixtures (one up).
	var raw []byte
	var err error
	for _, candidate := range []string{filepath.Join("..", "..", "fixtures", "gpu", "exchanges.json"), filepath.Join("..", "fixtures", "gpu", "exchanges.json")} {
		if raw, err = os.ReadFile(candidate); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("fixture corpus not present: %v", err)
	}
	var corpus struct {
		Exchanges []exchange `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	var out []exchange
	for _, e := range corpus.Exchanges {
		for _, n := range names {
			if e.Scenario == n {
				out = append(out, e)
			}
		}
	}
	return out
}

// replayServer answers each request with the next unused recorded exchange for
// its method + path (exact query preferred); the last match repeats.
func replayServer(t *testing.T, exchanges []exchange) (*aireserveruntime.Client, *routeServer, []exchange) {
	t.Helper()
	used := make([]bool, len(exchanges))
	var mu sync.Mutex
	rs := &routeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		q := map[string]string{}
		for k, v := range r.URL.Query() {
			q[k] = v[0]
		}
		rs.mu.Lock()
		rs.requests = append(rs.requests, seenRequest{Method: r.Method, Path: r.URL.Path, Query: q, Headers: r.Header.Clone(), Body: body})
		rs.mu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		var candidates, exact []int
		for i, e := range exchanges {
			if e.Request.Method != r.Method || e.Request.Path != r.URL.Path {
				continue
			}
			candidates = append(candidates, i)
			if sameQuery(e.Request.Query, q) {
				exact = append(exact, i)
			}
		}
		if len(candidates) == 0 {
			http.Error(w, "no recorded exchange", http.StatusNotFound)
			return
		}
		pool := candidates
		if len(exact) > 0 {
			pool = exact
		}
		index := pool[len(pool)-1]
		for _, i := range pool {
			if !used[i] {
				index = i
				break
			}
		}
		used[index] = true
		resp := exchanges[index].Response
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		var asString string
		if json.Unmarshal(resp.Body, &asString) == nil {
			_, _ = w.Write([]byte(asString))
			return
		}
		_, _ = w.Write(resp.Body)
	}))
	t.Cleanup(srv.Close)
	return aireserveruntime.New(aireserveruntime.Options{APIKey: "k", BaseURL: srv.URL, MaxRetries: -1}), rs, exchanges
}

func sameQuery(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestFixtureMarketplaceLifecycle(t *testing.T) {
	client, rs, _ := replayServer(t, loadScenarios(t, "marketplace-lifecycle"))
	ctx := context.Background()
	fast := &aireserveruntime.WaitOptions{PollInterval: 1}
	op, err := client.Gpu.Instances.Launch(ctx, &aireserveruntime.InstanceRequest{Accelerator: "RTX_4090_24GB", Region: "united_states", MaxHourlyPrice: "0.50", Name: "sd-webui",
		SSHPublicKey: "ssh-ed25519 AAAA fixture", Ports: []aireserveruntime.GpuPort{{Port: 7860, Protocol: "http"}}, PublicPortsAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := op.Wait(ctx, fast)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Status != "running" || instance.Accelerator.Model != "RTX_4090_24GB" || instance.Price.UsdPerHour != "0.340000" || !instance.Capabilities.StopPreservesRootDisk || instance.Capabilities.PersistentVolumes {
		t.Fatalf("instance %+v", instance)
	}
	conn, err := client.Gpu.Instances.Connections(ctx, instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	// AIR-1679: the marketplace `ssh -p <port> user@host` shape is structured, not nil.
	if conn.SSH == nil || conn.SSH.Command != "ssh -p 35410 root@198.51.100.7" || conn.SSH.Host != "198.51.100.7" || conn.SSH.Port != 35410 {
		t.Fatalf("ssh %+v", conn.SSH)
	}
	if _, err := client.Gpu.Instances.Get(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Gpu.Instances.Connections(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Gpu.Instances.Logs(ctx, instance.ID, &aireserveruntime.GpuLogOptions{Tail: 10, Source: "container"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for e := range stream.Events() {
		n++
		if !strings.Contains(e.Line, "[container] marketplace") {
			t.Fatalf("unexpected line %q", e.Line)
		}
	}
	if n == 0 || stream.Err() != nil {
		t.Fatalf("logs: %d lines, err %v", n, stream.Err())
	}
	for _, step := range []struct {
		run  func() (*aireserveruntime.GpuOperation, error)
		want string
	}{
		{func() (*aireserveruntime.GpuOperation, error) {
			return client.Gpu.Instances.Stop(ctx, instance.ID, nil)
		}, "stopped"},
		{func() (*aireserveruntime.GpuOperation, error) {
			return client.Gpu.Instances.Start(ctx, instance.ID, nil)
		}, "running"},
		{func() (*aireserveruntime.GpuOperation, error) {
			return client.Gpu.Instances.Terminate(ctx, instance.ID, nil)
		}, "terminated"},
	} {
		op, err := step.run()
		if err != nil {
			t.Fatal(err)
		}
		got, err := op.Wait(ctx, fast)
		if err != nil || got.Status != step.want {
			t.Fatalf("want %s: %v %+v", step.want, err, got)
		}
	}
	blob, _ := json.Marshal(rs.requests)
	if lower := strings.ToLower(string(blob)); strings.Contains(lower, "vast") || strings.Contains(lower, "runpod") {
		t.Fatal("a provider name reached the wire")
	}
}

func TestFixtureClusterNoProviderAndQuoteRefusals(t *testing.T) {
	client, rs, _ := replayServer(t, loadScenarios(t, "cluster-no-capacity", "instance-quote-no-capacity"))
	ctx := context.Background()
	_, err := client.Gpu.Clusters.Quote(ctx, &aireserveruntime.ClusterRequest{Accelerator: "RTX_4090_24GB", Nodes: 2, GpusPerNode: 1})
	var nc *types.NoCapacityError
	if !errors.As(err, &nc) || nc.Reason != "no_stock" {
		t.Fatalf("cluster no_capacity: %#v", err)
	}
	if lower := strings.ToLower(nc.Message); strings.Contains(lower, "vast") || strings.Contains(lower, "runpod") {
		t.Fatal("refusal names a provider")
	}
	if rs.last().Body["registryCredentialId"] != nil {
		t.Fatal("cluster quote carried a registry credential")
	}
	_, err = client.Gpu.Instances.Quote(ctx, &aireserveruntime.InstanceRequest{Accelerator: "H100_80GB", MaxHourlyPrice: "0.50"})
	if !errors.As(err, &nc) || nc.Reason != "price_ceiling" || nc.CheapestEligibleUsdPerHour == "" {
		t.Fatalf("instance price_ceiling: %#v", err)
	}
}

func TestFixtureVolumeGrowAndCluster(t *testing.T) {
	client, rs, exchanges := replayServer(t, loadScenarios(t, "volume-grow"))
	ctx := context.Background()
	fast := &aireserveruntime.WaitOptions{PollInterval: 1}
	parts := strings.Split(exchanges[0].Request.Path, "/")
	volumeID := parts[len(parts)-1]
	before, err := client.Gpu.Volumes.Get(ctx, volumeID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Gpu.Volumes.Grow(ctx, volumeID, &aireserveruntime.GrowVolumeInput{SizeGb: 500, MaxHourlyPrice: "0.000001"})
	var nc *types.NoCapacityError
	if !errors.As(err, &nc) || nc.Reason != "price_ceiling" {
		t.Fatalf("grow ceiling: %#v", err)
	}
	after, _ := client.Gpu.Volumes.Get(ctx, volumeID)
	if after.SizeGb != before.SizeGb {
		t.Fatal("a refused grow changed the volume")
	}
	op, err := client.Gpu.Volumes.Grow(ctx, volumeID, &aireserveruntime.GrowVolumeInput{SizeGb: 500})
	if err != nil {
		t.Fatal(err)
	}
	grown, err := op.WaitVolume(ctx, fast)
	if err != nil || grown.SizeGb != 500 {
		t.Fatalf("grow: %v %+v", err, grown)
	}
	for _, r := range rs.requests {
		if r.Path == "/v1/gpu/quotes" {
			t.Fatal("the SDK must never call the legacy quote route")
		}
	}

	client, rs, _ = replayServer(t, loadScenarios(t, "cluster"))
	quote, err := client.Gpu.Clusters.Quote(ctx, &aireserveruntime.ClusterRequest{Accelerator: "H200_141GB", Nodes: 8, GpusPerNode: 8, Region: "europe", Storage: &aireserveruntime.GpuStorageRequest{PersistentGb: 2000, MountPath: "/workspace"}})
	if err != nil || quote.TotalGpus != 64 {
		t.Fatalf("cluster quote: %v %+v", err, quote)
	}
	cop, err := client.Gpu.Clusters.Launch(ctx, &aireserveruntime.ClusterRequest{QuoteID: quote.QuoteID, SSHPublicKey: "ssh-ed25519 AAAA fixture"})
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := cop.WaitCluster(ctx, fast)
	if err != nil || cluster.Status != "running" {
		t.Fatalf("cluster: %v %+v", err, cluster)
	}
	conn, err := client.Gpu.Clusters.Connections(ctx, cluster.ID)
	if err != nil || conn.Primary == nil || len(conn.Nodes) != 8 {
		t.Fatalf("connections: %v %+v", err, conn)
	}
	stream, err := client.Gpu.Clusters.Logs(ctx, cluster.ID, &aireserveruntime.GpuLogOptions{Node: 5, Tail: 3})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range stream.Events() {
		n++
	}
	if n == 0 || rs.last().Query["node"] != "5" || rs.last().Query["tail"] != "3" {
		t.Fatalf("cluster logs: %d lines, query %v", n, rs.last().Query)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestStore builds a store directly without touching the filesystem.
func newTestStore(originTS float64, entries []struct {
	ts              float64
	variantName     string
	targetContainer string
	acceleratorType string
	capacity        string
	value           string
}) *store {
	s := &store{series: make(map[seriesKey][]sample)}
	for _, e := range entries {
		key := seriesKey{
			variantName:     e.variantName,
			targetContainer: e.targetContainer,
			acceleratorType: e.acceleratorType,
			capacity:        e.capacity,
		}
		s.series[key] = append(s.series[key], sample{ts: e.ts, value: e.value})
	}
	s.originTS = originTS
	return s
}

// newTestServer creates a server with a fixed startTime so tests are deterministic.
func newTestServer(s *store, startTime float64) *server {
	return &server{
		store:              s,
		replicaMetricName:  "desired_replica",
		capacityMetricName: "wva_desired_capacity_per_device",
		startTime:          startTime,
	}
}

// queryHTTP fires a GET /api/v1/query and returns the decoded response.
func queryHTTP(t *testing.T, srv *server, query string, wallTime float64) promResponse {
	t.Helper()
	q := url.Values{}
	q.Set("query", query)
	if wallTime > 0 {
		q.Set("time", formatFloat(wallTime))
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/query?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	srv.handleQuery(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rr.Code)
	}
	var resp promResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// queryHTTPPost fires a POST /api/v1/query (form-encoded body).
func queryHTTPPost(t *testing.T, srv *server, query string, wallTime float64) promResponse {
	t.Helper()
	form := url.Values{}
	form.Set("query", query)
	if wallTime > 0 {
		form.Set("time", formatFloat(wallTime))
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.handleQuery(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rr.Code)
	}
	var resp promResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func formatFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(
		strings.Replace(fmt.Sprintf("%.6f", f), ",", ".", 1),
		"0"), ".")
}

// ---------------------------------------------------------------------------
// store.queryAt
// ---------------------------------------------------------------------------

func TestQueryAt_BeforeFirstSample(t *testing.T) {
	s := newTestStore(100, []struct {
		ts              float64
		variantName     string
		targetContainer string
		acceleratorType string
		capacity        string
		value           string
	}{
		{ts: 100, variantName: "app", value: "1"},
		{ts: 130, variantName: "app", value: "2"},
	})
	key := seriesKey{variantName: "app"}
	_, ok := s.queryAt(key, 99)
	if ok {
		t.Fatal("expected no result before first sample")
	}
}

func TestQueryAt_ExactTimestamp(t *testing.T) {
	s := newTestStore(100, []struct {
		ts              float64
		variantName     string
		targetContainer string
		acceleratorType string
		capacity        string
		value           string
	}{
		{ts: 100, variantName: "app", value: "1"},
		{ts: 130, variantName: "app", value: "2"},
	})
	key := seriesKey{variantName: "app"}
	sp, ok := s.queryAt(key, 100)
	if !ok {
		t.Fatal("expected a result")
	}
	if sp.value != "1" {
		t.Fatalf("want value 1, got %s", sp.value)
	}
}

func TestQueryAt_BetweenSamples(t *testing.T) {
	s := newTestStore(100, []struct {
		ts              float64
		variantName     string
		targetContainer string
		acceleratorType string
		capacity        string
		value           string
	}{
		{ts: 100, variantName: "app", value: "1"},
		{ts: 130, variantName: "app", value: "2"},
		{ts: 160, variantName: "app", value: "3"},
	})
	key := seriesKey{variantName: "app"}
	sp, ok := s.queryAt(key, 145) // between 130 and 160
	if !ok {
		t.Fatal("expected a result")
	}
	if sp.value != "2" {
		t.Fatalf("want value 2, got %s", sp.value)
	}
}

func TestQueryAt_AfterLastSample(t *testing.T) {
	s := newTestStore(100, []struct {
		ts              float64
		variantName     string
		targetContainer string
		acceleratorType string
		capacity        string
		value           string
	}{
		{ts: 100, variantName: "app", value: "1"},
		{ts: 130, variantName: "app", value: "2"},
	})
	key := seriesKey{variantName: "app"}
	sp, ok := s.queryAt(key, 9999)
	if !ok {
		t.Fatal("expected a result")
	}
	if sp.value != "2" {
		t.Fatalf("want last value 2, got %s", sp.value)
	}
}

func TestQueryAt_UnknownKey(t *testing.T) {
	s := newTestStore(100, nil)
	_, ok := s.queryAt(seriesKey{variantName: "missing"}, 100)
	if ok {
		t.Fatal("expected no result for unknown key")
	}
}

// ---------------------------------------------------------------------------
// server time translation
// ---------------------------------------------------------------------------

func TestTimeTranslation_RoundTrip(t *testing.T) {
	s := newTestStore(1000, nil)
	srv := newTestServer(s, 5000)

	// toFileTime then toWallTime must round-trip
	for _, wall := range []float64{5000, 5030, 5060, 4999} {
		ft := srv.toFileTime(wall)
		wt := srv.toWallTime(ft)
		if wt != wall {
			t.Errorf("round-trip failed for wall=%.1f: got %.1f", wall, wt)
		}
	}
}

func TestTimeTranslation_AtStart(t *testing.T) {
	// At server start wall==startTime, fileTime must equal originTS
	s := newTestStore(1700000000, nil)
	srv := newTestServer(s, 9000000)

	ft := srv.toFileTime(9000000)
	if ft != 1700000000 {
		t.Fatalf("want fileTime=1700000000 at start, got %.0f", ft)
	}
}

func TestTimeTranslation_Offset30s(t *testing.T) {
	// 30s after start wall==startTime+30, fileTime must be originTS+30
	s := newTestStore(1700000000, nil)
	srv := newTestServer(s, 9000000)

	ft := srv.toFileTime(9000030)
	if ft != 1700000030 {
		t.Fatalf("want fileTime=1700000030, got %.0f", ft)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler — replica queries
// ---------------------------------------------------------------------------

// sharedStore is reused across handler tests.
// originTS=1000, samples at +0s, +30s, +60s, +90s.
func buildSampleStore() (*store, float64) {
	origin := 1000.0
	entries := []struct {
		ts              float64
		variantName     string
		targetContainer string
		acceleratorType string
		capacity        string
		value           string
	}{
		// replica series
		{ts: 1000, variantName: "test-app", value: "2"},
		{ts: 1030, variantName: "test-app", value: "3"},
		{ts: 1060, variantName: "test-app", value: "4"},
		{ts: 1090, variantName: "test-app", value: "3"},
		// capacity series
		{ts: 1000, variantName: "test-app", targetContainer: "vllm-container", acceleratorType: "gpu.example.com", capacity: "compute", value: "40"},
		{ts: 1030, variantName: "test-app", targetContainer: "vllm-container", acceleratorType: "gpu.example.com", capacity: "compute", value: "60"},
		{ts: 1060, variantName: "test-app", targetContainer: "vllm-container", acceleratorType: "gpu.example.com", capacity: "compute", value: "75"},
		{ts: 1090, variantName: "test-app", targetContainer: "vllm-container", acceleratorType: "gpu.example.com", capacity: "compute", value: "60"},
		{ts: 1000, variantName: "test-app", targetContainer: "vllm-container", acceleratorType: "gpu.example.com", capacity: "memory", value: "4294967296"},
		{ts: 1030, variantName: "test-app", targetContainer: "vllm-container", acceleratorType: "gpu.example.com", capacity: "memory", value: "8589934592"},
	}
	return newTestStore(origin, entries), origin
}

func TestHandleQuery_ReplicaAtStart(t *testing.T) {
	s, origin := buildSampleStore()
	startWall := 5000.0
	srv := newTestServer(s, startWall)

	// wall==startWall → fileTime==origin → first sample (value=2)
	resp := queryHTTP(t, srv, `desired_replica{variant_name="test-app"}`, startWall)
	if resp.Status != "success" {
		t.Fatalf("want success, got %s", resp.Status)
	}
	if len(resp.Data.Result) != 1 {
		t.Fatalf("want 1 result, got %d", len(resp.Data.Result))
	}
	if resp.Data.Result[0].Value[1] != "2" {
		t.Fatalf("want value 2, got %v", resp.Data.Result[0].Value[1])
	}
	// Returned wall timestamp must equal startWall + (fileTS - originTS)
	wantWallTS := startWall + (origin - origin) // = startWall
	if resp.Data.Result[0].Value[0] != wantWallTS {
		t.Fatalf("want wallTS=%.0f, got %v", wantWallTS, resp.Data.Result[0].Value[0])
	}
}

func TestHandleQuery_ReplicaProgresses(t *testing.T) {
	s, _ := buildSampleStore()
	startWall := 5000.0
	srv := newTestServer(s, startWall)

	cases := []struct {
		offsetWall float64
		wantValue  string
	}{
		{0, "2"},
		{30, "3"},
		{60, "4"},
		{90, "3"},
		{120, "3"}, // past last sample, still returns last
	}
	for _, tc := range cases {
		resp := queryHTTP(t, srv, `desired_replica{variant_name="test-app"}`, startWall+tc.offsetWall)
		if len(resp.Data.Result) == 0 {
			t.Errorf("offset +%.0fs: no results", tc.offsetWall)
			continue
		}
		got := resp.Data.Result[0].Value[1]
		if got != tc.wantValue {
			t.Errorf("offset +%.0fs: want %s, got %v", tc.offsetWall, tc.wantValue, got)
		}
	}
}

func TestHandleQuery_ReplicaBeforeOrigin(t *testing.T) {
	s, _ := buildSampleStore()
	startWall := 5000.0
	srv := newTestServer(s, startWall)

	// wall < startWall → fileTime < originTS → no samples yet
	resp := queryHTTP(t, srv, `desired_replica{variant_name="test-app"}`, startWall-1)
	if len(resp.Data.Result) != 0 {
		t.Fatalf("want empty result before origin, got %d results", len(resp.Data.Result))
	}
}

func TestHandleQuery_CapacityAtOffset30s(t *testing.T) {
	s, _ := buildSampleStore()
	startWall := 5000.0
	srv := newTestServer(s, startWall)

	resp := queryHTTP(t, srv,
		`wva_desired_capacity_per_device{variant_name="test-app",target_container="vllm-container",accelerator_type="gpu.example.com",capacity="compute"}`,
		startWall+30)
	if len(resp.Data.Result) != 1 {
		t.Fatalf("want 1 result, got %d", len(resp.Data.Result))
	}
	if resp.Data.Result[0].Value[1] != "60" {
		t.Fatalf("want value 60, got %v", resp.Data.Result[0].Value[1])
	}
}

func TestHandleQuery_CapacityWildcard(t *testing.T) {
	// Query without capacity filter should return all capacity series for the container
	s, _ := buildSampleStore()
	startWall := 5000.0
	srv := newTestServer(s, startWall)

	resp := queryHTTP(t, srv,
		`wva_desired_capacity_per_device{variant_name="test-app",target_container="vllm-container"}`,
		startWall+30)
	if len(resp.Data.Result) != 2 { // compute + memory
		t.Fatalf("want 2 results (all capacity series), got %d", len(resp.Data.Result))
	}
}

func TestHandleQuery_UnknownMetric(t *testing.T) {
	s, _ := buildSampleStore()
	srv := newTestServer(s, 5000)

	resp := queryHTTP(t, srv, `unknown_metric{variant_name="test-app"}`, 5000)
	if resp.Status != "success" {
		t.Fatalf("want success status even for unknown metric, got %s", resp.Status)
	}
	if len(resp.Data.Result) != 0 {
		t.Fatalf("want empty result for unknown metric, got %d", len(resp.Data.Result))
	}
}

func TestHandleQuery_PostMethod(t *testing.T) {
	s, _ := buildSampleStore()
	startWall := 5000.0
	srv := newTestServer(s, startWall)

	resp := queryHTTPPost(t, srv, `desired_replica{variant_name="test-app"}`, startWall)
	if len(resp.Data.Result) != 1 || resp.Data.Result[0].Value[1] != "2" {
		t.Fatalf("POST: unexpected result %+v", resp.Data.Result)
	}
}

// ---------------------------------------------------------------------------
// extractLabelValue
// ---------------------------------------------------------------------------

func TestExtractLabelValue(t *testing.T) {
	cases := []struct {
		query string
		label string
		want  string
	}{
		{`desired_replica{variant_name="test-app"}`, "variant_name", "test-app"},
		{`wva_desired_capacity_per_device{variant_name="x",target_container="vllm-container",accelerator_type="gpu.example.com",capacity="compute"}`, "target_container", "vllm-container"},
		{`wva_desired_capacity_per_device{variant_name="x",target_container="vllm-container",accelerator_type="gpu.example.com",capacity="compute"}`, "accelerator_type", "gpu.example.com"},
		{`wva_desired_capacity_per_device{variant_name="x",target_container="vllm-container",accelerator_type="gpu.example.com",capacity="compute"}`, "capacity", "compute"},
		{`desired_replica{variant_name="test-app"}`, "target_container", ""},
		{`no_labels`, "variant_name", ""},
	}
	for _, tc := range cases {
		got := extractLabelValue(tc.query, tc.label)
		if got != tc.want {
			t.Errorf("extractLabelValue(%q, %q) = %q, want %q", tc.query, tc.label, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// metricName
// ---------------------------------------------------------------------------

func TestMetricName(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{`desired_replica{variant_name="x"}`, "desired_replica"},
		{`wva_desired_capacity_per_device{a="b"}`, "wva_desired_capacity_per_device"},
		{`bare_name`, "bare_name"},
		{`func(arg)`, "func"},
	}
	for _, tc := range cases {
		got := metricName(tc.query)
		if got != tc.want {
			t.Errorf("metricName(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// Package main implements a fake Prometheus HTTP server that replays metrics
// from a timestamped CSV file. It supports the /api/v1/query endpoint, allowing
// the prometheus-vpa-recommender (or any Prometheus client) to query
// desired_replica and desired_capacity metrics as if they were live.
//
// CSV format (one row per data point):
//
//	timestamp, variant_name, target_container, accelerator_type, capacity, value
//
// When accelerator_type and capacity are both empty the row is treated as a
// desired_replica sample.
// When accelerator_type or capacity is non-empty the row is treated as a
// desired_capacity sample.
//
// Replay semantics: timestamps in the CSV are treated as relative offsets from
// the earliest timestamp found in the file (the "origin"). When the server
// starts, that origin is pinned to the current wall-clock time. Every incoming
// query is then mapped onto the file timeline by:
//
//	fileTime = originTS + (wallNow - serverStartTime)
//
// This means t=0 in the file corresponds to the moment the server starts, and
// the series plays forward in real time at 1:1 speed.
// The timestamp returned in each sample is translated back to wall-clock time
// so Prometheus clients see sensible absolute timestamps.
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

// seriesKey uniquely identifies a time-series.
type seriesKey struct {
	variantName     string
	targetContainer string
	acceleratorType string // empty → desired_replica series
	capacity        string // empty → desired_replica series
}

// sample is a single (timestamp, value) observation.
type sample struct {
	ts    float64 // Unix epoch seconds (may be fractional)
	value string
}

// store holds all loaded time-series indexed by their key.
type store struct {
	// series maps a seriesKey to a sorted (ascending by ts) slice of samples.
	series map[seriesKey][]sample
	// originTS is the minimum timestamp across all series; used as the replay
	// origin so that t=originTS in the file maps to the server start time.
	originTS float64
}

// isCapacity returns true when the key represents a capacity (not replica) series.
func (k seriesKey) isCapacity() bool {
	return k.acceleratorType != "" || k.capacity != ""
}

// ---------------------------------------------------------------------------
// CSV loading
// ---------------------------------------------------------------------------

func loadCSV(path string) (*store, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	s := &store{series: make(map[seriesKey][]sample)}

	r := csv.NewReader(bufio.NewReader(f))
	r.TrimLeadingSpace = true
	r.Comment = '#'

	lineNo := 0
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv line %d: %w", lineNo, err)
		}
		lineNo++

		if len(record) < 6 {
			log.Printf("WARN: skipping short CSV row %d: %v", lineNo, record)
			continue
		}

		// Parse fields
		tsStr := strings.TrimSpace(record[0])
		variantName := strings.TrimSpace(record[1])
		targetContainer := strings.TrimSpace(record[2])
		acceleratorType := strings.TrimSpace(record[3])
		capacity := strings.TrimSpace(record[4])
		valueStr := strings.TrimSpace(record[5])

		ts, err := strconv.ParseFloat(tsStr, 64)
		if err != nil {
			log.Printf("WARN: skipping CSV row %d – bad timestamp %q: %v", lineNo, tsStr, err)
			continue
		}

		key := seriesKey{
			variantName:     variantName,
			targetContainer: targetContainer,
			acceleratorType: acceleratorType,
			capacity:        capacity,
		}
		s.series[key] = append(s.series[key], sample{ts: ts, value: valueStr})
	}

	// Sort each series by ascending timestamp for binary search during queries.
	// Also find the global minimum timestamp (replay origin).
	originTS := math.MaxFloat64
	for k := range s.series {
		sort.Slice(s.series[k], func(i, j int) bool {
			return s.series[k][i].ts < s.series[k][j].ts
		})
		if len(s.series[k]) > 0 && s.series[k][0].ts < originTS {
			originTS = s.series[k][0].ts
		}
	}
	if originTS == math.MaxFloat64 {
		originTS = 0
	}
	s.originTS = originTS

	log.Printf("INFO: loaded %d unique time-series from %s (originTS=%.0f)", len(s.series), path, originTS)
	return s, nil
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

// queryAt returns the most recent sample with ts ≤ queryTime for the given key,
// or (nil, false) if none exists.
func (s *store) queryAt(key seriesKey, queryTime float64) (sample, bool) {
	samples, ok := s.series[key]
	if !ok || len(samples) == 0 {
		return sample{}, false
	}

	// Binary search for the last sample whose ts ≤ queryTime.
	idx := sort.Search(len(samples), func(i int) bool {
		return samples[i].ts > queryTime
	}) - 1

	if idx < 0 {
		return sample{}, false
	}
	return samples[idx], true
}

// ---------------------------------------------------------------------------
// PromQL label-matcher extraction (minimal, regex-free)
// ---------------------------------------------------------------------------

// extractLabelValue extracts the value of a label from a PromQL instant query
// string using naive string matching. Supports label="value" syntax only.
func extractLabelValue(query, label string) string {
	needle := label + `="`
	idx := strings.Index(query, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := strings.Index(query[start:], `"`)
	if end < 0 {
		return ""
	}
	return query[start : start+end]
}

// metricName returns the bare metric name at the start of a PromQL expression.
func metricName(query string) string {
	for i, ch := range query {
		if ch == '{' || ch == '(' || ch == ' ' {
			return query[:i]
		}
	}
	return query
}

// ---------------------------------------------------------------------------
// Prometheus JSON response types
// ---------------------------------------------------------------------------

// promResponse mirrors the Prometheus HTTP API JSON envelope.
type promResponse struct {
	Status string   `json:"status"`
	Data   promData `json:"data"`
}

type promData struct {
	ResultType string       `json:"resultType"`
	Result     []promSample `json:"result"`
}

type promSample struct {
	Metric map[string]string `json:"metric"`
	Value  [2]interface{}    `json:"value"` // [timestamp_float, value_string]
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

type server struct {
	store              *store
	replicaMetricName  string
	capacityMetricName string
	// startTime is the wall-clock time when the server started, used to map
	// incoming query times onto the file's relative timeline.
	startTime float64
}

// toFileTime converts a wall-clock Unix timestamp (seconds) to the equivalent
// position on the file's timeline.
//
//	fileTime = originTS + (wallTime - startTime)
func (srv *server) toFileTime(wallTime float64) float64 {
	return srv.store.originTS + (wallTime - srv.startTime)
}

// toWallTime is the inverse: converts a file timestamp back to wall-clock time.
func (srv *server) toWallTime(fileTime float64) float64 {
	return srv.startTime + (fileTime - srv.store.originTS)
}

func (srv *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	// Support both GET and POST (Prometheus clients use both).
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	query := r.FormValue("query")
	timeParam := r.FormValue("time")

	// Resolve query timestamp (wall-clock), then translate to file timeline.
	var wallTime float64
	if timeParam != "" {
		t, err := strconv.ParseFloat(timeParam, 64)
		if err != nil {
			http.Error(w, "bad time parameter", http.StatusBadRequest)
			return
		}
		wallTime = t
	} else {
		wallTime = float64(time.Now().UnixNano()) / 1e9
	}
	queryTime := srv.toFileTime(wallTime)

	log.Printf("DEBUG: query=%q wall=%.3f fileTime=%.3f", query, wallTime, queryTime)

	name := metricName(query)

	var results []promSample

	switch {
	case name == srv.replicaMetricName || strings.Contains(query, srv.replicaMetricName):
		variantName := extractLabelValue(query, "variant_name")
		results = srv.replicaResults(variantName, queryTime)

	case name == srv.capacityMetricName || strings.Contains(query, srv.capacityMetricName):
		variantName := extractLabelValue(query, "variant_name")
		targetContainer := extractLabelValue(query, "target_container")
		acceleratorType := extractLabelValue(query, "accelerator_type")
		capacity := extractLabelValue(query, "capacity")
		results = srv.capacityResults(variantName, targetContainer, acceleratorType, capacity, queryTime)

	default:
		log.Printf("WARN: unrecognised query %q", query)
	}

	if results == nil {
		results = []promSample{} // encode as [] not null
	}

	resp := promResponse{
		Status: "success",
		Data: promData{
			ResultType: "vector",
			Result:     results,
		},
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("ERROR: encode response: %v", err)
	}
}

// replicaResults resolves desired_replica samples.
// If variantName is empty every variant in the store is included.
func (srv *server) replicaResults(variantName string, queryTime float64) []promSample {
	var out []promSample
	for key := range srv.store.series {
		if key.isCapacity() {
			continue // skip capacity series
		}
		if variantName != "" && key.variantName != variantName {
			continue
		}
		sp, ok := srv.store.queryAt(key, queryTime)
		if !ok {
			continue
		}
		out = append(out, promSample{
			Metric: map[string]string{
				"__name__":     srv.replicaMetricName,
				"variant_name": key.variantName,
			},
			// Translate the file timestamp back to wall-clock time.
			Value: [2]interface{}{srv.toWallTime(sp.ts), sp.value},
		})
	}
	return out
}

// capacityResults resolves desired_capacity samples.
// Empty label values act as wildcards.
func (srv *server) capacityResults(variantName, targetContainer, acceleratorType, capacity string, queryTime float64) []promSample {
	var out []promSample
	for key := range srv.store.series {
		if !key.isCapacity() {
			continue // skip replica series
		}
		if variantName != "" && key.variantName != variantName {
			continue
		}
		if targetContainer != "" && key.targetContainer != targetContainer {
			continue
		}
		if acceleratorType != "" && key.acceleratorType != acceleratorType {
			continue
		}
		if capacity != "" && key.capacity != capacity {
			continue
		}
		sp, ok := srv.store.queryAt(key, queryTime)
		if !ok {
			continue
		}
		out = append(out, promSample{
			Metric: map[string]string{
				"__name__":         srv.capacityMetricName,
				"variant_name":     key.variantName,
				"target_container": key.targetContainer,
				"accelerator_type": key.acceleratorType,
				"capacity":         key.capacity,
			},
			// Translate the file timestamp back to wall-clock time.
			Value: [2]interface{}{srv.toWallTime(sp.ts), sp.value},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	csvPath := flag.String("metrics-file", "metrics.csv", "Path to the timestamped metrics CSV file")
	addr := flag.String("addr", ":9090", "Listen address")
	replicaMetric := flag.String("replica-metric", "desired_replica", "Metric name for replica counts")
	capacityMetric := flag.String("capacity-metric", "desired_capacity", "Metric name for capacity values")
	flag.Parse()

	s, err := loadCSV(*csvPath)
	if err != nil {
		log.Fatalf("ERROR: %v", err)
	}

	startTime := float64(time.Now().UnixNano()) / 1e9
	log.Printf("INFO: replay origin pinned to wall-clock %.0f (file originTS=%.0f)", startTime, s.originTS)

	srv := &server{
		store:              s,
		replicaMetricName:  *replicaMetric,
		capacityMetricName: *capacityMetric,
		startTime:          startTime,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", srv.handleQuery)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "OK")
	})

	log.Printf("INFO: metric-replayer listening on %s (replica-metric=%s, capacity-metric=%s)",
		*addr, *replicaMetric, *capacityMetric)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("ERROR: %v", err)
	}
}

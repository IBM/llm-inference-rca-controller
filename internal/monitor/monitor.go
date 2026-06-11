/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	ctrl "sigs.k8s.io/controller-runtime"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// Monitor handles Prometheus metrics monitoring
type Monitor struct {
	config  autoscalingv1alpha1.Monitoring
	queries map[string]string
	promAPI v1.API
	logger  logr.Logger
}

// NewMonitor creates a new Monitor instance
func NewMonitor(config autoscalingv1alpha1.Monitoring) *Monitor {
	// Create Prometheus API client
	client, err := api.NewClient(api.Config{
		Address: config.Endpoint,
	})

	var promAPI v1.API
	if err == nil {
		promAPI = v1.NewAPI(client)
	}

	logger := ctrl.Log.WithName("monitor")

	return &Monitor{
		config:  config,
		promAPI: promAPI,
		queries: getQueriesWithDefaults(config),
		logger:  logger,
	}
}

// ValidateMetrics checks if all required metrics are available by executing queries
func (m *Monitor) ValidateMetrics() ([]autoscalingv1alpha1.MetricHealth, error) {
	if m.promAPI == nil {
		return nil, fmt.Errorf("prometheus API client not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	metricHealths := make([]autoscalingv1alpha1.MetricHealth, 0, len(m.queries))

	for name, queryTemplate := range m.queries {
		// Format query with empty service filter for validation
		query := fmt.Sprintf(queryTemplate, "")

		// Try to execute the query to validate it
		_, warnings, err := m.promAPI.Query(ctx, query, time.Now())

		health := autoscalingv1alpha1.MetricHealth{
			Name:    name,
			Query:   query,
			Healthy: err == nil,
		}

		if err != nil {
			health.Message = fmt.Sprintf("Query failed: %v", err)
		} else if len(warnings) > 0 {
			health.Message = fmt.Sprintf("Query succeeded with warnings: %v", warnings)
		} else {
			health.Message = "Query succeeded"
		}

		metricHealths = append(metricHealths, health)
	}

	return metricHealths, nil
}

// isEmptyResult checks if a Prometheus query result is empty
func isEmptyResult(result model.Value) bool {
	switch v := result.(type) {
	case model.Vector:
		return len(v) == 0
	case *model.Scalar:
		return false // Scalar always has a value
	case model.Matrix:
		return len(v) == 0
	}
	return true
}

// CollectMetrics fetches and returns the current metric values from Prometheus using PromQL queries
// If serviceName is provided, it filters metrics by {service="$serviceName"}
// lookbackSeconds specifies how many seconds to look back from now (0 means instant query at current time)
func (m *Monitor) CollectMetrics(serviceName string, lookbackSeconds int) (map[string]string, error) {
	if m.promAPI == nil {
		return nil, fmt.Errorf("prometheus API client not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	metrics := make(map[string]string)

	// Prepare service filter
	// For queries that already have label selectors (like RPSQuery with finish_reason),
	// we use comma-separated format. For others, we use full bracket format.
	serviceFilter := ""
	serviceFilterWithComma := ""
	if serviceName != "" {
		serviceFilter = fmt.Sprintf(`{service="%s"}`, serviceName)
		serviceFilterWithComma = fmt.Sprintf(`,service="%s"`, serviceName)
	}

	now := time.Now()

	for name, queryTemplate := range m.queries {
		// Apply service filter to query template
		// RPSQuery uses comma format because it already has {finish_reason="length"}
		// All other queries use bracket format
		filter := serviceFilter
		if name == "RPSQuery" {
			filter = serviceFilterWithComma
		}
		query := fmt.Sprintf(queryTemplate, filter)

		var result model.Value
		var warnings v1.Warnings
		var err error

		if lookbackSeconds > 0 {
			// Query range from (now - lookbackSeconds) to now
			start := now.Add(-time.Duration(lookbackSeconds) * time.Second)
			r := v1.Range{
				Start: start,
				End:   now,
				Step:  time.Duration(lookbackSeconds) * time.Second, // Single step to get aggregated result
			}
			result, warnings, err = m.promAPI.QueryRange(ctx, query, r)
		} else {
			// Instant query at current time
			result, warnings, err = m.promAPI.Query(ctx, query, now)
		}

		if err != nil {
			metrics[name] = fmt.Sprintf("error: %v", err)
			continue
		}

		if len(warnings) > 0 {
			// Log warnings but continue - these should always be visible
			m.logger.Info("Query returned warnings", "query", name, "warnings", warnings)
		}

		// Extract value from result
		value := extractValueFromResult(result)
		metrics[name] = value
	}

	return metrics, nil
}

// Query executes a PromQL query and returns the result
func (m *Monitor) Query(ctx context.Context, query string) (model.Value, error) {
	if m.promAPI == nil {
		return nil, fmt.Errorf("prometheus API client not initialized")
	}

	result, warnings, err := m.promAPI.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if len(warnings) > 0 {
		m.logger.Info("Query returned warnings", "warnings", warnings)
	}

	return result, nil
}

// extractValueFromResult extracts a string value from Prometheus query result
// For Vector: returns the maximum value across all samples
// For Matrix: returns the maximum value across all time series
func extractValueFromResult(result model.Value) string {
	switch v := result.(type) {
	case model.Vector:
		if len(v) == 0 {
			return ""
		}
		// Find max value across all samples
		maxVal := float64(v[0].Value)
		for _, sample := range v {
			val := float64(sample.Value)
			if val > maxVal {
				maxVal = val
			}
		}
		return fmt.Sprintf("%f", maxVal)

	case *model.Scalar:
		return v.Value.String()

	case model.Matrix:
		if len(v) == 0 {
			return ""
		}
		// Find the maximum value across all time series
		var overallMax float64
		firstValue := true
		for _, series := range v {
			if len(series.Values) == 0 {
				continue
			}
			// Find max value in this series
			for _, pair := range series.Values {
				val := float64(pair.Value)
				if firstValue {
					overallMax = val
					firstValue = false
				} else if val > overallMax {
					overallMax = val
				}
			}
		}

		if firstValue {
			return ""
		}

		return fmt.Sprintf("%f", overallMax)
	}
	return ""
}

// getQueriesWithDefaults returns the queries map with defaults applied
// Queries include %s placeholder for service filter
func getQueriesWithDefaults(config autoscalingv1alpha1.Monitoring) map[string]string {
	queries := config.CustomQueries
	if queries == nil {
		queries = &autoscalingv1alpha1.CustomQueries{}
	}

	// Get rate range in seconds, default to 60
	rateRange := 60
	if config.RateRangeSeconds != nil {
		rateRange = int(*config.RateRangeSeconds)
	}

	// Get latency percentile, default to 0.95
	percentile := 0.95
	if config.LatencyPercentile != nil {
		percentile = *config.LatencyPercentile
	}

	// For counter metrics (RPS, tokens), wrap with rate() function
	// Include %s placeholder for service filter
	rpsQuery := getQueryOrDefault(queries.RPSQuery, "vllm:request_success_total")
	promptTokenQuery := getQueryOrDefault(queries.PromptTokenQuery, "vllm:prompt_tokens_total")
	generationTokenQuery := getQueryOrDefault(queries.GenerationTokenQuery, "vllm:generation_tokens_total")

	// Wrap counter metrics with rate() if they don't already have it
	if !strings.Contains(rpsQuery, "rate(") {
		// Add finish_reason="length" filter for RPS to only count successful completions
		// The %s placeholder will be replaced with service filter like {service="name"}
		// We need to merge it with finish_reason filter
		rpsQuery = fmt.Sprintf("rate(%s{finish_reason=\"length\"%%s}[%ds])", rpsQuery, rateRange)
	}
	if !strings.Contains(promptTokenQuery, "rate(") {
		promptTokenQuery = fmt.Sprintf("rate(%s%%s[%ds])", promptTokenQuery, rateRange)
	}
	if !strings.Contains(generationTokenQuery, "rate(") {
		generationTokenQuery = fmt.Sprintf("rate(%s%%s[%ds])", generationTokenQuery, rateRange)
	}

	// For histogram-based latency metrics, wrap with histogram_quantile() if not already present
	interTokenLatencyQuery := getQueryOrDefault(queries.InterTokenLatencyQuery, "vllm:inter_token_latency_seconds_bucket")
	if !strings.Contains(interTokenLatencyQuery, "histogram_quantile(") {
		// Append _bucket suffix if not present and wrap with histogram_quantile()
		bucketQuery := interTokenLatencyQuery
		if !strings.HasSuffix(bucketQuery, "_bucket") {
			bucketQuery = bucketQuery + "_bucket"
		}
		interTokenLatencyQuery = fmt.Sprintf("histogram_quantile(%g, rate(%s%%s[%ds]))", percentile, bucketQuery, rateRange)
	}

	timeToFirstTokenLatencyQuery := getQueryOrDefault(queries.TimeToFirstTokenLatencyQuery, "vllm:time_to_first_token_seconds_bucket")
	if !strings.Contains(timeToFirstTokenLatencyQuery, "histogram_quantile(") {
		// Append _bucket suffix if not present and wrap with histogram_quantile()
		bucketQuery := timeToFirstTokenLatencyQuery
		if !strings.HasSuffix(bucketQuery, "_bucket") {
			bucketQuery = bucketQuery + "_bucket"
		}
		timeToFirstTokenLatencyQuery = fmt.Sprintf("histogram_quantile(%g, rate(%s%%s[%ds]))", percentile, bucketQuery, rateRange)
	}

	endToEndLatencyBucketQuery := getQueryOrDefault(queries.EndToEndLatencyBucketQuery, "vllm:e2e_request_latency_seconds_bucket")
	if !strings.Contains(endToEndLatencyBucketQuery, "histogram_quantile(") {
		// Append _bucket suffix if not present and wrap with histogram_quantile()
		bucketQuery := endToEndLatencyBucketQuery
		if !strings.HasSuffix(bucketQuery, "_bucket") {
			bucketQuery = bucketQuery + "_bucket"
		}
		endToEndLatencyBucketQuery = fmt.Sprintf("histogram_quantile(%g, rate(%s%%s[%ds]))", percentile, bucketQuery, rateRange)
	}

	queueTimeQuery := getQueryOrDefault(queries.QueueTimeQuery, "vllm:request_queue_time_seconds_bucket")
	if !strings.Contains(queueTimeQuery, "histogram_quantile(") {
		// Append _bucket suffix if not present and wrap with histogram_quantile()
		bucketQuery := queueTimeQuery
		if !strings.HasSuffix(bucketQuery, "_bucket") {
			bucketQuery = bucketQuery + "_bucket"
		}
		queueTimeQuery = fmt.Sprintf("histogram_quantile(%g, rate(%s%%s[%ds]))", percentile, bucketQuery, rateRange)
	}

	// Add cache metrics for vLLM prefix caching
	prefixCacheHitsQuery := getQueryOrDefault(queries.PrefixCacheHitsQuery, "vllm:prefix_cache_hits")
	prefixCacheQueriesQuery := getQueryOrDefault(queries.PrefixCacheQueriesQuery, "vllm:prefix_cache_queries")

	// Add prefill tokens metric
	prefillTokensQuery := getQueryOrDefault(queries.PrefillTokensQuery, "vllm:prefill_tokens_total")
	if !strings.Contains(prefillTokensQuery, "rate(") {
		prefillTokensQuery = fmt.Sprintf("rate(%s%%s[%ds])", prefillTokensQuery, rateRange)
	}

	result := map[string]string{
		"RPSQuery":                     rpsQuery,
		"PromptTokenQuery":             promptTokenQuery,
		"GenerationTokenQuery":         generationTokenQuery,
		"InterTokenLatencyQuery":       interTokenLatencyQuery,
		"TimeToFirstTokenLatencyQuery": timeToFirstTokenLatencyQuery,
		"EndToEndLatencyBucketQuery":   endToEndLatencyBucketQuery,
		"QueueTimeQuery":               queueTimeQuery,
		"PrefixCacheHitsQuery":         prefixCacheHitsQuery + "%s",
		"PrefixCacheQueriesQuery":      prefixCacheQueriesQuery + "%s",
		"PrefillTokensQuery":           prefillTokensQuery,
	}

	deviceResourceMetrics := map[string]autoscalingv1alpha1.DeviceResourceMetrics{
		"compute": {
			UsageQuery:      "vllm:gpu_active_thread_percentage",
			AllocationQuery: "vllm:gpu_compute_allocation_percentage",
		},
		"memory": {
			UsageQuery:      "vllm:gpu_memory_usage_bytes",
			AllocationQuery: "vllm:gpu_memory_allocation_bytes",
		},
	}

	if queries.DeviceResourceMetrics != nil {
		for resourceType, metrics := range queries.DeviceResourceMetrics {
			deviceResourceMetrics[resourceType] = metrics
		}
	}

	for resourceType, metrics := range deviceResourceMetrics {
		// Add usage query if provided
		if metrics.UsageQuery != "" {
			queryKey := fmt.Sprintf("DeviceResource_%s_Usage", resourceType)
			// Use max() aggregation to get the maximum usage across all instances
			result[queryKey] = fmt.Sprintf("max(%s%%s)", metrics.UsageQuery)
		}

		// Add allocation query if provided
		if metrics.AllocationQuery != "" {
			queryKey := fmt.Sprintf("DeviceResource_%s_Allocation", resourceType)
			// Allocation is a static limit, use as-is without aggregation
			result[queryKey] = metrics.AllocationQuery + "%s"
		}
	}

	// Add allocation count query (count of allocation requests)
	// This is used to get the number of GPUs currently allocated
	if deviceResourceMetrics["compute"].AllocationQuery != "" {
		result["AllocationCountQuery"] = fmt.Sprintf("count(%s%%s) by (instance)", deviceResourceMetrics["compute"].AllocationQuery)
	}

	return result
}

// getQueryOrDefault returns the query if not empty, otherwise returns the default
func getQueryOrDefault(query, defaultQuery string) string {
	if query == "" {
		return defaultQuery
	}
	return query
}

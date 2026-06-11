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

package scaler

import (
	"fmt"
	"strconv"
	"strings"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// getArrivalRate extracts arrival rate from metrics
func (s *ResourceScaler) getArrivalRate(metrics map[string]string) float64 {
	// Try to get from RequestRate metric
	if rateStr, ok := metrics["RequestRate"]; ok {
		var rate float64
		if _, err := fmt.Sscanf(rateStr, "%f", &rate); err == nil {
			return rate
		}
	}

	// Try to get from RPSQuery metric (used by tuneArrivalRate)
	if rpsStr, ok := metrics["RPSQuery"]; ok && s.isValidMetric(rpsStr) {
		if rps, err := s.parseFloat(rpsStr); err == nil {
			// Return actual value even if zero - zero traffic means zero arrival rate
			return rps
		}
	}

	// Default to 0 req/s
	return 0.0
}

// getAvgPromptTokens extracts average prompt tokens from metrics
func (s *ResourceScaler) getPromptTokens(metrics map[string]string) int {
	if promptTokenRateStr, ok := metrics["PromptTokenQuery"]; ok && s.isValidMetric(promptTokenRateStr) {
		if promptTokenRate, err := s.parseFloat(promptTokenRateStr); err == nil {
			if promptTokenRate > 0 {
				return int(promptTokenRate)
			}
		}
	}
	return 0 // Default
}

// getAvgOutputTokens extracts average output tokens from metrics
func (s *ResourceScaler) getOutputTokens(metrics map[string]string) int {
	if genTokenRateStr, ok := metrics["GenerationTokenQuery"]; ok && s.isValidMetric(genTokenRateStr) {
		if genTokenRate, err := s.parseFloat(genTokenRateStr); err == nil {
			if genTokenRate > 0 {
				return int(genTokenRate)
			}
		}
	}
	return 0 // Default
}

// getMaxNumSeqs extracts max number of sequences from spec hint
func (s *ResourceScaler) getMaxNumSeqs(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) int {
	if spec.Target.Hint != nil && spec.Target.Hint.MaxNumSeqs > 0 {
		return int(spec.Target.Hint.MaxNumSeqs)
	}
	return 10 // Default
}

// getPrefillTokens extracts prefill tokens from metrics (total tokens processed in prefill phase)
func (s *ResourceScaler) getPrefillTokens(metrics map[string]string) float64 {
	if prefillStr, ok := metrics["PrefillTokensQuery"]; ok && s.isValidMetric(prefillStr) {
		if prefill, err := s.parseFloat(prefillStr); err == nil && prefill > 0 {
			return prefill
		}
	}
	return 0.0
}

// getCacheHitRatio extracts cache hit ratio from metrics
// Calculates ratio as: vllm:prefix_cache_hits / vllm:prefix_cache_queries
func (s *ResourceScaler) getCacheHitRatio(metrics map[string]string) float64 {
	// Try direct cache hit ratio metric first
	if cacheStr, ok := metrics["CacheHitRatioQuery"]; ok && s.isValidMetric(cacheStr) {
		if ratio, err := s.parseFloat(cacheStr); err == nil && ratio >= 0 && ratio <= 1 {
			return ratio
		}
	}

	// Calculate from vLLM prefix cache metrics
	hitsStr, hasHits := metrics["PrefixCacheHitsQuery"]
	queriesStr, hasQueries := metrics["PrefixCacheQueriesQuery"]

	if hasHits && hasQueries && s.isValidMetric(hitsStr) && s.isValidMetric(queriesStr) {
		hits, errHits := s.parseFloat(hitsStr)
		queries, errQueries := s.parseFloat(queriesStr)

		if errHits == nil && errQueries == nil && queries > 0 {
			ratio := hits / queries
			if ratio >= 0 && ratio <= 1 {
				return ratio
			}
		}
	}

	return 0.0 // Default: no cache hits
}

// getComputeThreadUtilization extracts compute thread utilization from metrics
func (s *ResourceScaler) getComputeThreadUtilization(metrics map[string]string) float64 {
	if utilStr, ok := metrics["DeviceResource_compute_Usage"]; ok && s.isValidMetric(utilStr) {
		if util, err := s.parseFloat(utilStr); err == nil && util >= 0 && util <= 100 {
			return util
		}
	}
	return 0.0 // Default: unknown utilization
}

// getMeasuredMemoryUsage extracts measured memory usage from metrics (in GB)
func (s *ResourceScaler) getMeasuredMemoryUsage(metrics map[string]string) float64 {
	if memStr, ok := metrics["DeviceResource_memory_Usage"]; ok && s.isValidMetric(memStr) {
		if mem, err := s.parseFloat(memStr); err == nil && mem > 0 {
			return mem
		}
	}
	return 0.0 // Default: unknown memory usage
}

// getNumGPU gets the current number of GPUs from metrics
func (s *ResourceScaler) getNumGPU(metrics map[string]string) int {
	// Try to get from AllocationCountQuery metric (count of allocation requests)
	if allocCountStr, ok := metrics["AllocationCountQuery"]; ok && s.isValidMetric(allocCountStr) {
		if count, err := s.parseFloat(allocCountStr); err == nil && count > 0 {
			return int(count)
		}
	}
	return 0
}

// isValidMetric checks if a metric value is valid (not empty and not an error)
func (s *ResourceScaler) isValidMetric(value string) bool {
	return value != "" && !strings.HasPrefix(value, "error:")
}

// parseFloat safely parses a string to float64
func (s *ResourceScaler) parseFloat(str string) (float64, error) {
	if str == "" {
		return 0, fmt.Errorf("empty string")
	}

	// Use strconv for more strict parsing
	value, err := strconv.ParseFloat(strings.TrimSpace(str), 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse float from '%s': %w", str, err)
	}

	return value, nil
}

// getInterTokenLatency extracts inter-token latency from metrics (in milliseconds)
func (s *ResourceScaler) getInterTokenLatency(metrics map[string]string) float64 {
	if itlStr, ok := metrics["InterTokenLatencyQuery"]; ok && s.isValidMetric(itlStr) {
		if itl, err := s.parseFloat(itlStr); err == nil && itl > 0 {
			// Convert from seconds to milliseconds
			return itl * 1000.0
		}
	}
	return 0.0 // Default: unknown ITL
}

// getTimeToFirstToken extracts time to first token latency from metrics (in milliseconds)
func (s *ResourceScaler) getTimeToFirstToken(metrics map[string]string) float64 {
	if ttftStr, ok := metrics["TimeToFirstTokenLatencyQuery"]; ok && s.isValidMetric(ttftStr) {
		if ttft, err := s.parseFloat(ttftStr); err == nil && ttft > 0 {
			// Convert from seconds to milliseconds
			return ttft * 1000.0
		}
	}
	return 0.0 // Default: unknown TTFT
}

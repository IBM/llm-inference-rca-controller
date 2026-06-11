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

package scaler_test

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/scaler"
)

var _ = Describe("MetricHandler", func() {
	var s *scaler.ResourceScaler

	BeforeEach(func() {
		s = scaler.NewResourceScaler(nil, logr.Discard())
	})

	DescribeTable("getArrivalRate",
		func(metrics map[string]string, expected float64) {
			rate := scaler.GetArrivalRate(s, metrics)
			Expect(rate).To(Equal(expected))
		},
		Entry("should return value from RequestRate when available",
			map[string]string{"RequestRate": "3.5"}, 3.5),
		Entry("should return value from RPSQuery when available",
			map[string]string{"RPSQuery": "2.5"}, 2.5),
		Entry("should prefer RequestRate over RPSQuery",
			map[string]string{"RequestRate": "3.5", "RPSQuery": "2.5"}, 3.5),
		Entry("should return zero when RPSQuery is explicitly zero",
			map[string]string{"RPSQuery": "0.0"}, 0.0),
		Entry("should default to 0.0 when no metrics available",
			map[string]string{}, 0.0),
		Entry("should return 0.0 for invalid metric values",
			map[string]string{"RPSQuery": "invalid"}, 0.0),
		Entry("should return 0.0 for error metrics",
			map[string]string{"RPSQuery": "error: connection failed"}, 0.0),
	)

	DescribeTable("getPromptTokens",
		func(metrics map[string]string, expected int) {
			tokens := scaler.GetPromptTokens(s, metrics)
			Expect(tokens).To(Equal(expected))
		},
		Entry("should return prompt tokens from metrics",
			map[string]string{"PromptTokenQuery": "512"}, 512),
		Entry("should return 0 when metric is missing",
			map[string]string{}, 0),
		Entry("should return 0 for invalid values",
			map[string]string{"PromptTokenQuery": "invalid"}, 0),
		Entry("should return 0 for negative values",
			map[string]string{"PromptTokenQuery": "-10"}, 0),
	)

	DescribeTable("getOutputTokens",
		func(metrics map[string]string, expected int) {
			tokens := scaler.GetOutputTokens(s, metrics)
			Expect(tokens).To(Equal(expected))
		},
		Entry("should return output tokens from metrics",
			map[string]string{"GenerationTokenQuery": "256"}, 256),
		Entry("should return 0 when metric is missing",
			map[string]string{}, 0),
		Entry("should return 0 for invalid values",
			map[string]string{"GenerationTokenQuery": "not-a-number"}, 0),
	)

	DescribeTable("getMaxNumSeqs",
		func(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, expected int) {
			maxSeqs := scaler.GetMaxNumSeqs(s, spec)
			Expect(maxSeqs).To(Equal(expected))
		},
		Entry("should return MaxNumSeqs from spec hint",
			&autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					Hint: &autoscalingv1alpha1.Hint{MaxNumSeqs: 32},
				},
			}, 32),
		Entry("should return default when hint is nil",
			&autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{Hint: nil},
			}, 10),
		Entry("should return default when MaxNumSeqs is 0",
			&autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					Hint: &autoscalingv1alpha1.Hint{MaxNumSeqs: 0},
				},
			}, 10),
	)

	DescribeTable("getPrefillTokens",
		func(metrics map[string]string, expected float64) {
			tokens := scaler.GetPrefillTokens(s, metrics)
			Expect(tokens).To(Equal(expected))
		},
		Entry("should return prefill tokens from metrics",
			map[string]string{"PrefillTokensQuery": "1024.5"}, 1024.5),
		Entry("should return 0.0 when metric is missing",
			map[string]string{}, 0.0),
		Entry("should return 0.0 for invalid values",
			map[string]string{"PrefillTokensQuery": "invalid"}, 0.0),
		Entry("should return 0.0 for negative values",
			map[string]string{"PrefillTokensQuery": "-100"}, 0.0),
	)

	DescribeTable("getCacheHitRatio",
		func(metrics map[string]string, expected float64) {
			ratio := scaler.GetCacheHitRatio(s, metrics)
			Expect(ratio).To(Equal(expected))
		},
		Entry("should return cache hit ratio from direct metric",
			map[string]string{"CacheHitRatioQuery": "0.75"}, 0.75),
		Entry("should calculate ratio from hits and queries",
			map[string]string{"PrefixCacheHitsQuery": "75", "PrefixCacheQueriesQuery": "100"}, 0.75),
		Entry("should return 0.0 when queries is zero",
			map[string]string{"PrefixCacheHitsQuery": "10", "PrefixCacheQueriesQuery": "0"}, 0.0),
		Entry("should return 0.0 when metrics are missing",
			map[string]string{}, 0.0),
		Entry("should reject ratios greater than 1.0",
			map[string]string{"CacheHitRatioQuery": "1.5"}, 0.0),
		Entry("should reject negative ratios",
			map[string]string{"CacheHitRatioQuery": "-0.5"}, 0.0),
	)

	DescribeTable("getComputeThreadUtilization",
		func(metrics map[string]string, expected float64) {
			util := scaler.GetComputeThreadUtilization(s, metrics)
			Expect(util).To(Equal(expected))
		},
		Entry("should return compute utilization from metrics",
			map[string]string{"DeviceResource_compute_Usage": "85.5"}, 85.5),
		Entry("should return 0.0 when metric is missing",
			map[string]string{}, 0.0),
		Entry("should reject values greater than 100",
			map[string]string{"DeviceResource_compute_Usage": "150"}, 0.0),
		Entry("should reject negative values",
			map[string]string{"DeviceResource_compute_Usage": "-10"}, 0.0),
	)

	DescribeTable("getMeasuredMemoryUsage",
		func(metrics map[string]string, expected float64) {
			memory := scaler.GetMeasuredMemoryUsage(s, metrics)
			Expect(memory).To(Equal(expected))
		},
		Entry("should return memory usage from metrics",
			map[string]string{"DeviceResource_memory_Usage": "64.5"}, 64.5),
		Entry("should return 0.0 when metric is missing",
			map[string]string{}, 0.0),
		Entry("should return 0.0 for negative values",
			map[string]string{"DeviceResource_memory_Usage": "-10"}, 0.0),
	)

	DescribeTable("getNumGPU",
		func(metrics map[string]string, expected int) {
			count := scaler.GetNumGPU(s, metrics)
			Expect(count).To(Equal(expected))
		},
		Entry("should return GPU count from metrics",
			map[string]string{"AllocationCountQuery": "4"}, 4),
		Entry("should return 0 when metric is missing",
			map[string]string{}, 0),
		Entry("should return 0 for negative values",
			map[string]string{"AllocationCountQuery": "-2"}, 0),
	)

	DescribeTable("getInterTokenLatency",
		func(metrics map[string]string, expected float64) {
			itl := scaler.GetInterTokenLatency(s, metrics)
			Expect(itl).To(Equal(expected))
		},
		Entry("should return ITL in milliseconds",
			map[string]string{"InterTokenLatencyQuery": "0.05"}, 50.0),
		Entry("should return 0.0 when metric is missing",
			map[string]string{}, 0.0),
		Entry("should return 0.0 for negative values",
			map[string]string{"InterTokenLatencyQuery": "-0.01"}, 0.0),
	)

	DescribeTable("getTimeToFirstToken",
		func(metrics map[string]string, expected float64) {
			ttft := scaler.GetTimeToFirstToken(s, metrics)
			Expect(ttft).To(Equal(expected))
		},
		Entry("should return TTFT in milliseconds",
			map[string]string{"TimeToFirstTokenLatencyQuery": "1.2"}, 1200.0),
		Entry("should return 0.0 when metric is missing",
			map[string]string{}, 0.0),
		Entry("should return 0.0 for negative values",
			map[string]string{"TimeToFirstTokenLatencyQuery": "-0.5"}, 0.0),
	)

	DescribeTable("isValidMetric",
		func(input string, expected bool) {
			result := scaler.IsValidMetric(s, input)
			Expect(result).To(Equal(expected))
		},
		Entry("should return true for valid numeric string", "123.45", true),
		Entry("should return true for zero", "0", true),
		Entry("should return true for non-numeric value", "some-value", true),
		Entry("should return false for empty string", "", false),
		Entry("should return false for error with space", "error: connection failed", false),
		Entry("should return false for error without space", "error:timeout", false),
	)

	Describe("parseFloat", func() {
		It("should parse valid float strings", func() {
			tests := []struct {
				input    string
				expected float64
			}{
				{"1.5", 1.5},
				{"0.015", 0.015},
				{"100", 100.0},
				{"0", 0.0},
				{"-5.5", -5.5},
				{"  3.14  ", 3.14}, // with whitespace
			}

			for _, tt := range tests {
				value, err := scaler.ParseFloat(s, tt.input)
				Expect(err).NotTo(HaveOccurred(), "input: %s", tt.input)
				Expect(value).To(Equal(tt.expected), "input: %s", tt.input)
			}
		})

		It("should return error for invalid strings", func() {
			invalidInputs := []string{
				"not-a-number",
				"",
				"1.2.3",
				"abc",
				"12.34.56",
			}

			for _, input := range invalidInputs {
				_, err := scaler.ParseFloat(s, input)
				Expect(err).To(HaveOccurred(), "input: %s", input)
			}
		})
	})
})

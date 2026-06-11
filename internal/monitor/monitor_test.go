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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/model"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

var _ = Describe("Monitor", func() {
	Context("When creating a new Monitor", func() {
		It("should initialize with valid endpoint", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
			}

			// Act
			monitor := NewMonitor(config)

			// Assert
			Expect(monitor).NotTo(BeNil())
			Expect(monitor.config.Endpoint).To(Equal(config.Endpoint))
			Expect(monitor.promAPI).NotTo(BeNil())
			Expect(monitor.queries).NotTo(BeNil())
			// Base queries: 7 (RPS, PromptToken, GenerationToken, InterTokenLatency, TimeToFirstToken, EndToEndLatencyBucket, QueueTime)
			// New queries: 3 (PrefillTokens, PrefixCacheHits, PrefixCacheQueries)
			// Device resource queries: 4 (compute usage/allocation, memory usage/allocation)
			// Derived query: 1 (AllocationCount)
			// Total: 15 queries
			Expect(monitor.queries).To(HaveLen(15))
		})

		It("should handle invalid endpoint gracefully", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "://invalid-url",
			}

			// Act
			monitor := NewMonitor(config)

			// Assert
			Expect(monitor).NotTo(BeNil())
			Expect(monitor.queries).NotTo(BeNil())
		})

		It("should wrap custom RPS query with rate() and include service filter placeholder", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
				CustomQueries: &autoscalingv1alpha1.CustomQueries{
					RPSQuery: "custom_rps",
				},
			}

			// Act
			monitor := NewMonitor(config)

			// Assert
			Expect(monitor.queries["RPSQuery"]).To(Equal("rate(custom_rps{finish_reason=\"length\"%s}[60s])"))
			Expect(monitor.queries["PromptTokenQuery"]).To(Equal("rate(vllm:prompt_tokens_total%s[60s])"))
		})
	})

	Context("When generating queries with defaults", func() {
		var (
			rateRange60 int32
			rateRange30 int32
		)

		BeforeEach(func() {
			rateRange60 = 60
			rateRange30 = 30
		})

		DescribeTable("query generation with different configurations",
			func(config autoscalingv1alpha1.Monitoring, expectedQueries map[string]string) {
				// Act
				queries := getQueriesWithDefaults(config)

				// Assert
				for name, expectedQuery := range expectedQueries {
					Expect(queries[name]).To(Equal(expectedQuery), "Query %s should match", name)
				}
			},
			Entry("nil queries uses all defaults with rate() and histogram_quantile()",
				autoscalingv1alpha1.Monitoring{
					Endpoint: "http://localhost:8080",
				},
				map[string]string{
					"RPSQuery":                          "rate(vllm:request_success_total{finish_reason=\"length\"%s}[60s])",
					"PromptTokenQuery":                  "rate(vllm:prompt_tokens_total%s[60s])",
					"GenerationTokenQuery":              "rate(vllm:generation_tokens_total%s[60s])",
					"InterTokenLatencyQuery":            "histogram_quantile(0.95, rate(vllm:inter_token_latency_seconds_bucket%s[60s]))",
					"TimeToFirstTokenLatencyQuery":      "histogram_quantile(0.95, rate(vllm:time_to_first_token_seconds_bucket%s[60s]))",
					"EndToEndLatencyBucketQuery":        "histogram_quantile(0.95, rate(vllm:e2e_request_latency_seconds_bucket%s[60s]))",
					"DeviceResource_compute_Usage":      "max(vllm:gpu_active_thread_percentage%s)",
					"DeviceResource_compute_Allocation": "vllm:gpu_compute_allocation_percentage%s",
					"DeviceResource_memory_Usage":       "max(vllm:gpu_memory_usage_bytes%s)",
					"DeviceResource_memory_Allocation":  "vllm:gpu_memory_allocation_bytes%s",
				},
			),
			Entry("custom rate range",
				autoscalingv1alpha1.Monitoring{
					Endpoint:         "http://localhost:8080",
					RateRangeSeconds: &rateRange30,
				},
				map[string]string{
					"RPSQuery":                          "rate(vllm:request_success_total{finish_reason=\"length\"%s}[30s])",
					"PromptTokenQuery":                  "rate(vllm:prompt_tokens_total%s[30s])",
					"GenerationTokenQuery":              "rate(vllm:generation_tokens_total%s[30s])",
					"InterTokenLatencyQuery":            "histogram_quantile(0.95, rate(vllm:inter_token_latency_seconds_bucket%s[30s]))",
					"TimeToFirstTokenLatencyQuery":      "histogram_quantile(0.95, rate(vllm:time_to_first_token_seconds_bucket%s[30s]))",
					"EndToEndLatencyBucketQuery":        "histogram_quantile(0.95, rate(vllm:e2e_request_latency_seconds_bucket%s[30s]))",
					"DeviceResource_compute_Usage":      "max(vllm:gpu_active_thread_percentage%s)",
					"DeviceResource_compute_Allocation": "vllm:gpu_compute_allocation_percentage%s",
					"DeviceResource_memory_Usage":       "max(vllm:gpu_memory_usage_bytes%s)",
					"DeviceResource_memory_Allocation":  "vllm:gpu_memory_allocation_bytes%s",
				},
			),
			Entry("custom query with rate already included",
				autoscalingv1alpha1.Monitoring{
					Endpoint:         "http://localhost:8080",
					RateRangeSeconds: &rateRange60,
					CustomQueries: &autoscalingv1alpha1.CustomQueries{
						RPSQuery: "rate(custom_rps[5m])",
					},
				},
				map[string]string{
					"RPSQuery":                          "rate(custom_rps[5m])",
					"PromptTokenQuery":                  "rate(vllm:prompt_tokens_total%s[60s])",
					"GenerationTokenQuery":              "rate(vllm:generation_tokens_total%s[60s])",
					"InterTokenLatencyQuery":            "histogram_quantile(0.95, rate(vllm:inter_token_latency_seconds_bucket%s[60s]))",
					"TimeToFirstTokenLatencyQuery":      "histogram_quantile(0.95, rate(vllm:time_to_first_token_seconds_bucket%s[60s]))",
					"EndToEndLatencyBucketQuery":        "histogram_quantile(0.95, rate(vllm:e2e_request_latency_seconds_bucket%s[60s]))",
					"DeviceResource_compute_Usage":      "max(vllm:gpu_active_thread_percentage%s)",
					"DeviceResource_compute_Allocation": "vllm:gpu_compute_allocation_percentage%s",
					"DeviceResource_memory_Usage":       "max(vllm:gpu_memory_usage_bytes%s)",
					"DeviceResource_memory_Allocation":  "vllm:gpu_memory_allocation_bytes%s",
				},
			),
		)

		It("should use default rate range when not specified", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["RPSQuery"]).To(ContainSubstring("[60s]"))
		})

		It("should use custom rate range when specified", func() {
			// Arrange
			customRange := int32(120)
			config := autoscalingv1alpha1.Monitoring{
				Endpoint:         "http://localhost:8080",
				RateRangeSeconds: &customRange,
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["RPSQuery"]).To(ContainSubstring("[120s]"))
		})
	})

	Context("When initializing queries", func() {
		DescribeTable("query initialization with different custom queries",
			func(customQueries *autoscalingv1alpha1.CustomQueries, checkQuery string, expectedValue string) {
				// Arrange
				config := autoscalingv1alpha1.Monitoring{
					Endpoint:      "http://localhost:8080",
					CustomQueries: customQueries,
				}

				// Act
				monitor := NewMonitor(config)

				// Assert
				Expect(monitor.queries[checkQuery]).To(Equal(expectedValue))
			},
			Entry("nil queries - all defaults with rate",
				nil,
				"RPSQuery",
				"rate(vllm:request_success_total{finish_reason=\"length\"%s}[60s])"),
			Entry("partial custom queries with rate",
				&autoscalingv1alpha1.CustomQueries{
					RPSQuery:         "custom_rps",
					PromptTokenQuery: "custom_prompt",
				},
				"RPSQuery",
				"rate(custom_rps{finish_reason=\"length\"%s}[60s])"),
			Entry("empty string uses default with rate",
				&autoscalingv1alpha1.CustomQueries{
					RPSQuery: "",
				},
				"RPSQuery",
				"rate(vllm:request_success_total{finish_reason=\"length\"%s}[60s])"),
		)
	})

	Context("When collecting metrics", func() {
		It("should return error when promAPI is not initialized", func() {
			// Arrange
			monitor := &Monitor{
				config:  autoscalingv1alpha1.Monitoring{Endpoint: "http://localhost:8080"},
				promAPI: nil,
				queries: map[string]string{},
			}

			// Act
			_, err := monitor.CollectMetrics("", 0)

			// Assert
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus API client not initialized"))
		})
	})

	Context("When validating metrics", func() {
		It("should return error when promAPI is not initialized", func() {
			// Arrange
			monitor := &Monitor{
				config:  autoscalingv1alpha1.Monitoring{Endpoint: "http://localhost:8080"},
				promAPI: nil,
				queries: map[string]string{},
			}

			// Act
			_, err := monitor.ValidateMetrics()

			// Assert
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus API client not initialized"))
		})
	})

	Context("When extracting values from Prometheus results", func() {
		DescribeTable("value extraction from different result types",
			func(result model.Value, expectedValue string) {
				// Act
				value := extractValueFromResult(result)

				// Assert
				Expect(value).To(Equal(expectedValue))
			},
			Entry("vector with single sample",
				model.Vector{
					&model.Sample{
						Metric: model.Metric{},
						Value:  model.SampleValue(42.5),
					},
				},
				"42.500000"),
			Entry("empty vector",
				model.Vector{},
				""),
			Entry("scalar value",
				&model.Scalar{
					Value: model.SampleValue(100.0),
				},
				"100"),
			Entry("matrix with values",
				model.Matrix{
					&model.SampleStream{
						Metric: model.Metric{},
						Values: []model.SamplePair{
							{Value: model.SampleValue(10.0)},
							{Value: model.SampleValue(20.0)},
							{Value: model.SampleValue(30.0)},
						},
					},
				},
				"30.000000"),
			Entry("matrix with empty values",
				model.Matrix{
					&model.SampleStream{
						Metric: model.Metric{},
						Values: []model.SamplePair{},
					},
				},
				""),
		)
	})

	Context("When checking if result is empty", func() {
		DescribeTable("empty result detection",
			func(result model.Value, expectedEmpty bool) {
				// Act
				isEmpty := isEmptyResult(result)

				// Assert
				Expect(isEmpty).To(Equal(expectedEmpty))
			},
			Entry("empty vector is empty",
				model.Vector{},
				true),
			Entry("vector with sample is not empty",
				model.Vector{
					&model.Sample{Value: model.SampleValue(1.0)},
				},
				false),
			Entry("scalar is never empty",
				&model.Scalar{Value: model.SampleValue(0.0)},
				false),
			Entry("empty matrix is empty",
				model.Matrix{},
				true),
			Entry("matrix with values is not empty",
				model.Matrix{
					&model.SampleStream{
						Values: []model.SamplePair{{Value: model.SampleValue(1.0)}},
					},
				},
				false),
		)
	})

	Context("When verifying all default queries", func() {
		It("should have all expected queries with correct format", func() {
			// Arrange
			expectedQueries := map[string]string{
				"RPSQuery":                          "rate(vllm:request_success_total{finish_reason=\"length\"%s}[60s])",
				"PromptTokenQuery":                  "rate(vllm:prompt_tokens_total%s[60s])",
				"GenerationTokenQuery":              "rate(vllm:generation_tokens_total%s[60s])",
				"InterTokenLatencyQuery":            "histogram_quantile(0.95, rate(vllm:inter_token_latency_seconds_bucket%s[60s]))",
				"TimeToFirstTokenLatencyQuery":      "histogram_quantile(0.95, rate(vllm:time_to_first_token_seconds_bucket%s[60s]))",
				"EndToEndLatencyBucketQuery":        "histogram_quantile(0.95, rate(vllm:e2e_request_latency_seconds_bucket%s[60s]))",
				"DeviceResource_compute_Usage":      "max(vllm:gpu_active_thread_percentage%s)",
				"DeviceResource_compute_Allocation": "vllm:gpu_compute_allocation_percentage%s",
				"DeviceResource_memory_Usage":       "max(vllm:gpu_memory_usage_bytes%s)",
				"DeviceResource_memory_Allocation":  "vllm:gpu_memory_allocation_bytes%s",
			}
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
			}

			// Act
			monitor := NewMonitor(config)

			// Assert
			for name, expectedValue := range expectedQueries {
				Expect(monitor.queries[name]).To(Equal(expectedValue), "Query %s should match", name)
			}
		})
	})

	Context("When using getQueryOrDefault helper", func() {
		It("should return custom query when provided", func() {
			// Act
			result := getQueryOrDefault("custom_query", "default_query")

			// Assert
			Expect(result).To(Equal("custom_query"))
		})

		It("should return default query when custom is empty", func() {
			// Act
			result := getQueryOrDefault("", "default_query")

			// Assert
			Expect(result).To(Equal("default_query"))
		})
	})

	Context("When using custom latency percentile", func() {
		It("should apply custom percentile to histogram queries", func() {
			// Arrange
			customPercentile := 0.99
			config := autoscalingv1alpha1.Monitoring{
				Endpoint:          "http://localhost:8080",
				LatencyPercentile: &customPercentile,
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["InterTokenLatencyQuery"]).To(ContainSubstring("histogram_quantile(0.99"))
			Expect(queries["TimeToFirstTokenLatencyQuery"]).To(ContainSubstring("histogram_quantile(0.99"))
			Expect(queries["EndToEndLatencyBucketQuery"]).To(ContainSubstring("histogram_quantile(0.99"))
		})

		It("should use default percentile 0.95 when not specified", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["InterTokenLatencyQuery"]).To(ContainSubstring("histogram_quantile(0.95"))
		})
	})

	Context("When handling histogram queries with custom configurations", func() {
		It("should not wrap histogram query if histogram_quantile already present", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
				CustomQueries: &autoscalingv1alpha1.CustomQueries{
					InterTokenLatencyQuery: "histogram_quantile(0.99, rate(custom_metric[5m]))",
				},
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["InterTokenLatencyQuery"]).To(Equal("histogram_quantile(0.99, rate(custom_metric[5m]))"))
		})

		It("should append _bucket suffix if not present for histogram queries", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
				CustomQueries: &autoscalingv1alpha1.CustomQueries{
					InterTokenLatencyQuery: "custom_latency_metric",
				},
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["InterTokenLatencyQuery"]).To(ContainSubstring("custom_latency_metric_bucket"))
			Expect(queries["InterTokenLatencyQuery"]).To(ContainSubstring("histogram_quantile"))
		})

		It("should not append _bucket suffix if already present", func() {
			// Arrange
			config := autoscalingv1alpha1.Monitoring{
				Endpoint: "http://localhost:8080",
				CustomQueries: &autoscalingv1alpha1.CustomQueries{
					TimeToFirstTokenLatencyQuery: "custom_metric_bucket",
				},
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["TimeToFirstTokenLatencyQuery"]).To(ContainSubstring("custom_metric_bucket"))
			Expect(queries["TimeToFirstTokenLatencyQuery"]).NotTo(ContainSubstring("custom_metric_bucket_bucket"))
		})
	})

	Context("When handling all query types with custom configurations", func() {
		It("should handle all custom queries correctly", func() {
			// Arrange
			rateRange := int32(90)
			percentile := 0.90
			config := autoscalingv1alpha1.Monitoring{
				Endpoint:          "http://localhost:8080",
				RateRangeSeconds:  &rateRange,
				LatencyPercentile: &percentile,
				CustomQueries: &autoscalingv1alpha1.CustomQueries{
					RPSQuery:                     "custom_rps",
					PromptTokenQuery:             "custom_prompt",
					GenerationTokenQuery:         "custom_generation",
					InterTokenLatencyQuery:       "custom_inter_token",
					TimeToFirstTokenLatencyQuery: "custom_ttft",
					EndToEndLatencyBucketQuery:   "custom_e2e_bucket",
					DeviceResourceMetrics: map[string]autoscalingv1alpha1.DeviceResourceMetrics{
						"compute": {
							UsageQuery:      "custom_compute_usage",
							AllocationQuery: "custom_compute_allocation",
						},
						"memory": {
							UsageQuery:      "custom_memory_usage",
							AllocationQuery: "custom_memory_allocation",
						},
					},
				},
			}

			// Act
			queries := getQueriesWithDefaults(config)

			// Assert
			Expect(queries["RPSQuery"]).To(Equal("rate(custom_rps{finish_reason=\"length\"%s}[90s])"))
			Expect(queries["PromptTokenQuery"]).To(Equal("rate(custom_prompt%s[90s])"))
			Expect(queries["GenerationTokenQuery"]).To(Equal("rate(custom_generation%s[90s])"))
			Expect(queries["InterTokenLatencyQuery"]).To(Equal("histogram_quantile(0.9, rate(custom_inter_token_bucket%s[90s]))"))
			Expect(queries["TimeToFirstTokenLatencyQuery"]).To(Equal("histogram_quantile(0.9, rate(custom_ttft_bucket%s[90s]))"))
			Expect(queries["EndToEndLatencyBucketQuery"]).To(Equal("histogram_quantile(0.9, rate(custom_e2e_bucket%s[90s]))"))
			Expect(queries["DeviceResource_compute_Usage"]).To(Equal("max(custom_compute_usage%s)"))
			Expect(queries["DeviceResource_compute_Allocation"]).To(Equal("custom_compute_allocation%s"))
			Expect(queries["DeviceResource_memory_Usage"]).To(Equal("max(custom_memory_usage%s)"))
			Expect(queries["DeviceResource_memory_Allocation"]).To(Equal("custom_memory_allocation%s"))
		})
	})

	Context("When Query method is called", func() {
		It("should return error when promAPI is not initialized", func() {
			// Arrange
			monitor := &Monitor{
				config:  autoscalingv1alpha1.Monitoring{Endpoint: "http://localhost:8080"},
				promAPI: nil,
			}

			// Act
			_, err := monitor.Query(context.Background(), "test_query")

			// Assert
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus API client not initialized"))
		})
	})

	Context("When extracting values from edge cases", func() {
		It("should return empty string for nil result", func() {
			// Act
			value := extractValueFromResult(nil)

			// Assert
			Expect(value).To(Equal(""))
		})

		It("should return empty string for empty matrix", func() {
			// Act
			value := extractValueFromResult(model.Matrix{})

			// Assert
			Expect(value).To(Equal(""))
		})

		It("should handle matrix with multiple streams", func() {
			// Arrange
			result := model.Matrix{
				&model.SampleStream{
					Metric: model.Metric{},
					Values: []model.SamplePair{
						{Value: model.SampleValue(10.0)},
						{Value: model.SampleValue(20.0)},
					},
				},
				&model.SampleStream{
					Metric: model.Metric{},
					Values: []model.SamplePair{
						{Value: model.SampleValue(30.0)},
					},
				},
			}

			// Act
			value := extractValueFromResult(result)

			// Assert - should return maximum value across all streams: 30
			Expect(value).To(Equal("30.000000"))
		})
	})

	Context("When checking empty results for edge cases", func() {
		It("should return true for nil result", func() {
			// Act
			isEmpty := isEmptyResult(nil)

			// Assert
			Expect(isEmpty).To(BeTrue())
		})
	})
})

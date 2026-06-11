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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceResourceMetrics defines usage and allocation queries for a device resource type
type DeviceResourceMetrics struct {
	// UsageQuery is the Prometheus query for current resource usage
	// +optional
	UsageQuery string `json:"usageQuery,omitempty"`

	// AllocationQuery is the Prometheus query for resource allocation limit
	// +optional
	AllocationQuery string `json:"allocationQuery,omitempty"`
}

// CustomQueries defines custom Prometheus queries for monitoring
type CustomQueries struct {
	// RPSQuery is the Prometheus query for requests per second
	// +optional
	// +kubebuilder:default="vllm:request_success_total"
	RPSQuery string `json:"rpsQuery,omitempty"`

	// PromptTokenQuery is the Prometheus query for prompt tokens
	// +optional
	// +kubebuilder:default="vllm:prompt_tokens_total"
	PromptTokenQuery string `json:"promptTokenQuery,omitempty"`

	// GenerationTokenQuery is the Prometheus query for generation tokens
	// +optional
	// +kubebuilder:default="vllm:generation_tokens_total"
	GenerationTokenQuery string `json:"generationTokenQuery,omitempty"`

	// InterTokenLatencyQuery is the Prometheus query for inter-token latency
	// +optional
	// +kubebuilder:default="vllm:inter_token_latency_seconds"
	InterTokenLatencyQuery string `json:"interTokenLatencyQuery,omitempty"`

	// TimeToFirstTokenLatencyQuery is the Prometheus query for time to first token latency
	// +optional
	// +kubebuilder:default="vllm:time_to_first_token_seconds"
	TimeToFirstTokenLatencyQuery string `json:"timeToFirstTokenLatencyQuery,omitempty"`

	// EndToEndLatencyBucketQuery is the Prometheus query for end-to-end latency buckets
	// +optional
	// +kubebuilder:default="vllm:e2e_request_latency_seconds_bucket"
	EndToEndLatencyBucketQuery string `json:"endToEndLatencyBucketQuery,omitempty"`

	// QueueTimeQuery is the Prometheus query for request queue time
	// +optional
	// +kubebuilder:default="vllm:request_queue_time_seconds_bucket"
	QueueTimeQuery string `json:"queueTimeQuery,omitempty"`

	// PrefillTokensQuery is the Prometheus query for prefill tokens
	// +optional
	// +kubebuilder:default="vllm:prefill_tokens_total"
	PrefillTokensQuery string `json:"prefillTokensQuery,omitempty"`

	// PrefixCacheHitsQuery is the Prometheus query for vLLM prefix cache hits
	// +optional
	// +kubebuilder:default="vllm:prefix_cache_hits"
	PrefixCacheHitsQuery string `json:"prefixCacheHitsQuery,omitempty"`

	// PrefixCacheQueriesQuery is the Prometheus query for vLLM prefix cache queries
	// +optional
	// +kubebuilder:default="vllm:prefix_cache_queries"
	PrefixCacheQueriesQuery string `json:"prefixCacheQueriesQuery,omitempty"`

	// DeviceResourceMetrics maps resource type names (e.g., "compute", "memory") to their usage/allocation queries
	// Example: {"compute": {usageQuery: "vllm:gpu_active_thread_percentage", allocationQuery: "vllm:gpu_compute_allocation_percentage"}}
	// +optional
	DeviceResourceMetrics map[string]DeviceResourceMetrics `json:"deviceResourceMetrics,omitempty"`
}

// Monitoring defines the monitoring configuration
type Monitoring struct {
	// Endpoint is the Prometheus monitoring endpoint
	// +kubebuilder:validation:Required
	Endpoint string `json:"endpoint"`

	// CustomQueries defines custom Prometheus queries
	// +optional
	CustomQueries *CustomQueries `json:"customQueries,omitempty"`

	// RateRangeSeconds is the time range in seconds for rate calculations
	// +optional
	// +kubebuilder:validation:Minimum=1
	RateRangeSeconds *int32 `json:"rateRangeSeconds,omitempty"`

	// LatencyPercentile is the percentile for latency calculations (0.0 to 1.0)
	// +optional
	// +kubebuilder:validation:Minimum=0.0
	// +kubebuilder:validation:Maximum=1.0
	// +kubebuilder:default=0.95
	LatencyPercentile *float64 `json:"latencyPercentile,omitempty"`
}

// LogLevel defines the logging level
// +kubebuilder:validation:Enum=DEBUG;INFO;WARN;ERROR
type LogLevel string

const (
	// LogLevelDebug enables debug logging
	LogLevelDebug LogLevel = "DEBUG"
	// LogLevelInfo enables info logging
	LogLevelInfo LogLevel = "INFO"
	// LogLevelWarn enables warning logging
	LogLevelWarn LogLevel = "WARN"
	// LogLevelError enables error logging
	LogLevelError LogLevel = "ERROR"
)

// Logging defines the logging configuration
type Logging struct {
	// Level is the logging level
	// +optional
	// +kubebuilder:default="INFO"
	Level LogLevel `json:"level,omitempty"`
}

// RCAControllerConfigSpec defines the desired state of RCAControllerConfig.
type RCAControllerConfigSpec struct {
	// Monitoring defines the monitoring configuration
	// +kubebuilder:validation:Required
	Monitoring Monitoring `json:"monitoring"`

	// Logging defines the logging configuration
	// +optional
	Logging *Logging `json:"logging,omitempty"`
}

// MetricHealth defines the health status of a specific metric
type MetricHealth struct {
	// Name is the name of the metric
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Query is the Prometheus query used to derive a metric
	Query string `json:"query,omitempty"`

	// Healthy indicates whether the metric is available and valid
	// +optional
	Healthy bool `json:"healthy,omitempty"`

	// Message provides additional information about the metric health
	// +optional
	Message string `json:"message,omitempty"`
}

// MonitoringStatus defines the monitoring connection status
type MonitoringStatus struct {
	// Connected indicates whether the monitoring endpoint is reachable
	// +optional
	Connected bool `json:"connected,omitempty"`

	// MetricHealths contains the health status of each monitored metric
	// +optional
	MetricHealths []MetricHealth `json:"metricHealths,omitempty"`

	// Message provides additional information about the connection status
	// +optional
	Message string `json:"message,omitempty"`
}

// RCAControllerConfigStatus defines the observed state of RCAControllerConfig.
type RCAControllerConfigStatus struct {
	// MonitoringStatus contains the monitoring connection and validation status
	// +optional
	MonitoringStatus *MonitoringStatus `json:"monitoringStatus,omitempty"`

	// Conditions represent the latest available observations of the config's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// RCAControllerConfig is the Schema for the rcacontrollerconfigs API.
type RCAControllerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RCAControllerConfigSpec   `json:"spec,omitempty"`
	Status RCAControllerConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RCAControllerConfigList contains a list of RCAControllerConfig.
type RCAControllerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RCAControllerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RCAControllerConfig{}, &RCAControllerConfigList{})
}

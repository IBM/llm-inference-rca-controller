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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceReference references a Kubernetes Service
type ServiceReference struct {
	// Name of the service
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace of the service. Defaults to the namespace of the ResourceClaimAutoscaler
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ResourceReference references a ResourceClaim by device class
type ResourceReference struct {
	// DeviceClassName is the name of the device class to target
	// This is used to select resource claims of the service owner
	// +kubebuilder:validation:Required
	DeviceClassName string `json:"deviceClassName"`
}

// Hint provides initial prediction hints for prefill/decode latency
type Hint struct {
	// ModelID is the model name
	// +optional
	ModelID string `json:"modelID,omitempty"`

	// ModelParams defines the model architecture parameters
	// If not provided, parameters will be inferred from ModelID
	// +optional
	ModelParams *ArchParams `json:"modelParams,omitempty"`

	// BatchSize is the batch size for inference (default: 1)
	// This is different from MaxNumSeqs which represents concurrency
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	BatchSize int32 `json:"batchSize"`

	//  MaxNumSeqs represents concurrency
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MaxNumSeqs int32 `json:"maxNumSeq"`

	// MaxNumBatchedTokens is the maximum total tokens that can be processed
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxNumBatchedTokens *int32 `json:"maxNumBatchedTokens,omitempty"`

	// PrefixCacheHitRate is the threshold for KV cache hits (0.0 to 1.0)
	// +optional
	// +kubebuilder:default=0.8
	PrefixCacheHitRate float64 `json:"PrefixCacheHitRate"`

	// KVCacheSpeedup is the speedup factor when KV cache is hit (0.0 to 1.0)
	// Affects latency calculation
	// +optional
	// +kubebuilder:default=1.0
	// +kubebuilder:validation:Minimum=0.0
	KVCacheSpeedup float64 `json:"kvCacheSpeedup"`

	// GPUName
	// +optional
	GPUName string `json:"gpuName,omitempty"`
}

type ArchParams struct {
	// ParametersB is the model size in billions of parameters
	// +required
	// +kubebuilder:validation:Minimum=0.1
	ParametersB float64 `json:"parametersB"`

	// Layers is the number of transformer layers
	// +required
	// +kubebuilder:validation:Minimum=1
	Layers int `json:"layers"`

	// KVHeads is the number of key-value attention heads
	// +required
	// +kubebuilder:validation:Minimum=1
	KVHeads int `json:"kvHeads"`

	// HiddenSize is the hidden dimension size
	// +required
	// +kubebuilder:validation:Minimum=128
	HiddenSize int `json:"hiddenSize"`

	// QueryHeads is the number of query attention heads
	// +required
	// +kubebuilder:validation:Minimum=1
	QueryHeads int `json:"queryHeads"`

	// Precision is the precision in bytes (1=FP8/INT8, 2=FP16/BF16, 4=FP32)
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	// +kubebuilder:default=2
	Precision int `json:"precision"`
}

// TargetSelector defines the scaling target
type TargetSelector struct {
	// ServiceRef references the target service to determine the pod-template to patch
	// +kubebuilder:validation:Required
	ServiceRef ServiceReference `json:"serviceRef"`

	// ResourceRef references the target resource claim to make a new revision
	// +kubebuilder:validation:Required
	ResourceRef ResourceReference `json:"resourceRef"`

	// Hint provides initial prediction hints for prefill/decode latency
	// +optional
	Hint *Hint `json:"hint,omitempty"`
}

// TargetLatency defines the service latency targets
type TargetLatency struct {
	// EndToEndLatencyMilliseconds is the target end-to-end latency in milliseconds
	// +optional
	// +kubebuilder:validation:Minimum=0
	EndToEndLatencyMilliseconds *int32 `json:"endToEndLatencyMilliseconds,omitempty"`

	// TimeToFirstTokenLatencyMilliseconds is the target time to first token latency in milliseconds
	// +optional
	// +kubebuilder:validation:Minimum=0
	TimeToFirstTokenLatencyMilliseconds *int32 `json:"timeToFirstTokenLatencyMilliseconds,omitempty"`

	// InterTokenLatencyMilliseconds is the target inter-token latency in milliseconds
	// +optional
	// +kubebuilder:validation:Minimum=0
	InterTokenLatencyMilliseconds *int32 `json:"interTokenLatencyMilliseconds,omitempty"`

	// ToleranceMilliseconds is the latency violation tolerance in milliseconds
	// +optional
	// +kubebuilder:validation:Minimum=0
	ToleranceMilliseconds *int32 `json:"toleranceMilliseconds,omitempty"`
}

// ScalingTrigger defines when to trigger scaling actions
type ScalingTrigger struct {
	// PeriodSeconds is the static monitoring period in seconds
	// This is the interval at which metrics are collected
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	PeriodSeconds int32 `json:"periodSeconds"`
}

// ScalingBehavior defines the behavior for scaling operations
type ScalingBehavior struct {
	// Trigger defines when to trigger scaling
	// +kubebuilder:validation:Required
	Trigger ScalingTrigger `json:"trigger"`

	// GracePeriodSeconds is the grace period before patching
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=0
	GracePeriodSeconds int32 `json:"gracePeriodSeconds"`

	// SingleTimeScalingLimit limits the amount of resource that can be scaled at a single time
	// +optional
	// +kubebuilder:validation:Minimum=1
	SingleTimeScalingLimit *int32 `json:"singleTimeScalingLimit,omitempty"`
}

// Behavior defines the scaling behaviors
type Behavior struct {
	// ScaleUp defines the behavior for scaling up
	// +optional
	ScaleUp *ScalingBehavior `json:"scaleUp,omitempty"`

	// ScaleDown defines the behavior for scaling down
	// +optional
	ScaleDown *ScalingBehavior `json:"scaleDown,omitempty"`
}

// CapacityPerCountConstraint defines min, max, and step capacity constraints per count
type CapacityPerCountConstraint struct {
	// Min is the minimum capacity per count
	// +optional
	Min *resource.Quantity `json:"min,omitempty"`

	// Max is the maximum capacity per count
	// +optional
	Max *resource.Quantity `json:"max,omitempty"`

	// Step is the increment step for capacity changes
	// +optional
	Step *resource.Quantity `json:"step,omitempty"`
}

// Constraints defines the scaling constraints
type Constraints struct {
	// MinReplicas is the minimum number of replicas. Default is 1, allows 0 for scale to zero
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the maximum number of replicas
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// MinCountPerReplica is the minimum count per replica. Default is 1, must be positive
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MinCountPerReplica *int32 `json:"minCountPerReplica,omitempty"`

	// MaxCountPerReplica is the maximum count per replica
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCountPerReplica *int32 `json:"maxCountPerReplica,omitempty"`

	// CapacityPerCount is a map between capacity name and its min/max/step constraints per count
	// Used to specify minimum, maximum, and step resource allocation per GPU/count
	// Example: {"compute": {min: "1", max: "100", step: "1"}, "memory": {min: "8Gi", max: "80Gi", step: "1Gi"}}
	// +optional
	CapacityPerCount map[corev1.ResourceName]CapacityPerCountConstraint `json:"capacityPerCount,omitempty"`
}

// ResourceClaimAutoscalerSpec defines the desired state of ResourceClaimAutoscaler.
type ResourceClaimAutoscalerSpec struct {
	// Target defines the scaling target selector
	// +kubebuilder:validation:Required
	Target TargetSelector `json:"target"`

	// TargetLatency defines the service latency targets
	// +optional
	TargetLatency *TargetLatency `json:"targetLatency,omitempty"`

	// Behavior defines the scaling behaviors
	// +optional
	Behavior *Behavior `json:"behavior,omitempty"`

	// Constraints defines the scaling constraints
	// +optional
	Constraints *Constraints `json:"constraints,omitempty"`

	// MinTargetUtilization defines the minimum target utilization per resource type
	// Maps resource type (e.g., "nvidia.com/gpu", "memory") to the minimum desired utilization ratio
	// When (usage / allocation) falls below this threshold, scale-down may be triggered
	// Values should be between 0.0 and 1.0 representing utilization percentage
	// Example: {"nvidia.com/gpu": Quantity("0.7")} means target 70% GPU utilization
	// +optional
	MinTargetUtilization map[string]resource.Quantity `json:"minTargetUtilization,omitempty"`
}

// DiscoveryResults contains the discovery results
type DiscoveryResults struct {
	// Owner is the detected owner of the service
	// +optional
	Owner *metav1.OwnerReference `json:"owner,omitempty"`

	// ResourceClaim is the original resource claim
	// +optional
	ResourceClaim string `json:"resourceClaim,omitempty"`
}

// LatencyEstimation contains the last latency estimation
type LatencyEstimation struct {
	// TimeToFirstTokenMilliseconds is the estimated time to first token latency
	// +optional
	// +kubebuilder:validation:Minimum=0
	TimeToFirstTokenMilliseconds *int32 `json:"timeToFirstTokenMilliseconds,omitempty"`

	// InterTokenLatencyMilliseconds is the estimated inter-token latency
	// +optional
	// +kubebuilder:validation:Minimum=0
	InterTokenLatencyMilliseconds *int32 `json:"interTokenLatencyMilliseconds,omitempty"`

	// TimeToFirstTokenOffsetMilliseconds is the cumulative offset applied to TTFT estimation
	// This is the difference between measured and calculated values from previous rounds
	// +optional
	TimeToFirstTokenOffsetMilliseconds *int32 `json:"timeToFirstTokenOffsetMilliseconds,omitempty"`

	// InterTokenLatencyOffsetMilliseconds is the cumulative offset applied to ITL estimation
	// This is the difference between measured and calculated values from previous rounds
	// +optional
	InterTokenLatencyOffsetMilliseconds *int32 `json:"interTokenLatencyOffsetMilliseconds,omitempty"`
}

// ScalingResults contains the scaling estimation results
type ScalingResults struct {
	// LastEstimationTime is the timestamp of the last estimation
	// +optional
	LastEstimationTime *metav1.Time `json:"lastEstimationTime,omitempty"`

	// LastEstimation contains the last latency estimation
	// +optional
	LastEstimation *LatencyEstimation `json:"lastEstimation,omitempty"`
}

// ScalingStatus contains the current scaling status
type ScalingStatus struct {
	// LastApplyTime is the timestamp when the claim was last applied
	// +optional
	LastApplyTime *metav1.Time `json:"lastApplyTime,omitempty"`

	// OldestClaimTemplate is the oldest resource claim template still in use
	// +optional
	OldestClaimTemplate string `json:"oldestClaimTemplate,omitempty"`

	// DesiredClaimTemplate is the desired resource claim template
	// +optional
	DesiredClaimTemplate string `json:"desiredClaimTemplate,omitempty"`

	// DesiredReplicas is the number of desired replicas
	// +optional
	DesiredReplicas *int32 `json:"desiredReplicas,omitempty"`

	// ReadyReplicas is the number of ready replicas
	// +optional
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	// AvailableReplicas is the number of available replicas
	// +optional
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`
}

// ResourceClaimAutoscalerStatus defines the observed state of ResourceClaimAutoscaler.
type ResourceClaimAutoscalerStatus struct {
	// Discovery contains the discovery results
	// +optional
	Discovery *DiscoveryResults `json:"discovery,omitempty"`

	// ScalingResults contains the scaling estimation results
	// +optional
	ScalingResults *ScalingResults `json:"scalingResults,omitempty"`

	// ScalingStatus contains the current scaling status
	// +optional
	ScalingStatus *ScalingStatus `json:"scalingStatus,omitempty"`

	// Conditions represent the latest available observations of the autoscaler's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rca
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.target.serviceRef.name`
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.status.scalingStatus.desiredReplicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.scalingStatus.readyReplicas`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.scalingStatus.availableReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ResourceClaimAutoscaler is the Schema for the resourceclaimautoscalers API.
type ResourceClaimAutoscaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResourceClaimAutoscalerSpec   `json:"spec,omitempty"`
	Status ResourceClaimAutoscalerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ResourceClaimAutoscalerList contains a list of ResourceClaimAutoscaler.
type ResourceClaimAutoscalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceClaimAutoscaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceClaimAutoscaler{}, &ResourceClaimAutoscalerList{})
}

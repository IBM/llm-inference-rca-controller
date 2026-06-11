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
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// Default scaling behavior constants
const (
	// DefaultPeriodSeconds is the default monitoring period (1 minute)
	DefaultPeriodSeconds = 60
	// DefaultGracePeriodSeconds is the default grace period before scaling (30 seconds)
	DefaultGracePeriodSeconds = 30
)

// Default target latency constants (in milliseconds)
const (
	// DefaultEndToEndLatencyMS is the default end-to-end latency target (5 seconds)
	DefaultEndToEndLatencyMS = 5000
	// DefaultTimeToFirstTokenLatencyMS is the default TTFT latency target (1 second)
	DefaultTimeToFirstTokenLatencyMS = 1000
	// DefaultInterTokenLatencyMS is the default inter-token latency target (50ms)
	DefaultInterTokenLatencyMS = 50
	// DefaultToleranceMS is the default latency tolerance (500ms)
	DefaultToleranceMS = 500
)

// GetDefaultScalingBehavior returns default scaling behavior with sensible defaults
func GetDefaultScalingBehavior() *autoscalingv1alpha1.ScalingBehavior {
	return &autoscalingv1alpha1.ScalingBehavior{
		Trigger: autoscalingv1alpha1.ScalingTrigger{
			PeriodSeconds: DefaultPeriodSeconds,
		},
		GracePeriodSeconds: DefaultGracePeriodSeconds,
	}
}

// GetEffectiveBehavior returns the effective behavior, applying defaults if needed
// If no behavior is configured, returns default with both ScaleUp and ScaleDown
// If only one is configured, applies defaults to the missing one
func GetEffectiveBehavior(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) *autoscalingv1alpha1.Behavior {
	if spec.Behavior == nil {
		// No behavior configured - return default with both ScaleUp and ScaleDown
		return &autoscalingv1alpha1.Behavior{
			ScaleUp:   GetDefaultScalingBehavior(),
			ScaleDown: GetDefaultScalingBehavior(),
		}
	}

	// Apply defaults to missing behaviors
	behavior := &autoscalingv1alpha1.Behavior{
		ScaleUp:   spec.Behavior.ScaleUp,
		ScaleDown: spec.Behavior.ScaleDown,
	}

	if behavior.ScaleUp == nil {
		behavior.ScaleUp = GetDefaultScalingBehavior()
	}
	if behavior.ScaleDown == nil {
		behavior.ScaleDown = GetDefaultScalingBehavior()
	}

	return behavior
}

// GetEffectiveTargetLatency returns the effective target latency, applying defaults if needed
func GetEffectiveTargetLatency(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) *autoscalingv1alpha1.TargetLatency {
	if spec.TargetLatency == nil {
		// No target latency configured - return default
		endToEnd := int32(DefaultEndToEndLatencyMS)
		ttft := int32(DefaultTimeToFirstTokenLatencyMS)
		itl := int32(DefaultInterTokenLatencyMS)
		tolerance := int32(DefaultToleranceMS)

		return &autoscalingv1alpha1.TargetLatency{
			EndToEndLatencyMilliseconds:         &endToEnd,
			TimeToFirstTokenLatencyMilliseconds: &ttft,
			InterTokenLatencyMilliseconds:       &itl,
			ToleranceMilliseconds:               &tolerance,
		}
	}

	// Apply defaults to missing fields
	targetLatency := &autoscalingv1alpha1.TargetLatency{
		EndToEndLatencyMilliseconds:         spec.TargetLatency.EndToEndLatencyMilliseconds,
		TimeToFirstTokenLatencyMilliseconds: spec.TargetLatency.TimeToFirstTokenLatencyMilliseconds,
		InterTokenLatencyMilliseconds:       spec.TargetLatency.InterTokenLatencyMilliseconds,
		ToleranceMilliseconds:               spec.TargetLatency.ToleranceMilliseconds,
	}

	// Apply defaults for missing fields
	if targetLatency.EndToEndLatencyMilliseconds == nil {
		endToEnd := int32(DefaultEndToEndLatencyMS)
		targetLatency.EndToEndLatencyMilliseconds = &endToEnd
	}
	if targetLatency.TimeToFirstTokenLatencyMilliseconds == nil {
		ttft := int32(DefaultTimeToFirstTokenLatencyMS)
		targetLatency.TimeToFirstTokenLatencyMilliseconds = &ttft
	}
	if targetLatency.InterTokenLatencyMilliseconds == nil {
		itl := int32(DefaultInterTokenLatencyMS)
		targetLatency.InterTokenLatencyMilliseconds = &itl
	}
	if targetLatency.ToleranceMilliseconds == nil {
		tolerance := int32(DefaultToleranceMS)
		targetLatency.ToleranceMilliseconds = &tolerance
	}

	return targetLatency
}

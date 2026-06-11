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
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// AutoscalerMetricsCollector implements the MetricsCollector interface
type AutoscalerMetricsCollector struct {
	monitor      *Monitor
	monitorMutex *sync.RWMutex
	logger       logr.Logger
}

// NewAutoscalerMetricsCollector creates a new metrics collector for an autoscaler
func NewAutoscalerMetricsCollector(mon *Monitor, monitorMutex *sync.RWMutex) *AutoscalerMetricsCollector {
	return &AutoscalerMetricsCollector{
		monitor:      mon,
		monitorMutex: monitorMutex,
		logger:       ctrl.Log.WithName("metrics-collector"),
	}
}

// CollectAndCompute collects metrics from Prometheus
// lastTriggerTime is the time of the last trigger event, used to calculate lookback window
func (c *AutoscalerMetricsCollector) CollectAndCompute(ctx context.Context, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, lastTriggerTime time.Time) (*MetricsResult, error) {
	logger := c.logger.WithValues("service", spec.Target.ServiceRef.Name)

	// Hold read lock during metric collection to prevent Monitor from being replaced
	c.monitorMutex.RLock()
	mon := c.monitor

	if mon == nil {
		c.monitorMutex.RUnlock()
		logger.Info("Monitor not initialized")
		return &MetricsResult{
			Message: "Monitor not initialized",
		}, fmt.Errorf("monitor not initialized")
	}

	// Get service name from spec
	serviceName := spec.Target.ServiceRef.Name

	// Calculate lookback seconds from last trigger time
	// If lastTriggerTime is zero (initial collection), use instant query (0 seconds)
	var lookbackSeconds int
	if !lastTriggerTime.IsZero() {
		lookbackSeconds = int(time.Since(lastTriggerTime).Seconds())
		logger.V(1).Info("Using lookback window", "lookbackSeconds", lookbackSeconds)
	}

	// Collect metrics from Prometheus while holding the lock
	metrics, err := mon.CollectMetrics(serviceName, lookbackSeconds)
	c.monitorMutex.RUnlock()

	if err != nil {
		logger.Error(err, "Failed to collect metrics")
		return &MetricsResult{
			Message: fmt.Sprintf("Failed to collect metrics: %v", err),
		}, err
	}

	logger.V(1).Info("Collected metrics", "metrics", metrics)

	return &MetricsResult{
		Message: "Metrics collected successfully",
		Metrics: metrics, // Include raw metrics for scaling decisions
	}, nil
}

// computeSingleLatencyViolation computes violation for a single latency metric
// Returns violation value (0.0 to 1.0) and whether a check was performed
func computeSingleLatencyViolation(
	metricKey string,
	metrics map[string]string,
	targetMs float64,
	toleranceMs float64,
	metricName string,
	logger logr.Logger,
) (violation float64, checked bool) {
	latencyStr, ok := metrics[metricKey]
	if !ok || latencyStr == "" {
		return 0.0, false
	}

	latency, err := strconv.ParseFloat(latencyStr, 64)
	if err != nil {
		return 0.0, false
	}

	// Convert seconds to milliseconds
	latencyMs := latency * 1000
	effectiveTarget := targetMs + toleranceMs

	if latencyMs > effectiveTarget {
		// Compute violation as percentage: min((measured - target) / target, 1.0)
		violation := (latencyMs - effectiveTarget) / effectiveTarget
		if violation > 1.0 {
			violation = 1.0
		}
		logger.V(1).Info(metricName+" violation detected",
			"current", latencyMs, "target", targetMs, "tolerance", toleranceMs, "violation", violation)
		return violation, true
	}

	return 0.0, true
}

// computeLatencyViolations compares current latency metrics with target latency and returns violation probability
// Violation is computed as: min((measured - target) / target, 1.0) when measured > target, otherwise 0
func (c *AutoscalerMetricsCollector) computeLatencyViolations(
	spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec,
	metrics map[string]string,
	logger logr.Logger,
) float64 {
	if spec.TargetLatency == nil {
		logger.V(1).Info("No target latency configured, returning 0 violation probability")
		return 0.0
	}

	targetLatency := spec.TargetLatency
	var totalViolation float64
	totalChecks := 0

	// Get tolerance (applies to all metrics)
	tolerance := 0.0
	if targetLatency.ToleranceMilliseconds != nil {
		tolerance = float64(*targetLatency.ToleranceMilliseconds)
	}

	// Check EndToEndLatency
	if targetLatency.EndToEndLatencyMilliseconds != nil {
		violation, checked := computeSingleLatencyViolation(
			"EndToEndLatencyBucketQuery",
			metrics,
			float64(*targetLatency.EndToEndLatencyMilliseconds),
			tolerance,
			"EndToEndLatency",
			logger,
		)
		if checked {
			totalChecks++
			totalViolation += violation
		}
	}

	// Check TimeToFirstTokenLatency
	if targetLatency.TimeToFirstTokenLatencyMilliseconds != nil {
		violation, checked := computeSingleLatencyViolation(
			"TimeToFirstTokenLatencyQuery",
			metrics,
			float64(*targetLatency.TimeToFirstTokenLatencyMilliseconds),
			tolerance,
			"TimeToFirstTokenLatency",
			logger,
		)
		if checked {
			totalChecks++
			totalViolation += violation
		}
	}

	// Check InterTokenLatency
	if targetLatency.InterTokenLatencyMilliseconds != nil {
		violation, checked := computeSingleLatencyViolation(
			"InterTokenLatencyQuery",
			metrics,
			float64(*targetLatency.InterTokenLatencyMilliseconds),
			tolerance,
			"InterTokenLatency",
			logger,
		)
		if checked {
			totalChecks++
			totalViolation += violation
		}
	}

	if totalChecks == 0 {
		logger.V(1).Info("No latency targets configured for checking")
		return 0.0
	}

	// Calculate average violation probability across all checks
	violationProb := totalViolation / float64(totalChecks)
	return violationProb
}

// computeResourceUtilization computes the utilization ratio for each device resource
// Returns a map of resource type to utilization ratio (usage / allocation)
func (c *AutoscalerMetricsCollector) computeResourceUtilization(
	spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec,
	metrics map[string]string,
	logger logr.Logger,
) map[string]float64 {
	utilization := make(map[string]float64)

	// Check if min target utilization thresholds are configured
	if len(spec.MinTargetUtilization) == 0 {
		logger.V(1).Info("No min target utilization thresholds configured")
		return utilization
	}

	// Iterate through configured min target utilization thresholds
	for resourceType := range spec.MinTargetUtilization {
		usageKey := fmt.Sprintf("DeviceResource_%s_Usage", resourceType)
		allocationKey := fmt.Sprintf("DeviceResource_%s_Allocation", resourceType)

		usageStr, hasUsage := metrics[usageKey]
		allocationStr, hasAllocation := metrics[allocationKey]

		if !hasUsage || !hasAllocation {
			logger.V(1).Info("Missing resource metrics", "resourceType", resourceType, "hasUsage", hasUsage, "hasAllocation", hasAllocation)
			continue
		}

		if usageStr == "" {
			logger.V(1).Info("Usage is zero", "resourceType", resourceType)
			continue
		}

		usage, err := strconv.ParseFloat(usageStr, 64)
		if err != nil {
			logger.V(1).Info("Failed to parse usage", "resourceType", resourceType, "value", usageStr, "error", err)
			continue
		}

		allocation, err := strconv.ParseFloat(allocationStr, 64)
		if err != nil {
			logger.V(1).Info("Failed to parse allocation", "resourceType", resourceType, "value", allocationStr, "error", err)
			continue
		}

		if allocation == 0 {
			logger.V(1).Info("Allocation is zero", "resourceType", resourceType)
			continue
		}

		// Calculate utilization ratio
		ratio := usage / allocation
		utilization[resourceType] = ratio
		logger.V(1).Info("Computed resource utilization", "resourceType", resourceType, "usage", usage, "allocation", allocation, "ratio", ratio)
	}

	return utilization
}

// computeUnderutilizationViolation calculates a violation probability based on resource underutilization
// Higher underutilization (lower usage) results in higher violation probability for scale-down
// Returns 0 if resources are well utilized (cannot scale down)
// Returns 0.1-1.0 if resources are underutilized (can scale down, minimum 10% for gradual scale-down)
func (c *AutoscalerMetricsCollector) computeUnderutilizationViolation(
	spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec,
	metrics map[string]string,
	logger logr.Logger,
) float64 {
	const minViolation = 0.1 // Minimum 10% violation for gradual scale-down

	// If no min target utilization thresholds defined, use minimum violation for gradual scale-down
	if len(spec.MinTargetUtilization) == 0 {
		logger.Info("No min target utilization thresholds defined, using minimum violation for gradual scale-down")
		return minViolation
	}

	// Compute resource utilization
	utilization := c.computeResourceUtilization(spec, metrics, logger)
	logger.V(1).Info("Computed resource utilization", "utilization", utilization)

	// Calculate violation based on underutilization
	totalViolation := 0.0
	count := 0

	for resourceType, threshold := range spec.MinTargetUtilization {
		ratio, exists := utilization[resourceType]
		if !exists {
			continue
		}

		thresholdValue := threshold.AsApproximateFloat64()

		// Calculate how much headroom we have below the threshold
		// If ratio is 0.3 and threshold is 0.7, headroom is 0.4 (57% below threshold)
		headroom := thresholdValue - ratio
		if headroom < 0 {
			headroom = 0
		}

		// Convert headroom to violation probability
		// More headroom = higher violation probability for scale-down
		violation := headroom / thresholdValue
		totalViolation += violation
		count++

		logger.V(1).Info("Computed underutilization violation",
			"resourceType", resourceType,
			"utilization", ratio,
			"threshold", thresholdValue,
			"headroom", headroom,
			"violation", violation)
	}

	if count == 0 || totalViolation == 0.0 {
		return 0.0
	}

	// Calculate average violation across all resources
	avgViolation := totalViolation / float64(count)

	// Ensure minimum violation of 10% for gradual scale-down
	if avgViolation < minViolation {
		logger.Info("Computed underutilization violation below minimum, using minimum",
			"computed", avgViolation, "minimum", minViolation)
		return minViolation
	}

	logger.Info("Computed average underutilization violation", "violation", avgViolation)
	return avgViolation
}

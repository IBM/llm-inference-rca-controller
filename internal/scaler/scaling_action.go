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
	"context"
	"fmt"
	"math"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/optimizer"
)

// ScalingAction represents the type of scaling action needed
type ScalingAction string

const (
	// ScalingActionNone indicates no scaling is needed
	ScalingActionNone ScalingAction = "None"
	// ScalingActionScaleUp indicates scaling up is needed (replicas or resources)
	ScalingActionScaleUp ScalingAction = "ScaleUp"
	// ScalingActionScaleDown indicates scaling down is needed (replicas or resources)
	ScalingActionScaleDown ScalingAction = "ScaleDown"
)

// ScalingDecision contains the scaling decision details
type ScalingDecision struct {
	Action                       ScalingAction
	ReplicaChange                bool
	ResourceChange               bool
	CurrentResourceConfiguration optimizer.ResourceConfiguration
	DesiredResourceConfiguration optimizer.ResourceConfiguration
	Reason                       string
}

// calculateResourceUtilization calculates the utilization ratio for a given resource type
// based on the desired configuration. It computes what the utilization would be after scaling
// to the desired configuration: (per-replica usage) / (per-replica allocation)
// Returns the utilization ratio or -1 if calculation fails
func (s *ResourceScaler) calculateResourceUtilization(
	resourceName string,
	decision *ScalingDecision,
) float64 {
	var utilization float64

	switch resourceName {
	case "compute":
		// For compute: utilization is the requested compute percentage (already a ratio)
		// RequestedCompute represents the fraction of GPU compute being used (e.g., 1.0 = 100%)
		utilization = decision.DesiredResourceConfiguration.RequestedCompute
	case "memory":
		// For memory: utilization is per-replica memory usage / per-replica memory allocation
		// Since RequestedMemoryGB is the allocation per replica, and we assume full usage,
		// utilization is 1.0 (100%) - memory is typically fully allocated
		utilization = 1.0
	default:
		// For GPU resources, use compute utilization as a proxy
		utilization = decision.DesiredResourceConfiguration.RequestedCompute
	}

	s.logger.V(1).Info("Calculated resource utilization",
		"resource", resourceName,
		"desiredReplicas", decision.DesiredResourceConfiguration.Replicas,
		"desiredGPUsPerReplica", decision.DesiredResourceConfiguration.GPUsPerReplica,
		"desiredCompute", decision.DesiredResourceConfiguration.RequestedCompute,
		"desiredMemoryGB", decision.DesiredResourceConfiguration.RequestedMemoryGB,
		"utilization", utilization)

	return utilization
}

// checkMinTargetUtilization checks if desired resource utilization is above the minimum target
// Returns true if scale-down should be blocked due to high utilization
func (s *ResourceScaler) checkMinTargetUtilization(
	autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler,
	decision *ScalingDecision,
	arrivalRate float64,
) (bool, string) {
	// Only check for scale-down actions
	if decision.Action != ScalingActionScaleDown {
		return false, ""
	}

	// If arrival rate is zero, don't block scale-down
	// No traffic means we can safely scale down without worrying about utilization
	if arrivalRate == 0 {
		s.logger.V(1).Info("Arrival rate is zero, allowing scale-down without utilization check")
		return false, ""
	}

	// Check if MinTargetUtilization is configured
	if len(autoscaler.Spec.MinTargetUtilization) == 0 {
		return false, ""
	}

	// Check compute utilization if configured
	if minComputeTarget, ok := autoscaler.Spec.MinTargetUtilization["compute"]; ok {
		minTargetRatio := minComputeTarget.AsApproximateFloat64()
		if minTargetRatio > 0 {
			computeUtilization := s.calculateResourceUtilization("compute", decision)
			if computeUtilization >= 0 && computeUtilization >= minTargetRatio {
				reason := fmt.Sprintf("Compute utilization (%.2f%%) is above minimum target (%.2f%%), blocking scale-down",
					computeUtilization*100, minTargetRatio*100)
				s.logger.Info("Scale-down blocked by MinTargetUtilization",
					"resource", "compute",
					"utilization", computeUtilization,
					"minTarget", minTargetRatio)
				return true, reason
			}
		}
	}

	// Check memory utilization if configured
	if minMemoryTarget, ok := autoscaler.Spec.MinTargetUtilization["memory"]; ok {
		minTargetRatio := minMemoryTarget.AsApproximateFloat64()
		if minTargetRatio > 0 {
			memoryUtilization := s.calculateResourceUtilization("memory", decision)
			if memoryUtilization >= 0 && memoryUtilization >= minTargetRatio {
				reason := fmt.Sprintf("Memory utilization (%.2f%%) is above minimum target (%.2f%%), blocking scale-down",
					memoryUtilization*100, minTargetRatio*100)
				s.logger.Info("Scale-down blocked by MinTargetUtilization",
					"resource", "memory",
					"utilization", memoryUtilization,
					"minTarget", minTargetRatio)
				return true, reason
			}
		}
	}

	// Check GPU utilization if configured (using nvidia.com/gpu or similar)
	for resourceKey, minTarget := range autoscaler.Spec.MinTargetUtilization {
		if resourceKey != "compute" && resourceKey != "memory" {
			// Handle other resource types like "nvidia.com/gpu"
			minTargetRatio := minTarget.AsApproximateFloat64()
			if minTargetRatio > 0 {
				// For GPU resources, use compute utilization as a proxy
				gpuUtilization := s.calculateResourceUtilization(resourceKey, decision)
				if gpuUtilization >= 0 && gpuUtilization >= minTargetRatio {
					reason := fmt.Sprintf("%s utilization (%.2f%%) is above minimum target (%.2f%%), blocking scale-down",
						resourceKey, gpuUtilization*100, minTargetRatio*100)
					s.logger.Info("Scale-down blocked by MinTargetUtilization",
						"resource", resourceKey,
						"utilization", gpuUtilization,
						"minTarget", minTargetRatio)
					return true, reason
				}
			}
		}
	}

	return false, ""
}

// determineScalingAction determines what scaling action is needed by comparing current and desired configuration
func (s *ResourceScaler) determineScalingAction(
	ctx context.Context,
	autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler,
	desiredConfig *optimizer.OptimalConfiguration,
	arrivalRate float64,
) *ScalingDecision {
	decision := &ScalingDecision{
		Action:         ScalingActionNone,
		ReplicaChange:  false,
		ResourceChange: false,
		DesiredResourceConfiguration: optimizer.ResourceConfiguration{
			Replicas:          desiredConfig.Replicas,
			GPUsPerReplica:    desiredConfig.GPUsPerReplica,
			RequestedCompute:  desiredConfig.RequestedCompute,
			RequestedMemoryGB: desiredConfig.RequestedMemoryGB,
		},
	}

	// Get current state from status
	decision.CurrentResourceConfiguration.Replicas = 1 // Default
	if autoscaler.Status.ScalingStatus != nil && autoscaler.Status.ScalingStatus.DesiredReplicas != nil {
		decision.CurrentResourceConfiguration.Replicas = int(*autoscaler.Status.ScalingStatus.DesiredReplicas)
	}

	// Get current resource allocation from ResourceClaimTemplate
	gpus, resources, err := s.getCurrentResourcesFromTemplate(ctx, autoscaler)
	decision.CurrentResourceConfiguration.GPUsPerReplica = defaultGPUsPerReplica
	decision.CurrentResourceConfiguration.RequestedCompute = defaultComputeThreadPercent
	decision.CurrentResourceConfiguration.RequestedMemoryGB = defaultMemoryGB
	if err != nil {
		s.logger.Error(err, "Failed to get current resources from template, using defaults")
	} else {
		decision.CurrentResourceConfiguration.GPUsPerReplica = gpus
		if computeResource, found := resources["compute"]; found {
			decision.CurrentResourceConfiguration.RequestedCompute = computeResource.AsApproximateFloat64()
		}
		if memoryResource, found := resources["memory"]; found {
			// Convert bytes to GB (1 GB = 1024^3 bytes)
			memoryBytes := memoryResource.AsApproximateFloat64()
			decision.CurrentResourceConfiguration.RequestedMemoryGB = memoryBytes / (1024 * 1024 * 1024)
		}
	}

	// Calculate total compute and memory across all replicas
	// Total compute = replicas × GPUs per replica × compute percentage
	currentTotalComputeGPUs := float64(decision.CurrentResourceConfiguration.Replicas) * float64(decision.CurrentResourceConfiguration.GPUsPerReplica) * decision.CurrentResourceConfiguration.RequestedCompute
	desiredTotalComputeGPUs := float64(decision.DesiredResourceConfiguration.Replicas) * float64(decision.DesiredResourceConfiguration.GPUsPerReplica) * decision.DesiredResourceConfiguration.RequestedCompute

	// Total memory = replicas × memory per replica
	currentTotalMemory := float64(decision.CurrentResourceConfiguration.Replicas) * decision.CurrentResourceConfiguration.RequestedMemoryGB
	desiredTotalMemory := float64(decision.DesiredResourceConfiguration.Replicas) * decision.DesiredResourceConfiguration.RequestedMemoryGB

	const resourceChangeThreshold = 0.01 // 1% threshold for resource changes

	// Check if resources are effectively equal (within threshold)
	computeEqual := math.Abs(desiredTotalComputeGPUs-currentTotalComputeGPUs) <= resourceChangeThreshold
	memoryEqual := math.Abs(desiredTotalMemory-currentTotalMemory) <= (currentTotalMemory * resourceChangeThreshold)

	// Determine scaling action based on total compute and memory
	if computeEqual && memoryEqual {
		// No action needed - current configuration meets requirements
		decision.Action = ScalingActionNone
		decision.Reason = fmt.Sprintf("Current configuration meets requirements (compute: %.2f GPU-units, memory: %.2f GB)",
			currentTotalComputeGPUs, currentTotalMemory)
	} else if desiredTotalComputeGPUs > currentTotalComputeGPUs {
		// Scale up - need more compute
		decision.Action = ScalingActionScaleUp
		decision.ReplicaChange = decision.CurrentResourceConfiguration.Replicas != decision.DesiredResourceConfiguration.Replicas
		// ResourceChange is true only if per-replica resources change (not just replica count)
		decision.ResourceChange = decision.CurrentResourceConfiguration.GPUsPerReplica != decision.DesiredResourceConfiguration.GPUsPerReplica ||
			math.Abs(decision.CurrentResourceConfiguration.RequestedCompute-decision.DesiredResourceConfiguration.RequestedCompute) > resourceChangeThreshold ||
			math.Abs(decision.CurrentResourceConfiguration.RequestedMemoryGB-decision.DesiredResourceConfiguration.RequestedMemoryGB) > resourceChangeThreshold
		decision.Reason = fmt.Sprintf("Scale up: total compute from %.2f to %.2f GPU-units, total memory from %.2f to %.2f GB",
			currentTotalComputeGPUs, desiredTotalComputeGPUs, currentTotalMemory, desiredTotalMemory)
	} else if desiredTotalComputeGPUs < currentTotalComputeGPUs {
		// Scale down - have excess compute
		decision.Action = ScalingActionScaleDown
		decision.ReplicaChange = decision.CurrentResourceConfiguration.Replicas != decision.DesiredResourceConfiguration.Replicas
		// ResourceChange is true only if per-replica resources change (not just replica count)
		decision.ResourceChange = decision.CurrentResourceConfiguration.GPUsPerReplica != decision.DesiredResourceConfiguration.GPUsPerReplica ||
			math.Abs(decision.CurrentResourceConfiguration.RequestedCompute-decision.DesiredResourceConfiguration.RequestedCompute) > resourceChangeThreshold ||
			math.Abs(decision.CurrentResourceConfiguration.RequestedMemoryGB-decision.DesiredResourceConfiguration.RequestedMemoryGB) > resourceChangeThreshold
		decision.Reason = fmt.Sprintf("Scale down: total compute from %.2f to %.2f GPU-units, total memory from %.2f to %.2f GB",
			currentTotalComputeGPUs, desiredTotalComputeGPUs, currentTotalMemory, desiredTotalMemory)
	} else {
		// Compute is equal, check memory
		if desiredTotalMemory > currentTotalMemory {
			// Scale up - need more memory
			decision.Action = ScalingActionScaleUp
			decision.ReplicaChange = decision.CurrentResourceConfiguration.Replicas != decision.DesiredResourceConfiguration.Replicas
			// ResourceChange is true only if per-replica memory changes (not just replica count)
			decision.ResourceChange = math.Abs(decision.CurrentResourceConfiguration.RequestedMemoryGB-decision.DesiredResourceConfiguration.RequestedMemoryGB) > resourceChangeThreshold
			decision.Reason = fmt.Sprintf("Scale up: total memory from %.2f to %.2f GB (compute equal at %.2f GPU-units)",
				currentTotalMemory, desiredTotalMemory, currentTotalComputeGPUs)
		} else {
			// Scale down - have excess memory
			decision.Action = ScalingActionScaleDown
			decision.ReplicaChange = decision.CurrentResourceConfiguration.Replicas != decision.DesiredResourceConfiguration.Replicas
			// ResourceChange is true only if per-replica memory changes (not just replica count)
			decision.ResourceChange = math.Abs(decision.CurrentResourceConfiguration.RequestedMemoryGB-decision.DesiredResourceConfiguration.RequestedMemoryGB) > resourceChangeThreshold
			decision.Reason = fmt.Sprintf("Scale down: total memory from %.2f to %.2f GB (compute equal at %.2f GPU-units)",
				currentTotalMemory, desiredTotalMemory, currentTotalComputeGPUs)
		}
	}

	// Check MinTargetUtilization before allowing scale-down
	if decision.Action == ScalingActionScaleDown {
		if shouldBlock, blockReason := s.checkMinTargetUtilization(autoscaler, decision, arrivalRate); shouldBlock {
			// Block unnecessary scale-down due to already high utilization
			decision.Action = ScalingActionNone
			decision.Reason = blockReason
			decision.ReplicaChange = false
			decision.ResourceChange = false
		}
	}

	// Apply singleTimeScalingLimit if configured
	if decision.Action != ScalingActionNone {
		s.applySingleTimeScalingLimit(autoscaler, decision)
	}

	return decision
}

// applySingleTimeScalingLimit limits the amount of resource change in a single scaling step
// If no limit is configured, scaling proceeds without restrictions
func (s *ResourceScaler) applySingleTimeScalingLimit(
	autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler,
	decision *ScalingDecision,
) {
	var limit *int32

	// Get the appropriate limit based on scaling direction
	if decision.Action == ScalingActionScaleUp && autoscaler.Spec.Behavior != nil && autoscaler.Spec.Behavior.ScaleUp != nil {
		limit = autoscaler.Spec.Behavior.ScaleUp.SingleTimeScalingLimit
	} else if decision.Action == ScalingActionScaleDown && autoscaler.Spec.Behavior != nil && autoscaler.Spec.Behavior.ScaleDown != nil {
		limit = autoscaler.Spec.Behavior.ScaleDown.SingleTimeScalingLimit
	}

	// No limit configured - scale without restrictions
	if limit == nil || *limit <= 0 {
		s.logger.V(1).Info("No singleTimeScalingLimit configured, scaling without restrictions",
			"action", decision.Action)
		return
	}

	limitValue := int(*limit)
	current := decision.CurrentResourceConfiguration
	desired := decision.DesiredResourceConfiguration

	// Calculate the change in total GPUs
	currentTotalGPUs := current.Replicas * current.GPUsPerReplica
	desiredTotalGPUs := desired.Replicas * desired.GPUsPerReplica
	gpuChange := desiredTotalGPUs - currentTotalGPUs

	// If change exceeds limit, cap it
	if math.Abs(float64(gpuChange)) > float64(limitValue) {
		var limitedTotalGPUs int
		if gpuChange > 0 {
			// Scale up: limit increase
			limitedTotalGPUs = currentTotalGPUs + limitValue
		} else {
			// Scale down: limit decrease
			limitedTotalGPUs = currentTotalGPUs - limitValue
			if limitedTotalGPUs < 1 {
				limitedTotalGPUs = 1 // Ensure at least 1 GPU
			}
		}

		// Adjust desired configuration to respect the limit
		// Prefer changing replicas over GPUs per replica
		if limitedTotalGPUs >= desired.GPUsPerReplica {
			decision.DesiredResourceConfiguration.Replicas = limitedTotalGPUs / desired.GPUsPerReplica
			// Handle remainder by adjusting GPUs per replica if needed
			remainder := limitedTotalGPUs % desired.GPUsPerReplica
			if remainder > 0 && decision.DesiredResourceConfiguration.Replicas > 0 {
				// Round up replicas to accommodate remainder
				decision.DesiredResourceConfiguration.Replicas++
			}
		} else {
			// Limited total is less than desired GPUs per replica
			decision.DesiredResourceConfiguration.Replicas = 1
			decision.DesiredResourceConfiguration.GPUsPerReplica = limitedTotalGPUs
		}

		originalReason := decision.Reason
		decision.Reason = fmt.Sprintf("%s (limited by singleTimeScalingLimit: %d GPUs, from %d to %d total GPUs)",
			originalReason, limitValue, currentTotalGPUs, limitedTotalGPUs)

		s.logger.Info("Applied singleTimeScalingLimit",
			"action", decision.Action,
			"limit", limitValue,
			"originalChange", gpuChange,
			"limitedChange", limitedTotalGPUs-currentTotalGPUs,
			"currentTotalGPUs", currentTotalGPUs,
			"desiredTotalGPUs", desiredTotalGPUs,
			"limitedTotalGPUs", limitedTotalGPUs)
	}
}

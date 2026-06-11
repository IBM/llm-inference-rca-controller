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
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/optimizer"
)

var (
	// Default values for scaling parameters
	defaultGPUsPerReplica       = 1
	defaultComputeThreadPercent = 100.0
	defaultMemoryGB             = 80.0
)

// ResourceScaler handles scaling operations for ResourceClaimAutoscaler
type ResourceScaler struct {
	client client.Client
	logger logr.Logger
}

// NewResourceScaler creates a new ResourceScaler
func NewResourceScaler(c client.Client, logger logr.Logger) *ResourceScaler {
	return &ResourceScaler{
		client: c,
		logger: logger,
	}
}

// EvaluateAndScale evaluates metrics and performs scaling if needed
// metrics parameter contains the already-collected metrics from the monitoring loop
func (s *ResourceScaler) EvaluateAndScale(ctx context.Context, name, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, metrics map[string]string) error {
	logger := s.logger.WithValues("autoscaler", name, "namespace", namespace)

	// Get current arrival rate from metrics
	arrivalRate := s.getArrivalRate(metrics)

	// Calculate latency offsets from previous round for tuning
	itlOffset, ttftOffset := s.calculateLatencyOffsets(ctx, name, namespace, metrics, logger)

	// Special case: if arrival rate is zero (no traffic), use minimum configuration from constraints
	// Queue theory calculations break down with zero arrival rate
	if arrivalRate == 0 {
		logger.Info("Arrival rate is zero (no traffic), using minimum configuration from constraints")

		// Get minimum configuration from constraints
		minReplicas := int32(1) // Default
		if spec.Constraints != nil && spec.Constraints.MinReplicas != nil {
			minReplicas = *spec.Constraints.MinReplicas
		}

		minCountPerReplica := int32(1) // Default (GPUs per replica)
		if spec.Constraints != nil && spec.Constraints.MinCountPerReplica != nil {
			minCountPerReplica = *spec.Constraints.MinCountPerReplica
		}

		// Get minimum compute and memory from CapacityPerCount constraints
		minRequestedCompute := 1.0                               // Default: 1% per GPU
		minRequestedMemoryBytes := int64(8 * 1024 * 1024 * 1024) // Default: 8GB

		if spec.Constraints != nil && spec.Constraints.CapacityPerCount != nil {
			// Get minimum compute if specified
			if computeConstraint, ok := spec.Constraints.CapacityPerCount["compute"]; ok && computeConstraint.Min != nil {
				minRequestedCompute = float64(computeConstraint.Min.AsApproximateFloat64())
			}

			// Get minimum memory if specified
			if memoryConstraint, ok := spec.Constraints.CapacityPerCount["memory"]; ok && memoryConstraint.Min != nil {
				minRequestedMemoryBytes = memoryConstraint.Min.Value()
			}
		}

		// Create minimal configuration based on constraints
		minConfig := &optimizer.OptimalConfiguration{
			ResourceConfiguration: optimizer.ResourceConfiguration{
				Replicas:          int(minReplicas),
				GPUsPerReplica:    int(minCountPerReplica),
				RequestedCompute:  minRequestedCompute,
				RequestedMemoryGB: float64(minRequestedMemoryBytes) / (1024 * 1024 * 1024),
			},

			TotalGPUs:        int(minReplicas * minCountPerReplica),
			EstimatedLatency: optimizer.LatencyResult{},
			MeetsConstraints: true,
		}

		logger.Info("Using minimum configuration",
			"minReplicas", minReplicas,
			"minCountPerReplica", minCountPerReplica,
			"totalGPUs", minConfig.TotalGPUs,
			"requestedCompute", minRequestedCompute,
			"requestedMemoryGB", minConfig.RequestedMemoryGB)

		// Apply scaling action with minimal configuration
		// Pass arrivalRate (which is 0 in this case) and zero offsets
		if err := s.applyScalingAction(ctx, name, namespace, minConfig, arrivalRate, metrics, 0, 0); err != nil {
			return fmt.Errorf("failed to apply scaling action: %w", err)
		}
		return nil
	}

	// Get effective target latency with defaults applied
	effectiveTargetLatency := GetEffectiveTargetLatency(spec)

	// Determine target latency targets (all specified targets)
	targetLatencies, err := s.getTargetLatency(effectiveTargetLatency)
	if err != nil {
		return err
	}

	// Extract workload parameters from metrics
	avgPromptTokens := s.getPromptTokens(metrics)
	avgOutputTokens := s.getOutputTokens(metrics)
	maxNumSeqs := s.getMaxNumSeqs(spec)

	// Create resource optimizer with new flow
	optimizer := optimizer.NewResourceOptimizer(spec, s.logger)
	optimizer.SetWorkloadParameters(arrivalRate, avgPromptTokens, avgOutputTokens, maxNumSeqs)

	// Apply latency offsets for tuning if available
	if itlOffset != 0 || ttftOffset != 0 {
		optimizer.SetLatencyOffsets(itlOffset, ttftOffset)
		logger.Info("Applied latency offsets for tuning",
			"itlOffset_ms", itlOffset,
			"ttftOffset_ms", ttftOffset)
	}

	// Extract tuning metrics if available
	prefillTokens := s.getPrefillTokens(metrics)
	promptLen := avgPromptTokens
	if prefillTokens == 0.0 {
		hint := spec.Target.Hint
		if hint != nil {
			maxNumBatchToken := hint.MaxNumBatchedTokens
			if maxNumBatchToken != nil {
				effectiveBatch := math.Min(float64(hint.BatchSize), float64(hint.MaxNumSeqs))
				prefillTokens = math.Min(float64(promptLen), float64(*maxNumBatchToken)-effectiveBatch)
			} else {
				prefillTokens = float64(promptLen)
			}
		} else {
			prefillTokens = float64(promptLen)
		}
	}
	outputTokens := avgOutputTokens
	cacheHitRatio := s.getCacheHitRatio(metrics)
	computeThreadUtilization := s.getComputeThreadUtilization(metrics)
	measuredMemoryUsage := s.getMeasuredMemoryUsage(metrics)
	numGPU := s.getNumGPU(metrics)

	// Tune with metrics if available
	// TODO: Remove numGPU condition when we add a way to calculate the number of GPUs and corresponding metrics
	if prefillTokens > 0 && numGPU == 1.0 {
		optimizer.TuneWithMetrics(prefillTokens, promptLen, outputTokens, cacheHitRatio, computeThreadUtilization, measuredMemoryUsage, numGPU)
	}

	// Find optimal configuration validating against all specified targets
	optimalConfig, err := optimizer.FindOptimalConfiguration(arrivalRate, targetLatencies)
	if err != nil {
		return fmt.Errorf("failed to find optimal configuration: %w", err)
	}

	logger.V(1).Info(fmt.Sprintf("Optimizer config: %v", optimalConfig))

	logger.Info("Found optimal configuration",
		"replicas", optimalConfig.Replicas,
		"gpusPerReplica", optimalConfig.GPUsPerReplica,
		"totalGPUs", optimalConfig.TotalGPUs,
		"requestedCompute", optimalConfig.RequestedCompute,
		"requestedMemoryGB", optimalConfig.RequestedMemoryGB,
		"estimatedLatency", optimalConfig.EstimatedLatency.E2E)

	// Apply scaling action if needed, passing the cumulative offsets
	if err := s.applyScalingAction(ctx, name, namespace, optimalConfig, arrivalRate, metrics, itlOffset, ttftOffset); err != nil {
		return fmt.Errorf("failed to apply scaling action: %w", err)
	}

	return nil
}

// LatencyTargets contains all specified latency targets
type LatencyTargets struct {
	E2E  *float64 // End-to-end latency target in ms
	TTFT *float64 // Time to first token target in ms
	ITL  *float64 // Inter-token latency target in ms
}

// HasAnyTarget returns true if at least one target is specified
func (t *LatencyTargets) HasAnyTarget() bool {
	return t.E2E != nil || t.TTFT != nil || t.ITL != nil
}

// GetPrimaryTarget returns the primary target for optimization
// Priority: E2E > TTFT > ITL
func (t *LatencyTargets) GetPrimaryTarget() float64 {
	if t.E2E != nil {
		return *t.E2E
	}
	if t.TTFT != nil {
		return *t.TTFT
	}
	if t.ITL != nil {
		return *t.ITL
	}
	return 0
}

// getTargetLatency extracts all target latencies from spec
// Returns all specified latency targets for validation with component estimator
// If no targets are specified, returns an error
func (s *ResourceScaler) getTargetLatency(targetLatency *autoscalingv1alpha1.TargetLatency) (*optimizer.LatencyTargets, error) {
	targets := &optimizer.LatencyTargets{}

	if targetLatency.EndToEndLatencyMilliseconds != nil {
		e2e := float64(*targetLatency.EndToEndLatencyMilliseconds)
		targets.E2E = &e2e
	}
	if targetLatency.TimeToFirstTokenLatencyMilliseconds != nil {
		ttft := float64(*targetLatency.TimeToFirstTokenLatencyMilliseconds)
		targets.TTFT = &ttft
	}
	if targetLatency.InterTokenLatencyMilliseconds != nil {
		itl := float64(*targetLatency.InterTokenLatencyMilliseconds)
		targets.ITL = &itl
	}

	// If no targets specified, return error - caller should use defaults
	if targets.E2E == nil && targets.TTFT == nil && targets.ITL == nil {
		return nil, fmt.Errorf("no latency target configured")
	}

	return targets, nil
}

// buildResourceChangeReason builds a detailed reason string for resource changes
func (s *ResourceScaler) buildResourceChangeReason(decision *ScalingDecision, changeType string) string {
	changes := []string{}

	if decision.CurrentResourceConfiguration.GPUsPerReplica != decision.DesiredResourceConfiguration.GPUsPerReplica {
		changes = append(changes, fmt.Sprintf("GPUs per replica from %d to %d",
			decision.CurrentResourceConfiguration.GPUsPerReplica, decision.DesiredResourceConfiguration.GPUsPerReplica))
	}

	const resourceChangeThreshold = 0.01
	if math.Abs(decision.CurrentResourceConfiguration.RequestedCompute-decision.DesiredResourceConfiguration.RequestedCompute) > resourceChangeThreshold {
		changes = append(changes, fmt.Sprintf("compute from %.1f%% to %.1f%%",
			decision.CurrentResourceConfiguration.RequestedCompute, decision.DesiredResourceConfiguration.RequestedCompute))
	}

	if math.Abs(decision.CurrentResourceConfiguration.RequestedMemoryGB-decision.DesiredResourceConfiguration.RequestedMemoryGB) > (decision.CurrentResourceConfiguration.RequestedMemoryGB * resourceChangeThreshold) {
		changes = append(changes, fmt.Sprintf("memory from %.2f GB to %.2f GB",
			decision.CurrentResourceConfiguration.RequestedMemoryGB, decision.DesiredResourceConfiguration.RequestedMemoryGB))
	}

	if len(changes) == 0 {
		return fmt.Sprintf("Resource %s", changeType)
	}

	return fmt.Sprintf("Resource %s: %s", changeType, strings.Join(changes, ", "))
}

// applyScalingAction applies the scaling action based on optimal configuration
func (s *ResourceScaler) applyScalingAction(ctx context.Context, name, namespace string, config *optimizer.OptimalConfiguration, arrivalRate float64, metrics map[string]string, itlOffset, ttftOffset float64) error {
	logger := s.logger.WithValues("autoscaler", name, "namespace", namespace)

	// Fetch the autoscaler to get full spec and update status
	autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, autoscaler); err != nil {
		logger.Error(err, "Failed to fetch autoscaler for scaling action")
		return err
	}

	// Compare measured latencies from metrics with previously calculated latencies from last round
	measuredITL := s.getInterTokenLatency(metrics)
	measuredTTFT := s.getTimeToFirstToken(metrics)
	currentCalculatedITL := config.EstimatedLatency.ITL
	currentCalculatedTTFT := config.EstimatedLatency.TTFT

	if autoscaler.Status.ScalingResults != nil &&
		autoscaler.Status.ScalingResults.LastEstimation != nil {

		// Compare ITL
		if autoscaler.Status.ScalingResults.LastEstimation.InterTokenLatencyMilliseconds != nil {
			previousCalculatedITL := float64(*autoscaler.Status.ScalingResults.LastEstimation.InterTokenLatencyMilliseconds)

			if measuredITL > 0 && previousCalculatedITL > 0 {
				itlDiff := measuredITL - previousCalculatedITL
				itlDiffPercent := (itlDiff / previousCalculatedITL) * 100.0

				logger.Info("ITL comparison: measured vs previous calculated",
					"measuredITL_ms", measuredITL,
					"previousCalculatedITL_ms", previousCalculatedITL,
					"currentCalculatedITL_ms", currentCalculatedITL,
					"difference_ms", itlDiff,
					"difference_percent", fmt.Sprintf("%.2f%%", itlDiffPercent))

				// Log warning if difference is significant (>20%)
				if math.Abs(itlDiffPercent) > 20.0 {
					logger.Info("WARNING: Significant ITL mismatch detected",
						"measuredITL_ms", measuredITL,
						"previousCalculatedITL_ms", previousCalculatedITL,
						"difference_ms", itlDiff,
						"difference_percent", fmt.Sprintf("%.2f%%", itlDiffPercent),
						"threshold", "20%")
				}
			} else if measuredITL == 0 {
				logger.V(1).Info("No measured ITL available from metrics for comparison",
					"previousCalculatedITL_ms", previousCalculatedITL,
					"currentCalculatedITL_ms", currentCalculatedITL)
			}
		}

		// Compare TTFT
		if autoscaler.Status.ScalingResults.LastEstimation.TimeToFirstTokenMilliseconds != nil {
			previousCalculatedTTFT := float64(*autoscaler.Status.ScalingResults.LastEstimation.TimeToFirstTokenMilliseconds)

			if measuredTTFT > 0 && previousCalculatedTTFT > 0 {
				ttftDiff := measuredTTFT - previousCalculatedTTFT
				ttftDiffPercent := (ttftDiff / previousCalculatedTTFT) * 100.0

				logger.Info("TTFT comparison: measured vs previous calculated",
					"measuredTTFT_ms", measuredTTFT,
					"previousCalculatedTTFT_ms", previousCalculatedTTFT,
					"currentCalculatedTTFT_ms", currentCalculatedTTFT,
					"difference_ms", ttftDiff,
					"difference_percent", fmt.Sprintf("%.2f%%", ttftDiffPercent))

				// Log warning if difference is significant (>20%)
				if math.Abs(ttftDiffPercent) > 20.0 {
					logger.Info("WARNING: Significant TTFT mismatch detected",
						"measuredTTFT_ms", measuredTTFT,
						"previousCalculatedTTFT_ms", previousCalculatedTTFT,
						"difference_ms", ttftDiff,
						"difference_percent", fmt.Sprintf("%.2f%%", ttftDiffPercent),
						"threshold", "20%")
				}
			} else if measuredTTFT == 0 {
				logger.V(1).Info("No measured TTFT available from metrics for comparison",
					"previousCalculatedTTFT_ms", previousCalculatedTTFT,
					"currentCalculatedTTFT_ms", currentCalculatedTTFT)
			}
		}
	} else {
		logger.V(1).Info("No previous calculated latencies available for comparison",
			"measuredITL_ms", measuredITL,
			"measuredTTFT_ms", measuredTTFT,
			"currentCalculatedITL_ms", currentCalculatedITL,
			"currentCalculatedTTFT_ms", currentCalculatedTTFT)
	}

	// Determine what scaling action is needed
	decision := s.determineScalingAction(ctx, autoscaler, config, arrivalRate)

	// Check if any change is needed
	if decision.Action == ScalingActionNone {
		logger.V(1).Info("No scaling needed",
			"currentReplicas", decision.CurrentResourceConfiguration.Replicas,
			"desiredReplicas", decision.DesiredResourceConfiguration.Replicas,
			"reason", decision.Reason)
		return nil
	}

	logger.Info("Scaling action determined",
		"action", decision.Action,
		"replicaChange", decision.ReplicaChange,
		"resourceChange", decision.ResourceChange,
		"reason", decision.Reason)

	// Check grace period
	if shouldWaitForGracePeriod := s.checkGracePeriod(autoscaler, decision, logger); shouldWaitForGracePeriod {
		return nil
	}

	logger.Info("Applying scaling action",
		"action", decision.Action,
		"currentReplicas", decision.CurrentResourceConfiguration.Replicas,
		"desiredReplicas", decision.DesiredResourceConfiguration.Replicas,
		"currentGPUsPerReplica", decision.CurrentResourceConfiguration.GPUsPerReplica,
		"desiredGPUsPerReplica", decision.DesiredResourceConfiguration.GPUsPerReplica,
		"currentCompute", decision.CurrentResourceConfiguration.RequestedCompute,
		"desiredCompute", decision.DesiredResourceConfiguration.RequestedCompute,
		"currentMemoryGB", decision.CurrentResourceConfiguration.RequestedMemoryGB,
		"desiredMemoryGB", decision.DesiredResourceConfiguration.RequestedMemoryGB)

	// Update status before applying changes, including cumulative offsets
	if err := s.updateScalingStatus(ctx, autoscaler, decision, config, itlOffset, ttftOffset); err != nil {
		return err
	}

	// Apply the actual scaling changes
	if err := s.executeScalingChanges(ctx, autoscaler, decision, logger); err != nil {
		return err
	}

	logger.Info("Scaling action applied successfully")
	return nil
}

// checkGracePeriod verifies if the grace period has elapsed since last scaling action
func (s *ResourceScaler) checkGracePeriod(autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, decision *ScalingDecision, logger logr.Logger) bool {
	effectiveBehavior := GetEffectiveBehavior(&autoscaler.Spec)
	var gracePeriod time.Duration
	if decision.Action == ScalingActionScaleUp {
		gracePeriod = time.Duration(effectiveBehavior.ScaleUp.GracePeriodSeconds) * time.Second
	} else {
		gracePeriod = time.Duration(effectiveBehavior.ScaleDown.GracePeriodSeconds) * time.Second
	}

	if autoscaler.Status.ScalingStatus != nil && autoscaler.Status.ScalingStatus.LastApplyTime != nil {
		elapsed := time.Since(autoscaler.Status.ScalingStatus.LastApplyTime.Time)
		if elapsed < gracePeriod {
			logger.V(1).Info("Grace period not elapsed", "elapsed", elapsed, "gracePeriod", gracePeriod)
			return true
		}
	}

	return false
}

// updateScalingStatus updates the autoscaler status with scaling information
func (s *ResourceScaler) updateScalingStatus(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, decision *ScalingDecision, config *optimizer.OptimalConfiguration, itlOffset, ttftOffset float64) error {
	now := metav1.Now()
	if autoscaler.Status.ScalingStatus == nil {
		autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
	}
	desiredReplicas := int32(decision.DesiredResourceConfiguration.Replicas)
	autoscaler.Status.ScalingStatus.DesiredReplicas = &desiredReplicas
	autoscaler.Status.ScalingStatus.LastApplyTime = &now

	// Update scaling results with latency estimation
	if autoscaler.Status.ScalingResults == nil {
		autoscaler.Status.ScalingResults = &autoscalingv1alpha1.ScalingResults{}
	}
	autoscaler.Status.ScalingResults.LastEstimationTime = &now
	if config.EstimatedLatency.E2E > 0.0 {
		// Ensure latency values are non-negative (validation requirement)
		ttft := int32(config.EstimatedLatency.TTFT)
		if ttft < 0 {
			ttft = 0
		}
		itl := int32(config.EstimatedLatency.ITL)
		if itl < 0 {
			itl = 0
		}

		// Store cumulative offsets
		itlOffsetInt := int32(itlOffset)
		ttftOffsetInt := int32(ttftOffset)

		autoscaler.Status.ScalingResults.LastEstimation = &autoscalingv1alpha1.LatencyEstimation{
			TimeToFirstTokenMilliseconds:        &ttft,
			InterTokenLatencyMilliseconds:       &itl,
			TimeToFirstTokenOffsetMilliseconds:  &ttftOffsetInt,
			InterTokenLatencyOffsetMilliseconds: &itlOffsetInt,
		}
	}

	// Update status condition with detailed message
	conditionMessage := decision.Reason
	if decision.ReplicaChange && decision.ResourceChange {
		conditionMessage = fmt.Sprintf("%s (replicas: %d→%d, resources also changed)",
			decision.Reason, decision.CurrentResourceConfiguration.Replicas, decision.DesiredResourceConfiguration.Replicas)
	} else if decision.ReplicaChange {
		conditionMessage = fmt.Sprintf("%s (replicas: %d→%d)",
			decision.Reason, decision.CurrentResourceConfiguration.Replicas, decision.DesiredResourceConfiguration.Replicas)
	}

	meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
		Type:               "ScalingActive",
		Status:             metav1.ConditionTrue,
		Reason:             "ScalingInProgress",
		Message:            conditionMessage,
		ObservedGeneration: autoscaler.Generation,
	})

	return s.client.Status().Update(ctx, autoscaler)
}

// executeScalingChanges applies the actual scaling changes (resources and/or replicas)
func (s *ResourceScaler) executeScalingChanges(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, decision *ScalingDecision, logger logr.Logger) error {
	if decision.ResourceChange {
		return s.applyResourceChanges(ctx, autoscaler, decision, logger)
	} else if decision.ReplicaChange {
		return s.applyReplicaChanges(ctx, autoscaler, decision, logger)
	}
	return nil
}

// applyResourceChanges handles resource claim template patching
func (s *ResourceScaler) applyResourceChanges(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, decision *ScalingDecision, logger logr.Logger) error {
	// Reset ready and available replicas to zero since pods will be recreated with new resources
	if autoscaler.Status.ScalingStatus == nil {
		autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
	}
	zero := int32(0)
	autoscaler.Status.ScalingStatus.ReadyReplicas = &zero
	autoscaler.Status.ScalingStatus.AvailableReplicas = &zero

	// Create new ResourceClaimTemplate with updated resources and revision suffix
	newTemplateName, err := s.patchResourceClaimTemplate(ctx, autoscaler, decision.DesiredResourceConfiguration)
	if err != nil {
		logger.Error(err, "Failed to patch ResourceClaimTemplate")
		return fmt.Errorf("failed to patch ResourceClaimTemplate: %w", err)
	}

	// Update the pod owner (Deployment/StatefulSet) to use the new template
	if err := s.patchPodOwner(ctx, autoscaler, newTemplateName, int32(decision.DesiredResourceConfiguration.Replicas)); err != nil {
		logger.Error(err, "Failed to patch pod owner")
		return fmt.Errorf("failed to patch pod owner: %w", err)
	}

	// Update status with the new template name and reset replica counts
	autoscaler.Status.ScalingStatus.DesiredClaimTemplate = newTemplateName

	// Update status again with the new template name and reset counts
	if err := s.client.Status().Update(ctx, autoscaler); err != nil {
		logger.Error(err, "Failed to update status with new template name")
	}

	logger.Info("Resource claim patching completed",
		"newTemplate", newTemplateName,
		"replicas", decision.DesiredResourceConfiguration.Replicas,
		"readyReplicas", 0,
		"availableReplicas", 0)

	return nil
}

// calculateLatencyOffsets calculates the offset between measured and previously calculated latencies
// Returns cumulative ITL offset and TTFT offset in milliseconds (including previous offsets)
func (s *ResourceScaler) calculateLatencyOffsets(ctx context.Context, name, namespace string, metrics map[string]string, logger logr.Logger) (float64, float64) {
	var newItlOffset, newTtftOffset float64
	var previousItlOffset, previousTtftOffset float64

	// Get measured latencies from metrics
	measuredITL := s.getInterTokenLatency(metrics)
	measuredTTFT := s.getTimeToFirstToken(metrics)

	// Fetch the autoscaler to get previous calculated latencies and offsets
	autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, autoscaler); err != nil {
		logger.V(1).Info("Failed to fetch autoscaler for latency offset calculation", "error", err)
		return 0, 0
	}

	if autoscaler.Status.ScalingResults == nil ||
		autoscaler.Status.ScalingResults.LastEstimation == nil {
		logger.V(1).Info("No previous latency estimation available for offset calculation")
		return 0, 0
	}

	lastEstimation := autoscaler.Status.ScalingResults.LastEstimation

	// Get previous cumulative offsets
	if lastEstimation.InterTokenLatencyOffsetMilliseconds != nil {
		previousItlOffset = float64(*lastEstimation.InterTokenLatencyOffsetMilliseconds)
	}
	if lastEstimation.TimeToFirstTokenOffsetMilliseconds != nil {
		previousTtftOffset = float64(*lastEstimation.TimeToFirstTokenOffsetMilliseconds)
	}

	// Calculate new ITL offset
	if lastEstimation.InterTokenLatencyMilliseconds != nil && measuredITL > 0 {
		previousCalculatedITL := float64(*lastEstimation.InterTokenLatencyMilliseconds)
		if previousCalculatedITL > 0 {
			// The stored value already includes the previous offset, so we need to subtract it
			// to get the raw calculated value for comparison
			rawPreviousCalculatedITL := previousCalculatedITL - previousItlOffset
			newItlOffset = measuredITL - rawPreviousCalculatedITL

			// Only apply positive offsets (when measured > calculated)
			if newItlOffset < 0 {
				newItlOffset = 0
			}

			itlDiffPercent := (newItlOffset / rawPreviousCalculatedITL) * 100.0

			// Cumulative offset includes previous offset
			cumulativeItlOffset := previousItlOffset + newItlOffset

			logger.Info("ITL offset calculated",
				"measuredITL_ms", measuredITL,
				"storedCalculatedITL_ms", previousCalculatedITL,
				"rawCalculatedITL_ms", rawPreviousCalculatedITL,
				"previousOffset_ms", previousItlOffset,
				"newOffset_ms", newItlOffset,
				"cumulativeOffset_ms", cumulativeItlOffset,
				"offset_percent", fmt.Sprintf("%.2f%%", itlDiffPercent))

			if math.Abs(itlDiffPercent) > 20.0 {
				logger.Info("WARNING: Significant ITL mismatch detected",
					"offset_percent", fmt.Sprintf("%.2f%%", itlDiffPercent),
					"threshold", "20%")
			}

			return cumulativeItlOffset, previousTtftOffset + (measuredTTFT - (float64(*lastEstimation.TimeToFirstTokenMilliseconds) - previousTtftOffset))
		}
	}

	// Calculate new TTFT offset
	if lastEstimation.TimeToFirstTokenMilliseconds != nil && measuredTTFT > 0 {
		previousCalculatedTTFT := float64(*lastEstimation.TimeToFirstTokenMilliseconds)
		if previousCalculatedTTFT > 0 {
			// The stored value already includes the previous offset, so we need to subtract it
			rawPreviousCalculatedTTFT := previousCalculatedTTFT - previousTtftOffset
			newTtftOffset = measuredTTFT - rawPreviousCalculatedTTFT

			// Only apply positive offsets (when measured > calculated)
			if newTtftOffset < 0 {
				newTtftOffset = 0
			}

			ttftDiffPercent := (newTtftOffset / rawPreviousCalculatedTTFT) * 100.0

			// Cumulative offset includes previous offset
			cumulativeTtftOffset := previousTtftOffset + newTtftOffset

			logger.Info("TTFT offset calculated",
				"measuredTTFT_ms", measuredTTFT,
				"storedCalculatedTTFT_ms", previousCalculatedTTFT,
				"rawCalculatedTTFT_ms", rawPreviousCalculatedTTFT,
				"previousOffset_ms", previousTtftOffset,
				"newOffset_ms", newTtftOffset,
				"cumulativeOffset_ms", cumulativeTtftOffset,
				"offset_percent", fmt.Sprintf("%.2f%%", ttftDiffPercent))

			if math.Abs(ttftDiffPercent) > 20.0 {
				logger.Info("WARNING: Significant TTFT mismatch detected",
					"offset_percent", fmt.Sprintf("%.2f%%", ttftDiffPercent),
					"threshold", "20%")
			}

			return previousItlOffset + (measuredITL - (float64(*lastEstimation.InterTokenLatencyMilliseconds) - previousItlOffset)), cumulativeTtftOffset
		}
	}

	return previousItlOffset, previousTtftOffset
}

// applyReplicaChanges handles replica count updates
func (s *ResourceScaler) applyReplicaChanges(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, decision *ScalingDecision, logger logr.Logger) error {
	if autoscaler.Status.Discovery == nil || autoscaler.Status.Discovery.Owner == nil {
		return nil
	}

	owner := autoscaler.Status.Discovery.Owner
	originalTemplateName := autoscaler.Status.Discovery.ResourceClaim

	// Get current replica status before patching
	if err := s.updateReplicaStatusFromOwner(ctx, autoscaler, owner); err != nil {
		logger.Error(err, "Failed to get current replica status")
		// Continue with patching even if status update fails
	}

	switch owner.Kind {
	case WorkloadKindDeployment:
		if err := s.patchDeployment(ctx, autoscaler.Namespace, owner.Name, originalTemplateName, originalTemplateName, int32(decision.DesiredResourceConfiguration.Replicas), logger); err != nil {
			logger.Error(err, "Failed to patch Deployment replicas")
			return fmt.Errorf("failed to patch Deployment replicas: %w", err)
		}
	case WorkloadKindStatefulSet:
		if err := s.patchStatefulSet(ctx, autoscaler.Namespace, owner.Name, originalTemplateName, originalTemplateName, int32(decision.DesiredResourceConfiguration.Replicas), logger); err != nil {
			logger.Error(err, "Failed to patch StatefulSet replicas")
			return fmt.Errorf("failed to patch StatefulSet replicas: %w", err)
		}
	default:
		logger.Info("Unsupported owner kind for replica scaling", "kind", owner.Kind)
	}

	logger.Info("Replica count updated",
		"replicas", decision.DesiredResourceConfiguration.Replicas,
		"currentReady", autoscaler.Status.ScalingStatus.ReadyReplicas,
		"currentAvailable", autoscaler.Status.ScalingStatus.AvailableReplicas)

	return nil
}

// updateReplicaStatusFromOwner updates the autoscaler status with current ready and available replica counts
func (s *ResourceScaler) updateReplicaStatusFromOwner(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, owner *metav1.OwnerReference) error {
	if autoscaler.Status.ScalingStatus == nil {
		autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
	}

	switch owner.Kind {
	case WorkloadKindDeployment:
		deployment := &appsv1.Deployment{}
		if err := s.client.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: autoscaler.Namespace}, deployment); err != nil {
			return fmt.Errorf("failed to get Deployment: %w", err)
		}
		autoscaler.Status.ScalingStatus.ReadyReplicas = &deployment.Status.ReadyReplicas
		autoscaler.Status.ScalingStatus.AvailableReplicas = &deployment.Status.AvailableReplicas

	case WorkloadKindStatefulSet:
		statefulSet := &appsv1.StatefulSet{}
		if err := s.client.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: autoscaler.Namespace}, statefulSet); err != nil {
			return fmt.Errorf("failed to get StatefulSet: %w", err)
		}
		autoscaler.Status.ScalingStatus.ReadyReplicas = &statefulSet.Status.ReadyReplicas
		autoscaler.Status.ScalingStatus.AvailableReplicas = &statefulSet.Status.AvailableReplicas

	default:
		return fmt.Errorf("unsupported owner kind: %s", owner.Kind)
	}

	// Update the autoscaler status
	if err := s.client.Status().Update(ctx, autoscaler); err != nil {
		return fmt.Errorf("failed to update autoscaler status: %w", err)
	}

	return nil
}

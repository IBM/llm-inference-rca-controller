package optimizer

import (
	"fmt"
	"math"

	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// GPUResourceConstraints defines GPU resource limits
type GPUResourceConstraints struct {
	// MinThreadPercentage is the minimum GPU thread utilization per GPU (0-100)
	MinThreadPercentage float64
	// MaxThreadPercentage is the maximum GPU thread utilization per GPU (0-100)
	MaxThreadPercentage float64
	// StepThreadPercentage is the step size for compute reduction (default: 1.0%)
	StepThreadPercentage float64
	// MinMemoryBytes is the minimum GPU memory per GPU in bytes
	MinMemoryBytes int64
	// MaxMemoryBytes is the maximum GPU memory per GPU in bytes
	MaxMemoryBytes int64
	// StepMemoryBytes is the step size for memory reduction (default: 1GB)
	StepMemoryBytes int64
	// TotalGPUs is the total number of GPUs available across all replicas (0 = unlimited)
	TotalGPUs int

	MaxGPUsPerReplica int
}

// OptimalConfiguration represents an optimal system configuration
type OptimalConfiguration struct {
	ResourceConfiguration
	TotalGPUs            int // Total GPUs used (Replicas × GPUsPerReplica)
	EstimatedLatency     LatencyResult
	ResourceRequirements ResourceRequirements
	MeetsConstraints     bool
}

type ResourceConfiguration struct {
	Replicas          int
	GPUsPerReplica    int     // Number of GPUs allocated per replica
	RequestedCompute  float64 // Compute per GPU
	RequestedMemoryGB float64 // Memory per GPU
}

type SqueezeSearchResult struct {
	BestConfiguration *OptimalConfiguration
	LastTested        *OptimalConfiguration
	LastError         string
}

// ResourceOptimizer finds optimal configurations considering GPU constraints
type ResourceOptimizer struct {
	// Queue Analyzer
	queueAnalyzer *QueueAnalyzer

	// Latency estimator
	latencyEstimator *LatencyEstimator

	// Resource estimator
	resourceEstimator *ResourcEstimator

	// Maximum replicas allowed
	maxReplicas int

	prefixCacheHitRate float64

	// GPU constraints
	constraints GPUResourceConstraints

	// Current resource configuration
	currentConfig ResourceConfiguration

	// Workload parameters
	arrivalRate     float64
	avgPromptTokens int
	avgOutputTokens int
	maxNumSeqs      int

	// Logger
	logger logr.Logger
}

func NewResourceOptimizer(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, slogger logr.Logger) *ResourceOptimizer {
	logger := slogger.WithName("ResourceOptimizer")

	// Get hint parameters for latency estimator
	hint := spec.Target.Hint
	if hint == nil {
		logger.V(1).Info("No hint configured, using default hint")
		hint = getDefaultHint()
	}

	resourceEstimator := NewResourceEstimatorWithHint(*hint, slogger)
	latencyEstimator := NewLatencyEstimatorWithHint(*hint, resourceEstimator.params, resourceEstimator.gpuSpec, slogger)
	return &ResourceOptimizer{
		resourceEstimator:  resourceEstimator,
		latencyEstimator:   latencyEstimator,
		maxReplicas:        getMaxReplicas(spec.Constraints),
		prefixCacheHitRate: hint.PrefixCacheHitRate,
		constraints:        extractGPUConstraints(spec),
		logger:             logger,
	}
}

// SetWorkloadParameters sets the workload parameters for optimization
func (o *ResourceOptimizer) SetWorkloadParameters(arrivalRate float64, avgPromptTokens, avgOutputTokens, maxNumSeqs int) {
	o.arrivalRate = arrivalRate
	o.avgPromptTokens = avgPromptTokens
	o.avgOutputTokens = avgOutputTokens
	o.maxNumSeqs = maxNumSeqs
	o.queueAnalyzer = NewQueueAnalyzer(arrivalRate)
}

// SetLatencyOffsets sets the latency offsets for tuning based on measured vs calculated differences
func (o *ResourceOptimizer) SetLatencyOffsets(itlOffset, ttftOffset float64) {
	if o.latencyEstimator != nil {
		o.latencyEstimator.SetLatencyOffsets(itlOffset, ttftOffset)
		o.logger.V(1).Info("Latency offsets set in optimizer",
			"itlOffset_ms", itlOffset,
			"ttftOffset_ms", ttftOffset)
	}
}

func (o *ResourceOptimizer) TuneWithMetrics(prefillTokens float64, promptLen, outputTokens int, cachedHitRatio float64, computeThreadUtilization, measuredMemoryUsage float64, numGPU int) {
	o.logger.V(1).Info("TuneWithMetrics",
		"prefillTokens", prefillTokens,
		"promptLen", promptLen,
		"outputTokens", outputTokens,
		"cachedHitRatio", cachedHitRatio,
		"computeThreadUtilization", computeThreadUtilization,
		"measuredMemoryUsage", measuredMemoryUsage,
		"numGPU", numGPU)
	o.resourceEstimator.tuneWithComputeMetric(prefillTokens, promptLen, cachedHitRatio, computeThreadUtilization, numGPU)
	o.resourceEstimator.tuneWithMemoryMetric(prefillTokens, promptLen, outputTokens, cachedHitRatio, measuredMemoryUsage, numGPU)
}

// SetRequestedResources sets the original requested resources
func (o *ResourceOptimizer) SetRequestedResources(replicas int, gpusPerReplica int, requestedCompute float64, requestedMemoryGB float64) {
	o.currentConfig = ResourceConfiguration{
		Replicas:          replicas,
		GPUsPerReplica:    gpusPerReplica,
		RequestedCompute:  requestedCompute,
		RequestedMemoryGB: requestedMemoryGB,
	}
}

// LatencyTargets contains all specified latency targets for validation
type LatencyTargets struct {
	E2E  *float64 // End-to-end latency target in ms
	TTFT *float64 // Time to first token target in ms
	ITL  *float64 // Inter-token latency target in ms
}

// meetsAllTargets checks if an estimate meets all specified latency targets
func (t *LatencyTargets) meetsAllTargets(result LatencyResult) bool {
	if t == nil {
		return true
	}
	if t.E2E != nil && result.E2E > *t.E2E {
		return false
	}
	if t.TTFT != nil && result.TTFT > *t.TTFT {
		return false
	}
	if t.ITL != nil && result.ITL > *t.ITL {
		return false
	}
	return true
}

// FindOptimalConfiguration finds the best configuration for given requirements.
//
// New Flow:
// 1. Use resource_estimator to compute resource requirements and contentionImpact
// 2. Use latency_estimator with contentionImpact to estimate latency
// 3. Use queue analyzer to compute total latency with queueing
// 4. Find optimal solution that meets all targets
func (o *ResourceOptimizer) FindOptimalConfiguration(
	arrivalRate float64,
	targets *LatencyTargets,
) (*OptimalConfiguration, error) {
	o.arrivalRate = arrivalRate
	o.queueAnalyzer = NewQueueAnalyzer(arrivalRate)

	bestConfig := &OptimalConfiguration{
		MeetsConstraints: false,
	}

	var lastTestedConfig *OptimalConfiguration
	var lastError string

	// Try each replica count
	for replicas := 1; replicas <= o.maxReplicas; replicas++ {
		o.logger.V(1).Info("Trying replica configuration", "replicas", replicas, "maxReplicas", o.maxReplicas, "maxGPUsPerReplica", o.constraints.MaxGPUsPerReplica, "totalGPUs", o.constraints.TotalGPUs)

		// Determine max GPUs per replica based on total GPU budget
		maxGPUsPerReplica := o.constraints.MaxGPUsPerReplica // Use the constraint
		if o.constraints.TotalGPUs > 0 {
			// Calculate max GPUs per replica based on total budget
			budgetBasedMax := o.constraints.TotalGPUs / replicas
			// Use the minimum of constraint and budget-based max
			if budgetBasedMax < maxGPUsPerReplica {
				maxGPUsPerReplica = budgetBasedMax
			}
			if maxGPUsPerReplica < 1 {
				o.logger.V(1).Info("Stopping search: insufficient GPUs for more replicas",
					"replicas", replicas,
					"totalGPUs", o.constraints.TotalGPUs,
					"maxGPUsPerReplica", maxGPUsPerReplica)
				goto searchComplete
			}
		}

		// Try increasing GPUs per replica (vertical scaling first)
		for gpusPerReplica := 1; gpusPerReplica <= maxGPUsPerReplica; gpusPerReplica++ {
			totalGPUs := replicas * gpusPerReplica
			if o.constraints.TotalGPUs > 0 && totalGPUs > o.constraints.TotalGPUs {
				break
			}

			resourceReq := o.resourceEstimator.CalculateResourceRequirementsPerGPU(
				o.avgPromptTokens,
				o.avgOutputTokens,
				o.prefixCacheHitRate,
				gpusPerReplica,
			)

			requiredMemoryBytes := math.Ceil(resourceReq.MemoryGB * 1e9)
			minRequestedMemoryBytesPerGPU := int64(requiredMemoryBytes)

			if minRequestedMemoryBytesPerGPU > o.constraints.MaxMemoryBytes {
				// Require more than max memory allowed by the constraints
				o.logger.V(1).Info("Skipping configuration: memory requirement exceeds max",
					"replicas", replicas,
					"gpusPerReplica", gpusPerReplica,
					"requiredMemoryGB", resourceReq.MemoryGB,
					"maxMemoryGB", float64(o.constraints.MaxMemoryBytes)/(1024*1024*1024))
				continue
			}

			// Start with maximum resources per GPU
			maxRequestedCompute := o.constraints.MaxThreadPercentage
			maxRequestedMemoryBytes := o.constraints.MaxMemoryBytes

			// First, try the maximum configuration
			config, err := o.evaluateConfiguration(replicas, gpusPerReplica, maxRequestedCompute, maxRequestedMemoryBytes, resourceReq, targets)
			lastTestedConfig = config
			if err != nil {
				lastError = err.Error()
				o.logger.V(1).Info("Configuration evaluation failed",
					"replicas", replicas,
					"gpusPerReplica", gpusPerReplica,
					"compute", maxRequestedCompute,
					"memoryGB", float64(maxRequestedMemoryBytes)/(1024*1024*1024),
					"error", err.Error())
			}
			if err == nil && config.MeetsConstraints {
				// Calculate minimum memory aligned with policy: min + x * step
				minRequestedMemoryBytes := math.Max(requiredMemoryBytes, float64(o.constraints.MinMemoryBytes))
				minRequestedCompute := math.Max(resourceReq.ThreadOccupancy, o.constraints.MinThreadPercentage)
				// Align to step boundary: round up to nearest step
				stepsNeeded := math.Ceil((minRequestedMemoryBytes - float64(o.constraints.MinMemoryBytes)) / float64(o.constraints.StepMemoryBytes))
				alignedMinMemoryBytes := int64(float64(o.constraints.MinMemoryBytes) + stepsNeeded*float64(o.constraints.StepMemoryBytes))

				// First, try with aligned minimum memory and max compute
				minMemConfig, err := o.evaluateConfiguration(replicas, gpusPerReplica, maxRequestedCompute, alignedMinMemoryBytes, resourceReq, targets)
				if err != nil || !minMemConfig.MeetsConstraints {
					// If aligned minimum memory doesn't work, keep the max memory config
					bestConfig = config
					goto searchComplete
				}
				bestConfig = minMemConfig

				// Try shrinking compute to find minimal resources
				for requestedCompute := maxRequestedCompute - o.constraints.StepThreadPercentage; requestedCompute >= minRequestedCompute; requestedCompute -= o.constraints.StepThreadPercentage {
					shrinkConfig, err := o.evaluateConfiguration(replicas, gpusPerReplica, requestedCompute, alignedMinMemoryBytes, resourceReq, targets)
					if err != nil || !shrinkConfig.MeetsConstraints {
						goto searchComplete
					}
					bestConfig = shrinkConfig
				}
				goto searchComplete
			}
		}
	}

searchComplete:
	if bestConfig.Replicas != 0 {
		return bestConfig, nil
	}

	// If no configuration found within search space, assign max replica configuration
	if lastTestedConfig != nil {
		o.logger.Info("No optimal configuration found, using max replica configuration",
			"maxReplicas", o.maxReplicas,
			"replicas", lastTestedConfig.Replicas,
			"gpusPerReplica", lastTestedConfig.GPUsPerReplica,
			"compute", lastTestedConfig.RequestedCompute,
			"memoryGB", lastTestedConfig.RequestedMemoryGB,
			"estimatedE2E", lastTestedConfig.EstimatedLatency.E2E,
			"meetsConstraints", lastTestedConfig.MeetsConstraints)

		// Return the last tested configuration (which should be at max replicas)
		return lastTestedConfig, nil
	}

	// Fallback: if no configuration was tested at all, return error
	errorMsg := fmt.Sprintf("no configuration found within search space up to %d replicas", o.maxReplicas)
	if lastError != "" {
		errorMsg += fmt.Sprintf("\nLast error: %s", lastError)
	}

	return nil, fmt.Errorf("%s", errorMsg)
}

// evaluateConfiguration evaluates a specific configuration using the new flow:
// 1. resource_estimator → compute contentionImpact
// 2. latency_estimator → estimate latency with contentionImpact
// 3. queue analyzer → compute total latency
func (o *ResourceOptimizer) evaluateConfiguration(
	replicas int,
	gpusPerReplica int,
	requestedComputePerGPU float64,
	requestedMemoryBytesPerGPU int64,
	resourceReq ResourceRequirements,
	targets *LatencyTargets,
) (*OptimalConfiguration, error) {
	ratio := resourceReq.ThreadOccupancy / requestedComputePerGPU
	alllocationImpact := o.CalculateResourceAllocationImpact(ratio)

	// Use latency_estimator with contentionImpact to estimate latency
	latencyResult := o.latencyEstimator.estimateLatency(
		o.avgPromptTokens,
		o.avgOutputTokens,
		gpusPerReplica,
		alllocationImpact,
	)

	// Create config object first so we can return it even if queue analysis fails
	config := &OptimalConfiguration{
		ResourceConfiguration: ResourceConfiguration{
			Replicas:          replicas,
			GPUsPerReplica:    gpusPerReplica,
			RequestedCompute:  requestedComputePerGPU,
			RequestedMemoryGB: float64(requestedMemoryBytesPerGPU) / (1024 * 1024 * 1024),
		},
		TotalGPUs:            replicas * gpusPerReplica,
		EstimatedLatency:     latencyResult,
		ResourceRequirements: resourceReq,
		MeetsConstraints:     false,
	}

	// Step 3: Use queue analyzer to compute total latency with queueing
	err := o.queueAnalyzer.AddQueuingResults(replicas, &latencyResult)
	if err != nil {
		// Return the config even on error so caller can track what was tested
		config.EstimatedLatency = latencyResult
		return config, err
	}

	// Update latency result after queue analysis
	config.EstimatedLatency = latencyResult

	// Check if configuration meets targets
	meetsTargets := targets.meetsAllTargets(latencyResult)
	config.MeetsConstraints = meetsTargets

	return config, nil
}

// String returns a formatted summary of the optimal configuration
func (c *OptimalConfiguration) String() string {
	status := "✓ VALID"
	if !c.MeetsConstraints {
		status = "✗ INVALID"
	}

	gpuInfo := ""
	if c.GPUsPerReplica > 1 {
		gpuInfo = fmt.Sprintf(`
	 GPU Allocation:
	 GPUs per Replica:     %d
	 Total GPUs:           %d
	 `, c.GPUsPerReplica, c.TotalGPUs)
	}

	result := fmt.Sprintf(`Optimal Configuration [%s]:
	 Replicas:             %d
	 %s
	 Requested Allocation per Replica:
	 Compute:              %.0f%%
	 Memory:               %.2f GB
	 
	 Estimated Working Set per Replica:
	 Thread Utilization:   %.1f%%
	 Memory Usage:         %.2f GB
	 
	 Performance:
	 Total Latency:        %.2f ms
	 TTFT Latency:         %.2f ms
	 ITL Latency:          %.2f ms
	 Service Time:         %.2f ms
	 Allocation Impact:    %.2f%%
	 `,
		status,
		c.Replicas,
		gpuInfo,
		c.RequestedCompute,
		c.RequestedMemoryGB,
		c.ResourceRequirements.ThreadOccupancy,
		c.ResourceRequirements.MemoryGB,
		c.EstimatedLatency.E2E,
		c.EstimatedLatency.TTFT,
		c.EstimatedLatency.ITL,
		c.EstimatedLatency.ServiceTime(),
		c.EstimatedLatency.AllocationImpact)
	return result
}

// getMaxReplicas extracts max replicas from constraints
func getMaxReplicas(constraints *autoscalingv1alpha1.Constraints) int {
	maxReplicas := 10 // Default
	if constraints != nil && constraints.MaxReplicas != nil {
		maxReplicas = int(*constraints.MaxReplicas)
	}
	return maxReplicas
}

// getDefaultHint returns a default hint configuration when hint is nil
func getDefaultHint() *autoscalingv1alpha1.Hint {
	return &autoscalingv1alpha1.Hint{
		ModelID:            "",
		BatchSize:          1,
		MaxNumSeqs:         10,
		PrefixCacheHitRate: 0.0,
		KVCacheSpeedup:     1.0,
	}
}

// extractGPUConstraints extracts GPU constraints from autoscaler spec
func extractGPUConstraints(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) GPUResourceConstraints {
	constraints := GPUResourceConstraints{
		MinThreadPercentage:  1.0,                     // Default 1%
		MaxThreadPercentage:  100.0,                   // Default 100%
		StepThreadPercentage: 1.0,                     // Default 1% step
		MinMemoryBytes:       8 * 1024 * 1024 * 1024,  // Default 8GB
		MaxMemoryBytes:       80 * 1024 * 1024 * 1024, // Default 80GB
		StepMemoryBytes:      1024 * 1024 * 1024,      // Default 1GB step
		MaxGPUsPerReplica:    8,                       // Default 8 GPUs per replica
		TotalGPUs:            0,                       // Unlimited by default
	}

	// Extract from constraints if available
	if spec.Constraints != nil {
		if spec.Constraints.MaxCountPerReplica != nil {
			constraints.MaxGPUsPerReplica = int(*spec.Constraints.MaxCountPerReplica)
			// Assume MaxCountPerReplica refers to GPU count
			constraints.TotalGPUs = constraints.MaxGPUsPerReplica * getMaxReplicas(spec.Constraints)
		}

		// Extract min/max/step from CapacityPerCount
		if spec.Constraints.CapacityPerCount != nil {
			// Get compute constraints
			if computeConstraint, ok := spec.Constraints.CapacityPerCount["compute"]; ok {
				if computeConstraint.Min != nil {
					constraints.MinThreadPercentage = computeConstraint.Min.AsApproximateFloat64()
				}
				if computeConstraint.Max != nil {
					constraints.MaxThreadPercentage = computeConstraint.Max.AsApproximateFloat64()
				}
				if computeConstraint.Step != nil {
					constraints.StepThreadPercentage = computeConstraint.Step.AsApproximateFloat64()
				}
			}

			// Get memory constraints
			if memoryConstraint, ok := spec.Constraints.CapacityPerCount["memory"]; ok {
				if memoryConstraint.Min != nil {
					constraints.MinMemoryBytes = memoryConstraint.Min.Value()
				}
				if memoryConstraint.Max != nil {
					constraints.MaxMemoryBytes = memoryConstraint.Max.Value()
				}
				if memoryConstraint.Step != nil {
					constraints.StepMemoryBytes = memoryConstraint.Step.Value()
				}
			}
		}
	}

	return constraints
}

func (o *ResourceOptimizer) CalculateResourceAllocationImpact(ratio float64) float64 {
	if ratio < 0.0 {
		ratio = 0.0
	}
	if ratio > 1.0 {
		ratio = 1.0
	}
	// 1. Under-provisioned (Low Density): Apply Speedup
	// We call this 'Overprovisioning the work' to get a speedup
	if ratio < 0.5 {
		// As the ratio gets smaller, the potential 'Speedup' bonus is higher
		// (1.0 - ratio) represents the 'empty space' we can fill
		return -(1.0 - ratio) * o.resourceEstimator.gpuSpec.OverprovisionSpeedUp
	}

	// 2. Over-provisioned (High Density): Apply Contention
	// We call this 'Under-provisioning the hardware' relative to the load
	if ratio > 0.8 {
		// As the ratio approaches 1.0 (or exceeds it via queuing),
		// the contention penalty scales up.
		return (ratio - 0.8) * o.resourceEstimator.gpuSpec.UnderprovisionContentionImpact
	}
	return 0.0
}

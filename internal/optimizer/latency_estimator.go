package optimizer

import (
	"math"

	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// LatencyEstimator provides queue theory-based latency estimation for LLM inference
// This
type LatencyEstimator struct {
	// Server configuration
	PrefixCacheHitRate float64 // Cache hit rate for prefix caching

	// Latency parameters
	PrefillTimePerToken float64 // Time per token during prefill phase (ms)
	InterTokenLatency   float64 // Time between tokens during decode (ms)

	// KV Cache parameters
	KVCacheHitRate float64 // Cache hit rate  (0-1)
	KVCacheSpeedup float64 // Speedup factor when cache hits (default 0.5 = 50% faster)

	// Workload parameters
	ArrivalRate     float64 // Requests per second (λ)
	AvgPromptTokens float64 // Average prompt length in tokens
	AvgOutputTokens float64 // Average output length in tokens

	// Tuning offsets (difference between measured and calculated from previous round)
	itlOffset  float64 // ITL offset in milliseconds
	ttftOffset float64 // TTFT offset in milliseconds

	effectiveBatch             float64
	kernelOverhead             float64
	prefillLatencyPerPromptLen float64
	kvReadTimePerPromptToken   float64
	totalWeightReadTime        float64

	logger logr.Logger
}

func NewLatencyEstimatorWithHint(hint autoscalingv1alpha1.Hint, archParams *autoscalingv1alpha1.ArchParams, gpuSpec *GPUSpecs, slogger logr.Logger) *LatencyEstimator {
	logger := slogger.WithName("latencyEstimator").WithValues("model", hint.ModelID)

	// Kernel launch overhead
	kernelOverhead := calculateKernelOverhead(archParams)
	modelBytes := (archParams.ParametersB * 1e9 * float64(archParams.Precision))
	memoryBandwidthBytesPerMilliSecond := gpuSpec.MemoryBW * 1e12 / 1000.0
	// Base cost: Reading the weights (Fixed)
	totalWeightReadTime := modelBytes / memoryBandwidthBytesPerMilliSecond
	prefillLatencyPerPromptLen := 2 * archParams.ParametersB * 1e9 / (gpuSpec.PeakTFLOPS * 1e12) * 1000.0
	effectiveBatch := math.Min(float64(hint.BatchSize), float64(hint.MaxNumSeqs))
	kvCacheBytesPerToken := calculateKVCacheBytesPerToken(archParams)
	// Variable cost: Reading/Writing KV Cache for the batch
	// (Approx 2 * hidden_size * layers * num_heads * precision)
	kvReadTimePerPromptToken := kvCacheBytesPerToken * effectiveBatch / memoryBandwidthBytesPerMilliSecond

	logger.V(1).Info("Latency estimator initialized",
		"kernelOverhead", kernelOverhead,
		"modelBytes", modelBytes,
		"memoryBandwidthBytesPerMilliSecond", memoryBandwidthBytesPerMilliSecond,
		"totalWeightReadTime", totalWeightReadTime,
		"prefillLatencyPerPromptLen", prefillLatencyPerPromptLen,
		"effectiveBatch", effectiveBatch,
		"kvCacheBytesPerToken", kvCacheBytesPerToken,
		"kvReadTimePerPromptToken", kvReadTimePerPromptToken)

	return &LatencyEstimator{
		PrefixCacheHitRate:         hint.PrefixCacheHitRate,
		KVCacheSpeedup:             hint.KVCacheSpeedup,
		effectiveBatch:             effectiveBatch,
		kernelOverhead:             kernelOverhead,
		totalWeightReadTime:        totalWeightReadTime,
		prefillLatencyPerPromptLen: prefillLatencyPerPromptLen,
		kvReadTimePerPromptToken:   kvReadTimePerPromptToken,
		logger:                     logger,
	}
}

// SetLatencyOffsets sets the tuning offsets based on measured vs calculated differences
func (r *LatencyEstimator) SetLatencyOffsets(itlOffset, ttftOffset float64) {
	r.itlOffset = itlOffset
	r.ttftOffset = ttftOffset
	r.logger.V(1).Info("Latency offsets set in estimator",
		"itlOffset_ms", itlOffset,
		"ttftOffset_ms", ttftOffset)
}

func (r *LatencyEstimator) estimateLatency(promptLen int, outputLen int, numGPU int, allocationImpact float64) LatencyResult {
	numGPUF := float64(numGPU)
	prefillOverhead := r.estimateStructuralOverhead(numGPUF)
	prefillLatencyByPrompt, kvCacheReduction := r.computePrefillLatencyByPrompt(promptLen, numGPUF, allocationImpact)
	prefillLatency := (prefillLatencyByPrompt + prefillOverhead)
	itl := r.computeInterTokenLatency(promptLen, numGPU, allocationImpact)
	decodeLatency := itl * float64(outputLen)
	return LatencyResult{
		ITL:              itl,
		Prefill:          prefillLatency,
		Decode:           decodeLatency,
		KVCacheReduction: kvCacheReduction,
		AllocationImpact: allocationImpact,
	}
}

func (r *LatencyEstimator) computePrefillLatencyByPrompt(promptLen int, numGPUF float64, allocationImpact float64) (float64, float64) {
	// Compute Prefill (TTFT)
	// We scale by effectiveBatch because we are processing all prompts in parallel
	totalPrefillTokens := float64(promptLen) * r.effectiveBatch
	basePrefill := (r.prefillLatencyPerPromptLen * totalPrefillTokens) / numGPUF

	// Apply KV cache logic
	kvCacheReduction := 0.0
	prefillLatencyByPrompt := basePrefill
	speedUp := r.KVCacheSpeedup * r.PrefixCacheHitRate
	if r.PrefixCacheHitRate > 0 && speedUp <= 1.0 {
		kvCacheReduction = (basePrefill * speedUp)
		prefillLatencyByPrompt -= kvCacheReduction

	}
	prefillLatencyByPrompt *= (1.0 + allocationImpact)
	// Apply tuning offset for TTFT
	prefillLatencyByPrompt += r.ttftOffset
	if prefillLatencyByPrompt < 0 {
		prefillLatencyByPrompt = 0
	}
	return prefillLatencyByPrompt, kvCacheReduction
}

func (r *LatencyEstimator) computeInterTokenLatency(promptLen int, numGPU int, allocationImpact float64) float64 {
	totalKvReadTime := r.kvReadTimePerPromptToken * float64(promptLen)
	itl := (r.totalWeightReadTime + totalKvReadTime) / float64(numGPU)
	itl *= (1.0 + allocationImpact)
	// Apply tuning offset
	itl += r.itlOffset
	if itl < 0 {
		itl = 0
	}
	return itl
}

func calculateKVCacheBytesPerToken(archParams *autoscalingv1alpha1.ArchParams) float64 {
	// If the model uses Grouped Query Attention (GQA)
	kvHeads := archParams.KVHeads
	if kvHeads == 0 {
		// Fallback to standard Multi-Head Attention if GQA params aren't provided
		kvHeads = archParams.QueryHeads
	}

	headDim := archParams.HiddenSize / archParams.QueryHeads

	// 2 (K and V) * Layers * Heads * Dim * Precision
	bytes := 2 * float64(archParams.Layers*kvHeads*headDim*archParams.Precision)

	return bytes
}

func calculateKernelOverhead(archParams *autoscalingv1alpha1.ArchParams) float64 {
	// 1. Layer-based launch overhead
	// Every layer requires a set of kernel launches.
	kernelLaunchTime := float64(archParams.Layers) * 0.1 // 0.1ms per layer
	// 2. KV Management Overhead
	// Scales with the complexity of the attention metadata.
	// GQA (Grouped Query Attention) reduces this management cost.
	kvManagementTime := float64(archParams.Layers*archParams.KVHeads) * 0.05
	return kernelLaunchTime + kvManagementTime
}

func (r *LatencyEstimator) estimateStructuralOverhead(numGPUF float64) float64 {
	// 3. Multi-GPU Synchronization Floor
	// NCCL/NVLink initialization overhead per request
	syncOverhead := 0.0
	if numGPUF > 1 {
		syncOverhead = 2 * numGPUF // 2ms base per GPU
	}

	return r.kernelOverhead + syncOverhead
}

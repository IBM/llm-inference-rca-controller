package optimizer

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

const (
	MaxMemoryOverheadFactor = 1.3 // Allow up to 30% overhead for framework variations
	MinMaxThreads           = 30720
	MinHiddenSize           = 128
)

type GPUSpecs struct {
	Name                           string
	VRAM                           int     // GB
	MemoryBW                       float64 // TB/s
	PeakTFLOPS                     float64 // FP16/BF16
	PeakFP8                        float64 // FP8 TFLOPS
	MaxThreads                     int     // Typical hardware thread capacity
	UnderprovisionContentionImpact float64 // 0.0 to 1.0
	OverprovisionSpeedUp           float64 // 0.0 to 1.0
}

var gpuRegistry = map[string]GPUSpecs{
	"h200": {Name: "H200 SXM", VRAM: 141, MemoryBW: 4.8, PeakTFLOPS: 989.0, PeakFP8: 3958.0, MaxThreads: 249856, UnderprovisionContentionImpact: 0.08, OverprovisionSpeedUp: 0.5},
	"h100": {Name: "H100 SXM", VRAM: 80, MemoryBW: 3.35, PeakTFLOPS: 989.0, PeakFP8: 1979.0, MaxThreads: 184320, UnderprovisionContentionImpact: 0.15, OverprovisionSpeedUp: 0.35},
	"a100": {Name: "A100 80GB", VRAM: 80, MemoryBW: 2.0, PeakTFLOPS: 312.0, PeakFP8: 0.0, MaxThreads: 108544, UnderprovisionContentionImpact: 0.3, OverprovisionSpeedUp: 0.2},
	"l4":   {Name: "L4 Tensor Core", VRAM: 24, MemoryBW: 0.3, PeakTFLOPS: 121.0, PeakFP8: 242.0, MaxThreads: 30720, UnderprovisionContentionImpact: 0.7, OverprovisionSpeedUp: 0.05},
}

var modelFamilyMap = map[string]autoscalingv1alpha1.ArchParams{
	"llama-3-8b":   {ParametersB: 8.0, Layers: 32, KVHeads: 8, HiddenSize: 4096, QueryHeads: 32, Precision: 2},
	"llama-3.1-8b": {ParametersB: 8.0, Layers: 32, KVHeads: 8, HiddenSize: 4096, QueryHeads: 32, Precision: 2},
	"llama-3-70b":  {ParametersB: 70.0, Layers: 80, KVHeads: 8, HiddenSize: 8192, QueryHeads: 64, Precision: 2},
	"mistral-nemo": {ParametersB: 12.0, Layers: 40, KVHeads: 8, HiddenSize: 5120, QueryHeads: 32, Precision: 2},
	"phi-3-mini":   {ParametersB: 3.8, Layers: 32, KVHeads: 8, HiddenSize: 3072, QueryHeads: 32, Precision: 2},
	"phi-3-medium": {ParametersB: 14.0, Layers: 40, KVHeads: 8, HiddenSize: 5120, QueryHeads: 40, Precision: 2},
}

type ComplexityMetrics struct {
	FlopsPerToken       float64 // Total FLOPs for one forward pass of one token
	ArithmeticIntensity float64 // FLOPs / Bytes (Determines if Compute or Memory bound)
	MinThreads          int     // Suggested minimum threads to saturate the hidden dimension
}

type ResourceRequirements struct {
	ThreadOccupancy float64
	MemoryGB        float64
}

type ResourcEstimator struct {
	modelID               string
	validParamsInBillions bool
	paramsFound           bool
	params                *autoscalingv1alpha1.ArchParams
	complexity            ComplexityMetrics

	effectiveBatchSize  float64
	maxNumBatchedTokens *int32

	modelSize                   float64
	memoryPerToken              float64
	activeMemoryPerPerfillToken float64
	gpuFound                    bool
	gpuSpec                     *GPUSpecs
	memoryOverheadFactor        float64

	minMemoryUsage float64

	logger logr.Logger
}

func NewResourceEstimatorWithHint(hint autoscalingv1alpha1.Hint, slogger logr.Logger) *ResourcEstimator {
	effectiveBatchSize := math.Min(float64(hint.BatchSize), float64(hint.MaxNumSeqs))
	if hint.ModelParams != nil {
		return NewResourceEstimatorWithArchParam(hint.ModelID, hint.GPUName, effectiveBatchSize, hint.MaxNumBatchedTokens, *hint.ModelParams, slogger)
	}
	return NewResourceEstimator(hint.ModelID, hint.GPUName, effectiveBatchSize, hint.MaxNumBatchedTokens, slogger)
}

func NewResourceEstimator(modelID, gpuName string, effectiveBatchSize float64, maxNumBatchedTokens *int32, slogger logr.Logger) *ResourcEstimator {
	logger := slogger.WithName("resourceEstimator").WithValues("model", modelID, "batch", effectiveBatchSize)
	paramsFound, params := inferredArchParams(modelID, logger)
	validParamsInBillions, _ := extractParams(modelID, logger)
	gpuFound := false
	var gpuSpec *GPUSpecs
	if gpuName == "" {
		_, gpuSpec = getGPUSpecs("a100", logger)
	} else {
		gpuFound, gpuSpec = getGPUSpecs(gpuName, logger)
	}
	return &ResourcEstimator{
		modelID:                     modelID,
		validParamsInBillions:       validParamsInBillions,
		paramsFound:                 paramsFound,
		params:                      &params,
		complexity:                  estimateModelComplexity(modelID, params.ParametersB, params.HiddenSize),
		effectiveBatchSize:          effectiveBatchSize,
		maxNumBatchedTokens:         maxNumBatchedTokens,
		modelSize:                   estimateModelSize(modelID, params.ParametersB, params.Precision),
		memoryPerToken:              estimateMemoryPerTokenFromID(params, params.Precision),
		activeMemoryPerPerfillToken: estimateActiveMemoryPerTokenFromID(effectiveBatchSize, params, params.Precision),
		gpuFound:                    gpuFound,
		gpuSpec:                     gpuSpec, // default GPU
		memoryOverheadFactor:        1.2,     // default 20% - more realistic for vLLM
		logger:                      logger,
	}
}

func NewResourceEstimatorWithArchParam(modelID, gpuName string, effectiveBatchSize float64, maxNumBatchedTokens *int32, params autoscalingv1alpha1.ArchParams, slogger logr.Logger) *ResourcEstimator {
	logger := slogger.WithName("resourceEstimatorWithParams").WithValues("model", modelID, "batch", effectiveBatchSize, "params", params)
	gpuFound := false
	var gpuSpec *GPUSpecs
	if gpuName == "" {
		_, gpuSpec = getGPUSpecs("a100", logger)
	} else {
		gpuFound, gpuSpec = getGPUSpecs(gpuName, logger)
	}
	return &ResourcEstimator{
		modelID:                     modelID,
		paramsFound:                 true,
		params:                      &params,
		complexity:                  estimateModelComplexity(modelID, params.ParametersB, params.HiddenSize),
		effectiveBatchSize:          effectiveBatchSize,
		maxNumBatchedTokens:         maxNumBatchedTokens,
		modelSize:                   estimateModelSize(modelID, params.ParametersB, params.Precision),
		memoryPerToken:              estimateMemoryPerTokenFromID(params, params.Precision),
		activeMemoryPerPerfillToken: estimateActiveMemoryPerTokenFromID(effectiveBatchSize, params, params.Precision),
		gpuFound:                    gpuFound,
		gpuSpec:                     gpuSpec, // default GPU
		memoryOverheadFactor:        1.2,     // default 20% - more realistic for vLLM
		logger:                      logger,
	}
}

func (r *ResourcEstimator) SetGPUSpec(gpuName string) {
	r.gpuFound, r.gpuSpec = getGPUSpecs(gpuName, r.logger)
}

func (r *ResourcEstimator) CalculateResourceRequirementsPerGPU(promptLen, outputLen int, cachedHitRatio float64, numGPU int) ResourceRequirements {
	// Calculate how many tokens are actually 'misses'
	// (These are the tokens the GPU must actually compute)
	prefillTokens := float64(promptLen) * (1.0 - cachedHitRatio) * r.effectiveBatchSize
	if r.maxNumBatchedTokens != nil {
		prefillTokens = math.Min(prefillTokens, float64(*r.maxNumBatchedTokens))
	}
	return ResourceRequirements{
		ThreadOccupancy: r.estimateThreadOccupancy(r.effectiveBatchSize, prefillTokens, numGPU),
		MemoryGB:        r.estimateMemoryUsage(promptLen, outputLen, prefillTokens, numGPU),
	}
}

// estimateThreadOccupancy estimates GPU thread utilization.
// NOTE: This is a simplified model that assumes prefill and decode threads
// are additive. In reality, vLLM's continuous batching may schedule these
// in separate kernel launches. This provides a reasonable upper bound for
// resource planning, with adaptive tuning compensating for inaccuracies.
func (r *ResourcEstimator) estimateThreadOccupancy(effectiveBatch, prefillTokens float64, numGPU int) float64 {
	// Determine Total Parallelism Demand
	// For LLMs, threads are typically assigned per hidden dimension.
	// Prefill phase: Uses hiddenSize threads for the entire prompt
	// Decode phase: Uses hiddenSize threads per sequence in the batch
	prefillThreads := prefillTokens * float64(r.complexity.MinThreads)
	decodeThreads := effectiveBatch * float64(r.complexity.MinThreads)

	// In a single vLLM iteration, the GPU handles both.
	// Use max instead of sum since they may not run simultaneously
	totalActiveThreads := math.Max(prefillThreads, decodeThreads)

	// 3. Compare against Hardware Capacity
	// g.MaxThreads represents the total resident thread slots (e.g., 184,320 for H100)
	occupancy := (totalActiveThreads / float64(r.gpuSpec.MaxThreads*numGPU)) * 100

	// Cap at 100% (Hardware cannot exceed its physical slots; it queues instead)
	if occupancy > 100.0 {
		occupancy = 100.0
	}

	r.logger.V(1).Info("Thread occupancy estimated",
		"effectiveBatch", effectiveBatch,
		"prefillTokens", prefillTokens,
		"numGPU", numGPU,
		"prefillThreads", prefillThreads,
		"decodeThreads", decodeThreads,
		"totalActiveThreads", totalActiveThreads,
		"maxThreads", r.gpuSpec.MaxThreads*numGPU,
		"occupancy", occupancy)

	return occupancy
}

func (r *ResourcEstimator) estimateMemoryUsage(promptLen int, outputLen int, prefillTokens float64, numGPU int) float64 {
	totalTokens := float64(promptLen+outputLen) * float64(r.effectiveBatchSize)
	modelWeight := r.modelSize / float64(numGPU)
	kvCacheMem := (float64(r.effectiveBatchSize) * totalTokens * r.memoryPerToken) / float64(numGPU) / 1e9
	activationMem := r.activeMemoryPerPerfillToken * float64(prefillTokens) / float64(numGPU) / 1e9

	// Add attention memory for long contexts
	attentionMem := r.estimateAttentionMemory(promptLen, prefillTokens) / float64(numGPU)

	totalMemoryGBPerGPU := (modelWeight + kvCacheMem + activationMem + attentionMem) * r.memoryOverheadFactor

	r.logger.V(1).Info("Memory usage estimated",
		"promptLen", promptLen,
		"outputLen", outputLen,
		"prefillTokens", prefillTokens,
		"numGPU", numGPU,
		"totalTokens", totalTokens,
		"modelWeight", modelWeight,
		"kvCacheMem", kvCacheMem,
		"activationMem", activationMem,
		"attentionMem", attentionMem,
		"memoryOverheadFactor", r.memoryOverheadFactor,
		"totalMemoryGBPerGPU", totalMemoryGBPerGPU)

	return totalMemoryGBPerGPU
}

// estimateAttentionMemory calculates additional memory needed for attention computation
// in long context scenarios. FlashAttention reduces memory from O(n²) to O(n), but
// for very long sequences, there's still some overhead that needs to be accounted for.
func (r *ResourcEstimator) estimateAttentionMemory(seqLen int, prefillTokens float64) float64 {
	// For sequences <= 8K tokens, FlashAttention makes attention memory negligible
	if seqLen <= 8192 {
		return 0
	}

	// For long contexts (> 8K), add a sublinear scaling factor
	// Real attention memory is reduced by FlashAttention but not eliminated entirely
	// The factor grows sublinearly (power of 1.3) rather than quadratically
	longContextFactor := math.Pow(float64(seqLen)/8192.0, 1.3)

	// Estimate attention overhead as a fraction of activation memory
	// This accounts for intermediate attention states and softmax buffers
	attentionOverhead := prefillTokens * float64(r.params.HiddenSize) * float64(r.params.Precision) * longContextFactor * 0.1

	return attentionOverhead / 1e9 // Convert to GB
}

func (r *ResourcEstimator) tuneWithComputeMetric(prefillTokens float64, _ int, _ float64, computeThreadUtilization float64, numGPU int) {
	if !r.gpuFound {
		if computeThreadUtilization > 100 {
			r.logger.Info("Failed to tune with computeThreadUtilization > 100",
				"computeThreadUtilization", computeThreadUtilization)
			return
		}
		totalActiveThreads := (prefillTokens + float64(r.effectiveBatchSize)) * float64(r.params.HiddenSize)
		maxThreads := int(totalActiveThreads * 100 / computeThreadUtilization / float64(numGPU))
		if maxThreads > MinMaxThreads {
			r.logger.Info("Tune max thread",
				"from", r.gpuSpec.MaxThreads,
				"to", maxThreads)
			r.gpuSpec.MaxThreads = maxThreads
		}
	}
}

func (r *ResourcEstimator) tuneWithMemoryMetric(prefillTokens float64, promptLen int, outputLen int, cachedHitRatio float64, measuredMemoryUsage float64, numGPU int) {
	if !r.paramsFound {
		r.tuneModelParams(prefillTokens, promptLen, outputLen, cachedHitRatio, measuredMemoryUsage, numGPU)
	}
	// Tune overhead factor
	estimatedMemoryUsage := r.estimateMemoryUsage(promptLen, outputLen, prefillTokens, numGPU)
	if estimatedMemoryUsage > 0 {
		memoryOverheadFactor := math.Min(MaxMemoryOverheadFactor, measuredMemoryUsage/estimatedMemoryUsage)
		if r.memoryOverheadFactor > 1 && memoryOverheadFactor > r.memoryOverheadFactor {
			r.logger.Info("Increase memory overhead factor",
				"from", r.memoryOverheadFactor,
				"to", memoryOverheadFactor)
			r.memoryOverheadFactor = memoryOverheadFactor
		}
	}
}

func (r *ResourcEstimator) tuneModelParams(prefillTokens float64, promptLen int, outputLen int, _ float64, measuredMemoryUsage float64, numGPU int) {
	rawMemoryUsage := measuredMemoryUsage / r.memoryOverheadFactor
	numGPUF := float64(numGPU)
	// Tune model size
	totalTokens := float64(promptLen+outputLen) * float64(r.effectiveBatchSize)

	if !r.validParamsInBillions {
		// try tuning model size
		kvCacheMem := (totalTokens * r.memoryPerToken) / numGPUF / 1e9
		activationMem := r.activeMemoryPerPerfillToken * float64(prefillTokens) / numGPUF / 1e9
		calculatedModelWeightPerGPU := rawMemoryUsage - kvCacheMem - activationMem
		if r.minMemoryUsage == 0 || (calculatedModelWeightPerGPU > 0 && r.minMemoryUsage > calculatedModelWeightPerGPU) {
			r.minMemoryUsage = calculatedModelWeightPerGPU
			modelSize := calculatedModelWeightPerGPU * float64(numGPU)
			r.logger.Info("Tune modelSize",
				"from", r.modelSize,
				"to", modelSize)
			r.modelSize = modelSize
		}
	}
	modelWeightPerGPU := r.modelSize / numGPUF
	dynamicMemory := rawMemoryUsage - modelWeightPerGPU

	if dynamicMemory > 0 {
		r.tuneDynamicMemory(outputLen, prefillTokens, dynamicMemory, totalTokens)
	}
}

func (r *ResourcEstimator) tuneDynamicMemory(outputLen int, prefillTokens, dynamicMemory, totalTokens float64) {
	// We have a linear equation: dynamicMemory = (activationMem + kvCacheMem)
	// Since both scale with hidden_dimension and precision, we can tune
	// a 'CompositeTokenFactor' or split them if we have multiple data points.

	// Tuning 'memoryPerToken' (KV Cache)
	// Note: During the very first iteration, dynamic memory is mostly activations.
	// After the first token, it becomes dominated by the KV Cache.
	if outputLen > 1 {
		// Adjusting the per-token cost based on observed delta
		calculatedPerToken := (dynamicMemory * 1e9) / totalTokens
		memoryPerToken := (r.memoryPerToken + calculatedPerToken) / 2
		if memoryPerToken > 0 {
			r.logger.Info("Tune memoryPerToken",
				"from", r.memoryPerToken,
				"to", memoryPerToken)
			r.memoryPerToken = memoryPerToken
		}
	}
	// Tuning 'activeMemoryPerPrefillToken' (Activation Spike)
	if prefillTokens > 0 {
		calculatedActiveFactor := (dynamicMemory * 1e9) / prefillTokens
		activeMemoryPerPerfillToken := (r.activeMemoryPerPerfillToken + calculatedActiveFactor) / 2
		hiddenSize := int(math.Ceil(activeMemoryPerPerfillToken / float64(r.effectiveBatchSize) / float64(r.params.Precision)))
		if activeMemoryPerPerfillToken > 0 && hiddenSize > MinHiddenSize {
			r.logger.Info("Tune activeMemoryPerPrefillToken and hiddenSize",
				"activeMemoryPerPrefillToken_from", r.activeMemoryPerPerfillToken,
				"activeMemoryPerPerfillToken_to", activeMemoryPerPerfillToken,
				"hiddenSize_from", r.params.HiddenSize,
				"hiddenSize_to", hiddenSize)
			r.activeMemoryPerPerfillToken = activeMemoryPerPerfillToken
			r.params.HiddenSize = hiddenSize
		}
	}
}

func estimateModelComplexity(_ string, paramsInBillions float64, hiddenSize int) ComplexityMetrics {
	// 1. Calculate FLOPs per token
	// Rule of Thumb: 2 * Parameters (one for mul, one for add)
	flops := 2 * paramsInBillions * 1e9

	// 2. Arithmetic Intensity (AI)
	// AI = FLOPs / Bytes accessed.
	// In the Decode phase (1 token), we load the whole model once (e.g., FP16 = 2 bytes per param).
	// So for Decode: AI = (2 * P) / (2 * P) = 1.0
	// This confirms the Decode phase is almost always Memory Bandwidth Bound.
	ai := 1.0

	// 3. Thread Utilization (Hidden Size scaling)
	// Most kernels (like those in vLLM) parallelize across the Hidden Dimension.
	// For optimal occupancy on modern GPUs (A100/H100), we want threads to be
	// multiples of the "Warp Size" (32) and "Max Threads Per Block" (1024).
	minThreads := hiddenSize

	return ComplexityMetrics{
		FlopsPerToken:       flops,
		ArithmeticIntensity: ai,
		MinThreads:          minThreads,
	}
}

func inferredArchParams(modelID string, logger logr.Logger) (bool, autoscalingv1alpha1.ArchParams) {
	id := strings.ToLower(modelID)
	// 1. Check for specific known families
	for key, p := range modelFamilyMap {
		if strings.Contains(id, key) {
			return true, p
		}
	}
	_, paramsInBillions := extractParams(modelID, logger)
	precisionBytes := inferredPrecisionBytes(modelID)

	logger.Info("Warning: Model unknown. Using generic parameters",
		"modelID", modelID,
		"paramsInBillions", paramsInBillions,
		"precisionBytes", precisionBytes)
	return false, autoscalingv1alpha1.ArchParams{ParametersB: paramsInBillions, Layers: 32, KVHeads: 8, HiddenSize: 4096, QueryHeads: 32, Precision: precisionBytes}
}

// estimateModelSize calculates the VRAM needed to load the model weights.
// It includes a 20% buffer for activation memory and CUDA kernels.
func estimateModelSize(_ string, paramsInBillions float64, precisionBytes int) float64 {
	// 1. Base weight memory: Params * Precision
	// (1 Billion params * 2 bytes (FP16) = 2 GB)
	return paramsInBillions * float64(precisionBytes)
}

func estimateMemoryPerTokenFromID(params autoscalingv1alpha1.ArchParams, precisionBytes int) float64 {
	// Calculate Head Dim
	headDim := params.HiddenSize / params.QueryHeads

	// Formula: 2 (K+V) * Layers * KV_Heads * Head_Dim * Precision
	bytesPerToken := 2 * params.Layers * params.KVHeads * headDim * precisionBytes

	return float64(bytesPerToken)
}

func estimateActiveMemoryPerTokenFromID(effectiveBatch float64, params autoscalingv1alpha1.ArchParams, precisionBytes int) float64 {
	// Base activation memory (attention output)
	baseActivation := effectiveBatch * float64(params.HiddenSize*precisionBytes)

	// FFN intermediate activations (typically 4x hidden size for MLP)
	// Most LLMs use SwiGLU or GELU with intermediate_size = 4 * hidden_size
	// This is the memory needed during the forward pass through the FFN layer
	ffnIntermediate := effectiveBatch * float64(params.HiddenSize*4*precisionBytes)

	// Total activation memory per token
	// Note: FlashAttention reduces attention memory from O(n²) to O(n),
	// so we don't add a separate quadratic term for attention scores
	return baseActivation + ffnIntermediate
}

func inferredPrecisionBytes(modelID string) int {
	id := strings.ToLower(modelID)

	// Check for low-precision/quantized indicators
	if strings.Contains(id, "fp8") || strings.Contains(id, "int8") {
		return 1
	}

	// Check for standard 16-bit indicators
	if strings.Contains(id, "fp16") || strings.Contains(id, "bf16") || strings.Contains(id, "half") {
		return 2
	}

	// Check for 4-bit weights (Caution: KV Cache might still be 16-bit)
	if strings.Contains(id, "awq") || strings.Contains(id, "gptq") || strings.Contains(id, "4bit") {
		// In vLLM, if weights are 4-bit, KV Cache is usually still 16-bit (2 bytes)
		// unless --kv-cache-dtype fp8 is used.
		return 2
	}

	// Default for most modern LLMs (Llama-3, Mistral, etc.) is BF16/FP16
	return 2
}

func extractParams(modelID string, logger logr.Logger) (bool, float64) {
	id := strings.ToLower(modelID)
	// Regex to find numbers followed by 'b' (e.g., 8b, 70b, 3.8b)
	re := regexp.MustCompile(`(\d+\.?\d*)b`)
	matches := re.FindStringSubmatch(id)

	if len(matches) > 1 {
		val, err := strconv.ParseFloat(matches[1], 64)
		if err == nil {
			return true, val
		}
	}
	logger.Info("Warning: cannot extract param size. Using default 8.0B",
		"modelID", modelID)
	return false, 8.0 // Default to 8B if unknown
}

func getGPUSpecs(name string, logger logr.Logger) (bool, *GPUSpecs) {
	name = strings.ToLower(name)
	for key, spec := range gpuRegistry {
		if strings.Contains(name, key) {
			return true, &spec
		}
	}
	spec := gpuRegistry["a100"]
	logger.Info("Warning: GPU unknown. Using generic a100",
		"gpuName", name)
	return false, &spec
}

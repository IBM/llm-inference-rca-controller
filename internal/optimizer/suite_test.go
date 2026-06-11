package optimizer

// This file exports internal functions and constants for testing purposes only.
// It uses the package optimizer (not optimizer_test) so it can access unexported identifiers.

import (
	"testing"

	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOptimizer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "optimizer Suite")
}

// Exported constants for testing
const (
	ExportedMaxMemoryOverheadFactor = MaxMemoryOverheadFactor
	ExportedMinMaxThreads           = MinMaxThreads
	ExportedMinHiddenSize           = MinHiddenSize
)

// Exported functions for testing
var (
	ExportedGetGPUSpecs             = getGPUSpecs
	ExportedInferredArchParams      = inferredArchParams
	ExportedExtractParams           = extractParams
	ExportedInferredPrecisionBytes  = inferredPrecisionBytes
	ExportedEstimateModelComplexity = estimateModelComplexity
)

// Exported types for testing
type (
	ExportedResourcEstimator = ResourcEstimator
	ExportedGPUSpecs         = GPUSpecs
)

// Exported constructors for testing
var (
	ExportedNewResourceEstimator              = NewResourceEstimator
	ExportedNewResourceEstimatorWithHint      = NewResourceEstimatorWithHint
	ExportedNewResourceEstimatorWithArchParam = NewResourceEstimatorWithArchParam
)

// Exported methods for testing - method value assignments
var (
	ExportedEstimateThreadOccupancy = (*ResourcEstimator).estimateThreadOccupancy
	ExportedEstimateMemoryUsage     = (*ResourcEstimator).estimateMemoryUsage
	ExportedEstimateAttentionMemory = (*ResourcEstimator).estimateAttentionMemory
	ExportedTuneWithComputeMetric   = (*ResourcEstimator).tuneWithComputeMetric
	ExportedTuneWithMemoryMetric    = (*ResourcEstimator).tuneWithMemoryMetric
)

// Getter methods for unexported fields
func (r *ResourcEstimator) GetModelID() string {
	return r.modelID
}

func (r *ResourcEstimator) GetEffectiveBatchSize() float64 {
	return r.effectiveBatchSize
}

func (r *ResourcEstimator) GetMemoryOverheadFactor() float64 {
	return r.memoryOverheadFactor
}

func (r *ResourcEstimator) GetGPUSpec() *GPUSpecs {
	return r.gpuSpec
}

func (r *ResourcEstimator) GetParamsFound() bool {
	return r.paramsFound
}

func (r *ResourcEstimator) GetParams() *autoscalingv1alpha1.ArchParams {
	return r.params
}

func (r *ResourcEstimator) GetModelSize() float64 {
	return r.modelSize
}

func (r *ResourcEstimator) GetMemoryPerToken() float64 {
	return r.memoryPerToken
}

func (r *ResourcEstimator) GetActiveMemoryPerPerfillToken() float64 {
	return r.activeMemoryPerPerfillToken
}

// Helper to call unexported methods
func (r *ResourcEstimator) CallEstimateThreadOccupancy(effectiveBatch, prefillTokens float64, numGPU int) float64 {
	return r.estimateThreadOccupancy(effectiveBatch, prefillTokens, numGPU)
}

func (r *ResourcEstimator) CallEstimateMemoryUsage(promptLen int, outputLen int, prefillTokens float64, numGPU int) float64 {
	return r.estimateMemoryUsage(promptLen, outputLen, prefillTokens, numGPU)
}

func (r *ResourcEstimator) CallEstimateAttentionMemory(seqLen int, prefillTokens float64) float64 {
	return r.estimateAttentionMemory(seqLen, prefillTokens)
}

func (r *ResourcEstimator) CallTuneWithComputeMetric(prefillTokens float64, promptLen int, cachedHitRatio float64, computeThreadUtilization float64, numGPU int) {
	r.tuneWithComputeMetric(prefillTokens, promptLen, cachedHitRatio, computeThreadUtilization, numGPU)
}

func (r *ResourcEstimator) CallTuneWithMemoryMetric(prefillTokens float64, promptLen int, outputLen int, cachedHitRatio float64, measuredMemoryUsage float64, numGPU int) {
	r.tuneWithMemoryMetric(prefillTokens, promptLen, outputLen, cachedHitRatio, measuredMemoryUsage, numGPU)
}

// Helper function to call getGPUSpecs
func CallGetGPUSpecs(name string, logger logr.Logger) (bool, *GPUSpecs) {
	return getGPUSpecs(name, logger)
}

// Helper function to call inferredArchParams
func CallInferredArchParams(modelID string, logger logr.Logger) (bool, autoscalingv1alpha1.ArchParams) {
	return inferredArchParams(modelID, logger)
}

// Helper function to call extractParams
func CallExtractParams(modelID string, logger logr.Logger) (bool, float64) {
	return extractParams(modelID, logger)
}

// Helper function to call inferredPrecisionBytes
func CallInferredPrecisionBytes(modelID string) int {
	return inferredPrecisionBytes(modelID)
}

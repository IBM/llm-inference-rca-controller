package optimizer

import (
	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceEstimator", func() {
	var (
		logger logr.Logger
	)

	BeforeEach(func() {
		logger = logr.Discard()
	})

	Describe("NewResourceEstimator", func() {
		It("should create estimator with default values", func() {
			estimator := NewResourceEstimator("llama-3-8b", "h100", 4.0, nil, logger)

			Expect(estimator).NotTo(BeNil())
			Expect(estimator.GetModelID()).To(Equal("llama-3-8b"))
			Expect(estimator.GetEffectiveBatchSize()).To(Equal(4.0))
			Expect(estimator.GetMemoryOverheadFactor()).To(Equal(1.2)) // Updated default
			Expect(estimator.GetGPUSpec()).NotTo(BeNil())
			Expect(estimator.GetGPUSpec().Name).To(Equal("H100 SXM"))
		})

		It("should use A100 as default GPU when no GPU specified", func() {
			estimator := NewResourceEstimator("llama-3-8b", "", 4.0, nil, logger)

			Expect(estimator.GetGPUSpec().Name).To(Equal("A100 80GB"))
		})

		It("should recognize known model families", func() {
			estimator := NewResourceEstimator("llama-3-8b", "h100", 4.0, nil, logger)

			Expect(estimator.GetParamsFound()).To(BeTrue())
			Expect(estimator.GetParams().ParametersB).To(Equal(8.0))
			Expect(estimator.GetParams().Layers).To(Equal(32))
			Expect(estimator.GetParams().HiddenSize).To(Equal(4096))
		})
	})

	Describe("NewResourceEstimatorWithArchParam", func() {
		It("should create estimator with custom architecture parameters", func() {
			params := autoscalingv1alpha1.ArchParams{
				ParametersB: 13.0,
				Layers:      40,
				KVHeads:     8,
				HiddenSize:  5120,
				QueryHeads:  40,
				Precision:   2,
			}

			estimator := NewResourceEstimatorWithArchParam("custom-model", "h100", 4.0, nil, params, logger)

			Expect(estimator).NotTo(BeNil())
			Expect(estimator.GetParams().ParametersB).To(Equal(13.0))
			Expect(estimator.GetParams().Layers).To(Equal(40))
			Expect(estimator.GetParams().HiddenSize).To(Equal(5120))
			Expect(estimator.GetMemoryOverheadFactor()).To(Equal(1.2))
		})
	})

	Describe("CalculateResourceRequirementsPerGPU", func() {
		var estimator *ResourcEstimator

		BeforeEach(func() {
			estimator = NewResourceEstimator("llama-3-8b", "h100", 4.0, nil, logger)
		})

		Context("with normal sequence length", func() {
			It("should calculate thread occupancy and memory correctly", func() {
				requirements := estimator.CalculateResourceRequirementsPerGPU(512, 128, 0.0, 1)

				Expect(requirements.ThreadOccupancy).To(BeNumerically(">", 0))
				Expect(requirements.ThreadOccupancy).To(BeNumerically("<=", 100))
				Expect(requirements.MemoryGB).To(BeNumerically(">", 0))
			})

			It("should account for cache hit ratio", func() {
				reqWithoutCache := estimator.CalculateResourceRequirementsPerGPU(512, 128, 0.0, 1)
				reqWithCache := estimator.CalculateResourceRequirementsPerGPU(512, 128, 0.8, 1)

				// With 80% cache hit, thread occupancy should be lower (unless already capped at 100%)
				if reqWithoutCache.ThreadOccupancy < 100 {
					Expect(reqWithCache.ThreadOccupancy).To(BeNumerically("<", reqWithoutCache.ThreadOccupancy))
				}
				// Memory should also be slightly lower due to fewer prefill tokens
				Expect(reqWithCache.MemoryGB).To(BeNumerically("<=", reqWithoutCache.MemoryGB))
			})
		})

		Context("with long context (> 8K tokens)", func() {
			It("should add attention memory overhead", func() {
				reqShort := estimator.CalculateResourceRequirementsPerGPU(4096, 128, 0.0, 1)
				reqLong := estimator.CalculateResourceRequirementsPerGPU(16384, 128, 0.0, 1)

				// Long context should have higher memory due to attention overhead
				Expect(reqLong.MemoryGB).To(BeNumerically(">", reqShort.MemoryGB))
			})

			It("should not add attention overhead for sequences <= 8K", func() {
				attentionMem := estimator.estimateAttentionMemory(8192, 1000)
				Expect(attentionMem).To(Equal(0.0))
			})

			It("should add sublinear attention overhead for sequences > 8K", func() {
				attentionMem16K := estimator.estimateAttentionMemory(16384, 1000)
				attentionMem32K := estimator.estimateAttentionMemory(32768, 1000)

				Expect(attentionMem16K).To(BeNumerically(">", 0))
				Expect(attentionMem32K).To(BeNumerically(">", attentionMem16K))
				// Should be sublinear (grows with power of 1.3, not linearly)
				// For 2x sequence length, memory should be less than 2x (approximately 2^1.3 = 2.46x)
				Expect(attentionMem32K).To(BeNumerically("<", attentionMem16K*2.5))
				Expect(attentionMem32K).To(BeNumerically(">", attentionMem16K*2.0))
			})
		})

		Context("with multi-GPU setup", func() {
			It("should divide resources across GPUs", func() {
				req1GPU := estimator.CalculateResourceRequirementsPerGPU(512, 128, 0.0, 1)
				req2GPU := estimator.CalculateResourceRequirementsPerGPU(512, 128, 0.0, 2)

				// Memory per GPU should be roughly half with 2 GPUs
				Expect(req2GPU.MemoryGB).To(BeNumerically("~", req1GPU.MemoryGB/2, 1.0))
			})
		})
	})

	Describe("Memory Calculations", func() {
		var estimator *ResourcEstimator

		BeforeEach(func() {
			estimator = NewResourceEstimator("llama-3-8b", "h100", 4.0, nil, logger)
		})

		It("should calculate model size correctly", func() {
			// 8B params × 2 bytes (FP16) = 16 GB
			Expect(estimator.modelSize).To(BeNumerically("~", 16.0, 0.1))
		})

		It("should calculate KV cache memory per token", func() {
			// memoryPerToken = 2 × layers × kvHeads × headDim × precision
			// headDim = hiddenSize / queryHeads = 4096 / 32 = 128
			// memoryPerToken = 2 × 32 × 8 × 128 × 2 = 131,072 bytes
			Expect(estimator.memoryPerToken).To(Equal(float64(131072)))
		})

		It("should include FFN intermediate in activation memory", func() {
			// activeMemoryPerToken should include both base and FFN intermediate
			// baseActivation = effectiveBatch × hiddenSize × precision = 4 × 4096 × 2 = 32,768
			// ffnIntermediate = effectiveBatch × hiddenSize × 4 × precision = 4 × 4096 × 4 × 2 = 131,072
			// total = 32,768 + 131,072 = 163,840
			Expect(estimator.activeMemoryPerPerfillToken).To(Equal(float64(163840)))
		})

		It("should apply memory overhead factor", func() {
			memory := estimator.estimateMemoryUsage(512, 128, 2048, 1)

			// Memory should be multiplied by overhead factor (1.2)
			Expect(estimator.memoryOverheadFactor).To(Equal(1.2))
			Expect(memory).To(BeNumerically(">", 0))
		})
	})

	Describe("Adaptive Tuning", func() {
		var estimator *ResourcEstimator

		BeforeEach(func() {
			estimator = NewResourceEstimator("llama-3-8b", "h100", 4.0, nil, logger)
		})

		Context("Memory Tuning", func() {
			DescribeTable("should tune memory overhead factor correctly",
				func(numGPUs int, memoryMultiplier float64, shouldIncrease bool) {
					initialFactor := estimator.memoryOverheadFactor
					estimatedMemory := estimator.estimateMemoryUsage(512, 128, 2048, numGPUs)
					measuredMemory := estimatedMemory * memoryMultiplier

					estimator.tuneWithMemoryMetric(2048, 512, 128, 0.0, measuredMemory, numGPUs)

					if shouldIncrease {
						Expect(estimator.memoryOverheadFactor).To(BeNumerically(">", initialFactor))
					}
					Expect(estimator.memoryOverheadFactor).To(BeNumerically("<=", MaxMemoryOverheadFactor))
				},
				Entry("increase overhead factor with 25% higher memory (single GPU)", 1, 1.25, true),
				Entry("cap overhead factor at maximum with 100% higher memory", 1, 2.0, false),
				Entry("work correctly with multiple GPUs", 4, 1.25, true),
			)

			It("should tune model size correctly with multiple GPUs", func() {
				// Create estimator with unknown model params
				unknownEstimator := NewResourceEstimator("unknown-model", "a100", 4.0, nil, logger)
				numGPUs := 8

				// Simulate measured memory for 8 GPUs
				estimatedMemory := unknownEstimator.estimateMemoryUsage(512, 128, 2048, numGPUs)
				measuredMemory := estimatedMemory * 1.1

				unknownEstimator.tuneWithMemoryMetric(2048, 512, 128, 0.0, measuredMemory, numGPUs)

				// Model size should be tuned based on multi-GPU measurements
				Expect(unknownEstimator.modelSize).To(BeNumerically(">", 0))
			})
		})

		Context("Compute Tuning", func() {
			DescribeTable("should tune max threads based on GPU knowledge",
				func(gpuName string, numGPUs int, shouldTune bool) {
					testEstimator := NewResourceEstimator("llama-3-8b", gpuName, 4.0, nil, logger)
					initialMaxThreads := testEstimator.gpuSpec.MaxThreads

					testEstimator.tuneWithComputeMetric(2048, 512, 128, 80.0, numGPUs)

					if shouldTune {
						Expect(testEstimator.gpuSpec.MaxThreads).To(BeNumerically(">=", MinMaxThreads))
						Expect(testEstimator.gpuSpec.MaxThreads).NotTo(Equal(initialMaxThreads))
					} else {
						Expect(testEstimator.gpuSpec.MaxThreads).To(Equal(initialMaxThreads))
					}
				},
				Entry("tune max threads when GPU is unknown (single GPU)", "unknown-gpu", 1, true),
				Entry("not tune when GPU is known", "h100", 1, false),
				Entry("tune max threads correctly with multiple GPUs", "unknown-gpu", 4, true),
			)

			It("should scale thread calculation with GPU count", func() {
				// Create two estimators with unknown GPU
				estimator1GPU := NewResourceEstimator("llama-3-8b", "unknown-gpu", 4.0, nil, logger)
				estimator4GPU := NewResourceEstimator("llama-3-8b", "unknown-gpu", 4.0, nil, logger)

				// Same utilization, different GPU counts
				estimator1GPU.tuneWithComputeMetric(2048, 512, 128, 80.0, 1)
				estimator4GPU.tuneWithComputeMetric(2048, 512, 128, 80.0, 4)

				// With 4 GPUs, max threads per GPU should be roughly 1/4 of single GPU
				// (accounting for the same total workload distributed across GPUs)
				ratio := float64(estimator1GPU.gpuSpec.MaxThreads) / float64(estimator4GPU.gpuSpec.MaxThreads)
				Expect(ratio).To(BeNumerically("~", 4.0, 0.5))
			})
		})
	})

	Describe("GPU Registry", func() {
		DescribeTable("should have correct GPU specifications",
			func(gpuName, expectedName string, expectedVRAM int, expectedMemoryBW, expectedPeakTFLOPS float64, expectedMaxThreads int) {
				_, spec := getGPUSpecs(gpuName, logger)

				Expect(spec.Name).To(Equal(expectedName))
				Expect(spec.VRAM).To(Equal(expectedVRAM))
				Expect(spec.MemoryBW).To(Equal(expectedMemoryBW))
				Expect(spec.PeakTFLOPS).To(Equal(expectedPeakTFLOPS))
				Expect(spec.MaxThreads).To(Equal(expectedMaxThreads))
			},
			Entry("H200", "h200", "H200 SXM", 141, 4.8, 989.0, 249856),
			Entry("H100", "h100", "H100 SXM", 80, 3.35, 989.0, 184320),
			Entry("A100", "a100", "A100 80GB", 80, 2.0, 312.0, 108544),
			Entry("L4", "l4", "L4 Tensor Core", 24, 0.3, 121.0, 30720),
		)

		It("should fallback to A100 for unknown GPU", func() {
			found, spec := getGPUSpecs("unknown-gpu", logger)

			Expect(found).To(BeFalse())
			Expect(spec.Name).To(Equal("A100 80GB"))
		})
	})

	Describe("Model Family Registry", func() {
		DescribeTable("should recognize known model families",
			func(modelID string, expectedParamsB float64, expectedLayers, expectedHiddenSize int) {
				found, params := inferredArchParams(modelID, logger)

				Expect(found).To(BeTrue())
				Expect(params.ParametersB).To(Equal(expectedParamsB))
				Expect(params.Layers).To(Equal(expectedLayers))
				Expect(params.HiddenSize).To(Equal(expectedHiddenSize))
			},
			Entry("Llama-3-8B", "llama-3-8b", 8.0, 32, 4096),
			Entry("Llama-3-70B", "llama-3-70b", 70.0, 80, 8192),
			Entry("Mistral-Nemo", "mistral-nemo", 12.0, 40, 5120),
			Entry("Phi-3-Mini", "phi-3-mini", 3.8, 32, 3072),
		)

		It("should extract parameters from model ID", func() {
			valid, params := extractParams("custom-model-13b", logger)

			Expect(valid).To(BeTrue())
			Expect(params).To(Equal(13.0))
		})

		It("should use default for unknown models", func() {
			found, params := inferredArchParams("unknown-model", logger)

			Expect(found).To(BeFalse())
			Expect(params.ParametersB).To(Equal(8.0)) // Default
		})
	})

	Describe("Precision Inference", func() {
		DescribeTable("should infer precision from model ID",
			func(modelID string, expectedPrecision int) {
				precision := inferredPrecisionBytes(modelID)
				Expect(precision).To(Equal(expectedPrecision))
			},
			Entry("FP8 precision", "model-fp8", 1),
			Entry("FP16 precision", "model-fp16", 2),
			Entry("default to FP16 for unknown", "model-unknown", 2),
			Entry("quantized models (KV cache still FP16)", "model-awq-4bit", 2),
		)
	})

	Describe("Thread Occupancy", func() {
		var estimator *ResourcEstimator

		BeforeEach(func() {
			estimator = NewResourceEstimator("llama-3-8b", "h100", 4.0, nil, logger)
		})

		It("should calculate occupancy based on prefill and decode threads", func() {
			occupancy := estimator.estimateThreadOccupancy(4.0, 2048, 1)

			Expect(occupancy).To(BeNumerically(">", 0))
			Expect(occupancy).To(BeNumerically("<=", 100))
		})

		It("should cap occupancy at 100%", func() {
			// Use very large prefill tokens to exceed capacity
			occupancy := estimator.estimateThreadOccupancy(100.0, 100000, 1)

			Expect(occupancy).To(Equal(100.0))
		})

		It("should scale with number of GPUs", func() {
			occupancy1GPU := estimator.estimateThreadOccupancy(4.0, 2048, 1)
			occupancy2GPU := estimator.estimateThreadOccupancy(4.0, 2048, 2)

			// With 2 GPUs, occupancy per GPU should be roughly half (unless capped at 100%)
			if occupancy1GPU < 100 {
				Expect(occupancy2GPU).To(BeNumerically("~", occupancy1GPU/2, 5.0))
			} else {
				// If 1 GPU is at 100%, 2 GPUs should be less than or equal to 100%
				Expect(occupancy2GPU).To(BeNumerically("<=", 100.0))
			}
		})
	})
})

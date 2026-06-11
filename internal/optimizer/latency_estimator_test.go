package optimizer

import (
	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("LatencyEstimator", func() {
	var (
		logger logr.Logger
	)

	BeforeEach(func() {
		logger = logr.Discard()
	})

	Describe("NewLatencyEstimatorWithHint", func() {
		It("should create estimator with hint values", func() {
			hint := autoscalingv1alpha1.Hint{
				ModelID:            "llama-3-8b",
				BatchSize:          1,
				MaxNumSeqs:         32,
				PrefixCacheHitRate: 0.8,
				KVCacheSpeedup:     0.5,
			}

			params := &autoscalingv1alpha1.ArchParams{
				ParametersB: 8.0,
				Layers:      32,
				KVHeads:     8,
				HiddenSize:  4096,
				QueryHeads:  32,
				Precision:   2,
			}

			gpuSpec := &GPUSpecs{
				Name:                           "H100 SXM",
				VRAM:                           80,
				MemoryBW:                       3.35,
				PeakTFLOPS:                     989.0,
				MaxThreads:                     184320,
				UnderprovisionContentionImpact: 0.15,
				OverprovisionSpeedUp:           0.35,
			}

			estimator := NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)

			Expect(estimator).NotTo(BeNil())
			Expect(estimator.PrefixCacheHitRate).To(Equal(0.8))
			Expect(estimator.KVCacheSpeedup).To(Equal(0.5))
		})
	})

	Describe("estimateLatency", func() {
		var (
			estimator *LatencyEstimator
			hint      autoscalingv1alpha1.Hint
			params    *autoscalingv1alpha1.ArchParams
			gpuSpec   *GPUSpecs
		)

		BeforeEach(func() {
			hint = autoscalingv1alpha1.Hint{
				ModelID:            "llama-3-8b",
				BatchSize:          1,
				MaxNumSeqs:         10,
				PrefixCacheHitRate: 0.0,
				KVCacheSpeedup:     0.5,
			}

			params = &autoscalingv1alpha1.ArchParams{
				ParametersB: 8.0,
				Layers:      32,
				KVHeads:     8,
				HiddenSize:  4096,
				QueryHeads:  32,
				Precision:   2,
			}

			gpuSpec = &GPUSpecs{
				Name:                           "H100 SXM",
				VRAM:                           80,
				MemoryBW:                       3.35,
				PeakTFLOPS:                     989.0,
				MaxThreads:                     184320,
				UnderprovisionContentionImpact: 0.15,
				OverprovisionSpeedUp:           0.35,
			}

			estimator = NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)
		})

		It("should calculate latency correctly with single GPU", func() {
			result := estimator.estimateLatency(100, 50, 1, 0.0)

			// Check that latency components are positive
			Expect(result.ITL).To(BeNumerically(">", 0))
			Expect(result.Prefill).To(BeNumerically(">", 0))
			Expect(result.Decode).To(BeNumerically(">", 0))
		})

		It("should reduce prefill latency with KV cache enabled", func() {
			hint.PrefixCacheHitRate = 0.8
			estimatorWithCache := NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)
			resultWithCache := estimatorWithCache.estimateLatency(100, 50, 1, 0.0)

			// Compare with no cache
			resultNoCache := estimator.estimateLatency(100, 50, 1, 0.0)

			// With cache, prefill should be reduced
			Expect(resultWithCache.Prefill).To(BeNumerically("<", resultNoCache.Prefill))
			Expect(resultWithCache.KVCacheReduction).To(BeNumerically(">", 0))
		})

		It("should reduce compute latency with multiple GPUs", func() {
			// Use larger prompt/output to see parallelization effect
			result1GPU := estimator.estimateLatency(1000, 500, 1, 0.0)
			result4GPU := estimator.estimateLatency(1000, 500, 4, 0.0)

			// Multi-GPU divides compute by numGPU but adds sync overhead
			// For this test, just verify both results are valid (positive)
			Expect(result1GPU.Prefill).To(BeNumerically(">", 0))
			Expect(result4GPU.Prefill).To(BeNumerically(">", 0))
			Expect(result1GPU.ITL).To(BeNumerically(">", 0))
			Expect(result4GPU.ITL).To(BeNumerically(">", 0))

			// The ITL should benefit from parallelization (no prefill overhead)
			Expect(result4GPU.ITL).To(BeNumerically("<", result1GPU.ITL))
		})

		DescribeTable("should handle allocation impact correctly",
			func(allocationImpact float64, expectIncrease bool) {
				resultBaseline := estimator.estimateLatency(100, 50, 1, 0.0)
				resultWithImpact := estimator.estimateLatency(100, 50, 1, allocationImpact)

				if expectIncrease {
					// With contention, latency should increase
					Expect(resultWithImpact.Prefill).To(BeNumerically(">", resultBaseline.Prefill))
					Expect(resultWithImpact.ITL).To(BeNumerically(">", resultBaseline.ITL))
				} else {
					// With speedup, latency should decrease
					Expect(resultWithImpact.Prefill).To(BeNumerically("<", resultBaseline.Prefill))
					Expect(resultWithImpact.ITL).To(BeNumerically("<", resultBaseline.ITL))
				}
			},
			Entry("increase latency with resource contention (positive impact)", 0.2, true),
			Entry("decrease latency with resource speedup (negative impact)", -0.1, false),
		)

		DescribeTable("should scale latency with workload parameters",
			func(promptLen1, outputLen1, promptLen2, outputLen2 int, checkPrefill, checkDecode bool) {
				result1 := estimator.estimateLatency(promptLen1, outputLen1, 1, 0.0)
				result2 := estimator.estimateLatency(promptLen2, outputLen2, 1, 0.0)

				if checkPrefill {
					// Longer prompts should have higher prefill latency
					Expect(result2.Prefill).To(BeNumerically(">", result1.Prefill))
				}
				if checkDecode {
					// Longer outputs should have higher decode latency
					Expect(result2.Decode).To(BeNumerically(">", result1.Decode))
				}
			},
			Entry("scale prefill latency with prompt length", 50, 50, 200, 50, true, false),
			Entry("scale decode latency with output length", 100, 25, 100, 100, false, true),
		)
	})

	Describe("computePrefillLatencyByPrompt", func() {
		var (
			estimator *LatencyEstimator
		)

		BeforeEach(func() {
			hint := autoscalingv1alpha1.Hint{
				ModelID:            "llama-3-8b",
				BatchSize:          1,
				MaxNumSeqs:         10,
				PrefixCacheHitRate: 0.0,
				KVCacheSpeedup:     0.5,
			}

			params := &autoscalingv1alpha1.ArchParams{
				ParametersB: 8.0,
				Layers:      32,
				KVHeads:     8,
				HiddenSize:  4096,
				QueryHeads:  32,
				Precision:   2,
			}

			gpuSpec := &GPUSpecs{
				Name:       "H100 SXM",
				VRAM:       80,
				MemoryBW:   3.35,
				PeakTFLOPS: 989.0,
				MaxThreads: 184320,
			}

			estimator = NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)
		})

		It("should compute prefill latency correctly", func() {
			prefill, kvReduction := estimator.computePrefillLatencyByPrompt(100, 1.0, 0.0)

			Expect(prefill).To(BeNumerically(">", 0))
			Expect(kvReduction).To(Equal(0.0)) // No cache hit
		})

		It("should apply KV cache reduction when enabled", func() {
			estimator.PrefixCacheHitRate = 0.8
			prefill, kvReduction := estimator.computePrefillLatencyByPrompt(100, 1.0, 0.0)

			Expect(prefill).To(BeNumerically(">", 0))
			Expect(kvReduction).To(BeNumerically(">", 0))
		})
	})

	Describe("computeInterTokenLatency", func() {
		var (
			estimator *LatencyEstimator
		)

		BeforeEach(func() {
			hint := autoscalingv1alpha1.Hint{
				ModelID:            "llama-3-8b",
				BatchSize:          1,
				MaxNumSeqs:         10,
				PrefixCacheHitRate: 0.0,
				KVCacheSpeedup:     0.5,
			}

			params := &autoscalingv1alpha1.ArchParams{
				ParametersB: 8.0,
				Layers:      32,
				KVHeads:     8,
				HiddenSize:  4096,
				QueryHeads:  32,
				Precision:   2,
			}

			gpuSpec := &GPUSpecs{
				Name:       "H100 SXM",
				VRAM:       80,
				MemoryBW:   3.35,
				PeakTFLOPS: 989.0,
				MaxThreads: 184320,
			}

			estimator = NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)
		})

		It("should compute ITL correctly", func() {
			itl := estimator.computeInterTokenLatency(100, 1, 0.0)

			Expect(itl).To(BeNumerically(">", 0))
		})

		It("should scale with prompt length (KV cache read)", func() {
			itlShort := estimator.computeInterTokenLatency(50, 1, 0.0)
			itlLong := estimator.computeInterTokenLatency(200, 1, 0.0)

			// Longer prompts mean more KV cache to read
			Expect(itlLong).To(BeNumerically(">", itlShort))
		})

		It("should decrease with more GPUs", func() {
			itl1GPU := estimator.computeInterTokenLatency(100, 1, 0.0)
			itl4GPU := estimator.computeInterTokenLatency(100, 4, 0.0)

			// More GPUs should reduce ITL
			Expect(itl4GPU).To(BeNumerically("<", itl1GPU))
		})
	})

	Describe("estimateStructuralOverhead", func() {
		var (
			estimator *LatencyEstimator
		)

		BeforeEach(func() {
			hint := autoscalingv1alpha1.Hint{
				ModelID:    "llama-3-8b",
				BatchSize:  1,
				MaxNumSeqs: 10,
			}

			params := &autoscalingv1alpha1.ArchParams{
				ParametersB: 8.0,
				Layers:      32,
				KVHeads:     8,
				HiddenSize:  4096,
				QueryHeads:  32,
				Precision:   2,
			}

			gpuSpec := &GPUSpecs{
				Name:       "H100 SXM",
				VRAM:       80,
				MemoryBW:   3.35,
				PeakTFLOPS: 989.0,
				MaxThreads: 184320,
			}

			estimator = NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)
		})

		It("should have minimal overhead for single GPU", func() {
			overhead := estimator.estimateStructuralOverhead(1.0)

			Expect(overhead).To(BeNumerically(">", 0))
			Expect(overhead).To(BeNumerically("<", 20.0)) // Should be small
		})

		It("should increase overhead with more GPUs (sync cost)", func() {
			overhead1GPU := estimator.estimateStructuralOverhead(1.0)
			overhead4GPU := estimator.estimateStructuralOverhead(4.0)

			// Multi-GPU has synchronization overhead
			Expect(overhead4GPU).To(BeNumerically(">", overhead1GPU))
		})
	})

	Describe("LatencyResult", func() {
		It("should have all required fields", func() {
			result := LatencyResult{
				ITL:              10.0,
				Prefill:          80.0,
				Decode:           200.0,
				E2E:              300.0,
				KVCacheReduction: 20.0,
			}

			Expect(result.ITL).To(Equal(10.0))
			Expect(result.Prefill).To(Equal(80.0))
			Expect(result.Decode).To(Equal(200.0))
			Expect(result.ServiceTime()).To(Equal(280.0)) // ServiceTime() is a method
			Expect(result.E2E).To(Equal(300.0))
			Expect(result.KVCacheReduction).To(Equal(20.0))
		})
	})

	Describe("Integration with different models", func() {
		It("should work with different model sizes", func() {
			models := []struct {
				name   string
				params float64
			}{
				{"phi-3-mini", 3.8},
				{"llama-3-8b", 8.0},
				{"llama-3-70b", 70.0},
			}

			for _, model := range models {
				hint := autoscalingv1alpha1.Hint{
					ModelID:    model.name,
					BatchSize:  1,
					MaxNumSeqs: 10,
				}

				params := &autoscalingv1alpha1.ArchParams{
					ParametersB: model.params,
					Layers:      32,
					KVHeads:     8,
					HiddenSize:  4096,
					QueryHeads:  32,
					Precision:   2,
				}

				gpuSpec := &GPUSpecs{
					Name:       "H100 SXM",
					VRAM:       80,
					MemoryBW:   3.35,
					PeakTFLOPS: 989.0,
					MaxThreads: 184320,
				}

				estimator := NewLatencyEstimatorWithHint(hint, params, gpuSpec, logger)
				result := estimator.estimateLatency(100, 50, 1, 0.0)

				Expect(result.Prefill).To(BeNumerically(">", 0))
				Expect(result.ITL).To(BeNumerically(">", 0))
			}
		})
	})
})

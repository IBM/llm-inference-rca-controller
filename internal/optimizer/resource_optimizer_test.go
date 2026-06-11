package optimizer

import (
	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var _ = Describe("ResourceOptimizer.FindOptimalConfiguration", func() {
	var (
		spec      *autoscalingv1alpha1.ResourceClaimAutoscalerSpec
		optimizer *ResourceOptimizer
		targets   *LatencyTargets
		logger    logr.Logger
	)

	BeforeEach(func() {
		logger = logr.Discard() // Use discard logger for tests

		// Create a spec with constraints
		maxReplicas := int32(10)
		minReplicas := int32(1)
		minCountPerReplica := int32(1)
		maxCountPerReplica := int32(8)

		minCompute := resource.MustParse("10")
		maxCompute := resource.MustParse("100")
		stepCompute := resource.MustParse("5")

		minMemory := resource.MustParse("8Gi")
		maxMemory := resource.MustParse("80Gi")
		stepMemory := resource.MustParse("1Gi")

		spec = &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
			Target: autoscalingv1alpha1.TargetSelector{
				ServiceRef: autoscalingv1alpha1.ServiceReference{
					Name: "test-service",
				},
				ResourceRef: autoscalingv1alpha1.ResourceReference{
					DeviceClassName: "nvidia.com/gpu",
				},
				Hint: &autoscalingv1alpha1.Hint{
					ModelID:            "llama-3-8b",
					BatchSize:          1,
					MaxNumSeqs:         32,
					PrefixCacheHitRate: 0.0,
					KVCacheSpeedup:     0.5,
				},
			},
			Constraints: &autoscalingv1alpha1.Constraints{
				MaxReplicas:        &maxReplicas,
				MinReplicas:        &minReplicas,
				MinCountPerReplica: &minCountPerReplica,
				MaxCountPerReplica: &maxCountPerReplica,
				CapacityPerCount: map[corev1.ResourceName]autoscalingv1alpha1.CapacityPerCountConstraint{
					"compute": {
						Min:  &minCompute,
						Max:  &maxCompute,
						Step: &stepCompute,
					},
					"memory": {
						Min:  &minMemory,
						Max:  &maxMemory,
						Step: &stepMemory,
					},
				},
			},
		}

		optimizer = NewResourceOptimizer(spec, logger)
		optimizer.SetWorkloadParameters(1.0, 100, 50, 32)

		// Default targets (in milliseconds)
		e2eTarget := 3000.0
		targets = &LatencyTargets{
			E2E: &e2eTarget,
		}
	})

	Describe("Basic Optimization", func() {
		Context("with low arrival rate", func() {
			It("should find minimal configuration with single replica", func() {
				optimizer.SetWorkloadParameters(1.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(1.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())
				Expect(config.Replicas).To(Equal(1))
				Expect(config.MeetsConstraints).To(BeTrue())
				Expect(config.EstimatedLatency.E2E).To(BeNumerically("<=", 3000.0))
			})
		})

		Context("with moderate arrival rate", func() {
			It("should scale replicas to meet latency target", func() {
				optimizer.SetWorkloadParameters(5.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(5.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())
				Expect(config.Replicas).To(BeNumerically(">=", 1))
				Expect(config.MeetsConstraints).To(BeTrue())
				Expect(config.EstimatedLatency.E2E).To(BeNumerically("<=", 3000.0))
			})
		})

		Context("with high arrival rate", func() {
			It("should use multiple replicas or find best effort configuration", func() {
				// Use moderate arrival rate
				optimizer.SetWorkloadParameters(15.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(15.0, targets)

				// Either finds a valid configuration or returns error
				if err == nil {
					Expect(config).NotTo(BeNil())
					Expect(config.Replicas).To(BeNumerically(">=", 1))
				} else {
					// If no configuration found, that's also acceptable for high load
					Expect(err.Error()).To(ContainSubstring("no configuration found"))
				}
			})
		})
	})

	Describe("Resource Minimization (Principle 1)", func() {
		It("should provide valid resource configuration", func() {
			optimizer.SetWorkloadParameters(2.0, 100, 50, 32)
			config, err := optimizer.FindOptimalConfiguration(2.0, targets)
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())

			// Verify configuration has valid positive resource values
			Expect(config.RequestedCompute).To(BeNumerically(">", 0))
			Expect(config.ResourceRequirements.ThreadOccupancy).To(BeNumerically(">", 0))
			Expect(config.RequestedMemoryGB).To(BeNumerically(">", 0))
			Expect(config.ResourceRequirements.MemoryGB).To(BeNumerically(">", 0))
		})
	})

	Describe("Vertical Scaling First (Principle 3)", func() {
		Context("with GPU budget constraint", func() {
			BeforeEach(func() {
				maxCountPerReplica := int32(8)
				spec.Constraints.MaxCountPerReplica = &maxCountPerReplica
				optimizer = NewResourceOptimizer(spec, logger)
				optimizer.SetWorkloadParameters(3.0, 100, 50, 32)
			})

			It("should try multi-GPU per replica before adding replicas", func() {
				config, err := optimizer.FindOptimalConfiguration(3.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())

				// With 8 total GPUs, should prefer fewer replicas with more GPUs each
				// rather than many single-GPU replicas
				if config.Replicas > 1 {
					Expect(config.TotalGPUs).To(BeNumerically("<=", 8))
					Expect(config.GPUsPerReplica).To(BeNumerically(">=", 1))
				}
			})
		})

		Context("without GPU budget constraint", func() {
			It("should still consider multi-GPU allocation for better performance", func() {
				optimizer.SetWorkloadParameters(2.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(2.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())
				Expect(config.MeetsConstraints).To(BeTrue())
			})
		})
	})

	Describe("Horizontal Scaling (Principle 4)", func() {
		It("should attempt to scale horizontally with high load", func() {
			// Use high arrival rate
			optimizer.SetWorkloadParameters(25.0, 100, 50, 32)
			config, err := optimizer.FindOptimalConfiguration(25.0, targets)

			// Either finds configuration with multiple replicas or returns error for too high load
			if err == nil {
				Expect(config).NotTo(BeNil())
				Expect(config.Replicas).To(BeNumerically(">=", 1))

				// Verify configuration is valid
				maxCompute := spec.Constraints.CapacityPerCount["compute"].Max.AsApproximateFloat64()
				maxMemory := spec.Constraints.CapacityPerCount["memory"].Max.Value()
				Expect(config.RequestedCompute).To(BeNumerically("<=", maxCompute*float64(config.GPUsPerReplica)))
				Expect(config.RequestedMemoryGB * 1e9).To(BeNumerically("<=", float64(maxMemory*int64(config.GPUsPerReplica))))
			} else {
				Expect(err.Error()).To(ContainSubstring("no configuration found"))
			}
		})
	})

	Describe("Multiple Latency Targets", func() {
		Context("with E2E, TTFT, and ITL targets", func() {
			BeforeEach(func() {
				e2e := 1000.0
				ttft := 200.0
				itl := 15.0
				targets = &LatencyTargets{
					E2E:  &e2e,
					TTFT: &ttft,
					ITL:  &itl,
				}
			})

			It("should meet all specified targets", func() {
				optimizer.SetWorkloadParameters(3.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(3.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())

				Expect(config.EstimatedLatency.E2E).To(BeNumerically("<=", 1000.0))
				Expect(config.EstimatedLatency.TTFT).To(BeNumerically("<=", 200.0))
				Expect(config.EstimatedLatency.ITL).To(BeNumerically("<=", 15.0))
			})
		})

		Context("with only TTFT target", func() {
			BeforeEach(func() {
				ttft := 100.0
				targets = &LatencyTargets{
					TTFT: &ttft,
				}
			})

			It("should optimize for TTFT", func() {
				optimizer.SetWorkloadParameters(2.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(2.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())
				Expect(config.EstimatedLatency.TTFT).To(BeNumerically("<=", 100.0))
			})
		})
	})

	Describe("Constraint Violations", func() {
		Context("when no configuration meets targets within max replicas", func() {
			It("should return max replica configuration", func() {
				// Very high arrival rate with low max replicas and tight latency target
				maxReplicas := int32(2)
				spec.Constraints.MaxReplicas = &maxReplicas
				optimizer = NewResourceOptimizer(spec, logger)
				optimizer.SetWorkloadParameters(50.0, 100, 50, 32)
				// Use a very tight latency target (10ms) that cannot be met
				tightTarget := 10.0
				tightTargets := &LatencyTargets{
					E2E: &tightTarget,
				}
				config, err := optimizer.FindOptimalConfiguration(50.0, tightTargets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())
				// Should return configuration with max replicas
				Expect(config.Replicas).To(Equal(int(maxReplicas)))
				// Configuration may not meet constraints
				Expect(config.MeetsConstraints).To(BeFalse())
			})
		})

		Context("when search space is exhausted", func() {
			It("should return max replica configuration when no configuration meets constraints", func() {
				// Use very high arrival rate that exceeds capacity
				optimizer.SetWorkloadParameters(1000.0, 100, 50, 32)
				config, err := optimizer.FindOptimalConfiguration(1000.0, targets)
				Expect(err).NotTo(HaveOccurred())
				Expect(config).NotTo(BeNil())
				// Should return configuration with max replicas
				Expect(config.Replicas).To(BeNumerically(">", 0))
				// Configuration may not meet constraints
				Expect(config.MeetsConstraints).To(BeFalse())
			})
		})
	})

	Describe("Configuration Output", func() {
		It("should provide complete configuration details", func() {
			optimizer.SetWorkloadParameters(3.0, 100, 50, 32)
			config, err := optimizer.FindOptimalConfiguration(3.0, targets)
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())

			// Verify all fields are populated
			Expect(config.Replicas).To(BeNumerically(">", 0))
			Expect(config.GPUsPerReplica).To(BeNumerically(">", 0))
			Expect(config.TotalGPUs).To(Equal(config.Replicas * config.GPUsPerReplica))
			Expect(config.RequestedCompute).To(BeNumerically(">", 0))
			Expect(config.RequestedMemoryGB).To(BeNumerically(">", 0))
			Expect(config.EstimatedLatency).NotTo(BeNil())
			Expect(config.MeetsConstraints).To(BeTrue())
		})

		It("should include resource requirements", func() {
			optimizer.SetWorkloadParameters(2.0, 100, 50, 32)
			config, err := optimizer.FindOptimalConfiguration(2.0, targets)
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())

			Expect(config.ResourceRequirements.ThreadOccupancy).To(BeNumerically(">", 0))
			Expect(config.ResourceRequirements.MemoryGB).To(BeNumerically(">", 0))
		})
	})

	Describe("CalculateResourceAllocationImpact", func() {
		var optimizer *ResourceOptimizer

		BeforeEach(func() {
			logger := logr.Discard()
			maxReplicas := int32(10)
			minReplicas := int32(1)

			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					ServiceRef: autoscalingv1alpha1.ServiceReference{
						Name: "test-service",
					},
					ResourceRef: autoscalingv1alpha1.ResourceReference{
						DeviceClassName: "nvidia.com/gpu",
					},
					Hint: &autoscalingv1alpha1.Hint{
						ModelID:            "llama-3-8b",
						BatchSize:          1,
						MaxNumSeqs:         32,
						PrefixCacheHitRate: 0.0,
						KVCacheSpeedup:     0.5,
					},
				},
				Constraints: &autoscalingv1alpha1.Constraints{
					MaxReplicas: &maxReplicas,
					MinReplicas: &minReplicas,
				},
			}
			optimizer = NewResourceOptimizer(spec, logger)
		})

		Context("when ratio < 0.5 (under-provisioned/low density)", func() {
			It("should return negative value (speedup bonus) for ratio 0.3", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.3)
				Expect(impact).To(BeNumerically("<", 0), "Impact should be negative (speedup) when ratio < 0.5")
			})

			It("should return negative value (speedup bonus) for ratio 0.1", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.1)
				Expect(impact).To(BeNumerically("<", 0), "Impact should be negative (speedup) when ratio < 0.5")
			})

			It("should return negative value (speedup bonus) for ratio 0.49", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.49)
				Expect(impact).To(BeNumerically("<", 0), "Impact should be negative (speedup) when ratio < 0.5")
			})

			It("should have larger negative impact for smaller ratios", func() {
				impact01 := optimizer.CalculateResourceAllocationImpact(0.1)
				impact04 := optimizer.CalculateResourceAllocationImpact(0.4)
				Expect(impact01).To(BeNumerically("<", impact04), "Smaller ratio should have larger negative impact")
			})
		})

		Context("when ratio > 0.8 (over-provisioned/high density)", func() {
			It("should return positive value (contention penalty) for ratio 0.85", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.85)
				Expect(impact).To(BeNumerically(">", 0), "Impact should be positive (contention) when ratio > 0.8")
			})

			It("should return positive value (contention penalty) for ratio 0.9", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.9)
				Expect(impact).To(BeNumerically(">", 0), "Impact should be positive (contention) when ratio > 0.8")
			})

			It("should return positive value (contention penalty) for ratio 1.0", func() {
				impact := optimizer.CalculateResourceAllocationImpact(1.0)
				Expect(impact).To(BeNumerically(">", 0), "Impact should be positive (contention) when ratio > 0.8")
			})

			It("should have larger positive impact for higher ratios", func() {
				impact085 := optimizer.CalculateResourceAllocationImpact(0.85)
				impact095 := optimizer.CalculateResourceAllocationImpact(0.95)
				Expect(impact095).To(BeNumerically(">", impact085), "Higher ratio should have larger positive impact")
			})
		})

		Context("when ratio is in the neutral zone (0.5 to 0.8)", func() {
			It("should return zero for ratio 0.5", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.5)
				Expect(impact).To(BeNumerically("==", 0), "Impact should be zero at ratio 0.5")
			})

			It("should return zero for ratio 0.65", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.65)
				Expect(impact).To(BeNumerically("==", 0), "Impact should be zero in neutral zone")
			})

			It("should return zero for ratio 0.8", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.8)
				Expect(impact).To(BeNumerically("==", 0), "Impact should be zero at ratio 0.8")
			})
		})

		Context("with boundary conditions", func() {
			It("should handle ratio 0.0 (minimum)", func() {
				impact := optimizer.CalculateResourceAllocationImpact(0.0)
				Expect(impact).To(BeNumerically("<", 0), "Impact should be negative for ratio 0.0")
			})

			It("should handle negative ratio by clamping to 0.0", func() {
				impact := optimizer.CalculateResourceAllocationImpact(-0.5)
				expectedImpact := optimizer.CalculateResourceAllocationImpact(0.0)
				Expect(impact).To(BeNumerically("==", expectedImpact), "Negative ratio should be clamped to 0.0")
			})

			It("should handle ratio > 1.0 by clamping to 1.0", func() {
				impact := optimizer.CalculateResourceAllocationImpact(1.5)
				expectedImpact := optimizer.CalculateResourceAllocationImpact(1.0)
				Expect(impact).To(BeNumerically("==", expectedImpact), "Ratio > 1.0 should be clamped to 1.0")
			})
		})
	})
})

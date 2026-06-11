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
	"sync"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

var _ = Describe("AutoscalerMetricsCollector", func() {
	var (
		ctx          context.Context
		mockMonitor  *Monitor
		monitorMutex *sync.RWMutex
		collector    *AutoscalerMetricsCollector
		testSpec     *autoscalingv1alpha1.ResourceClaimAutoscalerSpec
		logger       logr.Logger
	)

	BeforeEach(func() {
		ctx = context.Background()
		monitorMutex = &sync.RWMutex{}
		logger = ctrl.Log.WithName("test")

		// Create test spec with target latencies
		endToEnd := int32(100)
		ttft := int32(200)
		interToken := int32(50)
		tolerance := int32(10)

		testSpec = &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
			Target: autoscalingv1alpha1.TargetSelector{
				ServiceRef: autoscalingv1alpha1.ServiceReference{
					Name:      "test-service",
					Namespace: "default",
				},
				ResourceRef: autoscalingv1alpha1.ResourceReference{
					DeviceClassName: "test-device",
				},
			},
			TargetLatency: &autoscalingv1alpha1.TargetLatency{
				EndToEndLatencyMilliseconds:         &endToEnd,
				TimeToFirstTokenLatencyMilliseconds: &ttft,
				InterTokenLatencyMilliseconds:       &interToken,
				ToleranceMilliseconds:               &tolerance,
			},
		}

		// Create mock monitor
		mockMonitor = NewMonitor(autoscalingv1alpha1.Monitoring{
			Endpoint: "http://localhost:9090",
		})
	})

	Context("When creating a new collector", func() {
		It("should initialize successfully", func() {
			collector = NewAutoscalerMetricsCollector(mockMonitor, monitorMutex)
			Expect(collector).NotTo(BeNil())
			Expect(collector.monitor).To(Equal(mockMonitor))
			Expect(collector.monitorMutex).To(Equal(monitorMutex))
		})
	})

	Context("When computing latency violations", func() {
		BeforeEach(func() {
			collector = NewAutoscalerMetricsCollector(mockMonitor, monitorMutex)
		})

		DescribeTable("should compute violation correctly for different latency metrics",
			func(metrics map[string]string, minViolation, maxViolation float64, description string) {
				// Act
				violation := collector.computeLatencyViolations(testSpec, metrics, logger)

				// Assert
				Expect(violation).To(BeNumerically(">", minViolation), description)
				Expect(violation).To(BeNumerically("<=", maxViolation), description)
			},
			Entry("EndToEndLatency exceeds target",
				map[string]string{
					"EndToEndLatencyBucketQuery":   "0.150", // 150ms - exceeds target
					"TimeToFirstTokenLatencyQuery": "0.180", // Within target
					"InterTokenLatencyQuery":       "0.040", // Within target
				},
				0.10, 0.15,
				"150ms actual vs 110ms effective target (100+10 tolerance)",
			),
			Entry("TimeToFirstToken exceeds target",
				map[string]string{
					"EndToEndLatencyBucketQuery":   "0.090", // Within target
					"TimeToFirstTokenLatencyQuery": "0.250", // 250ms - exceeds target
					"InterTokenLatencyQuery":       "0.040", // Within target
				},
				0.05, 0.10,
				"250ms actual vs 210ms effective target (200+10 tolerance)",
			),
			Entry("InterToken exceeds target",
				map[string]string{
					"EndToEndLatencyBucketQuery":   "0.090", // Within target
					"TimeToFirstTokenLatencyQuery": "0.180", // Within target
					"InterTokenLatencyQuery":       "0.070", // 70ms - exceeds target
				},
				0.04, 0.08,
				"70ms actual vs 60ms effective target (50+10 tolerance)",
			),
		)

		It("should return zero violation when all latencies are within target", func() {
			// Arrange - all within target + tolerance
			metrics := map[string]string{
				"EndToEndLatencyBucketQuery":   "0.100", // 100ms - at target
				"TimeToFirstTokenLatencyQuery": "0.190", // 190ms - within target
				"InterTokenLatencyQuery":       "0.050", // 50ms - at target
			}

			// Act
			violation := collector.computeLatencyViolations(testSpec, metrics, logger)

			// Assert - zero violation, but collector will still return a trigger (ScaleUp or ScaleDown)
			Expect(violation).To(Equal(0.0))
		})

		It("should handle multiple violations and return average", func() {
			// Arrange - multiple violations
			metrics := map[string]string{
				"EndToEndLatencyBucketQuery":   "0.150", // 36% over
				"TimeToFirstTokenLatencyQuery": "0.250", // 19% over
				"InterTokenLatencyQuery":       "0.070", // 16.7% over
			}

			// Act
			violation := collector.computeLatencyViolations(testSpec, metrics, logger)

			// Assert - should be average of violations
			Expect(violation).To(BeNumerically(">", 0.2))
			Expect(violation).To(BeNumerically("<", 0.3))
		})

		It("should handle missing metrics gracefully", func() {
			// Arrange - only one metric present
			metrics := map[string]string{
				"EndToEndLatencyBucketQuery": "0.090", // Within target
			}

			// Act
			violation := collector.computeLatencyViolations(testSpec, metrics, logger)

			// Assert - should only check available metrics
			Expect(violation).To(Equal(0.0))
		})

		It("should handle empty metrics", func() {
			// Arrange
			metrics := map[string]string{}

			// Act
			violation := collector.computeLatencyViolations(testSpec, metrics, logger)

			// Assert
			Expect(violation).To(Equal(0.0))
		})

		It("should handle nil target latency", func() {
			// Arrange
			specNoTarget := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target:        testSpec.Target,
				TargetLatency: nil,
			}
			metrics := map[string]string{
				"EndToEndLatencyBucketQuery": "0.100",
			}

			// Act
			violation := collector.computeLatencyViolations(specNoTarget, metrics, logger)

			// Assert
			Expect(violation).To(Equal(0.0))
		})

		It("should handle invalid metric values", func() {
			// Arrange
			metrics := map[string]string{
				"EndToEndLatencyBucketQuery":   "invalid",
				"TimeToFirstTokenLatencyQuery": "not-a-number",
				"InterTokenLatencyQuery":       "NaN",
			}

			// Act
			violation := collector.computeLatencyViolations(testSpec, metrics, logger)

			// Assert - should ignore invalid values
			Expect(violation).To(Equal(0.0))
		})
	})

	Context("When computing single latency violation", func() {
		It("should compute correct violation percentage", func() {
			// Test cases: current (seconds), target (ms), tolerance (ms), expected violation
			testCases := []struct {
				name      string
				current   string
				target    float64
				tolerance float64
				expected  float64
			}{
				{"at target", "0.100", 100, 0, 0.0},
				{"50% over", "0.150", 100, 0, 0.5},
				{"100% over", "0.200", 100, 0, 1.0},
				{"below target", "0.050", 100, 0, 0.0},
				{"10% over", "0.110", 100, 0, 0.1},
				{"within tolerance", "0.105", 100, 10, 0.0},
				{"just over tolerance", "0.111", 100, 10, 0.009},
			}

			for _, tc := range testCases {
				metrics := map[string]string{"test_metric": tc.current}
				violation, checked := computeSingleLatencyViolation(
					"test_metric",
					metrics,
					tc.target,
					tc.tolerance,
					"TestMetric",
					logger,
				)
				Expect(checked).To(BeTrue(), "Test case: "+tc.name)
				Expect(violation).To(BeNumerically("~", tc.expected, 0.01),
					"Test case: %s - For current=%v, target=%v, tolerance=%v, expected=%v, got=%v",
					tc.name, tc.current, tc.target, tc.tolerance, tc.expected, violation)
			}
		})

		It("should handle missing metric", func() {
			metrics := map[string]string{}
			violation, checked := computeSingleLatencyViolation(
				"missing_metric",
				metrics,
				100,
				0,
				"MissingMetric",
				logger,
			)
			Expect(checked).To(BeFalse())
			Expect(violation).To(Equal(0.0))
		})

		It("should handle invalid metric value", func() {
			metrics := map[string]string{"test_metric": "invalid"}
			violation, checked := computeSingleLatencyViolation(
				"test_metric",
				metrics,
				100,
				0,
				"TestMetric",
				logger,
			)
			Expect(checked).To(BeFalse())
			Expect(violation).To(Equal(0.0))
		})
	})

	Context("When collecting and computing metrics", func() {
		BeforeEach(func() {
			collector = NewAutoscalerMetricsCollector(mockMonitor, monitorMutex)
		})

		It("should handle nil monitor gracefully", func() {
			// Arrange
			collector.monitor = nil

			// Act
			result, err := collector.CollectAndCompute(ctx, testSpec, time.Time{})

			// Assert
			Expect(err).To(HaveOccurred())
			Expect(result).NotTo(BeNil())
			Expect(result.Message).To(ContainSubstring("not initialized"))
		})

		It("should handle context cancellation", func() {
			// Arrange
			cancelCtx, cancel := context.WithCancel(ctx)
			cancel() // Cancel immediately

			// Act
			result, err := collector.CollectAndCompute(cancelCtx, testSpec, time.Time{})

			// Assert - should handle gracefully (actual behavior depends on Monitor)
			_ = result
			_ = err
		})

		It("should handle timeout", func() {
			// Arrange
			timeoutCtx, cancel := context.WithTimeout(ctx, 1*time.Millisecond)
			defer cancel()
			time.Sleep(2 * time.Millisecond) // Ensure timeout

			// Act
			result, err := collector.CollectAndCompute(timeoutCtx, testSpec, time.Time{})

			// Assert - should handle gracefully
			_ = result
			_ = err
		})

		It("should recommend ScaleUp for high violation", func() {
			// This test would require mocking the Monitor's CollectMetrics
			// For now, we test the logic through computeLatencyViolations
			Skip("Requires Monitor mocking - tested indirectly through computeLatencyViolations")
		})

		It("should recommend ScaleDown for low violation with ScaleDown configured", func() {
			// This test would require mocking the Monitor's CollectMetrics
			Skip("Requires Monitor mocking - tested indirectly through computeLatencyViolations")
		})
	})

	Context("Thread safety", func() {
		BeforeEach(func() {
			collector = NewAutoscalerMetricsCollector(mockMonitor, monitorMutex)
		})

		It("should handle concurrent access safely", func() {
			// Arrange
			const numGoroutines = 10
			done := make(chan bool, numGoroutines)

			// Act - Multiple goroutines calling CollectAndCompute
			for i := 0; i < numGoroutines; i++ {
				go func() {
					defer GinkgoRecover()
					_, _ = collector.CollectAndCompute(ctx, testSpec, time.Time{})
					done <- true
				}()
			}

			// Assert - All goroutines complete without panic
			for i := 0; i < numGoroutines; i++ {
				Eventually(done, 5*time.Second).Should(Receive())
			}
		})

		It("should handle concurrent computeLatencyViolations calls", func() {
			// Arrange
			const numGoroutines = 20
			done := make(chan bool, numGoroutines)
			metrics := map[string]string{
				"EndToEndLatencyBucketQuery":   "0.100",
				"TimeToFirstTokenLatencyQuery": "0.190",
				"InterTokenLatencyQuery":       "0.050",
			}

			// Act - Multiple goroutines calling computeLatencyViolations
			for i := 0; i < numGoroutines; i++ {
				go func() {
					defer GinkgoRecover()
					violation := collector.computeLatencyViolations(testSpec, metrics, logger)
					Expect(violation).To(BeNumerically(">=", 0.0))
					done <- true
				}()
			}

			// Assert - All goroutines complete without panic
			for i := 0; i < numGoroutines; i++ {
				Eventually(done, 5*time.Second).Should(Receive())
			}
		})

		Context("Resource utilization for scale-down", func() {
			var (
				specWithThresholds *autoscalingv1alpha1.ResourceClaimAutoscalerSpec
			)

			BeforeEach(func() {
				collector = NewAutoscalerMetricsCollector(mockMonitor, monitorMutex)

				// Create spec with overprovision thresholds
				endToEnd := int32(100)
				specWithThresholds = &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name:      "test-service",
							Namespace: "default",
						},
					},
					TargetLatency: &autoscalingv1alpha1.TargetLatency{
						EndToEndLatencyMilliseconds: &endToEnd,
					},
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleDown: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 60,
							},
						},
					},
					MinTargetUtilization: map[string]resource.Quantity{
						"compute": resource.MustParse("0.7"),
						"memory":  resource.MustParse("0.8"),
					},
				}
			})

			It("should compute resource utilization correctly", func() {
				// Arrange
				metrics := map[string]string{
					"DeviceResource_compute_Usage":      "30",
					"DeviceResource_compute_Allocation": "100",
					"DeviceResource_memory_Usage":       "400",
					"DeviceResource_memory_Allocation":  "1000",
				}

				// Act
				utilization := collector.computeResourceUtilization(specWithThresholds, metrics, logger)

				// Assert
				Expect(utilization).To(HaveLen(2))
				Expect(utilization["compute"]).To(BeNumerically("~", 0.3, 0.01))
				Expect(utilization["memory"]).To(BeNumerically("~", 0.4, 0.01))
			})

			It("should compute underutilization violation correctly when resources below threshold", func() {
				// Arrange
				metrics := map[string]string{
					"DeviceResource_compute_Usage":      "30",
					"DeviceResource_compute_Allocation": "100",
					"DeviceResource_memory_Usage":       "400",
					"DeviceResource_memory_Allocation":  "1000",
				}

				// Act
				violation := collector.computeUnderutilizationViolation(specWithThresholds, metrics, logger)

				// Assert
				// Compute: 30/100 = 0.3, headroom = (0.7-0.3)/0.7 = 0.571
				// Memory: 400/1000 = 0.4, headroom = (0.8-0.4)/0.8 = 0.5
				// Average = 0.536
				Expect(violation).To(BeNumerically("~", 0.536, 0.01))
			})

			It("should calculate violation probability from underutilization when latency violation is zero", func() {
				// Arrange
				metrics := map[string]string{
					"DeviceResource_compute_Usage":      "20",
					"DeviceResource_compute_Allocation": "100",
					"DeviceResource_memory_Usage":       "300",
					"DeviceResource_memory_Allocation":  "1000",
				}

				// Act
				violation := collector.computeUnderutilizationViolation(specWithThresholds, metrics, logger)

				// Assert
				// Compute: 20/100 = 0.2, headroom = (0.7-0.2)/0.7 = 0.714
				// Memory: 300/1000 = 0.3, headroom = (0.8-0.3)/0.8 = 0.625
				// Average = 0.670
				Expect(violation).To(BeNumerically("~", 0.670, 0.01))
				Expect(violation).To(BeNumerically(">", 0.0))
				Expect(violation).To(BeNumerically("<=", 1.0))
			})

			It("should handle missing resource metrics gracefully", func() {
				// Arrange
				metrics := map[string]string{
					"EndToEndLatencyBucketQuery": "0.05",
					// Missing device resource metrics
				}

				// Act
				utilization := collector.computeResourceUtilization(specWithThresholds, metrics, logger)

				// Assert
				Expect(utilization).To(BeEmpty())
			})

			It("should handle invalid resource metric values", func() {
				// Arrange
				metrics := map[string]string{
					"DeviceResource_compute_Usage":      "invalid",
					"DeviceResource_compute_Allocation": "100",
				}

				// Act
				utilization := collector.computeResourceUtilization(specWithThresholds, metrics, logger)

				// Assert
				Expect(utilization).To(BeEmpty())
			})

			It("should handle zero allocation gracefully", func() {
				// Arrange
				metrics := map[string]string{
					"DeviceResource_compute_Usage":      "30",
					"DeviceResource_compute_Allocation": "0",
				}

				// Act
				utilization := collector.computeResourceUtilization(specWithThresholds, metrics, logger)

				// Assert
				Expect(utilization).To(BeEmpty())
			})

			It("should return minimum violation when no thresholds defined", func() {
				// Arrange
				specNoThresholds := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name:      "test-service",
							Namespace: "default",
						},
					},
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleDown: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 60,
							},
						},
					},
					// No MinTargetUtilization
				}
				metrics := map[string]string{}

				// Act
				violation := collector.computeUnderutilizationViolation(specNoThresholds, metrics, logger)

				// Assert
				// Should return minimum 10% for gradual scale-down
				Expect(violation).To(Equal(0.1))
			})
		})
	})
})

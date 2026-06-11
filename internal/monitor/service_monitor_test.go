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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// Helper function to create a test spec
func createTestSpec() *autoscalingv1alpha1.ResourceClaimAutoscalerSpec {
	endToEnd := int32(100)
	return &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
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
			EndToEndLatencyMilliseconds: &endToEnd,
		},
	}
}

// mockMetricsCollector is a mock implementation of MetricsCollector for testing
type mockMetricsCollector struct {
	collectCalled   chan bool
	lastSpec        *autoscalingv1alpha1.ResourceClaimAutoscalerSpec
	lastTriggerTime time.Time
	returnError     bool
}

func (m *mockMetricsCollector) CollectAndCompute(ctx context.Context, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, lastTriggerTime time.Time) (*MetricsResult, error) {
	m.lastSpec = spec
	m.lastTriggerTime = lastTriggerTime
	select {
	case m.collectCalled <- true:
	default:
	}

	if m.returnError {
		return &MetricsResult{
			Message: "mock error",
		}, errors.New("mock error")
	}
	return &MetricsResult{
		Message: "mock result",
	}, nil
}

var _ = Describe("ServiceMonitor", func() {
	var (
		sm     ServiceMonitor
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		sm = NewServiceMonitor()
		ctx, cancel = context.WithCancel(context.Background())
		err := sm.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		sm.Stop()
		cancel()
	})

	Context("When creating a new ServiceMonitor", func() {
		It("should initialize successfully", func() {
			Expect(sm).NotTo(BeNil())
		})

		It("should start successfully", func() {
			newSm := NewServiceMonitor()
			err := newSm.Start(ctx)
			Expect(err).NotTo(HaveOccurred())
			newSm.Stop()
		})
	})

	Context("When registering with collector", func() {
		var (
			name      string
			namespace string
			behavior  *autoscalingv1alpha1.Behavior
			collector *mockMetricsCollector
		)

		BeforeEach(func() {
			name = "test-autoscaler"
			namespace = "default"
			trigger := autoscalingv1alpha1.ScalingTrigger{
				PeriodSeconds: 1,
			}
			behavior = &autoscalingv1alpha1.Behavior{
				ScaleUp: &autoscalingv1alpha1.ScalingBehavior{
					Trigger:            trigger,
					GracePeriodSeconds: 30,
				},
				ScaleDown: &autoscalingv1alpha1.ScalingBehavior{
					Trigger:            trigger,
					GracePeriodSeconds: 60,
				},
			}
			collector = &mockMetricsCollector{
				collectCalled: make(chan bool, 10),
			}
		})

		It("should register successfully with collector", func() {
			// Arrange - register first
			err := sm.RegisterWithCollector(ctx, name, namespace, createTestSpec(), behavior, collector)
			Expect(err).NotTo(HaveOccurred())

			// Provide a spec so collector can be called
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					ServiceRef: autoscalingv1alpha1.ServiceReference{
						Name: "test-service",
					},
				},
			}
			err = sm.UpdateCachedSpec(ctx, name, namespace, spec)
			Expect(err).NotTo(HaveOccurred())

			// Wait for collector to be called
			select {
			case <-collector.collectCalled:
				// Success - collector was called
			case <-time.After(3 * time.Second):
				Fail("Timeout waiting for collector to be called")
			}
		})

		It("should return false for unregistered autoscaler", func() {
			// Act
			isRegistered := sm.IsRegistered(name, namespace)

			// Assert
			Expect(isRegistered).To(BeFalse())
		})

		It("should return true for registered autoscaler", func() {
			// Arrange - register first
			err := sm.RegisterWithCollector(ctx, name, namespace, createTestSpec(), behavior, collector)
			Expect(err).NotTo(HaveOccurred())

			// Act
			isRegistered := sm.IsRegistered(name, namespace)

			// Assert
			Expect(isRegistered).To(BeTrue())
		})

		It("should return false after unregistering", func() {
			// Arrange - register first
			err := sm.RegisterWithCollector(ctx, name, namespace, createTestSpec(), behavior, collector)
			Expect(err).NotTo(HaveOccurred())
			Expect(sm.IsRegistered(name, namespace)).To(BeTrue())

			// Act - unregister
			err = sm.Unregister(ctx, name, namespace)
			Expect(err).NotTo(HaveOccurred())

			// Assert
			isRegistered := sm.IsRegistered(name, namespace)
			Expect(isRegistered).To(BeFalse())
		})

		It("should call collector with correct spec", func() {
			// Arrange - set up a spec
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					ServiceRef: autoscalingv1alpha1.ServiceReference{
						Name: "test-service",
					},
				},
			}
			err := sm.UpdateCachedSpec(ctx, name, namespace, spec)
			Expect(err).To(HaveOccurred()) // Should fail because not registered yet

			// Act
			err = sm.RegisterWithCollector(ctx, name, namespace, createTestSpec(), behavior, collector)
			Expect(err).NotTo(HaveOccurred())

			// Update spec after registration
			err = sm.UpdateCachedSpec(ctx, name, namespace, spec)
			Expect(err).NotTo(HaveOccurred())

			// Wait for collector to be called
			select {
			case <-collector.collectCalled:
				Expect(collector.lastSpec).NotTo(BeNil())
				Expect(collector.lastSpec.Target.ServiceRef.Name).To(Equal("test-service"))
			case <-time.After(3 * time.Second):
				Fail("Timeout waiting for collector to be called")
			}
		})

		It("should handle collector errors gracefully", func() {
			// Arrange - collector that returns error
			errorCollector := &mockMetricsCollector{
				collectCalled: make(chan bool, 10),
				returnError:   true,
			}

			// Act
			err := sm.RegisterWithCollector(ctx, name, namespace, createTestSpec(), behavior, errorCollector)
			Expect(err).NotTo(HaveOccurred())

			// Provide a spec
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					ServiceRef: autoscalingv1alpha1.ServiceReference{
						Name: "test-service",
					},
				},
			}
			err = sm.UpdateCachedSpec(ctx, name, namespace, spec)
			Expect(err).NotTo(HaveOccurred())

			// Collector should still be called even if it errors
			select {
			case <-errorCollector.collectCalled:
				// Success - collector was called
			case <-time.After(3 * time.Second):
				Fail("Timeout waiting for collector to be called")
			}
		})

		It("should stop calling collector after unregister", func() {
			// Arrange
			err := sm.RegisterWithCollector(ctx, name, namespace, createTestSpec(), behavior, collector)
			Expect(err).NotTo(HaveOccurred())

			// Provide a spec
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				Target: autoscalingv1alpha1.TargetSelector{
					ServiceRef: autoscalingv1alpha1.ServiceReference{
						Name: "test-service",
					},
				},
			}
			err = sm.UpdateCachedSpec(ctx, name, namespace, spec)
			Expect(err).NotTo(HaveOccurred())

			// Wait for first call
			select {
			case <-collector.collectCalled:
				// Success
			case <-time.After(3 * time.Second):
				Fail("Timeout waiting for first collector call")
			}

			// Act - unregister
			err = sm.Unregister(ctx, name, namespace)
			Expect(err).NotTo(HaveOccurred())

			// Assert - no more calls should happen
			select {
			case <-collector.collectCalled:
				Fail("Collector should not be called after unregister")
			case <-time.After(2 * time.Second):
				// Success - no more calls
			}
		})
	})

})

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

package scaler_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/scaler"
)

var _ = Describe("Defaults", func() {
	Describe("GetDefaultScalingBehavior", func() {
		It("should return non-nil behavior with defaults", func() {
			behavior := scaler.GetDefaultScalingBehavior()

			Expect(behavior).NotTo(BeNil())
			Expect(behavior.Trigger.PeriodSeconds).To(Equal(int32(scaler.DefaultPeriodSeconds)))
			Expect(behavior.GracePeriodSeconds).To(Equal(int32(scaler.DefaultGracePeriodSeconds)))
		})
	})

	Describe("GetEffectiveBehavior", func() {
		Context("when no behavior is configured", func() {
			It("should return defaults for both ScaleUp and ScaleDown", func() {
				spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Behavior: nil,
				}

				behavior := scaler.GetEffectiveBehavior(spec)

				Expect(behavior).NotTo(BeNil())
				Expect(behavior.ScaleUp).NotTo(BeNil())
				Expect(behavior.ScaleDown).NotTo(BeNil())
				Expect(behavior.ScaleUp.Trigger.PeriodSeconds).To(Equal(int32(scaler.DefaultPeriodSeconds)))
				Expect(behavior.ScaleDown.Trigger.PeriodSeconds).To(Equal(int32(scaler.DefaultPeriodSeconds)))
				Expect(behavior.ScaleUp.GracePeriodSeconds).To(Equal(int32(scaler.DefaultGracePeriodSeconds)))
				Expect(behavior.ScaleDown.GracePeriodSeconds).To(Equal(int32(scaler.DefaultGracePeriodSeconds)))
			})
		})

		Context("when only ScaleUp is configured", func() {
			It("should use configured ScaleUp and default ScaleDown", func() {
				spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleUp: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 30,
							},
							GracePeriodSeconds: 15,
						},
						ScaleDown: nil,
					},
				}

				behavior := scaler.GetEffectiveBehavior(spec)

				Expect(behavior).NotTo(BeNil())
				Expect(behavior.ScaleUp.Trigger.PeriodSeconds).To(Equal(int32(30)))
				Expect(behavior.ScaleUp.GracePeriodSeconds).To(Equal(int32(15)))
				Expect(behavior.ScaleDown).NotTo(BeNil())
				Expect(behavior.ScaleDown.Trigger.PeriodSeconds).To(Equal(int32(scaler.DefaultPeriodSeconds)))
				Expect(behavior.ScaleDown.GracePeriodSeconds).To(Equal(int32(scaler.DefaultGracePeriodSeconds)))
			})
		})

		Context("when only ScaleDown is configured", func() {
			It("should use default ScaleUp and configured ScaleDown", func() {
				spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleUp: nil,
						ScaleDown: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 45,
							},
							GracePeriodSeconds: 60,
						},
					},
				}

				behavior := scaler.GetEffectiveBehavior(spec)

				Expect(behavior).NotTo(BeNil())
				Expect(behavior.ScaleUp).NotTo(BeNil())
				Expect(behavior.ScaleUp.Trigger.PeriodSeconds).To(Equal(int32(scaler.DefaultPeriodSeconds)))
				Expect(behavior.ScaleUp.GracePeriodSeconds).To(Equal(int32(scaler.DefaultGracePeriodSeconds)))
				Expect(behavior.ScaleDown.Trigger.PeriodSeconds).To(Equal(int32(45)))
				Expect(behavior.ScaleDown.GracePeriodSeconds).To(Equal(int32(60)))
			})
		})

		Context("when both ScaleUp and ScaleDown are configured", func() {
			It("should use configured values for both", func() {
				spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleUp: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 20,
							},
							GracePeriodSeconds: 10,
						},
						ScaleDown: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 90,
							},
							GracePeriodSeconds: 120,
						},
					},
				}

				behavior := scaler.GetEffectiveBehavior(spec)

				Expect(behavior).NotTo(BeNil())
				Expect(behavior.ScaleUp.Trigger.PeriodSeconds).To(Equal(int32(20)))
				Expect(behavior.ScaleUp.GracePeriodSeconds).To(Equal(int32(10)))
				Expect(behavior.ScaleDown.Trigger.PeriodSeconds).To(Equal(int32(90)))
				Expect(behavior.ScaleDown.GracePeriodSeconds).To(Equal(int32(120)))
			})
		})
	})

	DescribeTable("GetEffectiveTargetLatency",
		func(inputLatency *autoscalingv1alpha1.TargetLatency, expectedE2E, expectedTTFT, expectedITL, expectedTolerance int32) {
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				TargetLatency: inputLatency,
			}
			targetLatency := scaler.GetEffectiveTargetLatency(spec)
			Expect(targetLatency).NotTo(BeNil())
			Expect(*targetLatency.EndToEndLatencyMilliseconds).To(Equal(expectedE2E))
			Expect(*targetLatency.TimeToFirstTokenLatencyMilliseconds).To(Equal(expectedTTFT))
			Expect(*targetLatency.InterTokenLatencyMilliseconds).To(Equal(expectedITL))
			Expect(*targetLatency.ToleranceMilliseconds).To(Equal(expectedTolerance))
		},
		Entry("no target latency specified - should return all defaults",
			nil,
			int32(scaler.DefaultEndToEndLatencyMS),
			int32(scaler.DefaultTimeToFirstTokenLatencyMS),
			int32(scaler.DefaultInterTokenLatencyMS),
			int32(scaler.DefaultToleranceMS)),
		Entry("only E2E latency specified - should apply defaults to missing fields",
			&autoscalingv1alpha1.TargetLatency{EndToEndLatencyMilliseconds: ptr.To(int32(1500))},
			int32(1500),
			int32(scaler.DefaultTimeToFirstTokenLatencyMS),
			int32(scaler.DefaultInterTokenLatencyMS),
			int32(scaler.DefaultToleranceMS)),
		Entry("only TTFT latency specified - should apply defaults to missing fields",
			&autoscalingv1alpha1.TargetLatency{TimeToFirstTokenLatencyMilliseconds: ptr.To(int32(800))},
			int32(scaler.DefaultEndToEndLatencyMS),
			int32(800),
			int32(scaler.DefaultInterTokenLatencyMS),
			int32(scaler.DefaultToleranceMS)),
		Entry("only ITL latency specified - should apply defaults to missing fields",
			&autoscalingv1alpha1.TargetLatency{InterTokenLatencyMilliseconds: ptr.To(int32(30))},
			int32(scaler.DefaultEndToEndLatencyMS),
			int32(scaler.DefaultTimeToFirstTokenLatencyMS),
			int32(30),
			int32(scaler.DefaultToleranceMS)),
		Entry("only tolerance specified - should apply defaults to missing fields",
			&autoscalingv1alpha1.TargetLatency{ToleranceMilliseconds: ptr.To(int32(200))},
			int32(scaler.DefaultEndToEndLatencyMS),
			int32(scaler.DefaultTimeToFirstTokenLatencyMS),
			int32(scaler.DefaultInterTokenLatencyMS),
			int32(200)),
		Entry("all fields specified - should use all specified values",
			&autoscalingv1alpha1.TargetLatency{
				EndToEndLatencyMilliseconds:         ptr.To(int32(3000)),
				TimeToFirstTokenLatencyMilliseconds: ptr.To(int32(600)),
				InterTokenLatencyMilliseconds:       ptr.To(int32(25)),
				ToleranceMilliseconds:               ptr.To(int32(300)),
			},
			int32(3000), int32(600), int32(25), int32(300)),
	)
})

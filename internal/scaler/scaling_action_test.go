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
	"context"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/optimizer"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/scaler"
)

var _ = Describe("ScalingAction", func() {
	var (
		s   *scaler.ResourceScaler
		ctx context.Context
	)

	BeforeEach(func() {
		s = scaler.NewResourceScaler(nil, logr.Discard())
		ctx = context.Background()
	})

	DescribeTable("determineScalingAction",
		func(currentReplicas int32, desiredConfig optimizer.ResourceConfiguration,
			expectedAction scaler.ScalingAction, expectedReplicaChange, expectedResourceChange bool) {
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-autoscaler",
					Namespace: "default",
				},
				Status: autoscalingv1alpha1.ResourceClaimAutoscalerStatus{
					ScalingStatus: &autoscalingv1alpha1.ScalingStatus{
						DesiredReplicas: &currentReplicas,
					},
				},
			}
			config := &optimizer.OptimalConfiguration{
				ResourceConfiguration: desiredConfig,
			}
			decision := scaler.DetermineScalingAction(s, ctx, autoscaler, config, 10.0)
			Expect(decision.Action).To(Equal(expectedAction))
			Expect(decision.ReplicaChange).To(Equal(expectedReplicaChange))
			Expect(decision.ResourceChange).To(Equal(expectedResourceChange))
		},
		Entry("no change needed - same configuration",
			int32(2), optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			scaler.ScalingActionNone, false, false),
		Entry("scale up replicas",
			int32(2), optimizer.ResourceConfiguration{Replicas: 4, GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			scaler.ScalingActionScaleUp, true, false),
		Entry("scale down replicas",
			int32(4), optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			scaler.ScalingActionScaleDown, true, false),
		Entry("scale up resources per replica",
			int32(2), optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 2, RequestedCompute: 200.0, RequestedMemoryGB: 160.0},
			scaler.ScalingActionScaleUp, false, true),
		Entry("scale down resources per replica",
			int32(2), optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1, RequestedCompute: 50.0, RequestedMemoryGB: 40.0},
			scaler.ScalingActionScaleDown, false, true),
		Entry("scale up memory only",
			int32(2), optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 120.0},
			scaler.ScalingActionScaleUp, false, true),
		Entry("scale down memory only",
			int32(2), optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 60.0},
			scaler.ScalingActionScaleDown, false, true),
		Entry("scale up both replicas and resources",
			int32(2), optimizer.ResourceConfiguration{Replicas: 4, GPUsPerReplica: 2, RequestedCompute: 150.0, RequestedMemoryGB: 120.0},
			scaler.ScalingActionScaleUp, true, true),
	)

	DescribeTable("buildResourceChangeReason",
		func(current, desired optimizer.ResourceConfiguration, action string, expectedSubstrings ...string) {
			decision := &scaler.ScalingDecision{
				CurrentResourceConfiguration: current,
				DesiredResourceConfiguration: desired,
			}
			reason := scaler.BuildResourceChangeReason(s, decision, action)
			for _, substr := range expectedSubstrings {
				Expect(reason).To(ContainSubstring(substr))
			}
		},
		Entry("should describe GPU changes",
			optimizer.ResourceConfiguration{GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			optimizer.ResourceConfiguration{GPUsPerReplica: 2, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			"increase", "GPUs per replica", "1", "2"),
		Entry("should describe compute changes",
			optimizer.ResourceConfiguration{GPUsPerReplica: 1, RequestedCompute: 50.0, RequestedMemoryGB: 80.0},
			optimizer.ResourceConfiguration{GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			"increase", "compute", "50.0", "100.0"),
		Entry("should describe memory changes",
			optimizer.ResourceConfiguration{GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 40.0},
			optimizer.ResourceConfiguration{GPUsPerReplica: 1, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			"increase", "memory", "40.00 GB", "80.00 GB"),
		Entry("should describe multiple changes",
			optimizer.ResourceConfiguration{GPUsPerReplica: 1, RequestedCompute: 50.0, RequestedMemoryGB: 40.0},
			optimizer.ResourceConfiguration{GPUsPerReplica: 2, RequestedCompute: 100.0, RequestedMemoryGB: 80.0},
			"increase", "Resource increase"),
	)

	DescribeTable("applySingleTimeScalingLimit",
		func(
			scaleUpLimit, scaleDownLimit *int32,
			action scaler.ScalingAction,
			currentConfig, desiredConfig optimizer.ResourceConfiguration,
			expectedReplicas, expectedGPUsPerReplica int,
			reasonShouldContain string,
		) {
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-autoscaler",
					Namespace: "default",
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{},
			}
			if scaleUpLimit != nil || scaleDownLimit != nil {
				autoscaler.Spec.Behavior = &autoscalingv1alpha1.Behavior{}
				if scaleUpLimit != nil {
					autoscaler.Spec.Behavior.ScaleUp = &autoscalingv1alpha1.ScalingBehavior{
						SingleTimeScalingLimit: scaleUpLimit,
					}
				}
				if scaleDownLimit != nil {
					autoscaler.Spec.Behavior.ScaleDown = &autoscalingv1alpha1.ScalingBehavior{
						SingleTimeScalingLimit: scaleDownLimit,
					}
				}
			}
			decision := &scaler.ScalingDecision{
				Action:                       action,
				CurrentResourceConfiguration: currentConfig,
				DesiredResourceConfiguration: desiredConfig,
				Reason:                       "Original reason",
			}
			scaler.ApplySingleTimeScalingLimit(s, autoscaler, decision)
			Expect(decision.DesiredResourceConfiguration.Replicas).To(Equal(expectedReplicas))
			Expect(decision.DesiredResourceConfiguration.GPUsPerReplica).To(Equal(expectedGPUsPerReplica))
			if reasonShouldContain != "" {
				Expect(decision.Reason).To(ContainSubstring(reasonShouldContain))
			} else {
				Expect(decision.Reason).To(Equal("Original reason"))
			}
		},
		Entry("no limit configured - should not modify",
			nil, nil, scaler.ScalingActionScaleUp,
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 10, GPUsPerReplica: 1},
			10, 1, ""),
		Entry("scale up limit - should cap within limit",
			ptr.To(int32(4)), nil, scaler.ScalingActionScaleUp,
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 10, GPUsPerReplica: 1},
			6, 1, "limited by singleTimeScalingLimit"),
		Entry("scale up limit - should not modify if within limit",
			ptr.To(int32(10)), nil, scaler.ScalingActionScaleUp,
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 5, GPUsPerReplica: 1},
			5, 1, ""),
		Entry("scale down limit - should cap within limit",
			nil, ptr.To(int32(3)), scaler.ScalingActionScaleDown,
			optimizer.ResourceConfiguration{Replicas: 10, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1},
			7, 1, "limited by singleTimeScalingLimit"),
		Entry("scale down limit - should enforce minimum of 1 GPU",
			nil, ptr.To(int32(2)), scaler.ScalingActionScaleDown,
			optimizer.ResourceConfiguration{Replicas: 3, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 0, GPUsPerReplica: 1},
			1, 1, "limited by singleTimeScalingLimit"),
		Entry("multiple GPUs per replica - should handle scale up",
			ptr.To(int32(4)), nil, scaler.ScalingActionScaleUp,
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 2},
			optimizer.ResourceConfiguration{Replicas: 5, GPUsPerReplica: 2},
			4, 2, "limited by singleTimeScalingLimit"),
		Entry("multiple GPUs per replica - should round up replicas with remainder",
			ptr.To(int32(5)), nil, scaler.ScalingActionScaleUp,
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 10, GPUsPerReplica: 2},
			4, 2, "limited by singleTimeScalingLimit"),
		Entry("multiple GPUs per replica - should adjust GPUs when limited total less than desired",
			ptr.To(int32(2)), nil, scaler.ScalingActionScaleUp,
			optimizer.ResourceConfiguration{Replicas: 1, GPUsPerReplica: 1},
			optimizer.ResourceConfiguration{Replicas: 2, GPUsPerReplica: 4},
			1, 3, "limited by singleTimeScalingLimit"),
	)

	DescribeTable("checkMinTargetUtilization",
		func(
			minTargetUtil map[string]resource.Quantity,
			action scaler.ScalingAction,
			config optimizer.ResourceConfiguration,
			arrivalRate float64,
			expectedBlock bool,
			expectedReasonSubstrings ...string,
		) {
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-autoscaler",
					Namespace: "default",
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					MinTargetUtilization: minTargetUtil,
				},
			}
			decision := &scaler.ScalingDecision{
				Action:                       action,
				DesiredResourceConfiguration: config,
			}
			shouldBlock, reason := scaler.CheckMinTargetUtilization(s, autoscaler, decision, arrivalRate)
			Expect(shouldBlock).To(Equal(expectedBlock))
			if expectedBlock {
				for _, substr := range expectedReasonSubstrings {
					Expect(reason).To(ContainSubstring(substr))
				}
			} else {
				Expect(reason).To(BeEmpty())
			}
		},
		Entry("should allow scale-down when arrival rate is zero",
			nil, scaler.ScalingActionScaleDown, optimizer.ResourceConfiguration{}, 0.0, false),
		Entry("should not block scale-up",
			nil, scaler.ScalingActionScaleUp, optimizer.ResourceConfiguration{}, 10.0, false),
		Entry("should not block scale-down when no MinTargetUtilization configured",
			nil, scaler.ScalingActionScaleDown, optimizer.ResourceConfiguration{}, 10.0, false),
		Entry("should block scale-down when compute utilization above minimum target",
			map[string]resource.Quantity{"compute": resource.MustParse("0.7")},
			scaler.ScalingActionScaleDown,
			optimizer.ResourceConfiguration{RequestedCompute: 0.8},
			10.0, true, "Compute utilization", "above minimum target"),
		Entry("should allow scale-down when compute utilization below minimum target",
			map[string]resource.Quantity{"compute": resource.MustParse("0.7")},
			scaler.ScalingActionScaleDown,
			optimizer.ResourceConfiguration{RequestedCompute: 0.5},
			10.0, false),
		Entry("should block scale-down when memory utilization above minimum target",
			map[string]resource.Quantity{"memory": resource.MustParse("0.8")},
			scaler.ScalingActionScaleDown,
			optimizer.ResourceConfiguration{RequestedMemoryGB: 80.0},
			10.0, true, "Memory utilization", "above minimum target"),
		Entry("should block scale-down when GPU utilization above minimum target",
			map[string]resource.Quantity{"nvidia.com/gpu": resource.MustParse("0.75")},
			scaler.ScalingActionScaleDown,
			optimizer.ResourceConfiguration{RequestedCompute: 0.85},
			10.0, true, "nvidia.com/gpu utilization", "above minimum target"),
	)

	DescribeTable("calculateResourceUtilization",
		func(resourceType string, config optimizer.ResourceConfiguration, expected float64) {
			decision := &scaler.ScalingDecision{
				DesiredResourceConfiguration: config,
			}
			utilization := scaler.CalculateResourceUtilization(s, resourceType, decision)
			Expect(utilization).To(Equal(expected))
		},
		Entry("should calculate compute utilization",
			"compute", optimizer.ResourceConfiguration{RequestedCompute: 0.75}, 0.75),
		Entry("should return 1.0 for memory utilization",
			"memory", optimizer.ResourceConfiguration{RequestedMemoryGB: 80.0}, 1.0),
		Entry("should use compute utilization for GPU resources",
			"nvidia.com/gpu", optimizer.ResourceConfiguration{RequestedCompute: 0.85}, 0.85),
	)
})

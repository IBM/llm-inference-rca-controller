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
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/optimizer"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/scaler"
)

func TestScaler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Scaler Suite")
}

var _ = Describe("ResourceScaler", func() {
	var (
		s      *scaler.ResourceScaler
		logger logr.Logger
	)

	BeforeEach(func() {
		logger = logr.Discard()
		s = scaler.NewResourceScaler(nil, logger)
	})

	// Note: createLatencyEstimator method has been removed in the new implementation.
	// Latency estimation is now handled by ResourceOptimizer using hint-based initialization.
	// Tests for metric parsing and arrival rate extraction are kept below.

	Describe("parseFloat", func() {
		It("should parse valid float strings", func() {
			tests := []struct {
				input    string
				expected float64
			}{
				{"1.5", 1.5},
				{"0.015", 0.015},
				{"100", 100.0},
				{"0", 0.0},
				{"-5.5", -5.5},
			}

			for _, tt := range tests {
				value, err := scaler.ParseFloat(s, tt.input)
				Expect(err).NotTo(HaveOccurred())
				Expect(value).To(Equal(tt.expected))
			}
		})

		It("should return error for invalid strings", func() {
			invalidInputs := []string{
				"not-a-number",
				"",
				"1.2.3",
				"abc",
			}

			for _, input := range invalidInputs {
				_, err := scaler.ParseFloat(s, input)
				Expect(err).To(HaveOccurred())
			}
		})
	})

	Describe("getArrivalRate", func() {
		It("should return zero when RPSQuery is explicitly zero", func() {
			metrics := map[string]string{
				"RPSQuery": "0.0",
			}
			rate := scaler.GetArrivalRate(s, metrics)
			Expect(rate).To(Equal(0.0))
		})

		It("should return value from RPSQuery when available", func() {
			metrics := map[string]string{
				"RPSQuery": "2.5",
			}
			rate := scaler.GetArrivalRate(s, metrics)
			Expect(rate).To(Equal(2.5))
		})

		It("should return value from RequestRate when available", func() {
			metrics := map[string]string{
				"RequestRate": "3.5",
			}
			rate := scaler.GetArrivalRate(s, metrics)
			Expect(rate).To(Equal(3.5))
		})

		It("should prefer RequestRate over RPSQuery", func() {
			metrics := map[string]string{
				"RequestRate": "3.5",
				"RPSQuery":    "2.5",
			}
			rate := scaler.GetArrivalRate(s, metrics)
			Expect(rate).To(Equal(3.5))
		})

		It("should default to 0.0 when no metrics available", func() {
			metrics := map[string]string{}
			rate := scaler.GetArrivalRate(s, metrics)
			Expect(rate).To(Equal(0.0))
		})
	})

	Describe("configuration helpers", func() {
		It("should get all target latencies when multiple are set", func() {
			targetLatency := &autoscalingv1alpha1.TargetLatency{
				EndToEndLatencyMilliseconds:         ptr.To(int32(2500)),
				TimeToFirstTokenLatencyMilliseconds: ptr.To(int32(1200)),
				InterTokenLatencyMilliseconds:       ptr.To(int32(50)),
			}

			targets, err := scaler.GetTargetLatency(s, targetLatency)
			Expect(err).NotTo(HaveOccurred())
			Expect(targets.E2E).NotTo(BeNil())
			Expect(*targets.E2E).To(Equal(2500.0))
			Expect(targets.TTFT).NotTo(BeNil())
			Expect(*targets.TTFT).To(Equal(1200.0))
			Expect(targets.ITL).NotTo(BeNil())
			Expect(*targets.ITL).To(Equal(50.0))
		})

		It("should get E2E target when only E2E is set", func() {
			targetLatency := &autoscalingv1alpha1.TargetLatency{
				EndToEndLatencyMilliseconds: ptr.To(int32(2500)),
			}

			targets, err := scaler.GetTargetLatency(s, targetLatency)
			Expect(err).NotTo(HaveOccurred())
			Expect(targets.E2E).NotTo(BeNil())
			Expect(*targets.E2E).To(Equal(2500.0))
			Expect(targets.TTFT).To(BeNil())
			Expect(targets.ITL).To(BeNil())
		})

		It("should get TTFT target when only TTFT is set", func() {
			targetLatency := &autoscalingv1alpha1.TargetLatency{
				TimeToFirstTokenLatencyMilliseconds: ptr.To(int32(900)),
			}

			targets, err := scaler.GetTargetLatency(s, targetLatency)
			Expect(err).NotTo(HaveOccurred())
			Expect(targets.E2E).To(BeNil())
			Expect(targets.TTFT).NotTo(BeNil())
			Expect(*targets.TTFT).To(Equal(900.0))
			Expect(targets.ITL).To(BeNil())
		})

		It("should get ITL target when only ITL is set", func() {
			targetLatency := &autoscalingv1alpha1.TargetLatency{
				InterTokenLatencyMilliseconds: ptr.To(int32(50)),
			}

			targets, err := scaler.GetTargetLatency(s, targetLatency)
			Expect(err).NotTo(HaveOccurred())
			Expect(targets.E2E).To(BeNil())
			Expect(targets.TTFT).To(BeNil())
			Expect(targets.ITL).NotTo(BeNil())
			Expect(*targets.ITL).To(Equal(50.0))
		})

		It("should return an error when no latency target is configured", func() {
			targetLatency := &autoscalingv1alpha1.TargetLatency{}

			_, err := scaler.GetTargetLatency(s, targetLatency)
			Expect(err).To(HaveOccurred())
		})

		It("should return default effective target latency when none is specified", func() {
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{}

			targetLatency := scaler.GetEffectiveTargetLatency(spec)
			Expect(*targetLatency.EndToEndLatencyMilliseconds).To(Equal(int32(scaler.DefaultEndToEndLatencyMS)))
			Expect(*targetLatency.TimeToFirstTokenLatencyMilliseconds).To(Equal(int32(scaler.DefaultTimeToFirstTokenLatencyMS)))
			Expect(*targetLatency.InterTokenLatencyMilliseconds).To(Equal(int32(scaler.DefaultInterTokenLatencyMS)))
			Expect(*targetLatency.ToleranceMilliseconds).To(Equal(int32(scaler.DefaultToleranceMS)))
		})

		It("should apply defaults only to missing target latency fields", func() {
			spec := &autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
				TargetLatency: &autoscalingv1alpha1.TargetLatency{
					EndToEndLatencyMilliseconds: ptr.To(int32(1500)),
				},
			}

			targetLatency := scaler.GetEffectiveTargetLatency(spec)
			Expect(*targetLatency.EndToEndLatencyMilliseconds).To(Equal(int32(1500)))
			Expect(*targetLatency.TimeToFirstTokenLatencyMilliseconds).To(Equal(int32(scaler.DefaultTimeToFirstTokenLatencyMS)))
			Expect(*targetLatency.InterTokenLatencyMilliseconds).To(Equal(int32(scaler.DefaultInterTokenLatencyMS)))
			Expect(*targetLatency.ToleranceMilliseconds).To(Equal(int32(scaler.DefaultToleranceMS)))
		})

		// Note: Helper methods getGPUConstraints, getMaxReplicas, and createContentionConfig
		// have been removed. These are now handled internally by ResourceOptimizer.

		It("should check if LatencyTargets has any target", func() {
			targets := &scaler.LatencyTargets{
				E2E: ptr.To(2500.0),
			}
			Expect(targets.HasAnyTarget()).To(BeTrue())

			emptyTargets := &scaler.LatencyTargets{}
			Expect(emptyTargets.HasAnyTarget()).To(BeFalse())
		})

		It("should get primary target with E2E priority", func() {
			targets := &scaler.LatencyTargets{
				E2E:  ptr.To(2500.0),
				TTFT: ptr.To(1000.0),
				ITL:  ptr.To(50.0),
			}
			Expect(targets.GetPrimaryTarget()).To(Equal(2500.0))
		})

		It("should get primary target with TTFT when E2E is nil", func() {
			targets := &scaler.LatencyTargets{
				TTFT: ptr.To(1000.0),
				ITL:  ptr.To(50.0),
			}
			Expect(targets.GetPrimaryTarget()).To(Equal(1000.0))
		})

		It("should get primary target with ITL when E2E and TTFT are nil", func() {
			targets := &scaler.LatencyTargets{
				ITL: ptr.To(50.0),
			}
			Expect(targets.GetPrimaryTarget()).To(Equal(50.0))
		})

		It("should return 0 when no targets are set", func() {
			targets := &scaler.LatencyTargets{}
			Expect(targets.GetPrimaryTarget()).To(Equal(0.0))
		})
	})

	Describe("buildResourceChangeReason", func() {
		It("should build reason for GPU changes only", func() {
			decision := &scaler.ScalingDecision{
				CurrentResourceConfiguration: optimizer.ResourceConfiguration{
					GPUsPerReplica:    1,
					RequestedCompute:  100.0,
					RequestedMemoryGB: 80.0,
				},
				DesiredResourceConfiguration: optimizer.ResourceConfiguration{
					GPUsPerReplica:    2,
					RequestedCompute:  100.0,
					RequestedMemoryGB: 80.0,
				},
			}

			reason := scaler.BuildResourceChangeReason(s, decision, "increase")
			Expect(reason).To(ContainSubstring("GPUs per replica"))
		})

		It("should build reason for compute changes only", func() {
			decision := &scaler.ScalingDecision{
				CurrentResourceConfiguration: optimizer.ResourceConfiguration{
					GPUsPerReplica:    1,
					RequestedCompute:  50.0,
					RequestedMemoryGB: 80.0,
				},
				DesiredResourceConfiguration: optimizer.ResourceConfiguration{
					GPUsPerReplica:    1,
					RequestedCompute:  100.0,
					RequestedMemoryGB: 80.0,
				},
			}

			reason := scaler.BuildResourceChangeReason(s, decision, "increase")
			Expect(reason).To(ContainSubstring("compute"))
		})

		It("should build reason for memory changes only", func() {
			decision := &scaler.ScalingDecision{
				CurrentResourceConfiguration: optimizer.ResourceConfiguration{
					GPUsPerReplica:    1,
					RequestedCompute:  100.0,
					RequestedMemoryGB: 40.0,
				},
				DesiredResourceConfiguration: optimizer.ResourceConfiguration{
					GPUsPerReplica:    1,
					RequestedCompute:  100.0,
					RequestedMemoryGB: 80.0,
				},
			}

			reason := scaler.BuildResourceChangeReason(s, decision, "increase")
			Expect(reason).To(ContainSubstring("memory"))
		})
	})

	Describe("applyScalingAction", func() {
		var (
			ctx        context.Context
			scheme     *runtime.Scheme
			autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler
		)

		BeforeEach(func() {
			ctx = context.Background()
			scheme = runtime.NewScheme()
			Expect(autoscalingv1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(appsv1.AddToScheme(scheme)).To(Succeed())
			Expect(resourcev1.AddToScheme(scheme)).To(Succeed())

			autoscaler = &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-autoscaler",
					Namespace: "default",
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleUp:   &autoscalingv1alpha1.ScalingBehavior{GracePeriodSeconds: 30},
						ScaleDown: &autoscalingv1alpha1.ScalingBehavior{GracePeriodSeconds: 30},
					},
				},
			}
		})

		It("should apply scaling action when configuration changes", func() {
			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
				DesiredReplicas: ptr.To(int32(1)),
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(autoscaler).
				WithObjects(autoscaler).
				Build(), logger)

			config := &optimizer.OptimalConfiguration{
				ResourceConfiguration: optimizer.ResourceConfiguration{
					Replicas:          2,
					GPUsPerReplica:    1,
					RequestedCompute:  100,
					RequestedMemoryGB: 80,
				},
			}

			err := scaler.ApplyScalingAction(s, ctx, "test-autoscaler", "default", config, 10.0, map[string]string{}, 0, 0)
			Expect(err).NotTo(HaveOccurred())

			updated := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "test-autoscaler", Namespace: "default"}, updated)).To(Succeed())
			cond := updated.Status.Conditions[len(updated.Status.Conditions)-1]
			Expect(cond.Reason).To(Equal("ScalingInProgress"))
			Expect(updated.Status.ScalingStatus.DesiredReplicas).To(Equal(ptr.To(int32(2))))
		})

		It("should skip scaling during grace period", func() {
			lastApply := metav1.NewTime(time.Now())
			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
				DesiredReplicas: ptr.To(int32(1)),
				LastApplyTime:   &lastApply,
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(autoscaler).
				WithObjects(autoscaler).
				Build(), logger)

			config := &optimizer.OptimalConfiguration{
				ResourceConfiguration: optimizer.ResourceConfiguration{
					Replicas:          2,
					GPUsPerReplica:    1,
					RequestedCompute:  100,
					RequestedMemoryGB: 80,
				},
			}

			err := scaler.ApplyScalingAction(s, ctx, "test-autoscaler", "default", config, 10.0, map[string]string{}, 0, 0)
			Expect(err).NotTo(HaveOccurred())

			updated := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "test-autoscaler", Namespace: "default"}, updated)).To(Succeed())
			Expect(updated.Status.Conditions).To(BeEmpty())
		})

		It("should apply replica-only scaling for a deployment owner", func() {
			lastApply := metav1.NewTime(time.Now().Add(-time.Minute))
			autoscaler.Status.Discovery = &autoscalingv1alpha1.DiscoveryResults{
				ResourceClaim: "base-template",
				Owner: &metav1.OwnerReference{
					Kind: "Deployment",
					Name: "workload",
				},
			}
			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
				DesiredReplicas: ptr.To(int32(1)),
				LastApplyTime:   &lastApply,
			}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas:     1,
					AvailableReplicas: 1,
				},
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(autoscaler).
				WithObjects(autoscaler, deployment).
				Build(), logger)

			config := &optimizer.OptimalConfiguration{
				ResourceConfiguration: optimizer.ResourceConfiguration{
					Replicas:          3,
					GPUsPerReplica:    1,
					RequestedCompute:  100,
					RequestedMemoryGB: 80,
				},
				EstimatedLatency: optimizer.LatencyResult{
					Prefill: 100.0,
					Decode:  200.0,
					TTFT:    120.0,
					ITL:     10.0,
					E2E:     320.0,
				},
			}

			err := scaler.ApplyScalingAction(s, ctx, "test-autoscaler", "default", config, 10.0, map[string]string{}, 0, 0)
			Expect(err).NotTo(HaveOccurred())

			updatedAutoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "test-autoscaler", Namespace: "default"}, updatedAutoscaler)).To(Succeed())
			Expect(*updatedAutoscaler.Status.ScalingStatus.DesiredReplicas).To(Equal(int32(3)))
			Expect(*updatedAutoscaler.Status.ScalingResults.LastEstimation.TimeToFirstTokenMilliseconds).To(Equal(int32(120)))
			Expect(*updatedAutoscaler.Status.ScalingResults.LastEstimation.InterTokenLatencyMilliseconds).To(Equal(int32(10)))

			updatedDeployment := &appsv1.Deployment{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "workload", Namespace: "default"}, updatedDeployment)).To(Succeed())
			Expect(*updatedDeployment.Spec.Replicas).To(Equal(int32(3)))
		})
	})

	Describe("resource claim template helpers", func() {
		var (
			ctx        context.Context
			scheme     *runtime.Scheme
			autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler
		)

		BeforeEach(func() {
			ctx = context.Background()
			scheme = runtime.NewScheme()
			Expect(autoscalingv1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(appsv1.AddToScheme(scheme)).To(Succeed())
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			Expect(resourcev1.AddToScheme(scheme)).To(Succeed())

			autoscaler = &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-autoscaler",
					Namespace: "default",
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: "gpu.nvidia.com",
						},
					},
				},
				Status: autoscalingv1alpha1.ResourceClaimAutoscalerStatus{
					Discovery: &autoscalingv1alpha1.DiscoveryResults{
						ResourceClaim: "base-template",
						Owner: &metav1.OwnerReference{
							Kind: "Deployment",
							Name: "workload",
						},
					},
				},
			}
		})

		It("should get current resources from an Exactly request", func() {
			template := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template",
					Namespace: "default",
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.nvidia.com",
										Count:           2,
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"compute": *resource.NewQuantity(80, resource.DecimalSI),
												"memory":  *resource.NewQuantity(32*1024*1024*1024, resource.BinarySI),
											},
										},
									},
								},
							},
						},
					},
				},
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(template).Build(), logger)

			gpus, resources, err := scaler.GetCurrentResourcesFromTemplate(s, ctx, autoscaler)
			Expect(err).NotTo(HaveOccurred())
			Expect(gpus).To(Equal(2))
			compute := resources["compute"]
			memory := resources["memory"]
			Expect(compute.AsApproximateFloat64()).To(Equal(80.0))
			Expect(memory.Value()).To(Equal(int64(32 * 1024 * 1024 * 1024)))
		})

		It("should get current resources from a FirstAvailable request", func() {
			template := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template",
					Namespace: "default",
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									FirstAvailable: []resourcev1.DeviceSubRequest{
										{
											Name:            "other",
											DeviceClassName: "other.class",
											Count:           1,
										},
										{
											Name:            "target",
											DeviceClassName: "gpu.nvidia.com",
											Count:           3,
											Capacity: &resourcev1.CapacityRequirements{
												Requests: map[resourcev1.QualifiedName]resource.Quantity{
													"compute": *resource.NewQuantity(65, resource.DecimalSI),
													"memory":  *resource.NewQuantity(24*1024*1024*1024, resource.BinarySI),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(template).Build(), logger)

			gpus, resources, err := scaler.GetCurrentResourcesFromTemplate(s, ctx, autoscaler)
			Expect(err).NotTo(HaveOccurred())
			Expect(gpus).To(Equal(3))
			compute := resources["compute"]
			memory := resources["memory"]
			Expect(compute.AsApproximateFloat64()).To(Equal(65.0))
			Expect(memory.Value()).To(Equal(int64(24 * 1024 * 1024 * 1024)))
		})

		It("should return defaults when discovery status is missing", func() {
			autoscaler.Status.Discovery = nil
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).Build(), logger)

			gpus, resources, err := scaler.GetCurrentResourcesFromTemplate(s, ctx, autoscaler)
			Expect(err).NotTo(HaveOccurred())
			Expect(gpus).To(Equal(1))
			Expect(resources).To(BeNil())
		})

		It("should detect matching template configuration", func() {
			template := &resourcev1.ResourceClaimTemplate{
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.nvidia.com",
										Count:           2,
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"compute": *resource.NewQuantity(70, resource.DecimalSI),
												"memory":  *resource.NewQuantity(16*1024*1024*1024, resource.BinarySI),
											},
										},
									},
								},
							},
						},
					},
				},
			}
			desired := optimizer.ResourceConfiguration{
				GPUsPerReplica:    2,
				RequestedCompute:  70,
				RequestedMemoryGB: 16,
			}

			Expect(scaler.TemplateMatchesDesiredConfig(s, template, desired, "gpu.nvidia.com")).To(BeTrue())
		})

		It("should detect mismatched template configuration", func() {
			template := &resourcev1.ResourceClaimTemplate{
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.nvidia.com",
										Count:           1,
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"compute": *resource.NewQuantity(50, resource.DecimalSI),
												"memory":  *resource.NewQuantity(8*1024*1024*1024, resource.BinarySI),
											},
										},
									},
								},
							},
						},
					},
				},
			}
			desired := optimizer.ResourceConfiguration{
				GPUsPerReplica:    2,
				RequestedCompute:  70,
				RequestedMemoryGB: 16,
			}

			Expect(scaler.TemplateMatchesDesiredConfig(s, template, desired, "gpu.nvidia.com")).To(BeFalse())
		})

		It("should update pod template resource claims", func() {
			podTemplate := &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "unchanged",
							ResourceClaimTemplateName: ptr.To("another-template"),
						},
						{
							Name:                      "target",
							ResourceClaimTemplateName: ptr.To("old-template"),
						},
					},
				},
			}

			updated := scaler.UpdatePodTemplateResourceClaims(s, podTemplate, "old-template", "new-template")
			Expect(updated).To(BeTrue())
			Expect(*podTemplate.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal("another-template"))
			Expect(*podTemplate.Spec.ResourceClaims[1].ResourceClaimTemplateName).To(Equal("new-template"))
		})

		It("should return false when no pod template resource claim matches", func() {
			podTemplate := &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "target",
							ResourceClaimTemplateName: ptr.To("different-template"),
						},
					},
				},
			}

			updated := scaler.UpdatePodTemplateResourceClaims(s, podTemplate, "old-template", "new-template")
			Expect(updated).To(BeFalse())
		})

		It("should get deployment owner revision", func() {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "workload",
					Namespace:  "default",
					Generation: 7,
				},
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build(), logger)

			revision, err := scaler.GetOwnerRevision(s, ctx, "default", &metav1.OwnerReference{
				Kind: "Deployment",
				Name: "workload",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(revision).To(Equal("7"))
		})

		It("should get statefulset owner revision", func() {
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "workload-sts",
					Namespace:  "default",
					Generation: 5,
				},
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(statefulSet).Build(), logger)

			revision, err := scaler.GetOwnerRevision(s, ctx, "default", &metav1.OwnerReference{
				Kind: "StatefulSet",
				Name: "workload-sts",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(revision).To(Equal("5"))
		})

		It("should reject unsupported owner kinds", func() {
			revision, err := scaler.GetOwnerRevision(s, ctx, "default", &metav1.OwnerReference{
				Kind: "Job",
				Name: "workload-job",
			})
			Expect(err).To(HaveOccurred())
			Expect(revision).To(BeEmpty())
		})

		It("should patch a deployment to update replicas and claim template reference", func() {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ResourceClaims: []corev1.PodResourceClaim{
								{
									Name:                      "gpu",
									ResourceClaimTemplateName: ptr.To("base-template"),
								},
							},
						},
					},
				},
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build(), logger)

			err := scaler.PatchDeployment(s, ctx, "default", "workload", "base-template", "base-template-rev-2", 3, logger)
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.Deployment{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "workload", Namespace: "default"}, updated)).To(Succeed())
			Expect(*updated.Spec.Replicas).To(Equal(int32(3)))
			Expect(*updated.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal("base-template-rev-2"))
		})

		It("should patch a statefulset to update replicas and claim template reference", func() {
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(2)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ResourceClaims: []corev1.PodResourceClaim{
								{
									Name:                      "gpu",
									ResourceClaimTemplateName: ptr.To("base-template"),
								},
							},
						},
					},
				},
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(statefulSet).Build(), logger)

			err := scaler.PatchStatefulSet(s, ctx, "default", "workload-sts", "base-template", "base-template-rev-3", 4, logger)
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.StatefulSet{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "workload-sts", Namespace: "default"}, updated)).To(Succeed())
			Expect(*updated.Spec.Replicas).To(Equal(int32(4)))
			Expect(*updated.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal("base-template-rev-3"))
		})

		It("should patch pod owner through deployment owner discovery", func() {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ResourceClaims: []corev1.PodResourceClaim{
								{
									Name:                      "gpu",
									ResourceClaimTemplateName: ptr.To("base-template"),
								},
							},
						},
					},
				},
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build(), logger)

			err := scaler.PatchPodOwner(s, ctx, autoscaler, "base-template-rev-5", 2)
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.Deployment{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "workload", Namespace: "default"}, updated)).To(Succeed())
			Expect(*updated.Spec.Replicas).To(Equal(int32(2)))
			Expect(*updated.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal("base-template-rev-5"))
		})

		It("should fail patching pod owner when discovery owner is unsupported", func() {
			autoscaler.Status.Discovery.Owner = &metav1.OwnerReference{
				Kind: "Job",
				Name: "unsupported",
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).Build(), logger)

			err := scaler.PatchPodOwner(s, ctx, autoscaler, "base-template-rev-5", 2)
			Expect(err).To(HaveOccurred())
		})

		It("should update replica status from deployment owner", func() {
			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas:     2,
					AvailableReplicas: 1,
				},
			}
			autoscaler.Status.Discovery.Owner = &metav1.OwnerReference{
				Kind: "Deployment",
				Name: "workload",
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(autoscaler).WithObjects(autoscaler, deployment).Build(), logger)

			err := scaler.UpdateReplicaStatusFromOwner(s, ctx, autoscaler, autoscaler.Status.Discovery.Owner)
			Expect(err).NotTo(HaveOccurred())
			Expect(*autoscaler.Status.ScalingStatus.ReadyReplicas).To(Equal(int32(2)))
			Expect(*autoscaler.Status.ScalingStatus.AvailableReplicas).To(Equal(int32(1)))
		})

		It("should update replica status from statefulset owner", func() {
			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload-sts",
					Namespace: "default",
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas:     3,
					AvailableReplicas: 2,
				},
			}
			autoscaler.Status.Discovery.Owner = &metav1.OwnerReference{
				Kind: "StatefulSet",
				Name: "workload-sts",
			}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(autoscaler).WithObjects(autoscaler, statefulSet).Build(), logger)

			err := scaler.UpdateReplicaStatusFromOwner(s, ctx, autoscaler, autoscaler.Status.Discovery.Owner)
			Expect(err).NotTo(HaveOccurred())
			Expect(*autoscaler.Status.ScalingStatus.ReadyReplicas).To(Equal(int32(3)))
			Expect(*autoscaler.Status.ScalingStatus.AvailableReplicas).To(Equal(int32(2)))
		})

		It("should reject unsupported owner kind when updating replica status", func() {
			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
			s = scaler.NewResourceScaler(fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(autoscaler).WithObjects(autoscaler).Build(), logger)

			err := scaler.UpdateReplicaStatusFromOwner(s, ctx, autoscaler, &metav1.OwnerReference{
				Kind: "CronJob",
				Name: "unsupported",
			})
			Expect(err).To(HaveOccurred())
		})

		It("should create a patched resource claim template with updated resources", func() {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "workload",
					Namespace:  "default",
					Generation: 4,
				},
			}
			originalTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template",
					Namespace: "default",
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"existing": "label",
						},
					},
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.nvidia.com",
										Count:           1,
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"compute": *resource.NewQuantity(40, resource.DecimalSI),
												"memory":  *resource.NewQuantity(8*1024*1024*1024, resource.BinarySI),
											},
										},
									},
								},
							},
						},
					},
				},
			}
			desired := optimizer.ResourceConfiguration{
				GPUsPerReplica:    2,
				RequestedCompute:  75,
				RequestedMemoryGB: 16,
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(originalTemplate, deployment).
				Build(), logger)

			newTemplateName, err := scaler.PatchResourceClaimTemplate(s, ctx, autoscaler, desired)
			Expect(err).NotTo(HaveOccurred())
			// New format: <autoscaler-name>-<hash>
			Expect(newTemplateName).To(HavePrefix("test-autoscaler-"))
			Expect(newTemplateName).To(HaveLen(len("test-autoscaler-") + 8)) // autoscaler name + dash + 8-char hash

			patched := &resourcev1.ResourceClaimTemplate{}
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: newTemplateName, Namespace: "default"}, patched)).To(Succeed())
			Expect(patched.Labels[scaler.ManagedByLabel]).To(Equal(scaler.OperatorName))
			Expect(patched.Labels[scaler.AutoscalerLabel]).To(Equal("test-autoscaler"))
			Expect(patched.Spec.ObjectMeta.Labels[scaler.TemplateNameLabel]).To(Equal(newTemplateName))

			request := patched.Spec.Spec.Devices.Requests[0].Exactly
			Expect(request.Count).To(Equal(int64(2)))
			compute := request.Capacity.Requests["compute"]
			memory := request.Capacity.Requests["memory"]
			Expect(compute.AsApproximateFloat64()).To(Equal(75.0))
			Expect(memory.Value()).To(Equal(int64(16 * 1024 * 1024 * 1024)))
		})

		It("should reuse an existing matching patched template", func() {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "workload",
					Namespace:  "default",
					Generation: 4,
				},
			}
			originalTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template",
					Namespace: "default",
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.nvidia.com",
										Count:           1,
									},
								},
							},
						},
					},
				},
			}
			existingTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template-rev-4",
					Namespace: "default",
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.nvidia.com",
										Count:           2,
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"compute": *resource.NewQuantity(75, resource.DecimalSI),
												"memory":  *resource.NewQuantity(16*1024*1024*1024, resource.BinarySI),
											},
										},
									},
								},
							},
						},
					},
				},
			}
			desired := optimizer.ResourceConfiguration{
				GPUsPerReplica:    2,
				RequestedCompute:  75,
				RequestedMemoryGB: 16,
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(originalTemplate, existingTemplate, deployment).
				Build(), logger)

			newTemplateName, err := scaler.PatchResourceClaimTemplate(s, ctx, autoscaler, desired)
			Expect(err).NotTo(HaveOccurred())
			// New format: <autoscaler-name>-<hash>
			Expect(newTemplateName).To(HavePrefix("test-autoscaler-"))
			Expect(newTemplateName).To(HaveLen(len("test-autoscaler-") + 8)) // autoscaler name + dash + 8-char hash
		})

		It("should clean up only unused resource claim templates", func() {
			templateInUse := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template-rev-1",
					Namespace: "default",
					Labels: map[string]string{
						scaler.ManagedByLabel:        scaler.OperatorName,
						scaler.OriginalTemplateLabel: "base-template",
					},
				},
			}
			templatePending := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template-rev-2",
					Namespace: "default",
					Labels: map[string]string{
						scaler.ManagedByLabel:        scaler.OperatorName,
						scaler.OriginalTemplateLabel: "base-template",
					},
				},
			}
			templateDesired := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template-rev-3",
					Namespace: "default",
					Labels: map[string]string{
						scaler.ManagedByLabel:        scaler.OperatorName,
						scaler.OriginalTemplateLabel: "base-template",
					},
				},
			}
			templateUnused := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "base-template-rev-4",
					Namespace: "default",
					Labels: map[string]string{
						scaler.ManagedByLabel:        scaler.OperatorName,
						scaler.OriginalTemplateLabel: "base-template",
					},
				},
			}
			boundClaim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bound-claim",
					Namespace: "default",
					Labels: map[string]string{
						scaler.ManagedByLabel:        scaler.OperatorName,
						scaler.AutoscalerLabel:       "test-autoscaler",
						scaler.OriginalTemplateLabel: "base-template",
						scaler.TemplateNameLabel:     "base-template-rev-1",
					},
				},
				Status: resourcev1.ResourceClaimStatus{
					Allocation: &resourcev1.AllocationResult{},
				},
			}
			pendingClaim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pending-claim",
					Namespace: "default",
					Labels: map[string]string{
						scaler.ManagedByLabel:        scaler.OperatorName,
						scaler.AutoscalerLabel:       "test-autoscaler",
						scaler.OriginalTemplateLabel: "base-template",
						scaler.TemplateNameLabel:     "base-template-rev-2",
					},
				},
			}

			autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
				DesiredClaimTemplate: "base-template-rev-3",
			}

			s = scaler.NewResourceScaler(fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(templateInUse, templatePending, templateDesired, templateUnused, boundClaim, pendingClaim).
				Build(), logger)

			err := scaler.CleanupOldResourceClaimTemplates(s, ctx, autoscaler)
			Expect(err).NotTo(HaveOccurred())

			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "base-template-rev-1", Namespace: "default"}, &resourcev1.ResourceClaimTemplate{})).To(Succeed())
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "base-template-rev-2", Namespace: "default"}, &resourcev1.ResourceClaimTemplate{})).To(Succeed())
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "base-template-rev-3", Namespace: "default"}, &resourcev1.ResourceClaimTemplate{})).To(Succeed())
			Expect(s.GetClient().Get(ctx, client.ObjectKey{Name: "base-template-rev-4", Namespace: "default"}, &resourcev1.ResourceClaimTemplate{})).NotTo(Succeed())
		})
	})
})

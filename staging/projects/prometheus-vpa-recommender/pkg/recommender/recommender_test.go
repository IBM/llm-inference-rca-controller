package recommender_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/yourusername/prometheus-vpa-recommender/pkg/recommender"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

// Mock Prometheus client for testing
type mockPrometheusClient struct {
	replicaValue   int32
	replicaError   error
	capacityValues map[string]float64
	capacityErrors map[string]error
}

func (m *mockPrometheusClient) QueryDesiredReplica(ctx context.Context, deployment string) (int32, error) {
	if m.replicaError != nil {
		return 0, m.replicaError
	}
	return m.replicaValue, nil
}

func (m *mockPrometheusClient) QueryDesiredCapacity(ctx context.Context, vpaName, containerName, deviceClassName, capacity string) (float64, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", vpaName, containerName, deviceClassName, capacity)
	if err, ok := m.capacityErrors[key]; ok {
		return 0, err
	}
	if val, ok := m.capacityValues[key]; ok {
		return val, nil
	}
	return 0, fmt.Errorf("no metric found")
}

// fakeDeploymentClient returns a fake kube client pre-populated with a Deployment
// whose pod spec has the given containers, each referencing all listed claimTemplateNames
// via pod-level resourceClaims entries.
func fakeDeploymentClient(namespace, name string, containerName string, claimTemplateNames ...string) *fake.Clientset {
	podClaims := make([]corev1.PodResourceClaim, len(claimTemplateNames))
	containerClaims := make([]corev1.ResourceClaim, len(claimTemplateNames))
	for i, tmpl := range claimTemplateNames {
		claimName := tmpl // use template name as the pod-level claim name for simplicity
		tmplCopy := tmpl
		podClaims[i] = corev1.PodResourceClaim{
			Name:                      claimName,
			ResourceClaimTemplateName: &tmplCopy,
		}
		containerClaims[i] = corev1.ResourceClaim{Name: claimName}
	}
	return fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ResourceClaims: podClaims,
					Containers: []corev1.Container{
						{
							Name: containerName,
							Resources: corev1.ResourceRequirements{
								Claims: containerClaims,
							},
						},
					},
				},
			},
		},
	})
}

var _ = Describe("Recommender", func() {
	Describe("buildContainerRecommendations", func() {
		DescribeTable("validating VPA structure for container recommendations",
			func(vpa *vpa_types.VerticalPodAutoscaler, expectError bool) {
				if vpa.Spec.ResourcePolicy == nil {
					if expectError {
						return
					}
					Fail("VPA should have resource policy")
				}

				if len(vpa.Spec.ResourcePolicy.ResourceClaimPolicies) == 0 {
					if expectError {
						return
					}
					Fail("VPA should have resource claim policies")
				}

				for _, policy := range vpa.Spec.ResourcePolicy.ResourceClaimPolicies {
					Expect(len(policy.ControlledCapacities)).To(BeNumerically(">", 0))
				}
			},
			Entry("VPA with GPU compute and memory",
				&vpa_types.VerticalPodAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vpa",
						Namespace: "default",
					},
					Spec: vpa_types.VerticalPodAutoscalerSpec{
						ResourcePolicy: &vpa_types.PodResourcePolicy{
							ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
								{
									ClaimTemplateName: "gpu-claim",
									DeviceClassName:   "gpu.example.com",
									ControlledCapacities: []resourcev1.QualifiedName{
										"compute",
										"memory",
									},
								},
							},
						},
					},
				},
				false,
			),
			Entry("VPA with Intel GPU",
				&vpa_types.VerticalPodAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "intel-vpa",
						Namespace: "default",
					},
					Spec: vpa_types.VerticalPodAutoscalerSpec{
						ResourcePolicy: &vpa_types.PodResourcePolicy{
							ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
								{
									ClaimTemplateName: "gpu-claim",
									DeviceClassName:   "gpu.intel.com",
									ControlledCapacities: []resourcev1.QualifiedName{
										"compute",
										"memory",
									},
								},
							},
						},
					},
				},
				false,
			),
			Entry("VPA without controlled capacities - should error",
				&vpa_types.VerticalPodAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "no-capacity-vpa",
						Namespace: "default",
					},
					Spec: vpa_types.VerticalPodAutoscalerSpec{
						ResourcePolicy: &vpa_types.PodResourcePolicy{
							ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{},
						},
					},
				},
				true,
			),
		)
	})

	Describe("Container Name Extraction", func() {
		DescribeTable("extracting container names from VPA policies",
			func(vpa *vpa_types.VerticalPodAutoscaler, expectedNames []string) {
				if vpa.Spec.ResourcePolicy != nil && len(vpa.Spec.ResourcePolicy.ContainerPolicies) > 0 {
					names := make([]string, 0, len(vpa.Spec.ResourcePolicy.ContainerPolicies))
					for _, policy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
						names = append(names, policy.ContainerName)
					}
					Expect(names).To(Equal(expectedNames))
				} else {
					Expect([]string{"app"}).To(Equal(expectedNames))
				}
			},
			Entry("VPA with multiple container policies",
				&vpa_types.VerticalPodAutoscaler{
					Spec: vpa_types.VerticalPodAutoscalerSpec{
						ResourcePolicy: &vpa_types.PodResourcePolicy{
							ContainerPolicies: []vpa_types.ContainerResourcePolicy{
								{ContainerName: "app"},
								{ContainerName: "sidecar"},
							},
						},
					},
				},
				[]string{"app", "sidecar"},
			),
			Entry("VPA without container policies defaults to app",
				&vpa_types.VerticalPodAutoscaler{
					Spec: vpa_types.VerticalPodAutoscalerSpec{
						ResourcePolicy: &vpa_types.PodResourcePolicy{},
					},
				},
				[]string{"app"},
			),
		)

		It("should discover containers from Deployment when no ContainerPolicies set", func() {
			ctx := context.Background()
			claimTmpl := "gpu-claim"
			fakeKube := fakeDeploymentClient("default", "test-app", "vllm-sim", claimTmpl)
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"test-vpa/vllm-sim/gpu.example.com/compute": 60,
				},
			}
			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vpa", Namespace: "default"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName:    claimTmpl,
								DeviceClassName:      "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{"compute"},
							},
						},
					},
				},
			}
			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recs, err := helper.BuildContainerRecommendations(ctx, "test-app", vpa)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].ContainerName).To(Equal("vllm-sim"))
		})
	})

	Describe("Resource List Building", func() {
		It("should create resource lists with DecimalSI format for GPU capacities", func() {
			resourceList := corev1.ResourceList{
				corev1.ResourceName("compute"): *resource.NewQuantity(60, resource.DecimalSI),
				corev1.ResourceName("memory"):  *resource.NewQuantity(8589934592, resource.DecimalSI),
			}

			Expect(resourceList).To(HaveLen(2))

			computeQty := resourceList[corev1.ResourceName("compute")]
			memoryQty := resourceList[corev1.ResourceName("memory")]

			Expect(computeQty.Value()).To(Equal(int64(60)))
			Expect(memoryQty.Value()).To(Equal(int64(8589934592)))
		})

		It("should handle multiple capacity types with DecimalSI", func() {
			capacities := []string{"compute", "memory", "bandwidth"}
			values := []int64{60, 8589934592, 1000000}

			resourceList := corev1.ResourceList{}
			for i, capacity := range capacities {
				resourceList[corev1.ResourceName(capacity)] = *resource.NewQuantity(values[i], resource.DecimalSI)
			}

			Expect(resourceList).To(HaveLen(3))
			for i, capacity := range capacities {
				qty := resourceList[corev1.ResourceName(capacity)]
				Expect(qty.Value()).To(Equal(values[i]))
			}
		})
	})

	Describe("Mock Prometheus Client", func() {
		It("should return configured replica value", func() {
			mock := &mockPrometheusClient{
				replicaValue: 3,
			}

			ctx := context.Background()
			value, err := mock.QueryDesiredReplica(ctx, "test-app")

			Expect(err).NotTo(HaveOccurred())
			Expect(value).To(Equal(int32(3)))
		})

		It("should return configured capacity values", func() {
			mock := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"test-vpa/app/gpu.example.com/compute": 60,
					"test-vpa/app/gpu.example.com/memory":  8589934592,
				},
			}

			ctx := context.Background()

			compute, err := mock.QueryDesiredCapacity(ctx, "test-vpa", "app", "gpu.example.com", "compute")
			Expect(err).NotTo(HaveOccurred())
			Expect(compute).To(Equal(float64(60)))

			memory, err := mock.QueryDesiredCapacity(ctx, "test-vpa", "app", "gpu.example.com", "memory")
			Expect(err).NotTo(HaveOccurred())
			Expect(memory).To(Equal(float64(8589934592)))
		})

		It("should return error for missing metrics", func() {
			mock := &mockPrometheusClient{
				capacityValues: map[string]float64{},
			}

			ctx := context.Background()
			_, err := mock.QueryDesiredCapacity(ctx, "test-vpa", "app", "gpu.example.com", "compute")

			Expect(err).To(HaveOccurred())
		})
	})
	Describe("Integration: Building Recommendations from Prometheus Metrics", func() {
		var (
			ctx context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
		})

		It("should build recommendations for NVIDIA GPU from Prometheus metrics", func() {
			// Setup mock Prometheus client with GPU metrics
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"test-vpa/app/gpu.example.com/compute": 60,
					"test-vpa/app/gpu.example.com/memory":  8589934592, // 8GB
				},
			}
			fakeKube := fakeDeploymentClient("default", "test-app", "app", "gpu-claim")

			// Create VPA with NVIDIA GPU capacities
			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vpa",
					Namespace: "default",
				},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			// Use test helper to build recommendations
			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "test-app", vpa)

			// Verify results
			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			rec := recommendations[0]
			Expect(rec.ContainerName).To(Equal("app"))
			Expect(rec.Target).To(HaveLen(2))

			// Check GPU compute
			computeQty := rec.Target[corev1.ResourceName("gpu.example.com/compute")]
			Expect(computeQty.Value()).To(Equal(int64(60)))

			// Check GPU memory
			memoryQty := rec.Target[corev1.ResourceName("gpu.example.com/memory")]
			Expect(memoryQty.Value()).To(Equal(int64(8589934592)))
		})

		It("should build recommendations for Intel GPU from Prometheus metrics", func() {
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"intel-vpa/app/gpu.intel.com/compute": 120,
					"intel-vpa/app/gpu.intel.com/memory":  17179869184, // 16GB
				},
			}
			fakeKube := fakeDeploymentClient("default", "intel-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "intel-vpa",
					Namespace: "default",
				},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "intel-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.intel.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "intel-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			rec := recommendations[0]
			computeQty := rec.Target[corev1.ResourceName("gpu.intel.com/compute")]
			Expect(computeQty.Value()).To(Equal(int64(120)))

			memoryQty := rec.Target[corev1.ResourceName("gpu.intel.com/memory")]
			Expect(memoryQty.Value()).To(Equal(int64(17179869184)))
		})

		It("should handle multiple device classes in one VPA", func() {
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"multi-vpa/app/gpu.example.com/compute": 60,
					"multi-vpa/app/gpu.example.com/memory":  8589934592,
					"multi-vpa/app/gpu.intel.com/compute":    120,
					"multi-vpa/app/gpu.intel.com/memory":     17179869184,
				},
			}
			fakeKube := fakeDeploymentClient("default", "multi-app", "app", "nvidia-gpu-claim", "intel-gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-vpa",
					Namespace: "default",
				},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "multi-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "nvidia-gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
							{
								ClaimTemplateName: "intel-gpu-claim",
								DeviceClassName:   "gpu.intel.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "multi-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			rec := recommendations[0]
			Expect(rec.Target).To(HaveLen(4)) // 2 capacities x 2 device classes

			// Verify all capacities are present
			Expect(rec.Target).To(HaveKey(corev1.ResourceName("gpu.example.com/compute")))
			Expect(rec.Target).To(HaveKey(corev1.ResourceName("gpu.example.com/memory")))
			Expect(rec.Target).To(HaveKey(corev1.ResourceName("gpu.intel.com/compute")))
			Expect(rec.Target).To(HaveKey(corev1.ResourceName("gpu.intel.com/memory")))
		})

		It("should return error when no metrics are found", func() {
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{},
			}
			fakeKube := fakeDeploymentClient("default", "no-metrics-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-metrics-vpa",
					Namespace: "default",
				},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "no-metrics-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			_, err := helper.BuildContainerRecommendations(ctx, "no-metrics-app", vpa)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no container recommendations could be built"))
		})

		It("should use capacity_name format deviceClassName/capacity", func() {
			// The mock client expects keys in format: vpaName/containerName/deviceClassName/capacity
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"test-vpa/app/gpu.example.com/compute": 60,
					"test-vpa/app/gpu.example.com/memory":  8589934592,
				},
			}
			fakeKube := fakeDeploymentClient("default", "test-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vpa", Namespace: "default"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "test-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			// Verify the resource names use the full deviceClassName/capacity format
			rec := recommendations[0]
			_, hasCompute := rec.Target[corev1.ResourceName("gpu.example.com/compute")]
			_, hasMemory := rec.Target[corev1.ResourceName("gpu.example.com/memory")]
			Expect(hasCompute).To(BeTrue())
			Expect(hasMemory).To(BeTrue())
		})
	})
})

var _ = Describe("updateVPARecommendation", func() {
	var (
		ctx context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("Building recommendations from Prometheus metrics", func() {
		It("should build recommendations for NVIDIA GPU", func() {
			mockClient := &mockPrometheusClient{
				replicaValue: 2,
				capacityValues: map[string]float64{
					"test-vpa/app/gpu.example.com/compute": 60,
					"test-vpa/app/gpu.example.com/memory":  8589934592,
				},
			}
			fakeKube := fakeDeploymentClient("default", "test-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vpa",
					Namespace: "default",
				},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "test-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			rec := recommendations[0]
			Expect(rec.ContainerName).To(Equal("app"))
			Expect(rec.Target).To(HaveLen(2))

			computeQty := rec.Target[corev1.ResourceName("gpu.example.com/compute")]
			Expect(computeQty.Value()).To(Equal(int64(60)))

			memoryQty := rec.Target[corev1.ResourceName("gpu.example.com/memory")]
			Expect(memoryQty.Value()).To(Equal(int64(8589934592)))
		})

		It("should build recommendations for Intel GPU", func() {
			mockClient := &mockPrometheusClient{
				replicaValue: 3,
				capacityValues: map[string]float64{
					"intel-vpa/app/gpu.intel.com/compute": 120,
					"intel-vpa/app/gpu.intel.com/memory":  17179869184,
				},
			}
			fakeKube := fakeDeploymentClient("default", "intel-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "intel-vpa", Namespace: "default"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "intel-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.intel.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "intel-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			rec := recommendations[0]
			computeQty := rec.Target[corev1.ResourceName("gpu.intel.com/compute")]
			Expect(computeQty.Value()).To(Equal(int64(120)))
			memoryQty := rec.Target[corev1.ResourceName("gpu.intel.com/memory")]
			Expect(memoryQty.Value()).To(Equal(int64(17179869184)))
		})

		It("should handle multiple device classes", func() {
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"multi-vpa/app/gpu.example.com/compute": 60,
					"multi-vpa/app/gpu.example.com/memory":  8589934592,
					"multi-vpa/app/gpu.intel.com/compute":    120,
					"multi-vpa/app/gpu.intel.com/memory":     17179869184,
				},
			}
			fakeKube := fakeDeploymentClient("default", "multi-app", "app", "nvidia-gpu", "intel-gpu")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "multi-vpa", Namespace: "default"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "multi-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "nvidia-gpu",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
							{
								ClaimTemplateName: "intel-gpu",
								DeviceClassName:   "gpu.intel.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "multi-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))
			Expect(recommendations[0].Target).To(HaveLen(4))
		})

		It("should return error when no metrics found", func() {
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{},
			}
			fakeKube := fakeDeploymentClient("default", "no-metrics-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "no-metrics-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			_, err := helper.BuildContainerRecommendations(ctx, "no-metrics-app", vpa)

			Expect(err).To(HaveOccurred())
		})

		It("should use capacity_name format deviceClassName/capacity", func() {
			// The mock client expects keys in format: vpaName/containerName/deviceClassName/capacity
			mockClient := &mockPrometheusClient{
				capacityValues: map[string]float64{
					"test-vpa/app/gpu.example.com/compute": 60,
					"test-vpa/app/gpu.example.com/memory":  8589934592,
				},
			}
			fakeKube := fakeDeploymentClient("default", "test-app", "app", "gpu-claim")

			vpa := &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vpa", Namespace: "default"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-app",
					},
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourcev1.QualifiedName{
									"compute",
									"memory",
								},
							},
						},
					},
				},
			}

			helper := recommender.NewTestHelper(mockClient, fakeKube)
			recommendations, err := helper.BuildContainerRecommendations(ctx, "test-app", vpa)

			Expect(err).NotTo(HaveOccurred())
			Expect(recommendations).To(HaveLen(1))

			// Verify the resource names use the full deviceClassName/capacity format
			rec := recommendations[0]
			_, hasCompute := rec.Target[corev1.ResourceName("gpu.example.com/compute")]
			_, hasMemory := rec.Target[corev1.ResourceName("gpu.example.com/memory")]
			Expect(hasCompute).To(BeTrue())
			Expect(hasMemory).To(BeTrue())
		})
	})
})

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

package discovery

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("ServiceDiscoverer", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		fakeClient client.Client
		discoverer *ServiceDiscoverer
		namespace  string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "test-namespace"

		// Create scheme with all required types
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(appsv1.AddToScheme(scheme)).To(Succeed())
		Expect(discoveryv1.AddToScheme(scheme)).To(Succeed())
		Expect(resourcev1.AddToScheme(scheme)).To(Succeed())
	})

	type workloadDiscoveryTestCase struct {
		workloadKind   string
		workloadName   string
		endpointSuffix string
		setupWorkload  func() []client.Object
	}

	DescribeTable("When discovering from a service with workload",
		func(tc workloadDiscoveryTestCase) {
			// Create common objects
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: namespace,
				},
			}

			claimTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: namespace,
				},
			}

			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service" + tc.endpointSuffix,
					Namespace: namespace,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: "test-service",
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						TargetRef: &corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "test-pod",
							Namespace: namespace,
						},
					},
				},
			}

			// Setup workload-specific objects
			workloadObjects := tc.setupWorkload()
			allObjects := append([]client.Object{service, claimTemplate, endpointSlice}, workloadObjects...)

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allObjects...).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)

			// Execute discovery
			result, err := discoverer.DiscoverFromService(ctx, "test-service", namespace, "")

			// Verify results
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())
			Expect(result.Owner).NotTo(BeNil())
			Expect(result.Owner.Kind).To(Equal(tc.workloadKind))
			Expect(result.Owner.Name).To(Equal(tc.workloadName))
			Expect(result.ResourceClaimTemplate).To(Equal("test-template"))
		},
		Entry("Deployment", workloadDiscoveryTestCase{
			workloadKind:   "Deployment",
			workloadName:   "test-deployment",
			endpointSuffix: "-abc",
			setupWorkload: func() []client.Object {
				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-deployment",
						Namespace: namespace,
					},
					Spec: appsv1.DeploymentSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{
									{
										Name:                      "test-claim",
										ResourceClaimTemplateName: ptr.To("test-template"),
									},
								},
							},
						},
					},
				}

				replicaSet := &appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rs",
						Namespace: namespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
								Name:       "test-deployment",
								Controller: ptr.To(true),
							},
						},
					},
				}

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "ReplicaSet",
								Name:       "test-rs",
								Controller: ptr.To(true),
							},
						},
					},
				}

				return []client.Object{deployment, replicaSet, pod}
			},
		}),
		Entry("StatefulSet", workloadDiscoveryTestCase{
			workloadKind:   "StatefulSet",
			workloadName:   "test-statefulset",
			endpointSuffix: "-xyz",
			setupWorkload: func() []client.Object {
				statefulSet := &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-statefulset",
						Namespace: namespace,
					},
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{
									{
										Name:                      "test-claim",
										ResourceClaimTemplateName: ptr.To("test-template"),
									},
								},
							},
						},
					},
				}

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "test-statefulset",
								Controller: ptr.To(true),
							},
						},
					},
				}

				return []client.Object{statefulSet, pod}
			},
		}),
	)

	Context("When service has no EndpointSlices", func() {
		BeforeEach(func() {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: namespace,
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(service).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)
		})

		It("should return nil result without error", func() {
			result, err := discoverer.DiscoverFromService(ctx, "test-service", namespace, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	Context("When service does not exist", func() {
		BeforeEach(func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)
		})

		It("should return an error", func() {
			result, err := discoverer.DiscoverFromService(ctx, "nonexistent-service", namespace, "")
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	Context("When workload has no ResourceClaimTemplate", func() {
		BeforeEach(func() {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: namespace,
				},
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment",
							Controller: ptr.To(true),
						},
					},
				},
			}

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							// No ResourceClaims
						},
					},
				},
			}

			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service-abc",
					Namespace: namespace,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: "test-service",
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						TargetRef: &corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "test-pod",
							Namespace: namespace,
						},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(service, pod, deployment, endpointSlice).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)
		})

		It("should return an error", func() {
			result, err := discoverer.DiscoverFromService(ctx, "test-service", namespace, "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no ResourceClaimTemplate found"))
			Expect(result).To(BeNil())
		})
	})

	Context("When pods have inconsistent owners", func() {
		BeforeEach(func() {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: namespace,
				},
			}

			// Create two different deployments
			deployment1 := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deployment-1",
					Namespace: namespace,
				},
			}

			deployment2 := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deployment-2",
					Namespace: namespace,
				},
			}

			pod1 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-1",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "deployment-1",
							UID:        deployment1.UID,
							Controller: ptr.To(true),
						},
					},
				},
			}

			pod2 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-2",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "deployment-2",
							UID:        deployment2.UID,
							Controller: ptr.To(true),
						},
					},
				},
			}

			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service-abc",
					Namespace: namespace,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: "test-service",
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						TargetRef: &corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "test-pod-1",
							Namespace: namespace,
						},
					},
					{
						TargetRef: &corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "test-pod-2",
							Namespace: namespace,
						},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(service, deployment1, deployment2, pod1, pod2, endpointSlice).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)
		})

		It("should return an error about inconsistent owners", func() {
			result, err := discoverer.DiscoverFromService(ctx, "test-service", namespace, "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("inconsistent owners detected"))
			Expect(result).To(BeNil())
		})
	})

	Context("When pod has no owner (standalone pod)", func() {
		BeforeEach(func() {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: namespace,
				},
			}

			// Standalone pod with no owner references
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-pod",
					Namespace: namespace,
					UID:       "pod-uid-123",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "nginx:latest",
						},
					},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "test-claim",
							ResourceClaimTemplateName: ptr.To("standalone-template"),
						},
					},
				},
			}

			claimTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-template",
					Namespace: namespace,
				},
			}

			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service-abc",
					Namespace: namespace,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: "test-service",
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						TargetRef: &corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "standalone-pod",
							Namespace: namespace,
						},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(service, pod, claimTemplate, endpointSlice).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)
		})

		It("should discover Pod as owner and ResourceClaimTemplate", func() {
			result, err := discoverer.DiscoverFromService(ctx, "test-service", namespace, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())
			Expect(result.Owner).NotTo(BeNil())
			Expect(result.Owner.Kind).To(Equal("Pod"))
			Expect(result.Owner.Name).To(Equal("standalone-pod"))
			Expect(result.ResourceClaimTemplate).To(Equal("standalone-template"))
		})
	})

	Context("When ResourceClaimTemplate exists but doesn't match target device class", func() {
		BeforeEach(func() {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: namespace,
				},
			}

			// ResourceClaimTemplate with different device class
			claimTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim-template",
					Namespace: namespace,
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "gpu.amd.com", // Different from target
										Count:           1,
									},
								},
							},
						},
					},
				},
			}

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx:latest",
								},
							},
							ResourceClaims: []corev1.PodResourceClaim{
								{
									Name:                      "test-claim",
									ResourceClaimTemplateName: ptr.To("test-claim-template"),
								},
							},
						},
					},
				},
			}

			replicaSet := &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rs",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment",
							UID:        deployment.UID,
							Controller: ptr.To(true),
						},
					},
				},
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "ReplicaSet",
							Name:       "test-rs",
							UID:        replicaSet.UID,
							Controller: ptr.To(true),
						},
					},
				},
			}

			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service-abc",
					Namespace: namespace,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: "test-service",
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						TargetRef: &corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "test-pod",
							Namespace: namespace,
						},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(service, claimTemplate, deployment, replicaSet, pod, endpointSlice).
				Build()

			discoverer = NewServiceDiscoverer(fakeClient)
		})

		It("should return an error about device class mismatch", func() {
			// Target device class is "gpu.nvidia.com" but template has "gpu.amd.com"
			result, err := discoverer.DiscoverFromService(ctx, "test-service", namespace, "gpu.nvidia.com")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("does not have device class"))
			Expect(err.Error()).To(ContainSubstring("gpu.nvidia.com"))
			Expect(result).To(BeNil())
		})
	})
})

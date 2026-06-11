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

package controller

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

const (
	testDeviceClassName = "gpu.nvidia.com"
)

var _ = Describe("ResourceClaimAutoscaler Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When discovering service targets", func() {
		var (
			ctx             context.Context
			namespace       string
			autoscalerName  string
			serviceName     string
			deviceClassName string
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = fmt.Sprintf("test-ns-%d", rand.Int63())
			autoscalerName = "test-autoscaler"
			serviceName = "test-service"
			deviceClassName = testDeviceClassName

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up namespace - best effort deletion
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Delete(ctx, ns)
			// Note: We don't wait for deletion as it can take time with finalizers
			// The test environment will clean up eventually
		})

		type workloadTestCase struct {
			workloadKind   string
			workloadName   string
			templateName   string
			appLabel       string
			podIP          string
			endpointSuffix string
			createWorkload func(context.Context, string, string, string, string, string) (types.UID, error)
		}

		createDeploymentWorkload := func(ctx context.Context, namespace, workloadName, templateName, appLabel, deviceClassName string) (types.UID, error) {
			// Create ResourceClaimTemplate
			claimTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      templateName,
					Namespace: namespace,
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: deviceClassName,
										Count:           1,
									},
								},
							},
						},
					},
				},
			}
			if err := k8sClient.Create(ctx, claimTemplate); err != nil {
				return "", err
			}

			// Create Deployment
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workloadName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": appLabel,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": appLabel,
							},
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
									ResourceClaimTemplateName: ptr.To(templateName),
								},
							},
						},
					},
				},
			}
			if err := k8sClient.Create(ctx, deployment); err != nil {
				return "", err
			}

			// Create ReplicaSet (owned by Deployment)
			replicaSet := &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workloadName + "-rs",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       workloadName,
							UID:        deployment.UID,
							Controller: ptr.To(true),
						},
					},
				},
				Spec: appsv1.ReplicaSetSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": appLabel,
						},
					},
					Template: deployment.Spec.Template,
				},
			}
			if err := k8sClient.Create(ctx, replicaSet); err != nil {
				return "", err
			}

			return deployment.UID, nil
		}

		createStatefulSetWorkload := func(ctx context.Context, namespace, workloadName, templateName, appLabel, deviceClassName string) (types.UID, error) {
			// Create ResourceClaimTemplate
			claimTemplate := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      templateName,
					Namespace: namespace,
				},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: deviceClassName,
										Count:           1,
									},
								},
							},
						},
					},
				},
			}
			if err := k8sClient.Create(ctx, claimTemplate); err != nil {
				return "", err
			}

			// Create StatefulSet
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workloadName,
					Namespace: namespace,
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": appLabel,
						},
					},
					ServiceName: serviceName,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": appLabel,
							},
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
									ResourceClaimTemplateName: ptr.To(templateName),
								},
							},
						},
					},
				},
			}
			if err := k8sClient.Create(ctx, statefulSet); err != nil {
				return "", err
			}

			return statefulSet.UID, nil
		}

		DescribeTable("should discover workload from Service endpoints",
			func(tc workloadTestCase) {
				// Create workload and get its UID
				workloadUID, err := tc.createWorkload(ctx, namespace, tc.workloadName, tc.templateName, tc.appLabel, deviceClassName)
				Expect(err).NotTo(HaveOccurred())

				// Create Pod (owned by workload)
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-" + tc.appLabel,
						Namespace: namespace,
						Labels: map[string]string{
							"app": tc.appLabel,
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       tc.workloadKind,
								Name:       tc.workloadName,
								UID:        workloadUID,
								Controller: ptr.To(true),
							},
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "nginx:latest",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						PodIP: tc.podIP,
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				// Create Service
				service := &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName,
						Namespace: namespace,
					},
					Spec: corev1.ServiceSpec{
						Selector: map[string]string{
							"app": tc.appLabel,
						},
						Ports: []corev1.ServicePort{
							{
								Port: 80,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, service)).To(Succeed())

				// Create EndpointSlice
				endpointSlice := &discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName + tc.endpointSuffix,
						Namespace: namespace,
						Labels: map[string]string{
							discoveryv1.LabelServiceName: serviceName,
						},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints: []discoveryv1.Endpoint{
						{
							Addresses: []string{tc.podIP},
							TargetRef: &corev1.ObjectReference{
								Kind:      "Pod",
								Name:      pod.Name,
								Namespace: namespace,
							},
						},
					},
					Ports: []discoveryv1.EndpointPort{
						{
							Port: ptr.To(int32(80)),
						},
					},
				}
				Expect(k8sClient.Create(ctx, endpointSlice)).To(Succeed())

				// Create ResourceClaimAutoscaler
				autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      autoscalerName,
						Namespace: namespace,
					},
					Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
						Target: autoscalingv1alpha1.TargetSelector{
							ServiceRef: autoscalingv1alpha1.ServiceReference{
								Name: serviceName,
							},
							ResourceRef: autoscalingv1alpha1.ResourceReference{
								DeviceClassName: deviceClassName,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

				// Wait for discovery to complete
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      autoscalerName,
						Namespace: namespace,
					}, autoscaler)
					if err != nil {
						return false
					}
					return autoscaler.Status.Discovery != nil &&
						autoscaler.Status.Discovery.Owner != nil &&
						autoscaler.Status.Discovery.ResourceClaim != ""
				}, timeout, interval).Should(BeTrue())

				// Verify discovered owner and template
				Expect(autoscaler.Status.Discovery.Owner.Kind).To(Equal(tc.workloadKind))
				Expect(autoscaler.Status.Discovery.Owner.Name).To(Equal(tc.workloadName))
				Expect(autoscaler.Status.Discovery.ResourceClaim).To(Equal(tc.templateName))

				// Verify ScalingTargetDiscovery condition
				cond := meta.FindStatusCondition(autoscaler.Status.Conditions, "ScalingTargetDiscovery")
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				Expect(cond.Reason).To(Equal("ScalingTargetDiscovered"))
			},
			Entry("Deployment",
				workloadTestCase{
					workloadKind:   "Deployment",
					workloadName:   "test-deployment",
					templateName:   "test-claim-template",
					appLabel:       "test",
					podIP:          "10.0.0.1",
					endpointSuffix: "-abc",
					createWorkload: createDeploymentWorkload,
				},
			),
			Entry("StatefulSet",
				workloadTestCase{
					workloadKind:   "StatefulSet",
					workloadName:   "test-statefulset",
					templateName:   "test-claim-template-sts",
					appLabel:       "test-sts",
					podIP:          "10.0.0.2",
					endpointSuffix: "-xyz",
					createWorkload: createStatefulSetWorkload,
				},
			),
		)

		It("should handle service with no endpoints", func() {
			// Create Service without endpoints
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: namespace,
				},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{
						"app": "nonexistent",
					},
					Ports: []corev1.ServicePort{
						{
							Port: 80,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			// Create empty Endpoints
			//nolint:staticcheck // Endpoints is deprecated but still widely used
			endpoints := &corev1.Endpoints{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: namespace,
				},
				//nolint:staticcheck // EndpointSubset is deprecated but still widely used
				Subsets: []corev1.EndpointSubset{},
			}
			Expect(k8sClient.Create(ctx, endpoints)).To(Succeed())

			// Create ResourceClaimAutoscaler
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName,
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: serviceName,
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Reconcile should succeed even with no endpoints
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName,
					Namespace: namespace,
				}, autoscaler)
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When monitoring is configured", func() {
		var (
			ctx             context.Context
			namespace       string
			autoscalerName  string
			serviceName     string
			deviceClassName string
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = fmt.Sprintf("test-monitor-ns-%d", rand.Int63())
			autoscalerName = "test-monitor-autoscaler"
			serviceName = "test-monitor-service"
			deviceClassName = testDeviceClassName

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should set MonitoringActive condition when ServiceMonitor is initialized", func() {
			// Create ResourceClaimAutoscaler with behavior configuration
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName,
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: serviceName,
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleUp: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 60,
							},
							GracePeriodSeconds: 30,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Wait for MonitoringActive condition to be set
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName,
					Namespace: namespace,
				}, autoscaler)
				if err != nil {
					return false
				}
				cond := meta.FindStatusCondition(autoscaler.Status.Conditions, "MonitoringActive")
				return cond != nil
			}, timeout, interval).Should(BeTrue())

			// Verify the condition status
			cond := meta.FindStatusCondition(autoscaler.Status.Conditions, "MonitoringActive")
			Expect(cond).NotTo(BeNil())
			// Condition should be True if ServiceMonitor is initialized, False otherwise
			// The actual status depends on whether ServiceMonitor was set up in the reconciler
		})

		It("should apply default behavior when none is specified", func() {
			// Create ResourceClaimAutoscaler without behavior configuration
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName + "-default",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: serviceName,
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
					// No Behavior specified - should use defaults
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Wait for reconciliation
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-default",
					Namespace: namespace,
				}, autoscaler)
			}, timeout, interval).Should(Succeed())

			// Verify MonitoringActive condition exists (defaults should be applied)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-default",
					Namespace: namespace,
				}, autoscaler)
				if err != nil {
					return false
				}
				cond := meta.FindStatusCondition(autoscaler.Status.Conditions, "MonitoringActive")
				return cond != nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should update cached spec on subsequent reconciliations", func() {
			// Create ResourceClaimAutoscaler
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName + "-update",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: serviceName,
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
					Behavior: &autoscalingv1alpha1.Behavior{
						ScaleUp: &autoscalingv1alpha1.ScalingBehavior{
							Trigger: autoscalingv1alpha1.ScalingTrigger{
								PeriodSeconds: 60,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Wait for initial reconciliation
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-update",
					Namespace: namespace,
				}, autoscaler)
				if err != nil {
					return false
				}
				cond := meta.FindStatusCondition(autoscaler.Status.Conditions, "MonitoringActive")
				return cond != nil
			}, timeout, interval).Should(BeTrue())

			// Update the spec
			autoscaler.Spec.Behavior.ScaleUp.Trigger.PeriodSeconds = 120
			Expect(k8sClient.Update(ctx, autoscaler)).To(Succeed())

			// Wait for update to be processed
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-update",
					Namespace: namespace,
				}, autoscaler)
				if err != nil {
					return 0
				}
				if autoscaler.Spec.Behavior != nil && autoscaler.Spec.Behavior.ScaleUp != nil {
					return autoscaler.Spec.Behavior.ScaleUp.Trigger.PeriodSeconds
				}
				return 0
			}, timeout, interval).Should(Equal(int32(120)))
		})

		It("should handle unregistration when autoscaler is deleted", func() {
			// Create ResourceClaimAutoscaler
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName + "-delete",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: serviceName,
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Wait for registration
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-delete",
					Namespace: namespace,
				}, autoscaler)
			}, timeout, interval).Should(Succeed())

			// Delete the autoscaler
			Expect(k8sClient.Delete(ctx, autoscaler)).To(Succeed())

			// Verify it's deleted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-delete",
					Namespace: namespace,
				}, autoscaler)
				return err != nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When discovery is enabled", func() {
		var (
			ctx             context.Context
			namespace       string
			autoscalerName  string
			serviceName     string
			deviceClassName string
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = fmt.Sprintf("test-discovery-ns-%d", rand.Int63())
			autoscalerName = "test-discovery-autoscaler"
			serviceName = "test-discovery-service"
			deviceClassName = testDeviceClassName

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should only run discovery when Status.Discovery is not nil", func() {
			// Create autoscaler without initializing Status.Discovery
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName,
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: serviceName,
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Wait for reconciliation
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName,
					Namespace: namespace,
				}, autoscaler)
			}, timeout, interval).Should(Succeed())

			// Discovery should not run (Status.Discovery is nil)
			Consistently(func() *autoscalingv1alpha1.DiscoveryResults {
				_ = k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName,
					Namespace: namespace,
				}, autoscaler)
				return autoscaler.Status.Discovery
			}, time.Second*2, interval).Should(BeNil())
		})

		It("should set ScalingTargetDiscovery condition on discovery failure", func() {
			// Create autoscaler with Status.Discovery initialized but no service
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      autoscalerName + "-fail",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
					Target: autoscalingv1alpha1.TargetSelector{
						ServiceRef: autoscalingv1alpha1.ServiceReference{
							Name: "nonexistent-service",
						},
						ResourceRef: autoscalingv1alpha1.ResourceReference{
							DeviceClassName: deviceClassName,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

			// Wait for finalizer to be added first
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-fail",
					Namespace: namespace,
				}, autoscaler)
				if err != nil {
					return false
				}
				for _, f := range autoscaler.Finalizers {
					if f == FinalizerName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Update status to initialize Discovery with retry to handle conflicts
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-fail",
					Namespace: namespace,
				}, autoscaler); err != nil {
					return err
				}
				autoscaler.Status.Discovery = &autoscalingv1alpha1.DiscoveryResults{}
				return k8sClient.Status().Update(ctx, autoscaler)
			}, timeout, interval).Should(Succeed())

			// Wait for ScalingTargetDiscovery condition
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      autoscalerName + "-fail",
					Namespace: namespace,
				}, autoscaler)
				if err != nil {
					return false
				}
				cond := meta.FindStatusCondition(autoscaler.Status.Conditions, "ScalingTargetDiscovery")
				return cond != nil && cond.Status == metav1.ConditionFalse
			}, timeout, interval).Should(BeTrue())
		})

		Context("When handling finalizer and cleanup", func() {
			var (
				ctx             context.Context
				namespace       string
				autoscalerName  string
				serviceName     string
				deploymentName  string
				templateName    string
				deviceClassName string
			)

			BeforeEach(func() {
				ctx = context.Background()
				namespace = fmt.Sprintf("test-finalizer-%d", rand.Int63())
				autoscalerName = "test-autoscaler-finalizer"
				serviceName = "test-service-finalizer"
				deploymentName = "test-deployment-finalizer"
				templateName = "test-claim-template-finalizer"
				deviceClassName = testDeviceClassName

				// Create namespace
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespace,
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			})

			AfterEach(func() {
				// Clean up namespace
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespace,
					},
				}
				_ = k8sClient.Delete(ctx, ns)
			})

			It("should add finalizer on creation", func() {
				// Create ResourceClaimTemplate
				claimTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      templateName,
						Namespace: namespace,
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           1,
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, claimTemplate)).To(Succeed())

				// Create Deployment
				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      deploymentName,
						Namespace: namespace,
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: ptr.To(int32(1)),
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"app": "test"},
							},
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{
									{
										Name:                      "gpu-claim",
										ResourceClaimTemplateName: &templateName,
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "test",
										Image: "nginx:latest",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

				// Create Pod
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-finalizer",
						Namespace: namespace,
						Labels:    map[string]string{"app": "test"},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
								Name:       deploymentName,
								UID:        deployment.UID,
							},
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "nginx:latest",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						PodIP: "10.0.0.1",
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				// Create Service
				service := &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName,
						Namespace: namespace,
					},
					Spec: corev1.ServiceSpec{
						Selector: map[string]string{"app": "test"},
						Ports: []corev1.ServicePort{
							{Port: 80},
						},
					},
				}
				Expect(k8sClient.Create(ctx, service)).To(Succeed())

				// Create EndpointSlice
				endpointSlice := &discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName + "-abc",
						Namespace: namespace,
						Labels: map[string]string{
							discoveryv1.LabelServiceName: serviceName,
						},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints: []discoveryv1.Endpoint{
						{
							Addresses: []string{"10.0.0.1"},
							TargetRef: &corev1.ObjectReference{
								Kind:      "Pod",
								Name:      pod.Name,
								Namespace: namespace,
							},
						},
					},
					Ports: []discoveryv1.EndpointPort{
						{Port: ptr.To(int32(80))},
					},
				}
				Expect(k8sClient.Create(ctx, endpointSlice)).To(Succeed())

				// Create ResourceClaimAutoscaler
				autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      autoscalerName,
						Namespace: namespace,
					},
					Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
						Target: autoscalingv1alpha1.TargetSelector{
							ServiceRef: autoscalingv1alpha1.ServiceReference{
								Name: serviceName,
							},
							ResourceRef: autoscalingv1alpha1.ResourceReference{
								DeviceClassName: deviceClassName,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

				// Wait for finalizer to be added
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      autoscalerName,
						Namespace: namespace,
					}, autoscaler)
					if err != nil {
						return false
					}
					for _, f := range autoscaler.Finalizers {
						if f == FinalizerName {
							return true
						}
					}
					return false
				}, timeout, interval).Should(BeTrue())

				// Verify finalizer is present
				Expect(autoscaler.Finalizers).To(ContainElement(FinalizerName))
			})

			It("should restore original template on deletion", func() {
				// Create original ResourceClaimTemplate
				originalTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      templateName,
						Namespace: namespace,
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           1,
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, originalTemplate)).To(Succeed())

				// Create revised template (simulating scaling action)
				revisedTemplateName := templateName + "-rev-1"
				revisedTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      revisedTemplateName,
						Namespace: namespace,
						Labels: map[string]string{
							"autoscaling.x-llmd.ai/managed-by":        "resourceclaimautoscaler",
							"autoscaling.x-llmd.ai/original-template": templateName,
						},
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           2, // Scaled up
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, revisedTemplate)).To(Succeed())

				// Create Deployment using revised template
				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      deploymentName,
						Namespace: namespace,
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: ptr.To(int32(1)),
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"app": "test"},
							},
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{
									{
										Name:                      "gpu-claim",
										ResourceClaimTemplateName: &revisedTemplateName,
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "test",
										Image: "nginx:latest",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

				// Create supporting resources (Pod, Service, EndpointSlice)
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-cleanup",
						Namespace: namespace,
						Labels:    map[string]string{"app": "test"},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
								Name:       deploymentName,
								UID:        deployment.UID,
							},
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "test", Image: "nginx:latest"},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						PodIP: "10.0.0.2",
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				service := &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName,
						Namespace: namespace,
					},
					Spec: corev1.ServiceSpec{
						Selector: map[string]string{"app": "test"},
						Ports:    []corev1.ServicePort{{Port: 80}},
					},
				}
				Expect(k8sClient.Create(ctx, service)).To(Succeed())

				endpointSlice := &discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName + "-xyz",
						Namespace: namespace,
						Labels: map[string]string{
							discoveryv1.LabelServiceName: serviceName,
						},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints: []discoveryv1.Endpoint{
						{
							Addresses: []string{"10.0.0.2"},
							TargetRef: &corev1.ObjectReference{
								Kind:      "Pod",
								Name:      pod.Name,
								Namespace: namespace,
							},
						},
					},
					Ports: []discoveryv1.EndpointPort{{Port: ptr.To(int32(80))}},
				}
				Expect(k8sClient.Create(ctx, endpointSlice)).To(Succeed())

				// Create ResourceClaimAutoscaler
				autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:       autoscalerName,
						Namespace:  namespace,
						Finalizers: []string{FinalizerName},
					},
					Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
						Target: autoscalingv1alpha1.TargetSelector{
							ServiceRef: autoscalingv1alpha1.ServiceReference{
								Name: serviceName,
							},
							ResourceRef: autoscalingv1alpha1.ResourceReference{
								DeviceClassName: deviceClassName,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

				// Update status to indicate revised template (with retry to handle conflicts)
				Eventually(func() error {
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      autoscalerName,
						Namespace: namespace,
					}, autoscaler); err != nil {
						return err
					}
					autoscaler.Status.Discovery = &autoscalingv1alpha1.DiscoveryResults{
						Owner: &metav1.OwnerReference{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							UID:        deployment.UID,
						},
						ResourceClaim: templateName,
					}
					autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
						DesiredClaimTemplate: revisedTemplateName,
					}
					return k8sClient.Status().Update(ctx, autoscaler)
				}, timeout, interval).Should(Succeed())

				// Delete the autoscaler
				Expect(k8sClient.Delete(ctx, autoscaler)).To(Succeed())

				// Wait for deployment to be restored to original template
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      deploymentName,
						Namespace: namespace,
					}, deployment)
					if err != nil {
						return false
					}
					if len(deployment.Spec.Template.Spec.ResourceClaims) == 0 {
						return false
					}
					claim := deployment.Spec.Template.Spec.ResourceClaims[0]
					return claim.ResourceClaimTemplateName != nil &&
						*claim.ResourceClaimTemplateName == templateName
				}, timeout, interval).Should(BeTrue())

				// Verify deployment is using original template
				Expect(deployment.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal(&templateName))
			})

			It("should revert replicas to minReplicas and restore template in single update", func() {
				// Create original ResourceClaimTemplate
				originalTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      templateName,
						Namespace: namespace,
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           1,
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, originalTemplate)).To(Succeed())

				// Create revised template (simulating scaling action)
				revisedTemplateName := templateName + "-rev-2"
				revisedTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      revisedTemplateName,
						Namespace: namespace,
						Labels: map[string]string{
							"autoscaling.x-llmd.ai/managed-by":        "resourceclaimautoscaler",
							"autoscaling.x-llmd.ai/original-template": templateName,
						},
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           4, // Scaled up
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, revisedTemplate)).To(Succeed())

				// Create Deployment with scaled replicas and revised template
				scaledReplicas := int32(5)
				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      deploymentName,
						Namespace: namespace,
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: &scaledReplicas, // Scaled up
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test-cleanup"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"app": "test-cleanup"},
							},
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{
									{
										Name:                      "gpu-claim",
										ResourceClaimTemplateName: &revisedTemplateName,
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "test",
										Image: "nginx:latest",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

				// Create supporting resources
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-cleanup-both",
						Namespace: namespace,
						Labels:    map[string]string{"app": "test-cleanup"},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
								Name:       deploymentName,
								UID:        deployment.UID,
							},
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "test", Image: "nginx:latest"},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						PodIP: "10.0.0.3",
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				service := &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName,
						Namespace: namespace,
					},
					Spec: corev1.ServiceSpec{
						Selector: map[string]string{"app": "test-cleanup"},
						Ports:    []corev1.ServicePort{{Port: 80}},
					},
				}
				Expect(k8sClient.Create(ctx, service)).To(Succeed())

				endpointSlice := &discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName + "-cleanup",
						Namespace: namespace,
						Labels: map[string]string{
							discoveryv1.LabelServiceName: serviceName,
						},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints: []discoveryv1.Endpoint{
						{
							Addresses: []string{"10.0.0.3"},
							TargetRef: &corev1.ObjectReference{
								Kind:      "Pod",
								Name:      pod.Name,
								Namespace: namespace,
							},
						},
					},
					Ports: []discoveryv1.EndpointPort{{Port: ptr.To(int32(80))}},
				}
				Expect(k8sClient.Create(ctx, endpointSlice)).To(Succeed())

				// Create ResourceClaimAutoscaler with minReplicas constraint
				minReplicas := int32(2)
				autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:       autoscalerName,
						Namespace:  namespace,
						Finalizers: []string{FinalizerName},
					},
					Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
						Target: autoscalingv1alpha1.TargetSelector{
							ServiceRef: autoscalingv1alpha1.ServiceReference{
								Name: serviceName,
							},
							ResourceRef: autoscalingv1alpha1.ResourceReference{
								DeviceClassName: deviceClassName,
							},
						},
						Constraints: &autoscalingv1alpha1.Constraints{
							MinReplicas: &minReplicas,
						},
					},
				}
				Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

				// Update status to indicate revised template and discovery
				Eventually(func() error {
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      autoscalerName,
						Namespace: namespace,
					}, autoscaler); err != nil {
						return err
					}
					autoscaler.Status.Discovery = &autoscalingv1alpha1.DiscoveryResults{
						Owner: &metav1.OwnerReference{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							UID:        deployment.UID,
						},
						ResourceClaim: templateName,
					}
					autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
						DesiredClaimTemplate: revisedTemplateName,
					}
					return k8sClient.Status().Update(ctx, autoscaler)
				}, timeout, interval).Should(Succeed())

				// Verify deployment is scaled up before deletion
				Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      deploymentName,
					Namespace: namespace,
				}, deployment)).To(Succeed())
				Expect(*deployment.Spec.Replicas).To(Equal(scaledReplicas))
				Expect(*deployment.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal(revisedTemplateName))

				// Delete the autoscaler
				Expect(k8sClient.Delete(ctx, autoscaler)).To(Succeed())

				// Wait for deployment to be cleaned up (both replicas and template)
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      deploymentName,
						Namespace: namespace,
					}, deployment)
					if err != nil {
						return false
					}

					// Check replicas reverted to minReplicas
					if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != minReplicas {
						return false
					}

					// Check template restored to original
					if len(deployment.Spec.Template.Spec.ResourceClaims) == 0 {
						return false
					}
					claim := deployment.Spec.Template.Spec.ResourceClaims[0]
					return claim.ResourceClaimTemplateName != nil &&
						*claim.ResourceClaimTemplateName == templateName
				}, timeout, interval).Should(BeTrue())

				// Verify both changes were applied
				Expect(*deployment.Spec.Replicas).To(Equal(minReplicas), "Replicas should be reverted to minReplicas")
				Expect(*deployment.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal(templateName), "Template should be restored to original")
			})

			It("should handle StatefulSet cleanup with replicas and template", func() {
				// Create original ResourceClaimTemplate
				originalTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      templateName,
						Namespace: namespace,
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           1,
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, originalTemplate)).To(Succeed())

				// Create revised template
				revisedTemplateName := templateName + "-rev-sts"
				revisedTemplate := &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      revisedTemplateName,
						Namespace: namespace,
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: deviceClassName,
											Count:           2,
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, revisedTemplate)).To(Succeed())

				// Create StatefulSet with scaled replicas
				statefulSetName := "test-statefulset"
				scaledReplicas := int32(3)
				statefulSet := &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      statefulSetName,
						Namespace: namespace,
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: &scaledReplicas,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test-sts"},
						},
						ServiceName: serviceName,
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"app": "test-sts"},
							},
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{
									{
										Name:                      "gpu-claim",
										ResourceClaimTemplateName: &revisedTemplateName,
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "test",
										Image: "nginx:latest",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

				// Create supporting resources
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-sts",
						Namespace: namespace,
						Labels:    map[string]string{"app": "test-sts"},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       statefulSetName,
								UID:        statefulSet.UID,
							},
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "test", Image: "nginx:latest"},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						PodIP: "10.0.0.4",
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				service := &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName,
						Namespace: namespace,
					},
					Spec: corev1.ServiceSpec{
						Selector: map[string]string{"app": "test-sts"},
						Ports:    []corev1.ServicePort{{Port: 80}},
					},
				}
				Expect(k8sClient.Create(ctx, service)).To(Succeed())

				endpointSlice := &discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceName + "-sts",
						Namespace: namespace,
						Labels: map[string]string{
							discoveryv1.LabelServiceName: serviceName,
						},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints: []discoveryv1.Endpoint{
						{
							Addresses: []string{"10.0.0.4"},
							TargetRef: &corev1.ObjectReference{
								Kind:      "Pod",
								Name:      pod.Name,
								Namespace: namespace,
							},
						},
					},
					Ports: []discoveryv1.EndpointPort{{Port: ptr.To(int32(80))}},
				}
				Expect(k8sClient.Create(ctx, endpointSlice)).To(Succeed())

				// Create ResourceClaimAutoscaler
				minReplicas := int32(1)
				autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:       autoscalerName + "-sts",
						Namespace:  namespace,
						Finalizers: []string{FinalizerName},
					},
					Spec: autoscalingv1alpha1.ResourceClaimAutoscalerSpec{
						Target: autoscalingv1alpha1.TargetSelector{
							ServiceRef: autoscalingv1alpha1.ServiceReference{
								Name: serviceName,
							},
							ResourceRef: autoscalingv1alpha1.ResourceReference{
								DeviceClassName: deviceClassName,
							},
						},
						Constraints: &autoscalingv1alpha1.Constraints{
							MinReplicas: &minReplicas,
						},
					},
				}
				Expect(k8sClient.Create(ctx, autoscaler)).To(Succeed())

				// Update status
				Eventually(func() error {
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      autoscaler.Name,
						Namespace: namespace,
					}, autoscaler); err != nil {
						return err
					}
					autoscaler.Status.Discovery = &autoscalingv1alpha1.DiscoveryResults{
						Owner: &metav1.OwnerReference{
							APIVersion: "apps/v1",
							Kind:       "StatefulSet",
							Name:       statefulSetName,
							UID:        statefulSet.UID,
						},
						ResourceClaim: templateName,
					}
					autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{
						DesiredClaimTemplate: revisedTemplateName,
					}
					return k8sClient.Status().Update(ctx, autoscaler)
				}, timeout, interval).Should(Succeed())

				// Delete the autoscaler
				Expect(k8sClient.Delete(ctx, autoscaler)).To(Succeed())

				// Wait for StatefulSet to be cleaned up
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      statefulSetName,
						Namespace: namespace,
					}, statefulSet)
					if err != nil {
						return false
					}

					// Check replicas and template
					if statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != minReplicas {
						return false
					}
					if len(statefulSet.Spec.Template.Spec.ResourceClaims) == 0 {
						return false
					}
					claim := statefulSet.Spec.Template.Spec.ResourceClaims[0]
					return claim.ResourceClaimTemplateName != nil &&
						*claim.ResourceClaimTemplateName == templateName
				}, timeout, interval).Should(BeTrue())

				// Verify both changes
				Expect(*statefulSet.Spec.Replicas).To(Equal(minReplicas))
				Expect(*statefulSet.Spec.Template.Spec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal(templateName))
			})
		})
	})
})

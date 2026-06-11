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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

var _ = Describe("RCAControllerConfig Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-config"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}

		rcacontrollerconfig := &autoscalingv1alpha1.RCAControllerConfig{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind RCAControllerConfig")
			err := k8sClient.Get(ctx, typeNamespacedName, rcacontrollerconfig)
			if err != nil {
				resource := &autoscalingv1alpha1.RCAControllerConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: autoscalingv1alpha1.RCAControllerConfigSpec{
						Monitoring: autoscalingv1alpha1.Monitoring{
							Endpoint: "http://localhost:9090",
							CustomQueries: &autoscalingv1alpha1.CustomQueries{
								RPSQuery:                     "vllm:request_success_total",
								PromptTokenQuery:             "vllm:prompt_tokens_total",
								GenerationTokenQuery:         "vllm:generation_tokens_total",
								InterTokenLatencyQuery:       "vllm:inter_token_latency_seconds",
								TimeToFirstTokenLatencyQuery: "vllm:time_to_first_token_seconds",
								EndToEndLatencyBucketQuery:   "vllm:e2e_request_latency_seconds_bucket",
							},
						},
						Logging: &autoscalingv1alpha1.Logging{
							Level: autoscalingv1alpha1.LogLevelInfo,
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &autoscalingv1alpha1.RCAControllerConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance RCAControllerConfig")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &RCAControllerConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	// Note: Endpoint connectivity tests removed as the monitor now uses Prometheus API
	// which requires a real Prometheus server. The monitor package has its own tests.

	Context("When updating conditions", func() {
		var reconciler *RCAControllerConfigReconciler
		var config *autoscalingv1alpha1.RCAControllerConfig

		BeforeEach(func() {
			reconciler = &RCAControllerConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			config = &autoscalingv1alpha1.RCAControllerConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
			}
		})

		DescribeTable("should update MetricsAvailable condition based on metric health",
			func(metricHealths []autoscalingv1alpha1.MetricHealth, expectedStatus metav1.ConditionStatus, expectedReason string, expectedMessageSubstrings []string) {
				reconciler.updateConditions(config, metricHealths)

				Expect(config.Status.Conditions).To(HaveLen(1))
				condition := config.Status.Conditions[0]
				Expect(condition.Type).To(Equal("MetricsAvailable"))
				Expect(condition.Status).To(Equal(expectedStatus))
				Expect(condition.Reason).To(Equal(expectedReason))
				for _, substring := range expectedMessageSubstrings {
					Expect(condition.Message).To(ContainSubstring(substring))
				}
			},
			Entry("all metrics healthy",
				[]autoscalingv1alpha1.MetricHealth{
					{Name: "RPSQuery", Healthy: true},
					{Name: "PromptTokenQuery", Healthy: true},
					{Name: "GenerationTokenQuery", Healthy: true},
					{Name: "InterTokenLatencyQuery", Healthy: true},
					{Name: "TimeToFirstTokenLatencyQuery", Healthy: true},
					{Name: "EndToEndLatencyBucketQuery", Healthy: true},
				},
				metav1.ConditionTrue,
				"AllMetricsAvailable",
				[]string{},
			),
			Entry("some metrics unhealthy",
				[]autoscalingv1alpha1.MetricHealth{
					{Name: "RPSQuery", Healthy: true},
					{Name: "PromptTokenQuery", Healthy: false},
					{Name: "GenerationTokenQuery", Healthy: false},
				},
				metav1.ConditionFalse,
				"MetricsMissing",
				[]string{"PromptTokenQuery", "GenerationTokenQuery"},
			),
		)

		It("should update existing condition", func() {
			// Set initial condition
			config.Status.Conditions = []metav1.Condition{
				{
					Type:   "MetricsAvailable",
					Status: metav1.ConditionTrue,
					Reason: "AllMetricsAvailable",
				},
			}

			metricHealths := []autoscalingv1alpha1.MetricHealth{
				{Name: "RPSQuery", Healthy: false},
			}

			reconciler.updateConditions(config, metricHealths)

			Expect(config.Status.Conditions).To(HaveLen(1))
			condition := config.Status.Conditions[0]
			Expect(condition.Type).To(Equal("MetricsAvailable"))
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("MetricsMissing"))
		})
	})

	Context("When setting up logging", func() {
		var reconciler *RCAControllerConfigReconciler
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &RCAControllerConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		DescribeTable("should set log level correctly",
			func(loggingConfig *autoscalingv1alpha1.Logging, initialLogLevel autoscalingv1alpha1.LogLevel, expectedLogLevel autoscalingv1alpha1.LogLevel) {
				config := &autoscalingv1alpha1.RCAControllerConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-config",
					},
					Spec: autoscalingv1alpha1.RCAControllerConfigSpec{
						Monitoring: autoscalingv1alpha1.Monitoring{
							Endpoint: "http://localhost:8080",
						},
						Logging: loggingConfig,
					},
				}

				reconciler.currentLogLevel = initialLogLevel
				reconciler.setupLogging(ctx, config)
				Expect(reconciler.currentLogLevel).To(Equal(expectedLogLevel))
			},
			Entry("default log level when not specified",
				nil,
				autoscalingv1alpha1.LogLevel(""),
				autoscalingv1alpha1.LogLevelInfo,
			),
			Entry("custom log level when specified",
				&autoscalingv1alpha1.Logging{Level: autoscalingv1alpha1.LogLevelDebug},
				autoscalingv1alpha1.LogLevel(""),
				autoscalingv1alpha1.LogLevelDebug,
			),
			Entry("unchanged log level",
				&autoscalingv1alpha1.Logging{Level: autoscalingv1alpha1.LogLevelInfo},
				autoscalingv1alpha1.LogLevelInfo,
				autoscalingv1alpha1.LogLevelInfo,
			),
		)
	})
})

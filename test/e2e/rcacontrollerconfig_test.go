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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ibm/k8s-resourceclaim-autoscaler/test/utils"
)

var _ = Describe("RCAControllerConfig", Ordered, func() {
	const (
		configName         = "test-rcacontrollerconfig"
		mockPrometheusNs   = "monitoring"
		mockPrometheusName = "mock-prometheus"
		mockPrometheusPort = "9090"
	)

	var mockPrometheusEndpoint string

	BeforeAll(func() {
		mockPrometheusEndpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local:%s",
			mockPrometheusName, mockPrometheusNs, mockPrometheusPort)

		By("creating monitoring namespace for mock Prometheus")
		cmd := exec.Command("kubectl", "create", "namespace", mockPrometheusNs, "--dry-run=client", "-o", "yaml")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(output)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("cleaning up test RCAControllerConfig")
		cmd := exec.Command("kubectl", "delete", "rcacontrollerconfig", configName, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("cleaning up monitoring namespace")
		cmd = exec.Command("kubectl", "delete", "namespace", mockPrometheusNs, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	Context("Basic Configuration", func() {
		It("should create RCAControllerConfig with valid configuration", func() {
			By("creating a basic RCAControllerConfig")
			configYAML := fmt.Sprintf(`
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
		name: %s
spec:
		monitoring:
		  endpoint: "%s"
		  rateRangeSeconds: 60
		  latencyPercentile: 0.95
		logging:
		  level: INFO
`, configName, mockPrometheusEndpoint)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(configYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should create RCAControllerConfig successfully")

			By("verifying the RCAControllerConfig was created")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(configName))
			}).Should(Succeed())
		})

		It("should have correct spec fields", func() {
			By("checking monitoring endpoint configuration")
			cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
				"-o", "jsonpath={.spec.monitoring.endpoint}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(mockPrometheusEndpoint))

			By("checking rate range configuration")
			cmd = exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
				"-o", "jsonpath={.spec.monitoring.rateRangeSeconds}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("60"))

			By("checking latency percentile configuration")
			cmd = exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
				"-o", "jsonpath={.spec.monitoring.latencyPercentile}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("0.95"))

			By("checking logging level configuration")
			cmd = exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
				"-o", "jsonpath={.spec.logging.level}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("INFO"))
		})

		It("should populate status fields", func() {
			By("waiting for status to be updated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
					"-o", "jsonpath={.status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "Status should be populated")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})

	Context("Custom Queries Configuration", func() {
		It("should accept custom Prometheus queries", func() {
			By("updating config with custom queries")
			configYAML := fmt.Sprintf(`
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
  name: %s
spec:
  monitoring:
    endpoint: "%s"
    customQueries:
      rpsQuery: "custom_rps_metric"
      promptTokenQuery: "custom_prompt_tokens"
      generationTokenQuery: "custom_generation_tokens"
      interTokenLatencyQuery: "custom_inter_token_latency"
      timeToFirstTokenLatencyQuery: "custom_ttft_latency"
      endToEndLatencyBucketQuery: "custom_e2e_latency_bucket"
      deviceResourceMetrics:
        compute:
          usageQuery: "custom_gpu_usage"
          allocationQuery: "custom_gpu_allocation"
        memory:
          usageQuery: "custom_memory_usage"
          allocationQuery: "custom_memory_allocation"
    rateRangeSeconds: 60
    latencyPercentile: 0.95
  logging:
    level: DEBUG
`, configName, mockPrometheusEndpoint)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(configYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should update RCAControllerConfig with custom queries")

			By("verifying custom queries are stored")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
					"-o", "jsonpath={.spec.monitoring.customQueries.rpsQuery}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("custom_rps_metric"))
			}).Should(Succeed())

			By("verifying device resource metrics are stored")
			cmd = exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
				"-o", "jsonpath={.spec.monitoring.customQueries.deviceResourceMetrics.compute.usageQuery}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("custom_gpu_usage"))
		})
	})

	Context("Configuration Updates", func() {
		It("should handle endpoint updates", func() {
			newEndpoint := "http://new-prometheus.monitoring.svc.cluster.local:9090"

			By("updating the monitoring endpoint")
			cmd := exec.Command("kubectl", "patch", "rcacontrollerconfig", configName,
				"--type=merge",
				"-p", fmt.Sprintf(`{"spec":{"monitoring":{"endpoint":"%s"}}}`, newEndpoint))
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the endpoint was updated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
					"-o", "jsonpath={.spec.monitoring.endpoint}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(newEndpoint))
			}).Should(Succeed())
		})

		It("should handle logging level updates", func() {
			By("updating the logging level to ERROR")
			cmd := exec.Command("kubectl", "patch", "rcacontrollerconfig", configName,
				"--type=merge",
				"-p", `{"spec":{"logging":{"level":"ERROR"}}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the logging level was updated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
					"-o", "jsonpath={.spec.logging.level}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("ERROR"))
			}).Should(Succeed())
		})
	})

	Context("Validation and Error Handling", func() {
		It("should reject invalid logging levels", func() {
			By("attempting to create config with invalid logging level")
			invalidConfigYAML := fmt.Sprintf(`
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
		name: invalid-config
spec:
		monitoring:
		  endpoint: "%s"
		logging:
		  level: INVALID_LEVEL
`, mockPrometheusEndpoint)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(invalidConfigYAML)
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should reject invalid logging level")
		})

		It("should require monitoring endpoint", func() {
			By("attempting to create config without monitoring endpoint")
			invalidConfigYAML := `
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
		name: no-endpoint-config
spec:
		logging:
		  level: INFO
`

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(invalidConfigYAML)
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should require monitoring endpoint")
		})

		It("should validate latency percentile range", func() {
			By("attempting to create config with invalid percentile")
			invalidConfigYAML := fmt.Sprintf(`
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
		name: invalid-percentile-config
spec:
		monitoring:
		  endpoint: "%s"
		  latencyPercentile: 1.5
`, mockPrometheusEndpoint)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(invalidConfigYAML)
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should reject percentile > 1.0")
		})
	})

	Context("Status Reporting", func() {
		It("should report monitoring connection status", func() {
			By("checking for monitoring status in the config")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
					"-o", "jsonpath={.status.monitoringStatus}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Status should be populated (even if connection fails)
				g.Expect(output).NotTo(BeEmpty())
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should have conditions in status", func() {
			By("checking for status conditions")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", configName,
					"-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Conditions array should exist
				g.Expect(output).To(ContainSubstring("["))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})

	Context("Multiple Configs", func() {
		const secondConfigName = "test-rcacontrollerconfig-2"

		AfterAll(func() {
			By("cleaning up second config")
			cmd := exec.Command("kubectl", "delete", "rcacontrollerconfig", secondConfigName, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should support multiple RCAControllerConfig instances", func() {
			By("creating a second RCAControllerConfig")
			configYAML := fmt.Sprintf(`
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
		name: %s
spec:
		monitoring:
		  endpoint: "http://prometheus-2.monitoring.svc.cluster.local:9090"
		  rateRangeSeconds: 30
		  latencyPercentile: 0.99
		logging:
		  level: WARN
`, secondConfigName)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(configYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying both configs exist")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig",
					"-o", "jsonpath={.items[*].metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(configName))
				g.Expect(output).To(ContainSubstring(secondConfigName))
			}).Should(Succeed())

			By("verifying configs have different settings")
			cmd = exec.Command("kubectl", "get", "rcacontrollerconfig", secondConfigName,
				"-o", "jsonpath={.spec.monitoring.rateRangeSeconds}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("30"))
		})
	})

	Context("Deletion and Cleanup", func() {
		const tempConfigName = "temp-rcacontrollerconfig"

		It("should handle config deletion gracefully", func() {
			By("creating a temporary config")
			configYAML := fmt.Sprintf(`
apiVersion: autoscaling.x-llmd.ai/v1alpha1
kind: RCAControllerConfig
metadata:
		name: %s
spec:
		monitoring:
		  endpoint: "%s"
		logging:
		  level: INFO
`, tempConfigName, mockPrometheusEndpoint)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(configYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the config exists")
			cmd = exec.Command("kubectl", "get", "rcacontrollerconfig", tempConfigName)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("deleting the config")
			cmd = exec.Command("kubectl", "delete", "rcacontrollerconfig", tempConfigName)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the config is deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rcacontrollerconfig", tempConfigName)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "Config should be deleted")
			}).Should(Succeed())
		})
	})
})

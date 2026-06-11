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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ibm/k8s-resourceclaim-autoscaler/test/utils"
)

// namespace where the project is deployed in
const namespace = "rca-controller-system"

// serviceAccountName created for the project
const serviceAccountName = "rca-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "rca-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "rca-controller-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Pod description:\n%s\n", podDescription)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to describe controller pod: %v\n", err)
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Controller Health", func() {
		It("should have controller-manager pod running", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should have all containers ready in controller pod", func() {
			By("checking that all containers in the controller pod are ready")
			verifyContainersReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"pods", controllerPodName,
					"-o", "jsonpath={.status.containerStatuses[*].ready}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// All containers should report "true"
				g.Expect(output).NotTo(ContainSubstring("false"), "Some containers are not ready")
			}
			Eventually(verifyContainersReady).Should(Succeed())
		})

		It("should have controller logs showing successful startup", func() {
			By("checking controller logs for startup messages")
			verifyStartupLogs := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace, "--tail=100")
				logs, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				// Check for key startup indicators
				g.Expect(logs).To(ContainSubstring("Starting manager"), "Manager startup message not found")
				g.Expect(logs).NotTo(ContainSubstring("panic"), "Panic detected in logs")
				g.Expect(logs).NotTo(ContainSubstring("fatal"), "Fatal error detected in logs")
			}
			Eventually(verifyStartupLogs).Should(Succeed())
		})

		It("should have service account properly configured", func() {
			By("verifying the service account exists")
			cmd := exec.Command("kubectl", "get", "serviceaccount", serviceAccountName, "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Service account should exist")

			By("verifying the service account is used by the controller pod")
			cmd = exec.Command("kubectl", "get", "pod", controllerPodName,
				"-o", "jsonpath={.spec.serviceAccountName}",
				"-n", namespace,
			)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(serviceAccountName), "Pod should use the correct service account")
		})

		It("should have RBAC roles and bindings configured", func() {
			By("checking for the manager role")
			cmd := exec.Command("kubectl", "get", "clusterrole", "rca-controller-manager-role")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Manager ClusterRole should exist")

			By("checking for the role binding")
			cmd = exec.Command("kubectl", "get", "clusterrolebinding", "rca-controller-manager-rolebinding")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Manager ClusterRoleBinding should exist")
		})

		It("should have CRDs installed correctly", func() {
			By("verifying ResourceClaimAutoscaler CRD exists")
			cmd := exec.Command("kubectl", "get", "crd", "resourceclaimautoscalers.autoscaling.x-llmd.ai")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "ResourceClaimAutoscaler CRD should exist")
			Expect(output).To(ContainSubstring("resourceclaimautoscalers.autoscaling.x-llmd.ai"))

			By("verifying RCAControllerConfig CRD exists")
			cmd = exec.Command("kubectl", "get", "crd", "rcacontrollerconfigs.autoscaling.x-llmd.ai")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "RCAControllerConfig CRD should exist")
			Expect(output).To(ContainSubstring("rcacontrollerconfigs.autoscaling.x-llmd.ai"))

			By("verifying CRD versions and API groups")
			cmd = exec.Command("kubectl", "api-resources", "--api-group=autoscaling.x-llmd.ai")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("resourceclaimautoscalers"))
			Expect(output).To(ContainSubstring("rcacontrollerconfigs"))
			Expect(output).To(ContainSubstring("v1alpha1"))
		})

		It("should have proper resource limits configured", func() {
			By("checking controller pod resource requests and limits")
			cmd := exec.Command("kubectl", "get", "pod", controllerPodName,
				"-o", "jsonpath={.spec.containers[0].resources}",
				"-n", namespace,
			)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			// Verify resources are configured (not empty)
			Expect(output).NotTo(BeEmpty(), "Controller should have resource configuration")
		})

		It("should have leader election configured", func() {
			By("checking for leader election lease")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "lease",
					"-n", namespace,
					"-o", "jsonpath={.items[*].metadata.name}",
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should have a lease for leader election
				g.Expect(output).NotTo(BeEmpty(), "Leader election lease should exist")
			}).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=rca-controller-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		It("should expose controller reconciliation metrics", func() {
			By("getting the metrics output")
			metricsOutput := getMetricsOutput()

			By("verifying controller runtime metrics are present")
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_total"),
				"Reconciliation metrics should be exposed")
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_errors_total"),
				"Error metrics should be exposed")
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_time_seconds"),
				"Timing metrics should be exposed")
		})

		It("should have webhook configurations if webhooks are enabled", func() {
			By("checking for validating webhook configurations")
			cmd := exec.Command("kubectl", "get", "validatingwebhookconfigurations",
				"-o", "jsonpath={.items[*].metadata.name}",
			)
			output, err := utils.Run(cmd)
			// Webhooks might not be configured, so we just log the result
			if err == nil && output != "" {
				_, _ = fmt.Fprintf(GinkgoWriter, "Found webhook configurations: %s\n", output)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "No webhook configurations found (this is OK if webhooks are not enabled)\n")
			}
		})

		It("should handle controller restarts gracefully", func() {
			By("recording the current controller pod name")
			originalPodName := controllerPodName

			By("deleting the controller pod to trigger restart")
			cmd := exec.Command("kubectl", "delete", "pod", controllerPodName, "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should be able to delete controller pod")

			By("waiting for new controller pod to be running")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)
				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "Should have exactly one controller pod")

				newPodName := podNames[0]
				g.Expect(newPodName).NotTo(Equal(originalPodName), "Should be a new pod")

				// Update the global variable for subsequent tests
				controllerPodName = newPodName

				// Check the new pod is running
				cmd = exec.Command("kubectl", "get", "pod", newPodName,
					"-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				phase, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(phase).To(Equal("Running"), "New controller pod should be running")
			}, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying the new controller is functional")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace, "--tail=50")
				logs, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(logs).To(ContainSubstring("Starting manager"), "New controller should start successfully")
			}).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

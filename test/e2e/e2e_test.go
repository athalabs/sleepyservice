// Copyright 2026 AthaLabs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/athalabs/sleepyservice/test/utils"
)

// namespace where the project is deployed in
const namespace = "sleepyservice-system"

// serviceAccountName created for the project
const serviceAccountName = "sleepyservice-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "sleepyservice-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "sleepyservice-metrics-binding"

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

		By("deleting all SleepyService instances before undeploying controller")
		cmd = exec.Command("kubectl", "delete", "sleepyservice", "--all", "-n", "default", "--timeout=60s")
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
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", Ordered, func() {
		It("should run successfully", func() {
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

		It("should successfully create and reconcile a SleepyService", func() {
			By("waiting for SleepyService CRD to be ready")
			verifyCRDReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "crd", "sleepyservices.sleepy.atha.gr")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "SleepyService CRD should exist")
			}
			Eventually(verifyCRDReady, 30*time.Second).Should(Succeed())

			By("creating a sample deployment to manage")
			sampleDeploymentYAML := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sample-app
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: sample
  template:
    metadata:
      labels:
        app: sample
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: sample-app
  namespace: default
spec:
  selector:
    app: sample
  ports:
  - port: 80
    targetPort: 80
`
			sampleFile := filepath.Join("/tmp", "sample-deployment.yaml")
			err := os.WriteFile(sampleFile, []byte(sampleDeploymentYAML), os.FileMode(0o644))
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", sampleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating a SleepyService CR")
			sleepyServiceYAML := `
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: sample-hibernating-service
  namespace: default
spec:
  backendService:
    type: ClusterIP
    ports:
    - name: http
      port: 80
      protocol: TCP
      targetPort: 80
  wakeTimeout: 5m
  idleTimeout: 10m
  components:
  - name: app
    type: Deployment
    ref:
      name: sample-app
    replicas: 1
`
			hsFile := filepath.Join("/tmp", "hibernating-service.yaml")
			err = os.WriteFile(hsFile, []byte(sleepyServiceYAML), os.FileMode(0o644))
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", hsFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying that proxy deployment is created")
			verifyProxyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"sample-hibernating-service-wakeproxy",
					"-n", "default",
					"-o", "jsonpath={.status.readyReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "Proxy deployment should have 1 ready replica")
			}
			Eventually(verifyProxyDeployment, 2*time.Minute).Should(Succeed())

			By("verifying that proxy service is created")
			cmd = exec.Command("kubectl", "get", "service",
				"sample-hibernating-service",
				"-n", "default")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("checking that the SleepyService has the proxy deployment in status")
			verifyStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "sleepyservice",
					"sample-hibernating-service",
					"-n", "default",
					"-o", "jsonpath={.status.proxyDeployment}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("sample-hibernating-service-wakeproxy"))
			}
			Eventually(verifyStatus).Should(Succeed())

			By("verifying that proxy deployment uses the operator image")
			cmd = exec.Command("kubectl", "get", "deployment",
				"sample-hibernating-service-wakeproxy",
				"-n", "default",
				"-o", "jsonpath={.spec.template.spec.containers[0].image}")
			imageOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(imageOutput).To(Equal(projectImage), "Proxy should use operator image")

			By("verifying that proxy deployment has correct command")
			cmd = exec.Command("kubectl", "get", "deployment",
				"sample-hibernating-service-wakeproxy",
				"-n", "default",
				"-o", "jsonpath={.spec.template.spec.containers[0].command[0]}")
			commandOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(commandOutput).To(Equal("/proxy"), "Proxy should use /proxy command")

			By("cleaning up the test resources")
			cmd = exec.Command("kubectl", "delete", "-f", hsFile)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "-f", sampleFile)
			_, _ = utils.Run(cmd)
		})

		It("should hibernate and wake up a service correctly", func() {
			By("creating a test application deployment")
			testAppYAML := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-webapp
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: test-webapp
  template:
    metadata:
      labels:
        app: test-webapp
    spec:
      containers:
      - name: webapp
        image: kennethreitz/httpbin:latest
        ports:
        - containerPort: 80
        readinessProbe:
          httpGet:
            path: /status/200
            port: 80
          initialDelaySeconds: 5
          periodSeconds: 5
`
			testAppFile := filepath.Join("/tmp", "test-webapp.yaml")
			err := os.WriteFile(testAppFile, []byte(testAppYAML), os.FileMode(0o644))
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", testAppFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for test app to be ready")
			verifyAppReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "test-webapp",
					"-n", "default", "-o", "jsonpath={.status.readyReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "Test app should have 1 ready replica")
			}
			Eventually(verifyAppReady, 2*time.Minute).Should(Succeed())

			By("creating a SleepyService with short idle timeout")
			hsYAML := `
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: test-hibernating-webapp
  namespace: default
spec:
  wakeTimeout: 3m
  idleTimeout: 10s
  backendService:
    ports:
    - port: 80
      targetPort: 80
  components:
  - name: webapp
    type: Deployment
    ref:
      name: test-webapp
    replicas: 1
`
			hsFile := filepath.Join("/tmp", "test-hibernating-webapp.yaml")
			err = os.WriteFile(hsFile, []byte(hsYAML), os.FileMode(0o644))
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", hsFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for proxy to be deployed")
			verifyProxyReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"test-hibernating-webapp-wakeproxy",
					"-n", "default",
					"-o", "jsonpath={.status.readyReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "Proxy should have 1 ready replica")
			}
			Eventually(verifyProxyReady, 2*time.Minute).Should(Succeed())

			By("waiting idleTimeout (10s) + buffer (5s) for idle timeout to cause hibernation")
			time.Sleep(15 * time.Second)

			By("verifying the app has hibernated (scaled to 0)")
			verifyHibernated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "test-webapp",
					"-n", "default", "-o", "jsonpath={.spec.replicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("0"), "App should be scaled to 0 after idle timeout")
			}
			Eventually(verifyHibernated, 30*time.Second).Should(Succeed())

			By("sending a request through the proxy to trigger wake-up")
			// Create a test pod to curl the proxy service
			curlPodName := fmt.Sprintf("curl-wakeup-test-%d", time.Now().Unix())
			cmd = exec.Command("kubectl", "run", curlPodName,
				"--image=curlimages/curl:latest",
				"--restart=Never",
				"-n", "default",
				"--command", "--", "sleep", "300")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for curl pod to be ready")
			verifyCurlPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", curlPodName,
					"-n", "default", "-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Curl pod should be running")
			}
			Eventually(verifyCurlPodReady, time.Minute).Should(Succeed())

			By("making a request through the proxy (should trigger wake-up)")
			cmd = exec.Command("kubectl", "exec", curlPodName, "-n", "default", "--",
				"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
				"http://test-hibernating-webapp/status/200")
			_, _ = utils.Run(cmd) // May fail initially as app is waking up

			By("verifying the app wakes up (scales back to 1)")
			verifyWokenUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "test-webapp",
					"-n", "default", "-o", "jsonpath={.status.readyReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "App should scale back to 1 after wake-up")
			}
			Eventually(verifyWokenUp, 3*time.Minute).Should(Succeed())

			By("verifying requests succeed after wake-up")
			verifyRequestSucceeds := func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", curlPodName, "-n", "default", "--",
					"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
					"http://test-hibernating-webapp/status/200")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("200"), "Request should return 200 after wake-up")
			}
			Eventually(verifyRequestSucceeds, time.Minute).Should(Succeed())

			By("waiting for the service to hibernate again (10s idle + 5s buffer)")
			time.Sleep(15 * time.Second)

			By("verifying the app has hibernated again")
			Eventually(verifyHibernated, 30*time.Second).Should(Succeed())

			By("testing interactive mode with browser-like request")
			cmd = exec.Command("kubectl", "exec", curlPodName, "-n", "default", "--",
				"curl", "-s", "-H", "Accept: text/html",
				"http://test-hibernating-webapp/status/200")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the waiting page HTML is returned")
			Expect(output).To(ContainSubstring("<!DOCTYPE html>"), "Should return HTML waiting page")
			Expect(output).To(ContainSubstring("Waking up"), "Should contain waking message")
			Expect(output).To(ContainSubstring("test-webapp"), "Should show service name")
			Expect(output).To(ContainSubstring("/_wake/events"), "Should include SSE endpoint")
			Expect(output).To(ContainSubstring("EventSource"), "Should have SSE client code")

			By("verifying SSE events endpoint streams state updates")
			cmd = exec.Command("kubectl", "exec", curlPodName, "-n", "default", "--",
				"timeout", "30", "curl", "-s", "-N", "-H", "Accept: text/event-stream",
				"http://test-hibernating-webapp/_wake/events")
			sseOutput, err := utils.Run(cmd)
			// Note: timeout command will return non-zero when it times out after 30s, which is expected
			_ = err

			By("verifying SSE events contain state and progress updates")
			Expect(sseOutput).To(ContainSubstring("data:"), "Should contain SSE data events")
			Expect(sseOutput).To(MatchRegexp(`"state":\s*"(sleeping|waking|awake)"`), "Should contain state field")
			Expect(sseOutput).To(ContainSubstring(`"message"`), "Should contain message field")
			Expect(sseOutput).To(ContainSubstring(`"progress"`), "Should contain progress field")

			By("waiting for the app to wake up after the interactive request")
			Eventually(verifyWokenUp, 3*time.Minute).Should(Succeed())

			By("verifying status endpoint returns correct JSON state")
			cmd = exec.Command("kubectl", "exec", curlPodName, "-n", "default", "--",
				"curl", "-s", "http://test-hibernating-webapp/_wake/status")
			statusOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(statusOutput).To(ContainSubstring(`"state":"awake"`), "Status should show awake state")
			Expect(statusOutput).To(ContainSubstring(`"progress":100`), "Progress should be 100")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "pod", curlPodName, "-n", "default", "--force", "--grace-period=0")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "-f", hsFile)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "-f", testAppFile)
			_, _ = utils.Run(cmd)
		})

		It("should verify metrics for SleepyService reconciliation", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=sleepyservice-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			// Ignore error if already exists
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")
			}

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics-hs", "--restart=Never",
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
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics-hs pod")

			By("waiting for the curl-metrics-hs pod to complete")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics-hs",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics-hs logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics-hs", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl-metrics-hs pod")
			Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_total"))

			By("cleaning up the curl-metrics-hs pod")
			cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics-hs", "-n", namespace)
			_, _ = utils.Run(cmd)
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=sleepyservice-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			// Ignore error if already exists from previous test
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")
			}

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

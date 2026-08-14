package prometheus_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/yourusername/prometheus-vpa-recommender/pkg/prometheus"
)

var _ = Describe("Prometheus Client", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	})

	AfterEach(func() {
		cancel()
	})

	Describe("NewClient", func() {
		DescribeTable("creating client with different URLs",
			func(prometheusURL string, expectError bool) {
				client, err := prometheus.NewClient(prometheusURL, "desired_capacity", false)

				if expectError {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(client).NotTo(BeNil())
				}
			},
			Entry("valid URL", "http://localhost:9090", false),
			Entry("valid URL with path", "http://prometheus.default.svc:9090", false),
			Entry("empty URL", "", false),
		)
	})

	Describe("QueryDesiredCapacity", func() {
		DescribeTable("querying desired capacity",
			func(vpaName, containerName, deviceClassName, capacity, responseBody string, expectedValue float64, expectError bool) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(responseBody))
				}))
				defer server.Close()

				client, err := prometheus.NewClient(server.URL, "wva_desired_capacity_per_device", false)
				Expect(err).NotTo(HaveOccurred())

				value, err := client.QueryDesiredCapacity(ctx, vpaName, containerName, deviceClassName, capacity)

				if expectError {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(value).To(Equal(expectedValue))
				}
			},
			Entry("GPU compute capacity",
				"sample-deployment-hpa", "vllm-container", "gpu.example.com", "compute",
				`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"variant_name":"sample-deployment-hpa","target_container":"vllm-container","accelerator_type":"gpu.example.com","capacity":"compute"},"value":[1234567890,"60"]}]}}`,
				float64(60),
				false,
			),
			Entry("GPU memory capacity",
				"sample-deployment-hpa", "vllm-container", "gpu.example.com", "memory",
				`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"variant_name":"sample-deployment-hpa","target_container":"vllm-container","accelerator_type":"gpu.example.com","capacity":"memory"},"value":[1234567890,"8589934592"]}]}}`,
				float64(8589934592),
				false,
			),
			Entry("zero capacity",
				"sample-deployment-hpa", "vllm-container", "gpu.example.com", "compute",
				`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"variant_name":"sample-deployment-hpa","target_container":"vllm-container","accelerator_type":"gpu.example.com","capacity":"compute"},"value":[1234567890,"0"]}]}}`,
				float64(0),
				false,
			),
			Entry("no results found",
				"sample-deployment-hpa", "vllm-container", "gpu.example.com", "compute",
				`{"status":"success","data":{"resultType":"vector","result":[]}}`,
				float64(0),
				true,
			),
			Entry("Intel GPU compute capacity",
				"my-vpa", "inference-container", "gpu.intel.com", "compute",
				`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"variant_name":"my-vpa","target_container":"inference-container","accelerator_type":"gpu.intel.com","capacity":"compute"},"value":[1234567890,"120"]}]}}`,
				float64(120),
				false,
			),
		)
	})
})

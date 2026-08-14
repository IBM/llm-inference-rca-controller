package recommender

import (
	"context"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/yourusername/prometheus-vpa-recommender/pkg/prometheus"
)

// TestHelper provides access to internal methods for testing
type TestHelper struct {
	recommender *Recommender
}

// NewTestHelper creates a test helper with a mock Prometheus client.
// Pass a fake kubernetes.Interface (e.g. k8s.io/client-go/kubernetes/fake.NewSimpleClientset)
// to exercise the Deployment-lookup fallback in getContainerNames.
// Pass nil when the VPA under test includes explicit ContainerPolicies.
func NewTestHelper(promClient prometheus.Client, kubeClient kubernetes.Interface) *TestHelper {
	return &TestHelper{
		recommender: &Recommender{
			prometheusClient: promClient,
			kubeClient:       kubeClient,
		},
	}
}

// BuildContainerRecommendations exposes the internal method for testing
func (h *TestHelper) BuildContainerRecommendations(ctx context.Context, targetName string, vpa *vpa_types.VerticalPodAutoscaler) ([]vpa_types.RecommendedContainerResources, error) {
	return h.recommender.buildContainerRecommendations(ctx, targetName, vpa)
}

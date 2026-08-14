package recommender

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"

	"github.com/yourusername/prometheus-vpa-recommender/pkg/config"
	"github.com/yourusername/prometheus-vpa-recommender/pkg/prometheus"
	"github.com/yourusername/prometheus-vpa-recommender/pkg/vpa"
)

// Recommender is the main recommender that watches VPAs and updates their recommendations
type Recommender struct {
	config           *config.Config
	kubeClient       kubernetes.Interface
	vpaClient        vpa_clientset.Interface
	prometheusClient prometheus.Client
	vpaUpdater       *vpa.Updater
}

// New creates a new Recommender instance
func New(cfg *config.Config) (*Recommender, error) {
	// Create Kubernetes config
	kubeConfig, err := buildKubeConfig(cfg.KubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kube config: %w", err)
	}

	// Create core Kubernetes client (used to look up Deployment container names)
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %w", err)
	}

	// Create VPA client
	vpaClient, err := vpa_clientset.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPA client: %w", err)
	}

	// Create Prometheus client
	promClient, err := prometheus.NewClient(cfg.PrometheusURL, cfg.CapacityMetricName, cfg.PrometheusInsecureSkipVerify)
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %w", err)
	}

	// Create VPA updater
	vpaUpdater := vpa.NewUpdater(vpaClient)

	return &Recommender{
		config:           cfg,
		kubeClient:       kubeClient,
		vpaClient:        vpaClient,
		prometheusClient: promClient,
		vpaUpdater:       vpaUpdater,
	}, nil
}

// Run starts the recommender main loop
func (r *Recommender) Run(ctx context.Context) error {
	klog.InfoS("Starting recommender loop", "interval", r.config.UpdateInterval)

	ticker := time.NewTicker(r.config.UpdateInterval)
	defer ticker.Stop()

	// Run once immediately
	if err := r.updateRecommendations(ctx); err != nil {
		klog.ErrorS(err, "Failed to update recommendations on startup")
	}

	// Then run periodically
	for {
		select {
		case <-ctx.Done():
			klog.InfoS("Context cancelled, stopping recommender")
			return nil
		case <-ticker.C:
			if err := r.updateRecommendations(ctx); err != nil {
				klog.ErrorS(err, "Failed to update recommendations")
			}
		}
	}
}

// updateRecommendations fetches all VPAs and updates their recommendations
func (r *Recommender) updateRecommendations(ctx context.Context) error {
	klog.V(2).InfoS("Starting recommendation update cycle")

	// List all VPAs (use metav1.NamespaceAll for all namespaces)
	namespace := r.config.Namespace
	if namespace == "" {
		namespace = metav1.NamespaceAll
	}

	vpas, err := r.vpaClient.AutoscalingV1().VerticalPodAutoscalers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list VPAs: %w", err)
	}

	klog.V(2).InfoS("Found VPAs", "count", len(vpas.Items))

	// Process each VPA
	processedCount := 0
	for i := range vpas.Items {
		vpa := &vpas.Items[i]

		// Filter: only process VPAs for this recommender
		if !r.shouldProcess(vpa) {
			klog.V(4).InfoS("Skipping VPA (not for this recommender)",
				"vpa", klog.KObj(vpa),
				"recommenders", vpa.Spec.Recommenders,
			)
			continue
		}

		klog.V(3).InfoS("Processing VPA", "vpa", klog.KObj(vpa))

		// Update recommendation for this VPA
		if err := r.updateVPARecommendation(ctx, vpa); err != nil {
			klog.ErrorS(err, "Failed to update VPA recommendation", "vpa", klog.KObj(vpa))
			continue
		}

		processedCount++
	}

	klog.InfoS("Completed recommendation update cycle",
		"totalVPAs", len(vpas.Items),
		"processed", processedCount,
	)

	return nil
}

// shouldProcess checks if this VPA should be processed by this recommender
func (r *Recommender) shouldProcess(vpa *vpa_types.VerticalPodAutoscaler) bool {
	// If no recommenders specified, don't process (let default recommender handle it)
	if len(vpa.Spec.Recommenders) == 0 {
		return false
	}

	// Check if our recommender name is in the list
	for _, rec := range vpa.Spec.Recommenders {
		if rec.Name == r.config.RecommenderName {
			return true
		}
	}

	return false
}

// updateVPARecommendation queries Prometheus and updates a single VPA's recommendation
func (r *Recommender) updateVPARecommendation(ctx context.Context, vpa *vpa_types.VerticalPodAutoscaler) error {
	// Get target deployment/statefulset name
	targetName := vpa.Spec.TargetRef.Name

	klog.V(4).InfoS("Querying Prometheus for recommendations",
		"vpa", klog.KObj(vpa),
		"target", targetName,
	)

	// TODO(extension): querying desired replica count from Prometheus and surfacing it
	// as a scaling signal (e.g. to drive HPA or KEDA) is a natural future extension.

	// Build container recommendations by querying capacity metrics
	containerRecommendations, err := r.buildContainerRecommendations(ctx, targetName, vpa)
	if err != nil {
		// Update VPA condition to reflect the failure before returning
		if condErr := r.vpaUpdater.SetConditionFailed(ctx, vpa, "BuildFailed", err.Error()); condErr != nil {
			klog.ErrorS(condErr, "Failed to set VPA condition", "vpa", klog.KObj(vpa))
		}
		return fmt.Errorf("failed to build container recommendations: %w", err)
	}

	// Create recommendation
	recommendation := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: containerRecommendations,
	}

	// Update VPA status
	if err := r.vpaUpdater.UpdateRecommendation(ctx, vpa, recommendation); err != nil {
		return fmt.Errorf("failed to update VPA status: %w", err)
	}

	klog.V(3).InfoS("Successfully updated VPA recommendation", "vpa", klog.KObj(vpa))
	return nil
}

// buildContainerRecommendations builds container recommendations from Prometheus metrics
func (r *Recommender) buildContainerRecommendations(ctx context.Context, targetName string, vpa *vpa_types.VerticalPodAutoscaler) ([]vpa_types.RecommendedContainerResources, error) {
	var recommendations []vpa_types.RecommendedContainerResources

	// Get resource claim policies from VPA spec
	if vpa.Spec.ResourcePolicy == nil || len(vpa.Spec.ResourcePolicy.ResourceClaimPolicies) == 0 {
		return nil, fmt.Errorf("no resource claim policies defined in VPA spec")
	}

	// Get container names from VPA policy or from the Deployment spec.
	containerNames, err := r.getContainerNames(ctx, vpa)
	if err != nil {
		return nil, fmt.Errorf("failed to determine container names: %w", err)
	}

	// vpaName is used as the variant_name label in the Prometheus query.
	vpaName := vpa.Name

	for _, containerName := range containerNames {
		// Build resource lists for this container
		target := make(corev1.ResourceList)

		// Query each resource claim policy
		hasAnyCapacity := false
		for _, policy := range vpa.Spec.ResourcePolicy.ResourceClaimPolicies {
			deviceClassName := policy.DeviceClassName

			// Query each controlled capacity for this device class.
			// The resource name stored in the VPA recommendation uses the
			// compound "deviceClassName/capacity" form.
			for _, capacity := range policy.ControlledCapacities {
				capacityStr := string(capacity)
				resourceName := corev1.ResourceName(fmt.Sprintf("%s/%s", deviceClassName, capacityStr))

				value, err := r.prometheusClient.QueryDesiredCapacity(ctx, vpaName, containerName, deviceClassName, capacityStr)
				if err != nil {
					klog.V(3).InfoS("Failed to query capacity",
						"vpa", klog.KObj(vpa),
						"container", containerName,
						"deviceClass", deviceClassName,
						"capacity", capacityStr,
						"error", err,
					)
					continue
				}

				hasAnyCapacity = true

				// Use DecimalSI format for all capacities (GPU compute, memory, etc.)
				quantity := resource.NewQuantity(int64(value), resource.DecimalSI)
				target[resourceName] = *quantity

				klog.V(4).InfoS("Queried capacity",
					"container", containerName,
					"deviceClass", deviceClassName,
					"capacity", capacityStr,
					"resourceName", resourceName,
					"value", quantity.String(),
				)
			}
		}

		// Skip this container if no capacities were successfully queried
		if !hasAnyCapacity {
			klog.V(3).InfoS("No capacities found for container, skipping",
				"target", targetName,
				"container", containerName,
			)
			continue
		}

		recommendation := vpa_types.RecommendedContainerResources{
			ContainerName: containerName,
			Target:        target,
			// LowerBound and UpperBound are intentionally omitted; the VPA updater
			// applies its own default 10% threshold to decide when to trigger updates.
		}

		recommendations = append(recommendations, recommendation)

		klog.V(4).InfoS("Built container recommendation",
			"container", containerName,
			"capacities", len(target),
		)
	}

	if len(recommendations) == 0 {
		return nil, fmt.Errorf("no container recommendations could be built")
	}

	return recommendations, nil
}

// getContainerNames returns the names of containers in the target Deployment
// that reference at least one of the claim templates listed in the VPA's
// resourceClaimPolicies.
//
// It prefers explicit ContainerPolicies when present (existing behaviour), and
// falls back to inspecting the Deployment pod spec:
//
//  1. Build the set of claimTemplateNames from the VPA resourceClaimPolicies.
//  2. Map each pod-level resourceClaim (claimName → templateName) to that set.
//  3. Include only containers whose resources.claims reference a matching claimName.
func (r *Recommender) getContainerNames(ctx context.Context, vpa *vpa_types.VerticalPodAutoscaler) ([]string, error) {
	// Explicit container policies take priority.
	if vpa.Spec.ResourcePolicy != nil && len(vpa.Spec.ResourcePolicy.ContainerPolicies) > 0 {
		names := make([]string, 0, len(vpa.Spec.ResourcePolicy.ContainerPolicies))
		for _, policy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
			names = append(names, policy.ContainerName)
		}
		return names, nil
	}

	// Build set of claim template names referenced by the VPA policies.
	claimTemplateNames := make(map[string]bool)
	for _, policy := range vpa.Spec.ResourcePolicy.ResourceClaimPolicies {
		claimTemplateNames[policy.ClaimTemplateName] = true
	}

	// Fetch the target Deployment.
	targetName := vpa.Spec.TargetRef.Name
	namespace := vpa.Namespace
	deployment, err := r.kubeClient.AppsV1().Deployments(namespace).Get(ctx, targetName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s/%s: %w", namespace, targetName, err)
	}

	// Map pod-level claim name → template name.
	claimNameToTemplate := make(map[string]string)
	for _, rc := range deployment.Spec.Template.Spec.ResourceClaims {
		if rc.ResourceClaimTemplateName != nil {
			claimNameToTemplate[rc.Name] = *rc.ResourceClaimTemplateName
		}
	}

	// Collect containers that reference at least one matching claim.
	var names []string
	for _, c := range deployment.Spec.Template.Spec.Containers {
		for _, claim := range c.Resources.Claims {
			if claimTemplateNames[claimNameToTemplate[claim.Name]] {
				names = append(names, c.Name)
				break
			}
		}
	}

	if len(names) == 0 {
		return nil, fmt.Errorf("no containers in deployment %s/%s reference a claim template managed by this VPA", namespace, targetName)
	}
	return names, nil
}

// getControlledCapacities extracts controlled capacities from VPA spec
func (r *Recommender) getControlledCapacities(vpa *vpa_types.VerticalPodAutoscaler) ([]string, error) {
	// Check if VPA spec has ResourceClaimPolicies with ControlledCapacities
	if vpa.Spec.ResourcePolicy != nil && len(vpa.Spec.ResourcePolicy.ResourceClaimPolicies) > 0 {
		// Collect all unique capacity names from all resource claim policies
		capacitySet := make(map[string]bool)
		for _, policy := range vpa.Spec.ResourcePolicy.ResourceClaimPolicies {
			for _, capacity := range policy.ControlledCapacities {
				capacitySet[string(capacity)] = true
			}
		}

		// Convert set to slice
		capacities := make([]string, 0, len(capacitySet))
		for capacity := range capacitySet {
			capacities = append(capacities, capacity)
		}

		if len(capacities) > 0 {
			return capacities, nil
		}
	}

	// Return error if no controlled capacities found
	return nil, fmt.Errorf("no controlled capacities defined in VPA spec")
}

// buildKubeConfig creates a Kubernetes REST config
func buildKubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		// Use kubeconfig file
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	// Use in-cluster config
	return rest.InClusterConfig()
}

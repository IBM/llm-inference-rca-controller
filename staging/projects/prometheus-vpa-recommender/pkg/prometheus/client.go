package prometheus

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/klog/v2"
)

// Client is an interface for querying Prometheus
type Client interface {
	// QueryDesiredCapacity queries Prometheus for the desired capacity value.
	// vpaName maps to the variant_name label, containerName to target_container,
	// deviceClassName to accelerator_type, and capacity to the capacity label.
	QueryDesiredCapacity(ctx context.Context, vpaName, containerName, deviceClassName, capacity string) (float64, error)

	// TODO(extension): add QueryDesiredReplica(ctx, deployment) (int32, error) to surface
	// desired replica count as a scaling signal (e.g. for HPA or KEDA integration).
}

// CapacityRecommendation represents a capacity recommendation
type CapacityRecommendation struct {
	CapacityName string
	Value        float64
}

type client struct {
	api                promv1.API
	capacityMetricName string
}

// NewClient creates a new Prometheus client.
// Set insecureSkipVerify=true to disable TLS certificate verification (e.g. for
// Prometheus instances that use a self-signed certificate).
func NewClient(prometheusURL, capacityMetricName string, insecureSkipVerify bool) (Client, error) {
	cfg := promapi.Config{
		Address: prometheusURL,
	}
	if insecureSkipVerify {
		cfg.RoundTripper = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // intentional, user-controlled flag
		}
		klog.V(2).InfoS("TLS certificate verification disabled for Prometheus")
	}
	promClient, err := promapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %w", err)
	}

	return &client{
		api:                promv1.NewAPI(promClient),
		capacityMetricName: capacityMetricName,
	}, nil
}

// QueryDesiredCapacity queries Prometheus for the desired capacity value.
// It matches the wva_desired_capacity_per_device metric label schema:
//
//	variant_name     → VPA name
//	target_container → application container name
//	accelerator_type → device class name
//	capacity         → raw capacity name
func (c *client) QueryDesiredCapacity(ctx context.Context, vpaName, containerName, deviceClassName, capacity string) (float64, error) {
	query := fmt.Sprintf(`%s{variant_name="%s", target_container="%s", accelerator_type="%s", capacity="%s"}`,
		c.capacityMetricName, vpaName, containerName, deviceClassName, capacity)

	klog.V(5).InfoS("Querying Prometheus", "query", query)

	result, warnings, err := c.api.Query(ctx, query, time.Now())
	if err != nil {
		return 0, fmt.Errorf("failed to query Prometheus: %w", err)
	}
	if len(warnings) > 0 {
		fmt.Printf("Prometheus query warnings: %v\n", warnings)
	}

	// Parse result and extract capacity value
	vector, ok := result.(model.Vector)
	if !ok {
		return 0, fmt.Errorf("unexpected result type: %T", result)
	}

	if len(vector) == 0 {
		return 0, fmt.Errorf("no metrics found for variant_name: %s, target_container: %s, accelerator_type: %s, capacity: %s",
			vpaName, containerName, deviceClassName, capacity)
	}

	// Get the first sample value
	sample := vector[0]
	capacityValue := float64(sample.Value)

	return capacityValue, nil
}

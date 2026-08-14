package config

import (
	"flag"
	"time"
)

// Config holds the configuration for the Prometheus VPA Recommender
type Config struct {
	// RecommenderName is the name of this recommender instance
	RecommenderName string

	// PrometheusURL is the URL of the Prometheus server
	PrometheusURL string

	// UpdateInterval is how often to update VPA recommendations
	UpdateInterval time.Duration

	// KubeConfig is the path to the kubeconfig file (for local development)
	KubeConfig string

	// Namespace limits the recommender to a specific namespace (empty = all namespaces)
	Namespace string

	// MetricsPort is the port to expose Prometheus metrics
	MetricsPort int

	// CapacityMetricName is the Prometheus metric name for desired capacity values
	CapacityMetricName string

	// PrometheusInsecureSkipVerify disables TLS certificate verification when
	// connecting to a Prometheus server that uses a self-signed certificate.
	PrometheusInsecureSkipVerify bool

	// TODO(extension): ReplicaMetricName string — Prometheus metric name for desired
	// replica count, to be used when QueryDesiredReplica is added to the Client interface.
}

// NewConfig creates a new Config with default values
func NewConfig() *Config {
	return &Config{
		RecommenderName:              "prometheus",
		PrometheusURL:                "http://prometheus.default.svc.cluster.local:9090",
		UpdateInterval:               30 * time.Second,
		KubeConfig:                   "",
		Namespace:                    "",
		MetricsPort:                  8080,
		CapacityMetricName:           "desired_capacity",
		PrometheusInsecureSkipVerify: false,
	}
}

// AddFlags adds configuration flags to the provided FlagSet
func (c *Config) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.RecommenderName, "recommender-name", c.RecommenderName,
		"Name of this recommender. VPAs must specify this name in spec.recommenders to use this recommender.")

	fs.StringVar(&c.PrometheusURL, "prometheus-url", c.PrometheusURL,
		"URL of the Prometheus server to query for desired resource metrics.")

	fs.DurationVar(&c.UpdateInterval, "update-interval", c.UpdateInterval,
		"How often to query Prometheus and update VPA recommendations.")

	fs.StringVar(&c.KubeConfig, "kubeconfig", c.KubeConfig,
		"Path to kubeconfig file. Leave empty to use in-cluster configuration.")

	fs.StringVar(&c.Namespace, "namespace", c.Namespace,
		"Namespace to watch for VPAs. Empty means all namespaces.")

	fs.IntVar(&c.MetricsPort, "metrics-port", c.MetricsPort,
		"Port to expose Prometheus metrics on.")

	fs.StringVar(&c.CapacityMetricName, "capacity-metric-name", c.CapacityMetricName,
		"Prometheus metric name for desired capacity values query.")

	fs.BoolVar(&c.PrometheusInsecureSkipVerify, "prometheus-insecure-skip-verify", c.PrometheusInsecureSkipVerify,
		"Skip TLS certificate verification when connecting to Prometheus. Use when Prometheus uses a self-signed certificate.")
}

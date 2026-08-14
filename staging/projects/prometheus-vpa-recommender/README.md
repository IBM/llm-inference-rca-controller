# Prometheus VPA Recommender

A Kubernetes VPA recommender that drives GPU device-capacity recommendations
directly from Prometheus metrics.
This project is designed to integrate with the [Workload Variant Autoscaler (WVA)](https://github.com/llm-d/llm-d-workload-variant-autoscaler) and the DRA support on  [Kubernetes Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler).

## How it works

```text
External system / WVA controller
        │
        ▼
   Prometheus metric
        │
        ▼
prometheus-vpa-recommender  ──reads──►  VPA.spec.resourceClaimPolicies
        │
        ▼
  VPA.status.recommendation  ──applied by──►  VPA updater / admission-controller
```

1. Watches all `VerticalPodAutoscaler` objects whose `spec.recommenders` lists this recommender.
2. For each `resourceClaimPolicy`, queries Prometheus for the desired capacity value.
3. Writes the results to `VPA.status.recommendation.containerRecommendations`.

## Quick start

See the **[Quick Start Guide](docs/QUICKSTART.md)** for a full walkthrough using a local Kind cluster and the metric-replayer.

## Documentation

| Guide | Description |
|---|---|
| [Metric format & VPA spec](docs/metric-format.md) | Required Prometheus metric labels and VPA YAML |
| [Configuration](docs/configuration.md) | All flags and Make variables |
| [Deploy — metric-replayer](docs/deploy-metric-replayer.md) | Local Kind cluster with fake metrics |
| [Deploy — WVA stack](docs/deploy-wva.md) | WVA controller + kube-prometheus-stack |
| [Troubleshooting](docs/troubleshooting.md) | Common errors and fixes |

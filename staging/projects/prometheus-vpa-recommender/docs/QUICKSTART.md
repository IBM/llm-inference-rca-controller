# Quick Start Guide

Get the Prometheus VPA Recommender running in 5 minutes.

## Prerequisites

- Kind
- kubectl
- Podman or Docker
- Go 1.26+

## Quick Test

```bash
# Clone the repository
cd /path/to/prometheus-vpa-recommender

# Run full test (creates cluster, deploys everything, runs tests)
./scripts/kind-setup.sh test
```

This single command will:

1. Create Kind cluster with VPA CRDs installed
2. Build container images (recommender + metric-replayer)
3. Deploy metric-replayer as the Prometheus endpoint
4. Deploy VPA recommender
5. Deploy test application with VPA
6. Show status and logs

## Step-by-Step

If you prefer manual steps:

### 1. Create Cluster

```bash
./scripts/kind-setup.sh create
```

### 2. Deploy Metric-Replayer

```bash
./scripts/kind-setup.sh deploy-prometheus
```

### 3. Build and Load Image

```bash
./scripts/kind-setup.sh load-image
```

### 4. Deploy Recommender

```bash
./scripts/kind-setup.sh deploy
```

### 5. Deploy Test App

```bash
kubectl apply -f demo/test-app.yaml
```

### 6. Check Status

```bash
./scripts/kind-setup.sh status
```

### 7. View Logs

```bash
./scripts/kind-setup.sh logs
```

## Verify It Works

### Check VPA Recommendations

```bash
kubectl get vpa test-app-vpa -n default \
  -o jsonpath='{.status.recommendation}' | jq .
```

You should see recommendations in the status:

```json
{
  "containerRecommendations": [{
    "containerName": "app",
    "target": {
      "gpu.example.com/compute": "60",
      "gpu.example.com/memory": "18589934592"
    }
  }]
}
```

Values change every ~30 seconds as the replayer advances through the CSV.

## Test Metric-Replayer API

```bash
kubectl run -it --rm test-curl --image=curlimages/curl --restart=Never -- \
  curl -sg 'http://prometheus.default.svc.cluster.local:9090/api/v1/query' \
  --data-urlencode 'query=desired_capacity{variant_name="test-app",target_container="vllm-container",accelerator_type="gpu.example.com",capacity="compute"}'
```

## Common Commands

```bash
# View recommender logs
kubectl logs -n kube-system -l app=prometheus-vpa-recommender -f

# Check recommender pod
kubectl get pods -n kube-system -l app=prometheus-vpa-recommender

# List all VPAs
kubectl get vpa --all-namespaces

# Describe VPA
kubectl describe vpa test-app-vpa

# Delete cluster
./scripts/kind-setup.sh delete
```

## Troubleshooting

### Recommender Not Starting

```bash
# Check pod status
kubectl describe pod -n kube-system -l app=prometheus-vpa-recommender

# Check logs
kubectl logs -n kube-system -l app=prometheus-vpa-recommender --tail=50
```

### No Recommendations

```bash
# Check if VPA has correct recommender name
kubectl get vpa test-app-vpa -o jsonpath='{.spec.recommenders}'

# Should output: [{"name":"prometheus"}]

# Check recommender logs for errors
kubectl logs -n kube-system -l app=prometheus-vpa-recommender | grep ERROR
```

### Prometheus Connection Failed

**Error**: `dial tcp: lookup prometheus on 10.96.0.10:53: no such host`

**Cause**: DNS resolution fails because the recommender (in `kube-system`) and Prometheus service (in `default`) are in different namespaces.

**Solution**: The deployment uses the fully qualified DNS name `prometheus.default.svc.cluster.local:9090`.

```bash
# Check if metric-replayer is running
kubectl get pods -l app=metric-replayer
kubectl get svc prometheus -n default

# Test connectivity using FQDN
kubectl run -it --rm test-curl --image=curlimages/curl --restart=Never -- \
  curl -v http://prometheus.default.svc.cluster.local:9090/health
```

## Clean Up

```bash
./scripts/kind-setup.sh delete
```

## Development

### Run Unit Tests

```bash
# Run all tests
go test ./...

# Run specific package tests
go test ./pkg/prometheus/...
go test ./pkg/recommender/...

# Run with verbose output
go test -v ./...
```

### Build Locally

```bash
# Build binary
make build

# Build container image
make container-build

# Run locally (requires kubeconfig)
./bin/prometheus-vpa-recommender \
  --kubeconfig=$HOME/.kube/config \
  --recommender-name=prometheus \
  --prometheus-url=http://localhost:9090
```

## Environment Variables

Customize the setup:

```bash
# Use different cluster name
KIND_CLUSTER_NAME=my-test ./scripts/kind-setup.sh create

# Use Docker instead of Podman
CONTAINER_RUNTIME=docker make container-build

# Use different image name
IMAGE_NAME=my-recommender ./scripts/kind-setup.sh load-image
```

## Help

```bash
# Show all available commands
./scripts/kind-setup.sh help

# Show Makefile targets
make help
```

# Deploy — metric-replayer (dev / test)

The metric-replayer is a fake Prometheus server that replays a CSV file as
capacity metrics. It exposes `/api/v1/query` so the recommender treats it as a
real Prometheus instance. The Service is named `prometheus` in namespace
`default`, matching the recommender's default `--prometheus-url`
(`http://prometheus.default.svc.cluster.local:9090`) — no flag overrides needed.

See [demo/metric-replayer/README.md](../demo/metric-replayer/README.md) for the
full CSV format and replay semantics.

---

## Prerequisites

- `kind`, `kubectl`, `podman` or `docker`, `make` installed
- VPA CRDs installed on the cluster (the setup script handles this)

---

## Quick start (one command)

```bash
./scripts/kind-setup.sh test
```

This creates a Kind cluster, installs VPA CRDs, builds and loads both images,
deploys the metric-replayer and the recommender, applies the test VPA workload,
and prints status.

---

## Step by step

### 1. Create the Kind cluster

```bash
./scripts/kind-setup.sh create
```

### 2. Deploy the metric-replayer

```bash
./scripts/kind-setup.sh deploy-prometheus
```

Builds the metric-replayer image, loads it into Kind, and applies
`demo/metric-replayer/deploy.yaml`. The default dataset uses
`variant_name=test-app`, `accelerator_type=gpu.example.com`,
`capacity=compute` and `memory`, spaced 30 seconds apart.

To switch to the vLLM / llm-d-sim dataset:

```bash
kubectl apply -f demo/metric-replayer/config_vllm.yaml
kubectl rollout restart deployment metric-replayer
```

To use a custom dataset, edit the `metrics.csv` key in the ConfigMap:

```bash
kubectl edit configmap metric-replayer-data
kubectl rollout restart deployment metric-replayer
```

### 3. Build and deploy the recommender

```bash
./scripts/kind-setup.sh load-image   # build + load image into Kind
make kind-deploy                     # apply RBAC + deployment
```

No Make variable overrides are needed — the defaults match the metric-replayer setup.

### 4. Deploy the test VPA workload

```bash
kubectl apply -f demo/test-app.yaml
```

This creates a `ResourceClaimTemplate`, a `Deployment` (nginx), and a `VPA`
named `test-app-vpa` that opts into this recommender.

### 5. Verify

```bash
# Watch recommender logs
make logs

# Check VPA recommendation
kubectl get vpa test-app-vpa -n default -o jsonpath='{.status.recommendation}' | jq .
```

Expected output (values change every ~30 s as the replayer advances):

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

---

## Tear down

```bash
./scripts/kind-setup.sh delete
```

# Metric Replayer

A fake Prometheus server that replays timestamped metrics from a CSV file.
Exposes `GET/POST /api/v1/query` so it can be used as a drop-in Prometheus
replacement for the prometheus-vpa-recommender during development and demos.

---

## CSV format

```
timestamp, variant_name, target_container, accelerator_type, capacity, value
```

| Column | Description |
|---|---|
| `timestamp` | Unix epoch seconds (integer or float) |
| `variant_name` | VPA / WorkloadVariant name — used as the `variant_name` label |
| `target_container` | Application container name — used as the `target_container` label. Leave empty for replica rows |
| `accelerator_type` | Device class name e.g. `gpu.example.com` — used as the `accelerator_type` label. Leave empty for replica rows |
| `capacity` | Raw capacity name e.g. `compute` — used as the `capacity` label. Leave empty for replica rows |
| `value` | Metric value |

**Row type rules:**
- `accelerator_type` and `capacity` both empty → **desired_replica** sample (emitted as the `-replica-metric` metric)
- `accelerator_type` or `capacity` non-empty → **desired_capacity** sample (emitted as the `-capacity-metric` metric)

A ready-to-use sample is in [`sample-metrics.csv`](./sample-metrics.csv).

---

## Replay semantics

Timestamps in the CSV are treated as **relative offsets from server start**, not
absolute wall-clock times:

```
fileTime = originTS + (wallNow − serverStartTime)
```

where `originTS` is the smallest timestamp in the file.

- The first sample is visible **immediately** when the server starts.
- Subsequent samples become visible as real seconds pass, at the same rate as
  the gaps in the file (e.g. rows 30 s apart become visible 30 s apart).
- Timestamps in responses are translated back to wall-clock time.

---

## Flags

| Flag | Default | Description |
|---|---|---|
| `-metrics-file` | `metrics.csv` | Path to the CSV file |
| `-addr` | `:9090` | Listen address |
| `-replica-metric` | `desired_replica` | Metric name for replica queries |
| `-capacity-metric` | `desired_capacity` | Metric name for capacity queries |

---

## Running locally

```bash
# Build
go build -o metric-replayer .

# Run against the sample file (listens on :9090)
./metric-replayer -metrics-file sample-metrics.csv

# Match the recommender's default capacity metric name
./metric-replayer \
  -metrics-file sample-metrics.csv \
  -capacity-metric desired_capacity

# WVA metric name
./metric-replayer \
  -metrics-file sample-metrics.csv \
  -capacity-metric wva_desired_capacity_per_device
```

### Smoke-test queries

```bash
# Capacity value (current replay position)
curl -sg 'http://localhost:9090/api/v1/query' \
  --data-urlencode 'query=desired_capacity{variant_name="test-app",target_container="vllm-container",accelerator_type="gpu.example.com",capacity="compute"}' \
  | jq .

# Health check
curl http://localhost:9090/health
```

---

## Building a container image

```bash
# Podman
podman build -t localhost/metric-replayer:v1.0.0 .

# Docker
docker build -t localhost/metric-replayer:v1.0.0 .
```

### Loading into a Kind cluster

```bash
# Podman — kind load docker-image is not supported; use image-archive
podman save localhost/metric-replayer:v1.0.0 -o /tmp/metric-replayer.tar
kind load image-archive /tmp/metric-replayer.tar --name <cluster-name>
rm /tmp/metric-replayer.tar

# Docker
docker build -t localhost/metric-replayer:v1.0.0 .
kind load docker-image localhost/metric-replayer:v1.0.0 --name <cluster-name>
```

---

## Running in Kubernetes

The manifests in [`deploy.yaml`](./deploy.yaml) create:
- A **ConfigMap** (`metric-replayer-data`) holding the CSV.
- A **Deployment** running the replayer.
- A **Service** named `prometheus` on port 9090, matching the recommender's
  default `--prometheus-url` (`http://prometheus.default.svc.cluster.local:9090`).

```bash
# From the project root — deploy everything at once
kubectl apply -f demo/metric-replayer/deploy.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
kubectl apply -f demo/test-app.yaml
```

Or use the helper script (builds and loads the image automatically):

```bash
./scripts/kind-setup.sh deploy-prometheus
```

### Swap dataset at runtime

Edit the ConfigMap and restart the pod — no image rebuild needed:

```bash
# Switch to the vLLM / llm-d-sim dataset
kubectl apply -f demo/metric-replayer/config_vllm.yaml
kubectl rollout restart deployment metric-replayer
```

---

## Running tests

```bash
go test -v ./...
```

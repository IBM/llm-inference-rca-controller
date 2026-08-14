# Configuration

## Flags

All flags have defaults that match the metric-replayer dev setup.
Override them on the command line or via Make variables at deploy time.

| Flag | Default | Description |
|---|---|---|
| `--recommender-name` | `prometheus` | VPAs must list this name in `spec.recommenders` |
| `--prometheus-url` | `http://prometheus.default.svc.cluster.local:9090` | Prometheus server URL |
| `--prometheus-insecure-skip-verify` | `false` | Skip TLS certificate verification (for self-signed certs) |
| `--capacity-metric-name` | `desired_capacity` | Prometheus metric name to query |
| `--update-interval` | `30s` | How often to poll Prometheus and update VPAs |
| `--namespace` | *(all namespaces)* | Restrict to a single namespace |
| `--metrics-port` | `8080` | Port for `/healthz` |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig for local development |
| `--v` | `0` | klog verbosity: `2`=info, `4`=debug, `5`=trace every Prometheus query |

---

## Make variables

`make deploy` (and `make kind-deploy`) patch the deployment manifest on-the-fly
using `sed` before applying to the cluster — no manual YAML editing required.

| Variable | Default | Description |
|---|---|---|
| `IMAGE_REGISTRY` | `localhost` | Container image registry |
| `IMAGE_TAG` | `v1.0.0` | Image tag |
| `CONTAINER_RUNTIME` | `podman` | `podman` or `docker` |
| `PROMETHEUS_URL` | `http://prometheus.default.svc.cluster.local:9090` | Prometheus URL |
| `PROMETHEUS_INSECURE_SKIP_VERIFY` | `false` | Skip TLS verification |
| `CAPACITY_METRIC_NAME` | `desired_capacity` | Prometheus metric name |
| `RECOMMENDER_NAME` | `prometheus` | Recommender name |
| `UPDATE_INTERVAL` | `30s` | Poll interval |

### Examples

```bash
# Deploy with all defaults (works with metric-replayer)
make deploy

# Deploy for WVA production
make deploy \
  IMAGE_REGISTRY=my-registry.example.com \
  IMAGE_TAG=v1.0.0 \
  CAPACITY_METRIC_NAME=wva_desired_capacity_per_device \
  PROMETHEUS_URL=https://kube-prometheus-stack-prometheus.workload-variant-autoscaler-monitoring.svc.cluster.local:9090 \
  PROMETHEUS_INSECURE_SKIP_VERIFY=true

# Run locally against a port-forwarded Prometheus
make run-local \
  PROMETHEUS_URL=http://localhost:9090 \
  CAPACITY_METRIC_NAME=wva_desired_capacity_per_device
```

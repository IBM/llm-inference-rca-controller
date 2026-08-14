# Metric format & VPA spec

## Required Prometheus metric

The recommender issues one instant-vector query per `(vpa, container, deviceClass, capacity)` tuple:

```
<capacity-metric-name>{
    variant_name     = "<vpa-name>",
    target_container = "<container-name>",
    accelerator_type = "<deviceClassName>",
    capacity         = "<capacity>",
}
```

| Label | Source | Example |
|---|---|---|
| `variant_name` | `VPA.metadata.name` | `sample-deployment` |
| `target_container` | Container name resolved from Deployment or VPA `containerPolicies` | `dev-model-decode` |
| `accelerator_type` | `resourceClaimPolicy.deviceClassName` | `gpu.example.com` |
| `capacity` | Entry in `resourceClaimPolicy.controlledCapacities` | `compute` |

The metric name is configurable via `--capacity-metric-name` (default: `desired_capacity`).
Extra labels on the metric (e.g. `container`, `namespace`, `pod`) are ignored.

### WVA metric

When using the WVA stack the metric published by the WVA controller is
`wva_desired_capacity_per_device`. Set `--capacity-metric-name=wva_desired_capacity_per_device`
(or the Make equivalent) when deploying against WVA.

Example metric as seen in Prometheus:

```
wva_desired_capacity_per_device{
    variant_name="sample-deployment",
    target_container="dev-model-decode",
    accelerator_type="gpu.example.com",
    capacity="compute",
    exported_namespace="llm-d-sim",
    ...
} 60
```

---

## VPA spec

Each field in the VPA spec maps directly to a Prometheus label used in the query.

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: sample-deployment        # → variant_name label
  namespace: llm-d-sim
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: sample-deployment
  recommenders:
    - name: prometheus             # must match --recommender-name (default: prometheus)
  updatePolicy:
    updateMode: DRARecreate
  resourcePolicy:
    resourceClaimPolicies:
      - claimTemplateName: gpu-claim
        deviceClassName: gpu.example.com   # → accelerator_type label
        controlledCapacities:
          - compute                          # → capacity label (one query per entry)
          - memory
        minAllowed:
          compute: "10"
          memory: "1Gi"
        maxAllowed:
          compute: "100"
          memory: "16Gi"
```

**Key constraints:**

- `metadata.name` must exactly match the `variant_name` label on the metric.
- `deviceClassName` must exactly match the `accelerator_type` label.
- Each entry in `controlledCapacities` must exactly match a `capacity` label value.
- `claimTemplateName` must match the name used in `Deployment.spec.template.spec.resourceClaims`.

### Resulting recommendation in VPA status

```yaml
status:
  recommendation:
    containerRecommendations:
    - containerName: dev-model-decode
      target:
        gpu.example.com/compute: "60"
        gpu.example.com/memory: "8589934592"
```

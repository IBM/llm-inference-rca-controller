# Deploy — WVA stack

Use this guide when the WVA controller is already running and publishing
`wva_desired_capacity_per_device` metrics to a kube-prometheus-stack instance.

---

## Prerequisites

- WVA controller running and publishing `wva_desired_capacity_per_device`
- kube-prometheus-stack deployed in namespace `workload-variant-autoscaler-monitoring`
- VPA CRDs installed on the cluster
- Access to push images to a container registry reachable from the cluster

---

## 1. Verify the metric exists

Port-forward to Prometheus and confirm the metric is present with the expected labels:

```bash
kubectl port-forward -n workload-variant-autoscaler-monitoring \
  svc/kube-prometheus-stack-prometheus 9090:9090
```

```bash
curl -sg 'http://localhost:9090/api/v1/query?query=wva_desired_capacity_per_device' | jq .
```

Confirm the result contains labels `variant_name`, `target_container`,
`accelerator_type`, and `capacity`. Example:

```json
{
  "metric": {
    "variant_name": "sample-deployment",
    "target_container": "dev-model-decode",
    "accelerator_type": "gpu.example.com",
    "capacity": "compute"
  },
  "value": [1234567890, "60"]
}
```

---

## 2. Build and push the recommender image

```bash
make container-build IMAGE_REGISTRY=<your-registry> IMAGE_TAG=v1.0.0
make container-push  IMAGE_REGISTRY=<your-registry> IMAGE_TAG=v1.0.0
```

---

## 3. Deploy the recommender

```bash
make deploy \
  IMAGE_REGISTRY=<your-registry> \
  IMAGE_TAG=v1.0.0 \
  CAPACITY_METRIC_NAME=wva_desired_capacity_per_device \
  PROMETHEUS_URL=https://kube-prometheus-stack-prometheus.workload-variant-autoscaler-monitoring.svc.cluster.local:9090 \
  PROMETHEUS_INSECURE_SKIP_VERIFY=true
```

`PROMETHEUS_INSECURE_SKIP_VERIFY=true` is required when kube-prometheus-stack
uses a self-signed certificate (the default). Remove it if you have a valid CA.

---

## 4. Create a VPA for your workload

The VPA `metadata.name` **must match** the `variant_name` label on the metric
exactly. Each `deviceClassName` and `controlledCapacity` entry must also match
the corresponding `accelerator_type` and `capacity` label values.

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: sample-deployment          # must match variant_name label
  namespace: llm-d-sim
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: sample-deployment
  recommenders:
    - name: prometheus
  updatePolicy:
    updateMode: DRARecreate
  resourcePolicy:
    resourceClaimPolicies:
      - claimTemplateName: gpu-claim
        deviceClassName: gpu.example.com   # must match accelerator_type label
        controlledCapacities:
          - compute                          # must match capacity="compute"
          - memory                           # must match capacity="memory"
        minAllowed:
          compute: "10"
          memory: "1Gi"
        maxAllowed:
          compute: "100"
          memory: "16Gi"
```

```bash
kubectl apply -f my-vpa.yaml
```

---

## 5. Verify

```bash
make logs

kubectl get vpa sample-deployment -n llm-d-sim \
  -o jsonpath='{.status.recommendation}' | jq .
```

Expected output:

```json
{
  "containerRecommendations": [{
    "containerName": "dev-model-decode",
    "target": {
      "gpu.example.com/compute": "60",
      "gpu.example.com/memory": "8589934592"
    }
  }]
}
```

---

## Undeploy

```bash
make undeploy
```

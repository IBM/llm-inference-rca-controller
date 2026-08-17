# Run Demo of Upstream Integration

Workspace: `staging`

## Prepare Cluster and Related Components

### Create Kind Cluster, Build, and Deploy VPA Autoscaler

Workspace: `projects/k8s-autoscaler/vertical-pod-autoscaler`

1. Install Kind cluster

    ```sh
    kind create cluster --config "$(pwd)/../.github/kind-config.yaml
    ```

    Expect

    ```sh
    > kubectl get nodes
    NAME                 STATUS   ROLES           AGE   VERSION
    kind-control-plane   Ready    control-plane   24m   v1.36.1
    kind-worker          Ready    <none>          24m   v1.36.1
    kind-worker2         Ready    <none>          24m   v1.36.1
    ```

2. Build and deploy VPA autoscaler

    ```sh
    FEATURE_GATES="DRARecreate=true" ./hack/deploy-for-e2e-locally.sh actuation
    ```

    Expect

    ```sh
    > kubectl get pods -n kube-system -lapp.kubernetes.io/instance=vpa
    NAME                                        READY   STATUS    RESTARTS   AGE
    vpa-admission-controller-76946dbdf4-8ld8l   1/1     Running   0          11m
    vpa-updater-97cb6b5d4-zp9hq                 1/1     Running   0          11m
    ```

### Build and Deploy DRA Driver

Workspace: `projects/dra-example-driver`

1. Build and load the driver image to Kind

    ```sh
    ./demo/build-driver.sh
    KIND_CLUSTER_NAME=kind ./demo/scripts/load-driver-image-into-kind.sh
    ```

2. Deploy via Helm chart

    ```sh
    helm upgrade -i \
    --create-namespace \
    --namespace dra-example-driver \
    --set gpuAllowMultipleAllocations=true \
    dra-example-driver \
    deployments/helm/dra-example-driver
    ```

    Expect

    ```sh
    > $ kubectl get po -n dra-example-driver
    NAME                                     READY   STATUS    RESTARTS   AGE
    dra-example-driver-kubeletplugin-6vrv4   1/1     Running   0          11s
    dra-example-driver-kubeletplugin-mh7dt   1/1     Running   0          11s
    ```

### Build and Deploy Prometheus recommender

Workspace: `projects/prometheus-vpa-recommender`

Run:

```sh
make kind-deploy \
KIND_CLUSTER_NAME=kind \
CAPACITY_METRIC_NAME=wva_desired_capacity_per_device \
PROMETHEUS_URL=https://kube-prometheus-stack-prometheus.workload-variant-autoscaler-monitoring.svc.cluster.local:9090 \
PROMETHEUS_INSECURE_SKIP_VERIFY=true
```

Expect:

```sh
> $ kubectl get po -n kube-system -l app=prometheus-vpa-recommender
NAME                                          READY   STATUS    RESTARTS   AGE
prometheus-vpa-recommender-555744b449-sh8k2   1/1     Running   0          48s
```

### Build and Deploy llm-d-workload-variant-autoscaler

Workspace: `projects/llm-d-workload-variant-autoscaler`

1. Build and load the image to Kind

    ```sh
    CONTAINER_TOOL=podman make docker-build IMG=ghcr.io/llm-d/llm-d-workload-variant-autoscaler:v0.0.1
    KIND_CLUSTER_NAME=kind
    IMAGE=ghcr.io/llm-d/llm-d-workload-variant-autoscaler:v0.0.1
    podman save ${IMAGE} -o /tmp/image.tar
    kind load image-archive /tmp/image.tar --name ${KIND_CLUSTER_NAME}
    rm /tmp/image.tar
    ```

    > Reset the pod `kubectl delete po -n workload-variant-autoscaler-system -l app.kubernetes.io/name=workload-variant-autoscaler`

2. Deploy

    ```sh
    make deploy-wva-on-k8s IMG=${IMAGE}
    ```

    Expect:

    ```sh
    > $ kubectl get po -n workload-variant-autoscaler-system
    NAME                                      READY   STATUS    RESTARTS   AGE
    wva-controller-manager-64d78d9bf6-994v8   1/1     Running   0          18m
    > $ kubectl get po -n workload-variant-autoscaler-monitoring
    NAME                                                        READY   STATUS    RESTARTS   AGE
    kube-prometheus-stack-grafana-56978c9cd7-htzjx              3/3     Running   0          20m
    kube-prometheus-stack-kube-state-metrics-6f664bc8c4-wx85k   1/1     Running   0          20m
    kube-prometheus-stack-operator-8469798b9d-r8vjm             1/1     Running   0          20m
    kube-prometheus-stack-prometheus-node-exporter-2dqrc        1/1     Running   0          20m
    kube-prometheus-stack-prometheus-node-exporter-6j2hk        1/1     Running   0          20m
    kube-prometheus-stack-prometheus-node-exporter-vgldg        1/1     Running   0          20m
    prometheus-adapter-6758b6bcfc-kfvxh                         1/1     Running   0          17m
    prometheus-adapter-6758b6bcfc-q75lm                         1/1     Running   0          17m
    prometheus-kube-prometheus-stack-prometheus-0               2/2     Running   0          18m
    ```

## Testing

### with llm-d-inference-sim

Deploy app:

```sh
kubectl apply -f samples/deployment.yaml
```

Expect:

```sh
> $ kubectl get po -n llm-d-sim
NAME                                READY   STATUS    RESTARTS   AGE
sample-deployment-69676f6f7-d7ttl   1/1     Running   0          117m
```

Deploy HPA+VPA:

```sh
kubectl apply -f samples/mpa.yaml
```

Expect:

```sh
> $ kubectl apply -f config/samples/vpa/mpa.yaml
horizontalpodautoscaler.autoscaling/sample-deployment created
verticalpodautoscaler.autoscaling.k8s.io/sample-deployment created
```

Generate workload

```sh
kubectl apply -f samples/load.yaml
```

#### Expected results

WVA metric:

> wva_desired_capacity_per_device{accelerator_type="gpu.example.com", **capacity="compute"**, container="manager", endpoint="https", exported_namespace="llm-d-sim", instance="10.244.3.61:8443", job="wva-controller-manager-metrics-service", model_id="test-model", namespace="workload-variant-autoscaler-system", pod="wva-controller-manager-7bdcc59cd9-rgkn2", service="wva-controller-manager-metrics-service", target_container="dev-model-decode", variant_name="sample-deployment"}

> wva_desired_capacity_per_device{accelerator_type="gpu.example.com", **capacity="memory"**, container="manager", endpoint="https", exported_namespace="llm-d-sim", instance="10.244.3.61:8443", job="wva-controller-manager-metrics-service", model_id="test-model", namespace="workload-variant-autoscaler-system", pod="wva-controller-manager-7bdcc59cd9-rgkn2", service="wva-controller-manager-metrics-service", target_container="dev-model-decode", variant_name="sample-deployment"}

VPA Recommendation status:

```sh
> $ kubectl get vpa sample-deployment -n llm-d-sim -oyaml | yq .status
conditions:
  - lastTransitionTime: "2026-08-14T09:43:20Z"
    status: "True"
    type: RecommendationProvided
recommendation:
  containerRecommendations:
    - containerName: dev-model-decode
      target:
        gpu.example.com/compute: "50"
        gpu.example.com/memory: "42949672960"
```

Log from `vpa-admission-controller`

```sh
> kubectl logs -n kube-system $(kubectl get po -n kube-system -l app.kubernetes.io/component=admission-controller -o name)
```

> I0814 09:43:43.732597       1 server.go:175] "Sending patches" patches=[{"op":"add","path":"/spec/devices/requests/0/exactly/capacity","value":{"requests":{"compute":"50"}}},{"op":"add","path":"/spec/devices/requests/0/exactly/capacity/requests/memory","value":"42949672960"}]

Reflect on ResourceClaim's patch:

```sh
  status:
    allocation:
      allocationTimestamp: "2026-08-14T09:43:43Z"
      devices:
        results:
        - consumedCapacity:
            compute: "50"
            memory: 40Gi
          device: gpu-0
          driver: gpu.example.com
          pool: kind-worker2
          request: gpu
          shareID: 390d099c-5b35-48ac-82aa-8fc3fe3c4b23
```

#### with inference-perf

```sh
kubectl port-forward -n workload-variant-autoscaler-monitoring   prometheus-kube-prometheus-stack-prometheus-0 30909:9090
```

```sh
kubectl port-forward -n llm-d-sim                                sample-deployment-69676f6f7-d7ttl 30080:8000
```

```sh
cd llm-d/inference-perf
conda activate py314
python -m inference_perf.main --config examples/vllm/config-random-test-model.yml
```

### with llm-d

TBD

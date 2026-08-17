# Proposal: Vertical Scaling and Multi-Dimensional Optimization in llm-d-workload-variant-autoscaler

## Status

**Implemented** - Available in [staging branch](https://github.com/IBM/llm-inference-rca-controller/tree/staging/staging/projects/llm-d-workload-variant-autoscaler)

<!-- TOC tocDepth:2..3 chapterDepth:2..6 -->

- [Status](#status)
- [Overview](#overview)
- [Goal](#goal)
- [Target Repository](#target-repository)
- [Background](#background)
- [Design](#design)
    - [Architecture](#architecture)
    - [Change 1: VPA Reconciler Support](#change-1-vpa-reconciler-support)
    - [Change 2: VariantObservationStore](#change-2-variantobservationstore)
    - [Change 3: Multi-Dimensional Optimizer](#change-3-multi-dimensional-optimizer)
    - [Vertical Scaling Decision Flow](#vertical-scaling-decision-flow)
    - [Output Metrics](#output-metrics)
    - [VPA and HPA Configuration](#vpa-and-hpa-configuration)
- [Timeline](#timeline)

<!-- /TOC -->

## Overview

Proposal to extend the [llm-d Workload Variant Autoscaler (WVA)](https://github.com/llm-d/llm-d-workload-variant-autoscaler) with three related capabilities:

1. **VPA Reconciler** — a controller that watches `VerticalPodAutoscaler` objects annotated with `llm-d.ai/managed: "true"` and integrates them into the WVA namespace-tracking and Coordinator loop, enabling VPA-driven DRA resource claim resizing alongside existing HPA-driven horizontal scaling.
2. **VariantObservationStore** — a thread-safe, per-tick empirical learning store that accumulates vertical scaling signals (compute intensity, memory weight, bytes-per-token) from raw replica metrics, and feeds them into the analyzers' `VerticalHint` computation.
3. **Multi-Dimensional Optimizer** — a new optimizer that wraps the existing horizontal-only optimizer with a DRA-aware vertical scaling pass, producing both replica-count decisions and per-replica GPU capacity targets in a single Coordinator tick.

Together these changes enable WVA to drive both **horizontal** (replica count via HPA/KEDA) and **vertical** (GPU compute + memory capacity via VPA + DRA) scaling axes from a single optimization loop.

## Goal

- Allow WVA to manage VPA objects (in addition to HPA and KEDA ScaledObjects) as first-class scaling targets.
- Empirically learn per-variant GPU compute and memory demand from vLLM metrics each tick, without any offline profiling or model-specific configuration.
- Enable WVA's optimizer to recommend GPU capacity changes per replica (vertical) in addition to replica count changes (horizontal), using DRA headroom as a budget constraint.
- Emit `wva_variant_target_capacity_per_gpu` and `wva_vertical_scaling_total` metrics that the [Prometheus VPA Recommender](../../tbd/prometheus-vpa-recommender/README.md) can consume to write VPA `.status.recommendation`.

## Target Repository

[llm-d/llm-d-workload-variant-autoscaler](https://github.com/llm-d/llm-d-workload-variant-autoscaler)

## Background

Before these changes, WVA's Coordinator loop only discovered and reconciled `HorizontalPodAutoscaler` and KEDA `ScaledObject` resources. Vertical resource sizing (GPU memory, compute) was entirely out of scope. The optimizer pipeline produced a single decision axis: desired replica count.

With DRA (Dynamic Resource Allocation, `resource.k8s.io/v1`) becoming the Kubernetes-native path for fine-grained GPU capacity slicing, and VPA gaining `resourceClaimPolicies` support (upstream PR in `k8s-autoscaler`), a natural integration point emerged: WVA can compute the optimal per-replica GPU capacity alongside the optimal replica count, emit that as a Prometheus metric, and let the VPA Recommender + VPA Updater/Admission Controller act on it.

## Design

### Architecture

```text
┌─────────────────────────────────────────────────────────────────────┐
│  WVA Controller Manager (wva-system)                                │
│                                                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────────┐     │
│  │ HPAReconciler│  │VPAReconciler │  │ScaledObjReconciler     │     │
│  │  watches HPA │  │  watches VPA │  │  watches ScaledObject  │     │
│  └──────┬───────┘  └──────┬───────┘  └────────────┬───────────┘     │
│         │                 │                       │                 │
│         └─────────────────┴───────────────────────┘                 │
│                           │ NamespaceTrack/Untrack                  │
│                           ▼                                         │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │  Coordinator (15 s loop)                                       │ │
│  │  ┌──────────────────────────────────────────────────────────┐  │ │
│  │  │  MultiDimensionalOptimizer                               │  │ │
│  │  │  ┌──────────────────────────────┐                        │  │ │
│  │  │  │  1. applyVerticalScaleUp     │  DRA snapshot          │  │ │
│  │  │  │     (DRA headroom check)     │◄─────────────────────  │  │ │
│  │  │  │  2. inner optimizer          │  ResourceCapacity      │  │ │
│  │  │  │     (CostAware / GreedyScore)│  Inventory             │  │ │
│  │  │  │  3. applyVerticalScaleDown   │                        │  │ │
│  │  │  │     (at minReplicas only)    │                        │  │ │
│  │  │  └──────────────────────────────┘                        │  │ │
│  │  └──────────────────────────────────────────────────────────┘  │ │
│  │  ┌──────────────────────────────────────────────────────────┐  │ │
│  │  │  Actuator / MetricsEmitter                               │  │ │
│  │  │  emits wva_variant_target_capacity_per_gpu               │  │ │
│  │  │  emits wva_desired_replicas (consumed by HPA/KEDA)       │  │ │
│  │  └──────────────────────────────────────────────────────────┘  │ │
│  └────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
                │ wva_variant_target_capacity_per_gpu
                ▼
┌─────────────────────────────────────────┐
│  Prometheus (wva-monitoring)            │
│  Prometheus VPA Recommender             │
│    queries → writes VPA .status         │
└─────────────────────────────────────────┘
                │ .status.recommendation
                ▼
┌─────────────────────────────────────────┐
│  VPA (llm-d-sim)                        │
│  VPA Updater  (DRARecreate)             │──► ResourceClaim patch
│  VPA Admission Controller               │    (compute + memory)
└─────────────────────────────────────────┘
```

### Change 1: VPA Reconciler Support

#### Problem

Before this change, WVA's Coordinator loop only activated for namespaces containing managed `HorizontalPodAutoscaler` or KEDA `ScaledObject` objects. A namespace that used only a `VerticalPodAutoscaler` (with `llm-d.ai/managed: "true"`) was never tracked and therefore never optimized.

#### Solution

A new [`VPAReconciler`](../../staging/projects/llm-d-workload-variant-autoscaler/internal/controller/vpa_reconciler.go) is registered with the controller manager when the VPA CRD is detected at startup. It mirrors the design of `HPAReconciler`: its only job is to call `Datastore.NamespaceTrack` / `NamespaceUntrack` so the Coordinator's polling loop scopes its `List` calls to relevant namespaces.

**Tracking condition** — a VPA is tracked when all of the following hold:

| Condition | Rationale |
|---|---|
| `llm-d.ai/managed: "true"` annotation | Opt-in; prevents WVA from touching unrelated VPAs |
| `spec.recommenders[].name == "prometheus"` | Only VPAs using the external Prometheus recommender are integrated; VPAs using the built-in recommender are left alone |
| `DeletionTimestamp` is zero | Skip VPAs being deleted |

A VPA that is managed but does not use the `prometheus` recommender logs a debug-level notice and is untracked:

```text
VPA is managed but does not use the prometheus recommender, not tracking
```

**RBAC** — The reconciler requires cluster-wide `get;list;watch` on `autoscaling.k8s.io/verticalpodautoscalers`. Kubernetes RBAC does not support annotation-based selectors; the `AnnotatedScalerPredicate` limits in-process event handling to managed objects.

#### Registration

`VPAReconciler` is registered only when the VPA CRD is present (detected by a `runtime.Scheme` lookup at startup). This keeps the binary safe to deploy on clusters without VPA installed.

### Change 2: VariantObservationStore

#### Problem

The analyzers (Saturation V2, Queueing Model) needed to compute a `VerticalHint` — the target per-replica capacity for scale-up and scale-down — but had no way to accumulate empirical GPU signals across ticks. Without a running history of compute intensity and memory usage, any per-tick vertical recommendation would be noisy and unsafe.

#### Solution

[`VariantObservationStore`](../../staging/projects/llm-d-workload-variant-autoscaler/internal/engines/analyzers/observationstore/store.go) is a shared, thread-safe in-memory store keyed by `"namespace|modelID|variantName"`. Both the Saturation V2 and Queueing Model analyzers call `UpdateFromReplicaMetrics` at the start of each `Analyze()` call, then read the accumulated signals when computing `VerticalHint`.

#### Stored Signals

| Field | Update rule | Used for |
|---|---|---|
| `ComputeIntensity` | Mean `I_live = PromptTokenRate + α×GenerationTokenRate` across ready replicas, updated every tick | Compute fraction: `I_live / I_max` |
| `MaxComputeIntensity` | High-water `I_live` only when `QueueLength > 0`; never decremented | Empirical compute wall `I_max`; gates `VerticalHint` (nil until first saturated tick) |
| `MemoryWeight` | Derived once from `TotalKvCapacityTokens × (1−U)/U × BytesPerKVToken`; never overwritten | Model-weight memory baseline in the memory demand formula |
| `BytePerToken` | `DeltaCacheBytes / DeltaTokens`; refreshed each tick when both > 0 | KV cache bytes per active token |

`MaxComputeIntensity == 0` means the store is not yet bootstrapped — no saturated tick has been observed. Both analyzers treat this as a nil hint and skip vertical recommendations until real saturation data has been seen.

#### Stale Entry Eviction

`EvictStale(timeout)` removes entries whose `UpdatedAt` timestamp is older than `timeout`. This prevents the map from growing unboundedly when variants are deleted.

#### VerticalHint Computation (Saturation V2 path)

`computeVerticalHint` reads the store and produces a `*interfaces.VerticalHint`:

```
scaleUpPRC   = totalDemand   / readyCount   # tokens per replica to absorb all demand
scaleDownPRC = totalCapacity / readyCount   # minimum tokens per replica, no spare

computeDemand = min(ComputeIntensity / MaxComputeIntensity, 1.0)
memoryDemand  = MemoryWeight + BytePerToken × targetPRC

# clamp to VPA ResourceClaimPolicy [MinAllowed, MaxAllowed]
roundedCompute, roundedMemory = applyStepPolicy(computeDemand, memoryDemand, policy)

DemandPerReplicaResource = {ComputeFraction: roundedCompute, MemoryBytes: roundedMemory}
```

Returns `nil` (no vertical action) when:
- Store has no entry yet, or `MaxComputeIntensity == 0` (not bootstrapped)
- `readyCount == 0`
- Supply and demand are balanced (`totalDemand == totalCapacity`)
- Scale-down target would not meaningfully reduce the current capacity

### Change 3: Multi-Dimensional Optimizer

#### Problem

The existing optimizers (`CostAwareOptimizer`, `GreedyByScoreOptimizer`) produced only horizontal decisions (replica count). GPU capacity per replica was fixed at deployment time with no runtime feedback loop.

#### Solution

[`MultiDimensionalOptimizer`](../../staging/projects/llm-d-workload-variant-autoscaler/internal/engines/pipeline/multi_dimensional_optimizer.go) wraps any existing `ScalingOptimizer` and adds a vertical scaling pass before/after the horizontal allocation:

```
MultiDimensionalOptimizer("cost-aware")
MultiDimensionalOptimizer("greedy-score")
```

The optimizer decides **direction** (scale-up / scale-down / no-change) per variant and records the result as a `verticalTarget` that flows through to `VariantDecision.VerticalAction` and `TargetPerReplicaCapacity`.

#### Scale-Up Path (vertical-first)

```
1. Snapshot DRA available capacity  (ResourceCapacityInventory)
2. For each variant with VerticalScalingEnabled=true:
     if ScaleUpPerReplicaCapacity > currentPRC AND draAvailable ≥ deltaTotal:
       patch vc.PerReplicaCapacity (working copy only)
       decrement draAvailable budget
       record verticalTarget{action=ScaleUp}
3. Run inner optimizer's horizontal allocation with patched PRC
```

**OOM safety floor** — before computing the capacity delta, `DemandPerReplicaResource.MemoryBytes` is floored at the current per-replica claimed allocation (`currentClaimed / readyCount / 1000`). This prevents recommending a memory slice smaller than what the pod currently holds, which would cause an OOM kill on the next pod restart.

**DRA unavailable** — when the DRA snapshot returns `nil` (CRD absent or API error), `applyVerticalScaleUpWithDRA` is a no-op and the optimizer falls through to a pure horizontal result. No vertical targets are emitted.

#### Scale-Down Path (horizontal-first)

```
1. Run inner optimizer's horizontal scale-down
2. For each variant at minReplicas with ScaleDownPerReplicaCapacity < currentPRC:
     record verticalTarget{action=ScaleDown}
```

Scale-down is only triggered when the variant is already at its minimum replica floor (horizontal has nothing more to remove). No DRA headroom check is needed for shrink.

#### VariantCapacity and VerticalHint

The optimizer reads vertical scaling hints from `interfaces.VariantCapacity.VerticalHint`, which is populated by the analyzer with:

| Field | Description |
|---|---|
| `ScaleUpPerReplicaCapacity` | Optimal per-replica capacity computed by the analyzer for the scale-up case |
| `ScaleDownPerReplicaCapacity` | Minimal safe per-replica capacity for the scale-down case |
| `DemandPerReplicaResource` | Step-policy-rounded `ResourceRequirement` (`MemoryBytes`, etc.) used by the actuator to derive the DRA capacity request |

`VerticalScalingEnabled` on `VariantReplicaState` is set to `true` when the variant has a non-nil `ResourceClaimPolicy` (sourced from `VPA.spec.resourcePolicy.resourceClaimPolicies[0]`).

### Vertical Scaling Decision Flow

```text
Analyzer
  └─ produces VerticalHint{ScaleUp/ScaleDownPRC, DemandPerReplicaResource}
        │
        ▼
MultiDimensionalOptimizer
  └─ applyVerticalScaleUpWithDRA / applyVerticalScaleDown
  └─ produces VariantDecision{VerticalAction, TargetPerReplicaCapacity,
                               DemandPerReplicaResource}
        │
        ▼
MetricsEmitter
  └─ emits wva_variant_target_capacity_per_gpu{capacity="memory", ...}
  └─ increments wva_vertical_scaling_total{direction="up"|"down", ...}
        │
        ▼
Prometheus (wva-monitoring)
        │
        ▼
Prometheus VPA Recommender (kube-system)
  └─ queries wva_variant_target_capacity_per_gpu
  └─ writes VPA .status.recommendation
        │
        ▼
VPA Updater (DRARecreate) / Admission Controller
  └─ patches ResourceClaim with new compute + memory capacity
        │
        ▼
DRA KubeletPlugin (dra-example-driver)
  └─ allocates GPU device with updated capacity slices
```

### Output Metrics

Two new Prometheus metrics are introduced for vertical scaling observability.

#### `wva_variant_target_capacity_per_gpu`

Gauge. Records the last recommended DRA capacity target per replica for each vertical-scaling-eligible variant. Consumed by the Prometheus VPA Recommender via the configurable `CAPACITY_METRIC_NAME` flag.

```prometheus
wva_variant_target_capacity_per_gpu{
    variant_name     = "<hpa-or-vpa-name>",
    namespace        = "<namespace>",
    model_id         = "<model-id>",
    accelerator_type = "<deviceClassName>",
    capacity         = "<capacity-dimension>",   # e.g. "memory", "compute"
}
```

Example output (matches demo expected results):

```prometheus
wva_variant_target_capacity_per_gpu{
    accelerator_type="gpu.example.com",
    capacity="compute",
    model_id="test-model",
    namespace="llm-d-sim",
    variant_name="sample-deployment"
} 50

wva_variant_target_capacity_per_gpu{
    accelerator_type="gpu.example.com",
    capacity="memory",
    model_id="test-model",
    namespace="llm-d-sim",
    variant_name="sample-deployment"
} 42949672960
```

**Note**: In the demo, the Prometheus VPA Recommender is deployed with `CAPACITY_METRIC_NAME=wva_desired_capacity_per_device`. This environment variable sets the query name the recommender uses to look up WVA's output; the underlying metric emitted by WVA is `wva_variant_target_capacity_per_gpu`.

#### `wva_vertical_scaling_total`

Counter. Tracks cumulative vertical scaling decisions by direction.

```prometheus
wva_vertical_scaling_total{
    variant_name = "<name>",
    namespace    = "<namespace>",
    model_id     = "<model-id>",
    direction    = "up" | "down",
}
```

### VPA and HPA Configuration

A workload that uses both axes requires an `HPA` (or KEDA `ScaledObject`) for horizontal scaling and a `VPA` for vertical scaling, both annotated with `llm-d.ai/managed: "true"` and targeting the same `Deployment`.

```yaml
# HPA — drives replica count via wva_desired_replicas
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: sample-deployment
  namespace: llm-d-sim
  annotations:
    llm-d.ai/managed: 'true'
    llm-d.ai/model-id: test-model
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: sample-deployment
  minReplicas: 1
  maxReplicas: 5
  metrics:
  - type: External
    external:
      metric:
        name: wva_desired_replicas
        selector:
          matchLabels:
            variant_name: sample-deployment
            exported_namespace: llm-d-sim
      target:
        type: AverageValue
        averageValue: "1"
---
# VPA — drives GPU capacity via wva_variant_target_capacity_per_gpu
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: sample-deployment
  namespace: llm-d-sim
  annotations:
    llm-d.ai/managed: 'true'
    llm-d.ai/model-id: test-model
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: sample-deployment
  recommenders:
    - name: prometheus          # must use the external recommender
  resourcePolicy:
    resourceClaimPolicies:
      - claimTemplateName: gpu-claim
        deviceClassName: gpu.example.com
        controlledCapacities: [compute, memory]
        minAllowed:
          compute: '10'
          memory: 1Gi
        maxAllowed:
          compute: '50'
          memory: 40Gi
  updatePolicy:
    updateMode: DRARecreate     # recreate pod to apply new ResourceClaim capacity
    minReplicas: 1
```

The `ResourceClaimTemplate` referenced by the `Deployment` is left unchanged. DRA capacity values are patched into the per-pod `ResourceClaim` by the VPA Admission Controller on pod (re)creation.

## Timeline

- 2026-08-14: Initial implementation added to [staging branch](https://github.com/IBM/llm-inference-rca-controller/tree/staging/staging/projects/llm-d-workload-variant-autoscaler), integration tested end-to-end with Kind cluster, VPA autoscaler fork (`DRARecreate` feature gate), DRA example driver, and `llm-d-inference-sim`.

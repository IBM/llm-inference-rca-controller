# Proposal: Prometheus-Based VPA Recommender

## Status

**Draft** - Target repository TBD

<!-- TOC tocDepth:2..3 chapterDepth:2..6 -->

- [Status](#status)
- [Overview](#overview)
- [Goal](#goal)
- [Target Repository](#target-repository)
- [Comparison with Default VPA Recommender](#comparison-with-default-vpa-recommender)
- [Design](#design)
    - [Architecture](#architecture)
    - [Prometheus Metrics Format](#prometheus-metrics-format)
    - [VPA Configuration](#vpa-configuration)
    - [ResourceClaimStatus](#resourceclaimstatus)
- [Timeline](#timeline)

<!-- /TOC -->

## Overview

Proposal to create a Prometheus-based recommender for Vertical Pod Autoscaler that uses explicitly desired device capacity scaling decision as a recommendation, rather than using the default VPA histogram-based algorithm.

This is useful when you have external systems (ML models, capacity planning tools, GPU schedulers, etc.) that can determine optimal resource allocations.

## Goal

Enable VPA to accept external scaling recommendations from Prometheus-based autoscalers, allowing external sophisticated optimization algorithms to provide resource recommendations while VPA handles the actual pod updates.

## Target Repository

TBD

## Design

How it works:

1. **Watches VPA objects** in the cluster that specify this recommender
2. **Queries Prometheus** for desired capacity metrics per container and device class
3. **Updates VPA status** with recommendations from Prometheus
4. **VPA components** (updater/admission-controller) apply the recommendations

### Architecture

```text
External System → Prometheus Metrics → This Recommender → VPA Status → Pod Resources
                                              ↓
                                    Capacity values per container
                                    (GPU compute, memory, etc.)
```

### Prometheus Metrics Format

The recommender queries Prometheus for the following metrics:

#### Capacity Metrics

```prometheus
# Capacity values for extended resources (GPUs, etc.)
# Format: desired_capacity{deployment="<name>", container="<name>", capacity_name="<device-class>/<capacity>"}
desired_capacity{deployment="<deployment-name>", container="<container-name>", capacity_name="<capacity-name>"} <value>

# Examples:
desired_capacity{deployment="my-app", container="app", capacity_name="vgpu.example.com/compute"} 60
desired_capacity{deployment="my-app", container="app", capacity_name="vgpu.example.com/memory"} 8589934592
```

**Note**: The `capacity_name` should match the format `<deviceClassName>/<capacity>` as defined in the VPA's `resourceClaimPolicies`.

### VPA Configuration

To use this recommender, specify it in your VPA object with `resourceClaimPolicies`:

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: my-app-vpa
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-app

  recommenders:
    - name: prometheus  # Use this custom recommender

   ...

  resourcePolicy:
    containerPolicies:
    - containerName: inference-server
      mode: Off          # optional: disable CPU/memory management
    resourceClaimPolicies:
    - claimTemplateName: gpu-claim-template   # matches pod.spec.resourceClaims[].resourceClaimTemplateName
      deviceClassName: vgpu.example.com       # matches the device class in the template
      minAllowed:
        memory: "8Gi"
        compute: "10"
      maxAllowed:
        memory: "80Gi"
        compute: "100"
      controlledCapacities:
      - memory                                # bare capacity name, as in ResourceClaim spec
      - compute

   ...

  status:
    # Reflect metrics in recommendation field
    recommendation:
      containerRecommendations:
      - containerName: app
        target:
          vgpu.example.com/compute: "60"
          vgpu.example.com/memory: "16442450944"
```

### ResourceClaimStatus

Together with DRA driver, and autoscaler, the result of recommender should be reflected in ResourceClaim as below:

```yaml
  apiVersion: resource.k8s.io/v1
  kind: ResourceClaim
  metadata:
    name: test-app-5c5869ff5c-wcg7d-gpu-claim-zwhx2
  spec:
    devices:
      requests:
      - exactly:
          allocationMode: ExactCount
          capacity:
            requests:
              compute: "60"
              memory: "16442450944"
          count: 1
          deviceClassName: vgpu.example.com
        name: gpu
  status:
    allocation:
      allocationTimestamp: "2026-06-30T04:16:23Z"
      devices:
        results:
        - consumedCapacity:
            compute: "60"
            memory: 16Gi
          device: gpu-0
          driver: vgpu.example.com
          pool: kind-worker
          request: gpu
          shareID: 0199e683-ae91-49ef-9dd4-8a0886df1ec2
```

## Timeline

- 2026-06-30: Finish initial implementation, integration test with enhanced VPA autoscaler, and DRA example driver

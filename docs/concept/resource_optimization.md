# Resource Optimization Concept

## Overview

The Resource Optimizer is a core component of the k8s-resourceclaim-autoscaler that finds optimal GPU resource configurations for LLM inference workloads. It balances performance requirements (latency targets) with resource efficiency by intelligently allocating GPU compute and memory resources across replicas.

## Architecture

The Resource Optimizer consists of three main components working together:

```
┌─────────────────────────────────────────────────────────────┐
│                    Resource Optimizer                       │
│                                                             │
│  ┌────────────────┐  ┌──────────────┐  ┌────────────────┐   │
│  │   Resource     │  │   Latency    │  │     Queue      │   │
│  │   Estimator    │  │  Estimator   │  │   Analyzer     │   │
│  └────────────────┘  └──────────────┘  └────────────────┘   │
│         │                    │                   │          │
│         ▼                    ▼                   ▼          │
│    GPU Resource      Latency Prediction    Queueing Theory  │
│    Requirements      (TTFT, ITL, E2E)      (M/M/c model)    │
└─────────────────────────────────────────────────────────────┘
```

### 1. Resource Estimator

**Purpose**: Calculates GPU resource requirements (compute threads and memory) based on model architecture and workload characteristics.

**Key Responsibilities**:
- Estimate GPU thread occupancy based on model complexity
- Calculate memory requirements (model weights, KV cache, activations)
- Support multiple GPU architectures (H200, H100, A100, L4)
- Adaptive tuning based on observed metrics

**Resource Calculation**:

```
Thread Occupancy = (Active Threads / Max GPU Threads) × 100%

Active Threads = max(Prefill Threads, Decode Threads)
  - Prefill Threads = prefill_tokens × hidden_size
  - Decode Threads = batch_size × hidden_size

Memory Usage = (Model Weights + KV Cache + Activations + Attention) × Overhead
  - Model Weights = parameters × precision / num_gpus
  - KV Cache = batch × tokens × layers × kv_heads × head_dim × precision / num_gpus
  - Activations = prefill_tokens × hidden_size × precision / num_gpus
  - Attention Memory = sublinear scaling for long contexts (>8K tokens)
```

**Supported GPU Specifications**:
- **H200 SXM**: 141GB VRAM, 4.8 TB/s bandwidth, 989 TFLOPS (FP16)
- **H100 SXM**: 80GB VRAM, 3.35 TB/s bandwidth, 989 TFLOPS (FP16)
- **A100 80GB**: 80GB VRAM, 2.0 TB/s bandwidth, 312 TFLOPS (FP16)
- **L4 Tensor Core**: 24GB VRAM, 0.3 TB/s bandwidth, 121 TFLOPS (FP16)

### 2. Latency Estimator

**Purpose**: Predicts inference latency based on model architecture, GPU specifications, and resource allocation.

**Key Metrics**:
- **TTFT (Time To First Token)**: Prefill latency + queue wait time
- **ITL (Inter-Token Latency)**: Time between consecutive output tokens
- **E2E (End-to-End Latency)**: Total request completion time

> [!IMPORTANT]
> Currently, the TTFT in logs is actually "prefill latency", not including queuing time, to align with the metric naming convention used by llm-d-inference-sim. This behavior needs to be fixed in future versions together with the simulator report.

**Latency Components**:

```
Prefill Latency = (Compute Time + KV Read Time + Overhead) × (1 + Allocation Impact) + TTFT Offset
  - Compute Time = 2 × parameters × tokens / (GPU TFLOPS × num_gpus)
  - KV Read Time = kv_cache_bytes × batch / memory_bandwidth
  - Overhead = kernel_launches + kv_management + multi_gpu_sync

Inter-Token Latency = (Weight Read Time + KV Read Time) / num_gpus × (1 + Allocation Impact) + ITL Offset
  - Weight Read Time = model_bytes / memory_bandwidth
  - KV Read Time = kv_cache_bytes × prompt_len × batch / memory_bandwidth

E2E Latency = Queue Wait + TTFT + (ITL × output_tokens)
```

**Prefix Cache Optimization**:
- Reduces prefill latency when prompt prefixes are cached
- Speedup = cache_hit_rate × kv_cache_speedup (default 0.5)

### 3. Queue Analyzer

**Purpose**: Models queueing behavior using Erlang C (M/M/c) queueing theory to predict wait times and system stability.

**Queue Metrics**:

```
Service Rate (μ) = 1 / service_time
Total Capacity = num_replicas × service_rate
Utilization (ρ) = arrival_rate / total_capacity

Probability of Wait = ErlangC(arrival_rate/service_rate, num_replicas)
Queue Wait Time = (P_wait / (service_rate - arrival_rate)) × service_time
Average Queue Length = arrival_rate × queue_wait_time
```

**Stability Condition**: System is stable only when utilization < 1.0 (arrival rate < total capacity)

## Resource Allocation Impact

The optimizer models the performance impact of resource allocation density:

### Under-provisioning (High Density)
When `ratio = required_compute / allocated_compute > 0.8`:
- **Contention Impact**: Resources are over-subscribed, causing slowdowns
- **Formula**: `impact = (ratio - 0.8) × underprovision_contention_factor`
- **Effect**: Increases latency due to resource contention

### Over-provisioning (Low Density)
When `ratio < 0.5`:
- **Speedup Benefit**: Excess resources enable better parallelization
- **Formula**: `speedup = -(1.0 - ratio) × overprovision_speedup_factor`
- **Effect**: Reduces latency due to better resource utilization

Otherwise, no impact (impact=0.0).

## Optimization Algorithm

The optimizer uses a **greedy search with resource squeezing** strategy:

### Search Strategy

```
1. For each replica count (1 to max_replicas):
   2. For each GPU count per replica (1 to max_gpus_per_replica):
      3. Calculate minimum required resources
      4. Start with maximum allowed resources
      5. If configuration meets latency targets:
         a. Try minimum memory with maximum compute
         b. If successful, iteratively reduce compute
         c. Stop when latency targets are violated
      6. Return first valid configuration (minimal resources)
```

### Resource Constraints

**GPU Constraints**:
- `MinThreadPercentage`: Minimum GPU compute allocation (default: 1%)
- `MaxThreadPercentage`: Maximum GPU compute allocation (default: 100%)
- `StepThreadPercentage`: Compute reduction step size (default: 1%)
- `MinMemoryBytes`: Minimum GPU memory per GPU (default: 8GB)
- `MaxMemoryBytes`: Maximum GPU memory per GPU (default: 80GB)
- `StepMemoryBytes`: Memory reduction step size (default: 1GB)
- `MaxGPUsPerReplica`: Maximum GPUs per replica (default: 8)
- `TotalGPUs`: Total GPU budget across all replicas (0 = unlimited)

### Optimization Goals

1. **Meet Latency Targets**: All configurations must satisfy E2E, TTFT, and ITL targets
2. **Minimize Resources**: Find the smallest resource allocation that meets targets
3. **Prefer Vertical Scaling**: Try more GPUs per replica before adding replicas
4. **Ensure Stability**: Utilization must be < 1.0 to avoid queue instability

## Configuration Output

The optimizer returns an `OptimalConfiguration` containing:

```go
type OptimalConfiguration struct {
    // Resource allocation
    Replicas          int     // Number of replicas
    GPUsPerReplica    int     // GPUs allocated per replica
    RequestedCompute  float64 // Compute percentage per GPU
    RequestedMemoryGB float64 // Memory in GB per GPU
    TotalGPUs         int     // Total GPUs used
    
    // Resource requirements (working set)
    ThreadOccupancy   float64 // Actual thread utilization
    MemoryGB          float64 // Actual memory usage
    
    // Performance metrics
    EstimatedLatency  LatencyResult
    MeetsConstraints  bool
}
```

## Adaptive Tuning

The optimizer supports runtime tuning based on observed metrics:

### Compute Tuning
```go
TuneWithMetrics(
    prefillTokens,
    promptLen,
    outputTokens,
    cachedHitRatio,
    computeThreadUtilization,  // Observed GPU utilization
    measuredMemoryUsage,       // Observed memory usage
    numGPU
)
```

**Adjustments**:

- **GPU MaxThreads**: Calibrated when GPU type is unknown
- **Memory Overhead Factor**: Adjusted based on measured vs. estimated memory (capped at 1.3x)
- **Model Parameters**: Tuned when model architecture is unknown
- **Latency Offsets**: Applied to correct systematic estimation errors

### Latency Offset Tuning
```go
SetLatencyOffsets(
    itlOffset,   // ITL correction in ms
    ttftOffset   // TTFT correction in ms
)
```

Offsets are calculated from previous measurements:
```
itlOffset = measured_ITL - calculated_ITL
ttftOffset = measured_TTFT - calculated_TTFT
```

## Example Optimization Flow

```
Input:
  - Model: llama-3-8b
  - GPU: H100 SXM
  - Arrival Rate: 10 req/s
  - Avg Prompt: 512 tokens
  - Avg Output: 128 tokens
  - Latency Targets: E2E < 2000ms, TTFT < 500ms, ITL < 50ms

Step 1: Resource Estimator
  → Required: 45% compute, 25GB memory per GPU

Step 2: Try Configuration (1 replica, 1 GPU, 100% compute, 80GB memory)
  → Latency Estimator: TTFT=450ms, ITL=45ms
  → Queue Analyzer: E2E=1850ms, utilization=0.85
  → Result: ✓ Meets targets

Step 3: Squeeze Resources (1 replica, 1 GPU, 100% compute, 25GB memory)
  → Result: ✓ Meets targets

Step 4: Squeeze Compute (1 replica, 1 GPU, 50% compute, 25GB memory)
  → Latency Estimator: TTFT=480ms, ITL=48ms
  → Result: ✓ Meets targets

Step 5: Squeeze Compute (1 replica, 1 GPU, 45% compute, 25GB memory)
  → Latency Estimator: TTFT=495ms, ITL=49ms
  → Result: ✓ Meets targets

Step 6: Squeeze Compute (1 replica, 1 GPU, 44% compute, 25GB memory)
  → Latency Estimator: TTFT=510ms, ITL=51ms
  → Result: ✗ Violates TTFT target

Output: Optimal Configuration
  - Replicas: 1
  - GPUs per Replica: 1
  - Compute: 45%
  - Memory: 25GB
  - Estimated E2E: 1900ms
  - Estimated TTFT: 495ms
  - Estimated ITL: 49ms
```

## Key Design Principles

1. **Physics-Based Modeling**: Uses GPU hardware specifications and model architecture for accurate predictions
2. **Queueing Theory**: Applies M/M/c model for realistic multi-server queueing behavior
3. **Adaptive Learning**: Continuously tunes parameters based on observed metrics
4. **Resource Efficiency**: Minimizes resource allocation while meeting performance targets
5. **Stability Guarantees**: Ensures system utilization stays below 100% to prevent queue buildup
6. **Multi-GPU Support**: Handles tensor parallelism across multiple GPUs per replica
7. **Cache-Aware**: Accounts for prefix caching and KV cache optimizations

## Limitations and Future Work

**Current Limitations**:
- Assumes homogeneous workload (single model, uniform request patterns)
- Simplified thread occupancy model (prefill and decode treated as additive)
- Does not model pipeline parallelism or expert parallelism (MoE)
- Queue model assumes exponential service times (M/M/c)

**Future Enhancements**:
- Support for heterogeneous workloads and multi-model serving
- More sophisticated GPU kernel scheduling models
- Integration with real-time performance monitoring
- Support for speculative decoding and other advanced inference techniques
- Dynamic reoptimization based on workload drift

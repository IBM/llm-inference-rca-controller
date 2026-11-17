# Resource Estimator

Estimates how much hardware capacity (memory, compute, bandwidth) an LLM inference workload needs, so a controller can right-size it instead of over-allocating whole devices.

- **Estimate**: predict per-capacity demand for a workload on a given hardware (GPU is the reference domain; the framework is hardware-agnostic so NPU, TPU, CPU, etc. can plug in) :construction:
- **Calibrate**: correct the hardware model from observed metrics (vLLM, DCGM) :construction:
- **Recommend**: suggest a resource allocation that meets a given SLO :construction:
- **Evaluate**: validate the estimates against measurements (see [`evaluation/`](evaluation/README.md)) :construction:

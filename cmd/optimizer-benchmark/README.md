# Optimizer Benchmark Tool

This tool runs variations of the resource optimizer with different arrival rates, output tokens, and input tokens to analyze performance characteristics.

## Usage

### Basic Usage

Run with default parameters using the demo spec:

```bash
go run cmd/optimizer-benchmark/main.go
```

### Custom Spec File

Use a custom ResourceClaimAutoscaler YAML spec:

```bash
go run cmd/optimizer-benchmark/main.go -spec demo/vllm_rca_basic.yaml
```

### Custom Parameters

```bash
go run cmd/optimizer-benchmark/main.go \
  -spec demo/vllm_rca_basic.yaml \
  -input-tokens "50,100,200" \
  -arrival-rates "0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9,1.0" \
  -output-tokens "50,100,200,300,400" \
  -output optimizer_results.csv
```

This will test all combinations: 10 arrival rates × 3 input tokens × 5 output tokens = 150 tests

### Override Latency Targets

Override the latency targets from the spec file:

```bash
go run cmd/optimizer-benchmark/main.go \
  -spec demo/vllm_rca_basic.yaml \
  -e2e-target 1000 \
  -ttft-target 200 \
  -itl-target 20
```

## Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-spec` | `demo/vllm_rca_basic.yaml` | Path to ResourceClaimAutoscaler YAML spec file |
| `-input-tokens` | `"50"` | Comma-separated input token counts (prompt lengths) |
| `-output-tokens` | `"50,100,200,300,400"` | Comma-separated output token counts |
| `-arrival-rates` | `"0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9,1.0"` | Comma-separated arrival rates (requests/sec) |
| `-output` | `optimizer_results.csv` | Output CSV file path |
| `-e2e-target` | `-1` | E2E latency target in ms (overrides spec, -1 = use spec) |
| `-ttft-target` | `-1` | TTFT latency target in ms (overrides spec, -1 = use spec) |
| `-itl-target` | `-1` | ITL latency target in ms (overrides spec, -1 = use spec) |

## Output Format

The tool generates a CSV file with the following columns:

- `arrival_rate`: Request arrival rate (requests/sec)
- `input_tokens`: Number of input tokens (prompt length)
- `output_tokens`: Number of output tokens
- `replicas`: Optimal number of replicas
- `gpus_per_replica`: GPUs allocated per replica
- `total_gpus`: Total GPUs used (replicas × gpus_per_replica)
- `requested_compute_pct`: Requested compute percentage per GPU
- `requested_memory_gb`: Requested memory in GB per GPU
- `thread_utilization_pct`: Estimated thread utilization percentage
- `memory_usage_gb`: Estimated memory usage in GB
- `e2e_latency_ms`: End-to-end latency in milliseconds
- `ttft_latency_ms`: Time to first token latency in milliseconds
- `itl_latency_ms`: Inter-token latency in milliseconds
- `queue_wait_time_ms`: Queue waiting time in milliseconds
- `service_time_ms`: Service time in milliseconds
- `allocation_impact_pct`: Resource allocation impact percentage
- `meets_constraints`: Whether configuration meets all constraints (true/false)
- `error`: Error message if optimization failed

## Example

Run a comprehensive benchmark:

```bash
go run cmd/optimizer-benchmark/main.go \
  -spec demo/vllm_rca_basic.yaml \
  -input-tokens "50,100,200" \
  -arrival-rates "0.1,0.5,1.0,2.0,5.0" \
  -output-tokens "50,100,200,500,1000" \
  -output results/comprehensive_benchmark.csv \
  -v=2
```

This will test 75 combinations (5 arrival rates × 3 input tokens × 5 output tokens) and save results to `results/comprehensive_benchmark.csv`.

## Analyzing Results

You can analyze the results using the plotting scripts:

```bash
python scripts/plot_optimizer_results.py results/comprehensive_benchmark.csv
```

Or use any data analysis tool that can read CSV files (Excel, pandas, R, etc.).

## Notes

- The tool uses the ResourceClaimAutoscaler spec to configure the optimizer, including:
  - Model parameters (from `spec.target.hint`)
  - GPU constraints (from `spec.constraints`)
  - Latency targets (from `spec.targetLatency`, can be overridden via flags)
- Each test runs independently, so the tool can be interrupted and resumed
- Results are written incrementally to the CSV file
- Use `-v=2` or higher for verbose logging
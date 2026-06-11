package optimizer

import "fmt"

type LatencyResult struct {
	ITL              float64 // Inter-token latency (ms)
	Prefill          float64 // Prefill phase latency (ms)
	Decode           float64 // Decode phase latency (ms)
	KVCacheReduction float64 // Latency reduction from cache (ms)

	TTFT               float64 // Time to first token (ms)
	E2E                float64 // End-to-end latency (ms)
	Utilization        float64 // System utilization
	ServiceTimeSeconds float64 // Service time in seconds
	QueueWaitTime      float64 // Queue wait time (ms)
	AvgQueueLength     float64
	AvgSystemLength    float64
	ProbabilityOfWait  float64
	AllocationImpact   float64
}

func (r *LatencyResult) ServiceTime() float64 {
	return r.Prefill + r.Decode
}

// String returns a formatted summary of the estimate
func (r *LatencyResult) String() string {
	return fmt.Sprintf(`
Latency Estimate:
  Total Latency (E2E):  %.2f ms
  TTFT:                 %.2f ms
  ITL:                  %.2f ms
  Queue Wait Time:      %.2f ms
  Service Time:         %.2f s
    - Prefill:          %.2f ms
    - Decode:           %.2f ms
    - KV Cache Savings: %.2f ms
  
  System Metrics:
  Utilization:          %.1f%%
  AllocationImpact:     %.2f%%
  Avg Queue Length:     %.2f requests
  Avg System Length:    %.2f requests
  Probability of Wait:  %.1f%%`,
		r.E2E,
		r.TTFT,
		r.ITL,
		r.QueueWaitTime,
		r.ServiceTimeSeconds,
		r.Prefill,
		r.Decode,
		r.KVCacheReduction,
		r.Utilization*100,
		r.AllocationImpact,
		r.AvgQueueLength,
		r.AvgSystemLength,
		r.ProbabilityOfWait*100)
}

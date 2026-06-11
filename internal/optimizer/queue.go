package optimizer

import (
	"fmt"
	"math"
)

type QueueAnalyzer struct {
	arrivalRate float64 // Requests per second
}

func NewQueueAnalyzer(arrivalRate float64) *QueueAnalyzer {
	return &QueueAnalyzer{
		arrivalRate: arrivalRate,
	}
}

func (a *QueueAnalyzer) AddQueuingResults(numServers int, results *LatencyResult) error {

	serviceTimeMS := results.ServiceTime()
	serviceTimeSeconds := serviceTimeMS / 1000.0
	results.ServiceTimeSeconds = serviceTimeSeconds

	prefillLatencyMilliSeconds := results.Prefill
	// Calculate service rate (req/s)
	serviceRate := 1.0 / serviceTimeSeconds

	// Calculate utilization
	totalServiceRate := float64(numServers) * serviceRate
	utilization := a.arrivalRate / totalServiceRate

	if utilization >= 1.0 {
		return fmt.Errorf("system is unstable: utilization %.2f >= 1.0 (arrival rate %.2f req/s exceeds capacity %.2f req/s)",
			utilization, a.arrivalRate, totalServiceRate)
	}
	results.Utilization = utilization

	rho := a.arrivalRate / serviceRate
	probabilityOfWait := erlangC(rho, float64(numServers))

	queueWaitTime := 0.0
	// Calculate queue wait time
	if probabilityOfWait > 0 {
		queueWaitSeconds := (probabilityOfWait / (serviceRate - a.arrivalRate)) * serviceTimeSeconds
		queueWaitTime = queueWaitSeconds * 1000.0
		results.AvgQueueLength = a.arrivalRate * queueWaitSeconds
		results.AvgSystemLength = a.arrivalRate * (queueWaitSeconds + serviceTimeSeconds)
		results.ProbabilityOfWait = probabilityOfWait
	}

	// Calculate total latency
	//
	// Should be changed to the following computation once the bug in llm-d-sim fixed:
	// results.TTFT = queueWaitTime + prefillLatencyMilliSeconds
	// results.E2E = results.TTFT + results.Decode
	results.TTFT = prefillLatencyMilliSeconds
	results.E2E = queueWaitTime + results.TTFT + results.Decode
	return nil
}

// erlangC calculates the Erlang C formula (probability all servers busy)
func erlangC(rho, c float64) float64 {
	if rho >= c {
		return 1.0
	}

	numerator := math.Pow(rho, c) / factorial(int(c))
	sum := 0.0
	for k := 0; k < int(c); k++ {
		sum += math.Pow(rho, float64(k)) / factorial(k)
	}
	denominator := sum + (numerator / (1.0 - rho/c))

	return numerator / denominator
}

// factorial calculates n!
func factorial(n int) float64 {
	if n <= 1 {
		return 1.0
	}
	result := 1.0
	for i := 2; i <= n; i++ {
		result *= float64(i)
	}
	return result
}

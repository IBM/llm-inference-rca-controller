package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/optimizer"
)

var (
	specFile        = flag.String("spec", "demo/vllm_rca_basic.yaml", "Path to ResourceClaimAutoscaler YAML spec file")
	inputTokensStr  = flag.String("input-tokens", "10,20,30,40,50,60,70,80,90,100", "Comma-separated input token counts (prompt lengths)")
	outputCSV       = flag.String("output", "optimizer_results.csv", "Output CSV file")
	e2eTargetMs     = flag.Int("e2e-target", -1, "E2E latency target in ms (overrides spec, -1 = use spec)")
	ttftTargetMs    = flag.Int("ttft-target", -1, "TTFT latency target in ms (overrides spec, -1 = use spec)")
	itlTargetMs     = flag.Int("itl-target", -1, "ITL latency target in ms (overrides spec, -1 = use spec)")
	arrivalRatesStr = flag.String("arrival-rates", "0.25,0.5,0.75,1.0,1.25,1.5,1.75,2.0", "Comma-separated arrival rates")
	outputTokensStr = flag.String("output-tokens", "50,100,200,300,400,500", "Comma-separated output token counts")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Set up controller-runtime logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	logger := ctrl.Log.WithName("optimizer-benchmark")

	// Load spec from YAML file
	spec, err := loadSpecFromYAML(*specFile)
	if err != nil {
		logger.Error(err, "Failed to load spec from YAML", "file", *specFile)
		os.Exit(1)
	}

	logger.Info("Loaded spec from YAML",
		"file", *specFile,
		"model", spec.Target.Hint.ModelID,
		"gpu", spec.Target.Hint.GPUName)

	// Parse arrival rates
	arrivalRates, err := parseFloatSlice(*arrivalRatesStr)
	if err != nil {
		logger.Error(err, "Failed to parse arrival rates")
		os.Exit(1)
	}

	// Parse input tokens
	inputTokensList, err := parseIntSlice(*inputTokensStr)
	if err != nil {
		logger.Error(err, "Failed to parse input tokens")
		os.Exit(1)
	}

	// Parse output tokens
	outputTokensList, err := parseIntSlice(*outputTokensStr)
	if err != nil {
		logger.Error(err, "Failed to parse output tokens")
		os.Exit(1)
	}

	// Create CSV file
	file, err := os.Create(*outputCSV)
	if err != nil {
		logger.Error(err, "Failed to create CSV file")
		os.Exit(1)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{
		"arrival_rate",
		"input_tokens",
		"output_tokens",
		"replicas",
		"gpus_per_replica",
		"total_gpus",
		"requested_compute_pct",
		"requested_memory_gb",
		"thread_utilization_pct",
		"memory_usage_gb",
		"e2e_latency_ms",
		"ttft_latency_ms",
		"itl_latency_ms",
		"queue_wait_time_ms",
		"service_time_ms",
		"allocation_impact_pct",
		"meets_constraints",
		"error",
	}
	if err := writer.Write(header); err != nil {
		logger.Error(err, "Failed to write CSV header")
		os.Exit(1)
	}

	// Run variations
	totalTests := len(arrivalRates) * len(inputTokensList) * len(outputTokensList)
	currentTest := 0

	for _, arrivalRate := range arrivalRates {
		for _, inputTokens := range inputTokensList {
			for _, outputTokens := range outputTokensList {
				currentTest++
				logger.Info("Running test",
					"progress", fmt.Sprintf("%d/%d", currentTest, totalTests),
					"arrivalRate", arrivalRate,
					"inputTokens", inputTokens,
					"outputTokens", outputTokens)

				// Create optimizer
				optimizer := optimizer.NewResourceOptimizer(spec, logger)

				// Set workload parameters
				optimizer.SetWorkloadParameters(arrivalRate, inputTokens, outputTokens, 10)

				// Create latency targets (use command-line overrides or spec values)
				targets := createLatencyTargets(spec)

				// Find optimal configuration
				config, err := optimizer.FindOptimalConfiguration(arrivalRate, targets)

				// Write result
				row := []string{
					fmt.Sprintf("%.1f", arrivalRate),
					strconv.Itoa(inputTokens),
					strconv.Itoa(outputTokens),
				}

				if err != nil {
					// Write error row
					row = append(row, "", "", "", "", "", "", "", "", "", "", "", "", "", "false", err.Error())
				} else {
					// Write successful configuration
					row = append(row,
						strconv.Itoa(config.Replicas),
						strconv.Itoa(config.GPUsPerReplica),
						strconv.Itoa(config.TotalGPUs),
						fmt.Sprintf("%.1f", config.RequestedCompute),
						fmt.Sprintf("%.2f", config.RequestedMemoryGB),
						fmt.Sprintf("%.1f", config.ResourceRequirements.ThreadOccupancy),
						fmt.Sprintf("%.2f", config.ResourceRequirements.MemoryGB),
						fmt.Sprintf("%.2f", config.EstimatedLatency.E2E),
						fmt.Sprintf("%.2f", config.EstimatedLatency.TTFT),
						fmt.Sprintf("%.2f", config.EstimatedLatency.ITL),
						fmt.Sprintf("%.2f", config.EstimatedLatency.QueueWaitTime),
						fmt.Sprintf("%.2f", config.EstimatedLatency.ServiceTime()),
						fmt.Sprintf("%.2f", config.EstimatedLatency.AllocationImpact),
						strconv.FormatBool(config.MeetsConstraints),
						"",
					)
				}

				if err := writer.Write(row); err != nil {
					logger.Error(err, "Failed to write CSV row")
				}
				writer.Flush()
			}
		}
	}

	logger.Info("Benchmark complete", "output", *outputCSV)
}

func loadSpecFromYAML(filename string) (*autoscalingv1alpha1.ResourceClaimAutoscalerSpec, error) {
	// Read YAML file
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Create scheme and decoder
	scheme := runtime.NewScheme()
	if err := autoscalingv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add to scheme: %w", err)
	}

	codecFactory := serializer.NewCodecFactory(scheme)
	decoder := codecFactory.UniversalDeserializer()

	// Decode YAML
	obj, _, err := decoder.Decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decode YAML: %w", err)
	}

	// Type assert to ResourceClaimAutoscaler
	rca, ok := obj.(*autoscalingv1alpha1.ResourceClaimAutoscaler)
	if !ok {
		return nil, fmt.Errorf("decoded object is not a ResourceClaimAutoscaler")
	}

	return &rca.Spec, nil
}

func createLatencyTargets(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) *optimizer.LatencyTargets {
	targets := &optimizer.LatencyTargets{}

	// Use command-line flags if provided, otherwise use spec values
	if *e2eTargetMs > 0 {
		e2e := float64(*e2eTargetMs)
		targets.E2E = &e2e
	} else if spec.TargetLatency != nil && spec.TargetLatency.EndToEndLatencyMilliseconds != nil {
		e2e := float64(*spec.TargetLatency.EndToEndLatencyMilliseconds)
		targets.E2E = &e2e
	}

	if *ttftTargetMs > 0 {
		ttft := float64(*ttftTargetMs)
		targets.TTFT = &ttft
	} else if spec.TargetLatency != nil && spec.TargetLatency.TimeToFirstTokenLatencyMilliseconds != nil {
		ttft := float64(*spec.TargetLatency.TimeToFirstTokenLatencyMilliseconds)
		targets.TTFT = &ttft
	}

	if *itlTargetMs > 0 {
		itl := float64(*itlTargetMs)
		targets.ITL = &itl
	} else if spec.TargetLatency != nil && spec.TargetLatency.InterTokenLatencyMilliseconds != nil {
		itl := float64(*spec.TargetLatency.InterTokenLatencyMilliseconds)
		targets.ITL = &itl
	}

	return targets
}

func parseFloatSlice(s string) ([]float64, error) {
	var result []float64
	var current string

	for _, c := range s {
		if c == ',' {
			if current != "" {
				val, err := strconv.ParseFloat(current, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid float value '%s': %w", current, err)
				}
				result = append(result, val)
				current = ""
			}
		} else {
			current += string(c)
		}
	}

	// Handle last value
	if current != "" {
		val, err := strconv.ParseFloat(current, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float value '%s': %w", current, err)
		}
		result = append(result, val)
	}

	return result, nil
}

func parseIntSlice(s string) ([]int, error) {
	var result []int
	var current string

	for _, c := range s {
		if c == ',' {
			if current != "" {
				val, err := strconv.Atoi(current)
				if err != nil {
					return nil, fmt.Errorf("invalid int value '%s': %w", current, err)
				}
				result = append(result, val)
				current = ""
			}
		} else {
			current += string(c)
		}
	}

	// Handle last value
	if current != "" {
		val, err := strconv.Atoi(current)
		if err != nil {
			return nil, fmt.Errorf("invalid int value '%s': %w", current, err)
		}
		result = append(result, val)
	}

	return result, nil
}

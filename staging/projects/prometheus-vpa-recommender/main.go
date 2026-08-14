package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/klog/v2"

	"github.com/yourusername/prometheus-vpa-recommender/pkg/config"
	"github.com/yourusername/prometheus-vpa-recommender/pkg/recommender"
)

func main() {
	// Initialize klog
	klog.InitFlags(nil)
	defer klog.Flush()

	// Parse configuration
	cfg := config.NewConfig()
	cfg.AddFlags(flag.CommandLine)
	flag.Parse()

	klog.InfoS("Starting Prometheus VPA Recommender",
		"recommenderName", cfg.RecommenderName,
		"prometheusURL", cfg.PrometheusURL,
		"updateInterval", cfg.UpdateInterval,
		"metricsPort", cfg.MetricsPort,
		"capacityMetricName", cfg.CapacityMetricName,
	)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		klog.InfoS("Received shutdown signal", "signal", sig)
		cancel()
	}()

	// Start health/metrics server
	healthServer := startHealthServer(cfg.MetricsPort)
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := healthServer.Shutdown(shutdownCtx); err != nil {
			klog.ErrorS(err, "Failed to shutdown health server")
		}
	}()

	// Create and run recommender
	rec, err := recommender.New(cfg)
	if err != nil {
		klog.ErrorS(err, "Failed to create recommender")
		os.Exit(1)
	}

	if err := rec.Run(ctx); err != nil {
		klog.ErrorS(err, "Recommender failed")
		os.Exit(1)
	}

	klog.InfoS("Recommender stopped gracefully")
}

// startHealthServer starts an HTTP server for health checks and metrics
func startHealthServer(port int) *http.Server {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Readiness check endpoint
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		klog.InfoS("Starting health server", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.ErrorS(err, "Health server failed")
		}
	}()

	return server
}

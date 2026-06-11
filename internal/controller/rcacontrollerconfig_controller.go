/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/monitor"
)

// RCAControllerConfigReconciler reconciles a RCAControllerConfig object
type RCAControllerConfigReconciler struct {
	client.Client
	Scheme                            *runtime.Scheme
	monitor                           *monitor.Monitor
	currentLogLevel                   autoscalingv1alpha1.LogLevel
	currentEndpoint                   string
	ResourceClaimAutoscalerReconciler *ResourceClaimAutoscalerReconciler
}

// +kubebuilder:rbac:groups=autoscaling.x-llmd.ai,resources=rcacontrollerconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.x-llmd.ai,resources=rcacontrollerconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoscaling.x-llmd.ai,resources=rcacontrollerconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *RCAControllerConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling RCAControllerConfig", "name", req.Name, "namespace", req.Namespace)

	// Fetch the RCAControllerConfig instance
	config := &autoscalingv1alpha1.RCAControllerConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RCAControllerConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get RCAControllerConfig")
		return ctrl.Result{}, err
	}

	logger.Info("Found RCAControllerConfig",
		"endpoint", config.Spec.Monitoring.Endpoint,
		"generation", config.Generation,
		"resourceVersion", config.ResourceVersion)

	// Setup Monitoring if monitor is nil or endpoint changed
	if r.monitor == nil || r.currentEndpoint != config.Spec.Monitoring.Endpoint {
		if r.monitor == nil {
			logger.Info("Initializing monitor for the first time")
		} else {
			logger.Info("Monitoring endpoint changed, setting up new monitor",
				"old", r.currentEndpoint,
				"new", config.Spec.Monitoring.Endpoint)
		}
		if err := r.setupMonitoring(ctx, config); err != nil {
			logger.Error(err, "Failed to setup monitoring")
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		r.currentEndpoint = config.Spec.Monitoring.Endpoint
	}

	// Setup Logging
	r.setupLogging(ctx, config)

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// setupMonitoring initializes the monitor and validates the endpoint and metrics
func (r *RCAControllerConfigReconciler) setupMonitoring(ctx context.Context, config *autoscalingv1alpha1.RCAControllerConfig) error {
	logger := log.FromContext(ctx)

	// Create or update monitor with current config
	r.monitor = monitor.NewMonitor(config.Spec.Monitoring)

	// Share the monitor with ResourceClaimAutoscaler controller
	if r.ResourceClaimAutoscalerReconciler != nil {
		r.ResourceClaimAutoscalerReconciler.SetMonitor(r.monitor)
		logger.Info("Shared monitor with ResourceClaimAutoscaler controller")
	}

	// Initialize status if needed
	if config.Status.MonitoringStatus == nil {
		config.Status.MonitoringStatus = &autoscalingv1alpha1.MonitoringStatus{}
	}

	// Validate metrics using the monitor
	metricHealths, err := r.monitor.ValidateMetrics()
	if err != nil {
		logger.Error(err, "Failed to validate metrics")
		config.Status.MonitoringStatus.Connected = false
		config.Status.MonitoringStatus.Message = fmt.Sprintf("Failed to validate: %v", err)
		config.Status.MonitoringStatus.MetricHealths = nil
		return r.updateStatus(ctx, config)
	}

	config.Status.MonitoringStatus.Connected = true
	config.Status.MonitoringStatus.Message = "Connected successfully"
	config.Status.MonitoringStatus.MetricHealths = metricHealths

	// Update conditions based on metric health
	r.updateConditions(config, metricHealths)

	return r.updateStatus(ctx, config)
}

// updateConditions updates the conditions based on metric health
func (r *RCAControllerConfigReconciler) updateConditions(config *autoscalingv1alpha1.RCAControllerConfig, metricHealths []autoscalingv1alpha1.MetricHealth) {
	allHealthy := true
	unhealthyMetrics := []string{}

	for _, health := range metricHealths {
		if !health.Healthy {
			allHealthy = false
			unhealthyMetrics = append(unhealthyMetrics, health.Name)
		}
	}

	condition := metav1.Condition{
		Type:               "MetricsAvailable",
		ObservedGeneration: config.Generation,
		LastTransitionTime: metav1.Now(),
	}

	if allHealthy {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "AllMetricsAvailable"
		condition.Message = "All required metrics are available"
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "MetricsMissing"
		condition.Message = fmt.Sprintf("Missing metrics: %s", strings.Join(unhealthyMetrics, ", "))
	}

	// Update or append condition
	found := false
	for i, c := range config.Status.Conditions {
		if c.Type == condition.Type {
			config.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		config.Status.Conditions = append(config.Status.Conditions, condition)
	}
}

// setupLogging configures the operator's log level
func (r *RCAControllerConfigReconciler) setupLogging(ctx context.Context, config *autoscalingv1alpha1.RCAControllerConfig) {
	logger := log.FromContext(ctx)

	logLevel := autoscalingv1alpha1.LogLevelInfo
	if config.Spec.Logging != nil && config.Spec.Logging.Level != "" {
		logLevel = config.Spec.Logging.Level
	}

	// Only update if log level has changed
	if r.currentLogLevel != logLevel {
		logger.Info("Updating log level", "from", r.currentLogLevel, "to", logLevel)

		// Map our log level to zap level
		var zapLevel zapcore.Level
		switch logLevel {
		case autoscalingv1alpha1.LogLevelDebug:
			zapLevel = zapcore.DebugLevel
		case autoscalingv1alpha1.LogLevelInfo:
			zapLevel = zapcore.InfoLevel
		case autoscalingv1alpha1.LogLevelWarn:
			zapLevel = zapcore.WarnLevel
		case autoscalingv1alpha1.LogLevelError:
			zapLevel = zapcore.ErrorLevel
		default:
			zapLevel = zapcore.InfoLevel
		}

		// Update the logger
		ctrl.SetLogger(zap.New(zap.Level(zapLevel)))
		r.currentLogLevel = logLevel

		logger.Info("Log level updated successfully", "level", logLevel)
	}
}

// updateStatus updates the status of the RCAControllerConfig
func (r *RCAControllerConfigReconciler) updateStatus(ctx context.Context, config *autoscalingv1alpha1.RCAControllerConfig) error {
	if err := r.Status().Update(ctx, config); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RCAControllerConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&autoscalingv1alpha1.RCAControllerConfig{}).
		Named("rcacontrollerconfig").
		Complete(r)
}

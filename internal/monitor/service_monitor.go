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

package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
)

// MetricsResult contains the result of metrics collection and analysis
type MetricsResult struct {
	// Message provides additional context about the metrics
	Message string
	// Metrics contains the raw metrics collected from Prometheus
	Metrics map[string]string
}

// MetricsCollector defines the interface for collecting metrics and determining scaling actions
type MetricsCollector interface {
	// CollectAndCompute collects metrics and determines which trigger should be used
	// lastTriggerTime is the time of the last trigger event, used to calculate lookback window
	// Returns MetricsResult containing violation probability and recommended trigger type
	CollectAndCompute(ctx context.Context, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, lastTriggerTime time.Time) (*MetricsResult, error)
}

// TriggerEvent represents a monitoring trigger event
type TriggerEvent struct {
	AutoscalerName string
	Namespace      string
	// CachedSpec contains the cached autoscaler spec to reduce API server load
	CachedSpec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec
	// MetricsResult contains the metrics analysis result
	MetricsResult *MetricsResult
}

// ScalingActionCallback is called when scaling action is needed
// metrics parameter contains the raw metrics collected from Prometheus
type ScalingActionCallback func(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, result *MetricsResult)

// ServiceMonitor manages monitoring events for ResourceClaimAutoscaler resources using channels
type ServiceMonitor interface {
	// RegisterWithCollector registers a new monitoring event with a metrics collector
	// ServiceMonitor will manage the monitoring loop internally and call the collector on each trigger
	// Accepts full Behavior with both ScaleUp and ScaleDown triggers and the autoscaler spec
	// Performs initial metric collection to determine the starting trigger
	RegisterWithCollector(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, behavior *autoscalingv1alpha1.Behavior, collector MetricsCollector) error

	// IsRegistered checks if an autoscaler is already registered for monitoring
	IsRegistered(autoscalerName, namespace string) bool

	// UpdateTriggerTime updates the trigger time for a registered autoscaler
	// Uses static window from the current trigger configuration
	UpdateTriggerTime(ctx context.Context, autoscalerName, namespace string) (<-chan TriggerEvent, error)

	// UpdateCachedSpec updates the cached spec for a registered autoscaler
	UpdateCachedSpec(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) error

	// Unregister removes a monitoring event for a ResourceClaimAutoscaler
	Unregister(ctx context.Context, autoscalerName, namespace string) error

	// SetScalingActionCallback sets the callback for scaling actions
	SetScalingActionCallback(callback ScalingActionCallback)

	// Start starts the service monitor (must be called before use)
	Start(ctx context.Context) error

	// Stop stops the service monitor
	Stop()
}

// MonitoringEvent represents a scheduled monitoring event for an autoscaler
type MonitoringEvent struct {
	AutoscalerName   string
	Namespace        string
	ScaleUpTrigger   autoscalingv1alpha1.ScalingTrigger
	ScaleDownTrigger autoscalingv1alpha1.ScalingTrigger
	cachedSpec       *autoscalingv1alpha1.ResourceClaimAutoscalerSpec // Cached spec to reduce API server load
	specMutex        sync.RWMutex                                     // Protects cachedSpec from concurrent access
	collector        MetricsCollector                                 // Metrics collector for this autoscaler
	lastTriggerTime  time.Time                                        // Time of last trigger event for lookback calculation
	timer            *time.Timer
	triggerChan      chan TriggerEvent
	monitorCancel    context.CancelFunc // Cancels the monitoring goroutine
	cancelFunc       context.CancelFunc // Cancels the timer
}

// getCachedSpec safely retrieves the cached spec
func (me *MonitoringEvent) getCachedSpec() *autoscalingv1alpha1.ResourceClaimAutoscalerSpec {
	me.specMutex.RLock()
	defer me.specMutex.RUnlock()
	return me.cachedSpec
}

// setCachedSpec safely sets the cached spec
func (me *MonitoringEvent) setCachedSpec(spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) {
	me.specMutex.Lock()
	defer me.specMutex.Unlock()
	me.cachedSpec = spec
}

// serviceMonitor implements ServiceMonitor interface using channels
type serviceMonitor struct {
	events                map[string]*MonitoringEvent // key: namespace/name
	mu                    sync.RWMutex
	logger                logr.Logger
	ctx                   context.Context
	cancel                context.CancelFunc
	scalingActionCallback ScalingActionCallback
	callbackMutex         sync.RWMutex
}

// NewServiceMonitor creates a new ServiceMonitor instance
func NewServiceMonitor() ServiceMonitor {
	return &serviceMonitor{
		events: make(map[string]*MonitoringEvent),
		logger: ctrl.Log.WithName("service-monitor"),
	}
}

// Start starts the service monitor
func (sm *serviceMonitor) Start(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.ctx != nil {
		return fmt.Errorf("service monitor already started")
	}

	sm.ctx, sm.cancel = context.WithCancel(ctx)
	sm.logger.Info("Service monitor started")
	return nil
}

// Stop stops the service monitor
func (sm *serviceMonitor) Stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if already stopped
	if sm.ctx == nil {
		return
	}

	if sm.cancel != nil {
		sm.cancel()
		sm.cancel = nil
		sm.ctx = nil
	}

	// Cancel all active timers
	for key, event := range sm.events {
		if event.timer != nil {
			event.timer.Stop()
		}
		if event.cancelFunc != nil {
			event.cancelFunc()
		}
		if event.triggerChan != nil {
			close(event.triggerChan)
		}
		delete(sm.events, key)
	}

	sm.logger.Info("Service monitor stopped")
}

// IsRegistered checks if an autoscaler is already registered for monitoring
func (sm *serviceMonitor) IsRegistered(autoscalerName, namespace string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	key := namespace + "/" + autoscalerName
	_, exists := sm.events[key]
	return exists
}

// RegisterWithCollector registers a new monitoring event with a metrics collector
// ServiceMonitor manages the monitoring loop internally
// Performs initial metric collection to determine the starting trigger
func (sm *serviceMonitor) RegisterWithCollector(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, behavior *autoscalingv1alpha1.Behavior, collector MetricsCollector) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := namespace + "/" + autoscalerName

	// Cancel existing event if present
	if existing, exists := sm.events[key]; exists {
		if existing.timer != nil {
			existing.timer.Stop()
		}
		if existing.monitorCancel != nil {
			existing.monitorCancel()
		}
		if existing.cancelFunc != nil {
			existing.cancelFunc()
		}
		if existing.triggerChan != nil {
			close(existing.triggerChan)
		}
	}

	// Use ScaleUp trigger's PeriodSeconds as the static monitoring interval
	duration := time.Duration(behavior.ScaleUp.Trigger.PeriodSeconds) * time.Second

	// Create trigger channel
	triggerChan := make(chan TriggerEvent, 1)

	// Create contexts
	eventCtx, eventCancel := context.WithCancel(sm.ctx)
	monitorCtx, monitorCancel := context.WithCancel(sm.ctx)

	// Create event
	event := &MonitoringEvent{
		AutoscalerName:   autoscalerName,
		Namespace:        namespace,
		ScaleUpTrigger:   behavior.ScaleUp.Trigger,
		ScaleDownTrigger: behavior.ScaleDown.Trigger,
		collector:        collector,
		lastTriggerTime:  time.Now(), // Initialize with current time
		triggerChan:      triggerChan,
		monitorCancel:    monitorCancel,
		cancelFunc:       eventCancel,
	}

	// Cache the spec
	event.setCachedSpec(spec)

	// Start timer goroutine
	event.timer = time.AfterFunc(duration, func() {
		select {
		case <-eventCtx.Done():
			return
		case triggerChan <- TriggerEvent{
			AutoscalerName: autoscalerName,
			Namespace:      namespace,
			CachedSpec:     event.getCachedSpec(),
		}:
			sm.logger.V(1).Info("Trigger event sent",
				"autoscaler", autoscalerName,
				"namespace", namespace)
		default:
			sm.logger.V(1).Info("Trigger channel full, skipping",
				"autoscaler", autoscalerName,
				"namespace", namespace)
		}
	})

	// Start monitoring goroutine that listens on the channel
	go sm.monitoringLoop(monitorCtx, event)

	sm.events[key] = event
	sm.logger.Info("Registered monitoring event with collector",
		"autoscaler", autoscalerName,
		"namespace", namespace,
		"periodSeconds", behavior.ScaleUp.Trigger.PeriodSeconds)

	return nil
}

// monitoringLoop listens for trigger events and calls the collector
func (sm *serviceMonitor) monitoringLoop(ctx context.Context, event *MonitoringEvent) {
	logger := sm.logger.WithValues("autoscaler", event.AutoscalerName, "namespace", event.Namespace)
	logger.Info("Starting monitoring loop")

	for {
		select {
		case triggerEvent, ok := <-event.triggerChan:
			if !ok {
				logger.Info("Trigger channel closed, stopping monitoring loop")
				return
			}

			logger.Info("Received trigger event")

			// Get cached spec
			spec := triggerEvent.CachedSpec
			if spec == nil {
				logger.Info("No cached spec available, skipping metric collection")
				continue
			}

			// Call collector to collect metrics
			// Pass lastTriggerTime for lookback window calculation
			result, err := event.collector.CollectAndCompute(ctx, spec, event.lastTriggerTime)
			if err != nil {
				logger.Error(err, "Failed to collect metrics")
				result = &MetricsResult{
					Message: fmt.Sprintf("Error collecting metrics: %v", err),
					Metrics: make(map[string]string), // Empty metrics on error
				}
			} else {
				logger.Info("Collected metrics", "message", result.Message)
			}

			// Update lastTriggerTime to current time for next iteration
			event.lastTriggerTime = time.Now()

			// Send trigger event with metrics result to controller for scaling action
			go sm.notifyScalingAction(ctx, event.AutoscalerName, event.Namespace, spec, result)

			// Update trigger time with static window
			_, err = sm.UpdateTriggerTime(ctx, event.AutoscalerName, event.Namespace)
			if err != nil {
				logger.Error(err, "Failed to update trigger time")
			}

		case <-ctx.Done():
			logger.Info("Context cancelled, stopping monitoring loop")
			return
		}
	}
}

// SetScalingActionCallback sets the callback for scaling actions
func (sm *serviceMonitor) SetScalingActionCallback(callback ScalingActionCallback) {
	sm.callbackMutex.Lock()
	defer sm.callbackMutex.Unlock()
	sm.scalingActionCallback = callback
}

// notifyScalingAction notifies the controller about scaling needs via callback
func (sm *serviceMonitor) notifyScalingAction(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, result *MetricsResult) {
	sm.callbackMutex.RLock()
	callback := sm.scalingActionCallback
	sm.callbackMutex.RUnlock()

	if callback != nil {
		callback(ctx, autoscalerName, namespace, spec, result)
	} else {
		sm.logger.V(1).Info("No scaling action callback set",
			"autoscaler", autoscalerName,
			"namespace", namespace)
	}
}

// UpdateTriggerTime updates the trigger time using static window from ScaleUp trigger
func (sm *serviceMonitor) UpdateTriggerTime(ctx context.Context, autoscalerName, namespace string) (<-chan TriggerEvent, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := namespace + "/" + autoscalerName

	event, exists := sm.events[key]
	if !exists {
		return nil, fmt.Errorf("monitoring event not found for %s", key)
	}

	// Stop existing timer
	if event.timer != nil {
		event.timer.Stop()
	}

	// Use static window from ScaleUp trigger's PeriodSeconds
	duration := time.Duration(event.ScaleUpTrigger.PeriodSeconds) * time.Second

	// Create context for this event
	eventCtx, eventCancel := context.WithCancel(sm.ctx)
	if event.cancelFunc != nil {
		event.cancelFunc()
	}
	event.cancelFunc = eventCancel

	// Start new timer
	event.timer = time.AfterFunc(duration, func() {
		select {
		case <-eventCtx.Done():
			return
		case event.triggerChan <- TriggerEvent{
			AutoscalerName: autoscalerName,
			Namespace:      namespace,
			CachedSpec:     event.getCachedSpec(),
		}:
			sm.logger.V(1).Info("Trigger event sent",
				"autoscaler", autoscalerName,
				"namespace", namespace)
		default:
			sm.logger.V(1).Info("Trigger channel full, skipping",
				"autoscaler", autoscalerName,
				"namespace", namespace)
		}
	})

	sm.logger.Info("Updated trigger time",
		"autoscaler", autoscalerName,
		"namespace", namespace,
		"periodSeconds", event.ScaleUpTrigger.PeriodSeconds,
		"duration", duration)

	return event.triggerChan, nil
}

// UpdateCachedSpec updates the cached spec for a registered autoscaler
func (sm *serviceMonitor) UpdateCachedSpec(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := namespace + "/" + autoscalerName

	event, exists := sm.events[key]
	if !exists {
		return fmt.Errorf("monitoring event not found for %s", key)
	}

	event.setCachedSpec(spec)
	sm.logger.V(1).Info("Updated cached spec",
		"autoscaler", autoscalerName,
		"namespace", namespace)

	return nil
}

// Unregister removes a monitoring event for a ResourceClaimAutoscaler
func (sm *serviceMonitor) Unregister(ctx context.Context, autoscalerName, namespace string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := namespace + "/" + autoscalerName

	event, exists := sm.events[key]
	if !exists {
		return fmt.Errorf("monitoring event not found for %s", key)
	}

	// Stop timer and cancel context
	if event.timer != nil {
		event.timer.Stop()
	}
	if event.cancelFunc != nil {
		event.cancelFunc()
	}
	if event.triggerChan != nil {
		close(event.triggerChan)
	}

	delete(sm.events, key)
	sm.logger.Info("Unregistered monitoring event",
		"autoscaler", autoscalerName,
		"namespace", namespace)

	return nil
}

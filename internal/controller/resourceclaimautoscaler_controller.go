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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/discovery"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/monitor"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/scaler"
)

// ResourceClaimAutoscalerReconciler reconciles a ResourceClaimAutoscaler object
type ResourceClaimAutoscalerReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ServiceMonitor monitor.ServiceMonitor
	Monitor        *monitor.Monitor
	monitorOnce    sync.Once
	monitorMutex   sync.RWMutex
}

const (
	// FinalizerName is the finalizer added to ResourceClaimAutoscaler for cleanup
	FinalizerName = "autoscaling.x-llmd.ai/finalizer"
)

// +kubebuilder:rbac:groups=autoscaling.x-llmd.ai,resources=resourceclaimautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.x-llmd.ai,resources=resourceclaimautoscalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoscaling.x-llmd.ai,resources=resourceclaimautoscalers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaimtemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ResourceClaimAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ResourceClaimAutoscaler", "name", req.Name, "namespace", req.Namespace)

	// Ensure ServiceMonitor is started (only once)
	r.monitorOnce.Do(func() {
		if r.ServiceMonitor != nil {
			if err := r.ServiceMonitor.Start(ctx); err != nil {
				logger.Error(err, "Failed to start ServiceMonitor")
			}
		}
	})

	// Fetch the ResourceClaimAutoscaler instance
	autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
	if err := r.Get(ctx, req.NamespacedName, autoscaler); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("ResourceClaimAutoscaler not found, unregistering from monitor")
			// Unregister from ServiceMonitor (ServiceMonitor will handle cleanup)
			if r.ServiceMonitor != nil {
				if err := r.ServiceMonitor.Unregister(ctx, req.Name, req.Namespace); err != nil {
					logger.V(1).Info("Failed to unregister from monitor", "error", err)
				}
			}
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get ResourceClaimAutoscaler")
		return ctrl.Result{}, err
	}

	// Handle finalizer for cleanup
	if autoscaler.DeletionTimestamp.IsZero() {
		// Object is not being deleted, ensure finalizer is present
		if !containsString(autoscaler.Finalizers, FinalizerName) {
			autoscaler.Finalizers = append(autoscaler.Finalizers, FinalizerName)
			if err := r.Update(ctx, autoscaler); err != nil {
				logger.Error(err, "Failed to add finalizer")
				return ctrl.Result{}, err
			}
			logger.Info("Added finalizer to ResourceClaimAutoscaler")
			// Requeue to continue with reconciliation
			return ctrl.Result{Requeue: true}, nil
		}
	} else {
		// Object is being deleted
		if containsString(autoscaler.Finalizers, FinalizerName) {
			// Perform cleanup
			logger.Info("Performing cleanup for ResourceClaimAutoscaler")

			// Unregister from ServiceMonitor
			if r.ServiceMonitor != nil {
				if err := r.ServiceMonitor.Unregister(ctx, autoscaler.Name, autoscaler.Namespace); err != nil {
					logger.Error(err, "Failed to unregister from monitor during cleanup")
				}
			}

			// Revert replicas and restore original template in a single call
			if autoscaler.Status.Discovery != nil && autoscaler.Status.Discovery.ResourceClaim != "" {
				if err := r.cleanupWorkload(ctx, autoscaler, logger); err != nil {
					logger.Error(err, "Failed to cleanup workload")
					return ctrl.Result{}, err
				}
			}

			// Remove finalizer
			autoscaler.Finalizers = removeString(autoscaler.Finalizers, FinalizerName)
			if err := r.Update(ctx, autoscaler); err != nil {
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
			logger.Info("Removed finalizer from ResourceClaimAutoscaler")
		}

		// Stop reconciliation as the object is being deleted
		return ctrl.Result{}, nil
	}

	if autoscaler.Status.Discovery == nil {
		// Discover service instance target using discovery package
		serviceNamespace := autoscaler.Spec.Target.ServiceRef.Namespace
		if serviceNamespace == "" {
			serviceNamespace = autoscaler.Namespace
		}

		// Get the target device class from resourceRef
		targetDeviceClass := autoscaler.Spec.Target.ResourceRef.DeviceClassName

		discoverer := discovery.NewServiceDiscoverer(r.Client)
		result, err := discoverer.DiscoverFromService(ctx, autoscaler.Spec.Target.ServiceRef.Name, serviceNamespace, targetDeviceClass)
		if err != nil {
			meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
				Type:               "ScalingTargetDiscovery",
				Status:             metav1.ConditionFalse,
				Reason:             "ScalingTargetDiscoveryFailed",
				Message:            fmt.Sprintf("Failed to discover scaling target: %v", err),
				ObservedGeneration: autoscaler.Generation,
			})
			// Don't return early - continue to set monitoring condition
		} else {
			// Update discovery results in status
			if result != nil {
				if autoscaler.Status.Discovery == nil {
					autoscaler.Status.Discovery = &autoscalingv1alpha1.DiscoveryResults{}
				}
				autoscaler.Status.Discovery.Owner = result.Owner
				autoscaler.Status.Discovery.ResourceClaim = result.ResourceClaimTemplate

				// Set success condition
				meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
					Type:               "ScalingTargetDiscovery",
					Status:             metav1.ConditionTrue,
					Reason:             "ScalingTargetDiscovered",
					Message:            fmt.Sprintf("Successfully discovered scaling target: %s/%s", result.Owner.Kind, result.Owner.Name),
					ObservedGeneration: autoscaler.Generation,
				})
			}
		}
	}

	// Register monitoring events - always enabled with defaults if not configured
	// Single collector handles both ScaleUp and ScaleDown decisions
	if r.ServiceMonitor == nil {
		// ServiceMonitor not initialized - update status condition and requeue
		logger.Info("ServiceMonitor not initialized, will retry")
		meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
			Type:               "MonitoringActive",
			Status:             metav1.ConditionFalse,
			Reason:             "ServiceMonitorNotInitialized",
			Message:            "ServiceMonitor is not initialized, will retry",
			ObservedGeneration: autoscaler.Generation,
		})
		// Update status before requeuing
		if err := r.Status().Update(ctx, autoscaler); err != nil {
			logger.Error(err, "Failed to update status")
		}
		// Requeue after 30 seconds
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Check if Monitor is initialized (required for metrics collection)
	r.monitorMutex.RLock()
	monitorInitialized := r.Monitor != nil
	r.monitorMutex.RUnlock()

	if !monitorInitialized {
		// Monitor not initialized - update status condition and requeue
		logger.Info("Monitor not initialized, will retry")
		meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
			Type:               "MonitoringActive",
			Status:             metav1.ConditionFalse,
			Reason:             "MonitorNotInitialized",
			Message:            "Monitor is not initialized, will retry",
			ObservedGeneration: autoscaler.Generation,
		})
		// Update status before requeuing
		if err := r.Status().Update(ctx, autoscaler); err != nil {
			logger.Error(err, "Failed to update status")
		}
		// Requeue after 30 seconds
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Check if already registered to avoid re-creating collector on every reconciliation
	if r.ServiceMonitor.IsRegistered(autoscaler.Name, autoscaler.Namespace) {
		// Already registered - just update the cached spec
		logger.V(1).Info("Monitoring already registered, updating cached spec")
		err := r.ServiceMonitor.UpdateCachedSpec(ctx, autoscaler.Name, autoscaler.Namespace, &autoscaler.Spec)
		if err != nil {
			logger.Error(err, "Failed to update cached spec")
			return ctrl.Result{}, err
		}
		goto updateStatus
	}
	if err := r.NewRegisteration(ctx, autoscaler, logger); err != nil {
		logger.Error(err, "Failed to register monitoring")
		meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
			Type:               "MonitoringActive",
			Status:             metav1.ConditionFalse,
			Reason:             "RegistrationFailed",
			Message:            fmt.Sprintf("Failed to register monitoring: %v", err),
			ObservedGeneration: autoscaler.Generation,
		})
	} else {
		// Registration succeeded
		meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
			Type:               "MonitoringActive",
			Status:             metav1.ConditionTrue,
			Reason:             "MonitoringRegistered",
			Message:            "Successfully registered monitoring",
			ObservedGeneration: autoscaler.Generation,
		})
	}

updateStatus:

	// Update status
	if err := r.Status().Update(ctx, autoscaler); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled ResourceClaimAutoscaler")
	return ctrl.Result{}, nil
}

func (r *ResourceClaimAutoscalerReconciler) NewRegisteration(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, logger logr.Logger) error {
	// Not registered yet - create collector and register
	logger.V(1).Info("Registering new monitoring with collector")

	// Create single metrics collector that determines ScaleUp/ScaleDown needs
	// This collector is created only once during initial registration
	collector := monitor.NewAutoscalerMetricsCollector(r.Monitor, &r.monitorMutex)

	// Get effective behavior with defaults applied
	effectiveBehavior := scaler.GetEffectiveBehavior(&autoscaler.Spec)

	// Register with ServiceMonitor (ServiceMonitor manages the monitoring lifecycle)
	// The collector will analyze metrics and recommend which action (ScaleUp/ScaleDown) is needed
	// ServiceMonitor performs initial metric collection to determine the starting trigger
	return r.ServiceMonitor.RegisterWithCollector(ctx, autoscaler.Name, autoscaler.Namespace, &autoscaler.Spec, effectiveBehavior, collector)
}

// SetMonitor sets the shared Monitor instance (called by AutoClaimerConfig controller)
func (r *ResourceClaimAutoscalerReconciler) SetMonitor(mon *monitor.Monitor) {
	r.monitorMutex.Lock()
	defer r.monitorMutex.Unlock()
	r.Monitor = mon
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceClaimAutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize ServiceMonitor if not already set
	if r.ServiceMonitor == nil {
		r.ServiceMonitor = monitor.NewServiceMonitor()
	}

	// Set up scaling action callback
	r.ServiceMonitor.SetScalingActionCallback(r.handleScalingAction)

	return ctrl.NewControllerManagedBy(mgr).
		For(&autoscalingv1alpha1.ResourceClaimAutoscaler{}).
		Watches(
			&resourcev1.ResourceClaim{},
			handler.EnqueueRequestsFromMapFunc(r.mapResourceClaimToAutoscaler),
			builder.WithPredicates(r.resourceClaimUpdatePredicate()),
		).
		Named("resourceclaimautoscaler").
		Complete(r)
}

// resourceClaimUpdatePredicate filters ResourceClaim events and triggers cleanup
func (r *ResourceClaimAutoscalerReconciler) resourceClaimUpdatePredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldClaim, oldOk := e.ObjectOld.(*resourcev1.ResourceClaim)
			newClaim, newOk := e.ObjectNew.(*resourcev1.ResourceClaim)

			if !oldOk || !newOk {
				return false
			}

			// Check if this claim is managed by our operator
			if newClaim.Labels == nil {
				return false
			}

			managedBy, hasManagedBy := newClaim.Labels["autoscaling.x-llmd.ai/managed-by"]
			autoscalerName, hasAutoscaler := newClaim.Labels["autoscaling.x-llmd.ai/autoscaler"]

			if !hasManagedBy || managedBy != scaler.OperatorName || !hasAutoscaler {
				return false
			}

			// Check if allocation status changed
			oldBound := oldClaim.Status.Allocation != nil
			newBound := newClaim.Status.Allocation != nil

			if oldBound != newBound {
				// Status changed, trigger update handler
				logger := log.Log.WithValues("claim", newClaim.Name, "autoscaler", autoscalerName)
				logger.Info("ResourceClaim status changed, triggering update handler",
					"oldBound", oldBound,
					"newBound", newBound)

				// Trigger cleanup asynchronously with both old and new objects
				go func() {
					ctx := context.Background()
					if err := r.handleResourceClaimUpdate(ctx, newClaim, oldClaim); err != nil {
						logger.Error(err, "Failed to handle ResourceClaim update")
					}
				}()

				// Return true to trigger reconciliation
				return true
			}

			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			// Don't trigger on create
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			claim, ok := e.Object.(*resourcev1.ResourceClaim)
			if !ok {
				return false
			}

			// Check if this claim is managed by our operator
			if claim.Labels == nil {
				return false
			}

			managedBy, hasManagedBy := claim.Labels["autoscaling.x-llmd.ai/managed-by"]
			autoscalerName, hasAutoscaler := claim.Labels["autoscaling.x-llmd.ai/autoscaler"]
			templateName, hasTemplate := claim.Labels["autoscaling.x-llmd.ai/template-name"]

			if !hasManagedBy || managedBy != scaler.OperatorName || !hasAutoscaler || !hasTemplate {
				return false
			}

			logger := log.Log.WithValues("claim", claim.Name, "autoscaler", autoscalerName, "template", templateName)
			logger.Info("ResourceClaim deleted (likely scale down), updating replica status")

			// Update replica status asynchronously
			go func() {
				ctx := context.Background()

				// Fetch the autoscaler
				autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
				if err := r.Get(ctx, client.ObjectKey{
					Name:      autoscalerName,
					Namespace: claim.Namespace,
				}, autoscaler); err != nil {
					if !errors.IsNotFound(err) {
						logger.Error(err, "Failed to fetch autoscaler")
					}
					return
				}

				// Check if this is the current desired template
				if autoscaler.Status.ScalingStatus != nil &&
					autoscaler.Status.ScalingStatus.DesiredClaimTemplate != "" {
					if autoscaler.Status.ScalingStatus.DesiredClaimTemplate == templateName {
						// Update replica status from owner
						if autoscaler.Status.Discovery != nil && autoscaler.Status.Discovery.Owner != nil {
							if err := r.updateReplicaStatus(ctx, autoscaler, autoscaler.Status.Discovery.Owner); err != nil {
								logger.Error(err, "Failed to update replica status after claim deletion")
							} else {
								logger.Info("Updated replica status after claim deletion",
									"ready", autoscaler.Status.ScalingStatus.ReadyReplicas,
									"available", autoscaler.Status.ScalingStatus.AvailableReplicas)
							}
						}
					} else if autoscaler.Status.Discovery != nil && templateName != autoscaler.Status.Discovery.ResourceClaim {
						// Delete unused template
						if err := DeleteTemplate(ctx, r.Client, templateName, claim.Namespace); err != nil {
							logger.Info("Failed to delete unused %s template when %s/%s deleted: %v", templateName, claim.Name, claim.Namespace, err)
						}
					}
				}
			}()

			// Trigger reconciliation
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			// Don't trigger on generic events
			return false
		},
	}
}

// mapResourceClaimToAutoscaler maps a ResourceClaim to its owning ResourceClaimAutoscaler
func (r *ResourceClaimAutoscalerReconciler) mapResourceClaimToAutoscaler(ctx context.Context, obj client.Object) []ctrl.Request {
	claim, ok := obj.(*resourcev1.ResourceClaim)
	if !ok {
		return nil
	}

	// Check if this claim is managed by our operator
	if claim.Labels == nil {
		return nil
	}

	managedBy, hasManagedBy := claim.Labels["autoscaling.x-llmd.ai/managed-by"]
	autoscalerName, hasAutoscaler := claim.Labels["autoscaling.x-llmd.ai/autoscaler"]

	if !hasManagedBy || managedBy != "resourceclaimautoscaler" || !hasAutoscaler {
		return nil
	}

	// Return the autoscaler to reconcile
	return []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Name:      autoscalerName,
				Namespace: claim.Namespace,
			},
		},
	}
}

// handleResourceClaimUpdate is called when a ResourceClaim status changes
// This triggers cleanup of old ResourceClaimTemplates when claims become bound
// and updates the autoscaler status with ready/available replica counts
func (r *ResourceClaimAutoscalerReconciler) handleResourceClaimUpdate(ctx context.Context, claim client.Object, oldClaim client.Object) error {
	logger := log.FromContext(ctx)

	// Check if this claim is managed by our operator
	if claim.GetLabels() == nil {
		return nil
	}

	managedBy, hasManagedBy := claim.GetLabels()["autoscaling.x-llmd.ai/managed-by"]
	autoscalerName, hasAutoscaler := claim.GetLabels()["autoscaling.x-llmd.ai/autoscaler"]

	if !hasManagedBy || managedBy != "resourceclaimautoscaler" || !hasAutoscaler {
		return nil
	}

	// Check if the claim status actually changed
	resourceClaim, ok := claim.(*resourcev1.ResourceClaim)
	if !ok {
		return nil
	}

	var oldResourceClaim *resourcev1.ResourceClaim
	if oldClaim != nil {
		oldResourceClaim, _ = oldClaim.(*resourcev1.ResourceClaim)
	}

	// Compare allocation status - only proceed if it changed
	if oldResourceClaim != nil {
		oldBound := oldResourceClaim.Status.Allocation != nil
		newBound := resourceClaim.Status.Allocation != nil
		if oldBound == newBound {
			// Status hasn't changed, do nothing
			return nil
		}
	}

	logger.Info("ResourceClaim status changed, updating autoscaler status",
		"claim", claim.GetName(),
		"autoscaler", autoscalerName,
		"bound", resourceClaim.Status.Allocation != nil)

	// Fetch the autoscaler
	autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      autoscalerName,
		Namespace: claim.GetNamespace(),
	}, autoscaler); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Autoscaler not found, skipping update", "autoscaler", autoscalerName)
			return nil
		}
		return err
	}

	// Update ready and available replica counts from owner
	if autoscaler.Status.Discovery != nil && autoscaler.Status.Discovery.Owner != nil {
		owner := autoscaler.Status.Discovery.Owner
		if err := r.updateReplicaStatus(ctx, autoscaler, owner); err != nil {
			logger.Error(err, "Failed to update replica status")
			// Continue with cleanup even if status update fails
		}
	}

	// Trigger cleanup of old templates only when claim becomes bound
	if resourceClaim.Status.Allocation != nil {
		resourceScaler := scaler.NewResourceScaler(r.Client, logger)
		if err := resourceScaler.CleanupOldResourceClaimTemplates(ctx, autoscaler); err != nil {
			logger.Error(err, "Failed to cleanup old ResourceClaimTemplates")
			// Don't return error - cleanup failure shouldn't block reconciliation
		}
	}

	return nil
}

// updateReplicaStatus updates the autoscaler status with ready and available replica counts from the owner
func (r *ResourceClaimAutoscalerReconciler) updateReplicaStatus(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, owner *metav1.OwnerReference) error {
	logger := log.FromContext(ctx).WithValues("autoscaler", autoscaler.Name, "namespace", autoscaler.Namespace)

	if autoscaler.Status.ScalingStatus == nil {
		autoscaler.Status.ScalingStatus = &autoscalingv1alpha1.ScalingStatus{}
	}

	switch owner.Kind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: autoscaler.Namespace}, deployment); err != nil {
			return fmt.Errorf("failed to get Deployment: %w", err)
		}

		autoscaler.Status.ScalingStatus.ReadyReplicas = &deployment.Status.ReadyReplicas
		autoscaler.Status.ScalingStatus.AvailableReplicas = &deployment.Status.AvailableReplicas

		logger.V(1).Info("Updated replica status from Deployment",
			"ready", deployment.Status.ReadyReplicas,
			"available", deployment.Status.AvailableReplicas)

	case "StatefulSet":
		statefulSet := &appsv1.StatefulSet{}
		if err := r.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: autoscaler.Namespace}, statefulSet); err != nil {
			return fmt.Errorf("failed to get StatefulSet: %w", err)
		}

		autoscaler.Status.ScalingStatus.ReadyReplicas = &statefulSet.Status.ReadyReplicas
		autoscaler.Status.ScalingStatus.AvailableReplicas = &statefulSet.Status.AvailableReplicas

		logger.V(1).Info("Updated replica status from StatefulSet",
			"ready", statefulSet.Status.ReadyReplicas,
			"available", statefulSet.Status.AvailableReplicas)

	default:
		return fmt.Errorf("unsupported owner kind: %s", owner.Kind)
	}

	// Update the autoscaler status
	if err := r.Status().Update(ctx, autoscaler); err != nil {
		return fmt.Errorf("failed to update autoscaler status: %w", err)
	}

	return nil
}

// handleScalingAction is called by the ServiceMonitor when scaling is needed
// This integrates the monitor → optimize → scale flow
// metrics parameter contains the raw metrics already collected by the monitoring loop
func (r *ResourceClaimAutoscalerReconciler) handleScalingAction(ctx context.Context, autoscalerName, namespace string, spec *autoscalingv1alpha1.ResourceClaimAutoscalerSpec, result *monitor.MetricsResult) {
	logger := log.FromContext(ctx).WithValues("autoscaler", autoscalerName, "namespace", namespace)
	logger.Info("Handling scaling action", "message", result.Message)

	resourceScaler := scaler.NewResourceScaler(r.Client, logger)

	// Evaluate and scale with metrics already collected by monitoring loop
	if err := resourceScaler.EvaluateAndScale(ctx, autoscalerName, namespace, spec, result.Metrics); err != nil {
		logger.Error(err, "Failed to evaluate and scale")

		// Update status with error condition, retrying on conflict
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Fetch the latest autoscaler to get current resource version
			autoscaler := &autoscalingv1alpha1.ResourceClaimAutoscaler{}
			if err := r.Get(ctx, client.ObjectKey{Name: autoscalerName, Namespace: namespace}, autoscaler); err != nil {
				return err
			}

			// Update status with error condition
			meta.SetStatusCondition(&autoscaler.Status.Conditions, metav1.Condition{
				Type:               "ScalingActive",
				Status:             metav1.ConditionFalse,
				Reason:             "ScalingFailed",
				Message:            fmt.Sprintf("Failed to scale: %v", err),
				ObservedGeneration: autoscaler.Generation,
			})

			// Attempt status update
			return r.Status().Update(ctx, autoscaler)
		})

		if updateErr != nil {
			logger.Error(updateErr, "Failed to update status after scaling error")
		}
	}
}

// containsString checks if a string slice contains a specific string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// removeString removes a string from a slice
func removeString(slice []string, s string) []string {
	result := []string{}
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

// cleanupWorkload reverts replicas to minReplicas and restores original template in a single update
// This is called during cleanup when the autoscaler is being deleted
func (r *ResourceClaimAutoscalerReconciler) cleanupWorkload(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler, logger logr.Logger) error {
	// Check if we have discovery information
	if autoscaler.Status.Discovery == nil || autoscaler.Status.Discovery.Owner == nil {
		logger.V(1).Info("No discovery information, skipping workload cleanup")
		return nil
	}

	owner := autoscaler.Status.Discovery.Owner
	originalTemplateName := autoscaler.Status.Discovery.ResourceClaim

	// Get minReplicas from constraints
	minReplicas := int32(1) // Default
	if autoscaler.Spec.Constraints != nil && autoscaler.Spec.Constraints.MinReplicas != nil {
		minReplicas = *autoscaler.Spec.Constraints.MinReplicas
	}

	logger.Info("Cleaning up workload: reverting replicas and restoring template",
		"owner", owner.Kind+"/"+owner.Name,
		"minReplicas", minReplicas,
		"originalTemplate", originalTemplateName)

	// Cleanup based on owner kind
	switch owner.Kind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{
			Name:      owner.Name,
			Namespace: autoscaler.Namespace,
		}, deployment); err != nil {
			if errors.IsNotFound(err) {
				logger.V(1).Info("Deployment not found, may have been deleted", "deployment", owner.Name)
				return nil
			}
			return fmt.Errorf("failed to get deployment: %w", err)
		}

		// Revert replicas to minReplicas
		deployment.Spec.Replicas = &minReplicas

		// Restore the original template in the pod spec
		if len(deployment.Spec.Template.Spec.ResourceClaims) > 0 {
			for i := range deployment.Spec.Template.Spec.ResourceClaims {
				if deployment.Spec.Template.Spec.ResourceClaims[i].ResourceClaimTemplateName != nil {
					deployment.Spec.Template.Spec.ResourceClaims[i].ResourceClaimTemplateName = &originalTemplateName
				}
			}
		}

		// Single update for both changes
		if err := r.Update(ctx, deployment); err != nil {
			return fmt.Errorf("failed to update deployment: %w", err)
		}

		logger.Info("Cleaned up deployment",
			"deployment", owner.Name,
			"replicas", minReplicas,
			"template", originalTemplateName)

	case "StatefulSet":
		statefulSet := &appsv1.StatefulSet{}
		if err := r.Get(ctx, client.ObjectKey{
			Name:      owner.Name,
			Namespace: autoscaler.Namespace,
		}, statefulSet); err != nil {
			if errors.IsNotFound(err) {
				logger.V(1).Info("StatefulSet not found, may have been deleted", "statefulSet", owner.Name)
				return nil
			}
			return fmt.Errorf("failed to get statefulSet: %w", err)
		}

		// Revert replicas to minReplicas
		statefulSet.Spec.Replicas = &minReplicas

		// Restore the original template in the pod spec
		if len(statefulSet.Spec.Template.Spec.ResourceClaims) > 0 {
			for i := range statefulSet.Spec.Template.Spec.ResourceClaims {
				if statefulSet.Spec.Template.Spec.ResourceClaims[i].ResourceClaimTemplateName != nil {
					statefulSet.Spec.Template.Spec.ResourceClaims[i].ResourceClaimTemplateName = &originalTemplateName
				}
			}
		}

		// Single update for both changes
		if err := r.Update(ctx, statefulSet); err != nil {
			return fmt.Errorf("failed to update statefulSet: %w", err)
		}

		logger.Info("Cleaned up statefulSet",
			"statefulSet", owner.Name,
			"replicas", minReplicas,
			"template", originalTemplateName)

	default:
		logger.V(1).Info("Unsupported owner kind for workload cleanup", "kind", owner.Kind)
	}

	return nil
}

func DeleteTemplate(ctx context.Context, c client.Client, templateName, namespace string) error {
	template := &resourcev1.ResourceClaimTemplate{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      templateName,
		Namespace: namespace,
	}, template)

	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get template: %v", err)
	}

	if err := c.Delete(ctx, template); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete template: %v", err)
		}
	}
	return nil
}

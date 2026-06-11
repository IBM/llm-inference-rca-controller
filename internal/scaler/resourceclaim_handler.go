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

package scaler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	autoscalingv1alpha1 "github.com/ibm/k8s-resourceclaim-autoscaler/api/v1alpha1"
	"github.com/ibm/k8s-resourceclaim-autoscaler/internal/optimizer"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ManagedByLabel indicates the resource is managed by this operator
	ManagedByLabel = "autoscaling.x-llmd.ai/managed-by"
	// OperatorName is the name of this operator
	OperatorName = "resourceclaimautoscaler"
	// RevisionLabel indicates the revision number of the resource claim template
	RevisionLabel = "autoscaling.x-llmd.ai/revision"
	// OriginalTemplateLabel stores the original template name
	OriginalTemplateLabel = "autoscaling.x-llmd.ai/original-template"
	// TemplateNameLabel stores the template name that created this claim
	TemplateNameLabel = "autoscaling.x-llmd.ai/template-name"
	// AutoscalerLabel stores the autoscaler name that manages this resource
	AutoscalerLabel = "autoscaling.x-llmd.ai/autoscaler"

	// WorkloadKindDeployment represents a Deployment workload
	WorkloadKindDeployment = "Deployment"
	// WorkloadKindStatefulSet represents a StatefulSet workload
	WorkloadKindStatefulSet = "StatefulSet"
)

// getCurrentResourcesFromTemplate retrieves current resource requests from ResourceClaimTemplate
func (s *ResourceScaler) getCurrentResourcesFromTemplate(ctx context.Context, autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler) (gpus int, resources map[resourcev1.QualifiedName]resource.Quantity, err error) {
	// Default values
	gpus = 1

	// Get ResourceClaimTemplate name - use current desired template if available, otherwise original
	if autoscaler.Status.Discovery == nil || autoscaler.Status.Discovery.ResourceClaim == "" {
		s.logger.V(1).Info("No ResourceClaimTemplate found in discovery status, using defaults")
		return gpus, resources, nil
	}

	// Determine which template to read from: current desired or original
	templateName := autoscaler.Status.Discovery.ResourceClaim
	if autoscaler.Status.ScalingStatus != nil && autoscaler.Status.ScalingStatus.DesiredClaimTemplate != "" {
		templateName = autoscaler.Status.ScalingStatus.DesiredClaimTemplate
		s.logger.V(1).Info("Using current desired template for resource lookup", "template", templateName)
	} else {
		s.logger.V(1).Info("Using original template for resource lookup", "template", templateName)
	}

	// Fetch the ResourceClaimTemplate
	template := &resourcev1.ResourceClaimTemplate{}
	if err := s.client.Get(ctx, client.ObjectKey{
		Name:      templateName,
		Namespace: autoscaler.Namespace,
	}, template); err != nil {
		s.logger.Error(err, "Failed to get ResourceClaimTemplate", "template", templateName)
		return gpus, resources, fmt.Errorf("failed to get ResourceClaimTemplate: %w", err)
	}

	// Extract resource requests from the template spec
	targetDeviceClass := autoscaler.Spec.Target.ResourceRef.DeviceClassName
	if len(template.Spec.Spec.Devices.Requests) > 0 {
		for _, request := range template.Spec.Spec.Devices.Requests {
			// Check Exactly field for device class
			if request.Exactly != nil && request.Exactly.DeviceClassName == targetDeviceClass {
				if request.Exactly.Count > 0 {
					gpus = int(request.Exactly.Count)
				}
				if request.Exactly.Capacity != nil && request.Exactly.Capacity.Requests != nil {
					resources = request.Exactly.Capacity.Requests
				}
				break
			}

			// Check FirstAvailable subrequests
			for _, subReq := range request.FirstAvailable {
				if subReq.DeviceClassName == targetDeviceClass {
					if subReq.Count > 0 {
						gpus = int(subReq.Count)
					}
					if subReq.Capacity != nil && subReq.Capacity.Requests != nil {
						resources = subReq.Capacity.Requests
					}
					break
				}
			}
		}
	}

	s.logger.V(1).Info("Retrieved current resources from template",
		"template", templateName,
		"gpus", gpus,
		"resources", resources)

	return gpus, resources, nil
}

// patchResourceClaimTemplate creates a new ResourceClaimTemplate with updated resources and revision suffix
// Returns the name of the new template
func (s *ResourceScaler) patchResourceClaimTemplate(
	ctx context.Context,
	autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler,
	desired optimizer.ResourceConfiguration,
) (string, error) {
	logger := s.logger.WithValues("autoscaler", autoscaler.Name, "namespace", autoscaler.Namespace)

	// Get the original ResourceClaimTemplate name from discovery status
	if autoscaler.Status.Discovery == nil || autoscaler.Status.Discovery.ResourceClaim == "" {
		return "", fmt.Errorf("no ResourceClaimTemplate found in discovery status")
	}

	originalTemplateName := autoscaler.Status.Discovery.ResourceClaim

	// Determine which template to use as base: current desired template or original
	// This ensures we build on the latest configuration, not always the original
	baseTemplateName := originalTemplateName
	if autoscaler.Status.ScalingStatus != nil && autoscaler.Status.ScalingStatus.DesiredClaimTemplate != "" {
		baseTemplateName = autoscaler.Status.ScalingStatus.DesiredClaimTemplate
		logger.V(1).Info("Using current desired template as base", "baseTemplate", baseTemplateName)
	} else {
		logger.V(1).Info("Using original template as base", "baseTemplate", baseTemplateName)
	}

	// Fetch the base ResourceClaimTemplate
	baseTemplate := &resourcev1.ResourceClaimTemplate{}
	if err := s.client.Get(ctx, client.ObjectKey{
		Name:      baseTemplateName,
		Namespace: autoscaler.Namespace,
	}, baseTemplate); err != nil {
		return "", fmt.Errorf("failed to get base ResourceClaimTemplate: %w", err)
	}

	// Get the owner reference to determine the revision
	if autoscaler.Status.Discovery.Owner == nil {
		return "", fmt.Errorf("no owner found in discovery status")
	}

	owner := autoscaler.Status.Discovery.Owner
	revision, err := s.getOwnerRevision(ctx, autoscaler.Namespace, owner)
	if err != nil {
		return "", fmt.Errorf("failed to get owner revision: %w", err)
	}

	// Create new template name using autoscaler name + hash
	// Format: <autoscaler-name>-<hash>
	// Hash is computed from: originalTemplateName + revision
	// This avoids name length issues while maintaining uniqueness
	// Kubernetes resource names must be <= 63 characters
	hashInput := fmt.Sprintf("%s-rev-%s", originalTemplateName, revision)
	hash := sha256.Sum256([]byte(hashInput))
	hashStr := hex.EncodeToString(hash[:])[:8] // Use first 8 characters of hash

	// Truncate autoscaler name if needed to ensure total length <= 63
	// Format: <truncated-autoscaler-name>-<8-char-hash>
	// Max autoscaler name length: 63 - 1 (dash) - 8 (hash) = 54 chars
	maxAutoscalerNameLen := 54
	autoscalerName := autoscaler.Name
	if len(autoscalerName) > maxAutoscalerNameLen {
		autoscalerName = autoscalerName[:maxAutoscalerNameLen]
		logger.V(1).Info("Truncated autoscaler name for template",
			"original", autoscaler.Name,
			"truncated", autoscalerName,
			"maxLength", maxAutoscalerNameLen)
	}

	newTemplateName := fmt.Sprintf("%s-%s", autoscalerName, hashStr)

	// Check if template with this revision already exists
	existingTemplate := &resourcev1.ResourceClaimTemplate{}
	err = s.client.Get(ctx, client.ObjectKey{
		Name:      newTemplateName,
		Namespace: autoscaler.Namespace,
	}, existingTemplate)

	if err == nil {
		// Template already exists, check if it needs updating
		if s.templateMatchesDesiredConfig(existingTemplate, desired, autoscaler.Spec.Target.ResourceRef.DeviceClassName) {
			logger.V(1).Info("ResourceClaimTemplate already exists with correct configuration", "template", newTemplateName)
			return newTemplateName, nil
		}
		// Template exists but needs updating - delete and recreate
		logger.Info("Deleting existing template with outdated configuration", "template", newTemplateName)
		if err := s.client.Delete(ctx, existingTemplate); err != nil {
			return "", fmt.Errorf("failed to delete outdated template: %w", err)
		}
	} else if !errors.IsNotFound(err) {
		return "", fmt.Errorf("failed to check existing template: %w", err)
	}

	// Create new ResourceClaimTemplate with updated resources
	newTemplate := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newTemplateName,
			Namespace: autoscaler.Namespace,
			Labels: map[string]string{
				ManagedByLabel:        OperatorName,
				RevisionLabel:         revision,
				OriginalTemplateLabel: originalTemplateName,
				TemplateNameLabel:     newTemplateName, // This label will be inherited by ResourceClaims
				AutoscalerLabel:       autoscaler.Name,
			},
			Annotations: map[string]string{
				"autoscaling.x-llmd.ai/autoscaler": autoscaler.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         autoscaler.APIVersion,
					Kind:               autoscaler.Kind,
					Name:               autoscaler.Name,
					UID:                autoscaler.UID,
					Controller:         func() *bool { b := true; return &b }(),
					BlockOwnerDeletion: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: *baseTemplate.Spec.DeepCopy(),
	}

	// Ensure the template spec has labels that will be applied to generated ResourceClaims
	if newTemplate.Spec.Labels == nil {
		newTemplate.Spec.Labels = make(map[string]string)
	}
	newTemplate.Spec.Labels[ManagedByLabel] = OperatorName
	newTemplate.Spec.Labels[TemplateNameLabel] = newTemplateName
	newTemplate.Spec.Labels[OriginalTemplateLabel] = originalTemplateName
	newTemplate.Spec.Labels[AutoscalerLabel] = autoscaler.Name

	// Update the device requests with new resource configuration
	targetDeviceClass := autoscaler.Spec.Target.ResourceRef.DeviceClassName
	updated := false

	for i := range newTemplate.Spec.Spec.Devices.Requests {
		request := &newTemplate.Spec.Spec.Devices.Requests[i]

		// Update Exactly field if it matches the target device class
		if request.Exactly != nil && request.Exactly.DeviceClassName == targetDeviceClass {
			request.Exactly.Count = int64(desired.GPUsPerReplica)
			if request.Exactly.Capacity == nil {
				request.Exactly.Capacity = &resourcev1.CapacityRequirements{}
			}
			if request.Exactly.Capacity.Requests == nil {
				request.Exactly.Capacity.Requests = make(map[resourcev1.QualifiedName]resource.Quantity)
			}
			// Update compute (as percentage)
			request.Exactly.Capacity.Requests["compute"] = *resource.NewQuantity(int64(desired.RequestedCompute), resource.DecimalSI)
			// Update memory (convert GB to bytes)
			memoryBytes := int64(desired.RequestedMemoryGB * 1024 * 1024 * 1024)
			request.Exactly.Capacity.Requests["memory"] = *resource.NewQuantity(memoryBytes, resource.BinarySI)
			updated = true
			break
		}

		// Update FirstAvailable subrequests if they match the target device class
		for j := range request.FirstAvailable {
			subReq := &request.FirstAvailable[j]
			if subReq.DeviceClassName == targetDeviceClass {
				subReq.Count = int64(desired.GPUsPerReplica)
				if subReq.Capacity == nil {
					subReq.Capacity = &resourcev1.CapacityRequirements{}
				}
				if subReq.Capacity.Requests == nil {
					subReq.Capacity.Requests = make(map[resourcev1.QualifiedName]resource.Quantity)
				}
				// Update compute (as percentage)
				subReq.Capacity.Requests["compute"] = *resource.NewQuantity(int64(desired.RequestedCompute), resource.DecimalSI)
				// Update memory (convert GB to bytes)
				memoryBytes := int64(desired.RequestedMemoryGB * 1024 * 1024 * 1024)
				subReq.Capacity.Requests["memory"] = *resource.NewQuantity(memoryBytes, resource.BinarySI)
				updated = true
				break
			}
		}
		if updated {
			break
		}
	}

	if !updated {
		return "", fmt.Errorf("failed to find device request matching target device class: %s", targetDeviceClass)
	}

	// Create the new template
	if err := s.client.Create(ctx, newTemplate); err != nil {
		return "", fmt.Errorf("failed to create new ResourceClaimTemplate: %w", err)
	}

	logger.Info("Created new ResourceClaimTemplate",
		"template", newTemplateName,
		"revision", revision,
		"gpus", desired.GPUsPerReplica,
		"compute", desired.RequestedCompute,
		"memoryGB", desired.RequestedMemoryGB)

	return newTemplateName, nil
}

// templateMatchesDesiredConfig checks if a template matches the desired configuration
func (s *ResourceScaler) templateMatchesDesiredConfig(
	template *resourcev1.ResourceClaimTemplate,
	desired optimizer.ResourceConfiguration,
	targetDeviceClass string,
) bool {
	for _, request := range template.Spec.Spec.Devices.Requests {
		// Check Exactly field
		if request.Exactly != nil && request.Exactly.DeviceClassName == targetDeviceClass {
			if int(request.Exactly.Count) != desired.GPUsPerReplica {
				return false
			}
			if request.Exactly.Capacity != nil && request.Exactly.Capacity.Requests != nil {
				if compute, ok := request.Exactly.Capacity.Requests["compute"]; ok {
					if compute.AsApproximateFloat64() != desired.RequestedCompute {
						return false
					}
				}
				if memory, ok := request.Exactly.Capacity.Requests["memory"]; ok {
					memoryGB := memory.AsApproximateFloat64() / (1024 * 1024 * 1024)
					if memoryGB != desired.RequestedMemoryGB {
						return false
					}
				}
			}
			return true
		}

		// Check FirstAvailable subrequests
		for _, subReq := range request.FirstAvailable {
			if subReq.DeviceClassName == targetDeviceClass {
				if int(subReq.Count) != desired.GPUsPerReplica {
					return false
				}
				if subReq.Capacity != nil && subReq.Capacity.Requests != nil {
					if compute, ok := subReq.Capacity.Requests["compute"]; ok {
						if compute.AsApproximateFloat64() != desired.RequestedCompute {
							return false
						}
					}
					if memory, ok := subReq.Capacity.Requests["memory"]; ok {
						memoryGB := memory.AsApproximateFloat64() / (1024 * 1024 * 1024)
						if memoryGB != desired.RequestedMemoryGB {
							return false
						}
					}
				}
				return true
			}
		}
	}
	return false
}

// getOwnerRevision gets the revision number from the owner (Deployment or StatefulSet)
func (s *ResourceScaler) getOwnerRevision(ctx context.Context, namespace string, owner *metav1.OwnerReference) (string, error) {
	switch owner.Kind {
	case WorkloadKindDeployment:
		deployment := &appsv1.Deployment{}
		if err := s.client.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: namespace}, deployment); err != nil {
			return "", fmt.Errorf("failed to get Deployment: %w", err)
		}
		// Use deployment's generation as revision
		return strconv.FormatInt(deployment.Generation, 10), nil

	case WorkloadKindStatefulSet:
		statefulSet := &appsv1.StatefulSet{}
		if err := s.client.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: namespace}, statefulSet); err != nil {
			return "", fmt.Errorf("failed to get StatefulSet: %w", err)
		}
		// Use statefulset's generation as revision
		return strconv.FormatInt(statefulSet.Generation, 10), nil

	default:
		return "", fmt.Errorf("unsupported owner kind: %s", owner.Kind)
	}
}

// patchPodOwner patches the Deployment or StatefulSet to use the new ResourceClaimTemplate
func (s *ResourceScaler) patchPodOwner(
	ctx context.Context,
	autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler,
	newTemplateName string,
	desiredReplicas int32,
) error {
	logger := s.logger.WithValues("autoscaler", autoscaler.Name, "namespace", autoscaler.Namespace)

	if autoscaler.Status.Discovery == nil || autoscaler.Status.Discovery.Owner == nil {
		return fmt.Errorf("no owner found in discovery status")
	}

	owner := autoscaler.Status.Discovery.Owner

	// Use the current desired template if available, otherwise use the original template
	currentTemplateName := autoscaler.Status.Discovery.ResourceClaim
	if autoscaler.Status.ScalingStatus != nil && autoscaler.Status.ScalingStatus.DesiredClaimTemplate != "" {
		currentTemplateName = autoscaler.Status.ScalingStatus.DesiredClaimTemplate
		logger.V(1).Info("Using current desired template for patching", "currentTemplate", currentTemplateName)
	}

	switch owner.Kind {
	case WorkloadKindDeployment:
		return s.patchDeployment(ctx, autoscaler.Namespace, owner.Name, currentTemplateName, newTemplateName, desiredReplicas, logger)

	case WorkloadKindStatefulSet:
		return s.patchStatefulSet(ctx, autoscaler.Namespace, owner.Name, currentTemplateName, newTemplateName, desiredReplicas, logger)

	default:
		return fmt.Errorf("unsupported owner kind: %s", owner.Kind)
	}
}

// patchDeployment patches a Deployment to use the new ResourceClaimTemplate
func (s *ResourceScaler) patchDeployment(
	ctx context.Context,
	namespace, name, oldTemplateName, newTemplateName string,
	desiredReplicas int32,
	logger logr.Logger,
) error {
	deployment := &appsv1.Deployment{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, deployment); err != nil {
		return fmt.Errorf("failed to get Deployment: %w", err)
	}

	// Update replicas
	deployment.Spec.Replicas = &desiredReplicas

	// Update ResourceClaim references in pod template
	updated := s.updatePodTemplateResourceClaims(&deployment.Spec.Template, oldTemplateName, newTemplateName)
	if !updated {
		logger.V(1).Info("No ResourceClaim references found to update in Deployment", "deployment", name)
	}

	// Update the deployment
	if err := s.client.Update(ctx, deployment); err != nil {
		return fmt.Errorf("failed to update Deployment: %w", err)
	}

	logger.Info("Patched Deployment",
		"deployment", name,
		"newTemplate", newTemplateName,
		"replicas", desiredReplicas)

	return nil
}

// patchStatefulSet patches a StatefulSet to use the new ResourceClaimTemplate
func (s *ResourceScaler) patchStatefulSet(
	ctx context.Context,
	namespace, name, oldTemplateName, newTemplateName string,
	desiredReplicas int32,
	logger logr.Logger,
) error {
	statefulSet := &appsv1.StatefulSet{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, statefulSet); err != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	// Update replicas
	statefulSet.Spec.Replicas = &desiredReplicas

	// Update ResourceClaim references in pod template
	updated := s.updatePodTemplateResourceClaims(&statefulSet.Spec.Template, oldTemplateName, newTemplateName)
	if !updated {
		logger.V(1).Info("No ResourceClaim references found to update in StatefulSet", "statefulSet", name)
	}

	// Update the statefulset
	if err := s.client.Update(ctx, statefulSet); err != nil {
		return fmt.Errorf("failed to update StatefulSet: %w", err)
	}

	logger.Info("Patched StatefulSet",
		"statefulSet", name,
		"newTemplate", newTemplateName,
		"replicas", desiredReplicas)

	return nil
}

// updatePodTemplateResourceClaims updates ResourceClaim references in a pod template
// Returns true if any updates were made
func (s *ResourceScaler) updatePodTemplateResourceClaims(
	podTemplate *corev1.PodTemplateSpec,
	oldTemplateName, newTemplateName string,
) bool {
	updated := false

	// Update ResourceClaims in pod spec
	for i := range podTemplate.Spec.ResourceClaims {
		claim := &podTemplate.Spec.ResourceClaims[i]
		if claim.ResourceClaimTemplateName != nil && *claim.ResourceClaimTemplateName == oldTemplateName {
			claim.ResourceClaimTemplateName = &newTemplateName
			updated = true
		}
	}

	return updated
}

// restoreAnyManagedTemplate restores any managed template back to original
// This is used when we don't know the exact current template name
func (s *ResourceScaler) restoreAnyManagedTemplate(
	podTemplate *corev1.PodTemplateSpec,
	originalTemplateName string,
) bool {
	updated := false

	// Update ResourceClaims in pod spec
	for i := range podTemplate.Spec.ResourceClaims {
		claim := &podTemplate.Spec.ResourceClaims[i]
		if claim.ResourceClaimTemplateName != nil {
			currentName := *claim.ResourceClaimTemplateName
			// If it's not the original template, restore it
			if currentName != originalTemplateName {
				claim.ResourceClaimTemplateName = &originalTemplateName
				updated = true
				s.logger.V(1).Info("Restoring managed template to original",
					"from", currentName,
					"to", originalTemplateName)
			}
		}
	}

	return updated
}

// CleanupOldResourceClaimTemplates removes old ResourceClaimTemplates that are no longer in use
// This should be called from the controller reconcile loop when ResourceClaims become bound
func (s *ResourceScaler) CleanupOldResourceClaimTemplates(
	ctx context.Context,
	autoscaler *autoscalingv1alpha1.ResourceClaimAutoscaler,
) error {
	logger := s.logger.WithValues("autoscaler", autoscaler.Name, "namespace", autoscaler.Namespace)

	if autoscaler.Status.Discovery == nil || autoscaler.Status.Discovery.ResourceClaim == "" {
		return nil
	}

	originalTemplateName := autoscaler.Status.Discovery.ResourceClaim

	// List all ResourceClaimTemplates managed by this operator for this original template
	templateList := &resourcev1.ResourceClaimTemplateList{}
	if err := s.client.List(ctx, templateList,
		client.InNamespace(autoscaler.Namespace),
		client.MatchingLabels{
			ManagedByLabel:        OperatorName,
			OriginalTemplateLabel: originalTemplateName,
		},
	); err != nil {
		return fmt.Errorf("failed to list ResourceClaimTemplates: %w", err)
	}

	// List ResourceClaims managed by this specific autoscaler
	claimList := &resourcev1.ResourceClaimList{}
	if err := s.client.List(ctx, claimList,
		client.InNamespace(autoscaler.Namespace),
		client.MatchingLabels{
			ManagedByLabel:        OperatorName,
			AutoscalerLabel:       autoscaler.Name,
			OriginalTemplateLabel: originalTemplateName,
		},
	); err != nil {
		return fmt.Errorf("failed to list ResourceClaims: %w", err)
	}

	// Build set of templates that have bound claims
	templatesWithBoundClaims := make(map[string]bool)
	templatesWithPendingClaims := make(map[string]bool)

	for _, claim := range claimList.Items {
		// Get the template name from labels
		if claim.Labels != nil {
			if templateName, ok := claim.Labels[TemplateNameLabel]; ok {
				// Check if claim is bound (has allocation)
				if claim.Status.Allocation != nil {
					templatesWithBoundClaims[templateName] = true
					logger.V(1).Info("Found bound claim for template", "template", templateName, "claim", claim.Name)
				} else {
					// Claim is pending (not yet bound)
					templatesWithPendingClaims[templateName] = true
					logger.V(1).Info("Found pending claim for template", "template", templateName, "claim", claim.Name)
				}
			}
		}
	}

	desiredTemplate := ""
	// Keep the current desired template
	if autoscaler.Status.ScalingStatus != nil && autoscaler.Status.ScalingStatus.DesiredClaimTemplate != "" {
		desiredTemplate = autoscaler.Status.ScalingStatus.DesiredClaimTemplate
	}

	// Delete templates that have no bound claims and no pending claims
	for _, template := range templateList.Items {
		if template.Name == desiredTemplate {
			continue
		}
		// Only delete if:
		// No bound claims reference this template
		if !templatesWithBoundClaims[template.Name] && !templatesWithPendingClaims[template.Name] {
			logger.Info("Deleting unused ResourceClaimTemplate", "template", template.Name)
			if err := s.client.Delete(ctx, &template); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "Failed to delete ResourceClaimTemplate", "template", template.Name)
				}
			}
		}
	}

	return nil
}

// RestoreDeploymentTemplate restores a Deployment to use the original ResourceClaimTemplate
func (s *ResourceScaler) RestoreDeploymentTemplate(
	ctx context.Context,
	namespace, name, currentTemplateName, originalTemplateName string,
) error {
	logger := s.logger.WithValues("deployment", name, "namespace", namespace)

	deployment := &appsv1.Deployment{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, deployment); err != nil {
		return fmt.Errorf("failed to get Deployment: %w", err)
	}

	// Update ResourceClaim references in pod template
	// If currentTemplateName is provided, use it; otherwise restore any managed template
	updated := false
	if currentTemplateName != "" {
		updated = s.updatePodTemplateResourceClaims(&deployment.Spec.Template, currentTemplateName, originalTemplateName)
	} else {
		// Restore any managed template (has our labels) back to original
		updated = s.restoreAnyManagedTemplate(&deployment.Spec.Template, originalTemplateName)
	}

	if !updated {
		logger.V(1).Info("No ResourceClaim references found to restore in Deployment")
		return nil
	}

	// Update the deployment
	if err := s.client.Update(ctx, deployment); err != nil {
		return fmt.Errorf("failed to update Deployment: %w", err)
	}

	logger.Info("Restored Deployment to original template",
		"originalTemplate", originalTemplateName)

	return nil
}

// RestoreStatefulSetTemplate restores a StatefulSet to use the original ResourceClaimTemplate
func (s *ResourceScaler) RestoreStatefulSetTemplate(
	ctx context.Context,
	namespace, name, currentTemplateName, originalTemplateName string,
) error {
	logger := s.logger.WithValues("statefulSet", name, "namespace", namespace)

	statefulSet := &appsv1.StatefulSet{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, statefulSet); err != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	// Update ResourceClaim references in pod template
	// If currentTemplateName is provided, use it; otherwise restore any managed template
	updated := false
	if currentTemplateName != "" {
		updated = s.updatePodTemplateResourceClaims(&statefulSet.Spec.Template, currentTemplateName, originalTemplateName)
	} else {
		// Restore any managed template (has our labels) back to original
		updated = s.restoreAnyManagedTemplate(&statefulSet.Spec.Template, originalTemplateName)
	}

	if !updated {
		logger.V(1).Info("No ResourceClaim references found to restore in StatefulSet")
		return nil
	}

	// Update the statefulset
	if err := s.client.Update(ctx, statefulSet); err != nil {
		return fmt.Errorf("failed to update StatefulSet: %w", err)
	}

	logger.Info("Restored StatefulSet to original template",
		"originalTemplate", originalTemplateName)

	return nil
}

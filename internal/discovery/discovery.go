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

package discovery

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// Workload owner kinds
	kindPod         = "Pod"
	kindDeployment  = "Deployment"
	kindStatefulSet = "StatefulSet"
	kindReplicaSet  = "ReplicaSet"
)

// ServiceDiscoverer discovers workload owners and resource claim templates from services
type ServiceDiscoverer struct {
	client client.Client
}

// NewServiceDiscoverer creates a new ServiceDiscoverer
func NewServiceDiscoverer(c client.Client) *ServiceDiscoverer {
	return &ServiceDiscoverer{
		client: c,
	}
}

// DiscoveryResult contains the results of service discovery
type DiscoveryResult struct {
	Owner                 *metav1.OwnerReference
	ResourceClaimTemplate string
}

// DiscoverFromService discovers the workload owner and resource claim template from a service
// If targetDeviceClass is provided, it validates that the discovered template matches that device class
func (d *ServiceDiscoverer) DiscoverFromService(ctx context.Context, serviceName, serviceNamespace, targetDeviceClass string) (*DiscoveryResult, error) {
	logger := log.FromContext(ctx)

	// Get the service
	service := &corev1.Service{}
	serviceKey := types.NamespacedName{
		Name:      serviceName,
		Namespace: serviceNamespace,
	}
	if err := d.client.Get(ctx, serviceKey, service); err != nil {
		return nil, fmt.Errorf("failed to get service %s: %w", serviceKey, err)
	}

	logger.V(1).Info("Found service", "name", service.Name, "namespace", service.Namespace)

	// List EndpointSlices for the service using label selector
	endpointSliceList := &discoveryv1.EndpointSliceList{}
	listOpts := []client.ListOption{
		client.InNamespace(serviceNamespace),
		client.MatchingLabels{
			discoveryv1.LabelServiceName: serviceName,
		},
	}
	if err := d.client.List(ctx, endpointSliceList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list EndpointSlices for service %s: %w", serviceKey, err)
	}

	// Collect all pod names from EndpointSlices
	var podNames []string
	for _, endpointSlice := range endpointSliceList.Items {
		for _, endpoint := range endpointSlice.Endpoints {
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == kindPod {
				podNames = append(podNames, endpoint.TargetRef.Name)
			}
		}
	}

	if len(podNames) == 0 {
		logger.Info("No pods found in service EndpointSlices", "service", serviceKey)
		return nil, nil
	}

	logger.V(1).Info("Found pods in EndpointSlices", "count", len(podNames), "service", serviceKey)

	// Discover owners for all pods and verify they are the same
	var commonOwner *metav1.OwnerReference
	for i, podName := range podNames {
		pod := &corev1.Pod{}
		podKey := types.NamespacedName{
			Name:      podName,
			Namespace: serviceNamespace,
		}
		if err := d.client.Get(ctx, podKey, pod); err != nil {
			logger.Error(err, "Failed to get pod, skipping", "pod", podKey)
			continue
		}

		owner := d.discoverOwner(ctx, pod)
		if owner == nil {
			logger.Info("No owner found for pod", "pod", podKey)
			continue
		}

		if i == 0 {
			// First pod sets the common owner
			commonOwner = owner
			logger.V(1).Info("Found first owner", "kind", owner.Kind, "name", owner.Name)
		} else {
			// Verify subsequent pods have the same owner
			if commonOwner.Kind != owner.Kind || commonOwner.Name != owner.Name {
				return nil, fmt.Errorf("inconsistent owners detected: pod %s has owner %s/%s, but expected %s/%s",
					podName, owner.Kind, owner.Name, commonOwner.Kind, commonOwner.Name)
			}
		}
	}

	if commonOwner == nil {
		logger.Info("No common owner found for service endpoints", "service", serviceKey)
		return nil, nil
	}

	result := &DiscoveryResult{
		Owner: commonOwner,
	}

	// Discover ResourceClaimTemplate from the workload
	claimTemplate, err := d.discoverResourceClaimTemplate(ctx, commonOwner, serviceNamespace, targetDeviceClass)
	if err != nil {
		return nil, fmt.Errorf("failed to discover ResourceClaimTemplate: %w", err)
	}
	result.ResourceClaimTemplate = claimTemplate

	logger.Info("Discovered workload owner and template",
		"kind", commonOwner.Kind,
		"name", commonOwner.Name,
		"template", claimTemplate,
		"service", serviceKey,
		"podsChecked", len(podNames))

	return result, nil
}

// discoverOwner discovers the top-level owner by recursively traversing the ownership chain
// Supports: Pod, ReplicaSet, Deployment, StatefulSet
// Returns the resource at the top of the ownership chain (the one with no owner)
func (d *ServiceDiscoverer) discoverOwner(ctx context.Context, pod *corev1.Pod) *metav1.OwnerReference {
	logger := log.FromContext(ctx)

	// Start with the pod and traverse up the ownership chain
	currentKind := kindPod
	currentName := pod.Name
	currentNamespace := pod.Namespace
	currentUID := pod.UID
	currentAPIVersion := "v1"

	// Keep traversing until we find a resource with no owner
	for {
		var ownerRefs []metav1.OwnerReference

		switch currentKind {
		case kindPod:
			obj := &corev1.Pod{}
			if err := d.client.Get(ctx, types.NamespacedName{Name: currentName, Namespace: currentNamespace}, obj); err != nil {
				logger.Error(err, "Failed to get Pod", "name", currentName)
				return nil
			}
			ownerRefs = obj.OwnerReferences

		case kindReplicaSet:
			obj := &appsv1.ReplicaSet{}
			if err := d.client.Get(ctx, types.NamespacedName{Name: currentName, Namespace: currentNamespace}, obj); err != nil {
				logger.Error(err, "Failed to get ReplicaSet", "name", currentName)
				return nil
			}
			ownerRefs = obj.OwnerReferences

		case kindDeployment:
			obj := &appsv1.Deployment{}
			if err := d.client.Get(ctx, types.NamespacedName{Name: currentName, Namespace: currentNamespace}, obj); err != nil {
				logger.Error(err, "Failed to get Deployment", "name", currentName)
				return nil
			}
			ownerRefs = obj.OwnerReferences

		case kindStatefulSet:
			obj := &appsv1.StatefulSet{}
			if err := d.client.Get(ctx, types.NamespacedName{Name: currentName, Namespace: currentNamespace}, obj); err != nil {
				logger.Error(err, "Failed to get StatefulSet", "name", currentName)
				return nil
			}
			ownerRefs = obj.OwnerReferences

		default:
			// Unsupported kind, return current as owner
			logger.V(1).Info("Unsupported owner kind, stopping traversal", "kind", currentKind, "name", currentName)
			return &metav1.OwnerReference{
				APIVersion: currentAPIVersion,
				Kind:       currentKind,
				Name:       currentName,
				UID:        currentUID,
			}
		}

		// If no owner references, this is the top-level owner
		if len(ownerRefs) == 0 {
			logger.V(1).Info("Found top-level owner (no parent)", "kind", currentKind, "name", currentName)
			return &metav1.OwnerReference{
				APIVersion: currentAPIVersion,
				Kind:       currentKind,
				Name:       currentName,
				UID:        currentUID,
			}
		}

		// Find the controller owner reference
		var controllerOwner *metav1.OwnerReference
		for i := range ownerRefs {
			if ownerRefs[i].Controller != nil && *ownerRefs[i].Controller {
				controllerOwner = &ownerRefs[i]
				break
			}
		}

		// If no controller owner found, current resource is the top-level owner
		if controllerOwner == nil {
			logger.V(1).Info("No controller owner found, returning current as owner", "kind", currentKind, "name", currentName)
			return &metav1.OwnerReference{
				APIVersion: currentAPIVersion,
				Kind:       currentKind,
				Name:       currentName,
				UID:        currentUID,
			}
		}

		// Check if the owner kind is supported
		if controllerOwner.Kind != kindReplicaSet &&
			controllerOwner.Kind != kindDeployment && controllerOwner.Kind != kindStatefulSet {
			// Unsupported owner kind, return current as owner
			logger.V(1).Info("Owner has unsupported kind, returning current as owner",
				"currentKind", currentKind, "currentName", currentName,
				"ownerKind", controllerOwner.Kind, "ownerName", controllerOwner.Name)
			return &metav1.OwnerReference{
				APIVersion: currentAPIVersion,
				Kind:       currentKind,
				Name:       currentName,
				UID:        currentUID,
			}
		}

		// Move up to the owner
		logger.V(1).Info("Traversing to owner", "from", currentKind+"/"+currentName, "to", controllerOwner.Kind+"/"+controllerOwner.Name)
		currentKind = controllerOwner.Kind
		currentName = controllerOwner.Name
		currentUID = controllerOwner.UID
		currentAPIVersion = controllerOwner.APIVersion
	}
}

// discoverResourceClaimTemplate discovers the ResourceClaimTemplate from the workload
// If targetDeviceClass is provided, validates that the template matches that device class
func (d *ServiceDiscoverer) discoverResourceClaimTemplate(ctx context.Context, owner *metav1.OwnerReference, namespace, targetDeviceClass string) (string, error) {
	logger := log.FromContext(ctx)

	if owner == nil {
		return "", fmt.Errorf("no owner provided")
	}

	// Get the workload (Deployment, StatefulSet, or Pod) to find ResourceClaimTemplate
	var podSpec *corev1.PodSpec

	switch owner.Kind {
	case kindDeployment:
		deployment := &appsv1.Deployment{}
		deploymentKey := types.NamespacedName{
			Name:      owner.Name,
			Namespace: namespace,
		}
		if err := d.client.Get(ctx, deploymentKey, deployment); err != nil {
			return "", fmt.Errorf("failed to get Deployment %s: %w", deploymentKey, err)
		}
		podSpec = &deployment.Spec.Template.Spec

	case kindStatefulSet:
		statefulSet := &appsv1.StatefulSet{}
		statefulSetKey := types.NamespacedName{
			Name:      owner.Name,
			Namespace: namespace,
		}
		if err := d.client.Get(ctx, statefulSetKey, statefulSet); err != nil {
			return "", fmt.Errorf("failed to get StatefulSet %s: %w", statefulSetKey, err)
		}
		podSpec = &statefulSet.Spec.Template.Spec

	case kindPod:
		// For standalone pods, get the pod directly
		pod := &corev1.Pod{}
		podKey := types.NamespacedName{
			Name:      owner.Name,
			Namespace: namespace,
		}
		if err := d.client.Get(ctx, podKey, pod); err != nil {
			return "", fmt.Errorf("failed to get Pod %s: %w", podKey, err)
		}
		podSpec = &pod.Spec

	default:
		return "", fmt.Errorf("unsupported owner kind: %s", owner.Kind)
	}

	// Find ResourceClaimTemplate references in the pod spec
	var claimTemplateName string
	for _, claim := range podSpec.ResourceClaims {
		if claim.ResourceClaimTemplateName != nil {
			// Found a ResourceClaimTemplate reference
			claimTemplateName = *claim.ResourceClaimTemplateName
			break
		}
	}

	if claimTemplateName == "" {
		return "", fmt.Errorf("no ResourceClaimTemplate found in workload %s/%s", owner.Kind, owner.Name)
	}

	logger.V(1).Info("Found ResourceClaimTemplate reference", "name", claimTemplateName)

	// Get the ResourceClaimTemplate to validate it exists
	claimTemplate := &resourcev1.ResourceClaimTemplate{}
	claimTemplateKey := types.NamespacedName{
		Name:      claimTemplateName,
		Namespace: namespace,
	}
	if err := d.client.Get(ctx, claimTemplateKey, claimTemplate); err != nil {
		return "", fmt.Errorf("failed to get ResourceClaimTemplate %s: %w", claimTemplateKey, err)
	}

	// If targetDeviceClass is specified and not empty, validate that the template matches
	if targetDeviceClass != "" {
		// Check if the template has device requests
		if len(claimTemplate.Spec.Spec.Devices.Requests) == 0 {
			return "", fmt.Errorf("ResourceClaimTemplate %s has no device requests to match device class %s", claimTemplateName, targetDeviceClass)
		}

		// Check if any request matches the target device class
		found := false
		for _, req := range claimTemplate.Spec.Spec.Devices.Requests {
			// Check Exactly field for device class
			if req.Exactly != nil && req.Exactly.DeviceClassName == targetDeviceClass {
				found = true
				break
			}
			// Check FirstAvailable subrequests
			for _, subReq := range req.FirstAvailable {
				if subReq.DeviceClassName == targetDeviceClass {
					found = true
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			return "", fmt.Errorf("ResourceClaimTemplate %s does not have device class %s", claimTemplateName, targetDeviceClass)
		}

		logger.Info("Validated ResourceClaimTemplate device class",
			"template", claimTemplateName,
			"deviceClass", targetDeviceClass,
			"workload", fmt.Sprintf("%s/%s", owner.Kind, owner.Name))
	} else {
		logger.Info("Discovered ResourceClaimTemplate",
			"template", claimTemplateName,
			"workload", fmt.Sprintf("%s/%s", owner.Kind, owner.Name))
	}

	return claimTemplateName, nil
}

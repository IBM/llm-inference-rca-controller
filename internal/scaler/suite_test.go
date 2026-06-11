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

// This file exports internal functions for testing purposes only.
// It uses the package scaler (not scaler_test) so it can access unexported identifiers.

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Exported for testing - method value assignments
var (
	DetermineScalingAction           = (*ResourceScaler).determineScalingAction
	BuildResourceChangeReason        = (*ResourceScaler).buildResourceChangeReason
	ApplySingleTimeScalingLimit      = (*ResourceScaler).applySingleTimeScalingLimit
	CheckMinTargetUtilization        = (*ResourceScaler).checkMinTargetUtilization
	CalculateResourceUtilization     = (*ResourceScaler).calculateResourceUtilization
	GetArrivalRate                   = (*ResourceScaler).getArrivalRate
	GetPromptTokens                  = (*ResourceScaler).getPromptTokens
	GetOutputTokens                  = (*ResourceScaler).getOutputTokens
	GetMaxNumSeqs                    = (*ResourceScaler).getMaxNumSeqs
	GetPrefillTokens                 = (*ResourceScaler).getPrefillTokens
	GetCacheHitRatio                 = (*ResourceScaler).getCacheHitRatio
	GetComputeThreadUtilization      = (*ResourceScaler).getComputeThreadUtilization
	GetMeasuredMemoryUsage           = (*ResourceScaler).getMeasuredMemoryUsage
	GetNumGPU                        = (*ResourceScaler).getNumGPU
	GetInterTokenLatency             = (*ResourceScaler).getInterTokenLatency
	GetTimeToFirstToken              = (*ResourceScaler).getTimeToFirstToken
	IsValidMetric                    = (*ResourceScaler).isValidMetric
	ParseFloat                       = (*ResourceScaler).parseFloat
	GetTargetLatency                 = (*ResourceScaler).getTargetLatency
	ApplyScalingAction               = (*ResourceScaler).applyScalingAction
	GetCurrentResourcesFromTemplate  = (*ResourceScaler).getCurrentResourcesFromTemplate
	TemplateMatchesDesiredConfig     = (*ResourceScaler).templateMatchesDesiredConfig
	UpdatePodTemplateResourceClaims  = (*ResourceScaler).updatePodTemplateResourceClaims
	GetOwnerRevision                 = (*ResourceScaler).getOwnerRevision
	PatchDeployment                  = (*ResourceScaler).patchDeployment
	PatchStatefulSet                 = (*ResourceScaler).patchStatefulSet
	PatchPodOwner                    = (*ResourceScaler).patchPodOwner
	UpdateReplicaStatusFromOwner     = (*ResourceScaler).updateReplicaStatusFromOwner
	PatchResourceClaimTemplate       = (*ResourceScaler).patchResourceClaimTemplate
	CleanupOldResourceClaimTemplates = (*ResourceScaler).CleanupOldResourceClaimTemplates
)

// GetClient returns the client for testing
func (s *ResourceScaler) GetClient() client.Client {
	return s.client
}

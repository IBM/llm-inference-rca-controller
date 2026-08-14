package vpa

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
)

// Updater handles updating VPA status with recommendations
type Updater struct {
	client vpa_clientset.Interface
}

// NewUpdater creates a new VPA updater
func NewUpdater(client vpa_clientset.Interface) *Updater {
	return &Updater{
		client: client,
	}
}

// UpdateRecommendation updates the VPA status with the given recommendation and
// sets the RecommendationProvided condition to True.
func (u *Updater) UpdateRecommendation(ctx context.Context, vpa *vpa_types.VerticalPodAutoscaler, recommendation *vpa_types.RecommendedPodResources) error {
	// Create a copy of the VPA to modify
	vpaCopy := vpa.DeepCopy()

	// Update the recommendation in the status
	vpaCopy.Status.Recommendation = recommendation

	// Set RecommendationProvided condition to True
	setCondition(&vpaCopy.Status.Conditions, vpa_types.RecommendationProvided, corev1.ConditionTrue, "", "")

	// Update the VPA status
	_, err := u.client.AutoscalingV1().VerticalPodAutoscalers(vpa.Namespace).UpdateStatus(ctx, vpaCopy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update VPA status: %w", err)
	}

	klog.V(4).InfoS("Updated VPA status",
		"vpa", klog.KObj(vpa),
		"containers", len(recommendation.ContainerRecommendations),
	)

	return nil
}

// SetConditionFailed updates the VPA status to reflect a failure to provide a
// recommendation by setting the RecommendationProvided condition to False.
func (u *Updater) SetConditionFailed(ctx context.Context, vpa *vpa_types.VerticalPodAutoscaler, reason, message string) error {
	vpaCopy := vpa.DeepCopy()

	setCondition(&vpaCopy.Status.Conditions, vpa_types.RecommendationProvided, corev1.ConditionFalse, reason, message)

	_, err := u.client.AutoscalingV1().VerticalPodAutoscalers(vpa.Namespace).UpdateStatus(ctx, vpaCopy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update VPA status conditions: %w", err)
	}

	klog.V(4).InfoS("Set VPA condition failed",
		"vpa", klog.KObj(vpa),
		"reason", reason,
	)

	return nil
}

// setCondition upserts a VerticalPodAutoscalerCondition in the slice.
// LastTransitionTime is updated only when the Status value changes.
func setCondition(conditions *[]vpa_types.VerticalPodAutoscalerCondition, condType vpa_types.VerticalPodAutoscalerConditionType, status corev1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range *conditions {
		if c.Type == condType {
			if c.Status != status {
				(*conditions)[i].LastTransitionTime = now
			}
			(*conditions)[i].Status = status
			(*conditions)[i].Reason = reason
			(*conditions)[i].Message = message
			return
		}
	}
	*conditions = append(*conditions, vpa_types.VerticalPodAutoscalerCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}

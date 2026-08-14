package vpa_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_fake "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/fake"

	"github.com/yourusername/prometheus-vpa-recommender/pkg/vpa"
)

var _ = Describe("Updater", func() {
	var (
		ctx        context.Context
		fakeClient *vpa_fake.Clientset
		updater    *vpa.Updater
		testVPA    *vpa_types.VerticalPodAutoscaler
	)

	BeforeEach(func() {
		ctx = context.Background()

		testVPA = &vpa_types.VerticalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-vpa",
				Namespace: "default",
			},
		}

		fakeClient = vpa_fake.NewSimpleClientset(testVPA)
		updater = vpa.NewUpdater(fakeClient)
	})

	Describe("UpdateRecommendation", func() {
		It("should set RecommendationProvided condition to True", func() {
			recommendation := &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{ContainerName: "app"},
				},
			}

			err := updater.UpdateRecommendation(ctx, testVPA, recommendation)
			Expect(err).NotTo(HaveOccurred())

			updated, err := fakeClient.AutoscalingV1().VerticalPodAutoscalers("default").Get(ctx, "test-vpa", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			cond := findCondition(updated.Status.Conditions, vpa_types.RecommendationProvided)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(corev1.ConditionTrue))
		})

		It("should update LastTransitionTime only when status changes", func() {
			recommendation := &vpa_types.RecommendedPodResources{}

			// First call — condition transitions from absent to True
			err := updater.UpdateRecommendation(ctx, testVPA, recommendation)
			Expect(err).NotTo(HaveOccurred())

			updated, _ := fakeClient.AutoscalingV1().VerticalPodAutoscalers("default").Get(ctx, "test-vpa", metav1.GetOptions{})
			firstTime := findCondition(updated.Status.Conditions, vpa_types.RecommendationProvided).LastTransitionTime

			// Second call with same status — LastTransitionTime must not change
			err = updater.UpdateRecommendation(ctx, updated, recommendation)
			Expect(err).NotTo(HaveOccurred())

			updated2, _ := fakeClient.AutoscalingV1().VerticalPodAutoscalers("default").Get(ctx, "test-vpa", metav1.GetOptions{})
			secondTime := findCondition(updated2.Status.Conditions, vpa_types.RecommendationProvided).LastTransitionTime

			Expect(secondTime).To(Equal(firstTime))
		})
	})

	Describe("SetConditionFailed", func() {
		It("should set RecommendationProvided condition to False with reason and message", func() {
			err := updater.SetConditionFailed(ctx, testVPA, "BuildFailed", "no metrics found")
			Expect(err).NotTo(HaveOccurred())

			updated, err := fakeClient.AutoscalingV1().VerticalPodAutoscalers("default").Get(ctx, "test-vpa", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			cond := findCondition(updated.Status.Conditions, vpa_types.RecommendationProvided)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(corev1.ConditionFalse))
			Expect(cond.Reason).To(Equal("BuildFailed"))
			Expect(cond.Message).To(Equal("no metrics found"))
		})

		It("should update LastTransitionTime when condition transitions from True to False", func() {
			recommendation := &vpa_types.RecommendedPodResources{}

			// Establish a True condition first
			err := updater.UpdateRecommendation(ctx, testVPA, recommendation)
			Expect(err).NotTo(HaveOccurred())

			updated, _ := fakeClient.AutoscalingV1().VerticalPodAutoscalers("default").Get(ctx, "test-vpa", metav1.GetOptions{})
			trueTime := findCondition(updated.Status.Conditions, vpa_types.RecommendationProvided).LastTransitionTime

			// Transition to False — LastTransitionTime must advance
			err = updater.SetConditionFailed(ctx, updated, "BuildFailed", "no metrics found")
			Expect(err).NotTo(HaveOccurred())

			updated2, _ := fakeClient.AutoscalingV1().VerticalPodAutoscalers("default").Get(ctx, "test-vpa", metav1.GetOptions{})
			falseTime := findCondition(updated2.Status.Conditions, vpa_types.RecommendationProvided).LastTransitionTime

			Expect(falseTime.After(trueTime.Time) || falseTime.Equal(&trueTime)).To(BeTrue())
		})
	})
})

// findCondition returns the first condition of the given type, or nil if not present.
func findCondition(conditions []vpa_types.VerticalPodAutoscalerCondition, condType vpa_types.VerticalPodAutoscalerConditionType) *vpa_types.VerticalPodAutoscalerCondition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

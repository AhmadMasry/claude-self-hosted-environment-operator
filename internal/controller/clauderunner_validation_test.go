package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

var _ = Describe("ClaudeRunner validation", func() {
	ctx := context.Background()

	It("accepts a minimal runner, defaults nothing, and rejects spec changes", func() {
		ns := newNamespace(ctx)
		r := &selfhostedv1alpha1.ClaudeRunner{
			ObjectMeta: metav1.ObjectMeta{Name: "order-abc", Namespace: ns},
			Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
				EnvironmentRef:     selfhostedv1alpha1.LocalObjectRef{Name: "env-a"},
				OrderID:            "order-abc",
				WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "order-abc-work-order"},
			},
		}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		Expect(r.Status.Phase).To(BeEmpty())

		r.Spec.SessionID = "session_changed"
		err := k8sClient.Update(ctx, r)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec is immutable"))
	})

	It("rejects an empty orderID", func() {
		ns := newNamespace(ctx)
		r := &selfhostedv1alpha1.ClaudeRunner{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
			Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
				EnvironmentRef:     selfhostedv1alpha1.LocalObjectRef{Name: "env-a"},
				WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "x"},
			},
		}
		Expect(k8sClient.Create(ctx, r)).NotTo(Succeed())
	})
})

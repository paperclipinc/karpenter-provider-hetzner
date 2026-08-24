package operator

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClusterUID_ReadsKubeSystemUID(t *testing.T) {
	const want = "34f25cbf-c7b5-49d1-833b-103bff8a34ad"
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: want},
	}).Build()

	got, err := ClusterUID(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Falling back to the name alone would quietly reinstate the cross-cluster
// deletion this identifier exists to prevent, so an unusable UID is an error.
func TestClusterUID_EmptyUIDIsAnError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system"},
	}).Build()

	if _, err := ClusterUID(context.Background(), c); err == nil {
		t.Error("expected an error when the namespace carries no UID")
	}
}

func TestClusterUID_MissingNamespaceIsAnError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()

	if _, err := ClusterUID(context.Background(), c); err == nil {
		t.Error("expected an error when kube-system cannot be read")
	}
}

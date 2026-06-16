package cloudprovider

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
)

func TestResolveUserData_Inline(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	nc := &v1alpha1.HCloudNodeClass{Spec: v1alpha1.HCloudNodeClassSpec{UserData: "#cloud-config\n"}}
	got, err := resolveUserData(context.Background(), c, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "#cloud-config\n" {
		t.Fatalf("got %q, want inline userData", got)
	}
}

func TestResolveUserData_SecretPrecedence(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos-worker", Namespace: "karpenter"},
		Data:       map[string][]byte{"userData": []byte("machineconfig-yaml")},
	}
	c := fake.NewClientBuilder().WithObjects(secret).Build()
	nc := &v1alpha1.HCloudNodeClass{Spec: v1alpha1.HCloudNodeClassSpec{
		UserData:     "inline-should-be-ignored",
		BootstrapRef: &v1alpha1.BootstrapRef{Name: "talos-worker", Namespace: "karpenter"},
	}}
	got, err := resolveUserData(context.Background(), c, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "machineconfig-yaml" {
		t.Fatalf("got %q, want secret value", got)
	}
}

func TestResolveUserData_MissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos-worker", Namespace: "karpenter"},
		Data:       map[string][]byte{"wrong": []byte("x")},
	}
	c := fake.NewClientBuilder().WithObjects(secret).Build()
	nc := &v1alpha1.HCloudNodeClass{Spec: v1alpha1.HCloudNodeClassSpec{
		BootstrapRef: &v1alpha1.BootstrapRef{Name: "talos-worker", Namespace: "karpenter", Key: "userData"},
	}}
	if _, err := resolveUserData(context.Background(), c, nc); err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
}

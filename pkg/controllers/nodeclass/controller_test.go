package nodeclass

import (
	"context"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
)

type fakeNetworks struct{ net *hcloud.Network }

func (f fakeNetworks) GetByID(_ context.Context, _ int64) (*hcloud.Network, *hcloud.Response, error) {
	return f.net, nil, nil
}

type fakeImages struct{ img *hcloud.Image }

func (f fakeImages) AllWithOpts(_ context.Context, _ hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return []*hcloud.Image{f.img}, nil
}

func newNodeClass() *v1alpha1.HCloudNodeClass {
	return &v1alpha1.HCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.HCloudNodeClassSpec{
			Locations:     []string{"nbg1"},
			NetworkID:     1,
			ImageSelector: v1alpha1.ImageSelector{Family: "ubuntu"},
		},
	}
}

func TestReconcile_SetsReadyWhenValid(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &v1alpha1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(v1alpha1.ConditionTypeNetworkReady).IsTrue() {
		t.Error("NetworkReady should be true")
	}
	if len(got.Status.ResolvedImages) == 0 {
		t.Error("expected resolved images")
	}
	if !got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should be true when all dependents are true")
	}
}

func TestReconcile_NetworkNotFound(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: nil}, img) // network missing

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &v1alpha1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if got.StatusConditions().Get(v1alpha1.ConditionTypeNetworkReady).IsTrue() {
		t.Error("NetworkReady should be false when network is missing")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when network is missing")
	}
}

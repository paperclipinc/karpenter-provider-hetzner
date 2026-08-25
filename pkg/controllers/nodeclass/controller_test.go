package nodeclass

import (
	"context"
	"errors"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
)

type fakeNetworks struct{ net *hcloud.Network }

func (f fakeNetworks) GetByID(_ context.Context, _ int64) (*hcloud.Network, *hcloud.Response, error) {
	return f.net, nil, nil
}

type fakeFirewalls struct{ fw *hcloud.Firewall }

func (f fakeFirewalls) GetByID(_ context.Context, _ int64) (*hcloud.Firewall, *hcloud.Response, error) {
	return f.fw, nil, nil
}

type fakeSSHKeys struct{ key *hcloud.SSHKey }

func (f fakeSSHKeys) GetByID(_ context.Context, _ int64) (*hcloud.SSHKey, *hcloud.Response, error) {
	return f.key, nil, nil
}

type fakeImages struct{ img *hcloud.Image }

func (f fakeImages) AllWithOpts(_ context.Context, _ hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return []*hcloud.Image{f.img}, nil
}

type emptyImages struct{}

func (emptyImages) AllWithOpts(_ context.Context, _ hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return nil, nil
}

// amd64OnlyImages returns an image for amd64/x86 requests and nothing for arm64,
// mimicking a cluster that only has an x86 OS image (the common case).
type amd64OnlyImages struct{ img *hcloud.Image }

func (f amd64OnlyImages) AllWithOpts(_ context.Context, opts hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	for _, a := range opts.Architecture {
		if a == hcloud.ArchitectureX86 {
			return []*hcloud.Image{f.img}, nil
		}
	}
	return nil, nil
}

func newNodeClass() *apiv1.HCloudNodeClass {
	return &apiv1.HCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: apiv1.HCloudNodeClassSpec{
			Locations:     []string{"nbg1"},
			NetworkID:     1,
			ImageSelector: apiv1.ImageSelector{Family: "ubuntu"},
		},
	}
}

func TestReconcile_SetsReadyWhenValid(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(apiv1.ConditionTypeNetworkReady).IsTrue() {
		t.Error("NetworkReady should be true")
	}
	if len(got.Status.ResolvedImages) == 0 {
		t.Error("expected resolved images")
	}
	for _, ri := range got.Status.ResolvedImages {
		if ri.Architecture == "" {
			t.Error("resolved image missing architecture")
		}
	}
	if !got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should be true when all dependents are true")
	}
}

func TestReconcile_SingleArchImageIsReady(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	// Cluster only has an amd64 image (no arm64) — the NodeClass must still be Ready.
	img := imagefamily.NewProvider(amd64OnlyImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(apiv1.ConditionTypeImagesReady).IsTrue() {
		t.Error("ImagesReady should be true when at least one arch resolves")
	}
	if !got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should be true with a single-arch image")
	}
	if len(got.Status.ResolvedImages) != 1 || got.Status.ResolvedImages[0].Architecture != "x86" {
		t.Errorf("expected exactly one x86 resolved image, got %+v", got.Status.ResolvedImages)
	}
}

func TestReconcile_ImageResolutionFails(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(emptyImages{})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if got.StatusConditions().Get(apiv1.ConditionTypeImagesReady).IsTrue() {
		t.Error("ImagesReady should be false when image resolution fails")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when images fail to resolve")
	}
	// Status must not keep advertising images that no longer resolve: instance-type
	// selection reads ResolvedImages to decide which architectures are launchable, so a
	// stale entry routes it at an architecture that cannot boot.
	if len(got.Status.ResolvedImages) != 0 {
		t.Errorf("expected ResolvedImages cleared when no architecture resolves, got %+v", got.Status.ResolvedImages)
	}
}

// TestReconcile_ClearsStaleResolvedImages verifies that images resolved on an earlier
// pass are removed once resolution stops succeeding, rather than lingering in status.
func TestReconcile_ClearsStaleResolvedImages(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Status.ResolvedImages = []apiv1.ResolvedImage{
		{Architecture: "x86", ImageID: 42},
		{Architecture: "arm", ImageID: 43},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(emptyImages{})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.ResolvedImages) != 0 {
		t.Errorf("stale resolved images survived a failed resolution: %+v", got.Status.ResolvedImages)
	}
}

func TestReconcile_NetworkNotFound(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: nil}, fakeFirewalls{}, fakeSSHKeys{}, img) // network missing

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if got.StatusConditions().Get(apiv1.ConditionTypeNetworkReady).IsTrue() {
		t.Error("NetworkReady should be false when network is missing")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when network is missing")
	}
}

func TestReconcile_FirewallNotFound(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Spec.FirewallIDs = []int64{7}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()
	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{fw: nil}, fakeSSHKeys{}, img) // firewall missing

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if got.StatusConditions().Get(apiv1.ConditionTypeResourcesReady).IsTrue() {
		t.Error("ResourcesReady should be false when a firewall is missing")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when a referenced firewall is missing")
	}
}

func TestReconcile_UserDataSecretValid(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Spec.UserDataSecretRef = &apiv1.UserDataSecretReference{
		Namespace: "kube-system",
		Name:      "talos",
		Key:       "userData",
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos", Namespace: "kube-system"},
		Data:       map[string][]byte{"userData": []byte("machine: {}\n")},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc, secret).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(apiv1.ConditionTypeUserDataReady).IsTrue() {
		t.Error("UserDataReady should be true for a valid secret ref")
	}
	if !got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should be true when all conditions including UserDataReady are satisfied")
	}
}

// TestReconcile_UserDataKeyMissing verifies that Reconcile sets UserDataReady=False
// (reason UserDataKeyMissing) when the Secret exists but does not contain the
// referenced key. This is the "secret present, key absent" branch that differs from
// the secret-missing case.
func TestReconcile_UserDataKeyMissing(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Spec.UserDataSecretRef = &apiv1.UserDataSecretReference{
		Namespace: "kube-system",
		Name:      "talos",
		Key:       "userData",
	}
	// Secret exists but has a different key — "userData" is absent.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos", Namespace: "kube-system"},
		Data:       map[string][]byte{"wrong": []byte("x")},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc, secret).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(apiv1.ConditionTypeUserDataReady).IsFalse() {
		t.Error("UserDataReady should be false when secret exists but referenced key is absent")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when UserDataReady is false")
	}
}

func TestReconcile_UserDataSecretMissing(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Spec.UserDataSecretRef = &apiv1.UserDataSecretReference{
		Namespace: "kube-system",
		Name:      "does-not-exist",
		Key:       "userData",
	}
	// Secret is NOT added to the fake client.
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(apiv1.ConditionTypeUserDataReady).IsFalse() {
		t.Error("UserDataReady should be false when the secret is missing")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when UserDataReady is false")
	}
}

// unreadableImages fails every list call, standing in for a 429/5xx from hcloud.
type unreadableImages struct{}

func (unreadableImages) AllWithOpts(_ context.Context, _ hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return nil, errors.New("rate limited (429)")
}

// x86OKArmErrors resolves x86 and fails the arm lookup with an API error, the shape of
// a partial outage: one architecture is genuinely known, the other is merely unreadable.
type x86OKArmErrors struct{ img *hcloud.Image }

func (f x86OKArmErrors) AllWithOpts(_ context.Context, opts hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	for _, a := range opts.Architecture {
		if a == hcloud.ArchitectureX86 {
			return []*hcloud.Image{f.img}, nil
		}
	}
	return nil, errors.New("server error (503)")
}

// TestReconcile_KeepsResolvedImagesOnTransientError verifies that an unreadable image
// catalogue does not clear status or declare the images gone. Instance-type selection
// reads ResolvedImages to decide which architectures are launchable, so clearing on an
// API blip makes Create reject every candidate and Karpenter delete NodeClaims over a
// transient error. Every other condition in Reconcile already sets Unknown and keeps
// state on API errors; images now match.
func TestReconcile_KeepsResolvedImagesOnTransientError(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Status.ResolvedImages = []apiv1.ResolvedImage{{Architecture: "x86", ImageID: 42}}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(unreadableImages{})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)
	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.ResolvedImages) != 1 || got.Status.ResolvedImages[0].ImageID != 42 {
		t.Errorf("last-known-good images discarded on a transient error: %+v", got.Status.ResolvedImages)
	}
	if cond := got.StatusConditions().Get(apiv1.ConditionTypeImagesReady); cond.IsFalse() {
		t.Errorf("transient error reported as definitive (False); want Unknown: %+v", cond)
	}
}

// TestReconcile_CarriesForwardArchOnTransientError verifies that when one architecture
// resolves and the other's lookup fails with an API error, the failing architecture's
// previous entry is carried forward rather than dropped. Dropping it reads as "this
// architecture has no image", which makes selection refuse it and Karpenter delete the
// NodeClaims of an arch-pinned NodePool over an unrelated blip.
func TestReconcile_CarriesForwardArchOnTransientError(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Status.ResolvedImages = []apiv1.ResolvedImage{
		{Architecture: "x86", ImageID: 42},
		{Architecture: "arm", ImageID: 43},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(x86OKArmErrors{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)
	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &apiv1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	var haveArm bool
	for _, ri := range got.Status.ResolvedImages {
		if ri.Architecture == "arm" && ri.ImageID == 43 {
			haveArm = true
		}
	}
	if !haveArm {
		t.Errorf("arm entry dropped on an API error instead of carried forward: %+v", got.Status.ResolvedImages)
	}
}

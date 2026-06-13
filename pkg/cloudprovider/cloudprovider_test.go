package cloudprovider_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/cloudprovider"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instancetype"
)

func newTestCloudProvider() *cloudprovider.CloudProvider {
	return cloudprovider.NewCloudProvider(nil, nil, nil, nil)
}

func TestName(t *testing.T) {
	cp := newTestCloudProvider()
	if got := cp.Name(); got != "hetzner" {
		t.Errorf("Name() = %q, want %q", got, "hetzner")
	}
}

func TestGetSupportedNodeClasses(t *testing.T) {
	cp := newTestCloudProvider()
	classes := cp.GetSupportedNodeClasses()
	if len(classes) != 1 {
		t.Fatalf("GetSupportedNodeClasses() returned %d classes, want 1", len(classes))
	}
	if _, ok := classes[0].(*v1alpha1.HCloudNodeClass); !ok {
		t.Errorf("GetSupportedNodeClasses()[0] is not *v1alpha1.HCloudNodeClass")
	}
}

func TestRepairPolicies(t *testing.T) {
	cp := newTestCloudProvider()
	policies := cp.RepairPolicies()
	if len(policies) != 2 {
		t.Fatalf("RepairPolicies() returned %d policies, want 2", len(policies))
	}
}

// ---------------------------------------------------------------------------
// Fake clients
// ---------------------------------------------------------------------------

type fakeServerClient struct {
	servers   map[int64]*hcloud.Server
	createErr error
	nextID    int64
}

func (f *fakeServerClient) Create(_ context.Context, opts hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, *hcloud.Response, error) {
	if f.createErr != nil {
		return hcloud.ServerCreateResult{}, nil, f.createErr
	}
	if f.servers == nil {
		f.servers = map[int64]*hcloud.Server{}
	}
	if f.nextID == 0 {
		f.nextID = 100
	}
	id := f.nextID
	f.nextID++
	s := &hcloud.Server{ID: id, Name: opts.Name, Labels: opts.Labels, ServerType: opts.ServerType, Location: opts.Location}
	f.servers[id] = s
	return hcloud.ServerCreateResult{Server: s}, nil, nil
}

func (f *fakeServerClient) DeleteWithResult(_ context.Context, server *hcloud.Server) (*hcloud.ServerDeleteResult, *hcloud.Response, error) {
	delete(f.servers, server.ID)
	return &hcloud.ServerDeleteResult{}, nil, nil
}

func (f *fakeServerClient) GetByID(_ context.Context, id int64) (*hcloud.Server, *hcloud.Response, error) {
	return f.servers[id], nil, nil
}

func (f *fakeServerClient) AllWithOpts(_ context.Context, _ hcloud.ServerListOpts) ([]*hcloud.Server, error) {
	out := make([]*hcloud.Server, 0, len(f.servers))
	for _, s := range f.servers {
		out = append(out, s)
	}
	return out, nil
}

type fakeServerTypeClient struct{ types []*hcloud.ServerType }

func (f *fakeServerTypeClient) All(_ context.Context) ([]*hcloud.ServerType, error) {
	return f.types, nil
}

type fakeImageClient struct{ images []*hcloud.Image }

func (f *fakeImageClient) AllWithOpts(_ context.Context, _ hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return f.images, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// buildCP builds a CloudProvider whose NodeClass is nc and whose single backing
// server is server, plus a NodeClaim whose desired state matches the server
// (BASELINE has no drift; each test perturbs one thing).
func buildCP(t *testing.T, nc *v1alpha1.HCloudNodeClass, server *hcloud.Server) (*cloudprovider.CloudProvider, *karpv1.NodeClaim) {
	t.Helper()
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	if nc.Name == "" {
		nc.Name = "default"
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).Build()

	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{server.ID: server}}
	stc := &fakeServerTypeClient{}
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04"}}}

	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(stc),
		imagefamily.NewProvider(imgc))

	nodeClaim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim"}}
	nodeClaim.Labels = map[string]string{}
	if server.ServerType != nil {
		nodeClaim.Labels[corev1.LabelInstanceTypeStable] = server.ServerType.Name
	}
	nodeClaim.Spec.NodeClassRef = &karpv1.NodeClassReference{Name: nc.Name, Group: v1alpha1.Group, Kind: "HCloudNodeClass"}
	nodeClaim.Status.ProviderID = instance.FormatProviderID(server.ID)
	if server.Image != nil {
		nodeClaim.Status.ImageID = strconv.FormatInt(server.Image.ID, 10)
	}
	return cp, nodeClaim
}

// baselineServer returns a server with no drift relative to a baseline NodeClass:
// type cx22, image 42, attached to network 1, no firewalls.
func baselineServer() *hcloud.Server {
	return &hcloud.Server{
		ID:         50,
		ServerType: &hcloud.ServerType{Name: "cx22"},
		Image:      &hcloud.Image{ID: 42},
		PrivateNet: []hcloud.ServerPrivateNet{{Network: &hcloud.Network{ID: 1}}},
	}
}

// baselineNodeClass returns a NodeClass matching baselineServer (network 1, no firewalls).
func baselineNodeClass() *v1alpha1.HCloudNodeClass {
	return &v1alpha1.HCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.HCloudNodeClassSpec{
			Locations:     []string{"nbg1"},
			NetworkID:     1,
			ImageSelector: v1alpha1.ImageSelector{Family: "ubuntu"},
		},
	}
}

// ---------------------------------------------------------------------------
// Drift tests
// ---------------------------------------------------------------------------

func TestIsDrifted_NoDrift(t *testing.T) {
	cp, nodeClaim := buildCP(t, baselineNodeClass(), baselineServer())
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("expected no drift, got %q", reason)
	}
}

func TestIsDrifted_Firewall(t *testing.T) {
	nc := baselineNodeClass()
	nc.Spec.FirewallIDs = []int64{7}
	server := baselineServer()
	server.PublicNet.Firewalls = []*hcloud.ServerFirewallStatus{{Firewall: hcloud.Firewall{ID: 9}}}
	cp, nodeClaim := buildCP(t, nc, server)
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != cloudprovider.DriftFirewall {
		t.Errorf("want FirewallDrift, got %q", reason)
	}
}

func TestIsDrifted_FirewallAttached_NoDrift(t *testing.T) {
	nc := baselineNodeClass()
	nc.Spec.FirewallIDs = []int64{7}
	server := baselineServer()
	server.PublicNet.Firewalls = []*hcloud.ServerFirewallStatus{{Firewall: hcloud.Firewall{ID: 7}}}
	cp, nodeClaim := buildCP(t, nc, server)
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("expected no drift when firewall attached, got %q", reason)
	}
}

func TestIsDrifted_ServerType(t *testing.T) {
	server := baselineServer()
	cp, nodeClaim := buildCP(t, baselineNodeClass(), server)
	// Desired (claim label) is cx22; mutate the live server type to differ.
	server.ServerType = &hcloud.ServerType{Name: "cx32"}
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != cloudprovider.DriftServerType {
		t.Errorf("want ServerTypeDrift, got %q", reason)
	}
}

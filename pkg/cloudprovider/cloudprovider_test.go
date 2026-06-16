package cloudprovider_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"

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
	lastOpts  hcloud.ServerCreateOpts
}

func (f *fakeServerClient) lastUserData() string {
	return f.lastOpts.UserData
}

func (f *fakeServerClient) Create(_ context.Context, opts hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, *hcloud.Response, error) {
	f.lastOpts = opts
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
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04", Architecture: hcloud.ArchitectureX86}}}

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

// cx22Type returns a representative shared-vCPU x86 server type with a single
// nbg1 offering, used by the Create/List/Get/GetInstanceTypes tests.
func cx22Type() *hcloud.ServerType {
	return &hcloud.ServerType{
		Name:         "cx22",
		Cores:        2,
		Memory:       4,
		Disk:         40,
		Architecture: hcloud.ArchitectureX86,
		CPUType:      hcloud.CPUTypeShared,
		Pricings: []hcloud.ServerTypeLocationPricing{
			{
				Location: &hcloud.Location{Name: "nbg1"},
				Hourly:   hcloud.Price{Net: "0.0070"},
				Monthly:  hcloud.Price{Net: "4.5100"},
			},
		},
	}
}

// buildCPWithTypes wires a CloudProvider with the given NodeClass and server types,
// and returns the fakes so tests can inject errors / inspect state. Unlike buildCP
// it does NOT pre-seed a backing server.
func buildCPWithTypes(t *testing.T, nc *v1alpha1.HCloudNodeClass, types []*hcloud.ServerType) (
	*cloudprovider.CloudProvider, *fakeServerClient, *instancetype.Provider) {
	t.Helper()
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	if nc.Name == "" {
		nc.Name = "default"
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{}}
	stc := &fakeServerTypeClient{types: types}
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04", Architecture: hcloud.ArchitectureX86}}}
	typeProvider := instancetype.NewProvider(stc)
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		typeProvider,
		imagefamily.NewProvider(imgc))
	return cp, fsc, typeProvider
}

// createNodeClaim returns a NodeClaim with empty requirements (compatible with any type).
func createNodeClaim() *karpv1.NodeClaim {
	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim"}}
	nc.Spec.NodeClassRef = &karpv1.NodeClassReference{Name: "default", Group: v1alpha1.Group, Kind: "HCloudNodeClass"}
	return nc
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
	server.ServerType = &hcloud.ServerType{Name: "cx32"} // live server is cx32
	cp, nodeClaim := buildCP(t, baselineNodeClass(), server)
	nodeClaim.Labels[corev1.LabelInstanceTypeStable] = "cx22" // desired was cx22
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != cloudprovider.DriftServerType {
		t.Errorf("want ServerTypeDrift, got %q", reason)
	}
}

// ---------------------------------------------------------------------------
// Create / Delete / Get / List / GetInstanceTypes tests
// ---------------------------------------------------------------------------

func TestCreate_Success(t *testing.T) {
	cp, _, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	out, err := cp.Create(context.Background(), createNodeClaim())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Status.ProviderID == "" || !strings.HasPrefix(out.Status.ProviderID, "hcloud://") {
		t.Errorf("expected hcloud provider ID, got %q", out.Status.ProviderID)
	}
	if out.Status.Capacity.Cpu().IsZero() {
		t.Error("expected non-zero CPU capacity")
	}
}

func TestCreate_InsufficientCapacityMarksUnavailable(t *testing.T) {
	cp, fsc, typeProvider := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	fsc.createErr = hcloud.Error{Code: hcloud.ErrorCodeResourceUnavailable}

	_, err := cp.Create(context.Background(), createNodeClaim())
	if err == nil {
		t.Fatal("expected error on capacity failure")
	}
	// The offering for (cx22, nbg1) should now be marked unavailable.
	its, lerr := typeProvider.List(context.Background(), []string{"nbg1"})
	if lerr != nil {
		t.Fatal(lerr)
	}
	for _, it := range its {
		if it.Name != "cx22" {
			continue
		}
		for _, o := range it.Offerings {
			if o.Available {
				t.Error("expected cx22/nbg1 offering to be unavailable after capacity error")
			}
		}
	}
}

func TestGet_NotFound(t *testing.T) {
	cp, _, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	_, err := cp.Get(context.Background(), instance.FormatProviderID(999))
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Errorf("expected NodeClaimNotFoundError, got %v", err)
	}
}

func TestGet_Found(t *testing.T) {
	cp, fsc, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	fsc.servers[55] = &hcloud.Server{ID: 55, ServerType: &hcloud.ServerType{Name: "cx22"}}
	nc, err := cp.Get(context.Background(), instance.FormatProviderID(55))
	if err != nil {
		t.Fatal(err)
	}
	if nc.Status.ProviderID != instance.FormatProviderID(55) {
		t.Errorf("got provider ID %q", nc.Status.ProviderID)
	}
}

func TestList_ReturnsManagedServers(t *testing.T) {
	cp, fsc, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	fsc.servers[1] = &hcloud.Server{ID: 1, ServerType: &hcloud.ServerType{Name: "cx22"}}
	fsc.servers[2] = &hcloud.Server{ID: 2, ServerType: &hcloud.ServerType{Name: "cx22"}}
	list, err := cp.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 nodeclaims, got %d", len(list))
	}
}

func TestDelete_RemovesServer(t *testing.T) {
	cp, fsc, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	fsc.servers[77] = &hcloud.Server{ID: 77}
	nodeClaim := &karpv1.NodeClaim{}
	nodeClaim.Status.ProviderID = instance.FormatProviderID(77)
	if err := cp.Delete(context.Background(), nodeClaim); err != nil {
		t.Fatal(err)
	}
	if _, ok := fsc.servers[77]; ok {
		t.Error("expected server 77 to be deleted")
	}
}

func TestGetInstanceTypes_NilNodePool(t *testing.T) {
	cp, _, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	its, err := cp.GetInstanceTypes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(its) == 0 {
		t.Error("expected at least one instance type")
	}
}

// ---------------------------------------------------------------------------
// Edge-case tests
// ---------------------------------------------------------------------------

// TestDelete_Idempotent verifies that deleting a NodeClaim whose backing server
// no longer exists (or never did) is a no-op and returns nil.
func TestDelete_Idempotent(t *testing.T) {
	cp, _, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	nodeClaim := &karpv1.NodeClaim{}
	nodeClaim.Status.ProviderID = instance.FormatProviderID(12345) // not seeded
	if err := cp.Delete(context.Background(), nodeClaim); err != nil {
		t.Errorf("Delete of missing server should be nil, got %v", err)
	}
}

// TestGet_NilServerType verifies Get does not panic when the live server has a
// nil ServerType (e.g. mid-provisioning); capacity may be empty in that case.
func TestGet_NilServerType(t *testing.T) {
	cp, fsc, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	fsc.servers[88] = &hcloud.Server{ID: 88} // ServerType nil (e.g. mid-provisioning)
	nc, err := cp.Get(context.Background(), instance.FormatProviderID(88))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if nc.Status.ProviderID != instance.FormatProviderID(88) {
		t.Errorf("expected provider ID set, got %q", nc.Status.ProviderID)
	}
	// Capacity may be empty when ServerType is nil; the call must not panic.
}

// TestCreate_NoCompatibleType verifies Create returns an InsufficientCapacityError
// when no instance type satisfies the NodeClaim requirements (selected == nil).
func TestCreate_NoCompatibleType(t *testing.T) {
	cp, _, _ := buildCPWithTypes(t, baselineNodeClass(), []*hcloud.ServerType{cx22Type()})
	nodeClaim := createNodeClaim()
	nodeClaim.Spec.Requirements = []karpv1.NodeSelectorRequirementWithMinValues{
		{
			Key:      corev1.LabelInstanceTypeStable,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{"does-not-exist"},
		},
	}
	_, err := cp.Create(context.Background(), nodeClaim)
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Errorf("expected InsufficientCapacityError when no type matches, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// UserData secret resolution tests
// ---------------------------------------------------------------------------

// TestCreate_UserDataFromSecret verifies that when UserDataSecretRef is set, the
// userData passed to the server-create call comes from the referenced Secret, NOT
// from the inline UserData field.
func TestCreate_UserDataFromSecret(t *testing.T) {
	nc := baselineNodeClass()
	nc.Spec.UserData = "inline-should-be-ignored"
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{
		Namespace: "kube-system",
		Name:      "talos",
		Key:       "userData",
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos", Namespace: "kube-system"},
		Data:       map[string][]byte{"userData": []byte("machine:\n  type: worker\n")},
	}

	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc, secret).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{}}
	stc := &fakeServerTypeClient{types: []*hcloud.ServerType{cx22Type()}}
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04", Architecture: hcloud.ArchitectureX86}}}
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(stc),
		imagefamily.NewProvider(imgc))

	if _, err := cp.Create(context.Background(), createNodeClaim()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := fsc.lastUserData(); got != "machine:\n  type: worker\n" {
		t.Errorf("expected userData from secret, got %q", got)
	}
}

// TestCreate_UserDataInlineWhenNoRef verifies that when UserDataSecretRef is nil,
// the inline UserData field is passed through to the server-create call unchanged.
func TestCreate_UserDataInlineWhenNoRef(t *testing.T) {
	nc := baselineNodeClass()
	nc.Spec.UserData = "cloud-init-inline"
	// UserDataSecretRef is intentionally left nil.

	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{}}
	stc := &fakeServerTypeClient{types: []*hcloud.ServerType{cx22Type()}}
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04", Architecture: hcloud.ArchitectureX86}}}
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(stc),
		imagefamily.NewProvider(imgc))

	if _, err := cp.Create(context.Background(), createNodeClaim()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := fsc.lastUserData(); got != "cloud-init-inline" {
		t.Errorf("expected inline userData %q, got %q", "cloud-init-inline", got)
	}
}

// TestCreate_UserDataSecretKeyMissing verifies that Create returns an error when
// UserDataSecretRef points to a Secret that exists but does not contain the
// referenced key (key absent == invalid, same as secret missing).
func TestCreate_UserDataSecretKeyMissing(t *testing.T) {
	nc := baselineNodeClass()
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{
		Namespace: "kube-system",
		Name:      "talos",
		Key:       "userData",
	}
	// Secret exists but contains a different key — "userData" is absent.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos", Namespace: "kube-system"},
		Data:       map[string][]byte{"other": []byte("x")},
	}

	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc, secret).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{}}
	stc := &fakeServerTypeClient{types: []*hcloud.ServerType{cx22Type()}}
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04", Architecture: hcloud.ArchitectureX86}}}
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(stc),
		imagefamily.NewProvider(imgc))

	_, err := cp.Create(context.Background(), createNodeClaim())
	if err == nil {
		t.Fatal("expected error when secret exists but referenced key is absent, got nil")
	}
}

// TestCreate_UserDataSecretMissing verifies that Create returns an error when
// UserDataSecretRef points to a Secret that does not exist in the cluster.
func TestCreate_UserDataSecretMissing(t *testing.T) {
	nc := baselineNodeClass()
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{
		Namespace: "kube-system",
		Name:      "does-not-exist",
		Key:       "userData",
	}

	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	// Secret is intentionally NOT added to the fake client.
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{}}
	stc := &fakeServerTypeClient{types: []*hcloud.ServerType{cx22Type()}}
	imgc := &fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04", Architecture: hcloud.ArchitectureX86}}}
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(stc),
		imagefamily.NewProvider(imgc))

	_, err := cp.Create(context.Background(), createNodeClaim())
	if err == nil {
		t.Fatal("expected error when userDataSecretRef points to a missing Secret, got nil")
	}
}

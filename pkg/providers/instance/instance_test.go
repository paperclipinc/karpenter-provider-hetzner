package instance

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
)

// mockActionWaiter is a fake ActionWaiter for testing.
type mockActionWaiter struct {
	waited int
	err    error
}

func (m *mockActionWaiter) WaitFor(_ context.Context, actions ...*hcloud.Action) error {
	m.waited += len(actions)
	return m.err
}

// mockServerClient is a fake ServerClient for testing.
type mockServerClient struct {
	servers          map[int64]*hcloud.Server
	nextID           int64
	deleted          []int64
	lastListSelector string
	lastListName     string
	action           *hcloud.Action
	nextActions      []*hcloud.Action
	createErr        error
	deleteErr        error
	listErr          error
	lastOpts         hcloud.ServerCreateOpts
}

func newMockServerClient() *mockServerClient {
	return &mockServerClient{
		servers: make(map[int64]*hcloud.Server),
		nextID:  100,
	}
}

func (m *mockServerClient) Create(_ context.Context, opts hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, *hcloud.Response, error) {
	m.lastOpts = opts
	if m.createErr != nil {
		return hcloud.ServerCreateResult{}, nil, m.createErr
	}
	id := m.nextID
	m.nextID++
	server := &hcloud.Server{ID: id, Name: opts.Name, Labels: opts.Labels, ServerType: opts.ServerType, Location: opts.Location}
	m.servers[id] = server
	return hcloud.ServerCreateResult{Server: server, Action: m.action, NextActions: m.nextActions}, nil, nil
}

func (m *mockServerClient) DeleteWithResult(_ context.Context, server *hcloud.Server) (*hcloud.ServerDeleteResult, *hcloud.Response, error) {
	if m.deleteErr != nil {
		return nil, nil, m.deleteErr
	}
	delete(m.servers, server.ID)
	m.deleted = append(m.deleted, server.ID)
	return &hcloud.ServerDeleteResult{}, nil, nil
}

func (m *mockServerClient) GetByID(ctx context.Context, id int64) (*hcloud.Server, *hcloud.Response, error) {
	// A real client fails immediately on a done context; honouring that here is
	// what lets a test tell a live cleanup path from a dead one.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	server, ok := m.servers[id]
	if !ok {
		return nil, nil, nil
	}
	return server, nil, nil
}

func (m *mockServerClient) AllWithOpts(_ context.Context, opts hcloud.ServerListOpts) ([]*hcloud.Server, error) {
	m.lastListSelector = opts.LabelSelector
	m.lastListName = opts.Name
	if m.listErr != nil {
		return nil, m.listErr
	}
	result := make([]*hcloud.Server, 0, len(m.servers))
	for _, s := range m.servers {
		if opts.Name != "" && s.Name != opts.Name {
			continue
		}
		result = append(result, s)
	}
	return result, nil
}

func TestCreate_LabelsApplied(t *testing.T) {
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")

	server, err := p.Create(context.Background(), CreateOpts{
		Name:       "test-node",
		ServerType: "cx11",
		Location:   "nbg1",
		Image:      &hcloud.Image{ID: 1},
		NodeClaim:  "my-claim",
		NodePool:   "my-pool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server == nil {
		t.Fatal("expected server, got nil")
	}

	// Verify management labels.
	if server.Labels[apiv1.ServerLabelManagedBy] != apiv1.ServerValueManagedBy {
		t.Errorf("missing managed-by label, got %q", server.Labels[apiv1.ServerLabelManagedBy])
	}
	if server.Labels[apiv1.ServerLabelNodeClaim] != "my-claim" {
		t.Errorf("expected nodeclaim label 'my-claim', got %q", server.Labels[apiv1.ServerLabelNodeClaim])
	}
	if server.Labels[apiv1.ServerLabelNodePool] != "my-pool" {
		t.Errorf("expected nodepool label 'my-pool', got %q", server.Labels[apiv1.ServerLabelNodePool])
	}
	if server.Labels[apiv1.ServerLabelCluster] != "test-cluster" {
		t.Errorf("missing cluster label, got %q", server.Labels[apiv1.ServerLabelCluster])
	}
}

func TestCreate_CustomLabelsPreserved(t *testing.T) {
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")

	server, err := p.Create(context.Background(), CreateOpts{
		Name:       "test-node",
		ServerType: "cx11",
		Location:   "nbg1",
		Image:      &hcloud.Image{ID: 1},
		Labels:     map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server.Labels["env"] != "prod" {
		t.Errorf("expected custom label env=prod, got %q", server.Labels["env"])
	}
}

func TestDelete_RemovesServer(t *testing.T) {
	client := newMockServerClient()
	client.servers[42] = &hcloud.Server{ID: 42, Name: "node-42"}
	p := NewProvider(client, "test-cluster")

	err := p.Delete(context.Background(), "hcloud://42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := client.servers[42]; ok {
		t.Error("expected server to be deleted")
	}
}

func TestDelete_NotFoundReturnsNodeClaimNotFound(t *testing.T) {
	// Deleting a server that no longer exists must return a NodeClaimNotFoundError
	// (not nil). Karpenter's termination controller calls Delete each reconcile and
	// only removes the NodeClaim finalizer once Delete reports the instance is gone;
	// returning nil makes it requeue forever and leaks the NodeClaim.
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")

	err := p.Delete(context.Background(), "hcloud://999")
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError for non-existent server, got: %v", err)
	}
}

func TestDelete_RaceDeletedReturnsNodeClaimNotFound(t *testing.T) {
	// The server exists at GetByID but is already gone by DeleteWithResult (e.g. a
	// concurrent delete in the Hetzner console). Delete must still surface a
	// NodeClaimNotFoundError so the NodeClaim can be finalized.
	client := newMockServerClient()
	client.servers[55] = &hcloud.Server{ID: 55, Name: "node-55"}
	client.deleteErr = hcloud.Error{Code: hcloud.ErrorCodeNotFound, Message: "not found"}
	p := NewProvider(client, "test-cluster")

	err := p.Delete(context.Background(), "hcloud://55")
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError on delete race, got: %v", err)
	}
}

func TestGet_Found(t *testing.T) {
	client := newMockServerClient()
	client.servers[77] = &hcloud.Server{ID: 77, Name: "my-node"}
	p := NewProvider(client, "test-cluster")

	server, err := p.Get(context.Background(), "hcloud://77")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server == nil {
		t.Fatal("expected server, got nil")
	}
	if server.ID != 77 {
		t.Errorf("expected ID=77, got %d", server.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")

	server, err := p.Get(context.Background(), "hcloud://999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server != nil {
		t.Errorf("expected nil server for missing ID, got %+v", server)
	}
}

func TestList(t *testing.T) {
	client := newMockServerClient()
	client.servers[1] = &hcloud.Server{ID: 1, Name: "node-1"}
	client.servers[2] = &hcloud.Server{ID: 2, Name: "node-2"}
	p := NewProvider(client, "test-cluster")

	servers, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(servers))
	}
}

func TestParseProviderID_Valid(t *testing.T) {
	cases := []struct {
		input    string
		expected int64
	}{
		{"hcloud://123", 123},
		{"hcloud://999999", 999999},
		{"hcloud://1", 1},
	}
	for _, tc := range cases {
		id, err := ParseProviderID(tc.input)
		if err != nil {
			t.Errorf("ParseProviderID(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if id != tc.expected {
			t.Errorf("ParseProviderID(%q) = %d, want %d", tc.input, id, tc.expected)
		}
	}
}

func TestParseProviderID_Invalid(t *testing.T) {
	cases := []string{
		"123",
		"aws://123",
		"hcloud://abc",
		"hcloud://",
		"",
	}
	for _, tc := range cases {
		_, err := ParseProviderID(tc)
		if err == nil {
			t.Errorf("ParseProviderID(%q) expected error, got nil", tc)
		}
	}
}

func TestFormatProviderID(t *testing.T) {
	cases := []struct {
		id       int64
		expected string
	}{
		{123, "hcloud://123"},
		{1, "hcloud://1"},
		{999999, "hcloud://999999"},
	}
	for _, tc := range cases {
		got := FormatProviderID(tc.id)
		if got != tc.expected {
			t.Errorf("FormatProviderID(%d) = %q, want %q", tc.id, got, tc.expected)
		}
	}
}

func TestCreate_ClusterLabelApplied(t *testing.T) {
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")
	server, err := p.Create(context.Background(), CreateOpts{
		Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server.Labels[apiv1.ServerLabelCluster] != "test-cluster" {
		t.Errorf("expected cluster label, got %q", server.Labels[apiv1.ServerLabelCluster])
	}
}

func TestList_ScopesByCluster(t *testing.T) {
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")
	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(client.lastListSelector, apiv1.ServerLabelCluster+"=test-cluster") {
		t.Errorf("List selector %q does not scope by cluster", client.lastListSelector)
	}
	if !strings.Contains(client.lastListSelector, apiv1.ServerLabelManagedBy+"="+apiv1.ServerValueManagedBy) {
		t.Errorf("List selector %q missing managed-by", client.lastListSelector)
	}
}

func TestCreate_WaitsForActionsAndSetsPublicNet(t *testing.T) {
	client := newMockServerClient()
	client.action = &hcloud.Action{ID: 1}
	waiter := &mockActionWaiter{}
	p := NewProviderWithWaiter(client, "test-cluster", waiter)

	_, err := p.Create(context.Background(), CreateOpts{
		Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1},
		EnablePublicIPv4: false, EnablePublicIPv6: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if waiter.waited == 0 {
		t.Error("expected the action waiter to be called")
	}
	if client.lastOpts.PublicNet == nil || client.lastOpts.PublicNet.EnableIPv4 {
		t.Error("expected public IPv4 to be disabled in create opts")
	}
	if !client.lastOpts.PublicNet.EnableIPv6 {
		t.Error("expected public IPv6 to default to enabled")
	}
}

func TestCreate_MapsCapacityError(t *testing.T) {
	client := newMockServerClient()
	client.createErr = hcloud.Error{Code: hcloud.ErrorCodeResourceUnavailable}
	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), CreateOpts{Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1}})
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Errorf("expected InsufficientCapacityError, got %v", err)
	}
}

// orphan builds a server that a previous Create attempt left behind, labelled
// as this provider labels its own servers and shaped like the request in
// adoptOpts below.
func orphan(id int64, name, cluster string) *hcloud.Server {
	return &hcloud.Server{
		ID:         id,
		Name:       name,
		ServerType: &hcloud.ServerType{Name: "cx22"},
		Location:   &hcloud.Location{Name: "nbg1"},
		Labels: map[string]string{
			apiv1.ServerLabelManagedBy: apiv1.ServerValueManagedBy,
			apiv1.ServerLabelCluster:   cluster,
		},
	}
}

// adoptOpts is the request every adoption test replays.
func adoptOpts() CreateOpts {
	return CreateOpts{
		Name: "worker-abc", ServerType: "cx22", Location: "nbg1",
		Image: &hcloud.Image{ID: 1}, NodeClaim: "worker-abc",
	}
}

// ownedOrphan is a server a previous attempt created for THIS NodeClaim, with
// the image the request resolves to.
func ownedOrphan(id int64) *hcloud.Server {
	s := orphan(id, "worker-abc", "test-cluster")
	s.Labels[apiv1.ServerLabelNodeClaim] = "worker-abc"
	s.Image = &hcloud.Image{ID: 1}
	s.Status = hcloud.ServerStatusRunning
	return s
}

// A server left by a different NodeClaim tells us nothing about whether it was
// built from this request's inputs -- userData, SSH keys, networks, firewalls and
// public-IP policy are all invisible on the returned server and none of them are
// drift-checked, so a name match alone is not evidence of a match.
func TestCreate_UniquenessErrorRefusesServerOfAnotherNodeClaim(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.Labels[apiv1.ServerLabelNodeClaim] = "some-other-claim"
	client.servers[42] = s
	client.createErr = uniquenessErr()

	_, err := NewProvider(client, "test-cluster").Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "adopted a server built for a different NodeClaim")
}

// Only a server that is running or still coming up is a plausible launch. An
// "off" machine adopted as a success gives Karpenter a NodeClaim with capacity
// that nothing will ever start.
func TestCreate_UniquenessErrorRefusesNonRunningServer(t *testing.T) {
	for _, status := range []hcloud.ServerStatus{
		hcloud.ServerStatusOff, hcloud.ServerStatusDeleting,
		hcloud.ServerStatusStopping, hcloud.ServerStatusUnknown,
	} {
		t.Run(string(status), func(t *testing.T) {
			client := newMockServerClient()
			s := ownedOrphan(42)
			s.Status = status
			client.servers[42] = s
			client.createErr = uniquenessErr()

			_, err := NewProvider(client, "test-cluster").Create(context.Background(), adoptOpts())
			assertRefusedAdoption(t, err, fmt.Sprintf("adopted a server in %q status", status))
		})
	}
}

// An unresolved location must fail closed. Karpenter derives the NodeClaim's
// zone label from the offering it picked, so adopting a machine in an unknown
// location silently breaks hcloud CSI volume scheduling.
func TestCreate_UniquenessErrorRefusesWhenLocationUnresolved(t *testing.T) {
	client := newMockServerClient()
	client.servers[42] = ownedOrphan(42)
	client.createErr = uniquenessErr()

	opts := adoptOpts()
	opts.Location = ""
	_, err := NewProvider(client, "test-cluster").Create(context.Background(), opts)
	assertRefusedAdoption(t, err, "adopted a server for a request with no resolved location")
}

// Karpenter records the adopted server's image as the NodeClaim's, and image
// drift compares those two -- so an image mismatch adopted here can never be
// detected afterwards.
func TestCreate_UniquenessErrorRefusesImageMismatch(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.Image = &hcloud.Image{ID: 999}
	client.servers[42] = s
	client.createErr = uniquenessErr()

	_, err := NewProvider(client, "test-cluster").Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "adopted a server running a different image")
}

// The create call succeeded, so the server exists; if waiting on its actions
// fails we must not walk away and leave it running. Deleting it here is what
// keeps adoption's input limited to crash-orphans, whose machines are healthy.
func TestCreate_WaiterFailureDeletesTheCreatedServer(t *testing.T) {
	client := newMockServerClient()
	client.action = &hcloud.Action{ID: 1}
	waiter := &mockActionWaiter{err: fmt.Errorf("action failed")}

	p := NewProviderWithWaiter(client, "test-cluster", waiter)
	if _, err := p.Create(context.Background(), adoptOpts()); err == nil {
		t.Fatal("expected the wait failure to surface")
	}
	if len(client.deleted) != 1 {
		t.Errorf("left the created server running after its actions failed: deleted=%v", client.deleted)
	}
}

// hcloud's WaitFor returns ctx.Err() when the caller's context is cancelled -- a
// SIGTERM, a lost leader lease, a reconcile deadline -- which is the likeliest
// way to reach the cleanup at all. Running the cleanup on that same context would
// fail it before it issued a single request, leaking the server it exists to
// reclaim and leaving a half-provisioned machine for a later retry to adopt.
func TestCreate_WaiterFailureDeletesTheServerOnACancelledContext(t *testing.T) {
	client := newMockServerClient()
	client.action = &hcloud.Action{ID: 1}
	waiter := &mockActionWaiter{err: context.Canceled}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := NewProviderWithWaiter(client, "test-cluster", waiter)
	if _, err := p.Create(ctx, adoptOpts()); err == nil {
		t.Fatal("expected the wait failure to surface")
	}
	if len(client.deleted) != 1 {
		t.Errorf("left the created server running after a cancelled create: deleted=%v", client.deleted)
	}
}

// assertRefusedAdoption asserts that Create declined to adopt AND that the
// original uniqueness error reached the caller unchanged. The contract is not
// "some error came back" but "a name collision stays an ordinary, retryable
// collision": mapping it to InsufficientCapacityError would make
// cloudprovider.Create call MarkUnavailable and blacklist a perfectly healthy
// server type/location offering on every collision, pushing the NodePool onto
// more expensive types.
func assertRefusedAdoption(t *testing.T, err error, why string) {
	t.Helper()
	if err == nil {
		t.Fatal(why)
	}
	if !hcloud.IsError(err, hcloud.ErrorCodeUniquenessError) {
		t.Errorf("expected the original uniqueness error to reach the caller, got %v", err)
	}
	if karpcp.IsInsufficientCapacityError(err) {
		t.Error("a name collision was mapped to InsufficientCapacityError, blacklisting a healthy offering")
	}
}

func uniquenessErr() error {
	return hcloud.Error{Code: hcloud.ErrorCodeUniquenessError, Message: "server name is already used"}
}

const testClusterUID = "34f25cbf-c7b5-49d1-833b-103bff8a34ad"

// providerWithUID is the production shape: a cluster name plus the UID that
// makes ownership unambiguous when two clusters share a name. It goes through
// the production constructor rather than assigning the field, so anything that
// construction derives from the UID applies here too.
func providerWithUID(client ServerClient) *Provider {
	return NewProviderWithPlacementGroups(client, nil, "test-cluster", testClusterUID, nil)
}

// Every server this installation creates must carry the cluster UID, or a
// same-named cluster sharing the Hetzner project cannot tell them apart.
func TestCreate_StampsClusterUID(t *testing.T) {
	client := newMockServerClient()

	server, err := providerWithUID(client).Create(context.Background(), adoptOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := server.Labels[apiv1.ServerLabelClusterUID]; got != testClusterUID {
		t.Errorf("cluster UID label = %q, want %q", got, testClusterUID)
	}
}

// Adoption hands a live machine to Karpenter, which will eventually terminate
// it. Taking one belonging to a same-named cluster would destroy their node.
func TestCreate_UniquenessErrorRefusesForeignClusterUID(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.Labels[apiv1.ServerLabelClusterUID] = "6e5f8dfb-e54b-41ee-8fb3-89a48a42231f"
	client.servers[42] = s
	client.createErr = uniquenessErr()

	_, err := providerWithUID(client).Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "adopted a server belonging to a same-named other cluster")
}

func TestCreate_AdoptsServerWithMatchingClusterUID(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.Labels[apiv1.ServerLabelClusterUID] = testClusterUID
	client.servers[42] = s
	client.createErr = uniquenessErr()

	server, err := providerWithUID(client).Create(context.Background(), adoptOpts())
	if err != nil {
		t.Fatalf("refused to adopt our own server: %v", err)
	}
	if server == nil || server.ID != 42 {
		t.Fatalf("expected server 42, got %+v", server)
	}
}

// Servers created before this label existed carry no UID; refusing them would
// make every pre-existing orphan unrecoverable.
func TestCreate_AdoptsLegacyServerWithoutClusterUID(t *testing.T) {
	client := newMockServerClient()
	client.servers[42] = ownedOrphan(42) // no UID label
	client.createErr = uniquenessErr()

	server, err := providerWithUID(client).Create(context.Background(), adoptOpts())
	if err != nil {
		t.Fatalf("refused to adopt a legacy server: %v", err)
	}
	if server == nil || server.ID != 42 {
		t.Fatalf("expected server 42, got %+v", server)
	}
}

// A crash between the Hetzner create call and persisting the provider ID leaves
// a running server that Karpenter has no record of. Every retry then collides on
// the name. Adopting the existing server is the only way to recover it, since
// the server's own name and labels are the sole surviving record.
func TestCreate_AdoptsOrphanedServerOnUniquenessError(t *testing.T) {
	client := newMockServerClient()
	client.servers[42] = ownedOrphan(42)
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	server, err := p.Create(context.Background(), adoptOpts())
	if err != nil {
		t.Fatalf("expected adoption to succeed, got error: %v", err)
	}
	if server == nil || server.ID != 42 {
		t.Fatalf("expected adopted server 42, got %+v", server)
	}
	if client.lastListName != "worker-abc" {
		t.Errorf("expected lookup by name %q, got %q", "worker-abc", client.lastListName)
	}
}

func TestCreate_UniquenessErrorRefusesForeignCluster(t *testing.T) {
	client := newMockServerClient()
	foreign := ownedOrphan(42)
	foreign.Labels[apiv1.ServerLabelCluster] = "someone-elses-cluster"
	client.servers[42] = foreign
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected error, adopted a server belonging to another cluster")
}

func TestCreate_UniquenessErrorRefusesUnmanagedServer(t *testing.T) {
	client := newMockServerClient()
	client.servers[42] = &hcloud.Server{ID: 42, Name: "worker-abc"} // no karpenter labels
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected error, adopted a server this provider does not manage")
}

// The caller builds the NodeClaim's capacity and zone labels from the offering
// it selected on THIS attempt, not from the server Create hands back. Adopting a
// server of a different shape would therefore advertise capacity the machine
// does not have, and a zone it is not in — which silently breaks volume
// scheduling. Declining leaves the orphan for the garbage collector.
func TestCreate_UniquenessErrorRefusesMismatchedServerType(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.ServerType = &hcloud.ServerType{Name: "cx42"}
	client.servers[42] = s
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected error, adopted a server of a different server type")
}

func TestCreate_UniquenessErrorRefusesMismatchedLocation(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.Location = &hcloud.Location{Name: "fsn1"}
	client.servers[42] = s
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected error, adopted a server in a different location")
}

// Hetzner deletes asynchronously and keeps the name reserved until it finishes,
// so the garbage collector reaping an orphan leaves a window where a retry
// collides with the dying server. Adopting it would bind the NodeClaim to a
// machine that vanishes seconds later, stalling until the registration timeout.
func TestCreate_UniquenessErrorRefusesDeletingServer(t *testing.T) {
	client := newMockServerClient()
	s := ownedOrphan(42)
	s.Status = hcloud.ServerStatusDeleting
	client.servers[42] = s
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected error, adopted a server Hetzner is deleting")
}

func TestCreate_UniquenessErrorWithNoMatchReturnsError(t *testing.T) {
	client := newMockServerClient()
	client.createErr = uniquenessErr()

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected the original uniqueness error when no server matches")
}

func TestCreate_UniquenessErrorLookupFailureReturnsCreateError(t *testing.T) {
	client := newMockServerClient()
	client.createErr = uniquenessErr()
	client.listErr = fmt.Errorf("api unavailable")

	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), adoptOpts())
	assertRefusedAdoption(t, err, "expected an error when the adoption lookup fails")
	if !strings.Contains(err.Error(), "already used") {
		t.Errorf("expected the original create error to survive, got %v", err)
	}
}

func TestCreate_WaiterErrorIsWrapped(t *testing.T) {
	client := newMockServerClient()
	client.action = &hcloud.Action{ID: 1}
	waiter := &mockActionWaiter{err: fmt.Errorf("action failed")}
	p := NewProviderWithWaiter(client, "test-cluster", waiter)
	_, err := p.Create(context.Background(), CreateOpts{Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1}})
	if err == nil || !strings.Contains(err.Error(), "waiting for server") {
		t.Errorf("expected wrapped wait error, got %v", err)
	}
}

func TestCreate_WaitsForNextActions(t *testing.T) {
	client := newMockServerClient()
	client.action = nil
	client.nextActions = []*hcloud.Action{{ID: 2}, {ID: 3}}
	waiter := &mockActionWaiter{}
	p := NewProviderWithWaiter(client, "test-cluster", waiter)
	if _, err := p.Create(context.Background(), CreateOpts{Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if waiter.waited != 2 {
		t.Errorf("expected 2 next-actions waited, got %d", waiter.waited)
	}
}

// ---------------------------------------------------------------------------
// mockPlacementGroupClient and placement group tests
// ---------------------------------------------------------------------------

type mockPlacementGroupClient struct {
	groups    []*hcloud.PlacementGroup
	nextID    int64
	createErr error
	// recorded arguments for assertions
	lastListOpts   hcloud.PlacementGroupListOpts
	lastCreateOpts hcloud.PlacementGroupCreateOpts
	createCalls    int
}

func newMockPlacementGroupClient() *mockPlacementGroupClient {
	return &mockPlacementGroupClient{nextID: 200}
}

func (m *mockPlacementGroupClient) AllWithOpts(_ context.Context, opts hcloud.PlacementGroupListOpts) ([]*hcloud.PlacementGroup, error) {
	m.lastListOpts = opts
	var result []*hcloud.PlacementGroup
	for _, pg := range m.groups {
		if opts.Name != "" && pg.Name != opts.Name {
			continue
		}
		if opts.Type != "" && pg.Type != opts.Type {
			continue
		}
		result = append(result, pg)
	}
	return result, nil
}

func (m *mockPlacementGroupClient) Create(_ context.Context, opts hcloud.PlacementGroupCreateOpts) (hcloud.PlacementGroupCreateResult, *hcloud.Response, error) {
	m.lastCreateOpts = opts
	m.createCalls++
	if m.createErr != nil {
		return hcloud.PlacementGroupCreateResult{}, nil, m.createErr
	}
	id := m.nextID
	m.nextID++
	pg := &hcloud.PlacementGroup{ID: id, Name: opts.Name, Type: opts.Type}
	m.groups = append(m.groups, pg)
	return hcloud.PlacementGroupCreateResult{PlacementGroup: pg}, nil, nil
}

// TestCreate_SpreadStrategy_CreatesPG verifies that strategy "spread" (default)
// causes a placement group to be created and assigned to the server.
func TestCreate_SpreadStrategy_CreatesPG(t *testing.T) {
	sc := newMockServerClient()
	pgc := newMockPlacementGroupClient()
	p := NewProviderWithPlacementGroups(sc, pgc, "test-cluster", testClusterUID, nil)

	_, err := p.Create(context.Background(), CreateOpts{
		Name:                   "n",
		ServerType:             "cx22",
		Location:               "nbg1",
		Image:                  &hcloud.Image{ID: 1},
		NodePool:               "my-pool",
		PlacementGroupStrategy: "spread",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A placement group should have been created.
	if pgc.createCalls != 1 {
		t.Errorf("expected 1 PG create call, got %d", pgc.createCalls)
	}
	if pgc.lastCreateOpts.Name != "karpenter-test-cluster-my-pool" {
		t.Errorf("expected PG name %q, got %q", "karpenter-test-cluster-my-pool", pgc.lastCreateOpts.Name)
	}
	if pgc.lastCreateOpts.Type != hcloud.PlacementGroupTypeSpread {
		t.Errorf("expected spread type, got %q", pgc.lastCreateOpts.Type)
	}
	// The server create opts must include the placement group.
	if sc.lastOpts.PlacementGroup == nil {
		t.Error("expected PlacementGroup to be set on server create opts")
	}
}

// TestCreate_SpreadStrategy_EmptyStrategy_CreatesPG verifies that an empty
// strategy (the kubebuilder default "spread") also creates a placement group.
func TestCreate_SpreadStrategy_EmptyStrategy_CreatesPG(t *testing.T) {
	sc := newMockServerClient()
	pgc := newMockPlacementGroupClient()
	p := NewProviderWithPlacementGroups(sc, pgc, "test-cluster", testClusterUID, nil)

	_, err := p.Create(context.Background(), CreateOpts{
		Name:       "n",
		ServerType: "cx22",
		Location:   "nbg1",
		Image:      &hcloud.Image{ID: 1},
		NodePool:   "pool-a",
		// PlacementGroupStrategy intentionally left empty -> treated as spread
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.lastOpts.PlacementGroup == nil {
		t.Error("expected PlacementGroup to be set on server create opts when strategy is empty")
	}
}

// TestCreate_NoneStrategy_NoPG verifies that strategy "none" does NOT create or
// assign a placement group.
func TestCreate_NoneStrategy_NoPG(t *testing.T) {
	sc := newMockServerClient()
	pgc := newMockPlacementGroupClient()
	p := NewProviderWithPlacementGroups(sc, pgc, "test-cluster", testClusterUID, nil)

	_, err := p.Create(context.Background(), CreateOpts{
		Name:                   "n",
		ServerType:             "cx22",
		Location:               "nbg1",
		Image:                  &hcloud.Image{ID: 1},
		NodePool:               "my-pool",
		PlacementGroupStrategy: "none",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pgc.createCalls != 0 {
		t.Errorf("expected 0 PG create calls for strategy=none, got %d", pgc.createCalls)
	}
	if sc.lastOpts.PlacementGroup != nil {
		t.Error("expected no PlacementGroup on server create opts when strategy=none")
	}
}

// TestCreate_SpreadStrategy_ReusesPG verifies that when a placement group with
// the expected name already exists, Create reuses it without calling create.
func TestCreate_SpreadStrategy_ReusesPG(t *testing.T) {
	sc := newMockServerClient()
	pgc := newMockPlacementGroupClient()
	// Pre-seed an existing placement group with the expected name.
	existingID := int64(999)
	pgc.groups = []*hcloud.PlacementGroup{
		{ID: existingID, Name: "karpenter-test-cluster-my-pool", Type: hcloud.PlacementGroupTypeSpread},
	}
	p := NewProviderWithPlacementGroups(sc, pgc, "test-cluster", testClusterUID, nil)

	_, err := p.Create(context.Background(), CreateOpts{
		Name:                   "n",
		ServerType:             "cx22",
		Location:               "nbg1",
		Image:                  &hcloud.Image{ID: 1},
		NodePool:               "my-pool",
		PlacementGroupStrategy: "spread",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should NOT have called create.
	if pgc.createCalls != 0 {
		t.Errorf("expected 0 PG create calls (reuse), got %d", pgc.createCalls)
	}
	// Should use the existing PG's ID.
	if sc.lastOpts.PlacementGroup == nil || sc.lastOpts.PlacementGroup.ID != existingID {
		t.Errorf("expected existing PG ID %d, got %v", existingID, sc.lastOpts.PlacementGroup)
	}
}

// TestCreate_SpreadStrategy_EmptyNodePool verifies the fallback PG name when
// NodePool is empty.
func TestCreate_SpreadStrategy_EmptyNodePool(t *testing.T) {
	sc := newMockServerClient()
	pgc := newMockPlacementGroupClient()
	p := NewProviderWithPlacementGroups(sc, pgc, "test-cluster", testClusterUID, nil)

	_, err := p.Create(context.Background(), CreateOpts{
		Name:                   "n",
		ServerType:             "cx22",
		Location:               "nbg1",
		Image:                  &hcloud.Image{ID: 1},
		NodePool:               "", // empty
		PlacementGroupStrategy: "spread",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pgc.lastCreateOpts.Name != "karpenter-test-cluster" {
		t.Errorf("expected PG name %q for empty NodePool, got %q", "karpenter-test-cluster", pgc.lastCreateOpts.Name)
	}
}

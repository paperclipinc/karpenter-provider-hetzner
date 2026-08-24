package garbagecollection

import (
	"context"
	"fmt"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
)

type fakeInstanceProvider struct {
	servers   []*hcloud.Server
	deleted   []string
	listErr   error
	deleteErr error
}

func (f *fakeInstanceProvider) List(_ context.Context) ([]*hcloud.Server, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.servers, nil
}

func (f *fakeInstanceProvider) Delete(_ context.Context, providerID string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, providerID)
	return nil
}

const testCluster = "test-cluster"

// server is a server this installation created: it carries the ownership labels
// the sweep re-checks before touching anything. It deliberately sets no Created
// timestamp -- the sweep never reads one, and a fixture that took an age would
// imply a young server is protected when nothing in the code protects it.
func server(id int64, name string) *hcloud.Server {
	return &hcloud.Server{
		ID: id, Name: name,
		Labels: map[string]string{
			apiv1.ServerLabelManagedBy: apiv1.ServerValueManagedBy,
			apiv1.ServerLabelCluster:   testCluster,
		},
	}
}

func newTestController(kubeClient client.Client, instances InstanceProvider) *Controller {
	return NewController(kubeClient, instances, testCluster)
}

// sweep runs one reconcile and fails the test on error.
func sweep(t *testing.T, c *Controller) {
	t.Helper()
	if _, err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// sweepPastGrace runs exactly enough sweeps for an orphan to become reapable.
func sweepPastGrace(t *testing.T, c *Controller) {
	t.Helper()
	for range requiredUnownedSweeps {
		sweep(t, c)
	}
}

func nodeClaimFor(id int64, name string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     karpv1.NodeClaimStatus{ProviderID: instance.FormatProviderID(id)},
	}
}

func nodeFor(id int64, name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{ProviderID: instance.FormatProviderID(id)},
	}
}

// readyNodeFor is Ready but never completed registration -- no
// karpenter.sh/registered label. This is the shape of a server that booted and
// joined but whose NodeClaim was lost before Karpenter finished with it.
func readyNodeFor(id int64, name string) *corev1.Node {
	n := nodeFor(id, name)
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	return n
}

// registeredReadyNodeFor is a fully registered, Ready node: Karpenter stamped
// karpenter.sh/registered=true on it, which is core's own definition of
// registered (state/statenode.go Registered()).
func registeredReadyNodeFor(id int64, name string) *corev1.Node {
	n := readyNodeFor(id, name)
	n.Labels = map[string]string{karpv1.NodeRegisteredLabelKey: "true"}
	return n
}

// newFakeClient registers NodeClaim's status subresource so the fake behaves like
// a real apiserver: status is stripped on CREATE and must be written through
// Status(). Without it a test could seed a provider ID in a single Create and
// prove a sequence karpenter never actually performs.
func newFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithStatusSubresource(&karpv1.NodeClaim{}).
		WithObjects(objs...).
		Build()
}

// A server whose NodeClaim no longer exists is billing with no owner. Nothing in
// Karpenter core reaps it: core's garbage collector only deletes NodeClaims that
// have no instance, never the reverse.
func TestReconcile_DeletesOrphanedServerAndNode(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	kubeClient := newFakeClient(nodeFor(42, "worker-abc"))

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 1 || instances.deleted[0] != instance.FormatProviderID(42) {
		t.Errorf("expected server 42 deleted, got %v", instances.deleted)
	}
	node := &corev1.Node{}
	err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "worker-abc"}, node)
	if err == nil {
		t.Error("expected the orphaned Node object to be deleted, it still exists")
	}
}

func TestReconcile_SparesServerWithNodeClaim(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	kubeClient := newFakeClient(nodeClaimFor(42, "worker-abc"), nodeFor(42, "worker-abc"))

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("deleted a server that has a NodeClaim: %v", instances.deleted)
	}
	node := &corev1.Node{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "worker-abc"}, node); err != nil {
		t.Errorf("deleted the Node of a live NodeClaim: %v", err)
	}
}

// A server whose provider ID has not reached the NodeClaim status yet looks
// unowned. Requiring several consecutive unowned observations keeps the sweep
// clear of nodes that are still being born.
func TestReconcile_SparesServerNotYetSeenUnownedEnough(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	c := newTestController(newFakeClient(), instances)

	for range requiredUnownedSweeps - 1 {
		sweep(t, c)
		if len(instances.deleted) != 0 {
			t.Fatalf("deleted before the orphan was observed enough times: %v", instances.deleted)
		}
	}
}

// The counter must measure how long the server has been an orphan, not how long
// the process has been running. An owner reappearing resets it, so a single bad
// snapshot of the NodeClaim list can never accumulate into a deletion.
func TestReconcile_OwnerReappearingResetsTheCounter(t *testing.T) {
	srv := server(42, "worker-abc")
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{srv}}
	kubeClient := newFakeClient()
	c := newTestController(kubeClient, instances)

	// One sweep short of reapable.
	for range requiredUnownedSweeps - 1 {
		sweep(t, c)
	}
	// The NodeClaim comes back (an informer relist, an etcd blip resolving). Write
	// it in the two steps karpenter's launch controller actually performs: a real
	// apiserver strips status on CREATE for a resource with a status subresource,
	// so seeding the provider ID through Create would only work against the fake.
	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "worker-abc"}}
	if err := kubeClient.Create(context.Background(), nc); err != nil {
		t.Fatalf("seeding nodeclaim: %v", err)
	}
	nc.Status.ProviderID = instance.FormatProviderID(42)
	if err := kubeClient.Status().Update(context.Background(), nc); err != nil {
		t.Fatalf("writing the nodeclaim provider ID: %v", err)
	}
	sweep(t, c)
	// It disappears again; the count must start over rather than resume.
	if err := kubeClient.Delete(context.Background(), nodeClaimFor(42, "worker-abc")); err != nil {
		t.Fatalf("removing nodeclaim: %v", err)
	}
	sweep(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("counter survived the owner reappearing: %v", instances.deleted)
	}
}

func TestReconcile_OrphanWithoutNodeObject(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	kubeClient := newFakeClient()

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 1 {
		t.Errorf("expected the server deleted even with no Node object, got %v", instances.deleted)
	}
}

// A server that is not this installation's must never be touched, whatever the
// caller's List returned. The label selector that normally scopes List lives in
// another package and no test here exercises it, so the sweep re-checks.
func TestReconcile_IgnoresServerFromAnotherCluster(t *testing.T) {
	foreign := server(42, "worker-abc")
	foreign.Labels[apiv1.ServerLabelCluster] = "someone-elses-cluster"
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{foreign}}

	c := newTestController(newFakeClient(), instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("deleted another cluster's server: %v", instances.deleted)
	}
}

func TestReconcile_IgnoresServerNotManagedByKarpenter(t *testing.T) {
	unmanaged := server(42, "worker-abc")
	delete(unmanaged.Labels, apiv1.ServerLabelManagedBy)
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{unmanaged}}

	c := newTestController(newFakeClient(), instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("deleted a server this provider does not manage: %v", instances.deleted)
	}
}

func TestReconcile_EmptyListIsNoOp(t *testing.T) {
	instances := &fakeInstanceProvider{}
	c := newTestController(newFakeClient(), instances)
	if _, err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(instances.deleted) != 0 {
		t.Errorf("deleted something from an empty list: %v", instances.deleted)
	}
}

// If the server could not be terminated it is still running, so its Node object
// must stay too. Removing the Node while the server lives would hide a billing
// instance from the very sweep meant to catch it.
//
// A server that can never be deleted (Hetzner delete protection, a locked
// server) also must not poison the sweep: returning an error drops RequeueAfter
// and ratchets controller-runtime's backoff toward its cap, so one stuck server
// would delay reaping every other orphan.
func TestReconcile_DeleteFailureKeepsNodeAndCadence(t *testing.T) {
	instances := &fakeInstanceProvider{
		servers:   []*hcloud.Server{server(42, "worker-abc")},
		deleteErr: fmt.Errorf("api unavailable"),
	}
	kubeClient := newFakeClient(nodeFor(42, "worker-abc"))

	c := newTestController(kubeClient, instances)
	for range requiredUnownedSweeps - 1 {
		sweep(t, c)
	}
	res, err := c.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("a single undeletable server must not fail the sweep: %v", err)
	}
	if res.RequeueAfter != resyncInterval {
		t.Errorf("expected the sweep to keep its cadence, got RequeueAfter %v", res.RequeueAfter)
	}

	node := &corev1.Node{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "worker-abc"}, node); err != nil {
		t.Errorf("removed the Node object though the server delete failed: %v", err)
	}
}

// The provider ID is written to the NodeClaim only after Create returns, and
// Create blocks waiting on Hetzner's create actions. The nodeclaim label is
// stamped on the server before any of that, so it is the ownership signal that
// survives a crash — or a slow launch — mid-create.
func TestReconcile_SparesServerLabelledForALiveNodeClaim(t *testing.T) {
	s := server(42, "worker-abc")
	// Assign into the map rather than replacing it: dropping managed-by and
	// cluster would make the sweep skip this server on the ownership re-check,
	// and the name-based branch under test would never be reached.
	s.Labels[apiv1.ServerLabelNodeClaim] = "worker-abc"
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{s}}
	// The NodeClaim exists but its status has no provider ID yet.
	kubeClient := newFakeClient(&karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "worker-abc"}})

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("deleted a server whose NodeClaim is still alive: %v", instances.deleted)
	}
}

// A kubelet still reporting Ready means the machine is alive and running
// workloads, whatever the NodeClaims say. Karpenter core refuses to act on
// cloud-provider truth in this state, and so must this sweep: a lost NodeClaim
// (CRD reinstall, etcd restore, forced finalizer removal) would otherwise become
// a fleet-wide termination with no eviction and no drain.
func TestReconcile_SparesOrphanWithRegisteredReadyNode(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	kubeClient := newFakeClient(registeredReadyNodeFor(42, "worker-abc"))

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("destroyed a server whose node is still Ready: %v", instances.deleted)
	}
	node := &corev1.Node{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "worker-abc"}, node); err != nil {
		t.Errorf("deleted the Node of a Ready machine: %v", err)
	}
}

// The Ready check must not key on Ready alone. A server that booted and joined
// but never completed registration carries no karpenter.sh/registered label, and
// those are precisely the orphans this package exists to reap -- sparing them
// because the kubelet answers would leave them billing forever.
//
// Registration, not the unregistered taint, is the signal: that taint reaches a
// node only if the NodePool declares it as a startupTaint, so a cluster that does
// not would never reap anything. Karpenter applies the registered label itself.
func TestReconcile_ReapsReadyButUnregisteredNode(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	kubeClient := newFakeClient(readyNodeFor(42, "worker-abc"))

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 1 {
		t.Errorf("failed to reap an unregistered orphan whose kubelet still reports Ready: %v", instances.deleted)
	}
}

// A node the CCM has not stamped with a provider ID yet cannot be matched by
// provider ID, and failing to find it must not silently skip the safety guard.
// The server name equals the NodeClaim name equals the node name, so fall back to
// that rather than treating the node as absent.
func TestReconcile_SparesRegisteredReadyNodeWithoutProviderID(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	node := registeredReadyNodeFor(42, "worker-abc")
	node.Spec.ProviderID = "" // hcloud CCM has not filled it in yet
	kubeClient := newFakeClient(node)

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("destroyed a live node the CCM had not yet stamped: %v", instances.deleted)
	}
}

// Two Nodes claiming one provider ID is an ambiguous state Karpenter core
// refuses to resolve by guessing. A sweep that destroys machines must not pick
// one arbitrarily by list order.
func TestReconcile_SparesServerWithAmbiguousNodes(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	kubeClient := newFakeClient(nodeFor(42, "worker-abc"), nodeFor(42, "worker-abc-stale"))

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	if len(instances.deleted) != 0 {
		t.Errorf("acted on an ambiguous provider ID: %v", instances.deleted)
	}
}

// A failed list must not become a returned error: operatorpkg discards
// RequeueAfter on error and lets controller-runtime's rate limiter ratchet the
// interval toward its cap, so a spell of API failures would slow the sweep for
// every orphan in the fleet. Log it and keep the cadence.
func TestReconcile_ListErrorKeepsCadence(t *testing.T) {
	instances := &fakeInstanceProvider{listErr: fmt.Errorf("api unavailable")}
	c := newTestController(newFakeClient(), instances)

	res, err := c.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("a list failure must not fail the sweep: %v", err)
	}
	if res.RequeueAfter != resyncInterval {
		t.Errorf("expected the sweep to keep its cadence, got RequeueAfter %v", res.RequeueAfter)
	}
	if len(instances.deleted) != 0 {
		t.Errorf("deleted something despite failing to list: %v", instances.deleted)
	}
}

// Losing sight of the NodeClaims must never be the reason a machine dies: an
// unreadable claim list makes every server look unowned.
func TestReconcile_NodeClaimListFailureDeletesNothing(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	c := newTestController(&failingClaimClient{Client: newFakeClient()}, instances)

	for range requiredUnownedSweeps + 2 {
		res, err := c.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("a nodeclaim list failure must not fail the sweep: %v", err)
		}
		if res.RequeueAfter != resyncInterval {
			t.Errorf("expected the sweep to keep its cadence, got %v", res.RequeueAfter)
		}
	}
	if len(instances.deleted) != 0 {
		t.Errorf("destroyed machines while unable to read NodeClaims: %v", instances.deleted)
	}
}

// failingClaimClient fails NodeClaim listing and passes everything else through.
type failingClaimClient struct {
	client.Client
}

func (f *failingClaimClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*karpv1.NodeClaimList); ok {
		return fmt.Errorf("apiserver unavailable")
	}
	return f.Client.List(ctx, list, opts...)
}

// failingNodeClient fails Node listing and passes everything else through.
type failingNodeClient struct {
	client.Client
}

func (f *failingNodeClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.NodeList); ok {
		return fmt.Errorf("apiserver unavailable")
	}
	return f.Client.List(ctx, list, opts...)
}

// Sparing a server must restart its grace, not merely postpone the deletion by
// one sweep. Otherwise a NodeClaim wipe (a CRD reinstall, an etcd restore) leaves
// every live machine sitting at a spent counter, and the first sweep that catches
// one with a briefly NotReady kubelet -- a reboot, a kubelet upgrade, a network
// blip -- destroys it with no drain and no volume detachment.
// The number of sweeps spent being spared is varied deliberately. If sparing
// merely drops the counter rather than never advancing it, the count cycles
// 1, 2, 3, reset, so whether the machine survives depends on where in that cycle
// the kubelet blips -- a test with a single fixed iteration count passes or fails
// by luck.
func TestReconcile_SparingAServerRestartsItsGrace(t *testing.T) {
	for _, sparedSweeps := range []int{1, 2, 3, 4, 5, 7, 11, 200} {
		t.Run(fmt.Sprintf("spared_%d", sparedSweeps), func(t *testing.T) {
			instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
			kubeClient := newFakeClient(registeredReadyNodeFor(42, "worker-abc"))

			c := newTestController(kubeClient, instances)
			for range sparedSweeps {
				sweep(t, c)
			}
			if len(instances.deleted) != 0 {
				t.Fatalf("destroyed a registered, Ready machine: %v", instances.deleted)
			}

			// The kubelet stops reporting for a moment.
			live := &corev1.Node{}
			if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "worker-abc"}, live); err != nil {
				t.Fatalf("reading the node: %v", err)
			}
			live.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}
			if err := kubeClient.Status().Update(context.Background(), live); err != nil {
				t.Fatalf("updating the node status: %v", err)
			}

			sweep(t, c)
			if len(instances.deleted) != 0 {
				t.Errorf("one NotReady sweep destroyed a machine spared for %d sweeps: %v",
					sparedSweeps, instances.deleted)
			}
		})
	}
}

// A machine that stays unreachable is still an orphan. Once sparing stops, a
// full fresh grace window must elapse -- no more, no less.
func TestReconcile_ReapsAfterAFullFreshWindowOfNotReady(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}
	node := registeredReadyNodeFor(42, "worker-abc")
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}
	kubeClient := newFakeClient(node)

	c := newTestController(kubeClient, instances)
	for range requiredUnownedSweeps - 1 {
		sweep(t, c)
	}
	if len(instances.deleted) != 0 {
		t.Fatalf("reaped before a full window elapsed: %v", instances.deleted)
	}
	sweep(t, c)
	if len(instances.deleted) != 1 {
		t.Errorf("failed to reap after a full window of NotReady: %v", instances.deleted)
	}
}

// Hetzner deletes asynchronously, so a reaped server keeps being listed while the
// delete runs. Re-deleting it double-counts the reap, or reports an error for a
// machine that was in fact reclaimed -- and result="error" is the signal the
// README tells operators to alert on.
func TestReconcile_DoesNotImmediatelyReReapADyingServer(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{server(42, "worker-abc")}}

	c := newTestController(newFakeClient(), instances)
	sweepPastGrace(t, c)
	if len(instances.deleted) != 1 {
		t.Fatalf("expected exactly one delete, got %v", instances.deleted)
	}

	// The server is still listed because Hetzner has not finished with it.
	sweep(t, c)
	if len(instances.deleted) != 1 {
		t.Errorf("deleted a server already being deleted: %v", instances.deleted)
	}
}

// The grace counters of servers the sweep never reached must survive a mid-loop
// Node-list failure. Committing the partially built map would reset them, so a
// persistent Node-list failure plus one orphan early in hcloud's list order would
// keep every orphan behind it at zero forever -- the billing leak this package
// exists to close.
func TestReconcile_NodeListFailureKeepsCountersOfUnvisitedServers(t *testing.T) {
	instances := &fakeInstanceProvider{servers: []*hcloud.Server{
		server(42, "worker-abc"),
		server(43, "worker-def"),
	}}
	kubeClient := &failingNodeClient{Client: newFakeClient()}

	c := newTestController(kubeClient, instances)
	// Both servers accumulate grace; the first to reach it trips the Node list.
	for range requiredUnownedSweeps + 2 {
		res, err := c.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("a node list failure must not fail the sweep: %v", err)
		}
		if res.RequeueAfter != resyncInterval {
			t.Errorf("expected the sweep to keep its cadence, got %v", res.RequeueAfter)
		}
	}
	if len(instances.deleted) != 0 {
		t.Fatalf("deleted while unable to read Nodes: %v", instances.deleted)
	}
	// Nothing may advance toward deletion while the guards cannot be evaluated,
	// and no server may be penalised for its position in the list.
	first, second := instance.FormatProviderID(42), instance.FormatProviderID(43)
	if c.unownedSweeps[first] != c.unownedSweeps[second] {
		t.Errorf("servers treated asymmetrically by list position: counters=%v", c.unownedSweeps)
	}

	// Once Nodes are readable again both progress together and are reaped in the
	// same sweep -- the outage neither advanced nor permanently penalised them.
	c.kubeClient = newFakeClient()
	for range requiredUnownedSweeps - 1 {
		sweep(t, c)
		if len(instances.deleted) != 0 {
			t.Fatalf("reaped before a full window after recovery: %v", instances.deleted)
		}
	}
	sweep(t, c)
	if len(instances.deleted) != 2 {
		t.Errorf("expected both servers reaped after recovery, got %v", instances.deleted)
	}
}

// A NodeClaimNotFoundError from Delete means hcloud has already finished with the
// server -- the outcome the sweep wanted -- so it must fall through and clean up
// the Node object rather than treat it as a failure. Without the
// IgnoreNodeClaimNotFoundError wrapper every already-reclaimed server would report
// result="error", which the README tells operators means a machine cannot be
// reclaimed and is still billing, and would strand its Node.
func TestReconcile_AlreadyDeletedServerStillCleansUpItsNode(t *testing.T) {
	instances := &fakeInstanceProvider{
		servers:   []*hcloud.Server{server(42, "worker-abc")},
		deleteErr: karpcp.NewNodeClaimNotFoundError(fmt.Errorf("server 42 not found")),
	}
	kubeClient := newFakeClient(nodeFor(42, "worker-abc"))

	c := newTestController(kubeClient, instances)
	sweepPastGrace(t, c)

	node := &corev1.Node{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "worker-abc"}, node); err == nil {
		t.Error("left behind the Node of a server hcloud had already deleted")
	}
}

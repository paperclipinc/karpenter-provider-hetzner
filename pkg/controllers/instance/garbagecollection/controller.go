// Package garbagecollection terminates Hetzner servers that Karpenter has no
// NodeClaim for.
//
// Karpenter core's own garbage collector only runs in one direction: it deletes
// NodeClaims whose instance has disappeared. Nothing in core terminates an
// instance whose NodeClaim has disappeared, so that direction is the cloud
// provider's responsibility.
//
// Servers end up orphaned when the controller dies between the Hetzner create
// call and persisting the provider ID — a crash window no in-process error
// handling can close. The server keeps running and billing, and because its
// NodeClaim is gone nothing ever reaps it.
package garbagecollection

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeutils "sigs.k8s.io/karpenter/pkg/utils/node"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/metrics"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
)

const (
	// requiredUnownedSweeps is how many consecutive sweeps a server must be seen
	// with no NodeClaim before it is reaped. Grace is counted in observations
	// rather than elapsed time so it measures how long the server has been an
	// orphan, not how old the machine is, and so it depends on no clock at all.
	// The counter resets the moment an owner reappears, so a transient gap in the
	// NodeClaim list can never accumulate to a deletion.
	requiredUnownedSweeps = 3

	// resyncInterval is how often the orphan sweep runs.
	resyncInterval = 2 * time.Minute
)

// InstanceProvider is the narrow instance API this controller needs.
type InstanceProvider interface {
	List(ctx context.Context) ([]*hcloud.Server, error)
	Delete(ctx context.Context, providerID string) error
}

// Controller sweeps orphaned servers and the Node objects left behind with them.
type Controller struct {
	kubeClient  client.Client
	instances   InstanceProvider
	clusterName string

	// unownedSweeps counts, per provider ID, how many consecutive sweeps have
	// seen the server without an owner. Entries vanish as soon as a server is
	// owned again or stops being returned by List, so the map cannot grow beyond
	// the current fleet. Only Reconcile touches it, and the controller is a
	// singleton, so it needs no lock.
	unownedSweeps map[string]int
}

func NewController(kubeClient client.Client, instances InstanceProvider, clusterName string) *Controller {
	return &Controller{
		kubeClient:    kubeClient,
		instances:     instances,
		clusterName:   clusterName,
		unownedSweeps: map[string]int{},
	}
}

func (c *Controller) Name() string { return "instance.garbagecollection" }

// claims indexes the NodeClaims Karpenter currently has by both of the signals a
// server carries: the provider ID Karpenter writes to the NodeClaim status, and
// the NodeClaim name the provider stamps on the server at create time. The label
// is set before the server exists, so it survives a crash mid-launch — the
// provider ID does not, because it is written only after Create returns.
type claims struct {
	providerIDs sets.Set[string]
	names       sets.Set[string]
}

// owns reports whether Karpenter still has a NodeClaim for this server.
func (cl claims) owns(s *hcloud.Server) bool {
	if cl.providerIDs.Has(instance.FormatProviderID(s.ID)) {
		return true
	}
	name := s.Labels[apiv1.ServerLabelNodeClaim]
	return name != "" && cl.names.Has(name)
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())
	log := logf.FromContext(ctx)

	// The provider scopes this by the managed-by and cluster labels, but every
	// server is re-checked below rather than trusting that.
	servers, err := c.instances.List(ctx)
	if err != nil {
		log.Error(err, "listing servers for the orphan sweep")
		metrics.RecordOrphanGC(metrics.OrphanSweepFailed)
		return reconciler.Result{RequeueAfter: resyncInterval}, nil
	}
	if len(servers) == 0 {
		c.unownedSweeps = map[string]int{}
		return reconciler.Result{RequeueAfter: resyncInterval}, nil
	}

	claimed, err := c.currentClaims(ctx)
	if err != nil {
		// Treat an unreadable NodeClaim list as "everything is owned": losing sight
		// of the claims must never be the reason a machine is destroyed. Leaving the
		// counters untouched means nothing advances toward deletion this sweep.
		log.Error(err, "listing nodeclaims for the orphan sweep")
		metrics.RecordOrphanGC(metrics.OrphanSweepFailed)
		return reconciler.Result{RequeueAfter: resyncInterval}, nil
	}

	// Nodes are only needed once an orphan candidate turns up, which is rare, so
	// the index is built at most once per sweep and only on demand.
	var idx *nodeIndex
	nodeIndexOnce := func() (*nodeIndex, error) {
		if idx == nil {
			loaded, err := c.buildNodeIndex(ctx)
			if err != nil {
				return nil, err
			}
			idx = &loaded
		}
		return idx, nil
	}

	// unowned records which servers were seen without an owner on THIS sweep.
	// Replacing the previous map at the end both advances the counters and drops
	// servers that regained an owner or no longer exist.
	unowned := make(map[string]int, len(c.unownedSweeps))

	for _, s := range servers {
		providerID := instance.FormatProviderID(s.ID)

		// Re-check ownership labels here rather than relying on the caller's list
		// filter. The scoping that keeps this sweep inside its own cluster lives in
		// the provider's label selector, which this package cannot see and its
		// tests do not exercise; a widened List would silently turn a per-cluster
		// sweep into a project-wide deleter.
		if s.Labels[apiv1.ServerLabelManagedBy] != apiv1.ServerValueManagedBy ||
			s.Labels[apiv1.ServerLabelCluster] != c.clusterName {
			continue
		}
		if claimed.owns(s) {
			continue
		}

		nodes, err := nodeIndexOnce()
		if err != nil {
			// Keep the cadence: dropping RequeueAfter here ratchets controller-runtime
			// backoff toward its cap and delays every orphan in the fleet. Leave the
			// counters untouched as the NodeClaim path above does -- `unowned` holds
			// only the servers visited so far, so committing it here would reset the
			// grace of every server behind this one.
			log.Error(err, "listing nodes for the orphan sweep")
			metrics.RecordOrphanGC(metrics.OrphanSweepFailed)
			return reconciler.Result{RequeueAfter: resyncInterval}, nil
		}
		node, ambiguous := nodes.find(providerID, s.Name)
		if ambiguous {
			// Two Nodes claiming one provider ID is an ambiguous state Karpenter core
			// refuses to resolve by guessing, and neither should a sweep that deletes
			// machines.
			log.Info("skipping server whose provider ID maps to multiple nodes",
				"name", s.Name, "id", s.ID)
			metrics.RecordOrphanGC(metrics.OrphanSkippedAmbiguous)
			continue
		}
		// A registered kubelet still reporting Ready means the machine is alive and
		// carrying workloads, whatever the NodeClaims say. Karpenter core applies
		// the same check before acting on cloud-provider truth; without it a lost
		// NodeClaim (a CRD reinstall, an etcd restore, a forced finalizer removal)
		// turns this sweep into a fleet-wide termination with no eviction, no drain
		// and no volume detachment.
		//
		// Ready alone is the wrong signal though. A server that booted and joined
		// but never finished registering carries no karpenter.sh/registered label,
		// and those are exactly the orphans this package exists to reap. Sparing
		// them because the kubelet answers would leave the leak in place.
		if node != nil &&
			nodeutils.GetCondition(node, corev1.NodeReady).Status == corev1.ConditionTrue &&
			isRegistered(node) {
			log.Info("skipping orphaned server whose registered node is still Ready",
				"name", s.Name, "id", s.ID, "node", node.Name)
			metrics.RecordOrphanGC(metrics.OrphanSkippedReady)
			continue
		}

		// Only a sweep that got this far counts toward grace: the server is
		// unowned AND nothing above objected to reclaiming it.
		//
		// Counting earlier would be wrong in a way that is easy to miss. A machine
		// spared by the guards above would still advance -- and, once it reached the
		// threshold, sit there fully armed -- so the first sweep that happened to
		// catch its kubelet briefly NotReady would destroy it outright, with no
		// drain and no volume detachment. Dropping the entry when a guard fires is
		// not enough on its own either: the count simply cycles back up, leaving the
		// machine armed a predictable fraction of the time.
		//
		// Counting only unguarded sweeps means a machine that stops being spared
		// must earn a complete fresh window before anything happens to it.
		sweeps := c.unownedSweeps[providerID] + 1
		unowned[providerID] = sweeps
		if sweeps < requiredUnownedSweeps {
			continue
		}

		// A NodeClaimNotFoundError means the server is already gone, which is the
		// outcome we wanted; fall through and clean up its Node object.
		if err := karpcp.IgnoreNodeClaimNotFoundError(c.instances.Delete(ctx, providerID)); err != nil {
			// One undeletable server (delete protection, a locked server) must not
			// suppress the sweep cadence. Returning an error drops the RequeueAfter
			// and ratchets controller-runtime's backoff toward its cap, delaying every
			// other orphan behind the one that cannot be reaped. The metric is what
			// makes it alertable -- otherwise a permanently undeletable server bills
			// forever behind a log line nobody reads.
			//
			// The count is carried forward here, unlike the guards above: this
			// server did look reapable, so the next sweep should retry rather than
			// re-earn the whole grace window.
			unowned[providerID] = sweeps
			log.Error(err, "deleting orphaned server", "name", s.Name, "id", s.ID, "providerID", providerID)
			metrics.RecordOrphanGC(metrics.OrphanError)
			continue
		}
		// Dropping the entry here matters because Hetzner deletes asynchronously:
		// the server keeps being listed while the delete runs, and a still-armed
		// counter would delete it again on the next sweep, double-counting the
		// reap or reporting a spurious error for a machine already reclaimed.
		delete(unowned, providerID)
		log.Info("garbage collected orphaned server",
			"name", s.Name, "id", s.ID, "providerID", providerID, "unownedSweeps", sweeps)
		metrics.RecordOrphanGC(metrics.OrphanReaped)

		if node != nil {
			// The index was read before a run of blocking hcloud calls, so this Node
			// may have been replaced by a same-named one since. Deleting by UID makes
			// that a no-op instead of destroying the replacement.
			if err := client.IgnoreNotFound(c.kubeClient.Delete(ctx, node,
				client.Preconditions{UID: &node.UID})); err != nil {
				log.Error(err, "deleting the node of a garbage collected server", "node", node.Name)
			}
		}
	}
	c.unownedSweeps = unowned
	return reconciler.Result{RequeueAfter: resyncInterval}, nil
}

// isRegistered reports whether Karpenter finished registering this node.
// Karpenter stamps karpenter.sh/registered=true as the last step of registration
// and treats the label as the definition of registered itself
// (state/statenode.go), so its absence means the node never completed the
// handshake no matter what its kubelet reports.
//
// The unregistered taint is deliberately not used for this. It reaches a node
// only when the NodePool declares it as a startupTaint, so a cluster that does
// not configure it would have every orphan look registered and nothing would ever
// be reaped. Karpenter applies this label on its own.
func isRegistered(node *corev1.Node) bool {
	return node.Labels[karpv1.NodeRegisteredLabelKey] == "true"
}

// currentClaims returns the NodeClaims Karpenter still has, indexed by provider
// ID and by name. A NodeClaim in any state counts as ownership: only servers
// with no NodeClaim at all are orphans.
func (c *Controller) currentClaims(ctx context.Context) (claims, error) {
	list := &karpv1.NodeClaimList{}
	if err := c.kubeClient.List(ctx, list); err != nil {
		return claims{}, fmt.Errorf("listing nodeclaims: %w", err)
	}
	cl := claims{providerIDs: sets.New[string](), names: sets.New[string]()}
	for i := range list.Items {
		if id := list.Items[i].Status.ProviderID; id != "" {
			cl.providerIDs.Insert(id)
		}
		cl.names.Insert(list.Items[i].Name)
	}
	return cl, nil
}

// nodeIndex locates the Node backing a server. Provider ID is the reliable key,
// but the hcloud CCM stamps it only after the kubelet has already registered the
// Node, so during that window -- or whenever the CCM is down -- a live node has
// none. Indexing by name as well closes that gap: this provider names every
// server after its NodeClaim, and the node inherits that name as its hostname.
type nodeIndex struct {
	byProviderID map[string][]*corev1.Node
	byName       map[string]*corev1.Node
}

// find returns the Node for a server and whether the match was ambiguous.
func (idx nodeIndex) find(providerID, serverName string) (*corev1.Node, bool) {
	if matching := idx.byProviderID[providerID]; len(matching) > 0 {
		if len(matching) > 1 {
			return nil, true
		}
		return matching[0], false
	}
	// Fall back to the name only for nodes carrying no provider ID at all. A node
	// stamped with a different provider ID belongs to a different server.
	if node, ok := idx.byName[serverName]; ok && node.Spec.ProviderID == "" {
		return node, false
	}
	return nil, false
}

// buildNodeIndex lists Nodes once and indexes them by provider ID and by name.
// Provider-ID collisions are kept rather than collapsed so the caller can refuse
// to act on an ambiguous provider ID.
func (c *Controller) buildNodeIndex(ctx context.Context) (nodeIndex, error) {
	list := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, list); err != nil {
		return nodeIndex{}, fmt.Errorf("listing nodes: %w", err)
	}
	idx := nodeIndex{
		byProviderID: make(map[string][]*corev1.Node, len(list.Items)),
		byName:       make(map[string]*corev1.Node, len(list.Items)),
	}
	for i := range list.Items {
		node := &list.Items[i]
		idx.byName[node.Name] = node
		if id := node.Spec.ProviderID; id != "" {
			idx.byProviderID[id] = append(idx.byProviderID[id], node)
		}
	}
	return idx, nil
}

// Register wires the controller into the manager.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}

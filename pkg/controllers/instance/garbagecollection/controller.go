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
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
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
	clusterUID  string
	clock       clock.Clock

	// mode selects whether the sweep reclaims or only reports.
	mode Mode

	// startedAt is when this process's sweeps began. Grace is counted in
	// consecutive observations, which a restart or a leader handover resets to
	// zero -- and the instability that strands servers is exactly what causes
	// those. Without a floor, a fresh process could reap on its third sweep
	// having seen the cluster for six minutes; with one, it must have been
	// watching for a full grace window first.
	//
	// This is the piece that makes in-process counting safe rather than merely
	// simple. It converts operator instability into DELAYED reaping instead of
	// either never reaping (no floor, counters forever reset) or reaping on
	// stale evidence (a durable marker written by a process that is gone).
	startedAt time.Time

	// recorder publishes reclamations onto the Node. Nil outside the manager.
	recorder events.EventRecorder

	// unownedSweeps counts, per provider ID, how many consecutive sweeps have
	// seen the server without an owner. Entries vanish as soon as a server is
	// owned again or stops being returned by List, so the map cannot grow beyond
	// the current fleet. Only Reconcile touches it, and the controller is a
	// singleton, so it needs no lock.
	unownedSweeps map[string]int

	// reportedForeignUIDs remembers which colliding clusters have already been
	// reported, so a shared CLUSTER_NAME is logged once rather than every sweep.
	reportedForeignUIDs map[string]bool
}

// Mode selects how the sweep behaves. It is the config's own type rather than a
// bool so that adding a mode cannot silently fall through to the deleting
// branch: the controller decides what each mode means, in one place.
type Mode string

const (
	// ModeEnabled reclaims orphaned servers.
	ModeEnabled Mode = "enabled"
	// ModeObserve runs every check and reports what it would reclaim, deleting
	// nothing and writing nothing outside the cluster.
	ModeObserve Mode = "observe"
)

func NewController(
	kubeClient client.Client,
	instances InstanceProvider,
	clusterName, clusterUID string,
	mode Mode,
	clk clock.Clock,
) *Controller {
	return &Controller{
		kubeClient:          kubeClient,
		instances:           instances,
		clusterName:         clusterName,
		clusterUID:          clusterUID,
		mode:                mode,
		clock:               clk,
		startedAt:           clk.Now(),
		unownedSweeps:       map[string]int{},
		reportedForeignUIDs: map[string]bool{},
	}
}

// watchedLongEnough reports whether this process has been sweeping for a full
// grace window. Until it has, its observation counts describe too short a
// history to justify deleting anything.
func (c *Controller) watchedLongEnough() bool {
	return c.clock.Since(c.startedAt) >= requiredUnownedSweeps*resyncInterval
}

// managedByThisCluster reports whether this installation may act on a server,
// reporting the one kind of refusal an operator needs to hear about.
//
// The ownership rule itself lives in apiv1.OwnedByCluster, shared with the
// adoption path so the two destructive callers cannot drift apart.
func (c *Controller) managedByThisCluster(ctx context.Context, s *hcloud.Server) bool {
	if apiv1.OwnedByCluster(s.Labels, c.clusterName, c.clusterUID) {
		return true
	}
	// A server that carries our management labels and our cluster name but a
	// different UID was refused on the UID alone -- the one refusal that means
	// something is misconfigured rather than simply not ours.
	if s.Labels[apiv1.ServerLabelManagedBy] == apiv1.ServerValueManagedBy &&
		s.Labels[apiv1.ServerLabelCluster] == c.clusterName {
		c.reportForeignCluster(ctx, s.Labels[apiv1.ServerLabelClusterUID])
	}
	return false
}

// reportForeignCluster records a server refused because its cluster UID is not
// ours.
//
// Declining is the safe half; saying so is the useful half. Left silent, this is
// indistinguishable from a cluster that simply has no orphans. The metric fires
// every sweep because it is the only alertable evidence -- the log deliberately
// does not repeat, so after a pod restart it is the sole remaining signal. The
// log fires once per foreign cluster, where it stays readable.
func (c *Controller) reportForeignCluster(ctx context.Context, uid string) {
	metrics.RecordOrphanGC(metrics.OrphanSkippedForeignCluster)
	if c.reportedForeignUIDs[uid] {
		return
	}
	c.reportedForeignUIDs[uid] = true
	logf.FromContext(ctx).Info(
		"refusing servers labelled for this cluster's name but stamped with a different cluster UID; "+
			"either two clusters share a CLUSTER_NAME in one Hetzner project, or this cluster's "+
			"control plane was rebuilt and these servers predate it",
		"clusterName", c.clusterName, "ourClusterUID", c.clusterUID, "theirClusterUID", uid)
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
		if !c.managedByThisCluster(ctx, s) {
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

		// Counters reset when this process started, so a fresh leader could reach
		// the threshold having watched the cluster for only a few minutes. Require
		// that it has been sweeping for a full window first: after a restart or a
		// handover the reap is delayed, never skipped and never premature.
		if !c.watchedLongEnough() {
			continue
		}

		// Observe mode stops here, before anything is deleted and before anything
		// outside the cluster is written. That boundary is the point: an operator
		// evaluating this on an unaudited fleet is told it changes nothing, and
		// nothing is what it must change -- including labels on their servers.
		if c.mode == ModeObserve {
			log.Info("would garbage collect orphaned server (observe mode, nothing deleted)",
				"name", s.Name, "id", s.ID, "providerID", providerID)
			metrics.RecordOrphanGC(metrics.OrphanWouldReap)
			c.recordOnNode(node, corev1.EventTypeNormal, "WouldGarbageCollect",
				"Hetzner server %s (%s) has no NodeClaim and would be reclaimed; "+
					"instance garbage collection is in observe mode, so nothing was deleted",
				s.Name, providerID)
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
		// Normal, not Warning: reclaiming an orphan is this controller working, not
		// a fault. A cluster alerting on Warning events against Nodes would page
		// every time the sweep does its job.
		c.recordOnNode(node, corev1.EventTypeNormal, "GarbageCollected",
			"Hetzner server %s (%s) was reclaimed after %d consecutive sweeps with no NodeClaim; "+
				"this Node is being removed with it",
			s.Name, providerID, sweeps)

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

// recordOnNode publishes an event against the Node a server backed.
//
// Most orphans have no Node at all -- the crash-window case this package exists
// for never registered one -- so this is a best-effort supplement to the log and
// the metric, not the primary signal. The event also outlives the Node it hangs
// off only for the cluster's event TTL, and `kubectl describe node` cannot show
// it once the Node is deleted; `kubectl get events` can.
func (c *Controller) recordOnNode(node *corev1.Node, eventType, reason, note string, args ...any) {
	if c.recorder == nil || node == nil {
		return
	}
	c.recorder.Eventf(node, nil, eventType, reason, reason, note, args...)
}

// Register wires the controller into the manager.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	c.recorder = m.GetEventRecorder(c.Name())
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}

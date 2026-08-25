// Package pricehealth reports Karpenter-owned nodes that Karpenter cannot price.
//
// Karpenter prices a running node by looking its zone and capacity-type up
// against the offerings of its instance type. A miss returns zero, and a node
// priced at zero can never be consolidated: nothing is cheaper than zero, so
// every candidate replacement is rejected and the decision is logged as
// "Can't replace with a cheaper node" — which reads like a considered choice
// rather than a broken lookup.
//
// That silence is the whole problem. The cost metric keeps reporting correctly
// throughout, because it reads the NodeClaim's labels rather than the Node's, so
// spend looks right while rightsizing is dead. On this provider the usual cause
// is hcloud-cloud-controller-manager labelling nodes with the legacy datacenter
// (nbg1-dc3) while offerings are keyed on the location (nbg1); disabling that
// label leaves unregistered nodes with no zone at all, which fails the same way.
//
// This controller runs the same lookup Karpenter does and counts the misses, so
// the failure is a number on a dashboard instead of a plausible-looking log line.
package pricehealth

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	corev1 "k8s.io/api/core/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/metrics"
)

const (
	// resyncInterval is how often nodes are re-checked. This detects a
	// misconfiguration that persists until someone fixes it, so it does not need
	// the cadence of a controller that reacts to churn.
	resyncInterval = 5 * time.Minute

	// maxLoggedNodes caps how many unpriced nodes are named in one log line. The
	// headline cause -- the CCM's datacenter zone label -- is the *default*
	// configuration and misses on every node at once, so the common case is a
	// fleet-sized list, not a handful. An uncapped line is tens of kilobytes every
	// resync, which log pipelines truncate or drop, losing the examples that name
	// the cause. The count is exact regardless; the list is a sample.
	maxLoggedNodes = 10
)

// CatalogueProvider is the narrow API this controller needs: the instance types
// Karpenter itself would price a NodePool's nodes against.
//
// It has to be per-NodePool. Karpenter never prices against the whole catalogue:
// core builds its map from CloudProvider.GetInstanceTypes(ctx, nodePool), which
// this provider answers by filtering to the NodeClass's locations (a required,
// min-1 field) and by applying any NodeOverlay pricing. Asking for every
// instance type in every location would resolve offerings Karpenter has already
// filtered away -- reporting healthy in exactly the case this controller exists
// to catch.
type CatalogueProvider interface {
	GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*karpcp.InstanceType, error)
}

// Controller counts Karpenter-owned nodes whose price does not resolve.
type Controller struct {
	kubeClient client.Client
	catalogues CatalogueProvider
}

func NewController(kubeClient client.Client, catalogues CatalogueProvider) *Controller {
	return &Controller{kubeClient: kubeClient, catalogues: catalogues}
}

func (c *Controller) Name() string { return "pricehealth" }

// unpricedNode is one node Karpenter cannot price, with the labels that explain
// why. Reporting the values is the point: "zone=nbg1-dc3" names the cause where
// a bare count would only say something is wrong.
//
// The fields are exported with JSON tags because that is what makes them appear
// in a log line. The logger encodes arbitrary values by reflection and never
// consults fmt.Stringer, so unexported fields render as "nodes":[{}] -- a log
// that looks informative and carries nothing.
type unpricedNode struct {
	Node         string `json:"node"`
	InstanceType string `json:"instanceType"`
	Zone         string `json:"zone"`
	CapacityType string `json:"capacityType"`
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())
	log := logf.FromContext(ctx)

	unresolved, err := c.scan(ctx)
	if err != nil {
		// Leave the gauge alone -- zeroing it here would report every node as
		// healthy whenever the catalogue is unreachable. That is not enough on its
		// own, though: a gauge reads zero from process start, so "no broken nodes"
		// and "never managed to look" are the same number. The counter is what
		// separates them, and it is the reason this swallowed error stays visible.
		metrics.RecordPriceHealthScan(metrics.ResultError)
		log.Error(err, "checking whether nodes can be priced")
		return reconciler.Result{RequeueAfter: resyncInterval}, nil
	}

	// Always publish, including zero, so the gauge falls back to healthy once the
	// cause is fixed rather than staying latched at its worst value.
	metrics.SetNodesPriceUnresolved(len(unresolved))
	metrics.RecordPriceHealthScan(metrics.ResultSuccess)

	if len(unresolved) > 0 {
		sample := unresolved
		if len(sample) > maxLoggedNodes {
			sample = sample[:maxLoggedNodes]
		}
		log.Info("nodes cannot be priced, so Karpenter cannot consolidate them; "+
			"check that the node zone label matches a Hetzner location",
			"count", len(unresolved), "nodes", sample)
	}
	return reconciler.Result{RequeueAfter: resyncInterval}, nil
}

// scan returns the Karpenter-owned nodes whose price does not resolve, running
// the same offering lookup Karpenter itself performs.
func (c *Controller) scan(ctx context.Context) ([]unpricedNode, error) {
	byNodePool, err := c.catalogueByNodePool(ctx)
	if err != nil {
		return nil, err
	}

	// Only nodes Karpenter owns. Control-plane and hand-built nodes are not its to
	// price, and flagging them would bury the real signal. Selecting server-side
	// also keeps the cache from deep-copying every unmanaged Node in the cluster.
	nodes := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodes, client.HasLabels{karpv1.NodePoolLabelKey}); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	var unresolved []unpricedNode
	for i := range nodes.Items {
		n := &nodes.Items[i]
		// Until registration completes, Karpenter prices a node from its NodeClaim's
		// labels rather than the Node's (state.StateNode.Labels), and the Node's zone
		// label is written by that same registration. An unregistered node is
		// mid-handshake, not broken; counting it would leave the gauge permanently
		// non-zero on any cluster that is still scaling.
		if n.Labels[karpv1.NodeRegisteredLabelKey] != "true" {
			continue
		}
		// A NodePool this provider does not serve belongs to another cloud provider
		// in the same cluster; its nodes are not ours to price.
		catalogue, ok := byNodePool[n.Labels[karpv1.NodePoolLabelKey]]
		if !ok {
			continue
		}
		candidate := unpricedNode{
			Node:         n.Name,
			InstanceType: n.Labels[corev1.LabelInstanceTypeStable],
			Zone:         n.Labels[corev1.LabelTopologyZone],
			CapacityType: n.Labels[karpv1.CapacityTypeLabelKey],
		}
		it, ok := catalogue[candidate.InstanceType]
		if !ok {
			// An instance type missing from the catalogue prices at zero just the
			// same, so it belongs in the count even though the cause differs.
			unresolved = append(unresolved, candidate)
			continue
		}
		// A matching offering is not enough: consolidation is disabled by the price
		// being zero, not by the lookup missing. hcloud can return a server type
		// whose pricing payload does not parse, and the catalogue keeps the offering
		// with Price 0 -- so an offering that resolves to zero (or NaN, which core
		// also maps to zero) fails in exactly the same way as no offering at all.
		if price, ok := it.OfferingPrice(candidate.Zone, candidate.CapacityType); !ok || price <= 0 || math.IsNaN(price) {
			unresolved = append(unresolved, candidate)
		}
	}
	return unresolved, nil
}

// catalogueByNodePool returns, per NodePool this provider serves, the instance
// types Karpenter prices that pool's nodes against, indexed by name.
func (c *Controller) catalogueByNodePool(ctx context.Context) (map[string]map[string]*karpcp.InstanceType, error) {
	nodePools := &karpv1.NodePoolList{}
	if err := c.kubeClient.List(ctx, nodePools); err != nil {
		return nil, fmt.Errorf("listing nodepools: %w", err)
	}

	out := make(map[string]map[string]*karpcp.InstanceType, len(nodePools.Items))
	for i := range nodePools.Items {
		np := &nodePools.Items[i]
		// Skip pools belonging to another provider before asking for their instance
		// types: resolving a NodeClass we do not own would fail, and a failure has
		// to stay meaningful -- it is what stops a broken scan reporting healthy.
		if np.Spec.Template.Spec.NodeClassRef == nil || np.Spec.Template.Spec.NodeClassRef.Group != apiv1.Group {
			continue
		}
		instanceTypes, err := c.catalogues.GetInstanceTypes(ctx, np)
		if err != nil {
			return nil, fmt.Errorf("listing instance types for nodepool %s: %w", np.Name, err)
		}
		byName := make(map[string]*karpcp.InstanceType, len(instanceTypes))
		for _, it := range instanceTypes {
			byName[it.Name] = it
		}
		out[np.Name] = byName
	}
	return out, nil
}

// Register wires the controller into the manager.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}

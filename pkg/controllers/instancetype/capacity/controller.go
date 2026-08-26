// Package capacity records the memory capacity Hetzner servers actually report
// once they boot, so Karpenter can size nodes from measurements instead of an
// estimate.
//
// Hetzner advertises the memory a VM is allocated, not what the guest kernel
// ends up seeing. The difference is a few percent, it varies with the size of
// the machine, and it is invisible from the API -- so the instance type provider
// falls back to a single conservative fraction until a real node of that type
// reports its own figure. This controller supplies that figure.
//
// The AWS provider solves the same problem the same way, for the same reason.
package capacity

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
)

// CapacityRecorder is the instance type provider's seam for accepting a
// measurement. It reports whether the observation changed what the provider will
// use, so that only first discoveries and genuine drops are logged.
type CapacityRecorder interface {
	Record(serverType string, nodeClass *apiv1.HCloudNodeClass, observed resource.Quantity) bool
}

// Controller records capacity from registered nodes.
type Controller struct {
	kubeClient client.Client
	recorder   CapacityRecorder
}

func NewController(kubeClient client.Client, recorder CapacityRecorder) *Controller {
	return &Controller{kubeClient: kubeClient, recorder: recorder}
}

func (c *Controller) Name() string {
	return "instancetype.capacity"
}

// Reconcile reads one registered node's reported memory capacity and hands it to
// the instance type provider.
//
// Every path that cannot produce a trustworthy measurement returns without
// recording rather than returning an error. A node whose node pool or node class
// has been deleted is a race with teardown, not a fault, and requeueing it would
// spin against objects that are never coming back.
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	node := &corev1.Node{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, node); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting node %s: %w", req.Name, err)
	}

	// Only a registered node has finished joining and settled on the capacity it
	// will report for the rest of its life.
	if node.Labels[karpv1.NodeRegisteredLabelKey] != "true" {
		return reconcile.Result{}, nil
	}

	serverType := node.Labels[corev1.LabelInstanceTypeStable]
	if serverType == "" {
		return reconcile.Result{}, nil
	}

	observed := node.Status.Capacity[corev1.ResourceMemory]
	// Zero is the absence of a reading, not a very small machine. Recording it
	// would make Karpenter believe the type holds nothing and strand every pod
	// scheduled onto it.
	if observed.Sign() <= 0 {
		return reconcile.Result{}, nil
	}

	nodeClass, err := c.nodeClassFor(ctx, node)
	if err != nil || nodeClass == nil {
		return reconcile.Result{}, err
	}

	if c.recorder.Record(serverType, nodeClass, observed) {
		log.FromContext(ctx).WithValues(
			"serverType", serverType,
			"nodeClass", nodeClass.Name,
			"node", node.Name,
			"capacity", observed.String(),
		).Info("recorded server type memory capacity measured from a registered node")
	}
	return reconcile.Result{}, nil
}

// nodeClassFor resolves the node's node pool and, through it, the node class the
// server was built from. It returns nil without error when the node is not ours
// or when either object has already been deleted.
func (c *Controller) nodeClassFor(ctx context.Context, node *corev1.Node) (*apiv1.HCloudNodeClass, error) {
	poolName := node.Labels[karpv1.NodePoolLabelKey]
	if poolName == "" {
		return nil, nil
	}

	nodePool := &karpv1.NodePool{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: poolName}, nodePool); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting nodepool %s: %w", poolName, err)
	}

	ref := nodePool.Spec.Template.Spec.NodeClassRef
	// A node pool belonging to another provider says nothing about how Hetzner
	// sizes a server, even if the label happens to name a type we know.
	if ref == nil || ref.Group != apiv1.Group || ref.Kind != "HCloudNodeClass" {
		return nil, nil
	}

	nodeClass := &apiv1.HCloudNodeClass{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: ref.Name}, nodeClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting hcloudnodeclass %s: %w", ref.Name, err)
	}
	return nodeClass, nil
}

// Register wires the controller to nodes becoming registered.
//
// A node's reported capacity does not change after it joins, so there is nothing
// to learn from watching it afterwards. The predicates narrow the watch to the
// transition into registration, plus already-registered nodes at startup so a
// fresh process rebuilds its cache from the running fleet rather than waiting for
// the next launch.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		For(&corev1.Node{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return e.Object.GetLabels()[karpv1.NodeRegisteredLabelKey] == "true"
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				// Only the moment registration is gained; a node that was already
				// registered has nothing new to say.
				if e.ObjectOld.GetLabels()[karpv1.NodeRegisteredLabelKey] != "" {
					return false
				}
				return e.ObjectNew.GetLabels()[karpv1.NodeRegisteredLabelKey] == "true"
			},
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
		})).
		Named(c.Name()).
		// One worker: the work is a map write behind a mutex, and serialising it
		// keeps contention off the provider's read path.
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(c)
}

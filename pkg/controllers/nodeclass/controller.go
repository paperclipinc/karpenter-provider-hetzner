package nodeclass

import (
	"context"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"k8s.io/apimachinery/pkg/api/equality"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
)

const resyncInterval = 5 * time.Minute

// NetworkGetter is the narrow hcloud networks API the controller needs.
type NetworkGetter interface {
	GetByID(ctx context.Context, id int64) (*hcloud.Network, *hcloud.Response, error)
}

// Controller reconciles HCloudNodeClass status.
type Controller struct {
	kubeClient client.Client
	networks   NetworkGetter
	images     *imagefamily.Provider
}

func NewController(kubeClient client.Client, networks NetworkGetter, images *imagefamily.Provider) *Controller {
	return &Controller{kubeClient: kubeClient, networks: networks, images: images}
}

func (c *Controller) Name() string { return "nodeclass.status" }

func (c *Controller) Reconcile(ctx context.Context, nc *v1alpha1.HCloudNodeClass) (reconcile.Result, error) {
	stored := nc.DeepCopy()

	// Network validation.
	net, _, err := c.networks.GetByID(ctx, nc.Spec.NetworkID)
	switch {
	case err != nil:
		nc.StatusConditions().SetUnknownWithReason(v1alpha1.ConditionTypeNetworkReady, "NetworkCheckFailed", err.Error())
	case net == nil:
		nc.StatusConditions().SetFalse(v1alpha1.ConditionTypeNetworkReady, "NetworkNotFound", "configured networkID does not exist")
	default:
		nc.StatusConditions().SetTrue(v1alpha1.ConditionTypeNetworkReady)
	}

	// Image resolution for both architectures.
	resolved, ierr := c.resolveImages(ctx, nc)
	if ierr != nil {
		nc.StatusConditions().SetFalse(v1alpha1.ConditionTypeImagesReady, "ImageResolutionFailed", ierr.Error())
	} else {
		nc.Status.ResolvedImages = resolved
		nc.StatusConditions().SetTrue(v1alpha1.ConditionTypeImagesReady)
	}

	if !equality.Semantic.DeepEqual(stored, nc) {
		if err := c.kubeClient.Status().Update(ctx, nc); err != nil {
			return reconcile.Result{}, err
		}
	}
	// Requeue periodically so the Ready condition re-reflects reality (e.g. a
	// network deleted out-of-band, or a newer image published).
	return reconcile.Result{RequeueAfter: resyncInterval}, nil
}

func (c *Controller) resolveImages(ctx context.Context, nc *v1alpha1.HCloudNodeClass) ([]v1alpha1.ResolvedImage, error) {
	// hcloud image IDs are global (not per-location), so resolve one image per
	// architecture. Resolution is all-or-nothing: if any supported architecture
	// fails to resolve, the whole NodeClass is marked not-ready rather than
	// publishing a partial set.
	var out []v1alpha1.ResolvedImage
	for _, arch := range []hcloud.Architecture{hcloud.ArchitectureX86, hcloud.ArchitectureARM} {
		img, err := c.images.Resolve(ctx, nc.Spec.ImageSelector, arch)
		if err != nil {
			return nil, err
		}
		out = append(out, v1alpha1.ResolvedImage{Architecture: string(arch), ImageID: img.ID})
	}
	return out, nil
}

// Register wires the controller into the manager.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		For(&v1alpha1.HCloudNodeClass{}).
		Named(c.Name()).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

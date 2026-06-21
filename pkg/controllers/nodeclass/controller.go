package nodeclass

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
)

const resyncInterval = 5 * time.Minute

// NetworkGetter is the narrow hcloud networks API the controller needs.
type NetworkGetter interface {
	GetByID(ctx context.Context, id int64) (*hcloud.Network, *hcloud.Response, error)
}

// FirewallGetter is the narrow hcloud firewalls API the controller needs.
type FirewallGetter interface {
	GetByID(ctx context.Context, id int64) (*hcloud.Firewall, *hcloud.Response, error)
}

// SSHKeyGetter is the narrow hcloud SSH keys API the controller needs.
type SSHKeyGetter interface {
	GetByID(ctx context.Context, id int64) (*hcloud.SSHKey, *hcloud.Response, error)
}

// Controller reconciles HCloudNodeClass status.
type Controller struct {
	kubeClient client.Client
	networks   NetworkGetter
	firewalls  FirewallGetter
	sshKeys    SSHKeyGetter
	images     *imagefamily.Provider
	recorder   events.EventRecorder
}

func NewController(kubeClient client.Client, networks NetworkGetter, firewalls FirewallGetter, sshKeys SSHKeyGetter, images *imagefamily.Provider) *Controller {
	return &Controller{kubeClient: kubeClient, networks: networks, firewalls: firewalls, sshKeys: sshKeys, images: images}
}

// warnf emits a Warning event on the HCloudNodeClass when a recorder is
// available, and is a no-op otherwise (the controller still sets conditions).
func (c *Controller) warnf(nc *apiv1.HCloudNodeClass, reason, action, format string, args ...interface{}) {
	if c.recorder == nil {
		return
	}
	c.recorder.Eventf(nc, nil, corev1.EventTypeWarning, reason, action, format, args...)
}

func (c *Controller) Name() string { return "nodeclass.status" }

func (c *Controller) Reconcile(ctx context.Context, nc *apiv1.HCloudNodeClass) (reconcile.Result, error) {
	stored := nc.DeepCopy()

	// Network validation.
	net, _, err := c.networks.GetByID(ctx, nc.Spec.NetworkID)
	switch {
	case err != nil:
		nc.StatusConditions().SetUnknownWithReason(apiv1.ConditionTypeNetworkReady, "NetworkCheckFailed", err.Error())
		c.warnf(nc, "NetworkCheckFailed", "ValidateNetwork", "network check failed: %v", err)
	case net == nil:
		nc.StatusConditions().SetFalse(apiv1.ConditionTypeNetworkReady, "NetworkNotFound", "configured networkID does not exist")
		c.warnf(nc, "NetworkNotFound", "ValidateNetwork", "networkID %d does not exist", nc.Spec.NetworkID)
	default:
		nc.StatusConditions().SetTrue(apiv1.ConditionTypeNetworkReady)
	}

	// Validate referenced firewalls and SSH keys exist.
	if reason, msg, unknown, ok := c.validateResources(ctx, nc); ok {
		nc.StatusConditions().SetTrue(apiv1.ConditionTypeResourcesReady)
	} else if unknown {
		nc.StatusConditions().SetUnknownWithReason(apiv1.ConditionTypeResourcesReady, reason, msg)
		c.warnf(nc, reason, "ValidateResources", "%s", msg)
	} else {
		nc.StatusConditions().SetFalse(apiv1.ConditionTypeResourcesReady, reason, msg)
		c.warnf(nc, reason, "ValidateResources", "%s", msg)
	}

	// Validate the userData secret ref (if set).
	if reason, msg, unknown, ok := c.validateUserData(ctx, nc); ok {
		nc.StatusConditions().SetTrue(apiv1.ConditionTypeUserDataReady)
	} else if unknown {
		nc.StatusConditions().SetUnknownWithReason(apiv1.ConditionTypeUserDataReady, reason, msg)
		c.warnf(nc, reason, "ValidateUserData", "%s", msg)
	} else {
		nc.StatusConditions().SetFalse(apiv1.ConditionTypeUserDataReady, reason, msg)
		c.warnf(nc, reason, "ValidateUserData", "%s", msg)
	}

	// Image resolution for both architectures.
	resolved, ierr := c.resolveImages(ctx, nc)
	if ierr != nil {
		nc.StatusConditions().SetFalse(apiv1.ConditionTypeImagesReady, "ImageResolutionFailed", ierr.Error())
		c.warnf(nc, "ImageResolutionFailed", "ResolveImages", "image resolution failed: %v", ierr)
	} else {
		nc.Status.ResolvedImages = resolved
		nc.StatusConditions().SetTrue(apiv1.ConditionTypeImagesReady)
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

// validateResources checks every referenced firewall and SSH key exists.
// Returns ok=true when all exist; unknown=true on a transient API error.
func (c *Controller) validateResources(ctx context.Context, nc *apiv1.HCloudNodeClass) (reason, msg string, unknown, ok bool) {
	for _, id := range nc.Spec.FirewallIDs {
		fw, _, err := c.firewalls.GetByID(ctx, id)
		if err != nil {
			return "FirewallCheckFailed", err.Error(), true, false
		}
		if fw == nil {
			return "FirewallNotFound", fmt.Sprintf("firewall %d does not exist", id), false, false
		}
	}
	for _, id := range nc.Spec.SSHKeyIDs {
		key, _, err := c.sshKeys.GetByID(ctx, id)
		if err != nil {
			return "SSHKeyCheckFailed", err.Error(), true, false
		}
		if key == nil {
			return "SSHKeyNotFound", fmt.Sprintf("ssh key %d does not exist", id), false, false
		}
	}
	return "", "", false, true
}

// validateUserData checks that the referenced userData Secret and key exist and
// are non-empty. Returns ok=true when there is nothing to validate or it
// resolves successfully; unknown=true on a transient API error.
func (c *Controller) validateUserData(ctx context.Context, nc *apiv1.HCloudNodeClass) (reason, msg string, unknown, ok bool) {
	ref := nc.Spec.UserDataSecretRef
	if ref == nil {
		return "", "", false, true
	}
	secret := &corev1.Secret{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "UserDataSecretNotFound", fmt.Sprintf("secret %s/%s not found", ref.Namespace, ref.Name), false, false
		}
		return "UserDataCheckFailed", err.Error(), true, false
	}
	if v, present := secret.Data[ref.Key]; !present || len(v) == 0 {
		return "UserDataKeyMissing", fmt.Sprintf("secret %s/%s has no non-empty key %q", ref.Namespace, ref.Name, ref.Key), false, false
	}
	return "", "", false, true
}

func (c *Controller) resolveImages(ctx context.Context, nc *apiv1.HCloudNodeClass) ([]apiv1.ResolvedImage, error) {
	// hcloud image IDs are global (not per-location), so resolve one image per
	// architecture. Resolve each architecture independently: many clusters only
	// have an image for a single arch (e.g. an all-amd64 cluster has no arm64
	// Talos snapshot), and a NodeClass is usable as long as at least one arch
	// resolves. Only fail when NO architecture resolves.
	var out []apiv1.ResolvedImage
	var errs []error
	for _, arch := range []hcloud.Architecture{hcloud.ArchitectureX86, hcloud.ArchitectureARM} {
		img, err := c.images.Resolve(ctx, nc.Spec.ImageSelector, arch)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", arch, err))
			continue
		}
		out = append(out, apiv1.ResolvedImage{Architecture: string(arch), ImageID: img.ID})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no image resolved for any architecture: %w", errors.Join(errs...))
	}
	return out, nil
}

// Register wires the controller into the manager.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	c.recorder = m.GetEventRecorder(c.Name())
	return controllerruntime.NewControllerManagedBy(m).
		For(&apiv1.HCloudNodeClass{}).
		Named(c.Name()).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

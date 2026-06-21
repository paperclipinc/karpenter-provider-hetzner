package cloudprovider

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/metrics"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instancetype"
)

const providerName = "hetzner"

// Drift reasons for HCloud-specific drift detection.
const (
	DriftImage      karpcp.DriftReason = "ImageDrift"
	DriftNetwork    karpcp.DriftReason = "NetworkDrift"
	DriftFirewall   karpcp.DriftReason = "FirewallDrift"
	DriftServerType karpcp.DriftReason = "ServerTypeDrift"
	DriftLocation   karpcp.DriftReason = "LocationDrift"
	DriftLabels     karpcp.DriftReason = "LabelsDrift"
)

// CloudProvider implements the Karpenter CloudProvider interface for Hetzner Cloud.
type CloudProvider struct {
	kubeClient       client.Client
	instanceProvider *instance.Provider
	typeProvider     *instancetype.Provider
	imageProvider    *imagefamily.Provider
}

// NewCloudProvider creates a new CloudProvider.
func NewCloudProvider(
	kubeClient client.Client,
	instanceProvider *instance.Provider,
	typeProvider *instancetype.Provider,
	imageProvider *imagefamily.Provider,
) *CloudProvider {
	return &CloudProvider{
		kubeClient:       kubeClient,
		instanceProvider: instanceProvider,
		typeProvider:     typeProvider,
		imageProvider:    imageProvider,
	}
}

// Name returns the cloud provider name.
func (cp *CloudProvider) Name() string {
	return providerName
}

// GetSupportedNodeClasses returns the supported node class types.
func (cp *CloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&apiv1.HCloudNodeClass{}}
}

// RepairPolicies returns the repair policies for unhealthy nodes.
func (cp *CloudProvider) RepairPolicies() []karpcp.RepairPolicy {
	return []karpcp.RepairPolicy{
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionFalse,
			TolerationDuration: 5 * time.Minute,
		},
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionUnknown,
			TolerationDuration: 5 * time.Minute,
		},
	}
}

// Create provisions a new Hetzner server for the given NodeClaim.
func (cp *CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	log := logf.FromContext(ctx)
	nodeClass, err := cp.resolveNodeClass(ctx, nodeClaim.Spec.NodeClassRef)
	if err != nil {
		return nil, fmt.Errorf("resolving node class: %w", err)
	}

	// Get instance types for the node class locations.
	instanceTypes, err := cp.typeProvider.List(ctx, nodeClass.Spec.Locations)
	if err != nil {
		return nil, fmt.Errorf("listing instance types: %w", err)
	}

	// Filter instance types by NodeClaim requirements.
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	var selected *karpcp.InstanceType
	for _, it := range instanceTypes {
		if !reqs.IsCompatible(it.Requirements, scheduling.AllowUndefinedWellKnownLabels) {
			continue
		}
		if !it.Offerings.Available().HasCompatible(reqs) {
			continue
		}
		selected = it
		break
	}
	if selected == nil {
		return nil, karpcp.NewInsufficientCapacityError(fmt.Errorf("no instance type satisfies requirements for nodeclaim %s", nodeClaim.Name))
	}

	// Determine architecture from the selected instance type.
	arch := selected.Requirements.Get(corev1.LabelArchStable).Any()
	hcloudArch := hcloud.ArchitectureX86
	if arch == "arm64" {
		hcloudArch = hcloud.ArchitectureARM
	}
	log.Info("selected instance type", "instanceType", selected.Name, "arch", arch)

	// Resolve OS image.
	image, err := cp.imageProvider.Resolve(ctx, nodeClass.Spec.ImageSelector, hcloudArch)
	if err != nil {
		return nil, fmt.Errorf("resolving image: %w", err)
	}
	// Guard: never provision a server whose image architecture diverges from the
	// architecture the NodeClaim requires. Fail loudly instead of booting a
	// wrong-arch node that would silently fail to run scheduled workloads.
	if image.Architecture != hcloudArch {
		return nil, fmt.Errorf("resolved image %d has arch %q but nodeclaim requires %q", image.ID, image.Architecture, hcloudArch)
	}

	// Pick the first compatible offering to determine the location.
	var location string
	compatibleOfferings := selected.Offerings.Available().Compatible(reqs)
	if len(compatibleOfferings) > 0 {
		location = compatibleOfferings[0].Requirements.Get(corev1.LabelTopologyZone).Any()
	}
	if location == "" && len(nodeClass.Spec.Locations) > 0 {
		location = nodeClass.Spec.Locations[0]
	}

	// Collect node pool name from NodeClaim labels (may be empty).
	nodePoolName := nodeClaim.Labels[karpv1.NodePoolLabelKey]

	// Resolve userData, preferring the Secret reference over inline userData.
	userData, err := cp.resolveUserData(ctx, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("resolving userData: %w", err)
	}

	// Create the server.
	server, err := cp.instanceProvider.Create(ctx, instance.CreateOpts{
		Name:                   nodeClaim.Name,
		ServerType:             selected.Name,
		Location:               location,
		Image:                  image,
		NetworkID:              nodeClass.Spec.NetworkID,
		FirewallIDs:            nodeClass.Spec.FirewallIDs,
		SSHKeyIDs:              nodeClass.Spec.SSHKeyIDs,
		Labels:                 nodeClass.Spec.Labels,
		UserData:               userData,
		NodeClaim:              nodeClaim.Name,
		NodePool:               nodePoolName,
		PlacementGroupStrategy: nodeClass.Spec.PlacementGroupStrategy,
		EnablePublicIPv4:       nodeClass.Spec.PublicIPv4Enabled(),
		EnablePublicIPv6:       nodeClass.Spec.PublicIPv6Enabled(),
	})
	if err != nil {
		if karpcp.IsInsufficientCapacityError(err) {
			cp.typeProvider.MarkUnavailable(selected.Name, location)
		}
		return nil, fmt.Errorf("creating server: %w", err)
	}

	// Build labels from instance type requirements.
	labels := map[string]string{}
	for key, req := range selected.Requirements {
		if req.Operator() == corev1.NodeSelectorOpIn {
			labels[key] = req.Values()[0]
		}
	}
	// Overlay offering-specific labels (zone, capacity-type).
	if len(compatibleOfferings) > 0 {
		for _, req := range compatibleOfferings[0].Requirements {
			labels[req.Key] = req.Any()
		}
	}
	// Merge existing NodeClaim labels.
	for k, v := range nodeClaim.Labels {
		labels[k] = v
	}

	// Build the hydrated NodeClaim.
	created := nodeClaim.DeepCopy()
	created.Labels = labels
	created.Status.ProviderID = instance.FormatProviderID(server.ID)
	created.Status.Capacity = selected.Capacity
	created.Status.Allocatable = selected.Allocatable()
	if server.Image != nil {
		created.Status.ImageID = strconv.FormatInt(server.Image.ID, 10)
	}

	return created, nil
}

// Delete terminates the server backing the given NodeClaim.
func (cp *CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	return cp.instanceProvider.Delete(ctx, nodeClaim.Status.ProviderID)
}

// Get retrieves the NodeClaim corresponding to the given provider ID.
func (cp *CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	server, err := cp.instanceProvider.Get(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if server == nil {
		return nil, karpcp.NewNodeClaimNotFoundError(fmt.Errorf("server with provider ID %q not found", providerID))
	}
	return serverToNodeClaim(server), nil
}

// List retrieves all NodeClaims managed by this provider.
func (cp *CloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	servers, err := cp.instanceProvider.List(ctx)
	if err != nil {
		return nil, err
	}
	nodeClaims := make([]*karpv1.NodeClaim, 0, len(servers))
	for _, s := range servers {
		nodeClaims = append(nodeClaims, serverToNodeClaim(s))
	}
	return nodeClaims, nil
}

// GetInstanceTypes returns the available instance types for the given NodePool.
func (cp *CloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*karpcp.InstanceType, error) {
	if nodePool == nil {
		return cp.typeProvider.List(ctx, nil)
	}

	nodeClass, err := cp.resolveNodeClass(ctx, nodePool.Spec.Template.Spec.NodeClassRef)
	if err != nil {
		return nil, fmt.Errorf("resolving node class for node pool %s: %w", nodePool.Name, err)
	}

	return cp.typeProvider.List(ctx, nodeClass.Spec.Locations)
}

// IsDrifted determines whether the given NodeClaim has drifted from its desired state.
func (cp *CloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (karpcp.DriftReason, error) {
	log := logf.FromContext(ctx)
	nodeClass, err := cp.resolveNodeClass(ctx, nodeClaim.Spec.NodeClassRef)
	if err != nil {
		return "", fmt.Errorf("resolving node class: %w", err)
	}

	server, err := cp.instanceProvider.Get(ctx, nodeClaim.Status.ProviderID)
	if err != nil {
		return "", fmt.Errorf("getting server: %w", err)
	}
	if server == nil {
		return "", nil
	}

	// logDrift emits a structured INFO log, records a Prometheus counter, and
	// returns the reason so callers can write: return logDrift(reason, id), nil
	logDrift := func(reason karpcp.DriftReason, providerID string) karpcp.DriftReason {
		log.Info("drift detected", "reason", string(reason), "providerID", providerID)
		metrics.RecordDrift(string(reason))
		return reason
	}

	// Check image drift: compare the resolved image ID recorded in NodeClaim status against
	// the current server image.
	if nodeClaim.Status.ImageID != "" && server.Image != nil {
		currentImageID := strconv.FormatInt(server.Image.ID, 10)
		if nodeClaim.Status.ImageID != currentImageID {
			return logDrift(DriftImage, nodeClaim.Status.ProviderID), nil
		}
	}

	// Check network drift: ensure the server is attached to the expected network.
	if nodeClass.Spec.NetworkID > 0 {
		attached := false
		for _, pn := range server.PrivateNet {
			if pn.Network != nil && pn.Network.ID == nodeClass.Spec.NetworkID {
				attached = true
				break
			}
		}
		if !attached {
			return logDrift(DriftNetwork, nodeClaim.Status.ProviderID), nil
		}
	}

	// Firewall drift: every NodeClass firewall must be attached to the server.
	// This is a subset check only; firewalls attached beyond the NodeClass spec
	// (e.g. applied out-of-band) are permitted and do not count as drift.
	if len(nodeClass.Spec.FirewallIDs) > 0 {
		attached := make(map[int64]bool, len(server.PublicNet.Firewalls))
		for _, fw := range server.PublicNet.Firewalls {
			if fw == nil {
				continue
			}
			attached[fw.Firewall.ID] = true
		}
		for _, want := range nodeClass.Spec.FirewallIDs {
			if !attached[want] {
				return logDrift(DriftFirewall, nodeClaim.Status.ProviderID), nil
			}
		}
	}

	// Server-type drift: the running server type must match the type recorded on
	// the NodeClaim's instance-type label. An absent label (e.g. a NodeClaim not
	// created by this provider) intentionally skips the check rather than
	// reporting false drift.
	if want := nodeClaim.Labels[corev1.LabelInstanceTypeStable]; want != "" &&
		server.ServerType != nil && server.ServerType.Name != want {
		return logDrift(DriftServerType, nodeClaim.Status.ProviderID), nil
	}

	// Location drift: the server's location must be in the NodeClass Locations
	// list. Guards a nil Location pointer defensively (e.g. mid-provisioning).
	if len(nodeClass.Spec.Locations) > 0 && server.Location != nil {
		serverLocation := server.Location.Name
		inAllowed := false
		for _, loc := range nodeClass.Spec.Locations {
			if loc == serverLocation {
				inAllowed = true
				break
			}
		}
		if !inAllowed {
			return logDrift(DriftLocation, nodeClaim.Status.ProviderID), nil
		}
	}

	// Label drift: every key/value in NodeClass spec.labels must be present and
	// matching on the server. Extra labels on the server (karpenter management,
	// offering labels, etc.) are permitted and do not count as drift. Empty/nil
	// spec labels means nothing is required — skip the check.
	if len(nodeClass.Spec.Labels) > 0 {
		for k, want := range nodeClass.Spec.Labels {
			if got, ok := server.Labels[k]; !ok || got != want {
				return logDrift(DriftLabels, nodeClaim.Status.ProviderID), nil
			}
		}
	}

	// SSH-key and user-data drift are intentionally not checked: Hetzner does not
	// reliably expose applied SSH keys or user-data after create, so a comparison
	// would produce false positives. They are omitted rather than faked.

	return "", nil
}

// resolveUserData returns the userData for a NodeClass, reading it from the
// referenced Secret when UserDataSecretRef is set (takes precedence over inline UserData).
func (cp *CloudProvider) resolveUserData(ctx context.Context, nc *apiv1.HCloudNodeClass) (string, error) {
	ref := nc.Spec.UserDataSecretRef
	if ref == nil {
		return nc.Spec.UserData, nil
	}
	secret := &corev1.Secret{}
	if err := cp.kubeClient.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		return "", fmt.Errorf("reading userData secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	data, ok := secret.Data[ref.Key]
	if !ok || len(data) == 0 {
		return "", fmt.Errorf("userData secret %s/%s has no non-empty key %q", ref.Namespace, ref.Name, ref.Key)
	}
	return string(data), nil
}

// resolveNodeClass fetches the HCloudNodeClass referenced by ref.
func (cp *CloudProvider) resolveNodeClass(ctx context.Context, ref *karpv1.NodeClassReference) (*apiv1.HCloudNodeClass, error) {
	if ref == nil {
		return nil, fmt.Errorf("nodeClassRef is nil")
	}
	nodeClass := &apiv1.HCloudNodeClass{}
	if err := cp.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, nodeClass); err != nil {
		return nil, fmt.Errorf("getting HCloudNodeClass %q: %w", ref.Name, err)
	}
	return nodeClass, nil
}

// serverToNodeClaim maps an hcloud.Server to a Karpenter NodeClaim.
func serverToNodeClaim(server *hcloud.Server) *karpv1.NodeClaim {
	nc := &karpv1.NodeClaim{}
	nc.Status.ProviderID = instance.FormatProviderID(server.ID)

	if server.Image != nil {
		nc.Status.ImageID = strconv.FormatInt(server.Image.ID, 10)
	}

	// Build capacity from ServerType if available.
	if server.ServerType != nil {
		st := server.ServerType
		memBytes := int64(float64(st.Memory) * 1024 * 1024 * 1024)
		diskBytes := int64(st.Disk) * 1024 * 1024 * 1024
		nc.Status.Capacity = corev1.ResourceList{
			corev1.ResourceCPU:              *resource.NewMilliQuantity(int64(st.Cores)*1000, resource.DecimalSI),
			corev1.ResourceMemory:           *resource.NewQuantity(memBytes, resource.BinarySI),
			corev1.ResourceEphemeralStorage: *resource.NewQuantity(diskBytes, resource.BinarySI),
			corev1.ResourcePods:             *resource.NewQuantity(110, resource.DecimalSI),
		}
	}

	// Propagate server labels to NodeClaim.
	if len(server.Labels) > 0 {
		nc.Labels = make(map[string]string, len(server.Labels))
		for k, v := range server.Labels {
			nc.Labels[k] = v
		}
	}

	return nc
}

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
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instancetype"
)

const providerName = "hetzner"

// Drift reasons for HCloud-specific drift detection.
const (
	DriftImage    karpcp.DriftReason = "ImageDrift"
	DriftNetwork  karpcp.DriftReason = "NetworkDrift"
	DriftFirewall karpcp.DriftReason = "FirewallDrift"
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
	return []status.Object{&v1alpha1.HCloudNodeClass{}}
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

	// Resolve OS image.
	image, err := cp.imageProvider.Resolve(ctx, nodeClass.Spec.ImageSelector, hcloudArch)
	if err != nil {
		return nil, fmt.Errorf("resolving image: %w", err)
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

	// Resolve userData: BootstrapRef (Secret) takes precedence over inline UserData.
	userData, err := resolveUserData(ctx, cp.kubeClient, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("resolving user data: %w", err)
	}

	// Create the server.
	server, err := cp.instanceProvider.Create(ctx, instance.CreateOpts{
		Name:        nodeClaim.Name,
		ServerType:  selected.Name,
		Location:    location,
		Image:       image,
		NetworkID:   nodeClass.Spec.NetworkID,
		FirewallIDs: nodeClass.Spec.FirewallIDs,
		SSHKeyIDs:   nodeClass.Spec.SSHKeyIDs,
		Labels:      nodeClass.Spec.Labels,
		UserData:    userData,
		NodeClaim:   nodeClaim.Name,
		NodePool:    nodePoolName,
	})
	if err != nil {
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

	// Check image drift: compare the resolved image ID recorded in NodeClaim status against
	// the current server image.
	if nodeClaim.Status.ImageID != "" && server.Image != nil {
		currentImageID := strconv.FormatInt(server.Image.ID, 10)
		if nodeClaim.Status.ImageID != currentImageID {
			return DriftImage, nil
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
			return DriftNetwork, nil
		}
	}

	return "", nil
}

// resolveNodeClass fetches the HCloudNodeClass referenced by ref.
func (cp *CloudProvider) resolveNodeClass(ctx context.Context, ref *karpv1.NodeClassReference) (*v1alpha1.HCloudNodeClass, error) {
	if ref == nil {
		return nil, fmt.Errorf("nodeClassRef is nil")
	}
	nodeClass := &v1alpha1.HCloudNodeClass{}
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

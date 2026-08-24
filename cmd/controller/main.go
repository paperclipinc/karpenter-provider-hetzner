package main

import (
	"sigs.k8s.io/controller-runtime/pkg/log"

	// Register karpenter core types into the default k8s scheme.
	_ "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/awslabs/operatorpkg/controller"

	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	"sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator"

	// Register our HCloudNodeClass v1 types.
	_ "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"

	hetznercp "github.com/paperclipinc/karpenter-provider-hetzner/pkg/cloudprovider"
	instancegc "github.com/paperclipinc/karpenter-provider-hetzner/pkg/controllers/instance/garbagecollection"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/controllers/nodeclass"
	hetznerop "github.com/paperclipinc/karpenter-provider-hetzner/pkg/operator"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instancetype"
)

func main() {
	ctx, op := operator.NewOperator()

	// Create the Hetzner Cloud API client.
	hcloudClient, err := hetznerop.NewHCloudClient()
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to create Hetzner Cloud client")
		return
	}

	cfg, err := hetznerop.LoadConfig()
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to load config")
		return
	}

	// Create the three providers.
	// Identify this cluster independently of its operator-chosen name. CLUSTER_NAME
	// is not guaranteed unique, and two clusters sharing one in a single Hetzner
	// project would otherwise each treat the other's servers as its own -- which
	// now means deleting them. The kube-system UID is unique per cluster and
	// stable for its lifetime. Read through the API reader because the manager's
	// cache is not running yet.
	clusterUID, err := hetznerop.ClusterUID(ctx, op.GetAPIReader())
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to read the cluster UID")
		return
	}

	instanceProvider := instance.NewProviderWithPlacementGroups(&hcloudClient.Server, &hcloudClient.PlacementGroup, cfg.ClusterName, clusterUID, &hcloudClient.Action)
	typeProvider := instancetype.NewProvider(&hcloudClient.ServerType)
	imageProvider := imagefamily.NewProvider(&hcloudClient.Image)

	// Create the cloud provider.
	baseCloudProvider := hetznercp.NewCloudProvider(
		op.GetClient(),
		instanceProvider,
		typeProvider,
		imageProvider,
	)

	// Wrap with the overlay decorator (required by NewControllers).
	cloudProvider := overlay.Decorate(baseCloudProvider, op.GetClient(), op.InstanceTypeStore)

	// Create cluster state.
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cloudProvider)

	// Our NodeClass status controller (network + image validation, Ready).
	nodeClassController := nodeclass.NewController(op.GetClient(), &hcloudClient.Network, &hcloudClient.Firewall, &hcloudClient.SSHKey, imageProvider)

	// Reap servers whose NodeClaim is gone. Karpenter core only garbage collects
	// the opposite direction (NodeClaims with no instance), so without this an
	// orphaned server runs and bills indefinitely.
	providerControllers := []controller.Controller{nodeClassController}
	if cfg.DisableInstanceGarbageCollection {
		log.FromContext(ctx).Info("instance garbage collection is disabled; " +
			"servers whose NodeClaim is gone will not be reclaimed")
	} else {
		// Log the enabled case too. DISABLE_INSTANCE_GARBAGE_COLLECTION leaves the
		// sweep running on any value it does not recognise, so a line for one state
		// only would let a typo'd pause ("disabled", "True!") look identical to a
		// pause that took effect -- on the one flag whose job is protecting a fleet
		// during maintenance that removes NodeClaims wholesale.
		log.FromContext(ctx).Info("instance garbage collection is enabled; " +
			"servers whose NodeClaim is gone will be reclaimed")
		providerControllers = append(providerControllers,
			instancegc.NewController(op.GetClient(), instanceProvider, cfg.ClusterName, clusterUID))
	}

	// Wire and start all controllers.
	op.WithControllers(ctx, append(
		controllers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.EventRecorder,
			cloudProvider,
			baseCloudProvider,
			clusterState,
			op.InstanceTypeStore,
		),
		providerControllers...,
	)...).Start(ctx)
}

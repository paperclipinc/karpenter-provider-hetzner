package main

import (
	"sigs.k8s.io/controller-runtime/pkg/log"

	// Register karpenter core types into the default k8s scheme.
	_ "sigs.k8s.io/karpenter/pkg/apis/v1"

	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	"sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator"

	// Register our v1alpha1 types.
	_ "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"

	hetznercp "github.com/paperclipinc/karpenter-provider-hetzner/pkg/cloudprovider"
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
	instanceProvider := instance.NewProviderWithWaiter(&hcloudClient.Server, cfg.ClusterName, &hcloudClient.Action)
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
		nodeClassController,
	)...).Start(ctx)
}

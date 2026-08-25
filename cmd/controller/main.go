package main

import (
	"context"
	"time"

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

// clusterUIDTimeout bounds the one API-server read the operator makes before the
// manager -- and therefore the health probes -- are running.
const clusterUIDTimeout = 30 * time.Second

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

	// Identify this cluster independently of its operator-chosen name. CLUSTER_NAME
	// is not guaranteed unique, and two clusters sharing one in a single Hetzner
	// project would otherwise each treat the other's servers as its own -- which
	// now means deleting them. The kube-system UID is unique per cluster and
	// stable for its lifetime. Read through the API reader because the manager's
	// cache is not running yet.
	//
	// Bound it: this runs before the manager starts, so the health probes are not
	// listening yet and an apiserver that accepts the connection but never answers
	// would hang the process where nothing can observe it. The rest config sets no
	// per-request timeout of its own.
	uidCtx, cancelUID := context.WithTimeout(ctx, clusterUIDTimeout)
	clusterUID, err := hetznerop.ClusterUID(uidCtx, op.GetAPIReader())
	cancelUID()
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to read the cluster UID")
		return
	}

	// Create the three providers.
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

	providerControllers := []controller.Controller{nodeClassController}
	// Reap servers whose NodeClaim is gone. Karpenter core only garbage collects
	// the opposite direction (NodeClaims with no instance), so without this an
	// orphaned server runs and bills indefinitely.
	//
	// Every mode is logged, not just the unusual ones: this controller deletes
	// machines, so which mode took effect must be answerable from the operator's
	// own startup logs rather than inferred from a values file.
	//
	// Every mode is named explicitly and `default` refuses to start. Routing the
	// unknown case to the deleting branch would re-open, one layer down, exactly
	// the hole parseGCMode exists to close: GCMode's zero value is "", not
	// "enabled", so any Config built without LoadConfig -- or any mode added to
	// the parser and forgotten here -- would silently select "delete servers".
	switch cfg.InstanceGarbageCollectionMode {
	case hetznerop.GCDisabled:
		log.FromContext(ctx).Info("instance garbage collection is disabled; " +
			"servers whose NodeClaim is gone will not be reclaimed")
	case hetznerop.GCObserve, hetznerop.GCEnabled:
		mode := instancegc.Mode(cfg.InstanceGarbageCollectionMode)
		log.FromContext(ctx).Info("instance garbage collection is active",
			"mode", string(mode),
			"reclaims", mode == instancegc.ModeEnabled)
		providerControllers = append(providerControllers,
			instancegc.NewController(op.GetClient(), instanceProvider,
				cfg.ClusterName, clusterUID, mode, op.Clock))
	default:
		log.FromContext(ctx).Error(nil, "unhandled instance garbage collection mode; refusing to start",
			"mode", string(cfg.InstanceGarbageCollectionMode))
		return
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

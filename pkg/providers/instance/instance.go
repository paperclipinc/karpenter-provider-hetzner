package instance

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/metrics"
)

// ServerClient is the narrow interface for the hcloud servers API needed by this provider.
type ServerClient interface {
	Create(ctx context.Context, opts hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, *hcloud.Response, error)
	DeleteWithResult(ctx context.Context, server *hcloud.Server) (*hcloud.ServerDeleteResult, *hcloud.Response, error)
	GetByID(ctx context.Context, id int64) (*hcloud.Server, *hcloud.Response, error)
	AllWithOpts(ctx context.Context, opts hcloud.ServerListOpts) ([]*hcloud.Server, error)
}

// PlacementGroupClient is the narrow interface for the hcloud placement groups API.
type PlacementGroupClient interface {
	AllWithOpts(ctx context.Context, opts hcloud.PlacementGroupListOpts) ([]*hcloud.PlacementGroup, error)
	Create(ctx context.Context, opts hcloud.PlacementGroupCreateOpts) (hcloud.PlacementGroupCreateResult, *hcloud.Response, error)
}

// ActionWaiter waits for hcloud actions to complete. *hcloud.ActionClient satisfies it.
type ActionWaiter interface {
	WaitFor(ctx context.Context, actions ...*hcloud.Action) error
}

// Provider wraps hcloud server CRUD operations for Karpenter.
type Provider struct {
	client      ServerClient
	pgClient    PlacementGroupClient
	waiter      ActionWaiter
	clusterName string

	// clusterUID identifies this cluster independently of its operator-chosen
	// name, so two clusters sharing a CLUSTER_NAME in one Hetzner project can
	// still tell their servers apart. Empty disables the check, which is the
	// pre-existing behaviour.
	clusterUID string
}

// NewProvider returns a Provider that does NOT wait for hcloud actions to
// complete after server creation and has no placement-group support. Intended
// for tests; production uses NewProviderWithPlacementGroups.
func NewProvider(client ServerClient, clusterName string) *Provider {
	return &Provider{client: client, clusterName: clusterName}
}

// NewProviderWithWaiter returns a Provider that blocks after server creation
// until all hcloud create actions complete, but has no placement-group support.
// Intended for tests; production uses NewProviderWithPlacementGroups.
func NewProviderWithWaiter(client ServerClient, clusterName string, waiter ActionWaiter) *Provider {
	return &Provider{client: client, waiter: waiter, clusterName: clusterName}
}

// NewProviderWithPlacementGroups returns a Provider that supports placement
// groups and waits for hcloud create actions to complete. This is the
// production constructor.
func NewProviderWithPlacementGroups(client ServerClient, pgClient PlacementGroupClient, clusterName, clusterUID string, waiter ActionWaiter) *Provider {
	return &Provider{
		client: client, pgClient: pgClient, waiter: waiter,
		clusterName: clusterName, clusterUID: clusterUID,
	}
}

// CreateOpts contains all parameters needed to create a Hetzner server node.
type CreateOpts struct {
	Name                   string
	ServerType             string
	Location               string
	Image                  *hcloud.Image
	NetworkID              int64
	FirewallIDs            []int64
	SSHKeyIDs              []int64
	Labels                 map[string]string
	UserData               string
	NodeClaim              string
	NodePool               string
	PlacementGroupStrategy string
	EnablePublicIPv4       bool
	EnablePublicIPv6       bool
}

// placementGroupName returns the deterministic placement group name for a node
// pool, scoped to the cluster so two clusters sharing a Hetzner project (and a
// node-pool name) do not collide on the same placement group. When nodePool is
// empty it falls back to the cluster-scoped default.
func placementGroupName(clusterName, nodePool string) string {
	base := "karpenter-" + clusterName
	if nodePool == "" {
		return base
	}
	return base + "-" + nodePool
}

// getOrCreatePlacementGroup finds an existing placement group of type spread
// with the given name, creating it if it does not exist yet. It returns the
// group's ID, or 0 and an error on failure.
func (p *Provider) getOrCreatePlacementGroup(ctx context.Context, name string) (int64, error) {
	// AllWithOpts filters server-side by Name + Type, so a non-empty result is
	// already the group we want.
	existing, err := p.pgClient.AllWithOpts(ctx, hcloud.PlacementGroupListOpts{
		Name: name,
		Type: hcloud.PlacementGroupTypeSpread,
	})
	if err != nil {
		return 0, fmt.Errorf("listing placement groups: %w", err)
	}
	if len(existing) > 0 {
		return existing[0].ID, nil
	}

	result, _, err := p.pgClient.Create(ctx, hcloud.PlacementGroupCreateOpts{
		Name: name,
		Type: hcloud.PlacementGroupTypeSpread,
	})
	if err != nil {
		return 0, fmt.Errorf("creating placement group %q: %w", name, err)
	}
	return result.PlacementGroup.ID, nil
}

// Create provisions a new Hetzner server, merging Karpenter management labels.
func (p *Provider) Create(ctx context.Context, opts CreateOpts) (*hcloud.Server, error) {
	start := time.Now()
	server, err := p.create(ctx, opts)
	result := metrics.ResultSuccess
	if err != nil {
		result = metrics.ResultError
	}
	metrics.RecordServerCreate(result, time.Since(start))
	return server, err
}

// create is the internal implementation of Create, instrumented by Create().
func (p *Provider) create(ctx context.Context, opts CreateOpts) (*hcloud.Server, error) {
	log := logf.FromContext(ctx)
	labels := make(map[string]string, len(opts.Labels)+3)
	for k, v := range opts.Labels {
		labels[k] = v
	}
	labels[apiv1.ServerLabelManagedBy] = apiv1.ServerValueManagedBy
	labels[apiv1.ServerLabelCluster] = p.clusterName
	if p.clusterUID != "" {
		labels[apiv1.ServerLabelClusterUID] = p.clusterUID
	}
	if opts.NodeClaim != "" {
		labels[apiv1.ServerLabelNodeClaim] = opts.NodeClaim
	}
	if opts.NodePool != "" {
		labels[apiv1.ServerLabelNodePool] = opts.NodePool
	}

	// Build networks list.
	var networks []*hcloud.Network
	if opts.NetworkID > 0 {
		networks = []*hcloud.Network{{ID: opts.NetworkID}}
	}

	// Build firewalls list.
	var firewalls []*hcloud.ServerCreateFirewall
	for _, fwID := range opts.FirewallIDs {
		firewalls = append(firewalls, &hcloud.ServerCreateFirewall{
			Firewall: hcloud.Firewall{ID: fwID},
		})
	}

	// Build SSH keys list.
	var sshKeys []*hcloud.SSHKey
	for _, keyID := range opts.SSHKeyIDs {
		sshKeys = append(sshKeys, &hcloud.SSHKey{ID: keyID})
	}

	createOpts := hcloud.ServerCreateOpts{
		Name:       opts.Name,
		ServerType: &hcloud.ServerType{Name: opts.ServerType},
		Image:      opts.Image,
		Location:   &hcloud.Location{Name: opts.Location},
		Networks:   networks,
		Firewalls:  firewalls,
		SSHKeys:    sshKeys,
		Labels:     labels,
		UserData:   opts.UserData,
	}

	createOpts.PublicNet = &hcloud.ServerCreatePublicNet{
		EnableIPv4: opts.EnablePublicIPv4,
		EnableIPv6: opts.EnablePublicIPv6,
	}

	// Apply placement group when strategy is "spread" (or empty, since spread
	// is the kubebuilder default). Strategy "none" intentionally skips this.
	if p.pgClient != nil && opts.PlacementGroupStrategy != "none" {
		pgName := placementGroupName(p.clusterName, opts.NodePool)
		pgID, err := p.getOrCreatePlacementGroup(ctx, pgName)
		if err != nil {
			return nil, fmt.Errorf("resolving placement group: %w", err)
		}
		createOpts.PlacementGroup = &hcloud.PlacementGroup{ID: pgID}
	}

	result, _, err := p.client.Create(ctx, createOpts)
	if err != nil {
		// Hetzner rejects duplicate server names. Reaching this means a previous
		// attempt already created the server but we never recorded its ID —
		// typically because the process died between the create call and the
		// status write. The server is running and billing with no owner, and its
		// name and labels are the only surviving record of it, so adopt it rather
		// than retrying into the same collision forever.
		if adopted := p.adoptOrphan(ctx, opts, err); adopted != nil {
			imageID := int64(0)
			if adopted.Image != nil {
				imageID = adopted.Image.ID
			}
			pgName := ""
			if adopted.PlacementGroup != nil {
				pgName = adopted.PlacementGroup.Name
			}
			// Log the shape actually adopted, not the shape requested: these are
			// the fields that would reveal a machine whose placement group or image
			// diverges from what this request would have built.
			log.Info("adopted orphaned server",
				"name", adopted.Name, "id", adopted.ID,
				"serverType", opts.ServerType, "location", opts.Location,
				"imageID", imageID, "placementGroup", pgName, "status", string(adopted.Status))
			return adopted, nil
		}
		return nil, MapCreateError(err)
	}

	// Wait for the create action and any follow-up actions so we only return a
	// server that is actually being provisioned.
	if p.waiter != nil {
		actions := make([]*hcloud.Action, 0, 1+len(result.NextActions))
		if result.Action != nil {
			actions = append(actions, result.Action)
		}
		actions = append(actions, result.NextActions...)
		if len(actions) > 0 {
			if err := p.waiter.WaitFor(ctx, actions...); err != nil {
				// The server exists. Walking away leaves it running and billing with
				// nothing pointing at it, and a later retry would then adopt a machine
				// whose provisioning actions are known to have failed -- turning a
				// loud repeated error into a silent success. Clean it up so adoption's
				// only input stays the crash case, where the machine is healthy.
				//
				// The cleanup runs on a context detached from the caller's. WaitFor
				// returns ctx.Err() when that context is cancelled -- a SIGTERM, a lost
				// leader lease, a reconcile deadline -- which is the likeliest way to
				// reach this branch at all, and reusing the dead context would fail the
				// delete before it issued a single request, leaking exactly the server
				// this is here to reclaim.
				if result.Server != nil {
					p.deleteAfterFailedCreate(ctx, opts.Name, result.Server.ID)
				}
				return nil, fmt.Errorf("waiting for server %q create actions: %w", opts.Name, err)
			}
		}
	}
	pgAttached := createOpts.PlacementGroup != nil
	imageID := int64(0)
	if opts.Image != nil {
		imageID = opts.Image.ID
	}
	log.Info("created server",
		"name", opts.Name,
		"serverType", opts.ServerType,
		"location", opts.Location,
		"imageID", imageID,
		"placementGroupAttached", pgAttached,
	)
	return result.Server, nil
}

// createCleanupTimeout bounds the compensating delete issued when a create's
// actions fail. That delete runs on a context detached from the caller's, so it
// needs a deadline of its own rather than inheriting one.
const createCleanupTimeout = 30 * time.Second

// deleteAfterFailedCreate terminates a server whose create actions did not
// complete. It goes through the instrumented Delete so the call shows up in
// server_delete_total like every other deletion this provider issues.
func (p *Provider) deleteAfterFailedCreate(ctx context.Context, name string, serverID int64) {
	delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createCleanupTimeout)
	defer cancel()
	if err := p.Delete(delCtx, FormatProviderID(serverID)); err != nil &&
		!karpcp.IsNodeClaimNotFoundError(err) {
		logf.FromContext(ctx).Error(err, "deleting a server whose create actions failed",
			"name", name, "id", serverID)
	}
}

// adoptOrphan returns the server that caused a uniqueness_error, provided this
// provider owns it and it is the server this call asked for. It returns nil for
// any other error, when the lookup fails, or when the existing server does not
// match — adopting a server belonging to another cluster would hand it to
// Karpenter for deletion, and adopting one of a different shape would make the
// NodeClaim advertise capacity and a zone the machine does not have.
//
// Declining leaves the original create error to surface. Note that the garbage
// collector will NOT reclaim the server while this NodeClaim is retrying: it
// treats the nodeclaim label as proof of ownership precisely so that an
// in-flight create is never destroyed. Recovery comes when Karpenter gives up on
// the NodeClaim, after which the name frees and the sweep reclaims the machine.
func (p *Provider) adoptOrphan(ctx context.Context, opts CreateOpts, createErr error) *hcloud.Server {
	if !hcloud.IsError(createErr, hcloud.ErrorCodeUniquenessError) {
		return nil
	}
	log := logf.FromContext(ctx)
	servers, err := p.client.AllWithOpts(ctx, hcloud.ServerListOpts{Name: opts.Name})
	if err != nil {
		// Without this the failed recovery is invisible: the caller only ever sees
		// the original uniqueness error, with no sign adoption was even attempted.
		log.Error(err, "looking up the server behind a name collision", "name", opts.Name)
		metrics.RecordServerAdopt(metrics.AdoptError)
		return nil
	}
	for _, s := range servers {
		if s.Name != opts.Name {
			continue
		}
		if s.Labels[apiv1.ServerLabelManagedBy] != apiv1.ServerValueManagedBy {
			continue
		}
		if s.Labels[apiv1.ServerLabelCluster] != p.clusterName {
			continue
		}
		// The name label alone is not proof of ownership: CLUSTER_NAME is
		// operator-supplied and not unique. Adoption hands a live machine to
		// Karpenter, which will eventually terminate it, so taking one belonging
		// to a same-named cluster would destroy their node. A missing UID predates
		// the label and is treated as ours.
		if uid := s.Labels[apiv1.ServerLabelClusterUID]; uid != "" && uid != p.clusterUID {
			continue
		}
		// Only a server this same NodeClaim created is evidence of a lost create.
		// userData, SSH keys and public-IP policy are invisible on the returned
		// server and are not drift-checked either, so matching the NodeClaim is
		// what makes it safe to assume the machine was built from these inputs.
		// Networks and firewalls do have drift checks in pkg/cloudprovider, so a
		// mismatch there self-heals. The placement group is the gap: it is visible
		// (the caller logs it) but neither checked here nor drift-checked, so an
		// adopted server outside its spread group stays that way for life.
		if opts.NodeClaim == "" || s.Labels[apiv1.ServerLabelNodeClaim] != opts.NodeClaim {
			continue
		}
		// The normal path waits on the create actions before returning, which is
		// how the caller knows the machine is really coming up. Adoption cannot
		// wait — those action handles are gone — so require the server to be
		// running or still coming up. Anything else (off, stopping, deleting,
		// unknown) would hand Karpenter a NodeClaim with capacity nothing starts.
		if s.Status != hcloud.ServerStatusRunning &&
			s.Status != hcloud.ServerStatusInitializing &&
			s.Status != hcloud.ServerStatusStarting {
			continue
		}
		// The caller builds the NodeClaim's capacity and zone labels from the
		// offering it selected on THIS attempt, not from the server it gets back.
		// A server left by an earlier attempt that selected a different offering
		// would be advertised with the wrong shape and the wrong zone — the latter
		// silently breaking volume scheduling. An unresolved location fails closed
		// for the same reason: that is the case where the zone is most likely wrong.
		if s.ServerType == nil || s.ServerType.Name != opts.ServerType {
			continue
		}
		if opts.Location == "" || s.Location == nil || s.Location.Name != opts.Location {
			continue
		}
		// Karpenter records the adopted server's image as the NodeClaim's and then
		// compares the two to detect image drift, so a mismatch accepted here can
		// never be detected again.
		if opts.Image != nil && (s.Image == nil || s.Image.ID != opts.Image.ID) {
			continue
		}
		metrics.RecordServerAdopt(metrics.AdoptAdopted)
		return s
	}
	// A NodeClaim retrying forever into a collision adoption keeps refusing is the
	// state that leaves a machine billing, so it needs its own signal rather than
	// looking like an ordinary create error.
	metrics.RecordServerAdopt(metrics.AdoptDeclined)
	return nil
}

// Delete removes the server identified by providerID. It returns nil once the
// deletion has been triggered (Hetzner deletes asynchronously), and a
// cloudprovider.NodeClaimNotFoundError once the server no longer exists — the
// signal Karpenter's termination controller uses to finalize the NodeClaim.
func (p *Provider) Delete(ctx context.Context, providerID string) error {
	err := p.delete(ctx, providerID)
	// A NodeClaimNotFoundError means the instance is already terminated, which is
	// the expected steady-state result of a completed deletion — not a failure —
	// so it must not be counted as a server-delete error.
	result := metrics.ResultSuccess
	if err != nil && !karpcp.IsNodeClaimNotFoundError(err) {
		result = metrics.ResultError
	}
	metrics.RecordServerDelete(result)
	return err
}

// delete is the internal implementation of Delete, instrumented by Delete().
func (p *Provider) delete(ctx context.Context, providerID string) error {
	log := logf.FromContext(ctx)
	id, err := ParseProviderID(providerID)
	if err != nil {
		return fmt.Errorf("parsing provider ID %q: %w", providerID, err)
	}

	server, _, err := p.client.GetByID(ctx, id)
	if err != nil {
		if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("server %d not found", id))
		}
		return fmt.Errorf("getting server %d: %w", id, err)
	}
	if server == nil {
		// Server already gone. Per the CloudProvider.Delete contract, return a
		// NodeClaimNotFoundError (not nil) once the instance is terminated — it is
		// the signal Karpenter's termination controller uses to remove the NodeClaim
		// finalizer. Returning nil makes it requeue indefinitely and leaks the NodeClaim.
		return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("server %d not found", id))
	}

	log.Info("deleting server", "serverID", id)
	_, _, err = p.client.DeleteWithResult(ctx, server)
	if err != nil {
		if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("server %d not found", id))
		}
		return fmt.Errorf("deleting server %d: %w", id, err)
	}
	return nil
}

// Get retrieves the server identified by providerID. Returns nil if not found.
func (p *Provider) Get(ctx context.Context, providerID string) (*hcloud.Server, error) {
	id, err := ParseProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("parsing provider ID %q: %w", providerID, err)
	}

	server, _, err := p.client.GetByID(ctx, id)
	if err != nil {
		if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting server %d: %w", id, err)
	}
	return server, nil
}

// List returns all servers managed by this Karpenter instance.
func (p *Provider) List(ctx context.Context) ([]*hcloud.Server, error) {
	opts := hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{
			LabelSelector: fmt.Sprintf("%s=%s,%s=%s",
				apiv1.ServerLabelManagedBy, apiv1.ServerValueManagedBy,
				apiv1.ServerLabelCluster, p.clusterName),
		},
	}
	servers, err := p.client.AllWithOpts(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing karpenter servers: %w", err)
	}
	return servers, nil
}

// ParseProviderID parses a Hetzner provider ID of the form "hcloud://<id>" and returns the integer ID.
func ParseProviderID(providerID string) (int64, error) {
	if !strings.HasPrefix(providerID, apiv1.ProviderIDPrefix) {
		return 0, fmt.Errorf("provider ID %q must start with %q", providerID, apiv1.ProviderIDPrefix)
	}
	idStr := strings.TrimPrefix(providerID, apiv1.ProviderIDPrefix)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid server ID in provider ID %q: %w", providerID, err)
	}
	return id, nil
}

// FormatProviderID formats a Hetzner server ID as a Karpenter provider ID.
func FormatProviderID(serverID int64) string {
	return apiv1.ProviderIDPrefix + strconv.FormatInt(serverID, 10)
}

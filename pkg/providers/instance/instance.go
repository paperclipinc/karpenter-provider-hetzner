package instance

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
)

// ServerClient is the narrow interface for the hcloud servers API needed by this provider.
type ServerClient interface {
	Create(ctx context.Context, opts hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, *hcloud.Response, error)
	DeleteWithResult(ctx context.Context, server *hcloud.Server) (*hcloud.ServerDeleteResult, *hcloud.Response, error)
	GetByID(ctx context.Context, id int64) (*hcloud.Server, *hcloud.Response, error)
	AllWithOpts(ctx context.Context, opts hcloud.ServerListOpts) ([]*hcloud.Server, error)
}

// ActionWaiter waits for hcloud actions to complete. *hcloud.ActionClient satisfies it.
type ActionWaiter interface {
	WaitFor(ctx context.Context, actions ...*hcloud.Action) error
}

// Provider wraps hcloud server CRUD operations for Karpenter.
type Provider struct {
	client      ServerClient
	waiter      ActionWaiter
	clusterName string
}

// NewProvider returns a Provider that does NOT wait for hcloud actions to
// complete after server creation. Use it only in tests that do not exercise
// action waiting; production code should use NewProviderWithWaiter.
func NewProvider(client ServerClient, clusterName string) *Provider {
	return &Provider{client: client, clusterName: clusterName}
}

// NewProviderWithWaiter returns a Provider that blocks after server creation
// until all hcloud create actions complete. Use this in production.
func NewProviderWithWaiter(client ServerClient, clusterName string, waiter ActionWaiter) *Provider {
	return &Provider{client: client, waiter: waiter, clusterName: clusterName}
}

// CreateOpts contains all parameters needed to create a Hetzner server node.
type CreateOpts struct {
	Name        string
	ServerType  string
	Location    string
	Image       *hcloud.Image
	NetworkID   int64
	FirewallIDs []int64
	SSHKeyIDs   []int64
	Labels      map[string]string
	UserData    string
	NodeClaim   string
	NodePool    string
	EnablePublicIPv4 bool
	EnablePublicIPv6 bool
}

// Create provisions a new Hetzner server, merging Karpenter management labels.
func (p *Provider) Create(ctx context.Context, opts CreateOpts) (*hcloud.Server, error) {
	labels := make(map[string]string, len(opts.Labels)+3)
	for k, v := range opts.Labels {
		labels[k] = v
	}
	labels[v1alpha1.ServerLabelManagedBy] = v1alpha1.ServerValueManagedBy
	labels[v1alpha1.ServerLabelCluster] = p.clusterName
	if opts.NodeClaim != "" {
		labels[v1alpha1.ServerLabelNodeClaim] = opts.NodeClaim
	}
	if opts.NodePool != "" {
		labels[v1alpha1.ServerLabelNodePool] = opts.NodePool
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

	result, _, err := p.client.Create(ctx, createOpts)
	if err != nil {
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
				return nil, fmt.Errorf("waiting for server %q create actions: %w", opts.Name, err)
			}
		}
	}
	return result.Server, nil
}

// Delete removes the server identified by providerID.
// If the server is already gone (NotFound), Delete returns nil (idempotent).
func (p *Provider) Delete(ctx context.Context, providerID string) error {
	id, err := ParseProviderID(providerID)
	if err != nil {
		return fmt.Errorf("parsing provider ID %q: %w", providerID, err)
	}

	server, _, err := p.client.GetByID(ctx, id)
	if err != nil {
		if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return nil
		}
		return fmt.Errorf("getting server %d: %w", id, err)
	}
	if server == nil {
		// Server not found.
		return nil
	}

	_, _, err = p.client.DeleteWithResult(ctx, server)
	if err != nil {
		if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return nil
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
				v1alpha1.ServerLabelManagedBy, v1alpha1.ServerValueManagedBy,
				v1alpha1.ServerLabelCluster, p.clusterName),
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
	if !strings.HasPrefix(providerID, v1alpha1.ProviderIDPrefix) {
		return 0, fmt.Errorf("provider ID %q must start with %q", providerID, v1alpha1.ProviderIDPrefix)
	}
	idStr := strings.TrimPrefix(providerID, v1alpha1.ProviderIDPrefix)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid server ID in provider ID %q: %w", providerID, err)
	}
	return id, nil
}

// FormatProviderID formats a Hetzner server ID as a Karpenter provider ID.
func FormatProviderID(serverID int64) string {
	return v1alpha1.ProviderIDPrefix + strconv.FormatInt(serverID, 10)
}

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

// Provider wraps hcloud server CRUD operations for Karpenter.
type Provider struct {
	client ServerClient
}

// NewProvider creates a new instance provider.
func NewProvider(client ServerClient) *Provider {
	return &Provider{client: client}
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
}

// Create provisions a new Hetzner server, merging Karpenter management labels.
func (p *Provider) Create(ctx context.Context, opts CreateOpts) (*hcloud.Server, error) {
	labels := make(map[string]string, len(opts.Labels)+3)
	for k, v := range opts.Labels {
		labels[k] = v
	}
	labels[v1alpha1.ServerLabelManagedBy] = v1alpha1.ServerValueManagedBy
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

	result, _, err := p.client.Create(ctx, createOpts)
	if err != nil {
		return nil, fmt.Errorf("creating server %q: %w", opts.Name, err)
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
			LabelSelector: v1alpha1.ServerLabelManagedBy + "=" + v1alpha1.ServerValueManagedBy,
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

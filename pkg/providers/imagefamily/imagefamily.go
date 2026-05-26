package imagefamily

import (
	"context"
	"fmt"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
)

// ImageClient is the narrow interface for the hcloud images API needed by this provider.
type ImageClient interface {
	AllWithOpts(ctx context.Context, opts hcloud.ImageListOpts) ([]*hcloud.Image, error)
}

// Provider resolves OS images from an HCloudNodeClass ImageSelector.
type Provider struct {
	client ImageClient
}

// NewProvider creates a new image family provider.
func NewProvider(client ImageClient) *Provider {
	return &Provider{client: client}
}

// Resolve returns the best matching image for the given selector and architecture.
// Supported families: "ubuntu", "talos".
func (p *Provider) Resolve(ctx context.Context, selector v1alpha1.ImageSelector, arch hcloud.Architecture) (*hcloud.Image, error) {
	switch strings.ToLower(selector.Family) {
	case "ubuntu":
		return p.resolveUbuntu(ctx, selector.Version, arch)
	case "talos":
		return p.resolveTalos(ctx, selector.Version, arch)
	default:
		return nil, fmt.Errorf("unsupported image family %q: must be one of ubuntu, talos", selector.Family)
	}
}

// resolveUbuntu finds a system image whose description contains "ubuntu" and optionally the given version.
// Returns the first matching image.
func (p *Provider) resolveUbuntu(ctx context.Context, version string, arch hcloud.Architecture) (*hcloud.Image, error) {
	images, err := p.client.AllWithOpts(ctx, hcloud.ImageListOpts{
		Type:         []hcloud.ImageType{hcloud.ImageTypeSystem},
		Architecture: []hcloud.Architecture{arch},
	})
	if err != nil {
		return nil, fmt.Errorf("listing ubuntu images: %w", err)
	}

	for _, img := range images {
		desc := strings.ToLower(img.Description)
		if !strings.Contains(desc, "ubuntu") {
			continue
		}
		if version != "" && !strings.Contains(desc, version) {
			continue
		}
		return img, nil
	}

	if version != "" {
		return nil, fmt.Errorf("no ubuntu image found for version %q and arch %q", version, arch)
	}
	return nil, fmt.Errorf("no ubuntu image found for arch %q", arch)
}

// resolveTalos finds the newest snapshot image whose description contains "talos" and optionally the given version.
func (p *Provider) resolveTalos(ctx context.Context, version string, arch hcloud.Architecture) (*hcloud.Image, error) {
	images, err := p.client.AllWithOpts(ctx, hcloud.ImageListOpts{
		Type:         []hcloud.ImageType{hcloud.ImageTypeSnapshot},
		Architecture: []hcloud.Architecture{arch},
	})
	if err != nil {
		return nil, fmt.Errorf("listing talos images: %w", err)
	}

	var best *hcloud.Image
	for _, img := range images {
		desc := strings.ToLower(img.Description)
		if !strings.Contains(desc, "talos") {
			continue
		}
		if version != "" && !strings.Contains(desc, version) {
			continue
		}
		if best == nil || img.Created.After(best.Created) {
			best = img
		}
	}

	if best == nil {
		if version != "" {
			return nil, fmt.Errorf("no talos snapshot found for version %q and arch %q", version, arch)
		}
		return nil, fmt.Errorf("no talos snapshot found for arch %q", arch)
	}
	return best, nil
}

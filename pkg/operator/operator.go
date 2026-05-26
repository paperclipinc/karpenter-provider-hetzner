package operator

import (
	"fmt"
	"os"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// NewHCloudClient creates a new Hetzner Cloud API client using the HCLOUD_TOKEN environment variable.
func NewHCloudClient() (*hcloud.Client, error) {
	token := os.Getenv("HCLOUD_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("HCLOUD_TOKEN environment variable is required")
	}
	return hcloud.NewClient(hcloud.WithToken(token)), nil
}

package operator

import (
	"fmt"
	"os"
	"strings"
)

// Config holds provider configuration sourced from the environment.
type Config struct {
	// ClusterName scopes all managed servers so multiple clusters can share
	// one Hetzner project without colliding.
	ClusterName string
}

// LoadConfig reads provider configuration from the environment.
// CLUSTER_NAME is required.
func LoadConfig() (*Config, error) {
	name := strings.TrimSpace(os.Getenv("CLUSTER_NAME"))
	if name == "" {
		return nil, fmt.Errorf("CLUSTER_NAME environment variable is required")
	}
	return &Config{ClusterName: name}, nil
}

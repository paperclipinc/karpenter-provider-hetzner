package operator

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var clusterNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,63}$`)

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
	if !clusterNameRE.MatchString(name) {
		return nil, fmt.Errorf("CLUSTER_NAME %q is not a valid Hetzner label value (must match [a-zA-Z0-9._-], max 63 chars)", name)
	}
	return &Config{ClusterName: name}, nil
}

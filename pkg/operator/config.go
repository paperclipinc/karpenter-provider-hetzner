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

	// DisableInstanceGarbageCollection stops the sweep that terminates servers
	// Karpenter no longer has a NodeClaim for. The states that produce such
	// servers wholesale — reinstalling the CRDs, restoring etcd, clearing
	// finalizers by hand — are maintenance windows, and during one an operator
	// needs to pause the sweep without also stopping provisioning and
	// disruption, which scaling the deployment to zero would do.
	DisableInstanceGarbageCollection bool
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
	disableGC, err := parseBool("DISABLE_INSTANCE_GARBAGE_COLLECTION", false)
	if err != nil {
		return nil, err
	}
	return &Config{
		ClusterName:                      name,
		DisableInstanceGarbageCollection: disableGC,
	}, nil
}

// parseBool reads a boolean environment variable, returning def when it is
// unset. An unrecognised value is an error rather than a silent fallback: this
// gates a controller that deletes servers, and the moment an operator reaches
// for it is a maintenance window where a typo failing open would reap the fleet
// they were trying to protect. Refusing to start is the safe failure.
func parseBool(key string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be one of true/false/1/0/yes/no/on/off, got %q", key, raw)
	}
}

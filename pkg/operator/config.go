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

	// InstanceGarbageCollectionMode controls the sweep that reclaims servers
	// Karpenter no longer has a NodeClaim for.
	//
	// "observe" exists because this deletes machines. An operator adopting it
	// cannot otherwise find out what it would do to their fleet except by letting
	// it do it, and a kill switch only helps once something has already gone
	// wrong. In observe mode every check runs and every signal is emitted; only
	// the deletion is skipped.
	//
	// "disabled" is for maintenance that removes NodeClaims wholesale --
	// reinstalling the CRDs, restoring etcd, clearing finalizers by hand -- where
	// the sweep must pause without also stopping provisioning and disruption, as
	// scaling the deployment to zero would.
	InstanceGarbageCollectionMode GCMode
}

// GCMode selects how the orphaned-server sweep behaves.
type GCMode string

const (
	// GCEnabled reclaims orphaned servers.
	GCEnabled GCMode = "enabled"
	// GCObserve runs every check and reports what it would reclaim, deleting
	// nothing.
	GCObserve GCMode = "observe"
	// GCDisabled does not sweep at all.
	GCDisabled GCMode = "disabled"
)

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
	gcMode, err := parseGCMode(os.Getenv("INSTANCE_GARBAGE_COLLECTION_MODE"))
	if err != nil {
		return nil, err
	}
	return &Config{
		ClusterName:                   name,
		InstanceGarbageCollectionMode: gcMode,
	}, nil
}

// parseGCMode reads the sweep's mode, defaulting to enabled.
//
// An unrecognised value is an error rather than a silent fallback. This gates a
// controller that deletes servers, and the moment an operator reaches for it is
// a maintenance window where a typo falling back to "enabled" would reap the
// fleet they were trying to protect. Refusing to start is the safe failure.
func parseGCMode(raw string) (GCMode, error) {
	switch mode := GCMode(strings.ToLower(strings.TrimSpace(raw))); mode {
	case "":
		return GCEnabled, nil
	case GCEnabled, GCObserve, GCDisabled:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"INSTANCE_GARBAGE_COLLECTION_MODE must be one of %q, %q or %q, got %q",
			GCEnabled, GCObserve, GCDisabled, raw)
	}
}

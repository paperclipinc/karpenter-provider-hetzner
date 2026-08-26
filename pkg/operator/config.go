package operator

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var clusterNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,63}$`)

// DefaultVMMemoryOverheadPercent is the fraction of a server type's advertised
// memory assumed to be unavailable to the guest.
//
// Hetzner advertises the RAM the VM is allocated, but the guest kernel never
// sees all of it: firmware, the kernel image and per-page structures take a cut
// that grows with the size of the machine. Measured across cx/cpx types the gap
// runs from roughly 4.4% on a 32Gi server to 6.9% on a 4Gi one, so a single
// fraction cannot be exact everywhere. 0.075 is deliberately on the safe side of
// every measurement: erring high costs a little schedulable memory, while erring
// low tells Karpenter a server is bigger than it is and strands pods on it.
//
// This is only the estimate used before a node of that type has been seen. Once
// one registers, its real capacity is recorded and used instead, so the constant
// matters for the first node of each server type and image, not the fleet.
const DefaultVMMemoryOverheadPercent = 0.075

// Config holds provider configuration sourced from the environment.
type Config struct {
	// ClusterName scopes all managed servers so multiple clusters can share
	// one Hetzner project without colliding.
	ClusterName string

	// VMMemoryOverheadPercent is subtracted from every server type's advertised
	// memory. See DefaultVMMemoryOverheadPercent.
	VMMemoryOverheadPercent float64
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

	overhead, err := parseVMMemoryOverheadPercent(os.Getenv("VM_MEMORY_OVERHEAD_PERCENT"))
	if err != nil {
		return nil, err
	}

	return &Config{ClusterName: name, VMMemoryOverheadPercent: overhead}, nil
}

// parseVMMemoryOverheadPercent reads the overhead fraction, defaulting when
// unset. A value of 1 or more would leave a server with no memory at all, and a
// negative one would claim more memory than Hetzner sells; both are rejected at
// startup rather than silently producing unschedulable or oversized nodes.
func parseVMMemoryOverheadPercent(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultVMMemoryOverheadPercent, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("VM_MEMORY_OVERHEAD_PERCENT %q is not a number (expected a fraction such as 0.075)", raw)
	}
	if v < 0 || v >= 1 {
		return 0, fmt.Errorf("VM_MEMORY_OVERHEAD_PERCENT %v is out of range (must be at least 0 and less than 1)", v)
	}
	return v, nil
}

package instancetype

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/resource"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
)

// discoveredCapacityCache records the memory capacity real servers report once
// they boot, keyed by server type and image.
//
// The estimate in operator.DefaultVMMemoryOverheadPercent is a single fraction
// standing in for a gap that varies by machine size, so it is wrong everywhere
// by a little. A booted node is not an estimate, and one observation is good for
// every future node of that type on that image. This is the same approach the
// AWS provider takes with its own overhead percent, for the same reason.
//
// State is process-local and deliberately not persisted. Karpenter must be able
// to size a node before any node exists, so a cold cache has to be survivable
// anyway; that being true, a durable store would add a failure mode without
// removing one. A restart simply falls back to the estimate until the next node
// registers.
type discoveredCapacityCache struct {
	mu     sync.RWMutex
	byType map[string]resource.Quantity
}

func newDiscoveredCapacityCache() *discoveredCapacityCache {
	return &discoveredCapacityCache{byType: map[string]resource.Quantity{}}
}

// discoveredKey scopes an observation to the image it was taken on. The gap
// between advertised and visible memory is set by the guest kernel, so a
// different image can produce a different figure for the same server type, and
// carrying a measurement across an image change would apply a number that was
// never true of the new one.
//
// A node class with no resolved images yet keys on the server type alone. That
// is only reachable before the image controller has run, and it is the same
// bucket such a node class would use consistently.
func discoveredKey(serverType string, nodeClass *apiv1.HCloudNodeClass) string {
	if nodeClass == nil || len(nodeClass.Status.ResolvedImages) == 0 {
		return serverType
	}
	ids := make([]string, 0, len(nodeClass.Status.ResolvedImages))
	for _, img := range nodeClass.Status.ResolvedImages {
		ids = append(ids, fmt.Sprintf("%s/%d", img.Architecture, img.ImageID))
	}
	// Sorted so that a reordering of the same images is the same key.
	sort.Strings(ids)
	return serverType + "|" + strings.Join(ids, ",")
}

// get returns the measured capacity for a server type under a node class's
// image, if one has been recorded.
func (c *discoveredCapacityCache) get(serverType string, nodeClass *apiv1.HCloudNodeClass) (resource.Quantity, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	q, ok := c.byType[discoveredKey(serverType, nodeClass)]
	return q, ok
}

// record stores a capacity observed on a registered node, keeping the smallest
// value seen.
//
// Monotonically downward on purpose. Nodes of one type do vary slightly, and the
// consequence of the two directions is not symmetric: too low costs a little
// schedulable memory, too high strands a pod on a node that cannot hold it. A
// genuine increase -- a new image, say -- arrives under a different key, so
// holding the minimum here never pins the cache to a stale low value.
func (c *discoveredCapacityCache) record(serverType string, nodeClass *apiv1.HCloudNodeClass, observed resource.Quantity) bool {
	if observed.Sign() <= 0 {
		return false
	}
	key := discoveredKey(serverType, nodeClass)

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.byType[key]; ok && existing.Cmp(observed) <= 0 {
		return false
	}
	c.byType[key] = observed
	return true
}

// Record stores a capacity measured on a registered node. It reports whether
// this changed what the provider will use, so callers can log first discoveries
// and genuine drops without narrating every node that agrees with the cache.
func (p *Provider) Record(serverType string, nodeClass *apiv1.HCloudNodeClass, observed resource.Quantity) bool {
	return p.discovered.record(serverType, nodeClass, observed)
}

package instancetype

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
)

const cacheTTL = 6 * time.Hour

// ServerTypeClient is the narrow interface for the hcloud server types API.
type ServerTypeClient interface {
	All(ctx context.Context) ([]*hcloud.ServerType, error)
}

// Provider resolves Hetzner server types to Karpenter InstanceTypes.
type Provider struct {
	client ServerTypeClient

	mu          sync.RWMutex
	cachedTypes []*cloudprovider.InstanceType
	cacheExpiry time.Time

	unavailable *unavailableCache
}

// NewProvider creates a new instance type provider.
func NewProvider(client ServerTypeClient) *Provider {
	return &Provider{
		client: client,
		unavailable: newUnavailableCache(
			// 5m: long enough to route around a saturated location, short enough to
			// retry it soon. TODO: make configurable via operator config if needed.
			5 * time.Minute,
		),
	}
}

// List returns all available InstanceTypes, filtered to those with offerings in the given locations.
// Results are cached for 6 hours.
func (p *Provider) List(ctx context.Context, locations []string) ([]*cloudprovider.InstanceType, error) {
	p.mu.RLock()
	if p.cachedTypes != nil && time.Now().Before(p.cacheExpiry) {
		cached := p.cachedTypes
		p.mu.RUnlock()
		return p.applyAvailability(filterByLocations(cached, locations)), nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if p.cachedTypes != nil && time.Now().Before(p.cacheExpiry) {
		return p.applyAvailability(filterByLocations(p.cachedTypes, locations)), nil
	}

	serverTypes, err := p.client.All(ctx)
	if err != nil {
		return nil, err
	}

	types := make([]*cloudprovider.InstanceType, 0, len(serverTypes))
	for _, st := range serverTypes {
		types = append(types, toInstanceType(st))
	}

	p.cachedTypes = types
	p.cacheExpiry = time.Now().Add(cacheTTL)

	return p.applyAvailability(filterByLocations(types, locations)), nil
}

// MarkUnavailable records that a (serverType, location) offering failed with a
// capacity error so it is reported unavailable for a TTL. The mark takes effect
// on the next call to List, which Karpenter invokes at the start of each
// provisioning cycle (not within the cycle that failed).
func (p *Provider) MarkUnavailable(serverType, location string) {
	p.unavailable.markUnavailable(serverType, location)
}

// applyAvailability returns copies of the given instance types with each
// offering's Available flag computed live from the unavailable cache, so the
// 6h type-catalog cache never bakes in (and thus never staleness-traps)
// availability.
//
// The returned InstanceType and Offering structs are fresh value-copies, so
// setting Available never mutates the cached entries. Note that nested
// reference fields (Requirements, Capacity, Overhead) are intentionally shared
// with the cache, not deep-copied: callers must treat returned types as
// read-only and must not mutate those maps.
func (p *Provider) applyAvailability(types []*cloudprovider.InstanceType) []*cloudprovider.InstanceType {
	out := make([]*cloudprovider.InstanceType, len(types))
	for i, it := range types {
		offerings := make(cloudprovider.Offerings, len(it.Offerings))
		for j, o := range it.Offerings {
			zone := o.Requirements.Get(corev1.LabelTopologyZone).Any()
			cp := *o
			cp.Available = !p.unavailable.isUnavailable(it.Name, zone)
			offerings[j] = &cp
		}
		// Construct a fresh InstanceType (rather than copying *it) to avoid
		// copying the embedded sync.Once (govet copylocks); Requirements/Capacity/
		// Overhead are intentionally shared read-only with the cached entry.
		out[i] = &cloudprovider.InstanceType{
			Name:         it.Name,
			Offerings:    offerings,
			Requirements: it.Requirements,
			Capacity:     it.Capacity,
			Overhead:     it.Overhead,
		}
	}
	return out
}

// toInstanceType maps a Hetzner ServerType to a Karpenter InstanceType.
func toInstanceType(st *hcloud.ServerType) *cloudprovider.InstanceType {
	arch := "amd64"
	if st.Architecture == hcloud.ArchitectureARM {
		arch = "arm64"
	}

	cpuType := string(st.CPUType) // "shared" or "dedicated"

	// Build offerings: one per pricing location.
	offerings := make(cloudprovider.Offerings, 0, len(st.Pricings))
	for _, p := range st.Pricings {
		if p.Location == nil {
			continue
		}
		price := hourlyNetPrice(p)
		offerings = append(offerings, &cloudprovider.Offering{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, p.Location.Name),
			),
			Price:     price,
			Available: true,
		})
	}

	// Memory: ServerType.Memory is float32 in GB.
	memBytes := int64(float64(st.Memory) * 1024 * 1024 * 1024)
	// Disk: ServerType.Disk is int in GB.
	diskBytes := int64(st.Disk) * 1024 * 1024 * 1024

	return &cloudprovider.InstanceType{
		Name:      st.Name,
		Offerings: offerings,
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, st.Name),
			scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, arch),
			scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
			scheduling.NewRequirement(v1alpha1.LabelCPUType, corev1.NodeSelectorOpIn, cpuType),
			scheduling.NewRequirement(v1alpha1.LabelServerFamily, corev1.NodeSelectorOpIn, serverFamily(st.Name)),
		),
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:              *resource.NewMilliQuantity(int64(st.Cores)*1000, resource.DecimalSI),
			corev1.ResourceMemory:           *resource.NewQuantity(memBytes, resource.BinarySI),
			corev1.ResourceEphemeralStorage: *resource.NewQuantity(diskBytes, resource.BinarySI),
			corev1.ResourcePods:             *resource.NewQuantity(110, resource.DecimalSI),
		},
		Overhead: &cloudprovider.InstanceTypeOverhead{
			KubeReserved: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
		},
	}
}

// serverFamily extracts the server type family prefix (e.g. "cax", "cx", "cpx", "ccx").
func serverFamily(name string) string {
	for _, prefix := range []string{"cax", "cpx", "ccx", "cx"} {
		if strings.HasPrefix(name, prefix) {
			return prefix
		}
	}
	// Fall back to any leading alpha characters.
	i := 0
	for i < len(name) && (name[i] < '0' || name[i] > '9') {
		i++
	}
	if i > 0 {
		return name[:i]
	}
	return name
}

// Pricing here is the server-type base net price and intentionally excludes the
// primary-IPv4 surcharge: the catalog is NodeClass-agnostic. Cost-sensitive
// clusters drop the IPv4 charge with HCloudNodeClass.spec.enablePublicIPv4=false.
//
// hourlyNetPrice returns the net hourly price for a server-type pricing entry,
// preferring the explicit hourly figure and falling back to monthly/730.
func hourlyNetPrice(p hcloud.ServerTypeLocationPricing) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(p.Hourly.Net), 64); err == nil && v > 0 {
		return v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(p.Monthly.Net), 64); err == nil {
		return v / 730
	}
	return 0
}

// filterByLocations returns only the instance types that have at least one offering in the requested locations.
// If locations is empty, all instance types are returned unchanged.
func filterByLocations(types []*cloudprovider.InstanceType, locations []string) []*cloudprovider.InstanceType {
	if len(locations) == 0 {
		return types
	}
	locSet := make(map[string]struct{}, len(locations))
	for _, l := range locations {
		locSet[l] = struct{}{}
	}

	result := make([]*cloudprovider.InstanceType, 0, len(types))
	for _, it := range types {
		filtered := make(cloudprovider.Offerings, 0, len(it.Offerings))
		for _, o := range it.Offerings {
			zone := o.Requirements.Get(corev1.LabelTopologyZone).Any()
			if _, ok := locSet[zone]; ok {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) > 0 {
			result = append(result, &cloudprovider.InstanceType{
				Name:         it.Name,
				Offerings:    filtered,
				Requirements: it.Requirements,
				Capacity:     it.Capacity,
				Overhead:     it.Overhead,
			})
		}
	}
	return result
}

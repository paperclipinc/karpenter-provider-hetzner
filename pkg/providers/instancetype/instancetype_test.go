package instancetype

import (
	"context"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
)

// mockServerTypeClient is a fake ServerTypeClient for testing.
type mockServerTypeClient struct {
	types []*hcloud.ServerType
	calls int
}

func (m *mockServerTypeClient) All(_ context.Context) ([]*hcloud.ServerType, error) {
	m.calls++
	return m.types, nil
}

func makeServerType(name string, arch hcloud.Architecture, cpuType hcloud.CPUType, cores int, memGB float32, diskGB int, pricings []hcloud.ServerTypeLocationPricing) *hcloud.ServerType {
	return &hcloud.ServerType{
		ID:           1,
		Name:         name,
		Architecture: arch,
		CPUType:      cpuType,
		Cores:        cores,
		Memory:       memGB,
		Disk:         diskGB,
		Pricings:     pricings,
	}
}

var testPricings = []hcloud.ServerTypeLocationPricing{
	{
		Location: &hcloud.Location{Name: "nbg1"},
		Monthly:  hcloud.Price{Net: "7.3000000000"},
	},
	{
		Location: &hcloud.Location{Name: "fsn1"},
		Monthly:  hcloud.Price{Net: "7.3000000000"},
	},
}

func TestList_NoLocationFilter(t *testing.T) {
	st := makeServerType("cx11", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 1, 2, 20, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client)

	types, err := p.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(types) != 1 {
		t.Fatalf("expected 1 instance type, got %d", len(types))
	}
	if types[0].Name != "cx11" {
		t.Errorf("expected name cx11, got %s", types[0].Name)
	}
}

func TestList_LocationFilter(t *testing.T) {
	st := makeServerType("cx11", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 1, 2, 20, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client)

	// Only request nbg1; fsn1 offering should be filtered out but type still returned.
	types, err := p.List(context.Background(), []string{"nbg1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(types) != 1 {
		t.Fatalf("expected 1 type, got %d", len(types))
	}
	if len(types[0].Offerings) != 1 {
		t.Errorf("expected 1 offering (nbg1 only), got %d", len(types[0].Offerings))
	}
}

func TestList_LocationFilterExcludesAll(t *testing.T) {
	st := makeServerType("cx11", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 1, 2, 20, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client)

	types, err := p.List(context.Background(), []string{"hel1"}) // not in pricings
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(types) != 0 {
		t.Errorf("expected 0 types (no matching location), got %d", len(types))
	}
}

func TestInstanceType_Capacity(t *testing.T) {
	st := makeServerType("cx21", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 2, 4, 40, testPricings)
	it := toInstanceType(st)

	cpu := it.Capacity[corev1.ResourceCPU]
	if cpu.Value() != 2 {
		t.Errorf("expected 2 CPUs, got %v", cpu.Value())
	}

	mem := it.Capacity[corev1.ResourceMemory]
	expectedMem := int64(4) * 1024 * 1024 * 1024
	if mem.Value() != expectedMem {
		t.Errorf("expected memory %d bytes, got %d", expectedMem, mem.Value())
	}

	disk := it.Capacity[corev1.ResourceEphemeralStorage]
	expectedDisk := int64(40) * 1024 * 1024 * 1024
	if disk.Value() != expectedDisk {
		t.Errorf("expected disk %d bytes, got %d", expectedDisk, disk.Value())
	}

	pods := it.Capacity[corev1.ResourcePods]
	if pods.Value() != 110 {
		t.Errorf("expected 110 pods, got %d", pods.Value())
	}
}

func TestInstanceType_ArchARM(t *testing.T) {
	st := makeServerType("cax11", hcloud.ArchitectureARM, hcloud.CPUTypeShared, 2, 4, 40, testPricings)
	it := toInstanceType(st)

	archReq := it.Requirements.Get("kubernetes.io/arch")
	if archReq.Any() != "arm64" {
		t.Errorf("expected arm64 arch, got %s", archReq.Any())
	}
}

func TestInstanceType_ArchX86(t *testing.T) {
	st := makeServerType("cx11", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 1, 2, 20, testPricings)
	it := toInstanceType(st)

	archReq := it.Requirements.Get("kubernetes.io/arch")
	if archReq.Any() != "amd64" {
		t.Errorf("expected amd64 arch, got %s", archReq.Any())
	}
}

func TestServerFamily(t *testing.T) {
	cases := []struct {
		name   string
		expect string
	}{
		{"cax11", "cax"},
		{"cax21", "cax"},
		{"cx11", "cx"},
		{"cx21", "cx"},
		{"cpx11", "cpx"},
		{"ccx13", "ccx"},
	}
	for _, tc := range cases {
		got := serverFamily(tc.name)
		if got != tc.expect {
			t.Errorf("serverFamily(%q) = %q, want %q", tc.name, got, tc.expect)
		}
	}
}

func TestHourlyNetPrice(t *testing.T) {
	// Prefer hourly net; fall back to monthly net / 730.
	if got := hourlyNetPrice(hcloud.ServerTypeLocationPricing{
		Hourly:  hcloud.Price{Net: "0.0100"},
		Monthly: hcloud.Price{Net: "7.3000"},
	}); got != 0.01 {
		t.Errorf("want 0.01 from hourly net, got %v", got)
	}
	got := hourlyNetPrice(hcloud.ServerTypeLocationPricing{Monthly: hcloud.Price{Net: "7.3000"}})
	if got < 0.0099 || got > 0.0101 {
		t.Errorf("want ~0.01 from monthly net/730, got %v", got)
	}
}

func TestList_CacheHit(t *testing.T) {
	st := makeServerType("cx11", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 1, 2, 20, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client)

	_, _ = p.List(context.Background(), nil)
	_, _ = p.List(context.Background(), nil)

	if client.calls != 1 {
		t.Errorf("expected 1 API call (cache should be hit on second call), got %d", client.calls)
	}
}

func TestList_ReflectsUnavailable(t *testing.T) {
	st := makeServerType("cx11", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 1, 2, 20, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client)

	// Before marking: both offerings (nbg1, fsn1) must be available.
	before, err := p.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected 1 instance type, got %d", len(before))
	}
	for _, o := range before[0].Offerings {
		zone := o.Requirements.Get(corev1.LabelTopologyZone).Any()
		if !o.Available {
			t.Errorf("before mark: offering %s should be available", zone)
		}
	}

	// Mark cx11/nbg1 unavailable.
	p.MarkUnavailable("cx11", "nbg1")

	// After marking: nbg1 offering must be unavailable, fsn1 must remain available.
	after, err := p.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("expected 1 instance type after mark, got %d", len(after))
	}
	for _, o := range after[0].Offerings {
		zone := o.Requirements.Get(corev1.LabelTopologyZone).Any()
		switch zone {
		case "nbg1":
			if o.Available {
				t.Errorf("offering nbg1 should be unavailable after MarkUnavailable")
			}
		case "fsn1":
			if !o.Available {
				t.Errorf("offering fsn1 should still be available (different location)")
			}
		default:
			t.Errorf("unexpected zone %q in offerings", zone)
		}
	}

	// Verify the original cached structs are NOT mutated (defensive copy check).
	for _, o := range before[0].Offerings {
		zone := o.Requirements.Get(corev1.LabelTopologyZone).Any()
		if zone == "nbg1" && !o.Available {
			t.Error("cached struct was mutated: original nbg1 offering Available should still be true")
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

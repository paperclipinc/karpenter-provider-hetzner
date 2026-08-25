package instancetype

import (
	"context"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/operator"
)

// measuredNodes records what real Hetzner servers actually report once booted
// and registered, captured from running clusters. Hetzner's advertised size is
// not what the guest sees: the kernel and firmware reserve a slice that grows
// with RAM, so a cx53 advertised as 32Gi registers 31337Mi of capacity. On top
// of that the kubelet subtracts whatever the bootstrap reserved.
//
// These numbers are the reason this package cannot compute allocatable from the
// advertised size alone, and the reason the test below asserts an inequality
// rather than an exact figure: over-reporting allocatable makes Karpenter pick a
// node the pod cannot fit on, which strands the pod and churns the node forever.
// Under-reporting only costs money.
var measuredNodes = []struct {
	name              string
	cores             int
	memGB             float32
	realCapacityKi    int64
	realAllocatableKi int64
}{
	{"cpx22", 2, 4, 3905948, 2447772},
	{"cpx32", 4, 8, 7931612, 6473436},
	{"cx33", 4, 8, 7937224, 6479048},
	{"cx43", 8, 16, 15988560, 14530384},
	{"cx53", 16, 32, 32089152, 30630976},
}

// locationsNodeClass is a node class that only constrains locations, for tests
// about filtering rather than allocatable.
func locationsNodeClass(locations ...string) *apiv1.HCloudNodeClass {
	return &apiv1.HCloudNodeClass{Spec: apiv1.HCloudNodeClassSpec{Locations: locations}}
}

// k3sNodeClass mirrors the kubelet reservations these clusters' bootstrap sets:
// system-reserved and kube-reserved at 512Mi/200m each, plus a 400Mi hard
// eviction threshold. 1424Mi and 400m in total, on every server type.
func k3sNodeClass() *apiv1.HCloudNodeClass {
	return &apiv1.HCloudNodeClass{
		Spec: apiv1.HCloudNodeClassSpec{
			Kubelet: &apiv1.KubeletConfiguration{
				SystemReserved: map[string]string{"cpu": "200m", "memory": "512Mi"},
				KubeReserved:   map[string]string{"cpu": "200m", "memory": "512Mi"},
				EvictionHard:   map[string]string{"memory.available": "400Mi"},
			},
		},
	}
}

// TestAllocatable_NeverExceedsRegisteredNode is the regression for the churn
// loop: Karpenter sized a node from an allocatable figure larger than the
// machine's real capacity, the pod stayed Pending, the empty node was reclaimed,
// and provisioning repeated indefinitely.
func TestAllocatable_NeverExceedsRegisteredNode(t *testing.T) {
	for _, tc := range measuredNodes {
		t.Run(tc.name, func(t *testing.T) {
			st := makeServerType(tc.name, hcloud.ArchitectureX86, hcloud.CPUTypeShared, tc.cores, tc.memGB, 80, testPricings)
			client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
			p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)

			types, err := p.List(context.Background(), k3sNodeClass())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(types) != 1 {
				t.Fatalf("expected 1 instance type, got %d", len(types))
			}

			alloc := types[0].Allocatable()
			gotMem := alloc[corev1.ResourceMemory]
			realMem := tc.realAllocatableKi * 1024
			if gotMem.Value() > realMem {
				t.Errorf("memory allocatable %dMi exceeds what the node registers (%dMi): overestimates by %dMi",
					gotMem.Value()/1024/1024, realMem/1024/1024, (gotMem.Value()-realMem)/1024/1024)
			}

			gotCPU := alloc[corev1.ResourceCPU]
			realCPU := int64(tc.cores)*1000 - 400
			if gotCPU.MilliValue() > realCPU {
				t.Errorf("cpu allocatable %dm exceeds what the node registers (%dm)", gotCPU.MilliValue(), realCPU)
			}
		})
	}
}

// TestAllocatable_WithinBudgetOfRegisteredNode guards the other direction. The
// estimate must be conservative, but an estimate far below reality silently
// buys larger servers than the workload needs, so hold it to a bounded shortfall.
func TestAllocatable_WithinBudgetOfRegisteredNode(t *testing.T) {
	const maxShortfallMi = 1200

	for _, tc := range measuredNodes {
		t.Run(tc.name, func(t *testing.T) {
			st := makeServerType(tc.name, hcloud.ArchitectureX86, hcloud.CPUTypeShared, tc.cores, tc.memGB, 80, testPricings)
			client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
			p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)

			types, err := p.List(context.Background(), k3sNodeClass())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			gotMem := types[0].Allocatable()[corev1.ResourceMemory]
			shortfallMi := (tc.realAllocatableKi*1024 - gotMem.Value()) / 1024 / 1024
			if shortfallMi > maxShortfallMi {
				t.Errorf("memory allocatable %dMi understates the node by %dMi (budget %dMi)",
					gotMem.Value()/1024/1024, shortfallMi, maxShortfallMi)
			}
		})
	}
}

// TestAllocatable_NoKubeletConfigKeepsLegacyReservation pins the upgrade path.
//
// Before this package read reservations from the node class it subtracted a flat
// 100m/100Mi from every type. A node class that declares no kubelet block has
// said nothing about its bootstrap, so silently dropping that to zero would raise
// advertised CPU on upgrade -- the wrong direction, and invisible. Absent means
// "unchanged", not "nothing reserved".
func TestAllocatable_NoKubeletConfigKeepsLegacyReservation(t *testing.T) {
	st := makeServerType("cx23", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 2, 4, 40, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)

	types, err := p.List(context.Background(), &apiv1.HCloudNodeClass{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cpu := types[0].Allocatable()[corev1.ResourceCPU]
	if cpu.MilliValue() != 1900 {
		t.Errorf("expected 1900m (2 cores less the legacy 100m reserve), got %dm", cpu.MilliValue())
	}
}

// A node class that does declare a kubelet block is taken at its word: the
// legacy default must not be added on top of what the operator stated.
func TestAllocatable_DeclaredKubeletOverridesLegacyDefault(t *testing.T) {
	st := makeServerType("cx23", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 2, 4, 40, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)

	nc := &apiv1.HCloudNodeClass{
		Spec: apiv1.HCloudNodeClassSpec{
			Kubelet: &apiv1.KubeletConfiguration{
				KubeReserved: map[string]string{"cpu": "250m"},
			},
		},
	}
	types, err := p.List(context.Background(), nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cpu := types[0].Allocatable()[corev1.ResourceCPU]
	if cpu.MilliValue() != 1750 {
		t.Errorf("expected 1750m (2 cores less the declared 250m), got %dm", cpu.MilliValue())
	}
}

// TestAllocatable_NoKubeletConfig falls back to advertised-minus-VM-overhead
// when the NodeClass declares no reservations. The result must still not exceed
// the machine's real capacity, since the VM overhead applies regardless.
func TestAllocatable_NoKubeletConfig(t *testing.T) {
	tc := measuredNodes[4] // cx53
	st := makeServerType(tc.name, hcloud.ArchitectureX86, hcloud.CPUTypeShared, tc.cores, tc.memGB, 80, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)

	types, err := p.List(context.Background(), &apiv1.HCloudNodeClass{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotMem := types[0].Allocatable()[corev1.ResourceMemory]
	if gotMem.Value() > tc.realCapacityKi*1024 {
		t.Errorf("memory allocatable %dMi exceeds real capacity %dMi with no kubelet config",
			gotMem.Value()/1024/1024, tc.realCapacityKi/1024)
	}
}

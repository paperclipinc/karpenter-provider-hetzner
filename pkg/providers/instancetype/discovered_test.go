package instancetype

import (
	"context"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/operator"
)

func imageNodeClass(images ...apiv1.ResolvedImage) *apiv1.HCloudNodeClass {
	return &apiv1.HCloudNodeClass{Status: apiv1.HCloudNodeClassStatus{ResolvedImages: images}}
}

func TestDiscovered_RecordThenGet(t *testing.T) {
	c := newDiscoveredCapacityCache()
	nc := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 42})

	if _, ok := c.get("cx53", nc); ok {
		t.Fatal("expected no entry before recording")
	}
	if changed := c.record("cx53", nc, resource.MustParse("31337Mi")); !changed {
		t.Error("first observation should report a change")
	}
	got, ok := c.get("cx53", nc)
	if !ok {
		t.Fatal("expected an entry after recording")
	}
	if got.String() != "31337Mi" {
		t.Errorf("expected 31337Mi, got %s", got.String())
	}
}

// A larger observation must not raise the cached figure. Nodes of one type vary
// slightly, and raising it would hand pods memory some nodes of that type do not
// have.
func TestDiscovered_KeepsSmallest(t *testing.T) {
	c := newDiscoveredCapacityCache()
	nc := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 42})

	c.record("cx53", nc, resource.MustParse("31337Mi"))

	if changed := c.record("cx53", nc, resource.MustParse("31400Mi")); changed {
		t.Error("a larger observation should not change the cache")
	}
	got, _ := c.get("cx53", nc)
	if got.String() != "31337Mi" {
		t.Errorf("larger observation overwrote the cache: got %s", got.String())
	}

	if changed := c.record("cx53", nc, resource.MustParse("31300Mi")); !changed {
		t.Error("a smaller observation should change the cache")
	}
	got, _ = c.get("cx53", nc)
	if got.String() != "31300Mi" {
		t.Errorf("expected the smaller value to win, got %s", got.String())
	}
}

// The advertised-to-visible gap is set by the guest kernel, so a measurement
// taken on one image says nothing about another.
func TestDiscovered_ScopedToImage(t *testing.T) {
	c := newDiscoveredCapacityCache()
	oldImage := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 42})
	newImage := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 99})

	c.record("cx53", oldImage, resource.MustParse("31337Mi"))

	if _, ok := c.get("cx53", newImage); ok {
		t.Error("a measurement from one image must not apply to another")
	}
	if _, ok := c.get("cx53", oldImage); !ok {
		t.Error("the original image should still resolve")
	}
}

// Resolved images are a set, not a sequence: the same images listed in a
// different order are the same image.
func TestDiscovered_ImageOrderIrrelevant(t *testing.T) {
	c := newDiscoveredCapacityCache()
	a := imageNodeClass(
		apiv1.ResolvedImage{Architecture: "x86", ImageID: 42},
		apiv1.ResolvedImage{Architecture: "arm", ImageID: 43},
	)
	b := imageNodeClass(
		apiv1.ResolvedImage{Architecture: "arm", ImageID: 43},
		apiv1.ResolvedImage{Architecture: "x86", ImageID: 42},
	)

	c.record("cx53", a, resource.MustParse("31337Mi"))
	if _, ok := c.get("cx53", b); !ok {
		t.Error("reordering the same images produced a different key")
	}
}

func TestDiscovered_RejectsNonPositive(t *testing.T) {
	c := newDiscoveredCapacityCache()
	nc := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 42})

	for _, bad := range []string{"0", "-1Mi"} {
		if changed := c.record("cx53", nc, resource.MustParse(bad)); changed {
			t.Errorf("observation %q should be rejected", bad)
		}
		if _, ok := c.get("cx53", nc); ok {
			t.Errorf("observation %q was stored", bad)
		}
	}
}

// The point of the whole mechanism: a measured node overrides the estimate.
func TestDiscovered_OverridesEstimateInList(t *testing.T) {
	st := makeServerType("cx53", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 16, 32, 320, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)
	nc := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 42})

	before, err := p.List(context.Background(), nc)
	if err != nil {
		t.Fatal(err)
	}
	estimated := before[0].Capacity[corev1.ResourceMemory]
	// 32Gi less 7.5% is ~30310Mi, short of the 31337Mi a real cx53 reports.
	if estimated.Value() >= 31337*1024*1024 {
		t.Fatalf("expected the estimate to undershoot reality, got %s", estimated.String())
	}

	p.Record("cx53", nc, resource.MustParse("31337Mi"))

	after, err := p.List(context.Background(), nc)
	if err != nil {
		t.Fatal(err)
	}
	measured := after[0].Capacity[corev1.ResourceMemory]
	if measured.Value() != 31337*1024*1024 {
		t.Errorf("expected the measured capacity to be used, got %s", measured.String())
	}
}

// A measurement recorded under one node class must not leak into another whose
// images differ, even though both share the catalogue cache.
func TestDiscovered_DoesNotLeakAcrossNodeClasses(t *testing.T) {
	st := makeServerType("cx53", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 16, 32, 320, testPricings)
	client := &mockServerTypeClient{types: []*hcloud.ServerType{st}}
	p := NewProvider(client, operator.DefaultVMMemoryOverheadPercent)

	measured := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 42})
	other := imageNodeClass(apiv1.ResolvedImage{Architecture: "x86", ImageID: 99})

	p.Record("cx53", measured, resource.MustParse("31337Mi"))

	types, err := p.List(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	got := types[0].Capacity[corev1.ResourceMemory]
	if got.Value() == 31337*1024*1024 {
		t.Error("a measurement leaked into a node class with a different image")
	}
}

func TestEvictionThreshold(t *testing.T) {
	capacity := corev1.ResourceList{
		corev1.ResourceMemory:           resource.MustParse("32Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
	}

	tests := []struct {
		name       string
		hard       map[string]string
		wantMemory string
	}{
		{name: "absent uses kubelet default", hard: nil, wantMemory: "100Mi"},
		{name: "explicit quantity", hard: map[string]string{"memory.available": "400Mi"}, wantMemory: "400Mi"},
		{name: "percentage of capacity", hard: map[string]string{"memory.available": "10%"}, wantMemory: "3435973836"},
		{name: "unparseable yields nothing", hard: map[string]string{"memory.available": "banana"}, wantMemory: ""},
		{name: "empty falls back to default", hard: map[string]string{"memory.available": ""}, wantMemory: "100Mi"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evictionThreshold(&apiv1.KubeletConfiguration{EvictionHard: tc.hard}, capacity)
			q, ok := got[corev1.ResourceMemory]
			if tc.wantMemory == "" {
				if ok {
					t.Errorf("expected no memory threshold, got %s", q.String())
				}
				return
			}
			if !ok {
				t.Fatal("expected a memory threshold")
			}
			if q.String() != tc.wantMemory {
				t.Errorf("expected %s, got %s", tc.wantMemory, q.String())
			}
		})
	}
}

// A negative or malformed reservation must be dropped rather than treated as
// zero, so a typo cannot silently remove a reservation the node really applies.
func TestParseResourceList_DropsInvalid(t *testing.T) {
	got := parseResourceList(map[string]string{
		"cpu":    "200m",
		"memory": "-512Mi",
		"pid":    "not-a-quantity",
	})
	if _, ok := got[corev1.ResourceMemory]; ok {
		t.Error("negative memory reservation was kept")
	}
	if _, ok := got["pid"]; ok {
		t.Error("unparseable reservation was kept")
	}
	cpu, ok := got[corev1.ResourceCPU]
	if !ok || cpu.MilliValue() != 200 {
		t.Errorf("valid cpu reservation was lost: %v", got)
	}
}

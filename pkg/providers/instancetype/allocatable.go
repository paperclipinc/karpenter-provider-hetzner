package instancetype

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
)

// Kubelet's own defaults, applied when the node class declares no eviction
// thresholds. The kubelet always holds something back even when nothing is
// configured, so modelling zero would overstate what a pod can use.
const (
	defaultMemoryEvictionThreshold = "100Mi"
	defaultNodefsEvictionThreshold = "10%"
)

// overheadFor converts a node class's declared kubelet reservations into the
// overhead Karpenter subtracts from capacity. Karpenter's own formula is
// allocatable = capacity - (kubeReserved + systemReserved + evictionThreshold),
// so every one of those three has to be filled in for allocatable to match what
// the node will actually report.
//
// capacity is required because eviction thresholds may be expressed as a
// percentage of it.
func overheadFor(nodeClass *apiv1.HCloudNodeClass, capacity corev1.ResourceList) *cloudprovider.InstanceTypeOverhead {
	var kubelet *apiv1.KubeletConfiguration
	if nodeClass != nil {
		kubelet = nodeClass.Spec.Kubelet
	}

	overhead := &cloudprovider.InstanceTypeOverhead{
		EvictionThreshold: evictionThreshold(kubelet, capacity),
	}
	if kubelet == nil {
		// A node class that declares nothing has said nothing about its bootstrap,
		// which is not the same as saying it reserves nothing. Before this package
		// read reservations from the node class it subtracted a flat 100m/100Mi
		// from every type; keeping that as the undeclared default means upgrading
		// cannot silently raise a node's advertised capacity, which would push pods
		// onto machines that never had room for them.
		overhead.KubeReserved = legacyDefaultKubeReserved()
		return overhead
	}
	overhead.KubeReserved = parseResourceList(kubelet.KubeReserved)
	overhead.SystemReserved = parseResourceList(kubelet.SystemReserved)
	return overhead
}

// legacyDefaultKubeReserved is what this provider reserved on every server type
// before reservations became declarable. It applies only when a node class omits
// the kubelet block entirely; one that declares a block is taken at its word.
func legacyDefaultKubeReserved() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("100Mi"),
	}
}

// evictionThreshold models the memory and disk the kubelet holds back to keep
// itself above its hard eviction signals. That headroom is unavailable to pods,
// so it reduces allocatable exactly as a reservation does.
//
// Only signals that move a resource Karpenter schedules on are translated:
// memory.available and nodefs.available. inodesFree and pid.available are
// accepted by the API but have no allocatable equivalent.
func evictionThreshold(kubelet *apiv1.KubeletConfiguration, capacity corev1.ResourceList) corev1.ResourceList {
	var hard map[string]string
	if kubelet != nil {
		hard = kubelet.EvictionHard
	}

	signal := func(name, fallback string) string {
		if v, ok := hard[name]; ok && strings.TrimSpace(v) != "" {
			return v
		}
		return fallback
	}

	threshold := corev1.ResourceList{}
	if q, ok := resolveThreshold(signal("memory.available", defaultMemoryEvictionThreshold), capacity[corev1.ResourceMemory]); ok {
		threshold[corev1.ResourceMemory] = q
	}
	if q, ok := resolveThreshold(signal("nodefs.available", defaultNodefsEvictionThreshold), capacity[corev1.ResourceEphemeralStorage]); ok {
		threshold[corev1.ResourceEphemeralStorage] = q
	}
	return threshold
}

// resolveThreshold reads an eviction threshold, which the kubelet accepts either
// as a quantity ("400Mi") or as a percentage of the resource's capacity ("10%").
// An unparseable value yields no threshold rather than a zero one, so a typo
// cannot quietly hand pods memory the kubelet is holding back.
func resolveThreshold(raw string, capacity resource.Quantity) (resource.Quantity, bool) {
	raw = strings.TrimSpace(raw)
	if pct, ok := strings.CutSuffix(raw, "%"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil || f < 0 || f > 100 {
			return resource.Quantity{}, false
		}
		return *resource.NewQuantity(int64(float64(capacity.Value())*f/100), resource.BinarySI), true
	}
	q, err := resource.ParseQuantity(raw)
	if err != nil || q.Sign() < 0 {
		return resource.Quantity{}, false
	}
	return q, true
}

// parseResourceList converts the node class's string-keyed reservations into a
// ResourceList. Values that do not parse are dropped: the CEL rules on the CRD
// reject them at admission, so anything reaching here came in some other way and
// is safer ignored than treated as zero.
func parseResourceList(in map[string]string) corev1.ResourceList {
	if len(in) == 0 {
		return nil
	}
	out := corev1.ResourceList{}
	for k, v := range in {
		q, err := resource.ParseQuantity(strings.TrimSpace(v))
		if err != nil || q.Sign() < 0 {
			continue
		}
		out[corev1.ResourceName(k)] = q
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// memoryWithVMOverhead reduces a server type's advertised memory to what the
// guest is expected to actually see. See operator.DefaultVMMemoryOverheadPercent
// for why the gap exists and why it is a fraction rather than a constant.
func memoryWithVMOverhead(advertisedBytes int64, overheadPercent float64) int64 {
	if overheadPercent <= 0 {
		return advertisedBytes
	}
	return int64(float64(advertisedBytes) * (1 - overheadPercent))
}

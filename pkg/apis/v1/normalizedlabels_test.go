package v1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

func TestCSILocationNormalizedToTopologyZone(t *testing.T) {
	// A requirement built from the hcloud CSI domain must normalize to the
	// standard zone label while preserving the location value, so that PV
	// nodeAffinity like "csi.hetzner.cloud/location In [nbg1]" constrains node
	// provisioning the same way "topology.kubernetes.io/zone In [nbg1]" does.
	req := scheduling.NewRequirement(LabelCSILocation, corev1.NodeSelectorOpIn, "nbg1")

	if req.Key != corev1.LabelTopologyZone {
		t.Fatalf("requirement key = %q, want %q (csi.hetzner.cloud/location must normalize to the standard zone label)",
			req.Key, corev1.LabelTopologyZone)
	}
	if req.Any() != "nbg1" {
		t.Errorf("requirement value = %q, want %q (location value must be preserved)", req.Any(), "nbg1")
	}
}

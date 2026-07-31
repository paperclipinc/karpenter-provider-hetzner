package v1

import (
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// The hcloud CSI driver pins PersistentVolume nodeAffinity (and reports CSINode
// topology) on the domain csi.hetzner.cloud/location. The driver deliberately
// uses this key and reverted from the standard topology labels
// (hetznercloud/csi-driver#333), so it cannot be changed upstream.
//
// Karpenter only recognizes the standard topology.kubernetes.io/zone, so
// without this alias it rejects every pod that carries an hcloud PVC with
// "label csi.hetzner.cloud/location does not have known values". Alias the CSI
// domain to the standard zone label that this provider already stamps on its
// offerings (pkg/providers/instancetype), so PV nodeAffinity constrains node
// provisioning. This mirrors how karpenter-provider-aws aliases
// topology.ebs.csi.aws.com/zone.
//
// It is safe without value remapping: the hcloud CSI driver reports the same
// location names (nbg1, fsn1, hel1, ...) that this provider derives from the
// hcloud pricing API.
func init() {
	karpv1.NormalizedLabels[LabelCSILocation] = corev1.LabelTopologyZone
}

package v1

import (
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// Register this provider's custom node labels as Karpenter well-known labels so
// they are tolerated by instance-type compatibility checks (a NodeClaim need not
// explicitly request them). Without this, Create rejects every instance type.
func init() {
	karpv1.WellKnownLabels = karpv1.WellKnownLabels.Insert(
		LabelCPUType,
		LabelServerFamily,
	)
}

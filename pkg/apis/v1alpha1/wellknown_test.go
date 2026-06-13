package v1alpha1

import (
	"testing"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestCustomLabelsRegisteredAsWellKnown(t *testing.T) {
	for _, l := range []string{LabelCPUType, LabelServerFamily} {
		if !karpv1.WellKnownLabels.Has(l) {
			t.Errorf("label %q must be registered in karpv1.WellKnownLabels", l)
		}
	}
}

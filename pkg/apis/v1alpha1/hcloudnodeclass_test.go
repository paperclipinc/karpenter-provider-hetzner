package v1alpha1

import "testing"

func TestStatusConditionsIncludeDependents(t *testing.T) {
	nc := &HCloudNodeClass{}
	cs := nc.StatusConditions()
	for _, ct := range []string{ConditionTypeImagesReady, ConditionTypeNetworkReady} {
		if cs.Get(ct) == nil {
			t.Errorf("expected condition %q to be registered", ct)
		}
	}
}

func TestPublicIPDefaultsHelper(t *testing.T) {
	nc := &HCloudNodeClass{}
	if !nc.Spec.PublicIPv4Enabled() {
		t.Error("nil EnablePublicIPv4 should default to true")
	}
	off := false
	nc.Spec.EnablePublicIPv4 = &off
	if nc.Spec.PublicIPv4Enabled() {
		t.Error("explicit false should disable public IPv4")
	}

	on := true
	nc.Spec.EnablePublicIPv4 = &on
	if !nc.Spec.PublicIPv4Enabled() {
		t.Error("explicit true should enable public IPv4")
	}
}

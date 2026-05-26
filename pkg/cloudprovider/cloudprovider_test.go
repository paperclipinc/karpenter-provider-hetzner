package cloudprovider_test

import (
	"testing"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/cloudprovider"
)

func newTestCloudProvider() *cloudprovider.CloudProvider {
	return cloudprovider.NewCloudProvider(nil, nil, nil, nil)
}

func TestName(t *testing.T) {
	cp := newTestCloudProvider()
	if got := cp.Name(); got != "hetzner" {
		t.Errorf("Name() = %q, want %q", got, "hetzner")
	}
}

func TestGetSupportedNodeClasses(t *testing.T) {
	cp := newTestCloudProvider()
	classes := cp.GetSupportedNodeClasses()
	if len(classes) != 1 {
		t.Fatalf("GetSupportedNodeClasses() returned %d classes, want 1", len(classes))
	}
	if _, ok := classes[0].(*v1alpha1.HCloudNodeClass); !ok {
		t.Errorf("GetSupportedNodeClasses()[0] is not *v1alpha1.HCloudNodeClass")
	}
}

func TestRepairPolicies(t *testing.T) {
	cp := newTestCloudProvider()
	policies := cp.RepairPolicies()
	if len(policies) != 2 {
		t.Fatalf("RepairPolicies() returned %d policies, want 2", len(policies))
	}
}

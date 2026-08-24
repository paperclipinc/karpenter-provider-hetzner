package operator

import "testing"

func TestLoadConfig_RequiresClusterName(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected error when CLUSTER_NAME is unset")
	}
}

func TestLoadConfig_ReadsClusterName(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClusterName != "paperclip-prod" {
		t.Errorf("got cluster name %q, want paperclip-prod", cfg.ClusterName)
	}
}

// The sweep must be on unless an operator deliberately turns it off.
func TestLoadConfig_InstanceGarbageCollectionDefaultsOn(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	for _, v := range []string{"", "0", "false", "no", "off", " FALSE "} {
		t.Setenv("DISABLE_INSTANCE_GARBAGE_COLLECTION", v)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("value %q: unexpected error: %v", v, err)
		}
		if cfg.DisableInstanceGarbageCollection {
			t.Errorf("value %q disabled garbage collection; only an affirmative value should", v)
		}
	}
}

// An operator reaches for this flag during maintenance that removes NodeClaims
// wholesale. A typo that silently left the sweep running would reap the fleet
// they were protecting, so an unrecognised value must stop the operator starting
// rather than pick a default for them.
func TestLoadConfig_RejectsUnrecognisedGarbageCollectionValue(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	for _, v := range []string{"maybe", "TRUEISH", "disabled", "True!", "enable"} {
		t.Setenv("DISABLE_INSTANCE_GARBAGE_COLLECTION", v)
		if _, err := LoadConfig(); err == nil {
			t.Errorf("value %q was accepted; an unrecognised value must be rejected", v)
		}
	}
}

func TestLoadConfig_InstanceGarbageCollectionCanBeDisabled(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " true "} {
		t.Setenv("DISABLE_INSTANCE_GARBAGE_COLLECTION", v)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.DisableInstanceGarbageCollection {
			t.Errorf("value %q did not disable garbage collection", v)
		}
	}
}

func TestLoadConfig_RejectsInvalidClusterName(t *testing.T) {
	for _, bad := range []string{"has space", "slash/name", "comma,name", "töö"} {
		t.Setenv("CLUSTER_NAME", bad)
		if _, err := LoadConfig(); err == nil {
			t.Errorf("expected error for invalid cluster name %q", bad)
		}
	}
}

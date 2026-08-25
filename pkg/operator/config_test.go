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

func TestLoadConfig_InstanceGarbageCollectionModes(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	for raw, want := range map[string]GCMode{
		"":          GCEnabled, // unset reclaims, matching AWS and Azure
		"enabled":   GCEnabled,
		"observe":   GCObserve,
		"disabled":  GCDisabled,
		" OBSERVE ": GCObserve,
	} {
		t.Setenv("INSTANCE_GARBAGE_COLLECTION_MODE", raw)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("value %q: unexpected error: %v", raw, err)
		}
		if cfg.InstanceGarbageCollectionMode != want {
			t.Errorf("value %q gave mode %q, want %q", raw, cfg.InstanceGarbageCollectionMode, want)
		}
	}
}

// An operator reaches for this during maintenance that removes NodeClaims
// wholesale. A typo silently falling back to "enabled" would reap the fleet they
// were protecting, so an unrecognised value must stop the operator starting
// rather than pick a default for them.
func TestLoadConfig_RejectsUnrecognisedGarbageCollectionMode(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	for _, v := range []string{"true", "false", "off", "dry-run", "dryrun", "observer", "Enabled!"} {
		t.Setenv("INSTANCE_GARBAGE_COLLECTION_MODE", v)
		if _, err := LoadConfig(); err == nil {
			t.Errorf("value %q was accepted; an unrecognised value must be rejected", v)
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

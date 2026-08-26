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

func TestLoadConfig_RejectsInvalidClusterName(t *testing.T) {
	for _, bad := range []string{"has space", "slash/name", "comma,name", "töö"} {
		t.Setenv("CLUSTER_NAME", bad)
		if _, err := LoadConfig(); err == nil {
			t.Errorf("expected error for invalid cluster name %q", bad)
		}
	}
}

func TestLoadConfig_VMMemoryOverheadPercent(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    float64
		wantErr bool
	}{
		{name: "unset uses default", env: "", want: 0.075},
		{name: "explicit value", env: "0.1", want: 0.1},
		{name: "zero is allowed", env: "0", want: 0},
		{name: "negative rejected", env: "-0.1", wantErr: true},
		{name: "one rejected", env: "1", wantErr: true},
		{name: "above one rejected", env: "1.5", wantErr: true},
		{name: "non-numeric rejected", env: "7.5%", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLUSTER_NAME", "test-cluster")
			if tc.env != "" {
				t.Setenv("VM_MEMORY_OVERHEAD_PERCENT", tc.env)
			}
			cfg, err := LoadConfig()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got config %+v", tc.env, cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.VMMemoryOverheadPercent != tc.want {
				t.Errorf("expected %v, got %v", tc.want, cfg.VMMemoryOverheadPercent)
			}
		})
	}
}

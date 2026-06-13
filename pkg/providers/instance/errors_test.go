package instance

import (
	"errors"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func TestMapCreateError(t *testing.T) {
	cases := []struct {
		name         string
		in           error
		wantInsuffic bool
		wantNil      bool
	}{
		{"nil", nil, false, true},
		{"unavailable", hcloud.Error{Code: hcloud.ErrorCodeResourceUnavailable}, true, false},
		{"resource-limit", hcloud.Error{Code: hcloud.ErrorCodeResourceLimitExceeded}, true, false},
		{"rate-limit", hcloud.Error{Code: hcloud.ErrorCodeRateLimitExceeded}, false, false},
		{"unsupported-location", hcloud.Error{Code: hcloud.ErrorCodeInvalidInput, Message: "unsupported location for server type"}, true, false},
		{"other-invalid-input", hcloud.Error{Code: hcloud.ErrorCodeInvalidInput, Message: "server name is invalid"}, false, false},
		{"other", errors.New("boom"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapCreateError(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %v", got)
				}
				return
			}
			if karpcp.IsInsufficientCapacityError(got) != tc.wantInsuffic {
				t.Errorf("IsInsufficientCapacityError=%v, want %v (err=%v)",
					karpcp.IsInsufficientCapacityError(got), tc.wantInsuffic, got)
			}
		})
	}
}

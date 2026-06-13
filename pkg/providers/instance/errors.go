package instance

import (
	"fmt"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// MapCreateError converts a Hetzner create error into the appropriate Karpenter
// error type. Capacity/quota errors become InsufficientCapacityError so the
// scheduler tries another instance type or zone; nil passes through as nil.
func MapCreateError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case hcloud.IsError(err, hcloud.ErrorCodeResourceUnavailable),
		hcloud.IsError(err, hcloud.ErrorCodeResourceLimitExceeded):
		return karpcp.NewInsufficientCapacityError(err)
	default:
		// Rate limits and transient errors stay as ordinary errors so Karpenter
		// requeues and retries the same offering.
		return fmt.Errorf("creating server: %w", err)
	}
}

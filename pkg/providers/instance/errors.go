package instance

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// MapCreateError converts a Hetzner create error into the appropriate Karpenter
// error type. Errors that mean "this server type cannot be created in this
// location right now" become InsufficientCapacityError so the scheduler marks
// the offering unavailable and falls back to another type/zone instead of
// retrying the same doomed combination. nil passes through as nil.
func MapCreateError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case hcloud.IsError(err, hcloud.ErrorCodeResourceUnavailable),
		hcloud.IsError(err, hcloud.ErrorCodeResourceLimitExceeded):
		return karpcp.NewInsufficientCapacityError(err)
	case isUnsupportedPlacement(err):
		// e.g. "unsupported location for server type" (invalid_input): the type
		// is priced but not creatable in this location. Treat it as an
		// unavailable offering so Karpenter routes around it.
		return karpcp.NewInsufficientCapacityError(err)
	default:
		// Rate limits and transient errors stay as ordinary errors so Karpenter
		// requeues and retries the same offering.
		return fmt.Errorf("creating server: %w", err)
	}
}

// isUnsupportedPlacement reports whether the error is Hetzner rejecting a
// (server type, location) combination as not creatable. Hetzner returns this as
// a generic invalid_input, so we match the placement-specific message rather
// than treating every invalid_input as a capacity signal.
func isUnsupportedPlacement(err error) bool {
	var he hcloud.Error
	if !errors.As(err, &he) || he.Code != hcloud.ErrorCodeInvalidInput {
		return false
	}
	m := strings.ToLower(he.Message)
	return strings.Contains(m, "unsupported location") ||
		strings.Contains(m, "location for server type")
}

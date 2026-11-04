//go:build !linux

package worktreereclaim

import (
	"context"
)

// CheckProcessUse deliberately holds on platforms without complete supported
// inspection. CWD-only platform helpers are not removal authorization.
func CheckProcessUse(context.Context, []string) error {
	return ErrProcessUseUnsupported
}

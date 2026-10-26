//go:build !linux

package worktreereclaim

import (
	"context"
	"errors"
)

// CheckProcessUse deliberately holds on platforms without complete supported
// inspection. CWD-only platform helpers are not removal authorization.
func CheckProcessUse(context.Context, []string) error {
	return errors.New("recovery process-use inspection unsupported on this platform")
}

//go:build !linux && !darwin && !windows

package daemon

import "errors"

// renameNoReplace has no single-call form on this platform, and a check
// followed by a rename could replace a file created in between, so it renames
// nothing.
func renameNoReplace(_, _ string) error {
	return errors.ErrUnsupported
}

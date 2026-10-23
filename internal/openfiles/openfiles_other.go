//go:build !windows

package openfiles

import (
	"context"
	"errors"
)

// takeLazy is only reached on windows; macOS and other Unixes Take shells out to lsof.
func takeLazy(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("openfiles: lazy snapshots exist only on windows")
}

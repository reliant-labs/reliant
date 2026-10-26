//go:build !linux

package worktreereclaim

import (
	"errors"
	"os"
)

func activityOpenRegular(string) (*os.File, error) {
	return nil, errors.New("safe activity file inspection unsupported on this platform")
}

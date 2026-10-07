// Copyright (c) 2025 Reliant Labs
//go:build windows

package worktreereclaim

import (
	"context"
	"errors"
)

// ProcessCwds has no Windows implementation. Failing closed means worktrees on
// Windows are never auto-removed: they are reported held and the user cleans
// them up by hand.
func ProcessCwds(_ context.Context) ([]string, error) {
	return nil, errors.New("process working directories are not available on windows")
}

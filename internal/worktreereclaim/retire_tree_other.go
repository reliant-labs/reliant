//go:build !linux

package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"time"
)

func retirementMatchesPath(*retirementTree, string) (bool, error) {
	return false, errors.New("mount-safe retirement unsupported")
}

type retirementTree struct {
	bytes  int64
	newest time.Time
}

func inspectRetirementTree(context.Context, string) (*retirementTree, error) {
	return nil, errors.New("mount-safe retirement unsupported on this platform")
}
func (t *retirementTree) close()                      {}
func (t *retirementTree) inspectionFiles() []*os.File { return nil }
func (t *retirementTree) validate(context.Context) error {
	return errors.New("mount-safe retirement unsupported")
}
func (t *retirementTree) remove(context.Context, func() error) error {
	return errors.New("mount-safe retirement unsupported")
}

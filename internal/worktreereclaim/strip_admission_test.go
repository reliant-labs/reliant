package worktreereclaim

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type stripTestLease struct {
	check   func() error
	release func()
}

func (l stripTestLease) Check(context.Context) error { return l.check() }
func (l stripTestLease) Release()                    { l.release() }

func TestRetirementRequiresAdmissionAndCompleteInspection(t *testing.T) {
	for _, fail := range []string{"missing-admission", "busy-admission", "missing-inspection", "unsupported", "open-file", "expired"} {
		t.Run(fail, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "node_modules", "keep")
			write(t, target, "untouched")
			released := false
			r := &Reclaimer{Cwds: func(context.Context) ([]string, error) { return nil, nil }, ProcessUse: func(context.Context, []string) error { return nil }}
			r.Acquire = func(context.Context, []string) (RecoveryLease, error) {
				return stripTestLease{check: func() error { return nil }, release: func() { released = true }}, nil
			}
			switch fail {
			case "missing-admission":
				r.Acquire = nil
			case "busy-admission":
				r.Acquire = func(context.Context, []string) (RecoveryLease, error) { return nil, errors.New("busy") }
			case "missing-inspection":
				r.ProcessUse = nil
			case "unsupported":
				r.ProcessUse = func(context.Context, []string) error { return ErrProcessUseUnsupported }
			case "open-file":
				r.ProcessUse = func(context.Context, []string) error { return errors.New("open fd/executable mapping") }
			case "expired":
				r.Acquire = func(context.Context, []string) (RecoveryLease, error) {
					return stripTestLease{check: func() error { return errors.New("expired") }, release: func() { released = true }}, nil
				}
			}
			got := r.retireWorkspace(context.Background(), Worktree{Path: root}, []string{root}, nil)
			if !exists(target) || got.Outcome != OutcomeHeld {
				t.Fatalf("unsafe retirement on %s: %+v", fail, got)
			}
			if fail == "unsupported" || fail == "open-file" || fail == "expired" {
				if !released {
					t.Fatal("admission leaked")
				}
			}
		})
	}
}

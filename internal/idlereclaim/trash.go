// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// TrashDir is the directory, at the top of each root the idle reclaim works
// in, that a removal moves its target into before deleting it. A rename is
// the one atomic step: a process that opens a path in the target after it
// fails, rather than seeing half of a node_modules. Its name starts with a
// dot, so neither the scratch nor the worktree discovery ever takes it for a
// directory of its own.
const TrashDir = ".idle-reclaim-trash"

// Lease is the daemon's admission lease over the paths a removal touches
// (daemonruntime.AcquireWorktreeRecovery): while it is held, new daemon work
// in them is refused. Check inspects the processes again and must pass
// immediately before the removal.
type admissionKey struct{}

type Lease interface {
	Check(ctx context.Context) error
	Release()
}

// underLease runs remove under the admission lease over guard, after the
// lease's process check passes, and releases it after.
func (r *Reclaimer) underLease(ctx context.Context, guard string, remove func() error) error {
	if admitted, _ := ctx.Value(admissionKey{}).(string); admitted == guard {
		return remove()
	}
	if r.Acquire == nil {
		return errors.New("no admission lease available")
	}
	lease, err := r.Acquire(ctx, []string{guard})
	if err != nil {
		return fmt.Errorf("not admitted: %w", err)
	}
	defer lease.Release()
	if err := lease.Check(ctx); err != nil {
		return fmt.Errorf("in use: %w", err)
	}
	return remove()
}

// keepingMtime runs change, which removes an entry of dir, and puts dir's
// modification time back: our own removal is not use of the directory, and
// without this every strip would restart the clock on removing it whole. The
// root itself is left alone.
func keepingMtime(dir, root string, change func() error) error {
	info, statErr := os.Stat(dir)
	if err := change(); err != nil {
		return err
	}
	if statErr == nil && filepath.Clean(dir) != filepath.Clean(root) {
		_ = os.Chtimes(dir, time.Time{}, info.ModTime())
	}
	return nil
}

// emptyTrash deletes what a removal moved into root's trash, including what
// an earlier pass left there when the daemon stopped mid-deletion.
func emptyTrash(ctx context.Context, root string) {
	trash := filepath.Join(root, TrashDir)
	entries, err := os.ReadDir(trash)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		p := filepath.Join(trash, e.Name())
		if err := os.RemoveAll(p); err != nil {
			logging.WarnContext(ctx, "[idlereclaim] Could not delete a trashed directory; the next pass retries", "path", p, "error", err)
		}
	}
	_ = os.Remove(trash)
}

// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// TrashDir is the directory, at the top of each root the idle reclaim works
// in, that a removal parks its target in before deleting it
// (worktreereclaim.Quarantine). The rename is the one atomic step: from then
// on no process can reach the target by its old name, so the final use
// check and the deletion that follows see every process that could use it.
// Each admission parks into a subdirectory of its own, which its lease
// covers. The name starts with a dot, so neither the scratch nor the
// worktree discovery ever takes it for a directory of its own.
const TrashDir = ".idle-reclaim-trash"

// leaseRenewAfter renews the admission lease before its two minutes end.
const leaseRenewAfter = 90 * time.Second

// Lease is the daemon's admission lease over the paths a removal touches
// (daemonruntime.AcquireWorktreeRecovery): while it is held, new daemon work
// in them is refused. Check inspects the processes again; it must pass
// before anything is removed and at every deletion boundary.
type Lease interface {
	Check(ctx context.Context) error
	Release()
}

type admissionKey struct{}

// admission is the lease one removal (or one scratch directory's pass) runs
// under: over guard, the directory whose use holds the removal, and trash,
// where it parks what it removes.
type admission struct {
	r            *Reclaimer
	guard, trash string
	lease        Lease
	acquired     time.Time
}

// check is the lease half of a deletion-boundary check: the lease is still
// held (renewed before it lapses), and its process check passes.
func (a *admission) check(ctx context.Context) error {
	if time.Since(a.acquired) > leaseRenewAfter {
		a.lease.Release()
		next, err := a.r.Acquire(ctx, []string{a.guard, a.trash})
		if err != nil || next == nil {
			a.lease = releasedLease{}
			return fmt.Errorf("not admitted again: %v", err)
		}
		a.lease, a.acquired = next, time.Now()
	}
	return a.lease.Check(ctx)
}

// releasedLease stands in for a lease that could not be renewed.
type releasedLease struct{}

func (releasedLease) Check(context.Context) error { return errors.New("admission lease released") }
func (releasedLease) Release()                    {}

var trashSeq atomic.Int64

// underLease runs remove under the admission lease over guard and a fresh
// trash subdirectory of root, after the lease's process check passes, and
// releases it after. A remove already running under guard's admission runs
// under it.
func (r *Reclaimer) underLease(ctx context.Context, guard, root string, remove func(context.Context) error) error {
	if a, _ := ctx.Value(admissionKey{}).(*admission); a != nil && a.guard == guard {
		return remove(ctx)
	}
	if r.Acquire == nil {
		return errors.New("no admission lease available")
	}
	trash := filepath.Join(root, TrashDir, strconv.FormatInt(time.Now().UnixNano(), 36)+"-"+strconv.FormatInt(trashSeq.Add(1), 36))
	lease, err := r.Acquire(ctx, []string{guard, trash})
	if err != nil {
		return fmt.Errorf("not admitted: %w", err)
	}
	if lease == nil {
		return errors.New("not admitted")
	}
	a := &admission{r: r, guard: guard, trash: trash, lease: lease, acquired: time.Now()}
	defer func() { a.lease.Release() }()
	defer func() { _ = os.Remove(trash) }() // empty unless a removal left something parked
	if err := lease.Check(ctx); err != nil {
		return fmt.Errorf("in use: %w", err)
	}
	return remove(context.WithValue(ctx, admissionKey{}, a))
}

// quarantine is how a removal under guard's admission (ctx) parks and
// deletes: content checks in authorize, then, once the target is parked, the
// lease and process checks over the target's paths and guard, at the start
// and at deletion boundaries.
func (r *Reclaimer) quarantine(ctx context.Context, guard string, authorize func(context.Context) error) worktreereclaim.Quarantine {
	a, _ := ctx.Value(admissionKey{}).(*admission)
	// The boundary checks are paced (BoundaryCheckEvery), as the worktree
	// reclaim's disposal paces them: a process scan per entry would make a
	// node_modules take hours. The target is parked by then, so only a
	// process that already held it can use it, and the check before the
	// first deletion sees those.
	q := worktreereclaim.Quarantine{Authorize: authorize, CheckEvery: r.BoundaryCheckEvery}
	if a == nil || a.guard != guard {
		return q // no Trash or InUse: the removal holds
	}
	q.Trash = a.trash
	q.InUse = func(ctx context.Context, paths []string) error {
		if err := a.check(ctx); err != nil {
			return err
		}
		return r.inUse(ctx, append(paths, guard)...)
	}
	return q
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

// recoverTrash puts back what an earlier removal left parked in root's
// trash and returns an entry for each tree it had to keep there. A tree
// stays parked when its deletion stopped (a process started using it, or it
// changed) and its path was taken again before it could go back, or when the
// daemon stopped mid-deletion. That is a hold, never a removal: nothing in
// the trash is deleted except empty directories. A tree goes back to the
// path its marker names (worktreereclaim.RestoreParked) once that path is
// free; one with no marker, or whose path stays taken, is kept and reported
// with where it belongs, for a person to recover.
func (r *Reclaimer) recoverTrash(ctx context.Context, root string) []Entry {
	if root == "" {
		return nil
	}
	trash := filepath.Join(root, TrashDir)
	batches, err := os.ReadDir(trash)
	if err != nil {
		return nil
	}
	var kept []Entry
	for _, b := range batches {
		if ctx.Err() != nil {
			return kept
		}
		batch := filepath.Join(trash, b.Name())
		entries, err := os.ReadDir(batch)
		if err != nil || !b.IsDir() {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue // a marker, read with its tree
			}
			parked := filepath.Join(batch, e.Name())
			original, err := worktreereclaim.RestoreParked(parked)
			if err == nil {
				logging.InfoContext(ctx, "[idlereclaim] Put a parked directory back", "path", original, "parked", parked)
				continue
			}
			reason := "kept parked, not deleted: " + err.Error()
			if original != "" {
				reason = fmt.Sprintf("kept parked, not deleted: it belongs at %s: %v", original, err)
			}
			logging.WarnContext(ctx, "[idlereclaim] A parked directory could not go back; kept for recovery", "parked", parked, "original", original, "error", err)
			kept = append(kept, Entry{Path: parked, Action: ActionHeld, Reason: reason})
		}
		_ = os.Remove(batch) // only when empty
	}
	_ = os.Remove(trash)
	return kept
}

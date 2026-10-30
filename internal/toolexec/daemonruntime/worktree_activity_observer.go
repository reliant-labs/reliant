package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

var runtimeActivity struct {
	sync.Mutex
	ctx      context.Context
	observer *worktreereclaim.ActivityObserver
	err      error
}

func initializeWorktreeActivity(ctx context.Context, dataDir string) {
	observer, err := NewWorktreeActivityObserver(dataDir)
	runtimeActivity.Lock()
	defer runtimeActivity.Unlock()
	runtimeActivity.ctx = ctx
	runtimeActivity.observer = observer
	runtimeActivity.err = err
}

// ObserveWorktreeRecovery starts a workspace's observer when none is running and
// returns fresh evidence. It never revives state from another daemon process.
// The command ctx limits inventory; watcher lifetime is the runtime's ctx.
//
// An observation ends on its own when its coverage is lost (a watcher
// overflow) or its workspace is deleted, releasing its inotify watches, so
// "not running" is asked of the observer, not remembered here: a remembered
// root was never observed again after an overflow, and a deleted one held
// its watcher until the daemon exited.
func ObserveWorktreeRecovery(ctx context.Context, root string) (worktreereclaim.RecoveryActivity, error) {
	canonical, err := canonicalAdmissionPath(root)
	if err != nil {
		return worktreereclaim.RecoveryActivity{}, err
	}
	runtimeActivity.Lock()
	if runtimeActivity.err != nil {
		err := runtimeActivity.err
		runtimeActivity.Unlock()
		return worktreereclaim.RecoveryActivity{}, err
	}
	if runtimeActivity.observer == nil || runtimeActivity.ctx == nil || runtimeActivity.ctx.Err() != nil {
		runtimeActivity.Unlock()
		return worktreereclaim.RecoveryActivity{}, errors.New("daemon activity observer unavailable")
	}
	observer := runtimeActivity.observer
	if !observer.Running(canonical) {
		err = observer.Start(runtimeActivity.ctx, canonical)
	}
	runtimeActivity.Unlock()
	if err != nil {
		return worktreereclaim.RecoveryActivity{}, err
	}
	return observer.Observe(ctx, canonical)
}

// ObserveWorktreeRecoveries combines registered sibling checkouts in a workspace
// group. The set itself is fenced by the aggregate epoch; adding/removing a repo
// cannot inherit the old workspace authority. Loose group-root files still need
// the capture inventory's eligibility check; they are never silently omitted.
func ObserveWorktreeRecoveries(ctx context.Context, paths []string) (worktreereclaim.RecoveryActivity, error) {
	if len(paths) == 0 {
		return worktreereclaim.RecoveryActivity{}, errors.New("registered checkout paths required")
	}
	canonical := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		p, err := canonicalAdmissionPath(path)
		if err != nil {
			return worktreereclaim.RecoveryActivity{}, err
		}
		if seen[p] {
			return worktreereclaim.RecoveryActivity{}, errors.New("duplicate checkout observation")
		}
		seen[p] = true
		canonical = append(canonical, p)
	}
	sort.Strings(canonical)
	evidence := make([]worktreereclaim.RecoveryActivity, 0, len(canonical))
	for _, path := range canonical {
		e, err := ObserveWorktreeRecovery(ctx, path)
		if err != nil {
			return worktreereclaim.RecoveryActivity{}, err
		}
		evidence = append(evidence, e)
	}
	return aggregateRecoveryActivity(canonical, evidence), nil
}
func aggregateRecoveryActivity(paths []string, evidence []worktreereclaim.RecoveryActivity) worktreereclaim.RecoveryActivity {
	aggregate := worktreereclaim.RecoveryActivity{Complete: len(evidence) > 0 && len(evidence) == len(paths)}
	h := sha256.New()
	for i, e := range evidence {
		if i >= len(paths) {
			aggregate.Complete = false
			break
		}
		fmt.Fprintf(h, "%d:%s:%d:%s;", len(paths[i]), paths[i], len(e.ObservationID), e.ObservationID)
		if e.Generation <= 0 || e.Generation > int64(^uint64(0)>>1)-aggregate.Generation {
			aggregate.Complete = false
		} else {
			aggregate.Generation += e.Generation
		}
		if !e.Complete || e.ObservationID == "" {
			aggregate.Complete = false
		}
		if e.ObservedSince.After(aggregate.ObservedSince) {
			aggregate.ObservedSince = e.ObservedSince
		}
		if e.LastActivity.After(aggregate.LastActivity) {
			aggregate.LastActivity = e.LastActivity
		}
		if aggregate.ObservedThrough.IsZero() || e.ObservedThrough.Before(aggregate.ObservedThrough) {
			aggregate.ObservedThrough = e.ObservedThrough
		}
	}
	aggregate.ObservationID = hex.EncodeToString(h.Sum(nil))
	return aggregate
}

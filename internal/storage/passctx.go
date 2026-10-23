package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// Per-pass shared state: one open-files snapshot and a time slice per layer.
//
// Under agent load (load average 60-100) lsof alone takes tens of seconds and
// every walk is slow, so a pass whose layers each took their own snapshot and
// ran on whatever time the previous layer left was cut off before the later
// layers removed anything, every hour. Two rules fix that:
//
//   - ONE snapshot per pass, started in the background when the pass begins so
//     it overlaps the first layers, and shared by every layer that needs it.
//   - Each layer runs in a slice of the pass budget (a share of the whole,
//     measured from when the layer starts) so none can starve the next. Time a
//     layer does not use rolls forward; the last layer takes whatever remains.

// Shares of the pass budget. They sum to 1; the final layer is uncapped.
const (
	shareLogs     = 0.05
	shareGoCaches = 0.35
	shareDocker   = 0.15
	shareTemp     = 0.20
	shareSources  = 0.10
	shareLast     = 1.0
)

// shareGoCacheDeletes is the most of a pass's delete budget the Go caches may
// spend, so the temp sweep and the source cache always have some left.
const shareGoCacheDeletes = 0.8

// sharedOpen takes the pass's open-files snapshot once.
type sharedOpen struct {
	once sync.Once
	take func(context.Context) (openfiles.Snapshot, error)
	ctx  context.Context
	done chan struct{}
	snap openfiles.Snapshot
	err  error
}

func (s *sharedOpen) start() {
	s.once.Do(func() {
		s.done = make(chan struct{})
		go func() {
			s.snap, s.err = s.take(s.ctx)
			close(s.done)
		}()
	})
}

// get waits for the snapshot until ctx ends. A failed snapshot is kept, so every
// later caller gets the same answer and fails closed the same way.
func (s *sharedOpen) get(ctx context.Context) (openfiles.Snapshot, error) {
	s.start()
	select {
	case <-s.done:
		return s.snap, s.err
	case <-ctx.Done():
		return openfiles.Snapshot{}, ctx.Err()
	}
}

func (r Runner) takeOpenFiles(ctx context.Context) (openfiles.Snapshot, error) {
	if r.OpenPaths != nil {
		return r.OpenPaths(ctx)
	}
	return openfiles.Take(ctx)
}

// openSnapshot is the pass's shared snapshot, or a direct one outside a pass.
func (r Runner) openSnapshot(ctx context.Context) (openfiles.Snapshot, error) {
	if r.openShared != nil {
		return r.openShared.get(ctx)
	}
	return r.takeOpenFiles(ctx)
}

// beginPass binds the runner to a pass context: its budget, for the layer
// slices, and the shared open-files snapshot, started now unless a test has not
// supplied a fake (a test must never be made to run the real lsof).
func (r Runner) beginPass(ctx context.Context) Runner {
	r.Ctx = ctx
	if deadline, ok := ctx.Deadline(); ok {
		r.passTotal = time.Until(deadline)
	}
	r.openShared = &sharedOpen{take: r.takeOpenFiles, ctx: ctx}
	if !testing.Testing() || r.OpenPaths != nil {
		r.openShared.start()
	}
	r.governor = newDeleter(r.Policy, r.removeFn, r.sleepFn)
	return r
}

// endPass says, once, that the pass backed off. It is the line an agent
// reading the output must not miss: re-running at once is the hammering the
// back-off exists to stop.
func (r Runner) endPass() {
	if b := r.backedOffErr(); b != nil {
		r.print("storage maintenance BACKED OFF: %v. The filesystem is in distress; do not re-run now. The next scheduled or automatic pass resumes where this one stopped.\n", b)
	}
}

// slice returns a runner whose Ctx is share of the pass budget, never past the
// pass deadline. Without a deadline (an interactive full GC) it is unbounded.
func (r Runner) slice(share float64) (Runner, context.CancelFunc) {
	parent := r.hostCtx()
	deadline, ok := parent.Deadline()
	if !ok || r.passTotal <= 0 || share >= 1 {
		ctx, cancel := context.WithCancel(parent)
		r.Ctx = ctx
		return r, cancel
	}
	end := time.Now().Add(time.Duration(float64(r.passTotal) * share))
	if end.After(deadline) {
		end = deadline
	}
	ctx, cancel := context.WithDeadline(parent, end)
	r.Ctx = ctx
	return r, cancel
}

// runLayer runs one layer in its slice and classifies the outcome: a layer
// that ended because its time ran out is CUT OFF (it did what it reached and
// resumes next pass); anything else is a failure. A layer is never started once
// the pass itself is over; it is recorded as cut off, so the record says which
// layers got no time rather than silently omitting them.
func (r Runner) runLayer(name string, share float64, fn func(Runner) error) error {
	if err := r.hostCtx().Err(); err != nil {
		return layerErr(name, &CutOffError{Err: err})
	}
	// After a back-off nothing more runs: every layer touches the filesystem
	// or forks a process, and either adds to the distress.
	if err := r.backedOffErr(); err != nil {
		r.print("%s: skipped, the pass backed off\n", name)
		return layerErr(name, err)
	}
	sub, done := r.slice(share)
	defer done()
	return cutOrFail(sub.Ctx, name, fn(sub))
}

// cutOrFail wraps err as layer name's outcome. When the layer's slice expired
// the error is a cut-off: a command killed by the deadline does not wrap
// context.DeadlineExceeded, so expiry itself is the signal, not the error text.
func cutOrFail(slice context.Context, name string, err error) error {
	if err == nil {
		return nil
	}
	var co *CutOffError
	if errors.As(err, &co) || onlyContextErrors(err) || slice.Err() != nil {
		if co == nil {
			err = &CutOffError{Err: err}
		}
		return layerErr(name, err)
	}
	return layerErr(name, err)
}

// sharedInUse answers gitsource's in-use question from the pass's snapshot,
// failing closed (everything in use) when it is missing.
func (r Runner) sharedInUse(ctx context.Context) func(string) bool {
	return func(entry string) bool {
		snap, err := r.openSnapshot(ctx)
		if err != nil {
			return true
		}
		return snap.Holds(entry)
	}
}

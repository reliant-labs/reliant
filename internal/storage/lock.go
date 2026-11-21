package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The machine storage lock: one exclusive flock on <policy>.lock, held by
// whoever reads-modifies-writes the policy and by every maintenance pass.
//
// It is exclusive for both on purpose. A writer (a release cut pinning the
// digests it just pushed, a registration) rewrites the same policy file a full
// GC prunes and rewrites, and a pin is only a guarantee if it lands BEFORE a
// registry pass computes its protected set: a shared lock for writers would let
// a pin race the very deletion it exists to prevent. What makes the two kinds
// of holder ordered correctly is not the lock mode but who gives way:
//
//   - A writer's critical section is milliseconds. A maintenance pass's is
//     minutes (an auto-gc pass under agent load takes about eight).
//   - So a writer that finds the lock held WAITS, bounded (WithLockWait), and
//     while it waits it holds a shared lock on <policy>.lock.waiters.
//   - A maintenance pass (WithMaintenance) does not start while anyone holds
//     that, and once someone does, cancels its own context: every layer checks
//     it, so the pass stops at its next safe point, records itself cut off
//     (the next pass resumes), and releases the lock to the writer.
//
// The waiters file is a lock rather than a marker file so a writer that dies
// mid-wait cannot leave maintenance yielding forever: the OS drops the lock
// with the process.
//
// v1.7.82 of control-plane failed its release cut this way: every image was
// built and pushed, then the pin waited five minutes behind an hourly auto-gc
// pass that never gave way, and the version was burned.

// LockWaitPoll is how often a waiting writer retries a held lock, and how often
// a maintenance pass checks for a waiting writer.
var LockWaitPoll = 200 * time.Millisecond

// ErrWriterWaiting is the cause a maintenance pass is cancelled with when a
// writer starts waiting for the lock, and what WithMaintenance returns when a
// writer is already waiting before the pass starts. Neither is a failure: the
// work resumes at the next pass.
var ErrWriterWaiting = errors.New("a release or registration is waiting for the storage lock")

// WithLock serializes policy updates and maintenance on this machine. It does
// not wait: a held lock is an immediate error, which is right for a pass that
// would only duplicate the holder's work.
func WithLock(policyPath string, fn func() error) error {
	return withLock(policyPath, 0, nil, fn)
}

// WithLockWait is WithLock for a writer that must not be turned away by
// maintenance. While the lock is held it announces itself as a waiter, which
// makes a running maintenance pass stop at its next safe point, and retries
// until wait elapses; out (when non-nil) gets one line naming the holder. The
// bound is the point: an unbounded wait would let a wedged pass hang a release
// cut forever.
func WithLockWait(policyPath string, wait time.Duration, out io.Writer, fn func() error) error {
	return withLock(policyPath, wait, out, fn)
}

// WithMaintenance runs one maintenance pass under the lock, yielding to
// writers. It refuses to start (ErrWriterWaiting) while a writer is waiting,
// and cancels fn's context with ErrWriterWaiting as soon as one starts to. fn
// must treat a cancelled context as "stop at the next safe point"; the storage
// passes all do, recording the remaining layers as cut off.
func WithMaintenance(ctx context.Context, policyPath string, out io.Writer, fn func(context.Context) error) error {
	if writerWaiting(policyPath) {
		return ErrWriterWaiting
	}
	return withLock(policyPath, 0, nil, func() error {
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		stop, watched := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(watched)
			tick := time.NewTicker(LockWaitPoll)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					if writerWaiting(policyPath) {
						cancel(ErrWriterWaiting)
						return
					}
				}
			}
		}()
		err := fn(ctx)
		close(stop)
		<-watched
		// Printed after fn returns, not from the watcher: fn's runner writes
		// to out too, and two writers on one io.Writer race.
		if out != nil && errors.Is(context.Cause(ctx), ErrWriterWaiting) {
			fmt.Fprintf(out, "storage maintenance: yielded the lock to a waiting release; the next pass resumes where this one stopped\n")
		}
		return err
	})
}

func withLock(policyPath string, wait time.Duration, out io.Writer, fn func() error) error {
	// Refused before the mkdir: the lock file would otherwise be created
	// beside the real machine policy even when every write inside is refused.
	if err := guardMachinePolicy(policyPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(policyPath), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(policyPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := lock(f); err != nil {
		if wait <= 0 || !contended(err) {
			return fmt.Errorf("storage maintenance is already running (%s): %w", lockHolder(policyPath), err)
		}
		if err := awaitLock(f, policyPath, wait, out); err != nil {
			return err
		}
	}
	recordHolder(policyPath)
	defer clearHolder(policyPath)
	return fn()
}

// awaitLock retries f's lock until wait elapses, announced as a waiter for the
// whole of it so a running maintenance pass gives way.
func awaitLock(f *os.File, policyPath string, wait time.Duration, out io.Writer) error {
	// Best effort: without the announcement the wait still works, it just
	// lasts until the pass ends on its own.
	if w, err := announceWaiter(policyPath); err == nil {
		defer func() { _ = w.Close() }()
	}
	holder := lockHolder(policyPath)
	if out != nil {
		fmt.Fprintf(out, "waiting for storage maintenance (%s) to release its lock, up to %s\n", holder, wait)
	}
	start := time.Now()
	deadline := start.Add(wait)
	for {
		time.Sleep(LockWaitPoll)
		err := lock(f)
		if err == nil {
			if out != nil {
				fmt.Fprintf(out, "storage maintenance released its lock after %s\n", time.Since(start).Round(time.Millisecond))
			}
			return nil
		}
		if !contended(err) {
			return fmt.Errorf("storage lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("storage maintenance is already running (%s) and still held its lock after waiting %s: %w", lockHolder(policyPath), wait, err)
		}
	}
}

// announceWaiter takes a shared lock on the waiters file and returns it open;
// closing it (or the process exiting) withdraws the announcement.
func announceWaiter(policyPath string) (*os.File, error) {
	f, err := os.OpenFile(policyPath+".lock.waiters", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockShared(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// writerWaiting reports whether any writer holds the waiters file: an
// exclusive probe fails exactly when one does. Any other error (no file yet,
// an unreadable one) reads as "nobody waiting", so a broken waiters file can
// never make maintenance yield forever.
func writerWaiting(policyPath string) bool {
	f, err := os.OpenFile(policyPath+".lock.waiters", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return contended(lock(f))
}

// recordHolder notes this process as the lock's holder, for a waiter's
// message. The lock file itself is not used: on Windows its locked byte cannot
// be read by anyone else.
func recordHolder(policyPath string) {
	_ = atomicfile.WriteFile(policyPath+".lock.holder", []byte(strconv.Itoa(os.Getpid())+"\n"), 0600)
}

// clearHolder removes the record while the lock is still held, so a waiter
// behind a holder that records nothing (an older forge) reads "holder unknown"
// rather than the pid of whoever held the lock before it.
func clearHolder(policyPath string) { _ = os.Remove(policyPath + ".lock.holder") }

// lockHolder describes the lock's recorded holder.
func lockHolder(policyPath string) string {
	b, err := os.ReadFile(policyPath + ".lock.holder")
	if err != nil {
		return "holder unknown"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return "holder unknown"
	}
	return "pid " + strconv.Itoa(pid)
}

package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// inotifyInstances counts this process's inotify file descriptors.
func inotifyInstances(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == "anon_inode:inotify" {
			n++
		}
	}
	return n
}

// startedObserver returns an observer whose observations, started with the
// returned ctx, all end before the test's temp dirs are removed.
func startedObserver(t *testing.T) (*ActivityObserver, context.Context, context.CancelFunc) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("activity observation is linux-only")
	}
	o, err := NewActivityObserver(filepath.Join(t.TempDir(), "state.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		eventually(t, "every observation to end", func() bool {
			o.mu.Lock()
			defer o.mu.Unlock()
			return len(o.running) == 0
		})
		o.mu.Lock()
		w := o.watcher
		o.mu.Unlock()
		if w != nil {
			w.Close()
		}
	})
	return o, ctx, cancel
}

// eventually polls cond; the observer reacts to inotify events, which arrive
// asynchronously but promptly.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pausedInventoryContext pauses an inventory before it can acquire a watch.
// The command context deliberately outlives the observation's own context.
type pausedInventoryContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (c *pausedInventoryContext) Err() error {
	c.once.Do(func() {
		close(c.entered)
		<-c.resume
	})
	return c.Context.Err()
}

func TestRecoveryActivityEndedInventoryCannotAcquireWatches(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		name := "cancel"
		if overflow {
			name = "overflow"
		}
		t.Run(name, func(t *testing.T) {
			o, ctx, cancel := startedObserver(t)
			root := t.TempDir()
			if err := o.Start(ctx, root); err != nil {
				t.Fatal(err)
			}
			_, key, err := activityKey(root)
			if err != nil {
				t.Fatal(err)
			}
			o.mu.Lock()
			w, watcher := o.running[key], o.watcher
			o.mu.Unlock()
			paused := &pausedInventoryContext{
				Context: context.Background(), entered: make(chan struct{}), resume: make(chan struct{}),
			}
			done := make(chan struct{})
			var scanErr error
			go func() {
				_, scanErr = o.inventory(paused, root, w)
				close(done)
			}()
			var resume sync.Once
			unpause := func() { resume.Do(func() { close(paused.resume) }) }
			defer func() { unpause(); <-done }()
			<-paused.entered
			if overflow {
				watcher.Errors <- fsnotify.ErrEventOverflow
			} else {
				cancel()
			}
			eventually(t, "observation teardown", func() bool { return !o.Running(root) })
			// Allow the old command's inventory to continue only after cleanup.
			unpause()
			<-done
			if scanErr == nil {
				t.Error("ended inventory acquired watches instead of failing closed")
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			if len(o.watchRefs) != 0 || len(w.watched) != 0 || len(watcher.WatchList()) != 0 {
				t.Errorf("ended inventory leaked watches: refs=%v owned=%v kernel=%v", o.watchRefs, w.watched, watcher.WatchList())
			}
		})
	}
}

// The regression: one inotify instance per observed workspace exhausted the
// user's fs.inotify.max_user_instances (128) on a machine with many worktrees.
func TestRecoveryActivityWorkspacesShareOneInotifyInstance(t *testing.T) {
	o, ctx, cancel := startedObserver(t)
	before := inotifyInstances(t)
	for i := 0; i < 5; i++ {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "sub", "dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := o.Start(ctx, root); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
	}
	if got := inotifyInstances(t) - before; got != 1 {
		t.Fatalf("observing 5 workspaces opened %d inotify instances, want 1", got)
	}
	cancel()
	eventually(t, "every observation to end", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return len(o.running) == 0
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.watchRefs) != 0 {
		t.Fatalf("ended observations still hold watches: %v", o.watchRefs)
	}
}

// A deleted worktree's observation ends and releases its watches and state
// instead of holding them until the daemon exits, and the path can be
// observed again if it comes back.
func TestRecoveryActivityReleasesDeletedWorkspace(t *testing.T) {
	o, ctx, _ := startedObserver(t)
	keep := t.TempDir()
	if err := o.Start(ctx, keep); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := o.Start(ctx, root); err != nil {
		t.Fatal(err)
	}
	canonical, key, err := activityKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the deleted workspace's observation to end", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.running[key] == nil
	})
	o.mu.Lock()
	for path := range o.watchRefs {
		if overlapActivityPath(canonical, path) {
			t.Errorf("deleted workspace still holds a watch on %s", path)
		}
	}
	if _, ok := o.records[key]; ok {
		t.Error("deleted workspace's record is still persisted")
	}
	o.mu.Unlock()
	if !o.Running(keep) {
		t.Fatal("deleting one workspace ended another's observation")
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := o.Start(ctx, root); err != nil {
		t.Fatalf("a recreated workspace must be observable again: %v", err)
	}
}

// Events reach only the workspace they happened in.
func TestRecoveryActivityRoutesEventsToTheirWorkspace(t *testing.T) {
	o, _, _ := startedObserver(t)
	a, b := t.TempDir(), t.TempDir()
	o.mu.Lock()
	if err := o.ensureWatcherLocked(); err != nil {
		o.mu.Unlock()
		t.Fatal(err)
	}
	wa, wb := newActivityWatch(a, "", func() {}), newActivityWatch(b, "", func() {})
	o.running["a"], o.running["b"] = wa, wb
	o.publishRoutesLocked()
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.running, "a")
		delete(o.running, "b")
		o.publishRoutesLocked()
		o.mu.Unlock()
	}()
	if err := o.addWatch(wa, a); err != nil {
		t.Fatal(err)
	}
	if err := o.addWatch(wb, b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "edit"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-wa.events:
		if !strings.HasPrefix(ev.Name, a) {
			t.Fatalf("workspace a received %s", ev.Name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("workspace a never saw its own edit")
	}
	select {
	case ev := <-wb.events:
		t.Fatalf("workspace b received a's event %s", ev.Name)
	default:
	}
}

// A shared-queue overflow cannot be attributed to one workspace, so every
// observation loses coverage and can be started again.
func TestRecoveryActivityOverflowEndsEveryObservation(t *testing.T) {
	o, ctx, _ := startedObserver(t)
	roots := []string{t.TempDir(), t.TempDir()}
	for _, r := range roots {
		if err := o.Start(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	o.mu.Lock()
	watcher := o.watcher
	o.mu.Unlock()
	// Errors is the channel fsnotify reports IN_Q_OVERFLOW on.
	watcher.Errors <- fsnotify.ErrEventOverflow
	for _, r := range roots {
		eventually(t, "overflow to end "+r, func() bool { return !o.Running(r) })
	}
	for _, r := range roots {
		if err := o.Start(ctx, r); err != nil {
			t.Fatalf("restart after overflow: %v", err)
		}
	}
}

package worktreereclaim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	gitindex "github.com/go-git/go-git/v5/plumbing/format/index"
)

type activityClock struct {
	sync.Mutex
	at time.Time
}

func (c *activityClock) now() time.Time      { c.Lock(); defer c.Unlock(); return c.at }
func (c *activityClock) add(d time.Duration) { c.Lock(); defer c.Unlock(); c.at = c.at.Add(d) }

// inotifyExhausted reports whether err is the host running out of inotify
// budget rather than the code under test failing. inotify instances
// (fs.inotify.max_user_instances, EMFILE: "too many open files") and watches
// (fs.inotify.max_user_watches, ENOSPC: "no space left on device") are
// per-user kernel budgets shared with every other process the user runs; on a
// shared dev box or a loaded gate, other agents' stacks exhaust them.
func inotifyExhausted(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOSPC) ||
		strings.Contains(err.Error(), "too many open files") || strings.Contains(err.Error(), "no space left on device")
}

// skipIfInotifyExhausted skips the test when err is the host's inotify budget
// running out, and fails it otherwise. Use it where a test starts watching.
func skipIfInotifyExhausted(t *testing.T, err error) {
	t.Helper()
	if inotifyExhausted(err) {
		t.Skipf("this host's inotify budget is exhausted by other processes (fs.inotify.max_user_instances / max_user_watches): %v", err)
	}
	t.Fatal(err)
}

// skipWithoutInotify skips a test that needs inotify when the host has no
// instance, or no watches, to give it.
func skipWithoutInotify(t *testing.T) {
	t.Helper()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		skipIfInotifyExhausted(t, err)
	}
	defer w.Close()
	dir := t.TempDir()
	for i := 0; i < 16; i++ {
		sub := filepath.Join(dir, strconv.Itoa(i))
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := w.Add(sub); err != nil {
			skipIfInotifyExhausted(t, err)
		}
	}
}

func activityFixture(t *testing.T) (*ActivityObserver, string, *activityClock) {
	t.Helper()
	skipWithoutInotify(t)
	root := t.TempDir()
	c := &activityClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	o, err := NewActivityObserver(filepath.Join(t.TempDir(), "state.json"), c.now)
	if err != nil {
		t.Fatal(err)
	}
	root, key, err := activityKey(root)
	if err != nil {
		t.Fatal(err)
	}
	// A registered observation without its event loop, so each test drives
	// Observe by hand.
	o.mu.Lock()
	if err := o.ensureWatcherLocked(); err != nil {
		o.mu.Unlock()
		skipIfInotifyExhausted(t, err)
	}
	watcher := o.watcher
	o.running[key] = newActivityWatch(root, "", func() {})
	o.publishRoutesLocked()
	o.mu.Unlock()
	t.Cleanup(func() {
		o.mu.Lock()
		delete(o.running, key)
		o.publishRoutesLocked()
		o.mu.Unlock()
		watcher.Close()
	})
	return o, root, c
}
func observeActivity(t *testing.T, o *ActivityObserver, root string) RecoveryActivity {
	t.Helper()
	e, err := o.Observe(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestRecoveryActivitySevenDayBoundary(t *testing.T) {
	now := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	e := RecoveryActivity{Generation: 1, ObservationID: "epoch", ObservedSince: now.Add(-RecoveryIdlePeriod), LastActivity: now.Add(-RecoveryIdlePeriod), ObservedThrough: now, Complete: true}
	if !RecoveryInactive(e, now) {
		t.Fatal("exact seven days must qualify")
	}
	if RecoveryInactive(e, now.Add(-time.Nanosecond)) {
		t.Fatal("future observation must not qualify")
	}
	e.LastActivity = e.LastActivity.Add(time.Nanosecond)
	if RecoveryInactive(e, now) {
		t.Fatal("not yet seven days")
	}
	e.LastActivity = e.ObservedSince
	e.Complete = false
	if RecoveryInactive(e, now) {
		t.Fatal("unknown coverage")
	}
	e.Complete = true
	if RecoveryInactive(e, now.Add(activityMaxGap+time.Nanosecond)) {
		t.Fatal("stale coverage")
	}
}
func TestRecoveryActivityContentResetsDespiteSameStatusAndMtime(t *testing.T) {
	o, root, c := activityFixture(t)
	path := filepath.Join(root, "dirty")
	if err := os.WriteFile(path, []byte("version a"), 0600); err != nil {
		t.Fatal(err)
	}
	first := observeActivity(t, o, root)
	stat, _ := os.Stat(path)
	c.add(time.Minute)
	if err := os.WriteFile(path, []byte("version b"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	second := observeActivity(t, o, root)
	if second.Generation <= first.Generation || !second.LastActivity.Equal(c.now()) {
		t.Fatal("same dirty status must not conceal changed bytes")
	}
	data, err := os.ReadFile(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "version b") || strings.Contains(string(data), "dirty") || strings.Contains(string(data), root) {
		t.Fatal("state leaks content or names")
	}
}
func TestRecoveryActivityGapRollbackRestartAndInvalidation(t *testing.T) {
	o, root, c := activityFixture(t)
	first := observeActivity(t, o, root)
	c.add(activityMaxGap + time.Nanosecond)
	gap := observeActivity(t, o, root)
	if gap.ObservationID == first.ObservationID || !gap.ObservedSince.Equal(c.now()) {
		t.Fatal("gap must reset epoch")
	}
	c.add(-time.Minute)
	rollback := observeActivity(t, o, root)
	if rollback.ObservationID == gap.ObservationID {
		t.Fatal("clock rollback must reset epoch")
	}
	if err := o.Invalidate(root); err != nil {
		t.Fatal(err)
	}
	invalid := observeActivity(t, o, root)
	if invalid.ObservationID == rollback.ObservationID {
		t.Fatal("overflow/gap invalidation must reset epoch")
	}
	reopened, err := NewActivityObserver(o.statePath, c.now)
	if err != nil {
		t.Fatal(err)
	}
	_, key, _ := activityKey(root)
	restart := reopened.records[key].Evidence
	if restart.Complete || restart.ObservationID == invalid.ObservationID {
		t.Fatal("restart must never inherit complete evidence")
	}
}
func TestRecoveryActivityNoWatcherNoProof(t *testing.T) {
	o, root, _ := activityFixture(t)
	_, key, _ := activityKey(root)
	delete(o.running, key)
	e, err := o.Observe(context.Background(), root)
	if err == nil || e.Complete {
		t.Fatal("poll-only evidence cannot authorize")
	}
}
func TestRecoveryActivityErrorsResetAndDoNotBackdate(t *testing.T) {
	o, root, c := activityFixture(t)
	before := observeActivity(t, o, root)
	c.add(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	after, err := o.Observe(ctx, root)
	if err == nil || after.Complete || after.ObservationID == before.ObservationID || !after.ObservedSince.Equal(c.now()) {
		t.Fatal("unknown scan must reset and hold")
	}
}
func TestRecoveryActivityQueuedOverflowHolds(t *testing.T) {
	o, root, c := activityFixture(t)
	before := observeActivity(t, o, root)
	_, key, _ := activityKey(root)
	// Buffered replacement is an injected watcher overflow, not a real OS wait.
	injected := make(chan error, 1)
	o.running[key].injectedErrors = injected
	injected <- fsnotify.ErrEventOverflow
	c.add(time.Minute)
	after, err := o.Observe(context.Background(), root)
	if err == nil || after.Complete || after.ObservationID == before.ObservationID {
		t.Fatal("overflow must invalidate observation")
	}
}
func TestRecoveryActivityStateInsideWorkspaceRefused(t *testing.T) {
	root := t.TempDir()
	o, err := NewActivityObserver(filepath.Join(root, "state.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(context.Background(), root); err == nil {
		t.Fatal("recursive self-observation must be refused")
	}
}
func TestRecoveryActivitySemanticIndexIgnoresRefresh(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeIndex := func(mod time.Time, oid string) {
		t.Helper()
		f, err := os.Create(filepath.Join(dir, "index"))
		if err != nil {
			t.Fatal(err)
		}
		idx := &gitindex.Index{Version: 2, Entries: []*gitindex.Entry{{Name: "file", Hash: plumbing.NewHash(oid), Mode: filemode.Regular, ModifiedAt: mod}}}
		if err := gitindex.NewEncoder(f).Encode(idx); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	writeIndex(time.Unix(1, 0), strings.Repeat("a", 40))
	a, err := activityGitDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	writeIndex(time.Unix(100, 0), strings.Repeat("a", 40))
	b, err := activityGitDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("index stat refresh is not activity")
	}
	writeIndex(time.Unix(100, 0), strings.Repeat("b", 40))
	c, err := activityGitDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b == c {
		t.Fatal("staged content change must be activity")
	}
}
func TestRecoveryActivityConcurrentPersistence(t *testing.T) {
	o, root, c := activityFixture(t)
	observeActivity(t, o, root)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				c.add(time.Second)
				if _, err := o.Observe(context.Background(), root); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatal("persistence torn")
	}
}

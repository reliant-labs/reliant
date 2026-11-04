package worktreereclaim

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// A quiet observation keeps its epoch continuous by heartbeat, without
// re-inventorying; anything that could have changed the tree needs the
// inventory again.
func TestRecoveryActivityHeartbeatExtendsOnlyQuietCompleteEvidence(t *testing.T) {
	o, root, c := activityFixture(t)
	_, key, _ := activityKey(root)
	w := o.running[key]

	if o.heartbeat(key, w) {
		t.Fatal("no complete inventory yet: a heartbeat must not extend coverage")
	}
	first := observeActivity(t, o, root)
	if !first.Complete {
		t.Fatal("quiet inventory must be complete")
	}

	c.add(activityPoll)
	if !o.heartbeat(key, w) {
		t.Fatal("quiet observation must heartbeat")
	}
	if got := o.records[key].Evidence; !got.ObservedThrough.Equal(c.now()) || got.ObservationID != first.ObservationID || got.Generation != first.Generation {
		t.Fatalf("heartbeat must extend coverage and nothing else: %+v", got)
	}

	// An event since the inventory needs the inventory.
	o.mu.Lock()
	w.seq++
	o.mu.Unlock()
	if o.heartbeat(key, w) {
		t.Fatal("a watcher event since the last inventory must force one")
	}
	observeActivity(t, o, root)

	// A pending, undelivered event does too.
	w.events <- fsnotify.Event{Name: filepath.Join(root, "x"), Op: fsnotify.Write}
	if o.heartbeat(key, w) {
		t.Fatal("a queued watcher event must force an inventory")
	}
	observeActivity(t, o, root) // drains it: incomplete
	if !observeActivity(t, o, root).Complete {
		t.Fatal("a quiet inventory after the event must be complete again")
	}

	// So does a gap: the epoch is no longer continuous.
	c.add(activityMaxGap + time.Nanosecond)
	if o.heartbeat(key, w) {
		t.Fatal("a gap must force an inventory (and a new epoch)")
	}
	observeActivity(t, o, root)

	// And age: the inventory is the backstop for what inotify cannot see.
	for elapsed := time.Duration(0); elapsed+activityPoll < activityReconcile; elapsed += activityPoll {
		c.add(activityPoll)
		if !o.heartbeat(key, w) {
			t.Fatalf("heartbeat refused %v after the inventory", elapsed+activityPoll)
		}
	}
	c.add(activityPoll)
	if o.heartbeat(key, w) {
		t.Fatal("an inventory older than activityReconcile must be redone")
	}
}

// Dependency install trees are neither inventoried nor watched: one can hold
// more directories than a whole repository.
func TestRecoveryActivityLeavesDependencyTreesUnobserved(t *testing.T) {
	o, root, c := activityFixture(t)
	_, key, _ := activityKey(root)
	deps := filepath.Join(root, "web", "node_modules", "pkg")
	if err := os.MkdirAll(deps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deps, "index.js"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := observeActivity(t, o, root)

	o.mu.Lock()
	for path := range o.running[key].watched {
		if filepath.Base(path) == "node_modules" || filepath.Base(path) == "pkg" {
			o.mu.Unlock()
			t.Fatalf("dependency tree watched: %s", path)
		}
	}
	o.mu.Unlock()

	c.add(time.Minute)
	if err := os.WriteFile(filepath.Join(deps, "index.js"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := observeActivity(t, o, root)
	if second.Generation != first.Generation || !second.LastActivity.Equal(first.LastActivity) {
		t.Fatal("a write inside node_modules must not count as activity")
	}

	// A node_modules directory is still observed as an entry of its parent:
	// creating or removing it is activity like any other.
	c.add(time.Minute)
	if err := os.RemoveAll(filepath.Join(root, "web", "node_modules")); err != nil {
		t.Fatal(err)
	}
	third := observeActivity(t, o, root)
	if third.Generation <= second.Generation {
		t.Fatal("removing the dependency tree itself must count as activity")
	}
}

// The observer holds at most its share of the user's inotify budget. An
// observation that cannot fit ends, releasing its watches, rather than
// re-walking the tree every poll.
func TestRecoveryActivityWatchBudgetEndsTheObservation(t *testing.T) {
	o, root, _ := activityFixture(t)
	_, key, _ := activityKey(root)
	for i := 0; i < 4; i++ {
		if err := os.Mkdir(filepath.Join(root, strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ended := false
	o.mu.Lock()
	o.watchBudget = 2
	o.running[key].cancel = func() { ended = true }
	o.mu.Unlock()

	e, err := o.Observe(t.Context(), root)
	if !errors.Is(err, errActivityBudget) || e.Complete {
		t.Fatalf("over-budget inventory = %+v, %v; want incomplete and errActivityBudget", e, err)
	}
	if !ended {
		t.Fatal("an observation that cannot fit its budget must end")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.watchRefs) > 2 {
		t.Fatalf("observer holds %d watches over its budget of 2", len(o.watchRefs))
	}
}

func TestActivityWatchBudgetIsAShareOfTheKernelLimit(t *testing.T) {
	data, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		if got := activityWatchBudget(); got != activityDefaultWatchBudget {
			t.Fatalf("unreadable limit: budget %d, want the default %d", got, activityDefaultWatchBudget)
		}
		return
	}
	limit, _ := strconv.Atoi(string(data[:len(data)-1]))
	if got := activityWatchBudget(); got != max(limit/activityWatchBudgetShare, 1) {
		t.Fatalf("budget %d, want 1/%d of %d", got, activityWatchBudgetShare, limit)
	}
}

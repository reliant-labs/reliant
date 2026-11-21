//go:build unix && !aix

// These tests need the entry lock, which only exists where x/sys/unix has
// flock(2) (see lock_other.go): without it nothing proves a staging directory dead, and
// concurrent builders are not serialized.

package forgedelegate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// A build runs in a heavy slot, and a staging directory a dead build left in
// the entry is removed by the next build, which holds the entry's lock.
func TestPinned_SweepsADeadBuildsStagingInsideTheSlot(t *testing.T) {
	r, _, _ := newResolver(t)
	var slots []string
	held := false
	r.Slot = func(_ context.Context, label string) (func(), error) {
		slots = append(slots, label)
		held = true
		return func() { held = false }, nil
	}
	install := r.Install
	r.Install = func(ctx context.Context, pkg, gobin string) error {
		if !held {
			t.Error("the build ran outside a heavy slot")
		}
		return install(ctx, pkg, gobin)
	}
	entry := filepath.Join(os.Getenv(CacheDirEnv), safeName(pin), runtime.GOOS+"-"+runtime.GOARCH)
	dead := filepath.Join(entry, stagingPrefix+"dead", ".work", "go-build123")
	if err := os.MkdirAll(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pinned(context.Background(), t.TempDir(), pin); err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || held {
		t.Errorf("slots taken %v, still held %v; want one, released", slots, held)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(dead))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dead build's staging directory survived: %v", err)
	}
	ents, _ := os.ReadDir(entry)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), stagingPrefix) {
			t.Errorf("the build left its staging directory %s", e.Name())
		}
	}
}

// Two builders of one entry build it once: the second waits for the entry's
// lock and then finds the first one's binary.
func TestPinned_ConcurrentRequestsBuildOnce(t *testing.T) {
	r, _, _ := newResolver(t)
	r.Slot = func(context.Context, string) (func(), error) { return func() {}, nil }
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var builds atomic.Int32
	r.Install = func(_ context.Context, _, gobin string) error {
		builds.Add(1)
		started <- struct{}{}
		<-release
		return atomicfile.WriteFile(filepath.Join(gobin, exeName()), []byte("#!/bin/sh\n"), 0o755)
	}
	// The second builder signals here once it has found the entry's lock
	// held: from then on it is excluded until the first one unlocks, which
	// it cannot do before release is closed.
	contended := make(chan struct{}, 1)
	lockEntryContended = func(string) {
		select {
		case contended <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { lockEntryContended = nil })
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := r.Pinned(context.Background(), t.TempDir(), pin)
			errs <- err
		}()
	}
	<-started
	<-contended
	select {
	case <-started:
		t.Fatal("a second build started while the first held the entry")
	default:
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := builds.Load(); n != 1 {
		t.Errorf("%d builds, want 1", n)
	}
}

// A canceled context never takes the entry's lock, even a free one, so a
// canceled caller never goes on to take a heavy slot and build.
func TestLockEntry_CanceledContextDoesNotLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, err := lockEntry(ctx, path); !errors.Is(err, context.Canceled) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("lockEntry with a canceled context: err %v, want context.Canceled", err)
	}
	unlock, err := lockEntry(context.Background(), path)
	if err != nil {
		t.Fatalf("the lock was left held: %v", err)
	}
	unlock()
}

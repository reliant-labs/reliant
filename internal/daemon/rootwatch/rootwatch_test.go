// Copyright (c) 2025 Reliant Labs
package rootwatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFS is a filesystem the test rewires between checks: what each path
// resolves to, and which paths are mount points.
type fakeFS struct {
	mu     sync.Mutex
	ids    map[string]Identity
	errs   map[string]error
	mounts map[string]bool
}

func newFakeFS() *fakeFS {
	return &fakeFS{ids: map[string]Identity{}, errs: map[string]error{}, mounts: map[string]bool{}}
}

func (f *fakeFS) set(path string, id Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids[path] = id
	delete(f.errs, path)
}

func (f *fakeFS) remove(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.ids, path)
	f.errs[path] = fs.ErrNotExist
}

func (f *fakeFS) fail(path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[path] = err
}

func (f *fakeFS) setMount(path string, isMount bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounts[path] = isMount
}

func (f *fakeFS) probe() Probe {
	return Probe{
		Stat: func(path string) (Identity, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if err, ok := f.errs[path]; ok {
				return Identity{}, err
			}
			if id, ok := f.ids[path]; ok {
				return id, nil
			}
			return Identity{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
		},
		MountPoint: func(path string) (bool, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.mounts[path], true
		},
		Mounts: func(path string) []string {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.mounts[path] {
				return []string{"42 1 0:50 / " + path + " rw - virtiofs none rw"}
			}
			return []string{"1 0 0:30 / / rw - overlay overlay rw"}
		},
	}
}

const home = "/home/workspace"

var (
	pvcRoot    = Identity{Dev: 50, Ino: 1}
	rootfsHome = Identity{Dev: 30, Ino: 977}
)

// newWatcher records home on fsys and collects every reported fault.
func newWatcher(t *testing.T, fsys *fakeFS) (*Watcher, func() []Fault) {
	t.Helper()
	var mu sync.Mutex
	var faults []Fault
	w, err := New(home, Options{
		Probe: fsys.probe(),
		OnFault: func(f Fault) {
			mu.Lock()
			defer mu.Unlock()
			faults = append(faults, f)
		},
	})
	require.NoError(t, err)
	return w, func() []Fault {
		mu.Lock()
		defer mu.Unlock()
		return append([]Fault(nil), faults...)
	}
}

// TestHomeDetach_Trips is the 2026-10-09 incident: /home/workspace was a
// mount point, the mount was detached, and the path fell through to the
// image's baked home on the container rootfs.
func TestHomeDetach_Trips(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	fsys.setMount(home, true)
	w, faults := newWatcher(t, fsys)

	fsys.set(home, rootfsHome)
	fsys.setMount(home, false)

	f, ok := w.Check("/home/workspace/projects/reliant-labs")
	require.True(t, ok, "a detached $HOME must trip on the very next check")
	assert.True(t, f.Home)
	assert.Equal(t, KindUnmounted, f.Kind)
	assert.Equal(t, pvcRoot, f.Recorded)
	assert.Equal(t, rootfsHome, f.Observed)
	assert.Contains(t, f.RecordedMounts[0], "virtiofs", "the report must carry the mount that was lost")
	assert.Contains(t, f.Mounts[0], "overlay", "and the one the path fell through to")
	assert.Len(t, faults(), 1)

	// Repeated checks do not re-report.
	w.CheckAll()
	w.Check("")
	assert.Len(t, faults(), 1)
}

// TestHomeNoLongerMountPoint_Trips covers the mount table disagreeing while
// stat does not: the poll's deep check reads the mount table.
func TestHomeNoLongerMountPoint_Trips(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	fsys.setMount(home, true)
	w, _ := newWatcher(t, fsys)

	fsys.setMount(home, false)
	w.CheckAll()

	f, ok := w.Fault("")
	require.True(t, ok)
	assert.Equal(t, KindUnmounted, f.Kind)
}

func TestHomeReplacedWithoutMount_Trips(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, Identity{Dev: 1, Ino: 100})
	w, _ := newWatcher(t, fsys)

	fsys.set(home, Identity{Dev: 1, Ino: 200})

	f, ok := w.Check("")
	require.True(t, ok, "$HOME changing identity is a fault even on the same device")
	assert.Equal(t, KindReplaced, f.Kind)
}

func TestHomeMissing_Trips(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	w, _ := newWatcher(t, fsys)

	fsys.remove(home)

	f, ok := w.Check("")
	require.True(t, ok)
	assert.Equal(t, KindMissing, f.Kind)
	assert.Equal(t, Identity{}, f.Observed)
}

func TestHomeUnchanged_DoesNotTrip(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	fsys.setMount(home, true)
	w, faults := newWatcher(t, fsys)

	for range 3 {
		w.CheckAll()
		_, ok := w.Check("/home/workspace/projects/x")
		assert.False(t, ok)
	}
	assert.Empty(t, faults())
}

// TestStatError_DoesNotTrip: an error that is not "does not exist" says
// nothing about which directory the path names.
func TestStatError_DoesNotTrip(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	w, faults := newWatcher(t, fsys)

	fsys.fail(home, fs.ErrPermission)
	w.CheckAll()

	assert.Empty(t, faults())
}

// TestRootRemoved_DoesNotTrip: a worktree deleted by the user or an agent is
// ENOENT — removal, not a lost mount — and is forgotten.
func TestRootRemoved_DoesNotTrip(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	root := "/home/workspace/.reliant/worktrees/reliant-labs/errors-5972f1d1"
	fsys.set(root, Identity{Dev: 50, Ino: 7})
	w, faults := newWatcher(t, fsys)
	w.Observe(root)

	fsys.remove(root)
	w.CheckAll()
	_, ok := w.Check(root)

	assert.False(t, ok)
	assert.Empty(t, faults())
	assert.False(t, w.known(root), "a removed root is forgotten")

	// Recreated later: recorded afresh, still no fault.
	fsys.set(root, Identity{Dev: 50, Ino: 99})
	_, ok = w.Check(root)
	assert.False(t, ok)
	assert.True(t, w.known(root))
}

// TestRootRecreatedInPlace_DoesNotTrip: a re-clone or `git worktree add`
// between two polls keeps the device and changes the inode.
func TestRootRecreatedInPlace_DoesNotTrip(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	root := "/home/workspace/projects/app"
	fsys.set(root, Identity{Dev: 50, Ino: 7})
	w, faults := newWatcher(t, fsys)
	w.Observe(root)

	fsys.set(root, Identity{Dev: 50, Ino: 8})
	w.CheckAll()
	_, ok := w.Check(root)

	assert.False(t, ok)
	assert.Empty(t, faults())

	// The new directory was adopted: a later device change is measured
	// against it, not the original.
	fsys.set(root, Identity{Dev: 51, Ino: 8})
	f, ok := w.Check(root)
	require.True(t, ok)
	assert.Equal(t, Identity{Dev: 50, Ino: 8}, f.Recorded)
}

// TestRootOnNewDevice_TripsOnlyThatRoot: a root that is suddenly on another
// filesystem is a fault, and covers that root and nothing else.
func TestRootOnNewDevice_TripsOnlyThatRoot(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, Identity{Dev: 1, Ino: 2})
	root := "/mnt/data/proj"
	other := "/mnt/data/other"
	fsys.set(root, Identity{Dev: 60, Ino: 3})
	fsys.set(other, Identity{Dev: 60, Ino: 4})
	w, faults := newWatcher(t, fsys)
	w.Observe(root)
	w.Observe(other)

	fsys.set(root, Identity{Dev: 61, Ino: 3})
	w.CheckAll()

	require.Len(t, faults(), 1)
	f := faults()[0]
	assert.False(t, f.Home)
	assert.Equal(t, KindReplaced, f.Kind)
	assert.Equal(t, root, f.Path)

	_, ok := w.Fault(root)
	assert.True(t, ok)
	_, ok = w.Fault(filepath.Join(root, "sub", "dir"))
	assert.True(t, ok, "a fault covers paths beneath the root")
	_, ok = w.Fault(other)
	assert.False(t, ok)
	_, ok = w.Fault("/mnt/data/proj-sibling")
	assert.False(t, ok, "a sibling sharing a name prefix is not beneath the root")
	_, ok = w.Fault("")
	assert.False(t, ok, "a root fault is not global")
}

func TestHomeFault_CoversEveryPath(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	w, _ := newWatcher(t, fsys)

	fsys.set(home, rootfsHome)
	w.CheckAll()

	for _, path := range []string{"", "/home/workspace/projects/x", "/opt/elsewhere"} {
		_, ok := w.Fault(path)
		assert.True(t, ok, path)
	}
}

// TestFaultClears_WhenIdentityRestored: a desktop user who remounts the
// original volume gets their daemon back without a restart.
func TestFaultClears_WhenIdentityRestored(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	var restored []Fault
	w, err := New(home, Options{Probe: fsys.probe(), OnRestored: func(f Fault) { restored = append(restored, f) }})
	require.NoError(t, err)

	fsys.set(home, rootfsHome)
	w.CheckAll()
	_, ok := w.Fault("")
	require.True(t, ok)

	fsys.set(home, pvcRoot)
	w.CheckAll()
	_, ok = w.Fault("")
	assert.False(t, ok)
	assert.Len(t, restored, 1)
}

// TestNoRootsRecordedWhileHomeFaulted: a root first seen while $HOME is
// faulted lives on the wrong filesystem and must not be recorded.
func TestNoRootsRecordedWhileHomeFaulted(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	w, _ := newWatcher(t, fsys)
	fsys.set(home, rootfsHome)
	w.CheckAll()

	root := "/home/workspace/.recovery"
	fsys.set(root, Identity{Dev: 30, Ino: 5})
	w.Observe(root)

	assert.False(t, w.known(root))
}

func TestNilWatcher_IsInert(t *testing.T) {
	var w *Watcher
	w.Observe("/x")
	w.CheckAll()
	_, ok := w.Check("/x")
	assert.False(t, ok)
	_, ok = w.Home()
	assert.False(t, ok)
}

func TestNew_UnreadableHomeIsNotWatched(t *testing.T) {
	fsys := newFakeFS()
	w, err := New(home, Options{Probe: fsys.probe()})
	require.Error(t, err)
	require.NotNil(t, w)
	_, ok := w.Home()
	assert.False(t, ok)
	_, ok = w.Check("")
	assert.False(t, ok)
}

func TestRun_PollsUntilCancelled(t *testing.T) {
	fsys := newFakeFS()
	fsys.set(home, pvcRoot)
	tripped := make(chan Fault, 1)
	w, err := New(home, Options{
		Probe:    fsys.probe(),
		Interval: 5 * time.Millisecond,
		OnFault:  func(f Fault) { tripped <- f },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	fsys.set(home, rootfsHome)
	select {
	case f := <-tripped:
		assert.True(t, f.Home)
	case <-time.After(5 * time.Second):
		t.Fatal("the poll never noticed $HOME change")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestSystemProbe_ReplacedHomeDirectory runs the real stat path: a home
// directory renamed away and recreated at the same path is a different
// directory, and a removed root is not a fault.
func TestSystemProbe_ReplacedHomeDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no device/inode identity on windows")
	}
	base := t.TempDir()
	fakeHome := filepath.Join(base, "home")
	root := filepath.Join(base, "home", "worktree")
	require.NoError(t, os.MkdirAll(root, 0o755))

	w, err := New(fakeHome, Options{})
	require.NoError(t, err)
	w.Observe(root)
	require.True(t, w.known(root))
	_, ok := w.Check(root)
	require.False(t, ok, "nothing changed yet")

	require.NoError(t, os.RemoveAll(root))
	_, ok = w.Check(root)
	assert.False(t, ok, "a removed worktree is not a fault")

	require.NoError(t, os.Rename(fakeHome, filepath.Join(base, "home.detached")))
	require.NoError(t, os.Mkdir(fakeHome, 0o755))
	f, ok := w.Check("")
	require.True(t, ok, "a recreated home directory is a different directory")
	assert.True(t, f.Home)
	assert.NotEqual(t, f.Recorded, f.Observed)
}

func TestParseMountInfo(t *testing.T) {
	data := []byte(`22 1 0:21 / / rw,relatime - overlay overlay rw
40 22 0:50 / /home/workspace rw,relatime - virtiofs none rw
41 40 0:50 /daemon.json /home/workspace/.reliant/daemon.json ro - virtiofs none rw
42 22 0:51 / /mnt/with\040space rw - tmpfs tmpfs rw
garbage
`)
	entries := parseMountInfo(data)
	require.Len(t, entries, 4)
	assert.Equal(t, "/home/workspace", entries[1].mountPoint)
	assert.Equal(t, "/mnt/with space", entries[3].mountPoint, "octal escapes are decoded")

	var covering []string
	for _, e := range entries {
		if within("/home/workspace/projects", e.mountPoint) {
			covering = append(covering, e.mountPoint)
		}
	}
	assert.Equal(t, []string{"/", "/home/workspace"}, covering)
}

func TestFaultError_NamesThePathAndBothIdentities(t *testing.T) {
	f := Fault{Path: home, Home: true, Kind: KindUnmounted, Recorded: pvcRoot, Observed: rootfsHome}
	msg := f.Error()
	assert.Contains(t, msg, home)
	assert.Contains(t, msg, pvcRoot.String())
	assert.Contains(t, msg, rootfsHome.String())

	var asFault Fault
	require.True(t, errors.As(fmt.Errorf("wrapped: %w", f), &asFault))
	assert.Equal(t, home, asFault.Path)
}

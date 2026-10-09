// Copyright (c) 2025 Reliant Labs

// Package rootwatch notices when a directory the daemon is serving stops being
// the directory it was.
//
// On 2026-10-09 a cloud workspace's /home/workspace volume was lazily
// unmounted inside the container while the daemon kept running. Nothing was
// deleted — the volume stayed attached on the node — but from then on every
// path under $HOME resolved to the image's baked home on the container
// rootfs. The daemon stayed connected and "healthy" for hours while every tool
// call failed with "working directory does not exist", terminals sat in a dead
// cwd, and whatever the agents wrote landed on a disk the next restart throws
// away. A container restart would have re-mounted the volume. Nothing asked
// for one.
//
// A path cannot say which filesystem it ought to be on, but its identity can:
// the (device, inode) pair stat reports. This package records that identity
// for $HOME at startup and for each project/worktree root the first time the
// daemon serves it, then re-checks them cheaply — on a poll, and before tool
// calls. What a fault MEANS (restart the container, or refuse work under that
// path) is the caller's policy. This package only detects.
//
// Telling a lost mount apart from ordinary churn:
//
//   - $HOME is held to the strictest standard. A changed identity, a mount
//     point that is no longer one, or the directory going missing are all
//     faults: nothing a user or agent does legitimately replaces their home
//     directory under a running daemon.
//   - A project or worktree root that disappears (ENOENT) was removed — a
//     worktree deleted, a repo re-cloned — and is simply forgotten. If it
//     comes back it is recorded afresh.
//   - A root recreated in place stays on its device and gets a new inode
//     (`git worktree add`, a re-clone between two polls). That is churn, and
//     is re-recorded silently.
//   - A root that is suddenly on a DIFFERENT device, with no disappearance in
//     between, now resolves to a different filesystem. That is a fault.
package rootwatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultInterval is how often Run re-checks every watched path. A check is
// one stat per path plus one read of the mount table, so this is cheap enough
// to run for the daemon's whole life.
const DefaultInterval = 5 * time.Second

// maxRoots bounds the root table. Removed roots are forgotten as soon as a
// check sees them gone, so a real daemon stays far below this; the cap only
// exists so nothing about a pathological caller can grow it without limit.
const maxRoots = 1024

// Identity is the (device, inode) pair that makes a directory THE directory.
// Two paths with equal identities are the same directory; one path whose
// identity changed now names a different one.
type Identity struct {
	Dev uint64
	Ino uint64
}

func (i Identity) String() string {
	return fmt.Sprintf("dev=%d ino=%d", i.Dev, i.Ino)
}

// Kind names how a watched path stopped being what it was.
type Kind string

const (
	// KindUnmounted: the path was a mount point when recorded and no longer
	// is. The 2026-10-09 shape: the volume was detached and the path fell
	// through to the directory underneath it.
	KindUnmounted Kind = "unmounted"
	// KindReplaced: the path resolves to a different directory than the one
	// recorded.
	KindReplaced Kind = "replaced"
	// KindMissing: $HOME no longer exists at all. Only $HOME has this kind;
	// a missing root is a removed root, not a fault.
	KindMissing Kind = "missing"
)

// Fault is a watched path that no longer resolves to the directory recorded
// for it.
type Fault struct {
	Path string
	// Home is true when Path is $HOME. A home fault covers every path: the
	// daemon's credentials, config and state all live under it.
	Home     bool
	Kind     Kind
	Recorded Identity
	// Observed is the identity found at detection; zero for KindMissing.
	Observed Identity
	// RecordedMounts are the mount-table lines that covered Path when it was
	// recorded, and Mounts the ones covering it at detection. Comparing them
	// is usually the whole diagnosis. Both are empty where the platform has
	// no mount table to read (macOS, Windows).
	RecordedMounts []string
	Mounts         []string
	At             time.Time
}

func (f Fault) Error() string {
	switch f.Kind {
	case KindMissing:
		return fmt.Sprintf("%s no longer exists (was %s)", f.Path, f.Recorded)
	case KindUnmounted:
		return fmt.Sprintf("%s is no longer a mount point (was %s, now %s)", f.Path, f.Recorded, f.Observed)
	default:
		return fmt.Sprintf("%s now resolves to a different directory (was %s, now %s)", f.Path, f.Recorded, f.Observed)
	}
}

// Covers reports whether this fault makes path untrustworthy. The empty path
// stands for "no particular path" and is covered only by a home fault.
func (f Fault) Covers(path string) bool {
	if f.Home {
		return true
	}
	return path != "" && within(path, f.Path)
}

// Probe is how the watcher looks at the filesystem. SystemProbe is the real
// one; tests substitute their own.
type Probe struct {
	// Stat returns path's identity, following symlinks. A path that does not
	// exist must yield an error matching fs.ErrNotExist.
	Stat func(path string) (Identity, error)
	// MountPoint reports whether path is a mount point. known is false when
	// the platform cannot tell, which turns the mount-point check off rather
	// than tripping it.
	MountPoint func(path string) (isMount, known bool)
	// Mounts returns the mount-table lines covering path, for the report.
	Mounts func(path string) []string
}

// Options configures a Watcher. Every field is optional.
type Options struct {
	// Interval between Run's checks. Zero means DefaultInterval.
	Interval time.Duration
	// Probe replaces SystemProbe. Nil funcs inside it fall back to the
	// system's.
	Probe Probe
	// OnFault is called once per new fault, outside the watcher's lock.
	OnFault func(Fault)
	// OnRestored is called when a faulted path resolves to its recorded
	// directory again (a mount restored by hand on a desktop).
	OnRestored func(Fault)
	// Now replaces time.Now.
	Now func() time.Time
}

// Record is what the watcher remembers about $HOME, exposed for the startup
// log line.
type Record struct {
	Path       string
	Identity   Identity
	MountPoint bool
	Mounts     []string
}

type entry struct {
	path       string
	id         Identity
	mountPoint bool
	mounts     []string
}

// Watcher holds the recorded identities and the faults found against them.
// All methods are safe for concurrent use, and safe on a nil *Watcher, which
// watches nothing.
type Watcher struct {
	probe      Probe
	interval   time.Duration
	onFault    func(Fault)
	onRestored func(Fault)
	now        func() time.Time

	mu     sync.Mutex
	home   *entry // nil when $HOME could not be recorded
	roots  map[string]*entry
	faults map[string]Fault
}

// New records home's identity and returns a watcher for it. err explains why
// home is NOT being watched (unset, unreadable, or a platform with no file
// identity); the watcher is still returned and still watches roots, so a
// caller logs err and carries on.
func New(home string, opts Options) (*Watcher, error) {
	w := &Watcher{
		probe:      withSystemDefaults(opts.Probe),
		interval:   opts.Interval,
		onFault:    opts.OnFault,
		onRestored: opts.OnRestored,
		now:        opts.Now,
		roots:      make(map[string]*entry),
		faults:     make(map[string]Fault),
	}
	if w.interval <= 0 {
		w.interval = DefaultInterval
	}
	if w.now == nil {
		w.now = time.Now
	}

	home = clean(home)
	if home == "" {
		return w, errors.New("no home directory to watch")
	}
	id, err := w.probe.Stat(home)
	if err != nil {
		return w, fmt.Errorf("recording %s: %w", home, err)
	}
	mountPoint, _ := w.probe.MountPoint(home)
	w.home = &entry{path: home, id: id, mountPoint: mountPoint, mounts: w.probe.Mounts(home)}
	return w, nil
}

// Home returns what was recorded for $HOME, and false when it is not watched.
func (w *Watcher) Home() (Record, bool) {
	if w == nil {
		return Record{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.home == nil {
		return Record{}, false
	}
	return Record{Path: w.home.path, Identity: w.home.id, MountPoint: w.home.mountPoint, Mounts: w.home.mounts}, true
}

// Observe records root the first time it is seen. A root that does not exist
// yet has nothing to record and is left for a later call.
func (w *Watcher) Observe(root string) {
	if w == nil {
		return
	}
	root = clean(root)
	if root == "" || !w.shouldRecord(root) {
		return
	}
	id, err := w.probe.Stat(root)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, known := w.roots[root]; !known && len(w.roots) < maxRoots {
		w.roots[root] = &entry{path: root, id: id}
	}
}

// Check re-verifies $HOME and root against the filesystem now — recording
// root if it is new — and returns the fault covering root, if any. An empty
// root checks $HOME alone. It is the pre-flight for a tool call: one stat per
// path, no mount-table read.
func (w *Watcher) Check(root string) (Fault, bool) {
	if w == nil {
		return Fault{}, false
	}
	root = clean(root)
	w.checkHome(false)
	if root != "" {
		if w.known(root) {
			w.checkRoot(root)
		} else {
			w.Observe(root)
		}
	}
	return w.Fault(root)
}

// Fault returns the fault covering path, from what the checks have already
// found; it touches no filesystem. The empty path is covered only by a home
// fault.
func (w *Watcher) Fault(path string) (Fault, bool) {
	if w == nil {
		return Fault{}, false
	}
	path = clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.home != nil {
		if f, ok := w.faults[w.home.path]; ok {
			return f, true
		}
	}
	for _, f := range w.faults {
		if f.Covers(path) {
			return f, true
		}
	}
	return Fault{}, false
}

// CheckAll re-verifies every watched path, including whether $HOME is still
// a mount point.
func (w *Watcher) CheckAll() {
	if w == nil {
		return
	}
	w.checkHome(true)
	w.mu.Lock()
	roots := make([]string, 0, len(w.roots))
	for path := range w.roots {
		roots = append(roots, path)
	}
	w.mu.Unlock()
	for _, path := range roots {
		w.checkRoot(path)
	}
}

// Run calls CheckAll every interval until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.CheckAll()
		}
	}
}

// checkHome compares $HOME with its record. deep adds the mount-table read
// that answers "is it still a mount point"; the identity comparison alone
// already catches a detach, so per-call checks skip it.
func (w *Watcher) checkHome(deep bool) {
	w.mu.Lock()
	rec := w.home
	w.mu.Unlock()
	if rec == nil {
		return
	}

	observed, err := w.probe.Stat(rec.path)
	var kind Kind
	switch {
	case errors.Is(err, fs.ErrNotExist):
		kind = KindMissing
	case err != nil:
		// EACCES, EIO, a hung network mount timing out: none of them says
		// the directory changed, so neither trip nor clear.
		return
	case deep && rec.mountPoint && !w.stillMountPoint(rec.path):
		kind = KindUnmounted
	case observed != rec.id:
		kind = KindReplaced
		// Identity alone cannot tell a detach from a replacement; the
		// mount table can, and this branch runs once per fault.
		if rec.mountPoint && !w.stillMountPoint(rec.path) {
			kind = KindUnmounted
		}
	default:
		w.restore(rec.path)
		return
	}
	w.trip(Fault{Path: rec.path, Home: true, Kind: kind, Recorded: rec.id, Observed: observed, RecordedMounts: rec.mounts})
}

func (w *Watcher) checkRoot(path string) {
	w.mu.Lock()
	rec := w.roots[path]
	w.mu.Unlock()
	if rec == nil {
		return
	}

	observed, err := w.probe.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Removed. Whatever appears here next is a new root.
		w.forget(path)
	case err != nil:
		return
	case observed == rec.id:
		w.restore(path)
	case observed.Dev == rec.id.Dev:
		// Recreated in place on the same filesystem: churn, not a lost
		// mount. Adopt the new directory.
		w.mu.Lock()
		if cur := w.roots[path]; cur != nil {
			cur.id = observed
		}
		w.mu.Unlock()
		w.restore(path)
	default:
		w.trip(Fault{Path: path, Kind: KindReplaced, Recorded: rec.id, Observed: observed})
	}
}

func (w *Watcher) stillMountPoint(path string) bool {
	isMount, known := w.probe.MountPoint(path)
	return isMount || !known
}

// trip records f unless its path is already faulted, and reports it once.
func (w *Watcher) trip(f Fault) {
	w.mu.Lock()
	_, already := w.faults[f.Path]
	w.mu.Unlock()
	if already {
		return
	}

	f.At = w.now()
	f.Mounts = w.probe.Mounts(f.Path)

	w.mu.Lock()
	if _, already = w.faults[f.Path]; !already {
		w.faults[f.Path] = f
	}
	w.mu.Unlock()
	if !already && w.onFault != nil {
		w.onFault(f)
	}
}

func (w *Watcher) restore(path string) {
	w.mu.Lock()
	f, faulted := w.faults[path]
	delete(w.faults, path)
	w.mu.Unlock()
	if faulted && w.onRestored != nil {
		w.onRestored(f)
	}
}

func (w *Watcher) forget(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.roots, path)
	delete(w.faults, path)
}

func (w *Watcher) known(root string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.roots[root]
	return ok
}

// shouldRecord reports whether root is worth a stat. $HOME has its own,
// stricter record, and nothing is recorded while $HOME is faulted: a root
// first seen then would be recorded on the wrong filesystem, and would read
// as a fault of its own once $HOME came back.
func (w *Watcher) shouldRecord(root string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.home != nil {
		if root == w.home.path {
			return false
		}
		if _, homeFaulted := w.faults[w.home.path]; homeFaulted {
			return false
		}
	}
	_, known := w.roots[root]
	return !known && len(w.roots) < maxRoots
}

func clean(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// within reports whether path is dir or lies beneath it.
func within(path, dir string) bool {
	if path == dir {
		return true
	}
	if dir == string(filepath.Separator) {
		return strings.HasPrefix(path, dir)
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

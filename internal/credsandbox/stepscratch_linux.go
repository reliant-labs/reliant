// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/logging"
)

// The Linux half of each credentialed command's own scratch (stepscratch.go):
// making it, proving from /proc that nothing holds it, and the sweep.

// Each directory's ownership lock, and the names around it.
const (
	// stepScratchLockName is the lock file inside a command's scratch.
	stepScratchLockName = ".lock"
	// stepScratchNewPrefix names a directory still being made: it gets its
	// lock before it is renamed to its step- name, so no step- directory is
	// ever unlocked while its daemon lives.
	stepScratchNewPrefix = ".new-"
	// stepScratchTrashPrefix names a directory the sweep judged removable and
	// moved out of every step- name before removing it.
	stepScratchTrashPrefix = ".trash-"
)

// makeStepScratch makes a command's own scratch directory in root, mode 0700,
// owned (newStepScratch) and registered (stepRegistry) before its name is
// returned, and starts the sweep when it is due.
func makeStepScratch(root string) (string, error) {
	if root == "" {
		return "", errors.New("no home directory")
	}
	dir, err := registerStepScratch(func() (string, *os.File, error) { return newStepScratch(root) })
	if err != nil {
		return "", err
	}
	maybeSweepStepScratch(root)
	return dir, nil
}

// newStepScratch makes root/step-<pid>-<random> and returns it with its
// ownership lock held: it makes .new-step-<pid>-<random> (MkdirTemp: a fresh
// name, mode 0700), creates .lock in it and takes flock(LOCK_EX) on it, and only
// then renames it to its step- name (RENAME_NOREPLACE). So a step- directory a
// live daemon made is locked from the moment the name exists. The descriptor is
// close-on-exec (os.OpenFile always sets O_CLOEXEC): no command inherits the
// lock, which therefore lives exactly as long as this daemon holds it.
func newStepScratch(root string) (string, *os.File, error) {
	tmp, err := os.MkdirTemp(root, fmt.Sprintf("%s%s%d-", stepScratchNewPrefix, stepScratchPrefix, os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	fail := func(lock *os.File, err error) (string, *os.File, error) {
		_ = removeTree(tmp)
		if lock != nil {
			_ = lock.Close()
		}
		return "", nil, err
	}
	lock, err := os.OpenFile(filepath.Join(tmp, stepScratchLockName), os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail(nil, fmt.Errorf("making the scratch directory's lock: %w", err))
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(lock, fmt.Errorf("locking the scratch directory: %w", err))
	}
	dir := filepath.Join(root, strings.TrimPrefix(filepath.Base(tmp), stepScratchNewPrefix))
	if err := unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, dir, unix.RENAME_NOREPLACE); err != nil {
		return fail(lock, fmt.Errorf("naming the scratch directory: %w", err))
	}
	return dir, lock, nil
}

// lockState is what tryLockStepScratch found.
type lockState int

const (
	lockTaken    lockState = iota // the sweep holds the lock now
	lockOwned                     // another descriptor holds it: a live owner
	lockAbsent                    // no lock file: legacy, or removed by its step
	lockUnusable                  // a lock file that cannot be judged: kept
)

// tryLockStepScratch tries to take dir's ownership lock without waiting. With
// lockTaken the caller holds it through the returned file until it closes it.
func tryLockStepScratch(dir string) (*os.File, lockState, error) {
	f, err := os.OpenFile(filepath.Join(dir, stepScratchLockName), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, lockAbsent, nil
	}
	if err != nil {
		return nil, lockUnusable, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, lockUnusable, fmt.Errorf("%s is not a regular file", stepScratchLockName)
	}
	switch err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); {
	case err == nil:
		return f, lockTaken, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		_ = f.Close()
		return nil, lockOwned, nil
	default:
		_ = f.Close()
		return nil, lockUnusable, err
	}
}

var stepSweep struct {
	sync.Mutex
	last    time.Time
	running bool
}

// maybeSweepStepScratch starts a sweep of root in the background when none ran
// in the last stepScratchSweepEvery (the first credentialed command after the
// daemon starts always does) and none is running.
func maybeSweepStepScratch(root string) {
	stepSweep.Lock()
	defer stepSweep.Unlock()
	now := stepScratchNow()
	if stepSweep.running || (!stepSweep.last.IsZero() && now.Sub(stepSweep.last) < stepScratchSweepEvery) {
		return
	}
	stepSweep.last, stepSweep.running = now, true
	go func() {
		defer func() {
			stepSweep.Lock()
			stepSweep.running = false
			stepSweep.Unlock()
		}()
		if _, complete := sweepStepScratch(root); !complete {
			// Walk again soon rather than a whole interval later.
			stepSweep.Lock()
			stepSweep.last = stepSweep.last.Add(stepScratchRetryEvery - stepScratchSweepEvery)
			stepSweep.Unlock()
		}
	}()
}

// SweepStepScratch removes, in the background, the scratch directories of
// credentialed commands (StepScratchEnv) that an earlier daemon left and that no
// live process holds. The daemon calls it when it starts; it does nothing where
// the sandbox is not in force, since no command gets one there.
func SweepStepScratch() {
	self, err := os.Executable()
	if err != nil || !Enabled() || Available(self) != nil || ensureCredScratch() != nil {
		return
	}
	maybeSweepStepScratch(CredScratchDir())
}

// stepSweepListed runs between the sweep's listing of the root and its first
// candidate, and stepSweepLocked once it holds a candidate's lock, before it
// re-checks /proc; tests use them to act inside those windows.
var (
	stepSweepListed = func() {}
	stepSweepLocked = func(string) {}
)

// sweepStepScratch removes the command scratch directories in root that no
// live daemon owns and no live process holds, and reports how many.
//
// Listed, it spares every directory this daemon handed out and has not removed
// (the registry, read after the listing, so one made during the sweep is never
// a candidate) and every one younger than stepScratchMinAge. Then, one
// candidate at a time, it tries the candidate's ownership lock: held by another
// descriptor (a live daemon, this one or another), the candidate is skipped.
// Taken, or absent (no lock file), it walks /proc afresh, while it holds the
// lock, and removes the candidate only when that walk is complete and shows no
// process holding it: renamed to a .trash- name (so no step- name points at a
// half-removed tree) and then removed. An incomplete walk stops the sweep:
// nothing more is removed, what it kept is logged, complete is false, and the
// next sweep comes after stepScratchRetryEvery instead of stepScratchSweepEvery.
//
// It also finishes what a crash left: .trash- directories (already judged), and
// .new- directories older than stepScratchMinAge whose lock is free (a daemon
// killed while making one; never handed out).
func sweepStepScratch(root string) (removed int, complete bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, true
	}
	now := stepScratchNow()
	var candidates []string
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(root, name)
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() || !ownedByUs(fi) {
			continue
		}
		switch {
		case strings.HasPrefix(name, stepScratchTrashPrefix+stepScratchPrefix):
			if removeTree(path) == nil {
				removed++
			}
		case strings.HasPrefix(name, stepScratchNewPrefix+stepScratchPrefix):
			if now.Sub(fi.ModTime()) < stepScratchMinAge {
				continue
			}
			lock, state, _ := tryLockStepScratch(path)
			if state == lockTaken || state == lockAbsent {
				if removeTree(path) == nil {
					removed++
				}
			}
			if lock != nil {
				_ = lock.Close()
			}
		case strings.HasPrefix(name, stepScratchPrefix):
			if now.Sub(fi.ModTime()) >= stepScratchMinAge && !stepScratchRegistered(path) {
				candidates = append(candidates, path)
			}
		}
	}
	stepSweepListed()
	for i, path := range candidates {
		ok, err := sweepOne(root, path)
		if err != nil {
			logging.Info("credsandbox: not sweeping credentialed commands' scratch directories: the live processes could not all be read, so none is proven unheld",
				"root", root, "kept", len(candidates)-i, "first_kept", path, "retry_in", stepScratchRetryEvery, "error", err)
			return removed, false
		}
		if ok {
			removed++
		}
	}
	if removed > 0 {
		logging.Info("credsandbox: removed credentialed commands' scratch directories no live daemon owns and no live process holds", "root", root, "removed", removed)
	}
	return removed, true
}

// sweepOne judges one candidate under its ownership lock and removes it when
// nothing owns or holds it. err is an incomplete walk of /proc, which stops the
// sweep.
func sweepOne(root, path string) (removed bool, err error) {
	lock, state, lerr := tryLockStepScratch(path)
	if lock != nil {
		defer func() { _ = lock.Close() }()
	}
	switch state {
	case lockOwned:
		return false, nil
	case lockUnusable:
		logging.Debug("credsandbox: keeping a command scratch directory whose lock cannot be judged", "dir", path, "error", lerr)
		return false, nil
	}
	stepSweepLocked(path)
	refs, err := stepScratchRefs(stepScratchProcRoot)
	if err != nil {
		return false, err
	}
	if refs[filepath.Base(path)] {
		return false, nil
	}
	trash, err := os.MkdirTemp(root, stepScratchTrashPrefix+filepath.Base(path)+"-")
	if err != nil {
		logging.Warn("credsandbox: cannot move an orphaned command scratch directory aside", "dir", path, "error", err)
		return false, nil
	}
	_ = os.Remove(trash)
	if err := unix.Renameat2(unix.AT_FDCWD, path, unix.AT_FDCWD, trash, unix.RENAME_NOREPLACE); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logging.Warn("credsandbox: cannot move an orphaned command scratch directory aside", "dir", path, "error", err)
		}
		return false, nil
	}
	if err := removeTree(trash); err != nil {
		logging.Warn("credsandbox: cannot remove an orphaned command scratch directory; the next sweep finishes it", "dir", trash, "error", err)
	}
	return true, nil
}

// credScratchMarker is how CredScratchDir appears inside a path: the scratch
// directory named after it is the one a path holds.
var credScratchMarker = "/" + filepath.Base(CredScratchRel) + "/"

// procFS is how the scan reads /proc; tests replace it to make a read fail
// the way the kernel's do (ESRCH from a process mid-exit, or one in another pid
// namespace).
var procFS = struct {
	readDir  func(string) ([]os.DirEntry, error)
	lstat    func(string) (os.FileInfo, error)
	readFile func(string) ([]byte, error)
	readlink func(string) (string, error)
}{os.ReadDir, os.Lstat, os.ReadFile, os.Readlink}

// stepScratchRefs lists the command scratch directories (by name) that a live
// process of this user holds: in a value of its environment (StepScratchEnv,
// which every process of a command inherits, or a TMPDIR under it), as its
// working directory, or through an open file. Matching by name rather than
// path counts a process that reaches the directory through another mount of
// the disk.
//
// It is a proof only when every read of every listed process succeeded. Any
// read error fails it, whatever the error: ENOENT and ESRCH included. Neither
// proves that the process no longer holds a scratch. A process that is exiting
// reports them, but by then its pid may be another process's, which the
// listing named and the read never saw, and ESRCH from a process in another pid
// namespace says nothing about what it holds. So a walk that met one proves
// nothing: the caller removes nothing and walks again later. A process in a
// container whose user shares our uid (k3d's coredns runs as 65532, a cloud
// workspace's user) whose files are withheld from us fails it the same way.
// Only another user's process, and a non-dumpable one of ours (the kernel
// hands its /proc files to root: a sandbox's init, which holds no
// credentialed scratch), is skipped: by the owner of its environ file, which
// the same Lstat that must succeed reports.
func stepScratchRefs(procRoot string) (map[string]bool, error) {
	entries, err := procFS.readDir(procRoot)
	if err != nil {
		return nil, err
	}
	uid := os.Geteuid()
	refs := map[string]bool{}
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		fi, err := procFS.lstat(filepath.Join(dir, "environ"))
		if err != nil {
			return nil, fmt.Errorf("pid %s: %w", e.Name(), err)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		if err := readStepScratchRefs(dir, refs); err != nil {
			return nil, fmt.Errorf("pid %s %w", e.Name(), err)
		}
	}
	return refs, nil
}

// readStepScratchRefs adds the command scratch directories the process at dir
// holds to refs. Every read must succeed (stepScratchRefs).
func readStepScratchRefs(dir string, refs map[string]bool) error {
	add := func(v string) {
		for {
			i := strings.Index(v, credScratchMarker)
			if i < 0 {
				return
			}
			v = v[i+len(credScratchMarker):]
			if name, _, _ := strings.Cut(v, "/"); strings.HasPrefix(name, stepScratchPrefix) {
				refs[name] = true
			}
		}
	}
	env, err := procFS.readFile(filepath.Join(dir, "environ"))
	if err != nil {
		return fmt.Errorf("environment: %w", err)
	}
	for _, kv := range strings.Split(string(env), "\x00") {
		add(kv)
	}
	cwd, err := procFS.readlink(filepath.Join(dir, "cwd"))
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	add(cwd)
	fds, err := procFS.readDir(filepath.Join(dir, "fd"))
	if err != nil {
		return fmt.Errorf("descriptors: %w", err)
	}
	for _, fd := range fds {
		target, err := procFS.readlink(filepath.Join(dir, "fd", fd.Name()))
		if err != nil {
			return fmt.Errorf("descriptor %s: %w", fd.Name(), err)
		}
		add(target)
	}
	return nil
}

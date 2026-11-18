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

	"github.com/reliant-labs/reliant/internal/logging"
)

// The Linux half of each credentialed command's own scratch (stepscratch.go):
// making it, proving from /proc that nothing holds it, and the sweep.

// makeStepScratch makes a command's own scratch directory in root, mode 0700,
// registered (stepRegistry) before its name is returned, and starts the sweep
// when it is due. MkdirTemp's random suffix keeps a restarted daemon that got
// the same pid from reusing a name.
func makeStepScratch(root string) (string, error) {
	if root == "" {
		return "", errors.New("no home directory")
	}
	dir, err := registerStepScratch(func() (string, error) {
		return os.MkdirTemp(root, fmt.Sprintf("%s%d-", stepScratchPrefix, os.Getpid()))
	})
	if err != nil {
		return "", err
	}
	maybeSweepStepScratch(root)
	return dir, nil
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

// stepSweepListed runs between the sweep's listing of the root and its scan of
// /proc; tests use it to make a directory inside that window.
var stepSweepListed = func() {}

// sweepStepScratch removes the command scratch directories in root that an
// earlier daemon left and no live process holds, and reports how many. It
// spares every directory this daemon handed out and has not removed (the
// registry, read after the listing, so one made during the sweep is never a
// candidate), and every one younger than stepScratchMinAge (another daemon's
// command may not have started yet). When /proc cannot be read completely,
// nothing is removed and the directories it would have judged are logged;
// complete is false then, and the next sweep comes after
// stepScratchRetryEvery instead of stepScratchSweepEvery.
func sweepStepScratch(root string) (removed int, complete bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, true
	}
	now := stepScratchNow()
	var candidates []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), stepScratchPrefix) {
			continue
		}
		path := filepath.Join(root, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() || !ownedByUs(fi) || now.Sub(fi.ModTime()) < stepScratchMinAge || stepScratchRegistered(path) {
			continue
		}
		candidates = append(candidates, path)
	}
	if len(candidates) == 0 {
		return 0, true
	}
	stepSweepListed()
	refs, err := stepScratchRefs(stepScratchProcRoot)
	if err != nil {
		logging.Info("credsandbox: not sweeping credentialed commands' scratch directories: the live processes could not all be read, so none is proven unheld",
			"root", root, "kept", len(candidates), "first_kept", candidates[0], "retry_in", stepScratchRetryEvery, "error", err)
		return 0, false
	}
	for _, path := range candidates {
		if refs[filepath.Base(path)] {
			continue
		}
		if err := removeTree(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logging.Warn("credsandbox: cannot remove an orphaned command scratch directory", "dir", path, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		logging.Info("credsandbox: removed credentialed commands' scratch directories no live process holds", "root", root, "removed", removed)
	}
	return removed, true
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

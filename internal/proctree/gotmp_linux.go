// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/credsandbox"
)

// Each command's own GOTMPDIR.
//
// The go command builds in a work directory under GOTMPDIR (go-build*, ~300
// MB for a linked binary) and removes it when it exits, but not when it is
// SIGKILLed, and it dies of SIGTERM without removing it too. In a shared
// /tmp nothing can tell such a directory from a live build's, so it stayed:
// on 2026-11-03 one machine held ~1,500 of them, 222 GB, and its disk filled.
//
// So every command the daemon starts gets a GOTMPDIR of its own,
// <temp dir>/reliant-gotmp-<uid>/gotmp-<daemon pid>-<n>, and the daemon removes
// it only after the command's cgroup is retired: ownership is
// a fact, not a guess from an mtime: at once when the cgroup can be removed as the
// command returns, else when a bounded watcher retires it (a killed
// command's processes still exiting; releaseGoTmp). A root whose cgroup
// outlives the watcher (the command left a server running) goes when this
// process's cgroup sweep retires that cgroup, on a later command's Prepare, or
// a later daemon's start-up sweep does (removePairedGoTmp): the root is named
// for its cgroup, gotmp-<pid>-c<n> beside cmd-<pid>-<n>, so retiring one names
// the other. One without a cgroup (gotmp-<pid>-<n>) stays; the daemon's
// go_work_dirs pass removes it once no live process holds it, which every one
// of its processes does in its environment (internal/diskwatch, gowork.go).
// That pass runs every 10 minutes whatever RELIANT_STORAGE_REAPER says, so a
// daemon without cgroup delegation does not leak one root per command.
//
// A credentials-none command (credsandbox) gets none: the sandbox points
// GOTMPDIR into its own private /tmp and hides the machine's, so the root was
// never used, and with no cgroup (it enters new namespaces) nothing removed it
// when the command ended: one empty root per sandboxed command.
//
// Only GOTMPDIR. TMPDIR is left alone: a file an agent makes with mktemp in
// one command is routinely read by its next one, and removing it with the
// command would break that. The linker's go-link-* (TMPDIR) are reclaimed by
// the reaper once proven dead.

// pairedGoTmpMark marks the name of a root whose command has a cgroup of the
// same number: gotmp-<pid>-c<n> is cmd-<pid>-<n>'s. Only such a root is ever
// removed by name from its cgroup's: a root without the mark (no cgroup, or
// made by a daemon from before the two shared a number) names no cgroup.
const pairedGoTmpMark = "c"

// prepareGoTmp gives cmd its own GOTMPDIR, unless its environment sets one or
// it is a credsandbox init (which never uses one), numbered seq like its
// cgroup. It never stops the command from starting: without the root it
// builds where it would have.
func (t *Tree) prepareGoTmp(cmd *exec.Cmd, seq uint64) {
	if credsandbox.IsInit(cmd) {
		return
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ() //nolint:forbidigo // a command with no environment of its own inherits this process's; written down to add GOTMPDIR
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GOTMPDIR=") {
			return
		}
	}
	base, ok := goTmpBase(true)
	if !ok {
		return
	}
	dir := filepath.Join(base, goTmpName(os.Getpid(), seq, t.dir != ""))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return
	}
	cmd.Env = append(cmd.Env, "GOTMPDIR="+dir)
	t.goTmp = dir
}

// goTmpName is the name of the root of this process's command seq: marked when
// the command has the cgroup cmd-<pid>-<seq>.
func goTmpName(pid int, seq uint64, paired bool) string {
	mark := ""
	if paired {
		mark = pairedGoTmpMark
	}
	return fmt.Sprintf("%s%d-%s%d", GoTmpPrefix, pid, mark, seq)
}

// goTmpBase is this user's root base, <temp dir>/reliant-gotmp-<uid>, made
// when create says so. ok only when it is ours and a real directory: /tmp is
// shared, and a directory another user made there is a way to read our builds
// or to point a removal elsewhere.
func goTmpBase(create bool) (string, bool) {
	base := filepath.Join(os.TempDir(), fmt.Sprintf("%s%d", GoTmpBasePrefix, os.Getuid()))
	if create {
		if err := os.Mkdir(base, 0o700); err != nil && !os.IsExist(err) {
			return "", false
		}
	}
	if fi, err := os.Lstat(base); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || !ownedByUs(fi) {
		return "", false
	}
	return base, true
}

// removePairedGoTmp removes, in the background, the root paired with the
// command cgroup named cgroup (cmd-<pid>-<n>), which the caller has just
// retired: none of that command's processes is left, as for releaseGoTmp. It
// is how a root outlives no cgroup this process sweeps: one whose watcher gave
// up or found no slot, and an exited daemon's, at start-up.
func removePairedGoTmp(cgroup string) {
	rest, ok := strings.CutPrefix(cgroup, cmdPrefix)
	if !ok {
		return
	}
	pidText, seqText, ok := strings.Cut(rest, "-")
	pid, perr := strconv.Atoi(pidText)
	seq, serr := strconv.ParseUint(seqText, 10, 64)
	if !ok || perr != nil || serr != nil {
		return
	}
	base, ok := goTmpBase(false)
	if !ok {
		return
	}
	root := filepath.Join(base, goTmpName(pid, seq, true))
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() || !ownedByUs(fi) {
		return
	}
	go func() { _ = os.RemoveAll(root) }()
}

// releaseGoTmp removes the command's GOTMPDIR once its cgroup proves none of
// its processes is left. In the background: a killed build's root holds
// hundreds of megabytes.
//
// Only a cgroup proves that. A descendant can leave the command's process
// group or session (setsid) and keep compiling in the GOTMPDIR it inherited,
// so an empty process group proves nothing; a command with no cgroup keeps
// its root, and the daemon's go_work_dirs pass removes it once no live
// process has it in its environment (internal/diskwatch, gowork.go). A cgroup
// cannot be left that way.
//
// Not even a root that is empty when its command ends goes here without a
// cgroup: an escaped descendant that has not started its build yet would
// find its GOTMPDIR gone and fail ("go: creating work dir: … no such file").
// An empty root costs an inode; the pass removes it on its /proc proof.
//
// A cgroup still populated when its command returns is usually a killed
// command whose processes are still exiting: SIGKILL is delivered, but a
// go build's dozens of compilers and linkers take a moment to be reaped.
// Its root is handed to a watcher (watchGoTmp) that removes it once the
// cgroup empties, so it does not wait for the go_work_dirs pass (reliant#3522:
// 2,800 roots, 20 GiB, on one host). watched closes when that watcher stops;
// nil when there is none.
func (t *Tree) releaseGoTmp() (watched <-chan struct{}) {
	dir := t.goTmp
	if dir == "" || t.dir == "" {
		return nil
	}
	if retireGoTmpCgroup(t.dir) {
		t.goTmp = ""
		go func() { _ = os.RemoveAll(dir) }()
		return nil
	}
	// Scheduling is not proof of retirement. Keep the reference even if
	// the watcher times out; the cgroup sweep that retires the cgroup later
	// frees the root (removePairedGoTmp), and the disk watcher remains the
	// fallback owner.
	done, _ := watchGoTmp(t.dir, dir)
	return done
}

var (
	// goTmpWatchFor bounds how long a watcher waits for a cgroup to empty:
	// a command that left a process running for longer (a daemon it
	// started) leaves its root to the cgroup sweep (removePairedGoTmp).
	goTmpWatchFor = 5 * time.Minute
	// goTmpWatchPoll is how often a watcher reads the cgroup.
	goTmpWatchPoll = time.Second
	// goTmpWatchers bounds the watchers at once; a root that finds none
	// free is left to the cgroup sweep too.
	goTmpWatchers = make(chan struct{}, 64)
)

// watchGoTmp removes root, in the background, once the cgroup at cgroup is
// proven empty, polling for at most goTmpWatchFor. ok is false when every
// watcher is busy: root is then left where it is. done closes when the
// watcher stops, whether it removed root or gave up. Nothing waits for it:
// a daemon that exits first leaves root to the next daemon's start-up sweep.
func watchGoTmp(cgroup, root string) (done <-chan struct{}, ok bool) {
	pool, poll, wait := goTmpWatchers, goTmpWatchPoll, goTmpWatchFor
	select {
	case pool <- struct{}{}:
	default:
		return nil, false
	}
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		defer func() { <-pool }()
		tick := time.NewTicker(poll)
		defer tick.Stop()
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		for {
			if retireGoTmpCgroup(cgroup) {
				_ = os.RemoveAll(root)
				return
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				return
			}
		}
	}()
	return ch, true
}

// retireGoTmpCgroup requires retirement, not a transient populated=0.
// The kernel's rmdir refuses a populated cgroup atomically with process
// attachment. Once removed, even an open cgroup FD cannot start a process
// in it. Command cgroup names are unique and are never reused. If removal
// loses a race to a new process, keep the root and try again later.
func retireGoTmpCgroup(dir string) bool {
	if !cgroupProvenEmpty(dir) {
		return false
	}
	if err := removeCgroup(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}

// cgroupProvenEmpty is only a hint to attempt retirement, never permission
// to delete GOTMPDIR. An unreadable cgroup proves nothing.
func cgroupProvenEmpty(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return true
		}
		return false
	}
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "populated "); ok {
			return v == "0"
		}
	}
	return false
}

// ownedByUs reports whether fi belongs to this process's user.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/logging"
)

// cgroupFS is where cgroup v2 is mounted.
const cgroupFS = "/sys/fs/cgroup"

// cmdPrefix names the cgroups this package creates: cmd-<daemon pid>-<seq>.
const cmdPrefix = "cmd-"

// minPidsMax keeps the derived per-command limit usable on a small machine.
const minPidsMax = 256

// New builds a limiter for this process: the user's process limit, and the
// delegated cgroup subtree when there is one.
func New(cfg Config) *Limiter {
	l := &Limiter{pidsMax: cfg.PidsMax}
	var rl unix.Rlimit
	finite := unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur != unix.RLIM_INFINITY && os.Geteuid() != 0
	if finite {
		l.nproc = uint64(float64(rl.Cur) * ReserveFraction)
	}
	if l.pidsMax <= 0 {
		l.pidsMax = DefaultPidsMax
		if finite {
			if third := int(rl.Cur / 3); third < l.pidsMax {
				l.pidsMax = max(third, minPidsMax)
			}
		}
	}
	switch cfg.CgroupRoot {
	case "-":
		l.why = "per-command cgroups are switched off"
	case "":
		l.root, l.why = delegatedRoot()
	default:
		l.root = cfg.CgroupRoot
	}
	if l.root != "" {
		l.sweep(true)
	}
	return l
}

// delegatedRoot is the cgroup directory command cgroups go in: the parent of
// this process's own cgroup, when it is delegated to this user (writable, with
// the pids controller enabled for its children). The parent and not this
// process's own cgroup, because cgroup v2's no-internal-process rule forbids
// enabling controllers for the children of a cgroup that has processes — the
// daemon's. The cloud entrypoint sets this up: the daemon in /daemon/self,
// /daemon delegated (docker/workspace-docker-entrypoint.sh in control-plane).
func delegatedRoot() (dir, why string) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", "no /proc/self/cgroup"
	}
	own := ""
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			own = p
		}
	}
	if own == "" {
		return "", "not on cgroup v2"
	}
	parent := filepath.Dir(own)
	if own == "/" || parent == "/" {
		return "", fmt.Sprintf("the daemon's cgroup %s has no delegated parent", own)
	}
	dir = filepath.Join(cgroupFS, parent)
	ctl, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return "", fmt.Sprintf("cgroup %s is not readable: %v", dir, err)
	}
	if !strings.Contains(" "+strings.TrimSpace(string(ctl))+" ", " pids ") {
		return "", fmt.Sprintf("cgroup %s does not enable the pids controller for its children", dir)
	}
	for _, p := range []string{dir, filepath.Join(dir, "cgroup.procs")} {
		if err := unix.Access(p, unix.W_OK); err != nil {
			return "", fmt.Sprintf("cgroup %s is not delegated to this user (%s: %v)", dir, filepath.Base(p), err)
		}
	}
	return dir, ""
}

func (t *Tree) prepareCgroup(cmd *exec.Cmd, seq uint64) {
	l := t.l
	l.sweep(false)
	// A command entering new namespaces (credsandbox) is left in the
	// daemon's cgroup: clone3 into a cgroup together with CLONE_NEWUSER is
	// not a combination worth risking on a command that must start. It keeps
	// the RLIMIT_NPROC reserve.
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Cloneflags != 0 {
		return
	}
	dir := filepath.Join(l.root, fmt.Sprintf("%s%d-%d", cmdPrefix, os.Getpid(), seq))
	if err := os.Mkdir(dir, 0o755); err != nil {
		logging.Warn("proctree: cannot create a command cgroup; the command runs unbounded", "dir", dir, "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(strconv.Itoa(l.pidsMax)), 0); err != nil {
		logging.Warn("proctree: cannot set pids.max; the command runs unbounded", "dir", dir, "error", err)
		_ = os.Remove(dir)
		return
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logging.Warn("proctree: cannot open a command cgroup; the command runs unbounded", "dir", dir, "error", err)
		_ = os.Remove(dir)
		return
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = fd
	t.dir, t.fd = dir, fd
}

func (t *Tree) closeFD() {
	if t.fd >= 0 {
		_ = unix.Close(t.fd)
		t.fd = -1
	}
}

// limitNproc lowers the started command's RLIMIT_NPROC to the limiter's
// share. Applied right after Start, before the command has had a chance to
// fork anything worth the name (a shell is still starting up); everything the
// command forks afterwards inherits it.
func (t *Tree) limitNproc() {
	n := t.l.nproc
	if err := unix.Prlimit(t.pid, unix.RLIMIT_NPROC, &unix.Rlimit{Cur: n, Max: n}, nil); err != nil && !errors.Is(err, unix.ESRCH) {
		logging.Debug("proctree: cannot lower the command's RLIMIT_NPROC", "pid", t.pid, "error", err)
	}
}

// Kill SIGKILLs every process in the tree's cgroup, wherever it went: a member
// that left the process group with setsid is still in the cgroup. A tree with
// no cgroup reports false and kills nothing (the caller kills the group).
func (t *Tree) Kill() bool {
	if t == nil || t.dir == "" {
		return false
	}
	if err := os.WriteFile(filepath.Join(t.dir, "cgroup.kill"), []byte("1"), 0); err != nil {
		logging.Warn("proctree: cgroup.kill failed", "dir", t.dir, "error", err)
		return false
	}
	return true
}

// Populated reports whether any process of the tree's cgroup is alive. False
// for a tree with no cgroup.
func (t *Tree) Populated() bool {
	if t == nil || t.dir == "" {
		return false
	}
	return populated(t.dir)
}

func populated(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "populated "); ok {
			return v != "0"
		}
	}
	return false
}

// Release removes the tree's cgroup once it is empty. One the command left
// processes in stays until they exit; the next Prepare sweeps it.
func (t *Tree) Release() {
	if t == nil {
		return
	}
	t.closeFD()
	if t.dir == "" {
		return
	}
	if err := os.Remove(t.dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.EBUSY) {
		logging.Debug("proctree: cannot remove a command cgroup", "dir", t.dir, "error", err)
	}
}

// sweep removes this package's empty command cgroups. At start-up (all) it
// takes those of earlier daemons too; afterwards only this daemon's, whose
// commands may have left processes behind when they were released.
func (l *Limiter) sweep(all bool) {
	entries, err := os.ReadDir(l.root)
	if err != nil {
		return
	}
	own := fmt.Sprintf("%s%d-", cmdPrefix, os.Getpid())
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, cmdPrefix) || (!all && !strings.HasPrefix(name, own)) {
			continue
		}
		dir := filepath.Join(l.root, name)
		if !populated(dir) {
			_ = os.Remove(dir)
		}
	}
}

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
	"runtime"
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

// trackLive: the daemon's trees are listed for internal/procguard, which
// prunes the list on each /proc scan.
const trackLive = true

// commandsName names the cgroup, in the delegated directory, that holds every
// command cgroup and bounds them together.
const commandsName = "commands"

// New builds a limiter for this process: thread limits sized to this machine
// (Size), and the delegated cgroup subtree when there is one.
func New(cfg Config) *Limiter {
	l := &Limiter{}
	var rl unix.Rlimit
	nproc := 0
	// RLIMIT_NPROC does not apply to root.
	if unix.Getrlimit(unix.RLIMIT_NPROC, &rl) == nil && rl.Cur != unix.RLIM_INFINITY && os.Geteuid() != 0 {
		nproc = int(min(rl.Cur, 1<<31))
		l.nproc = uint64(float64(rl.Cur) * CommandsShare)
	}
	l.size = Size(runtime.NumCPU(), threadBudget(nproc, threadsMax()), cfg.CommandThreads)
	switch cfg.CgroupRoot {
	case "-":
		l.why = "per-command cgroups are switched off"
	case "":
		l.root, l.why = delegatedRoot()
	default:
		l.root = cfg.CgroupRoot
	}
	if l.root == "" {
		return l
	}
	// Leftovers of an earlier daemon, made before the commands cgroup
	// existed or in it.
	sweep(l.root, true)
	commands, err := makeCommandsCgroup(l.root, commandsName, l.size.Shared)
	if err != nil {
		l.commands = l.root
		l.why = fmt.Sprintf("no limit on all commands together: %v", err)
		return l
	}
	l.commands = commands
	sweep(l.commands, true)
	if err := enableCPU(l.root, l.commands); err != nil {
		logging.Warn("proctree: cannot enable the cpu controller for command cgroups; heavy commands are bounded by GOMAXPROCS alone", "error", err)
	}
	return l
}

// enableCPU enables the cpu controller for the command cgroups, so a heavy
// command's slot wrapper can cap its own cgroup at its CPU share (cpu.max,
// internal/heavyjob). Both levels hold no process of their own (the daemon is
// in root's self/), as cgroup v2 requires.
func enableCPU(root, commands string) error {
	for _, dir := range []string{root, commands} {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+cpu"), 0); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}

// threadsMax is kernel.threads-max, the machine's limit on threads of every
// user; 0 when unreadable.
func threadsMax() int {
	data, err := os.ReadFile("/proc/sys/kernel/threads-max")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

// makeCommandsCgroup makes (or reuses) the cgroup name under root that holds
// every command cgroup: the pids controller enabled for its children, and
// pids.max shared (no limit when 0). It holds no process of its own, which
// cgroup v2's no-internal-process rule requires of a cgroup whose children
// have controllers. Creating it needs nothing beyond what creating a command
// cgroup in root needs: it is the delegatee's own.
func makeCommandsCgroup(root, name string, shared int) (string, error) {
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+pids"), 0); err != nil {
		return "", fmt.Errorf("cannot enable the pids controller under %s: %w", dir, err)
	}
	limit := "max"
	if shared > 0 {
		limit = strconv.Itoa(shared)
	}
	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(limit), 0); err != nil {
		return "", fmt.Errorf("cannot set %s/pids.max: %w", dir, err)
	}
	return dir, nil
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
	sweep(l.commands, false)
	// A command entering new namespaces (credsandbox) is left in the
	// daemon's cgroup: clone3 into a cgroup together with CLONE_NEWUSER is
	// not a combination worth risking on a command that must start. It gets
	// the RLIMIT_NPROC reserve instead.
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Cloneflags != 0 {
		return
	}
	dir := filepath.Join(l.commands, fmt.Sprintf("%s%d-%d", cmdPrefix, os.Getpid(), seq))
	if err := os.Mkdir(dir, 0o755); err != nil {
		logging.Warn("proctree: cannot create a command cgroup; the command runs bounded only by RLIMIT_NPROC", "dir", dir, "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(strconv.Itoa(l.size.PerCommand)), 0); err != nil {
		logging.Warn("proctree: cannot set pids.max; the command runs bounded only by RLIMIT_NPROC", "dir", dir, "error", err)
		_ = os.Remove(dir)
		return
	}
	if l.commands != l.root {
		t.sharedRefused, _ = refusals(l.commands)
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
// processes in stays until they exit; the next Prepare sweeps it. What
// LimitNote reports is read first, while the cgroup is still there.
func (t *Tree) Release() {
	if t == nil {
		return
	}
	t.forget()
	t.closeFD()
	defer t.releaseGoTmp()
	if t.dir == "" {
		return
	}
	if !t.noted {
		t.note, t.noted = t.limitNote(), true
		if t.note != "" {
			logging.Warn("proctree: a command was refused threads at a thread limit",
				"chat_id", t.owner.ChatID, "command", t.owner.Command, "cgroup", t.dir,
				"command_limit", t.l.size.PerCommand, "shared_limit", t.l.size.Shared)
		}
	}
	if err := os.Remove(t.dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.EBUSY) {
		logging.Debug("proctree: cannot remove a command cgroup", "dir", t.dir, "error", err)
	}
}

// limitNote reads the refusals at the command's own limit and, made while it
// ran, at the limit all commands share, and words them (limitMessage).
func (t *Tree) limitNote() string {
	if t.dir == "" || t.l == nil {
		return ""
	}
	own, exact := refusals(t.dir)
	if !exact {
		// Before Linux 6.13 a cgroup counts every fork of its own that was
		// refused, at its limit or an ancestor's: which one is not known.
		return limitMessage(t.l.size, refused{either: own})
	}
	var shared uint64
	if t.l.commands != t.l.root {
		if now, _ := refusals(t.l.commands); now > t.sharedRefused {
			shared = now - t.sharedRefused
		}
	}
	return limitMessage(t.l.size, refused{own: own, shared: shared})
}

// refusals is how many forks (threads or processes) the kernel refused at
// dir's own pids.max: pids.events.local's max (Linux 6.13+; pids.events then
// counts the descendants' too). exact is false on an older kernel, which has
// only pids.events, counting instead every fork of dir's own processes that
// was refused at any limit. 0 when neither is readable.
func refusals(dir string) (n uint64, exact bool) {
	exact = true
	data, err := os.ReadFile(filepath.Join(dir, "pids.events.local"))
	if err != nil {
		exact = false
		if data, err = os.ReadFile(filepath.Join(dir, "pids.events")); err != nil {
			return 0, false
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "max "); ok {
			n, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, exact
		}
	}
	return 0, exact
}

// sweep removes this package's empty command cgroups in dir. At start-up
// (all) it takes those of earlier daemons too, though not those of a daemon
// still running (a daemon an agent started for development makes its command
// cgroups beside the ones of the daemon that runs the agent); afterwards only
// this daemon's, whose commands may have left processes behind when they were
// released.
func sweep(dir string, all bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, e := range entries {
		name := e.Name()
		rest, ok := strings.CutPrefix(name, cmdPrefix)
		if !e.IsDir() || !ok {
			continue
		}
		pidText, _, _ := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			continue
		}
		if pid != self && (!all || !errors.Is(unix.Kill(pid, 0), unix.ESRCH)) {
			continue
		}
		if sub := filepath.Join(dir, name); !populated(sub) {
			_ = os.Remove(sub)
		}
	}
}

// processIdentity reads pid's process group and start time (fields 5 and 22
// of /proc/<pid>/stat), which /proc shows for an undumpable process too.
func processIdentity(pid int) (pgid int, start uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	// The comm may hold spaces and parentheses: fields start after the last ')'.
	pos := bytes.LastIndexByte(data, ')')
	if pos < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(string(data[pos+1:]))
	if len(fields) < 20 {
		return 0, 0, false
	}
	pgid, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return pgid, start, true
}
